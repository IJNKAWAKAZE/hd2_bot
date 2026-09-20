package push

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"hd2_bot/src/hd2"
)

// TestDetectNoBaselineReturnsNothing 校验首次运行（没有基线）时不产生事件：只写基线，不重播现状。
func TestDetectNoBaselineReturnsNothing(t *testing.T) {
	if got := Detect(Snapshot{}, testInput()); len(got) != 0 {
		t.Fatalf("没有基线时不应产生事件，实际 %v", got)
	}
}

// TestDetectNoChangeReturnsNothing 校验状态没变时一条事件都不产生。
func TestDetectNoChangeReturnsNothing(t *testing.T) {
	in := testInput()
	prev := SnapshotOf(in)
	if got := Detect(prev, in); len(got) != 0 {
		t.Fatalf("没有变化时不应产生事件，实际 %v", got)
	}
}

// TestDetectNewDispatch 校验新简报被检测出来，并抽出 <i=3> 高亮片段作为标题。
func TestDetectNewDispatch(t *testing.T) {
	prev := SnapshotOf(testInput())
	in := testInput()
	in.Dispatches = append([]hd2.Dispatch{{
		ID:      1049,
		Message: `<i=3>MAJOR ORDER WON</i> The Helldivers rapidly decomissioned over <i=1>200,000</i> Terminids.`,
	}}, in.Dispatches...)

	got := Detect(prev, in)
	if len(got) != 1 {
		t.Fatalf("应检测到 1 条事件，实际 %d：%v", len(got), got)
	}
	if got[0].Kind != KindDispatch || got[0].ID != "1049" {
		t.Fatalf("事件类型或 id 错误：%+v", got[0])
	}
	if got[0].Title != "MAJOR ORDER WON" {
		t.Fatalf("标题应取高亮片段，实际 %q", got[0].Title)
	}
	if strings.Contains(got[0].Body, "<i=") {
		t.Fatalf("正文里的游戏内标记应被清掉，实际 %q", got[0].Body)
	}
}

// TestDetectDispatchTitleFallback 校验没有高亮标记时用第一句话当标题，且长度受限。
func TestDetectDispatchTitleFallback(t *testing.T) {
	prev := SnapshotOf(testInput())
	in := testInput()
	in.Dispatches = append([]hd2.Dispatch{{ID: 1049, Message: "The war continues on every front."}}, in.Dispatches...)
	got := Detect(prev, in)
	if len(got) != 1 {
		t.Fatalf("应检测到 1 条事件，实际 %d", len(got))
	}
	if got[0].Title != "The war continues on every front." {
		t.Fatalf("标题应退回第一句话，实际 %q", got[0].Title)
	}
}

// TestDetectOwnerChange 校验星球易主：失守用「失守」、被解放用「解放」，说明里给新旧控制方。
func TestDetectOwnerChange(t *testing.T) {
	prev := SnapshotOf(testInput())
	in := testInput()
	in.Planets[0].CurrentOwner = "Terminids" // 7 号星球被终结族打下来了

	got := Detect(prev, in)
	if len(got) != 1 || got[0].Kind != KindOwner {
		t.Fatalf("应检测到 1 条易主事件，实际 %+v", got)
	}
	if !strings.Contains(got[0].Title, "失守") {
		t.Fatalf("落入敌手应写「失守」，实际 %q", got[0].Title)
	}
	if !strings.Contains(got[0].Title, "天园六IV") {
		t.Fatalf("标题应带中文译名，实际 %q", got[0].Title)
	}
	if !strings.Contains(got[0].Detail, "超级地球") || !strings.Contains(got[0].Detail, "终结族") {
		t.Fatalf("说明应给出新旧控制方，实际 %q", got[0].Detail)
	}
}

// TestDetectOwnerLiberated 校验被解放时写「解放」。
func TestDetectOwnerLiberated(t *testing.T) {
	in := testInput()
	in.Planets[1].CurrentOwner = "Humans"
	prev := SnapshotOf(in)

	// 先让基线是「敌方控制」，再改成「我方控制」
	prev.Owners["9"] = "Terminids"
	got := Detect(prev, in)
	if len(got) != 1 || !strings.Contains(got[0].Title, "解放") {
		t.Fatalf("回归超级地球应写「解放」，实际 %+v", got)
	}
}

// TestDetectCampaignStartAndEnd 校验战役开始与结束都被检测到，且带类型说明。
func TestDetectCampaignStartAndEnd(t *testing.T) {
	prev := SnapshotOf(testInput())

	// 开始：多了一场 type=4 的防守战
	in := testInput()
	in.Campaigns = append(in.Campaigns, hd2.Campaign{
		ID: 101, Planet: hd2.PlanetRef{Index: 7, Name: "Acamar IV"}, Type: 4,
	})
	got := Detect(prev, in)
	if len(got) != 1 || got[0].Kind != KindCampaign {
		t.Fatalf("应检测到 1 条战役事件，实际 %+v", got)
	}
	if !strings.Contains(got[0].Title, "新战役") || !strings.Contains(got[0].Detail, "防守战") {
		t.Fatalf("开始事件文案错误：%+v", got[0])
	}

	// 结束：把基线改成「有这场战役」，本轮没有
	ended := Detect(SnapshotOf(in), testInput())
	if len(ended) != 1 || ended[0].Kind != KindCampaign {
		t.Fatalf("应检测到 1 条战役结束事件，实际 %+v", ended)
	}
	if !strings.Contains(ended[0].Title, "战役结束") {
		t.Fatalf("结束事件文案错误：%+v", ended[0])
	}
	if ended[0].ID == got[0].ID {
		t.Fatal("同一场战役的开始与结束必须用不同的去重键，否则结束事件会被判成重复")
	}
}

