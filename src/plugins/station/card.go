package station

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"hd2_bot/src/hd2"
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

// StationAction 是空间站上的一项战术行动。
type StationAction struct {
	Name        string // 中文译名（对照表命中时）或上游英文原名
	Icon        string // 战术图标素材逻辑名；未命中时为空串（模板不显示图标）
	Status      string // 状态文案；未收录的枚举值显示「状态 N」
	StatusClass string // 状态标签的配色 class
}

// stationNameFallback 是上游没给空间站名字时的占位名。
// 实测 v2 端点不给 name，而卡片上的标题只剩这一个字段——留「—」看着像坏数据，这里补一个中性名字。
const stationNameFallback = "民主空间站"

// StationItem 是一座民主空间站。
type StationItem struct {
	Name        string          // 上游 name；实测 v2 端点没有给名字，此时显示 stationNameFallback
	ElectionEnd string          // 选举 / 跃迁截止时间；上游没给时「未知」
	Actions     []StationAction // 战术行动列表
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
		actions := make([]StationAction, 0, len(s.TacticalActions))
		for _, a := range s.TacticalActions {
			actions = append(actions, buildStationAction(a))
		}
		items = append(items, StationItem{
			Name:        plugutil.DefaultText(s.Name, stationNameFallback),
			ElectionEnd: endTimeText(s.ElectionEnd, loc),
			Actions:     actions,
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

// buildStationAction 把一项战术行动转成卡片上的一行。
// 译名、图标与状态文案全部走 plugutil：这些口径必须与推送卡片逐字相同。
func buildStationAction(a hd2.TacticalAction) StationAction {
	name, icon := plugutil.TacticalActionName(a.Name)
	name = plugutil.DefaultText(name, "未命名行动")
	status, class := plugutil.TacticalStatus(a.Status)
	return StationAction{
		Name:        name,
		Icon:        icon,
		Status:      status,
		StatusClass: class,
	}
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
