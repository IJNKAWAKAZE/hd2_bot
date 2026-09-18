package planets

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/ijnkawakaze/telegram-bot-api"

	"hd2_bot/src/core/bot"
	"hd2_bot/src/hd2"
	"hd2_bot/src/plugins/plugutil"
	"hd2_bot/src/plugins/plugutil/plugtest"
	"hd2_bot/src/render"
	"hd2_bot/src/translate"
)

// 编译期断言：真实实现满足本插件依赖的接口。
var (
	_ PlanetsService  = (*hd2.Service)(nil)
	_ Sender          = (*bot.Bot)(nil)
	_ render.Renderer = (*render.Engine)(nil)
)

// fakeService 是 PlanetsService 的假实现，可以精确模拟上游错误与战况缺失。
type fakeService struct {
	planets    hd2.Result[[]hd2.Planet]
	planetsErr error
	war        hd2.Result[*hd2.War]
	warErr     error
	planetsGot int
	warGot     int

	// 单星球卡的补充数据（兴趣点与行动变量）：用例只填自己要验的那一项，
	// 其余保持零值——零值就是「空结果、没有错误」，正好对应「这项数据取到了但列表是空的」。
	campaigns      hd2.Result[[]hd2.Campaign]
	campaignsErr   error
	assignments    hd2.Result[[]hd2.Assignment]
	assignmentsErr error
	stations       hd2.Result[[]hd2.SpaceStation]
	stationsErr    error
	effects        hd2.Result[[]hd2.PlanetEffect]
	effectsErr     error
}

// Planets 返回预先设定的星球列表。
func (f *fakeService) Planets(ctx context.Context) (hd2.Result[[]hd2.Planet], error) {
	f.planetsGot++
	return f.planets, f.planetsErr
}

// War 返回预先设定的战况；/planets 只用它取在线士兵。
func (f *fakeService) War(ctx context.Context) (hd2.Result[*hd2.War], error) {
	f.warGot++
	return f.war, f.warErr
}

// Campaigns 返回预先设定的战役列表。
func (f *fakeService) Campaigns(context.Context) (hd2.Result[[]hd2.Campaign], error) {
	return f.campaigns, f.campaignsErr
}

// Assignments 返回预先设定的重要指令。
func (f *fakeService) Assignments(context.Context) (hd2.Result[[]hd2.Assignment], error) {
	return f.assignments, f.assignmentsErr
}

// Stations 返回预先设定的空间站列表。
func (f *fakeService) Stations(context.Context) (hd2.Result[[]hd2.SpaceStation], error) {
	return f.stations, f.stationsErr
}

// PlanetEffects 返回预先设定的行动变量。
func (f *fakeService) PlanetEffects(context.Context) (hd2.Result[[]hd2.PlanetEffect], error) {
	return f.effects, f.effectsErr
}

// resolveFixtures 是参数解析用例的固定数据：5 颗覆盖中英文与前后缀匹配的星球。
func resolveFixtures() []hd2.Planet {
	return []hd2.Planet{
		{Index: 26, Name: "Nublaria I", Sector: "Akira", CurrentOwner: "Humans"},
		{Index: 110, Name: "Bekvam III", Sector: "Akira", CurrentOwner: "Automaton"},
		{Index: 216, Name: "Peacock", Sector: "Jin Xi", CurrentOwner: "Terminids"},
		{Index: 93, Name: "New Stockholm", Sector: "Ymir", CurrentOwner: "Illuminate"},
		{Index: 22, Name: "Acamar IV", Sector: "Valdis", CurrentOwner: "Humans"},
	}
}

// testPlanets 是总览用例的固定数据：一颗有事件、两颗有进攻行动、一颗高人气但无战事（不该进热点）。
func testPlanets() []hd2.Planet {
	return []hd2.Planet{
		{
			Index: 26, Name: "Nublaria I", Sector: "Akira", CurrentOwner: "Humans", InitialOwner: "Humans",
			Health: 1000000, MaxHealth: 1000000, RegenPerSecond: 0,
			Statistics: hd2.PlanetStats{PlayerCount: 4},
		},
		{
			Index: 110, Name: "Bekvam III", Sector: "Akira", CurrentOwner: "Automaton", InitialOwner: "Humans",
			// 群系按上游实测值给（2026-09-17：Ethereal Jungle）：单星球卡的群系头图只有拿到群系名才会出现，
			// 夹具里留空会让冒烟图永远看不到头图，等于这条渲染路径没人验。
			Biome:     hd2.Biome{Name: "Ethereal Jungle"},
			Health:    307863,
			MaxHealth: 1600000, RegenPerSecond: 5.5555553,
			Statistics: hd2.PlanetStats{PlayerCount: 19507},
		},
		{
			Index: 268, Name: "Luxuriant", Sector: "Jin Xi", CurrentOwner: "Humans", InitialOwner: "Humans",
			Health: 2000000, MaxHealth: 2000000, Attacking: []int{216},
			Event: &hd2.PlanetEvent{
				ID: 5695, EventType: 1, Faction: "Terminids", Health: 1499670, MaxHealth: 1500000,
				StartTime: time.Date(2026, 9, 14, 11, 2, 33, 0, time.UTC),
				EndTime:   eventEndAt(),
			},
			Statistics: hd2.PlanetStats{PlayerCount: 1846},
		},
		{
			Index: 216, Name: "Peacock", Sector: "Jin Xi", CurrentOwner: "Terminids", InitialOwner: "Humans",
			Health: 1300000, MaxHealth: 1300000, Attacking: []int{268},
			Statistics: hd2.PlanetStats{PlayerCount: 151},
		},
		{
			Index: 93, Name: "New Stockholm", Sector: "Ymir", CurrentOwner: "Illuminate", InitialOwner: "Humans",
			Health: 937571, MaxHealth: 1000000, RegenPerSecond: 8.333333,
			Statistics: hd2.PlanetStats{PlayerCount: 45},
		},
	}
}