// TestDetectCampaignStartDedupedWithOwnerChange 校验同一颗星球同时易主与开战时只报易主：
// 一条消息里对同一件事说两遍只会让人以为出了两个状况。
func TestDetectCampaignStartDedupedWithOwnerChange(t *testing.T) {
	prev := SnapshotOf(testInput())
	in := testInput()
	in.Planets[0].CurrentOwner = "Automatons"
	in.Campaigns = append(in.Campaigns, hd2.Campaign{
		ID: 102, Planet: hd2.PlanetRef{Index: 7, Name: "Acamar IV"}, Type: 0,
	})
	got := Detect(prev, in)
	if len(got) != 1 || got[0].Kind != KindOwner {
		t.Fatalf("同一星球的易主与开战应只保留易主，实际 %+v", got)
	}
}

// TestDetectStationChanges 校验空间站签名变化（flags / 战术行动状态）被检测出来并说清变化。
func TestDetectStationChanges(t *testing.T) {
	prev := SnapshotOf(testInput())
	in := testInput()
	in.Stations[0].Flags = 2
	in.Stations[0].TacticalActions[0].Status = 3 // 飞鹰风暴：已激活 → 冷却中

	got := Detect(prev, in)
	if len(got) != 1 || got[0].Kind != KindStation {
		t.Fatalf("应检测到 1 条空间站事件，实际 %+v", got)
	}
	if !strings.Contains(got[0].Detail, "飞鹰风暴") {
		t.Fatalf("应带战术行动的中文译名，实际 %q", got[0].Detail)
	}
	if !strings.Contains(got[0].Detail, "已激活") || !strings.Contains(got[0].Detail, "冷却中") {
		t.Fatalf("应说明状态变化前后，实际 %q", got[0].Detail)
	}
	if !strings.Contains(got[0].Detail, "1 → 2") {
		t.Fatalf("应说明 flags 变化，实际 %q", got[0].Detail)
	}
}

// TestDetectSkipsUnknownStations 校验本轮没有出现的空间站不产生事件（上游下线空间站属于已知情况）。
func TestDetectSkipsUnknownStations(t *testing.T) {
	prev := SnapshotOf(testInput())
	in := testInput()
	in.Stations = nil
	if got := Detect(prev, in); len(got) != 0 {
		t.Fatalf("空间站消失不应产生事件，实际 %+v", got)
	}
}

// TestStationSignatureRoundTrip 校验签名的格式化与解析是互逆的。
func TestStationSignatureRoundTrip(t *testing.T) {
	sig := stationSignature(hd2.SpaceStation{
		Flags: 1,
		TacticalActions: []hd2.TacticalAction{
			{Name: "ORBITAL BLOCKADE", Status: 3},
			{Name: "EAGLE STORM", Status: 2},
		},
	})
	parsed, ok := parseStationSignature(sig)
	if !ok {
		t.Fatalf("签名应能解析：%q", sig)
	}
	if parsed.Flags != "1" || len(parsed.Actions) != 2 {
		t.Fatalf("解析结果错误：%+v", parsed)
	}
	if parsed.Actions["EAGLE STORM"] != "2" {
		t.Fatalf("行动状态未记录：%+v", parsed.Actions)
	}
	// 行动顺序不影响签名（否则同样的状态会产生两种签名，每轮都误报）
	sig2 := stationSignature(hd2.SpaceStation{
		Flags: 1,
		TacticalActions: []hd2.TacticalAction{
			{Name: "EAGLE STORM", Status: 2},
			{Name: "ORBITAL BLOCKADE", Status: 3},
		},
	})
	if sig != sig2 {
		t.Fatalf("行动顺序不应影响签名：%q vs %q", sig, sig2)
	}
	if _, ok := parseStationSignature("结构不对"); ok {
		t.Fatal("非法签名应解析失败")
	}
}

// TestDetectDirtyPlanetNameUsesIndex 校验上游没给星球名（脏数据）时标题退回「星球 <编号>」：
// 少了这层兜底，标题会变成「新战役：」这种看不出说的是哪颗星球的半句话。
func TestDetectDirtyPlanetNameUsesIndex(t *testing.T) {
	prev := SnapshotOf(testInput())
	in := testInput()
	in.Planets = append(in.Planets, hd2.Planet{Index: 42, Name: "  "})
	in.Campaigns = append(in.Campaigns, hd2.Campaign{
		ID: 103, Planet: hd2.PlanetRef{Index: 42}, Type: 4,
	})
	got := Detect(prev, in)
	if len(got) != 1 || got[0].Kind != KindCampaign {
		t.Fatalf("应检测到 1 条战役事件，实际 %+v", got)
	}
	if !strings.Contains(got[0].Title, "星球 42") {
		t.Fatalf("星球名缺失时标题应退回编号，实际 %q", got[0].Title)
	}
}

