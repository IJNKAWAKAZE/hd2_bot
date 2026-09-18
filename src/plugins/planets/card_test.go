package planets

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"hd2_bot/src/core/bot"
	"hd2_bot/src/hd2"
	"hd2_bot/src/plugins/plugutil"
	"hd2_bot/src/plugins/plugutil/plugtest"
	"hd2_bot/src/render"
)

// TestSummarizePlanetsCountsAndHotspots 校验总览的汇总数字、派系统计与热点筛选。
func TestSummarizePlanetsCountsAndHotspots(t *testing.T) {
	s := SummarizePlanets(testPlanets(), &hd2.War{Statistics: hd2.Statistics{PlayerCount: 123456}}, testFetchedAt())

	if s.PlayerCount != "123,456" {
		t.Errorf("在线士兵应带千分位，实际 %q", s.PlayerCount)
	}
	if s.Total != "5" {
		t.Errorf("星球总数错误：%q", s.Total)
	}
	// 有进攻行动的星球：attacking 非空的两颗
	if s.Contested != "2" {
		t.Errorf("有进攻行动的星球数错误：%q", s.Contested)
	}
	// 有事件：Luxuriant
	if s.EventCount != "1" {
		t.Errorf("事件数错误：%q", s.EventCount)
	}

	// 派系控制：超级地球 2、终结族 1、机器人 1、光能者 1
	wantFactions := map[string]string{"超级地球": "2", "终结族": "1", "机器人": "1", "光能者": "1"}
	if len(s.Factions) != len(wantFactions) {
		t.Fatalf("派系统计应有 %d 项，实际 %d 项：%+v", len(wantFactions), len(s.Factions), s.Factions)
	}
	for _, f := range s.Factions {
		if want, ok := wantFactions[f.Name]; !ok {
			t.Errorf("出现了未预期的派系：%q", f.Name)
			continue
		} else if f.Count != want {
			t.Errorf("派系 %s 控制数应为 %s，实际 %s", f.Name, want, f.Count)
		}
	}
	if s.Factions[0].Name != "超级地球" {
		t.Errorf("我方派系应排在最前，实际 %q", s.Factions[0].Name)
	}
	// 占比：2/5 = 40.0%
	if s.Factions[0].Percent != "40.0%" {
		t.Errorf("派系占比错误：%q", s.Factions[0].Percent)
	}
}

// TestSummarizePlanetsUnknownFaction 校验未收录的控制方原样进派系统计：
// 计数一个都不能丢，也不能因为查不到译名就被静默吞掉（总数与明细要对得上）。
func TestSummarizePlanetsUnknownFaction(t *testing.T) {
	list := []hd2.Planet{
		{Index: 1, Name: "Known Rock", CurrentOwner: "Humans"},
		{Index: 2, Name: "Unknown Rock", CurrentOwner: "Foo"},
	}
	s := SummarizePlanets(list, nil, testFetchedAt())

	if s.Total != "2" {
		t.Fatalf("星球总数错误：%q", s.Total)
	}
	if len(s.Factions) != 2 {
		t.Fatalf("已知与未收录的控制方都应出现在派系统计里，实际 %+v", s.Factions)
	}
	// 已知派系按固定顺序排在前
	if s.Factions[0].Name != "超级地球" || s.Factions[0].Count != "1" {
		t.Errorf("已知派系应排在最前且计数正确：%+v", s.Factions[0])
	}
	// 未收录的控制方原样显示、计数不丢、没有配色 class
	if s.Factions[1].Name != "Foo" || s.Factions[1].Count != "1" || s.Factions[1].Class != "" {
		t.Errorf("未收录控制方应原样显示且计数不丢：%+v", s.Factions[1])
	}
	if s.Factions[1].Percent != "50.0%" {
		t.Errorf("未收录控制方的占比错误：%q", s.Factions[1].Percent)
	}
}

// TestSummarizePlanetsHotspotOrder 校验热点排序：有事件的最前，其次有进攻行动的，组内按在线士兵降序。
func TestSummarizePlanetsHotspotOrder(t *testing.T) {
	list := []hd2.Planet{
		{Index: 1, Name: "Quiet High", Statistics: hd2.PlanetStats{PlayerCount: 99999}},
		{Index: 2, Name: "Attacked Small", Attacking: []int{1}, Statistics: hd2.PlanetStats{PlayerCount: 10}},
		{Index: 3, Name: "Attacked Big", Attacking: []int{1}, Statistics: hd2.PlanetStats{PlayerCount: 500}},
		{Index: 4, Name: "Event Low", Event: &hd2.PlanetEvent{EventType: 1}, Statistics: hd2.PlanetStats{PlayerCount: 3}},
	}
	s := SummarizePlanets(list, nil, testFetchedAt())

	got := hotspotNames(s)
	want := []string{"Event Low", "Attacked Big", "Attacked Small"}
	if strings.Join(got, "、") != strings.Join(want, "、") {
		t.Fatalf("热点顺序错误：\n期望 %v\n实际 %v", want, got)
	}
}

// TestSummarizePlanetsHotspotsLimitedToEight 校验热点最多 8 条，且取玩家数最多的那批。
func TestSummarizePlanetsHotspotsLimitedToEight(t *testing.T) {
	var list []hd2.Planet
	for i := 0; i < 10; i++ {
		list = append(list, hd2.Planet{
			Index:      i,
			Name:       fmt.Sprintf("Planet %d", i),
			Attacking:  []int{1},
			Statistics: hd2.PlanetStats{PlayerCount: int64(100 - i*10)},
		})
	}
	s := SummarizePlanets(list, nil, testFetchedAt())
	if len(s.Hotspots) != maxHotspots {
		t.Fatalf("热点应截断到 %d 条，实际 %d 条", maxHotspots, len(s.Hotspots))
	}
	names := hotspotNames(s)
	if names[0] != "Planet 0" || names[len(names)-1] != "Planet 7" {
		t.Errorf("热点应取玩家数最多的 8 条，实际 %v", names)
	}
}

// TestSummarizePlanetsDefenseTimer 校验热点卡上的保卫倒计时口径：
// 起点是**数据时间**而不是本机时钟（上游数据可能已经滞后），
// 没有截止时刻、没有数据时间或已到期时一律留空，模板据此整行不渲染。
func TestSummarizePlanetsDefenseTimer(t *testing.T) {
	fetchedAt := testFetchedAt()
	cases := []struct {
		name    string
		event   *hd2.PlanetEvent
		fetched time.Time
		want    string
	}{
		{"整天数带天", &hd2.PlanetEvent{EndTime: ptrTime(fetchedAt.Add(32*time.Hour + 2*time.Minute + 33*time.Second))}, fetchedAt, "1天 08:02:33"},
		{"不足一天省掉天", &hd2.PlanetEvent{EndTime: ptrTime(fetchedAt.Add(2*time.Hour + 5*time.Minute))}, fetchedAt, "02:05:00"},
		{"没有截止时刻", &hd2.PlanetEvent{}, fetchedAt, ""},
		{"已经到期", &hd2.PlanetEvent{EndTime: ptrTime(fetchedAt.Add(-time.Minute))}, fetchedAt, ""},
		{"没有数据时间", &hd2.PlanetEvent{EndTime: ptrTime(fetchedAt.Add(time.Hour))}, time.Time{}, ""},
	}
	for _, c := range cases {
		list := []hd2.Planet{{
			Index: 268, Name: "Luxuriant", CurrentOwner: "Humans",
			Attacking: []int{216}, Event: c.event, Statistics: hd2.PlanetStats{PlayerCount: 10},
		}}
		s := SummarizePlanets(list, nil, c.fetched)
		if len(s.Hotspots) != 1 {
			t.Fatalf("%s：热点应有 1 条，实际 %d 条", c.name, len(s.Hotspots))
		}
		if got := s.Hotspots[0].Timer; got != c.want {
			t.Errorf("%s：倒计时应为 %q，实际 %q", c.name, c.want, got)
		}
	}
	// 没有事件的行（只有进攻行动）永远没有倒计时。
	s := SummarizePlanets([]hd2.Planet{{
		Index: 216, Name: "Peacock", Attacking: []int{268}, Statistics: hd2.PlanetStats{PlayerCount: 5},
	}}, nil, fetchedAt)
	if got := s.Hotspots[0].Timer; got != "" {
		t.Errorf("没有事件的星球不该有保卫倒计时，实际 %q", got)
	}
}

// TestFormatPlanetsTextIncludesDefenseTimer 校验文本回退与卡片同口径：
// 卡片上有的保卫倒计时，纯文本里也要有（渲染不可用时群友不能少看到一行）。
func TestFormatPlanetsTextIncludesDefenseTimer(t *testing.T) {
	s := SummarizePlanets(testPlanets(), nil, testFetchedAt())
	text := FormatPlanetsText(s, testFetchedAt(), false)
	if !strings.Contains(text, "保卫剩余 1天 08:00:00") {
		t.Errorf("文本总览应带上保卫倒计时，实际：\n%s", text)
	}
}

// ptrTime 取时间指针：上游 endTime 是指针，缺失与零值在展示层是两回事。
func ptrTime(v time.Time) *time.Time { return &v }

// TestSummarizePlanetsHandlesNilWar 校验战况缺失时在线士兵显示「—」，其余统计照常。
func TestSummarizePlanetsHandlesNilWar(t *testing.T) {
	s := SummarizePlanets(testPlanets(), nil, testFetchedAt())
	if s.PlayerCount != plugutil.DashText {
		t.Errorf("战况缺失时应显示 %q，实际 %q", plugutil.DashText, s.PlayerCount)
	}
	if s.Total != "5" || s.EventCount != "1" {
		t.Errorf("战况缺失不应影响其它统计：%+v", s)
	}
}

// hotspotNames 按顺序取出热点的英文名，便于断言顺序。
func hotspotNames(s PlanetsSummary) []string {
	names := make([]string, 0, len(s.Hotspots))
	for _, row := range s.Hotspots {
		names = append(names, row.NameEnglish)
	}
	return names
}

// TestBuildPlanetRowProgressSemantics 校验三种进度条语义：
// 有事件 → 防守剩余；敌方控制 → 解放进度；我方控制且无事件 → 星球血量。
func TestBuildPlanetRowProgressSemantics(t *testing.T) {
	cases := []struct {
		name        string
		planet      hd2.Planet
		wantLabel   string
		wantPercent float64
		wantText    string
	}{
		{
			name: "有事件时显示防守剩余",
			planet: hd2.Planet{Index: 268, Name: "Luxuriant", CurrentOwner: "Humans",
				Event: &hd2.PlanetEvent{EventType: 1, Health: 1200000, MaxHealth: 1500000}},
			wantLabel:   "防守剩余",
			wantPercent: 80,
			wantText:    "80.0%",
		},
		{
			name: "敌方控制时显示解放进度",
			planet: hd2.Planet{Index: 110, Name: "Bekvam III", CurrentOwner: "Automaton",
				Health: 307863, MaxHealth: 1600000},
			wantLabel:   "解放",
			wantPercent: 80.8,
			wantText:    "80.8%",
		},
		{
			name: "我方控制且无事件时显示星球血量",
			planet: hd2.Planet{Index: 26, Name: "Nublaria I", CurrentOwner: "Humans",
				Health: 1000000, MaxHealth: 1000000},
			wantLabel:   "星球血量",
			wantPercent: 100,
			wantText:    "100.0%",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := buildPlanetRow(tc.planet, testFetchedAt())
			if row.BarLabel != tc.wantLabel {
				t.Errorf("进度标签错误：期望 %q，实际 %q", tc.wantLabel, row.BarLabel)
			}
			if row.BarPercent != tc.wantPercent {
				t.Errorf("进度数值错误：期望 %v，实际 %v", tc.wantPercent, row.BarPercent)
			}
			if row.BarText != tc.wantText {
				t.Errorf("进度文案错误：期望 %q，实际 %q", tc.wantText, row.BarText)
			}
		})
	}
}