// testFetchedAt 是卡片用例共用的数据时间：固定值，用例不依赖真实时钟。
func testFetchedAt() time.Time {
	return time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
}

// eventEndAt 是防守战夹具的截止时刻：从 testFetchedAt() 起正好还有 32 小时，
// 于是倒计时文案是「1天 08:00:00」，用例可以直接断言这个常量而不必自己算一遍。
func eventEndAt() *time.Time {
	end := testFetchedAt().Add(32 * time.Hour)
	return &end
}

// runHandler 取出指定名字的处理器并执行，返回它的错误。
// 假 Sender、假渲染器、更新构造与「按名字找处理器」都在 plugtest 里，
// 这里只留本插件的接线（Handlers 的参数是各插件自己的）。
// 默认注入透传翻译层：不关心翻译的用例拿到的行为与「翻译没配好」时一致（正文保持英文）。
func runHandler(t *testing.T, name string, s *plugtest.Sender, svc PlanetsService, r render.Renderer, loc *time.Location, prefix string, update tgbotapi.Update) error {
	t.Helper()
	return runHandlerTrans(t, name, s, svc, r, loc, prefix, translate.Passthrough(), update)
}

// runHandlerTrans 与 runHandler 相同，但注入指定的翻译层（nil 表示没配翻译）。
func runHandlerTrans(t *testing.T, name string, s *plugtest.Sender, svc PlanetsService, r render.Renderer, loc *time.Location, prefix string, trans translate.Translator, update tgbotapi.Update) error {
	t.Helper()
	return plugtest.RunHandler(t, Handlers(s, svc, r, loc, prefix, trans), name, update)
}

// TestResolvePlanetMatchesIndexAndNames 校验参数解析顺序：编号 → 英文精确 → 英文前缀 → 中文精确 → 中文包含。
func TestResolvePlanetMatchesIndexAndNames(t *testing.T) {
	list := resolveFixtures()
	cases := []struct {
		name string
		arg  string
		want string
	}{
		{"纯数字按编号匹配", "26", "Nublaria I"},
		{"英文精确匹配", "bekvam iii", "Bekvam III"},
		{"英文精确匹配忽略大小写与首尾空白", "  ACAMAR IV  ", "Acamar IV"},
		{"英文前缀匹配", "nub", "Nublaria I"},
		{"中文精确匹配", "孔雀十一", "Peacock"},
		{"中文包含匹配", "斯德哥尔摩", "New Stockholm"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			planet, candidates, ok := ResolvePlanet(list, tc.arg)
			if !ok {
				t.Fatalf("期望命中 %s，实际未命中（候选 %d 条）", tc.want, len(candidates))
			}
			if planet.Name != tc.want {
				t.Errorf("命中星球错误：期望 %s，实际 %s", tc.want, planet.Name)
			}
			if len(candidates) != 0 {
				t.Errorf("唯一命中时不应返回候选：%v", candidates)
			}
		})
	}
}

// TestResolvePlanetNotFound 校验查无此星球时不 panic、也不乱给候选。
func TestResolvePlanetNotFound(t *testing.T) {
	cases := []struct{ name, arg string }{
		{"编号不存在", "99999"},
		{"中文名不存在", "不存在的星球"},
		{"英文名不存在", "zzzz"},
		{"空参数", "   "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, candidates, ok := ResolvePlanet(resolveFixtures(), tc.arg)
			if ok {
				t.Fatal("查无此星球时不应判定为命中")
			}
			if len(candidates) != 0 {
				t.Errorf("查无此星球时不应给候选：%v", candidates)
			}
		})
	}
}

// TestResolvePlanetAmbiguousReturnsCandidates 校验命中多条时交出候选列表而不是硬选一颗。
func TestResolvePlanetAmbiguousReturnsCandidates(t *testing.T) {
	// "n" 同时是 Nublaria I 与 New Stockholm 的前缀
	_, candidates, ok := ResolvePlanet(resolveFixtures(), "n")
	if ok {
		t.Fatal("命中多条时不应判定为唯一命中")
	}
	if len(candidates) != 2 {
		t.Fatalf("期望 2 条候选，实际 %d 条", len(candidates))
	}
}

// TestResolvePlanetCandidatesLimitedToFive 校验候选最多 5 条，避免回复刷屏。
func TestResolvePlanetCandidatesLimitedToFive(t *testing.T) {
	var list []hd2.Planet
	for i := 0; i < 7; i++ {
		list = append(list, hd2.Planet{Index: i, Name: fmt.Sprintf("Ymir %d", i), CurrentOwner: "Humans"})
	}
	_, candidates, ok := ResolvePlanet(list, "ymir")
	if ok {
		t.Fatal("命中多条时不应判定为唯一命中")
	}
	// 这里刻意写死 5 而不是引用 maxCandidates：拿常量跟自己比，改大改小都会一起变，等于没测。
	if len(candidates) != 5 {
		t.Fatalf("候选应截断到 5 条，实际 %d 条", len(candidates))
	}
}

