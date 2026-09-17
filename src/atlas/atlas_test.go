// 本包的内置数据是随仓库发布的：这些用例盯的是「数据还在、解释规则没走样」，
// 而不是逐字段比对上游内容（那样上游一更新就要改测试，也测不出真问题）。
package atlas

import (
	"strings"
	"testing"
)

// mustLoad 加载内置数据；解析失败直接让用例失败（数据坏了应当立刻暴露）。
func mustLoad(t *testing.T) *DB {
	t.Helper()
	db, err := Load()
	if err != nil {
		t.Fatalf("加载内置详情失败：%v", err)
	}
	return db
}

// TestLoadParsesEverything 校验三类条目都解析出来了，且总量像回事。
func TestLoadParsesEverything(t *testing.T) {
	db := mustLoad(t)
	if db.Len() < 250 {
		t.Fatalf("内置条目太少（%d）：数据文件可能没打进来", db.Len())
	}
	if len(db.weapons) < 80 || len(db.stratagems) < 100 || len(db.enemies) < 90 {
		t.Errorf("三类条目数不对：武器 %d、战备 %d、敌人 %d", len(db.weapons), len(db.stratagems), len(db.enemies))
	}
}

// TestLookupIgnoresCaseAndPunctuation 校验索引键折叠了大小写、空格与连字符：中文名、英文名都能查到同一条。
func TestLookupIgnoresCaseAndPunctuation(t *testing.T) {
	db := mustLoad(t)
	byZh, ok := db.Weapon("AR-23 解放者")
	if !ok {
		t.Fatal("按中文名应查到解放者")
	}
	for _, keyword := range []string{"AR-23 Liberator", "ar23liberator", " ar-23 liberator "} {
		got, ok := db.Weapon(keyword)
		if !ok {
			t.Errorf("关键字 %q 应命中", keyword)
			continue
		}
		if got.Name != byZh.Name {
			t.Errorf("关键字 %q 查到的不是同一条：%q", keyword, got.Name)
		}
	}
	for _, keyword := range []string{"", "   ", "不存在的装备名"} {
		if _, ok := db.Weapon(keyword); ok {
			t.Errorf("关键字 %q 不该命中", keyword)
		}
	}
}

// TestGearLooksUpWeaponThenStratagem 校验装备类查询先找武器再找战备（/gun、/strat 共用一条路）。
func TestGearLooksUpWeaponThenStratagem(t *testing.T) {
	db := mustLoad(t)
	weapon, ok := db.Gear("AR-23 Liberator")
	if !ok || weapon.Kind != KindWeapon {
		t.Fatalf("应查到武器：%+v", weapon)
	}
	stratagem, ok := db.Gear("自动哨戒炮")
	if !ok || stratagem.Kind != KindStratagem {
		t.Fatalf("应查到战备：%+v", stratagem)
	}
	if _, ok := db.Enemy("自动哨戒炮"); ok {
		t.Error("战备不该出现在敌人索引里")
	}
}

