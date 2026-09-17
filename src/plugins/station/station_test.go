package station

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
)

// 编译期断言：真实实现满足插件所需的接口（接线依赖这三条）。
var (
	_ StationService  = (*hd2.Service)(nil)
	_ Sender          = (*bot.Bot)(nil)
	_ render.Renderer = (*render.Engine)(nil)
)

// testZone 是样本数据使用的展示时区（东八区）：用例统一用它，避免用例与实现各自猜时区。
func testZone() *time.Location { return time.FixedZone("CST", 8*3600) }

// testFetchedAt 是样本数据的抓取时间（东八区 2026-09-16 20:00）。
func testFetchedAt() time.Time {
	return time.Date(2026, 9, 16, 20, 0, 0, 0, testZone())
}

// testEvents 返回 3 起事件：两起有结束时间（先结束的在前）、一起没有结束时间（排最后）。
// 字段取自实测的上游响应（卢克西恩特的防守战）。
func testEvents() []hd2.PlanetEvent {
	end := time.Date(2026, 9, 18, 11, 2, 12, 0, time.UTC)
	later := time.Date(2026, 9, 20, 11, 2, 12, 0, time.UTC)
	return []hd2.PlanetEvent{
		{
			ID: 5695, EventType: 1, Faction: "Terminids", Health: 1497963, MaxHealth: 1500000,
			StartTime: time.Date(2026, 9, 16, 11, 2, 12, 0, time.UTC), EndTime: &later,
			CampaignID: 51713, PlanetIndex: 110, PlanetName: "BEKVAM III",
		},
		{
			ID: 5700, EventType: 1, Faction: "Automatons", Health: 500000, MaxHealth: 1000000,
			StartTime: time.Date(2026, 9, 16, 11, 2, 12, 0, time.UTC), EndTime: &end,
			CampaignID: 51714, PlanetIndex: 268, PlanetName: "LUXURIANT",
		},
		{
			// 上游没给结束时间：卡片上要标「未知」，排序时排在有结束时间的后面。
			ID: 5701, EventType: 2, Faction: "Illuminate", Health: 0, MaxHealth: 0,
			StartTime:   time.Date(2026, 9, 16, 11, 2, 12, 0, time.UTC),
			PlanetIndex: 5, PlanetName: "ACAMAR IV",
		},
	}
}

// testStations 返回一座空间站，字段取自实测的上游 v2 响应（三项已知战术行动 + 一项未知状态）。
func testStations() []hd2.SpaceStation {
	election := time.Date(2026, 9, 16, 12, 39, 4, 0, time.UTC)
	return []hd2.SpaceStation{{
		ID32:        749875195,
		Name:        "", // 上游 v2 实测没有给 name
		Flags:       1,
		ElectionEnd: &election,
		TacticalActions: []hd2.TacticalAction{
			{ID32: 4091660627, Name: "EAGLE STORM", Status: 1},
			{ID32: 3248573007, Name: "ORBITAL BLOCKADE", Status: 2},
			{ID32: 3578080409, Name: "HEAVY ORDNANCE DISTRIBUTION", Status: 3},
			{ID32: 1, Name: "UNKNOWN ACTION", Status: 9},
		},
	}}
}

// fakeService 是 StationService 的假实现，可以精确模拟上游错误与空数据。
type fakeService struct {
	events      hd2.Result[[]hd2.PlanetEvent]
	stations    hd2.Result[[]hd2.SpaceStation]
	eventsErr   error
	stationsErr error
}

// Events 返回预先设定的结果。
func (f *fakeService) Events(context.Context) (hd2.Result[[]hd2.PlanetEvent], error) {
	return f.events, f.eventsErr
}

// Stations 返回预先设定的结果。
func (f *fakeService) Stations(context.Context) (hd2.Result[[]hd2.SpaceStation], error) {
	return f.stations, f.stationsErr
}

// runHandler 取出指定名字的处理器并执行。
// 假 Sender、假渲染器、日志捕获、更新构造与「按名字找处理器」都在 plugtest 里，
// 这里只留本插件的接线（Handlers 的参数是各插件自己的）。
func runHandler(t *testing.T, name string, s *plugtest.Sender, svc StationService, r render.Renderer, update tgbotapi.Update) error {
	t.Helper()
	return plugtest.RunHandler(t, Handlers(s, svc, r, testZone()), name, update)
}

