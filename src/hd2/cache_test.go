package hd2

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestCacheHit 校验命中缓存时不会再次调用取数函数。
func TestCacheHit(t *testing.T) {
	c := NewCache()
	var calls int32
	fn := func(ctx context.Context) (string, error) {
		atomic.AddInt32(&calls, 1)
		return "value", nil
	}
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		got, err := Fetch(c, ctx, "war", time.Minute, fn)
		if err != nil {
			t.Fatalf("第 %d 次取值失败：%v", i+1, err)
		}
		if got != "value" {
			t.Fatalf("返回值错误：%s", got)
		}
	}
	if calls != 1 {
		t.Fatalf("取数函数应只调用 1 次，实际 %d 次", calls)
	}
}

// TestCacheSingleFlight 校验同一 key 的并发请求只触发一次取数。
func TestCacheSingleFlight(t *testing.T) {
	c := NewCache()
	var calls int32
	fn := func(ctx context.Context) (int, error) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(30 * time.Millisecond)
		return 42, nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := Fetch(c, context.Background(), "planets", time.Minute, fn); err != nil {
				t.Errorf("并发取值失败：%v", err)
			}
		}()
	}
	wg.Wait()
	if calls != 1 {
		t.Fatalf("并发 8 次只应触发 1 次取数，实际 %d 次", calls)
	}
}

// TestCacheExpire 校验超过 TTL 后重新取数。
func TestCacheExpire(t *testing.T) {
	c := NewCache()
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	var calls int32
	fn := func(ctx context.Context) (string, error) {
		atomic.AddInt32(&calls, 1)
		return "v", nil
	}
	ctx := context.Background()
	if _, err := Fetch(c, ctx, "war", 60*time.Second, fn); err != nil {
		t.Fatalf("首次取值失败：%v", err)
	}
	now = now.Add(61 * time.Second)
	if _, err := Fetch(c, ctx, "war", 60*time.Second, fn); err != nil {
		t.Fatalf("过期后取值失败：%v", err)
	}
	if calls != 2 {
		t.Fatalf("过期后应重新取数，期望 2 次，实际 %d 次", calls)
	}
}

// TestCacheErrorNotCached 校验失败结果不会被缓存。
func TestCacheErrorNotCached(t *testing.T) {
	c := NewCache()
	var calls int32
	wantErr := errors.New("boom")
	fn := func(ctx context.Context) (string, error) {
		atomic.AddInt32(&calls, 1)
		return "", wantErr
	}
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := Fetch(c, ctx, "war", time.Minute, fn); !errors.Is(err, wantErr) {
			t.Fatalf("应返回取数错误，实际 %v", err)
		}
	}
	if calls != 2 {
		t.Fatalf("失败结果不应缓存，期望 2 次调用，实际 %d 次", calls)
	}
}

// TestCacheInvalidate 校验主动失效后重新取数。
func TestCacheInvalidate(t *testing.T) {
	c := NewCache()
	var calls int32
	fn := func(ctx context.Context) (string, error) {
		atomic.AddInt32(&calls, 1)
		return "v", nil
	}
	ctx := context.Background()
	if _, err := Fetch(c, ctx, "war", time.Minute, fn); err != nil {
		t.Fatalf("取值失败：%v", err)
	}
	c.Invalidate("war")
	if _, err := Fetch(c, ctx, "war", time.Minute, fn); err != nil {
		t.Fatalf("失效后取值失败：%v", err)
	}
	if calls != 2 {
		t.Fatalf("失效后应重新取数，期望 2 次，实际 %d 次", calls)
	}
}

// TestCachePanic 校验取数函数 panic 时返回错误、清理 in-flight，且该 key 之后仍能正常取数。
func TestCachePanic(t *testing.T) {
	c := NewCache()
	var calls int32
	fn := func(ctx context.Context) (string, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			panic("boom")
		}
		return "ok", nil
	}
	ctx := context.Background()
	_, err := Fetch(c, ctx, "war", time.Minute, fn)
	if err == nil {
		t.Fatal("取数函数 panic 时应返回错误")
	}
	if !strings.Contains(err.Error(), "war") {
		t.Fatalf("错误信息应包含 key，实际 %v", err)
	}
	got, err := Fetch(c, ctx, "war", time.Minute, fn)
	if err != nil {
		t.Fatalf("panic 之后同一 key 应能重新取数：%v", err)
	}
	if got != "ok" {
		t.Fatalf("返回值错误：%s", got)
	}
	if calls != 2 {
		t.Fatalf("期望取数 2 次，实际 %d 次", calls)
	}
}

