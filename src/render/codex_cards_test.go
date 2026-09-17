// 本文件属于外部测试包 render_test：断言「图鉴视图模型 → 卡片 HTML」这条跨包链路，
// 必须 import src/plugins/codex（codex 依赖 render，写进包内会形成 import 环）。
package render_test

import (
	"bytes"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"

	"hd2_bot/src/arsenal"
	"hd2_bot/src/bestiary"
	"hd2_bot/src/plugins/codex"
	"hd2_bot/src/render"
)

// codexFixtureJSON 是一份够排版用的小目录：一件主武器（带弹匣/射速/召唤指令/债券来源）、
// 一件护甲、一本债券。
const codexFixtureJSON = `{
  "meta": {"dataVersion": "2026.08.14.1", "capturedAt": "2026-08-14T03:25:51.159Z"},
  "warbonds": [
    {"id": "dust-devils", "nameZh": "沙漠魔影", "nameEn": "Dust Devils", "superCredits": 1000,
     "pages": [{"page": 1, "cumulativeMedals": 0}, {"page": 2, "cumulativeMedals": 60}],
     "wiki": {"url": "https://helldivers.wiki.gg/wiki/Dust_Devils"}}
  ],
  "items": [
    {"id": "ar-2-coyote", "productKind": "primary-weapon", "model": "AR-2", "nameZh": "野狼", "nameEn": "AR-2 Coyote",
     "image": {"path": "assets/wiki/ar-2-coyote.png"},
     "acquisition": {"kind": "warbond", "warbondId": "dust-devils", "page": 1, "itemMedals": 35},
     "combat": {"primaryComponentId": "1-component-1", "components": [
        {"label": "Ballistic", "fields": {"standardDamage": 75, "armorPenetration": {"value": 3, "labelZh": "中型"}}}]},
     "handling": {"magazine": 45, "spareMagazines": 8, "fireRate": 600, "firingModes": ["Auto", "Semi"]},
     "deployment": {"type": "Support Weapon", "code": ["down", "up", "left"], "cooldownSeconds": 480}},
    {"id": "a-35-recon", "productKind": "body-armor", "model": "A-35", "nameZh": "A-35“侦察者”", "nameEn": "A-35 Recon",
     "image": {"path": "assets/wiki/a-35-recon.png"},
     "acquisition": {"kind": "warbond", "warbondId": "dust-devils", "page": 2, "itemMedals": 55},
     "armor": {"class": "Medium", "rating": 100, "speed": 500, "staminaRegen": 100, "passive": "Feet First"}}
  ]
}`

// codexFixtureCatalog 解析上面那份目录；失败直接终止用例。
func codexFixtureCatalog(t *testing.T) *arsenal.Catalog {
	t.Helper()
	catalog, err := arsenal.ParseCatalog([]byte(codexFixtureJSON), nil)
	if err != nil {
		t.Fatalf("解析夹具目录失败：%v", err)
	}
	return catalog
}

// codexGearDataURI 造一张真 PNG 并转成卡片里的装备图 data URI。
func codexGearDataURI(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatalf("造 PNG 失败：%v", err)
	}
	return render.ThumbDataURI(buf.Bytes(), 240)
}

// codexGunSpec 是测试用的装备卡片口径（与线上 /gun 一致）。
func codexGunSpec() codex.EquipmentSpec {
	return codex.EquipmentSpec{
		Command:  "gun",
		Title:    "武器图鉴",
		Subtitle: "主武器 · 副武器 · 支援武器",
		Emblem:   "emblem.super_earth",
		Kinds:    arsenal.Weapons,
	}
}

