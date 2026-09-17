// engine_test.go 覆盖渲染引擎的关键行为：模板执行、串行渲染、超时、断线自愈与幂等关闭。
// 起真浏览器的用例由环境变量 HD2_RENDER_SMOKE=1 控制，默认跳过，
// 这样没装 Chromium 的开发机也能跑全绿。
package render

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/mxschmitt/playwright-go"
)

// pngMagic 是 PNG 文件头，冒烟用例用它断言输出格式。
var pngMagic = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}

// fakeImage 是假驱动默认返回的「图片」：带 PNG 魔数，方便和真图区分。
var fakeImage = append(append([]byte{}, pngMagic...), 0x01, 0x02, 0x03, 0x04)

// fakeDriver 是 pageDriver 的假实现：按脚本返回截图结果，并记录收到的 HTML。
// 内部状态用互斥锁保护，因为串行渲染的用例会从多个 goroutine 调用它。
type fakeDriver struct {
	mu sync.Mutex

	// screenshotFn 非 nil 时用它产出截图结果，为 nil 时返回 fakeImage。
	screenshotFn func(ctx context.Context, html string) ([]byte, error)
	// connected 控制 isConnected 的返回值，用来模拟浏览器掉线。
	connected bool
	// closeErr 非 nil 时 close 返回该错误，用来覆盖「关闭失败」分支。
	closeErr error
	// panicWhenClosed 为真时，close 之后再调用 screenshot / isConnected 直接 panic。
	// 真驱动被关掉后页面已置 nil，再用就是空指针崩溃（进程级故障），
	// 用例靠它把「关闭后还在用旧驱动」变成可读的测试失败。
	panicWhenClosed bool
	// closed 记录是否已关闭，配合 panicWhenClosed 使用。
	closed bool

	htmls  []string
	shots  int
	closes int
}

// newFakeDriver 造一个「连着浏览器」的假驱动。
func newFakeDriver() *fakeDriver {
	return &fakeDriver{connected: true}
}

func (d *fakeDriver) screenshot(ctx context.Context, html string) ([]byte, error) {
	d.mu.Lock()
	if d.closed && d.panicWhenClosed {
		d.mu.Unlock()
		panic("假驱动已被关闭，却仍被用来截图")
	}
	d.shots++
	d.htmls = append(d.htmls, html)
	fn := d.screenshotFn
	d.mu.Unlock()
	if fn != nil {
		return fn(ctx, html)
	}
	return fakeImage, nil
}

func (d *fakeDriver) isConnected() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.connected
}

func (d *fakeDriver) close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closes++
	d.closed = true
	// 关闭之后浏览器不可能还连着，顺手置为断线，省得用例再手动设置。
	d.connected = false
	return d.closeErr
}

func (d *fakeDriver) setConnected(v bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.connected = v
}

func (d *fakeDriver) lastHTML() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.htmls) == 0 {
		return ""
	}
	return d.htmls[len(d.htmls)-1]
}

func (d *fakeDriver) shotCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.shots
}

func (d *fakeDriver) closeCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.closes
}

// factorySpy 是替换包级 newDriver 的假工厂，按顺序发放在用例里准备好的驱动，
// 并记录每次构造收到的 DriverConfig。
type factorySpy struct {
	mu      sync.Mutex
	drivers []pageDriver
	configs []DriverConfig
	// failErr 非 nil 时构造直接失败，用来覆盖「启动浏览器失败」分支。
	failErr error
}

func (s *factorySpy) factory(cfg DriverConfig) (pageDriver, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.configs = append(s.configs, cfg)
	if s.failErr != nil {
		return nil, s.failErr
	}
	if len(s.drivers) == 0 {
		return nil, errors.New("假工厂：没有更多准备好的驱动了")
	}
	drv := s.drivers[0]
	s.drivers = s.drivers[1:]
	return drv, nil
}

func (s *factorySpy) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.configs)
}

func (s *factorySpy) firstConfig() DriverConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.configs) == 0 {
		return DriverConfig{}
	}
	return s.configs[0]
}

// installFakeFactory 把包级 newDriver 换成假工厂，用例结束自动恢复，
// 避免污染同包其它用例（包级变量是这里唯一的测试注入点）。
func installFakeFactory(t *testing.T, drivers ...pageDriver) *factorySpy {
	t.Helper()
	spy := &factorySpy{drivers: drivers}
	prev := newDriver
	newDriver = spy.factory
	t.Cleanup(func() { newDriver = prev })
	return spy
}

