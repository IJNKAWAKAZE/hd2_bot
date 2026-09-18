package orders

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
	"hd2_bot/src/hd2"
	"hd2_bot/src/plugins/plugutil/plugtest"
	"hd2_bot/src/render"
	"hd2_bot/src/translate"
)

// 编译期断言：真实实现满足插件所需的接口（接线依赖这三条）。
var (
	_ OrdersService   = (*hd2.Service)(nil)
	_ Sender          = (*bot.Bot)(nil)
	_ render.Renderer = (*render.Engine)(nil)
)

func TestTranslatedFallbackReusesRenderedContent(t *testing.T) {
	for _, command := range []string{"assignments", "dispatches"} {
		t.Run(command, func(t *testing.T) {
			s := &plugtest.Sender{GroupID: -100}
			svc := &fakeService{
				assignments: hd2.Result[[]hd2.Assignment]{Value: []hd2.Assignment{{Title: "Defend democracy", Briefing: "Hold this planet"}}},
				dispatches:  hd2.Result[[]hd2.Dispatch]{Value: []hd2.Dispatch{{Message: "Hold this planet"}}},
			}
			translated := []string{"守住这颗星球"}
			if command == "assignments" {
				translated = []string{"保卫民主", "守住这颗星球"}
			}
			tr := &plugtest.Translator{Out: translated}
			r := plugtest.FailRenderer()
			if err := runHandlerWith(t, command, s, svc, r, tr, plugtest.MessageUpdate(-100, "/"+command)); err != nil {
				t.Fatal(err)
			}
			if len(s.Replies) != 1 || !strings.Contains(s.Replies[0], "守住这颗星球") || strings.Contains(s.Replies[0], "Hold this planet") {
				t.Fatalf("translated fallback lost: %v", s.Replies)
			}
			if tr.Calls() != 1 {
				t.Fatalf("translation called %d times", tr.Calls())
			}
			for _, call := range s.Calls {
				if call == "delete" {
					t.Fatal("fallback must preserve command")
				}
			}
		})
	}
}

// testChatID 是本插件用例统一使用的群 id；机器人只服务配置里指定的这一个群，
// 其它会话的指令一律忽略，用例因此都拿它当唯一会话。
const testChatID int64 = -100

// testZone 是样本数据使用的展示时区（东八区）：指令用例统一用它，避免用例与实现各自猜时区。
func testZone() *time.Location { return time.FixedZone("CST", 8*3600) }

// testFetchedAt 是样本数据的抓取时间（东八区 2026-09-16 20:00）。
func testFetchedAt() time.Time {
	return time.Date(2026, 9, 16, 20, 0, 0, 0, testZone())
}

// testDispatches 返回 5 条简报，id 越大发布时间越新（与上游实测的倒序返回一致）。
func testDispatches() []hd2.Dispatch {
	base := testFetchedAt().UTC()
	return []hd2.Dispatch{
		{ID: 3923, Published: base.Add(-2 * time.Hour), Message: "<i=3>MAJOR ORDER WON</i>\n\n士兵们，干得漂亮。"},
		{ID: 3922, Published: base.Add(-26 * time.Hour), Message: "新的重要指令：清剿机器人。"},
		{ID: 3921, Published: base.Add(-50 * time.Hour), Message: "第二段简报。"},
		{ID: 3920, Published: base.Add(-74 * time.Hour), Message: "第三段简报。"},
		{ID: 3919, Published: base.Add(-98 * time.Hour), Message: "第四段简报。"},
	}
}

// testAssignments 返回两条重要指令：一条有截止时间、一条没有（用于验证排序把「未知」排最后）。
// 第一条的任务是实测的上游形状：valueTypes 与 values 按下标对齐，阵营在 valueType 1、
// 目标值在 valueType 3，进度数组与任务一一对应。
func testAssignments() []hd2.Assignment {
	deadline := testFetchedAt().Add(72 * time.Hour)
	return []hd2.Assignment{
		{ID: 2, Title: "没有截止时间的指令", Briefing: "上游没给 expiration。"},
		{
			ID:       1,
			Title:    "清剿机器人",
			Briefing: "消灭 200,000,000 名机器人士兵。",
			Tasks: []hd2.Task{{
				Type:       3,
				Values:     []int64{3, 0, 200000000, 0, 0, 0, 0, 0, 0, 0},
				ValueTypes: []int{1, 2, 3, 4, 6, 5, 8, 9, 11, 12},
			}},
			// 奖励按实测形状给：主源 /assignments 只给 {type, amount}（没有 id32），
			// 卡片按类别编号认成「勋章」，口径见 plugutil.RewardTypeName。
			Reward:     hd2.Reward{Type: 1, Amount: 400},
			Expiration: &deadline,
			Progress:   []int64{1234567},
		},
	}
}

// fakeService 是 OrdersService 的假实现，可以精确模拟上游错误与空数据。
type fakeService struct {
	assignments    hd2.Result[[]hd2.Assignment]
	dispatches     hd2.Result[[]hd2.Dispatch]
	planets        hd2.Result[[]hd2.Planet]
	assignmentsErr error
	dispatchesErr  error
	planetsErr     error
}

// Assignments 返回预先设定的结果。
func (f *fakeService) Assignments(context.Context) (hd2.Result[[]hd2.Assignment], error) {
	return f.assignments, f.assignmentsErr
}

// Dispatches 返回预先设定的结果。
func (f *fakeService) Dispatches(context.Context) (hd2.Result[[]hd2.Dispatch], error) {
	return f.dispatches, f.dispatchesErr
}

// Planets 返回预先设定的星球列表；只有任务里真的出现星球索引时它才会被调用。
func (f *fakeService) Planets(context.Context) (hd2.Result[[]hd2.Planet], error) {
	return f.planets, f.planetsErr
}

