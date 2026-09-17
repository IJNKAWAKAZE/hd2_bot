// card_test.go 覆盖图鉴卡片的视图模型构造：数值抽取、文案拼装、条数与截断。
// 用内联夹具目录（字段名与上游一致）而不是真上游文件：这里要钉住的是「数据怎么变成卡片上的字」，
// 不是「上游今天有没有改格式」（格式变化由 arsenal 包的解析用例负责报警）。
package codex

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"hd2_bot/src/arsenal"
	"hd2_bot/src/bestiary"
)

// fixtureJSON 是一份小目录：主武器（弹匣 / 射速 / 射击模式 / 穿甲 / 军需簿来源）、
// 战备（召唤指令 + 冷却 + 征用点来源）、护甲（等级 / 护甲值 / 速度 / 耐力回复 / 被动）、
// 手雷（默认解锁），以及两本军需簿。字段名与上游一致，改错 tag 时这些用例会红。
const fixtureJSON = `{
  "meta": {"game": "HELLDIVERS 2", "dataVersion": "2026.08.14.1", "capturedAt": "2026-08-14T03:25:51.159Z"},
  "warbonds": [
    {"id": "mobilize", "nameZh": "绝地潜兵总动员！", "nameEn": "Helldivers Mobilize!", "superCredits": null,
     "pages": [{"page": 1, "cumulativeMedals": 0}, {"page": 3, "cumulativeMedals": 35}],
     "wiki": {"url": "https://helldivers.wiki.gg/wiki/Helldivers_Mobilize"}},
    {"id": "dust-devils", "nameZh": "沙漠魔影", "nameEn": "Dust Devils", "superCredits": 1000,
     "pages": [{"page": 2, "cumulativeMedals": 60}],
     "wiki": {"url": "https://helldivers.wiki.gg/wiki/Dust_Devils"}}
  ],
  "items": [
    {"id": "ar-2-coyote", "productKind": "primary-weapon", "model": "AR-2", "nameZh": "野狼", "nameEn": "AR-2 Coyote",
     "image": {"path": "assets/wiki/ar-2-coyote.png", "filePage": "https://helldivers.wiki.gg/wiki/File:AR-2_Coyote_Primary_Render.png", "license": "License/Arrowhead"},
     "wiki": {"url": "https://helldivers.wiki.gg/wiki/AR-2_Coyote"},
     "acquisition": {"kind": "warbond", "warbondId": "dust-devils", "page": 1, "itemMedals": 35},
     "combat": {"primaryComponentId": "13826-component-1", "components": [
        {"label": "Ballistic", "fields": {"standardDamage": 75, "durableDamage": 22, "armorPenetration": {"value": 3, "labelZh": "中型"}}}]},
     "handling": {"magazine": 45, "spareMagazines": 8, "fireRate": 600, "firingModes": ["Auto&nbsp;", "Semi", "Semi", "Burst", "Full"]}},
    {"id": "ar-23-liberator", "productKind": "primary-weapon", "model": "AR-23", "nameZh": "解放者", "nameEn": "AR-23 Liberator",
     "image": {"path": "assets/wiki/ar-23-liberator.png"},
     "acquisition": {"kind": "requisition", "levelRequired": 5, "requisitionPoints": 3000},
     "combat": {"primaryComponentId": "23-component-1", "components": [
        {"label": "Ballistic", "fields": {"standardDamage": 60, "armorPenetration": {"value": 2, "labelZh": "轻型"}}}]},
     "handling": {"magazine": 45, "spareMagazines": 8, "fireRate": 640, "firingModes": ["Auto", "Semi"]}},
    {"id": "a-m-12-mortar-sentry", "productKind": "other-stratagem", "model": "A/M-12", "nameZh": "迫击哨戒炮", "nameEn": "A/M-12 Mortar Sentry",
     "image": {"path": "assets/wiki/a-m-12-mortar-sentry.svg", "filePage": "https://helldivers.wiki.gg/wiki/File:Mortar_Sentry_Stratagem_Icon.svg", "license": "License/CC-BY-NC-SA"},
     "wiki": {"url": "https://helldivers.wiki.gg/wiki/A/M-12_Mortar_Sentry"},
     "acquisition": {"kind": "requisition", "levelRequired": 13, "requisitionPoints": 6000},
     "combat": {"primaryComponentId": "2400-component-1", "components": [
        {"label": "Explosion", "fields": {"standardDamage": 300}}]},
     "deployment": {"type": "Sentry", "code": ["down", "up", "right", "up", "left", "up"], "cooldownSeconds": 180, "callInSeconds": 3}},
    {"id": "a-35-recon", "productKind": "body-armor", "model": "A-35", "nameZh": "A-35“侦察者”", "nameEn": "A-35 Recon",
     "image": {"path": "assets/wiki/a-35-recon.png", "filePage": "https://helldivers.wiki.gg/wiki/File:A-35_Recon_Body_Icon.png", "license": "License/CC-BY-NC-SA"},
     "wiki": {"url": "https://helldivers.wiki.gg/wiki/A-35_Recon"},
     "acquisition": {"kind": "warbond", "warbondId": "mobilize", "page": 2, "itemMedals": 55},
     "armor": {"class": "Medium", "rating": 100, "speed": 500, "staminaRegen": 100, "passive": "Feet First"}},
    {"id": "g-16-impact", "productKind": "grenade", "model": "G-16", "nameZh": "撞击手雷", "nameEn": "G-16 Impact",
     "image": {"path": "assets/wiki/g-16-impact.png", "filePage": "https://helldivers.wiki.gg/wiki/File:G-16_Impact_Grenade_Icon.png", "license": "License/CC-BY-NC-SA"},
     "wiki": {"url": "https://helldivers.wiki.gg/wiki/G-16_Impact"},
     "acquisition": {"kind": "default"},
     "combat": {"primaryComponentId": "77-component-1", "components": [
        {"label": "Explosion", "fields": {"standardDamage": 400}}]}}
  ]
}`

// mustCatalog 解析夹具目录；解析失败直接终止用例。
func mustCatalog(t *testing.T) *arsenal.Catalog {
	t.Helper()
	catalog, err := arsenal.ParseCatalog([]byte(fixtureJSON), nil)
	if err != nil {
		t.Fatalf("解析夹具目录失败：%v", err)
	}
	return catalog
}

// fixtureItem 从夹具目录里取一件装备。
func fixtureItem(t *testing.T, id string) arsenal.Item {
	t.Helper()
	item, ok := mustCatalog(t).Item(id)
	if !ok {
		t.Fatalf("夹具目录里没有 %s", id)
	}
	return item
}