// TestBuildPlanetRowFields 校验总览一行的字段（名称、分区、控制方、玩家数、状态标签）。
func TestBuildPlanetRowFields(t *testing.T) {
	row := buildPlanetRow(hd2.Planet{
		Index: 268, Name: "Luxuriant", Sector: "Jin Xi", CurrentOwner: "Humans",
		Attacking: []int{216}, Statistics: hd2.PlanetStats{PlayerCount: 1846},
		Event: &hd2.PlanetEvent{EventType: 1, Faction: "Terminids", Health: 1, MaxHealth: 2},
	}, testFetchedAt())
	if row.NameChinese != "富源" || row.NameEnglish != "Luxuriant" {
		t.Errorf("星球名错误：%+v", row)
	}
	if row.Sector != "锦栖分区" {
		t.Errorf("分区应显示中文译名，实际 %q", row.Sector)
	}
	if row.Owner != "超级地球" || row.OwnerClass != "faction--humans" {
		t.Errorf("控制方显示错误：%q / %q", row.Owner, row.OwnerClass)
	}
	if row.PlayerCount != "1,846" {
		t.Errorf("玩家数错误：%q", row.PlayerCount)
	}
	if row.Status != "防守战" || row.StatusClass != "tag--loss" {
		t.Errorf("状态标签错误：%q / %q", row.Status, row.StatusClass)
	}
	if row.Index != "268" {
		t.Errorf("编号错误：%q", row.Index)
	}
	// 防守战：卡面跟着入侵方（终结族）走色与徽记，而不是控制方（超级地球）——
	// 只换一半会出现「黄色的超级地球徽记」这种看着像 bug 的卡。
	if row.Faction != "终结族" || row.Emblem != "emblem.terminids" || row.CardClass != "f-term" || !row.Defense {
		t.Errorf("防守战卡面应跟着入侵方：%+v", row)
	}
	// 文本回退里的「控制方」仍然是控制方，不能跟着卡面一起变成入侵方。
	if row.Owner != "超级地球" {
		t.Errorf("控制方应保持上游的当前控制方，实际 %q", row.Owner)
	}
}

// TestBuildPlanetRowCardFaction 校验不打防守战时卡面跟着控制方走，以及未收录派系的兜底。
func TestBuildPlanetRowCardFaction(t *testing.T) {
	cases := []struct {
		name      string
		owner     string
		wantF     string
		wantClass string
		wantBadge string
	}{
		{"机器人控制", "Automatons", "机器人", "f-auto", "emblem.automaton"},
		{"终结族控制", "Terminids", "终结族", "f-term", "emblem.terminids"},
		{"光能者控制", "Illuminate", "光能者", "f-illu", "emblem.illuminate"},
		{"我方控制", "Humans", "超级地球", "f-hum", "emblem.super_earth"},
		// 未收录的取值原样显示，配色退回默认（空 class），徽记用超级地球兜底——
		// 宁可用一个中性兜底，也不要随手挑一个敌对阵营色。
		{"未收录控制方", "Foo", "Foo", "", "emblem.super_earth"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			row := buildPlanetRow(hd2.Planet{Index: 1, Name: "Test Planet", CurrentOwner: c.owner}, testFetchedAt())
			if row.Faction != c.wantF || row.CardClass != c.wantClass || row.Emblem != c.wantBadge {
				t.Errorf("卡面派系错误：%q / %q / %q（期望 %q / %q / %q）",
					row.Faction, row.CardClass, row.Emblem, c.wantF, c.wantClass, c.wantBadge)
			}
			if row.Defense {
				t.Error("没有事件的行不该标成防守战")
			}
		})
	}
}

// TestBuildPlanetRowDefenseWithoutFaction 校验防守战但没有进攻方时退回控制方：卡面不能没有主色。
func TestBuildPlanetRowDefenseWithoutFaction(t *testing.T) {
	row := buildPlanetRow(hd2.Planet{
		Index: 268, Name: "Luxuriant", CurrentOwner: "Humans",
		Event: &hd2.PlanetEvent{EventType: 1, Faction: "   "},
	}, testFetchedAt())
	if row.Faction != "超级地球" || row.CardClass != "f-hum" || !row.Defense {
		t.Errorf("上游没给进攻方时应退回控制方：%+v", row)
	}
}

// TestBuildCardsCarryBiomeAsset 校验群系实景图的素材逻辑名被带进视图模型
// （/planet 的头图与 /planets 热点的缩略图都用同一个字段）。
//
// 取图必须用**上游英文群系名**：译文会把名字翻成中文，拿译文去查映射表只会查空。
func TestBuildCardsCarryBiomeAsset(t *testing.T) {
	planet := hd2.Planet{Index: 268, Name: "Luxuriant", Biome: hd2.Biome{Name: "Deciduous Forest"}}

	if got := BuildPlanetCard(planet, time.Time{}, false).BiomeAsset; got != "biome.deciduous_grove_biome_header" {
		t.Errorf("单星球卡应带上群系实景图，实际 %q", got)
	}
	if got := buildPlanetRow(planet, testFetchedAt()).BiomeAsset; got != "biome.deciduous_grove_biome_header" {
		t.Errorf("总览热点行应带上群系缩略图，实际 %q", got)
	}

	// 映射不到时留空串：模板据此整块不渲染，配错一张图比没有图更糟。
	unknown := hd2.Planet{Index: 7, Name: "Unknown Rock", Biome: hd2.Biome{Name: "Uncharted Wastes"}}
	if got := BuildPlanetCard(unknown, time.Time{}, false).BiomeAsset; got != "" {
		t.Errorf("未知群系不该挂图，实际 %q", got)
	}
	if got := buildPlanetRow(hd2.Planet{Index: 7, Name: "Unknown Rock"}, testFetchedAt()).BiomeAsset; got != "" {
		t.Errorf("上游没给群系时不该挂图，实际 %q", got)
	}
}

// TestLocalizedCardKeepsBiomeAsset 校验本地化不会把群系图弄丢：
// Biome 被翻成中文之后，头图仍指向英文原名对应的那张图。
func TestLocalizedCardKeepsBiomeAsset(t *testing.T) {
	planet := hd2.Planet{Index: 268, Name: "Luxuriant",
		Biome: hd2.Biome{Name: "Deciduous Forest", Description: "Temperate woodland"}}
	trans := &plugtest.Translator{Out: []string{"落叶林", "温带林地"}}

	card := BuildLocalizedPlanetCard(context.Background(), planet, time.Time{}, false, trans)
	if card.Biome != "落叶林" {
		t.Fatalf("群系名应为译文，实际 %q", card.Biome)
	}
	if card.BiomeAsset != "biome.deciduous_grove_biome_header" {
		t.Errorf("翻译之后头图不应丢失，实际 %q", card.BiomeAsset)
	}
}

// TestBuildPlanetCardFields 校验单星球卡的字段格式化与缺省兜底。

func TestBuildPlanetCardFields(t *testing.T) {
	end := time.Date(2026, 9, 18, 11, 2, 10, 0, time.UTC)
	planet := hd2.Planet{
		Index: 268, Name: "Luxuriant", Sector: "Jin Xi",
		Biome:        hd2.Biome{Name: "Deciduous Forest"},
		Hazards:      []hd2.Hazard{{Name: "None"}, {Name: "Acid Storms"}},
		CurrentOwner: "Humans", InitialOwner: "Humans",
		Health: 2000000, MaxHealth: 2000000, RegenPerSecond: 5.5555553,
		Attacking:  []int{216},
		Statistics: hd2.PlanetStats{PlayerCount: 1846},
		Event: &hd2.PlanetEvent{
			ID: 5695, EventType: 1, Faction: "Terminids", Health: 1200000, MaxHealth: 1500000,
			StartTime: time.Date(2026, 9, 16, 11, 2, 10, 0, time.UTC), EndTime: &end,
		},
	}
	card := BuildPlanetCard(planet, time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC), true)

	// 外壳标题写「星球情报」：星球名在卡面里（.pc-name）已经是最显眼的一行，两处都写是重复。
	if card.Title != "星球情报" || !strings.Contains(card.Subtitle, "268") {
		t.Errorf("外壳标题与副标题错误：%q / %q", card.Title, card.Subtitle)
	}
	if card.NameChinese != "富源" || card.NameEnglish != "Luxuriant" {
		t.Errorf("卡面应给出中英文名，实际 %q / %q", card.NameChinese, card.NameEnglish)
	}
	// 有事件 → 防守战，整张卡跟着入侵方（终结族）走色，保卫倒计时也一起算。
	if card.Status != "防守战" || !card.Defense {
		t.Errorf("有事件时状态应为防守战并标成防守，实际 %q / %v", card.Status, card.Defense)
	}
	if card.Faction != "终结族" || card.CardClass != "f-term" {
		t.Errorf("防守战的卡面应跟着入侵方走色，实际 %q / %q", card.Faction, card.CardClass)
	}
	if card.Timer == "" {
		t.Error("防守战应给出保卫倒计时")
	}
	if card.Index != "268" || card.Sector != "锦栖分区" {
		t.Errorf("编号或分区错误：%q / %q", card.Index, card.Sector)
	}
	if card.Biome != "Deciduous Forest" {
		t.Errorf("生物群系应原样显示上游英文名（没有译名表），实际 %q", card.Biome)
	}
	if card.Hazards != "Acid Storms" {
		t.Errorf("危害列表应过滤上游的 None 占位，实际 %q", card.Hazards)
	}
	if card.Owner != "超级地球" || card.InitialOwner != "超级地球" {
		t.Errorf("控制方错误：%q / %q", card.Owner, card.InitialOwner)
	}
	if card.PlayerCount != "1,846" {
		t.Errorf("玩家数错误：%q", card.PlayerCount)
	}
	if card.HealthText != "2,000,000 / 2,000,000" {
		t.Errorf("血量文案错误：%q", card.HealthText)
	}
	if card.Regen != "5.6 /秒" {
		t.Errorf("每秒回复错误：%q", card.Regen)
	}
	if card.Attacking != "216" {
		t.Errorf("进攻目标编号错误：%q", card.Attacking)
	}
	// 徽记与卡框配色都跟着卡面派系走：这颗星球在打防守战，整张卡换成入侵方（终结族）的。
	if card.Emblem != "emblem.terminids" {
		t.Errorf("防守战应挂入侵方徽记，实际 %q", card.Emblem)
	}
	if !card.Stale || card.DataTime == "" {
		t.Errorf("过期标记与数据时间错误：stale=%v time=%q", card.Stale, card.DataTime)
	}
	if card.Event == nil {
		t.Fatal("有事件时不应丢掉事件区块")
	}
	// 上游的 eventType 是数字枚举、群里没人看得懂，卡片上不再展示（见 PlanetEventCard）。
	if card.Event.Faction != "终结族" || card.Event.FactionClass != "faction--terminid" {
		t.Errorf("进攻方与配色错误：%q / %q", card.Event.Faction, card.Event.FactionClass)
	}
	if card.Event.BarPercent != 80 || card.Event.BarText != "80.0%" {
		t.Errorf("事件进度错误：%v / %q", card.Event.BarPercent, card.Event.BarText)
	}
	if card.Event.StartTime == "" || card.Event.EndTime == "" {
		t.Errorf("事件起止时间应格式化，实际 %q / %q", card.Event.StartTime, card.Event.EndTime)
	}
}

// TestBuildPlanetCardDefaults 校验缺省值一律显示「—」/「未知」，不出现 0 或空串。
func TestBuildPlanetCardDefaults(t *testing.T) {
	card := BuildPlanetCard(hd2.Planet{Index: 7, Name: "Unknown Rock"}, time.Time{}, false)
	if card.Sector != plugutil.DashText {
		t.Errorf("分区缺省应为 %q，实际 %q", plugutil.DashText, card.Sector)
	}
	if card.Biome != plugutil.DashText || card.Hazards != "暂无环境危害" {
		t.Errorf("生物群系与危害的缺省错误：%q / %q", card.Biome, card.Hazards)
	}
	if card.PlayerCount != "0" {
		t.Errorf("玩家数为 0 是真实数据，应显示 0，实际 %q", card.PlayerCount)
	}
	if card.HealthText != plugutil.DashText {
		t.Errorf("没有血量数据时应显示 %q，实际 %q", plugutil.DashText, card.HealthText)
	}
	if card.DataTime != "" || card.Event != nil {
		t.Errorf("无数据时间与事件时不应编造：%q / %v", card.DataTime, card.Event)
	}
	if card.Emblem != "emblem.super_earth" {
		t.Errorf("未知控制方应回落到超级地球徽标，实际 %q", card.Emblem)
	}
	// 没有译名时中文名就是上游原文，此时不再单独显示一遍英文。
	if card.NameChinese != "Unknown Rock" || card.NameEnglish != "" {
		t.Errorf("没有译名时不该重复显示英文名：%q / %q", card.NameChinese, card.NameEnglish)
	}
	// 控制方是空值时未收录：卡框不加配色 class，退化成 .planet-card 的默认色（超级地球蓝），
	// 与同一张卡上的徽记兜底口径一致。
	if card.Status != "已解放" || card.CardClass != "" {
		t.Errorf("我方控制且无战事时应是已解放的超级地球卡，实际 %q / %q", card.Status, card.CardClass)
	}
}

// TestOwnerEmblemPluralOwners 校验徽标映射也走派系归一化：上游单复数混用
// （实测有 Automaton 与 Automatons），复数写法必须拿到同一个徽标，而不是退回超级地球。
func TestOwnerEmblemPluralOwners(t *testing.T) {
	cases := []struct{ owner, want string }{
		{"Automaton", "emblem.automaton"},
		{"Automatons", "emblem.automaton"},
		{"Terminids", "emblem.terminids"},
		{"  Illuminate  ", "emblem.illuminate"},
		{"Humans", "emblem.super_earth"}, // 我方用超级地球徽标
		{"Foo", "emblem.super_earth"},    // 未收录：兜底
	}
	for _, c := range cases {
		if got := ownerEmblem(c.owner); got != c.want {
			t.Errorf("ownerEmblem(%q) = %q，期望 %q", c.owner, got, c.want)
		}
	}
}

