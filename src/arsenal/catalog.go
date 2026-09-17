// Package arsenal 是《绝地潜兵 2》装备目录（军需簿 + 武器 / 战备 / 护甲 / 手雷）的数据层：
// 拉取上游目录、落本地缓存、按关键字检索，并按需下载装备图。
//
// 数据来源与许可：目录数据取自开源项目 SalmonC/HD2Tool（MIT License）的
// src/data/catalog.json 与 src/data/community-aliases.json；装备图取自同一仓库的 public/assets/wiki/。
// 图片自身的许可由目录里的 image.license 标注（实测 188 张 CC-BY-NC-SA、109 张 Arrowhead、1 张 Fairuse），
// 也就是说这些图是**游戏内素材或 wiki 截图**，不是 HD2Tool 自绘；上游仓库的 MIT 只覆盖代码与数据编排。
// 《Helldivers》及相关名称是 Arrowhead Game Studios / Sony Interactive Entertainment 的商标或知识产权，
// 本项目是非官方粉丝作品，与上述公司无关联。完整口径见 src/render/assets/LICENSES.md。
//
// 本包不做任何展示格式化（中文文案、千分位、箭头图案都在插件层）：
// 它只回答「有哪些装备」「命中哪些」「图在哪」，这样数据层能被独立测试，换上游也只需要改这里。
package arsenal

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Kind 是装备类别，取值就是上游 productKind 的原值：既当分类键，也当检索时的过滤条件。
type Kind string

const (
	KindPrimary   Kind = "primary-weapon"   // 主武器
	KindSecondary Kind = "secondary-weapon" // 副武器
	KindSupport   Kind = "support-weapon"   // 支援武器（战备里带支援位的那类）
	KindGrenade   Kind = "grenade"          // 手雷
	KindArmor     Kind = "body-armor"       // 护甲
	KindStratagem Kind = "other-stratagem"  // 其它战备（哨戒炮、轨道打击、飞鹰等）
)

// Weapons 是「一次查全部武器」的三个类别，顺序即卡片上的分组顺序。
var Weapons = []Kind{KindPrimary, KindSecondary, KindSupport}

// kindLabels 是类别中文名。游戏里没有「副武器」这种叫法，但群里说「副武器」比说
// 「secondary-weapon」清楚，所以固定成这六个词，卡片与帮助文案共用同一份。
var kindLabels = map[Kind]string{
	KindPrimary:   "主武器",
	KindSecondary: "副武器",
	KindSupport:   "支援武器",
	KindGrenade:   "手雷",
	KindArmor:     "护甲",
	KindStratagem: "战备",
}

// KindLabel 返回类别中文名；未收录的类别回退成上游原值（绝不返回空串）。
func KindLabel(k Kind) string {
	if label, ok := kindLabels[k]; ok {
		return label
	}
	return string(k)
}

// acqLabels 是获取方式中文名，键是上游 acquisition.kind 的原值。
var acqLabels = map[string]string{
	"warbond":     "军需簿",
	"requisition": "征用点",
	"superstore":  "超级商店",
	"event":       "活动奖励",
	"default":     "默认解锁",
	"edition":     "版本奖励",
	"poi":         "兴趣点",
	"unavailable": "已下架",
}

// AcqLabel 返回获取方式中文名；未收录的取值原样返回（不猜，也不吞）。
func AcqLabel(kind string) string {
	if label, ok := acqLabels[kind]; ok {
		return label
	}
	return kind
}

// Component 是装备的一个伤害部件（弹丸 / 爆炸 / 火焰 / 激光 / 近战…）。
// 上游一件装备可能给多个部件（哨戒炮就是「弹丸 + 爆炸」两段），卡片只展示主部件，
// 但两个都留着，免得以后想展示时又要改数据结构。
type Component struct {
	Label           string // 上游的部件标签，例如 Ballistic / Explosion / Fire
	StandardDamage  int    // 标准伤害
	DurableDamage   int    // 对耐用部位（重甲单位躯干等）伤害
	DPS             int    // 持续伤害（火焰/毒气这类）
	ArmorPenLabel   string // 穿甲等级中文名，例如「反坦克 III」「中型」
	ArmorPenValue   int    // 穿甲等级数值
	DemolitionForce int    // 破拆力
	Stagger         int
	Push            int
}

