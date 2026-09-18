package planets

import (
	"context"
	"fmt"
	"log"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"hd2_bot/src/hd2"
	"hd2_bot/src/hd2/glossary"
	"hd2_bot/src/plugins/plugutil"
	"hd2_bot/src/render"
	"hd2_bot/src/translate"
)

// maxHotspots 是总览热点列表的最大条数：卡片高度有限，群里也没人翻八条以外的星球。
const maxHotspots = 8

// 环境信息的展示预算（按 rune 计，S4 口径）：正文不再按「两行摘要」截断，真实上游内容要完整显示。
// 这三个上限是**终局防线**，只为挡住上游或翻译后端突然给出的异常长文本。
//
// 取值依据（真实上游实测 273 颗星球）：最长群系说明 337 字、最长危害说明 100 字、最长危害名 17 字，
// 所以 400 / 200 / 80 都留了 1.2 倍以上余量，正常内容一条都不会被截。
// 名字同样要有防线：群系名与危害名也来自上游与翻译后端，长度不受我们控制，只截说明挡不住。
// 危害条数不设上限（见 TestBuildLocalizedPlanetCardKeepsAllHazards）：漏报一种危害比卡片高一截更糟；
// 真实最长星球卡（群系说明 337 字 + 危害说明 337 字）实测 1966px，远低于 render.MaxCardHeightPx = 7000px。
const (
	maxNameRunes       = 80
	maxBiomeDescRunes  = 400
	maxHazardDescRunes = 200
)

// environmentNote 是环境信息保留英文时的说明文案：只有「配了翻译但没翻成」才写。
// 没配翻译层（translate.enabled=false）时不写这句——那不是失败，是本来就没开。
const environmentNote = "翻译暂不可用，环境信息为英文原文。"

// knownFactionKeys 是总览里已知派系的展示顺序（我方在前），只留归一化后的 key。
// 中文名、配色 class 与「算不算敌方」全项目只有 plugutil 一份（见 plugutil.FactionName 等），
// 这里剩下的只是排版顺序；上游 currentOwner 实测有 Humans / Terminids / Automaton /
// Illuminate（单复数混用），归一化同样交给 plugutil，本包不再复制任何文案。
var knownFactionKeys = []string{"human", "terminid", "automaton", "illuminate"}

// FactionStat 是一个派系的控制统计，字段都是已格式化字符串，模板直接渲染。
type FactionStat struct {
	Name    string // 派系中文名
	Count   string // 控制星球数（千分位）
	Percent string // 占全部星球的比例（一位小数）
	Class   string // base.tmpl 里的配色 class；超级地球为空串
}

// PlanetRow 是总览里的一行星球。字段都是拿来即用的字符串或已截断的数值，模板不做计算。
type PlanetRow struct {
	Index       string  // 上游星球编号
	NameChinese string  // 中文译名；没有译名时是英文原文
	NameEnglish string  // 上游英文名（大写原文）
	Sector      string  // 分区中文译名；缺省「—」
	Owner       string  // 控制方中文名
	OwnerClass  string  // 控制方配色 class
	Faction     string  // 卡面顶栏的派系名：防守战取入侵方（整张卡跟着入侵方配色），其余取控制方
	Emblem      string  // 派系徽记素材逻辑名（见 render 的 assetFiles 登记表）
	CardClass   string  // 卡内配色 class：f-term / f-auto / f-illu / f-hum；未收录的派系为空串
	Defense     bool    // 有事件（防守战）：卡片上星球名用白色显示，与解放/进攻行动区分
	PlayerCount string  // 在线士兵（千分位）
	Status      string  // 「防守战」「进攻中」；都没有时为空串
	StatusClass string  // 状态标签配色 class
	BarLabel    string  // 进度条语义：「防守剩余」「解放」「星球血量」
	BarPercent  float64 // 进度条宽度（0-100，一位小数），直接写进 CSS 宽度
	BarText     string  // 进度文案，例如「80.8%」
	BiomeAsset  string  // 群系缩略图的素材逻辑名（见 render.BiomeAsset）；没有合适配图时为空串，模板不挂图
	Timer       string  // 防守战的保卫倒计时（「1天 02:33:12」）；算不出截止时刻时为空串，模板不渲染这一行
}

// PlanetsSummary 是 /planets 的中间结果：汇总数字 + 已排序并截断的热点。
// 卡片与文本回退共用它，保证两条路径的数字口径与顺序完全一致。
type PlanetsSummary struct {
	PlayerCount string // 在线士兵（千分位）；战况缺失时为「—」
	Total       string // 星球总数
	Contested   string // 有进攻行动的星球数（attacking 非空：这些星球正在向别的星球发动进攻）
	EventCount  string // 有事件的星球数
	// Factions / Hotspots 是给卡片与文本用的细节：热点星球按用户 2026-09-17 的要求重新上卡
	// （派系控制暂时不渲染，算好放着，加回来只要在模板与 FormatPlanetsText 里补一段）。
	Factions []FactionStat // 各派系控制统计，顺序固定
	Hotspots []PlanetRow   // 热点列表，最多 maxHotspots 条
}

// PlanetsCard 是 templates/planets.tmpl 的视图模型。
type PlanetsCard struct {
	render.Meta
	PlayerCount string
	Total       string
	Contested   string
	EventCount  string
	Factions    []FactionStat
	Hotspots    []PlanetRow
}

// PlanetEventCard 是单星球卡片里的事件区块。
// 上游的 eventType 是数字枚举（实测值 1）、含义没有公布，卡片上不展示——
// 「这是防守战」由卡面头部的战役状态说清，比印一个「类型 1」有用得多。
type PlanetEventCard struct {
	Faction      string  // 进攻方中文名
	FactionClass string  // 进攻方配色 class
	HealthText   string  // 「1,200,000 / 1,500,000」
	BarPercent   float64 // 防守剩余宽度（0-100，一位小数）
	BarText      string  // 「80.0%」
	StartTime    string
	EndTime      string
}

// EnvItem 是环境危害的一条：名称 + 说明（都已是中文，翻译不可用时是英文原文）。
// 名称与说明分开存，模板与文本回退才能各自排版（说明为空时文本回退只写一行名称）。
type EnvItem struct {
	Name        string
	Description string
}