// TestPlanetsCardHTML 校验总览卡片的 HTML：关键文案、进度条宽度与 HTML 转义。
func TestPlanetsCardHTML(t *testing.T) {
	summary := SummarizePlanets(testPlanets(), &hd2.War{Statistics: hd2.Statistics{PlayerCount: 123456}}, testFetchedAt())
	html, err := render.HTML(render.Card{
		Name: "planets",
		Data: BuildPlanetsCard(summary, time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC), false),
	})
	if err != nil {
		t.Fatalf("生成 HTML 失败：%v", err)
	}
	// 汇总数字 + 热点星球（用户 2026-09-17 要求热点加回来）；派系控制仍然不上卡。
	wants := []string{
		"战线总览", "在线士兵", "123,456", "星球总数", "进攻中", "有事件",
		"热点星球", "planet-grid", "planet-card", "pc-landmark", "pc-bar",
		`class="cursor"`, "富源", "Luxuriant", "防守战", "防守剩余", "100.0%",
		// 保卫倒计时：夹具里 Luxuriant 的事件截止时刻是数据时间 + 32 小时
		"pc-timer", "保卫剩余", "1天 08:00:00",
	}
	for _, want := range wants {
		if !strings.Contains(html, want) {
			t.Errorf("HTML 缺少 %q", want)
		}
	}
	for _, unwanted := range []string{"派系控制"} {
		if strings.Contains(html, unwanted) {
			t.Errorf("/planets 卡片不该再出现 %q", unwanted)
		}
	}
	if strings.Contains(html, "ZgotmplZ") {
		t.Error("存在被模板引擎拒绝的样式值")
	}
}

// TestPlanetCardHTML 校验单星球卡片的 HTML：关键文案与进度条宽度。
func TestPlanetCardHTML(t *testing.T) {
	planet := hd2.Planet{
		Index: 110, Name: "Bekvam III", Sector: "Akira", CurrentOwner: "Automaton", InitialOwner: "Humans",
		Biome: hd2.Biome{Name: "Volcanic"}, Hazards: []hd2.Hazard{{Name: "None"}},
		Health: 307863, MaxHealth: 1600000, RegenPerSecond: 5.5555553,
		Statistics: hd2.PlanetStats{PlayerCount: 19507},
	}
	html, err := render.HTML(render.Card{
		Name: "planet",
		Data: BuildPlanetCard(planet, time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC), false),
	})
	if err != nil {
		t.Fatalf("生成 HTML 失败：%v", err)
	}
	wants := []string{
		// 卡面头：阵营色卡框（敌方控制 → 机器人红）＋ 顶栏 ＋ 群系图 ＋ 战役状态与星球名
		`class="planet-card planet-detail-card f-auto"`, "pc-top", "pc-ficon", "pc-fname", "机器人", "19,507",
		"pc-landmark", "解放战役", `class="pc-name">贝克温III</span>`, "Bekvam III",
		// 进度条与情报小节
		"解放", "80.8%", "width: 80.8%", `class="pd-title"`, "星球情报", "阿基拉分区",
		"环境危害", "暂无环境危害", "数据时间",
	}
	for _, want := range wants {
		if !strings.Contains(html, want) {
			t.Errorf("HTML 缺少 %q", want)
		}
	}
	if strings.Contains(html, "ZgotmplZ") {
		t.Error("存在被模板引擎拒绝的样式值")
	}
	if strings.Contains(html, "None") {
		t.Error("上游的 None 占位危害不应出现在卡片上")
	}
	// 没有群系配图时走斜纹占位，而不是留一个空框。
	if !strings.Contains(html, "pc-nobiome") {
		t.Error("映射不到群系配图时应有占位块")
	}
	// 上游的 eventType 是数字枚举，卡片上不再展示。
	if strings.Contains(html, "事件类型") {
		t.Error("卡片不该再展示事件类型")
	}
}

// TestPlanetsCardEscapesUpstreamText 校验热点行里的上游文本被转义：
// 热点星球名来自上游（译名表只覆盖已知星球，冷门星球直接显示原文），
// 所以这里必须看到转义后的文本，而不是原样的标签。
func TestPlanetsCardEscapesUpstreamText(t *testing.T) {
	summary := SummarizePlanets([]hd2.Planet{{
		Index: 9, Name: `<script>alert(1)</script>`, CurrentOwner: "Humans",
		Attacking: []int{1}, Statistics: hd2.PlanetStats{PlayerCount: 1},
	}}, nil, testFetchedAt())
	html, err := render.HTML(render.Card{Name: "planets", Data: BuildPlanetsCard(summary, time.Time{}, false)})
	if err != nil {
		t.Fatalf("生成 HTML 失败：%v", err)
	}
	if strings.Contains(html, "<script>alert") {
		t.Error("上游文本未转义，存在 HTML 注入")
	}
	escaped := "&lt;" + "script&gt;"
	if !strings.Contains(html, escaped) {
		t.Error("热点行应显示这颗星球（英文名走转义后的文本）")
	}
}

// TestPlanetsCardSmokeWithRealBrowser 真浏览器冒烟：把总览卡与单星球卡实际渲染成 PNG。
// 默认跳过，只有 HD2_RENDER_SMOKE=1 时才跑（CI/无浏览器环境不应依赖 Chromium）。
func TestPlanetsCardSmokeWithRealBrowser(t *testing.T) {
	if os.Getenv("HD2_RENDER_SMOKE") != "1" {
		t.Skip("未开启 HD2_RENDER_SMOKE")
	}
	engine, err := render.New(render.Config{
		Width: 900, Scale: 2, Timeout: 20 * time.Second, Format: render.FormatPNG,
	})
	if err != nil {
		t.Fatalf("创建渲染引擎失败：%v", err)
	}
	defer engine.Close()

	fetchedAt := time.Date(2026, 9, 16, 20, 0, 0, 0, time.UTC)
	summary := SummarizePlanets(testPlanets(), &hd2.War{Statistics: hd2.Statistics{PlayerCount: 123456}}, testFetchedAt())
	cases := []struct {
		file string
		card render.Card
	}{
		{"hd2_planets_card.png", render.Card{Name: "planets", Data: BuildPlanetsCard(summary, fetchedAt, false)}},
		// 补充数据没取到（行动变量「暂不可用」、兴趣点只剩星区）：生产里就是这一种。
		{"hd2_planet_card.png", render.Card{Name: "planet", Data: planetCardWithExtras(testPlanets()[1], PlanetExtras{}, fetchedAt)}},
		// 环境区块（群系说明 + 逐条危害）：真浏览器渲染一遍，模板改坏了这里会直接报错，
		// 只跑 render.HTML 的用例看不出排版被 CSS 挤坏。
		{"hd2_planet_env_card.png", render.Card{Name: "planet", Data: planetCardWithExtras(testPlanetWithEnvironment(), PlanetExtras{EffectsKnown: true}, fetchedAt)}},
		// 战略情报分析 / 行动变量 / 兴趣点（用户 2026-09-18 要求）：用带补充数据的样本渲染一遍，
		// 这三块只有拿到外部数据才说得出口，冒烟图里必须看得见（口径见 planetIntel / FillPlanetExtras）。
		{"hd2_planet_extras_card.png", render.Card{Name: "planet", Data: planetCardWithExtras(testPlanetWithExtras(), testPlanetExtras(), fetchedAt)}},
		// 防守战（有事件 + 保卫倒计时 + 事件小节）：整张卡跟着入侵方走色。
		{"hd2_planet_defense_card.png", render.Card{Name: "planet", Data: planetCardWithExtras(testPlanetOnDefense(), testDefenseExtras(), fetchedAt)}},
	}
	for _, tc := range cases {
		start := time.Now()
		png, err := engine.Render(context.Background(), tc.card)
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("渲染卡片 %s 失败：%v", tc.card.Name, err)
		}
		path := filepath.Join("..", "..", "..", "tmp", "screenshots", tc.file)
		if err := os.WriteFile(path, png, 0o644); err != nil {
			t.Fatalf("写出图片失败：%v", err)
		}
		width := pngWidth(t, png)
		if width < 800 {
			t.Fatalf("卡片宽度应 >= 800，实际 %d", width)
		}
		t.Logf("%s 卡片：%s（%d 字节，宽 %d，耗时 %s）", tc.card.Name, path, len(png), width, elapsed)
	}
}

// testPlanetOnDefense 是防守战的样本星球：有事件（终结族入侵 + 32 小时后打完）、
// 我方控制、带群系与危害——用来眼看「整张卡跟着入侵方走色 + 事件小节 + 保卫倒计时」这套版式。
func testPlanetOnDefense() hd2.Planet {
	end := testFetchedAt().Add(32 * time.Hour)
	return hd2.Planet{
		Index: 268, Name: "Luxuriant", Sector: "Jin Xi",
		Biome:        hd2.Biome{Name: "Deciduous Forest", Description: "Temperate woodland (UNOFFICIAL)"},
		Hazards:      []hd2.Hazard{{Name: "Acid Storms", Description: "Corrosive rain damages armor."}},
		CurrentOwner: "Humans", InitialOwner: "Humans",
		Health: 2000000, MaxHealth: 2000000, RegenPerSecond: 5.5555553,
		Statistics: hd2.PlanetStats{PlayerCount: 1846},
		Event: &hd2.PlanetEvent{
			EventType: 1, Faction: "Terminids", Health: 1200000, MaxHealth: 1500000,
			StartTime: testFetchedAt().Add(-6 * time.Hour), EndTime: &end,
		},
	}
}

// pngWidth 读取 PNG 的宽度：8 字节签名 + 4 字节长度 + 4 字节 "IHDR" + 4 字节宽 + 4 字节高。
func pngWidth(t *testing.T, data []byte) int {
	t.Helper()
	if len(data) < 24 || !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("输出不是合法 PNG：长度 %d", len(data))
	}
	return int(binary.BigEndian.Uint32(data[16:20]))
}

// TestBuildPlanetEventCardWithoutMaxHealth 校验事件缺上限血量时不出「防守剩余 0.0%」。
// 上游偶尔给 maxHealth=0，算出来的 0% 会被误读成「马上就守不住了」，宁可整块不显示。
func TestBuildPlanetEventCardWithoutMaxHealth(t *testing.T) {
	card := buildPlanetEventCard(&hd2.PlanetEvent{EventType: 1, Faction: "Terminids", Health: 0, MaxHealth: 0}, time.UTC)
	if card == nil {
		t.Fatal("有事件时应生成事件区块")
	}
	if card.BarText != "" {
		t.Errorf("缺上限血量时进度文案应为空（模板据此隐藏进度条），实际 %q", card.BarText)
	}
	if card.BarPercent != 0 {
		t.Errorf("缺上限血量时进度宽度应为 0，实际 %v", card.BarPercent)
	}
	if card.HealthText != plugutil.DashText {
		t.Errorf("缺上限血量时血量应显示 %s，实际 %q", plugutil.DashText, card.HealthText)
	}
}

// ---- 环境信息（群系说明 + 逐条环境危害）的用例 ----

// testPlanetWithEnvironment 是环境信息用例共用的星球：
// 带 (UNOFFICIAL) 标记的群系说明 + 一条上游 None 占位 + 一条真实危害。
func testPlanetWithEnvironment() hd2.Planet {
	return hd2.Planet{
		Index: 7,
		Name:  "Acamar IV",
		Biome: hd2.Biome{Name: "Deciduous Forest", Description: "Temperate woodland (UNOFFICIAL)"},
		Hazards: []hd2.Hazard{
			{Name: "None", Description: "Environmental conditions not yet disclosed."},
			{Name: "Acid Storms", Description: "Corrosive rain damages armor."},
		},
	}
}

