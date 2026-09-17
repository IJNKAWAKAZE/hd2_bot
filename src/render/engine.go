// Package render 把「卡片视图模型」渲染成可以直接发到 Telegram 的图片。
//
// 边界与约定：
//   - 引擎只认识 Card（卡片名 + 视图模型）与 Renderer 接口，不认识任何业务类型；
//     文案本地化与数值格式化由调用方完成，模板只负责排版。
//   - 卡片视图模型必须内嵌 Meta（满足 CardShell），外壳的标题、数据时间与过期角标都从它取。
//   - 模板与素材用 go:embed 打进二进制，渲染走 page.SetContent，不额外开 HTTP 路由，
//     因此不依赖端口占用与启动时序。
//   - 浏览器常驻：懒启动、串行渲染、断线自愈；进程退出时由 Close 释放。
//
// 失败策略：渲染是展示层的尽力而为能力，错误一律上抛，由调用方决定是否回退纯文本。
// 引擎不吞错，也绝不返回半成品图片；高度超过 MaxCardHeightPx 的整图同样按错误上抛
// （见 limits.go：宁可让调用方改发完整文本，也不为了压高度去截断卡片内容）。
package render

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/mxschmitt/playwright-go"
)

const (
	// FormatPNG / FormatJPEG 是卡片图片的输出格式，取值与配置里的 render.format 一致。
	FormatPNG  = "png"
	FormatJPEG = "jpeg"

	// 默认值与 config 包里的默认值保持一致。这里再兜一次，是为了让「零值 Config」
	// 在测试与工具代码里也能直接用，而不是渲染出 0 像素宽的图。
	defaultWidth   = 900
	defaultScale   = 2
	defaultTimeout = 15 * time.Second
	defaultFormat  = FormatPNG

	// jpegQuality 是 JPEG 的输出质量：再低文字边缘会发虚，再高只是白涨体积。
	jpegQuality = 85

	// viewportHeight 是浏览器视口的初始高度。截图走 FullPage，图片高度等于文档高度，
	// 而文档高度至少等于视口高度——所以这里刻意取一个很小的值，让图片贴着卡片内容裁剪；
	// 取大值会让短卡片下面拖出一大片空白背景（实测 1200 时输出 1800x2400 的半空图）。
	// 视口高度不参与排版（排版只由宽度决定），模板里也不要使用 vh 单位。
	viewportHeight = 120
)

// Renderer 是插件依赖的最小接口：给定卡片与视图模型，返回图片字节。
// 插件只依赖它，所以插件单测不需要浏览器，注入假实现即可。
type Renderer interface {
	// Render 渲染一张卡片，返回图片字节（PNG 或 JPEG 取决于 Config.Format）。
	// ctx 只影响这一次渲染，不改变引擎的常驻状态；超时返回的错误里带「超时」。
	Render(ctx context.Context, card Card) ([]byte, error)
	// Close 释放常驻浏览器；可重复调用，重复调用返回 nil。
	Close() error
}

// Config 是渲染引擎的运行参数，通常由 config.RenderConfig 换算而来。
// 零值会取默认值（900px / 2 倍 / 15s / png）；只有 Format 给了未知值才会报错，
// 因为那是「明确写错了」，而 0 宽度更像是调用方没设置。
type Config struct {
	Width   int           // 卡片 CSS 宽度（px），同时作为浏览器视口宽度
	Scale   int           // 设备缩放，2 表示二倍图
	Timeout time.Duration // 单张卡片的渲染超时
	Format  string        // 输出格式：FormatPNG / FormatJPEG
}

// Engine 是 Renderer 的默认实现：常驻一个 Chromium，串行地把卡片渲染成图片。
type Engine struct {
	cfg Config

	// renderMu 保证同一时刻只有一张卡片在截图：单群场景下并发出图只会互相抢 CPU，
	// 上下文里多页并行也更容易出问题。Close 同样先拿这把锁，
	// 从而不会出现「渲染到一半页面被关掉」。
	renderMu sync.Mutex

	// stateMu 保护 driver 与 closed 两个可变状态。
	// 加锁顺序固定为 renderMu → stateMu（Render 与 Close 都遵守），不要反过来。
	stateMu sync.Mutex
	driver  pageDriver
	closed  bool
}

// New 构造渲染引擎。构造时不会启动浏览器：浏览器在第一次渲染时懒启动，
// 所以「配置里关掉渲染」或「启动了但没人查数据」都不会白起 Chromium。
func New(cfg Config) (*Engine, error) {
	normalized, err := normalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Engine{cfg: normalized}, nil
}