// fieldLabels 返回字段标签序列，用来断言「卡片上先显示什么、后显示什么」。
func fieldLabels(fields []Field) []string {
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		out = append(out, field.Label)
	}
	return out
}

// fieldValue 取某个标签的值；没有这个标签返回空串。
func fieldValue(fields []Field, label string) string {
	for _, field := range fields {
		if field.Label == label {
			return field.Value
		}
	}
	return ""
}

// equalStrings 比较两个字符串切片是否完全相同（本包用例里用得最多的是「标签顺序」）。
func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// ---- 数值抽取 ----
// TestModesText 校验射击模式：实体转义与空白折叠、去重、认得出的词翻中文，超过三个用「等」收尾。
func TestModesText(t *testing.T) {
	cases := []struct {
		name  string
		modes []string
		want  string
	}{
		{"没有模式", nil, ""},
		{"全是空白", []string{"  ", ""}, ""},
		{"脏数据里的实体与空格", []string{"Auto&nbsp;"}, "自动"},
		{"去重且忽略大小写前后的空白", []string{"Semi", " semi "}, "半自动"},
		{"四个模式用等收尾", []string{"Burst", "Full", "Lever-Action", "40mm"}, "点射 / 全自动 / 杠杆 等"},
		{"认不出的原样显示", []string{"APHET"}, "APHET"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := modesText(c.modes); got != c.want {
				t.Fatalf("期望 %q，实际 %q", c.want, got)
			}
		})
	}
}

// TestAcquisitionText 校验获取方式：军需簿要把书名/页码/勋章写清，征用点要写价格与等级要求，
// 其余类别用目录里的中文名，未知类别原样显示。
func TestAcquisitionText(t *testing.T) {
	catalog := mustCatalog(t)
	cases := []struct {
		name    string
		item    arsenal.Item
		catalog *arsenal.Catalog
		want    string
	}{
		{"军需簿按 id 查中文名", arsenal.Item{Acq: arsenal.Acquisition{Kind: "warbond", WarbondID: "dust-devils", Page: 1, ItemMedals: 35}}, catalog, "军需簿「沙漠魔影」 · 第 1 页 · 35 勋章"},
		{"军需簿 id 未知时保留原值", arsenal.Item{Acq: arsenal.Acquisition{Kind: "warbond", WarbondID: "ghost"}}, catalog, "军需簿「ghost」"},
		{"征用点写价格与等级", arsenal.Item{Acq: arsenal.Acquisition{Kind: "requisition", RequisitionPoints: 6000, LevelRequired: 13}}, nil, "征用点 · 6,000 · 需等级 13"},
		{"征用点缺数值时不写多余分隔", arsenal.Item{Acq: arsenal.Acquisition{Kind: "requisition"}}, nil, "征用点"},
		{"默认解锁", arsenal.Item{Acq: arsenal.Acquisition{Kind: "default"}}, nil, "默认解锁"},
		{"超级商店", arsenal.Item{Acq: arsenal.Acquisition{Kind: "superstore"}}, nil, "超级商店"},
		{"未知类别原样显示", arsenal.Item{Acq: arsenal.Acquisition{Kind: "mystery"}}, nil, "mystery"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := acquisitionText(c.catalog, c.item); got != c.want {
				t.Fatalf("期望 %q，实际 %q", c.want, got)
			}
		})
	}
}

// TestArmorClassName 校验护甲等级的中文名；未收录的取值原样显示，空串仍是空串。
func TestArmorClassName(t *testing.T) {
	for raw, want := range map[string]string{"Light": "轻甲", "Medium": "中甲", "Heavy": "重甲", "Unknown": "Unknown", "": ""} {
		if got := armorClassName(raw); got != want {
			t.Errorf("等级 %q 应显示为 %q，实际 %q", raw, want, got)
		}
	}
}

// TestDeployTypeName 校验战备部署类型的中文名；空串返回空串（模板据此不显示这一行）。
func TestDeployTypeName(t *testing.T) {
	for raw, want := range map[string]string{
		"Support Weapon": "支援武器", "Backpack": "背包", "Orbital": "轨道", "Sentry": "哨戒炮",
		"Emplacement": "固定炮台", "Vehicle": "载具", "Eagle": "飞鹰", "Other": "其它", "": "",
	} {
		if got := deployTypeName(raw); got != want {
			t.Errorf("部署类型 %q 应显示为 %q，实际 %q", raw, want, got)
		}
	}
	if got := deployTypeName("Whatever"); got != "Whatever" {
		t.Errorf("未收录的类型应原样显示，实际 %q", got)
	}
}

// TestCreditsText 校验超级货币价：上游用 0 表示不单卖，卡片上写「—」而不是 0。
func TestCreditsText(t *testing.T) {
	for credits, want := range map[int]string{0: "—", -5: "—", 1000: "1,000", 250: "250"} {
		if got := creditsText(credits); got != want {
			t.Errorf("%d 应显示为 %q，实际 %q", credits, want, got)
		}
	}
}

// ---- 装备卡片 ----
// ---- 军需簿卡片 ----

// TestFindWarbond 校验查找顺序：精确（中文名/英文名/id）→ 前缀 → 包含；空关键字与未命中都返回 false。
func TestFindWarbond(t *testing.T) {
	catalog := mustCatalog(t)
	cases := []struct {
		keyword string
		wantID  string
		wantOK  bool
	}{
		{"沙漠魔影", "dust-devils", true},
		{"dust devils", "dust-devils", true},
		{"mobilize", "mobilize", true},
		{"沙漠", "dust-devils", true},
		{"魔影", "dust-devils", true},
		{"  ", "", false},
		{"不存在的本", "", false},
	}
	for _, c := range cases {
		t.Run(c.keyword, func(t *testing.T) {
			book, ok := FindWarbond(catalog, c.keyword)
			if ok != c.wantOK || book.ID != c.wantID {
				t.Fatalf("期望 (%q, %v)，实际 (%q, %v)", c.wantID, c.wantOK, book.ID, ok)
			}
		})
	}
}

