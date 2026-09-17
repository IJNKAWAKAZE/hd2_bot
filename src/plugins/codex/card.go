// 图鉴卡片的视图模型与构造函数。/gun、/strat、/armor、/grenade 四条指令共用 equipment 卡片，
// 差别只在标题与要查的类别上；/warbonds 与 /enemy 各有自己的卡片。
//
// 这一层只做「把数据排成卡片上要显示的样子」：文案、占位符、配色 class 都在这里定，
// 模板只负责排版。卡片与文本回退共用同一份视图模型，两条路径不会出现两种说法。
package codex

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"hd2_bot/src/arsenal"
	"hd2_bot/src/atlas"
	"hd2_bot/src/bestiary"
	"hd2_bot/src/plugins/plugutil"
	"hd2_bot/src/render"
)

// 卡片名必须与 src/render/templates 下的文件名一致（equipment.tmpl → "equipment"）。
const (
	equipmentCardName = "equipment"
	warbondsCardName  = "warbonds"
	enemyCardName     = "enemy"
)

// searchLimit 是一次检索最多取几条候选。图鉴查询是「按名字精确查一件」：
// 排在第一的那条就是卡片上那件（列表形态只存在于行内查询的候选里），
// 卡片上不再写「匹配到几件」这类统计（用户 2026-09-17：精确匹配下那是噪音）。
const searchLimit = 8

// maxWarbondItems 是单本军需簿明细最多列几件装备（实测单本最多 20 余件，留出余量）。
const maxWarbondItems = 40

// EquipmentSpec 描述一条装备查询指令的展示口径。
// 四条指令（/gun、/strat、/armor、/grenade）共用 equipment 卡片，差别只有标题、徽标与类别。
type EquipmentSpec struct {
	Command  string         // 指令名（不带斜杠），用于日志与用法文案
	Title    string         // 卡片标题，例如「武器图鉴」
	Subtitle string         // 卡片副标题
	Emblem   string         // 徽标素材逻辑名（见 src/render/assets.go）
	Kinds    []arsenal.Kind // 要查的类别，按卡片上的分组顺序排列
}

// 四条指令的展示口径。Emblem 用的是本仓库已有的徽标素材，不新增图片。
var (
	gunSpec = EquipmentSpec{
		Command:  "gun",
		Title:    "武器图鉴",
		Subtitle: "主武器 · 副武器 · 支援武器",
		Emblem:   "emblem.super_earth",
		Kinds:    arsenal.Weapons,
	}
	stratSpec = EquipmentSpec{
		Command:  "strat",
		Title:    "战备图鉴",
		Subtitle: "哨戒炮 · 轨道 · 飞鹰 · 背包",
		Emblem:   "emblem.super_earth",
		Kinds:    []arsenal.Kind{arsenal.KindStratagem},
	}
	armorSpec = EquipmentSpec{
		Command:  "armor",
		Title:    "护甲图鉴",
		Subtitle: "轻甲 · 中甲 · 重甲",
		Emblem:   "emblem.super_earth",
		Kinds:    []arsenal.Kind{arsenal.KindArmor},
	}
	grenadeSpec = EquipmentSpec{
		Command:  "grenade",
		Title:    "手雷图鉴",
		Subtitle: "投掷物与手雷",
		Emblem:   "emblem.super_earth",
		Kinds:    []arsenal.Kind{arsenal.KindGrenade},
	}
)

// Usage 返回这条指令的用法文案，用于卡片说明与文本回退。
func (s EquipmentSpec) Usage() string { return "/" + s.Command + " [关键字]" }

// Field 是卡片上的一格「标签 + 数值」。
// Class 是数值的额外 class（目前只有敌人的阵营格用它上配色），空串表示不加。
type Field struct {
	Label string
	Value string
	Class string
}

// EquipmentCard 是 equipment.tmpl 的视图模型。
//
// 图鉴查询是「按名字精确查一件」：卡片上只有这一件的详情（含内置社区维基的详细属性），
// 没有列表形态——列表只存在于行内查询的候选里（用户 2026-09-17 明确）。没命中时卡片只出一句提示。
type EquipmentCard struct {
	render.Meta
	Intro  string // 卡片开头的一句话（写清按什么查的、命中几件）
	Empty  string // 一件都没命中时的提示文案；非空时 Detail 一定为 nil
	Detail *EquipmentDetail
	Notes  []string // 卡片底部的口径说明
}

// WarbondsCard 是 warbonds.tmpl 的视图模型：要么是全部军需簿的名单，要么是某一本的明细。
type WarbondsCard struct {
	render.Meta
	Intro  string
	Books  []WarbondRow   // 名单（按关键字查到某一本时为空）
	Detail *WarbondDetail // 明细（没写关键字或没命中时为空）
	Empty  string         // 名单为空时的提示
	Notes  []string
}

// WarbondRow 是名单里的一本军需簿。
type WarbondRow struct {
	Name    string
	English string
	Pages   string
	Medals  string
	Credits string
	Items   string
}

// WarbondDetail 是一本军需簿的明细。
type WarbondDetail struct {
	Name    string
	English string
	Pages   string
	Medals  string
	Credits string
	Items   []WarbondItem
	More    string
}

// WarbondItem 是明细里的一件装备。
type WarbondItem struct {
	Name    string
	English string
	Kind    string
	Acquire string
}

// EnemyCard 是 enemy.tmpl 的视图模型。
//
// 与装备图鉴一样：一次只查一只（按名字精确查），列表形态只在行内查询的候选里。
type EnemyCard struct {
	render.Meta
	Intro string
	Empty string // 一只都没命中时的提示文案；非空时 Enemy 一定为 nil
	Enemy *EnemyItem
	Notes []string
}

// EnemyItem 是卡片上那只敌人。参照站（HD2_Wiki 的敌人详情页）把一只敌人排成
// 「外观 + 基础信息 + 部位数据」加右侧栏，这里按同一套来：详细资料来自内置的社区维基数据，
// 没有对应条目时那几段为空，卡片只画我们自己图鉴里的信息。
type EnemyItem struct {
	Name     string
	English  string
	Image    string   // 外观图（data URI）；没有图时为空串
	Desc     string   // 一句话描述
	Tags     []string // 阵营与体型这类标签
	Rows     []Field  // 基础信息（阵营那行带配色 class）
	Health   string   // 侧栏高亮框里的总生命值
	Parts    []PartRow
	Variants []string
	Related  []EnemyLink // 侧栏「同阵营敌人」
	Source   string      // 侧栏「来源」的上游页面地址
}