// TestPlanetsHandlerSendsPhotoOnly 校验 /planets 成功时只出一张总览图，
// 不再附「点按钮搜星球」的提示（用户 2026-09-17 要求：总览卡本身就是结果，搜索入口在 /planet）。
func TestPlanetsHandlerSendsPhotoOnly(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{
		planets: hd2.Result[[]hd2.Planet]{Value: testPlanets(), FetchedAt: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)},
		war:     hd2.Result[*hd2.War]{Value: &hd2.War{Statistics: hd2.Statistics{PlayerCount: 123456}}},
	}
	r := &plugtest.Renderer{Img: []byte("png-bytes")}

	if err := runHandler(t, "planets", s, svc, r, time.UTC, "星球-", plugtest.MessageUpdate(-100, "/planets")); err != nil {
		t.Fatalf("执行 /planets 失败：%v", err)
	}
	if len(s.Calls) != 1 || s.Calls[0] != "photo" {
		t.Fatalf("期望只发一张图，实际调用 %v", s.Calls)
	}
	if len(s.Markups) != 0 || len(s.Replies) != 0 {
		t.Fatalf("不该再发带按钮的提示，实际 %d 条按钮 / %d 条文本", len(s.Markups), len(s.Replies))
	}
	if len(r.Cards) != 1 || r.Cards[0].Name != "planets" {
		t.Fatalf("应渲染 planets 卡片，实际 %+v", r.Cards)
	}
	card, ok := r.Cards[0].Data.(PlanetsCard)
	if !ok {
		t.Fatalf("视图模型类型错误：%T", r.Cards[0].Data)
	}
	if card.PlayerCount != "123,456" {
		t.Errorf("在线士兵应带千分位，实际 %q", card.PlayerCount)
	}
	if len(card.Hotspots) == 0 {
		t.Error("总览卡应带上热点星球")
	}
}

// TestPlanetsHandlerHandlesMissingWar 校验战况拿不到时在线士兵显示「—」，不阻塞卡片。
func TestPlanetsHandlerHandlesMissingWar(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{
		planets: hd2.Result[[]hd2.Planet]{Value: testPlanets(), FetchedAt: time.Now()},
		warErr:  errors.New("上游返回状态码 500：boom"),
	}
	r := &plugtest.Renderer{Img: []byte("png-bytes")}

	if err := runHandler(t, "planets", s, svc, r, time.UTC, "星球-", plugtest.MessageUpdate(-100, "/planets")); err != nil {
		t.Fatalf("战况失败不应让 /planets 失败：%v", err)
	}
	card, ok := r.Cards[0].Data.(PlanetsCard)
	if !ok {
		t.Fatalf("视图模型类型错误：%T", r.Cards[0].Data)
	}
	if card.PlayerCount != plugutil.DashText {
		t.Errorf("战况缺失时应显示 %q，实际 %q", plugutil.DashText, card.PlayerCount)
	}
	if len(s.Photos) != 1 {
		t.Errorf("战况缺失不应影响出图，实际发图 %d 张", len(s.Photos))
	}
}

// TestPlanetsHandlerSkipsHintWhenPrefixEmpty 校验行内前缀没配时不发带按钮的提示（按钮点了也搜不出东西）。
func TestPlanetsHandlerSkipsHintWhenPrefixEmpty(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{
		planets: hd2.Result[[]hd2.Planet]{Value: testPlanets()},
		war:     hd2.Result[*hd2.War]{Value: &hd2.War{}},
	}
	r := &plugtest.Renderer{Img: []byte("png-bytes")}

	if err := runHandler(t, "planets", s, svc, r, time.UTC, "", plugtest.MessageUpdate(-100, "/planets")); err != nil {
		t.Fatalf("执行 /planets 失败：%v", err)
	}
	if len(s.Photos) != 1 {
		t.Fatalf("仍应出图，实际 %v", s.Calls)
	}
	if len(s.Markups) != 0 {
		t.Errorf("前缀为空时不应发带按钮的提示，实际 %d 条", len(s.Markups))
	}
}

// TestPlanetsHandlerWithoutRendererFallsBackToText 校验未启用渲染（renderer 为 nil）时直接走文本。
func TestPlanetsHandlerWithoutRendererFallsBackToText(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{
		planets: hd2.Result[[]hd2.Planet]{Value: testPlanets(), FetchedAt: time.Now()},
		war:     hd2.Result[*hd2.War]{Value: &hd2.War{}},
	}
	if err := runHandler(t, "planets", s, svc, nil, time.UTC, "星球-", plugtest.MessageUpdate(-100, "/planets")); err != nil {
		t.Fatalf("执行 /planets 失败：%v", err)
	}
	if len(s.Photos) != 0 {
		t.Error("未启用渲染时不应尝试发图")
	}
	if len(s.Replies) != 1 || !strings.Contains(s.Replies[0], "战线总览") {
		t.Fatalf("应回退到文本总览，实际 %v", s.Replies)
	}
}

