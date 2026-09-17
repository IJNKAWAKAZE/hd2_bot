package bestiary

import (
	"strings"
	"testing"

	"hd2_bot/src/wikigg"
)

// TestParseFiltersEmptyTitleAndHD1 校验空标题与《绝地潜兵 1》的阵营被剔除，HD2 条目留下并补上中文名。
func TestParseFiltersEmptyTitleAndHD1(t *testing.T) {
	rows := []wikigg.EnemyRow{
		{Title: "", Faction: "Illuminate", Size: "3", Health: "18,001"},
		{Title: "Squadleader Soldier", Faction: "Cyborgs", Size: "", Health: "250"},
		{Title: "Bug Warrior", Faction: "Bugs", Size: "1", Health: "300"},
		{Title: "Watcher", Faction: "HD1 Illuminate", Size: "1", Health: "300"},
		{Title: "Bug Trooper", Faction: "Super Earth Federation", Size: "1", Health: "300"},
		{Title: "Commando", Faction: "Cyborg Legion", Size: "1", Health: "300"},
		{Title: "Bile Titan", Faction: "Terminids", Size: "3", Health: "6,500"},
	}

	list := Parse(rows, nil, "https://helldivers.wiki.gg")
	if len(list) != 1 {
		t.Fatalf("应只留下 1 只 HD2 敌人，实际 %d 只：%+v", len(list), list)
	}
	got := list[0]
	if got.Title != "Bile Titan" || got.NameZh != "吐酸泰坦" {
		t.Errorf("应补上官方简中译名，实际 %q / %q", got.Title, got.NameZh)
	}
	if got.Faction != FactionTerminid || got.Variant != "" {
		t.Errorf("终结族条目不该有变体名，实际 %q / %q", got.Faction, got.Variant)
	}
	if got.Size != 3 || got.Health != 6500 {
		t.Errorf("体型与血量解析不对：%+v", got)
	}
}

// TestParseFallsBackToEnglishName 校验译名表没收录的条目沿用英文名，绝不返回空名字。
func TestParseFallsBackToEnglishName(t *testing.T) {
	list := Parse([]wikigg.EnemyRow{
		{Title: "Ground All-Terrain Extraction Rig (GATER)", Faction: "Super Earth"},
	}, nil, "")
	if len(list) != 1 {
		t.Fatalf("应留下 1 只敌人，实际 %d 只", len(list))
	}
	if list[0].NameZh != list[0].Title {
		t.Errorf("没有译名时应沿用英文名，实际 %q", list[0].NameZh)
	}
	if list[0].Faction != FactionHuman {
		t.Errorf("Super Earth 应归到超级地球，实际 %q", list[0].Faction)
	}
	if list[0].WikiURL != "" {
		t.Errorf("站点地址为空时不该拼出 wiki 地址，实际 %q", list[0].WikiURL)
	}
}

// TestParseIcons 校验图标按标题对应，没有图标的条目留空串（行内结果据此不带缩略图）。
func TestParseIcons(t *testing.T) {
	icons := map[string]string{"Bile Titan": "https://w/images/bile.png"}
	list := Parse([]wikigg.EnemyRow{
		{Title: "Bile Titan", Faction: "Terminids"},
		{Title: "Scavenger", Faction: "Terminids"},
	}, icons, "https://helldivers.wiki.gg")
	if list[0].IconURL != "https://w/images/bile.png" {
		t.Errorf("有图标的条目应带上地址，实际 %q", list[0].IconURL)
	}
	if list[1].IconURL != "" {
		t.Errorf("没有图标的条目应为空串，实际 %q", list[1].IconURL)
	}
}