// normalizeConfig 补齐零值并校验输出格式，返回可以直接使用的配置。
func normalizeConfig(cfg Config) (Config, error) {
	if cfg.Width <= 0 {
		cfg.Width = defaultWidth
	}
	if cfg.Scale <= 0 {
		cfg.Scale = defaultScale
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	cfg.Format = strings.ToLower(strings.TrimSpace(cfg.Format))
	if cfg.Format == "" {
		cfg.Format = defaultFormat
	}
	if cfg.Format != FormatPNG && cfg.Format != FormatJPEG {
		return Config{}, fmt.Errorf("渲染配置有误：format 只能是 %s 或 %s，实际为 %q", FormatPNG, FormatJPEG, cfg.Format)
	}
	return cfg, nil
}

// Render 渲染一张卡片。
//
// 与调用方的约定：
//   - 卡片名不存在、视图模型没内嵌 Meta、模板执行出错都会返回带中文原因的错误；
//   - 调用方传 Error 时应回退纯文本，不要重试（重试大概率再失败一次，还多占一次超时）；
//   - 截图高度超过 MaxCardHeightPx 时返回错误（`IsCardTooTall` 可辨认）：这不是「渲染坏了」，
//     而是「这张图太大」，同样要求调用方回退**完整文本**——S4 起不再为了压高度去截断内容。
func (e *Engine) Render(ctx context.Context, card Card) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background() // 防调用方传 nil，否则 WithTimeout 会拿到 nil 的父上下文
	}
	html, err := HTML(card)
	if err != nil {
		return nil, err
	}
	img, err := e.renderHTML(ctx, html)
	if err != nil {
		return nil, err
	}
	// 高度判定放在自愈重试之后：一张太高的图不该被当成「驱动掉线」再重画一遍。
	if err := checkCardHeight(img); err != nil {
		return nil, err
	}
	return img, nil
}

// HTML 把卡片渲染成 HTML 字符串，但不截图。它是 Render 的第一步，也供调试与测试复用：
// 卡片名是否注册过、视图模型有没有漏字段、文本有没有被正确转义，都能不开浏览器先查清楚，
// 排查「图出不来」时比对着整张图片找问题快得多。
// 错误与 Render 保持一致：未知卡片、视图模型没内嵌 Meta、模板执行出错都返回中文错误。
func HTML(card Card) (string, error) {
	tmpl, ok := cardTemplates[card.Name]
	if !ok {
		return "", fmt.Errorf("渲染失败：未知卡片 %q", card.Name)
	}
	if _, ok := card.Data.(CardShell); !ok {
		return "", fmt.Errorf("渲染失败：卡片 %s 的视图模型必须内嵌 render.Meta（提供标题与数据时间）", card.Name)
	}

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, baseTemplateName, card.Data); err != nil {
		return "", fmt.Errorf("渲染失败：卡片 %s 执行模板出错：%w", card.Name, err)
	}
	return buf.String(), nil
}

// Close 释放常驻浏览器。幂等：重复调用返回 nil；关闭失败会带上原因返回，
// 但引擎仍然算已关闭，不会出现「关一半又活过来」的状态。
func (e *Engine) Close() error {
	// 先拿 renderMu：等正在进行的那一张渲染结束，避免关掉正在使用的页面。
	e.renderMu.Lock()
	defer e.renderMu.Unlock()

	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	if e.closed {
		return nil
	}
	e.closed = true
	if e.driver == nil {
		return nil // 懒启动：一次都没渲染过就关闭，什么都不用做
	}

	drv := e.driver
	e.driver = nil
	if err := drv.close(); err != nil {
		return fmt.Errorf("关闭渲染引擎出错：%w", err)
	}
	return nil
}

// renderHTML 把 HTML 交给浏览器截图。串行、超时与自愈都收敛在这一层。
func (e *Engine) renderHTML(ctx context.Context, html string) ([]byte, error) {
	e.renderMu.Lock()
	defer e.renderMu.Unlock()

	// 调用方的 ctx 通常没有期限（命令处理里多是 Background），所以引擎自己再套一层超时：
	// 没有兜底超时，一次卡死的截图会永久占住 renderMu，后面所有卡片都只能排队。
	ctx, cancel := context.WithTimeout(ctx, e.cfg.Timeout)
	defer cancel()

	drv, err := e.acquireDriver()
	if err != nil {
		return nil, err
	}
	img, err := drv.screenshot(ctx, html)
	if err == nil {
		return img, nil
	}

	// 浏览器在渲染途中掉线属于可自愈故障：丢掉这个驱动、重建之后再试一次。
	// 其它错误（含超时）都不重试：超时说明这张图本来就太慢，重试只会再等一个超时。
	if ctx.Err() != nil || drv.isConnected() {
		return nil, describeRenderError(ctx, e.cfg.Timeout, err)
	}
	firstErr := err
	e.dropDriver(drv)

	rebuilt, err := e.acquireDriver()
	if err != nil {
		return nil, err
	}
	img, err = rebuilt.screenshot(ctx, html)
	if err != nil {
		return nil, describeRenderError(ctx, e.cfg.Timeout, errors.Join(firstErr, err))
	}
	return img, nil
}

