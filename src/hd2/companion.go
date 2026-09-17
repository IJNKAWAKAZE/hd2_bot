package hd2

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// 补充源允许访问的固定路径。S1 只接入下面两条；
// 个人指令位于另一台 CDN 主机，等 S5 需要时再单独接入。
const (
	CompanionPathAPIData = "/hell-divers-2-api/get-api-data-live"
	CompanionPathNews    = "/steam-api/news"
)

// defaultUserAgent 是补充源在调用方未提供 User-Agent 时使用的兜底值。
const defaultUserAgent = "hd2_bot/0.1"

// companionPaths 是路径白名单；限定路径可以避免被误导请求任意地址。
var companionPaths = map[string]bool{
	CompanionPathAPIData: true,
	CompanionPathNews:    true,
}

// Companion 是 Helldivers Companion（非官方）补充源客户端。
// 它只提供主源没有的数据，失败时不影响主链路，调用方自行决定是否降级；
// 因此这里不做重试，只做限流、路径白名单与错误封装。
type Companion struct {
	base      string
	userAgent string
	http      *http.Client
	limiter   *Limiter
	logf      func(string, ...any)
}

// NewCompanion 创建补充源客户端；base 形如 https://helldiverscompanion.com/api。
// userAgent 为空时使用默认值；timeout 与 logf 为零值时使用默认值；limiter 为 nil 时不限流。
func NewCompanion(base, userAgent string, timeout time.Duration, limiter *Limiter, logf func(string, ...any)) *Companion {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if strings.TrimSpace(userAgent) == "" {
		userAgent = defaultUserAgent
	}
	return &Companion{
		base:      strings.TrimSuffix(base, "/"),
		userAgent: userAgent,
		http:      &http.Client{Timeout: timeout},
		limiter:   limiter,
		logf:      logf,
	}
}

// Get 请求白名单内的路径并返回原始响应体。
// 路径不在白名单时直接返回错误，不会发出任何请求；429 等错误按 statusError 返回。
func (c *Companion) Get(ctx context.Context, path string) ([]byte, error) {
	if !companionPaths[path] {
		return nil, fmt.Errorf("不允许访问的补充源路径：%s", path)
	}
	if c.limiter != nil {
		if err := c.limiter.Wait(ctx); err != nil {
			return nil, err
		}
	}
	url := c.base + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("构造补充源请求失败：%w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	c.logf("补充源请求 path=%s", path)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求补充源 %s 失败：%w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return nil, &statusError{code: resp.StatusCode, body: strings.TrimSpace(string(body))}
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取补充源响应失败：%w", err)
	}
	return body, nil
}