// TestBuildLocalizedPlanetCardTranslatesEnvironment 校验生物群系与环境危害都走翻译层，
// 并且环境危害给出逐条说明。
func TestBuildLocalizedPlanetCardTranslatesEnvironment(t *testing.T) {
	// 送译顺序：群系名、群系说明、危害名、危害说明（None 占位会被剔除）
	trans := &plugtest.Translator{Out: []string{
		"落叶林", "温带林地", "酸雨风暴", "腐蚀性降雨会损坏护甲。",
	}}
	card := BuildLocalizedPlanetCard(context.Background(), testPlanetWithEnvironment(), testFetchedAt(), false, trans)

	if card.Biome != "落叶林" {
		t.Errorf("群系名应为译文，实际 %q", card.Biome)
	}
	if card.BiomeDesc != "温带林地" {
		t.Errorf("群系说明应为译文且剥掉 (UNOFFICIAL)，实际 %q", card.BiomeDesc)
	}
	if card.Hazards != "酸雨风暴" {
		t.Errorf("危害名应为译文，实际 %q", card.Hazards)
	}
	if len(card.EnvItems) != 1 {
		t.Fatalf("None 占位必须剔除，只应剩 1 条危害，实际 %+v", card.EnvItems)
	}
	if card.EnvItems[0].Name != "酸雨风暴" || card.EnvItems[0].Description != "腐蚀性降雨会损坏护甲。" {
		t.Errorf("环境危害应逐条带译文说明，实际 %+v", card.EnvItems[0])
	}
	if card.EnvNote != "" {
		t.Errorf("翻译成功时不应有环境说明文案，实际 %q", card.EnvNote)
	}

	// 段数与顺序是 apply 按下标回填的前提：段数对不上就整块回退英文。
	if len(trans.Texts) != 1 {
		t.Fatalf("应只送一次译，实际 %d 次：%v", len(trans.Texts), trans.Texts)
	}
	want := []string{"Deciduous Forest", "Temperate woodland", "Acid Storms", "Corrosive rain damages armor."}
	if len(trans.Texts[0]) != len(want) {
		t.Fatalf("应送 %d 段，实际 %d 段：%v", len(want), len(trans.Texts[0]), trans.Texts[0])
	}
	for i, w := range want {
		if trans.Texts[0][i] != w {
			t.Errorf("第 %d 段送译内容错误：期望 %q，实际 %q", i+1, w, trans.Texts[0][i])
		}
	}
}

// TestBuildLocalizedPlanetCardWithoutHazards 校验只有上游的 None 占位时显示「暂无环境危害」。
func TestBuildLocalizedPlanetCardWithoutHazards(t *testing.T) {
	planet := hd2.Planet{
		Index:   7,
		Name:    "Acamar IV",
		Biome:   hd2.Biome{Name: "Deciduous Forest"},
		Hazards: []hd2.Hazard{{Name: "None", Description: "Environmental conditions not yet disclosed."}},
	}
	// 上游没给群系说明：这一段是空串，翻译层原样返回空串，不能因此算成「没翻动」。
	trans := &plugtest.Translator{Out: []string{"落叶林", ""}}
	card := BuildLocalizedPlanetCard(context.Background(), planet, testFetchedAt(), false, trans)

	if card.Hazards != "暂无环境危害" {
		t.Errorf("只有占位项时应显示「暂无环境危害」，实际 %q", card.Hazards)
	}
	if len(card.EnvItems) != 0 {
		t.Errorf("没有真实危害时不应有环境条目，实际 %+v", card.EnvItems)
	}
	if card.Biome != "落叶林" || card.EnvNote != "" {
		t.Errorf("占位危害不该影响群系翻译，也不该写说明文案：%q / %q", card.Biome, card.EnvNote)
	}
	// 空段照送（翻译层对不含拉丁字母的段直接原样返回，不会真的发请求），
	// 段数与字段一一对应：群系名 + 群系说明。
	if len(trans.Texts) != 1 || len(trans.Texts[0]) != 2 || trans.Texts[0][0] != "Deciduous Forest" {
		t.Fatalf("送译段应为「群系名 + 群系说明」两段，实际 %v", trans.Texts)
	}
}

// TestBuildLocalizedPlanetCardWithoutHazardsOnEmptyList 校验上游连 hazards 字段都没给时同样是「暂无环境危害」。
func TestBuildLocalizedPlanetCardWithoutHazardsOnEmptyList(t *testing.T) {
	planet := hd2.Planet{Index: 7, Name: "Acamar IV", Biome: hd2.Biome{Name: "Volcanic"}}
	cases := []struct {
		name    string
		hazards []hd2.Hazard
	}{
		{"hazards 为 nil", nil},
		{"hazards 为空切片", []hd2.Hazard{}},
		{"hazards 只有空白名", []hd2.Hazard{{Name: "   "}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			planet.Hazards = c.hazards
			trans := &plugtest.Translator{Out: []string{"火山", ""}}
			card := BuildLocalizedPlanetCard(context.Background(), planet, testFetchedAt(), false, trans)
			if card.Hazards != "暂无环境危害" {
				t.Errorf("应显示「暂无环境危害」，实际 %q", card.Hazards)
			}
			if len(card.EnvItems) != 0 {
				t.Errorf("不该有环境条目，实际 %+v", card.EnvItems)
			}
			if card.Biome != "火山" || card.EnvNote != "" {
				t.Errorf("群系仍应翻译且不写说明文案：%q / %q", card.Biome, card.EnvNote)
			}
		})
	}
}

// TestBuildLocalizedPlanetCardKeepsEnglishWhenTranslationFails 校验翻译失败时保留英文并在卡片上注明。
func TestBuildLocalizedPlanetCardKeepsEnglishWhenTranslationFails(t *testing.T) {
	planet := hd2.Planet{
		Index:   7,
		Name:    "Acamar IV",
		Biome:   hd2.Biome{Name: "Deciduous Forest", Description: "Temperate woodland"},
		Hazards: []hd2.Hazard{{Name: "Acid Storms", Description: "Corrosive rain"}},
	}
	trans := &plugtest.Translator{Err: errors.New("翻译后端挂了")}
	card := BuildLocalizedPlanetCard(context.Background(), planet, testFetchedAt(), false, trans)

	if card.Biome != "Deciduous Forest" || card.Hazards != "Acid Storms" {
		t.Errorf("翻译失败时应保留英文，实际 %q / %q", card.Biome, card.Hazards)
	}
	if len(card.EnvItems) != 1 || card.EnvItems[0].Description != "Corrosive rain" {
		t.Errorf("翻译失败时逐条说明也要保留英文，实际 %+v", card.EnvItems)
	}
	// 文案写死在这里：改掉常量文案必须让这条用例变红（对着常量比等于拿自己跟自己比）。
	if card.EnvNote != "翻译暂不可用，环境信息为英文原文。" {
		t.Errorf("翻译失败时应注明环境信息保留英文，实际 %q", card.EnvNote)
	}
}

// TestBuildLocalizedPlanetCardKeepsEnglishWhenTranslationUnchanged 校验「译文与原文相同」也算没翻动：
// 翻译层把某段原样返回（含空段）时不能当成翻好了。
func TestBuildLocalizedPlanetCardKeepsEnglishWhenTranslationUnchanged(t *testing.T) {
	trans := &plugtest.Translator{Out: []string{
		"Deciduous Forest", "Temperate woodland", "Acid Storms", "Corrosive rain damages armor.",
	}}
	card := BuildLocalizedPlanetCard(context.Background(), testPlanetWithEnvironment(), testFetchedAt(), false, trans)
	if card.EnvNote != "翻译暂不可用，环境信息为英文原文。" {
		t.Errorf("原文原样返回时应注明翻译不可用，实际 %q", card.EnvNote)
	}
	if card.BiomeDesc != "Temperate woodland" {
		t.Errorf("原样返回时群系说明应保持英文，实际 %q", card.BiomeDesc)
	}
}

// shortTranslator 是本包内的局部假翻译层：无视入参、永远只回固定几段。
// 用它是为了覆盖「段数与入参不符」这条退化路径——plugtest.Translator 刻意保证逐段消费、
// 段数永远与入参等长，表达不了这种坏后端；不该为一条用例去改公共替身。
type shortTranslator struct {
	out []string
}

// Translate 原样返回预设的固定段数。
func (s shortTranslator) Translate(context.Context, []string) ([]string, error) {
	return s.out, nil
}

// TestBuildLocalizedPlanetCardTranslationLengthMismatch 校验翻译层返回段数不符时整块保留英文：
// 段数与字段对不上时按下标回填会错位（把危害说明当成群系名）。
func TestBuildLocalizedPlanetCardTranslationLengthMismatch(t *testing.T) {
	trans := shortTranslator{out: []string{"落叶林", "温带林地"}}
	card := BuildLocalizedPlanetCard(context.Background(), testPlanetWithEnvironment(), testFetchedAt(), false, trans)
	if card.Biome != "Deciduous Forest" || card.BiomeDesc != "Temperate woodland" {
		t.Errorf("段数不符时应整块保留英文，实际 %q / %q", card.Biome, card.BiomeDesc)
	}
	if card.EnvNote != "翻译暂不可用，环境信息为英文原文。" {
		t.Errorf("段数不符时应注明翻译不可用，实际 %q", card.EnvNote)
	}
}

// TestBuildLocalizedPlanetCardTruncatesDescriptions 校验群系说明与危害说明的终局防线仍在：
// 只有超过 400/200 字的异常内容才会被截断（真实上游最长 337/100 字，
// 「正常内容完整显示」由 TestEnvironmentBudgetsKeepRealUpstreamLengths 守住）。
func TestBuildLocalizedPlanetCardTruncatesDescriptions(t *testing.T) {
	longBiome := strings.Repeat("群系说明", 150)  // 600 字，超过 400 字预算
	longHazard := strings.Repeat("危害说明", 100) // 400 字，超过 200 字预算
	planet := hd2.Planet{
		Index:   7,
		Name:    "Acamar IV",
		Biome:   hd2.Biome{Name: "Deciduous Forest", Description: "Temperate woodland"},
		Hazards: []hd2.Hazard{{Name: "Acid Storms", Description: "Corrosive rain"}},
	}
	trans := &plugtest.Translator{Out: []string{"落叶林", longBiome, "酸雨风暴", longHazard}}
	card := BuildLocalizedPlanetCard(context.Background(), planet, testFetchedAt(), false, trans)

	gotBiome := []rune(card.BiomeDesc)
	if len(gotBiome) != maxBiomeDescRunes+1 || gotBiome[len(gotBiome)-1] != '…' {
		t.Errorf("群系说明应截断到 %d 字并以省略号结尾，实际 %d 字", maxBiomeDescRunes, len(gotBiome))
	}
	if len(card.EnvItems) != 1 {
		t.Fatalf("应剩 1 条危害，实际 %+v", card.EnvItems)
	}
	gotHazard := []rune(card.EnvItems[0].Description)
	if len(gotHazard) != maxHazardDescRunes+1 || gotHazard[len(gotHazard)-1] != '…' {
		t.Errorf("危害说明应截断到 %d 字并以省略号结尾，实际 %d 字", maxHazardDescRunes, len(gotHazard))
	}
	// 截断后的说明也要进文本回退，两条路径同一份数据。
	if text := FormatPlanetText(card); !strings.Contains(text, string(gotHazard)) {
		t.Errorf("文本回退应使用同一份截断结果：\n%s", text)
	}
}

// TestBuildLocalizedPlanetCardKeepsAllHazards 校验危害条数不做上限：
// 漏报一种危害比卡片高一截更糟，只有说明按 rune 截断。
func TestBuildLocalizedPlanetCardKeepsAllHazards(t *testing.T) {
	const count = 12
	hazards := make([]hd2.Hazard, 0, count)
	out := []string{"火山", ""}
	for i := 0; i < count; i++ {
		name := "Hazard " + strconv.Itoa(i)
		hazards = append(hazards, hd2.Hazard{Name: name, Description: "说明 " + strconv.Itoa(i)})
		out = append(out, "危害"+strconv.Itoa(i), strings.Repeat("很长", 100))
	}
	planet := hd2.Planet{Index: 7, Name: "Acamar IV", Biome: hd2.Biome{Name: "Volcanic"}, Hazards: hazards}
	card := BuildLocalizedPlanetCard(context.Background(), planet, testFetchedAt(), false, &plugtest.Translator{Out: out})

	if len(card.EnvItems) != count {
		t.Fatalf("上游给了 %d 条危害就该有 %d 条，实际 %d 条", count, count, len(card.EnvItems))
	}
	for i := 0; i < count; i++ {
		want := "危害" + strconv.Itoa(i)
		if card.EnvItems[i].Name != want {
			t.Errorf("第 %d 条危害名错误：期望 %q，实际 %q", i+1, want, card.EnvItems[i].Name)
		}
		if !strings.Contains(card.Hazards, want) {
			t.Errorf("危害名 %q 不该从汇总里丢掉，实际 %q", want, card.Hazards)
		}
	}
}