// acquireDriver 返回可用的浏览器驱动，必要时启动或重建。
// 调用方必须已持有 renderMu，因此这里不必担心并发渲染把驱动换掉。
func (e *Engine) acquireDriver() (pageDriver, error) {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()

	if e.closed {
		return nil, errors.New("渲染失败：渲染引擎已关闭")
	}
	if e.driver != nil && e.driver.isConnected() {
		return e.driver, nil
	}
	if e.driver != nil {
		// 断线的驱动只做关闭尝试：Chromium 已经死了，关不掉是常态，
		// 不能因为关闭失败就挡住自愈。
		if err := e.driver.close(); err != nil {
			log.Printf("[render] 关闭已断线的浏览器驱动失败（忽略并重建）：%v", err)
		}
		e.driver = nil
	}

	drv, err := newDriver(DriverConfig{Width: e.cfg.Width, Scale: e.cfg.Scale, Format: e.cfg.Format})
	if err != nil {
		return nil, fmt.Errorf("渲染失败：启动渲染浏览器出错：%w", err)
	}
	e.driver = drv
	return drv, nil
}

// dropDriver 丢弃指定驱动（仅当它还是当前驱动时），供渲染途中掉线的自愈路径调用。
func (e *Engine) dropDriver(drv pageDriver) {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	if e.driver != drv {
		return
	}
	if err := e.driver.close(); err != nil {
		log.Printf("[render] 关闭掉线的浏览器驱动失败（忽略并重建）：%v", err)
	}
	e.driver = nil
}

// describeRenderError 把底层错误包装成能区分「超时 / 取消 / 普通失败」的中文错误。
func describeRenderError(ctx context.Context, timeout time.Duration, err error) error {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		// 不写「超过 cfg.Timeout」这种断言：调用方也可能给了更短的期限，
		// 那样写会把实际等待时间说错；引擎的超时上限只作为参考信息附在后面。
		return fmt.Errorf("渲染超时：%w（引擎超时上限 %s）", err, timeout)
	case errors.Is(ctx.Err(), context.Canceled):
		return fmt.Errorf("渲染被取消：%w", err)
	default:
		return fmt.Errorf("渲染失败：%w", err)
	}
}

// DriverConfig 是启动浏览器驱动需要的参数，由 Config 换算而来。
type DriverConfig struct {
	Width  int    // 视口宽度（px），也就是卡片宽度
	Scale  int    // 设备缩放，2 表示二倍图
	Format string // 输出格式：FormatPNG / FormatJPEG
}

// pageDriver 是渲染引擎对浏览器的最小依赖：截图、探活、释放。
// 真实现是 playwrightDriver；单测注入假实现，所以引擎的串行、超时、自愈逻辑
// 都能在没有浏览器的环境里被覆盖。
type pageDriver interface {
	// screenshot 把 HTML 渲染成图片；ctx 到期时必须尽快返回 ctx.Err()。
	screenshot(ctx context.Context, html string) ([]byte, error)
	// isConnected 报告浏览器是否还活着，供断线自愈判断。
	isConnected() bool
	// close 释放浏览器与 playwright 驱动进程；可重复调用。
	close() error
}

// newDriver 是驱动工厂。声明成包级变量是为了让测试注入假实现：
// 真浏览器又慢又依赖本机环境，不适合放进单测，而引擎的自愈与串行逻辑又必须被测到。
var newDriver = newPlaywrightDriver

// PrepareRuntime 在启动阶段检查并补齐匹配版本的驱动和 Chromium。
// 已安装的组件由 Playwright 复用；下载不占用单条指令的渲染超时。
func PrepareRuntime() error {
	if err := playwright.Install(&playwright.RunOptions{Browsers: []string{"chromium"}}); err != nil {
		return fmt.Errorf("准备 Playwright 驱动和 Chromium 失败：%w", err)
	}
	return nil
}

// newPlaywrightDriver 启动 playwright 驱动与一个 headless Chromium，并建好复用的浏览器上下文。
//
// 驱动与浏览器由启动阶段的 PrepareRuntime 准备；渲染过程中不联网安装。
func newPlaywrightDriver(cfg DriverConfig) (pageDriver, error) {
	pw, err := playwright.Run()
	if err != nil {
		return nil, fmt.Errorf("启动 playwright 驱动失败（检查驱动是否已安装）：%w", err)
	}

	browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(true),
	})
	if err != nil {
		stopPlaywright(pw)
		return nil, fmt.Errorf("启动 Chromium 失败：%w", err)
	}

	bc, err := browser.NewContext(playwright.BrowserNewContextOptions{
		// 视口宽度直接用配置值：截图走 FullPage，出图宽度就等于 render.width × scale。
		Viewport:          &playwright.Size{Width: cfg.Width, Height: viewportHeight},
		DeviceScaleFactor: playwright.Float(float64(cfg.Scale)),
	})
	if err != nil {
		_ = browser.Close()
		stopPlaywright(pw)
		return nil, fmt.Errorf("创建浏览器上下文失败：%w", err)
	}

	return &playwrightDriver{pw: pw, browser: browser, context: bc, format: cfg.Format}, nil
}

