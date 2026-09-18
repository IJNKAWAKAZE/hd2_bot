package station

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"hd2_bot/src/hd2"
	"hd2_bot/src/hd2/glossary"
	"hd2_bot/src/plugins/plugutil"
	"hd2_bot/src/render"
)

// 卡片名必须与 src/render/templates/<名字>.tmpl 的文件名一致（写错会在启动期 panic）。
const (
	eventsCardName   = "events"
	stationsCardName = "stations"
)

// 卡片头栏的徽标素材逻辑名；取值必须来自 src/render/assets.go 的 assetFiles 登记表。
const (
	eventsEmblem   = "emblem.defense" // assets/game/ui/defense_campaign.svg（防守战，游戏原生图标）
	stationsEmblem = "emblem.dss"     // assets/game/effect/dss.svg（民主空间站，游戏原生图标）
)

// maxEvents 是事件卡最多展示的条数：群里最关心的是这几场正在打的，多出来的只报个数。
const maxEvents = 5

// EventItem 是事件卡上的一起事件。
// 字段都是已格式化、本地化好的字符串，模板只排版，不做任何计算。
type EventItem struct {
	PlanetName   string  // 「中文（English）」；译名缺失时只有英文
	PlanetIndex  string  // 星球编号
	Faction      string  // 进攻方中文名；上游出现未收录的取值时原样显示
	FactionClass string  // 进攻方配色 class
	HealthText   string  // 「1,497,963 / 1,500,000」；没有上限时「—」
	BarPercent   float64 // 防守剩余宽度（0-100，一位小数）
	BarText      string  // 「99.9%」；上游没给上限时是空串，模板整块隐藏进度条
	StartTime    string
	EndTime      string // EndTime 为 nil（上游没给）时是「未知」
}

// EventsCard 是 templates/events.tmpl 的视图模型。
type EventsCard struct {
	render.Meta
	MoreText string      // 「另有 N 起未显示」；全部展示时为空串
	Items    []EventItem // 已按结束时间升序（没有结束时间的排在最后）
	Empty    bool        // 上游没有事件：渲染占位卡，不是错误
}

// StationAction 是空间站卡片上的一项战术行动。
//
// 信息对齐参考项目 HD2 星图页 DSS 面板左栏的行动卡：名称 + 阶段（募捐中 / 已激活 / 冷却中）
// + 募捐进度 + 时间行。用户 2026-09-18 明确不要右侧那块「效果详情」——静态图里点不动左栏，
// 画一张永远停在第一个效果的详情只会误导人，所以效果说明不进本卡。
type StationAction struct {
	Name        string  // 中文译名（对照表命中时）或上游英文原名
	Icon        string  // 战术图标素材逻辑名；未命中时为空串（模板回退成占位方块）
	Status      string  // 阶段文案：募捐中 / 已激活 / 冷却中 / 未激活 / 状态 N
	StatusClass string  // 阶段标签的配色 class
	Detail      string  // 阶段细节：「已捐献 40,805 / 86,400（47.2%）」；募捐中但上游没给成本时「等待募捐启动」，其余阶段没有可写的进度就是空串
	Percent     float64 // 进度条宽度（0-100，一位小数）
	PercentText string  // 进度条右侧的整数百分比（参考站口径，例如「47%」）
	Bar         bool    // 是否画进度条：有募捐数据或行动已激活时才画
	Active      bool    // 已激活（status=2）：模板给这一块加高亮描边（参考站也是这么标记生效中的行动）
	TimeText    string  // 时间行：「行动剩余 1天 02:33:12」/「冷却剩余 05:12:30」；没有时为空串
}

// stationNameFallback 是上游没给空间站名字时的占位名。
// 实测 v2 端点不给 name，而卡片上的标题只剩这一个字段——留「—」看着像坏数据，这里补一个中性名字。
const stationNameFallback = "民主空间站"