// mustNewEngine 造一个引擎，并在用例结束时关闭它。
func mustNewEngine(t *testing.T, cfg Config) *Engine {
	t.Helper()
	eng, err := New(cfg)
	if err != nil {
		t.Fatalf("New(%+v) 应成功，实际 %v", cfg, err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	return eng
}

// newSmokeCard 构造一张最小卡片，用来把「模板 → HTML → 驱动」整条链路走通。
func newSmokeCard() Card {
	return Card{Name: "smoke", Data: SmokeCard{
		Meta: Meta{
			Title:    "冒烟卡片",
			Subtitle: "渲染链路自检",
			DataTime: "2026-09-16 12:00",
			Emblem:   "emblem.super_earth",
		},
		Lines: []string{"第一行", "<b>转义检查</b>"},
	}}
}

// smokeCardWithStale 返回一份改了过期标记的冒烟卡片。
func smokeCardWithStale(stale bool) Card {
	card := newSmokeCard()
	view := card.Data.(SmokeCard)
	view.Stale = stale
	card.Data = view
	return card
}

// TestRenderUsesInjectedDriver 校验渲染走注入的 driver，并把拼好的 HTML 交给它。
func TestRenderUsesInjectedDriver(t *testing.T) {
	drv := newFakeDriver()
	drv.screenshotFn = func(context.Context, string) ([]byte, error) { return []byte("假图片"), nil }
	installFakeFactory(t, drv)

	eng := mustNewEngine(t, Config{})
	img, err := eng.Render(context.Background(), newSmokeCard())
	if err != nil {
		t.Fatalf("Render 应成功，实际 %v", err)
	}
	if string(img) != "假图片" {
		t.Fatalf("应原样返回驱动产出的字节，实际 %q", img)
	}

	html := drv.lastHTML()
	for _, want := range []string{
		"<!DOCTYPE html>",         // 外壳
		"冒烟卡片",                    // 标题（来自 Meta）
		"渲染链路自检",                  // 副标题
		"2026-09-16 12:00",        // 数据时间
		`class="card__emblem"`,    // 徽标素材（游戏原生 SVG）内联进卡片并挂上 class
		"第一行",                     // 卡片 body
		"&lt;b&gt;转义检查&lt;/b&gt;", // 视图模型里的 HTML 必须被转义
	} {
		if !strings.Contains(html, want) {
			t.Errorf("HTML 应包含 %q，实际内容：\n%s", want, html)
		}
	}
	if strings.Contains(html, "#ZgotmplZ") {
		t.Errorf("素材应内联成 data URI，不能被 html/template 拦成 #ZgotmplZ：\n%s", html)
	}
}

// TestRenderShowsStaleBadge 校验过期角标只由 Meta.Stale 决定。
func TestRenderShowsStaleBadge(t *testing.T) {
	drv := newFakeDriver()
	installFakeFactory(t, drv)
	eng := mustNewEngine(t, Config{})

	if _, err := eng.Render(context.Background(), smokeCardWithStale(false)); err != nil {
		t.Fatalf("Render 应成功，实际 %v", err)
	}
	if strings.Contains(drv.lastHTML(), "数据可能已过期") {
		t.Error("Stale 为假时不应出现过期角标")
	}
	if _, err := eng.Render(context.Background(), smokeCardWithStale(true)); err != nil {
		t.Fatalf("Render 应成功，实际 %v", err)
	}
	if !strings.Contains(drv.lastHTML(), "数据可能已过期") {
		t.Error("Stale 为真时应出现过期角标")
	}
}

// TestRenderUnknownCardFails 校验未知卡片名返回带中文上下文的错误。
func TestRenderUnknownCardFails(t *testing.T) {
	installFakeFactory(t, newFakeDriver())
	eng := mustNewEngine(t, Config{})

	_, err := eng.Render(context.Background(), Card{Name: "不存在的卡片", Data: SmokeCard{}})
	if err == nil {
		t.Fatal("未知卡片应该报错")
	}
	if !strings.Contains(err.Error(), "未知卡片") {
		t.Fatalf("错误应说明「未知卡片」，实际 %v", err)
	}
}

// TestRenderRequiresMetaInViewModel 校验视图模型没内嵌 Meta 时给出明确的中文错误，
// 而不是抛出难以理解的模板执行错误。
func TestRenderRequiresMetaInViewModel(t *testing.T) {
	installFakeFactory(t, newFakeDriver())
	eng := mustNewEngine(t, Config{})

	_, err := eng.Render(context.Background(), Card{Name: "smoke", Data: struct{ Lines []string }{}})
	if err == nil {
		t.Fatal("视图模型缺少 render.Meta 时应该报错")
	}
	if !strings.Contains(err.Error(), "render.Meta") {
		t.Fatalf("错误应提示内嵌 render.Meta，实际 %v", err)
	}
}

// TestRenderSerializesConcurrentCalls 校验并发渲染被串行化：同一时刻只有一个截图在跑。
func TestRenderSerializesConcurrentCalls(t *testing.T) {
	drv := newFakeDriver()
	var running, maxRunning int32
	drv.screenshotFn = func(context.Context, string) ([]byte, error) {
		cur := atomic.AddInt32(&running, 1)
		for {
			old := atomic.LoadInt32(&maxRunning)
			if cur <= old || atomic.CompareAndSwapInt32(&maxRunning, old, cur) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&running, -1)
		return fakeImage, nil
	}
	installFakeFactory(t, drv)
	eng := mustNewEngine(t, Config{})

	const callers = 8
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			card := smokeCardWithStale(n%2 == 0)
			if _, err := eng.Render(context.Background(), card); err != nil {
				t.Errorf("并发渲染第 %d 个调用出错：%v", n, err)
			}
		}(i)
	}
	wg.Wait()

	if got := atomic.LoadInt32(&maxRunning); got != 1 {
		t.Fatalf("渲染必须串行，最大并发应为 1，实际 %d", got)
	}
	if got := drv.shotCount(); got != callers {
		t.Fatalf("每个调用都应渲染一次，期望 %d 次截图，实际 %d", callers, got)
	}
}

