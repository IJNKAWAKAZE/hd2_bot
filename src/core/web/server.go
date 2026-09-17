// Package web 提供本地 HTTP 服务：S1 只有健康检查，S3 会在这里托管出图模板。
package web

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// readHeaderTimeout 限制读请求头的时间，避免慢连接占住服务。
const readHeaderTimeout = 5 * time.Second

// Options 是 HTTP 服务的构造参数。
type Options struct {
	Host string // 监听地址，例如 127.0.0.1
	Port int    // 监听端口；为 0 时由系统分配（仅测试用）
	// Ready 报告依赖（数据层、后台任务）是否就绪；未就绪时 /healthz 返回 503，
	// 让探活方能区分「进程活着」与「服务可用」。为 nil 时始终视为就绪。
	Ready func() bool
	// Logger 是日志函数；为空时使用标准日志。
	Logger func(format string, args ...any)
}

// Server 是运行中的 HTTP 服务；请用 Start 创建，空实例的 Shutdown 是安全操作。
type Server struct {
	srv *http.Server
	ln  net.Listener
	// 关闭只做一次：重复调用 Shutdown 是停机路径的正常现象。
	once sync.Once
	err  error
}

// newEngine 构造路由。S1 只有 /healthz；S3 的出图接口会追加在这里。
func newEngine(opts Options) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()
	engine.Use(gin.Recovery())
	engine.GET("/healthz", func(c *gin.Context) {
		code := http.StatusOK
		status := "ok"
		if opts.Ready != nil && !opts.Ready() {
			// 依赖未就绪或正在停机：进程还在，但不能对外服务。
			code = http.StatusServiceUnavailable
			status = "starting"
		}
		c.JSON(code, gin.H{"status": status, "time": time.Now().Format(time.RFC3339)})
	})
	return engine
}

// Start 监听端口并启动服务（内部起 goroutine，不阻塞调用方）。
// 监听失败（端口被占用、地址非法等）会立即返回错误，且不返回半成品实例，
// 调用方据此以非零退出码结束进程。
func Start(opts Options) (*Server, error) {
	logf := opts.Logger
	if logf == nil {
		logf = log.Printf
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", opts.Host, opts.Port))
	if err != nil {
		return nil, fmt.Errorf("HTTP 服务启动失败（监听 %s:%d，端口可能已被占用）：%w", opts.Host, opts.Port, err)
	}
	srv := &http.Server{
		Handler:           newEngine(opts),
		ReadHeaderTimeout: readHeaderTimeout,
	}
	s := &Server{srv: srv, ln: ln}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logf("HTTP 服务退出：%v", err)
		}
	}()
	logf("HTTP 服务已启动：http://%s/healthz", ln.Addr().String())
	return s, nil
}

// Addr 返回实际监听地址（形如 127.0.0.1:25555）；空实例返回空串。
func (s *Server) Addr() string {
	if s == nil || s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

// Shutdown 优雅关闭：先停止接受新连接，再等待在途请求结束；ctx 超时仍未结束的请求会被强制中断。
// 可重复调用，空实例返回 nil，便于停机路径无脑调用。
func (s *Server) Shutdown(ctx context.Context) error {
	if s == nil || s.srv == nil {
		return nil
	}
	s.once.Do(func() { s.err = s.srv.Shutdown(ctx) })
	return s.err
}
