package hd2

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestClient 创建指向假服务器的客户端，限流宽松、退避很短以便测试快速跑完。
func newTestClient(t *testing.T, handler http.Handler) (*Client, *Limiter) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	limiter := NewLimiter(100, time.Second, 20*time.Second)
	client := NewClient(ClientConfig{
		BaseURL:       server.URL,
		Timeout:       5 * time.Second,
		Client:        "hd2_bot_test",
		Contact:       "dev@example.com",
		UserAgent:     "hd2_bot_test/0.1",
		RetryMax:      3,
		RetryBase:     10 * time.Millisecond,
		RetryMaxDelay: 20 * time.Millisecond,
	}, limiter)
	return client, limiter
}

// TestClientGetSuccess 校验成功取数并带上上游要求的请求头。
func TestClientGetSuccess(t *testing.T) {
	var gotClient, gotContact, gotUA string
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotClient = r.Header.Get("X-Super-Client")
		gotContact = r.Header.Get("X-Super-Contact")
		gotUA = r.Header.Get("User-Agent")
		if r.URL.Path != "/war" {
			t.Errorf("请求路径错误：%s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"clientVersion":"1.0"}`))
	}))
	body, err := client.Get(context.Background(), EndpointWar)
	if err != nil {
		t.Fatalf("请求失败：%v", err)
	}
	if string(body) != `{"clientVersion":"1.0"}` {
		t.Errorf("响应内容错误：%s", body)
	}
	if gotClient != "hd2_bot_test" || gotContact != "dev@example.com" || gotUA != "hd2_bot_test/0.1" {
		t.Errorf("请求头错误：client=%s contact=%s ua=%s", gotClient, gotContact, gotUA)
	}
}

// TestEndpointValues 校验端点常量与上游真实路径一致（改名会破坏取数，必须锁住）。
func TestEndpointValues(t *testing.T) {
	want := map[Endpoint]string{
		EndpointWar:         "war",
		EndpointPlanets:     "planets",
		EndpointCampaigns:   "campaigns",
		EndpointAssignments: "assignments",
		EndpointDispatches:  "dispatches",
		EndpointEvents:      "planet-events",
		EndpointStations:    "/api/v2/space-stations",
	}
	for ep, path := range want {
		if string(ep) != path {
			t.Errorf("端点常量错误：期望 %q，实际 %q", path, string(ep))
		}
	}
	if len(want) != 7 {
		t.Errorf("端点数量错误：%d", len(want))
	}
}

// TestEndpointURLResolvesAbsolutePath 校验以 "/" 开头的端点解析到 API 主机根目录，
// 普通端点仍然挂在配置的 base 之下，base 末尾多余的斜杠不会拼出双斜杠。
func TestEndpointURLResolvesAbsolutePath(t *testing.T) {
	cases := []struct {
		name string
		base string
		ep   Endpoint
		want string
	}{
		{"普通端点", "https://api.helldivers2.dev/api/v1", EndpointWar, "https://api.helldivers2.dev/api/v1/war"},
		{"base 末尾多余斜杠", "https://api.helldivers2.dev/api/v1/", EndpointWar, "https://api.helldivers2.dev/api/v1/war"},
		{"绝对路径端点跨版本", "https://api.helldivers2.dev/api/v1", EndpointStations, "https://api.helldivers2.dev/api/v2/space-stations"},
		{"base 解析不了时退回字面拼接", "://坏地址", EndpointStations, "://坏地址/api/v2/space-stations"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := endpointURL(c.base, c.ep); got != c.want {
				t.Errorf("endpointURL 期望 %q，实际 %q", c.want, got)
			}
		})
	}
}

// TestClientRequestsSpaceStationsOnV2 校验空间站请求真的打到 v2 路径上
// （v1 已下线，路径写错会让 /stations 永远拿 404）。
func TestClientRequestsSpaceStationsOnV2(t *testing.T) {
	var gotPath string
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`[]`))
	}))
	client.cfg.BaseURL = client.cfg.BaseURL + "/api/v1"

	if _, err := client.Get(context.Background(), EndpointStations); err != nil {
		t.Fatalf("请求失败：%v", err)
	}
	if gotPath != "/api/v2/space-stations" {
		t.Errorf("请求路径错误：%s", gotPath)
	}
}

// TestClientBaseURLTrailingSlash 校验 BaseURL 末尾多余的斜杠不会拼出双斜杠路径。
func TestClientBaseURLTrailingSlash(t *testing.T) {
	var gotPath string
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
	}))
	client.cfg.BaseURL = strings.TrimSuffix(client.cfg.BaseURL, "/") + "/"
	if _, err := client.Get(context.Background(), EndpointWar); err != nil {
		t.Fatalf("请求失败：%v", err)
	}
	if gotPath != "/war" {
		t.Errorf("请求路径错误：%s", gotPath)
	}
}

