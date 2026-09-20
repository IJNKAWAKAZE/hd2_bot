package push

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"hd2_bot/src/hd2"
	"hd2_bot/src/plugins/plugutil"
	"hd2_bot/src/translate"
)

// Kind 是事件类别，决定它落在卡片的哪一节。
type Kind string

// 四类事件（spec §6）。
const (
	KindDispatch Kind = "dispatch" // 新简报 / 重大指令
	KindOwner    Kind = "owner"    // 星球易主
	KindCampaign Kind = "campaign" // 战役开始 / 结束
	KindStation  Kind = "station"  // 空间站 / DSS 变更
)

// detailRunes 是简报正文在卡片上的摘要长度上限（按 rune 计）。
//
// 口径是「装下真实上游内容」，不再是「两行摘要」：2026-09-17 用真实上游量过，1048 条简报里
// 清洗后最长的正文是 807 rune，取整上调到 1000 留出余量。真实内容因此完整进卡片，
// 截断只会在异常数据（上游突然给出远超历史的正文）上发生，而那是 card.go 的终局防线
// （maxDetailRunes）负责的最后一道口子，不是这里的展示预算。
const detailRunes = 1000

// dispatchTitleRunes 是「没有高亮标记时」取第一句话作为标题的长度上限（按 rune 计）。
//
// 同样按「装下真实上游内容」取值：实测最长首行为 385 rune，取整上调到 400。
// 它决定标题取多长，超过它的首行会被压到 400 rune 加省略号；
// 最后一道口子是 card.go 的 maxTitleRunes（600），只有译文标题暴涨这类异常才会撞上。
const dispatchTitleRunes = 400

// Event 是一条待推送的变化。
// Body 只在「新简报」上有值：编排层把它送翻译，再把译文写回 Title/Detail；
// 其它三类事件的 Title/Detail 在检测阶段就已经是中文。
type Event struct {
	Kind   Kind   // 事件类别
	ID     string // 去重键（同一事件重启后不会重复推送）；不含冒号，且同一实体的第二次变化必须给出不同的键
	Title  string // 一行摘要（英文原文或已是中文）
	Detail string // 补充说明（中文，可为空）
	Body   string // 需要翻译的英文正文（只有新简报有）
}

// highlightRe 匹配简报正文里的游戏内高亮标记 <i=3>…</i>：标题优先取它。
var highlightRe = regexp.MustCompile(`(?i)<i=\d+>([^<]*)</i>`)

