package hd2

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Limiter 是全局请求节流器：滑动窗口 + 429 全局冷却。
//
// 滑动窗口记录最近放行的时间戳，保证任意闭区间 [t, t+window] 内放行不超过 rate 次：
// 只有严格早于 now-window 的记录才算过期（恰好落在 now-window 的仍占额度），
// 且等待量额外加 1 纳秒，避免「正好相隔一个窗口」时又立刻放行一整批。
// 上游没有公开窗口长度，这里一律取保守一侧。
// 相比固定窗口，不会在窗口边界两侧各放满一次、被上游算成成倍请求。
// 所有上游请求（命令触发与定时轮询）共用同一个实例，避免打爆上游 5 请求/窗口的限制。
//
// 两类调用共用同一个额度会让定时轮询把窗口占掉一大半：默认配置下一轮推送要连发 3 个请求，
// 而 60 秒窗口总共只有 4 个额度——推送刚跑完时用户发一条 /planet（最多要 4 个）就会撞上限流，
// 群友看到的是「上游接口限流中」，但原因其实是机器人自己把额度用完了。
// 因此限流器把请求分成两类（见 WithBackground / reserve）：
//   - 用户命令（交互）：可以用满 rate 个额度；
//   - 后台轮询：最多用 rate-reserve 个，剩下的额度始终留给命令。
type Limiter struct {
	rate     int           // 每个窗口允许的请求数
	reserve  int           // 只留给交互命令的额度（后台轮询不可动用）
	window   time.Duration // 窗口长度
	cooldown time.Duration // 429 后的默认冷却时长

	now   func() time.Time
	sleep func(context.Context, time.Duration) error

	mu        sync.Mutex
	times     []time.Time // 最近放行的请求时间戳，升序，长度不超过 rate
	coolUntil time.Time   // 429 全局冷却的截止时间
}

// NewLimiter 创建限流器；rate 为每 window 允许的请求数，cooldown 为 429 后的默认冷却时长。
func NewLimiter(rate int, window, cooldown time.Duration) *Limiter {
	if rate <= 0 {
		rate = 1
	}
	if window <= 0 {
		window = time.Second
	}
	if cooldown <= 0 {
		cooldown = 20 * time.Second
	}
	return &Limiter{rate: rate, window: window, cooldown: cooldown, now: time.Now, sleep: sleepContext}
}

// WithReserve 设置留给交互命令的窗口额度（默认 0 表示不预留），返回自身以便链式调用。
//
// reserve 大于等于 rate 时后台轮询仍能拿到 1 个额度：把定时任务彻底饿死会让推送永远不动，
// 那是比「慢一轮」更糟的结果（见 limitFor）。
func (l *Limiter) WithReserve(reserve int) *Limiter {
	if reserve < 0 {
		reserve = 0
	}
	l.mu.Lock()
	l.reserve = reserve
	l.mu.Unlock()
	return l
}

// backgroundKey 是「这次取数来自后台轮询」的 ctx 标记的键类型（用私有类型避免与其它包的键撞车）。
type backgroundKey struct{}

// WithBackground 标记这次取数来自后台轮询（定时推送），不是用户命令。
// 带这个标记的请求要限流时会让出 reserve 个额度（见 Limiter.reserve）；
// 标记随 ctx 一路传到 http 客户端的限流调用处，调用方不必改签名。
func WithBackground(ctx context.Context) context.Context {
	return context.WithValue(ctx, backgroundKey{}, true)
}

// IsBackground 判断这次取数是否来自后台轮询。
func IsBackground(ctx context.Context) bool {
	background, _ := ctx.Value(backgroundKey{}).(bool)
	return background
}

