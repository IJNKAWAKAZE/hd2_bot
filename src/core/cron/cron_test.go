package cron

import (
	"bytes"
	"fmt"
	"log"
	"strings"
	"sync"
	"testing"
	"time"
)

// fixed 是用例里注入的固定时钟时间。
var fixed = time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

// quiet 是不产生输出的日志函数。
func quiet(string, ...any) {}

// recorder 记录日志行，用于断言心跳日志的内容。
type recorder struct {
	mu    sync.Mutex
	lines []string
}

// logf 实现日志函数签名。
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

// TestBeatUsesInjectedClockAndLogger 验证心跳使用注入的时钟与日志函数，便于离线验证。
func TestBeatUsesInjectedClockAndLogger(t *testing.T) {
	logs := &recorder{}
	r := New(Options{Now: func() time.Time { return fixed }, Logger: logs.logf})
	r.beat()

	want := "定时任务心跳：" + fixed.Format(time.RFC3339)
	if !strings.Contains(logs.joined(), want) {
		t.Fatalf("期望日志包含 %q，实际：%s", want, logs.joined())
	}
}

// TestStartRegistersFiveMinuteHeartbeat 验证 Start 注册了每 5 分钟整点触发的心跳任务。
func TestStartRegistersFiveMinuteHeartbeat(t *testing.T) {
	r := New(Options{Logger: quiet, Beat: func(time.Time) {}})
	if err := r.Start(); err != nil {
		t.Fatalf("启动定时任务失败：%v", err)
	}
	defer r.Stop()

	entries := r.jobs.Entries()
	if len(entries) != 1 {
		t.Fatalf("期望注册 1 个任务，实际 %d 个", len(entries))
	}
	next := entries[0].Next
	if next.Second() != 0 || next.Minute()%5 != 0 {
		t.Fatalf("心跳触发时间不是每 5 分钟整点：%s", next.Format(time.RFC3339))
	}
	// 下一次触发必须在未来 5 分钟内（含当前分钟整点）。
	if wait := time.Until(next); wait <= 0 || wait > 5*time.Minute+time.Second {
		t.Fatalf("下一次心跳时间不合理：等待 %s", wait)
	}
}