// PlanetCard 是 templates/planet.tmpl 的视图模型。
//
// 版式对齐社区站点 HD2 真理部星图页的星球卡：阵营色卡框（.f-* 切 --fc/--fcd）＋
// 顶栏的派系徽记与在线士兵 ＋ 群系实景图 ＋ 战役状态与星球名 ＋ 进度条，
// 下面接「战略情报分析 / 星球情报 / 环境危害 / 行动变量 / 兴趣点 / 星球事件」六个小节（.pd-*）：
// 前三块里「战略情报分析」只靠星球自身的数据算得出（见 planetIntel），
// 「行动变量」与「兴趣点」需要别的接口的数据，由 FillPlanetExtras 在取到之后补上。
// 所有文案、配色 class、素材逻辑名都由这一层给好，模板只排版、不做计算。
type PlanetCard struct {
	render.Meta
	CardClass    string // 卡内配色 class：f-term / f-auto / f-illu / f-hum；未收录的派系为空串
	Faction      string // 卡面顶栏的派系名：防守战取入侵方（整张卡跟着入侵方配色），其余取控制方
	NameChinese  string // 星球中文名；没有译名时是上游英文原文
	NameEnglish  string // 上游英文名；与中文名相同（没有译名）时为空串，模板不重复显示一遍
	Status       string // 战役状态：防守战 / 进攻中 / 解放战役 / 已解放
	Defense      bool   // 有事件（防守战）：星球名用白色显示，与解放/进攻行动区分
	Timer        string // 防守战的保卫倒计时（「1天 02:33:12」）；算不出截止时刻时为空串
	Index        string
	Sector       string
	Biome        string    // 生物群系名（译文；翻译不可用时是上游英文原文）
	BiomeDesc    string    // 生物群系说明（剥掉 (UNOFFICIAL) 之类的标记；上游没给时为空串）
	Hazards      string    // 环境危害名（过滤掉上游的 None 占位）；没有真实危害时是「暂无环境危害」
	BiomeAsset   string    // 群系实景头图的素材逻辑名；没有合适配图时为空串，模板整块不渲染
	EnvItems     []EnvItem // 环境危害逐条（名称 + 说明）；没有真实危害时为空
	EnvNote      string    // 环境区块的说明（目前只有翻译不可用一种）；为空时整块不渲染
	Owner        string
	OwnerClass   string
	InitialOwner string
	PlayerCount  string
	HealthText   string
	BarLabel     string  // 「解放」或「星球血量」；没有进度数据时为空串
	BarPercent   float64 // 0-100，一位小数
	BarText      string
	Regen        string // 每秒回复，例如「5.6 /秒」
	Attacking    string // 这颗星球正在进攻的目标星球编号；没有时为空串
	Event        *PlanetEventCard

	// 以下三块是用户 2026-09-18 要求加的（参照社区站点 HD2 真理部星图页的星球详情）：
	Intel       []IntelRow   // 战略情报分析：解放度 / 抵抗度 / 星球血量 / 玩家数量
	EffectChips []EffectChip // 行动变量（补充源给出的 galactic effect，中文对照）
	EffectNote  string       // 没有行动变量时的一句说明：区分「确实没有」与「补充源没取到」
	POIs        []POIChip    // 兴趣点：MO 目标 / 战役 / DSS 停靠 / 敌军反攻 / 星区
}

// SummarizePlanets 把星球列表与战况聚合成总览所需的全部数字与热点顺序。
// war 为 nil（战况拿不到）时在线士兵显示「—」，其余统计照常。
//
// 热点的定义（spec §7.2）：有事件的星球优先，其次是有进攻行动的星球，组内按在线士兵降序；
// 既没有事件也没有进攻行动的星球不进热点——它们只是背景，不该挤掉真正在打的战线。
//
// fetchedAt 是这份数据的数据时间，只用于防守战的保卫倒计时（见 defenseTimerText）：
// 传零值时倒计时一律不显示，而不是拿本机时钟去猜一个剩余时间。
func SummarizePlanets(list []hd2.Planet, war *hd2.War, fetchedAt time.Time) PlanetsSummary {
	summary := PlanetsSummary{
		PlayerCount: plugutil.DashText,
		Total:       plugutil.FormatInt(int64(len(list))),
	}
	if war != nil {
		summary.PlayerCount = plugutil.FormatInt(war.Statistics.PlayerCount)
	}

	// 按上游原文统计控制方：归一化只用于归类，展示时未收录的派系要能原样显示。
	// attacking 是「这颗星球正在进攻的目标编号」，不是「被谁进攻」：实测 Peacock（编号 216，
	// 终结族）的 attacking=[268]，而 268 正是正在打防守战的 Luxuriant。所以这里数的是
	// 「正在发动进攻的星球数」；「被进攻」表现为防守事件，也就是下面的事件数。
	counts := make(map[string]int, len(knownFactionKeys))
	offensives, events := 0, 0
	hotspots := make([]hd2.Planet, 0, len(list))
	for _, p := range list {
		counts[p.CurrentOwner]++
		if len(p.Attacking) > 0 {
			offensives++
		}
		if p.Event != nil {
			events++
		}
		if p.Event != nil || len(p.Attacking) > 0 {
			hotspots = append(hotspots, p)
		}
	}
	summary.Contested = plugutil.FormatInt(int64(offensives))
	summary.EventCount = plugutil.FormatInt(int64(events))
	summary.Factions = factionStats(counts, len(list))

	sortPlanets(hotspots)
	if len(hotspots) > maxHotspots {
		hotspots = hotspots[:maxHotspots]
	}
	summary.Hotspots = make([]PlanetRow, 0, len(hotspots))
	for _, p := range hotspots {
		summary.Hotspots = append(summary.Hotspots, buildPlanetRow(p, fetchedAt))
	}
	return summary
}

// sortPlanets 按「有事件优先 → 有进攻行动优先 → 在线士兵降序 → 编号升序」排序。
// 最后一级编号是稳定的兜底：同一份数据每次给出同样的顺序，卡片与文本回退才不会飘。
func sortPlanets(list []hd2.Planet) {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if (a.Event != nil) != (b.Event != nil) {
			return a.Event != nil
		}
		if (len(a.Attacking) > 0) != (len(b.Attacking) > 0) {
			return len(a.Attacking) > 0
		}
		if a.Statistics.PlayerCount != b.Statistics.PlayerCount {
			return a.Statistics.PlayerCount > b.Statistics.PlayerCount
		}
		return a.Index < b.Index
	})
}