// TestRenderRespectsContextTimeout 校验引擎自己的超时到点后返回错误而不是挂死，并且不重试。
func TestRenderRespectsContextTimeout(t *testing.T) {
	drv := newFakeDriver()
	drv.screenshotFn = func(ctx context.Context, _ string) ([]byte, error) {
		<-ctx.Done() // 模拟卡死的截图：只有上下文到期才返回
		return nil, ctx.Err()
	}
	installFakeFactory(t, drv)
	eng := mustNewEngine(t, Config{Timeout: 150 * time.Millisecond})

	start := time.Now()
	_, err := eng.Render(context.Background(), newSmokeCard())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("超时应该报错")
	}
	if !strings.Contains(err.Error(), "超时") {
		t.Fatalf("错误应说明「超时」，实际 %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("超时应及时返回，实际耗时 %s", elapsed)
	}
	if got := drv.shotCount(); got != 1 {
		t.Fatalf("超时不应重试，期望 1 次截图，实际 %d", got)
	}
}

// TestRenderRespectsCallerDeadline 校验调用方给的更短期限同样生效。
func TestRenderRespectsCallerDeadline(t *testing.T) {
	drv := newFakeDriver()
	drv.screenshotFn = func(ctx context.Context, _ string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	installFakeFactory(t, drv)
	eng := mustNewEngine(t, Config{Timeout: time.Minute})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := eng.Render(ctx, newSmokeCard())
	if err == nil || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("调用方期限到点应报超时，实际 %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("调用方期限到点应及时返回，实际耗时 %s", elapsed)
	}
}

// TestRenderPropagatesDriverError 校验浏览器还连着时的截图错误直接上抛、不重试。
func TestRenderPropagatesDriverError(t *testing.T) {
	drv := newFakeDriver()
	drv.screenshotFn = func(context.Context, string) ([]byte, error) {
		return nil, errors.New("页面崩溃")
	}
	spy := installFakeFactory(t, drv)
	eng := mustNewEngine(t, Config{})

	_, err := eng.Render(context.Background(), newSmokeCard())
	if err == nil {
		t.Fatal("驱动报错时应该上抛")
	}
	if !strings.Contains(err.Error(), "页面崩溃") {
		t.Fatalf("错误应保留驱动给的原因，实际 %v", err)
	}
	if got := spy.callCount(); got != 1 {
		t.Fatalf("浏览器还连着时不应重建驱动，期望 1 次构造，实际 %d", got)
	}
}

// TestRenderRebuildsDriverWhenDisconnectedBeforeRender 校验渲染前发现浏览器断线时先重建再渲染。
func TestRenderRebuildsDriverWhenDisconnectedBeforeRender(t *testing.T) {
	dead := newFakeDriver()
	fresh := newFakeDriver()
	spy := installFakeFactory(t, dead, fresh)
	eng := mustNewEngine(t, Config{})

	// 第一次渲染会启动浏览器，引擎拿到的就是 dead 这个驱动。
	if _, err := eng.Render(context.Background(), newSmokeCard()); err != nil {
		t.Fatalf("首次渲染应成功，实际 %v", err)
	}
	if got := dead.shotCount(); got != 1 {
		t.Fatalf("首次渲染应使用 dead 驱动，实际 %d 次截图", got)
	}

	// 模拟浏览器在两次渲染之间掉线：下一次渲染前应该先换掉它。
	dead.setConnected(false)
	if _, err := eng.Render(context.Background(), newSmokeCard()); err != nil {
		t.Fatalf("断线自愈后应能渲染成功，实际 %v", err)
	}
	if got := dead.shotCount(); got != 1 {
		t.Fatalf("断线的驱动不应再被使用，实际总共调用了 %d 次截图", got)
	}
	if got := dead.closeCount(); got != 1 {
		t.Fatalf("断线的驱动应被关闭，实际 %d 次", got)
	}
	if got := fresh.shotCount(); got != 1 {
		t.Fatalf("重建后的驱动应承担第二次渲染，实际 %d 次截图", got)
	}
	if got := spy.callCount(); got != 2 {
		t.Fatalf("应重建一次驱动，期望 2 次构造，实际 %d", got)
	}
}

// TestRenderRetriesOnceAfterDisconnectDuringScreenshot 校验渲染途中掉线会重建驱动重试一次。
func TestRenderRetriesOnceAfterDisconnectDuringScreenshot(t *testing.T) {
	dead := newFakeDriver()
	dead.screenshotFn = func(context.Context, string) ([]byte, error) {
		dead.setConnected(false) // 模拟 Chromium 在截图时崩了
		return nil, errors.New("Target page, context or browser has been closed")
	}
	fresh := newFakeDriver()
	spy := installFakeFactory(t, dead, fresh)
	eng := mustNewEngine(t, Config{})

	img, err := eng.Render(context.Background(), newSmokeCard())
	if err != nil {
		t.Fatalf("掉线后重建驱动应能渲染成功，实际 %v", err)
	}
	if !bytes.Equal(img, fakeImage) {
		t.Fatalf("应返回重建后驱动产出的图片，实际 %q", img)
	}
	if got := spy.callCount(); got != 2 {
		t.Fatalf("应重建一次驱动，期望 2 次构造，实际 %d", got)
	}
	if got := fresh.shotCount(); got != 1 {
		t.Fatalf("重建后的驱动应重试一次截图，实际 %d 次", got)
	}
}

// TestRenderFailsWhenDriverCannotStart 校验浏览器起不来时返回带中文上下文的错误。
func TestRenderFailsWhenDriverCannotStart(t *testing.T) {
	spy := installFakeFactory(t)
	spy.failErr = errors.New("驱动目录不存在")
	eng := mustNewEngine(t, Config{})

	_, err := eng.Render(context.Background(), newSmokeCard())
	if err == nil {
		t.Fatal("浏览器起不来时应该报错")
	}
	if !strings.Contains(err.Error(), "启动渲染浏览器") || !strings.Contains(err.Error(), "驱动目录不存在") {
		t.Fatalf("错误应说明启动浏览器失败并保留原因，实际 %v", err)
	}
}

// TestRenderAfterCloseFails 校验引擎关闭后不再渲染，避免出现「半死的浏览器」。
func TestRenderAfterCloseFails(t *testing.T) {
	installFakeFactory(t, newFakeDriver())
	eng := mustNewEngine(t, Config{})

	if err := eng.Close(); err != nil {
		t.Fatalf("Close 应成功，实际 %v", err)
	}
	_, err := eng.Render(context.Background(), newSmokeCard())
	if err == nil || !strings.Contains(err.Error(), "已关闭") {
		t.Fatalf("关闭后应拒绝渲染并说明已关闭，实际 %v", err)
	}
}

// TestCloseIsIdempotentAndStopsDriver 校验 Close 幂等：驱动只关一次，重复调用返回 nil。
func TestCloseIsIdempotentAndStopsDriver(t *testing.T) {
	drv := newFakeDriver()
	installFakeFactory(t, drv)
	eng, err := New(Config{})
	if err != nil {
		t.Fatalf("New 应成功，实际 %v", err)
	}

	if _, err := eng.Render(context.Background(), newSmokeCard()); err != nil {
		t.Fatalf("Render 应成功，实际 %v", err)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("首次 Close 应成功，实际 %v", err)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("Close 应幂等，第二次应返回 nil，实际 %v", err)
	}
	if got := drv.closeCount(); got != 1 {
		t.Fatalf("驱动只应被关闭一次，实际 %d 次", got)
	}
}

// TestCloseReportsDriverError 校验关闭失败会带原因返回，但引擎仍算已关闭。
func TestCloseReportsDriverError(t *testing.T) {
	drv := newFakeDriver()
	drv.closeErr = errors.New("浏览器进程已退出")
	installFakeFactory(t, drv)
	eng, err := New(Config{})
	if err != nil {
		t.Fatalf("New 应成功，实际 %v", err)
	}
	if _, err := eng.Render(context.Background(), newSmokeCard()); err != nil {
		t.Fatalf("Render 应成功，实际 %v", err)
	}

	if err := eng.Close(); err == nil || !strings.Contains(err.Error(), "浏览器进程已退出") {
		t.Fatalf("关闭失败应带原因返回，实际 %v", err)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("关闭失败之后仍应幂等返回 nil，实际 %v", err)
	}
}

// TestNewAppliesDefaultsAndRejectsUnknownFormat 校验零值取默认、未知格式直接报错。
func TestNewAppliesDefaultsAndRejectsUnknownFormat(t *testing.T) {
	eng, err := New(Config{})
	if err != nil {
		t.Fatalf("零值配置应取默认值，实际 %v", err)
	}
	if eng.cfg.Width != 900 || eng.cfg.Scale != 2 || eng.cfg.Timeout != 15*time.Second || eng.cfg.Format != FormatPNG {
		t.Fatalf("零值配置应归一化成默认值，实际 %+v", eng.cfg)
	}

	neg, err := New(Config{Width: -1, Scale: -2, Timeout: -time.Second})
	if err != nil {
		t.Fatalf("负值也应按缺省处理，实际 %v", err)
	}
	if neg.cfg.Width != 900 || neg.cfg.Scale != 2 || neg.cfg.Timeout != 15*time.Second {
		t.Fatalf("负值应归一化成默认值，实际 %+v", neg.cfg)
	}

	up, err := New(Config{Width: 1200, Scale: 1, Timeout: time.Second, Format: " JPEG "})
	if err != nil {
		t.Fatalf("大小写与空白应被归一化，实际 %v", err)
	}
	if up.cfg.Format != FormatJPEG || up.cfg.Width != 1200 || up.cfg.Scale != 1 {
		t.Fatalf("配置应被原样保留并归一化格式，实际 %+v", up.cfg)
	}

	if _, err := New(Config{Format: "webp"}); err == nil || !strings.Contains(err.Error(), "format") {
		t.Fatalf("未知格式应报错并提到 format，实际 %v", err)
	}
}

// TestDriverConfigPassedToFactory 校验引擎把宽度、缩放与格式透传给驱动。
func TestDriverConfigPassedToFactory(t *testing.T) {
	spy := installFakeFactory(t, newFakeDriver())
	eng := mustNewEngine(t, Config{Width: 1200, Scale: 3, Timeout: time.Second, Format: FormatJPEG})

	if _, err := eng.Render(context.Background(), newSmokeCard()); err != nil {
		t.Fatalf("Render 应成功，实际 %v", err)
	}
	got := spy.firstConfig()
	if got.Width != 1200 || got.Scale != 3 || got.Format != FormatJPEG {
		t.Fatalf("驱动应收到归一化后的配置，实际 %+v", got)
	}
}

// TestScreenshotOptionsMatchFormat 校验图片格式落到 playwright 的截图参数上。
func TestScreenshotOptionsMatchFormat(t *testing.T) {
	png := screenshotOptions(FormatPNG)
	if png.Type == nil || *png.Type != *playwright.ScreenshotTypePng {
		t.Fatalf("png 配置应输出 PNG，实际 %v", png.Type)
	}
	if png.Quality != nil {
		t.Fatalf("PNG 是无损格式，不应设置 quality，实际 %v", *png.Quality)
	}
	if png.FullPage == nil || !*png.FullPage {
		t.Fatal("卡片高度由内容决定，必须整页截图")
	}

	jpg := screenshotOptions(FormatJPEG)
	if jpg.Type == nil || *jpg.Type != *playwright.ScreenshotTypeJpeg {
		t.Fatalf("jpeg 配置应输出 JPEG，实际 %v", jpg.Type)
	}
	if jpg.Quality == nil || *jpg.Quality != 85 {
		t.Fatalf("jpeg 质量应为 85，实际 %v", jpg.Quality)
	}
	if jpg.FullPage == nil || !*jpg.FullPage {
		t.Fatal("卡片高度由内容决定，必须整页截图")
	}
}

// TestCardRegistryLoadsRealTemplates 校验内置模板都能加载，且每张卡片都提供了 body 片段。
func TestCardRegistryLoadsRealTemplates(t *testing.T) {
	if len(cardTemplates) == 0 {
		t.Fatal("应至少注册一张卡片")
	}
	if _, ok := cardTemplates["smoke"]; !ok {
		t.Fatal("冒烟卡片 smoke 应被注册")
	}
	for name, tmpl := range cardTemplates {
		if tmpl.Lookup(baseTemplateName) == nil {
			t.Errorf("卡片 %s 的模板集合缺少 base 外壳", name)
		}
		if tmpl.Lookup(cardBodyTemplateName) == nil {
			t.Errorf("卡片 %s 的模板集合缺少 body 片段", name)
		}
	}
}

// TestMustLoadCardsFailFast 校验模板集合不完整时在加载阶段就 panic，而不是等渲染时才炸。
func TestMustLoadCardsFailFast(t *testing.T) {
	const okBase = `{{define "base"}}<html>{{template "body" .}}</html>{{end}}`

	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{
			name:  "没有任何卡片模板",
			files: map[string]string{"templates/base.tmpl": okBase},
			want:  "没有任何卡片模板",
		},
		{
			name:  "缺少 base 外壳",
			files: map[string]string{"templates/smoke.tmpl": `{{define "body"}}x{{end}}`},
			want:  "base",
		},
		{
			name: "卡片没有定义 body",
			files: map[string]string{
				"templates/base.tmpl":  okBase,
				"templates/smoke.tmpl": `{{define "别的名字"}}x{{end}}`,
			},
			want: "body",
		},
		{
			// 卡片自己定义 {{define "base"}} 会静默覆盖外壳，让所有卡片的标题栏一起走样：
			// 必须在加载阶段就拦下来（这是 mustLoadCards 里那道探针唯一挡得住的东西）。
			name: "卡片重新定义 base 外壳",
			files: map[string]string{
				"templates/base.tmpl":  okBase,
				"templates/smoke.tmpl": `{{define "base"}}<html>偷偷换掉外壳</html>{{end}}{{define "body"}}x{{end}}`,
			},
			want: "templates/smoke.tmpl 不应重新定义",
		},
		{
			name: "模板语法错误",
			files: map[string]string{
				"templates/base.tmpl":  okBase,
				"templates/smoke.tmpl": `{{define "body"}}{{if}}{{end}}`,
			},
			want: "模板",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fsys := fstest.MapFS{}
			for path, content := range tc.files {
				fsys[path] = &fstest.MapFile{Data: []byte(content)}
			}
			got := panicText(func() { mustLoadCards(fsys) })
			if !strings.Contains(got, tc.want) {
				t.Fatalf("panic 内容应包含 %q，实际 %q", tc.want, got)
			}
		})
	}
}