// runHandler 取出指定名字的处理器并执行。
// 假 Sender、假渲染器、日志捕获、更新构造与「按名字找处理器」都在 plugtest 里，
// 这里只留本插件的接线（Handlers 的参数是各插件自己的）。
// 不关心翻译的用例走它：注入透传翻译层，正文保持英文，断言口径与改造前一致。
func runHandler(t *testing.T, name string, s *plugtest.Sender, svc OrdersService, r render.Renderer, update tgbotapi.Update) error {
	t.Helper()
	return runHandlerWith(t, name, s, svc, r, translate.Passthrough(), update)
}

// runHandlerWith 与 runHandler 相同，但由用例决定翻译层：
// /dispatches 的正文中文化用例用它注入假翻译层，覆盖「译了 / 没译 / 段数不符」三条路径。
func runHandlerWith(t *testing.T, name string, s *plugtest.Sender, svc OrdersService, r render.Renderer, trans translate.Translator, update tgbotapi.Update) error {
	t.Helper()
	return plugtest.RunHandler(t, Handlers(s, svc, r, testZone(), trans), name, update)
}

// TestHandlersRegistered 校验两条指令都已注册。
func TestHandlersRegistered(t *testing.T) {
	names := map[string]bool{}
	for _, h := range Handlers(&plugtest.Sender{}, &fakeService{}, nil, time.UTC, translate.Passthrough()) {
		names[h.Name] = true
	}
	for _, want := range []string{"assignments", "dispatches"} {
		if !names[want] {
			t.Errorf("缺少 /%s 指令", want)
		}
	}
}

// TestDispatchCount 校验条数参数只有一套口径：
// 缺省、非数字、小于 1（含显式的 0 与负数）一律退回默认 3 条；1..10 按参数；超过 10 夹到 10。
//
// 「0 与缺省都退回默认 3」是刻意的：参数解析（DispatchCount）与出图
// （BuildDispatchesCard）原先对 0 的处理不一致（1 条 vs 3 条），同一个命令给出两种条数，
// 使用者无法预期。现在两边都走这里一个判断，用例把它钉死。
func TestDispatchCount(t *testing.T) {
	cases := []struct {
		name string
		arg  string
		want int
	}{
		{"缺省参数用默认 3 条", "", defaultDispatchCount},
		{"只有空白也用默认 3 条", "   ", defaultDispatchCount},
		{"显式 0 与缺省同口径（默认 3 条）", "0", defaultDispatchCount},
		{"负数与缺省同口径（默认 3 条）", "-5", defaultDispatchCount},
		{"非数字用默认 3 条", "三条", defaultDispatchCount},
		{"数字带单位用默认 3 条", "3条", defaultDispatchCount},
		{"下界 1 条", "1", 1},
		{"参数内的条数原样使用", "3", 3},
		{"参数两侧空白忽略", " 7 ", 7},
		{"上界 10 条", "10", 10},
		{"超过上界夹到 10 条", "11", maxDispatchCount},
		{"远超上界也只给 10 条", "100", maxDispatchCount},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := DispatchCount(c.arg)
			if got != c.want {
				t.Errorf("DispatchCount(%q)：期望 %d，实际 %d", c.arg, c.want, got)
			}
			// 参数解析与出图必须同口径：0 与缺省都退回默认 3 条，不会一个给 1 条一个给 3 条。
			if defaulted := c.arg == "" || c.arg == "   " || c.arg == "0" || c.arg == "-5" || c.arg == "三条" || c.arg == "3条"; defaulted && got != defaultDispatchCount {
				t.Errorf("DispatchCount(%q) 应退回默认 %d 条，实际 %d", c.arg, defaultDispatchCount, got)
			}
		})
	}
	// 出图侧对同一个参数给出同样的条数（两个入口不会再各说一套）。
	if got := len(BuildDispatchesCard(testDispatches(), 0, testFetchedAt(), false).Items); got != defaultDispatchCount {
		t.Errorf("BuildDispatchesCard(count=0) 应展示默认 %d 条，实际 %d 条", defaultDispatchCount, got)
	}
}

// TestAssignmentsSendsPhotoOnSuccess 校验有数据时出图、不回文本。
func TestAssignmentsSendsPhotoOnSuccess(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{assignments: hd2.Result[[]hd2.Assignment]{Value: testAssignments(), FetchedAt: testFetchedAt()}}
	r := &plugtest.Renderer{Img: []byte("假图片")}

	if err := runHandler(t, "assignments", s, svc, r, plugtest.MessageUpdate(-100, "/assignments")); err != nil {
		t.Fatalf("执行 /assignments 失败：%v", err)
	}
	if len(s.Photos) != 1 {
		t.Fatalf("应发送 1 张图片，实际 %d 张；文本 %v", len(s.Photos), s.Replies)
	}
	if len(s.Replies) != 0 {
		t.Fatalf("出图成功时不应再发文本：%v", s.Replies)
	}
	if len(r.Cards) != 1 || r.Cards[0].Name != assignmentsCardName {
		t.Fatalf("渲染卡片错误：%+v", r.Cards)
	}
}

// TestAssignmentsEmptyRendersPlaceholderCard 校验上游返回空列表时渲染占位卡而不是报错。
func TestAssignmentsEmptyRendersPlaceholderCard(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{assignments: hd2.Result[[]hd2.Assignment]{Value: []hd2.Assignment{}, FetchedAt: testFetchedAt()}}
	r := &plugtest.Renderer{Img: []byte("假图片")}

	if err := runHandler(t, "assignments", s, svc, r, plugtest.MessageUpdate(-100, "/assignments")); err != nil {
		t.Fatalf("执行 /assignments 失败：%v", err)
	}
	if len(s.Photos) != 1 {
		t.Fatalf("空数据也应出占位卡（图片），实际图片 %d 张、文本 %v", len(s.Photos), s.Replies)
	}
	if len(s.Replies) != 0 {
		t.Fatalf("空数据不是错误，不应回失败文本：%v", s.Replies)
	}

	card, ok := r.Cards[0].Data.(AssignmentsCard)
	if !ok {
		t.Fatalf("卡片视图模型类型错误：%T", r.Cards[0].Data)
	}
	if !card.Empty {
		t.Error("空数据时卡片应标记 Empty")
	}
	html, err := render.HTML(r.Cards[0])
	if err != nil {
		t.Fatalf("渲染占位卡 HTML 失败：%v", err)
	}
	for _, want := range []string{"重要指令", "暂无重要指令"} {
		if !strings.Contains(html, want) {
			t.Errorf("占位卡 HTML 缺少 %q", want)
		}
	}
}