// Handling 是操作性数值；不同类装备字段不齐（手雷没有弹匣），缺的字段就是零值。
type Handling struct {
	Magazine       int
	SpareMagazines int
	FireRate       int
	Recoil         float64
	FiringModes    []string // Auto / Semi / Burst
}

// Armor 是护甲专属数值。
type Armor struct {
	Class        string // Light / Medium / Heavy
	Rating       int    // 护甲值
	Speed        int    // 移动速度评级
	StaminaRegen int    // 耐力回复评级
	Passive      string // 被动名（上游是英文，译文由插件层的翻译层负责）
}

// Deploy 是战备专属信息：召唤指令序列 + 冷却。
type Deploy struct {
	Type            string   // Support Weapon / Sentry / Eagle / Orbital …
	Code            []string // 召唤指令：down / up / left / right
	CooldownSeconds int
	CallInSeconds   float64
}

// Acquisition 是「怎么拿到这件装备」。Kind 决定哪几个字段有意义：
// 军需簿看 WarbondID/Page/ItemMedals，征用点看 LevelRequired/RequisitionPoints，超级商店等只有 Kind。
type Acquisition struct {
	Kind              string
	WarbondID         string
	Page              int
	ItemMedals        int
	LevelRequired     int
	RequisitionPoints int
}

// Item 是一件装备。字段都是上游原值，没有做过展示格式化。
type Item struct {
	ID            string
	Kind          Kind
	Model         string   // 型号，例如 AR-23；上游没给时为空串
	NameZh        string   // 官方简中名
	NameEn        string   // 英文名
	Aliases       []string // 玩家外号（来自 community-aliases.json）
	WeaponType    string   // 上游武器分类，例如 Assault Rifles / Melee
	ImagePath     string   // HD2Tool 仓库内的相对路径，例如 assets/wiki/ar-2-coyote.png
	ImageIsSVG    bool     // 是不是 SVG（SVG 不能直接当 Telegram 行内缩略图，需要换 PNG）
	ImageFilePage string   // wiki.gg 的 File: 页地址，用于取 PNG 缩略图
	ImageLicense  string   // 图片许可，例如 License/Arrowhead
	WikiURL       string
	Acq           Acquisition
	Components    []Component
	Primary       int // Components 里主部件的下标；没有部件时为 -1
	Handling      Handling
	Armor         *Armor
	Deploy        *Deploy
}

// Warbond 是一本军需簿（债券）：页数、解锁所需勋章、超级货币价。
type Warbond struct {
	ID           string
	NameZh       string
	NameEn       string
	Pages        int // 页数
	MedalsTotal  int // 全部解锁所需勋章（上游给的是每页累计值，取最大的那一页）
	SuperCredits int // 超级货币价；0 表示不单卖（例如首发那本）
	WikiURL      string
	ItemCount    int // 这本债券包含多少件装备（由目录反查，上游不直接给）
}

// Catalog 是一份解析好的装备目录。解析完成后只读，可并发使用。
type Catalog struct {
	items     []Item
	byID      map[string]Item
	warbonds  []Warbond
	byWarbond map[string]Warbond
	itemsOf   map[string][]Item // 债券 id → 该债券的装备（保持目录顺序）
	// DataVersion / CapturedAt 是上游标注的数据版本与采集时间，卡片页脚用它说明数据是哪一版。
	DataVersion string
	CapturedAt  string
}

// Items 返回全部装备（目录顺序的副本）。
func (c *Catalog) Items() []Item {
	out := make([]Item, len(c.items))
	copy(out, c.items)
	return out
}

// Item 按 id 取装备；不存在时返回 false。
func (c *Catalog) Item(id string) (Item, bool) {
	it, ok := c.byID[id]
	return it, ok
}

// Warbonds 返回全部债券（目录顺序：首发在前）。
func (c *Catalog) Warbonds() []Warbond {
	out := make([]Warbond, len(c.warbonds))
	copy(out, c.warbonds)
	return out
}

