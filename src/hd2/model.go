// Package hd2 是《绝地潜兵 2》数据领域层：负责取数、限流、缓存与降级，不依赖 Telegram。
package hd2

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// War 银河战争全局状态。
// 注意：上游这几个字段的口径比较随意，展示时请以 Result.FetchedAt 作为「数据时间」，
// 并且不要用 Ended/Now 做倒计时（实测值分别是 2028 年和 1972 年）。
// 本类型只用于解码上游 JSON，不要把它 marshal 回上游格式：时间容错解析是单向的。
type War struct {
	Started time.Time `json:"started"`
	// Ended 上游给的结束时间不可信（2026-09-16 实测为 2028-02-08），只做透传，
	// 展示层不得用它做倒计时或「战争还剩多久」。
	Ended time.Time `json:"ended"`
	// Now 同理不可信（实测为 1972-08-02），只做透传；
	// 展示层要「数据时间」请用 Result.FetchedAt，不要用这里的 Now。
	Now              time.Time  `json:"now"`
	ClientVersion    string     `json:"clientVersion"`
	Factions         []string   `json:"factions"`
	ImpactMultiplier float64    `json:"impactMultiplier"`
	Statistics       Statistics `json:"statistics"`
}

// UnmarshalJSON 容忍 started/ended/now 为空串或 null（上游历史行为）：这类值表示上游确实没有给值，
// 一律按零值处理；但格式非法（例如 "now":"oops"）会返回错误，
// 让上层走快照降级，而不是静默展示一个错误时间。
func (w *War) UnmarshalJSON(b []byte) error {
	type warAlias War
	raw := struct {
		*warAlias
		Started json.RawMessage `json:"started"`
		Ended   json.RawMessage `json:"ended"`
		Now     json.RawMessage `json:"now"`
	}{warAlias: (*warAlias)(w)}
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("war 字段格式非法：%w", err)
	}
	started, err := flexibleTime(raw.Started)
	if err != nil {
		return fmt.Errorf("started %w", err)
	}
	ended, err := flexibleTime(raw.Ended)
	if err != nil {
		return fmt.Errorf("ended %w", err)
	}
	now, err := flexibleTime(raw.Now)
	if err != nil {
		return fmt.Errorf("now %w", err)
	}
	w.Started, w.Ended, w.Now = started, ended, now
	return nil
}

// flexibleTime 解析值类型时间字段：空串、null、缺失都表示「上游没有给值」，一律返回零值；
// 非字符串值（数字、对象等）与非法 RFC3339 属于格式异常，返回错误交由上层降级处理。
func flexibleTime(raw json.RawMessage) (time.Time, error) {
	parsed, err := flexibleTimePtr(raw)
	if err != nil || parsed == nil {
		return time.Time{}, err
	}
	return *parsed, nil
}

// flexibleTimePtr 解析指针类型时间字段：空串、null、缺失都表示「上游没有给值」，返回 nil；
// 非字符串值（数字、对象等）与非法 RFC3339 属于格式异常，返回错误交由上层降级处理。
func flexibleTimePtr(raw json.RawMessage) (*time.Time, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("时间字段格式非法：%s", raw)
	}
	if s == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, fmt.Errorf("时间字段格式非法：%s", s)
	}
	return &parsed, nil
}

// Statistics 战争累计统计，json tag 与上游字段保持一致；
// 其中上游把 accuracy 拼成了 accurracy（少一个 a），本类型两种拼写都兼容。
// 本类型只用于解码上游 JSON，不要把它 marshal 回上游格式。
type Statistics struct {
	MissionsWon        int64 `json:"missionsWon"`
	MissionsLost       int64 `json:"missionsLost"`
	MissionTime        int64 `json:"missionTime"`
	TerminidKills      int64 `json:"terminidKills"`
	AutomatonKills     int64 `json:"automatonKills"`
	IlluminateKills    int64 `json:"illuminateKills"`
	BulletsFired       int64 `json:"bulletsFired"`
	BulletsHit         int64 `json:"bulletsHit"`
	TimePlayed         int64 `json:"timePlayed"`
	Deaths             int64 `json:"deaths"`
	Revives            int64 `json:"revives"`
	Friendlies         int64 `json:"friendlies"`
	MissionSuccessRate int64 `json:"missionSuccessRate"`
	// Accurracy 命中率；上游当前返回 accuracy，历史与文档中是 accurracy（少一个 a），
	// 两种拼写由 UnmarshalJSON 兼容，对外字段名保持 Accurracy 不变。
	Accurracy   float64 `json:"accurracy"`
	PlayerCount int64   `json:"playerCount"`
}