// TestAssignmentsFallbackKeepsSameWording 校验渲染失败时回退的文本与卡片口径一致
// （同样说「暂无重要指令」，而不是换一句查询失败）。
func TestAssignmentsFallbackKeepsSameWording(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{assignments: hd2.Result[[]hd2.Assignment]{
		Value:     []hd2.Assignment{},
		FetchedAt: testFetchedAt(),
		Stale:     true,
	}}

	if err := runHandler(t, "assignments", s, svc, plugtest.FailRenderer(), plugtest.MessageUpdate(-100, "/assignments")); err != nil {
		t.Fatalf("执行 /assignments 失败：%v", err)
	}
	if len(s.Replies) != 1 {
		t.Fatalf("渲染失败应回退一条文本，实际 %d 条", len(s.Replies))
	}
	for _, want := range []string{"暂无重要指令", "2026\\-09\\-16 20:00:00", "数据可能已过期"} {
		if !strings.Contains(s.Replies[0], want) {
			t.Errorf("回退文本缺少 %q：\n%s", want, s.Replies[0])
		}
	}
}

// TestDispatchesDefaultsToThree 校验不带参数时取最新 3 条。
func TestDispatchesDefaultsToThree(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{dispatches: hd2.Result[[]hd2.Dispatch]{Value: testDispatches(), FetchedAt: testFetchedAt()}}
	r := &plugtest.Renderer{Img: []byte("假图片")}

	if err := runHandler(t, "dispatches", s, svc, r, plugtest.MessageUpdate(-100, "/dispatches")); err != nil {
		t.Fatalf("执行 /dispatches 失败：%v", err)
	}
	card := r.Cards[0].Data.(DispatchesCard)
	if len(card.Items) != 3 {
		t.Fatalf("默认应展示 3 条，实际 %d 条", len(card.Items))
	}
	// 卡片上不再写「展示最新 N 条 ｜ 上游共 M 条」这类统计（用户 2026-09-17 要求）。
	if card.Subtitle != "" {
		t.Errorf("简报卡片不该再有统计副标题，实际 %q", card.Subtitle)
	}
	// 最新的一条是 id 3923（正文里带游戏内标记，展示前应被清理）。
	if !strings.Contains(card.Items[0].Message, "MAJOR ORDER WON") {
		t.Errorf("最新一条应是 id 3923：%+v", card.Items[0])
	}
}

// TestDispatchesHandlerHonoursCount 校验带参数时按参数取条数，并在超范围时夹到上界。
func TestDispatchesHandlerHonoursCount(t *testing.T) {
	cases := []struct {
		text string
		want int
	}{
		{"/dispatches 2", 2},
		{"/dispatches 10", 5}, // 上游只有 5 条，要 10 条也只能给 5 条
		{"/dispatches 100", 5},
		{"/dispatches 三条", 3},
		{"/dispatches@maa_remote_bot 1", 1},
	}
	for _, c := range cases {
		t.Run(c.text, func(t *testing.T) {
			s := &plugtest.Sender{GroupID: -100}
			svc := &fakeService{dispatches: hd2.Result[[]hd2.Dispatch]{Value: testDispatches(), FetchedAt: testFetchedAt()}}
			r := &plugtest.Renderer{Img: []byte("假图片")}

			if err := runHandler(t, "dispatches", s, svc, r, plugtest.MessageUpdate(-100, c.text)); err != nil {
				t.Fatalf("执行 /dispatches 失败：%v", err)
			}
			card := r.Cards[0].Data.(DispatchesCard)
			if len(card.Items) != c.want {
				t.Fatalf("应展示 %d 条，实际 %d 条", c.want, len(card.Items))
			}
		})
	}
}

// TestDispatchesEmptyRendersPlaceholderCard 校验上游没有简报时出占位卡（不是错误、也不是失败文本）。
func TestDispatchesEmptyRendersPlaceholderCard(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{dispatches: hd2.Result[[]hd2.Dispatch]{Value: []hd2.Dispatch{}, FetchedAt: testFetchedAt()}}
	r := &plugtest.Renderer{Img: []byte("假图片")}

	if err := runHandler(t, "dispatches", s, svc, r, plugtest.MessageUpdate(-100, "/dispatches")); err != nil {
		t.Fatalf("执行 /dispatches 失败：%v", err)
	}
	if len(s.Photos) != 1 || len(s.Replies) != 0 {
		t.Fatalf("空数据应出占位卡：图片 %d 张、文本 %v", len(s.Photos), s.Replies)
	}
	html, err := render.HTML(r.Cards[0])
	if err != nil {
		t.Fatalf("渲染占位卡 HTML 失败：%v", err)
	}
	if !strings.Contains(html, "暂无战役简报") {
		t.Error("占位卡应说明暂无简报")
	}
}

// TestDispatchesFallbackText 校验渲染失败回退的文本与卡片口径一致（同一条数、同一批正文）。
func TestDispatchesFallbackText(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{dispatches: hd2.Result[[]hd2.Dispatch]{Value: testDispatches(), FetchedAt: testFetchedAt()}}

	if err := runHandler(t, "dispatches", s, svc, plugtest.FailRenderer(), plugtest.MessageUpdate(-100, "/dispatches 1")); err != nil {
		t.Fatalf("执行 /dispatches 失败：%v", err)
	}
	if len(s.Replies) != 1 {
		t.Fatalf("渲染失败应回退一条文本，实际 %d 条", len(s.Replies))
	}
	for _, want := range []string{"战役简报", "MAJOR ORDER WON", "数据时间：2026\\-09\\-16 20:00:00"} {
		if !strings.Contains(s.Replies[0], want) {
			t.Errorf("回退文本缺少 %q：\n%s", want, s.Replies[0])
		}
	}
}

