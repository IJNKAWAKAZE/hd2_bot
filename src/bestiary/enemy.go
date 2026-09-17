// Package bestiary 是《绝地潜兵 2》敌人图鉴的数据层：拉取 wiki.gg 的 Enemies 表、剔掉
// 《绝地潜兵 1》的条目、把 wiki 富文本清洗成可读文本、补上官方简中译名，并按关键字检索。
//
// 数据来源与许可：敌人数值取自 helldivers.wiki.gg 的 Cargo 表（站点内容按 CC BY-NC-SA 授权），
// 图标是站点上的页面配图，中文名取自本地译名表（源自开源项目 astrbot_plugin_Helldivers，MIT）。
// 完整口径见 src/render/assets/LICENSES.md。
//
// 与 src/arsenal 同一套分层约定：本包不做展示格式化（中文文案、占位符、配色都在插件层），
// 只回答「有哪些敌人」「命中哪些」「图标在哪」，这样数据层能独立测试，换上游也只需要改这里。
package bestiary

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"hd2_bot/src/hd2/glossary"
	"hd2_bot/src/wikigg"
)

// SizeUnknown 表示上游没有给出体型（实测 83 只 HD2 敌人里有 26 只没给）。
// 用一个显式的负数而不是 0：0 在 wiki 的取值里是有意义的「最小体型」。
const SizeUnknown = -1

// 四个阵营的 key，与 plugutil 的派系表对齐（FactionName / FactionClass 认的就是这几个词）。
const (
	FactionTerminid   = "terminid"
	FactionAutomaton  = "automaton"
	FactionIlluminate = "illuminate"
	FactionHuman      = "human"
)

// factionAliases 把 wiki 的 faction 原文归到四个阵营；键是折叠大小写与首尾空白后的原文。
//
// 未收录的取值一律留空：站点随时会加新变异株，届时宁可显示原文（卡片会写「阵营未知」），
// 也不要把一只新虫硬塞进终结族里报出去。
// 归类依据是站点给每个敌人都打了 Category:<阵营>（实测 Appropriators、Vote Snatchers
// 这两组虽然名字与基础阵营不同，分类都落在 Illuminate）。
var factionAliases = map[string]string{
	"terminids":          FactionTerminid,
	"spore burst strain": FactionTerminid,
	"rupture strain":     FactionTerminid,
	"predator strain":    FactionTerminid,
	"automatons":         FactionAutomaton,
	"jet brigade":        FactionAutomaton,
	"incineration corps": FactionAutomaton,
	"illuminate":         FactionIlluminate,
	"appropriators":      FactionIlluminate,
	"vote snatchers":     FactionIlluminate,
	"super earth":        FactionHuman,
}

// baseFactionNames 是每个阵营的「基础」原文：原文与它不同，说明这条属于某个派系变体
// （例如 Jet Brigade 之于 Automatons），卡片上要把变体名一并写出来。
var baseFactionNames = map[string]string{
	FactionTerminid:   "Terminids",
	FactionAutomaton:  "Automatons",
	FactionIlluminate: "Illuminate",
	FactionHuman:      "Super Earth",
}

// hd1Factions 是《绝地潜兵 1》的阵营：站点把两代游戏的敌人放在同一张表里，
// 本机器人只做 HD2，这些条目在解析阶段就丢掉（实测 123 行里有 39 行是 HD1）。
var hd1Factions = map[string]bool{
	"cyborgs":                true,
	"cyborg legion":          true,
	"bugs":                   true,
	"hd1 illuminate":         true,
	"super earth federation": true,
}

// damageTypeNames 把 wiki 伤害图标的英文类型名译成中文；未收录的取值原样保留。
// 这九个类型是实测出现过的全部取值（Melee / Explosion / Ballistic / Acid / Fire /
// Confusion / Slow / Arc / Laser）。
var damageTypeNames = map[string]string{
	"melee":     "近战",
	"explosion": "爆炸",
	"ballistic": "弹道",
	"acid":      "酸液",
	"fire":      "火焰",
	"arc":       "电弧",
	"laser":     "激光",
	"confusion": "混乱",
	"slow":      "减速",
}