// TestDetectNewPlanetNotReported 校验基线里没有的星球编号不算易主：上游新增星球或回填历史数据时
// 报成「易主」只会刷屏（口径与「新增星球不算变化」一致）。
func TestDetectNewPlanetNotReported(t *testing.T) {
	prev := SnapshotOf(testInput())
	in := testInput()
	in.Planets = append(in.Planets, hd2.Planet{Index: 42, Name: "New World", CurrentOwner: "Automatons"})
	if got := Detect(prev, in); len(got) != 0 {
		t.Fatalf("新增星球不应产生事件，实际 %+v", got)
	}
}

// TestDetectStationActionsAddedAndRemoved 校验战术行动的增删都会被说出来：
// 只说「状态变了」而漏掉「少了一项」，看消息的人会以为空间站还是老样子。
func TestDetectStationActionsAddedAndRemoved(t *testing.T) {
	prev := SnapshotOf(testInput())
	in := testInput()
	in.Stations[0].TacticalActions = []hd2.TacticalAction{
		{Name: "ORBITAL BLOCKADE", Status: 3},            // 没变
		{Name: "HEAVY ORDNANCE DISTRIBUTION", Status: 1}, // 新增
	}
	got := Detect(prev, in)
	if len(got) != 1 || got[0].Kind != KindStation {
		t.Fatalf("应检测到 1 条空间站事件，实际 %+v", got)
	}
	if !strings.Contains(got[0].Detail, "新增战术行动『重型军械分发』：募捐中") {
		t.Fatalf("应说明新增的战术行动，实际 %q", got[0].Detail)
	}
	if !strings.Contains(got[0].Detail, "战术行动『飞鹰风暴』已结束") {
		t.Fatalf("应说明消失的战术行动，实际 %q", got[0].Detail)
	}
	if strings.Contains(got[0].Detail, "轨道封锁") {
		t.Fatalf("没变化的行动不该出现在差异里，实际 %q", got[0].Detail)
	}
}

// TestDetectDispatchTitleTruncated 校验没有高亮标记时标题只取第一行并按 dispatchTitleRunes 截断。
// 首行刻意造到远超预算：预算已抬到「装下真实上游内容」（实测最长首行 385 rune），
// 只有异常长的首行才会被压到上限。
func TestDetectDispatchTitleTruncated(t *testing.T) {
	prev := SnapshotOf(testInput())
	in := testInput()
	first := strings.Repeat("The Helldivers pushed the Automatons off the planet. ", dispatchTitleRunes)
	in.Dispatches = append([]hd2.Dispatch{{
		ID:      1050,
		Message: first + "\nSecond line must not be part of the title.",
	}}, in.Dispatches...)
	got := Detect(prev, in)
	if len(got) != 1 {
		t.Fatalf("应检测到 1 条事件，实际 %d", len(got))
	}
	title := got[0].Title
	if strings.Contains(title, "Second line") {
		t.Fatalf("标题不应包含第二行，实际 %q", title)
	}
	want := dispatchTitleRunes + 1 // 截断到上限后还有一个省略号
	if !strings.HasSuffix(title, "…") || len([]rune(title)) != want {
		t.Fatalf("超长标题应截断成 %d 个字符加省略号，实际 %q（%d 字符）", dispatchTitleRunes, title, len([]rune(title)))
	}
}

// TestDetectDispatchTitleKeepsRealUpstreamFirstLine 校验真实上游最长首行不被截断。
// 385 rune 这个数字来自 2026-09-17 的真实上游统计（1048 条简报里清洗后最长的首行）；
// 把 dispatchTitleRunes 改回旧值（40）会让本用例变红。
func TestDetectDispatchTitleKeepsRealUpstreamFirstLine(t *testing.T) {
	first := strings.Repeat("a", realUpstreamMaxTitleRunes)
	prev := SnapshotOf(testInput())
	in := testInput()
	in.Dispatches = append([]hd2.Dispatch{{ID: 1051, Message: first + "\nSecond line must not be part of the title."}}, in.Dispatches...)

	got := Detect(prev, in)
	if len(got) != 1 {
		t.Fatalf("应检测到 1 条事件，实际 %d", len(got))
	}
	if got[0].Title != first {
		t.Fatalf("实测最长首行（%d rune）应原样当标题，实际 %q（%d 字符）",
			realUpstreamMaxTitleRunes, got[0].Title, len([]rune(got[0].Title)))
	}
}

// TestDetectCampaignTypeUnknownShowsNumber 校验未公布过的战役类型原样显示数字，不猜成既有类型。
func TestDetectCampaignTypeUnknownShowsNumber(t *testing.T) {
	prev := SnapshotOf(testInput())
	in := testInput()
	in.Campaigns = append(in.Campaigns, hd2.Campaign{
		ID: 104, Planet: hd2.PlanetRef{Index: 9, Name: "Turing"}, Type: 7,
	})
	got := Detect(prev, in)
	if len(got) != 1 || got[0].Kind != KindCampaign {
		t.Fatalf("应检测到 1 条战役事件，实际 %+v", got)
	}
	if !strings.Contains(got[0].Detail, "战役类型 7") {
		t.Fatalf("未知战役类型应原样显示数字，实际 %+v", got[0])
	}
}

