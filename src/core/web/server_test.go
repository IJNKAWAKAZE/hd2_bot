package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// quiet 是不产生输出的日志函数，避免用例刷屏。
func quiet(string, ...any) {}

// recorder 记录日志行，用于断言启动日志的内容。
type recorder struct {
	mu    sync.Mutex
	lines []string
}

// logf 实现日志函数签名，把每行日志追加到内部切片。
func (r *recorder) logf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, fmt.Sprintf(format, args...))
}

// joined 返回全部日志行拼接结果。
func (r *recorder) joined() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.lines, "\n")
}

// healthBody 解析 /healthz 的 JSON 响应。
func healthBody(t *testing.T, resp *http.Response) map[string]string {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("解析 /healthz 响应失败：%v", err)
	}
	return body
}

// TestHealthzReady 验证依赖就绪时 /healthz 返回 200、状态 ok 与 RFC3339 时间。
func TestHealthzReady(t *testing.T) {
	srv := httptest.NewServer(newEngine(Options{Ready: func() bool { return true }, Logger: quiet}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("请求 /healthz 失败：%v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", resp.StatusCode)
	}
	body := healthBody(t, resp)
	if body["status"] != "ok" {
		t.Fatalf("期望 status=ok，实际 %q", body["status"])
	}
	if _, err := time.Parse(time.RFC3339, body["time"]); err != nil {
		t.Fatalf("time 不是 RFC3339 格式：%q（%v）", body["time"], err)
	}
}

// TestHealthzNotReady 验证依赖未就绪时 /healthz 返回 503，不能一律报 ok。
func TestHealthzNotReady(t *testing.T) {
	srv := httptest.NewServer(newEngine(Options{Ready: func() bool { return false }, Logger: quiet}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("请求 /healthz 失败：%v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("期望 503，实际 %d", resp.StatusCode)
	}
	body := healthBody(t, resp)
	if body["status"] == "ok" {
		t.Fatal("未就绪时不应返回 status=ok")
	}
	if _, err := time.Parse(time.RFC3339, body["time"]); err != nil {
		t.Fatalf("time 不是 RFC3339 格式：%q（%v）", body["time"], err)
	}
}

// TestHealthzWithoutReadyHook 验证未注入就绪回调时按就绪处理（简化的离线场景）。
func TestHealthzWithoutReadyHook(t *testing.T) {
	srv := httptest.NewServer(newEngine(Options{Logger: quiet}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("请求 /healthz 失败：%v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", resp.StatusCode)
	}
	_ = healthBody(t, resp)
}

// TestUnknownPathNotFound 验证未注册的路径返回 404，健康检查不会兜住所有请求。
func TestUnknownPathNotFound(t *testing.T) {
	srv := httptest.NewServer(newEngine(Options{Logger: quiet}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/nope")
	if err != nil {
		t.Fatalf("请求未知路径失败：%v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("期望 404，实际 %d", resp.StatusCode)
	}
}

// TestStartListenError 验证端口被占用时 Start 立即返回中文错误，且不返回半成品服务。
func TestStartListenError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("占位监听失败：%v", err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port

	srv, err := Start(Options{Host: "127.0.0.1", Port: port, Logger: quiet})
	if err == nil {
		if srv != nil {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = srv.Shutdown(ctx)
		}
		t.Fatal("端口被占用时应当返回错误")
	}
	if srv != nil {
		t.Fatal("启动失败时不应返回服务实例")
	}
	if !strings.Contains(err.Error(), "HTTP 服务启动失败") {
		t.Fatalf("错误信息应说明 HTTP 服务启动失败，实际：%v", err)
	}
}

// TestStartServesAndShutsDown 验证真实监听端口可访问，关闭后不再接受连接，且关闭可重复调用。
func TestStartServesAndShutsDown(t *testing.T) {
	logs := &recorder{}
	srv, err := Start(Options{Host: "127.0.0.1", Port: 0, Ready: func() bool { return true }, Logger: logs.logf})
	if err != nil {
		t.Fatalf("启动 HTTP 服务失败：%v", err)
	}
	addr := srv.Addr()
	if !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Fatalf("监听地址异常：%q", addr)
	}
	if !strings.Contains(logs.joined(), "HTTP 服务已启动") {
		t.Fatalf("缺少启动日志，实际：%s", logs.joined())
	}

	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("请求 /healthz 失败：%v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", resp.StatusCode)
	}
	_ = healthBody(t, resp)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("关闭 HTTP 服务失败：%v", err)
	}
	if _, err := http.Get("http://" + addr + "/healthz"); err == nil {
		t.Fatal("关闭后仍能连接 HTTP 服务")
	}
	// 重复关闭应当安全（停机路径可能被重复触发）。
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("重复关闭 HTTP 服务失败：%v", err)
	}
}

// TestNilServerShutdown 验证空实例关闭不出错，便于停机路径直接调用。
func TestNilServerShutdown(t *testing.T) {
	var srv *Server
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("空实例关闭应当返回 nil，实际：%v", err)
	}
}
