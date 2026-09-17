package hd2

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Endpoint 是上游数据端点的相对路径。
type Endpoint string

// 上游可用的端点（2026-09-16 实测确认；重要指令是 assignments，不是 major-orders）。
//
// 端点里只有空间站带版本前缀：上游已经下线 /api/v1/space-stations（实测恒定 404），
// 数据只在 /api/v2/space-stations 上。以 "/" 开头的端点表示「相对 API 主机根目录」，
// 由 endpointURL 解析（见那里的说明），这样不必为了一个端点改整套 base 配置。
const (
	EndpointWar         Endpoint = "war"
	EndpointPlanets     Endpoint = "planets"
	EndpointCampaigns   Endpoint = "campaigns"
	EndpointAssignments Endpoint = "assignments"
	EndpointDispatches  Endpoint = "dispatches"
	EndpointEvents      Endpoint = "planet-events"
	EndpointStations    Endpoint = "/api/v2/space-stations"
)

// maxErrorBody 是失败响应最多读取的字节数，够用于日志排查即可。
const maxErrorBody = 512

// ClientConfig 是主数据源客户端的配置。零值会被 NewClient 补齐为默认值。
type ClientConfig struct {
	BaseURL       string
	Timeout       time.Duration
	Client        string // X-Super-Client，上游必填
	Contact       string // X-Super-Contact，上游必填
	UserAgent     string
	RetryMax      int
	RetryBase     time.Duration
	RetryMaxDelay time.Duration
	Logger        func(format string, args ...any)
}

// Client 访问 api.helldivers2.dev。所有请求共用同一个限流器。
type Client struct {
	cfg     ClientConfig
	http    *http.Client
	limiter *Limiter
	logf    func(string, ...any)
}

// NewClient 创建主数据源客户端；limiter 为全局限流器，所有请求共用。
// limiter 为 nil 时不限流，仅测试或离线场景可用（正常业务必须传入实例）。
func NewClient(cfg ClientConfig, limiter *Limiter) *Client {
	if cfg.Logger == nil {
		cfg.Logger = func(string, ...any) {}
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.RetryMax <= 0 {
		cfg.RetryMax = 3
	}
	if cfg.RetryBase <= 0 {
		cfg.RetryBase = 5 * time.Second
	}
	if cfg.RetryMaxDelay <= 0 {
		cfg.RetryMaxDelay = 45 * time.Second
	}
	return &Client{
		cfg:     cfg,
		http:    &http.Client{Timeout: cfg.Timeout},
		limiter: limiter,
		logf:    cfg.Logger,
	}
}

// Get 请求指定端点并返回原始 JSON。
// 流程：取令牌 → 请求 → 429 则全局冷却并返回包装了 ErrRateLimited 的错误（不重试）→
// 其余 4xx 直接返回（重试没有意义）→ 5xx 与网络错误按指数退避重试，最多 RetryMax 次。
// ctx 取消或超时会在取令牌、退避等待中立即返回对应的 ctx 错误。
func (c *Client) Get(ctx context.Context, ep Endpoint) ([]byte, error) {
	var lastErr error
	for attempt := 1; attempt <= c.cfg.RetryMax; attempt++ {
		if err := c.wait(ctx); err != nil {
			return nil, err
		}
		c.logf("上游请求 endpoint=%s attempt=%d", ep, attempt)
		body, retryAfter, err := c.request(ctx, ep)
		if err == nil {
			return body, nil
		}
		lastErr = err

		var se *statusError
		if errors.As(err, &se) {
			switch {
			case se.code == http.StatusTooManyRequests:
				// limiter 为 nil 时（离线调试）只汇报限流，不做全局冷却。
				if c.limiter != nil {
					c.limiter.NoteRateLimited(retryAfter)
				}
				c.logf("上游限流 endpoint=%s retry_after=%s", ep, retryAfter)
				// 只保留一个 %w：多 %w 是 Go 1.20 起才支持的写法，本仓库跑 -race 用的
				// go1.19.3 既不包装错误、还会把占位符渲染成 %!w。状态码上面已由 logf 输出。
				return nil, fmt.Errorf("%w：%v", ErrRateLimited, err)
			case se.code >= 400 && se.code < 500:
				return nil, err
			}
		}
		if attempt == c.cfg.RetryMax {
			break
		}
		if err := c.backoff(ctx, attempt); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

// endpointURL 把端点拼成完整地址。
//
// 普通端点挂在配置的 base 之下（base = https://api.helldivers2.dev/api/v1 → …/api/v1/war）；
// 以 "/" 开头的端点表示「相对 API 主机根目录」，用于引用同一个上游服务的其它版本
// （例如 v1 已下线的 /api/v2/space-stations）。base 解析失败时退回按字面拼接，
// 让请求照常发出去并得到上游的真实响应，而不是在本进程里先报一个构造错误。
func endpointURL(base string, ep Endpoint) string {
	trimmed := strings.TrimSuffix(base, "/")
	if strings.HasPrefix(string(ep), "/") {
		if u, err := url.Parse(base); err == nil && u.Host != "" {
			return u.Scheme + "://" + u.Host + string(ep)
		}
		return trimmed + string(ep)
	}
	return trimmed + "/" + string(ep)
}

// wait 取得一次请求额度；limiter 为 nil 时直接放行。
func (c *Client) wait(ctx context.Context) error {
	if c.limiter == nil {
		return nil
	}
	return c.limiter.Wait(ctx)
}

// request 发起一次请求，返回响应体、Retry-After 时长与错误。
func (c *Client) request(ctx context.Context, ep Endpoint) ([]byte, time.Duration, error) {
	url := endpointURL(c.cfg.BaseURL, ep)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("构造请求失败：%w", err)
	}
	req.Header.Set("X-Super-Client", c.cfg.Client)
	req.Header.Set("X-Super-Contact", c.cfg.Contact)
	req.Header.Set("User-Agent", c.cfg.UserAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("请求 %s 失败：%w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
	// 上游只会给 x-ratelimit-limit / x-ratelimit-remaining，没有 reset，因此窗口长度无法得知；
	// 把剩余额度打进日志，方便线上判断本地限流参数是否需要调整。
	if left := resp.Header.Get("x-ratelimit-remaining"); left != "" {
		c.logf("上游限流剩余 endpoint=%s remaining=%s limit=%s",
			ep, left, resp.Header.Get("x-ratelimit-limit"))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return nil, retryAfter, &statusError{code: resp.StatusCode, body: strings.TrimSpace(string(body))}
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("读取 %s 响应失败：%w", url, err)
	}
	return body, retryAfter, nil
}

// backoff 按 2 的幂次退避（RetryBase、2×RetryBase、4×RetryBase…），最长不超过 RetryMaxDelay。
func (c *Client) backoff(ctx context.Context, attempt int) error {
	d := c.cfg.RetryBase * time.Duration(1<<(attempt-1))
	if d > c.cfg.RetryMaxDelay {
		d = c.cfg.RetryMaxDelay
	}
	c.logf("上游请求失败，%s 后重试", d)
	return sleepContext(ctx, d)
}

// parseRetryAfter 解析 Retry-After 响应头，支持秒数与 HTTP 日期两种写法。
// 缺省、0、负数、非法值以及已经过去的日期都返回 0，表示没有可用的冷却时长。
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}
