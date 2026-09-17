package war

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/ijnkawakaze/telegram-bot-api"

	"hd2_bot/src/core/bot"
	"hd2_bot/src/hd2"
	"hd2_bot/src/plugins/plugutil/plugtest"
	"hd2_bot/src/render"
)

// 编译期断言：真实实现满足插件所需的接口（接线依赖这三条）。
var (
	_ WarService      = (*hd2.Service)(nil)
	_ Sender          = (*bot.Bot)(nil)
	_ render.Renderer = (*render.Engine)(nil)
)

// fakeService 是 WarService 的假实现，可以精确模拟上游错误。
type fakeService struct {
	res hd2.Result[*hd2.War]
	err error
}

// War 返回预先设定的结果。
func (f *fakeService) War(ctx context.Context) (hd2.Result[*hd2.War], error) {
	return f.res, f.err
}

// runWar 取出 /war 处理器并执行，返回它的错误。
// 假 Sender、假渲染器、日志捕获、更新构造与「按名字找处理器」都在 plugtest 里，
// 这里只留 /war 自己的接线（Handlers 的参数是各插件自己的）。
func runWar(t *testing.T, s *plugtest.Sender, svc WarService, r render.Renderer, loc *time.Location, update tgbotapi.Update) error {
	t.Helper()
	return plugtest.RunHandler(t, Handlers(s, svc, r, loc), "war", update)
}

// TestWarCardViewModel 校验视图模型把战况格式化成卡片文案（千分位、成功率一位小数、数据时间）。
func TestWarCardViewModel(t *testing.T) {
	at := time.Date(2026, 9, 16, 20, 0, 0, 0, time.FixedZone("CST", 8*3600))
	view := BuildWarCard(testWar(), at, false)

	wants := []struct {
		field string
		got   string
		want  string
	}{
		{"标题", view.Title, "银河战况"},
		{"徽标", view.Emblem, "emblem.super_earth"},
		{"数据时间", view.DataTime, "2026-09-16 20:00:00"},
		{"在线士兵", view.PlayerCount, "23,162"},
		{"胜利场次", view.MissionsWon, "217,853,802"},
		{"失败场次", view.MissionsLost, "25,121,616"},
		{"任务成功率", view.SuccessRate, "89.7%"},
		{"终结族击杀", view.TerminidKills, "12,000,000"},
		{"机器人击杀", view.AutomatonKills, "0"},
		{"光能者击杀", view.IlluminateKills, "0"},
	}
	for _, w := range wants {
		if w.got != w.want {
			t.Errorf("%s 应为 %q，实际 %q", w.field, w.want, w.got)
		}
	}
	if view.Stale {
		t.Error("实时数据的视图模型不应标记过期")
	}
}

// TestWarCardViewModelStaleAndDefaults 校验过期标记透传，以及空数据与缺失字段的兜底。
func TestWarCardViewModelStaleAndDefaults(t *testing.T) {
	if stale := BuildWarCard(testWar(), time.Now(), true); !stale.Stale {
		t.Error("stale 必须原样透传到视图模型，卡片靠它显示过期角标")
	}

	// 空数据（war 为 nil、没有数据时间）必须能渲染成一张卡片，而不是 panic 或显示 0001 年。
	empty := BuildWarCard(nil, time.Time{}, false)
	if empty.Stale {
		t.Error("实时数据的视图模型不应标记过期")
	}
	if empty.DataTime != "未知" {
		t.Errorf("数据时间缺失应显示「未知」，实际 %q", empty.DataTime)
	}
	if empty.SuccessRate != "0.0%" {
		t.Errorf("没有场次时成功率应为 0.0%%，实际 %q", empty.SuccessRate)
	}
	for _, w := range []struct {
		field string
		got   string
	}{
		{"在线士兵", empty.PlayerCount},
		{"胜利场次", empty.MissionsWon},
		{"失败场次", empty.MissionsLost},
		{"终结族击杀", empty.TerminidKills},
		{"机器人击杀", empty.AutomatonKills},
		{"光能者击杀", empty.IlluminateKills},
	} {
		if w.got != "0" {
			t.Errorf("%s 缺失时应为 0，实际 %q", w.field, w.got)
		}
	}
}

