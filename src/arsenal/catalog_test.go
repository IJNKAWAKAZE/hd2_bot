// catalog_test.go 覆盖装备目录的解析、索引与检索。用内联夹具而不是真的上游文件：
// 这几条用例要钉住的是「字段怎么映射、排序怎么算」，不是「上游今天有没有改格式」。
package arsenal

import (
	"strings"
	"testing"
)

// fixtureJSON 是一份小目录：一件主武器（有外号）、一件战备（SVG 图 + 召唤指令）、一件护甲（有护甲段），
// 以及两本债券。字段名与上游一致，改错 tag 时这些用例会红。
const fixtureJSON = `{
  "meta": {"game": "HELLDIVERS 2", "dataVersion": "2026.08.14.1", "capturedAt": "2026-08-14T03:25:51.159Z"},
  "warbonds": [
    {"id": "mobilize", "nameZh": "绝地潜兵总动员！", "nameEn": "Helldivers Mobilize!", "superCredits": null,
     "pages": [{"page": 1, "cumulativeMedals": 0}, {"page": 2, "cumulativeMedals": 8}, {"page": 3, "cumulativeMedals": 35}],
     "wiki": {"url": "https://helldivers.wiki.gg/wiki/Helldivers_Mobilize"}},
    {"id": "dust-devils", "nameZh": "沙漠魔影", "nameEn": "Dust Devils", "superCredits": 1000,
     "pages": [{"page": 1, "cumulativeMedals": 0}, {"page": 2, "cumulativeMedals": 60}],
     "wiki": {"url": "https://helldivers.wiki.gg/wiki/Dust_Devils"}}
  ],
  "items": [
    {"id": "ar-2-coyote", "productKind": "primary-weapon", "model": "AR-2", "nameZh": "野狼", "nameEn": "AR-2 Coyote",
     "weaponType": "Assault Rifles",
     "image": {"path": "assets/wiki/ar-2-coyote.png", "filePage": "https://helldivers.wiki.gg/wiki/File:AR-2_Coyote_Primary_Render.png", "license": "License/Arrowhead"},
     "wiki": {"pageId": 13826, "url": "https://helldivers.wiki.gg/wiki/AR-2_Coyote"},
     "acquisition": {"kind": "warbond", "warbondId": "dust-devils", "page": 1, "itemMedals": 35},
     "combat": {"primaryComponentId": "13826-component-1", "components": [
        {"label": "Ballistic", "fields": {"standardDamage": 75, "armorPenetration": {"value": 3, "labelZh": "中型"}, "demolitionForce": 10}}]},
     "handling": {"magazine": 45, "spareMagazines": 8, "fireRate": 600, "recoil": 17, "firingModes": ["Auto", "Semi"]}},
    {"id": "a-m-12-mortar-sentry", "productKind": "other-stratagem", "model": "A/M-12", "nameZh": "迫击哨戒炮", "nameEn": "A/M-12 Mortar Sentry",
     "image": {"path": "assets/wiki/a-m-12-mortar-sentry.svg", "filePage": "https://helldivers.wiki.gg/wiki/File:Mortar_Sentry_Stratagem_Icon.svg", "license": "License/CC-BY-NC-SA"},
     "wiki": {"pageId": 2400, "url": "https://helldivers.wiki.gg/wiki/A/M-12_Mortar_Sentry"},
     "acquisition": {"kind": "requisition", "levelRequired": 13, "requisitionPoints": 6000},
     "combat": {"primaryComponentId": "2400-component-1", "components": [
        {"label": "Explosion", "fields": {"standardDamage": 300}},
        {"label": "Explosion", "fields": {"standardDamage": 100}}]},
     "deployment": {"type": "Sentry", "code": ["down", "up", "right", "up", "left", "up"], "cooldownSeconds": 180, "callInSeconds": 3}},
    {"id": "a-35-recon", "productKind": "body-armor", "model": "A-35", "nameZh": "A-35“侦察者”", "nameEn": "A-35 Recon",
     "image": {"path": "assets/wiki/a-35-recon.png", "filePage": "https://helldivers.wiki.gg/wiki/File:A-35_Recon_Body_Icon.png", "license": "License/CC-BY-NC-SA"},
     "wiki": {"pageId": 13673, "url": "https://helldivers.wiki.gg/wiki/A-35_Recon"},
     "acquisition": {"kind": "warbond", "warbondId": "mobilize", "page": 2, "itemMedals": 55},
     "armor": {"class": "Medium", "rating": 100, "speed": 500, "staminaRegen": 100, "passive": "Feet First"}}
  ]
}`