// TestBuildWarbondsCardList 校验不带关键字时的名单：条目数、页数、勋章、价格与包含件数。
func TestBuildWarbondsCardList(t *testing.T) {
	catalog := mustCatalog(t)
	card := BuildWarbondsCard(catalog.Warbonds(), nil, "", time.Time{})
	if card.Detail != nil {
		t.Fatal("不带关键字时不该出明细")
	}
	if !strings.Contains(card.Intro, "全部军需簿") {
		t.Errorf("名单开头应说明这是全部军需簿：%q", card.Intro)
	}
	if len(card.Books) != 2 {
		t.Fatalf("夹具里有两本军需簿，实际 %d 本", len(card.Books))
	}
	first := card.Books[0]
	if first.Name != "绝地潜兵总动员！" || first.English != "Helldivers Mobilize!" {
		t.Errorf("第一本名称错误：%+v", first)
	}
	if first.Pages != "2 页" || first.Medals != "35 勋章" || first.Items != "1 件" {
		t.Errorf("第一本的页数/勋章/件数错误：%+v", first)
	}
	if first.Credits != "—" {
		t.Errorf("不单卖的军需簿价格应写「—」，实际 %q", first.Credits)
	}
	if card.Books[1].Credits != "1,000" || card.Books[1].Items != "1 件" {
		t.Errorf("第二本的价格/件数错误：%+v", card.Books[1])
	}
}

// TestBuildWarbondsCardDetailAndMiss 校验命中时出明细、没命中时说明没找到并照常给名单。
func TestBuildWarbondsCardDetailAndMiss(t *testing.T) {
	catalog := mustCatalog(t)
	book, ok := catalog.Warbond("dust-devils")
	if !ok {
		t.Fatal("夹具里应有 dust-devils")
	}
	detail := BuildWarbondDetail(book, catalog.ItemsOfWarbond(book.ID), catalog)
	hit := BuildWarbondsCard(catalog.Warbonds(), detail, "沙漠魔影", time.Time{})
	if hit.Detail == nil || !strings.Contains(hit.Intro, "沙漠魔影") {
		t.Fatalf("命中时应出明细并写明关键字：%+v", hit.Intro)
	}
	if len(hit.Books) != 0 {
		t.Errorf("出明细时不再列名单，实际 %d 本", len(hit.Books))
	}

	miss := BuildWarbondsCard(catalog.Warbonds(), nil, "不存在的本", time.Time{})
	if miss.Detail != nil {
		t.Error("没命中时不该出明细")
	}
	if !strings.Contains(miss.Intro, "没有找到匹配") {
		t.Errorf("没命中时开头应说明没找到：%q", miss.Intro)
	}
	if len(miss.Books) != 2 {
		t.Errorf("没命中时应照常给全部名单，实际 %d 本", len(miss.Books))
	}
}

// TestBuildWarbondDetail 校验明细字段：名称、页数、勋章、价格、每件装备的类别与来源。
func TestBuildWarbondDetail(t *testing.T) {
	catalog := mustCatalog(t)
	book, _ := catalog.Warbond("dust-devils")
	detail := BuildWarbondDetail(book, catalog.ItemsOfWarbond(book.ID), catalog)
	if detail.Name != "沙漠魔影" || detail.English != "Dust Devils" {
		t.Errorf("明细名称错误：%+v", detail)
	}
	if detail.Pages != "1 页" || detail.Medals != "60 勋章" || detail.Credits != "1,000" {
		t.Errorf("明细页数/勋章/价格错误：%+v", detail)
	}
	if len(detail.Items) != 1 {
		t.Fatalf("这本应含 1 件装备，实际 %d 件", len(detail.Items))
	}
	item := detail.Items[0]
	if item.Name != "野狼" || item.Kind != "主武器" {
		t.Errorf("明细条目错误：%+v", item)
	}
	if !strings.Contains(item.Acquire, "沙漠魔影") {
		t.Errorf("明细条目应写明来源军需簿：%q", item.Acquire)
	}
}

// TestBuildWarbondDetailTruncates 校验件数超上限时截断并给出说明（40 件以上不会把卡片撑爆）。
func TestBuildWarbondDetailTruncates(t *testing.T) {
	items := make([]arsenal.Item, 0, maxWarbondItems+2)
	for i := 0; i < maxWarbondItems+2; i++ {
		items = append(items, arsenal.Item{ID: fmt.Sprintf("i-%d", i), Kind: arsenal.KindGrenade, NameEn: fmt.Sprintf("Grenade %d", i)})
	}
	detail := BuildWarbondDetail(arsenal.Warbond{ID: "test", NameZh: "测试本", NameEn: "Test Warbond"}, items, nil)
	if len(detail.Items) != maxWarbondItems {
		t.Fatalf("应截断到 %d 件，实际 %d 件", maxWarbondItems, len(detail.Items))
	}
	if detail.More != "另有 2 件未列出。" {
		t.Errorf("截断说明错误：%q", detail.More)
	}
}

// TestBuildWarbondDetailDropsRepeatedEnglishName 校验中文名与英文名相同时不重复显示。
func TestBuildWarbondDetailDropsRepeatedEnglishName(t *testing.T) {
	detail := BuildWarbondDetail(arsenal.Warbond{ID: "test", NameEn: "Test Warbond"}, nil, nil)
	if detail.Name != "Test Warbond" || detail.English != "" {
		t.Fatalf("中英文名相同时只显示一次，实际 %+v", detail)
	}
}

// TestBuildWarbondsCardEmpty 校验目录里没有军需簿时给出的提示。
func TestBuildWarbondsCardEmpty(t *testing.T) {
	card := BuildWarbondsCard(nil, nil, "", time.Time{})
	if card.Empty == "" {
		t.Fatal("没有军需簿时应给出提示")
	}
	if len(card.Books) != 0 {
		t.Errorf("没有军需簿时名单应为空，实际 %d 本", len(card.Books))
	}
}

// ---- 敌人卡片 ----
// TestHealthText 校验血量占位：上游没给或解析不出时写「—」，不写 0（0 会被读成「没血」）。
func TestHealthText(t *testing.T) {
	for health, want := range map[int]string{0: "—", -1: "—", 60: "60", 6500: "6,500"} {
		if got := healthText(health); got != want {
			t.Errorf("%d 应显示为 %q，实际 %q", health, want, got)
		}
	}
}