// TestPlanetsHandlerFallsBackToTextOnRenderError 校验渲染失败时回退纯文本。
func TestPlanetsHandlerFallsBackToTextOnRenderError(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{
		planets: hd2.Result[[]hd2.Planet]{Value: testPlanets(), FetchedAt: time.Now()},
		war:     hd2.Result[*hd2.War]{Value: &hd2.War{Statistics: hd2.Statistics{PlayerCount: 10}}},
	}
	r := &plugtest.Renderer{Err: errors.New("渲染超时")}

	if err := runHandler(t, "planets", s, svc, r, time.UTC, "星球-", plugtest.MessageUpdate(-100, "/planets")); err != nil {
		t.Fatalf("渲染失败应回退文本而不是报错：%v", err)
	}
	if len(s.Photos) != 0 {
		t.Error("渲染失败时不应发图")
	}
	if len(s.Replies) != 1 || !strings.Contains(s.Replies[0], "战线总览") {
		t.Fatalf("文本回退应含总览数字，实际 %v", s.Replies)
	}
	if len(s.Markups) != 0 {
		t.Error("渲染失败时不应再发带按钮的提示")
	}
}

// TestPlanetsHandlerEmptyPlanetsRepliesFailure 校验上游返回空列表时提示稍后再试，而不是渲染空卡片。
func TestPlanetsHandlerEmptyPlanetsRepliesFailure(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{planets: hd2.Result[[]hd2.Planet]{Value: []hd2.Planet{}}}
	r := &plugtest.Renderer{Img: []byte("png-bytes")}

	if err := runHandler(t, "planets", s, svc, r, time.UTC, "星球-", plugtest.MessageUpdate(-100, "/planets")); err != nil {
		t.Fatalf("执行 /planets 失败：%v", err)
	}
	if len(r.Cards) != 0 {
		t.Error("空数据不应渲染卡片")
	}
	if len(s.Replies) != 1 || !strings.Contains(s.Replies[0], "暂时获取不到星球数据") {
		t.Fatalf("空数据提示错误：%v", s.Replies)
	}
}

// TestPlanetsHandlerRateLimited 校验上游限流时给出中文提示。
func TestPlanetsHandlerRateLimited(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{planetsErr: fmt.Errorf("请求 planets 失败：%w", hd2.ErrRateLimited)}
	if err := runHandler(t, "planets", s, svc, &plugtest.Renderer{}, time.UTC, "星球-", plugtest.MessageUpdate(-100, "/planets")); err != nil {
		t.Fatalf("执行 /planets 失败：%v", err)
	}
	if len(s.Replies) != 1 || !strings.Contains(s.Replies[0], "上游接口限流中，请稍后再试") {
		t.Fatalf("限流提示错误：%v", s.Replies)
	}
}

// TestPlanetsHandlerIgnoresOtherChat 校验非目标群的消息被忽略。
func TestPlanetsHandlerIgnoresOtherChat(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{planetsErr: errors.New("不应被调用")}
	if err := runHandler(t, "planets", s, svc, &plugtest.Renderer{}, time.UTC, "星球-", plugtest.MessageUpdate(-200, "/planets")); err != nil {
		t.Fatalf("执行 /planets 失败：%v", err)
	}
	if len(s.Calls) != 0 {
		t.Fatalf("非目标群不应有任何回复：%v", s.Calls)
	}
}

// TestPlanetHandlerSendsCard 校验 /planet 命中后渲染单星球卡片。
func TestPlanetHandlerSendsCard(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{
		planets: hd2.Result[[]hd2.Planet]{Value: testPlanets(), FetchedAt: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)},
	}
	r := &plugtest.Renderer{Img: []byte("png-bytes")}

	if err := runHandler(t, "planet", s, svc, r, time.UTC, "星球-", plugtest.MessageUpdate(-100, "/planet 孔雀十一")); err != nil {
		t.Fatalf("执行 /planet 失败：%v", err)
	}
	if len(s.Photos) != 1 {
		t.Fatalf("应发出 1 张卡片，实际 %d 张", len(s.Photos))
	}
	if len(r.Cards) != 1 || r.Cards[0].Name != "planet" {
		t.Fatalf("应渲染 planet 卡片，实际 %+v", r.Cards)
	}
	card, ok := r.Cards[0].Data.(PlanetCard)
	if !ok {
		t.Fatalf("视图模型类型错误：%T", r.Cards[0].Data)
	}
	// 星球名在卡面里（外壳标题固定是「星球情报」）。
	if card.NameChinese != "孔雀十一" || card.NameEnglish != "Peacock" {
		t.Errorf("卡面应给出中英文名，实际 %q / %q", card.NameChinese, card.NameEnglish)
	}
	if svc.warGot != 0 {
		t.Errorf("/planet 不该查战况，实际调用 %d 次", svc.warGot)
	}
}

// TestPlanetHandlerAcceptsBotSuffixAndSpaces 校验「/planet@机器人名 参数」与多余空格都能解析出参数。
func TestPlanetHandlerAcceptsBotSuffixAndSpaces(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{planets: hd2.Result[[]hd2.Planet]{Value: testPlanets()}}
	r := &plugtest.Renderer{Img: []byte("png-bytes")}

	if err := runHandler(t, "planet", s, svc, r, time.UTC, "星球-", plugtest.MessageUpdate(-100, "/planet@maa_remote_bot   Peacock  ")); err != nil {
		t.Fatalf("执行 /planet 失败：%v", err)
	}
	if len(s.Photos) != 1 {
		t.Fatalf("应识别出参数并出图，实际 %v", s.Calls)
	}
}