// TestWeaponDetailIsTranslated 校验武器的详细属性：键名中文化、数值翻译、攻击按弹体/伤害/穿透分组。
func TestWeaponDetailIsTranslated(t *testing.T) {
	db := mustLoad(t)
	entry, ok := db.Weapon("AR-23 Liberator")
	if !ok {
		t.Fatal("应查到 AR-23 解放者")
	}
	if entry.Category != "主武器" || entry.SubCategory != "突击步枪" {
		t.Errorf("类别应中文化：%q / %q", entry.Category, entry.SubCategory)
	}
	if entry.Description == "" || entry.Unlock == "" {
		t.Errorf("描述与解锁条件不该为空：%+v", entry)
	}
	if len(entry.Stats) == 0 || entry.Stats[0].Title != "详细属性" {
		t.Fatalf("第一组应是「详细属性」：%+v", entry.Stats)
	}
	cells := map[string]string{}
	for _, group := range entry.Stats {
		for _, cell := range group.Cells {
			cells[cell.Label] = cell.Value
		}
	}
	if cells["射速"] == "" || cells["弹匣容量"] == "" || cells["后坐力"] == "" {
		t.Errorf("详细属性缺项：%+v", cells)
	}
	if strings.Contains(cells["射速"], "rpm") {
		t.Errorf("射速应翻译成中文单位，实际 %q", cells["射速"])
	}

	if len(entry.Attacks) == 0 {
		t.Fatal("应有攻击条目")
	}
	attack := entry.Attacks[0]
	if attack.Title != "" {
		t.Errorf("攻击标题留给展示层编号，数据层不填上游内部代号：%q", attack.Title)
	}
	if attack.Type != "弹道" {
		t.Errorf("攻击类型应中文化，实际 %q", attack.Type)
	}
	groupTitles := map[string]bool{}
	values := map[string]string{}
	for _, group := range attack.Groups {
		groupTitles[group.Title] = true
		for _, cell := range group.Cells {
			values[cell.Label] = cell.Value
		}
	}
	for _, want := range []string{"弹体", "伤害", "穿透", "特殊效果"} {
		if !groupTitles[want] {
			t.Errorf("攻击条目应有「%s」组：%+v", want, groupTitles)
		}
	}
	if values["标准伤害"] == "" || values["耐久伤害"] == "" {
		t.Errorf("伤害组缺项：%+v", values)
	}
	if strings.Contains(values["标准伤害"], "Ballistic") {
		t.Errorf("伤害类型应翻译，实际 %q", values["标准伤害"])
	}
	if strings.Contains(values["直射穿透"], "Light") {
		t.Errorf("穿透等级应翻译，实际 %q", values["直射穿透"])
	}
}

// TestStratagemCodeSteps 校验战备的呼叫指令串按方向字母拆开（参照站用的是首字母缩写）。
func TestStratagemCodeSteps(t *testing.T) {
	db := mustLoad(t)
	entry, ok := db.Stratagem("轨道120 MM高爆弹火力网")
	if !ok {
		t.Fatal("应查到轨道120 MM高爆弹火力网")
	}
	if got := strings.Join(entry.Code, ","); got != "right,right,down,left,right,down" {
		t.Errorf("指令串解析错误：%q", got)
	}
	if entry.Cooldown == "" {
		t.Error("战备应有冷却时间")
	}
}

// TestExplosionOnImpactShowsYes 校验「命中引爆」这一列不显示上游的弹药内部代号，只显示是不是。
func TestExplosionOnImpactShowsYes(t *testing.T) {
	db := mustLoad(t)
	entry, ok := db.Stratagem("自动哨戒炮")
	if !ok {
		t.Fatal("应查到自动哨戒炮")
	}
	for _, attack := range entry.Attacks {
		for _, group := range attack.Groups {
			for _, cell := range group.Cells {
				if cell.Label == "命中引爆" && cell.Value != "是" {
					t.Errorf("命中引爆应显示「是」，实际 %q", cell.Value)
				}
				if strings.Contains(cell.Value, "HE CANNON") || strings.Contains(cell.Value, "_P1_IE") {
					t.Errorf("数值里不该出现上游内部代号：%q = %q", cell.Label, cell.Value)
				}
			}
		}
	}
}

// TestEnemyDetail 校验敌人详情：生命值/伤害类型/最低难度与部位数据都翻好了。
func TestEnemyDetail(t *testing.T) {
	db := mustLoad(t)
	entry, ok := db.Enemy("Bile Titan")
	if !ok {
		t.Fatal("应查到 Bile Titan")
	}
	if entry.Health == "" || entry.Damage == "" || entry.DamageType == "" || entry.Difficulty == "" {
		t.Errorf("基础信息缺项：%+v", entry)
	}
	if len(entry.Parts) < 3 {
		t.Fatalf("部位数据太少：%d 个", len(entry.Parts))
	}
	fatal, weak := false, false
	for _, part := range entry.Parts {
		if part.Name == "" {
			t.Errorf("部位名不该为空：%+v", part)
		}
		fatal = fatal || part.Fatal
		weak = weak || part.Weak
	}
	if !fatal || !weak {
		t.Errorf("部位表应能标出致命与弱点：致命 %v、弱点 %v", fatal, weak)
	}
	if _, ok := db.Enemy("吐酸泰坦"); !ok {
		t.Error("按上游中文名也该查得到")
	}
}