// factionStats 统计各派系控制的星球数：先是 knownFactionKeys 里的固定顺序，
// 再把未收录的控制方按原文排序追加在后面（上游一旦出现新派系也不会被静默吞掉）。
// 「是不是已知派系」由 plugutil.FactionKey 判定，归并同一派系的不同写法（Humans / Human）
// 也用它给出的归一化 key：FactionName 为了让未收录的取值原样显示、返回的就是原文，
// 拿它的返回值跟原文比相等判断不出「认不认识」（原文本身就是中文名的脏数据会误判），
// 那会让这颗星球从统计里静默消失。
func factionStats(counts map[string]int, total int) []FactionStat {
	known := make(map[string]int, len(knownFactionKeys))
	others := make([]string, 0, len(counts))
	for owner, count := range counts {
		if key, ok := plugutil.FactionKey(owner); ok {
			known[key] += count
			continue
		}
		others = append(others, owner)
	}

	stats := make([]FactionStat, 0, len(counts))
	for _, key := range knownFactionKeys {
		n := known[key]
		if n == 0 {
			continue
		}
		stats = append(stats, FactionStat{
			Name:    plugutil.FactionName(key),
			Count:   plugutil.FormatInt(int64(n)),
			Percent: plugutil.PercentText(sharePercent(n, total)),
			Class:   plugutil.FactionClass(key),
		})
	}

	// 未收录的控制方按原文排序：它们没有既定的展示顺序，但顺序必须稳定。
	sort.Strings(others)
	for _, owner := range others {
		stats = append(stats, FactionStat{
			Name:    ownerText(owner),
			Count:   plugutil.FormatInt(int64(counts[owner])),
			Percent: plugutil.PercentText(sharePercent(counts[owner], total)),
		})
	}
	return stats
}

// buildPlanetRow 把一颗星球转成总览的一行。fetchedAt 用于防守战的保卫倒计时。
func buildPlanetRow(p hd2.Planet, fetchedAt time.Time) PlanetRow {
	label, percent, text := rowProgress(p)
	status, statusClass := planetStatus(p)
	// 卡面配色跟谁走：防守战整张卡换成**入侵方**的颜色与徽记（社区站点就是这么做的：
	// 卡框颜色与图标同源，只换一半会出现「黄色的超级地球徽记」这种看着像 bug 的卡）。
	// 上游没给事件进攻方时退回控制方，不留下没有主色的卡。
	faction := p.CurrentOwner
	if p.Event != nil && strings.TrimSpace(p.Event.Faction) != "" {
		faction = p.Event.Faction
	}
	return PlanetRow{
		Index:       strconv.Itoa(p.Index),
		NameChinese: glossary.Planet(p.Name),
		NameEnglish: strings.TrimSpace(p.Name),
		Sector:      sectorText(p.Sector),
		Owner:       ownerText(p.CurrentOwner),
		OwnerClass:  ownerClass(p.CurrentOwner),
		Faction:     ownerText(faction),
		Emblem:      ownerEmblem(faction),
		CardClass:   factionCardClass(faction),
		Defense:     p.Event != nil,
		PlayerCount: plugutil.FormatInt(p.Statistics.PlayerCount),
		Status:      status,
		StatusClass: statusClass,
		BarLabel:    label,
		BarPercent:  percent,
		BarText:     text,
		BiomeAsset:  render.BiomeAsset(p.Biome.Name),
		Timer:       defenseTimerText(p.Event, fetchedAt),
	}
}

// planetStatus 给一行星球加状态标签：有事件对象就是防守战，其次看它有没有在进攻别的星球。
func planetStatus(p hd2.Planet) (string, string) {
	if p.Event != nil {
		return "防守战", "tag--loss"
	}
	if len(p.Attacking) > 0 {
		return "进攻中", "tag--gold"
	}
	return "", ""
}

// defenseTimerText 给出防守战的保卫倒计时（形如「1天 02:33:12」）。
//
// 截止时刻取上游事件的 endTime，起点取**数据时间**而不是本机当前时间：上游数据本身可能已经滞后几分钟，
// 拿 now 去减会把这段滞后算成额外的剩余时间（社区站点 HD2 真理部在同一个坑上有过明确记录）。
// 没有 endTime、没有数据时间、或者按数据时间已经到期时返回空串——模板据此整行不渲染，
// 而不是显示「00:00:00」或负数，那种数字比不显示更容易误导人。
func defenseTimerText(e *hd2.PlanetEvent, fetchedAt time.Time) string {
	if e == nil || e.EndTime == nil || fetchedAt.IsZero() {
		return ""
	}
	remaining := e.EndTime.Sub(fetchedAt)
	if remaining <= 0 {
		return ""
	}
	return formatCountdown(remaining)
}

// formatCountdown 保留本包的调用点，实际排版交给 plugutil.CountdownText：
// 空间站卡的「冷却剩余」用的是同一份格式，两处各写一份迟早会出现两种写法。
func formatCountdown(d time.Duration) string {
	return plugutil.CountdownText(d)
}

// rowProgress 决定总览热点行的进度条（spec §7.2 的「解放/防守进度条」）：
//   - 有事件 → 防守剩余（event.health / event.maxHealth），方向与游戏内防守条一致；
//   - 敌方控制 → 解放进度（1 - health / maxHealth）：实测 health < maxHealth 的 11 颗星球
//     全部是敌方控制、initialOwner 为 Humans，血量随我方推进下降；
//   - 其它（我方控制且无事件）→ 星球血量。
//
// 三种语义都对得上实测数据，所以不额外做方向换算；上游没有给出的口径一律不猜。
func rowProgress(p hd2.Planet) (string, float64, string) {
	if p.Event != nil && p.Event.MaxHealth > 0 {
		percent := plugutil.PercentOf(p.Event.Health, p.Event.MaxHealth)
		return "防守剩余", percent, plugutil.PercentText(percent)
	}
	return planetProgress(p)
}

// planetProgress 决定单星球卡的进度条：敌方控制看解放进度，其它看星球血量。
// 防守进度是事件自身的字段，放在卡片的事件区块里单独展示，这里不重复。
func planetProgress(p hd2.Planet) (string, float64, string) {
	if p.MaxHealth <= 0 {
		return "", 0, plugutil.DashText
	}
	if isEnemyOwner(p.CurrentOwner) {
		percent := complementPercent(p.Health, p.MaxHealth)
		return "解放", percent, plugutil.PercentText(percent)
	}
	percent := plugutil.PercentOf(p.Health, p.MaxHealth)
	return "星球血量", percent, plugutil.PercentText(percent)
}

// BuildPlanetsCard 构造总览卡片视图模型。
// fetchedAt 必须是已转换到展示时区的时间（Result.FetchedAt 的时区不统一）。
func BuildPlanetsCard(summary PlanetsSummary, fetchedAt time.Time, stale bool) PlanetsCard {
	return PlanetsCard{
		Meta: render.Meta{
			Title:    "战线总览",
			Subtitle: "全部 " + summary.Total + " 颗星球",
			DataTime: plugutil.FormatDataTime(fetchedAt),
			Stale:    stale,
			Emblem:   "emblem.super_earth",
		},
		PlayerCount: summary.PlayerCount,
		Total:       summary.Total,
		Contested:   summary.Contested,
		EventCount:  summary.EventCount,
		Factions:    summary.Factions,
		Hotspots:    summary.Hotspots,
	}
}