// UnmarshalJSON 兼容上游两种拼写：accurracy 非 0 时优先，为 0 或缺失时回落 accuracy。
// 上游当前返回 accuracy，历史与文档中出现的是 accurracy，缺一个都会导致命中率静默变成 0。
func (s *Statistics) UnmarshalJSON(b []byte) error {
	type statAlias Statistics
	raw := struct {
		*statAlias
		AccurracyAlt *float64 `json:"accuracy"`
	}{statAlias: (*statAlias)(s)}
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("statistics 字段格式非法：%w", err)
	}
	if raw.AccurracyAlt != nil && s.Accurracy == 0 {
		s.Accurracy = *raw.AccurracyAlt
	}
	return nil
}

// Planet 星球当前状态；字段按上游实际的 Key 命名。
type Planet struct {
	Index          int          `json:"index"`
	Name           string       `json:"name"`
	Sector         string       `json:"sector"`
	Biome          Biome        `json:"biome"`
	Hazards        []Hazard     `json:"hazards"`
	CurrentOwner   string       `json:"currentOwner"`
	InitialOwner   string       `json:"initialOwner"`
	Health         int64        `json:"health"`
	MaxHealth      int64        `json:"maxHealth"`
	RegenPerSecond float64      `json:"regenPerSecond"`
	Waypoints      []int        `json:"waypoints"`
	Attacking      []int        `json:"attacking"`
	Event          *PlanetEvent `json:"event"`
	Statistics     PlanetStats  `json:"statistics"`
}

// Biome 星球地貌。
type Biome struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Hazard 星球环境危害。
type Hazard struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// PlanetStats 星球维度的统计信息。
type PlanetStats struct {
	PlayerCount int64 `json:"playerCount"`
}

// PlanetEvent 星球上正在发生的事件（例如防守战）。
type PlanetEvent struct {
	ID         int64      `json:"id"`
	EventType  int        `json:"eventType"`
	Faction    string     `json:"faction"`
	Health     int64      `json:"health"`
	MaxHealth  int64      `json:"maxHealth"`
	StartTime  time.Time  `json:"startTime"`
	EndTime    *time.Time `json:"endTime"`
	CampaignID int64      `json:"campaignId"`
	// PlanetIndex / PlanetName 在 /planet-events 的响应里没有：事件嵌在星球对象中，
	// 由 DecodeEvents 从所属星球补进来，仅用于展示「哪颗星球出事了」。
	PlanetIndex int    `json:"planetIndex"`
	PlanetName  string `json:"-"`
}

// UnmarshalJSON 把 startTime/endTime 走容错解析：空串或 null 表示上游没有给值，取零值/nil；
// 格式非法则报错。planets 是最大暴露面，单个星球的坏时间字段不应该拖垮整批星球。
func (e *PlanetEvent) UnmarshalJSON(b []byte) error {
	type eventAlias PlanetEvent
	raw := struct {
		*eventAlias
		StartTime json.RawMessage `json:"startTime"`
		EndTime   json.RawMessage `json:"endTime"`
	}{eventAlias: (*eventAlias)(e)}
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("planetEvent 字段格式非法：%w", err)
	}
	startTime, err := flexibleTime(raw.StartTime)
	if err != nil {
		return fmt.Errorf("startTime %w", err)
	}
	endTime, err := flexibleTimePtr(raw.EndTime)
	if err != nil {
		return fmt.Errorf("endTime %w", err)
	}
	e.StartTime, e.EndTime = startTime, endTime
	return nil
}

// Campaign 进行中的战役。
type Campaign struct {
	ID      int64     `json:"id"`
	Planet  PlanetRef `json:"planet"`
	Type    int       `json:"type"`
	Count   int       `json:"count"`
	Faction string    `json:"faction"`
}

// PlanetRef 战役中引用的星球。
type PlanetRef struct {
	Index int    `json:"index"`
	Name  string `json:"name"`
}

// Assignment 重要指令（Major Order）。
type Assignment struct {
	ID          int64      `json:"id"`
	Title       string     `json:"title"`
	Briefing    string     `json:"briefing"`
	Description string     `json:"description"`
	Tasks       []Task     `json:"tasks"`
	Reward      Reward     `json:"reward"`
	Expiration  *time.Time `json:"expiration"`
	Progress    []int64    `json:"progress"`
	Flags       int        `json:"flags"`
}

