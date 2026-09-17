// Package atlas 是《绝地潜兵 2》图鉴的「详细资料」数据层：把社区中文维基
// （Jerry114514.github.io 的 HD2_Wiki/data/wiki/zh）整理好的武器 / 战备 / 敌人详情内置进仓库，
// 按名字（中文名、英文名、条目 id）索引，供图鉴卡片在命中后补充详细属性。
//
// 与 src/arsenal、src/bestiary 的分工：那两个包回答「有哪些装备 / 敌人」，是目录与检索引擎，
// 数据在运行期从上游拉取；本包只回答「这条目的详细属性长什么样」，数据随仓库发布，
// 因此少一条只是卡片少几段内容，不会让命令失败。
//
// 数据来源与许可见 src/render/assets/LICENSES.md。
package atlas

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

//go:embed data/weapons.json data/stratagems.json data/enemies.json data/terms.json
var dataFiles embed.FS

// Kind 是条目的类别。
type Kind string

const (
	// KindWeapon 是武器（主武器 / 副武器 / 支援武器 / 投掷物）。
	KindWeapon Kind = "weapon"
	// KindStratagem 是战备。
	KindStratagem Kind = "stratagem"
	// KindEnemy 是敌人。
	KindEnemy Kind = "enemy"
)

// Cell 是一格「标签 + 数值」。标签已中文化，数值已翻译可翻译的部分。
type Cell struct {
	Label string
	Value string
}

// Group 是一组数值，对应参照站详情页里的一个网格。
type Group struct {
	Title string
	Cells []Cell
}

// Block 是一个攻击条目（参照站详情页的「攻击 #N」），内部按弹体 / 伤害 / 穿透等再分组。
type Block struct {
	Title  string // 上游给的名字，取不到时为空串
	Type   string // 部件类型的中文名（弹道 / 爆炸 / 火焰…），取不到时为空串
	Groups []Group
}

// Part 是敌人的一个部位。
type Part struct {
	Name     string
	Count    string // 数量（例如「2」）；上游没给时为空串
	Health   string
	Armor    string
	Location string
	Durable  string // 耐久倍率（例如「0%」）
	ToMain   string // 对主体伤害比例（例如「100%」）
	Image    string // 部位示意图的公网地址（200px 缩略图）；上游没给时为空串
	Fatal    bool   // 命中即致命
	Weak     bool   // 弱点部位
}

// Entry 是一条详情。字段都取「能显示就显示」的口径：上游没给的一律留空，由展示层决定渲染哪几段。
type Entry struct {
	Kind        Kind
	Name        string // 中文名；上游没给时等于英文名
	NameEn      string
	Category    string // 类别中文名（主武器 / 轨道 / 终结族…）
	SubCategory string // 细分中文名（突击步枪 / 突击步枪…）；没有时为空串
	Unlock      string
	Description string
	Lore        string
	Image       string // 公网图片地址
	Source      string // 上游来源页地址（社区维基的页面链接）；没有时为空串
	Code        []string
	CallIn      string // 呼叫时间（战备）
	Cooldown    string // 冷却时间（战备）
	Uses        string // 使用次数（战备）
	Traits      []string
	Tips        []string
	Variants    []string
	Stats       []Group // 详细属性（顶层数值）
	Attacks     []Block // 攻击条目
	// 以下是敌人专属：分类已在 Category 里，其余几项上游给什么就是什么（已翻译可翻译的部分）。
	Health         string
	Damage         string
	DamageType     string
	FireMultiplier string
	Stagger        string
	Difficulty     string
	Parts          []Part // 部位数据
}

// DB 是索引好的详情库。构造后只读，可并发使用。
type DB struct {
	entries    []*Entry
	weapons    map[string]*Entry
	stratagems map[string]*Entry
	enemies    map[string]*Entry
	terms      map[string]string
}

// Len 返回库里的条目总数。
func (db *DB) Len() int {
	if db == nil {
		return 0
	}
	return len(db.entries)
}

// Weapon 按关键字找一件武器；关键字可以是中文名、英文名或条目 id。
func (db *DB) Weapon(keyword string) (*Entry, bool) { return db.lookup(db.weapons, keyword) }

// Stratagem 按关键字找一件战备。
func (db *DB) Stratagem(keyword string) (*Entry, bool) { return db.lookup(db.stratagems, keyword) }

// Enemy 按关键字找一只敌人。
func (db *DB) Enemy(keyword string) (*Entry, bool) { return db.lookup(db.enemies, keyword) }