// TestFactionText 校验阵营文案：收录的阵营给中文名与配色，变体附在括号里，未收录的原样显示。
func TestFactionText(t *testing.T) {
	name, class := factionText(bestiary.Enemy{Faction: bestiary.FactionAutomaton, FactionRaw: "Automatons"})
	if name != "机器人" || class != "faction--automaton" {
		t.Errorf("收录阵营的中文名与配色错误：%q / %q", name, class)
	}
	name, class = factionText(bestiary.Enemy{Faction: bestiary.FactionAutomaton, FactionRaw: "Jet Brigade", Variant: "Jet Brigade"})
	if name != "机器人（Jet Brigade）" {
		t.Errorf("变体应附在中文名后的括号里，实际 %q", name)
	}
	if class != "faction--automaton" {
		t.Errorf("变体沿用主阵营配色，实际 %q", class)
	}
	name, class = factionText(bestiary.Enemy{Faction: "", FactionRaw: "Some New Faction"})
	if name != "Some New Faction" || class != "" {
		t.Errorf("未收录阵营应原样显示且没有配色：%q / %q", name, class)
	}
	name, _ = factionText(bestiary.Enemy{})
	if name != "未知" {
		t.Errorf("连原文都没有时应写「未知」，实际 %q", name)
	}
}

// ---- 单件详情卡 ----

// TestBuildEquipmentDetailWeapon 校验武器详情卡：标题、标签、获取方式、内置详情的详细属性与攻击条目。
// 野狼在参照站数据里有详情，因此卡片走的是「维基那段替掉目录里那段」的路子。
func TestBuildEquipmentDetailWeapon(t *testing.T) {
	catalog := mustCatalog(t)
	detail := BuildEquipmentDetail(fixtureItem(t, "ar-2-coyote"), catalog, "data:image/png;base64,AAA")
	if detail.Name != "野狼" || detail.Model != "AR-2" || detail.English != "AR-2 Coyote" {
		t.Errorf("详情标题错误：%+v", detail)
	}
	if !equalStrings(detail.Tags, []string{"主武器"}) {
		t.Errorf("详情标签应是类别：%v", detail.Tags)
	}
	if detail.Image != "data:image/png;base64,AAA" {
		t.Errorf("详情头部应带上装备图：%q", detail.Image)
	}
	if detail.Acquire != "军需簿「沙漠魔影」 · 第 1 页 · 35 勋章" {
		t.Errorf("获取方式错误：%q", detail.Acquire)
	}
	if len(detail.Arrows) != 0 {
		t.Errorf("这把枪没有召唤指令：%v", detail.Arrows)
	}
	if wiki := detail.Wiki; wiki == nil {
		t.Fatal("参照站数据里有这把枪，应挂上详细资料")
	}
	if got := sectionTitles(detail.Sections); len(got) != 0 {
		t.Errorf("有详细属性时不该再画目录里的操作性：%v", got)
	}
	if len(detail.Attacks) != 0 {
		t.Errorf("有详细攻击条目时不该再画目录里的攻击段：%v", detail.Attacks)
	}
	if got := fieldValue(detail.Wiki.Cells, "射速"); got == "" {
		t.Errorf("详细属性里应有射速：%+v", detail.Wiki.Cells)
	}
	if len(detail.Wiki.Attacks) == 0 {
		t.Fatal("应带上内置详情的攻击条目")
	}
	if got := fieldValue(detail.Wiki.Attacks[0].Cells, "耐久伤害"); got == "" {
		t.Errorf("攻击条目里应有耐久伤害：%+v", detail.Wiki.Attacks[0].Cells)
	}
	// 攻击标题用类型名，不该带上游的弹药内部代号。
	for _, attack := range detail.Wiki.Attacks {
		if strings.Contains(attack.Title, "P1") || strings.Contains(attack.Title, "CANNON") {
			t.Errorf("攻击标题应中文化，实际 %q", attack.Title)
		}
	}
}

// TestBuildEquipmentDetailWithoutAtlas 校验内置详情里没有这件装备时，卡片照常画我们目录里的那几段
// （操作性、攻击部件）——这条路径由护甲与非官方装备走到。
func TestBuildEquipmentDetailWithoutAtlas(t *testing.T) {
	item := arsenal.Item{
		ID: "test-gun", Kind: arsenal.KindPrimary, Model: "TX-1",
		NameZh: "测试枪", NameEn: "Test Gun",
		Handling: arsenal.Handling{Magazine: 30, SpareMagazines: 5, FireRate: 600, FiringModes: []string{"Auto", "Semi"}},
		Components: []arsenal.Component{{
			Label: "Ballistic", StandardDamage: 100, DurableDamage: 20,
			ArmorPenLabel: "中型", ArmorPenValue: 3, DemolitionForce: 10,
		}},
		Primary: 0,
	}
	detail := BuildEquipmentDetail(item, nil, "")
	if detail.Wiki != nil {
		t.Fatalf("内置详情里没有这件，不该挂详细资料：%+v", detail.Wiki)
	}
	if got := sectionTitles(detail.Sections); !equalStrings(got, []string{"操作性"}) {
		t.Fatalf("数值小节错误：%v", got)
	}
	cells := detail.Sections[0].Cells
	if got := fieldValue(cells, "弹匣"); got != "30 发" {
		t.Errorf("弹匣应分成一格：%q", got)
	}
	if got := fieldValue(cells, "备用弹匣"); got != "5 个" {
		t.Errorf("备用弹匣错误：%q", got)
	}
	if got := fieldValue(cells, "射击模式"); got != "自动 / 半自动" {
		t.Errorf("射击模式错误：%q", got)
	}
	if len(detail.Attacks) != 1 || detail.Attacks[0].Title != "攻击 #1 · 弹道" {
		t.Fatalf("攻击部件错误：%+v", detail.Attacks)
	}
	if got := fieldValue(detail.Attacks[0].Cells, "耐久伤害"); got != "20" {
		t.Errorf("耐久伤害错误：%q", got)
	}
}

// arrowAssets 把召唤指令的箭头转成内置图标的逻辑名，便于逐项比对。
func arrowAssets(steps []ArrowStep) []string {
	out := make([]string, 0, len(steps))
	for _, step := range steps {
		out = append(out, step.Asset)
	}
	return out
}

