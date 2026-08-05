package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/meirongdev/readlist/internal/api"
	"github.com/meirongdev/readlist/internal/calibre"
	"github.com/meirongdev/readlist/internal/config"
	"github.com/meirongdev/readlist/internal/corpus"
	"github.com/meirongdev/readlist/internal/facts"
	"github.com/meirongdev/readlist/internal/preset"
	"github.com/meirongdev/readlist/internal/score"
	"github.com/meirongdev/readlist/internal/store"
)

const usage = "usage: readlist [snapshot|ingest|init|seed|score|dryrun|diff <runA> <runB>|serve]"

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("readlist failed", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := config.Load()

	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()

	presets, err := preset.Load()
	if err != nil {
		return fmt.Errorf("load presets: %w", err)
	}

	switch cmd {
	case "init":
		// initContainer 用(distroless 无 shell):建库 + 保证有一份可服务的榜单。
		//
		// 幂等 —— 只在尚未发布任何 run 时才打分。无条件重打分会让每次 Pod 重启都
		// 产出一个新 run_id:matrix 的 immutable 缓存全部失效,而 serve 本身早就有
		// 「无已发布 run 才自愈打分」的逻辑,这里再打一遍是纯粹的重复。
		if _, err := corpus.Seed(db); err != nil {
			return fmt.Errorf("init seed: %w", err)
		}
		runID, err := publishedRunID(db)
		if err != nil {
			return err
		}
		if runID != "" {
			fmt.Printf("init: run=%s 已发布,跳过打分\n", runID)
			return nil
		}
		res, err := newEngine(cfg, db).Run(presets)
		if err != nil {
			return fmt.Errorf("init score: %w", err)
		}
		printRunSummary(res, presets)

	case "snapshot":
		// 只有这个命令碰 calibre 的两个卷,且全程不发网络请求。
		// 部署上它是一个独立的短命 CronJob:公开 web 应用永不挂 calibre 卷。
		snap, err := calibre.Load(calibre.Config{
			MetadataDB:  cfg.SourceMetadataDB,
			AppDB:       cfg.SourceAppDB,
			SnapshotDir: cfg.SnapshotDir,
			UserID:      cfg.CalibreUserID,
		})
		if err != nil {
			return fmt.Errorf("snapshot: %w", err)
		}
		st, err := corpus.Import(db, snap, time.Now().UTC())
		if err != nil {
			return fmt.Errorf("snapshot import: %w", err)
		}
		printSnapshotSummary(st)

	case "ingest":
		// 唯一发起网络请求的命令。配额打满就干净停下,下次接着跑。
		st, err := facts.Ingest(db, facts.Config{
			GoogleBase:      cfg.GoogleBooksBase,
			OpenLibraryBase: cfg.OpenLibraryBase,
			HNBase:          cfg.HNSearchBase,
			GoogleKey:       cfg.GoogleBooksKey,
			Budget:          cfg.IngestBudget,
			Sleep:           500 * time.Millisecond, // OpenLibrary 建议的礼貌间隔
			Now:             time.Now().UTC(),
		})
		// 即使出错也把已拿到的统计打出来 —— 部分成功是这条管道的常态。
		printIngestSummary(st)
		if err != nil {
			return fmt.Errorf("ingest: %w", err)
		}

	case "seed":
		n, err := corpus.Seed(db)
		if err != nil {
			return fmt.Errorf("seed: %w", err)
		}
		fmt.Printf("seed: wrote %d editions (already present → 0)\n", n)

	case "score":
		res, err := newEngine(cfg, db).Run(presets)
		if err != nil {
			return fmt.Errorf("score: %w", err)
		}
		printRunSummary(res, presets)

	case "dryrun":
		c, err := newEngine(cfg, db).Compute(presets)
		if err != nil {
			return fmt.Errorf("dryrun: %w", err)
		}
		printDryRun(c, presets)

	case "diff":
		if len(os.Args) < 4 {
			return errors.New(usage)
		}
		return diffLists(db, os.Args[2], os.Args[3])

	case "serve":
		return serve(cfg, db, presets)

	default:
		return errors.New(usage)
	}
	return nil
}

func newEngine(cfg config.Config, db *store.DB) *score.Engine {
	eng := score.NewEngine(db, cfg.StandardVer, time.Now().UTC())
	eng.KeepRuns = cfg.KeepRuns
	return eng
}