// TestMustLoadCardsAcceptsValidSet 校验合法的模板集合能被正确注册，并跳过 base 自身。
func TestMustLoadCardsAcceptsValidSet(t *testing.T) {
	const okBase = `{{define "base"}}<html>{{template "body" .}}</html>{{end}}`
	fsys := fstest.MapFS{
		"templates/base.tmpl":  &fstest.MapFile{Data: []byte(okBase)},
		"templates/smoke.tmpl": &fstest.MapFile{Data: []byte(`{{define "body"}}冒烟{{end}}`)},
		"templates/war.tmpl":   &fstest.MapFile{Data: []byte(`{{define "body"}}战况{{end}}`)},
	}

	cards := mustLoadCards(fsys)
	if _, ok := cards[baseTemplateName]; ok {
		t.Fatalf("base 是外壳而不是卡片，不该出现在注册表里：%v", cards)
	}
	if len(cards) != 2 || cards["smoke"] == nil || cards["war"] == nil {
		t.Fatalf("应注册 smoke 与 war 两张卡片，实际 %v", cards)
	}
}

// panicText 执行 fn 并返回 panic 值的文本；没有 panic 时返回空串。
func panicText(fn func()) (text string) {
	defer func() {
		if r := recover(); r != nil {
			text = fmt.Sprint(r)
		}
	}()
	fn()
	return ""
}