// TestPlanetHandlerAmbiguousRepliesCandidates 校验命中多条时回文本候选、不出图。
func TestPlanetHandlerAmbiguousRepliesCandidates(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{planets: hd2.Result[[]hd2.Planet]{Value: resolveFixtures()}}
	r := &plugtest.Renderer{Img: []byte("png-bytes")}

	if err := runHandler(t, "planet", s, svc, r, time.UTC, "星球-", plugtest.MessageUpdate(-100, "/planet n")); err != nil {
		t.Fatalf("执行 /planet 失败：%v", err)
	}
	if len(s.Photos) != 0 {
		t.Error("歧义时不应出图")
	}
	if len(s.Replies) != 1 {
		t.Fatalf("应回复候选文本，实际 %v", s.Calls)
	}
	text := s.Replies[0]
	if !strings.Contains(text, "更精确") {
		t.Errorf("候选提示应教用户怎么更精确地查，实际：\n%s", text)
	}
	for _, want := range []string{"努布拉里亚I", "新斯德哥尔摩"} {
		if !strings.Contains(text, want) {
			t.Errorf("候选列表缺少 %s：\n%s", want, text)
		}
	}
}

// TestPlanetHandlerNotFoundRepliesHint 校验查无此星球时给用法提示、不出图。
func TestPlanetHandlerNotFoundRepliesHint(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{planets: hd2.Result[[]hd2.Planet]{Value: resolveFixtures()}}
	r := &plugtest.Renderer{Img: []byte("png-bytes")}

	if err := runHandler(t, "planet", s, svc, r, time.UTC, "星球-", plugtest.MessageUpdate(-100, "/planet 不存在的星球")); err != nil {
		t.Fatalf("执行 /planet 失败：%v", err)
	}
	if len(s.Photos) != 0 {
		t.Error("查无此星球时不应出图")
	}
	if len(s.Replies) != 1 || !strings.Contains(s.Replies[0], "找不到") {
		t.Fatalf("查无此星球的提示错误：%v", s.Replies)
	}
}

// TestPlanetHandlerEmptyArgShowsInlineButton 校验不带参数时发的是「点按钮搜星球」的提示，
// 而不是一句用法（用户 2026-09-17 要求照明日方舟的 /operator 那样弹行内搜索按钮）。
func TestPlanetHandlerEmptyArgShowsInlineButton(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{planets: hd2.Result[[]hd2.Planet]{Value: resolveFixtures()}}
	if err := runHandler(t, "planet", s, svc, &plugtest.Renderer{}, time.UTC, "星球-", plugtest.MessageUpdate(-100, "/planet")); err != nil {
		t.Fatalf("执行 /planet 失败：%v", err)
	}
	if len(s.Photos) != 0 {
		t.Error("不带参数时不该出图")
	}
	if len(s.Markups) != 1 {
		t.Fatalf("不带参数时应发一条带按钮的提示，实际 %d 条", len(s.Markups))
	}
	if len(s.Replies) != 1 || strings.Contains(s.Replies[0], "用法") {
		t.Errorf("不带参数时不该再回用法文本，实际 %v", s.Replies)
	}
	button := s.Markups[0].InlineKeyboard[0][0]
	if button.SwitchInlineQueryCurrentChat == nil || *button.SwitchInlineQueryCurrentChat != "星球-" {
		t.Fatalf("按钮应带行内查询前缀 星球-，实际 %v", button.SwitchInlineQueryCurrentChat)
	}
}

// TestPlanetHandlerFallsBackToTextOnRenderError 校验单星球卡渲染失败时回退纯文本。
func TestPlanetHandlerFallsBackToTextOnRenderError(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{planets: hd2.Result[[]hd2.Planet]{Value: testPlanets()}}
	r := &plugtest.Renderer{Err: errors.New("浏览器已断开")}

	if err := runHandler(t, "planet", s, svc, r, time.UTC, "星球-", plugtest.MessageUpdate(-100, "/planet 孔雀十一")); err != nil {
		t.Fatalf("渲染失败应回退文本而不是报错：%v", err)
	}
	if len(s.Photos) != 0 {
		t.Error("渲染失败时不应发图")
	}
	if len(s.Replies) != 1 || !strings.Contains(s.Replies[0], "孔雀十一") {
		t.Fatalf("文本回退应含星球名，实际 %v", s.Replies)
	}
}

// TestPlanetHandlerIgnoresNonMessage 校验非消息更新被忽略。
func TestPlanetHandlerIgnoresNonMessage(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{planetsErr: errors.New("不应被调用")}
	update := tgbotapi.Update{CallbackQuery: &tgbotapi.CallbackQuery{
		ID:      "1",
		Message: &tgbotapi.Message{Chat: &tgbotapi.Chat{ID: -100}},
	}}
	if err := runHandler(t, "planet", s, svc, &plugtest.Renderer{}, time.UTC, "星球-", update); err != nil {
		t.Fatalf("执行 /planet 失败：%v", err)
	}
	if len(s.Calls) != 0 {
		t.Fatalf("非消息更新不应回复：%v", s.Calls)
	}
}

// TestPlanetsHandlerSendErrorPropagates 校验发送失败会把错误上抛给分发层记录日志。
func TestPlanetsHandlerSendErrorPropagates(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100, Err: errors.New("发送失败")}
	svc := &fakeService{
		planets: hd2.Result[[]hd2.Planet]{Value: testPlanets()},
		war:     hd2.Result[*hd2.War]{Value: &hd2.War{}},
	}
	r := &plugtest.Renderer{Img: []byte("png-bytes")}
	if err := runHandler(t, "planets", s, svc, r, time.UTC, "星球-", plugtest.MessageUpdate(-100, "/planets")); err == nil {
		t.Fatal("发送失败应返回错误")
	}
}