// Detect 比较上一轮快照与本轮数据，返回本轮要推送的事件。
//
// 口径（spec §6）：
//   - prev 是零值（首次运行或状态文件被清）时返回空列表：只写基线，不把现状重播一遍；
//   - 四类事件各自独立判定，同一颗星球同时出现「易主」与「战役开始/结束」时只保留易主；
//   - Event 不带时间字段，展示时间由编排层统一取 cur.FetchedAt（上游的 War.Now 不可信，S1 已定）。
//
// 返回值不含「去重」与「翻译」：那两步由编排层做（它们要碰存储与网络）。
func Detect(prev Snapshot, cur Input) []Event {
	if prev.Empty() {
		return nil
	}
	var events []Event
	ownerPlanets := map[int]bool{}

	// 1) 新简报：两段式判新，缺一不可。
	//   a. 时间：只收「发布时间不早于基线最新发布时间」的条目。基线只留最近 MaxDispatchIDs 条 id，
	//      若判新只看 id 集合，同一份 1048 条数据回灌会报出 848 条假事件（上游实测总量 1048 条）；
	//   b. id：再用基线里的 id 集合挡同一时间戳发布的多条（同一秒可能连发数条）。
	// 两边只要有一边时间不可用（上游没给时间 / 旧版本基线），就退化成「完全按 id 集合判新」：
	// 宁可多报一条可疑的，也不要静默丢掉真正的战况。
	seen := make(map[int64]bool, len(prev.DispatchIDs))
	for _, id := range prev.DispatchIDs {
		seen[id] = true
	}
	useTime := !prev.LatestDispatchPublished.IsZero()
	fresh := make([]hd2.Dispatch, 0, len(cur.Dispatches))
	for _, d := range cur.Dispatches {
		if seen[d.ID] {
			continue
		}
		if useTime && !d.Published.IsZero() && d.Published.Before(prev.LatestDispatchPublished) {
			// 比基线还旧、又不在去重集里：多半是基线被截断或上游回填历史，报出来只会刷屏。
			continue
		}
		fresh = append(fresh, d)
	}
	// 排序口径与快照一致：Published 倒序，时间相同时按 id 倒序兜底。
	sort.SliceStable(fresh, func(i, j int) bool {
		if !fresh[i].Published.Equal(fresh[j].Published) {
			return fresh[i].Published.After(fresh[j].Published)
		}
		return fresh[i].ID > fresh[j].ID
	})
	for _, d := range fresh {
		body := translate.CleanGameText(d.Message)
		title := dispatchTitle(d.Message, body)
		events = append(events, Event{
			Kind:  KindDispatch,
			ID:    strconv.FormatInt(d.ID, 10),
			Title: title,
			// 正文里与标题重复的那一行要剥掉：简报正文常常就是「标题独占一行 + 正文另起一行」，
			// 原样留着会让卡片出现「· 新的重大命令 ｜ 新的重大命令 有人发现…」（标题说两遍），
			// 同一段文字还会被送译两次。判据见 dropTitleLine。
			Body: dropTitleLine(body, title),
		})
	}

	// 2) 星球易主：同一编号的控制方变了（新增的星球不算变化，那多半是上游加了新区域）。
	planets := make([]hd2.Planet, len(cur.Planets))
	copy(planets, cur.Planets)
	sort.SliceStable(planets, func(i, j int) bool { return planets[i].Index < planets[j].Index })
	for _, p := range planets {
		key := strconv.Itoa(p.Index)
		before, known := prev.Owners[key]
		if !known || before == p.CurrentOwner {
			continue
		}
		events = append(events, ownerEvent(p, before))
		ownerPlanets[p.Index] = true
	}

	// 3) 战役开始：本轮有、上一轮没有的战役；与易主去重。
	campaigns := make([]hd2.Campaign, len(cur.Campaigns))
	copy(campaigns, cur.Campaigns)
	sort.SliceStable(campaigns, func(i, j int) bool { return campaigns[i].ID < campaigns[j].ID })
	current := make(map[string]bool, len(campaigns))
	for _, c := range campaigns {
		id := strconv.FormatInt(c.ID, 10)
		current[id] = true
		if prev.Campaigns[id] != "" {
			continue
		}
		if ownerPlanets[c.Planet.Index] {
			continue
		}
		events = append(events, Event{
			Kind:   KindCampaign,
			ID:     id,
			Title:  "新战役：" + planetLabelOf(planetName(cur, prev, c.Planet.Index), c.Planet.Index),
			Detail: campaignTypeText(c.Type),
		})
	}

	// 4) 战役结束：上一轮有、本轮没有的战役。星球与类型从上一轮快照里恢复。
	ended := make([]string, 0, len(prev.Campaigns))
	for id := range prev.Campaigns {
		if !current[id] {
			ended = append(ended, id)
		}
	}
	sort.Strings(ended)
	for _, id := range ended {
		index, ctype, ok := parseCampaignValue(prev.Campaigns[id])
		if !ok {
			continue
		}
		if ownerPlanets[index] {
			continue
		}
		events = append(events, Event{
			Kind: KindCampaign,
			// 去重键加 #end：同一场战役的开始与结束要各推一次，共用 id 会让后者被判成重复。
			ID:     id + "#end",
			Title:  "战役结束：" + planetLabelOf(planetName(cur, prev, index), index),
			Detail: campaignTypeText(ctype),
		})
	}

	// 5) 空间站 / DSS：签名变了就报，本轮没有的空间站不报（上游下线空间站属于已知情况）。
	stationIDs := make([]string, 0, len(cur.Stations))
	sigs := make(map[string]string, len(cur.Stations))
	for _, s := range cur.Stations {
		id := strconv.FormatInt(s.ID32, 10)
		stationIDs = append(stationIDs, id)
		sigs[id] = stationSignature(s)
	}
	sort.Strings(stationIDs)
	for _, id := range stationIDs {
		before, known := prev.Stations[id]
		if !known || before == sigs[id] {
			continue
		}
		// 说明里的星球写名字而不是编号：「位置 星球 216 → 星球 268」在群里没人能对上号。
		// 名字优先取本轮数据，本轮没有（星球已从列表消失等）时回落到上一轮快照记下的名字，
		// 两处都没有才退回「星球 N」——与战役事件同一套口径（见 planetName / planetLabelOf）。
		detail := stationDiff(before, sigs[id], func(index int) string {
			return planetLabelOf(planetName(cur, prev, index), index)
		})
		if detail == "" {
			// 中性表述：既可能是上游换了字段结构，也可能是签名本身的编码变了（例如旧版本基线），
			// 这里没有足够信息分辨，不写死原因。
			detail = "无法逐项对比空间站签名（可能是上游字段结构变化，也可能是签名编码本身的问题）。"
		}
		events = append(events, Event{
			Kind: KindStation,
			// 去重键带「变化前后的签名短哈希」：state 的去重表是永久表，只用 id32 会让空间站的
			// 第二次变化被判成重复而静默丢弃；带方向是因为 1→2 与 2→1 是两次不同的变化。
			ID:     id + "#" + signatureHash(before, sigs[id]),
			Title:  "空间站状态变更",
			Detail: detail,
		})
	}

	return events
}