// Warbond 按 id 取债券；不存在时返回 false。
func (c *Catalog) Warbond(id string) (Warbond, bool) {
	w, ok := c.byWarbond[id]
	return w, ok
}

// WarbondName 返回债券中文名；未知 id 返回空串（调用方据此决定怎么显示「未知来源」）。
func (c *Catalog) WarbondName(id string) string {
	if w, ok := c.byWarbond[id]; ok {
		return w.NameZh
	}
	return ""
}

// ItemsOfWarbond 返回某本债券里的装备（保持目录顺序）；未知债券返回空切片。
func (c *Catalog) ItemsOfWarbond(id string) []Item {
	items := c.itemsOf[id]
	out := make([]Item, len(items))
	copy(out, items)
	return out
}

// Len 返回装备总件数。
func (c *Catalog) Len() int { return len(c.items) }

// jsonInt 是「上游写整数也认、写小数也认」的数值类型。
//
// 为什么需要它：上游把两件装备的弹匣写成 6.67 / 12.5（实测），再用小数写别的字段只是时间问题，
// 而用 int 接收会让**整份目录**解析失败——一件装备的脏数据不该让 298 件都查不了。
// 小数按四舍五入取整（6.67 → 7），数值本来只用于展示；实在解析不出来就留 0（卡片上不显示那一项），
// 绝不因为一个数字把整张卡片打掉。
type jsonInt int

// UnmarshalJSON 认下数字、数字字符串（"45"）与 null；认不出的留 0。
func (v *jsonInt) UnmarshalJSON(raw []byte) error {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" {
		return nil
	}
	if text[0] == '"' {
		unquoted, err := strconv.Unquote(text)
		if err != nil {
			return nil
		}
		text = strings.TrimSpace(unquoted)
	}
	if f, err := strconv.ParseFloat(text, 64); err == nil {
		*v = jsonInt(math.Round(f))
	}
	return nil
}

// rawCatalog 是 catalog.json 的解析目标。只声明用得上的字段：
// 上游还有 currencies、demolitionSource 等我们用不到的结构，不声明它们可以少踩「上游小改字段就解析失败」的坑。
type rawCatalog struct {
	Meta struct {
		DataVersion string `json:"dataVersion"`
		CapturedAt  string `json:"capturedAt"`
	} `json:"meta"`
	Warbonds []rawWarbond `json:"warbonds"`
	Items    []rawItem    `json:"items"`
}

type rawWarbond struct {
	ID           string  `json:"id"`
	NameZh       string  `json:"nameZh"`
	NameEn       string  `json:"nameEn"`
	SuperCredits jsonInt `json:"superCredits"`
	Pages        []struct {
		Page             jsonInt `json:"page"`
		CumulativeMedals jsonInt `json:"cumulativeMedals"`
	} `json:"pages"`
	Wiki struct {
		URL string `json:"url"`
	} `json:"wiki"`
}