// fixtureAliases 是一份小外号表。
const fixtureAliases = `{"version": "2026-08-08.1", "entries": [
  {"equipmentId": "a-m-12-mortar-sentry", "aliases": ["迫击炮", "  ", "迫击哨戒"]},
  {"equipmentId": "不存在的装备", "aliases": ["幽灵"]}
]}`

// mustCatalog 解析夹具目录，解析失败直接终止用例。
func mustCatalog(t *testing.T) *Catalog {
	t.Helper()
	catalog, err := ParseCatalog([]byte(fixtureJSON), ParseAliases([]byte(fixtureAliases)))
	if err != nil {
		t.Fatalf("解析夹具目录失败：%v", err)
	}
	return catalog
}

// TestParseCatalogIndexes 校验字段映射与索引：债券装备数、主部件、SVG 标记、外号绑定、数据版本。
func TestParseCatalogIndexes(t *testing.T) {
	catalog := mustCatalog(t)

	if catalog.Len() != 3 {
		t.Fatalf("装备件数错误：%d", catalog.Len())
	}
	if catalog.DataVersion != "2026.08.14.1" || catalog.CapturedAt == "" {
		t.Errorf("数据版本与采集时间应保留：%q / %q", catalog.DataVersion, catalog.CapturedAt)
	}

	weapon, ok := catalog.Item("ar-2-coyote")
	if !ok {
		t.Fatal("应能按 id 取到装备")
	}
	if weapon.Kind != KindPrimary || KindLabel(weapon.Kind) != "主武器" {
		t.Errorf("类别映射错误：%q / %q", weapon.Kind, KindLabel(weapon.Kind))
	}
	if weapon.ImageIsSVG {
		t.Error("png 图不该被标成 SVG")
	}
	if weapon.Primary != 0 || len(weapon.Components) != 1 || weapon.Components[0].StandardDamage != 75 {
		t.Errorf("部件解析错误：primary=%d components=%+v", weapon.Primary, weapon.Components)
	}
	if weapon.Components[0].ArmorPenLabel != "中型" {
		t.Errorf("穿甲等级中文名应保留：%q", weapon.Components[0].ArmorPenLabel)
	}
	if weapon.Handling.FireRate != 600 || weapon.Handling.Recoil != 17 || len(weapon.Handling.FiringModes) != 2 {
		t.Errorf("操作性数值解析错误：%+v", weapon.Handling)
	}
	if weapon.Acq.Kind != "warbond" || weapon.Acq.WarbondID != "dust-devils" || weapon.Acq.Page != 1 || weapon.Acq.ItemMedals != 35 {
		t.Errorf("获取方式解析错误：%+v", weapon.Acq)
	}

	sentry, _ := catalog.Item("a-m-12-mortar-sentry")
	if !sentry.ImageIsSVG {
		t.Error("svg 图应被标记成 SVG（行内缩略图要换 PNG）")
	}
	if sentry.Primary != 0 || len(sentry.Components) != 2 {
		t.Errorf("多部件装备的主部件下标应为 0：primary=%d components=%d", sentry.Primary, len(sentry.Components))
	}
	if sentry.Deploy == nil || len(sentry.Deploy.Code) != 6 || sentry.Deploy.CooldownSeconds != 180 {
		t.Errorf("战备召唤信息解析错误：%+v", sentry.Deploy)
	}
	if sentry.Acq.Kind != "requisition" || sentry.Acq.RequisitionPoints != 6000 || sentry.Acq.LevelRequired != 13 {
		t.Errorf("征用点获取信息解析错误：%+v", sentry.Acq)
	}
	if len(sentry.Aliases) != 2 || sentry.Aliases[0] != "迫击炮" {
		t.Errorf("外号应绑定到装备上且剔除空串：%+v", sentry.Aliases)
	}

	armor, _ := catalog.Item("a-35-recon")
	if armor.Armor == nil || armor.Armor.Rating != 100 || armor.Armor.Passive != "Feet First" {
		t.Errorf("护甲数值解析错误：%+v", armor.Armor)
	}
	if armor.Deploy != nil {
		t.Error("护甲不该有召唤信息")
	}
}