// TestBuildLocalizedPlanetCardWithoutTranslator 校验没传翻译层时（translate.enabled=false）
// 卡片仍有完整的英文环境信息，且不写「翻译不可用」的说明——那条文案是给「配了翻译但没翻成」用的。
func TestBuildLocalizedPlanetCardWithoutTranslator(t *testing.T) {
	card := BuildLocalizedPlanetCard(context.Background(), testPlanetWithEnvironment(), testFetchedAt(), false, nil)

	if card.Biome != "Deciduous Forest" || card.BiomeDesc != "Temperate woodland" {
		t.Errorf("没有翻译层时群系应保持英文且剥掉标记，实际 %q / %q", card.Biome, card.BiomeDesc)
	}
	if card.Hazards != "Acid Storms" || len(card.EnvItems) != 1 {
		t.Fatalf("没有翻译层时环境危害应保持英文，实际 %q / %+v", card.Hazards, card.EnvItems)
	}
	if card.EnvItems[0].Description != "Corrosive rain damages armor." {
		t.Errorf("没有翻译层时危害说明应保持英文，实际 %q", card.EnvItems[0].Description)
	}
	if card.EnvNote != "" {
		t.Errorf("没有翻译层时不该写翻译不可用的说明，实际 %q", card.EnvNote)
	}

	// 截断在收尾阶段完成，与有没有翻译层无关：英文原文同样要按预算截断。
	long := hd2.Planet{Index: 7, Name: "Acamar IV", Hazards: []hd2.Hazard{{
		Name: "Acid Storms", Description: strings.Repeat("long ", 100),
	}}}
	got := BuildLocalizedPlanetCard(context.Background(), long, testFetchedAt(), false, nil)
	if len(got.EnvItems) != 1 {
		t.Fatalf("应有 1 条危害，实际 %+v", got.EnvItems)
	}
	if runes := []rune(got.EnvItems[0].Description); len(runes) != maxHazardDescRunes+1 {
		t.Errorf("没有翻译层时危害说明也要截断到 %d 字，实际 %d 字", maxHazardDescRunes, len(runes))
	}
}

// TestBuildLocalizedPlanetCardEmptySourceNotMissed 校验上游没给的内容不算「没翻动」：
// 群系名与说明为空时不该因此写上「翻译不可用」，也不该采信翻译层为空原文编出来的内容。
func TestBuildLocalizedPlanetCardEmptySourceNotMissed(t *testing.T) {
	planet := hd2.Planet{
		Index:   7,
		Name:    "Acamar IV",
		Hazards: []hd2.Hazard{{Name: "Acid Storms", Description: "Corrosive rain"}},
	}
	cases := []struct {
		name string
		out  []string
	}{
		{"空段原样返回", []string{"", "", "酸雨风暴", "腐蚀性降雨"}},
		{"翻译层给空原文编了内容", []string{"落叶林", "温带林地", "酸雨风暴", "腐蚀性降雨"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			card := BuildLocalizedPlanetCard(context.Background(), planet, testFetchedAt(), false, &plugtest.Translator{Out: c.out})
			if card.Biome != plugutil.DashText || card.BiomeDesc != "" {
				t.Errorf("上游没给群系时应保持缺省，实际 %q / %q", card.Biome, card.BiomeDesc)
			}
			if card.Hazards != "酸雨风暴" || len(card.EnvItems) != 1 || card.EnvItems[0].Description != "腐蚀性降雨" {
				t.Errorf("真实危害仍应翻译，实际 %q / %+v", card.Hazards, card.EnvItems)
			}
			if card.EnvNote != "" {
				t.Errorf("空原文不算没翻动，不该写翻译不可用的说明，实际 %q", card.EnvNote)
			}
		})
	}
}

// TestCleanUnofficialStripsMarkers 校验上游括号标记的剥离规则：
// 已知标记（含大小写变体）连带括号一起剥掉，不认识的内容原样保留。
func TestCleanUnofficialStripsMarkers(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"UNOFFICIAL", "Temperate woodland (UNOFFICIAL)", "Temperate woodland"},
		{"UNOFFICIAL DATA", "Temperate woodland (UNOFFICIAL DATA)", "Temperate woodland"},
		{"BETA", "Cold tundra (BETA)", "Cold tundra"},
		{"WIP", "Cold tundra (WIP)", "Cold tundra"},
		{"小写变体", "temperate woodland (unofficial)", "temperate woodland"},
		{"大小写混写", "Cold tundra (Beta)", "Cold tundra"},
		{"小写 wip", "Cold tundra (wip)", "Cold tundra"},
		{"标记在中间", "Temperate (BETA) woodland", "Temperate woodland"},
		{"多个标记", "Temperate (BETA) woodland (WIP)", "Temperate woodland"},
		{"不认识的括号内容原样保留", "Temperate woodland (unknown)", "Temperate woodland (unknown)"},
		{"只有标记时清空", "(UNOFFICIAL)", ""},
		{"多余空白折叠成一个空格", "  Temperate   woodland  (UNOFFICIAL)  ", "Temperate woodland"},
		{"没有标记", "Temperate woodland", "Temperate woodland"},
		{"空串", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := cleanUnofficial(c.in); got != c.want {
				t.Errorf("cleanUnofficial(%q) = %q，期望 %q", c.in, got, c.want)
			}
		})
	}
}

// TestHazardsTextFiltersPlaceholder 校验危害名汇总：占位项（None / NONE / 空白）一律剔除。
func TestHazardsTextFiltersPlaceholder(t *testing.T) {
	cases := []struct {
		name    string
		hazards []hd2.Hazard
		want    string
	}{
		{"nil", nil, "暂无环境危害"},
		{"只有 None", []hd2.Hazard{{Name: "None"}}, "暂无环境危害"},
		{"None 大小写与空白变体", []hd2.Hazard{{Name: " none "}, {Name: "NONE"}, {Name: "  "}}, "暂无环境危害"},
		{"真实危害与占位混排", []hd2.Hazard{{Name: "None"}, {Name: "Acid Storms"}, {Name: "Extreme Cold"}}, "Acid Storms、Extreme Cold"},
		{"危害名两端空白会去掉", []hd2.Hazard{{Name: "  Acid Storms  "}}, "Acid Storms"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hazardsText(c.hazards); got != c.want {
				t.Errorf("hazardsText = %q，期望 %q", got, c.want)
			}
		})
	}
}

// TestPlanetCardHTMLShowsEnvironment 校验卡片上的环境区块：群系说明、逐条危害名与说明都在，
// 上游的 (UNOFFICIAL) 标记与 None 占位都不该出现。
func TestPlanetCardHTMLShowsEnvironment(t *testing.T) {
	trans := &plugtest.Translator{Out: []string{"落叶林", "温带林地", "酸雨风暴", "腐蚀性降雨会损坏护甲。"}}
	card := BuildLocalizedPlanetCard(context.Background(), testPlanetWithEnvironment(), testFetchedAt(), false, trans)

	html, err := render.HTML(render.Card{Name: "planet", Data: card})
	if err != nil {
		t.Fatalf("生成 HTML 失败：%v", err)
	}
	wants := []string{
		"落叶林", "群系说明", "温带林地",
		`<div class="pd-title">环境危害<span class="pd-cn">ENVIRONMENTAL HAZARDS</span></div>`,
		`<span class="pd-chip pd-chip-neg">酸雨风暴</span>`, "腐蚀性降雨会损坏护甲。",
	}
	for _, want := range wants {
		if !strings.Contains(html, want) {
			t.Errorf("卡片缺少 %q：\n%s", want, html)
		}
	}
	if strings.Contains(html, "UNOFFICIAL") || strings.Contains(html, "None") {
		t.Error("上游的括号标记与 None 占位不该出现在卡片上")
	}
}

// TestPlanetCardHTMLHidesEmptyEnvironment 校验环境区块的隐藏条件：
// 没有真实危害时不渲染「环境危害」列表节，翻译正常（EnvNote 为空）时不渲染说明区块，
// 上游没给群系说明时连那一行都不渲染。
func TestPlanetCardHTMLHidesEmptyEnvironment(t *testing.T) {
	planet := hd2.Planet{
		Index:   7,
		Name:    "Acamar IV",
		Biome:   hd2.Biome{Name: "Volcanic"},
		Hazards: []hd2.Hazard{{Name: "None"}},
	}
	trans := &plugtest.Translator{Out: []string{"火山", ""}}
	card := BuildLocalizedPlanetCard(context.Background(), planet, testFetchedAt(), false, trans)

	html, err := render.HTML(render.Card{Name: "planet", Data: card})
	if err != nil {
		t.Fatalf("生成 HTML 失败：%v", err)
	}
	if !strings.Contains(html, "暂无环境危害") {
		t.Error("环境危害小节应显示「暂无环境危害」")
	}
	// 注意断言的是渲染出来的元素：.pd-chip 的样式定义本来就写在页面 <style> 里。
	if strings.Contains(html, `<span class="pd-chip`) {
		t.Error("没有真实危害时不该渲染危害 chips")
	}
	if strings.Contains(html, "群系说明") {
		t.Error("上游没给群系说明时不该渲染这一行")
	}
	if strings.Contains(html, "翻译暂不可用") {
		t.Error("翻译正常时不该渲染说明区块")
	}
}

// TestPlanetCardHTMLShowsTranslateNote 校验翻译不可用时卡片上有说明区块（群友知道下面为什么是英文）。
func TestPlanetCardHTMLShowsTranslateNote(t *testing.T) {
	trans := &plugtest.Translator{Err: errors.New("翻译后端挂了")}
	card := BuildLocalizedPlanetCard(context.Background(), testPlanetWithEnvironment(), testFetchedAt(), false, trans)

	html, err := render.HTML(render.Card{Name: "planet", Data: card})
	if err != nil {
		t.Fatalf("生成 HTML 失败：%v", err)
	}
	if !strings.Contains(html, environmentNote) {
		t.Errorf("卡片应带翻译说明 %q：\n%s", environmentNote, html)
	}
}

// TestFormatPlanetTextIncludesEnvironment 校验文本回退与卡片同口径（含环境区块）。
func TestFormatPlanetTextIncludesEnvironment(t *testing.T) {
	trans := &plugtest.Translator{Out: []string{"落叶林", "温带林地", "酸雨风暴", "腐蚀性降雨会损坏护甲。"}}
	card := BuildLocalizedPlanetCard(context.Background(), testPlanetWithEnvironment(), testFetchedAt(), false, trans)
	text := FormatPlanetText(card)

	for _, want := range []string{"落叶林", "温带林地", "酸雨风暴", "腐蚀性降雨"} {
		if !strings.Contains(text, want) {
			t.Errorf("文本回退缺少 %q：\n%s", want, text)
		}
	}
	if strings.Contains(text, "UNOFFICIAL") {
		t.Errorf("上游的 (UNOFFICIAL) 标记应被剥掉：\n%s", text)
	}
}

// TestFormatPlanetTextOmitsEmptyEnvironment 校验文本回退与模板同口径：
// 没有群系说明、没有真实危害、翻译正常时不写这些行（不留空行、不写空文案）。
func TestFormatPlanetTextOmitsEmptyEnvironment(t *testing.T) {
	planet := hd2.Planet{
		Index:   7,
		Name:    "Acamar IV",
		Biome:   hd2.Biome{Name: "Volcanic"},
		Hazards: []hd2.Hazard{{Name: "None"}},
	}
	trans := &plugtest.Translator{Out: []string{"火山", ""}}
	card := BuildLocalizedPlanetCard(context.Background(), planet, testFetchedAt(), false, trans)
	text := FormatPlanetText(card)

	if !strings.Contains(text, "暂无环境危害") {
		t.Errorf("文本回退应显示「暂无环境危害」：\n%s", text)
	}
	if strings.Contains(text, "群系说明") {
		t.Errorf("上游没给群系说明时不该有这一行：\n%s", text)
	}
	if strings.Contains(text, "环境危害：") {
		t.Errorf("没有真实危害时不该有逐条环境危害行：\n%s", text)
	}
	if strings.Contains(text, "翻译暂不可用") {
		t.Errorf("翻译正常时不该有说明文案：\n%s", text)
	}
}

// TestFormatPlanetTextIncludesTranslateNote 校验翻译不可用时文本回退也带那句说明
// （回退文本与卡片同源，不会只有图片里说明英文的原因）。
func TestFormatPlanetTextIncludesTranslateNote(t *testing.T) {
	trans := &plugtest.Translator{Err: errors.New("翻译后端挂了")}
	card := BuildLocalizedPlanetCard(context.Background(), testPlanetWithEnvironment(), testFetchedAt(), false, trans)
	text := FormatPlanetText(card)
	if !strings.Contains(text, environmentNote) {
		t.Errorf("文本回退应带翻译说明 %q：\n%s", environmentNote, text)
	}
}

// TestFormatPlanetTextKeepsDataTimeAndStale 校验数据时间与过期提示取自卡片视图模型：
// 文本与图片同源，不会一边标「已过期」一边不提。
func TestFormatPlanetTextKeepsDataTimeAndStale(t *testing.T) {
	card := BuildPlanetCard(hd2.Planet{Index: 7, Name: "Acamar IV"}, testFetchedAt(), true)
	text := FormatPlanetText(card)
	if !strings.Contains(text, "数据时间：") || !strings.Contains(text, "数据可能已过期") {
		t.Errorf("文本回退应带数据时间与过期提示：\n%s", text)
	}
}

