// Package cron 负责注册定时任务。S1 只有心跳任务，S4 的推送轮询会追加在这里。
package cron

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	robfigcron "github.com/robfig/cron/v3"
)

// heartbeatSpec 是心跳任务的默认表达式（六段式，秒级）：每 5 分钟整点触发一次。
// 心跳只写一行日志，用于确认调度器与时钟正常，不做任何网络请求。
const heartbeatSpec = "0 */5 * * * *"

// Job 是心跳之外的周期任务：Name 用于日志与报错，Spec 是六段式表达式，Run 是执行体。
// 执行体刻意不返回错误：调度器没有处理错误的通道，失败由执行体自己记日志（否则 err 只能被丢掉）。
// Run 的两条契约（后续任务注册推送轮询时必须遵守，本包不做强制检查）：
//  1. Run 必须自带超时或可取消：Stop 会无界等待在途执行体，忽略超时的执行体会拖住整个停机序列；
//  2. Run 不得自我重叠：robfig 每个 tick 新开一个 goroutine，执行时长必须小于调度周期，
//     否则同一条任务会并发跑多份（本包不提供 SkipIfStillRunning 之类的串行化保证）。
type Job struct {
	Name string
	Spec string
	Run  func()
}

// Options 是调度器的构造参数；时钟、日志与心跳执行体都可注入，便于离线测试。
type Options struct {
	// Spec 是心跳任务的 cron 表达式；为空时使用 heartbeatSpec。
	Spec string
	// Beat 是心跳执行体，参数为本次心跳时间；为空时写一行日志。
	Beat func(at time.Time)
	// Jobs 是心跳之外的周期任务（S3 起推送轮询走这里）。
	Jobs []Job
	// Now 提供心跳时间；为空时使用系统时钟。
	Now func() time.Time
	// Logger 是日志函数；为空时使用标准日志。
	Logger func(format string, args ...any)
}

// Runner 是定时任务调度器；请用 New 创建，Start 只调用一次，Stop 可重复调用。
type Runner struct {
	jobs      *robfigcron.Cron
	spec      string
	beatFn    func(at time.Time)
	extraJobs []Job
	started   bool
	now       func() time.Time
	logf      func(format string, args ...any)
}

// newScheduler 创建使用秒级表达式（六段式：秒 分 时 日 月 周）的 robfig 调度器。
// Start 每次都会新建一个：注册失败时直接丢弃，调度器里不会留下半启动的条目。
func newScheduler() *robfigcron.Cron { return robfigcron.New(robfigcron.WithSeconds()) }

// New 创建调度器，使用秒级表达式（六段式：秒 分 时 日 月 周）。
func New(opts Options) *Runner {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	logf := opts.Logger
	if logf == nil {
		logf = log.Printf
	}
	r := &Runner{
		jobs:      newScheduler(),
		spec:      opts.Spec,
		extraJobs: opts.Jobs,
		now:       now,
		logf:      logf,
	}
	r.beatFn = opts.Beat
	if r.beatFn == nil {
		r.beatFn = r.logBeat
	}
	return r
}

// Start 注册心跳与附加周期任务并启动调度器。
// 注册只有两种结果：全部成功并启动，或整体失败且不留痕迹（先注册到临时调度器，成功后才接管）；
// 失败信息用 errors.Join 汇总，多条任务同时写错时每个问题都能在错误里看到。
// 表达式两端的空白会被忽略（心跳与附加任务同一套规则）；Start 只能调用一次，
// 重复调用返回错误，避免心跳与附加任务被注册两遍（停机后要重新启动请新建实例）。
func (r *Runner) Start() error {
	if r == nil || r.jobs == nil {
		return errors.New("定时任务未初始化，请先用 New 创建调度器")
	}
	if r.started {
		return errors.New("定时任务已启动，Start 只能调用一次；如需重新启动请新建调度器")
	}
	spec := strings.TrimSpace(r.spec)
	if spec == "" {
		spec = heartbeatSpec
	}

	jobs := newScheduler()
	var errs []error
	if _, err := jobs.AddFunc(spec, r.fire); err != nil {
		errs = append(errs, fmt.Errorf("注册定时任务失败（表达式 %q）：%w", spec, err))
	}
	names := make([]string, 0, len(r.extraJobs))
	for _, job := range r.extraJobs {
		jobSpec := strings.TrimSpace(job.Spec)
		if jobSpec == "" {
			errs = append(errs, fmt.Errorf("周期任务 %q 缺少 cron 表达式", job.Name))
			continue
		}
		if job.Run == nil {
			errs = append(errs, fmt.Errorf("周期任务 %q 缺少执行体", job.Name))
			continue
		}
		if _, err := jobs.AddFunc(jobSpec, job.Run); err != nil {
			errs = append(errs, fmt.Errorf("注册周期任务 %q（表达式 %q）失败：%w", job.Name, jobSpec, err))
			continue
		}
		names = append(names, job.Name)
	}
	if joined := errors.Join(errs...); joined != nil {
		// 有任务注册失败就整体不启动，并且不接管临时调度器：
		// 半启动的调度器会让「哪些任务在跑」变得没人说得清，事后也无法从日志里复原。
		return joined
	}

	r.jobs = jobs
	r.started = true
	r.jobs.Start()
	extra := "无"
	if len(names) > 0 {
		extra = strings.Join(names, "、")
	}
	r.logf("定时任务已启动：心跳表达式 %q，附加周期任务 %d 个（%s）", spec, len(names), extra)
	return nil
}

// Stop 停止调度器并等待正在执行的任务结束，因此停机序列里的「停止定时任务」已经覆盖了
// 正在执行的推送轮询，不会把写到一半的一轮切成两半。
// 注意 robfig/cron 的 Stop 只停止调度、并返回一个「在途任务跑完才取消」的 context，
// 真正等待要由这里完成；执行体卡死不返回时停机也会一直等下去（契约见 Job.Run 的说明）。
// 可重复调用（停止后再启动需新建实例）。
func (r *Runner) Stop() {
	if r == nil || r.jobs == nil {
		return
	}
	<-r.jobs.Stop().Done()
	r.logf("定时任务已停止")
}

// fire 是调度器实际调用的入口，把时间与执行体解耦，便于测试直接触发一次心跳。
func (r *Runner) fire() { r.beatFn(r.now()) }

// beat 直接执行一次心跳，便于测试在不启动调度器的情况下验证心跳行为。
func (r *Runner) beat() { r.fire() }

// logBeat 是默认心跳：只写一行日志，说明调度器按时钟正常触发。
func (r *Runner) logBeat(at time.Time) {
	r.logf("定时任务心跳：%s", at.Format(time.RFC3339))
}