// TestRenderSmokeWithRealBrowser 是真浏览器冒烟：默认跳过，用 HD2_RENDER_SMOKE=1 打开。
func TestRenderSmokeWithRealBrowser(t *testing.T) {
	if os.Getenv("HD2_RENDER_SMOKE") != "1" {
		t.Skip("未开启 HD2_RENDER_SMOKE，跳过真浏览器冒烟")
	}
	if err := PrepareRuntime(); err != nil {
		t.Fatalf("准备浏览器运行环境失败：%v", err)
	}

	eng, err := New(Config{})
	if err != nil {
		t.Fatalf("New 应成功，实际 %v", err)
	}
	defer func() {
		if err := eng.Close(); err != nil {
			t.Errorf("Close 出错：%v", err)
		}
	}()

	card := smokeCardWithStale(true)
	start := time.Now()
	first, err := eng.Render(context.Background(), card)
	if err != nil {
		t.Fatalf("首次渲染应成功，实际 %v", err)
	}
	cold := time.Since(start)

	start = time.Now()
	if _, err := eng.Render(context.Background(), card); err != nil {
		t.Fatalf("第二次渲染应成功（复用常驻浏览器），实际 %v", err)
	}
	warm := time.Since(start)

	if !bytes.HasPrefix(first, pngMagic) {
		t.Fatalf("应输出 PNG，实际开头 % x", first[:4])
	}
	width, height, err := pngSize(first)
	if err != nil {
		t.Fatalf("解析 PNG 尺寸失败：%v", err)
	}
	if width < 800 {
		t.Fatalf("卡片宽度应至少 800px，实际 %d", width)
	}
	// 高度必须贴着内容：截图走 FullPage，而视口高度刻意取小值，
	// 把 viewportHeight 改回大值不会报错，只会让短卡片下面拖出一大片空白。
	if height < 100 || height > 900 {
		t.Fatalf("卡片高度应贴合内容（视口高度取小值），实际 %dx%d", width, height)
	}

	out := filepath.Join(os.TempDir(), "hd2_render_smoke.png")
	if err := os.WriteFile(out, first, 0o600); err != nil {
		t.Fatalf("写出冒烟图片失败：%v", err)
	}
	t.Logf("真浏览器渲染：冷启动+首张 %s，复用浏览器第二张 %s，尺寸 %dx%d，体积 %d 字节，落盘 %s",
		cold, warm, width, height, len(first), out)
}