// TestOrdersServiceErrorReplies 校验上游错误转成中文提示：限流单独说明，其它错误各用自己的领域文案。
func TestOrdersServiceErrorReplies(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"assignments 限流", hd2.ErrRateLimited, "上游接口限流中，请稍后再试。"},
		{"assignments 其它错误", errors.New("网络不通"), assignmentsFailureReply},
		{"dispatches 限流", hd2.ErrRateLimited, "上游接口限流中，请稍后再试。"},
		{"dispatches 其它错误", errors.New("网络不通"), dispatchesFailureReply},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &plugtest.Sender{GroupID: -100}
			svc := &fakeService{}
			if strings.HasPrefix(c.name, "assignments") {
				svc.assignmentsErr = c.err
			} else {
				svc.dispatchesErr = c.err
			}
			cmd := strings.Fields(c.name)[0]

			if err := runHandler(t, cmd, s, svc, &plugtest.Renderer{Img: []byte("假图片")}, plugtest.MessageUpdate(-100, "/"+cmd)); err != nil {
				t.Fatalf("执行 /%s 失败：%v", cmd, err)
			}
			if len(s.Replies) != 1 || s.Replies[0] != c.want {
				t.Fatalf("回复错误：期望 %q，实际 %v", c.want, s.Replies)
			}
		})
	}
}

// TestOrdersHandlersIgnoreOtherChatAndNonMessage 校验非目标群与非消息更新一律不回应。
func TestOrdersHandlersIgnoreOtherChatAndNonMessage(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{
		assignmentsErr: errors.New("不应被调用"),
		dispatchesErr:  errors.New("不应被调用"),
	}
	callback := tgbotapi.Update{CallbackQuery: &tgbotapi.CallbackQuery{ID: "1"}}
	for _, name := range []string{"assignments", "dispatches"} {
		if err := runHandler(t, name, s, svc, nil, plugtest.MessageUpdate(-200, "/"+name)); err != nil {
			t.Fatalf("执行 /%s 失败：%v", name, err)
		}
		if err := runHandler(t, name, s, svc, nil, callback); err != nil {
			t.Fatalf("执行 /%s 失败：%v", name, err)
		}
	}
	if len(s.Replies) != 0 || len(s.Photos) != 0 {
		t.Fatalf("不应有任何回复：文本 %v、图片 %d 张", s.Replies, len(s.Photos))
	}
}

// TestOrdersSendErrorPropagates 校验发送失败会把错误上抛给分发层记录日志。
func TestOrdersSendErrorPropagates(t *testing.T) {
	svc := &fakeService{
		assignments: hd2.Result[[]hd2.Assignment]{Value: testAssignments(), FetchedAt: testFetchedAt()},
		dispatches:  hd2.Result[[]hd2.Dispatch]{Value: testDispatches(), FetchedAt: testFetchedAt()},
	}
	for _, name := range []string{"assignments", "dispatches"} {
		s := &plugtest.Sender{GroupID: -100, Err: errors.New("发送失败")}
		if err := runHandler(t, name, s, svc, &plugtest.Renderer{Img: []byte("假图片")}, plugtest.MessageUpdate(-100, "/"+name)); err == nil {
			t.Errorf("/%s 发图失败应返回错误", name)
		}
		s = &plugtest.Sender{GroupID: -100, Err: errors.New("发送失败")}
		if err := runHandler(t, name, s, svc, plugtest.FailRenderer(), plugtest.MessageUpdate(-100, "/"+name)); err == nil {
			t.Errorf("/%s 回退文本失败应返回错误", name)
		}
	}
}

// TestOrdersFallbackListsAssignmentLines 校验非空指令的文本回退把每一条都排全：
// 标题、简报、任务、奖励、截止时间与进度（占位也要写出来，不能留空行）。
// 出图路径有 TestAssignmentsSendsPhotoOnSuccess 守着，回退路径漏一行的话群友就只能看到半条信息。
func TestOrdersFallbackListsAssignmentLines(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{assignments: hd2.Result[[]hd2.Assignment]{Value: testAssignments(), FetchedAt: testFetchedAt()}}

	if err := runHandler(t, "assignments", s, svc, plugtest.FailRenderer(), plugtest.MessageUpdate(-100, "/assignments")); err != nil {
		t.Fatalf("执行 /assignments 失败：%v", err)
	}
	if len(s.Replies) != 1 {
		t.Fatalf("渲染失败应回退一条文本，实际 %d 条", len(s.Replies))
	}
	wants := []string{
		"进行中：2 条",
		"*清剿机器人*",
		"消灭 200,000,000 名机器人士兵。",
		"任务：消灭机器人敌人",              // 任务名已解码：阵营来自 valueType 1
		"1,234,567 / 200,000,000", // 「当前 / 目标」
		"奖励：勋章 ×400",
		"截止：2026\\-09\\-19 20:00",
		"*没有截止时间的指令*",
		"截止：未知",
		"数据时间：2026\\-09\\-16 20:00:00",
	}
	for _, want := range wants {
		if !strings.Contains(s.Replies[0], want) {
			t.Errorf("回退文本缺少 %q：\n%s", want, s.Replies[0])
		}
	}
	// 百分比里的点号要按 MarkdownV2 转义，否则整条消息会被 Telegram 拒收。
	if !strings.Contains(s.Replies[0], "0\\.6%") {
		t.Errorf("回退文本里的百分比未转义：\n%s", s.Replies[0])
	}
}

