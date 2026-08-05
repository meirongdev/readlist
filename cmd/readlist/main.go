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
	"strings"
	"syscall"
	"time"

	"github.com/meirongdev/readlist/internal/api"
	"github.com/meirongdev/readlist/internal/config"
	"github.com/meirongdev/readlist/internal/corpus"
	"github.com/meirongdev/readlist/internal/preset"
	"github.com/meirongdev/readlist/internal/score"
	"github.com/meirongdev/readlist/internal/store"
)

func main() {
	cfg := config.Load()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		slog.Error("open db", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	presets, err := preset.Load()
	if err != nil {
		slog.Error("load presets", "err", err)
		os.Exit(1)
	}

	switch cmd {
	case "init": // 供 kind 的 initContainer:seed + score 后退出(无可执行 shell 的镜像友好)。
		if _, err := corpus.Seed(db); err != nil {
			slog.Error("init seed", "err", err)
			os.Exit(1)
		}
		eng := score.NewEngine(db, cfg.StandardVer, time.Now().UTC())
		res, err := eng.Run(presets)
		if err != nil {
			slog.Error("init score", "err", err)
			os.Exit(1)
		}
		fmt.Printf("init: run=%s works=%d\n", res.RunID, len(res.Works))

	case "seed":
		n, err := corpus.Seed(db)
		if err != nil {
			slog.Error("seed", "err", err)
			os.Exit(1)
		}
		fmt.Printf("seed: wrote %d editions (already present → 0)\n", n)

	case "score":
		eng := score.NewEngine(db, cfg.StandardVer, time.Now().UTC())
		res, err := eng.Run(presets)
		if err != nil {
			slog.Error("score", "err", err)
			os.Exit(1)
		}
		printRunSummary(res, presets)

	case "dryrun":
		eng := score.NewEngine(db, cfg.StandardVer, time.Now().UTC())
		c, err := eng.Compute(presets)
		if err != nil {
			slog.Error("dryrun", "err", err)
			os.Exit(1)
		}
		printDryRun(c, presets)

	case "diff":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "usage: readlist diff <runA> <runB>")
			os.Exit(2)
		}
		diffLists(db, os.Args[2], os.Args[3])

	case "serve":
		serve(cfg, db, presets)

	default:
		fmt.Fprintln(os.Stderr, "usage: readlist [init|seed|score|dryrun|diff|serve]")
		os.Exit(2)
	}
}

func printRunSummary(res *score.RunResult, presets []preset.Preset) {
	fmt.Printf("score: run=%s works=%d\n", res.RunID, len(res.Works))
	grade := map[string]int{}
	for _, g := range res.Grade {
		grade[g]++
	}
	for _, g := range []string{"A", "B", "C", "D"} {
		fmt.Printf("  grade %s: %d\n", g, grade[g])
	}
	for _, p := range presets {
		fmt.Printf("  list %-16s %2d items\n", p.ID, len(res.Lists[p.ID]))
	}
}

func printDryRun(c *score.Computation, presets []preset.Preset) {
	fmt.Println("dryrun: 只数不算(每维 measured 比例 + 每 preset 已选数)")
	byDim := map[score.Dim]int{}
	total := len(c.Inputs)
	for _, raws := range c.Raws {
		for dim, r := range raws {
			if r.State == score.StateMeasured {
				byDim[dim]++
			}
		}
	}
	for _, dim := range score.AllDims {
		fmt.Printf("  dim %-11s measured %3d / %d (%.0f%%)\n", dim, byDim[dim], total, 100*float64(byDim[dim])/float64(total))
	}
	public := 0
	for _, g := range c.Grade {
		if g != "D" {
			public++
		}
	}
	fmt.Printf("  public works (A/B/C): %d / %d\n", public, total)
	for _, p := range presets {
		fmt.Printf("  preset %-16s selected %2d\n", p.ID, len(c.Lists[p.ID]))
	}
}

func diffLists(db *store.DB, a, b string) {
	load := func(runID string) map[string]map[string]string {
		out := map[string]map[string]string{}
		rows, err := db.SQL().Query(`SELECT list_id, rank, work_id FROM lists WHERE run_id=? ORDER BY list_id, rank`, runID)
		if err != nil {
			slog.Error("diff", "err", err)
			os.Exit(1)
		}
		defer rows.Close()
		for rows.Next() {
			var listID, workID string
			var rank int
			if err := rows.Scan(&listID, &rank, &workID); err != nil {
				continue
			}
			if out[listID] == nil {
				out[listID] = map[string]string{}
			}
			out[listID][workID] = fmt.Sprintf("%d", rank)
		}
		return out
	}
	la, lb := load(a), load(b)
	for listID, ma := range la {
		mb := lb[listID]
		var added, removed []string
		for wid := range ma {
			if _, ok := mb[wid]; !ok {
				removed = append(removed, wid)
			}
		}
		for wid := range mb {
			if _, ok := ma[wid]; !ok {
				added = append(added, wid)
			}
		}
		if len(added) > 0 || len(removed) > 0 {
			fmt.Printf("%s: +%d -%d\n", listID, len(added), len(removed))
			fmt.Printf("  removed: %s\n  added:   %s\n", strings.Join(removed, ", "), strings.Join(added, ", "))
		}
	}
}

// serve 启动只读 API + 内嵌 SPA;若尚未发布 run,先自愈打分一次。
func serve(cfg config.Config, db *store.DB, presets []preset.Preset) {
	var runID string
	err := db.SQL().QueryRow(`SELECT run_id FROM published_run WHERE id=1`).Scan(&runID)
	if err == sql.ErrNoRows || runID == "" {
		slog.Info("no published run, scoring once before serve")
		eng := score.NewEngine(db, cfg.StandardVer, time.Now().UTC())
		if _, err := eng.Run(presets); err != nil {
			slog.Error("startup score", "err", err)
			os.Exit(1)
		}
	} else if err != nil {
		slog.Error("published_run", "err", err)
		os.Exit(1)
	}

	httpServer := &http.Server{
		Addr:              cfg.APIListenAddr,
		Handler:           api.NewServer(db, presets, cfg.ExposeReadStatus).Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		slog.Info("api listening", "addr", cfg.APIListenAddr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http server", "err", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	slog.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(ctx)
}