// TestEquipmentCardHTML 校验装备卡片：命中那一件的图框、名称/型号/标签/数值/召唤指令与卡片说明。
func TestEquipmentCardHTML(t *testing.T) {
	catalog := codexFixtureCatalog(t)
	result := catalog.Search(arsenal.Query{Kinds: arsenal.Weapons, Keyword: "野狼", Limit: 8})
	card := codex.BuildEquipmentCard(codexGunSpec(), result, catalog,
		map[string]string{"ar-2-coyote": codexGearDataURI(t)}, "野狼",
		time.Date(2026, 8, 14, 11, 25, 0, 0, time.UTC))

	html, err := render.HTML(render.Card{Name: "equipment", Data: card})
	if err != nil {
		t.Fatalf("渲染装备卡片 HTML 失败：%v", err)
	}
	for _, want := range []string{
		"武器图鉴",
		"主武器 · 副武器 · 支援武器",
		"2026-08-14 11:25",
		// 卡片头栏的徽标换成了游戏原生图标（内联 SVG）：这里断言的是大图框，不再断言 PNG 的 data URI。
		`<div class="hero__img"><img src="data:image/png;base64,`, // 单件详情的大图框（整块大图，与参照站详情页一致）
		`<div class="item__name">野狼 <span class="model mono">AR-2</span></div>`,
		"AR-2 Coyote",
		// 数值走参考站详情页的「详细数据」样式：4 列小格，左边属性名、下面数值。
		`<h2 class="panel__head">详细数据</h2>`,
		`<div class="tiles">`,
		`<span class="tile__k">弹匣容量</span><span class="tile__v ">45</span>`,
		`<h2 class="panel__head">攻击部件</h2>`,
		`<span class="summon__k">召唤指令</span>`,
		`<span class="summon__arrow"><svg`, // 召唤指令的箭头是内联的游戏箭头图形
		"债券「沙漠魔影」",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("装备卡片 HTML 缺少 %q", want)
		}
	}
	// 数据来源与许可声明不上卡片（用户 2026-09-17 要求），完整口径只在仓库 LICENSES.md 里。
	for _, unwanted := range []string{"数据来源", "LICENSES.md", "HD2Tool"} {
		if strings.Contains(html, unwanted) {
			t.Errorf("装备卡片 HTML 不该出现 %q", unwanted)
		}
	}
	// 卡片上只有命中的这一件：分组列表不再出现——列表形态只存在于行内查询的候选里。
	if strings.Contains(html, `<ul class="list">`) {
		t.Error("详情卡不该带分组列表")
	}
	if strings.Contains(html, "没有命中的装备。") {
		t.Error("有命中时不该出空结果提示")
	}
}

// TestEquipmentCardHTMLWithoutImages 校验没有装备图时整个图框不渲染（不留空白占位）。
func TestEquipmentCardHTMLWithoutImages(t *testing.T) {
	catalog := codexFixtureCatalog(t)
	result := catalog.Search(arsenal.Query{Kinds: arsenal.Weapons, Keyword: "野狼", Limit: 8})
	card := codex.BuildEquipmentCard(codexGunSpec(), result, catalog, nil, "野狼", time.Time{})

	html, err := render.HTML(render.Card{Name: "equipment", Data: card})
	if err != nil {
		t.Fatalf("渲染装备卡片 HTML 失败：%v", err)
	}
	if strings.Contains(html, `class="media`) {
		t.Error("没有图时不该渲染装备图框")
	}
	if !strings.Contains(html, "野狼") {
		t.Error("没有图不影响文字排版")
	}
}

// TestEquipmentCardHTMLEmpty 校验一件都没命中时的提示与建议。
func TestEquipmentCardHTMLEmpty(t *testing.T) {
	card := codex.BuildEquipmentCard(codexGunSpec(), arsenal.Result{}, nil, nil, "不存在", time.Time{})
	html, err := render.HTML(render.Card{Name: "equipment", Data: card})
	if err != nil {
		t.Fatalf("渲染装备卡片 HTML 失败：%v", err)
	}
	for _, want := range []string{"关键字「不存在」没有命中。", "玩家外号"} {
		if !strings.Contains(html, want) {
			t.Errorf("空结果卡片 HTML 缺少 %q", want)
		}
	}
	if strings.Contains(html, `class="media`) {
		t.Error("空结果不该渲染图框")
	}
}

// TestWarbondsCardHTMLLists 校验债券名单分支：标题、名称、页数、勋章、价格与件数。
func TestWarbondsCardHTMLLists(t *testing.T) {
	catalog := codexFixtureCatalog(t)
	card := codex.BuildWarbondsCard(catalog.Warbonds(), nil, "", time.Date(2026, 8, 14, 11, 25, 0, 0, time.UTC))
	html, err := render.HTML(render.Card{Name: "warbonds", Data: card})
	if err != nil {
		t.Fatalf("渲染债券卡片 HTML 失败：%v", err)
	}
	for _, want := range []string{"债券", "全部债券", "沙漠魔影", "Dust Devils", "2 页", "60 勋章", "1,000", "2 件", `class="card__emblem"`} {
		if !strings.Contains(html, want) {
			t.Errorf("债券名单 HTML 缺少 %q", want)
		}
	}
	if strings.Contains(html, "<h2 class=\"section__title\">装备</h2>") {
		t.Error("名单分支不该出现装备明细小节")
	}
}