// Gear 先按武器找，再按战备找：装备类图鉴（/gun、/strat、/grenade）用得上。
func (db *DB) Gear(keyword string) (*Entry, bool) {
	if entry, ok := db.Weapon(keyword); ok {
		return entry, true
	}
	return db.Stratagem(keyword)
}

func (db *DB) lookup(index map[string]*Entry, keyword string) (*Entry, bool) {
	if db == nil {
		return nil, false
	}
	key := normalize(keyword)
	if key == "" {
		return nil, false
	}
	entry, ok := index[key]
	return entry, ok
}

// ---- 加载 ----

// Load 解析内置数据并建立索引。数据是随仓库发布的，解析失败属于发布问题，应当在上层暴露出来。
func Load() (*DB, error) {
	db := &DB{
		weapons:    map[string]*Entry{},
		stratagems: map[string]*Entry{},
		enemies:    map[string]*Entry{},
	}
	terms, err := loadTerms()
	if err != nil {
		return nil, err
	}
	db.terms = terms

	var weapons struct {
		Weapons []rawWeapon `json:"weapons"`
	}
	if err := db.readJSON("data/weapons.json", &weapons); err != nil {
		return nil, err
	}
	var stratagems struct {
		Stratagems []rawStratagem `json:"stratagems"`
	}
	if err := db.readJSON("data/stratagems.json", &stratagems); err != nil {
		return nil, err
	}
	var enemies struct {
		Enemies []rawEnemy `json:"enemies"`
	}
	if err := db.readJSON("data/enemies.json", &enemies); err != nil {
		return nil, err
	}

	for i := range weapons.Weapons {
		entry := db.buildWeapon(weapons.Weapons[i])
		db.add(entry, db.weapons)
	}
	for i := range stratagems.Stratagems {
		entry := db.buildStratagem(stratagems.Stratagems[i])
		db.add(entry, db.stratagems)
	}
	for i := range enemies.Enemies {
		entry := db.buildEnemy(enemies.Enemies[i])
		db.add(entry, db.enemies)
	}
	sort.SliceStable(db.entries, func(i, j int) bool {
		if db.entries[i].Kind != db.entries[j].Kind {
			return db.entries[i].Kind < db.entries[j].Kind
		}
		return db.entries[i].Name < db.entries[j].Name
	})
	return db, nil
}

var (
	defaultOnce sync.Once
	defaultDB   *DB
)

// Default 返回内置的详情库（只解析一次）。解析失败时返回空库：详情只是锦上添花，
// 不该因为一份内置数据坏了就让整条命令失败。
func Default() *DB {
	defaultOnce.Do(func() {
		db, err := Load()
		if err != nil {
			defaultDB = &DB{
				weapons:    map[string]*Entry{},
				stratagems: map[string]*Entry{},
				enemies:    map[string]*Entry{},
			}
			return
		}
		defaultDB = db
	})
	return defaultDB
}

func (db *DB) readJSON(name string, target any) error {
	raw, err := dataFiles.ReadFile(name)
	if err != nil {
		return fmt.Errorf("读内置详情数据 %s 失败：%w", name, err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("解析内置详情数据 %s 失败：%w", name, err)
	}
	return nil
}

func (db *DB) add(entry *Entry, index map[string]*Entry) {
	db.entries = append(db.entries, entry)
	for _, key := range []string{entry.Name, entry.NameEn} {
		if normalized := normalize(key); normalized != "" {
			if _, exists := index[normalized]; !exists {
				index[normalized] = entry
			}
		}
	}
}

func loadTerms() (map[string]string, error) {
	raw, err := dataFiles.ReadFile("data/terms.json")
	if err != nil {
		return nil, fmt.Errorf("读内置术语表失败：%w", err)
	}
	terms := map[string]string{}
	if err := json.Unmarshal(raw, &terms); err != nil {
		return nil, fmt.Errorf("解析内置术语表失败：%w", err)
	}
	return terms, nil
}

// ---- 原始结构 ----

// flexString 是「上游可能给字符串、数字、布尔或 null」的字段：一律收成字符串，null 与空串等价。
type flexString string

func (f *flexString) UnmarshalJSON(raw []byte) error {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		*f = ""
		return nil
	}
	if trimmed[0] == 0x22 {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return err
		}
		*f = flexString(text)
		return nil
	}
	*f = flexString(trimmed)
	return nil
}

// orderedObj 是保留键顺序的 JSON 对象：详情里字段顺序就是站点上的顺序，用 map 会打乱（Go 不保证顺序），
// 卡片上的数值顺序会随机变。
type orderedObj struct {
	Keys []string
	Vals map[string]json.RawMessage
}