// TestWarSendsPhotoOnRenderSuccess 校验渲染成功时发图片、不再发文本，并把格式化好的视图模型交给引擎。
func TestWarSendsPhotoOnRenderSuccess(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{res: hd2.Result[*hd2.War]{
		Value:     testWar(),
		FetchedAt: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC),
	}}
	r := &plugtest.Renderer{Img: []byte("假图片")}

	if err := runWar(t, s, svc, r, time.UTC, plugtest.MessageUpdate(-100, "/war")); err != nil {
		t.Fatalf("执行 /war 失败：%v", err)
	}
	if len(s.Photos) != 1 || string(s.Photos[0]) != "假图片" {
		t.Fatalf("应发送渲染出来的图片，实际 %d 张：%v", len(s.Photos), s.Photos)
	}
	if s.PhotoChats[0] != -100 || s.PhotoReplyTo[0] != 42 {
		t.Errorf("发图目标错误：chat=%d replyTo=%d", s.PhotoChats[0], s.PhotoReplyTo[0])
	}
	if len(s.Replies) != 0 {
		t.Errorf("出图成功不应再发文本：%v", s.Replies)
	}
	if len(r.Cards) != 1 || r.Cards[0].Name != "war" {
		t.Fatalf("应把 war 卡片交给渲染引擎，实际 %+v", r.Cards)
	}
	view, ok := r.Cards[0].Data.(WarCard)
	if !ok {
		t.Fatalf("卡片数据应是 WarCard，实际 %T", r.Cards[0].Data)
	}
	if view.PlayerCount != "23,162" || view.DataTime != "2026-09-16 12:00:00" {
		t.Errorf("卡片视图模型未按展示时区格式化：%+v", view)
	}
}

// TestWarHandlerStale 校验降级数据把过期标记透传给卡片（外壳据此显示过期角标）。
func TestWarHandlerStale(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{res: hd2.Result[*hd2.War]{
		Value:     testWar(),
		FetchedAt: time.Now(),
		Stale:     true,
	}}
	r := &plugtest.Renderer{Img: []byte("假图片")}

	if err := runWar(t, s, svc, r, time.UTC, plugtest.MessageUpdate(-100, "/war")); err != nil {
		t.Fatalf("执行 /war 失败：%v", err)
	}
	if len(r.Cards) != 1 {
		t.Fatalf("应渲染一张卡片，实际 %d 张", len(r.Cards))
	}
	view, ok := r.Cards[0].Data.(WarCard)
	if !ok {
		t.Fatalf("卡片数据应是 WarCard，实际 %T", r.Cards[0].Data)
	}
	if !view.Stale {
		t.Error("降级数据应把过期标记透传给卡片")
	}
}

// TestWarHandlerFallsBackToTextOnRenderError 校验渲染失败时回退成与出图前完全一致的纯文本：
// 时区转换、引用原消息、文本内容都与出图前一致，并把失败原因记进日志。
func TestWarHandlerFallsBackToTextOnRenderError(t *testing.T) {
	logs := plugtest.CaptureLog(t)
	s := &plugtest.Sender{GroupID: -100}
	fetchedAt := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	svc := &fakeService{res: hd2.Result[*hd2.War]{Value: testWar(), FetchedAt: fetchedAt}}
	loc := time.FixedZone("CST", 8*3600)

	if err := runWar(t, s, svc, plugtest.FailRenderer(), loc, plugtest.MessageUpdate(-100, "/war")); err != nil {
		t.Fatalf("执行 /war 失败：%v", err)
	}
	if len(s.Photos) != 0 {
		t.Fatalf("渲染失败不应发图，实际 %d 张", len(s.Photos))
	}
	if len(s.Replies) != 1 {
		t.Fatalf("渲染失败应回退一条文本，实际 %d 条：%v", len(s.Replies), s.Replies)
	}
	if want := FormatWar(testWar(), fetchedAt.In(loc), false); s.Replies[0] != want {
		t.Errorf("回退文本必须与 FormatWar 完全一致：\n实际 %q\n期望 %q", s.Replies[0], want)
	}
	if !strings.Contains(s.Replies[0], "20:00:00") {
		t.Errorf("数据时间未转换到展示时区：\n%s", s.Replies[0])
	}
	if s.Chats[0] != -100 || s.ReplyTo[0] != 42 {
		t.Errorf("回复目标错误：chat=%d replyTo=%d", s.Chats[0], s.ReplyTo[0])
	}
	if strings.Contains(s.Replies[0], "数据可能已过期") {
		t.Error("实时数据不应出现过期提示")
	}
	if !strings.Contains(logs.String(), "渲染失败") || !strings.Contains(logs.String(), plugtest.RenderErr.Error()) {
		t.Errorf("日志应记录渲染失败原因，实际 %q", logs.String())
	}
}

// TestWarNilRendererFallsBackToText 校验没有渲染引擎（未配置）时同样按渲染失败回退文本。
func TestWarNilRendererFallsBackToText(t *testing.T) {
	logs := plugtest.CaptureLog(t)
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{res: hd2.Result[*hd2.War]{Value: testWar(), FetchedAt: time.Now()}}

	if err := runWar(t, s, svc, nil, time.UTC, plugtest.MessageUpdate(-100, "/war")); err != nil {
		t.Fatalf("执行 /war 失败：%v", err)
	}
	if len(s.Photos) != 0 || len(s.Replies) != 1 {
		t.Fatalf("渲染引擎缺失时应回退文本：photos=%d replies=%v", len(s.Photos), s.Replies)
	}
	if !strings.Contains(logs.String(), "渲染失败") {
		t.Errorf("日志应记录渲染失败，实际 %q", logs.String())
	}
}