// TestWarbondsCardHTMLDetail 校验债券明细分支：页数/勋章/价格三行、装备列表与截断说明。
func TestWarbondsCardHTMLDetail(t *testing.T) {
	catalog := codexFixtureCatalog(t)
	book, ok := catalog.Warbond("dust-devils")
	if !ok {
		t.Fatal("夹具里应有 dust-devils")
	}
	detail := codex.BuildWarbondDetail(book, catalog.ItemsOfWarbond(book.ID), catalog)
	card := codex.BuildWarbondsCard(catalog.Warbonds(), detail, "沙漠魔影", time.Time{})

	html, err := render.HTML(render.Card{Name: "warbonds", Data: card})
	if err != nil {
		t.Fatalf("渲染债券明细 HTML 失败：%v", err)
	}
	for _, want := range []string{"沙漠魔影", "全部解锁勋章", "超级货币", "装备", "野狼", "主武器", "A-35“侦察者”"} {
		if !strings.Contains(html, want) {
			t.Errorf("债券明细 HTML 缺少 %q", want)
		}
	}
	if strings.Contains(html, "全部债券") {
		t.Error("明细分支不该出现名单小节")
	}
}

// TestEnemyCardHTML 校验敌人卡片：命中的那一只（阵营配色 class、外观图、基础信息、部位表与侧栏）都在。
func TestEnemyCardHTML(t *testing.T) {
	list := []bestiary.Enemy{
		{Title: "Bile Titan", NameZh: "胆汁泰坦", Faction: bestiary.FactionTerminid, FactionRaw: "Terminids", Size: 3, Health: 6500, Damage: "【酸液】950 ｜ 【近战】1000"},
		{Title: "Scavenger", NameZh: "食腐虫", Faction: bestiary.FactionAutomaton, FactionRaw: "Automatons", Size: 0, Health: 60},
	}
	result := bestiary.Search(list, bestiary.Query{Keyword: "泰坦", Limit: 8})
	images := codex.EnemyImages{
		Icon:  codexGearDataURI(t),
		Parts: map[string]string{"主体": codexGearDataURI(t)},
	}
	related := []codex.EnemyLink{{Name: "食腐虫", Category: "小型"}}
	card := codex.BuildEnemyCard(result, "泰坦", images, related, time.Date(2026, 8, 14, 11, 25, 0, 0, time.UTC))

	html, err := render.HTML(render.Card{Name: "enemy", Data: card})
	if err != nil {
		t.Fatalf("渲染敌人卡片 HTML 失败：%v", err)
	}
	for _, want := range []string{
		"敌人图鉴", "阵营 · 体型 · 血量 · 部位", "2026-08-14 11:25",
		"胆汁泰坦", "Bile Titan",
		// 左右两栏的版式：主栏放外观 / 基础信息 / 部位数据，侧栏放总生命值、同阵营与来源。
		`<div class="wiki">`,
		`<aside class="wiki__side">`,
		`<h2 class="panel__head">外观</h2>`,
		`<div class="hero__img"><img src="data:image/png;base64,`,
		`<h2 class="panel__head">基础信息</h2>`,
		`<div class="side-sum__hp"><b>6,500</b>总生命值</div>`,
		`<span class="kv__v faction--terminid">终结族</span>`,
		`<span class="kv__k">体型</span><span class="kv__v ">巨型</span>`,
		`<h2 class="panel__head">部位数据 <small>共 12 个部位</small></h2>`,
		// 部位示意图那一列：图是运行时下载的，走 data URI。
		`<th>图片</th>`,
		`<img class="part-img" src="data:image/png;base64,`,
		`<th>装甲</th>`,
		// 侧栏的同阵营名单与来源。
		`<h2 class="panel__head">同阵营敌人</h2>`,
		`<span>食腐虫</span>`,
		"派系变体",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("敌人卡片 HTML 缺少 %q", want)
		}
	}
	// 只画命中的那一只：没命中的敌人只在侧栏的「同阵营敌人」里列名字，不出现在正文。
	if strings.Contains(html, `class="item__name">食腐虫`) {
		t.Error("卡片正文只该画命中的那一只")
	}
	// 侧栏的「来源」是这一页的出处（参照站详情页也有），但「数据来源」这种口径说明不上卡片。
	for _, unwanted := range []string{"数据来源", "wiki.gg 数值"} {
		if strings.Contains(html, unwanted) {
			t.Errorf("敌人卡片 HTML 不该出现 %q", unwanted)
		}
	}
	if strings.Contains(html, `<h2 class="panel__head">来源</h2>`) {
		t.Error("敌人卡片不该有「来源」侧栏")
	}
	if strings.Contains(html, "没有这只敌人") {
		t.Error("有命中时不该出空结果提示")
	}
}