// TestPlanetTextAndCardShareEnvironment 校验文本回退与 HTML 卡片吃同一份视图模型：
// 同一张卡片的两条路径在群系说明、环境说明与逐条危害上逐字一致。
func TestPlanetTextAndCardShareEnvironment(t *testing.T) {
	trans := &plugtest.Translator{Err: errors.New("翻译后端挂了")}
	card := BuildLocalizedPlanetCard(context.Background(), testPlanetWithEnvironment(), testFetchedAt(), false, trans)

	html, err := render.HTML(render.Card{Name: "planet", Data: card})
	if err != nil {
		t.Fatalf("生成 HTML 失败：%v", err)
	}
	text := FormatPlanetText(card)
	if len(card.EnvItems) != 1 {
		t.Fatalf("用例前提是 1 条危害，实际 %+v", card.EnvItems)
	}
	// 文本回退里的正文经过 MarkdownV2 转义（句点会变成 \.），HTML 侧则走 html/template 的转义。
	wants := []string{card.Biome, card.BiomeDesc, card.EnvNote, card.EnvItems[0].Name, card.EnvItems[0].Description}
	for _, want := range wants {
		if !strings.Contains(html, want) {
			t.Errorf("卡片缺少 %q：\n%s", want, html)
		}
		if !strings.Contains(text, bot.Escape(want)) {
			t.Errorf("文本回退缺少 %q：\n%s", want, text)
		}
	}
}

// TestTruncateTextBounds 校验截断的边界：不超预算的原样返回，max <= 0 表示不限制
// （调用方给 0 时按「不限制」处理，而不是把正文清空）。
// 这条契约与 plugins/orders 的同名 truncateRunes 正好相反（那边 max<=0 返回空串并标记已截断），
// 两侧各有一条用例把自己的契约钉死，避免改错包。
func TestTruncateTextBounds(t *testing.T) {
	const short = "温带林地"
	for _, max := range []int{4, 10, 0, -1} {
		if got := truncateText(short, max); got != short {
			t.Errorf("truncateText(%q, %d) = %q，期望原样返回", short, max, got)
		}
	}
	if got := truncateText(short, 2); got != "温带…" {
		t.Errorf("超预算时应按 rune 截断并补省略号，实际 %q", got)
	}
}

// TestFormatPlanetTextHandlesHazardWithoutDescription 校验危害没有说明时文本回退只写一行名称、
// 不留悬空的分隔符（上游对部分危害只给名字不给说明）。
func TestFormatPlanetTextHandlesHazardWithoutDescription(t *testing.T) {
	planet := hd2.Planet{
		Index:   7,
		Name:    "Acamar IV",
		Biome:   hd2.Biome{Name: "Volcanic"},
		Hazards: []hd2.Hazard{{Name: "Acid Storms"}},
	}
	trans := &plugtest.Translator{Out: []string{"火山", "", "酸雨风暴", ""}}
	card := BuildLocalizedPlanetCard(context.Background(), planet, testFetchedAt(), false, trans)
	if len(card.EnvItems) != 1 || card.EnvItems[0].Description != "" {
		t.Fatalf("上游没给说明时描述应为空串，实际 %+v", card.EnvItems)
	}
	if card.EnvNote != "" {
		t.Errorf("上游没给说明不算没翻动，实际 %q", card.EnvNote)
	}

	text := FormatPlanetText(card)
	if !strings.Contains(text, "环境危害：酸雨风暴") {
		t.Errorf("没有说明时只写一行名称：\n%s", text)
	}
	if strings.Contains(text, "酸雨风暴 ｜") {
		t.Errorf("没有说明时不该留分隔符：\n%s", text)
	}
	html, err := render.HTML(render.Card{Name: "planet", Data: card})
	if err != nil {
		t.Fatalf("生成 HTML 失败：%v", err)
	}
	if !strings.Contains(html, `<span class="pd-chip pd-chip-neg">酸雨风暴</span>`) {
		t.Errorf("卡片仍应列出这条危害：\n%s", html)
	}
}

// TestFormatPlanetTextIncludesEvent 校验文本回退也带星球事件区块（与卡片吃同一份视图模型）。
func TestFormatPlanetTextIncludesEvent(t *testing.T) {
	end := time.Date(2026, 9, 18, 11, 2, 10, 0, time.UTC)
	card := BuildPlanetCard(hd2.Planet{
		Index: 268, Name: "Luxuriant", CurrentOwner: "Humans",
		Event: &hd2.PlanetEvent{
			EventType: 1, Faction: "Terminids", Health: 1200000, MaxHealth: 1500000,
			StartTime: time.Date(2026, 9, 16, 11, 2, 10, 0, time.UTC), EndTime: &end,
		},
	}, testFetchedAt(), false)

	text := FormatPlanetText(card)
	for _, want := range []string{"星球事件：进攻方 终结族", "防守剩余：80", "防守血量：1,200,000 / 1,500,000", "起止："} {
		if !strings.Contains(text, want) {
			t.Errorf("文本回退缺少 %q：\n%s", want, text)
		}
	}
}

// TestBuildLocalizedPlanetCardChineseSourceNotFlagged 校验「环境信息本来就是中文」时不写翻译说明。
//
// 翻译层对不含拉丁字母的段落根本不会发请求，原样回显是正常结果；
// 过去按「译文 == 原文」一律判成没翻动，于是这类星球的卡片上会多一句
// 「翻译暂不可用，环境信息为英文原文」，而卡片里一个英文都没有。
func TestBuildLocalizedPlanetCardChineseSourceNotFlagged(t *testing.T) {
	planet := hd2.Planet{
		Index:   7,
		Name:    "Acamar IV",
		Biome:   hd2.Biome{Name: "落叶林", Description: "温带林地"},
		Hazards: []hd2.Hazard{{Name: "酸雨风暴", Description: "腐蚀性降雨会损坏护甲。"}},
	}
	trans := &plugtest.Translator{} // 回显替身：逐段原样返回
	card := BuildLocalizedPlanetCard(context.Background(), planet, testFetchedAt(), false, trans)

	if card.Biome != "落叶林" || card.BiomeDesc != "温带林地" {
		t.Errorf("中文原文应原样保留，实际 %q / %q", card.Biome, card.BiomeDesc)
	}
	if card.Hazards != "酸雨风暴" {
		t.Errorf("危害名应原样保留，实际 %q", card.Hazards)
	}
	if card.EnvNote != "" {
		t.Errorf("中文环境信息不该被标成翻译故障，实际 %q", card.EnvNote)
	}
	if text := FormatPlanetText(card); strings.Contains(text, "翻译暂不可用") {
		t.Errorf("文本回退里也不该出现翻译说明：\n%s", text)
	}
	html, err := render.HTML(render.Card{Name: "planet", Data: card})
	if err != nil {
		t.Fatalf("生成 HTML 失败：%v", err)
	}
	if strings.Contains(html, environmentNote) {
		t.Errorf("卡片 HTML 里不该出现翻译说明：\n%s", html)
	}

	// 同一颗星球里夹一段真英文（层会真的送它）时，回显必须仍被认成没翻动：修复不能把失败也吞掉。
	mixed := planet
	mixed.Biome.Name = "Deciduous Forest"
	echo := BuildLocalizedPlanetCard(context.Background(), mixed, testFetchedAt(), false, &plugtest.Translator{})
	if echo.EnvNote != environmentNote {
		t.Fatalf("英文群系名回显仍应注明翻译不可用，实际 %q", echo.EnvNote)
	}
}

// TestBuildLocalizedPlanetCardTruncatesNames 校验群系名与危害名也按预算截断。
// 名字同样来自上游与翻译后端，5000 字的群系名（或译文）足以把卡片拉成一屏，
// 只截说明挡不住。
func TestBuildLocalizedPlanetCardTruncatesNames(t *testing.T) {
	longName := strings.Repeat("名", 5000)
	planet := hd2.Planet{
		Index:   7,
		Name:    "Acamar IV",
		Biome:   hd2.Biome{Name: "Deciduous Forest", Description: "Temperate woodland"},
		Hazards: []hd2.Hazard{{Name: "Acid Storms", Description: "Corrosive rain"}},
	}
	trans := &plugtest.Translator{Out: []string{longName, "温带林地", longName, "腐蚀性降雨"}}
	card := BuildLocalizedPlanetCard(context.Background(), planet, testFetchedAt(), false, trans)

	if got := []rune(card.Biome); len(got) != maxNameRunes+1 || got[len(got)-1] != '…' {
		t.Errorf("群系名应截到 %d 字并以省略号结尾，实际 %d 字", maxNameRunes, len(got))
	}
	if len(card.EnvItems) != 1 {
		t.Fatalf("应剩 1 条危害，实际 %+v", card.EnvItems)
	}
	if got := []rune(card.EnvItems[0].Name); len(got) != maxNameRunes+1 || got[len(got)-1] != '…' {
		t.Errorf("危害名应截到 %d 字并以省略号结尾，实际 %d 字", maxNameRunes, len(got))
	}
	// 汇总行与逐条列表必须同源：汇总里不能留着没截断的长名字。
	if got := []rune(card.Hazards); len(got) != maxNameRunes+1 {
		t.Errorf("汇总的危害名应与逐条一致（%d 字），实际 %d 字", maxNameRunes+1, len(got))
	}
	if text := FormatPlanetText(card); !strings.Contains(text, card.EnvItems[0].Name) {
		t.Errorf("文本回退应使用同一份截断结果：\n%s", text)
	}

	// 英文原文路径（没有翻译层）同样受预算约束。
	plain := BuildLocalizedPlanetCard(context.Background(), hd2.Planet{
		Index:   7,
		Name:    "Acamar IV",
		Biome:   hd2.Biome{Name: strings.Repeat("A", 200)},
		Hazards: []hd2.Hazard{{Name: strings.Repeat("B", 200)}},
	}, testFetchedAt(), false, nil)
	if got := []rune(plain.Biome); len(got) != maxNameRunes+1 {
		t.Errorf("没有翻译层时群系名也要截到 %d 字，实际 %d 字", maxNameRunes, len(got))
	}
	if got := []rune(plain.EnvItems[0].Name); len(got) != maxNameRunes+1 {
		t.Errorf("没有翻译层时危害名也要截到 %d 字，实际 %d 字", maxNameRunes, len(got))
	}
}

// TestBuildLocalizedPlanetCardCleansTranslatedMarkers 校验译文回填前也剥一次上游标记。
// 后端偶尔会把原文里的 (UNOFFICIAL) 一起带回来（模型照抄），标记留在卡片上比英文更扎眼。
func TestBuildLocalizedPlanetCardCleansTranslatedMarkers(t *testing.T) {
	// 标记一律是半角括号（后端照抄原文时也是半角，cleanUnofficial 的正则只认半角）。
	trans := &plugtest.Translator{Out: []string{
		"落叶林 (UNOFFICIAL)", "温带林地 (unofficial)", "酸雨风暴 (BETA)", "腐蚀性降雨 (WIP)",
	}}
	card := BuildLocalizedPlanetCard(context.Background(), testPlanetWithEnvironment(), testFetchedAt(), false, trans)

	if card.Biome != "落叶林" {
		t.Errorf("译文里的标记也要剥掉，实际 %q", card.Biome)
	}
	if card.BiomeDesc != "温带林地" {
		t.Errorf("小写标记同样要剥（正则大小写不敏感），实际 %q", card.BiomeDesc)
	}
	if card.Hazards != "酸雨风暴" || card.EnvItems[0].Name != "酸雨风暴" {
		t.Errorf("危害名的译文同样要剥标记，实际 %q / %+v", card.Hazards, card.EnvItems[0])
	}
	if card.EnvItems[0].Description != "腐蚀性降雨" {
		t.Errorf("危害说明的译文同样要剥标记，实际 %q", card.EnvItems[0].Description)
	}
	if strings.Contains(FormatPlanetText(card), "UNOFFICIAL") {
		t.Errorf("文本回退里不该出现标记：\n%s", FormatPlanetText(card))
	}
}

// TestBuildLocalizedPlanetCardTruncatesEnglishFallback 校验翻译失败时英文原文同样按预算截断。
// 这条路径真实存在（翻译失败时保留的英文原文同样可能很长），
// 只在翻译成功路径截断的话，回退时卡片就会被一条英文说明拉长。
func TestBuildLocalizedPlanetCardTruncatesEnglishFallback(t *testing.T) {
	longDesc := strings.Repeat("long description ", 30) // 远超 maxBiomeDescRunes
	planet := hd2.Planet{
		Index:   7,
		Name:    "Acamar IV",
		Biome:   hd2.Biome{Name: "Deciduous Forest", Description: longDesc},
		Hazards: []hd2.Hazard{{Name: "Acid Storms", Description: longDesc}},
	}
	card := BuildLocalizedPlanetCard(context.Background(), planet, testFetchedAt(), false, &plugtest.Translator{Err: errors.New("翻译后端挂了")})

	if card.EnvNote != environmentNote {
		t.Fatalf("翻译失败时应注明保留英文，实际 %q", card.EnvNote)
	}
	if got := []rune(card.BiomeDesc); len(got) != maxBiomeDescRunes+1 || got[len(got)-1] != '…' {
		t.Errorf("回退的群系说明应截到 %d 字并以省略号结尾，实际 %d 字", maxBiomeDescRunes, len(got))
	}
	if len(card.EnvItems) != 1 {
		t.Fatalf("应剩 1 条危害，实际 %+v", card.EnvItems)
	}
	if got := []rune(card.EnvItems[0].Description); len(got) != maxHazardDescRunes+1 || got[len(got)-1] != '…' {
		t.Errorf("回退的危害说明应截到 %d 字并以省略号结尾，实际 %d 字", maxHazardDescRunes, len(got))
	}
	if !strings.HasPrefix(card.BiomeDesc, "long description") {
		t.Errorf("截断不能把内容改样，实际 %q", card.BiomeDesc)
	}
}