// playwrightDriver 是 pageDriver 的真实现：一个常驻的 playwright 驱动进程 + 一个浏览器上下文。
type playwrightDriver struct {
	pw      *playwright.Playwright
	browser playwright.Browser
	context playwright.BrowserContext
	format  string
}

// isConnected 报告浏览器是否还活着。驱动进程崩掉（比如被系统杀死）时为假，
// 引擎据此重建，而不是继续往一个死连接上发命令。
func (d *playwrightDriver) isConnected() bool {
	return d.browser != nil && d.browser.IsConnected()
}

// screenshot 新建页面、写入 HTML 并整页截图。
//
// 为什么把 SetContent + Screenshot 丢进 goroutine：playwright-go 的调用本身不吃 context，
// 只有在另一个 goroutine 里等，ctx 到期时才能主动 Close 页面把这次调用打断。
// 否则一个卡死的截图会把引擎一直拖住，超时形同虚设。
func (d *playwrightDriver) screenshot(ctx context.Context, html string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	page, err := d.context.NewPage()
	if err != nil {
		return nil, fmt.Errorf("新建页面失败：%w", err)
	}
	defer func() {
		// 无论成功失败都要关页面，否则残留页面会一直占着 Chromium 的内存。
		_ = page.Close()
	}()

	type result struct {
		data []byte
		err  error
	}
	// 带缓冲的通道：即使调用方已经放弃等待，goroutine 里的写入也不会阻塞。
	done := make(chan result, 1)
	go func() {
		if err := page.SetContent(html, playwright.PageSetContentOptions{
			WaitUntil: playwright.WaitUntilStateLoad,
		}); err != nil {
			done <- result{nil, fmt.Errorf("写入页面 HTML 失败：%w", err)}
			return
		}
		data, err := page.Screenshot(screenshotOptions(d.format))
		if err != nil {
			done <- result{nil, fmt.Errorf("截图失败：%w", err)}
			return
		}
		done <- result{data, nil}
	}()

	select {
	case <-ctx.Done():
		// 强制关掉页面，让上面那次调用尽快返回错误，而不是继续占着浏览器不放。
		_ = page.Close()
		return nil, ctx.Err()
	case r := <-done:
		if r.err != nil {
			return nil, r.err
		}
		return r.data, nil
	}
}

// close 释放浏览器相关资源，顺序固定为 context → browser → playwright 驱动进程，
// 否则 Chromium 可能变成收不掉的孤儿进程。
// 幂等：重复调用返回 nil；某一步失败不影响继续释放后面的资源，错误用 errors.Join 汇总。
func (d *playwrightDriver) close() error {
	var errs []error
	if d.context != nil {
		if err := d.context.Close(); err != nil {
			errs = append(errs, fmt.Errorf("关闭浏览器上下文出错：%w", err))
		}
		d.context = nil
	}
	if d.browser != nil {
		if err := d.browser.Close(); err != nil {
			errs = append(errs, fmt.Errorf("关闭浏览器出错：%w", err))
		}
		d.browser = nil
	}
	if d.pw != nil {
		if err := d.pw.Stop(); err != nil {
			errs = append(errs, fmt.Errorf("结束 playwright 进程出错：%w", err))
		}
		d.pw = nil
	}
	if len(errs) == 0 {
		return nil
	}
	return errors.Join(errs...)
}

// screenshotOptions 把输出格式映射成截图参数。
// FullPage 是必须的：卡片高度由内容决定，不整页截图只会截到视口范围。
// 只有 JPEG 需要显式给 Quality——PNG 无损，传 quality 会被驱动忽略。
func screenshotOptions(format string) playwright.PageScreenshotOptions {
	opts := playwright.PageScreenshotOptions{FullPage: playwright.Bool(true)}
	if format == FormatJPEG {
		opts.Type = playwright.ScreenshotTypeJpeg
		opts.Quality = playwright.Int(jpegQuality)
		return opts
	}
	opts.Type = playwright.ScreenshotTypePng
	return opts
}

// stopPlaywright 在启动中途失败时尽力结束驱动进程，避免留下孤立的 node 进程。
func stopPlaywright(pw *playwright.Playwright) {
	if pw == nil {
		return
	}
	if err := pw.Stop(); err != nil {
		log.Printf("[render] 结束半启动的 playwright 进程失败（忽略）：%v", err)
	}
}
