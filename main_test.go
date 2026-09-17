package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tgbotapi "github.com/ijnkawakaze/telegram-bot-api"

	"hd2_bot/src/config"
	"hd2_bot/src/core/bot"
	"hd2_bot/src/hd2"
	"hd2_bot/src/plugins/plugutil/plugtest"
	"hd2_bot/src/plugins/system"
	"hd2_bot/src/push"
	"hd2_bot/src/render"
	"hd2_bot/src/state"
	"hd2_bot/src/translate"
)

// equalStrings 比较两个字符串切片是否完全相同（go1.20 工具链跑 -race 时没有 slices 包可用）。
func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// captureLog 把标准日志重定向到缓冲区，用例结束时还原。
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	oldWriter, oldFlags := log.Writer(), log.Flags()
	log.SetOutput(buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
	})
	return buf
}

// chdir 切换工作目录（run 按工作目录查找 hd2.yaml），用例结束时还原。
func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatalf("获取工作目录失败：%v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("切换工作目录失败：%v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(old); err != nil {
			t.Errorf("还原工作目录失败：%v", err)
		}
	})
}

// fakeServer 记录 HTTP 服务的关闭调用。
type fakeServer struct{ closed bool }

// Shutdown 记录一次关闭。
func (f *fakeServer) Shutdown(context.Context) error {
	f.closed = true
	return nil
}

// fakeStore 记录状态文件的关闭调用；onClose 用于断言它排在渲染引擎之后关闭。
type fakeStore struct {
	closed  bool
	onClose func()
}

// Close 记录一次关闭。
func (f *fakeStore) Close() error {
	f.closed = true
	if f.onClose != nil {
		f.onClose()
	}
	return nil
}

// fakeRenderer 是渲染引擎的假实现：出图一律失败（用例只关心停机时它有没有被关闭），
// 但必须实现完整契约，这样 shutdownDeps 的字段类型才能把「渲染引擎与状态文件传反」挡在编译期。
type fakeRenderer struct {
	closed  bool
	onClose func()
}

// Render 永远失败：停机用例不需要真的出图。
func (f *fakeRenderer) Render(context.Context, render.Card) ([]byte, error) {
	return nil, errors.New("用例用渲染器不出图")
}

// Close 记录一次关闭。
func (f *fakeRenderer) Close() error {
	f.closed = true
	if f.onClose != nil {
		f.onClose()
	}
	return nil
}