// StationItem 是一座民主空间站。
type StationItem struct {
	Name          string          // 上游 name；实测 v2 端点没有给名字，此时显示 stationNameFallback
	Location      string          // 「当前停靠：贝文III（Bekvam III）· 阿基拉分区」；上游没给停靠星球时「位置未知」
	ElectionEnd   string          // 跃迁（选举）截止时间；上游没给时「未知」
	JumpCountdown string          // 「13:01:59」形式的跃迁剩余；算不出时为空串（模板只渲染绝对时间）
	ActionCount   string          // 行动条数（参考站在小节标题右侧标数量）
	Actions       []StationAction // 战术行动列表：全部已知行动，没在募捐的也在（见 stationActions）
	PassiveName   string          // 常驻被动的名字（「行动支持」）
	PassiveDesc   string          // 常驻被动的说明；为空串时模板整块隐藏
}

// StationsCard 是 templates/stations.tmpl 的视图模型。
type StationsCard struct {
	render.Meta
	Count string        // 空间站数量
	Empty bool          // 上游返回空列表：渲染占位卡
	Items []StationItem // 上游实测通常只有一座（民主空间站）
}

// BuildEventsCard 把事件列表转成卡片视图模型。
//
// 排序：按 EndTime 升序（最快结束的排前面），EndTime 为 nil 的排最后并在卡片上标「未知」
// ——这些事件不知道什么时候结束，排在按时间可预期的那些后面才不干扰阅读；
// 同一时间的两起事件按星球编号升序兜底，保证顺序稳定。
// 最多展示 maxEvents 起，多出来的在卡片上报个数。
// fetchedAt 必须是已转换到展示时区的时间（Result.FetchedAt 的时区不统一）。
func BuildEventsCard(list []hd2.PlanetEvent, fetchedAt time.Time, stale bool) EventsCard {
	ordered := make([]hd2.PlanetEvent, len(list))
	copy(ordered, list)
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := ordered[i].EndTime, ordered[j].EndTime
		switch {
		case a == nil && b == nil:
			return ordered[i].PlanetIndex < ordered[j].PlanetIndex
		case a == nil:
			return false // nil 排在最后
		case b == nil:
			return true
		default:
			if !a.Equal(*b) {
				return a.Before(*b)
			}
			return ordered[i].PlanetIndex < ordered[j].PlanetIndex
		}
	})

	show := ordered
	if len(show) > maxEvents {
		show = show[:maxEvents]
	}

	loc := fetchedAt.Location()
	items := make([]EventItem, 0, len(show))
	for _, e := range show {
		items = append(items, buildEventItem(e, loc))
	}

	card := EventsCard{
		Meta: render.Meta{
			Title:    "星球事件",
			Subtitle: fmt.Sprintf("共 %d 起", len(ordered)),
			DataTime: plugutil.FormatDataTime(fetchedAt),
			Stale:    stale,
			Emblem:   eventsEmblem,
		},
		Items: items,
		Empty: len(items) == 0,
	}
	if extra := len(ordered) - len(items); extra > 0 {
		card.MoreText = fmt.Sprintf("另有 %d 起事件未显示（卡片最多展示 %d 起）。", extra, maxEvents)
	}
	return card
}

// buildEventItem 把一起事件转成卡片上的一块内容。
func buildEventItem(e hd2.PlanetEvent, loc *time.Location) EventItem {
	faction, class := factionText(e.Faction)
	percent, barText := 0.0, ""
	if e.MaxHealth > 0 {
		// 上游没给上限时算不出进度：留空会让模板整块隐藏进度条，
		// 而不是显示一条宽度为 0 的「防守剩余 0.0%」——0% 会被误读成「马上就守不住了」。
		percent = plugutil.PercentOf(e.Health, e.MaxHealth)
		barText = plugutil.PercentText(percent)
	}
	return EventItem{
		PlanetName:   plugutil.PlanetDisplayName(e.PlanetName),
		PlanetIndex:  indexText(e.PlanetIndex),
		Faction:      faction,
		FactionClass: class,
		HealthText:   plugutil.HealthText(e.Health, e.MaxHealth),
		BarPercent:   percent,
		BarText:      barText,
		StartTime:    plugutil.EventTime(e.StartTime, loc),
		EndTime:      endTimeText(e.EndTime, loc),
	}
}