// ownerEvent 拼一条星球易主事件：标题区分「失守 / 解放 / 易主」，说明给出新旧控制方。
func ownerEvent(p hd2.Planet, before string) Event {
	name := planetLabelOf(p.Name, p.Index)
	title := name + " 易主"
	switch {
	case plugutil.IsEnemyFaction(p.CurrentOwner):
		title = name + " 失守"
	case plugutil.IsEnemyFaction(before):
		title = name + " 解放"
	}
	return Event{
		Kind: KindOwner,
		// 去重键必须带上「从谁变到谁」：state 的去重表是永久表，若只用星球编号，
		// 同一颗星球第二次易主（终结族 → 超级地球）会被判成重复而静默丢弃。
		ID:     strconv.Itoa(p.Index) + "#" + keyPart(before) + "->" + keyPart(p.CurrentOwner),
		Title:  title,
		Detail: fmt.Sprintf("控制方：%s → %s", factionText(before), factionText(p.CurrentOwner)),
	}
}

// keyPart 去掉片段里的冒号并按需裁剪空白，用于拼接去重键。
// 编排层的去重键是 kind + ":" + id（见 state.MarkPushed 的调用），id 里再出现冒号会把这段边界搅乱；
// 控制方字段是上游自由文本，实测偶有奇怪字符，这里只做最小清洗。
func keyPart(text string) string {
	return strings.ReplaceAll(strings.TrimSpace(text), ":", "_")
}

// signatureHash 返回「变化前后签名」的短哈希（8 个 hex 字符）。
// 去重键要短、稳定、不含冒号，也不需要把整条签名（含战术行动名，又长又会变）塞进状态文件。
func signatureHash(before, after string) string {
	sum := sha256.Sum256([]byte(before + "->" + after))
	return fmt.Sprintf("%x", sum[:4])
}

// factionText 返回控制方的中文名；未收录时原样显示，空值时显示「未知」（这里是有意义的信息：
// 「从一个没见过的控制方变成另一个」也必须能读出来）。
func factionText(faction string) string {
	return plugutil.DefaultText(plugutil.FactionName(faction), plugutil.UnknownText)
}

// planetLabel 拼星球显示名「中文（English）」；上游没给名字时返回空串，由调用方补编号。
func planetLabel(name string) string {
	if strings.TrimSpace(name) == "" {
		return ""
	}
	label := plugutil.PlanetDisplayName(name)
	if label == plugutil.UnknownText {
		return ""
	}
	return label
}

// planetLabelOf 拼星球显示名，并在上游没给名字（缺失、空串或只有空白）时退回「星球 <编号>」。
// 带编号的兜底是必要的：上游实测会给出 planet: "?"、缺字段或空列表（见 model.go 的容错解析），
// 没有它标题会变成「新战役：」这样的半句话，看消息的人反而不知道说的是哪颗星球。
func planetLabelOf(name string, index int) string {
	if label := planetLabel(name); label != "" {
		return label
	}
	return fmt.Sprintf("星球 %d", index)
}