// TestDetectStationPlanetChange 校验空间站换位置也会被检测出来，且上游用 0 表示「没有位置信息」。
// 位置写星球名而不是编号：卡面上一行「星球 216 → 星球 268」在群里没人能对上号。
func TestDetectStationPlanetChange(t *testing.T) {
	prev := SnapshotOf(testInput())
	in := testInput()
	in.Stations[0].PlanetIndex = 9
	got := Detect(prev, in)
	if len(got) != 1 || got[0].Kind != KindStation {
		t.Fatalf("应检测到 1 条空间站事件，实际 %+v", got)
	}
	if !strings.Contains(got[0].Detail, "位置 未知 → 图灵（Turing）") {
		t.Fatalf("应说明位置变化并把 0 写成「未知」、把编号写成星球名，实际 %q", got[0].Detail)
	}
}

// TestDetectStationPlanetChangeUnknownName 校验星球名查不到时退回「星球 N」：
// 上游偶尔给一颗不在列表里的编号（或列表本身缺了它），这时宁可显示编号也不编名字。
func TestDetectStationPlanetChangeUnknownName(t *testing.T) {
	prev := SnapshotOf(testInput())
	in := testInput()
	in.Stations[0].PlanetIndex = 424242
	got := Detect(prev, in)
	if len(got) != 1 || got[0].Kind != KindStation {
		t.Fatalf("应检测到 1 条空间站事件，实际 %+v", got)
	}
	if !strings.Contains(got[0].Detail, "位置 未知 → 星球 424242") {
		t.Fatalf("查不到星球名时应退回「星球 N」，实际 %q", got[0].Detail)
	}
}

// TestDetectDispatchWithoutText 校验简报正文为空（上游脏数据）时标题给「新简报」，不产生空标题。
func TestDetectDispatchWithoutText(t *testing.T) {
	prev := SnapshotOf(testInput())
	in := testInput()
	in.Dispatches = append([]hd2.Dispatch{{ID: 1051, Message: "   "}}, in.Dispatches...)
	got := Detect(prev, in)
	if len(got) != 1 || got[0].Kind != KindDispatch {
		t.Fatalf("应检测到 1 条简报事件，实际 %+v", got)
	}
	if got[0].Title != "新简报" {
		t.Fatalf("正文为空时标题应为「新简报」，实际 %q", got[0].Title)
	}
}

// TestDetectToleratesBrokenBaseline 校验基线里存着旧格式或坏数据时不 panic、不误报：
// 认不出来的战役指纹跳过（不编事件），认不出来的空间站签名退化成通用文案。
func TestDetectToleratesBrokenBaseline(t *testing.T) {
	prev := Snapshot{
		FetchedAt:   fixedAt,
		Owners:      map[string]string{"7": "Humans", "9": "Terminids"},
		PlanetNames: map[string]string{"7": "Acamar IV", "9": "Turing"},
		Campaigns:   map[string]string{"100": "9|0", "999": "不是指纹"},
		Stations:    map[string]string{"749875195": "结构不对"},
		DispatchIDs: []int64{1048, 1047},
	}
	in := testInput()
	in.Planets[0].CurrentOwner = "Terminids" // 唯一确定的变化

	got := Detect(prev, in)
	if len(got) != 2 {
		t.Fatalf("应只产生易主与空间站两条事件，实际 %+v", got)
	}
	var station *Event
	for i := range got {
		switch got[i].Kind {
		case KindCampaign:
			t.Fatalf("认不出来的战役指纹应跳过而不是编一条事件：%+v", got[i])
		case KindStation:
			station = &got[i]
		}
	}
	if station == nil {
		t.Fatalf("空间站签名认不出来时仍应报「有变化」，实际 %+v", got)
	}
	if !strings.Contains(station.Detail, "无法逐项对比空间站签名") {
		t.Fatalf("签名解析失败时应退化成中性文案，实际 %q", station.Detail)
	}
}

// TestDetectNewStationNotReported 校验基线里没有的空间站编号不算变化（与「新增星球不算易主」同口径）：
// 上线与下线都由上游决定，把「首次见到」报成「状态变更」会在上游补数据时刷屏。
func TestDetectNewStationNotReported(t *testing.T) {
	in := testInput()
	in.Stations = nil
	prev := SnapshotOf(in) // 基线里一座空间站都没有
	in.Stations = testInput().Stations
	if got := Detect(prev, in); len(got) != 0 {
		t.Fatalf("首次出现的空间站不应产生事件，实际 %+v", got)
	}
}

// liveDispatches 造一份形如真上游的简报列表：条数与发布节奏对齐实测（1048 条、数小时到数天一条）。
func liveDispatches(n int, base time.Time) []hd2.Dispatch {
	list := make([]hd2.Dispatch, 0, n)
	for i := 0; i < n; i++ {
		list = append(list, hd2.Dispatch{
			ID:        int64(5000 - i),
			Published: base.Add(-time.Duration(i) * 6 * time.Hour),
			Type:      0,
			Message:   "<i=3>MAJOR ORDER WON</i>\n\nReport " + strconv.Itoa(i) + ": the front has moved.",
		})
	}
	return list
}