// EnemyLink 是侧栏「同阵营敌人」里的一条：名字 + 分类。
type EnemyLink struct {
	Name     string
	Category string
}

// EnemyImages 是卡片上要画、但图鉴数据本身没带的图（由 handler 事先下载成 data URI）：
// 外观图与部位示意图。缺哪张就空着，卡片照常出数值。
type EnemyImages struct {
	Icon  string            // 外观图
	Parts map[string]string // 部位名 → 部位示意图
}

// PartRow 是敌人详情里的一个部位（一行）。Image 是部位示意图（data URI），没有就空着。
type PartRow struct {
	Name     string
	Count    string
	Health   string
	Armor    string
	Location string
	Durable  string
	ToMain   string
	Image    string
	Fatal    bool
	Weak     bool
}

// ---- 装备卡片 ----

// BuildEquipmentCard 把一次检索结果排成装备卡片。
//
// result 是目录检索的原始结果（已按匹配度排序：完全相等 → 前缀 → 包含），
// 第一条就是「用户想查的那件」；images 是「装备 id → data URI」，只有第一条用得上。
// dataTime 是数据采集时间，零值时卡片不显示这一行。
func BuildEquipmentCard(spec EquipmentSpec, result arsenal.Result, catalog *arsenal.Catalog,
	images map[string]string, keyword string, dataTime time.Time) EquipmentCard {
	card := EquipmentCard{
		Meta: render.Meta{
			Title:    spec.Title,
			Subtitle: spec.Subtitle,
			DataTime: dataTimeText(dataTime),
			Emblem:   spec.Emblem,
		},
		Intro: equipmentIntro(spec, keyword, result),
	}
	if len(result.Items) == 0 {
		card.Empty = fmt.Sprintf("目录里没有名字匹配「%s」的%s：试试中文名、英文名、型号或玩家外号（例如「解放者」「AR-23」「电喷」）。",
			strings.TrimSpace(keyword), kindWord(spec))
		return card
	}
	best := result.Items[0]
	card.Detail = BuildEquipmentDetail(best, catalog, images[best.ID])
	return card
}

// kindWord 返回这条指令查的是哪一类，用于「没找到」的提示文案。
func kindWord(spec EquipmentSpec) string {
	if len(spec.Kinds) == 1 {
		return arsenal.KindLabel(spec.Kinds[0])
	}
	return "装备"
}

// equipmentNames 取这批装备的展示名（中文名，缺中文名时用英文名）。
func equipmentNames(items []arsenal.Item) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, plugutil.DefaultText(item.NameZh, item.NameEn))
	}
	return names
}

// equipmentIntro 只在「没命中」与「名字没写全」时说一句话：
// 命中一件精确条目时不再写「匹配到 N 件」这类统计（用户 2026-09-17 明确：那是噪音），
// 卡片上本来就是一件装备的详情。
func equipmentIntro(spec EquipmentSpec, keyword string, result arsenal.Result) string {
	trimmed := strings.TrimSpace(keyword)
	if trimmed == "" {
		// 不带关键字时指令只发行内搜索按钮，这里只是兜底（例如未来又允许不带参数直接出卡）。
		return fmt.Sprintf("按名字查询%s：把名字写在指令后面，例如 %s。", kindWord(spec), spec.Usage())
	}
	if len(result.Items) == 0 {
		return fmt.Sprintf("关键字「%s」没有命中。", trimmed)
	}
	best := result.Items[0]
	if isExactName(best, trimmed) {
		return ""
	}
	return fmt.Sprintf("「%s」不是完整名字，这里显示最接近的「%s」。", trimmed, plugutil.DefaultText(best.NameZh, best.NameEn))
}

// isExactName 判断关键字是不是这件装备的完整名字（中文名 / 英文名 / 型号 / 外号，忽略大小写与空格）。
func isExactName(item arsenal.Item, keyword string) bool {
	needle := foldName(keyword)
	if needle == "" {
		return false
	}
	for _, name := range append([]string{item.NameZh, item.NameEn, item.Model}, item.Aliases...) {
		if foldName(name) == needle {
			return true
		}
	}
	return false
}

// foldName 折叠名字用于比较：去掉空格与大小写差异（中文名不受影响）。
func foldName(name string) string {
	trimmed := strings.ToLower(strings.TrimSpace(name))
	return strings.Join(strings.Fields(trimmed), "")
}

// matchDetails 按装备的中英文名去内置详情库里找对应条目：英文名优先，
// 因为两边译名可能不同（本地图鉴叫「胆汁泰坦」，社区维基叫「吐酸泰坦」）。
func matchDetails(item arsenal.Item) *atlas.Entry {
	db := atlas.Default()
	if entry, ok := db.Gear(item.NameEn); ok {
		return entry
	}
	if entry, ok := db.Gear(item.NameZh); ok {
		return entry
	}
	return nil
}

// MatchEnemyDetail 按中英文名找敌人的详情条目；找不到时返回 nil（卡片只画本地图鉴的信息）。
// 导出是因为 handler 也要用它来取外观图与部位图的地址（图必须在上游下载，卡片层只拿 data URI）。
func MatchEnemyDetail(enemy bestiary.Enemy) *atlas.Entry {
	db := atlas.Default()
	if entry, ok := db.Enemy(enemy.Title); ok {
		return entry
	}
	if entry, ok := db.Enemy(enemy.NameZh); ok {
		return entry
	}
	return nil
}

// armorFields 拼护甲专属数值：等级、护甲值、速度、耐力回复、被动。
// 后三项是上游给的原始评分数值（实测速度 450/500/550、耐力回复 50/100/125），原样显示并由卡片说明。
func armorFields(item arsenal.Item) []Field {
	if item.Armor == nil {
		return nil
	}
	fields := make([]Field, 0, 4)
	if class := armorClassName(item.Armor.Class); class != "" {
		fields = append(fields, Field{Label: "等级", Value: class})
	}
	if item.Armor.Rating > 0 {
		fields = append(fields, Field{Label: "护甲值", Value: fmt.Sprintf("%d", item.Armor.Rating)})
	}
	if item.Armor.Speed > 0 {
		fields = append(fields, Field{Label: "速度", Value: fmt.Sprintf("%d", item.Armor.Speed)})
	}
	if item.Armor.StaminaRegen > 0 {
		fields = append(fields, Field{Label: "耐力回复", Value: fmt.Sprintf("%d", item.Armor.StaminaRegen)})
	}
	if passive := strings.TrimSpace(item.Armor.Passive); passive != "" {
		fields = append(fields, Field{Label: "被动", Value: passive})
	}
	return fields
}