// Enemy 是一只敌人的图鉴条目。字段都是「解释过、但没格式化」的值。
type Enemy struct {
	Title      string // 上游标题（英文原名），也是查重与回填指令用的标识
	NameZh     string // 官方简中译名；译名表没收录时等于 Title
	Faction    string // 归一化后的阵营 key（terminid/automaton/illuminate/human）；未收录为空串
	FactionRaw string // 上游原文（例如 Jet Brigade）
	Variant    string // 与基础阵营不同的派系变体名（原文）；本来就是基础阵营时为空串
	Size       int    // wiki 的 size 字段（0 最小、3 最大）；上游没给时是 SizeUnknown
	Health     int    // 血量；上游没给或解析不出时是 0
	Damage     string // 清洗后的伤害说明；上游没给时为空串
	IconURL    string // 公网图标地址；没有配图或配图是 SVG 时为空串
	WikiURL    string // 条目在 wiki 上的地址
}

// SizeLabel 返回 wiki 的 size 字段对应的中文体型名。
// 实测 0 = 食腐虫这类杂兵、1 = 虫族战士、2 = 冲击虫、3 = 胆汁泰坦，因此在卡片上按四级展示；
// 未收录的取值与「上游没给」都返回空串，由展示层决定占位文案（这里不猜）。
func SizeLabel(size int) string {
	switch size {
	case 0:
		return "小型"
	case 1:
		return "中型"
	case 2:
		return "大型"
	case 3:
		return "巨型"
	default:
		return ""
	}
}

// Parse 把上游的行与图标地址转成图鉴条目。
//
// 过滤规则（都在这一层，测试不用起 HTTP）：
//   - 空标题丢掉（实测 123 行里有 1 行标题为空，来源未知）；
//   - HD1 阵营丢掉；
//   - 中文名查本地译名表，查不到就沿用英文名（绝不返回空名字）。
//
// icons 允许为 nil（图标取不到时不影响数值）；baseURL 用来拼条目的 wiki 地址，为空时不拼。
func Parse(rows []wikigg.EnemyRow, icons map[string]string, baseURL string) []Enemy {
	enemies := make([]Enemy, 0, len(rows))
	for _, row := range rows {
		title := strings.TrimSpace(row.Title)
		if title == "" || isHD1Faction(row.Faction) {
			continue
		}
		faction, variant := classifyFaction(row.Faction)
		enemies = append(enemies, Enemy{
			Title:      title,
			NameZh:     glossary.Enemy(title),
			Faction:    faction,
			FactionRaw: strings.TrimSpace(row.Faction),
			Variant:    variant,
			Size:       parseSize(row.Size),
			Health:     parseHealth(row.Health),
			Damage:     CleanDamage(row.Damage),
			IconURL:    strings.TrimSpace(icons[title]),
			WikiURL:    wikiURL(baseURL, title),
		})
	}
	return enemies
}

// isHD1Faction 判断一行是不是《绝地潜兵 1》的敌人。
func isHD1Faction(faction string) bool {
	return hd1Factions[normalizeFaction(faction)]
}

// classifyFaction 返回归一化后的阵营 key 与派系变体名。
// 变体名只在「原文不是该阵营的基础原文」时给出（例如 Jet Brigade）；未收录的阵营返回空 key。
func classifyFaction(faction string) (key string, variant string) {
	normalized := normalizeFaction(faction)
	key = factionAliases[normalized]
	if key == "" {
		return "", ""
	}
	raw := strings.TrimSpace(faction)
	if !strings.EqualFold(raw, baseFactionNames[key]) {
		variant = raw
	}
	return key, variant
}

// normalizeFaction 折叠大小写与首尾空白（站点写作 "Terminids"，分类里是 "Terminid"）。
func normalizeFaction(faction string) string {
	return strings.ToLower(strings.TrimSpace(faction))
}

// parseSize 解析 wiki 的 size 字段；空串或非数字一律按「上游没给」处理。
func parseSize(raw string) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return SizeUnknown
	}
	return n
}

// parseHealth 从上游的血量文本里取出数字。
//
// 实测取值不统一："6,500"、"60"、"18,001"、"2,000 HP + 400 Constitution"，
// 因此只取第一段数字（允许千分位逗号），后面的说明一律丢掉——卡片上写「2,000」比
// 原样贴一整句「2,000 HP + 400 Constitution」清楚。取不到数字时返回 0，展示层据此显示占位符。
func parseHealth(raw string) int {
	token := make([]rune, 0, 8)
	for _, r := range raw {
		switch {
		case r >= '0' && r <= '9':
			token = append(token, r)
		case r == ',' && len(token) > 0:
			token = append(token, r)
		default:
			if len(token) > 0 {
				return atoiOrZero(strings.ReplaceAll(string(token), ",", ""))
			}
		}
	}
	return atoiOrZero(strings.ReplaceAll(string(token), ",", ""))
}