// TestDetectDispatchReplayKeepsQuiet 是评审用真上游数据回灌查出的缺陷的回归用例：
// 基线只留最近 MaxDispatchIDs 条 id，若判新只看 id 集合，同一份 1048 条数据回灌会报出 848 条假事件。
// 这里连「第二轮把列表顺序打乱」一起测（上游返回顺序不保证稳定）。
func TestDetectDispatchReplayKeepsQuiet(t *testing.T) {
	first := testInput()
	first.Dispatches = liveDispatches(1048, fixedAt)

	if got := Detect(Snapshot{}, first); len(got) != 0 {
		t.Fatalf("首轮（没有基线）不应发事件，实际 %d 条", len(got))
	}
	prev := SnapshotOf(first) // 第一轮：只写基线

	second := first
	second.Dispatches = make([]hd2.Dispatch, len(first.Dispatches))
	for i, d := range first.Dispatches {
		second.Dispatches[len(first.Dispatches)-1-i] = d
	}
	if got := Detect(prev, second); len(got) != 0 {
		t.Fatalf("同一份数据回灌不应产生事件，实际 %d 条（首条 %+v）", len(got), got[0])
	}
}

// TestDetectDispatchNewerPublishedOrNewID 校验三道判新口径：
// 发布时间更新 ⇒ 报；与基线最新时间相同但 id 是新的（同一秒连发）⇒ 报；只改正文 ⇒ 不报。
func TestDetectDispatchNewerPublishedOrNewID(t *testing.T) {
	base := testInput()
	base.Dispatches = liveDispatches(300, fixedAt)
	prev := SnapshotOf(base)

	fresh := base
	fresh.Dispatches = append([]hd2.Dispatch{{
		ID: 6000, Published: fixedAt.Add(2 * time.Hour), Message: "<i=3>NEW ORDER</i>\n\nDeploy now.",
	}}, base.Dispatches...)
	if got := Detect(prev, fresh); len(got) != 1 || got[0].ID != "6000" {
		t.Fatalf("发布时间更新的新简报应报 1 条，实际 %+v", got)
	}

	sameStamp := base
	sameStamp.Dispatches = append([]hd2.Dispatch{{
		ID: 6001, Published: fixedAt, Message: "<i=3>SAME SECOND</i>\n\nAnother order.",
	}}, base.Dispatches...)
	if got := Detect(prev, sameStamp); len(got) != 1 || got[0].ID != "6001" {
		t.Fatalf("同一时间戳的新 id 应报 1 条，实际 %+v", got)
	}

	edited := base
	edited.Dispatches = append([]hd2.Dispatch(nil), base.Dispatches...)
	edited.Dispatches[0].Message = "<i=3>MAJOR ORDER WON</i>\n\nThe text was edited upstream."
	if got := Detect(prev, edited); len(got) != 0 {
		t.Fatalf("只改正文不应报事件，实际 %+v", got)
	}
}

// TestDetectZeroBaselineTimeFallsBackToIDSet 钉住「基线没有发布时间」时的退化口径：
// 完全按 id 集合判新——已在集合里的不报；新 id 一律报，哪怕它的发布时间看起来更旧。
func TestDetectZeroBaselineTimeFallsBackToIDSet(t *testing.T) {
	prev := Snapshot{
		FetchedAt:   fixedAt,
		Owners:      map[string]string{"7": "Humans", "9": "Terminids"},
		PlanetNames: map[string]string{"7": "Acamar IV", "9": "Turing"},
		Campaigns:   map[string]string{"100": "9|0"},
		Stations:    map[string]string{},
		DispatchIDs: []int64{1048, 1047},
	}
	if got := Detect(prev, testInput()); len(got) != 0 {
		t.Fatalf("id 已在集合里时不应报事件，实际 %+v", got)
	}
	older := testInput()
	older.Dispatches = append([]hd2.Dispatch{{
		ID: 900, Published: fixedAt.Add(-240 * time.Hour), Message: "An old dispatch.",
	}}, older.Dispatches...)
	if got := Detect(prev, older); len(got) != 1 || got[0].ID != "900" {
		t.Fatalf("退化口径下新 id 一律报（不看时间），实际 %+v", got)
	}
}

// permanentDedup 模拟 state.MarkPushed 的永久去重表：键是 kind + ":" + id，命中过就永久返回 false。
// 在 push 包里重写一遍是为了不让纯函数包反过来依赖 state，键的拼法与 state 保持一致。
type permanentDedup map[string]bool

func (d permanentDedup) mark(kind Kind, id string) bool {
	key := string(kind) + ":" + id
	if d[key] {
		return false
	}
	d[key] = true
	return true
}

