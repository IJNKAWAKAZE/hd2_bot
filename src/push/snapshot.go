// Package push 负责战况推送：把每一轮抓到的数据压成指纹、比较出变化、渲染成一条卡片推给群。
//
// 分工（详见 docs/superpowers/specs/2026-09-16-hd2-bot-s3-design.md §6、§7）：
//   - snapshot.go：Input → Snapshot（状态指纹，存 bbolt）
//   - detect.go：Snapshot + Input → []Event（纯函数，不碰网络与存储）
//   - card.go：[]Event → 卡片视图模型 + MarkdownV2 文本回退
//   - collector.go：一轮轮询的编排（取数 → 检测 → 去重 → 翻译 → 出图/文本 → 发送 → 写基线）
package push

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"hd2_bot/src/hd2"
)

const (
	// MaxDispatchIDs 是快照里保留的简报 id 条数上限，它现在只承担一件事：
	// 「与基线最新发布时间相同的那一批简报」的去重集（同一秒可能连发数条）。
	//
	// 判新的第一道关卡是 Snapshot.LatestDispatchPublished（时间），不是这个集合：
	// 上游实测共 1048 条简报（最近四条相隔 2~5 天），把 id 集合当全量基线会让同一份数据
	// 回灌时凭空报出 848 条「新简报」。200 条足够覆盖任何一次同秒批量发布，不必留全量。
	MaxDispatchIDs = 200
	// Baseline 是基线快照在状态文件里的名字（state 的快照桶）。
	Baseline = "push_baseline"
)

// Input 是一轮轮询取到的原始数据：检测变化需要正文与名字，所以本轮数据单独作为入参传进来，
// 快照里只存能代表「变了没有」的小指纹。
type Input struct {
	FetchedAt  time.Time
	Planets    []hd2.Planet
	Campaigns  []hd2.Campaign
	Stations   []hd2.SpaceStation
	Dispatches []hd2.Dispatch
}

// Snapshot 是一轮数据的指纹：可 JSON 序列化，存进 bbolt 的快照桶。
type Snapshot struct {
	FetchedAt time.Time `json:"fetchedAt"` // 本次抓取时间（UTC）；零值表示这一轮没有可用的抓取时间
	// LatestDispatchPublished 是本轮简报里最新的发布时间（UTC）；零值表示这一轮没拿到带时间的简报，
	// 此时 Detect 的新简报判定退化成「完全按 DispatchIDs 判新」（旧版本基线也是这个形态）。
	LatestDispatchPublished time.Time         `json:"latestDispatchPublished"`
	Owners                  map[string]string `json:"owners"`      // 星球编号 → currentOwner
	PlanetNames             map[string]string `json:"planetNames"` // 星球编号 → 上游英文名（事件标题补名用）
	Campaigns               map[string]string `json:"campaigns"`   // 战役 id → "<星球编号>|<type>"
	Stations                map[string]string `json:"stations"`    // 空间站 id32 → 签名
	// DispatchIDs 是最近 MaxDispatchIDs 条简报 id（倒序，新的在前），只用于挡同一发布时间的重复。
	DispatchIDs []int64 `json:"dispatchIds"`
}

// SnapshotOf 把一轮数据压成快照。
func SnapshotOf(in Input) Snapshot {
	snap := Snapshot{
		FetchedAt:   in.FetchedAt.UTC(),
		Owners:      make(map[string]string, len(in.Planets)),
		PlanetNames: make(map[string]string, len(in.Planets)),
		Campaigns:   make(map[string]string, len(in.Campaigns)),
		Stations:    make(map[string]string, len(in.Stations)),
	}
	for _, p := range in.Planets {
		key := strconv.Itoa(p.Index)
		snap.Owners[key] = p.CurrentOwner
		snap.PlanetNames[key] = p.Name
	}
	for _, c := range in.Campaigns {
		snap.Campaigns[strconv.FormatInt(c.ID, 10)] = campaignValue(c.Planet.Index, c.Type)
	}
	for _, s := range in.Stations {
		snap.Stations[strconv.FormatInt(s.ID32, 10)] = stationSignature(s)
	}

	// 简报按发布时间倒序（时间相同时按 id 倒序兜底），只留最近 MaxDispatchIDs 条。
	ordered := make([]hd2.Dispatch, len(in.Dispatches))
	copy(ordered, in.Dispatches)
	sort.SliceStable(ordered, func(i, j int) bool {
		if !ordered[i].Published.Equal(ordered[j].Published) {
			return ordered[i].Published.After(ordered[j].Published)
		}
		return ordered[i].ID > ordered[j].ID
	})
	if len(ordered) > MaxDispatchIDs {
		ordered = ordered[:MaxDispatchIDs]
	}
	snap.DispatchIDs = make([]int64, 0, len(ordered))
	for _, d := range ordered {
		snap.DispatchIDs = append(snap.DispatchIDs, d.ID)
	}
	// 最新发布时间取简报本体的时间，不受上面截断影响：它是下一轮判「有没有新简报」的第一道关卡。
	// 全为脏数据（发布时间都是零值）时保持零值，Detect 会退化成按 id 集合判新。
	if len(ordered) > 0 {
		snap.LatestDispatchPublished = ordered[0].Published.UTC()
	}
	return snap
}

// Empty 判断快照是否完全没有内容：首次运行或状态文件被清时用它区分「没有基线」与「基线是空的」。
//
// 判据是「所有字段都为零值」，而不是只看 FetchedAt：抓取时间来自编排层的时钟，
// 上游时钟异常、字段缺失或某个调用点忘了赋值时它可能是零值，若据此判「首次运行」，
// 推送会永久静默而且没有任何日志（基线每轮都被当成第一轮重写）。
func (s Snapshot) Empty() bool {
	return s.FetchedAt.IsZero() &&
		s.LatestDispatchPublished.IsZero() &&
		len(s.Owners) == 0 &&
		len(s.PlanetNames) == 0 &&
		len(s.Campaigns) == 0 &&
		len(s.Stations) == 0 &&
		len(s.DispatchIDs) == 0
}

// Marshal 序列化快照，供写入状态文件。
func (s Snapshot) Marshal() ([]byte, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("序列化推送快照失败：%w", err)
	}
	return raw, nil
}

// Unmarshal 解析快照；四个 map 一定不是 nil，调用方可以直接写入（否则第一次记变化会静默丢数据）。
func Unmarshal(raw []byte) (Snapshot, error) {
	var snap Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return Snapshot{}, fmt.Errorf("解析推送快照失败：%w", err)
	}
	if snap.Owners == nil {
		snap.Owners = map[string]string{}
	}
	if snap.PlanetNames == nil {
		snap.PlanetNames = map[string]string{}
	}
	if snap.Campaigns == nil {
		snap.Campaigns = map[string]string{}
	}
	if snap.Stations == nil {
		snap.Stations = map[string]string{}
	}
	return snap, nil
}

// campaignValue 把战役压成 "<星球编号>|<type>"：结束事件发生时战役已经消失，
// 星球与类型只能从上一轮的快照里恢复。
func campaignValue(index, ctype int) string {
	return strconv.Itoa(index) + "|" + strconv.Itoa(ctype)
}

// parseCampaignValue 还原战役指纹；结构不对时 ok 为 false。
func parseCampaignValue(value string) (index, ctype int, ok bool) {
	parts := strings.Split(value, "|")
	if len(parts) != 2 {
		return 0, 0, false
	}
	index, err1 := strconv.Atoi(parts[0])
	ctype, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return index, ctype, true
}
