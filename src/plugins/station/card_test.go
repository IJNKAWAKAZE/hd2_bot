package station

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"hd2_bot/src/hd2"
	"hd2_bot/src/plugins/plugutil"
	"hd2_bot/src/render"
)

// TestBuildEventsCardOrder 校验排序：按结束时间升序，EndTime 为 nil 的排最后并标「未知」。
func TestBuildEventsCardOrder(t *testing.T) {
	card := BuildEventsCard(testEvents(), testFetchedAt(), false)

	if card.Empty || len(card.Items) != 3 {
		t.Fatalf("条数/空标记错误：%+v", card)
	}
	wantIndex := []string{"268", "110", "5"} // 结束时间早的在前，没有结束时间的（编号 5）最后
	for i, index := range wantIndex {
		if card.Items[i].PlanetIndex != index {
			t.Fatalf("第 %d 起事件的星球编号应为 %s，实际 %s（顺序：%+v）", i+1, index, card.Items[i].PlanetIndex, card.Items)
		}
	}
	if card.Items[2].EndTime != "未知" {
		t.Errorf("没有结束时间应显示「未知」，实际 %q", card.Items[2].EndTime)
	}
	if card.MoreText != "" {
		t.Errorf("全部展示时不该有「未显示」提示：%q", card.MoreText)
	}
}

// TestBuildEventsCardLimitsToFive 校验事件最多展示 5 起，多出来的只报个数。
func TestBuildEventsCardLimitsToFive(t *testing.T) {
	base := testFetchedAt().UTC()
	list := make([]hd2.PlanetEvent, 0, 7)
	for i := 0; i < 7; i++ {
		end := base.Add(time.Duration(i) * time.Hour)
		list = append(list, hd2.PlanetEvent{
			ID: int64(i), EventType: 1, Faction: "Terminids", Health: 100, MaxHealth: 200,
			EndTime: &end, PlanetIndex: 100 + i, PlanetName: fmt.Sprintf("PLANET %d", i),
		})
	}
	card := BuildEventsCard(list, testFetchedAt(), false)

	if len(card.Items) != maxEvents {
		t.Fatalf("最多展示 %d 起，实际 %d 起", maxEvents, len(card.Items))
	}
	if !strings.Contains(card.MoreText, "另有 2 起") {
		t.Errorf("应提示还有几起没显示，实际 %q", card.MoreText)
	}
}

// TestBuildEventsCardMissingMaxHealth 校验上游没给血量上限时不画进度条：
// 显示「防守剩余 0.0%」会被误读成「马上就守不住了」。
func TestBuildEventsCardMissingMaxHealth(t *testing.T) {
	list := []hd2.PlanetEvent{{ID: 1, EventType: 2, Faction: "Terminids", Health: 0, MaxHealth: 0, PlanetIndex: 5, PlanetName: "ACAMAR IV"}}
	item := BuildEventsCard(list, testFetchedAt(), false).Items[0]

	if item.BarText != "" {
		t.Errorf("没有上限时进度条文案应留空（模板整块隐藏），实际 %q", item.BarText)
	}
	if item.BarPercent != 0 {
		t.Errorf("没有上限时进度应为 0，实际 %v", item.BarPercent)
	}
	if item.HealthText != "—" {
		t.Errorf("没有上限时血量应显示「—」，实际 %q", item.HealthText)
	}
}

// TestBuildEventsCardFactionFallback 校验未收录的派系原样显示，不归到某个已知派系上。
func TestBuildEventsCardFactionFallback(t *testing.T) {
	list := []hd2.PlanetEvent{
		{ID: 1, Faction: "Automatons", PlanetIndex: 1, PlanetName: "ACAMAR IV"}, // 复数写法要归到机器人
		{ID: 2, Faction: "Foo", PlanetIndex: 2, PlanetName: "ACAMAR IV"},
		{ID: 3, Faction: "", PlanetIndex: 3, PlanetName: "ACAMAR IV"},
	}
	items := BuildEventsCard(list, testFetchedAt(), false).Items

	if items[0].Faction != "机器人" || items[0].FactionClass != "faction--automaton" {
		t.Errorf("Automatons 应归到机器人：%+v", items[0])
	}
	if items[1].Faction != "Foo" || items[1].FactionClass != "" {
		t.Errorf("未收录派系应原样显示：%+v", items[1])
	}
	if items[2].Faction != plugutil.DashText {
		t.Errorf("没有派系时应显示 %q，实际 %q", plugutil.DashText, items[2].Faction)
	}
}