// TestClassifyFaction 校验阵营归一与变体名的判定。
func TestClassifyFaction(t *testing.T) {
	cases := []struct {
		raw     string
		key     string
		variant string
	}{
		{"Terminids", FactionTerminid, ""},
		{"Spore Burst Strain", FactionTerminid, "Spore Burst Strain"},
		{"Automatons", FactionAutomaton, ""},
		{"Jet Brigade", FactionAutomaton, "Jet Brigade"},
		{"Illuminate", FactionIlluminate, ""},
		{"Appropriators", FactionIlluminate, "Appropriators"},
		{"Vote Snatchers", FactionIlluminate, "Vote Snatchers"},
		{"Super Earth", FactionHuman, ""},
		{"Terminids ", FactionTerminid, ""},
		// 站点加了新阵营时既不给 key 也不给它编变体名，展示层会照原文显示。
		{"Void Swarm", "", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		key, variant := classifyFaction(c.raw)
		if key != c.key || variant != c.variant {
			t.Errorf("classifyFaction(%q) = (%q, %q)，期望 (%q, %q)", c.raw, key, variant, c.key, c.variant)
		}
	}
}

// TestFactionTablesCoverWikiValues 校验两张阵营表自洽：每个 key 都有基础原文，
// 且基础原文能反查回同一个 key（否则「是不是变体」的判断会永远成立）。
func TestFactionTablesCoverWikiValues(t *testing.T) {
	for key, base := range baseFactionNames {
		if got := factionAliases[normalizeFaction(base)]; got != key {
			t.Errorf("基础原文 %q 应反查回 %q，实际 %q", base, key, got)
		}
	}
	for raw, key := range factionAliases {
		if _, ok := baseFactionNames[key]; !ok {
			t.Errorf("阵营 %q 不在基础表里（key=%q，原文=%q）", raw, key, raw)
		}
	}
}

// TestParseHealth 校验血量文本的解析：带千分位、带说明、空值都能处理。
func TestParseHealth(t *testing.T) {
	cases := map[string]int{
		"6,500":                       6500,
		"60":                          60,
		"18,001":                      18001,
		"2,000 HP + 400 Constitution": 2000,
		"":                            0,
		"未知":                          0,
	}
	for raw, want := range cases {
		if got := parseHealth(raw); got != want {
			t.Errorf("parseHealth(%q) = %d，期望 %d", raw, got, want)
		}
	}
}

// TestParseSize 校验体型解析：空串与非数字都按「上游没给」处理。
func TestParseSize(t *testing.T) {
	cases := map[string]int{"3": 3, "0": 0, "": SizeUnknown, "big": SizeUnknown}
	for raw, want := range cases {
		if got := parseSize(raw); got != want {
			t.Errorf("parseSize(%q) = %d，期望 %d", raw, got, want)
		}
	}
}

// TestSizeLabel 校验体型文案：0..3 有中文名，未收录与「没给」都返回空串（占位交给展示层）。
func TestSizeLabel(t *testing.T) {
	cases := map[int]string{0: "小型", 1: "中型", 2: "大型", 3: "巨型", SizeUnknown: "", 9: ""}
	for size, want := range cases {
		if got := SizeLabel(size); got != want {
			t.Errorf("SizeLabel(%d) = %q，期望 %q", size, got, want)
		}
	}
}

// TestCleanDamageRealSamples 用实测原文盯清洗结果：伤害图标换成中文类型、<br> 分条按「；」拼、
// 「值 +」用空格接住、标点不重复。
func TestCleanDamageRealSamples(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "酸液与近战（含 hr 与加号换行）",
			raw: `<span class="DamageIcons icon-outline">[[File:Damage Acid Icon.svg|20px|link=Damage|alt=Acid]]</span>&nbsp;` +
				`<span class="DamageIcons" >[[Damage#Damage_Types|<span style="color: chartreuse;">60 Bile Spew +<br>3/s Acid Burn (4s); </span>]]</span> <hr> ` +
				`<span class="DamageIcons icon-outline">[[File:Damage Melee Icon.svg|20px|link=Damage|alt=Melee]]</span>&nbsp;` +
				`<span class="DamageIcons" >[[Damage#Damage_Types|<span style="color: var(--wiki-content-text-color);">1000 Stomp + <br> 0 Area of Effect </span>]]</span>`,
			want: "【酸液】 60 Bile Spew + 3/s Acid Burn (4s)；【近战】 1000 Stomp + 0 Area of Effect",
		},
		{
			name: "多类型只用 br 分隔",
			raw: `<span>[[File:Damage Ballistic Icon.svg|20px|link=Damage|alt=Ballistic]]</span> [[Damage#Damage_Types|50 Gatling Gun]] <br> ` +
				`<span>[[File:Damage Explosion Icon.svg|20px|link=Damage|alt=Explosion]]</span> [[Damage#Damage_Types|<span style="color: orange;">400 Inner / 200 Outer Death Explosion </span>]]`,
			want: "【弹道】 50 Gatling Gun；【爆炸】 400 Inner / 200 Outer Death Explosion",
		},
		{
			name: "未收录的伤害类型原样保留，实体转义还原",
			raw:  `<span>[[File:Damage Weird Icon.svg|20px|alt=Weird]]</span> [[Damage#Damage_Types|5&nbsp;Strange]]`,
			want: "【Weird】 5 Strange",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CleanDamage(c.raw); got != c.want {
				t.Errorf("清洗结果不对\n实际：%s\n期望：%s", got, c.want)
			}
		})
	}
}

// TestCleanDamageTrimsAndToleratesEmpty 校验尾标点被剪掉、空输入与纯标签输入返回空串。
func TestCleanDamageTrimsAndToleratesEmpty(t *testing.T) {
	if got := CleanDamage("   "); got != "" {
		t.Errorf("空白输入应返回空串，实际 %q", got)
	}
	if got := CleanDamage(`<span class="x"></span> <hr> `); got != "" {
		t.Errorf("纯标签输入应返回空串，实际 %q", got)
	}
	if got := CleanDamage("10 Slash ; "); got != "10 Slash" {
		t.Errorf("块尾分号应被剪掉，实际 %q", got)
	}
}