// atoiOrZero 解析整数，失败或负数返回 0（血量不可能是负数）。
func atoiOrZero(raw string) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// 清洗 damage 富文本用的正则。都包在这一层，不导出：调用方只该拿到清洗后的文本。
var (
	// reFileLink 匹配 wiki 的图片链接：[[File:Damage Acid Icon.svg|20px|link=Damage|alt=Acid]]
	reFileLink = regexp.MustCompile(`\[\[File:[^\]]*\]\]`)
	// reAlt 从图片链接里取 alt= 的值（就是伤害类型名）。
	reAlt = regexp.MustCompile(`alt=([^\]|]*)`)
	// reBreak 匹配换行与分隔线：wiki 用 <br> 分条、<hr> 分伤害类型，两者在这里都当分块边界。
	reBreak = regexp.MustCompile(`(?i)<br\s*/?>|<hr\s*/?>`)
	// reTag 匹配其余 HTML 标签（只丢标签，留下标签里的文字）。
	reTag = regexp.MustCompile(`<[^>]*>`)
	// reLinkWithText 匹配 [[目标|显示文本]]；reLinkPlain 匹配 [[目标]]。
	reLinkWithText = regexp.MustCompile(`\[\[([^\]|]*)\|([^\]]*)\]\]`)
	reLinkPlain    = regexp.MustCompile(`\[\[([^\]]*)\]\]`)
)

// htmlEntities 是 wiki 富文本里会出现的实体转义。
var htmlEntities = strings.NewReplacer(
	"&nbsp;", " ",
	"&amp;", "&",
	"&lt;", "<",
	"&gt;", ">",
	"&quot;", "\"",
	"&#39;", "'",
)

// blockTrailingTrim 是块尾要剪掉的标点：wiki 自己在块尾写了分号或逗号，
// 不剪掉再拼上我们的「；」就会出现「(4s);；85 Area of Effect」这种双分号。
const blockTrailingTrim = ";；,，、 "

// UnescapeEntities 还原 wiki 富文本里的常见实体转义（&nbsp; 等）。
// 导出是因为展示层也要用：上游并非只有 damage 一列是富文本，射击模式里也混着 "Auto&nbsp;"，
// 清洗规则只维护这一份。
func UnescapeEntities(text string) string { return htmlEntities.Replace(text) }

// CleanDamage 把上游的 damage 富文本清洗成一行可读文本（多条之间用「；」分隔）。
//
// 原始数据形如（实测胆汁泰坦）：
//
//	<span>[[File:Damage Acid Icon.svg|20px|link=Damage|alt=Acid]]</span>
//	[[Damage#Damage_Types|<span>60 Bile Spew +<br>3/s Acid Burn (4s); </span>]]
//
// 处理顺序是固定的：图片链接与 wiki 链接 → 分块（<br>/<hr>）→ 剥 HTML 标签 → 实体转义 →
// 逐块折叠空白 → 拼接。顺序颠倒会被后一步破坏（例如先剥标签，链接里嵌的 <span> 会让
// [[ 与 ]] 留成孤零零的括号；先还原实体，&lt;b&gt; 这种「本该显示出来的字面标签」会被当成真标签删掉）。
//
// 为什么按 <br> 分块后还要看块尾是不是加号：实测 wiki 用「1000 Stomp +<br>0 Area of Effect」
// 表示两个数值相加，直接按「；」拼会把一个伤害拆成两条。因此上一块以加号结尾时用空格接住。
func CleanDamage(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	// 链接与伤害图标必须在分块之前处理：实测 <br> 会落在 [[...]] 内部
	// （60 Bile Spew +<br>3/s Acid Burn），先分块会把一个链接切成两半、括号留在文本里。
	linked := cleanWikiMarkup(raw)
	blocks := make([]string, 0, 8)
	for _, chunk := range reBreak.Split(linked, -1) {
		if block := cleanDamageBlock(chunk); block != "" {
			blocks = append(blocks, block)
		}
	}
	var b strings.Builder
	for i, block := range blocks {
		switch {
		case i == 0:
		case strings.HasSuffix(blocks[i-1], "+"):
			// 上一块是「值 +」的形式：用空格接住，读起来仍是一句话。
			b.WriteString(" ")
		default:
			b.WriteString("；")
		}
		b.WriteString(block)
	}
	return b.String()
}