// TestBuildEquipmentDetailStratagem 校验战备详情卡：召唤指令箭头、内置详情的详细属性与攻击条目。
func TestBuildEquipmentDetailStratagem(t *testing.T) {
	catalog := mustCatalog(t)
	detail := BuildEquipmentDetail(fixtureItem(t, "a-m-12-mortar-sentry"), catalog, "")
	// 召唤指令的箭头现在带两副面孔：卡片用游戏图标（Asset），文本回退用符号（Glyph）。
	if got := arrowAssets(detail.Arrows); !equalStrings(got, []string{
		"arrow.down", "arrow.up", "arrow.right", "arrow.up", "arrow.left", "arrow.up",
	}) {
		t.Errorf("召唤指令图标的逻辑名错误：%v", got)
	}
	if got := arrowGlyphs(detail.Arrows); !equalStrings(got, []string{"↓", "↑", "→", "↑", "←", "↑"}) {
		t.Errorf("召唤指令的文本符号错误：%v", got)
	}
	if wiki := detail.Wiki; wiki == nil {
		t.Fatal("参照站数据里有这门哨戒炮，应挂上详细资料")
	} else if got := fieldValue(detail.Wiki.Cells, "冷却时间"); got == "" {
		t.Errorf("详细属性里应有冷却时间：%+v", detail.Wiki.Cells)
	}
	if got := sectionTitles(detail.Sections); len(got) != 0 {
		t.Errorf("有详细属性时不该再画目录里的部署段：%v", got)
	}
	if len(detail.Attacks) != 0 {
		t.Errorf("有内置攻击条目时不该再画目录里的攻击段：%v", detail.Attacks)
	}
	if detail.Image != "" {
		t.Error("没传图时详情卡不该自己造一张")
	}
}

// TestBuildEquipmentDetailStratagemWithoutAtlas 校验内置详情里没有这件战备时，卡片照常画部署段与攻击部件。
func TestBuildEquipmentDetailStratagemWithoutAtlas(t *testing.T) {
	item := arsenal.Item{
		ID: "test-strat", Kind: arsenal.KindStratagem, NameZh: "测试战备", NameEn: "Test Stratagem",
		Deploy:  &arsenal.Deploy{Type: "Sentry", Code: []string{"down", "up"}, CooldownSeconds: 180, CallInSeconds: 3},
		Primary: -1,
	}
	item.Components = []arsenal.Component{{Label: "Explosion", StandardDamage: 300, DemolitionForce: 30}}
	detail := BuildEquipmentDetail(item, nil, "")
	if detail.Wiki != nil {
		t.Fatalf("内置详情里没有这件，不该挂详细资料：%+v", detail.Wiki)
	}
	if got := sectionTitles(detail.Sections); !equalStrings(got, []string{"部署"}) {
		t.Fatalf("数值小节错误：%v", got)
	}
	cells := detail.Sections[0].Cells
	if got := fieldValue(cells, "部署类型"); got != "哨戒炮" {
		t.Errorf("部署类型错误：%q", got)
	}
	if got := fieldValue(cells, "呼叫时间"); got != "3 秒" {
		t.Errorf("呼叫时间错误：%q", got)
	}
	if len(detail.Attacks) != 1 || detail.Attacks[0].Title != "攻击 #1 · 爆炸" {
		t.Errorf("爆炸部件错误：%+v", detail.Attacks)
	}
}

// TestBuildEquipmentDetailArmor 校验护甲详情卡：只有护甲小节，且一个攻击部件都不出（目录里护甲没有伤害部件）。
func TestBuildEquipmentDetailArmor(t *testing.T) {
	catalog := mustCatalog(t)
	detail := BuildEquipmentDetail(fixtureItem(t, "a-35-recon"), catalog, "")
	if got := sectionTitles(detail.Sections); !equalStrings(got, []string{"护甲"}) {
		t.Errorf("数值小节错误：%v", got)
	}
	if len(detail.Attacks) != 0 {
		t.Errorf("护甲不该出攻击部件：%+v", detail.Attacks)
	}
	if got := fieldValue(detail.Sections[0].Cells, "等级"); got != "中甲" {
		t.Errorf("护甲等级错误：%q", got)
	}
}

// TestWeaponTypeName 校验武器类型译名：收录的翻中文，未收录的原样显示，空串不出标签。
func TestWeaponTypeName(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"Assault Rifles", "突击步枪"},
		{"Marksman Rifles", "精准步枪"},
		{"  ", ""},
		{"Future Gun", "Future Gun"},
	}
	for _, c := range cases {
		if got := weaponTypeName(c.raw); got != c.want {
			t.Errorf("weaponTypeName(%q) = %q，期望 %q", c.raw, got, c.want)
		}
	}
}

// TestComponentName 校验部件标签译名：大小写不敏感（上游混着 explosion / Explosion），未收录的原样显示。
func TestComponentName(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"Ballistic", "弹道"},
		{"explosion", "爆炸"},
		{"Impact Explosion", "撞击爆炸"},
		{"", ""},
		{"Mystery", "Mystery"},
	}
	for _, c := range cases {
		if got := componentName(c.raw); got != c.want {
			t.Errorf("componentName(%q) = %q，期望 %q", c.raw, got, c.want)
		}
	}
}

// sectionTitles 取出数值小节的标题序列。
func sectionTitles(sections []DetailSection) []string {
	out := make([]string, 0, len(sections))
	for _, section := range sections {
		out = append(out, section.Title)
	}
	return out
}

// ---- 装备卡片：按名字精确查一件 ----

// TestBuildEquipmentCardExactHit 校验精确命中一件时：卡片上就这一件，
// 开头不再写「匹配到几件」这类统计（用户 2026-09-17：精确匹配下那是噪音）。
func TestBuildEquipmentCardExactHit(t *testing.T) {
	catalog := mustCatalog(t)
	images := map[string]string{"ar-2-coyote": "data:image/png;base64,AAA"}
	at := time.Date(2026, 8, 14, 11, 25, 0, 0, time.UTC)

	result := catalog.Search(arsenal.Query{Kinds: gunSpec.Kinds, Keyword: "野狼", Limit: searchLimit})
	card := BuildEquipmentCard(gunSpec, result, catalog, images, "野狼", at)

	if card.Meta.Title != "武器图鉴" || card.Meta.Subtitle != "主武器 · 副武器 · 支援武器" {
		t.Errorf("标题与副标题错误：%+v", card.Meta)
	}
	if card.Meta.DataTime != "2026-08-14 11:25" {
		t.Errorf("数据时间格式错误：%q", card.Meta.DataTime)
	}
	if card.Empty != "" {
		t.Errorf("有命中时不该标成空结果：%q", card.Empty)
	}
	if card.Intro != "" {
		t.Errorf("精确命中时开头不该有多余的话：%q", card.Intro)
	}
	if card.Detail == nil {
		t.Fatal("命中时应出单件详情")
	}
	if card.Detail.Name != "野狼" || card.Detail.Model != "AR-2" {
		t.Errorf("详情应写命中的那一件：%+v", card.Detail)
	}
	if card.Detail.Image != "data:image/png;base64,AAA" {
		t.Errorf("装备图应按 id 回填，实际 %q", card.Detail.Image)
	}
	// 数据来源与许可声明不上卡片（用户 2026-09-17 要求），完整口径只在仓库 LICENSES.md 里。
	for _, note := range card.Notes {
		for _, unwanted := range []string{"数据来源", "LICENSES.md", "HD2Tool"} {
			if strings.Contains(note, unwanted) {
				t.Errorf("卡片上不该再出现 %q：%v", unwanted, card.Notes)
			}
		}
	}
}

