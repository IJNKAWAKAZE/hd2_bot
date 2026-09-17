package system

import (
	"bytes"
	"context"
	"errors"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/ijnkawakaze/telegram-bot-api"

	"hd2_bot/src/core/bot"
	"hd2_bot/src/plugins/plugutil/plugtest"
	"hd2_bot/src/render"
)

// 编译期断言：真实实现满足插件与接线所需的接口（main 装配时依赖它们）。
var (
	_ Sender          = (*bot.Bot)(nil)
	_ render.Renderer = (*render.Engine)(nil)
)

// runHandler 取出指定名字的处理器并执行。
// 假 Sender、假渲染器、日志捕获、更新构造与「按名字找处理器」都在 plugtest 里，
// 这里只留本插件的接线（Handlers 的参数是各插件自己的）。
func runHandler(t *testing.T, s *plugtest.Sender, r render.Renderer, name string, update tgbotapi.Update) error {
	t.Helper()
	return plugtest.RunHandler(t, Handlers(s, r), name, update)
}

// TestHandlersRegistered 校验 /help 已注册，且注册的指令都在帮助清单里。
func TestHandlersRegistered(t *testing.T) {
	names := map[string]bool{}
	for _, h := range Handlers(&plugtest.Sender{}, plugtest.FailRenderer()) {
		names[h.Name] = true
	}
	for _, want := range []string{"help"} {
		if !names[want] {
			t.Errorf("缺少 /%s 指令", want)
		}
	}
	// 注册了却没写进帮助清单的指令，群里没人知道它存在，这里直接拦住。
	documented := map[string]bool{}
	for _, name := range CommandNames() {
		documented[name] = true
	}
	for name := range names {
		if !documented[name] {
			t.Errorf("/%s 已注册但没写进帮助清单", name)
		}
	}
}

// TestHelpRendersPhoto 校验 /help 在有渲染引擎时出图：卡片名正确、视图模型带全部指令、不再补发文本。
func TestHelpRendersPhoto(t *testing.T) {
	r := &plugtest.Renderer{Img: []byte("假图片")}
	s := &plugtest.Sender{GroupID: -100}
	if err := runHandler(t, s, r, "help", plugtest.MessageUpdate(-100, "/help")); err != nil {
		t.Fatalf("执行 /help 失败：%v", err)
	}
	if len(s.Photos) != 1 {
		t.Fatalf("期望发出 1 张图，实际 %d 张", len(s.Photos))
	}
	if len(s.Replies) != 0 {
		t.Fatalf("出图成功不应再发文本：%v", s.Replies)
	}
	if s.PhotoChats[0] != -100 || s.PhotoReplyTo[0] != plugtest.DefaultMessageID {
		t.Errorf("图片目标或引用错误：chat=%d replyTo=%d", s.PhotoChats[0], s.PhotoReplyTo[0])
	}
	if len(r.Cards) != 1 || r.Cards[0].Name != helpCardName {
		t.Fatalf("卡片名应为 %q：%+v", helpCardName, r.Cards)
	}
	card, ok := r.Cards[0].Data.(HelpCard)
	if !ok {
		t.Fatalf("卡片数据应是 HelpCard，实际 %T", r.Cards[0].Data)
	}
	if len(card.Commands) != len(commands) {
		t.Errorf("卡片应列出全部 %d 条指令，实际 %d 条", len(commands), len(card.Commands))
	}
}

// TestHelpFallsBackToTextOnRenderFailure 校验渲染失败时回退文本，且文本与卡片口径一致（同样的指令清单）。
func TestHelpFallsBackToTextOnRenderFailure(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	if err := runHandler(t, s, plugtest.FailRenderer(), "help", plugtest.MessageUpdate(-100, "/help")); err != nil {
		t.Fatalf("执行 /help 失败：%v", err)
	}
	if len(s.Replies) != 1 {
		t.Fatalf("期望回退 1 条文本，实际 %d 条", len(s.Replies))
	}
	if len(s.Photos) != 0 {
		t.Fatalf("渲染失败不应发图：%d 张", len(s.Photos))
	}
	for _, c := range commands {
		if !strings.Contains(s.Replies[0], "/"+c.Name) {
			t.Errorf("回退文本缺少 /%s", c.Name)
		}
		if !strings.Contains(s.Replies[0], c.Example) {
			t.Errorf("回退文本缺少 /%s 的示例 %q", c.Name, c.Example)
		}
	}
}

