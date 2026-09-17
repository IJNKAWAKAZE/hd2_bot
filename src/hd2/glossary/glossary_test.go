package glossary

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

// TestMain 在跑任何用例之前确认四张词表已在 init 阶段加载完毕。
// 若改回 sync.Once 懒加载，这里的表仍为 nil，整个包会直接失败，
// 从而防止「首次查询才加载、panic 后静默降级」的实现回归。
func TestMain(m *testing.M) {
	missing := make([]string, 0, 4)
	for name, table := range map[string]map[string]string{
		"planets":    planets,
		"sectors":    sectors,
		"enemies":    enemies,
		"stratagems": stratagems,
	} {
		if len(table) == 0 {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		fmt.Fprintln(os.Stderr, "词表应在包 init 阶段加载完成，实际未加载：", strings.Join(missing, "、"))
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// isPrefixHit 按 SearchPlanets 的规则判断命中项是否为前缀命中（英文名或中文名以关键字开头）。
func isPrefixHit(hit PlanetHit, keyword string) bool {
	kw := strings.ToLower(keyword)
	return strings.HasPrefix(hit.English, kw) || strings.HasPrefix(strings.ToLower(hit.Chinese), kw)
}

// TestPlanetKnown 校验已知星球名返回官方简中名。
func TestPlanetKnown(t *testing.T) {
	if got := Planet("Acamar IV"); got != "天园六IV" {
		t.Fatalf("Acamar IV 应译作 天园六IV，实际 %q", got)
	}
}

// TestPlanetUnknownFallsBackToOriginal 校验未收录的名字原样返回，而不是空字符串。
func TestPlanetUnknownFallsBackToOriginal(t *testing.T) {
	const name = "Nonexistent Planet XYZ"
	if got := Planet(name); got != name {
		t.Fatalf("未收录的名字应原样返回，实际 %q", got)
	}
}

// TestLookupIsCaseInsensitive 校验大小写不影响命中。
func TestLookupIsCaseInsensitive(t *testing.T) {
	if Planet("acamar iv") != Planet("ACAMAR IV") {
		t.Fatal("大小写不同的同一名字应命中同一条译名")
	}
}

// TestLookupTrimsWhitespace 校验四个单查函数都会忽略入参首尾空白。
func TestLookupTrimsWhitespace(t *testing.T) {
	if got := Planet("  Acamar IV  "); got != "天园六IV" {
		t.Fatalf("Planet 应忽略首尾空白，实际 %q", got)
	}
	if got := Sector("  Akira "); got != "阿基拉分区" {
		t.Fatalf("Sector 应忽略首尾空白，实际 %q", got)
	}
	if got := Enemy("\tHunter\n"); got != "追猎虫" {
		t.Fatalf("Enemy 应忽略首尾空白，实际 %q", got)
	}
	if got := Stratagem("  AC-8 Autocannon  "); got != "机炮" {
		t.Fatalf("Stratagem 应忽略首尾空白，实际 %q", got)
	}
}

// TestSearchMatchesChineseAndEnglish 校验按关键字检索行星时，中英文都能命中。
func TestSearchMatchesChineseAndEnglish(t *testing.T) {
	for _, kw := range []string{"acamar", "天园六"} {
		hits := SearchPlanets(kw, 10)
		if len(hits) == 0 {
			t.Fatalf("关键字 %q 应命中至少一条", kw)
		}
		if hits[0].English == "" || hits[0].Chinese == "" {
			t.Fatalf("命中项应同时带英文与中文名：%+v", hits[0])
		}
	}
}

// TestSearchRespectsLimit 校验结果条数上限生效（行内查询最多 10 条）：命中足够时恰好截到 limit 条。
func TestSearchRespectsLimit(t *testing.T) {
	if got := len(SearchPlanets("a", 3)); got != 3 {
		t.Fatalf("关键字 a 命中远超 3 条，结果应恰好 3 条，实际 %d", got)
	}
}

// TestSearchEmptyKeywordReturnsNothing 校验空关键字不返回全表（避免刷屏）。
func TestSearchEmptyKeywordReturnsNothing(t *testing.T) {
	if got := SearchPlanets("  ", 10); len(got) != 0 {
		t.Fatalf("空关键字应返回 0 条，实际 %d", len(got))
	}
}

// TestSectorAndEnemyAndStratagem 校验其它三类词表能查到精确译名，未收录时原样返回。
func TestSectorAndEnemyAndStratagem(t *testing.T) {
	if got := Sector("akira"); got != "阿基拉分区" {
		t.Fatalf("akira 应译作 阿基拉分区，实际 %q", got)
	}
	if got := Enemy("hunter"); got != "追猎虫" {
		t.Fatalf("hunter 应译作 追猎虫，实际 %q", got)
	}
	if got := Stratagem("ac-8 autocannon"); got != "机炮" {
		t.Fatalf("ac-8 autocannon 应译作 机炮，实际 %q", got)
	}
	if got := Sector("查无此分区"); got != "查无此分区" {
		t.Fatalf("未收录的分区应原样返回，实际 %q", got)
	}
}

// TestStratagemKnownAndCaseFolded 校验战术配备表大小写折叠，且未命中返回原文。
func TestStratagemKnownAndCaseFolded(t *testing.T) {
	if got := Stratagem("AC-8 Autocannon"); got != "机炮" {
		t.Fatalf("AC-8 Autocannon 应译作 机炮，实际 %q", got)
	}
	if got := Stratagem("No Such Stratagem"); got != "No Such Stratagem" {
		t.Fatalf("未收录的战术配备应原样返回，实际 %q", got)
	}
}

// TestSearchTrimsAndFoldsKeyword 校验关键字两侧空白被忽略、英文大小写折叠。
func TestSearchTrimsAndFoldsKeyword(t *testing.T) {
	hits := SearchPlanets("  ACAMAR  ", 10)
	if len(hits) != 1 {
		t.Fatalf("关键字 acamar 应命中 1 条，实际 %d", len(hits))
	}
	if hits[0].English != "acamar iv" || hits[0].Chinese != "天园六IV" {
		t.Fatalf("命中项应为 acamar iv / 天园六IV，实际 %+v", hits[0])
	}
}

// TestSearchFoldsChineseSideCase 校验中文名里的拉丁片段同样折叠大小写（表里是 天园六IV）。
func TestSearchFoldsChineseSideCase(t *testing.T) {
	for _, kw := range []string{"天园六iv", "天园六IV"} {
		hits := SearchPlanets(kw, 10)
		if len(hits) != 1 {
			t.Fatalf("关键字 %q 应命中 1 条，实际 %d：%+v", kw, len(hits), hits)
		}
		if hits[0].Chinese != "天园六IV" {
			t.Fatalf("关键字 %q 应命中 天园六IV，实际 %+v", kw, hits[0])
		}
	}
	// 中文名 UVP阿尔法 以拉丁片段开头，小写关键字也要能命中并视作前缀命中。
	hits := SearchPlanets("uvp阿尔法", 10)
	if len(hits) != 1 || hits[0].Chinese != "UVP阿尔法" {
		t.Fatalf("关键字 uvp阿尔法 应精确命中 UVP阿尔法，实际 %+v", hits)
	}
	if !isPrefixHit(hits[0], "uvp阿尔法") {
		t.Fatalf("中文名 UVP阿尔法 折叠后应以 uvp阿尔法 开头：%+v", hits[0])
	}
}

// TestSearchSkipsPlaceholderEntries 校验检索结果跳过 "(void)" / "(unused)" 占位条目，中文名不重复。
func TestSearchSkipsPlaceholderEntries(t *testing.T) {
	hits := SearchPlanets("uvp", 10)
	if len(hits) == 0 {
		t.Fatal("关键字 uvp 应命中至少一条")
	}
	seen := make(map[string]bool, len(hits))
	for _, h := range hits {
		if isPlaceholder(h.English) {
			t.Fatalf("占位条目不应出现在检索结果里：%+v", h)
		}
		if seen[h.Chinese] {
			t.Fatalf("同一中文名不应重复出现：%q（结果 %+v）", h.Chinese, hits)
		}
		seen[h.Chinese] = true
	}
}

// TestSearchChinesePrefixComesFirst 校验中文关键字的前缀命中同样优先（英文名排序会把它压后）。
func TestSearchChinesePrefixComesFirst(t *testing.T) {
	const kw = "希"
	hits := SearchPlanets(kw, 10)
	if len(hits) < 2 {
		t.Fatalf("关键字 %q 应命中多条，实际 %d", kw, len(hits))
	}
	if !isPrefixHit(hits[0], kw) || !strings.HasPrefix(hits[0].Chinese, kw) {
		t.Fatalf("首条应为中文以 %q 开头的前缀命中，实际 %+v", kw, hits[0])
	}
	seenNonPrefix := false
	for i, h := range hits {
		prefix := isPrefixHit(h, kw)
		if prefix && seenNonPrefix {
			t.Fatalf("第 %d 条前缀命中 %q 排在了非前缀命中之后", i, h.English)
		}
		if !prefix {
			seenNonPrefix = true
		}
	}
}

// TestSearchPrefixHitsComeFirst 校验前缀命中优先于「仅包含」命中，且组内按英文名升序。
// 关键字 ba 的非前缀命中（afoyay bay 等）按字母序排在前面，因此这条用例能真正约束优先级。
func TestSearchPrefixHitsComeFirst(t *testing.T) {
	const kw = "ba"
	hits := SearchPlanets(kw, 10)
	prefixCount := 0
	seenNonPrefix := false
	for i, h := range hits {
		if isPrefixHit(h, kw) {
			if seenNonPrefix {
				t.Fatalf("第 %d 条前缀命中 %q 排在了非前缀命中之后", i, h.English)
			}
			prefixCount++
			continue
		}
		seenNonPrefix = true
	}
	if prefixCount == 0 {
		t.Fatalf("关键字 %q 应至少有 1 条前缀命中，实际命中 %+v", kw, hits)
	}
	if hits[0].English != "baldrick prime" {
		t.Fatalf("前缀命中应按英文名升序，首条应为 baldrick prime，实际 %q", hits[0].English)
	}
	if prefixCount != 4 {
		t.Fatalf("关键字 %q 的前缀命中应全部进入 10 条结果（共 4 条），实际 %d", kw, prefixCount)
	}
}

// TestSearchDefaultsLimitWhenNonPositive 校验 limit <= 0 时按默认上限 10 条截断。
func TestSearchDefaultsLimitWhenNonPositive(t *testing.T) {
	if got := len(SearchPlanets("a", 0)); got != 10 {
		t.Fatalf("limit=0 应返回 10 条，实际 %d", got)
	}
	if got := len(SearchPlanets("a", -5)); got != 10 {
		t.Fatalf("limit=-5 应返回 10 条，实际 %d", got)
	}
}

// TestMustLoadTablePanicsWithChineseReason 校验词表读取/解析失败时 panic 且带中文原因（fail-fast）。
func TestMustLoadTablePanicsWithChineseReason(t *testing.T) {
	cases := []struct {
		label    string
		fsys     fstest.MapFS
		fileName string
		wantWord string
	}{
		{
			label:    "JSON 损坏",
			fsys:     fstest.MapFS{"data/broken.json": &fstest.MapFile{Data: []byte("{不是合法 JSON")}},
			fileName: "broken.json",
			wantWord: "解析",
		},
		{
			label:    "文件缺失",
			fsys:     fstest.MapFS{},
			fileName: "missing.json",
			wantWord: "无法读取",
		},
		{
			label:    "词表为空",
			fsys:     fstest.MapFS{"data/empty.json": &fstest.MapFile{Data: []byte("{}")}},
			fileName: "empty.json",
			wantWord: "没有任何词条",
		},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			defer func() {
				reason := recover()
				if reason == nil {
					t.Fatalf("%s 时应 panic", c.label)
				}
				msg, ok := reason.(string)
				if !ok {
					t.Fatalf("panic 值应为字符串说明，实际 %T：%v", reason, reason)
				}
				if !strings.HasPrefix(msg, "译名表加载失败") || !strings.Contains(msg, c.wantWord) {
					t.Fatalf("panic 说明应为中文且包含 %q，实际 %q", c.wantWord, msg)
				}
			}()
			mustLoadTable(c.fsys, c.fileName)
		})
	}
}

// TestTermsCoversAllTables 校验 Terms 导出四张表的全部词条：翻译前的专有名词预替换依赖它，
// 少一张表就会让一整类名词（例如分区、敌人）被模型音译。
func TestTermsCoversAllTables(t *testing.T) {
	terms := Terms()
	if len(terms) < 500 {
		t.Fatalf("词条数量明显偏少（%d 条），可能漏了某张表", len(terms))
	}
	if terms["acamar iv"] != "天园六IV" {
		t.Errorf("星球表未导出：acamar iv = %q", terms["acamar iv"])
	}
	if terms["hunter"] != "追猎虫" {
		t.Errorf("敌人表未导出：hunter = %q", terms["hunter"])
	}
	if _, ok := terms["查无此词"]; ok {
		t.Error("不存在的词不应出现在词表里")
	}
	// 返回的必须是副本：调用方改写不能影响内置词表
	terms["acamar iv"] = "被改坏了"
	if Planet("acamar iv") != "天园六IV" {
		t.Error("Terms 返回的应是副本，改动不应污染内置词表")
	}
}

// TestTermsHasNoDuplicateKeys 校验四张表合并后没有重键：一旦某个键同时出现在两张表里，
// Terms 只留其中一张的值，而留哪张取决于 map 遍历顺序——翻译层查到的译名会随机变化。
// 词条数对得上，才说明没有互相覆盖的键。
func TestTermsHasNoDuplicateKeys(t *testing.T) {
	want := 0
	for _, table := range []map[string]string{planets, sectors, enemies, stratagems} {
		for english, chinese := range table {
			if english != "" && chinese != "" {
				want++
			}
		}
	}
	if got := len(Terms()); got != want {
		t.Fatalf("合并后 %d 条，四张表的非空词条共 %d 条：存在互相覆盖的重键", got, want)
	}
}