// TestOrdersAssignmentsResolvesPlanetNames 校验带星球索引的任务会把星球名画进卡片：
// 任务里的星球是索引（valueType 12），要靠一次星球查询翻成名字。
// 星球查询失败不算 /assignments 失败——指令照常出图，这些任务退回「星球 #N」。
func TestOrdersAssignmentsResolvesPlanetNames(t *testing.T) {
	list := []hd2.Assignment{{
		ID:    1,
		Title: "解放麦拉芬蒙河",
		Tasks: []hd2.Task{{
			Type:       11,
			Values:     []int64{0, 0, 0, 0, 0, 0, 0, 0, 0, 5},
			ValueTypes: []int{1, 2, 3, 4, 6, 5, 8, 9, 11, 12},
		}},
	}}
	svc := &fakeService{
		assignments: hd2.Result[[]hd2.Assignment]{Value: list, FetchedAt: testFetchedAt()},
		planets: hd2.Result[[]hd2.Planet]{
			Value:     []hd2.Planet{{Index: 5, Name: "Malevelon Creek"}},
			FetchedAt: testFetchedAt(),
		},
	}
	s := &plugtest.Sender{GroupID: -100}
	if err := runHandler(t, "assignments", s, svc, plugtest.FailRenderer(), plugtest.MessageUpdate(-100, "/assignments")); err != nil {
		t.Fatalf("执行 /assignments 失败：%v", err)
	}
	if !strings.Contains(s.Replies[0], "星球：麦拉芬蒙河") {
		t.Errorf("任务里的星球索引应翻成简中星球名：\n%s", s.Replies[0])
	}

	// 星球查询失败：指令照常出图，只是星球显示编号（编号里的 # 会被 MarkdownV2 转义）。
	svc.planetsErr = errors.New("星球接口挂了")
	fallen := &plugtest.Sender{GroupID: -100}
	if err := runHandler(t, "assignments", fallen, svc, plugtest.FailRenderer(), plugtest.MessageUpdate(-100, "/assignments")); err != nil {
		t.Fatalf("星球查询失败不该让 /assignments 失败：%v", err)
	}
	if !strings.Contains(fallen.Replies[0], "星球 \\#5") {
		t.Errorf("取不到星球数据时应退回编号：\n%s", fallen.Replies[0])
	}
}

// TestOrdersLogsCarryCommandAndChat 校验两条指令的两条失败路径，日志里都带命令名与群 id。
// 只断言前缀（例如「/dispatches 渲染失败」）的话，把 chat=%d 删掉也全绿，排查时定位不到群。
func TestOrdersLogsCarryCommandAndChat(t *testing.T) {
	cases := []struct {
		name    string
		cmd     string
		svc     *fakeService
		wantLog string
	}{
		{
			name:    "assignments 渲染失败",
			cmd:     "assignments",
			svc:     &fakeService{assignments: hd2.Result[[]hd2.Assignment]{Value: testAssignments(), FetchedAt: testFetchedAt()}},
			wantLog: "/assignments 渲染失败",
		},
		{
			name:    "assignments 查询失败",
			cmd:     "assignments",
			svc:     &fakeService{assignmentsErr: errors.New("网络不通")},
			wantLog: "/assignments 查询失败",
		},
		{
			name:    "dispatches 渲染失败",
			cmd:     "dispatches",
			svc:     &fakeService{dispatches: hd2.Result[[]hd2.Dispatch]{Value: testDispatches(), FetchedAt: testFetchedAt()}},
			wantLog: "/dispatches 渲染失败",
		},
		{
			name:    "dispatches 查询失败",
			cmd:     "dispatches",
			svc:     &fakeService{dispatchesErr: errors.New("网络不通")},
			wantLog: "/dispatches 查询失败",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			logs := plugtest.CaptureLog(t)
			s := &plugtest.Sender{GroupID: -100}
			if err := runHandler(t, c.cmd, s, c.svc, plugtest.FailRenderer(), plugtest.MessageUpdate(-100, "/"+c.cmd)); err != nil {
				t.Fatalf("执行 /%s 失败：%v", c.cmd, err)
			}
			plugtest.AssertLogFields(t, logs, c.wantLog, -100)
		})
	}
}