// TestInlineHintTextHasNoMarkdownSpecials 守住「静态文案不含未转义的 MarkdownV2 特殊字符」这条约定：
// 提示文案是常量、不经过 bot.Escape，一旦有人塞进 ASCII 特殊字符，Telegram 会直接拒收整条消息。
func TestInlineHintTextHasNoMarkdownSpecials(t *testing.T) {
	if strings.ContainsAny(inlineHintText, "_*[]()~`>#+-=|{}.!") {
		t.Fatalf("静态文案不能含 MarkdownV2 特殊字符：%q", inlineHintText)
	}
	if strings.TrimSpace(inlineHintButton) == "" {
		t.Fatal("按钮文案不能为空，否则 Telegram 会拒收")
	}
}

// TestPlanetHandlerUsesConfiguredInlinePrefix 校验按钮里的行内前缀来自配置，而不是写死的默认值。
// 前缀是可以改的配置项：写死之后用户配了新前缀，输入框里预填的文本会跟机器人注册的前缀对不上，
// 行内搜索会直接失效，而默认值恰好是「星球-」的用例看不出来。
func TestPlanetHandlerUsesConfiguredInlinePrefix(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{
		planets: hd2.Result[[]hd2.Planet]{Value: testPlanets(), FetchedAt: time.Now()},
		war:     hd2.Result[*hd2.War]{Value: &hd2.War{}},
	}
	r := &plugtest.Renderer{Img: []byte("png-bytes")}

	if err := runHandler(t, "planet", s, svc, r, time.UTC, "查星-", plugtest.MessageUpdate(-100, "/planet")); err != nil {
		t.Fatalf("执行 /planet 失败：%v", err)
	}
	if len(s.Markups) != 1 {
		t.Fatalf("应发一条带按钮的提示，实际 %d 条", len(s.Markups))
	}
	got := s.Markups[0].InlineKeyboard[0][0].SwitchInlineQueryCurrentChat
	if got == nil || *got != "查星-" {
		t.Fatalf("按钮应带配置里的前缀 查星-，实际 %v", got)
	}
}

// TestPlanetsHandlerQueriesServiceOnce 校验 /planets 只查一次星球列表：
// 多查一次既白占上游配额，卡片与在线士兵也可能来自两个不同时刻。
func TestPlanetsHandlerQueriesServiceOnce(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{
		planets: hd2.Result[[]hd2.Planet]{Value: testPlanets(), FetchedAt: time.Now()},
		war:     hd2.Result[*hd2.War]{Value: &hd2.War{}},
	}
	r := &plugtest.Renderer{Img: []byte("png-bytes")}

	if err := runHandler(t, "planets", s, svc, r, time.UTC, "星球-", plugtest.MessageUpdate(-100, "/planets")); err != nil {
		t.Fatalf("执行 /planets 失败：%v", err)
	}
	if svc.planetsGot != 1 {
		t.Errorf("星球列表应只查一次，实际 %d 次", svc.planetsGot)
	}
	if svc.warGot != 1 {
		t.Errorf("战况应只查一次（只为在线士兵那一行），实际 %d 次", svc.warGot)
	}
}

// TestPlanetsHandlerPassesStaleToCardAndText 校验快照降级（Stale=true）既标在卡片上、也写进文本回退。
// 少了这条，群友看到的就是一份「看起来是实时的」旧数据。
func TestPlanetsHandlerPassesStaleToCardAndText(t *testing.T) {
	stalePlanets := hd2.Result[[]hd2.Planet]{Value: testPlanets(), FetchedAt: time.Now(), Stale: true}
	staleWar := hd2.Result[*hd2.War]{Value: &hd2.War{}}

	// 出图路径：过期标记要落在卡片外壳上
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{planets: stalePlanets, war: staleWar}
	r := &plugtest.Renderer{Img: []byte("png-bytes")}
	if err := runHandler(t, "planets", s, svc, r, time.UTC, "星球-", plugtest.MessageUpdate(-100, "/planets")); err != nil {
		t.Fatalf("执行 /planets 失败：%v", err)
	}
	card, ok := r.Cards[0].Data.(PlanetsCard)
	if !ok {
		t.Fatalf("视图模型类型错误：%T", r.Cards[0].Data)
	}
	if !card.Stale {
		t.Error("降级数据应把过期标记透传给总览卡片")
	}

	// 文本回退路径：同一份降级数据也要带上过期提示
	s2 := &plugtest.Sender{GroupID: -100}
	svc2 := &fakeService{planets: stalePlanets, war: staleWar}
	r2 := &plugtest.Renderer{Err: errors.New("渲染超时")}
	if err := runHandler(t, "planets", s2, svc2, r2, time.UTC, "星球-", plugtest.MessageUpdate(-100, "/planets")); err != nil {
		t.Fatalf("执行 /planets 失败：%v", err)
	}
	if len(s2.Replies) != 1 || !strings.Contains(s2.Replies[0], "数据可能已过期") {
		t.Fatalf("文本回退应提示数据可能已过期，实际 %q", s2.Replies)
	}
}