// BuildStationsCard 把空间站列表转成卡片视图模型；空列表渲染占位卡（不是错误）。
func BuildStationsCard(list []hd2.SpaceStation, fetchedAt time.Time, stale bool) StationsCard {
	loc := fetchedAt.Location()
	items := make([]StationItem, 0, len(list))
	for _, s := range list {
		actions := stationActions(s.TacticalActions, fetchedAt)
		items = append(items, StationItem{
			Name:          plugutil.DefaultText(s.Name, stationNameFallback),
			Location:      stationLocation(s),
			ElectionEnd:   endTimeText(s.ElectionEnd, loc),
			JumpCountdown: jumpCountdownText(s.ElectionEnd, fetchedAt),
			ActionCount:   plugutil.FormatInt(int64(len(actions))),
			Actions:       actions,
			PassiveName:   passiveName,
			PassiveDesc:   passiveDesc,
		})
	}

	return StationsCard{
		Meta: render.Meta{
			Title:    "民主空间站",
			Subtitle: "DSS",
			DataTime: plugutil.FormatDataTime(fetchedAt),
			Stale:    stale,
			Emblem:   stationsEmblem,
		},
		Count: plugutil.FormatInt(int64(len(items))),
		Empty: len(items) == 0,
		Items: items,
	}
}

// passiveName / passiveDesc 是空间站的常驻被动（「行动支持」）：DSS 停靠在哪颗星球，
// 那颗星球就常驻这份加成，与募捐中的战术行动无关，所以单列一节。
//
// 文案取自参考项目 HD2-Galatic_war-Map 的 tables/hd2_variables.json
// （tactical_action 分类里 effect_ids 含 1238 的那条），与参考站 DSS 面板「常驻被动」一节同源。
const (
	passiveName = "行动支持"
	passiveDesc = "DSS停靠在这颗星球附近，一定程度上有助于该星球的解放战争进程。" +
		"在该星球执行任务的小队所携带的所有“外骨骼机甲”战略配备冷却时间减少35%。"
)

// waitDonationText 是参考站在「还没开始募捐」这个阶段的原话。
const waitDonationText = "等待募捐启动"

// dssFundraisingStatus 是「募捐中」的 status 取值（口径见 plugutil.TacticalStatus）。
const dssFundraisingStatus = 1

// stationLocation 拼「当前停靠」一行：星球中英名 + 星区中文名。
// 上游没给停靠星球（planet 是 "?" 或纯数字，没有名字）时写「位置未知」——
// 只有编号时宁可说不知道，也不要写一个群里没人认得的数字。
func stationLocation(s hd2.SpaceStation) string {
	name := strings.TrimSpace(s.PlanetName)
	if name == "" {
		return "位置未知"
	}
	location := plugutil.PlanetDisplayName(name)
	if sector := strings.TrimSpace(glossary.Sector(s.PlanetSector)); sector != "" {
		location += " · " + sector
	}
	return location
}

// stationActions 把「上游这次报的行动」与「全部已知行动」合成卡片上的行动列表。
//
// 为什么要合：上游只报正在募捐 / 已激活 / 冷却中的那几项，其余行动在响应里根本不出现
// （实测 2026-09-18 只报了 3 项，而游戏里一共 5 项），参考站的 DSS 面板则是**全部列出来**、
// 没在募捐的写「等待募捐启动」——群友看这一栏就是要一眼知道「还有哪些行动、各在什么阶段」。
//
// 顺序固定为 plugutil.TacticalActions() 的顺序（照参考站），上游报了清单里没有的行动则追加在最后：
// 宁可多一行英文行动名，也不把上游的真实状态吞掉。
func stationActions(list []hd2.TacticalAction, fetchedAt time.Time) []StationAction {
	used := make([]bool, len(list))
	specs := plugutil.TacticalActions()
	actions := make([]StationAction, 0, len(specs)+len(list))
	for _, spec := range specs {
		index := matchTacticalAction(list, used, spec.EffectIDs)
		if index < 0 {
			actions = append(actions, placeholderAction(spec))
			continue
		}
		used[index] = true
		actions = append(actions, buildStationAction(list[index], spec.Name, spec.Icon, fetchedAt))
	}
	for i, a := range list {
		if used[i] {
			continue
		}
		name, icon := plugutil.TacticalActionName(a.Name)
		actions = append(actions, buildStationAction(a, name, icon, fetchedAt))
	}
	return actions
}