// TestOrdersCardsSmokeWithRealBrowser 是两张卡片的真浏览器冒烟：默认跳过，用 HD2_RENDER_SMOKE=1 打开。
// 除了看排版，这里还守住 S4 的卡片高度安全线（render.MaxCardHeightPx = 7000px，取代 spec §7.4 的 4000px）：
// 简报卡按两种最坏情况渲染——默认 3 条 × 真实最长正文（807 字，实测 3254px）与 10 条 × 单条上限
// （实测 6112px）——高度都必须
// 小于安全线，超过就由引擎拦成文本回退（内容不丢，只是形态从图变成文本）。
func TestOrdersCardsSmokeWithRealBrowser(t *testing.T) {
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

	// 1600 字，超过单条上限 1500 字（maxDispatchRunes），10 条都会走截断路径：这是「条数拉满」的最坏高度。
	long := strings.Repeat("超级地球需要你。", 200)
	worst := make([]hd2.Dispatch, 0, maxDispatchCount)
	for i := 0; i < maxDispatchCount; i++ {
		worst = append(worst, hd2.Dispatch{ID: int64(100 - i), Published: testFetchedAt().UTC().Add(-time.Duration(i) * time.Hour), Message: long})
	}
	// 807 字是真实上游最长简报正文（S4 设计 §2）：默认 3 条时一条都不该被截，高度也要在安全线内。
	realBody := string([]rune(strings.Repeat("超级地球需要你。", 101))[:807])
	real := make([]hd2.Dispatch, 0, defaultDispatchCount)
	for i := 0; i < defaultDispatchCount; i++ {
		real = append(real, hd2.Dispatch{ID: int64(100 - i), Published: testFetchedAt().UTC().Add(-time.Duration(i) * time.Hour), Message: realBody})
	}

	cards := []struct {
		file string
		card render.Card
		full bool // 正文必须一条都没被截断（真实内容完整进卡）
	}{
		{"hd2_assignments_card.png", render.Card{Name: assignmentsCardName, Data: BuildAssignmentsCard(testAssignments(), testFetchedAt(), true, nil)}, false},
		{"hd2_assignments_empty_card.png", render.Card{Name: assignmentsCardName, Data: BuildAssignmentsCard(nil, testFetchedAt(), false, nil)}, false},
		{"hd2_dispatches_card.png", render.Card{Name: dispatchesCardName, Data: BuildDispatchesCard(testDispatches(), defaultDispatchCount, testFetchedAt(), false)}, false},
		{"hd2_dispatches_real_3_card.png", render.Card{Name: dispatchesCardName, Data: BuildDispatchesCard(real, defaultDispatchCount, testFetchedAt(), false)}, true},
		{"hd2_dispatches_10_card.png", render.Card{Name: dispatchesCardName, Data: BuildDispatchesCard(worst, maxDispatchCount, testFetchedAt(), false)}, false},
	}
	for _, c := range cards {
		start := time.Now()
		img, err := eng.Render(context.Background(), c.card)
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("渲染 %s 失败：%v", c.file, err)
		}
		if !bytes.HasPrefix(img, []byte{0x89, 'P', 'N', 'G'}) {
			t.Fatalf("%s 应输出 PNG，实际开头 % x", c.file, img[:min(4, len(img))])
		}
		decoded, err := png.Decode(bytes.NewReader(img))
		if err != nil {
			t.Fatalf("%s 不是合法 PNG：%v", c.file, err)
		}
		width, height := decoded.Bounds().Dx(), decoded.Bounds().Dy()
		if width < 800 {
			t.Fatalf("%s 宽度应至少 800px，实际 %d", c.file, width)
		}
		// S4 的唯一高度口径：任何一张卡都必须低于安全线（超标的图引擎会直接报错，走文本回退）。
		if height >= render.MaxCardHeightPx {
			t.Fatalf("%s 高度应小于安全线 %dpx，实际 %d", c.file, render.MaxCardHeightPx, height)
		}
		if c.full {
			data, ok := c.card.Data.(DispatchesCard)
			if !ok || truncatedItems(data) != 0 || data.Note != "" {
				t.Fatalf("%s 的正文应完整进卡，实际 count=%d note=%q", c.file, truncatedItems(data), data.Note)
			}
		}
		out := filepath.Join(os.TempDir(), c.file)
		if err := os.WriteFile(out, img, 0o600); err != nil {
			t.Fatalf("写出 %s 失败：%v", c.file, err)
		}
		t.Logf("%s：耗时 %s，尺寸 %dx%d，体积 %d 字节", c.file, elapsed, width, height, len(img))
	}
}

// TestOrdersDefaultDisplayLocation 校验调用方没给时区时按东八区展示（loc 为 nil 的兜底路径）。
func TestOrdersDefaultDisplayLocation(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{dispatches: hd2.Result[[]hd2.Dispatch]{
		Value:     []hd2.Dispatch{{ID: 1, Published: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC), Message: "正文"}},
		FetchedAt: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC),
	}}
	for _, h := range Handlers(s, svc, plugtest.FailRenderer(), nil, translate.Passthrough()) {
		if h.Name != "dispatches" {
			continue
		}
		if err := h.Run(plugtest.MessageUpdate(-100, "/dispatches")); err != nil {
			t.Fatalf("执行 /dispatches 失败：%v", err)
		}
	}
	if len(s.Replies) != 1 || !strings.Contains(s.Replies[0], "20:00:00") {
		t.Fatalf("默认时区应为东八区：%v", s.Replies)
	}
}

// TestDispatchesUsesTranslatedText 校验 /dispatches 的正文走翻译层：卡片里的正文是中文。
// 送译的必须是「清洗后的英文正文」——游戏内标记不能混进译文，
// 否则翻译后端会把 <i=3> 一起搬回来，卡片上又冒出尖括号噪声。
func TestDispatchesUsesTranslatedText(t *testing.T) {
	svc := &fakeService{dispatches: hd2.Result[[]hd2.Dispatch]{
		Value: []hd2.Dispatch{{
			ID:        1,
			Published: testFetchedAt().UTC(),
			Message:   "<i=3>MAJOR ORDER WON</i> Victory.",
		}},
		FetchedAt: testFetchedAt(),
	}}
	trans := &plugtest.Translator{Out: []string{"重大指令达成：取得胜利。"}}
	renderer := &plugtest.Renderer{Img: []byte("png")}
	s := &plugtest.Sender{GroupID: testChatID}

	if err := runHandlerWith(t, "dispatches", s, svc, renderer, trans, plugtest.MessageUpdate(testChatID, "/dispatches 1")); err != nil {
		t.Fatalf("处理器返回错误：%v", err)
	}
	card, ok := renderer.Cards[0].Data.(DispatchesCard)
	if !ok {
		t.Fatalf("卡片类型错误：%T", renderer.Cards[0].Data)
	}
	if len(card.Items) != 1 || !strings.Contains(card.Items[0].Message, "重大指令达成") {
		t.Fatalf("正文应为译文，实际 %+v", card.Items)
	}
	if card.TranslateNote != "" {
		t.Fatalf("翻译成功时不应有说明文案，实际 %q", card.TranslateNote)
	}
	if len(trans.Texts) != 1 || len(trans.Texts[0]) != 1 {
		t.Fatalf("应把每条简报正文各送一段，实际 %v", trans.Texts)
	}
	// 送译前已清洗：游戏内标记不进翻译层。
	if got := trans.Texts[0][0]; got != "MAJOR ORDER WON Victory." {
		t.Fatalf("送译文本应为清洗后的英文正文，实际 %q", got)
	}
}