// TestSearchMatchesChineseAndEnglish 校验检索口径：中文名、英文名、阵营原文都能命中，
// 完全相等优先于前缀、前缀优先于包含。
func TestSearchMatchesChineseAndEnglish(t *testing.T) {
	list := []Enemy{
		{Title: "Bile Titan", NameZh: "吐酸泰坦", FactionRaw: "Terminids"},
		{Title: "Bile Spewer", NameZh: "吐酸虫", FactionRaw: "Terminids"},
		{Title: "Hunter", NameZh: "追猎虫", FactionRaw: "Terminids"},
		{Title: "Jet Brigade Trooper", NameZh: "喷气旅装甲兵", FactionRaw: "Jet Brigade"},
	}

	cases := []struct {
		keyword string
		want    []string
	}{
		{"吐酸泰坦", []string{"Bile Titan"}},
		// 两条都是前缀命中（同分），保持目录顺序，不做字母排序
		{"bile", []string{"Bile Titan", "Bile Spewer"}},
		{"HUNTER", []string{"Hunter"}},
		{"Jet Brigade", []string{"Jet Brigade Trooper"}},
		{"不存在的敌人", nil},
	}
	for _, c := range cases {
		res := Search(list, Query{Keyword: c.keyword, Limit: 10})
		got := make([]string, 0, len(res.Enemies))
		for _, enemy := range res.Enemies {
			got = append(got, enemy.Title)
		}
		if len(got) != len(c.want) {
			t.Errorf("关键字 %q 命中 %d 条，期望 %d 条（%v）", c.keyword, len(got), len(c.want), got)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("关键字 %q 第 %d 条应为 %q，实际 %q", c.keyword, i+1, c.want[i], got[i])
			}
		}
		if res.Total < len(res.Enemies) {
			t.Errorf("Total 不能小于返回条数：%d < %d", res.Total, len(res.Enemies))
		}
	}
}

// TestSearchEmptyKeywordReturnsCatalogOrder 校验不打关键字时按输入顺序给默认列表，
// 并遵守 limit（默认 8 条）。
func TestSearchEmptyKeywordReturnsCatalogOrder(t *testing.T) {
	list := make([]Enemy, 0, 12)
	for i := 0; i < 12; i++ {
		list = append(list, Enemy{Title: string(rune('A' + i)), NameZh: string(rune('A' + i))})
	}

	res := Search(list, Query{})
	if len(res.Enemies) != defaultSearchLimit {
		t.Fatalf("默认应给 %d 条，实际 %d 条", defaultSearchLimit, len(res.Enemies))
	}
	if res.Total != len(list) {
		t.Errorf("Total 应是命中总数 %d，实际 %d", len(list), res.Total)
	}
	if res.Enemies[0].Title != "A" || res.Enemies[len(res.Enemies)-1].Title != "H" {
		t.Errorf("应保持输入顺序，实际首尾 %q / %q", res.Enemies[0].Title, res.Enemies[len(res.Enemies)-1].Title)
	}

	limited := Search(list, Query{Limit: 3})
	if len(limited.Enemies) != 3 {
		t.Errorf("应遵守 limit=3，实际 %d 条", len(limited.Enemies))
	}
}

// TestValidate 校验空列表算坏数据（群里看起来和「没搜到」一样，必须报出来）。
func TestValidate(t *testing.T) {
	if err := Validate(nil); err == nil {
		t.Error("空列表应报错")
	}
	if err := Validate([]Enemy{{Title: "Hulk"}}); err != nil {
		t.Errorf("非空列表不该报错，实际 %v", err)
	}
}

// TestWikiURL 校验 wiki 地址的拼法：空格转下划线、末尾斜杠去掉、站点地址为空时不拼。
func TestWikiURL(t *testing.T) {
	cases := map[string]string{
		"Bile Titan": "https://helldivers.wiki.gg/wiki/Bile_Titan",
		"Hulk":       "https://helldivers.wiki.gg/wiki/Hulk",
	}
	for title, want := range cases {
		got := wikiURL("https://helldivers.wiki.gg/", title)
		if got != want {
			t.Errorf("wikiURL(%q) = %q，期望 %q", title, got, want)
		}
	}
	if got := wikiURL("", "Hulk"); got != "" {
		t.Errorf("站点地址为空时应返回空串，实际 %q", got)
	}
	if got := wikiURL("https://helldivers.wiki.gg", "Rupture Spewer"); !strings.HasSuffix(got, "/wiki/Rupture_Spewer") {
		t.Errorf("带空格的标题应转成下划线，实际 %q", got)
	}
}
