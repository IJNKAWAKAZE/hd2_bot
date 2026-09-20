package hd2

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// newTestLimiter 返回一个使用假时钟的限流器；sleep 会直接推进假时钟。
func newTestLimiter(rate int, window, cooldown time.Duration) (*Limiter, *time.Time) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	l := NewLimiter(rate, window, cooldown)
	l.now = func() time.Time { return now }
	l.sleep = func(ctx context.Context, d time.Duration) error {
		now = now.Add(d)
		return nil
	}
	return l, &now
}

// TestLimiterAllowsRate 校验一个窗口内可连续取到 rate 个令牌。
func TestLimiterAllowsRate(t *testing.T) {
	l, _ := newTestLimiter(2, 10*time.Second, 20*time.Second)
	for i := 0; i < 2; i++ {
		if err := l.Wait(context.Background()); err != nil {
			t.Fatalf("第 %d 次取令牌不应失败：%v", i+1, err)
		}
	}
}

// TestLimiterWaitsNextWindow 校验超出上限后会等到下一个窗口。
func TestLimiterWaitsNextWindow(t *testing.T) {
	l, now := newTestLimiter(2, 10*time.Second, 20*time.Second)
	start := *now
	for i := 0; i < 3; i++ {
		if err := l.Wait(context.Background()); err != nil {
			t.Fatalf("第 %d 次取令牌失败：%v", i+1, err)
		}
	}
	// 等待量是一个窗口再加 1 纳秒，多出的纳秒保证闭区间内的放行次数也不超过 rate。
	if got := now.Sub(start); got != 10*time.Second+time.Nanosecond {
		t.Fatalf("第 3 次取令牌应等待一个窗口加 1ns，实际等待 %v", got)
	}
}

// TestLimiterBackgroundReserve 校验后台轮询给用户命令让出额度：rate=4、reserve=1 时
// 后台轮询最多取到 3 个令牌，剩下的那个始终留给命令——推送跑完时命令仍能立刻取到数据。
func TestLimiterBackgroundReserve(t *testing.T) {
	l, now := newTestLimiter(4, 10*time.Second, 20*time.Second)
	l.WithReserve(1)
	background := WithBackground(context.Background())
	start := *now
	for i := 0; i < 3; i++ {
		if err := l.Wait(background); err != nil {
			t.Fatalf("后台第 %d 次取令牌不应失败：%v", i+1, err)
		}
	}
	// 预留的那个额度给命令：立刻拿到，不用等窗口。
	if err := l.Wait(context.Background()); err != nil {
		t.Fatalf("用户命令应能用上预留额度：%v", err)
	}
	if got := now.Sub(start); got != 0 {
		t.Fatalf("预留额度应立即可用，实际等待 %v", got)
	}
	// 四个额度都用掉后，后台要等到最早那条记录滑出窗口（一个窗口加 1 纳秒）。
	if err := l.Wait(background); err != nil {
		t.Fatalf("后台取令牌失败：%v", err)
	}
	if got := now.Sub(start); got != 10*time.Second+time.Nanosecond {
		t.Fatalf("窗口额度用完后应等到下个窗口，实际等待 %v", got)
	}
}

// TestLimiterBackgroundKeepsOneSlot 校验 reserve 配得过大（≥ rate）时后台仍能取到令牌：
// 把定时轮询彻底饿死会让推送永远不动，比「慢一轮」更糟，所以后台额度至少留 1 个。
func TestLimiterBackgroundKeepsOneSlot(t *testing.T) {
	l, now := newTestLimiter(1, 10*time.Second, 20*time.Second)
	l.WithReserve(5)
	background := WithBackground(context.Background())
	start := *now
	if err := l.Wait(background); err != nil {
		t.Fatalf("后台至少应能取到一个令牌：%v", err)
	}
	if err := l.Wait(background); err != nil {
		t.Fatalf("后台取令牌失败：%v", err)
	}
	if got := now.Sub(start); got != 10*time.Second+time.Nanosecond {
		t.Fatalf("后台额度用完后应等到下个窗口，实际等待 %v", got)
	}
}