// UnmarshalJSON 把 expiration 走容错解析：空串或 null 表示上游没有给值，取 nil；
// 格式非法则报错。
func (a *Assignment) UnmarshalJSON(b []byte) error {
	type assignmentAlias Assignment
	raw := struct {
		*assignmentAlias
		Expiration json.RawMessage `json:"expiration"`
	}{assignmentAlias: (*assignmentAlias)(a)}
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("assignment 字段格式非法：%w", err)
	}
	expiration, err := flexibleTimePtr(raw.Expiration)
	if err != nil {
		return fmt.Errorf("expiration %w", err)
	}
	a.Expiration = expiration
	return nil
}

// Task 重要指令中的一条任务。
type Task struct {
	Type       int     `json:"type"`
	Values     []int64 `json:"values"`
	ValueTypes []int   `json:"valueTypes"`
}

// ValueOf 取这条任务里某个 valueType 对应的值。
// 上游的 values 与 valueTypes 是两个等长数组，靠下标对齐（valueTypes[i] 说明 values[i] 是什么）；
// 长度不一致（上游抽风）时取不到就返回 false，绝不猜一个位置。
//
// 这是全项目唯一的对位实现：plugins/orders 与 plugins/planets 都调它，
// 免得两边各写一份、其中一份哪天忘了检查下标。
func (t Task) ValueOf(valueType int) (int64, bool) {
	for i, vt := range t.ValueTypes {
		if vt != valueType {
			continue
		}
		if i >= len(t.Values) {
			return 0, false
		}
		return t.Values[i], true
	}
	return 0, false
}

// Reward 重要指令奖励。
type Reward struct {
	Type   int   `json:"type"`
	Amount int   `json:"amount"`
	ID32   int64 `json:"id32"`
}

// Dispatch 战役简报（游戏内通知文本）。
type Dispatch struct {
	ID        int64     `json:"id"`
	Published time.Time `json:"published"`
	Type      int       `json:"type"`
	Message   string    `json:"message"`
}

// UnmarshalJSON 把 published 走容错解析：空串或 null 表示上游没有给值，取零值；
// 格式非法则报错。
func (d *Dispatch) UnmarshalJSON(b []byte) error {
	type dispatchAlias Dispatch
	raw := struct {
		*dispatchAlias
		Published json.RawMessage `json:"published"`
	}{dispatchAlias: (*dispatchAlias)(d)}
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("dispatch 字段格式非法：%w", err)
	}
	published, err := flexibleTime(raw.Published)
	if err != nil {
		return fmt.Errorf("published %w", err)
	}
	d.Published = published
	return nil
}

// SpaceStation 民主空间站；S1 只需要这些展示字段。
type SpaceStation struct {
	ID32  int64  `json:"id32"`
	ID    string `json:"id"`
	Name  string `json:"name"`
	Flags int    `json:"flags"`
	// PlanetIndex 是空间站当前所在星球编号；上游实测该字段可能是问号或缺失，此时为 0（0 也表示「没有位置信息」）。
	// 它没有 json tag，改由 UnmarshalJSON 容错解析：实测响应里 planet 是字符串 "?"，
	// 直接声明成 int 会让整批空间站解码失败，而推送的变化检测只关心这个值变没变。
	PlanetIndex int        `json:"-"`
	ElectionEnd *time.Time `json:"electionEnd"`
	// PlanetName / PlanetSector 是停靠星球的英文原名与星区，只在 planet 是完整对象形态时才有
	// （实测 /api/v2/space-stations 会把整颗星球嵌进来）。卡片用它们写「当前停靠」一行；
	// 取不到时留空，由卡片写「位置未知」，而不是拿编号冒充名字。
	PlanetName      string           `json:"-"`
	PlanetSector    string           `json:"-"`
	TacticalActions []TacticalAction `json:"tacticalActions"`
}