// BuildPlanetCard 构造单星球卡片视图模型；fetchedAt 必须是已转换到展示时区的时间。
// 事件时间用 fetchedAt 的时区展示：卡片上不该出现两个时区混杂的时间。
func BuildPlanetCard(p hd2.Planet, fetchedAt time.Time, stale bool) PlanetCard {
	// 进度条口径与总览热点行一致（rowProgress）：防守战看防守剩余，敌方控制看解放度，其余看星球血量。
	// 防守战不能走 planetProgress——那时候星球血量恒满，画出来的永远是「星球血量 100.0%」。
	label, percent, text := rowProgress(p)
	loc := fetchedAt.Location()
	// 卡面配色与徽记跟谁走：防守战整张卡换成**入侵方**的颜色与徽记（与总览热点行同一套口径——
	// 只换一半会得到「黄色的超级地球徽记」这种看着像 bug 的卡）。上游没给事件进攻方时退回控制方。
	faction := p.CurrentOwner
	if p.Event != nil && strings.TrimSpace(p.Event.Faction) != "" {
		faction = p.Event.Faction
	}
	// 星球名：中文名缺译名时就是上游英文原文，这时不再单独显示一遍英文。
	nameZh := plugutil.DefaultText(glossary.Planet(p.Name), plugutil.DashText)
	nameEn := strings.TrimSpace(p.Name)
	if strings.EqualFold(nameZh, nameEn) {
		nameEn = ""
	}
	return PlanetCard{
		Meta: render.Meta{
			// 外壳标题写「星球情报」而不是星球名：名字在卡面里（.pc-name）已经是最显眼的一行，
			// 两处都写只会得到一张重复报名字的卡。
			Title:    "星球情报",
			Subtitle: "编号 " + strconv.Itoa(p.Index) + " · " + sectorText(p.Sector),
			DataTime: plugutil.FormatDataTime(fetchedAt),
			Stale:    stale,
			Emblem:   ownerEmblem(faction),
		},
		CardClass:   factionCardClass(faction),
		Faction:     ownerText(faction),
		NameChinese: nameZh,
		NameEnglish: nameEn,
		Status:      planetStatusText(p),
		Defense:     p.Event != nil,
		Timer:       defenseTimerText(p.Event, fetchedAt),
		Index:       strconv.Itoa(p.Index),
		Sector:      sectorText(p.Sector),
		Biome:       plugutil.DefaultText(p.Biome.Name, plugutil.DashText),
		// 头图按**上游英文群系名**取：译文会把名字翻成中文，拿译文去查映射表只会查空。
		// 所以这一行必须在本地化之前用原文算，BuildLocalizedPlanetCard 后续覆盖 Biome 不影响它。
		BiomeAsset:   render.BiomeAsset(p.Biome.Name),
		BiomeDesc:    cleanUnofficial(p.Biome.Description),
		Hazards:      hazardsText(p.Hazards),
		Owner:        ownerText(p.CurrentOwner),
		OwnerClass:   ownerClass(p.CurrentOwner),
		InitialOwner: ownerText(p.InitialOwner),
		PlayerCount:  plugutil.FormatInt(p.Statistics.PlayerCount),
		HealthText:   plugutil.HealthText(p.Health, p.MaxHealth),
		BarLabel:     label,
		BarPercent:   percent,
		BarText:      text,
		Regen:        fmt.Sprintf("%.1f /秒", p.RegenPerSecond),
		Attacking:    attackingText(p.Attacking),
		Event:        buildPlanetEventCard(p.Event, loc),
		Intel:        planetIntel(p),
	}
}

// planetStatusText 给卡面头部一句战役状态：有事件是防守战，其次看它是否正在进攻别的星球，
// 再按控制方分「解放战役」（敌方控制）与「已解放」（我方控制且没有战事）。
func planetStatusText(p hd2.Planet) string {
	if status, _ := planetStatus(p); status != "" {
		return status
	}
	if isEnemyOwner(p.CurrentOwner) {
		return "解放战役"
	}
	return "已解放"
}

// buildPlanetEventCard 构造事件区块；event 为 nil 时返回 nil，模板会整块隐藏。
func buildPlanetEventCard(e *hd2.PlanetEvent, loc *time.Location) *PlanetEventCard {
	if e == nil {
		return nil
	}
	// 上游没给 maxHealth 时算不出进度：BarText 留空会让模板整块隐藏进度条，
	// 而不是显示一条宽度为 0 的「防守剩余 0.0%」——0% 会被误读成「马上就守不住了」。
	var percent float64
	barText := ""
	if e.MaxHealth > 0 {
		percent = plugutil.PercentOf(e.Health, e.MaxHealth)
		barText = plugutil.PercentText(percent)
	}
	return &PlanetEventCard{
		Faction:      ownerText(e.Faction),
		FactionClass: ownerClass(e.Faction),
		HealthText:   plugutil.HealthText(e.Health, e.MaxHealth),
		BarPercent:   percent,
		BarText:      barText,
		StartTime:    plugutil.EventTime(e.StartTime, loc),
		EndTime:      plugutil.EventTime(timeOf(e.EndTime), loc),
	}
}