// pngSize 从 PNG 的 IHDR 里读出宽高，避免为了断言尺寸引入图片解码依赖。
func pngSize(data []byte) (int, int, error) {
	if len(data) < 24 || !bytes.HasPrefix(data, pngMagic) {
		return 0, 0, errors.New("不是合法的 PNG 数据")
	}
	width := int(data[16])<<24 | int(data[17])<<16 | int(data[18])<<8 | int(data[19])
	height := int(data[20])<<24 | int(data[21])<<16 | int(data[22])<<8 | int(data[23])
	return width, height, nil
}

// recoverToError 把 goroutine 里的 panic 转成写进 errCh 的错误。
// 有了它，「关闭后还在用旧驱动」这类崩溃才会表现为一条可读的失败信息，
// 而不是让整个测试进程直接挂掉。errCh 必须带缓冲，否则 panic 路径会再卡一次。
func recoverToError(errCh chan<- error, what string) {
	if r := recover(); r != nil {
		errCh <- fmt.Errorf("%s 过程中 panic：%v", what, r)
	}
}

// TestNewDoesNotStartBrowser 校验懒启动：只 New 不起浏览器，第一次渲染才启动，之后复用。
// 配置里关掉渲染、或起进程后没人查数据时，都不该白起一个 Chromium。
func TestNewDoesNotStartBrowser(t *testing.T) {
	spy := installFakeFactory(t, newFakeDriver())
	eng := mustNewEngine(t, Config{})

	if got := spy.callCount(); got != 0 {
		t.Fatalf("New 不应启动浏览器，实际启动 %d 次", got)
	}
	if _, err := eng.Render(context.Background(), newSmokeCard()); err != nil {
		t.Fatalf("首次渲染应成功，实际 %v", err)
	}
	if got := spy.callCount(); got != 1 {
		t.Fatalf("首次渲染应启动一次浏览器，实际 %d 次", got)
	}
	if _, err := eng.Render(context.Background(), newSmokeCard()); err != nil {
		t.Fatalf("第二次渲染应成功，实际 %v", err)
	}
	if got := spy.callCount(); got != 1 {
		t.Fatalf("第二次渲染应复用常驻浏览器，实际启动 %d 次", got)
	}
}