// UnmarshalJSON 把 electionEnd 与 planet 走容错解析：electionEnd 为空串或 null 表示上游没有给值，
// 取 nil，格式非法则报错；planet 可能是数字、数字字符串或 "?"，非法值一律取 0 且不报错（见 flexibleIndex）。
func (s *SpaceStation) UnmarshalJSON(b []byte) error {
	type stationAlias SpaceStation
	raw := struct {
		*stationAlias
		ElectionEnd json.RawMessage `json:"electionEnd"`
		Planet      json.RawMessage `json:"planet"`
	}{stationAlias: (*stationAlias)(s)}
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("spaceStation 字段格式非法：%w", err)
	}
	electionEnd, err := flexibleTimePtr(raw.ElectionEnd)
	if err != nil {
		return fmt.Errorf("electionEnd %w", err)
	}
	s.ElectionEnd = electionEnd
	s.PlanetIndex, s.PlanetName, s.PlanetSector = flexiblePlanet(raw.Planet)
	return nil
}

// flexibleIndex 解析「本该是编号、但上游可能换形态」的字段：数字、数字字符串、以及带 index 的对象都认；
// null / 缺失 / "?" / 认不出的内容一律取 0，且不返回错误。
//
// 为什么只给空间站的 planet 做这层容错：本项目实测过的编号字段里，只有它同时出现过三种形态——
// 字符串 "?"（早先实测）、{"index":100,…} 整个星球对象（2026-09 实测）、以及数字/数字字符串；
// 其它编号字段（星球 index、战役 id、空间站 id32 等）实测都是数字，各自坏掉时由调用方的
// 兜底文案承接即可，不该在这里替它们猜。
//
// 与 flexibleTime 的区别：时间脏了必须报错（让上层走快照降级），因为展示一个错误时间会误导人；
// 而空间站的 planet 认不出来只等于「没有位置信息」（0），把它当错误会让整批空间站解码失败。
func flexibleIndex(raw json.RawMessage) int {
	index, _, _ := flexiblePlanet(raw)
	return index
}

// flexiblePlanet 解析空间站的 planet 字段，返回编号、星球英文原名与星区。
//
// 上游这个字段出现过三种形态：数字、数字字符串，以及完整星球对象
// （实测 2026-09-18 的 /api/v2/space-stations 给的是对象，带 index / name / sector）。
// 只有对象形态才有名字与星区，另外两种一律留空——卡片据此写「位置未知」，而不是拿编号冒充名字。
// 认不出来不是错误：那只是「没有位置信息」，详见 flexibleIndex 的注释。
func flexiblePlanet(raw json.RawMessage) (int, string, string) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return 0, "", ""
	}
	switch trimmed[0] {
	case '{':
		var obj struct {
			Index  json.RawMessage `json:"index"`
			Name   string          `json:"name"`
			Sector string          `json:"sector"`
		}
		if err := json.Unmarshal(trimmed, &obj); err != nil {
			return 0, "", ""
		}
		// index 本身可能又是数字字符串，递归复用同一套解析；嵌套深度由 encoding/json 的上限兜住。
		return flexibleIndex(obj.Index), strings.TrimSpace(obj.Name), strings.TrimSpace(obj.Sector)
	case '"':
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return 0, "", ""
		}
		value, err := strconv.Atoi(strings.TrimSpace(text))
		if err != nil {
			return 0, "", ""
		}
		return value, "", ""
	}
	var value int
	if err := json.Unmarshal(trimmed, &value); err == nil {
		return value, "", ""
	}
	var f float64
	if err := json.Unmarshal(trimmed, &f); err == nil {
		return int(f), "", ""
	}
	return 0, "", ""
}

// TacticalAction 空间站上的战术行动。
//
// 字段按上游 /api/v2/space-stations 的实测响应（2026-09-18）补齐：
// 卡片要画「募捐中 / 已激活 / 冷却中」的阶段、募捐进度与冷却剩余，
// 这些都只能从下面这几个字段来，缺一个就画不出参考站那一栏。
type TacticalAction struct {
	ID32   int64  `json:"id32"`
	Name   string `json:"name"`
	Status int    `json:"status"`
	// Description / StrategicDescription 是上游给的行动说明；strategicDescription 里带
	// <span data-ah="1">…</span> 标记，展示前要清洗（复用 translate.CleanGameText）。
	Description          string `json:"description"`
	StrategicDescription string `json:"strategicDescription"`
	// EffectIDs 是这项行动关联的效果编号；上游实测「飞鹰风暴 = 1209/1212/1216」这类组合。
	EffectIDs []int `json:"effectIds"`
	// Costs 是募捐进度，上游实测每项行动只给一条。
	Costs []TacticalCost `json:"costs"`
	// StatusExpire 的含义随 Status 变：status=2（已激活）时是本次行动结束时刻，
	// status=3（冷却中）时是冷却结束时刻。上游可能给空串，故走容错解析（见 UnmarshalJSON）。
	StatusExpire *time.Time `json:"-"`
}