// TestLimiterReserveZeroKeepsFullRate 校验不预留（reserve=0，默认值）时后台与命令同额度：旧行为不变。
func TestLimiterReserveZeroKeepsFullRate(t *testing.T) {
	l, _ := newTestLimiter(2, 10*time.Second, 20*time.Second)
	background := WithBackground(context.Background())
	for i := 0; i < 2; i++ {
		if err := l.Wait(background); err != nil {
			t.Fatalf("不预留额度时后台第 %d 次取令牌不应失败：%v", i+1, err)
		}
	}
}

// TestIsBackground 校验后台标记随 ctx 传递，没有标记的一律按交互调用对待。
func TestIsBackground(t *testing.T) {
	if IsBackground(context.Background()) {
		t.Error("没有标记的 ctx 不应被判成后台轮询")
	}
	if !IsBackground(WithBackground(context.Background())) {
		t.Error("WithBackground 标记后的 ctx 应判成后台轮询")
	}
}

// TestLimiterCooldown 校验 429 后的全局冷却：没有截止时间的调用（定时轮询）会把冷却等过去，
// 期间不消耗额度，冷却结束后照常取到令牌。
func TestLimiterCooldown(t *testing.T) {
	l, now := newTestLimiter(5, 10*time.Second, 20*time.Second)
	l.NoteRateLimited(0)
	start := *now
	if err := l.Wait(context.Background()); err != nil {
		t.Fatalf("没有截止时间时应把冷却等过去，实际 %v", err)
	}
	if got := now.Sub(start); got != 20*time.Second {
		t.Fatalf("应等满一个冷却时长，实际等待 %v", got)
	}
	if err := l.Wait(context.Background()); err != nil {
		t.Fatalf("冷却结束后应能取到令牌：%v", err)
	}
}

// TestLimiterCooldownFailsFastWithoutBudget 校验预算不够时立刻报错，而不是睡到 ctx 超时：
// 用户命令的预算（plugutil.Timeout，20 秒）小于冷却时长时，群友应当马上看到「限流中」，
// 而不是等 20 秒只拿到一句「获取失败」。
func TestLimiterCooldownFailsFastWithoutBudget(t *testing.T) {
	l, _ := newTestLimiter(5, 10*time.Second, 20*time.Second)
	l.NoteRateLimited(0)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := l.Wait(ctx)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("预算不够时应返回 ErrRateLimited，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "冷却剩余") {
		t.Errorf("错误里应写清冷却还剩多久：%v", err)
	}
}

// TestLimiterCooldownWaitsWithBudget 校验预算够时会等过冷却：用户命令仍能拿到数据。
func TestLimiterCooldownWaitsWithBudget(t *testing.T) {
	l, now := newTestLimiter(5, 10*time.Second, 5*time.Second)
	l.NoteRateLimited(0)
	start := *now
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := l.Wait(ctx); err != nil {
		t.Fatalf("预算够时应等过冷却并取到令牌，实际 %v", err)
	}
	if got := now.Sub(start); got != 5*time.Second {
		t.Fatalf("应等满冷却时长，实际 %v", got)
	}
}