// planetName 按编号找星球名：先看本轮数据，本轮没有（上游分页、脏数据，或星球已从列表里消失）时
// 退回上一轮快照记下的名字——「战役结束」正是这种场景，本轮往往已经看不到那颗星球，
// 不回落就只能显示「星球 268」。两处都没有名字时返回空串，由调用方补编号。
func planetName(cur Input, prev Snapshot, index int) string {
	for _, p := range cur.Planets {
		if p.Index == index {
			if strings.TrimSpace(p.Name) != "" {
				return p.Name
			}
			break // 本轮只有个空名字：视为没有，继续走快照回退
		}
	}
	return prev.PlanetNames[strconv.Itoa(index)]
}

// campaignTypeText 说明战役类型：type=4 是实测的防守战（该战役所在的星球带 event），
// type=0 是解放战；其它取值原样显示数字，不猜。
func campaignTypeText(ctype int) string {
	switch ctype {
	case 4:
		return "防守战"
	case 0:
		return "解放战"
	default:
		return fmt.Sprintf("战役类型 %d", ctype)
	}
}

// dispatchTitle 抽简报标题：优先第一个 <i=3>…</i> 片段（原始正文，清洗前就要取，
// 因为清洗会把标记删掉），其次清洗后正文的第一句话，都没有时给「新简报」。
func dispatchTitle(raw, clean string) string {
	if m := highlightRe.FindStringSubmatch(raw); m != nil {
		if title := strings.TrimSpace(m[1]); title != "" {
			return title
		}
	}
	first := clean
	if idx := strings.IndexByte(first, '\n'); idx >= 0 {
		first = first[:idx]
	}
	first = strings.TrimSpace(first)
	if first == "" {
		return "新简报"
	}
	return translate.Summarize(first, dispatchTitleRunes)
}

// dropTitleLine 在「正文的第一个非空行就是标题」时把这一行从正文里剥掉，其余情况一个字都不动。
//
// 上游简报正文的形态是「高亮标题独占一行 + 正文另起一行」，例如：
//
//	<i=3>NEW MAJOR ORDER</i>
//	有人发现了一个更加令人发指的赛博格阴谋：……
//
// 清洗后第一行与高亮标题一字不差，不剥掉的话卡片上会出现「· 新的重大命令 ｜ 新的重大命令 有人发现…」，
// 标题说两遍、同一段文字还要被送进翻译层两次（白烧一次额度）。
//
// 判据刻意保守——只有「去掉标记、去掉首尾空白后的第一个非空行」与提取出的标题完全相等时才动手：
//   - 标题是从正文里摘要出来的（没有高亮标记）时，超过上限的首行会被截断成带「…」的形式，
//     与原文不相等，于是这里什么都不做（截断只影响标题，不该顺手改正文）；
//   - 标题来自高亮片段、而正文首行是别的内容时同样不动：宁可多显示一次，也不要把真正的正文吃掉；
//   - 正文只有标题一行时剥完得到空正文：卡片上仍然是「标题一条信息」，不会丢内容，
//     编排层对空正文直接跳过摘要，不会因此崩掉或画出空说明行。
func dropTitleLine(body, title string) string {
	want := strings.TrimSpace(title)
	if want == "" {
		return body
	}
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(translate.CleanGameText(line))
		if trimmed == "" {
			continue // 前导空行照旧跳过：上游偶尔以换行开头
		}
		if trimmed != want {
			return body
		}
		return strings.TrimSpace(strings.Join(lines[i+1:], "\n"))
	}
	return body
}

// stationSignature 把一座空间站压成一行字符串用于检测变化。
// 格式：flags|planetIndex|name:status,name:status（行动按名字升序，保证同样的状态得到同样的签名）。
func stationSignature(s hd2.SpaceStation) string {
	actions := make([]string, 0, len(s.TacticalActions))
	for _, a := range s.TacticalActions {
		actions = append(actions, strings.TrimSpace(a.Name)+":"+strconv.Itoa(a.Status))
	}
	sort.Strings(actions)
	return fmt.Sprintf("%d|%d|%s", s.Flags, s.PlanetIndex, strings.Join(actions, ","))
}