// cleanWikiMarkup 把 wiki 的两种链接换成可读文字：伤害图标换成【类型】，其余链接只留显示文本。
// 这一步必须在剥 HTML 标签之前做：链接里嵌着 <span>，先剥标签会把 [[ 与 ]] 留成孤零零的括号。
func cleanWikiMarkup(raw string) string {
	text := reFileLink.ReplaceAllStringFunc(raw, func(link string) string {
		// 伤害图标换成【类型】：图标本身在卡片里不显示，但类型名是有用信息。
		name := ""
		if match := reAlt.FindStringSubmatch(link); len(match) > 1 {
			name = strings.TrimSpace(match[1])
		}
		if name == "" {
			return ""
		}
		return "【" + damageTypeName(name) + "】"
	})
	text = reLinkWithText.ReplaceAllString(text, "$2")
	return reLinkPlain.ReplaceAllString(text, "$1")
}

// cleanDamageBlock 清洗单个文本块：剥掉 HTML 标签、还原实体转义、折叠空白、剪掉块尾标点。
func cleanDamageBlock(chunk string) string {
	text := reTag.ReplaceAllString(chunk, "")
	text = htmlEntities.Replace(text)
	// Fields 按任意空白切分，等于同时做掉「折叠连续空格」与「去掉首尾空白」。
	text = strings.Join(strings.Fields(text), " ")
	// 剪掉块尾的标点，但要留住有意义的加号（它表示与下一块相加）。
	return strings.TrimRight(text, blockTrailingTrim)
}

// damageTypeName 返回伤害类型的中文名；未收录的取值原样返回（不猜）。
func damageTypeName(name string) string {
	if zh, ok := damageTypeNames[strings.ToLower(strings.TrimSpace(name))]; ok {
		return zh
	}
	return strings.TrimSpace(name)
}

// wikiURL 拼条目在 wiki 上的地址；baseURL 为空时不拼（返回空串）。
// 标题里的空格在 wiki 地址里写作下划线，其余字符交给 PathEscape 编码。
func wikiURL(baseURL, title string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return ""
	}
	slug := url.PathEscape(strings.ReplaceAll(strings.TrimSpace(title), " ", "_"))
	return base + "/wiki/" + slug
}

// defaultSearchLimit 是检索在 limit <= 0 时的默认条数上限。
const defaultSearchLimit = 8

// Query 是一次敌人检索。
// Keyword 为空表示「不打关键字」：这时返回目录顺序的前若干只，而不是空结果。
type Query struct {
	Keyword string
	Limit   int
}

// Result 是一次检索的结果：命中的条目（已截断到 limit）与未截断的命中总数。
type Result struct {
	Enemies []Enemy
	Total   int
}

// Search 按关键字检索敌人：匹配中文名、英文名与阵营原文，都做首尾空白裁剪与大小写折叠。
// 排序：完全相等 → 前缀命中 → 包含命中；同一档内保持输入顺序（稳定，同一关键字每次结果一致）。
func Search(list []Enemy, q Query) Result {
	limit := q.Limit
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	keyword := strings.ToLower(strings.TrimSpace(q.Keyword))
	type scored struct {
		enemy Enemy
		score int
	}
	matched := make([]scored, 0, len(list))
	for _, enemy := range list {
		if keyword == "" {
			matched = append(matched, scored{enemy: enemy})
			continue
		}
		score := matchScore(enemy, keyword)
		if score == 0 {
			continue
		}
		matched = append(matched, scored{enemy: enemy, score: score})
	}
	sort.SliceStable(matched, func(i, j int) bool { return matched[i].score > matched[j].score })

	result := Result{Total: len(matched)}
	for i, m := range matched {
		if i >= limit {
			break
		}
		result.Enemies = append(result.Enemies, m.enemy)
	}
	return result
}

// matchScore 给一只敌人与关键字的匹配度打分：3 = 完全相等，2 = 前缀命中，1 = 包含命中，0 = 不匹配。
// 阵营原文也参与匹配：「Jet Brigade」这种变体名群里是直接这么叫的。
func matchScore(enemy Enemy, keyword string) int {
	best := 0
	consider := func(value string) {
		v := strings.ToLower(strings.TrimSpace(value))
		if v == "" {
			return
		}
		switch {
		case v == keyword:
			if best < 3 {
				best = 3
			}
		case strings.HasPrefix(v, keyword):
			if best < 2 {
				best = 2
			}
		case strings.Contains(v, keyword):
			if best < 1 {
				best = 1
			}
		}
	}
	consider(enemy.NameZh)
	consider(enemy.Title)
	consider(enemy.FactionRaw)
	return best
}

// Validate 检查一份条目列表能不能用：空列表算坏数据，返回中文错误。
// 与 arsenal.ParseCatalog 同一套判断：坏数据在群里看起来和「没搜到」一模一样，很难查。
func Validate(list []Enemy) error {
	if len(list) == 0 {
		return fmt.Errorf("敌人图鉴条目为空")
	}
	return nil
}