// TestLimiterWindowFailsFastWithoutBudget 校验窗口额度用完且等不起时也立刻报错——
// 窗口 60 秒而命令预算只有 20 秒时，睡到超时只会让错误变成看不出原因的 deadline exceeded。
func TestLimiterWindowFailsFastWithoutBudget(t *testing.T) {
	l, _ := newTestLimiter(1, 60*time.Second, 20*time.Second)
	if err := l.Wait(context.Background()); err != nil {
		t.Fatalf("第一次取令牌不应失败：%v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := l.Wait(ctx)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("等不起时应返回 ErrRateLimited，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "窗口额度已用完") {
		t.Errorf("错误里应说清是窗口额度用完了：%v", err)
	}
}

// TestLimiterRetryAfter 校验 Retry-After 更长时以它为准。
func TestLimiterRetryAfter(t *testing.T) {
	l, _ := newTestLimiter(5, 10*time.Second, 20*time.Second)
	l.NoteRateLimited(90 * time.Second)
	if got := l.Cooling(); got < 89*time.Second {
		t.Fatalf("冷却剩余应接近 90s，实际 %v", got)
	}
}

// TestLimiterContextCancel 校验等待令牌时 context 取消能立刻返回。
func TestLimiterContextCancel(t *testing.T) {
	l, _ := newTestLimiter(1, time.Hour, time.Second)
	l.sleep = func(ctx context.Context, d time.Duration) error { return ctx.Err() }
	if err := l.Wait(context.Background()); err != nil {
		t.Fatalf("第一次取令牌失败：%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("应返回 context.Canceled，实际 %v", err)
	}
}

// TestLimiterSlidingWindow 校验任意 window 长度的窗口内放行次数都不超过 rate（滑动窗口语义）。
func TestLimiterSlidingWindow(t *testing.T) {
	const (
		rate   = 4
		window = 10 * time.Second
	)
	l, now := newTestLimiter(rate, window, 20*time.Second)
	start := *now
	var releases []time.Duration
	record := func() {
		t.Helper()
		if err := l.Wait(context.Background()); err != nil {
			t.Fatalf("第 %d 次取令牌失败：%v", len(releases)+1, err)
		}
		releases = append(releases, now.Sub(start))
	}

	record() // 窗口开头先放 1 次
	*now = start.Add(9900 * time.Millisecond)
	record() // 窗口末尾再放 3 次，凑满 rate
	record()
	record()
	*now = start.Add(10001 * time.Millisecond)
	record() // 刚跨过固定窗口边界：滑动窗口下 0s 那条记录已滑出，这一次仍可放行
	before := *now
	record() // 第 6 次必须等最早的 9.9s 记录滑出窗口，固定窗口在这里会立刻放行
	if waited := now.Sub(before); waited < 9*time.Second {
		t.Fatalf("第 6 次取令牌应等最早的记录滑出窗口（约 9.9s），实际只等 %v", waited)
	}

	for i := 0; i+rate < len(releases); i++ {
		if span := releases[i+rate] - releases[i]; span < window {
			t.Fatalf("第 %d 次与第 %d 次放行相隔 %v，不足一个 window %v，说明存在超过 %d 次的窗口",
				i+1, i+rate+1, span, window, rate)
		}
	}
}

// TestLimiterCooldownNoBurst 校验冷却结束瞬间不会一次性放开整个窗口的额度。
func TestLimiterCooldownNoBurst(t *testing.T) {
	const rate = 4
	l, now := newTestLimiter(rate, 10*time.Second, 20*time.Second)
	l.NoteRateLimited(0)
	*now = now.Add(20 * time.Second) // 冷却结束

	immediate := 0
	for i := 0; i < rate; i++ {
		before := *now
		if err := l.Wait(context.Background()); err != nil {
			t.Fatalf("第 %d 次取令牌失败：%v", i+1, err)
		}
		if now.Equal(before) {
			immediate++
		}
	}
	if immediate != rate-1 {
		t.Fatalf("冷却结束瞬间应立刻放行 rate-1=%d 次，实际 %d 次", rate-1, immediate)
	}
}

// TestLimiterRealSleep 校验真实 sleepContext 路径（不使用假时钟）：等待到期、ctx 取消立即返回。
func TestLimiterRealSleep(t *testing.T) {
	l := NewLimiter(1, 20*time.Millisecond, time.Second)
	ctx := context.Background()
	if err := l.Wait(ctx); err != nil {
		t.Fatalf("第一次取令牌失败：%v", err)
	}
	start := time.Now()
	if err := l.Wait(ctx); err != nil {
		t.Fatalf("第二次取令牌失败：%v", err)
	}
	if elapsed := time.Since(start); elapsed < 15*time.Millisecond {
		t.Fatalf("第二次取令牌应真实等待约一个窗口 20ms，实际 %v", elapsed)
	}
	// ctx 取消用一个长窗口的限流器验证：20ms 窗口下这次取令牌可能因为窗口已经滑落而直接
	// 放行，断言就依赖真实时钟快慢（慢机器上会偶发失败），长窗口才能稳定走到等待分支。
	long := NewLimiter(1, time.Hour, time.Second)
	if err := long.Wait(ctx); err != nil {
		t.Fatalf("长窗口第一次取令牌失败：%v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	start = time.Now()
	if err := long.Wait(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("ctx 取消后应返回 context.Canceled，实际 %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("ctx 取消后应立即返回，实际耗时 %v", elapsed)
	}
}

// TestLimiterPruneBoundary 校验窗口边界的剔除精度。
// 「恰好相隔一个 window」的记录必须算过期，否则闭区间内会多放行一次（等价于 rate+1）。
func TestLimiterPruneBoundary(t *testing.T) {
	const window = 10 * time.Second
	base := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		now  time.Time
		kept int
	}{
		{"窗口内 1 毫秒", base.Add(window - time.Millisecond), 1},
		{"恰恰相隔一个窗口（仍占额度，保证闭区间不超限）", base.Add(window), 1},
		{"超出窗口 1 纳秒", base.Add(window + time.Nanosecond), 0},
		{"超出窗口 1 毫秒", base.Add(window + time.Millisecond), 0},
	}
	for _, c := range cases {
		l := NewLimiter(4, window, time.Minute)
		l.times = []time.Time{base}
		l.pruneLocked(c.now)
		if len(l.times) != c.kept {
			t.Errorf("%s：期望保留 %d 条记录，实际 %d 条", c.name, c.kept, len(l.times))
		}
	}
}

// TestLimiterDefaults 校验限流器的兜底参数与未冷却时的 Cooling。
func TestLimiterDefaults(t *testing.T) {
	l := NewLimiter(0, 0, 0)
	if l.rate != 1 || l.window != time.Second || l.cooldown != 20*time.Second {
		t.Errorf("非法参数应回落到默认值：rate=%d window=%v cooldown=%v", l.rate, l.window, l.cooldown)
	}
	if l.now == nil || l.sleep == nil {
		t.Error("时钟与等待函数应有默认实现")
	}
	if got := l.Cooling(); got != 0 {
		t.Errorf("未冷却时 Cooling 应为 0，实际 %v", got)
	}
	l.NoteRateLimited(0)
	if got := l.Cooling(); got < 19*time.Second || got > 20*time.Second {
		t.Errorf("NoteRateLimited(0) 应使用默认冷却 20s，实际 %v", got)
	}
}

// TestLimiterClosedWindowInvariant 校验「任意闭区间 [t, t+window] 内放行不超过 rate 次」。
// 关键点是恰好落在 now-window 的那条记录仍然占额度，否则边界上会多放行一次。
func TestLimiterClosedWindowInvariant(t *testing.T) {
	l, now := newTestLimiter(1, 10*time.Second, 20*time.Second)
	start := *now
	if err := l.Wait(context.Background()); err != nil {
		t.Fatalf("第一次取令牌失败：%v", err)
	}
	*now = start.Add(10 * time.Second) // 与上次放行恰好相隔一个窗口
	before := *now
	if err := l.Wait(context.Background()); err != nil {
		t.Fatalf("第二次取令牌失败：%v", err)
	}
	if waited := now.Sub(before); waited < time.Nanosecond {
		t.Fatalf("闭区间 [t, t+window] 内不应再放行，实际等待 %v", waited)
	}
}