// TestWarHandlerRateLimited 校验上游限流时给出中文提示。
func TestWarHandlerRateLimited(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{err: fmt.Errorf("请求 war 失败：%w", hd2.ErrRateLimited)}
	if err := runWar(t, s, svc, plugtest.FailRenderer(), time.UTC, plugtest.MessageUpdate(-100, "/war")); err != nil {
		t.Fatalf("执行 /war 失败：%v", err)
	}
	if len(s.Replies) != 1 || !strings.Contains(s.Replies[0], "上游接口限流中，请稍后再试") {
		t.Fatalf("限流提示错误：%v", s.Replies)
	}
}

// TestWarHandlerUpstreamError 校验其它上游错误也给出中文提示。
func TestWarHandlerUpstreamError(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{err: errors.New("上游返回状态码 500：boom")}
	if err := runWar(t, s, svc, plugtest.FailRenderer(), time.UTC, plugtest.MessageUpdate(-100, "/war")); err != nil {
		t.Fatalf("执行 /war 失败：%v", err)
	}
	if len(s.Replies) != 1 || !strings.Contains(s.Replies[0], "暂时获取不到战况数据") {
		t.Fatalf("错误提示错误：%v", s.Replies)
	}
	if len(s.Replies) == 1 && strings.ContainsAny(s.Replies[0], "_*[]()~") {
		t.Errorf("提示文本不应包含未转义的 MarkdownV2 字符：%s", s.Replies[0])
	}
}

// TestWarHandlerNilValue 校验 err 为 nil 但数据为空时不会崩溃。
func TestWarHandlerNilValue(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{}
	if err := runWar(t, s, svc, plugtest.FailRenderer(), time.UTC, plugtest.MessageUpdate(-100, "/war")); err != nil {
		t.Fatalf("执行 /war 失败：%v", err)
	}
	if len(s.Replies) != 1 || !strings.Contains(s.Replies[0], "暂时获取不到战况数据") {
		t.Fatalf("空数据提示错误：%v", s.Replies)
	}
}

// TestWarHandlerNilLocation 校验未指定时区时回落到东八区。
func TestWarHandlerNilLocation(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{res: hd2.Result[*hd2.War]{
		Value:     testWar(),
		FetchedAt: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC),
	}}
	if err := runWar(t, s, svc, plugtest.FailRenderer(), nil, plugtest.MessageUpdate(-100, "/war")); err != nil {
		t.Fatalf("执行 /war 失败：%v", err)
	}
	if len(s.Replies) != 1 || !strings.Contains(s.Replies[0], "20:00:00") {
		t.Fatalf("默认时区应为东八区：%v", s.Replies)
	}
}

// TestWarHandlerIgnoresOtherChat 校验非目标群的消息被忽略。
func TestWarHandlerIgnoresOtherChat(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{err: errors.New("不应被调用")}
	if err := runWar(t, s, svc, plugtest.FailRenderer(), time.UTC, plugtest.MessageUpdate(-200, "/war")); err != nil {
		t.Fatalf("执行 /war 失败：%v", err)
	}
	if len(s.Replies) != 0 {
		t.Fatalf("非目标群不应回复：%v", s.Replies)
	}
}

// TestWarHandlerIgnoresNonMessage 校验非文本消息更新被忽略。
func TestWarHandlerIgnoresNonMessage(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{err: errors.New("不应被调用")}
	update := tgbotapi.Update{CallbackQuery: &tgbotapi.CallbackQuery{
		ID:      "1",
		Message: &tgbotapi.Message{Chat: &tgbotapi.Chat{ID: -100}},
	}}
	if err := runWar(t, s, svc, plugtest.FailRenderer(), time.UTC, update); err != nil {
		t.Fatalf("执行 /war 失败：%v", err)
	}
	if len(s.Replies) != 0 {
		t.Fatalf("非消息更新不应回复：%v", s.Replies)
	}
}