// TestPlanetUsageTextHasNoMarkdownSpecials 守住用法文案这条常量不含 MarkdownV2 保留字符。
// 它是常量、不经过 bot.Escape，混进一个 '>' 或 '.' 就会让 Telegram 拒收整条消息
// （'/planet 无参数' 与 '查无此星球' 两条路径都会发它）。
func TestPlanetUsageTextHasNoMarkdownSpecials(t *testing.T) {
	if strings.ContainsAny(planetUsageText, "_*[]()~`>#+-=|{}.!") {
		t.Fatalf("用法文案不能含 MarkdownV2 保留字符：%q", planetUsageText)
	}
}

// TestResolvePlanetCandidatesSortedByPlayers 校验候选顺序：在线士兵降序、同人数按编号升序。
// 人数多的星球更可能是用户想查的那颗；编号兜底保证同一关键字每次给出同样的顺序。
func TestResolvePlanetCandidatesSortedByPlayers(t *testing.T) {
	list := []hd2.Planet{
		{Index: 30, Name: "Ymir C", CurrentOwner: "Humans", Statistics: hd2.PlanetStats{PlayerCount: 10}},
		{Index: 10, Name: "Ymir A", CurrentOwner: "Humans", Statistics: hd2.PlanetStats{PlayerCount: 500}},
		{Index: 20, Name: "Ymir B", CurrentOwner: "Humans", Statistics: hd2.PlanetStats{PlayerCount: 10}},
	}
	_, candidates, ok := ResolvePlanet(list, "ymir")
	if ok {
		t.Fatal("命中多条时不应判定为唯一命中")
	}
	want := []int{10, 20, 30}
	if len(candidates) != len(want) {
		t.Fatalf("候选数量错误：%d", len(candidates))
	}
	for i, index := range want {
		if candidates[i].Index != index {
			t.Fatalf("候选顺序错误，第 %d 位应是编号 %d，实际 %d（全部候选 %v）", i+1, index, candidates[i].Index, candidates)
		}
	}
}

// TestPlanetsLogsCarryCommandAndChat 校验两条指令的失败路径，日志里都带命令名与群 id
// （含 /planets 里「战况拿不到」这条降级日志）：线上多群并发时靠这两个字段定位。
func TestPlanetsLogsCarryCommandAndChat(t *testing.T) {
	cases := []struct {
		name    string
		cmd     string
		text    string
		svc     *fakeService
		wantLog string
	}{
		{
			name:    "planets 渲染失败",
			cmd:     "planets",
			svc:     &fakeService{planets: hd2.Result[[]hd2.Planet]{Value: testPlanets(), FetchedAt: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}},
			wantLog: "/planets 渲染失败",
		},
		{
			name:    "planets 查询失败",
			cmd:     "planets",
			svc:     &fakeService{planetsErr: errors.New("网络不通")},
			wantLog: "/planets 查询失败",
		},
		{
			name:    "planet 渲染失败",
			cmd:     "planet",
			text:    "/planet 孔雀十一",
			svc:     &fakeService{planets: hd2.Result[[]hd2.Planet]{Value: testPlanets(), FetchedAt: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}},
			wantLog: "/planet 渲染失败",
		},
		{
			name:    "planet 查询失败",
			cmd:     "planet",
			text:    "/planet 孔雀十一",
			svc:     &fakeService{planetsErr: errors.New("网络不通")},
			wantLog: "/planet 查询失败",
		},
		{
			name:    "planets 战况查询失败的降级日志",
			cmd:     "planets",
			svc:     &fakeService{planets: hd2.Result[[]hd2.Planet]{Value: testPlanets(), FetchedAt: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}, warErr: errors.New("网络不通")},
			wantLog: "/planets 战况查询失败",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			logs := plugtest.CaptureLog(t)
			s := &plugtest.Sender{GroupID: -100}
			text := c.text
			if text == "" {
				text = "/" + c.cmd
			}
			if err := runHandler(t, c.cmd, s, c.svc, plugtest.FailRenderer(), time.UTC, "星球-", plugtest.MessageUpdate(-100, text)); err != nil {
				t.Fatalf("执行 %s 失败：%v", text, err)
			}
			plugtest.AssertLogFields(t, logs, c.wantLog, -100)
		})
	}
}

// TestPlanetHandlerTranslatesEnvironment 校验 /planet 把翻译层接到环境信息上：
// 文本回退（渲染失败）里出现的是中文群系名与危害名。
func TestPlanetHandlerTranslatesEnvironment(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{planets: hd2.Result[[]hd2.Planet]{Value: []hd2.Planet{testPlanetWithEnvironment()}, FetchedAt: testFetchedAt()}}
	trans := &plugtest.Translator{Out: []string{"落叶林", "温带林地", "酸雨风暴", "腐蚀性降雨会损坏护甲。"}}

	if err := runHandlerTrans(t, "planet", s, svc, plugtest.FailRenderer(), time.UTC, "星球-", trans, plugtest.MessageUpdate(-100, "/planet 7")); err != nil {
		t.Fatalf("执行 /planet 失败：%v", err)
	}
	if len(s.Replies) != 1 {
		t.Fatalf("应回一条文本消息，实际 %v", s.Calls)
	}
	for _, want := range []string{"落叶林", "温带林地", "酸雨风暴"} {
		if !strings.Contains(s.Replies[0], want) {
			t.Errorf("文本回退缺少译文 %q：\n%s", want, s.Replies[0])
		}
	}
	if len(trans.Texts) != 1 || len(trans.Texts[0]) != 4 {
		t.Fatalf("环境信息应送一次译、共 4 段，实际 %v", trans.Texts)
	}
}