// Wait 取得一次请求额度：窗口内已放行 rate 次时，等到最早一次放行滑出窗口再重试。
//
// 冷却期内（429 之后）不消耗额度：等得起就等过去，等不起就直接返回包装了 ErrRateLimited 的错误。
// 「等得起」的判定见 waitFits——定时轮询（没有截止时间）照旧等，用户命令（有截止时间）只在预算内等。
// 这样命令要么真拿到数据，要么立刻收到「限流中」的明确原因，不会一直睡到 ctx 超时后
// 只剩一句含糊的 deadline exceeded。
func (l *Limiter) Wait(ctx context.Context) error {
	for {
		l.mu.Lock()
		now := l.now()
		if left := l.coolUntil.Sub(now); left > 0 {
			l.mu.Unlock()
			if !waitFits(ctx, left) {
				return fmt.Errorf("%w（冷却剩余 %s）", ErrRateLimited, left.Round(time.Millisecond))
			}
			if err := l.sleep(ctx, left); err != nil {
				return err
			}
			continue
		}
		l.pruneLocked(now)
		limit := l.limitForLocked(ctx)
		if len(l.times) < limit {
			l.times = append(l.times, now)
			l.mu.Unlock()
			return nil
		}
		// 记录均未滑出窗口时，等到「第 len(times)-limit 条」滑出窗口再多 1 纳秒：多出的纳秒让
		// 「正好相隔一个 window」的两次放行落在不同窗口内，把闭区间不变式补严；pruneLocked 保证
		// wait 为正，不会空转。交互调用（limit == rate）时它退化成原来的 times[0]，口径不变。
		wait := l.times[len(l.times)-limit].Add(l.window).Sub(now) + time.Nanosecond
		l.mu.Unlock()
		// 额度要等到 wait 之后才有：预算不够时同样立刻报「限流中」，
		// 而不是睡到 ctx 超时（那时错误里看不出是限流，群友只会看到一句「获取失败」）。
		if !waitFits(ctx, wait) {
			return fmt.Errorf("%w（窗口额度已用完，还需等 %s）", ErrRateLimited, wait.Round(time.Millisecond))
		}
		if err := l.sleep(ctx, wait); err != nil {
			return err
		}
	}
}

// limitForLocked 返回这次调用可用的窗口额度：后台轮询让出 reserve 个额度给用户命令，
// 其余调用（用户命令、启动期查询等）用满 rate。
//
// 后台额度最少给 1：reserve >= rate（配错了）时定时推送仍能走，只是慢一点，
// 不会因为「一直拿不到额度」而彻底停摆。调用方需持有 l.mu（reserve 可被 WithReserve 改）。
func (l *Limiter) limitForLocked(ctx context.Context) int {
	if !IsBackground(ctx) || l.reserve <= 0 {
		return l.rate
	}
	if limit := l.rate - l.reserve; limit > 1 {
		return limit
	}
	return 1
}

// waitMargin 是「等完之后还要真发一次请求」的余量：只剩一点点预算时宁可直接快速失败，
// 也不要等完才发现请求已经没时间发了。
const waitMargin = 3 * time.Second

// waitFits 判断这次等待值不值得：
//   - 没有截止时间的调用（定时轮询、启动预热）照旧等——它们本来就没有「必须多久返回」的约束；
//   - 有截止时间的调用（用户命令，预算见 plugutil.Timeout）只在「等待 + 余量」仍落在预算内时等，
//     否则立刻返回 ErrRateLimited，把「上游限流中」这个原因原样告诉调用方。
//
// 判定用的是 ctx 截止时间（真实时钟），与限流器里可注入的 now 无关：预算说的是调用方的真实等待。
func waitFits(ctx context.Context, wait time.Duration) bool {
	deadline, ok := ctx.Deadline()
	if !ok {
		return true
	}
	return wait+waitMargin <= time.Until(deadline)
}

// NoteRateLimited 记录一次 429：进入全局冷却，并把窗口记录重置成一条位于冷却截止时刻的
// 合成记录。取舍是：冷却期间没人消耗额度，重置后冷却结束瞬间最多只放行 rate-1 次，
// 不会一次性打满整个窗口再次触发上游限流；代价是冷却后的恢复速度略慢于满额。
// 比默认冷却更短的 Retry-After 不会缩短冷却，即冷却时长取 max(retryAfter, cooldown)。
func (l *Limiter) NoteRateLimited(retryAfter time.Duration) {
	if retryAfter < l.cooldown {
		retryAfter = l.cooldown
	}
	l.mu.Lock()
	if until := l.now().Add(retryAfter); until.After(l.coolUntil) {
		l.coolUntil = until
		l.times = []time.Time{until}
	}
	l.mu.Unlock()
}

// Cooling 返回冷却剩余时长；0 表示不在冷却中。
func (l *Limiter) Cooling() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if left := l.coolUntil.Sub(l.now()); left > 0 {
		return left
	}
	return 0
}

// pruneLocked 剔除已经滑出窗口的记录：times 升序，过期的都在前面。
// 判定用 Before（严格早于 now-window 才算过期），恰好落在 now-window 的记录仍占额度，
// 这样闭区间 [t, t+window] 内的放行次数也不会超过 rate。
// 调用方需持有 l.mu；结束后 times 里全是窗口内的记录，可直接用于判断额度。
func (l *Limiter) pruneLocked(now time.Time) {
	cutoff := now.Add(-l.window)
	drop := 0
	for drop < len(l.times) && l.times[drop].Before(cutoff) {
		drop++
	}
	if drop == 0 {
		return
	}
	n := copy(l.times, l.times[drop:])
	l.times = l.times[:n]
}

// sleepContext 等待指定时长，ctx 取消时提前返回。
func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