type rawItem struct {
	ID          string `json:"id"`
	ProductKind string `json:"productKind"`
	Model       string `json:"model"`
	NameZh      string `json:"nameZh"`
	NameEn      string `json:"nameEn"`
	WeaponType  string `json:"weaponType"`
	Image       struct {
		Path     string `json:"path"`
		FilePage string `json:"filePage"`
		License  string `json:"license"`
	} `json:"image"`
	Wiki struct {
		URL string `json:"url"`
	} `json:"wiki"`
	Acquisition struct {
		Kind              string  `json:"kind"`
		WarbondID         string  `json:"warbondId"`
		Page              jsonInt `json:"page"`
		ItemMedals        jsonInt `json:"itemMedals"`
		LevelRequired     jsonInt `json:"levelRequired"`
		RequisitionPoints jsonInt `json:"requisitionPoints"`
	} `json:"acquisition"`
	Combat struct {
		Components []struct {
			Label  string `json:"label"`
			Fields struct {
				StandardDamage   jsonInt `json:"standardDamage"`
				DurableDamage    jsonInt `json:"durableDamage"`
				DPS              jsonInt `json:"dps"`
				DemolitionForce  jsonInt `json:"demolitionForce"`
				Stagger          jsonInt `json:"stagger"`
				Push             jsonInt `json:"push"`
				ArmorPenetration struct {
					Value   jsonInt `json:"value"`
					LabelZh string  `json:"labelZh"`
				} `json:"armorPenetration"`
			} `json:"fields"`
		} `json:"components"`
		PrimaryComponentID string `json:"primaryComponentId"`
	} `json:"combat"`
	Handling struct {
		Magazine       jsonInt  `json:"magazine"`
		SpareMagazines jsonInt  `json:"spareMagazines"`
		FireRate       jsonInt  `json:"fireRate"`
		Recoil         float64  `json:"recoil"`
		FiringModes    []string `json:"firingModes"`
	} `json:"handling"`
	Armor *struct {
		Class        string  `json:"class"`
		Rating       jsonInt `json:"rating"`
		Speed        jsonInt `json:"speed"`
		StaminaRegen jsonInt `json:"staminaRegen"`
		Passive      string  `json:"passive"`
	} `json:"armor"`
	Deployment *struct {
		Type            string   `json:"type"`
		Code            []string `json:"code"`
		CooldownSeconds jsonInt  `json:"cooldownSeconds"`
		CallInSeconds   float64  `json:"callInSeconds"`
	} `json:"deployment"`
}

// rawAliases 是 community-aliases.json 的解析目标。
type rawAliases struct {
	Entries []struct {
		EquipmentID string   `json:"equipmentId"`
		Aliases     []string `json:"aliases"`
	} `json:"entries"`
}