// TestPlanetHandlerWithoutTranslatorKeepsEnglish 校验没配翻译层（nil）时 /planet 不 panic、
// 正文保持英文，且不写「翻译不可用」的说明（那条文案是给「配了但没翻成」用的）。
func TestPlanetHandlerWithoutTranslatorKeepsEnglish(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{planets: hd2.Result[[]hd2.Planet]{Value: []hd2.Planet{testPlanetWithEnvironment()}, FetchedAt: testFetchedAt()}}

	if err := runHandlerTrans(t, "planet", s, svc, plugtest.FailRenderer(), time.UTC, "星球-", nil, plugtest.MessageUpdate(-100, "/planet 7")); err != nil {
		t.Fatalf("执行 /planet 失败：%v", err)
	}
	if len(s.Replies) != 1 || !strings.Contains(s.Replies[0], "Deciduous Forest") {
		t.Fatalf("没有翻译层时应保持英文，实际 %v", s.Replies)
	}
	if strings.Contains(s.Replies[0], "翻译暂不可用") {
		t.Errorf("没配翻译层时不该写翻译不可用的说明：\n%s", s.Replies[0])
	}
}

// TestPlanetHandlerFillsExtras 校验 /planet 把补充数据填进单星球卡：
// 行动变量来自补充源，兴趣点里的「重要指令目标 / 战役进行中 / DSS 停靠中」来自另外三路数据。
func TestPlanetHandlerFillsExtras(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{
		planets:     hd2.Result[[]hd2.Planet]{Value: testPlanets(), FetchedAt: testFetchedAt()},
		campaigns:   hd2.Result[[]hd2.Campaign]{Value: []hd2.Campaign{{ID: 1, Planet: hd2.PlanetRef{Index: 110}, Type: 0}}},
		assignments: hd2.Result[[]hd2.Assignment]{Value: []hd2.Assignment{{ID: 1, Tasks: []hd2.Task{{Type: 11, Values: []int64{110}, ValueTypes: []int{12}}}}}},
		stations:    hd2.Result[[]hd2.SpaceStation]{Value: []hd2.SpaceStation{{ID32: 1, PlanetIndex: 110}}},
		effects:     hd2.Result[[]hd2.PlanetEffect]{Value: []hd2.PlanetEffect{{PlanetIndex: 110, EffectID: 1243}}},
	}
	r := &plugtest.Renderer{Img: []byte("png-bytes")}

	if err := runHandler(t, "planet", s, svc, r, time.UTC, "星球-", plugtest.MessageUpdate(-100, "/planet 110")); err != nil {
		t.Fatalf("执行 /planet 失败：%v", err)
	}
	if len(r.Cards) != 1 || r.Cards[0].Name != "planet" {
		t.Fatalf("应渲染 planet 卡片，实际 %+v", r.Cards)
	}
	card, ok := r.Cards[0].Data.(PlanetCard)
	if !ok {
		t.Fatalf("视图模型类型错误：%T", r.Cards[0].Data)
	}
	if len(card.EffectChips) != 1 || card.EffectChips[0].Name != "掠食变种" {
		t.Errorf("行动变量应来自补充源并翻译成中文：%+v", card.EffectChips)
	}
	if card.EffectNote != "" {
		t.Errorf("有行动变量时不该写说明文案，实际 %q", card.EffectNote)
	}
	texts := make([]string, 0, len(card.POIs))
	for _, poi := range card.POIs {
		texts = append(texts, poi.Text)
	}
	joined := strings.Join(texts, " ｜ ")
	for _, want := range []string{"🎯 重要指令目标", "⚔️ 解放战役进行中", "🛰️ DSS 民主空间站停靠中", "🌍 星区："} {
		if !strings.Contains(joined, want) {
			t.Errorf("兴趣点缺少 %q，实际 %q", want, joined)
		}
	}
}

// TestPlanetHandlerExtrasFailureKeepsCard 校验补充数据全部取不到时卡片照常出图：
// 行动变量写「暂不可用」，兴趣点只剩始终可得的「星区」——不能因为这几路数据挂了就整张卡失败。
func TestPlanetHandlerExtrasFailureKeepsCard(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	svc := &fakeService{
		planets:        hd2.Result[[]hd2.Planet]{Value: testPlanets(), FetchedAt: testFetchedAt()},
		campaignsErr:   errors.New("上游限流"),
		assignmentsErr: errors.New("上游限流"),
		stationsErr:    errors.New("上游限流"),
		effectsErr:     errors.New("补充源不可用"),
	}
	r := &plugtest.Renderer{Img: []byte("png-bytes")}

	if err := runHandler(t, "planet", s, svc, r, time.UTC, "星球-", plugtest.MessageUpdate(-100, "/planet 110")); err != nil {
		t.Fatalf("执行 /planet 失败：%v", err)
	}
	if len(s.Photos) != 1 {
		t.Fatalf("补充数据失败也应出图，实际调用 %v", s.Calls)
	}
	card := r.Cards[0].Data.(PlanetCard)
	if card.EffectNote != effectsUnknownText {
		t.Errorf("补充源没取到时卡片应写 %q，实际 %q", effectsUnknownText, card.EffectNote)
	}
	if len(card.POIs) != 1 || !strings.Contains(card.POIs[0].Text, "🌍 星区：") {
		t.Errorf("只剩星区这一枚兴趣点标签，实际 %+v", card.POIs)
	}
}