// stationSig 是解析后的空间站签名。
type stationSig struct {
	Flags   string
	Planet  string
	Actions map[string]string // 行动英文名 → 状态数字
}

// parseStationSignature 还原签名；结构不对时 ok 为 false（上游换了结构时调用方退化成通用文案）。
func parseStationSignature(sig string) (stationSig, bool) {
	parts := strings.Split(sig, "|")
	if len(parts) != 3 {
		return stationSig{}, false
	}
	parsed := stationSig{Flags: parts[0], Planet: parts[1], Actions: map[string]string{}}
	if actions := strings.TrimSpace(parts[2]); actions != "" {
		for _, item := range strings.Split(actions, ",") {
			name, status, found := strings.Cut(item, ":")
			if !found {
				return stationSig{}, false
			}
			parsed.Actions[strings.TrimSpace(name)] = strings.TrimSpace(status)
		}
	}
	return parsed, true
}

// stationDiff 比较两轮签名，拼出中文说明；解析失败时返回空串。
// planetLabel 把签名里的星球编号翻成显示名（调用方传入，见 Detect 里的查表口径）；
// 为 nil 时退回「星球 N」——空间站换位置是少见事件，但真发生时读者必须知道换到了哪颗星球。
func stationDiff(before, after string, planetLabel func(int) string) string {
	b, ok1 := parseStationSignature(before)
	a, ok2 := parseStationSignature(after)
	if !ok1 || !ok2 {
		return ""
	}
	var parts []string
	if b.Flags != a.Flags {
		parts = append(parts, fmt.Sprintf("标记 %s → %s", b.Flags, a.Flags))
	}
	if b.Planet != a.Planet {
		parts = append(parts, fmt.Sprintf("位置 %s → %s", stationPlanetText(b.Planet, planetLabel), stationPlanetText(a.Planet, planetLabel)))
	}
	names := make([]string, 0, len(a.Actions))
	for name := range a.Actions {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		statusAfter, ok := a.Actions[name]
		if !ok {
			continue
		}
		statusBefore, existed := b.Actions[name]
		if existed && statusBefore == statusAfter {
			continue
		}
		chinese, _ := plugutil.TacticalActionName(name)
		afterText, _ := plugutil.TacticalStatus(atoiOrZero(statusAfter))
		if !existed {
			parts = append(parts, fmt.Sprintf("新增战术行动『%s』：%s", chinese, afterText))
			continue
		}
		beforeText, _ := plugutil.TacticalStatus(atoiOrZero(statusBefore))
		parts = append(parts, fmt.Sprintf("『%s』：%s → %s", chinese, beforeText, afterText))
	}
	// 被移除的战术行动也要说出来（否则「少了一项」会静默无感）。
	removed := make([]string, 0, len(b.Actions))
	for name := range b.Actions {
		if _, ok := a.Actions[name]; !ok {
			removed = append(removed, name)
		}
	}
	sort.Strings(removed)
	for _, name := range removed {
		chinese, _ := plugutil.TacticalActionName(name)
		parts = append(parts, fmt.Sprintf("战术行动『%s』已结束", chinese))
	}
	return strings.Join(parts, "；")
}

// stationPlanetText 拼空间站所在星球的显示：上游用 0 表示「没有位置信息」；
// 有名字就用「中文（English）」，没有才退回「星球 N」（planetLabelOf 的兜底口径）。
func stationPlanetText(index string, planetLabel func(int) string) string {
	value, err := strconv.Atoi(strings.TrimSpace(index))
	if err != nil || value <= 0 {
		return plugutil.UnknownText
	}
	if planetLabel == nil {
		return planetLabelOf("", value)
	}
	return planetLabel(value)
}

// atoiOrZero 把状态字符串转成数字；不是数字时返回 0（会显示成「未激活」，属于可接受的退化）。
func atoiOrZero(text string) int {
	value, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil {
		return 0
	}
	return value
}