// TestHandlersRegistered 校验两条指令都已注册。
func TestHandlersRegistered(t *testing.T) {
	names := map[string]bool{}
	for _, h := range Handlers(&plugtest.Sender{}, &fakeService{}, nil, testZone()) {
		names[h.Name] = true
	}
	for _, want := range []string{"events", "stations"} {
		if !names[want] {
			t.Errorf("缺少 /%s 指令", want)
		}
	}
}

// TestEventsSendsPhotoOnSuccess 校验有事件时出图、不回文本。
func TestEventsSendsPhotoOnSuccess(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{events: hd2.Result[[]hd2.PlanetEvent]{Value: testEvents(), FetchedAt: testFetchedAt()}}
	r := &plugtest.Renderer{Img: []byte("假图片")}

	if err := runHandler(t, "events", s, svc, r, plugtest.MessageUpdate(-100, "/events")); err != nil {
		t.Fatalf("执行 /events 失败：%v", err)
	}
	if len(s.Photos) != 1 || len(s.Replies) != 0 {
		t.Fatalf("应只发 1 张图片：图片 %d 张、文本 %v", len(s.Photos), s.Replies)
	}
	if r.Cards[0].Name != eventsCardName {
		t.Fatalf("渲染卡片错误：%+v", r.Cards)
	}
}

// TestEventsEmptyRendersPlaceholderCard 校验上游没有事件时出占位卡（不是错误、也不是失败文本）。
func TestEventsEmptyRendersPlaceholderCard(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{events: hd2.Result[[]hd2.PlanetEvent]{Value: []hd2.PlanetEvent{}, FetchedAt: testFetchedAt()}}
	r := &plugtest.Renderer{Img: []byte("假图片")}

	if err := runHandler(t, "events", s, svc, r, plugtest.MessageUpdate(-100, "/events")); err != nil {
		t.Fatalf("执行 /events 失败：%v", err)
	}
	if len(s.Photos) != 1 || len(s.Replies) != 0 {
		t.Fatalf("空数据应出占位卡：图片 %d 张、文本 %v", len(s.Photos), s.Replies)
	}
	card, ok := r.Cards[0].Data.(EventsCard)
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
	if !strings.Contains(html, "暂无星球事件") {
		t.Error("占位卡应说明暂无事件")
	}
}

// TestEventsFallbackText 校验渲染失败时回退的文本与卡片口径一致（同样的条数、排序与「未知」）。
func TestEventsFallbackText(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{events: hd2.Result[[]hd2.PlanetEvent]{Value: testEvents(), FetchedAt: testFetchedAt(), Stale: true}}

	if err := runHandler(t, "events", s, svc, plugtest.FailRenderer(), plugtest.MessageUpdate(-100, "/events")); err != nil {
		t.Fatalf("执行 /events 失败：%v", err)
	}
	if len(s.Replies) != 1 {
		t.Fatalf("渲染失败应回退一条文本，实际 %d 条", len(s.Replies))
	}
	for _, want := range []string{"星球事件", "天园六IV", "至 未知", "数据可能已过期"} {
		if !strings.Contains(s.Replies[0], want) {
			t.Errorf("回退文本缺少 %q：\n%s", want, s.Replies[0])
		}
	}
}

// TestStationsSendsPhotoOnSuccess 校验空间站出图。
func TestStationsSendsPhotoOnSuccess(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{stations: hd2.Result[[]hd2.SpaceStation]{Value: testStations(), FetchedAt: testFetchedAt()}}
	r := &plugtest.Renderer{Img: []byte("假图片")}

	if err := runHandler(t, "stations", s, svc, r, plugtest.MessageUpdate(-100, "/stations")); err != nil {
		t.Fatalf("执行 /stations 失败：%v", err)
	}
	if len(s.Photos) != 1 || len(s.Replies) != 0 {
		t.Fatalf("应只发 1 张图片：图片 %d 张、文本 %v", len(s.Photos), s.Replies)
	}
	if r.Cards[0].Name != stationsCardName {
		t.Fatalf("渲染卡片错误：%+v", r.Cards)
	}
}