// TestBuildEquipmentCardPrefersFirstHit 校验关键字不是完整名字时：卡片仍只画排序第一的那一件，
// 开头只说「这不是完整名字」，不列其余命中的数量——列表形态只存在于行内查询的候选里。
func TestBuildEquipmentCardPrefersFirstHit(t *testing.T) {
	catalog := mustCatalog(t)
	// 两把主武器的型号都以 AR- 开头：一个关键字命中两件，第一条是目录里在前的那件。
	result := catalog.Search(arsenal.Query{Kinds: gunSpec.Kinds, Keyword: "AR-", Limit: searchLimit})
	card := BuildEquipmentCard(gunSpec, result, catalog, nil, "AR-", time.Time{})

	if card.Detail == nil || card.Detail.Name != "野狼" {
		t.Fatalf("应画排序第一的那件：%+v", card.Detail)
	}
	if !strings.Contains(card.Intro, "不是完整名字") || !strings.Contains(card.Intro, "野狼") {
		t.Errorf("开头应说明这不是完整名字并给出画的是哪一件：%q", card.Intro)
	}
	if strings.Contains(card.Intro, "匹配到") {
		t.Errorf("开头不该再写匹配到几件：%q", card.Intro)
	}
	if card.Meta.DataTime != "" {
		t.Errorf("数据时间为零值时不该显示这一行，实际 %q", card.Meta.DataTime)
	}
}

// TestBuildEquipmentCardEmpty 校验一件都没命中时只出一句提示：没有详情，也没有其它命中提示。
func TestBuildEquipmentCardEmpty(t *testing.T) {
	card := BuildEquipmentCard(grenadeSpec, arsenal.Result{}, nil, nil, "不存在", time.Time{})
	if card.Detail != nil {
		t.Errorf("没命中时不该有详情：%+v", card.Detail)
	}
	if !strings.Contains(card.Intro, "没有命中") {
		t.Errorf("开头应说明关键字没命中：%q", card.Intro)
	}
	// 提示里给出可用的查法（中文名 / 英文名 / 型号 / 玩家外号）。
	if !strings.Contains(card.Empty, "玩家外号") || !strings.Contains(card.Empty, "手雷") {
		t.Errorf("空结果提示应写清怎么查：%q", card.Empty)
	}
}

// TestKindWord 校验「没找到」提示里的类别词：单类别用类别中文名，多类别（/gun）用「装备」。
func TestKindWord(t *testing.T) {
	if got := kindWord(grenadeSpec); got != "手雷" {
		t.Errorf("单类别应写类别名，实际 %q", got)
	}
	if got := kindWord(gunSpec); got != "装备" {
		t.Errorf("多类别应写「装备」，实际 %q", got)
	}
}

// ---- 敌人卡片：按名字精确查一只 ----

// fixtureEnemies 是敌人用例共用的图鉴：一只终结族、一只机器人、还有一只名字里带「泰坦」的变体。
func fixtureEnemies() []bestiary.Enemy {
	return []bestiary.Enemy{
		{Title: "Bile Titan", NameZh: "胆汁泰坦", Faction: bestiary.FactionTerminid, FactionRaw: "Terminids", Size: 3, Health: 6500,
			Damage: "【酸液】950 ｜ ｜ 【近战】1000"},
		{Title: "Titan Variant", NameZh: "变异泰坦", Faction: bestiary.FactionAutomaton, FactionRaw: "Automatons", Size: 2, Health: 1200},
		{Title: "Scavenger", NameZh: "食腐虫", Faction: bestiary.FactionTerminid, FactionRaw: "Terminids", Size: 0, Health: 60},
	}
}