// armorClassNames 是护甲等级的官方简中名；未收录的取值原样显示。
var armorClassNames = map[string]string{
	"Light":  "轻甲",
	"Medium": "中甲",
	"Heavy":  "重甲",
}

// armorClassName 返回护甲等级中文名；未收录的取值（实测有 "Unknown"）原样返回。
func armorClassName(class string) string {
	trimmed := strings.TrimSpace(class)
	if name, ok := armorClassNames[trimmed]; ok {
		return name
	}
	return trimmed
}

// deployTypeNames 是战备部署类型的中文名；未收录的取值原样显示。
var deployTypeNames = map[string]string{
	"Support Weapon": "支援武器",
	"Backpack":       "背包",
	"Orbital":        "轨道",
	"Sentry":         "哨戒炮",
	"Emplacement":    "固定炮台",
	"Vehicle":        "载具",
	"Eagle":          "飞鹰",
	"Other":          "其它",
}

// deployTypeName 返回部署类型的中文名；空串返回空串（模板据此不显示这一行）。
func deployTypeName(kind string) string {
	trimmed := strings.TrimSpace(kind)
	if name, ok := deployTypeNames[trimmed]; ok {
		return name
	}
	return trimmed
}

// firingModeNames 是常见射击模式的中文名。上游这一列并不干净（实测混着 "Auto&nbsp;"、
// "40mm"、"APHET" 这类值），因此只翻译认得出的词，其余原样显示——不猜。
var firingModeNames = map[string]string{
	"auto":           "自动",
	"automatic":      "自动",
	"semi":           "半自动",
	"semi-auto":      "半自动",
	"semi-automatic": "半自动",
	"burst":          "点射",
	"burst fire":     "点射",
	"full":           "全自动",
	"bolt-action":    "栓动",
	"lever-action":   "杠杆",
}

// modesText 拼射击模式：先做实体转义与空白折叠（上游有 "Auto&nbsp;" 这种脏数据），
// 去重后最多列 3 个，多了用「等」收尾。
//
// 去重放在翻译之后（比较时折叠大小写）：上游同一件装备可能既给 "Auto" 又给 "Automatic"，
// 按原文去重会留下两个「自动」；中文没有大小写，折叠只影响认不出的原文。
func modesText(modes []string) string {
	seen := make(map[string]bool, len(modes))
	out := make([]string, 0, len(modes))
	for _, mode := range modes {
		cleaned := bestiary.UnescapeEntities(mode)
		cleaned = strings.Join(strings.Fields(cleaned), " ")
		if cleaned == "" {
			continue
		}
		if zh, ok := firingModeNames[strings.ToLower(cleaned)]; ok {
			cleaned = zh
		}
		key := strings.ToLower(cleaned)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, cleaned)
	}
	if len(out) == 0 {
		return ""
	}
	if len(out) > 3 {
		return strings.Join(out[:3], " / ") + " 等"
	}
	return strings.Join(out, " / ")
}

// ArrowStep 是召唤指令里的一步。
//
// 为什么不是直接存一个字符：卡片上的箭头要画成游戏里的箭头图形（内联 SVG，见 assets.go），
// 而文本回退要的是字符；两者同源一份数据，卡片与文本才不会出现两种顺序或两种方向。
// 只有上游给了没见过的方向时 Asset 才会为空，那时卡片退回显示上游原文。
type ArrowStep struct {
	Asset string // 内置箭头图标的逻辑名（arrow.up / arrow.down / arrow.left / arrow.right）
	Glyph string // 文本回退用的方向符号；认不出的方向为空串
	Text  string // 认不出的方向的上游原文；认得出来时为空串
}

// codeArrows 把召唤指令翻成逐个箭头：认得出的方向带上图标与符号，认不出的原样保留（不猜也不吞）。
func codeArrows(deploy *arsenal.Deploy) []ArrowStep {
	if deploy == nil || len(deploy.Code) == 0 {
		return nil
	}
	arrows := make([]ArrowStep, 0, len(deploy.Code))
	for _, step := range deploy.Code {
		raw := strings.TrimSpace(step)
		if arrow, ok := summonArrows[strings.ToLower(raw)]; ok {
			arrows = append(arrows, arrow)
			continue
		}
		arrows = append(arrows, ArrowStep{Text: raw})
	}
	return arrows
}

// summonArrows 是召唤指令的四个方向：图标逻辑名与文本符号一一对应。
var summonArrows = map[string]ArrowStep{
	"up":    {Asset: "arrow.up", Glyph: "↑"},
	"down":  {Asset: "arrow.down", Glyph: "↓"},
	"left":  {Asset: "arrow.left", Glyph: "←"},
	"right": {Asset: "arrow.right", Glyph: "→"},
}

// acquisitionText 拼获取方式：军需簿要把书名、页码与勋章数写清楚，征用点要写清价格与等级要求。
func acquisitionText(catalog *arsenal.Catalog, item arsenal.Item) string {
	acq := item.Acq
	switch acq.Kind {
	case "warbond":
		name := strings.TrimSpace(acq.WarbondID)
		if catalog != nil {
			if zh := catalog.WarbondName(acq.WarbondID); zh != "" {
				name = zh
			}
		}
		parts := make([]string, 0, 3)
		if name != "" {
			parts = append(parts, "军需簿「"+name+"」")
		}
		if acq.Page > 0 {
			parts = append(parts, fmt.Sprintf("第 %d 页", acq.Page))
		}
		if acq.ItemMedals > 0 {
			parts = append(parts, fmt.Sprintf("%d 勋章", acq.ItemMedals))
		}
		return strings.Join(parts, " · ")
	case "requisition":
		parts := []string{"征用点"}
		if acq.RequisitionPoints > 0 {
			parts = append(parts, plugutil.FormatInt(int64(acq.RequisitionPoints)))
		}
		if acq.LevelRequired > 0 {
			parts = append(parts, fmt.Sprintf("需等级 %d", acq.LevelRequired))
		}
		return strings.Join(parts, " · ")
	default:
		return arsenal.AcqLabel(acq.Kind)
	}
}

// dataTimeText 把数据采集时间格式化到展示时区；零值返回空串（外壳会隐藏这一行）。
func dataTimeText(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02 15:04")
}

// ---- 军需簿卡片 ----