// TestHelpFallsBackWithoutRenderer 校验未配置渲染引擎（renderer 为 nil）时同样回退文本，不 panic。
func TestHelpFallsBackWithoutRenderer(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	if err := runHandler(t, s, nil, "help", plugtest.MessageUpdate(-100, "/help")); err != nil {
		t.Fatalf("执行 /help 失败：%v", err)
	}
	if len(s.Replies) != 1 || !strings.Contains(s.Replies[0], "/stations") {
		t.Fatalf("未启用渲染时应回退文本：%v", s.Replies)
	}
}

// TestHelpRenderFailureIsLogged 校验渲染失败会记一条日志，且日志里带命令名与群 id：
// 只有命令名的话，线上多群并发时分不清是哪个群出的问题。
func TestHelpRenderFailureIsLogged(t *testing.T) {
	logs := plugtest.CaptureLog(t)
	s := &plugtest.Sender{GroupID: -100}
	if err := runHandler(t, s, plugtest.FailRenderer(), "help", plugtest.MessageUpdate(-100, "/help")); err != nil {
		t.Fatalf("执行 /help 失败：%v", err)
	}
	plugtest.AssertLogFields(t, logs, "/help 渲染失败", -100)
	if !strings.Contains(logs.String(), plugtest.RenderErr.Error()) {
		t.Errorf("日志应带上失败原因，实际：%s", logs.String())
	}
}

// TestHelpTextListsAllCommandsAndIsEscaped 校验文本回退列全指令、示例，且每个连字符都已转义（MarkdownV2 合法）。
func TestHelpTextListsAllCommandsAndIsEscaped(t *testing.T) {
	text := helpText()
	for _, c := range commands {
		if !strings.Contains(text, "/"+c.Name) {
			t.Errorf("帮助文本缺少 /%s：\n%s", c.Name, text)
		}
		if !strings.Contains(text, bot.Escape(c.Desc)) {
			t.Errorf("帮助文本缺少 /%s 的说明：\n%s", c.Name, text)
		}
		if !strings.Contains(text, bot.Escape(c.Example)) {
			t.Errorf("帮助文本缺少 /%s 的示例：\n%s", c.Name, text)
		}
	}
	// 逐个字符检查：只看「出现过 \-」会漏掉某个漏转义的连字符，那种消息会被 Telegram 整条拒收。
	runes := []rune(text)
	for i, r := range runes {
		if r == '-' && (i == 0 || runes[i-1] != '\\') {
			t.Fatalf("帮助文本第 %d 个字符处的连字符未转义：\n%s", i, text)
		}
	}
}

// TestHelpCardHTML 校验帮助卡排出了全部指令、说明与示例，且没有数据时间与过期角标（帮助是静态内容）。
func TestHelpCardHTML(t *testing.T) {
	html, err := render.HTML(render.Card{Name: helpCardName, Data: BuildHelpCard()})
	if err != nil {
		t.Fatalf("渲染帮助卡片 HTML 失败：%v", err)
	}
	for _, want := range []string{"指令一览", "全部指令", `class="card__emblem"`} {
		if !strings.Contains(html, want) {
			t.Errorf("帮助卡片 HTML 缺少 %q", want)
		}
	}
	for _, c := range commands {
		if !strings.Contains(html, "/"+c.Name) {
			t.Errorf("帮助卡片 HTML 缺少 /%s", c.Name)
		}
		if !strings.Contains(html, c.Example) {
			t.Errorf("帮助卡片 HTML 缺少 /%s 的示例 %q", c.Name, c.Example)
		}
	}
	if strings.Contains(html, "数据时间") {
		t.Error("帮助卡是静态内容，不该显示数据时间")
	}
	if strings.Contains(html, "数据可能已过期") {
		t.Error("帮助卡不该出现过期角标")
	}
}

// TestHelpCardHTMLEscapesText 校验卡片上的说明与示例被转义，宿主/上游文本不会注入 HTML。
func TestHelpCardHTMLEscapesText(t *testing.T) {
	injection := `<script>alert(1)</script>`
	card := BuildHelpCard()
	card.Commands = []HelpCommand{{Name: "war", Desc: injection, Example: "/war " + injection}}
	html, err := render.HTML(render.Card{Name: helpCardName, Data: card})
	if err != nil {
		t.Fatalf("渲染帮助卡片 HTML 失败：%v", err)
	}
	if strings.Contains(html, "<script>") {
		t.Error("说明与示例里的标签必须被转义")
	}
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Errorf("应保留转义后的文本：%s", html)
	}
}

