package hd2

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestCompanionGetSuccess 校验白名单内的路径可以取回数据。
func TestCompanionGetSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != CompanionPathAPIData {
			t.Errorf("请求路径错误：%s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	c := NewCompanion(server.URL, "hd2_bot_test/0.1", 5*time.Second, NewLimiter(100, time.Second, time.Second), nil)
	body, err := c.Get(context.Background(), CompanionPathAPIData)
	if err != nil {
		t.Fatalf("补充源请求失败：%v", err)
	}
	if string(body) != `{"ok":true}` {
		t.Errorf("响应内容错误：%s", body)
	}
}

// TestCompanionPathValues 校验白名单常量与真实路径一致。
func TestCompanionPathValues(t *testing.T) {
	if CompanionPathAPIData != "/hell-divers-2-api/get-api-data-live" {
		t.Errorf("数据接口路径错误：%s", CompanionPathAPIData)
	}
	if CompanionPathNews != "/steam-api/news" {
		t.Errorf("新闻接口路径错误：%s", CompanionPathNews)
	}
}

// TestCompanionRejectsUnknownPath 校验白名单外的路径被拒绝，且不会真的发出请求。
func TestCompanionRejectsUnknownPath(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
	}))
	defer server.Close()

	c := NewCompanion(server.URL, "hd2_bot_test/0.1", time.Second, NewLimiter(10, time.Second, time.Second), nil)
	for _, path := range []string{"/evil/path", "", "/hell-divers-2-api/get-api-data-live/../..", "https://evil.example.com/"} {
		if _, err := c.Get(context.Background(), path); err == nil {
			t.Errorf("白名单外的路径 %q 应被拒绝", path)
		}
	}
	if calls != 0 {
		t.Errorf("被拒绝的路径不应发出请求，实际请求 %d 次", calls)
	}
}

// TestCompanionErrorStatus 校验非 2xx 返回带状态码与响应体的错误。
func TestCompanionErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("bad gateway"))
	}))
	defer server.Close()

	c := NewCompanion(server.URL, "hd2_bot_test/0.1", 5*time.Second, NewLimiter(100, time.Second, time.Second), nil)
	_, err := c.Get(context.Background(), CompanionPathNews)
	if err == nil {
		t.Fatal("502 应返回错误")
	}
	if !strings.Contains(err.Error(), "502") || !strings.Contains(err.Error(), "bad gateway") {
		t.Errorf("错误信息应包含状态码与响应体：%v", err)
	}
}

// TestCompanionBaseTrailingSlash 校验 base 末尾多余的斜杠不会拼出双斜杠。
func TestCompanionBaseTrailingSlash(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
	}))
	defer server.Close()

	c := NewCompanion(server.URL+"/", "hd2_bot_test/0.1", 5*time.Second, NewLimiter(100, time.Second, time.Second), nil)
	if _, err := c.Get(context.Background(), CompanionPathNews); err != nil {
		t.Fatalf("补充源请求失败：%v", err)
	}
	if gotPath != CompanionPathNews {
		t.Errorf("请求路径错误：%s", gotPath)
	}
}

// TestCompanionDefaults 校验缺省参数被补齐，nil 限流器与 nil 日志函数都可用。
func TestCompanionDefaults(t *testing.T) {
	c := NewCompanion("https://example.com/", "", 0, nil, nil)
	if c.http.Timeout != 30*time.Second {
		t.Errorf("默认超时错误：%v", c.http.Timeout)
	}
	if c.logf == nil {
		t.Error("默认日志函数应可用，不能为 nil")
	}
	if c.base != "https://example.com" {
		t.Errorf("base 末尾斜杠未清理：%s", c.base)
	}
	if c.userAgent != defaultUserAgent {
		t.Errorf("空 User-Agent 应回落到默认值：%q", c.userAgent)
	}
}

// TestCompanionContextCanceled 校验 ctx 取消后返回 ctx 错误且不发出请求。
func TestCompanionContextCanceled(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
	}))
	defer server.Close()

	c := NewCompanion(server.URL, "hd2_bot_test/0.1", 5*time.Second, NewLimiter(100, time.Second, time.Second), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Get(ctx, CompanionPathNews); err == nil {
		t.Fatal("ctx 已取消应返回错误")
	}
	if calls != 0 {
		t.Errorf("ctx 已取消不应发出请求，实际 %d 次", calls)
	}
}