// TestHeartbeatFiresOnSchedule 验证任务真的会被调度执行，而不只是注册成功。
func TestHeartbeatFiresOnSchedule(t *testing.T) {
	beats := make(chan time.Time, 4)
	r := New(Options{Spec: "* * * * * *", Logger: quiet, Beat: func(at time.Time) { beats <- at }})
	if err := r.Start(); err != nil {
		t.Fatalf("启动定时任务失败：%v", err)
	}
	defer r.Stop()

	select {
	case at := <-beats:
		if at.IsZero() {
			t.Fatal("心跳时间不应为零值")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("3 秒内没有触发心跳任务")
	}
}

// TestStopIsIdempotentAndStopsBeats 验证停止后可重复调用，且停止后不再触发心跳。
func TestStopIsIdempotentAndStopsBeats(t *testing.T) {
	beats := make(chan struct{}, 16)
	r := New(Options{Spec: "* * * * * *", Logger: quiet, Beat: func(time.Time) { beats <- struct{}{} }})
	if err := r.Start(); err != nil {
		t.Fatalf("启动定时任务失败：%v", err)
	}
	r.Stop()
	// 排空停止前已排队的触发，确认之后不再有新触发。
	for len(beats) > 0 {
		<-beats
	}
	// 睡过下一个整秒边界（多留 150ms）：与用例开始的相位无关地跨越一次秒级 tick，
	// 否则「开始时刻刚好卡在整秒之后」会让等待窗口盖不住下一次触发，出现假阴性。
	waitSecondTick(150 * time.Millisecond)
	if n := len(beats); n != 0 {
		t.Fatalf("停止后仍然触发 %d 次", n)
	}
	r.Stop() // 幂等：重复调用不应 panic
}

// TestStopBeforeStart 验证未启动时停止是安全的（停机路径可能提前触发）。
func TestStopBeforeStart(t *testing.T) {
	r := New(Options{Logger: quiet})
	r.Stop()
	var zero *Runner
	zero.Stop()
}

// TestStartInvalidSpecReturnsError 验证表达式非法时返回说明原因的错误。
func TestStartInvalidSpecReturnsError(t *testing.T) {
	r := New(Options{Spec: "不是表达式", Logger: quiet})
	err := r.Start()
	if err == nil {
		t.Fatal("非法表达式应当返回错误")
	}
	if !strings.Contains(err.Error(), "定时任务") {
		t.Fatalf("错误信息应说明定时任务注册失败，实际：%v", err)
	}
}

// TestDefaultLoggerAndBeat 验证未注入日志函数时心跳写到标准日志，且默认心跳执行体可用。
func TestDefaultLoggerAndBeat(t *testing.T) {
	var buf bytes.Buffer
	oldWriter := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(oldWriter)

	r := New(Options{})
	r.beat()

	if !strings.Contains(buf.String(), "定时任务心跳") {
		t.Fatalf("默认心跳应当写入标准日志，实际：%s", buf.String())
	}
}

// TestStartLogsStarted 验证启动与停止各记录一行日志，便于确认调度状态。
func TestStartLogsStarted(t *testing.T) {
	logs := &recorder{}
	r := New(Options{Logger: logs.logf, Beat: func(time.Time) {}})
	if err := r.Start(); err != nil {
		t.Fatalf("启动定时任务失败：%v", err)
	}
	defer r.Stop()

	if !strings.Contains(logs.joined(), "定时任务已启动") {
		t.Fatalf("缺少启动日志，实际：%s", logs.joined())
	}
}

// TestStartRegistersExtraJobs 校验附加周期任务与心跳一起注册（心跳不算在内时条目数应等于任务数 + 1）。
func TestStartRegistersExtraJobs(t *testing.T) {
	r := New(Options{
		Logger: quiet,
		Beat:   func(time.Time) {},
		Jobs: []Job{
			{Name: "推送", Spec: "15 */5 * * * *", Run: func() {}},
			{Name: "清理", Spec: "0 0 4 * * *", Run: func() {}},
		},
	})
	if err := r.Start(); err != nil {
		t.Fatalf("启动失败：%v", err)
	}
	defer r.Stop()
	if got := len(r.jobs.Entries()); got != 3 {
		t.Fatalf("应注册 3 个任务（心跳 + 2），实际 %d", got)
	}
}

// TestExtraJobRunsOnSchedule 校验附加任务真的会被调度执行。
func TestExtraJobRunsOnSchedule(t *testing.T) {
	fired := make(chan struct{}, 4)
	r := New(Options{
		Logger: quiet,
		Beat:   func(time.Time) {},
		Jobs:   []Job{{Name: "推送", Spec: "* * * * * *", Run: func() { fired <- struct{}{} }}},
	})
	if err := r.Start(); err != nil {
		t.Fatalf("启动失败：%v", err)
	}
	defer r.Stop()
	select {
	case <-fired:
	case <-time.After(3 * time.Second):
		t.Fatal("3 秒内没有触发附加周期任务")
	}
}

// TestExtraJobInvalidSpecReturnsError 校验非法表达式在启动时就报错，并且带上任务名。
func TestExtraJobInvalidSpecReturnsError(t *testing.T) {
	r := New(Options{
		Logger: quiet,
		Beat:   func(time.Time) {},
		Jobs:   []Job{{Name: "推送", Spec: "不是表达式", Run: func() {}}},
	})
	err := r.Start()
	if err == nil {
		t.Fatal("非法表达式应报错")
	}
	if !strings.Contains(err.Error(), "推送") {
		t.Fatalf("错误信息应带任务名，实际：%v", err)
	}
}

// TestExtraJobWithoutRunReturnsError 校验漏写执行体的任务在启动时被拦下（否则会在夜里静默什么都不做）。
func TestExtraJobWithoutRunReturnsError(t *testing.T) {
	r := New(Options{Logger: quiet, Beat: func(time.Time) {}, Jobs: []Job{{Name: "推送", Spec: "15 */5 * * * *"}}})
	if err := r.Start(); err == nil {
		t.Fatal("缺少执行体应报错")
	}
}

// TestStopWaitsForInflightJob 校验 Stop 会等在途任务跑完：停机时不能把写到一半的推送切断。
func TestStopWaitsForInflightJob(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	r := New(Options{
		Logger: quiet,
		Beat:   func(time.Time) {},
		Jobs: []Job{{Name: "推送", Spec: "* * * * * *", Run: func() {
			close(started)
			time.Sleep(200 * time.Millisecond)
			close(finished)
		}}},
	})
	if err := r.Start(); err != nil {
		t.Fatalf("启动失败：%v", err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("任务没有启动")
	}
	r.Stop()
	select {
	case <-finished:
	default:
		t.Fatal("Stop 返回时在途任务还没跑完")
	}
}

// waitSecondTick 睡到下一个整秒边界再多留 extra，用于与用例开始的相位无关地跨越一次秒级 tick。
// 秒级表达式的任务都在整秒触发，先对齐边界再观察，才不会因为「开始时刻刚好在整秒之后」漏看一次触发。
func waitSecondTick(extra time.Duration) {
	time.Sleep(time.Until(time.Now().Truncate(time.Second).Add(time.Second)) + extra)
}

// TestStartFailureLeavesNothingRegistered 校验注册失败是零副作用：
// 失败后调度器里没有残留条目，等一拍确认心跳与附加任务都没有被启动。
func TestStartFailureLeavesNothingRegistered(t *testing.T) {
	beats := make(chan struct{}, 4)
	extra := make(chan struct{}, 4)
	r := New(Options{
		Spec:   "* * * * * *",
		Logger: quiet,
		Beat:   func(time.Time) { beats <- struct{}{} },
		Jobs:   []Job{{Name: "推送", Spec: "不是表达式", Run: func() { extra <- struct{}{} }}},
	})
	if err := r.Start(); err == nil {
		t.Fatal("非法表达式应报错")
	}
	if got := len(r.jobs.Entries()); got != 0 {
		t.Fatalf("启动失败后不应残留任何已注册任务，实际 %d 个", got)
	}
	// 心跳是每秒一次：真启动了的话，跨过下一个整秒边界必然已经触发。
	waitSecondTick(300 * time.Millisecond)
	if n := len(beats); n != 0 {
		t.Fatalf("启动失败后心跳不应触发，实际 %d 次", n)
	}
	if n := len(extra); n != 0 {
		t.Fatalf("启动失败后附加任务不应触发，实际 %d 次", n)
	}
	r.Stop() // 失败后调用 Stop 应当安全
}

// TestStartIsNotReentrant 校验重复调用 Start 被拒绝，且不会让任务被注册两遍、触发翻倍。
func TestStartIsNotReentrant(t *testing.T) {
	beats := make(chan struct{}, 8)
	fired := make(chan struct{}, 8)
	r := New(Options{
		Spec:   "* * * * * *",
		Logger: quiet,
		Beat:   func(time.Time) { beats <- struct{}{} },
		Jobs:   []Job{{Name: "推送", Spec: "* * * * * *", Run: func() { fired <- struct{}{} }}},
	})
	if err := r.Start(); err != nil {
		t.Fatalf("首次启动失败：%v", err)
	}
	defer r.Stop()
	if err := r.Start(); err == nil {
		t.Fatal("重复调用 Start 应返回错误")
	}
	if got := len(r.jobs.Entries()); got != 2 {
		t.Fatalf("重复调用 Start 后条目应仍为 2（心跳 + 1），实际 %d 个", got)
	}
	// 跨过一个整秒边界再观察一个完整的一秒窗口：注册两遍的话每个任务会各触发两次。
	waitSecondTick(200 * time.Millisecond)
	for len(beats) > 0 {
		<-beats
	}
	for len(fired) > 0 {
		<-fired
	}
	waitSecondTick(300 * time.Millisecond)
	if n := len(beats); n != 1 {
		t.Fatalf("一个 tick 内心跳应触发 1 次（没有被重复注册），实际 %d 次", n)
	}
	if n := len(fired); n != 1 {
		t.Fatalf("一个 tick 内附加任务应触发 1 次（没有被重复注册），实际 %d 次", n)
	}
}

// TestStartTrimsHeartbeatSpec 校验心跳表达式两端的空白被忽略（配置多打空格不应导致启动失败）。
func TestStartTrimsHeartbeatSpec(t *testing.T) {
	r := New(Options{Spec: "  * * * * * *  ", Logger: quiet, Beat: func(time.Time) {}})
	if err := r.Start(); err != nil {
		t.Fatalf("两端带空白的心跳表达式应能启动：%v", err)
	}
	defer r.Stop()
	if got := len(r.jobs.Entries()); got != 1 {
		t.Fatalf("应注册 1 个心跳任务，实际 %d 个", got)
	}
}

// TestExtraJobTrimsSpec 校验附加任务表达式两端的空白同样被忽略（与心跳一套规则）。
func TestExtraJobTrimsSpec(t *testing.T) {
	r := New(Options{
		Logger: quiet,
		Beat:   func(time.Time) {},
		Jobs:   []Job{{Name: "推送", Spec: "  15 */5 * * * *  ", Run: func() {}}},
	})
	if err := r.Start(); err != nil {
		t.Fatalf("两端带空白的附加表达式应能启动：%v", err)
	}
	defer r.Stop()
	if got := len(r.jobs.Entries()); got != 2 {
		t.Fatalf("应注册 2 个任务（心跳 + 1），实际 %d 个", got)
	}
}

// TestStartReportsAllInvalidJobs 校验多条任务同时非法时，错误里每条都能看到（不能只报第一条）。
func TestStartReportsAllInvalidJobs(t *testing.T) {
	r := New(Options{
		Logger: quiet,
		Beat:   func(time.Time) {},
		Jobs: []Job{
			{Name: "推送", Spec: "不是表达式", Run: func() {}},
			{Name: "清理", Spec: "也不是表达式", Run: func() {}},
		},
	})
	err := r.Start()
	if err == nil {
		t.Fatal("非法表达式应报错")
	}
	for _, name := range []string{"推送", "清理"} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("错误信息应同时包含 %q，实际：%v", name, err)
		}
	}
}