// ParseCatalog 解析目录 JSON 并建立索引；结构不对（没有装备、缺 id/英文名）时返回中文错误。
//
// 为什么在这里就判「结构不对」：目录是外部数据，坏掉的后果是整条命令变成空卡片，
// 而这种失败在群里看起来和「没搜到」一模一样，很难查。宁可在这里报错并记日志。
func ParseCatalog(raw []byte, aliases map[string][]string) (*Catalog, error) {
	var parsed rawCatalog
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("解析装备目录失败：%w", err)
	}
	if len(parsed.Items) == 0 {
		return nil, fmt.Errorf("解析装备目录失败：items 为空")
	}
	if aliases == nil {
		aliases = map[string][]string{}
	}

	c := &Catalog{
		items:       make([]Item, 0, len(parsed.Items)),
		byID:        make(map[string]Item, len(parsed.Items)),
		byWarbond:   make(map[string]Warbond, len(parsed.Warbonds)),
		itemsOf:     make(map[string][]Item, len(parsed.Warbonds)),
		DataVersion: parsed.Meta.DataVersion,
		CapturedAt:  parsed.Meta.CapturedAt,
	}
	for _, w := range parsed.Warbonds {
		if w.ID == "" {
			continue
		}
		warbond := Warbond{
			ID:           w.ID,
			NameZh:       strings.TrimSpace(w.NameZh),
			NameEn:       strings.TrimSpace(w.NameEn),
			Pages:        len(w.Pages),
			SuperCredits: int(w.SuperCredits),
			WikiURL:      w.Wiki.URL,
		}
		for _, page := range w.Pages {
			// 上游给的是「每页累计勋章」，总勋章就是最后一页的累计值；
			// 用 max 而不是「取最后一个」：页面顺序万一被打乱，也不会算出一个小得离谱的总数。
			if medals := int(page.CumulativeMedals); medals > warbond.MedalsTotal {
				warbond.MedalsTotal = medals
			}
		}
		if warbond.NameZh == "" {
			warbond.NameZh = warbond.NameEn
		}
		c.warbonds = append(c.warbonds, warbond)
		c.byWarbond[warbond.ID] = warbond
	}

	for _, it := range parsed.Items {
		if strings.TrimSpace(it.ID) == "" || strings.TrimSpace(it.NameEn) == "" {
			return nil, fmt.Errorf("解析装备目录失败：有条目缺少 id 或 nameEn")
		}
		item := Item{
			ID:            it.ID,
			Kind:          Kind(it.ProductKind),
			Model:         strings.TrimSpace(it.Model),
			NameZh:        strings.TrimSpace(it.NameZh),
			NameEn:        strings.TrimSpace(it.NameEn),
			Aliases:       aliases[it.ID],
			WeaponType:    strings.TrimSpace(it.WeaponType),
			ImagePath:     strings.TrimSpace(it.Image.Path),
			ImageIsSVG:    strings.HasSuffix(strings.ToLower(it.Image.Path), ".svg"),
			ImageFilePage: strings.TrimSpace(it.Image.FilePage),
			ImageLicense:  strings.TrimSpace(it.Image.License),
			WikiURL:       it.Wiki.URL,
			Acq: Acquisition{
				Kind:              it.Acquisition.Kind,
				WarbondID:         it.Acquisition.WarbondID,
				Page:              int(it.Acquisition.Page),
				ItemMedals:        int(it.Acquisition.ItemMedals),
				LevelRequired:     int(it.Acquisition.LevelRequired),
				RequisitionPoints: int(it.Acquisition.RequisitionPoints),
			},
			Handling: Handling{
				Magazine:       int(it.Handling.Magazine),
				SpareMagazines: int(it.Handling.SpareMagazines),
				FireRate:       int(it.Handling.FireRate),
				Recoil:         it.Handling.Recoil,
				FiringModes:    it.Handling.FiringModes,
			},
		}
		if item.NameZh == "" {
			// 上游 298 件里 291 件是官方译名、7 件是社区校对；真缺了就退回英文名，
			// 绝不让卡片出现一个空标题。
			item.NameZh = item.NameEn
		}
		for _, comp := range it.Combat.Components {
			item.Components = append(item.Components, Component{
				Label:           comp.Label,
				StandardDamage:  int(comp.Fields.StandardDamage),
				DurableDamage:   int(comp.Fields.DurableDamage),
				DPS:             int(comp.Fields.DPS),
				ArmorPenLabel:   comp.Fields.ArmorPenetration.LabelZh,
				ArmorPenValue:   int(comp.Fields.ArmorPenetration.Value),
				DemolitionForce: int(comp.Fields.DemolitionForce),
				Stagger:         int(comp.Fields.Stagger),
				Push:            int(comp.Fields.Push),
			})
		}
		item.Primary = primaryComponentIndex(it.Combat.PrimaryComponentID, len(item.Components))
		if it.Armor != nil {
			item.Armor = &Armor{
				Class:        it.Armor.Class,
				Rating:       int(it.Armor.Rating),
				Speed:        int(it.Armor.Speed),
				StaminaRegen: int(it.Armor.StaminaRegen),
				Passive:      it.Armor.Passive,
			}
		}
		if it.Deployment != nil {
			item.Deploy = &Deploy{
				Type:            it.Deployment.Type,
				Code:            it.Deployment.Code,
				CooldownSeconds: int(it.Deployment.CooldownSeconds),
				CallInSeconds:   it.Deployment.CallInSeconds,
			}
		}
		c.items = append(c.items, item)
		c.byID[item.ID] = item
	}

	// 债券 → 装备：必须在装备全部解析完之后再建，且只收 kind=warbond 的条目。
	for _, item := range c.items {
		if item.Acq.Kind != "warbond" || item.Acq.WarbondID == "" {
			continue
		}
		c.itemsOf[item.Acq.WarbondID] = append(c.itemsOf[item.Acq.WarbondID], item)
	}
	// 装备数写回债券：Warbonds() 返回的是副本，所以这里要把改动同步进切片与索引两处。
	for i := range c.warbonds {
		c.warbonds[i].ItemCount = len(c.itemsOf[c.warbonds[i].ID])
		c.byWarbond[c.warbonds[i].ID] = c.warbonds[i]
	}
	return c, nil
}