// TestCacheInvalidateDuringInflight 校验 in-flight 期间失效后，旧值不会被写回缓存。
func TestCacheInvalidateDuringInflight(t *testing.T) {
	c := NewCache()
	var calls int32
	started := make(chan struct{})
	release := make(chan struct{})
	fn := func(ctx context.Context) (string, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			close(started)
			<-release
		}
		return "v", nil
	}
	ctx := context.Background()
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := Fetch(c, ctx, "war", time.Minute, fn); err != nil {
			t.Errorf("取数失败：%v", err)
		}
	}()
	<-started
	c.Invalidate("war")
	close(release)
	<-done

	if _, err := Fetch(c, ctx, "war", time.Minute, fn); err != nil {
		t.Fatalf("再次取值失败：%v", err)
	}
	if calls != 2 {
		t.Fatalf("in-flight 期间失效后应重新取数，期望 2 次，实际 %d 次", calls)
	}
}

// TestCacheWaiterSurvivesLeaderCancel 校验 leader 的 ctx 取消不会毒化同 key 的等待者。
func TestCacheWaiterSurvivesLeaderCancel(t *testing.T) {
	c := NewCache()
	var calls int32
	started := make(chan struct{})
	fn := func(ctx context.Context) (string, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			close(started)
			<-ctx.Done() // 模拟 leader 所在命令超时
			time.Sleep(50 * time.Millisecond)
			return "", ctx.Err()
		}
		return "ok", nil
	}
	type result struct {
		value string
		err   error
	}
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leader := make(chan result, 1)
	go func() {
		v, err := Fetch(c, leaderCtx, "war", time.Minute, fn)
		leader <- result{v, err}
	}()
	<-started

	waiter := make(chan result, 1)
	go func() {
		v, err := Fetch(c, context.Background(), "war", time.Minute, fn)
		waiter <- result{v, err}
	}()
	time.Sleep(20 * time.Millisecond) // 让等待者加入同一次取数
	cancelLeader()

	got := <-waiter
	if got.err != nil {
		t.Fatalf("等待者自身的 ctx 有效，应拿到成功结果，实际错误 %v", got.err)
	}
	if got.value != "ok" {
		t.Fatalf("等待者返回值错误：%s", got.value)
	}
	if lead := <-leader; !errors.Is(lead.err, context.Canceled) {
		t.Fatalf("leader 应因自身 ctx 取消而失败，实际 %v", lead.err)
	}
	if calls != 2 {
		t.Fatalf("期望 leader 与等待者各取数一次，实际 %d 次", calls)
	}
}