// TestDetectOwnerIDsDifferBetweenChanges 校验同一颗星球第二次易主不会被永久去重表吃掉：
// 去重键必须把「从谁变到谁」编进去（state 的去重表是永久表，只用星球编号会静默丢弃第二次变化）。
func TestDetectOwnerIDsDifferBetweenChanges(t *testing.T) {
	prev := SnapshotOf(testInput())
	in := testInput()
	in.Planets[0].CurrentOwner = "Terminids"
	first := Detect(prev, in)
	if len(first) != 1 || first[0].Kind != KindOwner {
		t.Fatalf("应检测到 1 条易主事件，实际 %+v", first)
	}
	if first[0].ID != "7#Humans->Terminids" {
		t.Fatalf("易主去重键应形如 <编号>#<旧>-><新>，实际 %q", first[0].ID)
	}

	back := testInput()
	back.Planets[0].CurrentOwner = "Terminids"
	prevBack := SnapshotOf(back)
	second := Detect(prevBack, testInput()) // 7 号星球 Terminids → Humans
	if len(second) != 1 || second[0].Kind != KindOwner {
		t.Fatalf("应检测到 1 条易主事件，实际 %+v", second)
	}
	if second[0].ID != "7#Terminids->Humans" {
		t.Fatalf("反向变化的去重键应与正向不同，实际 %q", second[0].ID)
	}
	if first[0].ID == second[0].ID {
		t.Fatalf("两次易主的去重键不能相同：%q", first[0].ID)
	}

	dedup := permanentDedup{}
	if !dedup.mark(first[0].Kind, first[0].ID) {
		t.Fatalf("第一条易主被永久去重表判成重复：%q", first[0].ID)
	}
	if !dedup.mark(second[0].Kind, second[0].ID) {
		t.Fatalf("同一星球的第二次易主被永久去重表判成重复（会被静默丢弃）：%q", second[0].ID)
	}
}

// TestDetectStationIDsDifferBetweenChanges 校验空间站第二次变化不会被永久去重表吃掉：
// 去重键带签名短哈希，且方向不同的两次变化（1→2 / 2→1）必须互不相同。
func TestDetectStationIDsDifferBetweenChanges(t *testing.T) {
	up := testInput()
	up.Stations[0].Flags = 2
	first := Detect(SnapshotOf(testInput()), up)
	if len(first) != 1 || first[0].Kind != KindStation {
		t.Fatalf("应检测到 1 条空间站事件，实际 %+v", first)
	}
	second := Detect(SnapshotOf(up), testInput()) // 基线 flags=2，本轮回到 1
	if len(second) != 1 || second[0].Kind != KindStation {
		t.Fatalf("应检测到 1 条空间站事件，实际 %+v", second)
	}
	if first[0].ID == second[0].ID {
		t.Fatalf("方向不同的两次空间站变化必须有不同去重键，实际都是 %q", first[0].ID)
	}
	if !strings.HasPrefix(first[0].ID, "749875195#") || len(first[0].ID) != len("749875195#")+8 {
		t.Fatalf("空间站去重键应形如 <id32>#<8 位短哈希>，实际 %q", first[0].ID)
	}
	dedup := permanentDedup{}
	if !dedup.mark(first[0].Kind, first[0].ID) || !dedup.mark(second[0].Kind, second[0].ID) {
		t.Fatalf("两次空间站变化都应通过永久去重表：%q / %q", first[0].ID, second[0].ID)
	}
}

// TestEventIDsHaveNoColon 校验所有事件的去重键都不含冒号（硬约束）：
// 编排层的键是 kind + ":" + id，id 里再冒出一个冒号就会把边界搅乱，上游脏数据也不例外。
func TestEventIDsHaveNoColon(t *testing.T) {
	prev := SnapshotOf(testInput())
	in := testInput()
	in.Planets[0].CurrentOwner = "Automatons: 5th Corps" // 脏控制方，故意带冒号
	in.Campaigns = append(in.Campaigns,
		hd2.Campaign{ID: 101, Planet: hd2.PlanetRef{Index: 9, Name: "Turing"}, Type: 4},
		hd2.Campaign{ID: 102, Planet: hd2.PlanetRef{Index: 9, Name: "Turing"}, Type: 0},
	)
	in.Stations[0].Flags = 2
	in.Dispatches = append([]hd2.Dispatch{{
		ID: 7777, Published: fixedAt.Add(time.Hour), Message: "<i=3>X</i>\n\nBody.",
	}}, in.Dispatches...)

	got := Detect(prev, in)
	if len(got) != 5 {
		t.Fatalf("四类事件应各出现一次（战役两次），实际 %d 条：%+v", len(got), got)
	}
	for _, e := range got {
		if strings.Contains(e.ID, ":") {
			t.Fatalf("去重键不能含冒号：%q（%s）", e.ID, e.Kind)
		}
	}
}

// TestDetectCampaignEndUsesBaselinePlanetName 校验战役结束时本轮拿不到星球名，也能用上一轮快照里的名字
// 显示中文译名（否则会退化成「星球 7」，看消息的人不知道是哪颗星球）。
func TestDetectCampaignEndUsesBaselinePlanetName(t *testing.T) {
	withCampaign := testInput()
	withCampaign.Campaigns = append(withCampaign.Campaigns, hd2.Campaign{
		ID: 101, Planet: hd2.PlanetRef{Index: 7, Name: "Acamar IV"}, Type: 0,
	})
	prev := SnapshotOf(withCampaign)

	cur := testInput()
	cur.Planets = []hd2.Planet{{Index: 9, Name: "Turing", CurrentOwner: "Terminids"}} // 本轮缺 7 号星球
	got := Detect(prev, cur)
	if len(got) != 1 || got[0].Kind != KindCampaign {
		t.Fatalf("应检测到 1 条战役结束事件，实际 %+v", got)
	}
	if !strings.Contains(got[0].Title, "天园六IV") {
		t.Fatalf("星球名应退回上一轮快照并显示中文译名，实际 %q", got[0].Title)
	}
}