// BuildWarbondsCard 组军需簿卡片：没写关键字时列全部名单，写了关键字且命中时给明细。
// books 是全部军需簿（按目录顺序），detail 为 nil 时卡片只出名单。
func BuildWarbondsCard(books []arsenal.Warbond, detail *WarbondDetail, keyword string, dataTime time.Time) WarbondsCard {
	card := WarbondsCard{
		Meta: render.Meta{
			Title:    "军需簿",
			Subtitle: "债券与解锁进度",
			DataTime: dataTimeText(dataTime),
			Emblem:   "emblem.major_order",
		},
		Detail: detail,
		Notes: []string{
			"勋章数是把该本全部页解锁所需的总量（上游给的是每页累计值，取最大的一页）。",
			"价格栏的「—」表示这本不单卖（例如首发本）。",
		},
	}
	if detail != nil {
		card.Intro = fmt.Sprintf("关键字「%s」命中：%s", keyword, detail.Name)
		return card
	}
	if strings.TrimSpace(keyword) == "" {
		card.Intro = "全部军需簿，按目录顺序排列；带上关键字可以看某一本里的装备。"
	} else {
		card.Intro = fmt.Sprintf("没有找到匹配「%s」的军需簿，这里是全部名单。", keyword)
	}
	card.Books = make([]WarbondRow, 0, len(books))
	for _, book := range books {
		card.Books = append(card.Books, WarbondRow{
			Name:    plugutil.DefaultText(book.NameZh, book.NameEn),
			English: strings.TrimSpace(book.NameEn),
			Pages:   fmt.Sprintf("%d 页", book.Pages),
			Medals:  fmt.Sprintf("%s 勋章", plugutil.FormatInt(int64(book.MedalsTotal))),
			Credits: creditsText(book.SuperCredits),
			Items:   fmt.Sprintf("%d 件", book.ItemCount),
		})
	}
	if len(card.Books) == 0 {
		card.Empty = "目录里没有军需簿数据。"
	}
	return card
}

// creditsText 拼超级货币价：上游用 0 表示不单卖（首发那本的字段是 null）。
func creditsText(credits int) string {
	if credits <= 0 {
		return plugutil.DashText
	}
	return plugutil.FormatInt(int64(credits))
}

// BuildWarbondDetail 把一本军需簿转成明细；items 超出 maxWarbondItems 时截断并给出说明。
func BuildWarbondDetail(book arsenal.Warbond, items []arsenal.Item, catalog *arsenal.Catalog) *WarbondDetail {
	detail := &WarbondDetail{
		Name:    plugutil.DefaultText(book.NameZh, book.NameEn),
		English: strings.TrimSpace(book.NameEn),
		Pages:   fmt.Sprintf("%d 页", book.Pages),
		Medals:  fmt.Sprintf("%s 勋章", plugutil.FormatInt(int64(book.MedalsTotal))),
		Credits: creditsText(book.SuperCredits),
		Items:   make([]WarbondItem, 0, min(len(items), maxWarbondItems)),
	}
	if detail.Name == detail.English {
		detail.English = ""
	}
	for i, item := range items {
		if i >= maxWarbondItems {
			detail.More = fmt.Sprintf("另有 %d 件未列出。", len(items)-maxWarbondItems)
			break
		}
		detail.Items = append(detail.Items, WarbondItem{
			Name:    plugutil.DefaultText(item.NameZh, item.NameEn),
			English: strings.TrimSpace(item.NameEn),
			Kind:    arsenal.KindLabel(item.Kind),
			Acquire: acquisitionText(catalog, item),
		})
	}
	return detail
}

// FindWarbond 按关键字找一本军需簿：先精确匹配（中文名/英文名/id，忽略大小写），
// 再前缀、再包含；命中多条时返回第一条并说明不确定（由调用方决定文案）。
func FindWarbond(catalog *arsenal.Catalog, keyword string) (arsenal.Warbond, bool) {
	books := catalog.Warbonds()
	if len(books) == 0 {
		return arsenal.Warbond{}, false
	}
	key := strings.ToLower(strings.TrimSpace(keyword))
	if key == "" {
		return arsenal.Warbond{}, false
	}
	for _, match := range []func(arsenal.Warbond) bool{
		func(b arsenal.Warbond) bool {
			return strings.EqualFold(strings.TrimSpace(b.NameZh), keyword) ||
				strings.EqualFold(strings.TrimSpace(b.NameEn), keyword) ||
				strings.EqualFold(strings.TrimSpace(b.ID), keyword)
		},
		func(b arsenal.Warbond) bool {
			return strings.HasPrefix(strings.ToLower(strings.TrimSpace(b.NameZh)), key) ||
				strings.HasPrefix(strings.ToLower(strings.TrimSpace(b.NameEn)), key)
		},
		func(b arsenal.Warbond) bool {
			return strings.Contains(strings.ToLower(strings.TrimSpace(b.NameZh)), key) ||
				strings.Contains(strings.ToLower(strings.TrimSpace(b.NameEn)), key)
		},
	} {
		for _, book := range books {
			if match(book) {
				return book, true
			}
		}
	}
	return arsenal.Warbond{}, false
}

// ---- 敌人卡片 ----