// TestBuildEnemyCardExactHit 校验精确命中一只时：卡片上就这一只，
// 基础信息（阵营 / 体型 / 分类 / 伤害…）、外观图与部位示意图都对得上。
func TestBuildEnemyCardExactHit(t *testing.T) {
	list := fixtureEnemies()
	result := bestiary.Search(list, bestiary.Query{Keyword: "Bile Titan", Limit: searchLimit})
	at := time.Date(2026, 8, 14, 11, 25, 0, 0, time.UTC)
	images := EnemyImages{
		Icon:  "data:image/png;base64,AAA",
		Parts: map[string]string{"主体": "data:image/png;base64,BBB"},
	}
	card := BuildEnemyCard(result, "Bile Titan", images, RelatedEnemies(list, result.Enemies[0], 3), at)

	if card.Meta.Title != "敌人图鉴" || card.Meta.DataTime != "2026-08-14 11:25" {
		t.Errorf("标题或数据时间错误：%+v", card.Meta)
	}
	if card.Empty != "" {
		t.Errorf("精确命中时不该标成空结果：%q", card.Empty)
	}
	if card.Intro != "" {
		t.Errorf("精确命中时开头不该有多余的话：%q", card.Intro)
	}
	if card.Enemy == nil {
		t.Fatal("命中时应出这一只")
	}
	if card.Enemy.Name != "胆汁泰坦" || card.Enemy.English != "Bile Titan" {
		t.Errorf("名称与英文名错误：%+v", card.Enemy)
	}
	if got := card.Enemy.Rows[0].Label; got != "阵营" {
		t.Fatalf("基础信息第一行应是阵营：%+v", card.Enemy.Rows)
	}
	if got := fieldValue(card.Enemy.Rows, "阵营"); got != "终结族" {
		t.Errorf("阵营应翻成中文，实际 %q", got)
	}
	if got := card.Enemy.Rows[0].Class; got != "faction--terminid" {
		t.Errorf("阵营那行应带上配色 class，实际 %q", got)
	}
	if got := fieldValue(card.Enemy.Rows, "体型"); got != "巨型" {
		t.Errorf("体型应翻成中文，实际 %q", got)
	}
	if got := fieldValue(card.Enemy.Rows, "总生命值"); got != "6,500" {
		t.Errorf("总生命值应带千分位，实际 %q", got)
	}
	// 侧栏的标签与高亮框：阵营、体型两个标签 + 总生命值。
	if !equalStrings(card.Enemy.Tags, []string{"终结族", "巨型"}) {
		t.Errorf("标签应是阵营与体型：%v", card.Enemy.Tags)
	}
	if card.Enemy.Health != "6,500" {
		t.Errorf("侧栏高亮的总生命值错误：%q", card.Enemy.Health)
	}
	if card.Enemy.Image != "data:image/png;base64,AAA" {
		t.Errorf("敌人图应回填到卡片上，实际 %q", card.Enemy.Image)
	}
	// 内置社区维基里有这只：描述、基础信息、部位数据与来源都要出。
	if card.Enemy.Desc == "" {
		t.Error("内置详情里有这只敌人的描述，应该显示出来")
	}
	if got := fieldValue(card.Enemy.Rows, "分类"); got == "" {
		t.Errorf("基础信息里应有分类：%+v", card.Enemy.Rows)
	}
	if got := fieldValue(card.Enemy.Rows, "伤害"); got == "" || strings.Contains(got, ";") {
		t.Errorf("伤害应压成一行（分号换成中文分号）：%q", got)
	}
	if !strings.HasPrefix(card.Enemy.Source, "https://") {
		t.Errorf("侧栏的来源应是上游页面地址，实际 %q", card.Enemy.Source)
	}
	if len(card.Enemy.Parts) == 0 {
		t.Fatal("内置详情里有部位数据，应该显示出来")
	}
	sawPartImage := false
	for _, part := range card.Enemy.Parts {
		if strings.TrimSpace(part.Name) == "" {
			t.Errorf("部位名不该为空：%+v", part)
		}
		if part.Name == "主体" && part.Image == "data:image/png;base64,BBB" {
			sawPartImage = true
		}
	}
	if !sawPartImage {
		t.Error("部位示意图应按部位名回填到那一行")
	}
	// 侧栏的同阵营邻居：夹具里三只，另外两只是别的阵营或自身，所以只应列出同阵营的那只。
	if len(card.Enemy.Related) != 1 || card.Enemy.Related[0].Name != "食腐虫" {
		t.Errorf("同阵营邻居应只有食腐虫：%+v", card.Enemy.Related)
	}
}

// TestBuildEnemyCardMultipleHits 校验名字没写全时仍只画第一只，且不报「匹配到几只」。
func TestBuildEnemyCardMultipleHits(t *testing.T) {
	list := fixtureEnemies()
	result := bestiary.Search(list, bestiary.Query{Keyword: "泰坦", Limit: searchLimit})
	card := BuildEnemyCard(result, "泰坦", EnemyImages{}, nil, time.Time{})

	if card.Enemy == nil || card.Enemy.Name != "胆汁泰坦" {
		t.Fatalf("应画排序第一的那只：%+v", card.Enemy)
	}
	if !strings.Contains(card.Intro, "不是完整名字") {
		t.Errorf("开头应说明这不是完整名字：%q", card.Intro)
	}
	if strings.Contains(card.Intro, "匹配到") {
		t.Errorf("开头不该再写匹配到几只：%q", card.Intro)
	}
}

// TestBuildEnemyCardEmpty 校验一只都没命中时的提示：没有条目，但给得出怎么查。
func TestBuildEnemyCardEmpty(t *testing.T) {
	result := bestiary.Search(fixtureEnemies(), bestiary.Query{Keyword: "不存在", Limit: searchLimit})
	card := BuildEnemyCard(result, "不存在", EnemyImages{}, nil, time.Time{})

	if card.Enemy != nil {
		t.Errorf("没命中时不该有条目：%+v", card.Enemy)
	}
	if !strings.Contains(card.Intro, "没有命中") {
		t.Errorf("开头应说明关键字没命中：%q", card.Intro)
	}
	if !strings.Contains(card.Empty, "追猎虫") {
		t.Errorf("空结果提示应给出可用的例子：%q", card.Empty)
	}
}

// TestBuildEnemyCardUnlistedFaction 校验未收录的阵营原样显示且不带配色 class（不硬塞进某一族）。
func TestBuildEnemyCardUnlistedFaction(t *testing.T) {
	list := []bestiary.Enemy{{Title: "Ground All-Terrain Extraction Rig (GATER)", FactionRaw: "Super Earth"}}
	card := BuildEnemyCard(bestiary.Search(list, bestiary.Query{Keyword: "GATER", Limit: searchLimit}), "GATER", EnemyImages{}, nil, time.Time{})
	if card.Enemy == nil {
		t.Fatal("应命中这一只")
	}
	if got := fieldValue(card.Enemy.Rows, "阵营"); got != "Super Earth" {
		t.Errorf("未收录的阵营应原样显示，实际 %q", got)
	}
	if got := card.Enemy.Rows[0].Class; got != "" {
		t.Errorf("未收录的阵营不该带配色 class，实际 %q", got)
	}
	// 中文名缺失时退回英文名，且不再重复显示一遍。
	if card.Enemy.Name != "Ground All-Terrain Extraction Rig (GATER)" || card.Enemy.English != "" {
		t.Errorf("中文名缺失时应只显示一次英文名：%+v", card.Enemy)
	}
}

// TestDamageText 校验伤害压成一行：上游用「;」分段（段内还可能带换行），
// 卡片上是表格里的一格，换行会被吃成空格，所以统一用中文分号连起来。
func TestDamageText(t *testing.T) {
	cases := []struct {
		name   string
		damage string
		want   string
	}{
		{"空串", "", ""},
		{"只有分隔符", ";;", ""},
		{"正常两条", "60 Bile Spew + 3/s Acid Burn (4s); 1000 Stomp", "60 Bile Spew + 3/s Acid Burn (4s)；1000 Stomp"},
		{"段内多余空白折叠", "  酸液   950 \n 近战 1000 ", "酸液 950；近战 1000"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := damageText(c.damage); got != c.want {
				t.Fatalf("期望 %q，实际 %q", c.want, got)
			}
		})
	}
}