// TestExtraJobBlankSpecReturnsError 校验纯空白表达式被自己的中文文案拦下，
// 而不是退化成 robfig 的 "empty spec string"。
func TestExtraJobBlankSpecReturnsError(t *testing.T) {
	r := New(Options{
		Logger: quiet,
		Beat:   func(time.Time) {},
		Jobs:   []Job{{Name: "推送", Spec: "   ", Run: func() {}}},
	})
	err := r.Start()
	if err == nil {
		t.Fatal("纯空白表达式应报错")
	}
	if !strings.Contains(err.Error(), "缺少 cron 表达式") || !strings.Contains(err.Error(), "推送") {
		t.Fatalf("应报出自己的中文文案并带上任务名，实际：%v", err)
	}
	if strings.Contains(err.Error(), "empty spec string") {
		t.Fatalf("不应退化成 robfig 的英文报错，实际：%v", err)
	}
}

// TestStartBlankHeartbeatSpecFallsBackToDefault 校验心跳表达式只剩下空白时回退到默认心跳而不是启动失败。
// 这条用例才是 trim 的真正裁判：robfig 用 strings.Fields 切分，两端带空白的合法表达式本来就能解析，
// 只有「全是空白」这一种输入会在不 trim 时退化成 robfig 的 "expected exactly 6 fields, found 0"。
func TestStartBlankHeartbeatSpecFallsBackToDefault(t *testing.T) {
	r := New(Options{Spec: "   ", Logger: quiet, Beat: func(time.Time) {}})
	if err := r.Start(); err != nil {
		t.Fatalf("只有空白的心跳表达式应回退到默认心跳：%v", err)
	}
	defer r.Stop()
	entries := r.jobs.Entries()
	if len(entries) != 1 {
		t.Fatalf("应注册 1 个心跳任务，实际 %d 个", len(entries))
	}
	if next := entries[0].Next; next.Second() != 0 || next.Minute()%5 != 0 {
		t.Fatalf("回退后的心跳不是每 5 分钟整点：%s", next.Format(time.RFC3339))
	}
}

// TestStartLogsRegisteredJobNames 校验启动日志把注册成功的附加任务名逐条列出，便于事后核对哪条注册上了。
func TestStartLogsRegisteredJobNames(t *testing.T) {
	logs := &recorder{}
	r := New(Options{
		Logger: logs.logf,
		Beat:   func(time.Time) {},
		Jobs: []Job{
			{Name: "推送", Spec: "15 */5 * * * *", Run: func() {}},
			{Name: "清理", Spec: "0 0 4 * * *", Run: func() {}},
		},
	})
	if err := r.Start(); err != nil {
		t.Fatalf("启动失败：%v", err)
	}
	defer r.Stop()
	for _, name := range []string{"推送", "清理"} {
		if !strings.Contains(logs.joined(), name) {
			t.Fatalf("启动日志应包含已注册任务名 %q，实际：%s", name, logs.joined())
		}
	}
}