// TestDetectCampaignEndDedupedWithOwnerChange 校验同一颗星球同时易主与战役结束时只报易主
// （与「开始」侧的去重是两条独立分支，各要一条用例）。
func TestDetectCampaignEndDedupedWithOwnerChange(t *testing.T) {
	withCampaign := testInput()
	withCampaign.Campaigns = append(withCampaign.Campaigns, hd2.Campaign{
		ID: 101, Planet: hd2.PlanetRef{Index: 7, Name: "Acamar IV"}, Type: 4,
	})
	prev := SnapshotOf(withCampaign)

	cur := testInput()
	cur.Planets[0].CurrentOwner = "Automatons" // 7 号星球易主，同时 101 战役结束
	got := Detect(prev, cur)
	if len(got) != 1 || got[0].Kind != KindOwner {
		t.Fatalf("同星球的易主与战役结束应只保留易主，实际 %+v", got)
	}
}

// TestDetectCampaignStartLiberation 校验 type=0 的文案是「解放战」（改成「防守战」必须在这里变红）。
func TestDetectCampaignStartLiberation(t *testing.T) {
	prev := SnapshotOf(testInput())
	in := testInput()
	in.Campaigns = append(in.Campaigns, hd2.Campaign{
		ID: 105, Planet: hd2.PlanetRef{Index: 9, Name: "Turing"}, Type: 0,
	})
	got := Detect(prev, in)
	if len(got) != 1 || got[0].Kind != KindCampaign {
		t.Fatalf("应检测到 1 条战役事件，实际 %+v", got)
	}
	if got[0].Detail != "解放战" {
		t.Fatalf("type=0 应写「解放战」，实际 %q", got[0].Detail)
	}
}

// TestDetectFreshDispatchOrder 校验多条新简报按「Published 倒序、时间相同再按 id 倒序」输出，
// 与快照里的排序口径一致（顺序反了会让卡片上最新的一条排在最后）。
// 顺带钉住「基线只留 200 条 id」时，多出来的那 20 条旧简报会被时间挡掉、不会挤进事件里。
func TestDetectFreshDispatchOrder(t *testing.T) {
	base := testInput()
	base.Dispatches = liveDispatches(220, fixedAt)
	prev := SnapshotOf(base)

	in := testInput()
	older := hd2.Dispatch{ID: 7001, Published: fixedAt.Add(time.Hour), Message: "<i=3>OLDER</i>\n\nBody."}
	newer := hd2.Dispatch{ID: 7002, Published: fixedAt.Add(2 * time.Hour), Message: "<i=3>NEWER</i>\n\nBody."}
	// 同一发布时间的两条：id 小的先给出，靠「时间相同按 id 倒序」的兜底排序拨正。
	tieLow := hd2.Dispatch{ID: 7003, Published: fixedAt.Add(3 * time.Hour), Message: "<i=3>TIE-LOW</i>\n\nBody."}
	tieHigh := hd2.Dispatch{ID: 7004, Published: fixedAt.Add(3 * time.Hour), Message: "<i=3>TIE-HIGH</i>\n\nBody."}
	in.Dispatches = append(append([]hd2.Dispatch{}, base.Dispatches...), older, newer, tieLow, tieHigh)

	got := Detect(prev, in)
	if len(got) != 4 {
		t.Fatalf("应只检测到 4 条新简报（旧的 20 条已被基线时间挡掉），实际 %d 条", len(got))
	}
	want := []string{"7004", "7003", "7002", "7001"}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("应按「发布时间倒序、同一时间按 id 倒序」输出，期望 %v，实际首条 %s（全部 %+v）", want, got[i].ID, got)
		}
	}
}

// TestDetectDispatchBodyDropsRepeatedTitleLine 校验正文里与标题重复的首行会被剥掉。
//
// 上游简报正文的形态就是「高亮标题独占一行 + 正文另起一行」，清洗后首行与高亮标题一字不差；
// 不剥掉的话卡片上会出现「· 新的重大命令 ｜ 新的重大命令 有人发现了一个更加令人发指的赛博格阴谋：…」
// ——标题说两遍，同一段文字还会被送进翻译层两次（白烧一次额度）。
func TestDetectDispatchBodyDropsRepeatedTitleLine(t *testing.T) {
	prev := SnapshotOf(testInput())
	in := testInput()
	in.Dispatches = append([]hd2.Dispatch{{
		ID: 1049,
		Message: "<i=3>NEW MAJOR ORDER</i>\n" +
			"Someone found a more egregious Cyborg plot: they are going to do something terrible.",
	}}, in.Dispatches...)

	got := Detect(prev, in)
	if len(got) != 1 {
		t.Fatalf("应检测到 1 条事件，实际 %d：%v", len(got), got)
	}
	if got[0].Title != "NEW MAJOR ORDER" {
		t.Fatalf("标题仍应取高亮片段，实际 %q", got[0].Title)
	}
	if strings.Contains(got[0].Body, "NEW MAJOR ORDER") {
		t.Fatalf("与标题重复的首行应从正文里剥掉，实际 %q", got[0].Body)
	}
	if !strings.HasPrefix(got[0].Body, "Someone found") {
		t.Fatalf("剥掉首行后正文应从第二行开始，实际 %q", got[0].Body)
	}
}