// TestWarHandlerSendError 校验发送失败会把错误上抛给分发层记录日志（文本回退与出图两条路径都要）。
func TestWarHandlerSendError(t *testing.T) {
	svc := &fakeService{res: hd2.Result[*hd2.War]{Value: testWar(), FetchedAt: time.Now()}}

	text := &plugtest.Sender{GroupID: -100, Err: errors.New("发送失败")}
	if err := runWar(t, text, svc, plugtest.FailRenderer(), time.UTC, plugtest.MessageUpdate(-100, "/war")); err == nil {
		t.Error("回退文本发送失败应返回错误")
	}

	photo := &plugtest.Sender{GroupID: -100, Err: errors.New("发送失败")}
	if err := runWar(t, photo, svc, &plugtest.Renderer{Img: []byte("假图片")}, time.UTC, plugtest.MessageUpdate(-100, "/war")); err == nil {
		t.Error("发图失败应返回错误")
	}
}

// TestWarCardSmokeWithRealBrowser 是 /war 卡片的真浏览器冒烟：默认跳过，用 HD2_RENDER_SMOKE=1 打开。
// 单测只覆盖「视图模型 → 模板 → HTML」，出图链路（模板排版、素材内联、截图尺寸）靠这条用例，
// 跑完把图片落到 %TEMP% 供人工看图。
func TestWarCardSmokeWithRealBrowser(t *testing.T) {
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

	at := time.Date(2026, 9, 16, 20, 0, 0, 0, time.FixedZone("CST", 8*3600))
	card := render.Card{Name: warCardName, Data: BuildWarCard(testWar(), at, true)}

	start := time.Now()
	img, err := eng.Render(context.Background(), card)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("渲染战况卡片失败：%v", err)
	}

	if !bytes.HasPrefix(img, []byte{0x89, 'P', 'N', 'G'}) {
		t.Fatalf("应输出 PNG，实际开头 % x", img[:min(4, len(img))])
	}
	decoded, err := png.Decode(bytes.NewReader(img))
	if err != nil {
		t.Fatalf("输出不是合法 PNG：%v", err)
	}
	width, height := decoded.Bounds().Dx(), decoded.Bounds().Dy()
	if width < 800 {
		t.Fatalf("卡片宽度应至少 800px，实际 %d", width)
	}

	out := filepath.Join(os.TempDir(), "hd2_war_card.png")
	if err := os.WriteFile(out, img, 0o600); err != nil {
		t.Fatalf("写出卡片图片失败：%v", err)
	}
	t.Logf("战况卡片渲染：耗时 %s，尺寸 %dx%d，体积 %d 字节，落盘 %s", elapsed, width, height, len(img), out)
}

// TestWarFallbackTextKeepsStaleHint 校验文本回退路径同样带上过期提示。
// 出图路径的过期标记有 TestWarHandlerStale 盯着，回退路径如果写死 false，
// 群友在降级时看到的文本会是一份看起来实时的旧战况。
func TestWarFallbackTextKeepsStaleHint(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{res: hd2.Result[*hd2.War]{
		Value:     testWar(),
		FetchedAt: time.Now(),
		Stale:     true,
	}}
	r := plugtest.FailRenderer()

	if err := runWar(t, s, svc, r, time.UTC, plugtest.MessageUpdate(-100, "/war")); err != nil {
		t.Fatalf("执行 /war 失败：%v", err)
	}
	if len(s.Replies) != 1 {
		t.Fatalf("渲染失败应回退一条文本，实际 %d 条", len(s.Replies))
	}
	if !strings.Contains(s.Replies[0], "数据可能已过期") {
		t.Fatalf("文本回退应提示数据可能已过期，实际 %q", s.Replies[0])
	}
}

// TestWarLogsCarryCommandAndChat 校验两条失败路径的日志都带命令名与群 id。
// 只用前缀断言的话，把 chat=%d 删掉也全绿；线上多群并发时就没法从日志里定位是哪个群出的问题。
func TestWarLogsCarryCommandAndChat(t *testing.T) {
	cases := []struct {
		name     string
		svc      *fakeService
		renderer render.Renderer
		wantLog  string
	}{
		{
			name:     "渲染失败",
			svc:      &fakeService{res: hd2.Result[*hd2.War]{Value: testWar(), FetchedAt: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}},
			renderer: plugtest.FailRenderer(),
			wantLog:  "/war 渲染失败",
		},
		{
			name:     "上游查询失败",
			svc:      &fakeService{err: errors.New("上游返回状态码 500：boom")},
			renderer: plugtest.FailRenderer(),
			wantLog:  "/war 查询失败",
		},
		{
			name:     "上游返回空数据",
			svc:      &fakeService{},
			renderer: plugtest.FailRenderer(),
			wantLog:  "/war 返回空数据",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			logs := plugtest.CaptureLog(t)
			s := &plugtest.Sender{GroupID: -100}
			if err := runWar(t, s, c.svc, c.renderer, time.UTC, plugtest.MessageUpdate(-100, "/war")); err != nil {
				t.Fatalf("执行 /war 失败：%v", err)
			}
			plugtest.AssertLogFields(t, logs, c.wantLog, -100)
		})
	}
}