// TestEnvironmentBudgetsKeepRealUpstreamLengths 校验真实上游最长的环境信息完整显示（不带省略号）：
// 群系说明 337 字、危害说明 100 字、危害名 17 字（S4 设计 §2 的实测口径）。
// 预算 400/200/80 都留了 1.2 倍以上余量，正常内容不该被终局防线碰到。
func TestEnvironmentBudgetsKeepRealUpstreamLengths(t *testing.T) {
	biomeDesc := strings.Repeat("温", 337)
	hazardName := strings.Repeat("害", 17)
	hazardDesc := strings.Repeat("危", 100)
	planet := hd2.Planet{
		Index:   7,
		Name:    "Acamar IV",
		Biome:   hd2.Biome{Name: "Deciduous Forest", Description: "Temperate woodland"},
		Hazards: []hd2.Hazard{{Name: "Acid Storms", Description: "Corrosive rain"}},
	}
	trans := &plugtest.Translator{Out: []string{"落叶林", biomeDesc, hazardName, hazardDesc}}
	card := BuildLocalizedPlanetCard(context.Background(), planet, testFetchedAt(), false, trans)

	if card.BiomeDesc != biomeDesc {
		t.Errorf("337 字的群系说明应完整显示，实际 %d 字", len([]rune(card.BiomeDesc)))
	}
	if len(card.EnvItems) != 1 {
		t.Fatalf("应剩 1 条危害，实际 %+v", card.EnvItems)
	}
	if card.EnvItems[0].Name != hazardName {
		t.Errorf("17 字的危害名应完整显示，实际 %q", card.EnvItems[0].Name)
	}
	if card.EnvItems[0].Description != hazardDesc {
		t.Errorf("100 字的危害说明应完整显示，实际 %d 字", len([]rune(card.EnvItems[0].Description)))
	}
	if strings.Contains(FormatPlanetText(card), "…") {
		t.Errorf("文本回退里不该出现省略号：\n%s", FormatPlanetText(card))
	}
	html, err := render.HTML(render.Card{Name: "planet", Data: card})
	if err != nil {
		t.Fatalf("渲染星球卡片 HTML 失败：%v", err)
	}
	if strings.Contains(html, "…") {
		t.Error("卡片 HTML 里不该出现省略号")
	}
}

// TestEnvironmentBudgetsTerminalLimitBoundary 校验环境信息的终局防线边界：
// 正好等于预算不截、多一个字才截。边界写字面量（80/400/200），
// 把常量改回旧的 40/160/120 会让这条变红。
func TestEnvironmentBudgetsTerminalLimitBoundary(t *testing.T) {
	cases := []struct {
		name  string
		limit int
		card  func(text string) PlanetCard
		got   func(card PlanetCard) string
	}{
		{
			name:  "群系名",
			limit: 80,
			card:  func(text string) PlanetCard { return PlanetCard{Biome: text} },
			got:   func(card PlanetCard) string { return card.Biome },
		},
		{
			name:  "群系说明",
			limit: 400,
			card:  func(text string) PlanetCard { return PlanetCard{BiomeDesc: text} },
			got:   func(card PlanetCard) string { return card.BiomeDesc },
		},
		{
			name:  "危害名",
			limit: 80,
			card: func(text string) PlanetCard {
				return PlanetCard{EnvItems: []EnvItem{{Name: text}}}
			},
			got: func(card PlanetCard) string { return card.EnvItems[0].Name },
		},
		{
			name:  "危害说明",
			limit: 200,
			card: func(text string) PlanetCard {
				return PlanetCard{EnvItems: []EnvItem{{Name: "酸雨", Description: text}}}
			},
			got: func(card PlanetCard) string { return card.EnvItems[0].Description },
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			exact := c.got(finishEnvironment(c.card(strings.Repeat("字", c.limit))))
			if got := []rune(exact); len(got) != c.limit {
				t.Errorf("正好 %d 字不该截断，实际 %d 字", c.limit, len(got))
			}
			over := []rune(c.got(finishEnvironment(c.card(strings.Repeat("字", c.limit+1)))))
			if len(over) != c.limit+1 || over[len(over)-1] != 0x2026 {
				t.Errorf("%d 字应截到 %d 字并补省略号，实际 %d 字", c.limit+1, c.limit, len(over))
			}
		})
	}
}

// ---- 战略情报分析 / 行动变量 / 兴趣点的用例（用户 2026-09-18 要求，参照社区站点星图页）----

// intelValue 取「战略情报分析」里某一行的数值；标签不存在时返回空串（用例自行断言缺行）。
func intelValue(rows []IntelRow, label string) string {
	for _, row := range rows {
		if row.Label == label {
			return row.Value
		}
	}
	return ""
}

// intelClass 取某一行的配色 class。
func intelClass(rows []IntelRow, label string) string {
	for _, row := range rows {
		if row.Label == label {
			return row.Class
		}
	}
	return ""
}

// TestPlanetIntelRows 校验战略情报分析四行的口径：解放度两位小数、抵抗度带强度词与配色 class、
// 星球血量「百分比 · 当前/上限」、玩家数量千分位。
// 样本取自 testPlanets()[1]（机器人控制、被打到 19.24% 血量、每秒回复 5.5555553）。
func TestPlanetIntelRows(t *testing.T) {
	rows := planetIntel(testPlanets()[1])
	want := []struct{ label, value, class string }{
		{"解放度", "80.76%", ""},
		{"抵抗度", "1.25% / 小时（低）", "res-low"},
		{"星球血量", "19% · 307,863 / 1,600,000", ""},
		{"玩家数量", "19,507", ""},
	}
	if len(rows) != len(want) {
		t.Fatalf("战略情报分析应有 %d 行，实际 %+v", len(want), rows)
	}
	for i, w := range want {
		if rows[i].Label != w.label || rows[i].Value != w.value || rows[i].Class != w.class {
			t.Errorf("第 %d 行错误：期望 %+v，实际 %+v", i+1, w, rows[i])
		}
	}
}

// TestPlanetIntelSpecialCases 校验两种固定说法与缺数据时的兜底：
// 我方满血且没有战事写「已解放」，我方控制但正在挨打写「超级地球控制中」，
// 玩家数为 0 写「—」，血量上限缺失时整行写「—」。
func TestPlanetIntelSpecialCases(t *testing.T) {
	peaceful := hd2.Planet{Index: 1, Name: "Nublaria I", Sector: "Akira", CurrentOwner: "Humans", Health: 1000000, MaxHealth: 1000000}
	if got := intelValue(planetIntel(peaceful), "解放度"); got != "已解放" {
		t.Errorf("我方满血且无战事应写「已解放」，实际 %q", got)
	}
	if got := intelValue(planetIntel(peaceful), "抵抗度"); got != "无" {
		t.Errorf("每秒回复为 0 时抵抗度应写「无」，实际 %q", got)
	}
	if got := intelValue(planetIntel(peaceful), "玩家数量"); got != plugutil.DashText {
		t.Errorf("玩家数为 0 应写 %q，实际 %q", plugutil.DashText, got)
	}

	// 我方控制但有防守事件：血量恒满，直接说「0.00%」会被读成「还没拿下来」。
	defending := testPlanets()[2]
	if got := intelValue(planetIntel(defending), "解放度"); got != "超级地球控制中" {
		t.Errorf("我方控制且正在防守应写「超级地球控制中」，实际 %q", got)
	}

	// 上限缺失（上游脏数据）：算不出百分比，写「—」而不是「0%」。
	broken := hd2.Planet{Index: 2, Name: "Bekvam III", CurrentOwner: "Automaton"}
	if got := intelValue(planetIntel(broken), "星球血量"); got != plugutil.DashText {
		t.Errorf("血量上限缺失应写 %q，实际 %q", plugutil.DashText, got)
	}
}

// TestResistanceTiers 校验抵抗强度四档的阈值与配色：低 ≤1.99 / 中 2–2.99 / 高 3–3.99 / 极高 ≥4。
// 上限统一取 1,000,000，每秒回复换算成「每小时百分之几」后正好落在各档。
func TestResistanceTiers(t *testing.T) {
	cases := []struct {
		regen float64
		value string
		class string
	}{
		{0, "无", "res-none"},
		{5, "1.8% / 小时（低）", "res-low"},
		{7, "2.52% / 小时（中）", "res-mid"},
		{9, "3.24% / 小时（高）", "res-high"},
		{12, "4.32% / 小时（极高）", "res-max"},
	}
	for _, c := range cases {
		p := hd2.Planet{Index: 3, Name: "Bekvam III", Health: 1000000, MaxHealth: 1000000, RegenPerSecond: c.regen}
		value, class := resistanceText(p)
		if value != c.value || class != c.class {
			t.Errorf("每秒回复 %v 时抵抗度应为 %q/%q，实际 %q/%q", c.regen, c.value, c.class, value, class)
		}
	}
}

// TestFillPlanetExtrasResolvesAttackingName 校验「进攻目标」在补上星球列表后写星球名：
// 卡面上一行编号（「216」）在群里没人能对上号；列表里找不到那颗星球时才退回编号，不编名字。
// 模板里也刻意不带「编号」二字的前缀，标题行直接就是星球名。
func TestFillPlanetExtrasResolvesAttackingName(t *testing.T) {
	p := testPlanets()[2] // 编号 268（Luxuriant），Attacking = [216]
	card := FillPlanetExtras(BuildPlanetCard(p, testFetchedAt(), false), p, PlanetExtras{Planets: testPlanets()})
	if card.Attacking != "孔雀十一（Peacock）" {
		t.Errorf("进攻目标应写星球名，实际 %q", card.Attacking)
	}
	html, err := render.HTML(render.Card{Name: "planet", Data: card})
	if err != nil {
		t.Fatalf("生成 HTML 失败：%v", err)
	}
	if !strings.Contains(html, "进攻目标") || !strings.Contains(html, "孔雀十一（Peacock）") {
		t.Error("卡片上应把进攻目标写成星球名")
	}
	if strings.Contains(html, "编号 216") {
		t.Error("进攻目标不该再带「编号」前缀")
	}

	// 星球列表里没有那颗星球（补充数据缺失）时退回编号兜底，并带上「#」标明这是编号而不是名字。
	lonely := FillPlanetExtras(BuildPlanetCard(p, testFetchedAt(), false), p, PlanetExtras{})
	if lonely.Attacking != "#216" {
		t.Errorf("查不到星球名时应退回带 # 的编号，实际 %q", lonely.Attacking)
	}
}

// TestFillPlanetExtrasEffectChips 校验行动变量标签：对照表与同族别名命中的给中文名，
// 作战限制类标成负面，两处都查不到的并成一条「未知行动变量」（不显示编号），
// 别的星球的效果不进这张卡，同编号与同名（同族变体）的重复各只留一条。
func TestFillPlanetExtrasEffectChips(t *testing.T) {
	p := testPlanets()[1] // 编号 110
	extras := PlanetExtras{
		EffectsKnown: true,
		Effects: []hd2.PlanetEffect{
			{PlanetIndex: 110, EffectID: 1243}, // 掠食变种：对照表收录且带说明
			{PlanetIndex: 110, EffectID: 1243}, // 上游偶发重复：只留一条
			{PlanetIndex: 110, EffectID: 1245}, // 同名变体编号：按名字去重，不再多占一格
			{PlanetIndex: 110, EffectID: 1272}, // 对照表没收、同族别名命中：给名字与分类，但不带说明
			{PlanetIndex: 110, EffectID: 1313}, // 作战限制：负面
			{PlanetIndex: 110, EffectID: 1358}, // 两处都查不到：并成一条「未知行动变量」
			{PlanetIndex: 216, EffectID: 1313}, // 别的星球：不该出现
		},
	}
	card := FillPlanetExtras(BuildPlanetCard(p, testFetchedAt(), false), p, extras)

	if len(card.EffectChips) != 4 {
		t.Fatalf("应得到 4 条行动变量，实际 %+v", card.EffectChips)
	}
	predator := card.EffectChips[0]
	if predator.Name != "掠食变种" || predator.Category != "终结族变种" || predator.Negative {
		t.Errorf("掠食变种的展示形态错误：%+v", predator)
	}
	if predator.Description == "" {
		t.Error("对照表收录了说明的效果应带上说明")
	}
	// 别名只给名字与分类，不照抄同族其它编号的说明：那些说明里写着具体数值，抄过来就是错的。
	if cut := card.EffectChips[1]; cut.Name != "预算削减" || cut.Category != "作战限制" || !cut.Negative || cut.Description != "" {
		t.Errorf("同族别名应给名字与分类、不写说明：%+v", cut)
	}
	if delay := card.EffectChips[2]; delay.Name != "班机延误" || !delay.Negative {
		t.Errorf("作战限制类的效果应标成负面：%+v", delay)
	}
	if unknown := card.EffectChips[3]; unknown.Name != unknownEffectText || unknown.Description != "" {
		t.Errorf("查不到名字的效果应并成 %q 且不带说明：%+v", unknownEffectText, unknown)
	}
	if card.EffectNote != "" {
		t.Errorf("有行动变量时不该写说明文案，实际 %q", card.EffectNote)
	}
}