// TestHelpCardListsAllCommands 校验卡片里的指令清单与 CommandNames 完全一致（同源，不会一边多一边少）。
func TestHelpCardListsAllCommands(t *testing.T) {
	card := BuildHelpCard()
	names := CommandNames()
	if len(card.Commands) != len(names) {
		t.Fatalf("卡片列出 %d 条，CommandNames 给出 %d 条", len(card.Commands), len(names))
	}
	for i, want := range names {
		if card.Commands[i].Name != want {
			t.Errorf("第 %d 条应为 %s，实际 %s", i+1, want, card.Commands[i].Name)
		}
		if card.Commands[i].Desc == "" || card.Commands[i].Example == "" {
			t.Errorf("/%s 缺少说明或示例：%+v", want, card.Commands[i])
		}
	}
}

// TestHandlersIgnoreOtherChat 校验非目标群的消息被忽略。
func TestHandlersIgnoreOtherChat(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	for _, name := range []string{"help"} {
		if err := runHandler(t, s, &plugtest.Renderer{Img: []byte("假图片")}, name, plugtest.MessageUpdate(-200, "/"+name)); err != nil {
			t.Fatalf("执行 /%s 失败：%v", name, err)
		}
	}
	if len(s.Replies) != 0 || len(s.Photos) != 0 {
		t.Fatalf("非目标群不应回复：%v / %d 张图", s.Replies, len(s.Photos))
	}
}

// TestHandlersIgnoreNonMessage 校验非文本消息更新被忽略。
func TestHandlersIgnoreNonMessage(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	update := tgbotapi.Update{CallbackQuery: &tgbotapi.CallbackQuery{ID: "1"}}
	for _, name := range []string{"help"} {
		if err := runHandler(t, s, &plugtest.Renderer{Img: []byte("假图片")}, name, update); err != nil {
			t.Fatalf("执行 /%s 失败：%v", name, err)
		}
	}
	if len(s.Replies) != 0 || len(s.Photos) != 0 {
		t.Fatalf("非消息更新不应回复：%v / %d 张图", s.Replies, len(s.Photos))
	}
}

// TestSendErrorPropagates 校验发送失败会把错误上抛给分发层记录日志（出图失败与回退文本两条路径都要）。
func TestSendErrorPropagates(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100, Err: errors.New("发送失败")}
	if err := runHandler(t, s, &plugtest.Renderer{Img: []byte("假图片")}, "help", plugtest.MessageUpdate(-100, "/help")); err == nil {
		t.Error("/help 发图失败应返回错误")
	}
	s = &plugtest.Sender{GroupID: -100, Err: errors.New("发送失败")}
	if err := runHandler(t, s, plugtest.FailRenderer(), "help", plugtest.MessageUpdate(-100, "/help")); err == nil {
		t.Error("/help 回退文本失败应返回错误")
	}
}

// TestHelpCardSmokeWithRealBrowser 是真浏览器冒烟：默认跳过，用 HD2_RENDER_SMOKE=1 打开。
// 除了看图，这里守住「卡片宽度 ≥ 800px」与「输出确实是 PNG」这两条基本的出图约定。
func TestHelpCardSmokeWithRealBrowser(t *testing.T) {
	if os.Getenv("HD2_RENDER_SMOKE") != "1" {
		t.Skip("未开启 HD2_RENDER_SMOKE，跳过真浏览器冒烟")
	}

	eng, err := render.New(render.Config{})
	if err != nil {
		t.Fatalf("构造渲染引擎失败：%v", err)
	}
	defer func() {
		if err := eng.Close(); err != nil {
			t.Errorf("关闭渲染引擎失败：%v", err)
		}
	}()

	start := time.Now()
	img, err := eng.Render(context.Background(), render.Card{Name: helpCardName, Data: BuildHelpCard()})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("渲染帮助卡失败：%v", err)
	}
	if !bytes.HasPrefix(img, []byte{0x89, 'P', 'N', 'G'}) {
		t.Fatalf("帮助卡应输出 PNG，实际开头 % x", img[:min(4, len(img))])
	}
	decoded, err := png.Decode(bytes.NewReader(img))
	if err != nil {
		t.Fatalf("帮助卡不是合法 PNG：%v", err)
	}
	width, height := decoded.Bounds().Dx(), decoded.Bounds().Dy()
	if width < 800 {
		t.Fatalf("帮助卡宽度应至少 800px，实际 %d", width)
	}
	out := filepath.Join(os.TempDir(), "hd2_help_card.png")
	if err := os.WriteFile(out, img, 0o600); err != nil {
		t.Fatalf("写出帮助卡失败：%v", err)
	}
	t.Logf("hd2_help_card.png：耗时 %s，尺寸 %dx%d，体积 %d 字节", elapsed, width, height, len(img))
}