func (o *orderedObj) UnmarshalJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delim, ok := token.(json.Delim); !ok || delim != 0x7b {
		return fmt.Errorf("期望 JSON 对象，实际是 %v", token)
	}
	o.Vals = map[string]json.RawMessage{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok {
			return fmt.Errorf("期望对象键，实际是 %v", keyToken)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		o.Keys = append(o.Keys, key)
		o.Vals[key] = value
	}
	return nil
}

// rawStats 是 detailed_stats：既有一层键值，也有 attacks / status_effects 两个特殊键，
// 因此整段按有序对象收下来，交给 buildStats 解释。
type rawStats struct {
	Obj orderedObj
}

func (s *rawStats) UnmarshalJSON(raw []byte) error {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	return json.Unmarshal(raw, &s.Obj)
}

type rawVariant struct {
	Name   flexString `json:"name"`
	Unlock flexString `json:"unlock"`
}

type rawWeapon struct {
	ID              string       `json:"id"`
	Name            flexString   `json:"name"`
	NameEn          flexString   `json:"name_en"`
	Category        flexString   `json:"category"`
	SubCategoryName flexString   `json:"subcategory_name"`
	Traits          []string     `json:"traits"`
	Unlock          flexString   `json:"unlock"`
	UnlockZh        flexString   `json:"unlock_zh"`
	Description     flexString   `json:"description"`
	DescriptionZh   flexString   `json:"description_zh"`
	Lore            flexString   `json:"lore"`
	Tips            []string     `json:"tips"`
	Variants        []rawVariant `json:"variants"`
	DetailedStats   rawStats     `json:"detailed_stats"`
	Icon            flexString   `json:"icon"`
	SourceURL       flexString   `json:"source_url"`
}

type rawStratagem struct {
	ID            string     `json:"id"`
	Name          flexString `json:"name"`
	NameEn        flexString `json:"name_en"`
	Category      flexString `json:"category"`
	CategoryZh    flexString `json:"category_label"`
	Code          flexString `json:"code"`
	CallIn        flexString `json:"call_in_time"`
	Cooldown      flexString `json:"cooldown"`
	Uses          flexString `json:"uses"`
	Unlock        flexString `json:"unlock"`
	UnlockZh      flexString `json:"unlock_zh"`
	Description   flexString `json:"description"`
	Icon          flexString `json:"icon"`
	Image         flexString `json:"image"`
	DetailedStats rawStats   `json:"detailed_stats"`
	SourcePage    flexString `json:"source_page"`
}

type rawPart struct {
	Name          flexString `json:"name"`
	NameEn        flexString `json:"name_en"`
	Health        flexString `json:"health"`
	ArmorLevelZh  flexString `json:"armor_level_zh"`
	ArmorLevel    flexString `json:"armor_level"`
	LocationZh    flexString `json:"location_zh"`
	Location      flexString `json:"location"`
	Durable       flexString `json:"durable"`
	PercentToMain flexString `json:"percent_to_main"`
	Count         flexString `json:"count"`
	Image         flexString `json:"image"`
	Fatal         flexString `json:"fatal"`
	WeakPoint     flexString `json:"is_weak_point"`
}

type rawEnemy struct {
	ID                string     `json:"id"`
	Name              flexString `json:"name"`
	NameZh            flexString `json:"name_zh"`
	FactionLabel      flexString `json:"faction_label"`
	Image             flexString `json:"image"`
	ImageThumb        flexString `json:"image_thumb"`
	Description       flexString `json:"description"`
	DescriptionZh     flexString `json:"description_zh"`
	Category          flexString `json:"category"`
	CategoryZh        flexString `json:"category_zh"`
	HealthTotal       flexString `json:"health_total"`
	Damage            flexString `json:"damage"`
	DamageZh          flexString `json:"damage_zh"`
	DamageType        flexString `json:"damage_type"`
	DamageTypeZh      flexString `json:"damage_type_zh"`
	FireMultiplier    flexString `json:"fire_damage_multiplier_zh"`
	FireMultiplierRaw flexString `json:"fire_damage_multiplier"`
	Stagger           flexString `json:"stagger_threshold_zh"`
	StaggerRaw        flexString `json:"stagger_threshold"`
	MinDifficulty     flexString `json:"minimum_difficulty_zh"`
	MinDifficultyRaw  flexString `json:"minimum_difficulty"`
	Variants          []string   `json:"variants"`
	BodyParts         []rawPart  `json:"body_parts"`
	SourcePage        flexString `json:"source_page"`
}