// matchTacticalAction 在还没被认领的上游行动里找第一条「效果编号完整包含 ids」的，返回下标，找不到返回 -1。
//
// 用「完整包含」而不是「有交集」：同一族行动的编号互相重叠（飞鹰风暴 1209/1212/1216 与
// 飞鹰封锁 1209/1215 都含 1209），只比交集会把两者错配（参考站也是这么判的）。
// ids 为空（没有对照数据）时直接返回 -1，不去猜哪条上游行动对应它。
func matchTacticalAction(list []hd2.TacticalAction, used []bool, ids []int) int {
	if len(ids) == 0 {
		return -1
	}
	for i, a := range list {
		if used[i] || !containsAllIDs(a.EffectIDs, ids) {
			continue
		}
		return i
	}
	return -1
}

// containsAllIDs 判断 have 是否完整包含 want 里的每个编号（want 为空时返回 false）。
func containsAllIDs(have, want []int) bool {
	if len(want) == 0 {
		return false
	}
	for _, w := range want {
		found := false
		for _, h := range have {
			if h == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// placeholderAction 给「上游这次没有报到的行动」造一行：阶段写「募捐中」，细节写「等待募捐启动」，
// 进度 0%（与参考站在这个阶段的写法一致）。刻意不写时间行与效果说明——
// 没有数据就没有倒计时，画一个空倒计时或一段猜来的说明比不画更容易误导。
func placeholderAction(spec plugutil.TacticalActionSpec) StationAction {
	status, class := plugutil.TacticalStatus(dssFundraisingStatus)
	return StationAction{
		Name:        plugutil.DefaultText(spec.Name, "未命名行动"),
		Icon:        spec.Icon,
		Status:      status,
		StatusClass: class,
		Detail:      waitDonationText,
		PercentText: "0%",
		Bar:         true,
	}
}

// buildStationAction 把一项战术行动转成卡片上的一行。
// 译名、图标与状态文案全部走 plugutil：这些口径必须与推送卡片逐字相同。
// name / icon 由调用方给出：清单里命中时用清单的译名（上游英文名与译名是两套字符串），
// 清单外命中时用 plugutil.TacticalActionName 现查的结果。
func buildStationAction(a hd2.TacticalAction, name, icon string, fetchedAt time.Time) StationAction {
	name = plugutil.DefaultText(name, "未命名行动")
	status, class := plugutil.TacticalStatus(a.Status)
	action := StationAction{
		Name:        name,
		Icon:        icon,
		Status:      status,
		StatusClass: class,
		Active:      a.Status == 2,
		Detail:      actionDetail(a),
		TimeText:    actionTimeText(a, fetchedAt),
	}
	if percent, ok := donationPercent(a); ok {
		action.Percent = percent
		action.PercentText = fmt.Sprintf("%d%%", int(math.Round(percent)))
		action.Bar = true
	}
	if a.Status == 2 {
		// 已激活：参考站把进度条画满（募捐已完成，条子不再是进度而是「生效中」的状态色）。
		action.Percent = 100
		action.PercentText = "100%"
		action.Bar = true
	}
	return action
}

// actionDetail 给出行动那一行的细节文案，规则按阶段区分（口径对齐参考站的 DSS 面板）：
//   - 有募捐数据：写募捐进度「已捐献 40,805 / 86,400（47.2%）」——已激活/冷却中时它同样有意义
//     （募捐期攒了多少、有没有达成），上游实测在整个行动周期里都会给 costs；
//   - 募捐中但没有成本数据：写参考站在这个阶段的原话「等待募捐启动」；
//   - 其余阶段（未激活 / 已激活 / 冷却中）没有成本数据：返回空串。
//
// 空串是刻意的：冷却中的行动写一句「等待募捐启动」会让人以为它又要开始募捐了，
// 宁可不写——阶段由标签说清，时间由时间行说清。
func actionDetail(a hd2.TacticalAction) string {
	if _, ok := firstCost(a.Costs); ok {
		return donationText(a)
	}
	if a.Status == dssFundraisingStatus {
		return waitDonationText
	}
	return ""
}

// donationText 拼募捐进度那一行。上游的 costs 可能缺失或多条（实测只有一条），
// 缺失或目标值为 0 时给「等待募捐启动」——那是参考站在这个阶段的原话，不是错误。
func donationText(a hd2.TacticalAction) string {
	cost, ok := firstCost(a.Costs)
	if !ok || cost.TargetValue <= 0 {
		return "等待募捐启动"
	}
	percent, _ := donationPercent(a)
	return fmt.Sprintf("已捐献 %s / %s（%s）",
		countText(cost.CurrentValue), countText(cost.TargetValue), plugutil.PercentText(percent))
}

// donationPercent 算募捐进度；没有可用的成本数据时返回 false（调用方不画进度条）。
func donationPercent(a hd2.TacticalAction) (float64, bool) {
	cost, ok := firstCost(a.Costs)
	if !ok || cost.TargetValue <= 0 {
		return 0, false
	}
	return plugutil.PercentOf(int64(math.Round(cost.CurrentValue)), int64(math.Round(cost.TargetValue))), true
}

// firstCost 取第一条募捐成本；上游没有给 costs 时返回 false。
func firstCost(costs []hd2.TacticalCost) (hd2.TacticalCost, bool) {
	if len(costs) == 0 {
		return hd2.TacticalCost{}, false
	}
	return costs[0], true
}

// countText 把募捐数值排成千分位整数：上游给的是浮点（实测 40805.23），
// 卡片上按整数显示（「40,805」），小数位对读者没有意义。
func countText(value float64) string {
	return plugutil.FormatInt(int64(math.Round(value)))
}

// actionTimeText 给出一项行动的时间行：
//   - 已激活（status=2）：statusExpire 是行动结束时刻 → 「行动剩余 …」；
//   - 冷却中（status=3）：statusExpire 是冷却结束时刻 → 「冷却剩余 …」；
//   - 其余阶段（未激活/募捐中）没有可倒数的时间 → 空串。
//
// 上游没给时刻、数据时间是零值或时刻已经过去时一律返回空串：
// 显示「剩余 00:00:00」比不显示更容易误导（读者会以为是刚结束，而不是数据没更新）。
func actionTimeText(a hd2.TacticalAction, fetchedAt time.Time) string {
	if a.StatusExpire == nil || fetchedAt.IsZero() {
		return ""
	}
	remaining := a.StatusExpire.Sub(fetchedAt)
	if remaining <= 0 {
		return ""
	}
	switch a.Status {
	case 2:
		return "行动剩余 " + plugutil.CountdownText(remaining)
	case 3:
		return "冷却剩余 " + plugutil.CountdownText(remaining)
	default:
		return ""
	}
}

// jumpCountdownText 拼空间站跃迁剩余（参考站右上角的「空间站即将移动」）。
// 上游没给截止时刻、数据时间是零值或时刻已过时返回空串：那时模板只写绝对时间。
func jumpCountdownText(end *time.Time, fetchedAt time.Time) string {
	if end == nil || fetchedAt.IsZero() {
		return ""
	}
	remaining := end.Sub(fetchedAt)
	if remaining <= 0 {
		return ""
	}
	return plugutil.CountdownText(remaining)
}

// factionText 返回派系中文名与配色 class；未收录时返回原文与空 class。
// 归一到 plugutil：事件卡与推送卡片对同一批 faction 必须说同一句话。
func factionText(faction string) (string, string) {
	if strings.TrimSpace(faction) == "" {
		return plugutil.DashText, ""
	}
	return plugutil.FactionName(faction), plugutil.FactionClass(faction)
}

// indexText 拼星球编号；上游没给（0）时显示「—」。
func indexText(index int) string {
	if index <= 0 {
		return plugutil.DashText
	}
	return strconv.Itoa(index)
}

// endTimeText 格式化结束/截止时间；上游没给（nil 或零值）时显示「未知」
// ——事件卡上「不知道什么时候结束」是有意义的信息，不能空着。
// 格式化与占位口径统一走 plugutil：事件卡与空间站卡不会对同一个字段说两种话。
func endTimeText(t *time.Time, loc *time.Location) string {
	return plugutil.MinuteTimePtrText(t, loc, plugutil.UnknownText)
}