// TestEquipmentDetailCardHTML 校验单件详情卡：大图、标签、召唤指令箭头块、数值网格、攻击部件与右侧栏都在
// （版式照着社区维基的详情页：左主栏放数值，右栏放类型与关键数值）。
func TestEquipmentDetailCardHTML(t *testing.T) {
	catalog := codexFixtureCatalog(t)
	result := catalog.Search(arsenal.Query{Kinds: arsenal.Weapons, Keyword: "野狼", Limit: 8})
	card := codex.BuildEquipmentCard(codexGunSpec(), result, catalog,
		map[string]string{"ar-2-coyote": codexGearDataURI(t)}, "野狼", time.Time{})

	html, err := render.HTML(render.Card{Name: "equipment", Data: card})
	if err != nil {
		t.Fatalf("渲染装备详情卡 HTML 失败：%v", err)
	}
	for _, want := range []string{
		`<div class="hero__img"><img src="data:image/png;base64,`,
		"data:image/png;base64,", // 装备图（运行时下载的位图）走 data URI
		"野狼",
		"AR-2 Coyote",
		`<div class="chips"><span class="chip">主武器</span></div>`,
		"获取：债券「沙漠魔影」",
		`<span class="summon__arrow"><svg`,                // 召唤指令的箭头是内联的游戏箭头图形
		`points="12,21 5,13 10,13 10,3 14,3 14,13 19,13"`, // 向下箭头（↓）的形状
		`<h2 class="panel__head">详细数据</h2>`,
		`<span class="tile__k">弹匣容量</span><span class="tile__v ">45</span>`,
		`<span class="tile__k">备用弹匣</span><span class="tile__v ">8</span>`,
		`<h2 class="panel__head">攻击部件</h2>`,
		`<div class="attack__head"><span>攻击 #1</span><span class="chip">弹道</span></div>`,
		`<span class="tile__k">耐久伤害</span>`,
		// 右侧栏：类型 / 分类这类标签，加上关键数值。
		`<aside class="wiki__side">`,
		`<h2 class="panel__head">武器信息</h2>`,
		`<span class="kv__k">类型</span><span class="kv__v ">主武器</span>`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("装备详情卡 HTML 缺少 %q", want)
		}
	}
	// 详情卡只写这一件：分组列表不再出现（否则同一件装备在一张图里出现两次）。
	if strings.Contains(html, `<ul class="list">`) {
		t.Error("详情卡不该再带分组列表")
	}
}

// TestEnemyCardHTMLEmpty 校验没有命中时的提示。
func TestEnemyCardHTMLEmpty(t *testing.T) {
	card := codex.BuildEnemyCard(bestiary.Result{}, "不存在", codex.EnemyImages{}, nil, time.Time{})
	html, err := render.HTML(render.Card{Name: "enemy", Data: card})
	if err != nil {
		t.Fatalf("渲染敌人卡片 HTML 失败：%v", err)
	}
	for _, want := range []string{"关键字「不存在」没有命中。", "追猎虫", "支持中文名与英文名"} {
		if !strings.Contains(html, want) {
			t.Errorf("空结果卡片 HTML 缺少 %q", want)
		}
	}
	if strings.Contains(html, `<div class="damage__row">`) {
		t.Error("空结果不该出伤害行")
	}
}