func publishedRunID(db *store.DB) (string, error) {
	var runID string
	err := db.SQL().QueryRow(`SELECT run_id FROM published_run WHERE id=1`).Scan(&runID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read published_run: %w", err)
	}
	return runID, nil
}

func printSnapshotSummary(st corpus.ImportStats) {
	fmt.Printf("snapshot: run=%s works=%d editions=%d\n", st.RunID, st.Works, st.Editions)
	fmt.Printf("  出版社归一: %d 个原始名\n", st.Publishers)
	fmt.Printf("  阅读状态镜像: %d 行\n", st.ReadingRows)
	if st.DroppedBooks > 0 {
		fmt.Printf("  已从库中消失、本次删除: %d 个版次\n", st.DroppedBooks)
	}
	// 这两个数是数据质量的警报线,不该埋在日志里。
	fmt.Printf("  ⚠️ 孤儿行(book id 漂移): %d\n", st.OrphanRows)
	fmt.Printf("  ⚠️ pubdate 判为 mtime 兜底: %d;缺失/占位: %d —— 这些书的时效维度记 unknown\n",
		st.PubdateSuspect, st.PubdateUnknown)
}

func printIngestSummary(st facts.Stats) {
	fmt.Printf("ingest: 外部请求 %d 次(缓存命中 %d,限流 %d,失败 %d)\n",
		st.Requests, st.CacheHits, st.Throttled, st.Errors)
	fmt.Printf("  考察版次 %d;Google 命中 %d;OpenLibrary 命中 %d(拿到 work id %d)\n",
		st.EditionsSeen, st.GoogleFound, st.OpenLibraryFound, st.OLWorkIDs)
	fmt.Printf("  写入可信出版日期 %d 条 —— 时效维度靠它才有判别力\n", st.PubdatesWritten)
	fmt.Printf("  HN 提及 %d 条;标题过短跳过 %d 本;无标识符跳过 %d 个版次\n",
		st.MentionsFound, st.SkippedShortTitle, st.SkippedNoID)
	if st.BudgetExhausted {
		fmt.Println("  ⚠️ 本次预算已用完 —— 下次运行会从缓存未覆盖的地方接着跑")
	}
	if st.Throttled > 0 {
		fmt.Println("  ⚠️ 有源返回 429。Google Books 的匿名配额按共享项目计," +
			"建议配 GOOGLE_BOOKS_KEY")
	}
}

func printRunSummary(res *score.RunResult, presets []preset.Preset) {
	fmt.Printf("score: run=%s works=%d corpus=%s facts=%s\n",
		res.RunID, len(res.Works), res.CorpusID, res.FactsHash)
	grade := map[string]int{}
	for _, g := range res.Grade {
		grade[g]++
	}
	fmt.Print("  证据徽章(仅展示,不决定准入):")
	for _, g := range []string{"A", "B", "C", "D"} {
		fmt.Printf(" %s=%d", g, grade[g])
	}
	fmt.Println()
	for _, p := range presets {
		fmt.Printf("  list %-16s %2d items\n", p.ID, len(res.Lists[p.ID]))
	}
}

func printDryRun(c *score.Computation, presets []preset.Preset) {
	fmt.Println("dryrun: 只数不算(每维 measured 比例 + 每 preset 已选数)")
	fmt.Printf("  corpus=%s facts=%s works=%d\n", c.CorpusID, c.FactsHash, len(c.Inputs))
	total := len(c.Inputs)
	byDim := map[score.Dim]int{}
	for _, raws := range c.Raws {
		for dim, r := range raws {
			if r.State == score.StateMeasured {
				byDim[dim]++
			}
		}
	}
	for _, dim := range score.AllDims {
		pct := 0.0
		if total > 0 {
			pct = 100 * float64(byDim[dim]) / float64(total)
		}
		fmt.Printf("  dim %-11s measured %3d / %d (%.0f%%)\n", dim, byDim[dim], total, pct)
	}
	grade := map[string]int{}
	for _, g := range c.Grade {
		grade[g]++
	}
	fmt.Printf("  证据徽章: A=%d B=%d C=%d D=%d\n", grade["A"], grade["B"], grade["C"], grade["D"])
	for _, p := range presets {
		fmt.Printf("  preset %-16s selected %2d\n", p.ID, len(c.Lists[p.ID]))
	}
}