// TestStationsEmptyRendersPlaceholderCard 校验上游返回空列表时出占位卡。
func TestStationsEmptyRendersPlaceholderCard(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{stations: hd2.Result[[]hd2.SpaceStation]{Value: []hd2.SpaceStation{}, FetchedAt: testFetchedAt()}}
	r := &plugtest.Renderer{Img: []byte("假图片")}

	if err := runHandler(t, "stations", s, svc, r, plugtest.MessageUpdate(-100, "/stations")); err != nil {
		t.Fatalf("执行 /stations 失败：%v", err)
	}
	if len(s.Photos) != 1 || len(s.Replies) != 0 {
		t.Fatalf("空数据应出占位卡：图片 %d 张、文本 %v", len(s.Photos), s.Replies)
	}
	html, err := render.HTML(r.Cards[0])
	if err != nil {
		t.Fatalf("渲染占位卡 HTML 失败：%v", err)
	}
	if !strings.Contains(html, "暂无空间站数据") {
		t.Error("占位卡应说明暂无空间站数据")
	}
}

// TestStationsFallbackText 校验渲染失败时回退文本同样列出战术行动与状态。
func TestStationsFallbackText(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{stations: hd2.Result[[]hd2.SpaceStation]{Value: testStations(), FetchedAt: testFetchedAt()}}

	if err := runHandler(t, "stations", s, svc, plugtest.FailRenderer(), plugtest.MessageUpdate(-100, "/stations")); err != nil {
		t.Fatalf("执行 /stations 失败：%v", err)
	}
	if len(s.Replies) != 1 {
		t.Fatalf("渲染失败应回退一条文本，实际 %d 条", len(s.Replies))
	}
	for _, want := range []string{"民主空间站", "飞鹰风暴", "进行中", "数据时间：2026\\-09\\-16 20:00:00"} {
		if want == "投票" { // 「选举 / 跃迁截止」里含「选举」，这里用整段文案断言更直白
			if !strings.Contains(s.Replies[0], "选举 / 跃迁截止：2026\\-09\\-16 20:39") {
				t.Errorf("回退文本缺少截止时间：\n%s", s.Replies[0])
			}
			continue
		}
		if !strings.Contains(s.Replies[0], want) {
			t.Errorf("回退文本缺少 %q：\n%s", want, s.Replies[0])
		}
	}
}

// TestStationServiceErrorReplies 校验上游错误转成中文提示：限流单独说明，其它用各自的领域文案。
func TestStationServiceErrorReplies(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		err  error
		want string
	}{
		{"events 限流", "events", hd2.ErrRateLimited, "上游接口限流中，请稍后再试。"},
		{"events 其它错误", "events", errors.New("网络不通"), eventsFailureReply},
		{"stations 限流", "stations", hd2.ErrRateLimited, "上游接口限流中，请稍后再试。"},
		{"stations 其它错误", "stations", errors.New("网络不通"), stationsFailureReply},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &plugtest.Sender{GroupID: -100}
			svc := &fakeService{}
			if c.cmd == "events" {
				svc.eventsErr = c.err
			} else {
				svc.stationsErr = c.err
			}
			if err := runHandler(t, c.cmd, s, svc, &plugtest.Renderer{Img: []byte("假图片")}, plugtest.MessageUpdate(-100, "/"+c.cmd)); err != nil {
				t.Fatalf("执行 /%s 失败：%v", c.cmd, err)
			}
			if len(s.Replies) != 1 || s.Replies[0] != c.want {
				t.Fatalf("回复错误：期望 %q，实际 %v", c.want, s.Replies)
			}
		})
	}
}