// TestEnemyPartsCarryImageAndSource 校验部位带示意图地址、条目带来源页：
// 卡片上的部位表要画缩略图，侧栏要给出来源链接，这两项丢了就成了一条没有出处的数据。
func TestEnemyPartsCarryImageAndSource(t *testing.T) {
	db := mustLoad(t)
	entry, ok := db.Enemy("Bile Titan")
	if !ok {
		t.Fatal("按英文名应查到胆汁泰坦")
	}
	if !strings.HasPrefix(entry.Source, "https://") {
		t.Errorf("来源页应是公网地址，实际 %q", entry.Source)
	}
	if !strings.HasPrefix(entry.Image, "https://") {
		t.Errorf("敌人应有外观图地址，实际 %q", entry.Image)
	}
	withImage := 0
	for _, part := range entry.Parts {
		if strings.HasPrefix(part.Image, "https://") {
			withImage++
		}
	}
	if withImage == 0 {
		t.Errorf("部位数据里应至少有几个带示意图的部位，实际 %d/%d", withImage, len(entry.Parts))
	}
}

// TestLabelOfFallsBackToRawKey 校验没收录的键名原样显示（下划线换空格），不吞字段。
func TestLabelOfFallsBackToRawKey(t *testing.T) {
	if got := labelOf("unknown_stat_key"); got != "unknown stat key" {
		t.Errorf("未收录键名应原样显示，实际 %q", got)
	}
	if got := labelOf("fire_rate"); got != "射速" {
		t.Errorf("收录的键名应中文化，实际 %q", got)
	}
}

// TestDefaultReturnsUsableDB 校验 Default() 现成可用（插件层就是这么拿的），失败也只退化成空库。
func TestDefaultReturnsUsableDB(t *testing.T) {
	db := Default()
	if db == nil {
		t.Fatal("Default 不该返回 nil")
	}
	if _, ok := db.Weapon("AR-23 Liberator"); !ok {
		t.Error("默认库里应能查到解放者")
	}
}

// TestDecodeObjectKeepsKeyOrder 校验有序对象：键顺序就是站点上的顺序，卡片上的数值顺序才不会随机。
func TestDecodeObjectKeepsKeyOrder(t *testing.T) {
	obj, ok := decodeObject([]byte(`{"b": 1, "a": 2, "c": 3}`))
	if !ok {
		t.Fatal("应解析成有序对象")
	}
	if got := strings.Join(obj.Keys, ","); got != "b,a,c" {
		t.Errorf("键顺序被改了：%q", got)
	}
}

// TestDescriptionDropsVersionString 校验「版本号 + 日期」这种不是描述的描述被丢掉
// （实测毒气榴弹的 description 就是 "1.006.202 2026-04-28"）。
func TestDescriptionDropsVersionString(t *testing.T) {
	if got := cleanDescription("1.006.202 2026-04-28"); got != "" {
		t.Errorf("版本号不该当描述，实际 %q", got)
	}
	if got := cleanDescription("任何计算都应基于详细武器统计部分列出的数值。"); got == "" {
		t.Error("正常描述应保留")
	}
}

// TestGrenadeDescriptionIsClean 校验图鉴里那颗毒气榴弹的描述是空的（上游那一列是版本号）。
func TestGrenadeDescriptionIsClean(t *testing.T) {
	db := mustLoad(t)
	entry, ok := db.Weapon("G-4 Gas")
	if !ok {
		t.Fatal("应查到 G-4 毒气榴弹")
	}
	if entry.Description != "" {
		t.Errorf("版本号不该当描述显示：%q", entry.Description)
	}
}