// BuildEnemyCard 把一次检索结果排成敌人卡片。
//
// result 是图鉴检索的原始结果（已按匹配度排序）：第一只就是用户想查的那只。
// images 是这只敌人的外观图与部位示意图（数据层下不到时为空的），related 是侧栏要列的同阵营邻居，
// 两者都由 handler 准备好；详细资料（描述 / 分类 / 部位数据 / 来源）从内置的社区维基数据里按名字匹配，
// 匹配不上就只有我们自己的图鉴信息。空结果时卡片出一句提示。
func BuildEnemyCard(result bestiary.Result, keyword string, images EnemyImages, related []EnemyLink,
	dataTime time.Time) EnemyCard {
	card := EnemyCard{
		Meta: render.Meta{
			Title:    "敌人图鉴",
			Subtitle: "阵营 · 体型 · 血量 · 部位",
			DataTime: dataTimeText(dataTime),
			Emblem:   "emblem.terminids",
		},
		// 数据来源不再写进卡片（用户 2026-09-17 要求），只留这条「怎么读」的口径说明。
		Notes: []string{
			"派系变体（例如 Jet Brigade）沿用上游原文，加了新变体时不会被硬塞进某个阵营。",
		},
	}
	trimmed := strings.TrimSpace(keyword)
	if len(result.Enemies) == 0 {
		card.Intro = fmt.Sprintf("关键字「%s」没有命中。", trimmed)
		card.Empty = "图鉴里没有这只敌人：支持中文名与英文名，例如「追猎虫」或 Hunter。"
		return card
	}

	enemy := result.Enemies[0]
	if !isExactEnemyName(enemy, trimmed) {
		card.Intro = fmt.Sprintf("「%s」不是完整名字，这里显示最接近的「%s」。", trimmed, plugutil.DefaultText(enemy.NameZh, enemy.Title))
	}
	entry := MatchEnemyDetail(enemy)

	faction, class := factionText(enemy)
	size := plugutil.DefaultText(bestiary.SizeLabel(enemy.Size), plugutil.DashText)
	health := healthText(enemy.Health)
	if entry != nil && strings.TrimSpace(entry.Health) != "" {
		// 内置详情的总生命值是分难度给的（"130 - 4 难度以下 160 - 4 难度以上"），比一个整数更有用。
		health = entry.Health
	}
	item := &EnemyItem{
		Name:    plugutil.DefaultText(enemy.NameZh, enemy.Title),
		English: englishOnly(enemy),
		Image:   images.Icon,
		Tags:    []string{faction, size},
		Health:  health,
		Related: related,
		// 基础信息的前两行来自我们自己的图鉴，内置详情有的那几行由 enemyRows 接在后面。
		Rows: []Field{
			{Label: "阵营", Value: faction, Class: class},
			{Label: "体型", Value: size},
		},
		Parts:    partRows(entry, images),
		Variants: nil,
	}
	if entry != nil {
		item.Desc = entry.Description
		item.Rows = append(item.Rows, enemyRows(entry)...)
		item.Variants = entry.Variants
		item.Source = entry.Source
	}
	card.Enemy = item
	return card
}

// enemyRows 拼敌人卡片「基础信息」里的行：分类、体型、总生命值、伤害、伤害类型、火焰伤害倍率、
// 踉跄阈值、最低难度。数值里已经写清单位的（"50 踉跄力度"）原样显示，不再加单位；
// 内置详情没收录的字段直接不出现，不写「—」占位。
func enemyRows(entry *atlas.Entry) []Field {
	if entry == nil {
		return nil
	}
	candidates := []Field{
		{Label: "分类", Value: entry.Category},
		{Label: "总生命值", Value: entry.Health},
		{Label: "伤害", Value: damageText(entry.Damage)},
		{Label: "伤害类型", Value: entry.DamageType},
		{Label: "火焰伤害倍率", Value: entry.FireMultiplier},
		{Label: "踉跄阈值", Value: entry.Stagger},
		{Label: "最低难度", Value: entry.Difficulty},
	}
	rows := make([]Field, 0, len(candidates))
	for _, field := range candidates {
		if strings.TrimSpace(field.Value) == "" {
			continue
		}
		rows = append(rows, field)
	}
	return rows
}

// damageText 把内置详情的伤害说明压成一行（上游用「;」分隔，段内还可能带换行）；
// 卡片上是表格里的一格，换行会被表格吃成空格，所以这里显式用「；」连起来。
func damageText(damage string) string {
	parts := strings.FieldsFunc(damage, func(r rune) bool { return r == ';' || r == '\n' })
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if cleaned := strings.Join(strings.Fields(part), " "); cleaned != "" {
			out = append(out, cleaned)
		}
	}
	return strings.Join(out, "；")
}

// partRows 把内置详情的部位数据转成卡片上的行，并按部位名补上示意图（拿不到图就空着）。
func partRows(entry *atlas.Entry, images EnemyImages) []PartRow {
	if entry == nil || len(entry.Parts) == 0 {
		return nil
	}
	rows := make([]PartRow, 0, len(entry.Parts))
	for _, part := range entry.Parts {
		rows = append(rows, PartRow{
			Name:     part.Name,
			Count:    part.Count,
			Health:   part.Health,
			Armor:    part.Armor,
			Location: part.Location,
			Durable:  part.Durable,
			ToMain:   part.ToMain,
			Image:    images.Parts[part.Name],
			Fatal:    part.Fatal,
			Weak:     part.Weak,
		})
	}
	return rows
}

// RelatedEnemies 取同阵营的其它敌人（按图鉴顺序，最多 limit 条）：侧栏「同阵营敌人」用。
// 派系变体（Variant 非空）也是独立条目，照常列出来。
func RelatedEnemies(list []bestiary.Enemy, enemy bestiary.Enemy, limit int) []EnemyLink {
	if limit <= 0 {
		return nil
	}
	links := make([]EnemyLink, 0, limit)
	for _, other := range list {
		if other.Faction != enemy.Faction || strings.EqualFold(other.Title, enemy.Title) {
			continue
		}
		links = append(links, EnemyLink{
			Name:     plugutil.DefaultText(other.NameZh, other.Title),
			Category: bestiary.SizeLabel(other.Size),
		})
		if len(links) == limit {
			break
		}
	}
	if len(links) == 0 {
		return nil
	}
	return links
}

// isExactEnemyName 判断关键字是不是这只敌人的完整名字（中文名 / 英文名，忽略大小写与空格）。
func isExactEnemyName(enemy bestiary.Enemy, keyword string) bool {
	needle := foldName(keyword)
	if needle == "" {
		return false
	}
	return foldName(enemy.NameZh) == needle || foldName(enemy.Title) == needle
}

// enemyNames 取这批敌人的展示名。
func enemyNames(enemies []bestiary.Enemy) []string {
	names := make([]string, 0, len(enemies))
	for _, enemy := range enemies {
		names = append(names, plugutil.DefaultText(enemy.NameZh, enemy.Title))
	}
	return names
}

// damageItems 把上游的伤害说明拆成逐条（上游用全角「｜」连接），
// 卡片上一条一行排，比堵在一行里好读；空串与空段在这里就剔掉。
func damageItems(damage string) []string {
	parts := strings.Split(damage, "｜")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		cleaned := strings.Join(strings.Fields(part), " ")
		if cleaned != "" {
			out = append(out, cleaned)
		}
	}
	return out
}

// englishOnly 在中文名与英文名相同时不重复显示英文名。
func englishOnly(enemy bestiary.Enemy) string {
	name := plugutil.DefaultText(enemy.NameZh, enemy.Title)
	if strings.EqualFold(strings.TrimSpace(name), strings.TrimSpace(enemy.Title)) {
		return ""
	}
	return strings.TrimSpace(enemy.Title)
}