// TestDispatchesKeepsEnglishWhenTranslationFails 校验翻译失败时保留英文并注明。
// 翻译失败不算命令失败：照常出图，只是正文是英文加一句说明。
func TestDispatchesKeepsEnglishWhenTranslationFails(t *testing.T) {
	svc := &fakeService{dispatches: hd2.Result[[]hd2.Dispatch]{
		Value:     []hd2.Dispatch{{ID: 1, Published: testFetchedAt().UTC(), Message: "Victory."}},
		FetchedAt: testFetchedAt(),
	}}
	trans := &plugtest.Translator{Err: errors.New("翻译后端挂了")}
	renderer := &plugtest.Renderer{Img: []byte("png")}
	s := &plugtest.Sender{GroupID: testChatID}

	if err := runHandlerWith(t, "dispatches", s, svc, renderer, trans, plugtest.MessageUpdate(testChatID, "/dispatches 1")); err != nil {
		t.Fatalf("处理器返回错误：%v", err)
	}
	card := renderer.Cards[0].Data.(DispatchesCard)
	if card.Items[0].Message != "Victory." {
		t.Fatalf("翻译失败应保留英文，实际 %q", card.Items[0].Message)
	}
	if card.TranslateNote == "" {
		t.Fatal("翻译失败应在卡片上注明")
	}
	// 文本回退也要带上同一句说明
	if !strings.Contains(FormatDispatchesText(card), "翻译暂不可用") {
		t.Fatalf("文本回退应带同口径说明：\n%s", FormatDispatchesText(card))
	}
}

// TestDispatchesHandlesNilTranslator 校验没传翻译层时不会 panic（等价于「不翻译」）。
// nil 表示「配置里关掉了翻译」，此时正文是英文但没有坏消息，不该在卡片上写翻译不可用。
func TestDispatchesHandlesNilTranslator(t *testing.T) {
	svc := &fakeService{dispatches: hd2.Result[[]hd2.Dispatch]{
		Value:     []hd2.Dispatch{{ID: 1, Published: testFetchedAt().UTC(), Message: "Victory."}},
		FetchedAt: testFetchedAt(),
	}}
	renderer := &plugtest.Renderer{Img: []byte("png")}
	s := &plugtest.Sender{GroupID: testChatID}
	if err := runHandlerWith(t, "dispatches", s, svc, renderer, nil, plugtest.MessageUpdate(testChatID, "/dispatches 1")); err != nil {
		t.Fatalf("处理器返回错误：%v", err)
	}
	card := renderer.Cards[0].Data.(DispatchesCard)
	if card.Items[0].Message != "Victory." || card.TranslateNote != "" {
		t.Fatalf("不翻译时应原样展示英文且不注明，实际 %+v", card)
	}
}

// shortTranslator 是「返回段数少于入参段数」的翻译层替身。
//
// 为什么不用 plugtest.Translator：它刻意保证「返回段数永远等于入参段数」（队列用完后各段回退原文），
// 所以「段数不符」这条退化路径在它身上根本走不到。替身不放进 plugtest（那是全项目共用的测试包，
// 不为单个包的特殊退化路径开口子），只在本包里补齐这条分支。
type shortTranslator struct {
	out   []string
	calls int
}

// Translate 记录调用次数并原样返回预设的短切片。
func (s *shortTranslator) Translate(_ context.Context, _ []string) ([]string, error) {
	s.calls++
	return s.out, nil
}

// TestDispatchesTranslationLengthMismatchKeepsEnglish 校验翻译层返回长度不符时保留英文（不做错位替换）。
// 段数对不上时最糟的选择是「按位置硬塞」：那会把 A 的译文贴到 B 的正文上。
func TestDispatchesTranslationLengthMismatchKeepsEnglish(t *testing.T) {
	svc := &fakeService{dispatches: hd2.Result[[]hd2.Dispatch]{
		Value: []hd2.Dispatch{
			{ID: 2, Published: testFetchedAt().UTC(), Message: "Victory."},
			{ID: 1, Published: testFetchedAt().UTC().Add(-time.Hour), Message: "Another."},
		},
		FetchedAt: testFetchedAt(),
	}}
	// 故意返回过短的切片（plugtest.Translator 造不出这种返回，见 shortTranslator 的说明）
	trans := &shortTranslator{out: []string{"只有一条译文"}}
	renderer := &plugtest.Renderer{Img: []byte("png")}
	s := &plugtest.Sender{GroupID: testChatID}
	if err := runHandlerWith(t, "dispatches", s, svc, renderer, trans, plugtest.MessageUpdate(testChatID, "/dispatches 2")); err != nil {
		t.Fatalf("处理器返回错误：%v", err)
	}
	if trans.calls != 1 {
		t.Fatalf("应只调用翻译层一次，实际 %d 次", trans.calls)
	}
	card := renderer.Cards[0].Data.(DispatchesCard)
	if card.TranslateNote == "" {
		t.Fatal("长度不符时应按「翻译不可用」处理并在卡片上注明")
	}
	for i, want := range []string{"Victory.", "Another."} {
		if got := card.Items[i].Message; got != want {
			t.Errorf("第 %d 条应保留英文原文（不做错位替换），实际 %q", i+1, got)
		}
	}
}

// TestDispatchesNilRendererFallsBackToText 校验未启用渲染（renderer 为 nil）时退回文本。
// 配置里关掉渲染时线上走的就是这条路，不能只在「假渲染器报错」时才被覆盖。
func TestDispatchesNilRendererFallsBackToText(t *testing.T) {
	svc := &fakeService{dispatches: hd2.Result[[]hd2.Dispatch]{
		Value:     []hd2.Dispatch{{ID: 1, Published: testFetchedAt().UTC(), Message: "Victory."}},
		FetchedAt: testFetchedAt(),
	}}
	trans := &plugtest.Translator{Out: []string{"胜利。"}}
	s := &plugtest.Sender{GroupID: testChatID}

	if err := runHandlerWith(t, "dispatches", s, svc, nil, trans, plugtest.MessageUpdate(testChatID, "/dispatches 1")); err != nil {
		t.Fatalf("处理器返回错误：%v", err)
	}
	if len(s.Photos) != 0 || len(s.Replies) != 1 {
		t.Fatalf("未启用渲染应只回一条文本：图片 %d 张、文本 %v", len(s.Photos), s.Replies)
	}
	if !strings.Contains(s.Replies[0], "战役简报") || !strings.Contains(s.Replies[0], "胜利。") {
		t.Fatalf("文本回退应带标题与译文：\n%s", s.Replies[0])
	}
}