// primaryComponentIndex 把上游的 primaryComponentId（形如 "20141-component-1"）解析成部件下标。
// 解析不出来时退回第一个部件：卡片上少一行是小事，整件装备不显示才是大事。
func primaryComponentIndex(id string, total int) int {
	if total <= 0 {
		return -1
	}
	if _, after, found := strings.Cut(id, "-component-"); found {
		if n, err := strconv.Atoi(strings.TrimSpace(after)); err == nil && n >= 1 && n <= total {
			return n - 1
		}
	}
	return 0
}

// ParseAliases 解析玩家外号表；文件缺字段、条目为空都只是「少几条别名」，不报错。
//
// 与目录不同：别名是锦上添花，坏掉顶多搜不到外号，不值得让整条命令失败。
func ParseAliases(raw []byte) map[string][]string {
	var parsed rawAliases
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return map[string][]string{}
	}
	out := make(map[string][]string, len(parsed.Entries))
	for _, entry := range parsed.Entries {
		id := strings.TrimSpace(entry.EquipmentID)
		if id == "" {
			continue
		}
		cleaned := make([]string, 0, len(entry.Aliases))
		for _, alias := range entry.Aliases {
			if alias = strings.TrimSpace(alias); alias != "" {
				cleaned = append(cleaned, alias)
			}
		}
		if len(cleaned) > 0 {
			out[id] = cleaned
		}
	}
	return out
}

// defaultSearchLimit 是检索在 limit <= 0 时的默认条数上限。
const defaultSearchLimit = 8

// Query 是一次装备检索。
// Keyword 为空表示「不打关键字」：这时返回该类别目录顺序的前若干件，
// 而不是空结果——群里打 /gun 什么都不带时，给一份清单比回一句「请提供关键字」有用。
type Query struct {
	Kind    Kind   // 单类别检索；为空且 Kinds 也为空时表示不限类别
	Kinds   []Kind // 多类别检索（/gun 一次查主/副/支援三种）；非空时优先于 Kind
	Keyword string
	Limit   int
}

// Result 是一次检索的结果：命中的条目（已截断到 limit）与未截断的命中总数。
type Result struct {
	Items []Item
	Total int
}

// Search 按关键字检索装备。
//
// 匹配范围：中文名、英文名、id、型号、玩家外号，都做首尾空白裁剪与大小写折叠。
// 排序：完全相等 → 前缀命中 → 包含命中；同一档内保持目录顺序（稳定，同一关键字每次结果一致）。
// 空关键字不做匹配，直接按目录顺序返回——「默认列表」和「搜索」共用同一条路径，行为可预期。
func (c *Catalog) Search(q Query) Result {
	limit := q.Limit
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	kinds := q.Kinds
	if len(kinds) == 0 && q.Kind != "" {
		kinds = []Kind{q.Kind}
	}
	allow := make(map[Kind]bool, len(kinds))
	for _, k := range kinds {
		allow[k] = true
	}

	keyword := strings.ToLower(strings.TrimSpace(q.Keyword))
	type scored struct {
		item  Item
		score int
	}
	matched := make([]scored, 0, len(c.items))
	for _, item := range c.items {
		if len(allow) > 0 && !allow[item.Kind] {
			continue
		}
		if keyword == "" {
			matched = append(matched, scored{item: item})
			continue
		}
		score := item.matchScore(keyword)
		if score == 0 {
			continue
		}
		matched = append(matched, scored{item: item, score: score})
	}

	// 稳定排序：同分保持目录顺序，避免同一关键字两次结果不一样。
	sort.SliceStable(matched, func(i, j int) bool { return matched[i].score > matched[j].score })

	result := Result{Total: len(matched)}
	for i, m := range matched {
		if i >= limit {
			break
		}
		result.Items = append(result.Items, m.item)
	}
	return result
}

// matchScore 给一件装备与关键字的匹配度打分：3 = 完全相等，2 = 前缀命中，1 = 包含命中，0 = 不匹配。
// 型号与外号都参与匹配：群里说「电喷」「EMP 迫击炮」时，用户心里想的就是某件具体装备。
func (it Item) matchScore(keyword string) int {
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
	consider(it.NameZh)
	consider(it.NameEn)
	consider(it.ID)
	consider(it.Model)
	for _, alias := range it.Aliases {
		consider(alias)
	}
	return best
}