// healthText 拼血量；上游没给或解析不出时用占位符（不写 0，0 会被读成「没血」）。
func healthText(health int) string {
	if health <= 0 {
		return plugutil.DashText
	}
	return plugutil.FormatInt(int64(health))
}

// factionText 返回阵营展示文案与配色 class。
// 未收录的阵营照原文显示（站点随时会加新派系，硬塞进某一族只会报错信息）；
// 变体名附在中文阵营名后面的括号里。
func factionText(enemy bestiary.Enemy) (string, string) {
	if enemy.Faction == "" {
		return plugutil.DefaultText(enemy.FactionRaw, plugutil.UnknownText), ""
	}
	name := plugutil.FactionName(enemy.Faction)
	if enemy.Variant != "" {
		name = name + "（" + enemy.Variant + "）"
	}
	return name, plugutil.FactionClass(enemy.Faction)
}

// ---- 单件详情卡 ----

// AttackBlock 是详情卡上的一个攻击部件（弹道 / 爆炸 / 火焰…），
// 对应社区站点详情页里的「攻击 #1」区块。
type AttackBlock struct {
	Title string  // 区块标题，例如「攻击 #1 · 弹道」
	Tag   string  // 上游原文标签（例如 Ballistic）；与中文名相同或为空时不渲染
	Cells []Field // 这一段的数值
}

// DetailSection 是详情卡上的一组数值（操作性 / 部署 / 护甲），标题由模板渲染成带编号的小节。
type DetailSection struct {
	Title string
	Cells []Field
}

// EquipmentDetail 是「关键字精确命中一件」时的详情卡内容。
// 参考站点的详情页把「武器信息 / 战略配备信息」放在右侧栏，这里按用户 2026-09-17 的要求
// 并进主栏：整张卡只有一列，一张图就装得下。
type EquipmentDetail struct {
	Name      string
	English   string
	Model     string
	Tags      []string // 类别与武器类型这类标签
	Image     string   // data URI；没有图时为空串
	Acquire   string
	Arrows    []ArrowStep     // 召唤指令箭头（图标 + 文本符号）；不是战备时为 nil
	SideTitle string          // 右侧栏标题（武器信息 / 战备信息…）；侧栏没内容时为空串
	Side      []Field         // 右侧栏的关键数值
	Sections  []DetailSection // 数值小节，顺序即卡片上的顺序
	Attacks   []AttackBlock   // 攻击部件；没有可展示的数值时为 nil
	Wiki      *WikiBlock      // 内置社区维基的详细属性；没有对应条目时为 nil
}

// WikiBlock 是内置社区维基（见 src/atlas）里的详细资料：缺哪一段就不渲染哪一段。
//
// 数值摊成一列格子（不是一组一段）：参照站详情页的「详细数据」就是一张 4 列小格网，
// 每格左边写属性名、下面写数值，摊平后卡片高度能压到一半。
type WikiBlock struct {
	Unlock   string
	Desc     string
	Traits   []string
	Tips     []string
	Variants []string
	Cells    []Field
	Attacks  []WikiAttack
}

// WikiAttack 是详细资料里的一个攻击条目。
type WikiAttack struct {
	Title string
	Tag   string
	Cells []Field
}

// BuildEquipmentDetail 把一件装备排成详情卡。catalog 只用来把军需簿 id 翻成书名（可以传 nil），
// image 是这件装备的图（data URI），没有图时传空串。
func BuildEquipmentDetail(item arsenal.Item, catalog *arsenal.Catalog, image string) *EquipmentDetail {
	detail := &EquipmentDetail{
		Name:    plugutil.DefaultText(item.NameZh, item.NameEn),
		English: strings.TrimSpace(item.NameEn),
		Model:   strings.TrimSpace(item.Model),
		Tags:    detailTags(item),
		Image:   image,
		Acquire: acquisitionText(catalog, item),
		Arrows:  codeArrows(item.Deploy),
	}
	if detail.Name == detail.English {
		// 中文名缺失时上面会退回英文名，这里不再重复显示一遍。
		detail.English = ""
	}
	entry := matchDetails(item)
	detail.Wiki = buildWikiBlock(entry)
	// 有内置详情的条目不再重复画目录里那几段：维基的「详细属性」已经涵盖操作性（弹匣、射速、后坐力），
	// 「攻击部件」也比目录里那几格细。目录里留下的只有维基没有的护甲数值。
	if detail.Wiki != nil {
		detail.Sections = detailSections(item, false)
		detail.Attacks = nil
		detail.Tags = appendTag(detail.Tags, wikiTag(entry))
	} else {
		detail.Sections = detailSections(item, true)
		detail.Attacks = attackBlocks(item)
	}
	if len(detail.Arrows) == 0 && entry != nil {
		// 上游目录没给指令串的战备，用内置详情里的方向串补上（同一份游戏数据，两个来源写法不同）。
		detail.Arrows = codeArrowsFromAtlas(entry.Code)
	}
	detail.SideTitle, detail.Side = sideInfo(item.Kind, entry, detail.Tags)
	return detail
}

// sideInfo 拼详情卡右侧栏的「武器信息 / 战备信息」：先放类型与分类标签，再放详细属性里最关键的那几个数值
// （伤害、射速、弹匣、冷却…），最后是变体数量。参照站详情页把这些摆在右侧栏，正文只留数值网格。
func sideInfo(kind arsenal.Kind, entry *atlas.Entry, tags []string) (string, []Field) {
	fields := make([]Field, 0, 8)
	kindName := ""
	if len(tags) > 0 {
		kindName = tags[0]
		fields = append(fields, Field{Label: "类型", Value: kindName})
	}
	// 分类优先用内置详情的细分（主武器 → 突击步枪）；那份数据没有细分时退回目录给的第二个标签。
	subCategory := ""
	if entry != nil {
		subCategory = strings.TrimSpace(entry.SubCategory)
	}
	if subCategory == "" && len(tags) > 1 {
		subCategory = tags[1]
	}
	if subCategory != "" && subCategory != kindName {
		fields = append(fields, Field{Label: "分类", Value: subCategory})
	}
	if entry != nil {
		cells := cellIndex(entry)
		for _, label := range sideLabels[entry.Kind] {
			if value := cells[label]; value != "" {
				fields = append(fields, Field{Label: label, Value: value})
			}
		}
	}
	if entry != nil && len(entry.Variants) > 0 {
		fields = append(fields, Field{Label: "变体", Value: fmt.Sprintf("%d 种", len(entry.Variants))})
	}
	if len(fields) == 0 {
		return "", nil
	}
	return sideTitle(kind), fields
}