// TestCachePanicWaiterNotBlocked 校验 leader panic 时等待者不会永久阻塞，并拿到同一个错误。
func TestCachePanicWaiterNotBlocked(t *testing.T) {
	c := NewCache()
	var calls int32
	started := make(chan struct{})
	fn := func(ctx context.Context) (string, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			close(started)
			time.Sleep(50 * time.Millisecond) // 留出等待者加入同一次取数的时间
			panic("boom")
		}
		return "ok", nil
	}
	type result struct {
		value string
		err   error
	}
	leader := make(chan result, 1)
	go func() {
		v, err := Fetch(c, context.Background(), "war", time.Minute, fn)
		leader <- result{v, err}
	}()
	<-started

	waiter := make(chan result, 1)
	go func() {
		v, err := Fetch(c, context.Background(), "war", time.Minute, fn)
		waiter <- result{v, err}
	}()

	select {
	case got := <-waiter:
		// 等待者要么拿到 leader 的 panic 错误，要么因为调度太慢自己成了新 leader 而拿到 "ok"，
		// 两种都算正常；这里要验的是它不会被永久阻塞。
		if got.err != nil && !strings.Contains(got.err.Error(), "war") {
			t.Errorf("等待者拿到的错误应带 key 信息，实际 %v", got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("leader panic 之后等待者被永久阻塞（说明 inflight 没被清理）")
	}
	if lead := <-leader; lead.err == nil {
		t.Fatalf("leader 应返回错误，实际 %v", lead.err)
	}
	// 白盒断言 inflight 已被清空，这条不依赖任何时序假设。
	c.mu.Lock()
	inflight := len(c.inflight)
	c.mu.Unlock()
	if inflight != 0 {
		t.Fatalf("panic 之后 inflight 应为空，实际还有 %d 条", inflight)
	}
	if got, err := Fetch(c, context.Background(), "war", time.Minute, fn); err != nil || got != "ok" {
		t.Fatalf("panic 之后同一 key 应能重新取数：value=%q err=%v", got, err)
	}
}

// TestCacheTypeMismatch 校验同一 key 用不同类型取值时报错，且不会把该 key 永久污染。
func TestCacheTypeMismatch(t *testing.T) {
	c := NewCache()
	ctx := context.Background()
	if _, err := Fetch(c, ctx, "war", time.Minute, func(context.Context) (string, error) { return "v", nil }); err != nil {
		t.Fatalf("首次取值失败：%v", err)
	}

	_, err := Fetch(c, ctx, "war", time.Minute, func(context.Context) (int, error) { return 42, nil })
	if err == nil {
		t.Fatal("类型不匹配时应返回错误")
	}
	if !strings.Contains(err.Error(), "war") {
		t.Fatalf("错误信息应包含 key，实际 %v", err)
	}

	// 类型不匹配的条目已被删除，之后同一 key 用新类型仍能正常取数。
	got, err := Fetch(c, ctx, "war", time.Minute, func(context.Context) (int, error) { return 7, nil })
	if err != nil {
		t.Fatalf("类型不匹配之后同一 key 应能重新取数：%v", err)
	}
	if got != 7 {
		t.Fatalf("返回值错误：%d", got)
	}
}

// TestCacheZeroTTLNotCached 校验 ttl<=0 时不写缓存，每次调用都重新取数（Cache 文档承诺的行为）。
func TestCacheZeroTTLNotCached(t *testing.T) {
	c := NewCache()
	var calls int32
	fn := func(context.Context) (int, error) {
		atomic.AddInt32(&calls, 1)
		return 42, nil
	}
	for i := 0; i < 2; i++ {
		got, err := Fetch(c, context.Background(), "war", 0, fn)
		if err != nil || got != 42 {
			t.Fatalf("第 %d 次取值失败：value=%d err=%v", i+1, got, err)
		}
	}
	if calls != 2 {
		t.Errorf("ttl 为 0 时每次都应重新取数，实际取数 %d 次", calls)
	}
	if _, ok := c.FetchedAt("war"); ok {
		t.Error("ttl 为 0 时不应留下可用的缓存条目")
	}
	c.mu.Lock()
	entries := len(c.entries)
	c.mu.Unlock()
	if entries != 0 {
		t.Errorf("ttl 为 0 时不应写入缓存，实际留下了 %d 条记录", entries)
	}
}

// TestCacheWaiterContextCanceled 校验等待者自己的 ctx 取消后立即返回，且不影响 leader 写回缓存。
func TestCacheWaiterContextCanceled(t *testing.T) {
	c := NewCache()
	started := make(chan struct{})
	gate := make(chan struct{})
	leaderDone := make(chan struct{})
	var calls int32
	go func() {
		defer close(leaderDone)
		_, _ = Fetch(c, context.Background(), "war", time.Minute, func(context.Context) (int, error) {
			atomic.AddInt32(&calls, 1)
			close(started)
			<-gate
			return 7, nil
		})
	}()
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := Fetch(c, ctx, "war", time.Minute, func(context.Context) (int, error) { return 0, nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("等待者 ctx 超时应返回 DeadlineExceeded，实际 %v", err)
	}
	close(gate)
	<-leaderDone

	// 等待者提前退出不能影响 leader 的结果写回。
	got, err := Fetch(c, context.Background(), "war", time.Minute, func(context.Context) (int, error) { return 0, nil })
	if err != nil || got != 7 {
		t.Fatalf("leader 的结果应写入缓存：value=%d err=%v", got, err)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("应只取数一次，实际 %d 次", n)
	}
}

// TestCacheGoexitLeader 校验取数函数里发生 runtime.Goexit（典型场景：子 goroutine 调用 t.Fatal）
// 时，inflight 一定被清理、等待者不会永久阻塞。这是 panic 之外的第三条异常退出路径。
func TestCacheGoexitLeader(t *testing.T) {
	c := NewCache()
	leaderGone := make(chan struct{})
	go func() {
		defer close(leaderGone)
		_, _ = Fetch(c, context.Background(), "war", time.Minute, func(context.Context) (string, error) {
			runtime.Goexit()
			return "", nil
		})
	}()

	select {
	case <-leaderGone:
	case <-time.After(2 * time.Second):
		t.Fatal("取数函数 Goexit 之后调用者被永久阻塞（说明 inflight 没被清理）")
	}

	c.mu.Lock()
	inflight := len(c.inflight)
	c.mu.Unlock()
	if inflight != 0 {
		t.Fatalf("Goexit 之后 inflight 应为空，实际还有 %d 条", inflight)
	}
	if _, err := c.FetchedAt("war"); err {
		t.Error("Goexit 之后不应留下缓存条目")
	}
	if got, err := Fetch(c, context.Background(), "war", time.Minute, func(context.Context) (string, error) {
		return "ok", nil
	}); err != nil || got != "ok" {
		t.Fatalf("Goexit 之后同一 key 应能重新取数：value=%q err=%v", got, err)
	}
}

// TestCachePanicNil 校验 panic(nil) 同样按失败处理，不会把 nil 当成正常结果写进缓存。
func TestCachePanicNil(t *testing.T) {
	c := NewCache()
	_, err := Fetch(c, context.Background(), "war", time.Minute, func(context.Context) (string, error) {
		panic(nil)
	})
	if err == nil || !strings.Contains(err.Error(), "war") {
		t.Fatalf("panic(nil) 应返回带 key 的错误，实际 %v", err)
	}
	if _, ok := c.FetchedAt("war"); ok {
		t.Error("panic(nil) 之后不应留下缓存条目")
	}
	if got, err := Fetch(c, context.Background(), "war", time.Minute, func(context.Context) (string, error) {
		return "ok", nil
	}); err != nil || got != "ok" {
		t.Fatalf("panic(nil) 之后同一 key 应能重新取数：value=%q err=%v", got, err)
	}
}