// TestRunMissingConfig 验证缺少 hd2.yaml 时给出中文提示、返回非零退出码，且不创建状态文件。
func TestRunMissingConfig(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	logs := captureLog(t)

	if code := run(); code != 1 {
		t.Fatalf("缺少配置时期望退出码 1，实际 %d", code)
	}
	out := logs.String()
	if !strings.Contains(out, "未找到配置文件 hd2.yaml") {
		t.Fatalf("日志应提示未找到配置文件，实际：%s", out)
	}
	if !strings.Contains(out, "启动失败") {
		t.Fatalf("日志应说明启动失败，实际：%s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "data", "hd2.db")); err == nil {
		t.Fatal("配置校验失败时不应创建状态文件")
	}
}

// TestRunInvalidConfigReportsAllProblems 验证缺字段的配置会被拦下，并一次性列出全部必填项。
func TestRunInvalidConfigReportsAllProblems(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	// 只有 api.client 合法，其余必填项（bot.token、bot.group_id、api.contact）都缺失。
	content := "api:\n  client: hd2_bot\n"
	if err := os.WriteFile(filepath.Join(dir, "hd2.yaml"), []byte(content), 0o600); err != nil {
		t.Fatalf("写入测试配置失败：%v", err)
	}
	logs := captureLog(t)

	if code := run(); code != 1 {
		t.Fatalf("配置不完整时期望退出码 1，实际 %d", code)
	}
	out := logs.String()
	for _, want := range []string{"bot.token 不能为空", "bot.group_id 不能为空", "api.contact 不能为空"} {
		if !strings.Contains(out, want) {
			t.Fatalf("日志缺少 %q，实际：%s", want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "data", "hd2.db")); err == nil {
		t.Fatal("配置校验失败时不应创建状态文件")
	}
}

// TestShutdownStepsReleaseEverythingInOrder 验证停机序列的顺序与覆盖面：
// 每停机一步，HTTP 服务与状态文件都必须被关闭，且状态文件最后关闭。
func TestShutdownStepsReleaseEverythingInOrder(t *testing.T) {
	logs := captureLog(t)
	var (
		order []string
		ready = true
	)

	oldPoll := stopPolling
	stopPolling = func(func(string, ...any)) error {
		order = append(order, "telegram")
		return nil
	}
	t.Cleanup(func() { stopPolling = oldPoll })

	cronStop := func() { order = append(order, "cron") }
	server := &fakeServer{}
	// 渲染引擎与状态文件都用可关闭的假实现，并各自把关闭动作记进顺序表：
	// 只断言「被关过」不足以证明顺序，而渲染引擎必须赶在状态文件之前释放。
	renderer := &fakeRenderer{onClose: func() { order = append(order, "render") }}
	store := &fakeStore{onClose: func() { order = append(order, "state") }}

	steps := shutdownSteps(shutdownDeps{
		markNotReady: func() { ready = false },
		cronStop:     cronStop,
		server:       server,
		renderer:     renderer,
		store:        store,
	})

	want := []string{"标记服务未就绪", "停止定时任务", "关闭 HTTP 服务", "关闭渲染引擎", "停止 Telegram 轮询", "关闭状态文件"}
	names := make([]string, 0, len(steps))
	for _, step := range steps {
		names = append(names, step.name)
	}
	if !equalStrings(names, want) {
		t.Fatalf("停机顺序不符：%v，期望：%v", names, want)
	}

	if err := shutdownAll(log.Printf, steps); err != nil {
		t.Fatalf("停机不应报错：%v", err)
	}
	if ready {
		t.Fatal("停机后服务仍然标记为就绪")
	}
	if !equalStrings(order, []string{"cron", "render", "telegram", "state"}) {
		t.Fatalf("组件停机调用不符：%v", order)
	}
	if !server.closed {
		t.Fatal("停机没有关闭 HTTP 服务")
	}
	if !renderer.closed {
		t.Fatal("停机没有关闭渲染引擎")
	}
	if !store.closed {
		t.Fatal("停机没有关闭状态文件")
	}
	out := logs.String()
	if !strings.Contains(out, "停机步骤「关闭状态文件」完成") {
		t.Fatalf("日志缺少状态文件关闭记录，实际：%s", out)
	}
}

// TestShutdownAllContinuesAfterFailure 验证某一步失败不会中断后面的步骤（状态文件必须被关掉）。
func TestShutdownAllContinuesAfterFailure(t *testing.T) {
	logs := captureLog(t)
	ran := false
	steps := []shutdownStep{
		{name: "第一步", stop: func() error { return errors.New("故意失败") }},
		{name: "第二步", stop: func() error { ran = true; return nil }},
	}
	err := shutdownAll(log.Printf, steps)
	if err == nil {
		t.Fatal("有步骤失败时应当返回错误")
	}
	if !strings.Contains(err.Error(), "第一步") {
		t.Fatalf("错误信息应指出失败的步骤，实际：%v", err)
	}
	if !ran {
		t.Fatal("前一步失败后没有继续执行后续步骤")
	}
	if !strings.Contains(logs.String(), "停机步骤「第一步」失败") {
		t.Fatalf("日志缺少失败记录，实际：%s", logs.String())
	}
}

// TestBuildClientConfigUsesConfig 验证主源客户端参数（超时、退避、重试）全部来自配置。
func TestBuildClientConfigUsesConfig(t *testing.T) {
	cfg := &config.Config{}
	cfg.API.Client = "hd2_bot"
	cfg.API.Contact = "a@b.c"
	cfg.API.UserAgent = "hd2_bot/0.1"
	cfg.API.Timeout = 30
	cfg.API.BaseHelldivers = "https://api.example/api/v1"
	cfg.Limit.RetryMax = 3
	cfg.Limit.RetryBase = 5
	cfg.Limit.RetryMaxDelay = 45

	got := buildClientConfig(cfg)
	if got.Timeout != 30*time.Second {
		t.Fatalf("Timeout 换算错误：%v", got.Timeout)
	}
	if got.RetryBase != 5*time.Second || got.RetryMaxDelay != 45*time.Second {
		t.Fatalf("退避换算错误：base=%v maxDelay=%v", got.RetryBase, got.RetryMaxDelay)
	}
	if got.Client != "hd2_bot" || got.Contact != "a@b.c" || got.BaseURL != "https://api.example/api/v1" {
		t.Fatalf("请求头或地址未取自配置：%+v", got)
	}
	if got.RetryMax != 3 {
		t.Fatalf("RetryMax 未取自配置：%d", got.RetryMax)
	}
}

// TestBuildTTLConfigUsesConfig 验证 7 类数据的缓存时长都被透传，没有「配了但没用」的字段。
func TestBuildTTLConfigUsesConfig(t *testing.T) {
	cfg := &config.Config{}
	cfg.Cache.WarTTL = 60
	cfg.Cache.PlanetsTTL = 61
	cfg.Cache.CampaignsTTL = 62
	cfg.Cache.AssignmentsTTL = 63
	cfg.Cache.DispatchesTTL = 64
	cfg.Cache.EventsTTL = 65
	cfg.Cache.StationsTTL = 66

	got := buildTTLConfig(cfg)
	want := hd2.TTLConfig{
		War:         60 * time.Second,
		Planets:     61 * time.Second,
		Campaigns:   62 * time.Second,
		Assignments: 63 * time.Second,
		Dispatches:  64 * time.Second,
		Events:      65 * time.Second,
		Stations:    66 * time.Second,
	}
	if got != want {
		t.Fatalf("TTL 换算错误：%+v，期望 %+v", got, want)
	}
}

// TestBuildWebOptionsUsesConfig 验证 HTTP 监听地址取自配置，且就绪状态由传入标记决定。
func TestBuildWebOptionsUsesConfig(t *testing.T) {
	cfg := &config.Config{}
	cfg.HTTP.Host = "0.0.0.0"
	cfg.HTTP.Port = 34567

	var flag atomic.Bool
	opts := buildWebOptions(cfg, &flag)
	if opts.Host != "0.0.0.0" || opts.Port != 34567 {
		t.Fatalf("监听参数未取自配置：%+v", opts)
	}
	if opts.Ready == nil {
		t.Fatal("就绪回调不应为空")
	}
	if opts.Ready() {
		t.Fatal("标记为假时不应报就绪")
	}
	flag.Store(true)
	if !opts.Ready() {
		t.Fatal("标记为真时应报就绪")
	}
}

// 编译期断言：关闭渲染时注入的实现满足插件依赖的渲染接口。
var _ render.Renderer = disabledRenderer{}

// TestBuildRendererDisabledFailsEveryCard 验证关闭渲染时注入的是「必定失败」的实现：
// 卡片命令据此回退纯文本，而且不会去启动浏览器。
func TestBuildRendererDisabledFailsEveryCard(t *testing.T) {
	cfg := &config.Config{} // render.enabled 缺省为 false
	r, err := buildRenderer(cfg)
	if err != nil {
		t.Fatalf("关闭渲染不应报错：%v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("关闭渲染器不应报错：%v", err)
	}
	if _, err := r.Render(context.Background(), render.Card{Name: "war"}); err == nil {
		t.Fatal("关闭渲染时任何卡片都必须渲染失败，命令才会回退纯文本")
	}
}

// TestBuildRendererEnabledUsesConfig 验证启用渲染时按配置构造引擎，且 format 非法时报错而不是静默兜底。
func TestBuildRendererEnabledUsesConfig(t *testing.T) {
	cfg := &config.Config{}
	cfg.Render.Enabled = true
	cfg.Render.Width = 720
	cfg.Render.Scale = 1
	cfg.Render.Timeout = 5
	cfg.Render.Format = "PNG"

	r, err := buildRenderer(cfg)
	if err != nil {
		t.Fatalf("按配置构造渲染引擎失败：%v", err)
	}
	// 只构造不渲染：引擎懒启动，这里不应该起浏览器进程。
	if err := r.Close(); err != nil {
		t.Fatalf("关闭渲染引擎失败：%v", err)
	}

	cfg.Render.Format = "bmp"
	if _, err := buildRenderer(cfg); err == nil {
		t.Fatal("非法 format 应报错，而不是静默用默认格式")
	}
}

// TestShutdownStepsWithoutRenderer 校验渲染引擎还没构造出来时（启动早期失败路径）停机不会崩。
func TestShutdownStepsWithoutRenderer(t *testing.T) {
	captureLog(t)
	// renderer 留空表示「引擎还没构造出来」，此时停机不能崩。
	steps := shutdownSteps(shutdownDeps{
		markNotReady: func() {},
		cronStop:     func() {},
		server:       &fakeServer{},
		store:        &fakeStore{},
	})
	if err := shutdownAll(log.Printf, steps); err != nil {
		t.Fatalf("停机不应报错：%v", err)
	}
}

// fakeRegisterer 记录装配阶段实际注册了什么。
// 嵌入 nil 的 bot.Sender：注册动作不会用到发送能力，用例因此不用把六个发送方法都实现一遍。
type fakeRegisterer struct {
	bot.Sender
	handlers     []string
	inlinePrefix []string
}

// Register 记录注册的指令名。
func (f *fakeRegisterer) Register(handlers []bot.Handler) {
	for _, h := range handlers {
		f.handlers = append(f.handlers, h.Name)
	}
}

// RegisterInline 记录行内查询前缀。
func (f *fakeRegisterer) RegisterInline(prefix string, _ func(query tgbotapi.InlineQuery) error) {
	f.inlinePrefix = append(f.inlinePrefix, prefix)
}

// AnswerInline 满足 registerer 契约；装配用例不会触发行内应答。
func (f *fakeRegisterer) AnswerInline(string, []interface{}) error { return nil }

// TestRegisterPluginsRegistersCommandsAndInline 校验装配把命令与行内查询都交出去了，且前缀取自同一个参数：
// 少了行内那半，群里点 /planets 的按钮后搜不出任何东西；前缀不一致则 Telegram 根本不会把查询交过来。
func TestRegisterPluginsRegistersCommandsAndInline(t *testing.T) {
	fake := &fakeRegisterer{}
	registerPlugins(fake, nil, nil, time.UTC, translate.Passthrough(), nil, nil)

	if len(fake.handlers) == 0 {
		t.Fatal("装配应注册插件命令，实际一条都没注册")
	}
	// 七个前缀：星球来自 planets（前缀取自配置），其余六个来自 codex 的图鉴检索。
	// 这份清单写死在这里（不以实现为准）：加前缀时测试先红，逼着人同时更新帮助与文档。
	wantPrefix := []string{"星球-", "武器-", "战备-", "护甲-", "手雷-", "债券-", "敌人-"}
	if !equalStrings(fake.inlinePrefix, wantPrefix) {
		t.Fatalf("应注册七个行内前缀 %v，实际 %v", wantPrefix, fake.inlinePrefix)
	}
	// 行内结果的回填指令必须真的注册过，否则选中候选只会得到「未知指令」。
	for _, name := range fake.handlers {
		if name == "planet" {
			return
		}
	}
	t.Errorf("行内结果回填的 /planet 没被注册，实际注册了 %v", fake.handlers)
}

// TestPluginHandlersCoverAllHelpCommands 校验接线与帮助清单一致：帮助里列的命令 main 都注册了，
// 注册的命令也都写进了帮助。加了命令忘了写帮助（群里没人知道它存在）或写了帮助忘了接线
// （群里打了命令没反应）都会在这里被拦下。
func TestPluginHandlersCoverAllHelpCommands(t *testing.T) {
	registered := map[string]bool{}
	for _, h := range pluginHandlers(nil, nil, nil, nil, nil, nil, nil) {
		registered[h.Name] = true
	}
	for _, name := range system.CommandNames() {
		if !registered[name] {
			t.Errorf("帮助里列了 /%s，但 main 没有注册它", name)
		}
	}
	for name := range registered {
		documented := false
		for _, want := range system.CommandNames() {
			if want == name {
				documented = true
				break
			}
		}
		if !documented {
			t.Errorf("/%s 已注册但没写进帮助清单", name)
		}
	}
}

// ---------------------------------------------------------------------------
// Task 12：main 装配与提示性检查
//
// 这一段都在守「装配接线」：配置换算对不对、翻译层按什么语义分给插件与推送、
// 推送任务的群与预算有没有配对。这些地方错了都不会编译失败，
// 只会在群里表现为「静默失联」或「每张卡片都挂着一句假告警」。
// ---------------------------------------------------------------------------

// TestTranslateConfigMapping 校验配置段到翻译层参数的换算（秒 → time.Duration 是最容易写错的一处）。
func TestTranslateConfigMapping(t *testing.T) {
	cfg := &config.Config{}
	cfg.Translate.Enabled = true
	cfg.Translate.Mode = "auto"
	cfg.Translate.APIURL = "https://api.openai.com/v1"
	cfg.Translate.APIKey = "sk-x"
	cfg.Translate.Model = "gpt-4o-mini"
	cfg.Translate.TargetLang = "zh-CN"
	cfg.Translate.Timeout = 20
	cfg.Translate.MaxTokens = 1200
	cfg.Translate.MinInterval = 6
	cfg.Translate.ErrorCooldown = 120
	cfg.Translate.FreeAPIURL = "https://uapis.cn/api/v1/ai/translate"

	got := translateConfig(cfg)
	if got.Timeout != 20*time.Second {
		t.Errorf("timeout 换算错误：%v", got.Timeout)
	}
	if got.MinInterval != 6*time.Second || got.ErrorCooldown != 120*time.Second {
		t.Errorf("秒字段换算错误：min=%v cooldown=%v", got.MinInterval, got.ErrorCooldown)
	}
	if got.FreeAPIURL != cfg.Translate.FreeAPIURL || got.MaxTokens != 1200 {
		t.Errorf("字段映射错误：%+v", got)
	}
	if !got.Enabled || got.Mode != "auto" || got.APIURL != cfg.Translate.APIURL ||
		got.APIKey != "sk-x" || got.Model != "gpt-4o-mini" || got.TargetLang != "zh-CN" {
		t.Errorf("开关 / 模式 / 地址 / 凭据 / 模型 / 目标语言没有原样透传：%+v", got)
	}
}

// TestTranslateConfigKeepsDisabledFlag 校验关闭翻译时也照常映射：
// 零值配置必须得出「Enabled=false + 零时长」。把 Enabled 写死成 true，
// 或把没填的秒字段兜底成非零值，都会让「用户主动关掉翻译」在翻译层里变成另一种状态。
func TestTranslateConfigKeepsDisabledFlag(t *testing.T) {
	got := translateConfig(&config.Config{})
	if got.Enabled {
		t.Fatalf("关闭状态必须透传，实际 Enabled=true：%+v", got)
	}
	if got.Timeout != 0 || got.MinInterval != 0 || got.ErrorCooldown != 0 {
		t.Fatalf("秒字段的零值应换算成零时长（兜底交给 translate.New），实际 timeout=%v min=%v cooldown=%v",
			got.Timeout, got.MinInterval, got.ErrorCooldown)
	}
}

// TestNewTranslatorDisabledReturnsPassthrough 校验关闭翻译时构造出来的是「原样返回」的透传实现：
// 它不发任何请求，所以装配阶段不必为它准备后端。
//
// 这里纠正了计划里的一句错注释（原话是「插件与推送都不用判断配置」）：透传实现**不能**直接交给
// 插件与推送 —— 它们把「译文与原文逐字相同」判成「这一段没翻动」，于是每张卡片都会挂一句假的
// 「翻译暂不可用」。真正的装配口径见 pluginTranslator：用户主动关掉时给的是 nil。
func TestNewTranslatorDisabledReturnsPassthrough(t *testing.T) {
	captureLog(t) // translate.New 会记一行「翻译层已关闭」，收进缓冲区免得刷屏
	cfg := &config.Config{}
	cfg.Translate.Enabled = false
	tr, err := newTranslator(cfg, nil)
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	if tr == nil {
		t.Fatal("translate.New 返回的是非 nil 接口：nil 在本项目里是「不翻译也不注明」的哨兵值，两者不能混用")
	}
	got, err := tr.Translate(context.Background(), []string{"Hold the line"})
	if err != nil || got[0] != "Hold the line" {
		t.Fatalf("关闭翻译时应原样返回：%v err=%v", got, err)
	}

	// 前提守护：Passthrough() 同样是「非 nil 且逐字回显」，这正是「透传不等于关闭翻译」的原因。
	pt := translate.Passthrough()
	if pt == nil {
		t.Fatal("translate.Passthrough() 不该返回 nil")
	}
	got, err = pt.Translate(context.Background(), []string{"Hold the line"})
	if err != nil || got[0] != "Hold the line" {
		t.Fatalf("透传实现必须逐字回显：%v err=%v", got, err)
	}
}

// TestNewTranslatorWiresCacheStore 校验状态文件真的接到了翻译层的缓存上（构造日志里写明 缓存=true/false）。
// 这一步静默丢掉的后果是「每次重启都把同一批正文重新送译一遍」——白烧额度，不翻日志根本看不出来。
func TestNewTranslatorWiresCacheStore(t *testing.T) {
	cfg := &config.Config{}
	cfg.Translate.Enabled = true
	cfg.Translate.Mode = "openai"
	cfg.Translate.APIURL = "https://api.example.com/v1"
	cfg.Translate.APIKey = "sk-x"
	cfg.Translate.Model = "gpt-4o-mini"
	cfg.Translate.TargetLang = "zh-CN"
	cfg.Translate.Timeout = 20

	// 没有状态文件（测试路径）：缓存关闭。
	logs := captureLog(t)
	tr, err := newTranslator(cfg, nil)
	if err != nil {
		t.Fatalf("构造翻译层失败：%v", err)
	}
	if tr == nil {
		t.Fatal("配了 OpenAI 兼容后端时应拿到真正的翻译层，而不是 nil")
	}
	if !strings.Contains(logs.String(), "缓存=false") {
		t.Fatalf("没有状态文件时日志应说明缓存关闭，实际：%s", logs.String())
	}

	// 有状态文件：缓存打开。
	store, err := state.Open(filepath.Join(t.TempDir(), "translate.db"))
	if err != nil {
		t.Fatalf("打开状态文件失败：%v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	logs.Reset()
	if _, err := newTranslator(cfg, store); err != nil {
		t.Fatalf("构造翻译层失败：%v", err)
	}
	if !strings.Contains(logs.String(), "缓存=true") {
		t.Fatalf("有状态文件时日志应说明缓存开启，实际：%s", logs.String())
	}
}

// TestPluginTranslatorNilWhenTranslationDisabled 守护装配口径：
// 用户主动关掉翻译时交给插件与推送的必须是 nil（语义是「不翻译也不注明」），
// 而不是 translate.New 的透传实现 —— 透传会被下游判成「没翻动」，每张卡都挂假的「翻译暂不可用」。
// 开着翻译时必须原样透传同一个实例：包一层壳会让下游拿不到真正的缓存与限速。
func TestPluginTranslatorNilWhenTranslationDisabled(t *testing.T) {
	tr := &plugtest.Translator{}

	disabled := &config.Config{}
	disabled.Translate.Enabled = false
	if got := pluginTranslator(disabled, tr); got != nil {
		t.Fatalf("关闭翻译时必须给插件 nil，实际 %T", got)
	}

	enabled := &config.Config{}
	enabled.Translate.Enabled = true
	if got := pluginTranslator(enabled, tr); got != translate.Translator(tr) {
		t.Fatalf("开着翻译时必须原样透传同一个实例，实际 %T", got)
	}
}

// TestPluginTranslatorNilWhenNoUsableBackend 是本轮修复的核心用例。
//
// 触发条件很现实：用户配了 translate.enabled=true 与 mode=openai，却忘了填 api_url
// （或把 free_api_url 清空、写错）。这时 translate.New 返回「逐字回显」的透传实现，
// 日志里写的是「翻译整体停用，正文保持英文」；只判 Enabled 的装配会把它交给插件，
// 于是每张卡片都挂一句假的「翻译暂不可用」——两处说法自相矛盾。
// 装配因此必须把这两种兜底情况（用户关掉、或选不出后端）一起归一成 nil。
func TestPluginTranslatorNilWhenNoUsableBackend(t *testing.T) {
	logs := captureLog(t)
	cfg := &config.Config{}
	cfg.Translate.Enabled = true
	cfg.Translate.Mode = "openai"
	cfg.Translate.APIURL = "" // 忘填地址：真实触发条件
	cfg.Translate.Timeout = 20

	trans, err := newTranslator(cfg, nil)
	if err != nil {
		t.Fatalf("构造翻译层失败：%v", err)
	}
	if !translate.IsPassthrough(trans) {
		t.Fatalf("没有可用后端时构造出来的应是透传实现，实际 %T", trans)
	}
	if !strings.Contains(logs.String(), "翻译整体停用") {
		t.Fatalf("日志应说明「翻译整体停用」（这句与卡片文案必须一致），实际：%s", logs.String())
	}
	if got := pluginTranslator(cfg, trans); got != nil {
		t.Fatalf("开着翻译却没有可用后端时也必须给插件 nil，实际 %T", got)
	}
}

// TestPluginHandlersWithNoUsableBackendKeepsCardsClean 是与上一条配套的端到端用例：
// 用真插件（真数据层 + 本地假上游）跑一遍 /dispatches，回复里不能出现「翻译暂不可用」。
// 只做结构断言（拿到 nil）不够——真正要守住的是「群里看到的那一行文案」。
func TestPluginHandlersWithNoUsableBackendKeepsCardsClean(t *testing.T) {
	up := &testUpstream{owners: []string{"Humans"}}
	svc := newTestService(t, up)

	cfg := &config.Config{}
	cfg.Translate.Enabled = true
	cfg.Translate.Mode = "openai"
	cfg.Translate.APIURL = ""
	cfg.Translate.Timeout = 20
	captureLog(t) // translate.New 会记一行「翻译整体停用」，收进缓冲区免得刷屏
	trans, err := newTranslator(cfg, nil)
	if err != nil {
		t.Fatalf("构造翻译层失败：%v", err)
	}

	s := &wiringSender{Sender: &plugtest.Sender{GroupID: -100}}
	handlers := pluginHandlers(s, svc, nil, time.UTC, pluginTranslator(cfg, trans), nil, nil)
	if err := plugtest.RunHandler(t, handlers, "dispatches", plugtest.MessageUpdate(-100, "/dispatches")); err != nil {
		t.Fatalf("执行 /dispatches 失败：%v", err)
	}
	if len(s.Replies) != 1 {
		t.Fatalf("应回一条文本（没有渲染引擎时回退文本），实际 %v", s.Calls)
	}
	if strings.Contains(strings.Join(s.Replies, "\n"), "翻译暂不可用") {
		t.Fatalf("「开着翻译但没有可用后端」属于「不翻译」而不是故障，回复里不该有翻译说明：\n%s", s.Replies[0])
	}
}

// TestRunWiresPluginTranslatorThroughHelper 是一条**结构性**守卫。
//
// run() 要读真配置、连真 Telegram 客户端，单测进不去；但「run 里必须经 pluginTranslator 再分发」
// 这件事一旦被改回去（直接把 trans 传给插件），translate.enabled=false 的部署就会每张卡片挂上
// 假的「翻译暂不可用」。与其为了可测把 run() 拆一层，这里直接读源码钉住这个调用点，
// 改动更小、也不动生产路径。
func TestRunWiresPluginTranslatorThroughHelper(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("读取 main.go 失败：%v", err)
	}
	if !strings.Contains(string(source), "pluginTrans := pluginTranslator(cfg, trans)") {
		t.Fatal("run() 必须用 pluginTranslator(cfg, trans) 决定交给插件与推送的翻译层")
	}
}

// TestWarnIfCacheTTLTooLong 校验缓存 TTL 提示性检查：只警告、不阻止启动，且默认配置不产生噪声。
func TestWarnIfCacheTTLTooLong(t *testing.T) {
	// 注意：这里必须把参数渲染进日志行。计划里只收集 format 字符串，
	// 而字段名（cache.planets_ttl）是作为参数传进去的，那样断言永远不可能成立。
	var lines []string
	logf := func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }

	// 默认配置：三个端点的 TTL 都小于 5 分钟，不该有提示
	def := &config.Config{}
	def.Push.Enabled = true
	def.Cache.PlanetsTTL = 60
	def.Cache.CampaignsTTL = 120
	def.Cache.StationsTTL = 120
	def.Cache.DispatchesTTL = 600 // 简报 TTL 长只会晚一轮发现，不算漏检，刻意不参与检查
	warnIfCacheTTLTooLong(def, logf)
	if len(lines) != 0 {
		t.Fatalf("默认配置不该产生提示，实际 %v", lines)
	}

	// 把星球 TTL 调到 10 分钟：应当提示，且提示里带字段名
	bad := &config.Config{}
	bad.Push.Enabled = true
	bad.Cache.PlanetsTTL = 600
	warnIfCacheTTLTooLong(bad, logf)
	if len(lines) == 0 || !strings.Contains(lines[0], "cache.planets_ttl") {
		t.Fatalf("TTL 过长时应提示，实际 %v", lines)
	}

	// 推送关闭时不检查（用户就是不想让它轮询）
	lines = nil
	bad.Push.Enabled = false
	warnIfCacheTTLTooLong(bad, logf)
	if len(lines) != 0 {
		t.Fatalf("推送关闭时不该提示，实际 %v", lines)
	}

	// 阈值边界：正好等于「推送轮询间隔」就要提示（判据是 >=，不是 >）
	lines = nil
	edge := &config.Config{}
	edge.Push.Enabled = true
	edge.Cache.CampaignsTTL = config.Second(int(pushIntervalCeiling / time.Second))
	warnIfCacheTTLTooLong(edge, logf)
	if len(lines) != 1 || !strings.Contains(lines[0], "cache.campaigns_ttl") {
		t.Fatalf("TTL 正好等于阈值时应提示一次，实际 %v", lines)
	}

	// 边界再差一秒就不提示：把 >= 收成 > 会让「刚好等于阈值」的漏检溜过去
	lines = nil
	edge.Cache.CampaignsTTL = config.Second(int(pushIntervalCeiling/time.Second) - 1)
	warnIfCacheTTLTooLong(edge, logf)
	if len(lines) != 0 {
		t.Fatalf("低于阈值时不该提示，实际 %v", lines)
	}
}

// TestBuildPushJobs 校验推送任务的装配：开关关闭时没有任务，打开时按配置的表达式注册一个。
func TestBuildPushJobs(t *testing.T) {
	cfg := &config.Config{}
	cfg.Push.Enabled = true
	cfg.Push.Cron = "15 */5 * * * *"
	cfg.Push.MaxItems = 5
	cfg.Bot.GroupID = -100

	jobs := buildPushJobs(cfg, nil, nil, &plugtest.Sender{}, nil, translate.Passthrough(), time.UTC, func(string, ...any) {})
	if len(jobs) != 1 {
		t.Fatalf("应注册 1 个推送任务，实际 %d", len(jobs))
	}
	if jobs[0].Spec != "15 */5 * * * *" || jobs[0].Name == "" || jobs[0].Run == nil {
		t.Fatalf("推送任务字段不完整：%+v", jobs[0])
	}

	cfg.Push.Enabled = false
	if jobs := buildPushJobs(cfg, nil, nil, &plugtest.Sender{}, nil, translate.Passthrough(), time.UTC, func(string, ...any) {}); len(jobs) != 0 {
		t.Fatalf("关闭推送时不该注册任务，实际 %d", len(jobs))
	}
}

// TestBuildPushJobsUsesConfiguredGroupAndMaxItems 用真编排（真 *hd2.Service + 本地假上游 + 真状态文件）
// 验证推送任务把配置用在了对的地方：发到配置里的群、卡片按 max_items 截断并注明剩余条数。
// 只断言「注册了一个任务」不够：群 id 与 max_items 配错时任务照样注册，
// 群里却是「静默失联」或「一屏刷不完」。
func TestBuildPushJobsUsesConfiguredGroupAndMaxItems(t *testing.T) {
	cfg := &config.Config{}
	cfg.Push.Enabled = true
	cfg.Push.Cron = "15 */5 * * * *"
	cfg.Push.MaxItems = 2
	cfg.Bot.GroupID = -100777

	up := &testUpstream{owners: sixOwners("Humans")}
	svc := newTestService(t, up)

	store, err := state.Open(filepath.Join(t.TempDir(), "push.db"))
	if err != nil {
		t.Fatalf("打开状态文件失败：%v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	sender := &plugtest.Sender{GroupID: cfg.Bot.GroupID}
	var lines []string
	logf := func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }

	jobs := buildPushJobs(cfg, svc, store, sender, nil, nil, time.UTC, logf)
	if len(jobs) != 1 {
		t.Fatalf("应注册 1 个推送任务，实际 %d", len(jobs))
	}

	// 第一轮：没有基线，只写基线不推送（宁可不推，也不把现状当新闻播一遍）。
	jobs[0].Run()
	if len(sender.Calls) != 0 {
		t.Fatalf("首轮不该推送，实际 %v，日志 %v", sender.Calls, lines)
	}

	// 第二轮：六颗星球全部易主，条数超过 max_items=2。
	up.setOwners(sixOwners("Terminids")...)
	jobs[0].Run()
	if len(sender.Replies) != 1 {
		t.Fatalf("第二轮应发一条文本（没有渲染引擎时回退文本），实际调用 %v，日志 %v", sender.Calls, lines)
	}
	if sender.Chats[0] != cfg.Bot.GroupID {
		t.Fatalf("推送应发到配置里的群 %d，实际 %d", cfg.Bot.GroupID, sender.Chats[0])
	}
	if !strings.Contains(sender.Replies[0], "另有 4 条未显示") {
		t.Fatalf("max_items=2 时应只列 2 条并注明剩余 4 条：\n%s", sender.Replies[0])
	}
}

// TestPushTimeoutFitsInsideInterval 钉住「一轮预算必须明显小于轮询间隔」这条不变式：
// 预算比间隔还长的话，上一轮没跑完下一轮就叠上来了（cron 不会替我们串行化同一任务）。
func TestPushTimeoutFitsInsideInterval(t *testing.T) {
	if pushTimeout <= 0 {
		t.Fatalf("一轮推送的预算必须为正，实际 %v", pushTimeout)
	}
	if pushTimeout >= pushIntervalCeiling {
		t.Fatalf("一轮预算（%v）必须明显小于轮询间隔（%v），否则两轮会叠在一起",
			pushTimeout, pushIntervalCeiling)
	}
}

// TestBuildPushJobsPassesTranslatorToCollector 校验推送任务把翻译层交给了编排。
//
// 少了这一步，简报正文会以英文推到群里，而且**不会**注明「翻译暂不可用」
// （nil 的语义是「用户自己关了翻译」），从群里的表现看与真关了翻译一模一样，
// 只有这条用例能把「忘了接翻译层」和「用户主动关闭」区分开。
func TestBuildPushJobsPassesTranslatorToCollector(t *testing.T) {
	cfg := &config.Config{}
	cfg.Push.Enabled = true
	cfg.Push.Cron = "15 */5 * * * *"
	cfg.Push.MaxItems = 5
	cfg.Bot.GroupID = -100777

	up := &testUpstream{owners: []string{"Humans"}}
	svc := newTestService(t, up)

	store, err := state.Open(filepath.Join(t.TempDir(), "push-translate.db"))
	if err != nil {
		t.Fatalf("打开状态文件失败：%v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	sender := &plugtest.Sender{GroupID: cfg.Bot.GroupID}
	trans := &plugtest.Translator{Out: []string{
		"新指令：守住阿卡马尔IV",
		"绝地潜兵，守住阿卡马尔IV。终结族的虫群正在前线集结。",
	}}
	jobs := buildPushJobs(cfg, svc, store, sender, nil, trans, time.UTC, func(string, ...any) {})
	if len(jobs) != 1 {
		t.Fatalf("应注册 1 个推送任务，实际 %d", len(jobs))
	}

	jobs[0].Run() // 第一轮：写基线
	if trans.Calls() != 0 {
		t.Fatalf("首轮没有变化，不该送译，实际 %d 次", trans.Calls())
	}

	// 第二轮：上游发了一条 id 更大、发布时间更晚的新简报 → 应当被翻译后推送。
	up.setDispatches([]hd2.Dispatch{{
		ID: 2, Type: 1, Published: time.Date(2026, 9, 16, 13, 0, 0, 0, time.UTC),
		Message: "New major order: defend Acamar IV against the Terminid swarm.",
	}})
	jobs[0].Run()

	if trans.Calls() == 0 {
		t.Fatal("新简报的标题与正文都要送译，翻译层一次都没被调用")
	}
	reply := strings.Join(sender.Replies, "\n")
	if !strings.Contains(reply, "新指令") {
		t.Fatalf("译文应回填到推送内容里，实际：\n%s", reply)
	}
}

// TestPushJobAppliesTimeoutBudget 校验 pushJob 把「一轮推送的总预算」真的变成了 ctx 截止时间。
//
// 为什么必须自控超时：本仓库用的 robfig cron 每个 tick 新开一个 goroutine，
// 同一任务的多次触发会自我重叠（没有任何 chain 包装替我们串行化）。
// 预算没传下去的话，上一轮还在取数出图，下一轮就会叠上来。
func TestPushJobAppliesTimeoutBudget(t *testing.T) {
	probe := &deadlineProbeService{}
	collector := push.New(push.Config{ChatID: -100}, push.Deps{
		Service: probe,
		Store:   &noopPushStore{},
		Sender:  &plugtest.Sender{},
		Logger:  func(string, ...any) {},
	})

	job := pushJob("15 */5 * * * *", collector, 90*time.Second, func(string, ...any) {})
	job.Run()

	probe.mu.Lock()
	deadline, hasDead := probe.deadline, probe.hasDead
	probe.mu.Unlock()
	if !hasDead {
		t.Fatal("推送轮询的 ctx 必须带截止时间，否则一轮跑不完就会叠上下一轮")
	}
	if left := time.Until(deadline); left <= 85*time.Second || left > 90*time.Second {
		t.Fatalf("预算应为 90s，实际剩余 %v", left)
	}
}

// TestPushJobLogsFailure 校验推送轮的失败被记进日志（cron 任务没有返回错误的通道）。
func TestPushJobLogsFailure(t *testing.T) {
	var lines []string
	logf := func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	collector := push.New(push.Config{ChatID: -100}, push.Deps{
		Service: &failingPushService{},
		Store:   &noopPushStore{},
		Sender:  &plugtest.Sender{},
		Logger:  logf,
	})
	job := pushJob("15 */5 * * * *", collector, time.Second, logf)
	job.Run()
	if len(lines) == 0 {
		t.Fatal("推送失败时应记日志")
	}
	if !strings.Contains(strings.Join(lines, "\n"), "战况推送失败") {
		t.Fatalf("日志里应有「战况推送失败」这样的可搜索前缀，实际 %v", lines)
	}
}

// TestPushJobQuietOnSuccess 校验轮询成功时 pushJob 不写失败日志：
// 失败前缀必须只在真失败时出现，否则日志里全是噪声，真出问题时反而看不见。
func TestPushJobQuietOnSuccess(t *testing.T) {
	var lines []string
	logf := func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	collector := push.New(push.Config{ChatID: -100}, push.Deps{
		Service: &deadlineProbeService{},
		Store:   &noopPushStore{},
		Sender:  &plugtest.Sender{},
		Logger:  logf,
	})
	job := pushJob("15 */5 * * * *", collector, time.Second, logf)
	job.Run()
	if strings.Contains(strings.Join(lines, "\n"), "战况推送失败") {
		t.Fatalf("成功轮询不该写失败日志，实际 %v", lines)
	}
}

// failingPushService 让四个端点全部报错，用来验证「上游失败只记日志、不发消息」。
type failingPushService struct{}

// Planets 固定报错。
func (f *failingPushService) Planets(context.Context) (hd2.Result[[]hd2.Planet], error) {
	return hd2.Result[[]hd2.Planet]{}, errors.New("上游不可用")
}

// Campaigns 固定报错。
func (f *failingPushService) Campaigns(context.Context) (hd2.Result[[]hd2.Campaign], error) {
	return hd2.Result[[]hd2.Campaign]{}, errors.New("上游不可用")
}

// Dispatches 固定报错。
func (f *failingPushService) Dispatches(context.Context) (hd2.Result[[]hd2.Dispatch], error) {
	return hd2.Result[[]hd2.Dispatch]{}, errors.New("上游不可用")
}

// Stations 固定报错。
func (f *failingPushService) Stations(context.Context) (hd2.Result[[]hd2.SpaceStation], error) {
	return hd2.Result[[]hd2.SpaceStation]{}, errors.New("上游不可用")
}

// noopPushStore 是只实现接口、不做任何事的假存储。
type noopPushStore struct{}

// PutSnapshot 什么都不写。
func (n *noopPushStore) PutSnapshot(string, []byte, time.Time) error { return nil }

// GetSnapshot 永远报「没有基线」，让每轮都按首次运行处理。
func (n *noopPushStore) GetSnapshot(string) ([]byte, time.Time, bool, error) {
	return nil, time.Time{}, false, nil
}

// MarkPushed 永远返回「首次推送」。
func (n *noopPushStore) MarkPushed(string, string, time.Time) (bool, error) { return true, nil }

// deadlineProbeService 记录取数时拿到的 ctx 截止时间：
// 用来证明 pushJob 真的把超时预算传了下去（其余三个端点返回空数据）。
type deadlineProbeService struct {
	mu       sync.Mutex
	deadline time.Time
	hasDead  bool
}

// record 记下 ctx 的截止时间。
func (d *deadlineProbeService) record(ctx context.Context) {
	deadline, ok := ctx.Deadline()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.deadline, d.hasDead = deadline, ok
}

// Planets 记一次截止时间并返回一颗星球。
func (d *deadlineProbeService) Planets(ctx context.Context) (hd2.Result[[]hd2.Planet], error) {
	d.record(ctx)
	return hd2.Result[[]hd2.Planet]{
		Value:     []hd2.Planet{{Index: 1, Name: "Probe", CurrentOwner: "Humans"}},
		FetchedAt: time.Now(),
	}, nil
}

// Campaigns 返回空列表：探测用例只关心取数时的 ctx。
func (d *deadlineProbeService) Campaigns(context.Context) (hd2.Result[[]hd2.Campaign], error) {
	return hd2.Result[[]hd2.Campaign]{Value: []hd2.Campaign{}, FetchedAt: time.Now()}, nil
}

// Dispatches 返回空列表。
func (d *deadlineProbeService) Dispatches(context.Context) (hd2.Result[[]hd2.Dispatch], error) {
	return hd2.Result[[]hd2.Dispatch]{Value: []hd2.Dispatch{}, FetchedAt: time.Now()}, nil
}

// Stations 返回空列表。
func (d *deadlineProbeService) Stations(context.Context) (hd2.Result[[]hd2.SpaceStation], error) {
	return hd2.Result[[]hd2.SpaceStation]{Value: []hd2.SpaceStation{}, FetchedAt: time.Now()}, nil
}

// sixOwners 造 6 个控制方：让易主事件数确定地超过 max_items，断言不依赖上游真实数据。
func sixOwners(owner string) []string {
	owners := make([]string, 6)
	for i := range owners {
		owners[i] = owner
	}
	return owners
}

// ---------------------------------------------------------------------------
// 装配集成用例的脚手架：本地假上游 + 真数据层 + 能当 bot.Sender 用的假发送方。
// ---------------------------------------------------------------------------

// testUpstream 是本地假上游：只实现装配用例要用的端点，其余一律 404
// （插件对拿不到的端点只记一条日志，正好证明「上游缺数据」不影响接线判断）。
// 星球控制方可以在两轮之间改，易主事件就是这么造出来的。
type testUpstream struct {
	mu     sync.Mutex
	owners []string
	// dispatches 为 nil 时返回默认那条英文简报；用例可以换掉它来造「新简报」事件。
	dispatches []hd2.Dispatch
}

// setOwners 覆盖 /planets 返回的控制方列表。
func (u *testUpstream) setOwners(owners ...string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.owners = owners
}

// setDispatches 覆盖 /dispatches 返回的简报列表。
func (u *testUpstream) setDispatches(list []hd2.Dispatch) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.dispatches = list
}

// defaultDispatch 是假上游默认返回的那条英文简报。
func defaultDispatch() hd2.Dispatch {
	return hd2.Dispatch{
		ID: 1, Type: 1, Published: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC),
		Message: "Helldivers, hold the line on Acamar IV. The Terminid swarm is massing at the front.",
	}
}

// ServeHTTP 按路径返回数据：/planets 造星球（带群系与环境危害），/dispatches 造一条英文简报。
func (u *testUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.mu.Lock()
	owners := append([]string(nil), u.owners...)
	dispatches := append([]hd2.Dispatch(nil), u.dispatches...)
	u.mu.Unlock()
	if dispatches == nil {
		dispatches = []hd2.Dispatch{defaultDispatch()}
	}

	switch r.URL.Path {
	case "/planets":
		planets := make([]hd2.Planet, 0, len(owners))
		for i, owner := range owners {
			planets = append(planets, hd2.Planet{
				Index: i + 1, Name: "Test Planet " + strconv.Itoa(i+1), Sector: "Test",
				CurrentOwner: owner, InitialOwner: "Humans",
				Health: 1000000, MaxHealth: 1000000,
				Biome:   hd2.Biome{Name: "Deciduous Forest", Description: "Temperate woodlands with long, mild seasons."},
				Hazards: []hd2.Hazard{{Name: "Acid Storms", Description: "Corrosive rain damages armor over time."}},
			})
		}
		writeJSON(w, planets)
	case "/campaigns":
		// 一轮推送要求四个端点都成功，战役列表给空数组即可（用例只关心易主事件）。
		writeJSON(w, []hd2.Campaign{})
	case "/dispatches":
		writeJSON(w, dispatches)
	case "/api/v2/space-stations":
		// 空间站端点带版本前缀（上游已下线 /api/v1 的那条），给空数组。
		writeJSON(w, []hd2.SpaceStation{})
	default:
		http.NotFound(w, r)
	}
}

// writeJSON 写出 JSON 响应；编码失败只可能是用例自己的数据有问题，直接 500 让断言失败。
func writeJSON(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
	}
}

// newTestService 构造一个指向本地假上游的 *hd2.Service。
//
// 为什么不用假数据层：插件与 buildPushJobs 要的是 *hd2.Service（不是接口），
// 要让它们真跑一遍命令，只能给一个真数据层 + 本地 HTTP 服务替掉上游。
// TTL 全为 0（不缓存），每次调用都会重新取数，用例可以按轮次换数据。
func newTestService(t *testing.T, handler http.Handler) *hd2.Service {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client := hd2.NewClient(hd2.ClientConfig{
		BaseURL:       server.URL,
		Timeout:       2 * time.Second,
		Client:        "hd2_bot",
		Contact:       "test@example.com",
		UserAgent:     "hd2_bot/test",
		RetryMax:      1,
		RetryBase:     time.Millisecond,
		RetryMaxDelay: 5 * time.Millisecond,
		Logger:        func(string, ...any) {},
	}, nil)
	return hd2.NewService(hd2.ServiceConfig{Client: client, Logger: func(string, ...any) {}})
}

// wiringSender 让 plugtest.Sender 满足 bot.Sender：插件构造函数要的是 bot.Sender，
// 而公共替身缺一个 SendChatAction（它被多个包共用，不能为了让装配用例编译而改它）。
type wiringSender struct{ *plugtest.Sender }

// SendChatAction 满足契约；装配用例关心的是「翻译层有没有被用上」，状态提示不参与断言。
func (wiringSender) SendChatAction(int64, string) {}

// TestPluginHandlersShareOneTranslator 校验装配把同一个翻译层交给了 planets 与 orders：
// 两个插件各跑一遍命令，记录型替身必须收到两次调用（一次环境信息、一次简报正文）。
// 两种错法都会被抓到：漏传（写死 nil）少一次调用；各传一个不同的实现则这个替身一次都收不到。
// 顺带钉住「处理器数量与帮助清单一致」，防止加命令时漏接一条。
func TestPluginHandlersShareOneTranslator(t *testing.T) {
	up := &testUpstream{owners: []string{"Humans"}}
	svc := newTestService(t, up)
	trans := &plugtest.Translator{Out: []string{
		"绝地潜兵，守住阿卡马尔IV。终结族的虫群正在前线集结。",
		"落叶林", "温带林地", "酸雨风暴", "腐蚀性降雨会损坏护甲。",
	}}
	s := &wiringSender{Sender: &plugtest.Sender{GroupID: -100}}

	handlers := pluginHandlers(s, svc, nil, time.UTC, trans, nil, nil)
	if len(handlers) != len(system.CommandNames()) {
		t.Fatalf("装配的指令数应与帮助清单一致：%d != %d", len(handlers), len(system.CommandNames()))
	}

	if err := plugtest.RunHandler(t, handlers, "dispatches", plugtest.MessageUpdate(-100, "/dispatches")); err != nil {
		t.Fatalf("执行 /dispatches 失败：%v", err)
	}
	if err := plugtest.RunHandler(t, handlers, "planet", plugtest.MessageUpdate(-100, "/planet 1")); err != nil {
		t.Fatalf("执行 /planet 失败：%v", err)
	}

	if trans.Calls() != 2 {
		t.Fatalf("/dispatches 与 /planet 应各送译一次、且共用同一个翻译层，实际 %d 次：%v", trans.Calls(), trans.Texts)
	}
	replies := strings.Join(s.Replies, "\n")
	for _, want := range []string{"绝地潜兵", "落叶林", "酸雨风暴"} {
		if !strings.Contains(replies, want) {
			t.Errorf("回复里应出现译文 %q（说明翻译层真的被用上了）：\n%s", want, replies)
		}
	}
}

// TestPluginHandlersWithPassthroughLooksLikeFailure 用真插件钉住「透传 ≠ 关闭翻译」这条前提：
// 同一份英文简报，交 nil 时正文原样保留、不注明；交 translate.Passthrough() 时会被判成
// 「没翻动」，于是多出一句翻译不可用的说明。装配因此必须经 pluginTranslator 给 nil。
func TestPluginHandlersWithPassthroughLooksLikeFailure(t *testing.T) {
	up := &testUpstream{owners: []string{"Humans"}}
	svc := newTestService(t, up)

	run := func(trans translate.Translator) string {
		s := &wiringSender{Sender: &plugtest.Sender{GroupID: -100}}
		handlers := pluginHandlers(s, svc, nil, time.UTC, trans, nil, nil)
		if err := plugtest.RunHandler(t, handlers, "dispatches", plugtest.MessageUpdate(-100, "/dispatches")); err != nil {
			t.Fatalf("执行 /dispatches 失败：%v", err)
		}
		if len(s.Replies) != 1 {
			t.Fatalf("应回一条文本（没有渲染引擎时回退文本），实际 %v", s.Calls)
		}
		return s.Replies[0]
	}

	plain := run(nil)
	echoed := run(translate.Passthrough())
	if strings.Contains(plain, "翻译暂不可用") {
		t.Errorf("没配翻译层时不该写翻译不可用的说明：\n%s", plain)
	}
	if echoed == plain {
		t.Fatalf("透传实现必须与「不翻译」区分得开（它会被判成没翻动），两次输出却一模一样：\n%s", plain)
	}
}