// TacticalCost 是战术行动的募捐进度（上游字段名照抄，含它自己的拼写）。
type TacticalCost struct {
	TargetValue              float64 `json:"targetValue"`
	CurrentValue             float64 `json:"currentValue"`
	DeltaPerSecond           float64 `json:"deltaPerSecond"`
	MaxDonationAmmount       float64 `json:"maxDonationAmmount"`
	MaxDonationPeriodSeconds float64 `json:"maxDonationPeriodSeconds"`
}

// UnmarshalJSON 把 statusExpire 走容错解析：空串或 null 表示上游没有给值，取 nil；格式非法则报错。
// 与 Assignment.expiration 同一套口径：坏时间字段不该让整批空间站解码失败。
func (a *TacticalAction) UnmarshalJSON(b []byte) error {
	type actionAlias TacticalAction
	raw := struct {
		*actionAlias
		StatusExpire json.RawMessage `json:"statusExpire"`
	}{actionAlias: (*actionAlias)(a)}
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("tacticalAction 字段格式非法：%w", err)
	}
	expire, err := flexibleTimePtr(raw.StatusExpire)
	if err != nil {
		return fmt.Errorf("statusExpire %w", err)
	}
	a.StatusExpire = expire
	return nil
}

// DecodeWar 解析战况响应。
func DecodeWar(b []byte) (*War, error) { return decodeOne[War](b, "war") }

// DecodePlanets 解析星球列表响应。
func DecodePlanets(b []byte) ([]Planet, error) { return decodeList[Planet](b, "planets") }

// DecodeCampaigns 解析战役列表响应。
func DecodeCampaigns(b []byte) ([]Campaign, error) { return decodeList[Campaign](b, "campaigns") }

// DecodeAssignments 解析重要指令响应；上游无进行中的指令时返回空切片。
func DecodeAssignments(b []byte) ([]Assignment, error) {
	return decodeList[Assignment](b, "assignments")
}

// DecodeDispatches 解析战役简报响应。
func DecodeDispatches(b []byte) ([]Dispatch, error) { return decodeList[Dispatch](b, "dispatches") }

// DecodeEvents 解析星球事件响应。
//
// 上游 /planet-events 返回的是「星球对象数组」（每个对象里嵌着 event），不是扁平的事件数组：
// 实测响应形如 [{"index":268,"name":"LUXURIANT",…,"event":{"id":5695,…}}]。
// 所以这里按星球解析，只保留带事件的星球，并把所属星球的编号与名字补进事件里
// （事件对象自身没有这两个字段），展示层才知道「哪颗星球出事了」。
// 没有事件的星球直接跳过；上游返回 [] 时给出空切片而不是 nil。
func DecodeEvents(b []byte) ([]PlanetEvent, error) {
	planets, err := decodeList[Planet](b, "planet-events")
	if err != nil {
		return nil, err
	}
	events := make([]PlanetEvent, 0, len(planets))
	for _, p := range planets {
		if p.Event == nil {
			continue
		}
		event := *p.Event
		event.PlanetIndex = p.Index
		event.PlanetName = p.Name
		events = append(events, event)
	}
	return events, nil
}

// DecodeStations 解析民主空间站响应。
func DecodeStations(b []byte) ([]SpaceStation, error) {
	return decodeList[SpaceStation](b, "space-stations")
}

// decodeOne 解析单个对象；缺字段时取零值，{} 同样静默返回零值对象，语义校验由上层负责。
// body 为 null 则直接报错：单对象端点返回 null 说明上游响应异常，不该当成空数据继续往下走。
// name 用于错误信息，与数据端点对齐。
func decodeOne[T any](b []byte, name string) (*T, error) {
	if string(bytes.TrimSpace(b)) == "null" {
		return nil, fmt.Errorf("解析 %s 数据失败：上游返回 null", name)
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("解析 %s 数据失败：%w", name, err)
	}
	return &v, nil
}

// decodeList 解析数组；上游返回 null 时统一成空切片，调用方无需判空。
// name 用于错误信息，与数据端点对齐。
func decodeList[T any](b []byte, name string) ([]T, error) {
	var v []T
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("解析 %s 数据失败：%w", name, err)
	}
	if v == nil {
		v = []T{}
	}
	return v, nil
}