// TestBuildStationsCardTranslatesActions 校验战术行动的译名、图标与状态文案。
func TestBuildStationsCardTranslatesActions(t *testing.T) {
	card := BuildStationsCard(testStations(), testFetchedAt(), false)

	if card.Empty || len(card.Items) != 1 {
		t.Fatalf("空间站数量错误：%+v", card)
	}
	item := card.Items[0]
	if item.Name != stationNameFallback {
		t.Errorf("上游没给名字时应显示 %q，实际 %q", stationNameFallback, item.Name)
	}
	if item.ElectionEnd != "2026-09-16 20:39" {
		t.Errorf("截止时间应按展示时区格式化，实际 %q", item.ElectionEnd)
	}
	// 6 行 = 固定清单的 5 项战术行动（上游这次只报了前 3 项，其余补成「等待募捐启动」）
	// + 上游多出来那项清单外的行动（追加在末尾）。
	if len(item.Actions) != 6 {
		t.Fatalf("应有 6 项战术行动，实际 %d 项：%+v", len(item.Actions), item.Actions)
	}

	want := []struct {
		name, status, icon string
	}{
		{"飞鹰风暴", "募捐中", "tactical.eagle_storm"},
		{"轨道封锁", "已激活", "tactical.orbital_blockade"},
		{"重型军械分发", "冷却中", "tactical.heavy_ordnance"},
		{"飞鹰封锁", "募捐中", "tactical.eagle_storm"},           // 上游没报：占位「等待募捐启动」
		{"星球轰炸", "募捐中", "tactical.planetary_bombardment"}, // 同上
		{"UNKNOWN ACTION", "状态 9", ""},                    // 清单外：原样显示，也不给图标
	}
	for i, w := range want {
		got := item.Actions[i]
		if got.Name != w.name || got.Status != w.status || got.Icon != w.icon {
			t.Errorf("第 %d 项战术行动错误：期望 %+v，实际 %+v", i+1, w, got)
		}
	}
	// 没报到的两项写成参考站的募捐占位文案，不能因为上游没提就整行消失。
	for i, got := range item.Actions[3:5] {
		if got.Detail != waitDonationText || got.PercentText != "0%" {
			t.Errorf("第 %d 项未报到的行动应写成募捐占位，实际 %+v", i+4, got)
		}
	}
}

// TestBuildStationsCardUnnamedAction 校验战术行动没有名字（空串或纯空白）时显示「未命名行动」：
// 上游 name 字段实测可能缺失，不占位的话表格里会出现一行空白。
// 这两项都没有效果编号，匹配不上固定清单，所以排在清单占位之后（末尾两行）。
func TestBuildStationsCardUnnamedAction(t *testing.T) {
	list := []hd2.SpaceStation{{
		ID32: 1,
		TacticalActions: []hd2.TacticalAction{
			{ID32: 11, Name: "", Status: 0},
			{ID32: 12, Name: "   ", Status: 0},
		},
	}}
	card := BuildStationsCard(list, testFetchedAt(), false)
	if len(card.Items) != 1 {
		t.Fatalf("样本数据错误：%+v", card.Items)
	}
	actions := card.Items[0].Actions
	if len(actions) != len(plugutil.TacticalActions())+2 {
		t.Fatalf("应有 %d 项行动（清单占位 + 两项未命名），实际 %d 项", len(plugutil.TacticalActions())+2, len(actions))
	}
	for i, action := range actions[len(actions)-2:] {
		if action.Name != "未命名行动" {
			t.Errorf("第 %d 项未命名行动应显示占位「未命名行动」，实际 %q", i+1, action.Name)
		}
		if action.Icon != "" {
			t.Errorf("第 %d 项未命名行动不该有图标，实际 %q", i+1, action.Icon)
		}
	}
}

// TestBuildStationsCardEmpty 校验空列表生成占位卡数据。
func TestBuildStationsCardEmpty(t *testing.T) {
	card := BuildStationsCard(nil, testFetchedAt(), false)
	if !card.Empty || card.Count != "0" || len(card.Items) != 0 {
		t.Fatalf("空数据占位卡错误：%+v", card)
	}
}