// sideTitle 是右侧栏的标题：按我们自己目录里的类别取（内置详情把战备与投掷物放在同一张表里，
// 直接用它那个 Kind 会把手雷标成「战备信息」）。
func sideTitle(kind arsenal.Kind) string {
	switch kind {
	case arsenal.KindPrimary, arsenal.KindSecondary, arsenal.KindSupport:
		return "武器信息"
	case arsenal.KindStratagem:
		return "战备信息"
	case arsenal.KindGrenade:
		return "投掷物信息"
	case arsenal.KindArmor:
		return "护甲信息"
	default:
		return "装备信息"
	}
}

// sideLabels 是右侧栏从详细属性里挑出来的关键数值（按这个顺序取）。
var sideLabels = map[atlas.Kind][]string{
	atlas.KindWeapon:    {"标准伤害", "射速", "后坐力", "弹匣容量", "直射穿透"},
	atlas.KindStratagem: {"冷却时间", "使用次数", "呼叫时间", "主体生命值", "主体装甲"},
}

// cellIndex 把详细属性所有网格（含攻击条目里的）摊成「标签 → 数值」，同名标签先出现的为准。
func cellIndex(entry *atlas.Entry) map[string]string {
	cells := make(map[string]string)
	collect := func(groups []atlas.Group) {
		for _, group := range groups {
			for _, cell := range group.Cells {
				if _, seen := cells[cell.Label]; !seen && strings.TrimSpace(cell.Value) != "" {
					cells[cell.Label] = cell.Value
				}
			}
		}
	}
	collect(entry.Stats)
	for _, attack := range entry.Attacks {
		collect(attack.Groups)
	}
	return cells
}

// wikiTag 返回内置详情给的分类标签（轨道 / 飞鹰 / 哨戒炮…）；与已有标签重复或为空时返回空串。
func wikiTag(entry *atlas.Entry) string {
	if entry == nil {
		return ""
	}
	return strings.TrimSpace(entry.Category)
}

// buildWikiBlock 把内置详情翻成卡片用的段；没有可显示内容时返回 nil。
func buildWikiBlock(entry *atlas.Entry) *WikiBlock {
	if entry == nil {
		return nil
	}
	block := &WikiBlock{
		Unlock:   strings.TrimSpace(entry.Unlock),
		Desc:     strings.TrimSpace(entry.Description),
		Traits:   entry.Traits,
		Tips:     entry.Tips,
		Variants: entry.Variants,
	}
	for _, group := range entry.Stats {
		block.Cells = append(block.Cells, wikiCells(group)...)
	}
	attackNo := 0
	for _, attack := range entry.Attacks {
		converted := WikiAttack{Title: strings.TrimSpace(attack.Title), Tag: strings.TrimSpace(attack.Type)}
		if converted.Title == "" {
			attackNo++
			converted.Title = fmt.Sprintf("攻击 #%d", attackNo)
		}
		for _, group := range attack.Groups {
			converted.Cells = append(converted.Cells, wikiCells(group)...)
		}
		if len(converted.Cells) > 0 {
			block.Attacks = append(block.Attacks, converted)
		}
	}
	if block.Unlock == "" && block.Desc == "" && len(block.Cells) == 0 && len(block.Attacks) == 0 &&
		len(block.Traits) == 0 && len(block.Tips) == 0 && len(block.Variants) == 0 {
		return nil
	}
	return block
}

// wikiCells 把数据层的一组数值转成卡片上的字段。
func wikiCells(group atlas.Group) []Field {
	cells := make([]Field, 0, len(group.Cells))
	for _, cell := range group.Cells {
		cells = append(cells, Field{Label: cell.Label, Value: cell.Value})
	}
	return cells
}

// codeArrowsFromAtlas 把内置详情的指令串（"up"/"down"/"left"/"right"）翻成箭头；
// 认不出的方向原样保留，与 codeArrows 一个口径。
func codeArrowsFromAtlas(code []string) []ArrowStep {
	if len(code) == 0 {
		return nil
	}
	arrows := make([]ArrowStep, 0, len(code))
	for _, step := range code {
		raw := strings.TrimSpace(step)
		if arrow, ok := summonArrows[strings.ToLower(raw)]; ok {
			arrows = append(arrows, arrow)
			continue
		}
		arrows = append(arrows, ArrowStep{Text: raw})
	}
	return arrows
}

// detailTags 拼详情卡头部的标签：类别 + 武器类型。
// 目录里 165 件没有武器类型，缺了就只显示类别。
func detailTags(item arsenal.Item) []string {
	tags := []string{arsenal.KindLabel(item.Kind)}
	if name := weaponTypeName(item.WeaponType); name != "" {
		tags = append(tags, name)
	}
	return tags
}

// appendTag 往标签里加一个：空串与已有标签（忽略大小写）都跳过。
func appendTag(tags []string, tag string) []string {
	trimmed := strings.TrimSpace(tag)
	if trimmed == "" {
		return tags
	}
	for _, existing := range tags {
		if strings.EqualFold(existing, trimmed) {
			return tags
		}
	}
	return append(tags, trimmed)
}

// weaponTypeNames 是目录里 weaponType 的中文名。
// 只收实测出现过的取值，没收录的原样显示——不猜。
var weaponTypeNames = map[string]string{
	"Assault Rifles":       "突击步枪",
	"Submachine Guns":      "冲锋枪",
	"Pistols":              "手枪",
	"Shotguns":             "霰弹枪",
	"Marksman Rifles":      "精准步枪",
	"Explosives":           "爆破武器",
	"Melee":                "近战武器",
	"Standard":             "标准装备",
	"Special":              "特化装备",
	"Energy-Based":         "能量武器",
	"Heavy Weapons":        "重型武器",
	"Heavy Energy-Based":   "重型能量武器",
	"Heavy Explosives":     "重型爆破武器",
	"Heavy Explosive":      "重型爆破武器",
	"Anti-Tank":            "反坦克",
	"Anti-Armor Precision": "反装甲精准武器",
	"Incendiary":           "燃烧武器",
	"Missiles":             "导弹",
	"Rocket Launcher":      "火箭筒",
	"Stun Tesla":           "电击武器",
}

// weaponTypeName 返回武器类型的中文名；空串返回空串（模板据此不渲染这个标签）。
func weaponTypeName(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if zh, ok := weaponTypeNames[trimmed]; ok {
		return zh
	}
	return trimmed
}