// TestStationHandlersIgnoreOtherChatAndNonMessage 校验非目标群与非消息更新一律不回应。
func TestStationHandlersIgnoreOtherChatAndNonMessage(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{
		eventsErr:   errors.New("不应被调用"),
		stationsErr: errors.New("不应被调用"),
	}
	callback := tgbotapi.Update{CallbackQuery: &tgbotapi.CallbackQuery{ID: "1"}}
	for _, name := range []string{"events", "stations"} {
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

// TestStationSendErrorPropagates 校验发送失败会把错误上抛给分发层记录日志。
func TestStationSendErrorPropagates(t *testing.T) {
	svc := &fakeService{
		events:   hd2.Result[[]hd2.PlanetEvent]{Value: testEvents(), FetchedAt: testFetchedAt()},
		stations: hd2.Result[[]hd2.SpaceStation]{Value: testStations(), FetchedAt: testFetchedAt()},
	}
	for _, name := range []string{"events", "stations"} {
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

// TestStationLogsCarryCommandAndChat 校验两条指令的两条失败路径，日志里都带命令名与群 id。
// 只断言前缀（例如「/stations 渲染失败」）的话，把 chat=%d 删掉也全绿，排查时定位不到群。
func TestStationLogsCarryCommandAndChat(t *testing.T) {
	cases := []struct {
		name    string
		cmd     string
		svc     *fakeService
		wantLog string
	}{
		{
			name:    "events 渲染失败",
			cmd:     "events",
			svc:     &fakeService{events: hd2.Result[[]hd2.PlanetEvent]{Value: testEvents(), FetchedAt: testFetchedAt()}},
			wantLog: "/events 渲染失败",
		},
		{
			name:    "events 查询失败",
			cmd:     "events",
			svc:     &fakeService{eventsErr: errors.New("网络不通")},
			wantLog: "/events 查询失败",
		},
		{
			name:    "stations 渲染失败",
			cmd:     "stations",
			svc:     &fakeService{stations: hd2.Result[[]hd2.SpaceStation]{Value: testStations(), FetchedAt: testFetchedAt()}},
			wantLog: "/stations 渲染失败",
		},
		{
			name:    "stations 查询失败",
			cmd:     "stations",
			svc:     &fakeService{stationsErr: errors.New("网络不通")},
			wantLog: "/stations 查询失败",
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

// TestStationDefaultDisplayLocation 校验调用方没给时区时按东八区展示（loc 为 nil 的兜底路径）。
// 与 TestOrdersDefaultDisplayLocation 同款：bot 层没配时区时，卡片与文本回退都必须是东八区，
// 否则快照降级（UTC）的时间会比群里实际时间早 8 小时。
func TestStationDefaultDisplayLocation(t *testing.T) {
	end := time.Date(2026, 9, 18, 11, 2, 12, 0, time.UTC)
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{events: hd2.Result[[]hd2.PlanetEvent]{
		Value: []hd2.PlanetEvent{{
			ID: 5695, EventType: 1, Faction: "Terminids", Health: 1, MaxHealth: 2,
			PlanetIndex: 110, PlanetName: "BEKVAM III", EndTime: &end,
		}},
		FetchedAt: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC),
	}}
	for _, h := range Handlers(s, svc, plugtest.FailRenderer(), nil) {
		if h.Name != "events" {
			continue
		}
		if err := h.Run(plugtest.MessageUpdate(-100, "/events")); err != nil {
			t.Fatalf("执行 /events 失败：%v", err)
		}
	}
	if len(s.Replies) != 1 {
		t.Fatalf("渲染失败应回退一条文本，实际 %d 条", len(s.Replies))
	}
	if !strings.Contains(s.Replies[0], "20:00:00") {
		t.Errorf("数据时间应按东八区展示（UTC 12:00 → 20:00），实际：\n%s", s.Replies[0])
	}
	if !strings.Contains(s.Replies[0], "至 2026\\-09\\-18 19:02") {
		t.Errorf("结束时间应按东八区展示（UTC 11:02 → 19:02），实际：\n%s", s.Replies[0])
	}
}

// TestStationCardsSmokeWithRealBrowser 是两张卡片的真浏览器冒烟：默认跳过，用 HD2_RENDER_SMOKE=1 打开。
func TestStationCardsSmokeWithRealBrowser(t *testing.T) {
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

	cards := []struct {
		file string
		card render.Card
	}{
		{"hd2_events_card.png", render.Card{Name: eventsCardName, Data: BuildEventsCard(testEvents(), testFetchedAt(), true)}},
		{"hd2_stations_card.png", render.Card{Name: stationsCardName, Data: BuildStationsCard(testStations(), testFetchedAt(), false)}},
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
		out := filepath.Join(os.TempDir(), c.file)
		if err := os.WriteFile(out, img, 0o600); err != nil {
			t.Fatalf("写出 %s 失败：%v", c.file, err)
		}
		t.Logf("%s：耗时 %s，尺寸 %dx%d，体积 %d 字节", c.file, elapsed, width, height, len(img))
	}
}