// TestParseCatalogWarbondIndexes 校验债券侧索引：装备数、勋章总数（取最大累计值）、超级货币价。
func TestParseCatalogWarbondIndexes(t *testing.T) {
	catalog := mustCatalog(t)

	warbonds := catalog.Warbonds()
	if len(warbonds) != 2 {
		t.Fatalf("债券数错误：%d", len(warbonds))
	}
	mobilize := warbonds[0]
	if mobilize.Pages != 3 || mobilize.MedalsTotal != 35 || mobilize.ItemCount != 1 {
		t.Errorf("债券汇总错误：%+v", mobilize)
	}
	if mobilize.SuperCredits != 0 {
		t.Errorf("首发债券不单卖，超级货币应为 0：%d", mobilize.SuperCredits)
	}
	if got := catalog.WarbondName("dust-devils"); got != "沙漠魔影" {
		t.Errorf("按 id 取债券名错误：%q", got)
	}
	if got := catalog.WarbondName("不存在"); got != "" {
		t.Errorf("未知债券应返回空串，实际 %q", got)
	}
	if items := catalog.ItemsOfWarbond("mobilize"); len(items) != 1 || items[0].ID != "a-35-recon" {
		t.Errorf("债券装备反查错误：%+v", items)
	}
	if items := catalog.ItemsOfWarbond("不存在"); len(items) != 0 {
		t.Errorf("未知债券应返回空切片，实际 %+v", items)
	}
}

// TestParseCatalogWarbondMedalsUsesMax 校验勋章总数取「最大累计值」而不是「最后一个」：
// 上游万一调整页序，也不会算出一个偏小的总数。
func TestParseCatalogWarbondMedalsUsesMax(t *testing.T) {
	raw := `{"items": [{"id": "x", "nameEn": "X"}],
	         "warbonds": [{"id": "w", "nameZh": "乱序债", "pages": [{"page": 1, "cumulativeMedals": 900}, {"page": 2, "cumulativeMedals": 10}]}]}`
	catalog, err := ParseCatalog([]byte(raw), nil)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if got := catalog.Warbonds()[0].MedalsTotal; got != 900 {
		t.Errorf("勋章总数应取最大累计值 900，实际 %d", got)
	}
}