// pagingRenderer 模拟「一张图装不下」：按卡片里的条目数算高度，超过安全线就回 TooTallError，
// 条目少到装得下就成功。真浏览器验证分页要几十秒且依赖 Chromium，这里只需要分页器的行为。
type pagingRenderer struct {
	itemHeight int              // 每个条目占的高度（px）
	pages      []DispatchesCard // 每次渲染拿到的卡片数据
}

func (r *pagingRenderer) Render(_ context.Context, card render.Card) ([]byte, error) {
	data, ok := card.Data.(DispatchesCard)
	if !ok {
		return nil, errors.New("卡片数据不是 DispatchesCard")
	}
	r.pages = append(r.pages, data)
	if h := len(data.Items) * r.itemHeight; h > render.MaxCardHeightPx {
		return nil, render.TooTallError(h)
	}
	return []byte("假图片"), nil
}

func (r *pagingRenderer) Close() error { return nil }

// manyDispatches 造 n 条发布时间递减的简报，用来触发分页（条目越多一张图越高）。
func manyDispatches(n int) []hd2.Dispatch {
	out := make([]hd2.Dispatch, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, hd2.Dispatch{
			ID:        int64(100 - i),
			Published: testFetchedAt().UTC().Add(-time.Duration(i) * time.Hour),
			Message:   "超级地球需要你。",
		})
	}
	return out
}

// TestDispatchesPaginatesWhenCardTooTall 是 /dispatches 分页的主用例：
// 4 条 × 3000px 一张图装不下 → 分两页发两张图（不是退文本），每页带页码、上游总数不被分页改小。
func TestDispatchesPaginatesWhenCardTooTall(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{dispatches: hd2.Result[[]hd2.Dispatch]{Value: manyDispatches(6), FetchedAt: testFetchedAt()}}
	r := &pagingRenderer{itemHeight: 3000}

	if err := runHandler(t, "dispatches", s, svc, r, plugtest.MessageUpdate(-100, "/dispatches 4")); err != nil {
		t.Fatalf("执行 /dispatches 失败：%v", err)
	}
	if len(s.Photos) != 2 {
		t.Fatalf("应分两页发两张图，实际 %d 张、文本 %d 条", len(s.Photos), len(s.Replies))
	}
	if len(s.Replies) != 0 {
		t.Fatalf("分页成功时不该再发文本：%v", s.Replies)
	}
	// 第一页回复用户的命令，后续页不各自回复一次（免得刷出好几条「回复 XX」）。
	if len(s.PhotoReplyTo) != 2 || s.PhotoReplyTo[0] != plugtest.DefaultMessageID || s.PhotoReplyTo[1] != 0 {
		t.Fatalf("图片的回复关系应为「第一页回复命令、后续页不回复」，实际 %v", s.PhotoReplyTo)
	}
	pages := r.pages[len(r.pages)-2:]
	for i, card := range pages {
		if got, want := card.Note, render.PageLabel(i+1, 2); !strings.Contains(got, want) {
			t.Fatalf("第 %d 页的说明里应有 %q，实际 %q", i+1, want, got)
		}
		// 分页只切展示区间：每页各自只装自己那几条。
		if got := len(card.Items); got != 2 {
			t.Fatalf("第 %d 页展示条数应为 2，实际 %d", i+1, got)
		}
	}
}

// TestDispatchesFallsBackToTextWhenPaginationExhausted 说明分页是「尽力」而不是「保证」：
// 每条都超过安全线时，回到完整文本兜底，不往群里刷图。
func TestDispatchesFallsBackToTextWhenPaginationExhausted(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{dispatches: hd2.Result[[]hd2.Dispatch]{Value: manyDispatches(4), FetchedAt: testFetchedAt()}}
	r := &pagingRenderer{itemHeight: render.MaxCardHeightPx + 1}

	if err := runHandler(t, "dispatches", s, svc, r, plugtest.MessageUpdate(-100, "/dispatches 4")); err != nil {
		t.Fatalf("执行 /dispatches 失败：%v", err)
	}
	if len(s.Photos) != 0 {
		t.Fatalf("连分页都装不下时不该发图，实际 %d 张", len(s.Photos))
	}
	if len(s.Replies) != 1 {
		t.Fatalf("应回退一条完整文本，实际 %d 条", len(s.Replies))
	}
	if !strings.Contains(s.Replies[0], "超级地球需要你。") {
		t.Fatalf("回退文本应带上简报正文，实际 %q", s.Replies[0])
	}
}

// TestAssignmentsNilRendererFallsBackToText 校验未启用渲染（renderer 为 nil）时 /assignments 直接退回文本。
//
// 为什么单独写一条：/dispatches 现在自己在分页前判 nil（见 handler 里的分页路径），
// 于是 h.render 的「渲染未启用」分支只剩 /assignments 会走到，没有这条用例它就成了没被覆盖的死路。
func TestAssignmentsNilRendererFallsBackToText(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{assignments: hd2.Result[[]hd2.Assignment]{
		Value:     []hd2.Assignment{},
		FetchedAt: testFetchedAt(),
	}}

	if err := runHandler(t, "assignments", s, svc, nil, plugtest.MessageUpdate(-100, "/assignments")); err != nil {
		t.Fatalf("执行 /assignments 失败：%v", err)
	}
	if len(s.Photos) != 0 {
		t.Fatalf("渲染未启用时不该发图，实际 %d 张", len(s.Photos))
	}
	if len(s.Replies) != 1 {
		t.Fatalf("渲染未启用时应回退一条文本，实际 %d 条", len(s.Replies))
	}
	if !strings.Contains(s.Replies[0], "暂无重要指令") {
		t.Errorf("回退文本应说明暂无指令：%s", s.Replies[0])
	}
}