// TestStationCardHTML 校验模板把视图模型排成预期内容。
func TestStationCardHTML(t *testing.T) {
	events, err := render.HTML(render.Card{Name: eventsCardName, Data: BuildEventsCard(testEvents(), testFetchedAt(), false)})
	if err != nil {
		t.Fatalf("渲染事件卡 HTML 失败：%v", err)
	}
	for _, want := range []string{
		"星球事件",                 // 标题
		"2026-09-16 20:00:00",  // 数据时间
		`class="card__emblem"`, // 徽标（游戏原生 SVG）内联进卡片
		"进攻方", "防守剩余",          // 字段标签
		"终结族", "机器人", "99.9%", // 派系与进度（1497963/1500000）
		"2026-09-18 19:02", // 结束时间（UTC 11:02 → 东八区 19:02）
	} {
		if !strings.Contains(events, want) {
			t.Errorf("事件卡 HTML 缺少 %q", want)
		}
	}
	// 用户 2026-09-17 要求删掉的三处：事件类型一行、顶部两个统计、底部说明。
	for _, unwanted := range []string{"事件类型", "本次展示", "进行中事件", "上游 eventType"} {
		if strings.Contains(events, unwanted) {
			t.Errorf("事件卡不该再出现 %q", unwanted)
		}
	}

	stations, err := render.HTML(render.Card{Name: stationsCardName, Data: BuildStationsCard(testStations(), testFetchedAt(), true)})
	if err != nil {
		t.Fatalf("渲染空间站卡 HTML 失败：%v", err)
	}
	for _, want := range []string{
		"民主空间站", "战术行动", "跃迁截止",
		"当前停靠：", "贝克温III", "斯坦分区", // 停靠位置：星球中英名 + 星区中文名
		"常驻被动", "行动支持", // 常驻被动一节
		"飞鹰风暴", "轨道封锁", "重型军械分发", "飞鹰封锁", "星球轰炸", // 固定清单 5 项都画出来
		"募捐中", "已激活", "冷却中", "等待募捐启动", // 各阶段与未报到行动的占位文案
		"数据可能已过期",
		"空间站即将移动：00:39:04",                  // 头栏的跃迁倒计时（时刻差 39 分 04 秒）
		"dss-buff--active", "dss-bar__fill", // 已激活那一块高亮，募捐/冷却按进度画条
	} {
		if !strings.Contains(stations, want) {
			t.Errorf("空间站卡 HTML 缺少 %q", want)
		}
	}
	// 用户 2026-09-17 要求删掉：空间站编号、标记、战术行动的上游编号、底部说明。
	// 用户 2026-09-18 要求不做参考站右侧那块「效果详情」：静态图里点不动左栏，画一张停在
	// 第一个效果的详情只会误导人，所以模板里也不该残留那套版式的 class。
	for _, unwanted := range []string{"空间站编号", "749875195", "标记", "4091660627", "dss-grid", "dss-hero", "效果详情"} {
		if strings.Contains(stations, unwanted) {
			t.Errorf("空间站卡不该再出现 %q", unwanted)
		}
	}
	// 五项固定清单的战术行动各有图标（未报到的两项用同一族的素材兜底）：图标由 asset 函数
	// 内联成 data URI，这里按 img 元素数量断言；末尾那项清单外行动没有图标，不算在内。
	if got := strings.Count(stations, `class="dss-icon"`); got != len(plugutil.TacticalActions()) {
		t.Errorf("应有 %d 个战术行动图标，实际 %d 个", len(plugutil.TacticalActions()), got)
	}
}

// TestStationCardHTMLEscapesUpstreamText 校验上游字段里的标签被转义，不会注入卡片。
func TestStationCardHTMLEscapesUpstreamText(t *testing.T) {
	list := []hd2.PlanetEvent{{
		ID: 1, EventType: 1, Faction: "Terminids", Health: 1, MaxHealth: 2,
		PlanetIndex: 7, PlanetName: `<script>alert(1)</script>`,
	}}
	html, err := render.HTML(render.Card{Name: eventsCardName, Data: BuildEventsCard(list, testFetchedAt(), false)})
	if err != nil {
		t.Fatalf("渲染事件卡 HTML 失败：%v", err)
	}
	if strings.Contains(html, "<script>") {
		t.Error("上游字段里的脚本标签必须被转义")
	}
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Errorf("应保留转义后的星球名：%s", html)
	}
}

// TestBuildEventsCardSameEndTimeOrder 校验两起事件结束时间完全相同时按星球编号升序兜底。
// 上游返回顺序并不保证稳定（实测同一批数据里两起事件可能同秒结束），
// 没有兜底的话群里每次刷新看到的顺序都可能不同，看起来像数据在跳。
func TestBuildEventsCardSameEndTimeOrder(t *testing.T) {
	end := time.Date(2026, 9, 18, 11, 2, 0, 0, time.UTC)
	// 刻意把编号大的排在前面：只靠「保持输入顺序」的实现会给出 200 → 100。
	list := []hd2.PlanetEvent{
		{ID: 2, EventType: 1, Faction: "Terminids", Health: 1, MaxHealth: 2, PlanetIndex: 200, PlanetName: "PEACOCK", EndTime: &end},
		{ID: 1, EventType: 1, Faction: "Automatons", Health: 1, MaxHealth: 2, PlanetIndex: 100, PlanetName: "ACAMAR IV", EndTime: &end},
	}
	items := BuildEventsCard(list, testFetchedAt(), false).Items
	if len(items) != 2 {
		t.Fatalf("应有 2 起事件，实际 %d 起", len(items))
	}
	if items[0].PlanetIndex != "100" || items[1].PlanetIndex != "200" {
		t.Fatalf("结束时间相同时应按星球编号升序兜底，实际 %s → %s",
			items[0].PlanetIndex, items[1].PlanetIndex)
	}
}