// timeOf 取时间指针的值；nil 返回零值，调用方按「上游没有给值」处理。
func timeOf(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// sectorText 返回分区中文译名；缺省「—」。
func sectorText(sector string) string {
	if strings.TrimSpace(sector) == "" {
		return plugutil.DashText
	}
	return glossary.Sector(sector)
}

// ownerText 返回控制方中文名；上游出现未收录的取值时原样显示，不猜成某个派系。
func ownerText(owner string) string {
	return plugutil.DefaultText(plugutil.FactionName(owner), plugutil.DashText)
}

// ownerClass 返回控制方配色 class；未收录时返回空串（模板里退化成普通文字）。
func ownerClass(owner string) string {
	return plugutil.FactionClass(owner)
}

// ownerEmblem 返回控制方对应的徽标素材名；未知控制方用超级地球徽标兜底。
// 徽标是星球卡自己的素材，不参与跨插件的文案口径，所以映射表留在本包；
// 归一化（折叠大小写、去掉复数词尾、去首尾空白）交给 plugutil.FactionKey，
// 与派系中文名共用同一套规则，上游单复数混用（Automaton / Automatons）才不会漏。
// factionCardClass 返回卡片配色用的 class（base.tmpl 里的 .f-* 系列，卡内 --fc/--fcd 由它切换）。
// 未收录的派系返回空串：模板退化成默认的超级地球色，而不是随便挑一个阵营色。
func factionCardClass(faction string) string {
	key, ok := plugutil.FactionKey(faction)
	if !ok {
		return ""
	}
	switch key {
	case "terminid":
		return "f-term"
	case "automaton":
		return "f-auto"
	case "illuminate":
		return "f-illu"
	default:
		return "f-hum"
	}
}

func ownerEmblem(owner string) string {
	key, _ := plugutil.FactionKey(owner)
	switch key {
	case "terminid":
		return "emblem.terminids"
	case "automaton":
		return "emblem.automaton"
	case "illuminate":
		return "emblem.illuminate"
	default:
		return "emblem.super_earth"
	}
}

// isEnemyOwner 交给 plugutil 判断（口径全项目一致：只有敌人控制的星球才谈得上「解放进度」）。
func isEnemyOwner(owner string) bool { return plugutil.IsEnemyFaction(owner) }

// noHazardText 是没有真实环境危害时的文案（spec §5.2）；模板与文本回退共用这一句。
const noHazardText = "暂无环境危害"

// hazardsText 拼环境危害名。没有真实危害时返回 noHazardText。
func hazardsText(hazards []hd2.Hazard) string {
	real := hazardList(hazards)
	if len(real) == 0 {
		return noHazardText
	}
	names := make([]string, 0, len(real))
	for _, h := range real {
		names = append(names, strings.TrimSpace(h.Name))
	}
	return strings.Join(names, "、")
}

// hazardList 提取真实的环境危害：上游在没有危害时会返回一条名为 None 的占位记录（实测），
// 占位项不是危害必须剔除；名称为空白的记录同样跳过（否则卡片上会多一条没名字的危害）。
func hazardList(hazards []hd2.Hazard) []hd2.Hazard {
	out := make([]hd2.Hazard, 0, len(hazards))
	for _, h := range hazards {
		name := strings.TrimSpace(h.Name)
		if name == "" || strings.EqualFold(name, "none") {
			continue
		}
		out = append(out, h)
	}
	return out
}

// cleanUnofficial 剥掉上游说明里的括号标记（实测形如 "(UNOFFICIAL)"），并把空白折叠成一个空格。
// 只剥已知标记且大小写不敏感；其它括号内容一律原样保留——
// 把一句正常说明里的括号也删掉，比留着标记更糟。
func cleanUnofficial(text string) string {
	cleaned := unofficialRe.ReplaceAllString(text, "")
	return strings.TrimSpace(strings.Join(strings.Fields(cleaned), " "))
}

// unofficialRe 匹配上游说明里的括号标记；上游实测全大写，这里放宽到大小写不敏感。
var unofficialRe = regexp.MustCompile(`(?i)\((?:UNOFFICIAL DATA|UNOFFICIAL|BETA|WIP)\)`)

// truncateText 把文本截断到 max 个字符（按 rune，中文不会被切坏），截断时补一个省略号。
//
// max <= 0 的契约是「不限制」：原样返回，绝不把正文清空。注意 plugins/orders 里有个同名的
// truncateRunes，它的 max <= 0 语义正好相反（返回空串，并把非空原文标记成「已截断」）——
// 两个函数名字相近、边界相反，改动其中任何一个之前先看清楚包名；
// 本次刻意不统一（会牵动两边一堆断言），只在两侧各留一条用例把各自的契约钉死。
// 本包的合约由 TestTruncateTextBounds 守住。
func truncateText(text string, max int) string {
	if max <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "…"
}

// finishEnvironment 收尾环境信息：说明按预算截断，危害名汇总回 card.Hazards。
// 逐条列表与状态行里的「环境危害」必须同源，否则文本回退里会出现列表中没有的危害名。
func finishEnvironment(card PlanetCard) PlanetCard {
	card.Biome = truncateText(card.Biome, maxNameRunes)
	card.BiomeDesc = truncateText(card.BiomeDesc, maxBiomeDescRunes)
	names := make([]string, 0, len(card.EnvItems))
	for i := range card.EnvItems {
		card.EnvItems[i].Name = truncateText(strings.TrimSpace(card.EnvItems[i].Name), maxNameRunes)
		card.EnvItems[i].Description = truncateText(card.EnvItems[i].Description, maxHazardDescRunes)
		names = append(names, card.EnvItems[i].Name)
	}
	if len(names) > 0 {
		card.Hazards = strings.Join(names, "、")
	}
	return card
}

// BuildLocalizedPlanetCard 在 BuildPlanetCard 的基础上补齐环境信息的中文，是 /planet 唯一的入口
// （卡片与文本回退都吃它返回的同一份视图模型，见 format.go 的 FormatPlanetText）。
//
// 送译段落与顺序固定：群系名、群系说明、每条真实危害的名称与说明（None 占位不送译）。
// 上游没给的内容以空串照送——翻译层对不含拉丁字母的段落直接原样返回，不会真的发请求。
// 「没翻动」的判据统一走 translate.Moved：原文本来就是中文（或空串）时它恒为 false 且不算故障，
// 免得每条中文环境信息都被挂上一句「翻译暂不可用」的假告警。
// （补充实测：273/273 颗星球的 biome.description 都非空，上游 schema 允许空串但当前不存在；
// 空段无从替换，判据本身也把它排除在 missed 之外，所以既不会漏报也不会误报。）
//
// 翻译失败（err 非空、段数不符、某段没翻动）时保留英文原文并写上 environmentNote。
// 段落对应关系始终按下标回填，段数不符时整块回退英文，不做错位替换
// （把危害说明当成群系名填进去比显示英文更糟）。
func BuildLocalizedPlanetCard(ctx context.Context, p hd2.Planet, fetchedAt time.Time, stale bool, trans translate.Translator) PlanetCard {
	card := BuildPlanetCard(p, fetchedAt, stale)
	real := hazardList(p.Hazards)
	card.EnvItems = make([]EnvItem, 0, len(real))
	for _, h := range real {
		card.EnvItems = append(card.EnvItems, EnvItem{
			Name:        strings.TrimSpace(h.Name),
			Description: cleanUnofficial(h.Description),
		})
	}
	if trans == nil {
		// 没有翻译层（translate.enabled=false）时环境信息保持英文，也不写说明文案。
		return finishEnvironment(card)
	}

	texts := make([]string, 0, 2+len(card.EnvItems)*2)
	texts = append(texts, strings.TrimSpace(p.Biome.Name), cleanUnofficial(p.Biome.Description))
	for _, item := range card.EnvItems {
		texts = append(texts, item.Name, item.Description)
	}

	translated, err := trans.Translate(ctx, texts)
	if err != nil {
		log.Printf("/planet 环境信息翻译失败，保留英文 name=%s err=%v", p.Name, err)
	}
	if len(translated) != len(texts) {
		log.Printf("/planet 环境信息翻译层返回 %d 段，期望 %d 段，保留英文 name=%s", len(translated), len(texts), p.Name)
		card.EnvNote = environmentNote
		return finishEnvironment(card)
	}

	missed := 0
	// apply 把第 index 段译文回填到 dst：译文为空或与原文相同时保留原文（原文已在 dst 里）。
	apply := func(index int, dst *string) {
		source := strings.TrimSpace(texts[index])
		if !translate.NeedsTranslation(source) {
			// 上游没给这段内容，或这一段本来就是中文：翻译层根本没送它，
			// 回显的“译文”不是翻译结果，既不该回填也不该算「没翻动」。
			return
		}
		if !translate.Moved(source, translated[index]) {
			missed++
			return
		}
		// 译文回填前过一次 cleanUnofficial：后端偶尔会把原文里的 (UNOFFICIAL) 一起带回来，
		// 标记留在卡片上比英文原文更扎眼；顺带把换行与多余空白折叠成一个空格。
		*dst = cleanUnofficial(translated[index])
	}
	apply(0, &card.Biome)
	apply(1, &card.BiomeDesc)
	for i := range card.EnvItems {
		apply(2+i*2, &card.EnvItems[i].Name)
		apply(3+i*2, &card.EnvItems[i].Description)
	}
	if missed > 0 {
		card.EnvNote = environmentNote
	}
	return finishEnvironment(card)
}

// attackingNames 把「正在进攻的目标」编号翻成星球名（中文（English）），彼此用「、」隔开。
// 名字查不到时退回「#N」——宁可显示编号，也不要编一个名字（同 orders 包的 planetLabel 口径）。
func attackingNames(indexes []int, planets []hd2.Planet) string {
	names := make(map[int]string, len(planets))
	for _, p := range planets {
		names[p.Index] = p.Name
	}
	parts := make([]string, 0, len(indexes))
	for _, index := range indexes {
		name := strings.TrimSpace(names[index])
		if name == "" {
			parts = append(parts, fmt.Sprintf("#%d", index))
			continue
		}
		parts = append(parts, plugutil.PlanetDisplayName(name))
	}
	return strings.Join(parts, "、")
}

// attackingText 拼这颗星球正在进攻的目标编号；上游用 []int 给编号（实测是相邻星球编号）。
// 这是没有星球列表时的兜底（BuildPlanetCard 阶段）；/planet 会在 FillPlanetExtras 里换成星球名。
func attackingText(indexes []int) string {
	if len(indexes) == 0 {
		return ""
	}
	parts := make([]string, 0, len(indexes))
	for _, index := range indexes {
		parts = append(parts, strconv.Itoa(index))
	}
	return strings.Join(parts, "、")
}

// clampPercent 把百分比夹在 0-100：上游偶尔给出 health > maxHealth 之类的脏数据。
func clampPercent(percent float64) float64 {
	if percent < 0 {
		return 0
	}
	if percent > 100 {
		return 100
	}
	return percent
}

// round1 四舍五入到一位小数：进度条的宽度与文案用同一个数值，不会出现「条 80.8%、字 81%」。
func round1(v float64) float64 {
	return math.Round(v*10) / 10
}

// complementPercent 返回 100 - value/total 的百分比（0-100，一位小数），即「还差多少」。
func complementPercent(value, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return round1(clampPercent(100 - float64(value)/float64(total)*100))
}

// sharePercent 返回 n/total 的百分比（0-100，一位小数）。
func sharePercent(n, total int) float64 {
	if total <= 0 {
		return 0
	}
	return round1(clampPercent(float64(n) / float64(total) * 100))
}

// ---- 战略情报分析 / 行动变量 / 兴趣点（用户 2026-09-18 要求，参照社区站点 HD2 真理部星图页的星球详情）----

// IntelRow 是「战略情报分析」里的一行：标签 + 数值 + 数值配色 class。
type IntelRow struct {
	Label string
	Value string
	Class string // 数值的配色 class（抵抗强度四档等）；空串表示按普通数值显示
}

// EffectChip 是一条行动变量（galactic effect）在卡片上的展示形态。
type EffectChip struct {
	Name        string // 中文名；对照表没收录时是「未知行动变量」（见 unknownEffectText）
	Description string // 中文说明；没有说明时为空串
	Category    string // 分类中文名；没有时为空串
	Negative    bool   // 作战限制类：卡片上用红色标出来
}

// POIChip 是兴趣点上的一枚标签。
type POIChip struct {
	Text  string // 标签文案（带 emoji）
	Class string // 配色 class；空串表示普通标签
}

// PlanetExtras 是单星球卡的补充数据。
//
// 这些数据都来自星球自身之外（其它接口），任何一项取不到都不该影响主卡——
// 缺了只会少几枚兴趣点标签或写一句「行动变量暂不可用」，不会让整张卡变成错误提示。
//
// 来源分工：Planets / Campaigns / Assignments / Stations 来自主数据源；
// Effects 来自补充源（主数据源的 /planets 没有 activeEffects 字段，只能另取一份）。
//
// EffectsKnown 刻意是三态：区分「补充源说这颗星球没有行动变量」与「补充源没取到」——
// 卡片上分别写「暂无已知行动变量」与「行动变量信息暂不可用」。把后者说成前者就是在编数据。
type PlanetExtras struct {
	Planets      []hd2.Planet       // 全量星球：用来找「敌军反攻」是从哪颗星球来的
	Campaigns    []hd2.Campaign     // 进行中的战役：兴趣点里的「战役进行中」
	Assignments  []hd2.Assignment   // 重要指令：兴趣点里的「重要指令目标」
	Stations     []hd2.SpaceStation // 民主空间站：兴趣点里的「DSS 停靠中」
	Effects      []hd2.PlanetEffect // 各星球的行动变量（补充源）
	EffectsKnown bool               // 补充源是否取到（false 时写「暂不可用」而不是「暂无」）
}

// effectsEmptyText 是补充源明确表示「这颗星球没有行动变量」时的文案，用参考站的原话。
const effectsEmptyText = "暂无已知行动变量。"

// effectsUnknownText 是补充源没取到时的文案：与「暂无」分开说，不把「不知道」写成「没有」。
// 文本回退会在这句前面加「行动变量：」，所以这里不再重复那四个字。
const effectsUnknownText = "暂不可用（补充数据源未取到）。"

// unknownEffectText 是「ID 查不到中文名」时的占位标签。
//
// 刻意不写「效果 #1272」这类编号：群里没人认得编号，摆在卡上只是噪声（参考站对这类效果是直接不显示的）。
// 但也刻意不整条丢掉——上游报了它，就说明这颗星球确实带着一条我们不认识的效果，
// 写一句「未知行动变量」比假装没有更有用。同一颗星球上多条未知效果只占一个位置。
const unknownEffectText = "未知行动变量"

// FillPlanetExtras 把补充数据填进单星球卡：行动变量区块与兴趣点区块。
// 星球自身的信息（含战略情报分析）在 BuildPlanetCard / BuildLocalizedPlanetCard 里已经算完，
// 这里只补「必须有外部数据才说得出口」的那两块。
func FillPlanetExtras(card PlanetCard, p hd2.Planet, extras PlanetExtras) PlanetCard {
	// 「进攻目标」在 BuildPlanetCard 里只能写编号（那时手里还没有别的星球），这里补上星球名：
	// 卡面上一行「编号 173」在群里没人能对上号。取不到那颗星球时 attackingNames 会退回编号。
	if len(p.Attacking) > 0 {
		card.Attacking = attackingNames(p.Attacking, extras.Planets)
	}
	card.EffectChips = planetEffectChips(p.Index, extras)
	if len(card.EffectChips) == 0 {
		if extras.EffectsKnown {
			card.EffectNote = effectsEmptyText
		} else {
			card.EffectNote = effectsUnknownText
		}
	}
	card.POIs = planetPOIs(p, extras)
	return card
}

// planetIntel 组装「战略情报分析」四行：解放度 / 抵抗度 / 星球血量 / 玩家数量。
//
// 口径对齐参考站：
//   - 解放度 = 100% − 星球血量百分比（实测敌方控制星球的 health 随我方推进下降，所以这个反推是对的）；
//   - 抵抗度 = 每秒回复量 ÷ 星球血量上限 × 3600 × 100，也就是「每小时恢复总血量的百分之几」。
//     这个换算与参考站数据里的 resistance 字段逐颗星球完全一致（实测 273/273），
//     所以卡片上直接算得出来，不必再依赖一个我们没有的数据字段；
//   - 星球血量与玩家数量原样展示（玩家数为 0 时写「—」，参考站也是这么处理的）。
func planetIntel(p hd2.Planet) []IntelRow {
	rows := make([]IntelRow, 0, 4)
	rows = append(rows, IntelRow{Label: "解放度", Value: liberationText(p)})
	resistText, resistClass := resistanceText(p)
	rows = append(rows, IntelRow{Label: "抵抗度", Value: resistText, Class: resistClass})
	rows = append(rows, IntelRow{Label: "星球血量", Value: healthPercentText(p)})
	// 玩家数量与卡面顶栏的「在线士兵」同源，这里按参考站再列一行：详情区不必回头去看顶栏。
	players := plugutil.DashText
	if p.Statistics.PlayerCount > 0 {
		players = plugutil.FormatInt(p.Statistics.PlayerCount)
	}
	rows = append(rows, IntelRow{Label: "玩家数量", Value: players})
	return rows
}

// liberationPercent 返回解放度百分比（0-100，不四舍五入）。
// 上游偶尔给出 health > maxHealth 的脏数据，所以最后夹一次区间。
func liberationPercent(p hd2.Planet) float64 {
	if p.MaxHealth <= 0 {
		return 0
	}
	return clampPercent(100 - float64(p.Health)/float64(p.MaxHealth)*100)
}

// liberationText 给「解放度」那一行：
//   - 已解放：卡面头部就是这么标的，或者血量拉满（≥99.9%）；
//   - 超级地球控制中：我方控制但卡面头部另有说法（例如正在挨打的防守战），
//     这时候说「0.00%」会让人以为这颗星球还没拿下来；
//   - 其余：两位小数的解放度，敌方控制的星球就是它在慢慢涨。
func liberationText(p hd2.Planet) string {
	percent := liberationPercent(p)
	if planetStatusText(p) == "已解放" || percent >= 99.9 {
		return "已解放"
	}
	if !isEnemyOwner(p.CurrentOwner) && percent <= 0.01 {
		return "超级地球控制中"
	}
	return fmt.Sprintf("%.2f%%", percent)
}

// 抵抗强度四档的配色 class；阈值与参考站一致（低 ≤1.99 / 中 2–2.99 / 高 3–3.99 / 极高 ≥4）。
const (
	resistClassNone = "res-none"
	resistClassLow  = "res-low"
	resistClassMid  = "res-mid"
	resistClassHigh = "res-high"
	resistClassMax  = "res-max"
)

// resistancePercentPerHour 把每秒回复量换算成「每小时恢复星球总血量的百分之几」。
// 换算结果保留两位小数：上游给的是浮点，卡片上再多的小数位也没有意义。
func resistancePercentPerHour(p hd2.Planet) float64 {
	if p.MaxHealth <= 0 || p.RegenPerSecond <= 0 {
		return 0
	}
	return math.Round(p.RegenPerSecond/float64(p.MaxHealth)*3600*100*100) / 100
}

// resistanceText 拼「抵抗度」那一行：数值 + 强度词，并给出配色 class。
// 数值按最短形式显示（1.5 / 2 / 0.75），不补无意义的零——参考站也是这么显示的。
func resistanceText(p hd2.Planet) (string, string) {
	percent := resistancePercentPerHour(p)
	if percent <= 0 {
		return "无", resistClassNone
	}
	class := resistClassLow
	switch {
	case percent < 2:
		class = resistClassLow
	case percent < 3:
		class = resistClassMid
	case percent < 4:
		class = resistClassHigh
	default:
		class = resistClassMax
	}
	word := map[string]string{
		resistClassLow:  "低",
		resistClassMid:  "中",
		resistClassHigh: "高",
		resistClassMax:  "极高",
	}[class]
	return fmt.Sprintf("%s%% / 小时（%s）", strconv.FormatFloat(percent, 'f', -1, 64), word), class
}

// healthPercentText 拼「星球血量」那一行：「100% · 1,000,000 / 1,000,000」。
// 百分比取整（参考站口径），血量用千分位；上游没给上限时写「—」，不写一个算不出来的百分比。
func healthPercentText(p hd2.Planet) string {
	if p.MaxHealth <= 0 {
		return plugutil.DashText
	}
	percent := int64(math.Round(float64(p.Health) / float64(p.MaxHealth) * 100))
	return fmt.Sprintf("%d%% · %s", percent, plugutil.HealthText(p.Health, p.MaxHealth))
}

// planetEffectChips 把一颗星球的行动变量 ID 翻成卡片上的标签。
//
// 去重分两层：同一个 ID 重复出现只留一条（上游实测会重复），同名的不同 ID 也只留一条
// ——行动变量常有「基础 id 与变体 id」两个编号（掠食变种 1243/1245、炽灼部队 1248/1249），
// 名字一样、说明一样，列两遍只是把版面撑长（参考站也按名字去重）。
// 查不到中文名的（含只有内部代号的新效果）合并成一条「未知行动变量」，不显示编号。
func planetEffectChips(index int, extras PlanetExtras) []EffectChip {
	chips := make([]EffectChip, 0, 4)
	seenID := make(map[int]bool, 4)
	seenName := make(map[string]bool, 4)
	for _, effect := range extras.Effects {
		if effect.PlanetIndex != index || seenID[effect.EffectID] {
			continue
		}
		seenID[effect.EffectID] = true
		info, ok := hd2.GalacticEffectOf(effect.EffectID)
		name := strings.TrimSpace(info.Name)
		if !ok || name == "" {
			// 对照表与别名表都没收录：合并成一条占位标签，不猜名字、也不显示编号。
			if seenName[unknownEffectText] {
				continue
			}
			seenName[unknownEffectText] = true
			chips = append(chips, EffectChip{Name: unknownEffectText})
			continue
		}
		if seenName[name] {
			continue
		}
		seenName[name] = true
		chips = append(chips, EffectChip{
			Name:        name,
			Description: info.Desc,
			Category:    info.Category,
			Negative:    info.Negative,
		})
	}
	return chips
}

// campaignTypeDefense 是战役类型里的「入侵防御战」，其余取值一律按解放战役处理。
// 实测（2026-09-18，共 37 场战役）：type 4 的两场都与 planetEvents 里的防守事件一一对应，
// 另外 35 场全是 type 0。上游没有公布枚举含义，这里只按实测用法翻译，未收录的取值不猜。
const campaignTypeDefense = 4

// planetPOIs 组装兴趣点标签，五类口径对齐参考站（写不出结论的一律不写）：
//   - 🎯 重要指令目标：某条重要指令任务的 valueType 12 就是这颗星球的编号；
//   - ⚔️ 战役进行中：这颗星球在战役列表里，按战役类型分「入侵防御战」与「解放战役」
//     （有防守事件也算防御战：事件与战役类型同源，任一先到都能得出结论）；
//   - 🛰️ DSS 停靠中：空间站的停靠星球就是它；
//   - ⚠️ 敌军反攻中：另一颗星球正在进攻它（来源星球见 counterAttackSource）；
//   - 🌍 星区：始终显示，作为最后一条兜底信息。
func planetPOIs(p hd2.Planet, extras PlanetExtras) []POIChip {
	pois := make([]POIChip, 0, 5)
	if isMOTarget(p.Index, extras.Assignments) {
		pois = append(pois, POIChip{Text: "🎯 重要指令目标", Class: "poi-mo"})
	}
	switch campaignKind(p, extras.Campaigns) {
	case campaignDefense:
		pois = append(pois, POIChip{Text: "⚔️ 入侵防御战进行中", Class: "poi-camp"})
	case campaignLiberation:
		pois = append(pois, POIChip{Text: "⚔️ 解放战役进行中", Class: "poi-camp"})
	}
	if dssDocked(p.Index, extras.Stations) {
		pois = append(pois, POIChip{Text: "🛰️ DSS 民主空间站停靠中", Class: "poi-dss"})
	}
	if source, ok := counterAttackSource(p.Index, extras.Planets); ok {
		pois = append(pois, POIChip{Text: "⚠️ 敌军反攻中 · 来自 " + source, Class: "poi-atk"})
	}
	pois = append(pois, POIChip{Text: "🌍 星区：" + sectorText(p.Sector)})
	return pois
}

// 战役种类：兴趣点的文案按它挑。
const (
	campaignNone = iota
	campaignLiberation
	campaignDefense
)

// campaignKind 判断这颗星球当前在打哪一类战役。
// 防守战优先：事件与战役类型同源，只要有一边说它在防守就按防守说——
// 把「正在挨打」写成「解放战役进行中」是最容易让人误判的一句话。
func campaignKind(p hd2.Planet, campaigns []hd2.Campaign) int {
	if p.Event != nil {
		return campaignDefense
	}
	kind := campaignNone
	for _, c := range campaigns {
		if c.Planet.Index != p.Index {
			continue
		}
		if c.Type == campaignTypeDefense {
			return campaignDefense
		}
		kind = campaignLiberation
	}
	return kind
}

// dssDocked 判断民主空间站是不是停在这颗星球上。
// 上游没给停靠星球（PlanetIndex 为 0）时不算停靠，避免「站停在编号 0 的星球上」这种假标签。
func dssDocked(index int, stations []hd2.SpaceStation) bool {
	for _, s := range stations {
		if s.PlanetIndex != 0 && s.PlanetIndex == index {
			return true
		}
	}
	return false
}

// counterAttackSource 找「正在进攻这颗星球的星球」，返回它的展示名。
//
// 上游的 Planet.Attacking 是**发起方**字段：这颗星球正在进攻的目标编号。
// 实测 Peacock（编号 216）的 attacking=[268]，而 268 正是当时被打的那颗；
// 所以要反过来找「谁的 attacking 里含它」。多颗来源时取编号最小的那颗——
// 同一份数据每次给出同一枚标签，不会因为遍历顺序变了就换一个名字。
func counterAttackSource(index int, planets []hd2.Planet) (string, bool) {
	found := false
	best := hd2.Planet{}
	for _, q := range planets {
		if q.Index == index {
			// 自己打自己不是有效情报（上游脏数据），跳过。
			continue
		}
		hit := false
		for _, target := range q.Attacking {
			if target == index {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		if !found || q.Index < best.Index {
			best, found = q, true
		}
	}
	if !found {
		return "", false
	}
	return plugutil.PlanetDisplayName(best.Name), true
}

// taskValueTypePlanet 与 plugins/orders 的 valueTypePlanet 是同一个口径：
// 上游任务用 valueType 12 携带星球索引，0 表示「不限星球」而不是编号 0 的星球。
const taskValueTypePlanet = 12

// isMOTarget 判断这颗星球是不是某条重要指令任务的目标。
func isMOTarget(index int, assignments []hd2.Assignment) bool {
	if index <= 0 {
		return false
	}
	for _, assignment := range assignments {
		for _, task := range assignment.Tasks {
			target, ok := task.ValueOf(taskValueTypePlanet)
			if ok && target != 0 && int(target) == index {
				return true
			}
		}
	}
	return false
}