// TestBuildEnemyCardWithoutAtlasDetail 校验内置详情里没有这只时，卡片照常出基本三格，
// 描述/部位那几段为空（不硬凑、也不报错）。
func TestBuildEnemyCardWithoutAtlasDetail(t *testing.T) {
	list := []bestiary.Enemy{{Title: "Unknown Straggler", NameZh: "未知游荡者", Faction: bestiary.FactionTerminid, Size: 1, Health: 300, Damage: "10 Bite"}}
	card := BuildEnemyCard(bestiary.Search(list, bestiary.Query{Keyword: "未知游荡者", Limit: searchLimit}), "未知游荡者", EnemyImages{}, nil, time.Time{})
	if card.Enemy == nil {
		t.Fatal("应命中这一只")
	}
	if card.Enemy.Desc != "" || len(card.Enemy.Parts) != 0 || card.Enemy.Source != "" {
		t.Errorf("内置详情里没有这只时，那几段应为空：%+v", card.Enemy)
	}
	// 基础信息只剩我们自己的两行（阵营、体型），血量退回本地图鉴那个整数。
	if got := fieldLabels(card.Enemy.Rows); !equalStrings(got, []string{"阵营", "体型"}) {
		t.Errorf("应只剩阵营与体型两行：%v", got)
	}
	if card.Enemy.Health != "300" {
		t.Errorf("退回本地图鉴的血量，实际 %q", card.Enemy.Health)
	}
}

// TestBuildEquipmentCardWithAtlasDetail 校验命中内置详情的装备：详细属性、攻击条目与特性都挂到卡片上，
// 并且不再重复画目录里的操作性/攻击段（同一份数字只出现一遍）。
func TestBuildEquipmentCardWithAtlasDetail(t *testing.T) {
	catalog := mustCatalog(t)
	result := catalog.Search(arsenal.Query{Kinds: gunSpec.Kinds, Keyword: "野狼", Limit: searchLimit})
	card := BuildEquipmentCard(gunSpec, result, catalog, nil, "野狼", time.Time{})
	if card.Detail == nil {
		t.Fatal("应命中山那一件")
	}
	wiki := card.Detail.Wiki
	if wiki == nil {
		t.Fatal("野狼在参照站数据里有详情，卡片应挂上 Wiki 段")
	}
	if len(wiki.Cells) == 0 {
		t.Error("应有详细属性")
	}
	if len(wiki.Attacks) == 0 {
		t.Error("应有攻击条目")
	}
	if len(card.Detail.Sections) != 0 {
		t.Errorf("有详细属性时不该再画目录里的操作性/部署段：%+v", card.Detail.Sections)
	}
	if len(card.Detail.Attacks) != 0 {
		t.Errorf("有详细属性时不该再画目录里的攻击段：%+v", card.Detail.Attacks)
	}
	// 攻击条目的标题不该带上游的内部代号。
	for _, attack := range wiki.Attacks {
		if strings.Contains(attack.Title, "P1") || strings.Contains(attack.Title, "CANNON") {
			t.Errorf("攻击标题应中文化，实际 %q", attack.Title)
		}
	}
	// 右侧栏：类型 / 分类 + 详细属性里最关键的那几个数值（参照站详情页也把这些摆在侧栏）。
	side := card.Detail.Side
	if got := fieldValue(side, "类型"); got != "主武器" {
		t.Errorf("侧栏第一行应是类型，实际 %q", got)
	}
	if got := fieldValue(side, "分类"); got != "突击步枪" {
		t.Errorf("侧栏应有分类，实际 %q", got)
	}
	if got := fieldValue(side, "弹匣容量"); got != "45" {
		t.Errorf("侧栏应带上弹匣容量，实际 %q", got)
	}
	if card.Detail.SideTitle != "武器信息" {
		t.Errorf("武器卡的侧栏标题应是「武器信息」，实际 %q", card.Detail.SideTitle)
	}
}

// TestBuildEquipmentDetailSideForStratagem 校验战备卡的侧栏标题与挑出来的数值：
// 战备关心的是冷却、使用次数与呼叫时间，不是弹匣射速。
func TestBuildEquipmentDetailSideForStratagem(t *testing.T) {
	catalog := mustCatalog(t)
	result := catalog.Search(arsenal.Query{Kinds: stratSpec.Kinds, Keyword: "哨戒炮", Limit: searchLimit})
	card := BuildEquipmentCard(stratSpec, result, catalog, nil, "哨戒炮", time.Time{})
	if card.Detail == nil {
		t.Fatal("应命中那一件")
	}
	if card.Detail.SideTitle != "战备信息" {
		t.Errorf("战备卡的侧栏标题应是「战备信息」，实际 %q", card.Detail.SideTitle)
	}
	if got := fieldValue(card.Detail.Side, "冷却时间"); got == "" {
		t.Errorf("侧栏应带上冷却时间：%+v", card.Detail.Side)
	}
	if got := fieldValue(card.Detail.Side, "使用次数"); got == "" {
		t.Errorf("侧栏应带上使用次数：%+v", card.Detail.Side)
	}
}

// TestSideTitle 校验侧栏标题按我们自己目录里的类别取：
// 内置详情把战备与投掷物放在同一张表里，手雷不该被标成「战备信息」。
func TestSideTitle(t *testing.T) {
	cases := []struct {
		kind arsenal.Kind
		want string
	}{
		{arsenal.KindPrimary, "武器信息"},
		{arsenal.KindSecondary, "武器信息"},
		{arsenal.KindSupport, "武器信息"},
		{arsenal.KindStratagem, "战备信息"},
		{arsenal.KindGrenade, "投掷物信息"},
		{arsenal.KindArmor, "护甲信息"},
	}
	for _, c := range cases {
		if got := sideTitle(c.kind); got != c.want {
			t.Errorf("%s 的侧栏标题应是 %q，实际 %q", c.kind, c.want, got)
		}
	}
}

// TestBuildEquipmentDetailSideWithoutAtlas 校验内置详情里没有的装备（护甲）也有侧栏：
// 只有类型 / 分类这类标签，不会凭空长出数值。
func TestBuildEquipmentDetailSideWithoutAtlas(t *testing.T) {
	catalog := mustCatalog(t)
	result := catalog.Search(arsenal.Query{Kinds: armorSpec.Kinds, Keyword: "侦察者", Limit: searchLimit})
	card := BuildEquipmentCard(armorSpec, result, catalog, nil, "侦察者", time.Time{})
	if card.Detail == nil {
		t.Fatal("应命中那一件")
	}
	if card.Detail.Wiki != nil {
		t.Skip("这件护甲在内置详情里出现了，换个关键字")
	}
	for _, field := range card.Detail.Side {
		if field.Label != "类型" {
			t.Errorf("没有内置详情时侧栏只该有类型这一行：%+v", card.Detail.Side)
		}
	}
}
