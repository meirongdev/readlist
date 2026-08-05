// Package facts 摄入外部证据(Google Books / OpenLibrary / HN Algolia)。
//
// 这是**唯一**发起网络请求的地方。score 命令永远离线(system-design §9),
// 所以反复调评分公式不需要重新烧配额,也不会因为外部源挂掉而出不了榜。
//
// 三条硬约束:
//   - **配额感知**:每次运行有请求预算,打满就干净停下,下次接着跑(NFR-9);
//   - **原样缓存 + TTL**:外部响应原样存 evidence,派生分全部从缓存重算(FR-11);
//   - **礼貌限速**:请求之间留间隔;拿到 429 就停掉该源,不空转打 429。
package facts

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// ErrBudgetExhausted 本次运行的请求预算用完了。不是失败 —— 下次运行继续。
var ErrBudgetExhausted = errors.New("外部请求预算已用完")

// ErrSourceBlocked 该源返回了 429,本次运行不再打它。
var ErrSourceBlocked = errors.New("外部源已限流")

// client 带预算、限速与 429 熔断的 HTTP 客户端。
type client struct {
	http    *http.Client
	budget  int
	used    int
	sleep   time.Duration
	blocked map[string]bool
	lastAt  time.Time
	// Requests 之外单独记 429,便于把「配额打满」与「网络故障」分开报。
	throttled int
}

func newClient(budget int, sleep time.Duration) *client {
	return &client{
		http:    &http.Client{Timeout: 20 * time.Second},
		budget:  budget,
		sleep:   sleep,
		blocked: map[string]bool{},
	}
}

// getJSON 取一个 JSON 文档。
//
// 返回 found=false 表示对方明确说"没有这条记录"(404/410),这也是一条值得缓存的
// 事实 —— 不缓存的话每晚都会为同一批查不到的书重新烧配额。
func (c *client) getJSON(source, rawURL string, out any) (found bool, err error) {
	if c.blocked[source] {
		return false, ErrSourceBlocked
	}
	if c.budget > 0 && c.used >= c.budget {
		return false, ErrBudgetExhausted
	}
	if c.sleep > 0 && !c.lastAt.IsZero() {
		if wait := c.sleep - time.Since(c.lastAt); wait > 0 {
			time.Sleep(wait)
		}
	}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return false, err
	}
	// 带上可联系的 UA:OpenLibrary 明确要求,也是对免费服务应有的礼貌。
	req.Header.Set("User-Agent", "readlist/1.0 (+https://readlist.meirong.dev)")
	req.Header.Set("Accept", "application/json")

	c.used++
	c.lastAt = time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	// 限制读入体积:外部源不可控,不能让一个畸形响应吃掉内存。
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return false, err
	}

	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return false, nil
	case resp.StatusCode == http.StatusTooManyRequests:
		// 实测过:Google Books 的匿名配额是按共享项目算的,很容易一上来就是 429。
		// 熔断该源,本次运行不再打它,已拿到的数据照常落库。
		c.blocked[source] = true
		c.throttled++
		return false, fmt.Errorf("%w: %s", ErrSourceBlocked, source)
	case resp.StatusCode >= 500:
		return false, fmt.Errorf("%s 返回 %d", source, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return false, fmt.Errorf("%s 返回 %d: %s", source, resp.StatusCode, truncate(body, 200))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return false, fmt.Errorf("解析 %s 响应: %w", source, err)
	}
	return true, nil
}

// remaining 还剩多少请求预算(0 表示不限)。
func (c *client) remaining() int {
	if c.budget <= 0 {
		return 1 << 30
	}
	if c.used >= c.budget {
		return 0
	}
	return c.budget - c.used
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}

func joinURL(base string, path string, query url.Values) string {
	u := base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	return u
}