// TestFillPlanetExtrasEffectNotes 校验「确实没有」与「补充源没取到」是两句话：
// 前者是补充源给出的结论，后者是数据缺失，卡片上不能混为一谈。
func TestFillPlanetExtrasEffectNotes(t *testing.T) {
	p := testPlanets()[1]
	known := FillPlanetExtras(BuildPlanetCard(p, testFetchedAt(), false), p, PlanetExtras{EffectsKnown: true})
	if known.EffectNote != effectsEmptyText {
		t.Errorf("补充源取到但没有行动变量时应写 %q，实际 %q", effectsEmptyText, known.EffectNote)
	}
	unknown := FillPlanetExtras(BuildPlanetCard(p, testFetchedAt(), false), p, PlanetExtras{})
	if unknown.EffectNote != effectsUnknownText {
		t.Errorf("补充源没取到时应写 %q，实际 %q", effectsUnknownText, unknown.EffectNote)
	}
}

// TestPlanetPOIs 校验五类兴趣点标签的文案、配色与顺序（目标 → 战役 → 空间站 → 反攻 → 星区），
// 数据来源分别是重要指令、战役、空间站与「谁在打它」。
func TestPlanetPOIs(t *testing.T) {
	p := hd2.Planet{Index: 110, Name: "Bekvam III", Sector: "Akira", CurrentOwner: "Automaton"}
	extras := PlanetExtras{
		Planets: []hd2.Planet{
			p,
			{Index: 216, Name: "Peacock", Sector: "Jin Xi", Attacking: []int{110}},
		},
		Campaigns: []hd2.Campaign{{ID: 1, Planet: hd2.PlanetRef{Index: 110}, Type: 0}},
		Assignments: []hd2.Assignment{{ID: 1, Tasks: []hd2.Task{
			{Type: 11, Values: []int64{100, 110}, ValueTypes: []int{3, 12}},
		}}},
		Stations: []hd2.SpaceStation{{ID32: 749875195, PlanetIndex: 110}},
	}
	pois := planetPOIs(p, extras)
	want := []struct{ text, class string }{
		{"🎯 重要指令目标", "poi-mo"},
		{"⚔️ 解放战役进行中", "poi-camp"},
		{"🛰️ DSS 民主空间站停靠中", "poi-dss"},
		{"⚠️ 敌军反攻中 · 来自 孔雀十一（Peacock）", "poi-atk"},
		{"🌍 星区：阿基拉分区", ""},
	}
	if len(pois) != len(want) {
		t.Fatalf("应有 %d 枚兴趣点标签，实际 %+v", len(want), pois)
	}
	for i, w := range want {
		if pois[i].Text != w.text || pois[i].Class != w.class {
			t.Errorf("第 %d 枚标签错误：期望 %+v，实际 %+v", i+1, w, pois[i])
		}
	}
}

// TestPlanetPOIsDefenseCampaignAndNoSelfAttack 校验两点：
// 只有战役类型（type 4）没有事件时也按「入侵防御战」说；自己打自己这种脏数据不产生「敌军反攻」标签。
func TestPlanetPOIsDefenseCampaignAndNoSelfAttack(t *testing.T) {
	p := hd2.Planet{Index: 244, Name: "Varylia 5", Sector: "Jin Xi", CurrentOwner: "Humans"}
	extras := PlanetExtras{
		Planets:   []hd2.Planet{{Index: 244, Name: "Varylia 5", Attacking: []int{244}}}, // 脏数据：自己打自己
		Campaigns: []hd2.Campaign{{ID: 2, Planet: hd2.PlanetRef{Index: 244}, Type: campaignTypeDefense}},
	}
	texts := make([]string, 0, len(planetPOIs(p, extras)))
	for _, poi := range planetPOIs(p, extras) {
		texts = append(texts, poi.Text)
	}
	joined := strings.Join(texts, " ｜ ")
	if !strings.Contains(joined, "⚔️ 入侵防御战进行中") {
		t.Errorf("战役类型为防御战时应写「入侵防御战进行中」，实际 %q", joined)
	}
	if strings.Contains(joined, "敌军反攻中") {
		t.Errorf("自己打自己不该产生「敌军反攻中」标签，实际 %q", joined)
	}
	if !strings.Contains(joined, "🌍 星区：") {
		t.Errorf("星区标签始终要显示，实际 %q", joined)
	}
}

// TestPlanetCardHTMLExtras 校验模板把战略情报分析 / 行动变量 / 兴趣点三块排进卡片。
func TestPlanetCardHTMLExtras(t *testing.T) {
	p := testPlanets()[1]
	extras := PlanetExtras{
		EffectsKnown: true,
		Effects:      []hd2.PlanetEffect{{PlanetIndex: 110, EffectID: 1243}, {PlanetIndex: 110, EffectID: 1313}},
		Planets:      []hd2.Planet{p, {Index: 216, Name: "Peacock", Attacking: []int{110}}},
		Campaigns:    []hd2.Campaign{{ID: 1, Planet: hd2.PlanetRef{Index: 110}, Type: 0}},
		Assignments:  []hd2.Assignment{{ID: 1, Tasks: []hd2.Task{{Type: 11, Values: []int64{110}, ValueTypes: []int{12}}}}},
		Stations:     []hd2.SpaceStation{{ID32: 1, PlanetIndex: 110}},
	}
	card := FillPlanetExtras(BuildPlanetCard(p, testFetchedAt(), false), p, extras)
	html, err := render.HTML(render.Card{Name: "planet", Data: card})
	if err != nil {
		t.Fatalf("生成 HTML 失败：%v", err)
	}
	for _, want := range []string{
		"战略情报分析", "STRATEGIC ANALYSIS", "解放度", "抵抗度", "星球血量", "玩家数量",
		"res-low", // 抵抗强度四档的配色 class
		"行动变量", "OPERATIONAL PARAMETERS", "掠食变种", "班机延误", "pd-chip-neg",
		"兴趣点", "POINTS OF INTEREST", "🎯 重要指令目标", "⚔️ 解放战役进行中",
		"🛰️ DSS 民主空间站停靠中", "⚠️ 敌军反攻中", "🌍 星区：阿基拉分区",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("星球卡 HTML 缺少 %q", want)
		}
	}
}

// TestFillPlanetExtrasHTMLEmptyNotes 校验没有补充数据时卡片写的是「暂不可用」而不是「暂无」。
func TestFillPlanetExtrasHTMLEmptyNotes(t *testing.T) {
	p := testPlanets()[0]
	card := FillPlanetExtras(BuildPlanetCard(p, testFetchedAt(), false), p, PlanetExtras{})
	html, err := render.HTML(render.Card{Name: "planet", Data: card})
	if err != nil {
		t.Fatalf("生成 HTML 失败：%v", err)
	}
	if !strings.Contains(html, effectsUnknownText) {
		t.Errorf("补充源没取到时卡片应写 %q", effectsUnknownText)
	}
}

// TestFormatPlanetTextIncludesExtras 校验文本回退与卡片同口径：战略情报分析、行动变量与兴趣点都写出来。
func TestFormatPlanetTextIncludesExtras(t *testing.T) {
	p := testPlanets()[1]
	extras := PlanetExtras{
		EffectsKnown: true,
		Effects:      []hd2.PlanetEffect{{PlanetIndex: 110, EffectID: 1243}},
		Planets:      []hd2.Planet{p, {Index: 216, Name: "Peacock", Attacking: []int{110}}},
	}
	text := FormatPlanetText(FillPlanetExtras(BuildPlanetCard(p, testFetchedAt(), false), p, extras))
	for _, want := range []string{
		"解放度：", "抵抗度：", "星球血量：", "玩家数量：19,507",
		"行动变量：掠食变种", "兴趣点：", "🌍 星区：阿基拉分区",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("文本回退缺少 %q：\n%s", want, text)
		}
	}
}

// TestFormatPlanetTextExtrasNotes 校验文本回退里「暂无」与「暂不可用」也是两句话。
func TestFormatPlanetTextExtrasNotes(t *testing.T) {
	p := testPlanets()[0]
	known := FormatPlanetText(FillPlanetExtras(BuildPlanetCard(p, testFetchedAt(), false), p, PlanetExtras{EffectsKnown: true}))
	if !strings.Contains(known, "行动变量："+effectsEmptyText) {
		t.Errorf("补充源取到但没有行动变量时文本应写「%s」：\n%s", effectsEmptyText, known)
	}
	unknown := FormatPlanetText(FillPlanetExtras(BuildPlanetCard(p, testFetchedAt(), false), p, PlanetExtras{}))
	if !strings.Contains(unknown, "行动变量："+effectsUnknownText) {
		t.Errorf("补充源没取到时文本应写「%s」：\n%s", effectsUnknownText, unknown)
	}
}

// testPlanetWithExtras 是「战略情报分析 / 行动变量 / 兴趣点」三块的样本星球：
// 机器人控制、被打到 19.24% 血量（解放度 80.76%）、每秒回复 5.5555553（抵抗度 1.25%/小时）。
func testPlanetWithExtras() hd2.Planet {
	return testPlanets()[1]
}

// testPlanetExtras 是上面那颗星球的补充数据：五类兴趣点各拿一条，外加三条行动变量
// （一条带说明、一条作战限制、一条对照表未收录）。
func testPlanetExtras() PlanetExtras {
	return PlanetExtras{
		Planets: []hd2.Planet{
			testPlanets()[1],
			{Index: 216, Name: "Peacock", Sector: "Jin Xi", Attacking: []int{110}},
		},
		Campaigns:    []hd2.Campaign{{ID: 51428, Planet: hd2.PlanetRef{Index: 110}, Type: 0}},
		Assignments:  []hd2.Assignment{{ID: 1, Tasks: []hd2.Task{{Type: 11, Values: []int64{100, 110}, ValueTypes: []int{3, 12}}}}},
		Stations:     []hd2.SpaceStation{{ID32: 749875195, PlanetIndex: 110}},
		EffectsKnown: true,
		Effects: []hd2.PlanetEffect{
			{PlanetIndex: 110, EffectID: 1243},
			{PlanetIndex: 110, EffectID: 1272},
			{PlanetIndex: 110, EffectID: 1313},
		},
	}
}

// testDefenseExtras 是防守战样本星球的补充数据：五类兴趣点各一条（战役按防御战说），
// 外加两条行动变量——用来在冒烟图里核对「跟着入侵方配色 ＋ 三块新内容」同时出现时的排版。
func testDefenseExtras() PlanetExtras {
	p := testPlanetOnDefense()
	return PlanetExtras{
		Planets:      []hd2.Planet{p, {Index: 216, Name: "Peacock", Sector: "Jin Xi", Attacking: []int{268}}},
		Campaigns:    []hd2.Campaign{{ID: 51713, Planet: hd2.PlanetRef{Index: 268}, Type: campaignTypeDefense}},
		Assignments:  []hd2.Assignment{{ID: 1, Tasks: []hd2.Task{{Type: 11, Values: []int64{100, 268}, ValueTypes: []int{3, 12}}}}},
		Stations:     []hd2.SpaceStation{{ID32: 749875195, PlanetIndex: 268}},
		EffectsKnown: true,
		Effects: []hd2.PlanetEffect{
			{PlanetIndex: 268, EffectID: 1243},
			{PlanetIndex: 268, EffectID: 1313},
		},
	}
}

// planetCardWithExtras 生成一张「本地化环境信息 ＋ 补充数据」都齐的单星球卡（冒烟用例共用）：
// 环境信息固定用同一份译文，方便逐张对比排版，不让译文差异干扰判断。
func planetCardWithExtras(p hd2.Planet, extras PlanetExtras, fetchedAt time.Time) PlanetCard {
	card := BuildLocalizedPlanetCard(context.Background(), p, fetchedAt, false,
		&plugtest.Translator{Out: []string{"落叶林", "温带林地", "酸雨风暴", "腐蚀性降雨会损坏护甲。"}})
	return FillPlanetExtras(card, p, extras)
}