// detailSections 组详情卡的数值小节：操作性 → 部署 → 护甲（哪些有数据就出哪些）。
// withUpstream 为 false 表示卡片已经有内置详情的「详细属性」（那边的弹匣/射速/后坐力/冷却更全），
// 这时只留维基没有的护甲数值，避免同一份数字在卡片上出现两遍。
func detailSections(item arsenal.Item, withUpstream bool) []DetailSection {
	sections := make([]DetailSection, 0, 3)
	if withUpstream {
		if cells := handlingFields(item); len(cells) > 0 {
			sections = append(sections, DetailSection{Title: "操作性", Cells: cells})
		}
		if cells := deployFields(item); len(cells) > 0 {
			sections = append(sections, DetailSection{Title: "部署", Cells: cells})
		}
	}
	if cells := armorFields(item); len(cells) > 0 {
		sections = append(sections, DetailSection{Title: "护甲", Cells: cells})
	}
	return sections
}

// handlingFields 拼操作性数值。与列表模式的 equipmentFields 不同，这里把弹匣与备弹分两格，
// 详情卡有的是地方，不必为了省一行而拼字符串。
func handlingFields(item arsenal.Item) []Field {
	fields := make([]Field, 0, 5)
	if item.Handling.Magazine > 0 {
		fields = append(fields, Field{Label: "弹匣", Value: fmt.Sprintf("%d 发", item.Handling.Magazine)})
	}
	if item.Handling.SpareMagazines > 0 {
		fields = append(fields, Field{Label: "备用弹匣", Value: fmt.Sprintf("%d 个", item.Handling.SpareMagazines)})
	}
	if item.Handling.FireRate > 0 {
		fields = append(fields, Field{Label: "射速", Value: fmt.Sprintf("%d 发/分", item.Handling.FireRate)})
	}
	if item.Handling.Recoil > 0 {
		fields = append(fields, Field{Label: "后坐力", Value: strconv.FormatFloat(item.Handling.Recoil, 'f', -1, 64)})
	}
	if modes := modesText(item.Handling.FiringModes); modes != "" {
		fields = append(fields, Field{Label: "射击模式", Value: modes})
	}
	return fields
}

// deployFields 拼战备的部署信息：部署类型、冷却、呼叫时间。
func deployFields(item arsenal.Item) []Field {
	if item.Deploy == nil {
		return nil
	}
	fields := make([]Field, 0, 3)
	if name := deployTypeName(item.Deploy.Type); name != "" {
		fields = append(fields, Field{Label: "部署类型", Value: name})
	}
	if item.Deploy.CooldownSeconds > 0 {
		fields = append(fields, Field{Label: "冷却", Value: fmt.Sprintf("%d 秒", item.Deploy.CooldownSeconds)})
	}
	if item.Deploy.CallInSeconds > 0 {
		fields = append(fields, Field{Label: "呼叫时间", Value: strconv.FormatFloat(item.Deploy.CallInSeconds, 'f', -1, 64) + " 秒"})
	}
	return fields
}

// attackBlocks 把每个伤害部件排成一块：标题里带上编号与部件名，
// 原文标签（Ballistic 这类）单独做一个标签；一个数值都没有的部件直接跳过，不出空网格。
func attackBlocks(item arsenal.Item) []AttackBlock {
	blocks := make([]AttackBlock, 0, len(item.Components))
	for _, component := range item.Components {
		cells := componentFields(component)
		if len(cells) == 0 {
			continue
		}
		title := fmt.Sprintf("攻击 #%d", len(blocks)+1)
		if name := componentName(component.Label); name != "" {
			title += " · " + name
		}
		tag := strings.TrimSpace(component.Label)
		if strings.EqualFold(tag, componentName(component.Label)) {
			// 翻译后与原文一模一样（未收录的标签就是这种情况）时不再重复出一个标签。
			tag = ""
		}
		blocks = append(blocks, AttackBlock{Title: title, Tag: tag, Cells: cells})
	}
	return blocks
}

// componentNames 是伤害部件标签的中文名。上游这一列并不干净（实测混着大小写不一的 explosion / spray / projectile），
// 所以键统一折叠大小写，没收录的原样显示。
var componentNames = map[string]string{
	"ballistic":        "弹道",
	"explosion":        "爆炸",
	"impact explosion": "撞击爆炸",
	"fire":             "火焰",
	"arc":              "电弧",
	"gas":              "毒气",
	"melee":            "近战",
	"laser":            "激光",
	"beam":             "光束",
	"direct":           "直击",
	"projectile":       "弹丸",
	"spray":            "喷射",
	"shrapnel":         "破片",
}

// componentName 返回部件标签的中文名；没收录的原样返回，空串囔空串。
func componentName(label string) string {
	trimmed := strings.TrimSpace(label)
	if trimmed == "" {
		return ""
	}
	if zh, ok := componentNames[strings.ToLower(trimmed)]; ok {
		return zh
	}
	return trimmed
}

// componentFields 拼一个部件的数值：标准伤害 → 耐久伤害 → 持续伤害 → 穿甲 → 破拆力 → 击退 → 推力。
// 没有的字段不出格（不用占位符把网格撑满）。
func componentFields(component arsenal.Component) []Field {
	fields := make([]Field, 0, 7)
	if component.StandardDamage > 0 {
		fields = append(fields, Field{Label: "标准伤害", Value: plugutil.FormatInt(int64(component.StandardDamage))})
	}
	if component.DurableDamage > 0 {
		fields = append(fields, Field{Label: "耐久伤害", Value: plugutil.FormatInt(int64(component.DurableDamage))})
	}
	if component.DPS > 0 {
		fields = append(fields, Field{Label: "持续伤害", Value: fmt.Sprintf("%d/秒", component.DPS)})
	}
	if pen := strings.TrimSpace(component.ArmorPenLabel); pen != "" {
		fields = append(fields, Field{Label: "穿甲", Value: pen})
	} else if component.ArmorPenValue > 0 {
		fields = append(fields, Field{Label: "穿甲", Value: strconv.Itoa(component.ArmorPenValue)})
	}
	if component.DemolitionForce > 0 {
		fields = append(fields, Field{Label: "破拆力", Value: plugutil.FormatInt(int64(component.DemolitionForce))})
	}
	if component.Stagger > 0 {
		fields = append(fields, Field{Label: "击退", Value: plugutil.FormatInt(int64(component.Stagger))})
	}
	if component.Push > 0 {
		fields = append(fields, Field{Label: "推力", Value: plugutil.FormatInt(int64(component.Push))})
	}
	return fields
}