// TestCloseWaitsForInFlightRender 校验 Close 与在途渲染互斥。
// 关掉正在使用的浏览器会让真驱动空指针崩溃（进程级故障），所以 Close 必须等这张渲染结束；
// 去掉 Close 里的 renderMu 时这个用例会因「关闭后仍被用来截图」而失败。
func TestCloseWaitsForInFlightRender(t *testing.T) {
	drv := newFakeDriver()
	drv.panicWhenClosed = true
	started := make(chan struct{})
	release := make(chan struct{})
	drv.screenshotFn = func(context.Context, string) ([]byte, error) {
		close(started)
		<-release
		return fakeImage, nil
	}
	installFakeFactory(t, drv)
	eng := mustNewEngine(t, Config{})

	renderErr := make(chan error, 1)
	go func() {
		defer recoverToError(renderErr, "渲染")
		_, err := eng.Render(context.Background(), newSmokeCard())
		renderErr <- err
	}()
	<-started

	closeErr := make(chan error, 1)
	go func() {
		defer recoverToError(closeErr, "关闭")
		closeErr <- eng.Close()
	}()

	// 渲染还卡在截图里时，Close 不该提前返回
	select {
	case err := <-closeErr:
		t.Fatalf("渲染仍在进行时 Close 不该返回，实际返回 %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	close(release)
	if err := <-renderErr; err != nil {
		t.Fatalf("在途渲染应正常结束，实际 %v", err)
	}
	if err := <-closeErr; err != nil {
		t.Fatalf("Close 应成功，实际 %v", err)
	}
	if got := drv.closeCount(); got != 1 {
		t.Fatalf("驱动应被关闭一次，实际 %d 次", got)
	}
}

// TestSelfHealingGivesUpAfterOneRetry 校验自愈只重试一次：重建后的浏览器仍然掉线时立刻放弃。
// 浏览器反复崩掉时无限重建会把机器人困在启动循环里，所以重试次数必须有界。
func TestSelfHealingGivesUpAfterOneRetry(t *testing.T) {
	var drivers []*fakeDriver
	prev := newDriver
	newDriver = func(DriverConfig) (pageDriver, error) {
		// 每次构造都发一个「已掉线」的驱动：截图必然失败，自愈也无从救起
		drv := newFakeDriver()
		drv.setConnected(false)
		drv.screenshotFn = func(context.Context, string) ([]byte, error) {
			return nil, errors.New("浏览器已断开")
		}
		drivers = append(drivers, drv)
		return drv, nil
	}
	t.Cleanup(func() { newDriver = prev })

	eng := mustNewEngine(t, Config{})
	_, err := eng.Render(context.Background(), newSmokeCard())
	if err == nil {
		t.Fatal("首次与重试都掉线时应报错")
	}
	if !strings.Contains(err.Error(), "渲染失败") {
		t.Fatalf("错误应带中文上下文，实际 %v", err)
	}
	if len(drivers) != 2 {
		t.Fatalf("自愈只应重建一次，实际构造 %d 个驱动", len(drivers))
	}
	shots := 0
	for _, drv := range drivers {
		shots += drv.shotCount()
	}
	if shots != 2 {
		t.Fatalf("应截图两次（首次 + 一次重试）后放弃，实际 %d 次", shots)
	}
}

// TestHTMLRendersCardWithoutBrowser 校验 HTML 是纯模板执行、不起浏览器，且能挡住未知卡片。
// 插件只要断言「视图模型 → HTML」的正确性时用它就够了，不需要一台 Chromium。
func TestHTMLRendersCardWithoutBrowser(t *testing.T) {
	spy := installFakeFactory(t) // 一个驱动都不给：HTML 不该碰浏览器

	html, err := HTML(newSmokeCard())
	if err != nil {
		t.Fatalf("HTML 应成功，实际 %v", err)
	}
	if !strings.Contains(html, "冒烟卡片") {
		t.Fatalf("HTML 应包含卡片标题，实际 %s", html)
	}
	// 视图模型里的尖括号必须被 html/template 转义，避免上游文本注入 HTML
	if !strings.Contains(html, "&lt;b&gt;转义检查&lt;/b&gt;") {
		t.Fatalf("卡片内容里的尖括号应被转义，实际 %s", html)
	}
	if spy.callCount() != 0 {
		t.Fatalf("HTML 不应启动浏览器，实际启动 %d 次", spy.callCount())
	}

	if _, err := HTML(Card{Name: "并不存在的卡片"}); err == nil || !strings.Contains(err.Error(), "未知卡片") {
		t.Fatalf("未知卡片应返回中文错误，实际 %v", err)
	}
}