// TestDetectDispatchBodyKeepsUnrelatedFirstLine 校验判据保守：首行与标题不一致时正文一个字都不动。
// 标题来自高亮片段、正文首行是别的内容时，「多显示一次」比「把真正的正文吃掉」安全。
func TestDetectDispatchBodyKeepsUnrelatedFirstLine(t *testing.T) {
	prev := SnapshotOf(testInput())
	in := testInput()
	in.Dispatches = append([]hd2.Dispatch{{
		ID:      1050,
		Message: "The Terminid front is collapsing.\n<i=3>MAJOR ORDER WON</i>",
	}}, in.Dispatches...)

	got := Detect(prev, in)
	if len(got) != 1 {
		t.Fatalf("应检测到 1 条事件，实际 %d", len(got))
	}
	if got[0].Title != "MAJOR ORDER WON" {
		t.Fatalf("标题取的是高亮片段，实际 %q", got[0].Title)
	}
	if !strings.Contains(got[0].Body, "Terminid front is collapsing.") {
		t.Fatalf("首行不是标题时正文应原样保留，实际 %q", got[0].Body)
	}
}

// TestDetectDispatchBodyOnlyTitleLineBecomesEmpty 校验「正文只有标题一行」时剥完得到空正文：
// 卡片上仍然只有标题一条信息（不会重复显示），编排层对空正文直接跳过摘要，不会崩、也不画空说明行。
func TestDetectDispatchBodyOnlyTitleLineBecomesEmpty(t *testing.T) {
	prev := SnapshotOf(testInput())
	in := testInput()
	in.Dispatches = append([]hd2.Dispatch{{
		ID:      1052,
		Message: "<i=3>MAJOR ORDER WON</i>\n  \n",
	}}, in.Dispatches...)

	got := Detect(prev, in)
	if len(got) != 1 {
		t.Fatalf("应检测到 1 条事件，实际 %d", len(got))
	}
	if got[0].Title != "MAJOR ORDER WON" {
		t.Fatalf("标题应取高亮片段，实际 %q", got[0].Title)
	}
	if strings.TrimSpace(got[0].Body) != "" {
		t.Fatalf("正文只剩标题一行时剥完应为空，实际 %q", got[0].Body)
	}
}

// TestDetectDispatchBodyKeepsContentWhenTitleTruncated 校验「标题是截断后的摘要」时正文不被误删：
// 没有高亮标记且首行超过 dispatchTitleRunes 时，标题带「…」而正文首行是完整原文，两者不相等，
// 正文必须原样保留——否则每一条长简报都会被吃掉一整行。
func TestDetectDispatchBodyKeepsContentWhenTitleTruncated(t *testing.T) {
	prev := SnapshotOf(testInput())
	in := testInput()
	long := strings.Repeat("Super Earth needs you. ", 40)
	in.Dispatches = append([]hd2.Dispatch{{ID: 1053, Message: long}}, in.Dispatches...)

	got := Detect(prev, in)
	if len(got) != 1 {
		t.Fatalf("应检测到 1 条事件，实际 %d", len(got))
	}
	if !strings.Contains(got[0].Title, "…") {
		t.Fatalf("超长首行的标题应被截断，实际 %q", got[0].Title)
	}
	if !strings.Contains(got[0].Body, long[:40]) {
		t.Fatalf("标题只是截断后的摘要，正文不该被剥掉，实际 %q", got[0].Body)
	}
}

// TestDropTitleLine 是 dropTitleLine 的纯函数取值表：把 Detect 里跑不到的退化输入单独钉住
// （标题为空的上游脏数据、前导空行、正文纯空白、正文只有标题一行）。
func TestDropTitleLine(t *testing.T) {
	cases := []struct{ name, body, title, want string }{
		{"首行就是标题", "MAJOR ORDER WON\nHold the line.", "MAJOR ORDER WON", "Hold the line."},
		{"首行是别的内容（一个字都不动）", "The front moved.\nMAJOR ORDER WON", "MAJOR ORDER WON", "The front moved.\nMAJOR ORDER WON"},
		{"前导空行也跳过", "\n\nMAJOR ORDER WON\nHold the line.", "MAJOR ORDER WON", "Hold the line."},
		{"首行与标题只差首尾空白", "  MAJOR ORDER WON  \nHold the line.", "MAJOR ORDER WON", "Hold the line."},
		{"正文只有标题一行", "MAJOR ORDER WON", "MAJOR ORDER WON", ""},
		{"正文是纯空白", "   \n\t ", "MAJOR ORDER WON", "   \n\t "},
		{"标题为空（上游脏数据）", "Hold the line.", "", "Hold the line."},
		{"正文为空", "", "MAJOR ORDER WON", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := dropTitleLine(c.body, c.title); got != c.want {
				t.Errorf("dropTitleLine(%q, %q) = %q，期望 %q", c.body, c.title, got, c.want)
			}
		})
	}
}