// TestClientRetryOnServerError 校验 5xx 会重试，重试后成功。
func TestClientRetryOnServerError(t *testing.T) {
	var calls int32
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	body, err := client.Get(context.Background(), EndpointWar)
	if err != nil {
		t.Fatalf("重试后应成功：%v", err)
	}
	if string(body) != `{"ok":true}` {
		t.Errorf("响应内容错误：%s", body)
	}
	if calls != 2 {
		t.Errorf("应请求 2 次，实际 %d 次", calls)
	}
}

// TestClientRetryExhausted 校验一直 5xx 时重试到上限后返回最后一次的错误。
func TestClientRetryExhausted(t *testing.T) {
	var calls int32
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	_, err := client.Get(context.Background(), EndpointWar)
	if err == nil {
		t.Fatal("持续 5xx 应返回错误")
	}
	if calls != 3 {
		t.Errorf("应重试到上限共 3 次，实际 %d 次", calls)
	}
	if !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("错误信息应包含状态码与响应体：%v", err)
	}
}

// TestClientRateLimited 校验 429 触发全局冷却并返回 ErrRateLimited，且不再重试。
func TestClientRateLimited(t *testing.T) {
	var calls int32
	client, limiter := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	_, err := client.Get(context.Background(), EndpointPlanets)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("应返回 ErrRateLimited，实际 %v", err)
	}
	if cooling := limiter.Cooling(); cooling < 29*time.Second {
		t.Fatalf("应进入 30 秒冷却，实际剩余 %v", cooling)
	}
	if calls != 1 {
		t.Fatalf("429 不应重试，实际请求 %d 次", calls)
	}
}

// TestClientNotFoundNoRetry 校验 4xx 不重试。
func TestClientNotFoundNoRetry(t *testing.T) {
	var calls int32
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("not found"))
	}))
	if _, err := client.Get(context.Background(), EndpointCampaigns); err == nil {
		t.Fatal("404 应返回错误")
	}
	if calls != 1 {
		t.Fatalf("4xx 不应重试，实际请求 %d 次", calls)
	}
}

// TestClientContextCanceledDuringBackoff 校验退避等待期间取消 ctx 会立即返回。
func TestClientContextCanceledDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel() // 第一次响应后取消，随后的退避应立即中断
		w.WriteHeader(http.StatusInternalServerError)
	}))
	client.cfg.RetryBase = time.Hour
	client.cfg.RetryMaxDelay = time.Hour
	start := time.Now()
	_, err := client.Get(ctx, EndpointWar)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("应返回 context.Canceled，实际 %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("取消后应立即返回，实际耗时 %v", elapsed)
	}
}

// TestClientDefaults 校验缺省配置会被补齐为可用值。
func TestClientDefaults(t *testing.T) {
	c := NewClient(ClientConfig{}, nil)
	if c.cfg.Timeout != 30*time.Second {
		t.Errorf("默认超时错误：%v", c.cfg.Timeout)
	}
	if c.cfg.RetryMax != 3 || c.cfg.RetryBase != 5*time.Second || c.cfg.RetryMaxDelay != 45*time.Second {
		t.Errorf("默认重试参数错误：%+v", c.cfg)
	}
	if c.logf == nil {
		t.Error("默认日志函数应可用，不能为 nil")
	}
}

// TestParseRetryAfter 校验 Retry-After 的两种格式。
func TestParseRetryAfter(t *testing.T) {
	if got := parseRetryAfter("30"); got != 30*time.Second {
		t.Errorf("秒数格式解析错误：%v", got)
	}
	if got := parseRetryAfter("  30  "); got != 30*time.Second {
		t.Errorf("带空白的秒数应解析成功：%v", got)
	}
	if got := parseRetryAfter("0"); got != 0 {
		t.Errorf("0 应返回 0：%v", got)
	}
	if got := parseRetryAfter("-5"); got != 0 {
		t.Errorf("负数应返回 0：%v", got)
	}
	if got := parseRetryAfter(""); got != 0 {
		t.Errorf("空值应返回 0：%v", got)
	}
	if got := parseRetryAfter("not-a-date"); got != 0 {
		t.Errorf("非法值应返回 0：%v", got)
	}
	future := time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(future); got < 25*time.Second || got > 31*time.Second {
		t.Errorf("HTTP 日期格式解析错误：%v", got)
	}
	past := time.Now().Add(-time.Minute).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(past); got != 0 {
		t.Errorf("过去的日期应返回 0：%v", got)
	}
}

// TestClientNilLimiter 校验未配置限流器时（离线调试场景）请求 429 不会 panic。
func TestClientNilLimiter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	client := NewClient(ClientConfig{
		BaseURL:       server.URL,
		RetryMax:      1,
		RetryBase:     time.Millisecond,
		RetryMaxDelay: time.Millisecond,
	}, nil)
	_, err := client.Get(context.Background(), EndpointWar)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("应返回 ErrRateLimited，实际 %v", err)
	}
}