// TestParseCatalogRejectsBroken 校验结构不对时给中文错误，而不是悄悄生成一份空目录。
func TestParseCatalogRejectsBroken(t *testing.T) {
	cases := map[string]string{
		"不是 JSON": `{`,
		"没有装备":    `{"items": []}`,
		"缺 id":    `{"items": [{"nameEn": "X"}]}`,
		"缺英文名":    `{"items": [{"id": "x"}]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseCatalog([]byte(raw), nil); err == nil {
				t.Fatal("结构不对时应报错")
			} else if !strings.Contains(err.Error(), "装备目录") {
				t.Errorf("错误文案应说明是装备目录问题，实际 %v", err)
			}
		})
	}
}

// TestParseCatalogFallsBackToEnglishName 校验缺中文名（或只有一个空串）时退回英文名，卡片不会出现空标题。
func TestParseCatalogFallsBackToEnglishName(t *testing.T) {
	raw := `{"items": [{"id": "x", "nameEn": "X Gun", "nameZh": "   "}]}`
	catalog, err := ParseCatalog([]byte(raw), nil)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if got := catalog.Items()[0].NameZh; got != "X Gun" {
		t.Errorf("缺中文名时应退回英文名，实际 %q", got)
	}
}

// TestParseAliasesToleratesJunk 校验外号表坏掉只是「没有外号」，不抛错也不留空串。
func TestParseAliasesToleratesJunk(t *testing.T) {
	if got := ParseAliases([]byte("不是 JSON")); len(got) != 0 {
		t.Errorf("坏 JSON 应按空表处理，实际 %+v", got)
	}
	if got := ParseAliases(nil); len(got) != 0 {
		t.Errorf("nil 应按空表处理，实际 %+v", got)
	}
	got := ParseAliases([]byte(`{"entries": [{"equipmentId": "", "aliases": ["x"]}, {"equipmentId": "y", "aliases": []}]}`))
	if len(got) != 0 {
		t.Errorf("空 id 与空别名都不该留下条目，实际 %+v", got)
	}
}

// TestSearchByKeyword 校验检索范围与排序：中文、英文、型号、外号都能搜到，精确命中排在前面。
func TestSearchByKeyword(t *testing.T) {
	catalog := mustCatalog(t)

	cases := []struct {
		name      string
		keyword   string
		wantTop   string
		wantTotal int
	}{
		{"中文名", "迫击哨戒炮", "a-m-12-mortar-sentry", 1},
		{"英文名片段", "mortar", "a-m-12-mortar-sentry", 1},
		{"id", "a-35", "a-35-recon", 1},
		{"型号", "AR-2", "ar-2-coyote", 1},
		{"外号", "迫击炮", "a-m-12-mortar-sentry", 1},
		{"大小写与空白折叠", "  COYOTE ", "ar-2-coyote", 1},
		{"中文片段可同时命中多件", "哨戒", "a-m-12-mortar-sentry", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := catalog.Search(Query{Keyword: c.keyword})
			if len(got.Items) == 0 {
				t.Fatalf("关键字 %q 应至少命中一条", c.keyword)
			}
			if got.Items[0].ID != c.wantTop {
				t.Errorf("关键字 %q 的首条应为 %s，实际 %s", c.keyword, c.wantTop, got.Items[0].ID)
			}
			if got.Total != c.wantTotal {
				t.Errorf("关键字 %q 命中数应为 %d，实际 %d", c.keyword, c.wantTotal, got.Total)
			}
		})
	}
}

// TestSearchRankingExactBeatsContains 校验「完全相等 > 前缀 > 包含」的排序：
// 搜「野狼」时，名字就叫野狼的那把枪要排在「野狼plus」前面。
func TestSearchRankingExactBeatsContains(t *testing.T) {
	raw := `{"items": [
	  {"id": "gun-a", "nameEn": "Wild Wolf Plus", "nameZh": "野狼加强型"},
	  {"id": "gun-b", "nameEn": "Wild Wolf", "nameZh": "野狼"},
	  {"id": "gun-c", "nameEn": "Super Wild Wolf", "nameZh": "超级野狼"}]}`
	catalog, err := ParseCatalog([]byte(raw), nil)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	got := catalog.Search(Query{Keyword: "野狼"})
	if len(got.Items) != 3 || got.Total != 3 {
		t.Fatalf("三件都应命中：%+v", got)
	}
	if got.Items[0].ID != "gun-b" {
		t.Errorf("完全相等的应排第一，实际 %s", got.Items[0].ID)
	}
	// 「野狼加强型」是前缀命中，「超级野狼」只是包含命中，所以顺序是 加强型 → 超级野狼。
	if got.Items[1].ID != "gun-a" || got.Items[2].ID != "gun-c" {
		t.Errorf("前缀命中应排在包含命中前面，实际顺序 %s / %s", got.Items[1].ID, got.Items[2].ID)
	}
}

// TestSearchKindsFilter 校验多类别过滤：/gun 只查武器三类，不把护甲与战备混进来。
func TestSearchKindsFilter(t *testing.T) {
	catalog := mustCatalog(t)

	all := catalog.Search(Query{Keyword: "哨戒"})
	if all.Total != 1 {
		t.Fatalf("不限类别时命中数错误：%d", all.Total)
	}
	weapons := catalog.Search(Query{Kinds: Weapons, Keyword: ""})
	if weapons.Total != 1 || weapons.Items[0].ID != "ar-2-coyote" {
		t.Errorf("武器类别应只命中主武器那件：%+v", weapons.Items)
	}
	armor := catalog.Search(Query{Kind: KindArmor, Keyword: ""})
	if len(armor.Items) != 1 || armor.Items[0].ID != "a-35-recon" {
		t.Errorf("护甲类别命中错误：%+v", armor.Items)
	}
	if got := catalog.Search(Query{Kind: KindGrenade, Keyword: ""}); got.Total != 0 || len(got.Items) != 0 {
		t.Errorf("夹具里没有手雷，应命中 0 条：%+v", got)
	}
}

// TestSearchEmptyKeywordReturnsCatalogOrder 校验空关键字返回目录顺序的前若干件（默认列表），
// 而不是空结果——群里打 /gun 不带关键字时，给一份清单比回一句「请提供关键字」有用。
func TestSearchEmptyKeywordReturnsCatalogOrder(t *testing.T) {
	catalog := mustCatalog(t)
	got := catalog.Search(Query{Limit: 2})
	if len(got.Items) != 2 || got.Total != 3 {
		t.Fatalf("应返回前两件并给出总数 3：len=%d total=%d", len(got.Items), got.Total)
	}
	if got.Items[0].ID != "ar-2-coyote" || got.Items[1].ID != "a-m-12-mortar-sentry" {
		t.Errorf("默认列表应保持目录顺序：%+v", got.Items)
	}
}

// TestKindAndAcqLabelsFallBack 校验类别与获取方式的中文名：未知取值原样返回，绝不返回空串。
func TestKindAndAcqLabelsFallBack(t *testing.T) {
	if got := KindLabel(Kind("new-kind")); got != "new-kind" {
		t.Errorf("未知类别应原样返回，实际 %q", got)
	}
	for kind, want := range map[string]string{
		"warbond": "军需簿", "requisition": "征用点", "superstore": "超级商店",
		"event": "活动奖励", "default": "默认解锁", "unavailable": "已下架",
	} {
		if got := AcqLabel(kind); got != want {
			t.Errorf("AcqLabel(%q) = %q，期望 %q", kind, got, want)
		}
	}
	if got := AcqLabel("brand-new"); got != "brand-new" {
		t.Errorf("未知获取方式应原样返回，实际 %q", got)
	}
}

// TestPrimaryComponentIndex 校验主部件下标的解析与兜底。
func TestPrimaryComponentIndex(t *testing.T) {
	cases := []struct {
		id    string
		total int
		want  int
	}{
		{"20141-component-1", 2, 0},
		{"20141-component-2", 2, 1},
		{"", 2, 0},                   // 上游没给：退回第一个
		{"20141-component-9", 2, 0},  // 越界：退回第一个
		{"乱写", 2, 0},                 // 解析不出来：退回第一个
		{"20141-component-1", 0, -1}, // 没有部件：-1 表示没有
	}
	for _, c := range cases {
		if got := primaryComponentIndex(c.id, c.total); got != c.want {
			t.Errorf("primaryComponentIndex(%q, %d) = %d，期望 %d", c.id, c.total, got, c.want)
		}
	}
}

// TestParseCatalogToleratesNonIntegerNumbers 钉住一条实测事实：上游确实会把数值写成小数
// （2026-08-14 版目录里，两件装备的弹匣是 6.67 / 12.5，还有一件的射速是 3.34）。
// 用 int 接收这类字段会让**整份目录**解析失败——一件装备的脏数据不该让 298 件都查不了。
func TestParseCatalogToleratesNonIntegerNumbers(t *testing.T) {
	const dirty = `{
	  "warbonds": [{"id": "w", "nameZh": "测试本", "superCredits": 1000.5,
	    "pages": [{"page": 1, "cumulativeMedals": "35"}, {"page": 2, "cumulativeMedals": 59.6}]}],
	  "items": [
	    {"id": "dirty-gun", "productKind": "primary-weapon", "nameEn": "Dirty Gun", "nameZh": "脏数据枪",
	     "acquisition": {"kind": "warbond", "warbondId": "w", "page": 1.0, "itemMedals": "35"},
	     "combat": {"primaryComponentId": "x-component-1", "components": [
	        {"label": "Ballistic", "fields": {"standardDamage": 75.4, "durableDamage": "22", "armorPenetration": {"value": 3.6}}}]},
	     "handling": {"magazine": 6.67, "spareMagazines": 12.5, "fireRate": 3.34, "firingModes": ["Auto"]},
	     "deployment": {"type": "Support Weapon", "code": ["down"], "cooldownSeconds": 480.5}},
	    {"id": "junk-number", "productKind": "grenade", "nameEn": "Junk Number",
	     "handling": {"magazine": "说不清", "fireRate": "x"}}
	  ]
	}`
	catalog, err := ParseCatalog([]byte(dirty), nil)
	if err != nil {
		t.Fatalf("数值写成小数不该让整份目录解析失败：%v", err)
	}

	item, ok := catalog.Item("dirty-gun")
	if !ok {
		t.Fatal("应能取到脏数据条目")
	}
	// 小数四舍五入取整，数字字符串按数字收下。
	if item.Handling.Magazine != 7 || item.Handling.SpareMagazines != 13 || item.Handling.FireRate != 3 {
		t.Errorf("操作性数值换算错误：%+v", item.Handling)
	}
	if item.Components[0].StandardDamage != 75 || item.Components[0].DurableDamage != 22 || item.Components[0].ArmorPenValue != 4 {
		t.Errorf("伤害数值换算错误：%+v", item.Components[0])
	}
	if item.Deploy == nil || item.Deploy.CooldownSeconds != 481 {
		t.Errorf("冷却时间换算错误：%+v", item.Deploy)
	}
	if item.Acq.Page != 1 || item.Acq.ItemMedals != 35 {
		t.Errorf("获取方式数值换算错误：%+v", item.Acq)
	}

	book, ok := catalog.Warbond("w")
	if !ok {
		t.Fatal("应能取到脏数据军需簿")
	}
	if book.SuperCredits != 1001 || book.MedalsTotal != 60 {
		t.Errorf("军需簿数值换算错误：%+v", book)
	}

	// 实在解析不出来的数值留 0（卡片上不显示那一项），不影响其它字段。
	junk, ok := catalog.Item("junk-number")
	if !ok {
		t.Fatal("应能取到数值无法解析的条目")
	}
	if junk.Handling.Magazine != 0 || junk.Handling.FireRate != 0 {
		t.Errorf("解析不出的数值应留 0，实际 %+v", junk.Handling)
	}
	if junk.NameZh != "Junk Number" {
		t.Errorf("数值问题不该影响名称兜底：%q", junk.NameZh)
	}
}