// diffLists 两个 run 的榜单差异(升版评审材料,scoring-standard §8 要求的排名对比)。
func diffLists(db *store.DB, a, b string) error {
	ra, err := loadRunLists(db, a)
	if err != nil {
		return err
	}
	rb, err := loadRunLists(db, b)
	if err != nil {
		return err
	}
	if len(ra) == 0 {
		return fmt.Errorf("run %s 没有榜单数据(可能已被 GC 回收)", a)
	}
	if len(rb) == 0 {
		return fmt.Errorf("run %s 没有榜单数据(可能已被 GC 回收)", b)
	}

	listIDs := map[string]bool{}
	for id := range ra {
		listIDs[id] = true
	}
	for id := range rb {
		listIDs[id] = true
	}
	ids := make([]string, 0, len(listIDs))
	for id := range listIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	fmt.Printf("diff %s → %s\n", a, b)
	unchanged := 0
	for _, listID := range ids {
		ma, mb := ra[listID], rb[listID]
		var left, entered, moved []string
		for wid, rank := range ma {
			switch newRank, ok := mb[wid]; {
			case !ok:
				left = append(left, fmt.Sprintf("#%d %s", rank, wid))
			case newRank != rank:
				moved = append(moved, fmt.Sprintf("%s %d→%d", wid, rank, newRank))
			}
		}
		for wid, rank := range mb {
			if _, ok := ma[wid]; !ok {
				entered = append(entered, fmt.Sprintf("#%d %s", rank, wid))
			}
		}
		if len(left) == 0 && len(entered) == 0 && len(moved) == 0 {
			unchanged++
			continue
		}
		sort.Strings(left)
		sort.Strings(entered)
		sort.Strings(moved)
		fmt.Printf("\n%s: 进 %d / 出 %d / 位次变化 %d\n", listID, len(entered), len(left), len(moved))
		for _, label := range []struct {
			name string
			vals []string
		}{{"出", left}, {"进", entered}, {"移", moved}} {
			if len(label.vals) > 0 {
				fmt.Printf("  %s: %s\n", label.name, strings.Join(label.vals, ", "))
			}
		}
	}
	if unchanged > 0 {
		fmt.Printf("\n%d 份榜单完全一致\n", unchanged)
	}
	return nil
}

func loadRunLists(db *store.DB, runID string) (map[string]map[string]int, error) {
	rows, err := db.SQL().Query(
		`SELECT list_id, rank, work_id FROM lists WHERE run_id=? ORDER BY list_id, rank`, runID)
	if err != nil {
		return nil, fmt.Errorf("load run %s: %w", runID, err)
	}
	defer rows.Close()
	out := map[string]map[string]int{}
	for rows.Next() {
		var listID, workID string
		var rank int
		if err := rows.Scan(&listID, &rank, &workID); err != nil {
			return nil, err
		}
		if out[listID] == nil {
			out[listID] = map[string]int{}
		}
		out[listID][workID] = rank
	}
	return out, rows.Err()
}

// serve 启动只读 API + 内嵌 SPA;若尚未发布 run,先自愈打分一次。
func serve(cfg config.Config, db *store.DB, presets []preset.Preset) error {
	runID, err := publishedRunID(db)
	if err != nil {
		return err
	}
	if runID == "" {
		slog.Info("no published run, scoring once before serve")
		if _, err := newEngine(cfg, db).Run(presets); err != nil {
			return fmt.Errorf("startup score: %w", err)
		}
	}

	// 四个超时都要给。公开端点面对的是慢连接与爬虫:少了 WriteTimeout,一个读得
	// 很慢的客户端就能长期占住 goroutine 与那条 SQLite 连接(matrix 响应有几十 KB);
	// 少了 IdleTimeout,keep-alive 连接会一直堆积。
	httpServer := &http.Server{
		Addr:              cfg.APIListenAddr,
		Handler:           api.NewServer(db, presets, cfg.ExposeReadStatus).Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		slog.Info("api listening", "addr", cfg.APIListenAddr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-errCh:
		// 端口占用之类的启动失败必须让进程失败退出,而不是挂在那里等信号 ——
		// 否则 readinessProbe 会一直红,但容器看起来"在跑"。
		return fmt.Errorf("http server: %w", err)
	case <-stop:
	}
	slog.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return httpServer.Shutdown(ctx)
}