// TestBuildStationsCardActionProgress 校验行动列表上的四个细节：募捐进度、阶段百分比、
// 进度条与时间行（已激活看「行动剩余」、冷却中看「冷却剩余」）。
// 口径对齐参考项目 HD2 星图页的 DSS 面板；样本见 testStationsWithProgress。
func TestBuildStationsCardActionProgress(t *testing.T) {
	item := BuildStationsCard(testStationsWithProgress(), testFetchedAt(), false).Items[0]

	if item.JumpCountdown != "00:39:04" {
		t.Errorf("跃迁剩余应为 00:39:04（12:39:04 − 12:00:00），实际 %q", item.JumpCountdown)
	}
	// 上游这次只报了 3 项，卡片仍按固定清单列 5 行（另外两项写「等待募捐启动」）。
	if item.ActionCount != "5" {
		t.Errorf("行动条数应为 5，实际 %q", item.ActionCount)
	}
	// 募捐中：进度条按募捐比例画（40805.23 / 86400 = 47.2%），右侧百分比取整，
	// 没有可倒数的时间——募捐什么时候结束由玩家捐得快慢决定。
	donating := item.Actions[0]
	if donating.Detail != "已捐献 40,805 / 86,400（47.2%）" || donating.PercentText != "47%" {
		t.Errorf("募捐中的行动应给出募捐进度与整数百分比：%+v", donating)
	}
	if !donating.Bar || donating.Active || donating.TimeText != "" {
		t.Errorf("募捐中不该标记高亮，时间行应为空：%+v", donating)
	}
	// 已激活：进度条画满（此时条子表示「生效中」而不是募捐进度），时间行是行动剩余。
	on := item.Actions[1]
	if !on.Active || !on.Bar || on.PercentText != "100%" {
		t.Errorf("已激活的行动应画满进度条并标记高亮：%+v", on)
	}
	// 已激活/冷却中且上游没给成本数据时不写募捐那一行：写「等待募捐启动」会让人以为它又要开始募捐。
	if on.Detail != "" {
		t.Errorf("已激活且没有成本数据时不该写募捐进度，实际 %q", on.Detail)
	}
	if on.TimeText != "行动剩余 01:02:33" {
		t.Errorf("已激活的时间行应是行动剩余，实际 %q", on.TimeText)
	}
	// 冷却中：时间行是冷却剩余。
	off := item.Actions[2]
	if off.TimeText != "冷却剩余 05:12:30" {
		t.Errorf("冷却中的时间行应是冷却剩余，实际 %q", off.TimeText)
	}
	if off.Detail != "" {
		t.Errorf("冷却中且没有成本数据时不该写募捐进度，实际 %q", off.Detail)
	}
	// 上游这次没报的两项照样列出来，写成参考站的募捐占位文案。
	for i, action := range item.Actions[3:] {
		if action.Status != "募捐中" || action.Detail != waitDonationText || action.PercentText != "0%" {
			t.Errorf("第 %d 项未报到的行动应写成募捐占位，实际 %+v", i+4, action)
		}
	}
}

// TestBuildStationsCardExpiredActionSilent 校验已过期的时刻不显示倒计时：
// 显示「剩余 00:00:00」会被读成「刚刚结束」，而真实原因往往是数据没更新。
// 样本带上效果编号，行动才会落在清单第 2 行（轨道封锁），不会被补到清单外的末尾。
func TestBuildStationsCardExpiredActionSilent(t *testing.T) {
	past := testFetchedAt().Add(-time.Minute)
	list := []hd2.SpaceStation{{TacticalActions: []hd2.TacticalAction{
		{ID32: 2, Name: "ORBITAL BLOCKADE", Status: 2, EffectIDs: []int{1210, 1213, 1215}, StatusExpire: &past},
	}}}
	actions := BuildStationsCard(list, testFetchedAt(), false).Items[0].Actions
	if actions[1].Name != "轨道封锁" {
		t.Fatalf("样本数据错误：第 2 行应是轨道封锁，实际 %+v", actions[1])
	}
	if got := actions[1].TimeText; got != "" {
		t.Errorf("时刻已过时应不显示时间行，实际 %q", got)
	}
}
