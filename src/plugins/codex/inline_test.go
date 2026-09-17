// inline_test.go 覆盖行内查询：前缀清单、关键字切分、候选拼装与出错时的兜底。
// 行内查询是「边打字边触发」的，出错必须回空结果而不是让客户端一直转圈，
// 因此这里除了正常路径，重点覆盖没有数据源、上游报错这些边界。
package codex

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/ijnkawakaze/telegram-bot-api"

	"hd2_bot/src/arsenal"
	"hd2_bot/src/bestiary"
	"hd2_bot/src/plugins/plugutil/plugtest"
)

// recordingAnswerer 记录行内应答：应答了哪个查询、回了什么。Err 非空时模拟发送失败。
type recordingAnswerer struct {
	queryIDs []string
	results  [][]interface{}
	err      error
}

// AnswerInline 记录一次应答。
func (a *recordingAnswerer) AnswerInline(queryID string, results []interface{}) error {
	a.queryIDs = append(a.queryIDs, queryID)
	a.results = append(a.results, results)
	return a.err
}

// fakeGear 是图鉴要的装备数据源：目录、装备图与缩略图都是预设的，Err 非空时表示取目录失败。
// 三个方法一起实现，指令（要装备图）与行内查询（要缩略图地址）才能共用同一个替身。
type fakeGear struct {
	catalog *arsenal.Catalog
	images  map[string][]byte
	thumbs  map[string]string
	err     error
}

// Catalog 返回预设目录。
func (g *fakeGear) Catalog(context.Context) (*arsenal.Catalog, error) {
	if g.err != nil {
		return nil, g.err
	}
	return g.catalog, nil
}

// Image 返回预设的装备图字节；没有配的 id 报错（线上就是「这张图下载失败」）。
func (g *fakeGear) Image(_ context.Context, item arsenal.Item) ([]byte, error) {
	raw, ok := g.images[item.ID]
	if !ok {
		return nil, errors.New("用例没有准备这张装备图")
	}
	return raw, nil
}

// ThumbURL 返回预设缩略图地址；没配就是空串（SVG 装备在线上就是这种情况）。
func (g *fakeGear) ThumbURL(_ context.Context, item arsenal.Item) string {
	return g.thumbs[item.ID]
}

// fakeBeasts 是图鉴要的敌人数据源：图鉴内容与加载时间都是预设的。
type fakeBeasts struct {
	list     []bestiary.Enemy
	loadedAt time.Time
	err      error
	icon     []byte
	iconErr  error
	// images 是「按地址下载」的预设结果（内置详情里的外观图与部位示意图走这条路）。
	images    map[string][]byte
	imageErr  error
	imageURLs []string
}

// List 返回预设图鉴。
func (b *fakeBeasts) List(context.Context) ([]bestiary.Enemy, error) {
	if b.err != nil {
		return nil, b.err
	}
	return b.list, nil
}

// LoadedAt 返回预设的图鉴加载时间（卡片上的「数据时间」）。
func (b *fakeBeasts) LoadedAt() time.Time { return b.loadedAt }

// Icon 返回预设的敌人图标字节；用例没配就报错（卡片会画成无图）。
func (b *fakeBeasts) Icon(context.Context, bestiary.Enemy) ([]byte, error) {
	if b.iconErr != nil {
		return nil, b.iconErr
	}
	if len(b.icon) == 0 {
		return nil, errors.New("这只没有图")
	}
	return b.icon, nil
}

// Image 返回预设的图片字节（按地址查）；用例没配就报错（卡片那部分不带图）。
func (b *fakeBeasts) Image(_ context.Context, url string) ([]byte, error) {
	b.imageURLs = append(b.imageURLs, url)
	if b.imageErr != nil {
		return nil, b.imageErr
	}
	if raw, ok := b.images[url]; ok {
		return raw, nil
	}
	return nil, errors.New("这个地址没有图")
}

// catalogWithKinds 造一份只含指定类别与件数的小目录（行内查询的截断要用几十条才看得出来，
// 手写 JSON 太长，这里按件数拼出来）。
func catalogWithKinds(t *testing.T, counts map[arsenal.Kind]int, order []arsenal.Kind) *arsenal.Catalog {
	t.Helper()
	entries := make([]string, 0, 64)
	for _, kind := range order {
		for i := 0; i < counts[kind]; i++ {
			entries = append(entries, fmt.Sprintf(
				`{"id": "%s-%02d", "productKind": %q, "nameZh": "%s%02d", "nameEn": "%s %02d"}`,
				kind, i, string(kind), arsenal.KindLabel(kind), i, arsenal.KindLabel(kind), i))
		}
	}
	raw := `{"items": [` + strings.Join(entries, ",") + `]}`
	catalog, err := arsenal.ParseCatalog([]byte(raw), nil)
	if err != nil {
		t.Fatalf("造目录失败：%v", err)
	}
	return catalog
}

// articleOf 把一条候选断言成文章型结果。
func articleOf(t *testing.T, result interface{}) tgbotapi.InlineQueryResultArticle {
	t.Helper()
	article, ok := result.(tgbotapi.InlineQueryResultArticle)
	if !ok {
		t.Fatalf("候选应是文章型结果，实际 %T", result)
	}
	return article
}

// textOf 取候选回填到输入框的指令。
func textOf(t *testing.T, article tgbotapi.InlineQueryResultArticle) string {
	t.Helper()
	content, ok := article.InputMessageContent.(tgbotapi.InputTextMessageContent)
	if !ok {
		t.Fatalf("候选应回填一条文本消息，实际 %T", article.InputMessageContent)
	}
	return content.Text
}

// ---- 前缀清单 ----

// TestInlineSpecsCoverEveryCodexKind 校验六个前缀各自负责什么：武器前缀一次查三类，
// 其余装备前缀各查一类，债券前缀查名单，敌人前缀标记成查图鉴而不是装备目录。
// 还要求每个前缀都能被自己的指令反查回来——六条指令不带参数时就是靠它找按钮的前缀。
func TestInlineSpecsCoverEveryCodexKind(t *testing.T) {
	byPrefix := make(map[string]InlineSpec, len(InlineSpecs))
	for _, spec := range InlineSpecs {
		if !strings.HasSuffix(spec.Prefix, "-") {
			t.Errorf("前缀应以短横线结尾（否则没法与关键字区分）：%q", spec.Prefix)
		}
		if spec.Label == "" {
			t.Errorf("前缀 %q 缺少中文名", spec.Prefix)
		}
		if spec.Command == "" {
			t.Errorf("前缀 %q 缺少对应指令（不带参数时找不到按钮该带哪个前缀）", spec.Prefix)
			continue
		}
		if matched, ok := inlineSpecForCommand(spec.Command); !ok || matched.Prefix != spec.Prefix {
			t.Errorf("指令 /%s 反查前缀应得 %q，实际 %q（ok=%v）", spec.Command, spec.Prefix, matched.Prefix, ok)
		}
		if _, dup := byPrefix[spec.Prefix]; dup {
			t.Errorf("前缀 %q 重复", spec.Prefix)
		}
		byPrefix[spec.Prefix] = spec
	}
	if _, ok := inlineSpecForCommand("不存在的指令"); ok {
		t.Error("没登记的指令不该反查到前缀")
	}
	want := []string{"武器-", "战备-", "护甲-", "手雷-", "债券-", "敌人-"}
	if got := len(byPrefix); got != len(want) {
		t.Fatalf("应有 %d 个前缀，实际 %d 个", len(want), got)
	}
	for _, prefix := range want {
		if _, ok := byPrefix[prefix]; !ok {
			t.Errorf("缺少前缀 %q", prefix)
		}
	}
	if spec := byPrefix["武器-"]; !equalStrings(kindsOf(spec.Kinds), kindsOf(arsenal.Weapons)) {
		t.Errorf("武器前缀应查主武器/副武器/支援武器，实际 %v", spec.Kinds)
	}
	for prefix, kind := range map[string]arsenal.Kind{
		"战备-": arsenal.KindStratagem, "护甲-": arsenal.KindArmor, "手雷-": arsenal.KindGrenade,
	} {
		spec := byPrefix[prefix]
		if len(spec.Kinds) != 1 || spec.Kinds[0] != kind || spec.Enemy || spec.Warbond {
			t.Errorf("%s 应只查 %s，实际 %v（enemy=%v warbond=%v）", prefix, kind, spec.Kinds, spec.Enemy, spec.Warbond)
		}
	}
	// 债券前缀既不是装备类别也不是图鉴：它查的是 24 本名单。
	if spec := byPrefix["债券-"]; !spec.Warbond || spec.Enemy || len(spec.Kinds) != 0 || spec.Command != "warbonds" {
		t.Errorf("债券前缀应查名单并对应 /warbonds，实际 %+v", spec)
	}
	if spec := byPrefix["敌人-"]; !spec.Enemy || spec.Warbond {
		t.Errorf("敌人前缀应标记成查图鉴，实际 %+v", spec)
	}
}

// kindsOf 把类别切片转成字符串切片，便于比较。
func kindsOf(kinds []arsenal.Kind) []string {
	out := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		out = append(out, string(kind))
	}
	return out
}

// TestInlineHandlersMatchSpecs 校验注册表与前缀清单一一对应且顺序稳定（注册顺序会进日志与用例断言）。
func TestInlineHandlersMatchSpecs(t *testing.T) {
	answerer := &recordingAnswerer{}
	registrations := InlineHandlers(&fakeGear{}, &fakeBeasts{}, answerer)
	if len(registrations) != len(InlineSpecs) {
		t.Fatalf("应生成 %d 个注册项，实际 %d 个", len(InlineSpecs), len(registrations))
	}
	for i, spec := range InlineSpecs {
		if registrations[i].Prefix != spec.Prefix {
			t.Errorf("第 %d 个注册项的前缀应为 %q，实际 %q", i+1, spec.Prefix, registrations[i].Prefix)
		}
		if registrations[i].Handler == nil {
			t.Errorf("前缀 %q 缺少回调", spec.Prefix)
		}
	}
}

// ---- 关键字 ----

// TestInlineKeyword 校验关键字切分：带前缀时只取前缀之后的部分，忘了带前缀时按整串处理。
func TestInlineKeyword(t *testing.T) {
	cases := []struct {
		query  string
		prefix string
		want   string
	}{
		{"武器-焦土", "武器-", "焦土"},
		{"武器-  焦土  ", "武器-", "焦土"},
		{"武器-", "武器-", ""},
		{"焦土", "武器-", "焦土"},
		{"护甲-侦察 者", "护甲-", "侦察 者"},
		{"任何输入", "", "任何输入"},
	}
	for _, c := range cases {
		if got := inlineKeyword(c.query, c.prefix); got != c.want {
			t.Errorf("inlineKeyword(%q, %q) = %q，期望 %q", c.query, c.prefix, got, c.want)
		}
	}
}

// ---- 检索合并 ----

// TestSearchInlineItemsEmptyKeywordGivesDefaults 校验只打前缀不打关键字时给默认列表，
// 并截断到候选上限（否则「护甲-a」这类单字会把上百件塞进客户端）。
func TestSearchInlineItemsEmptyKeywordGivesDefaults(t *testing.T) {
	catalog := catalogWithKinds(t, map[arsenal.Kind]int{arsenal.KindGrenade: 25}, []arsenal.Kind{arsenal.KindGrenade})
	items := searchInlineItems(catalog, []arsenal.Kind{arsenal.KindGrenade}, "")
	if len(items) != inlineResultLimit {
		t.Fatalf("默认列表应截断到 %d 条，实际 %d 条", inlineResultLimit, len(items))
	}
	if items[0].NameZh != "手雷00" {
		t.Errorf("默认列表应保持目录顺序，首条实际是 %q", items[0].NameZh)
	}
}

// TestSearchInlineItemsMergesKindsAndDedupes 校验多类合并：按类别顺序拼接、同一件只出现一次、合并后再截断。
func TestSearchInlineItemsMergesKindsAndDedupes(t *testing.T) {
	catalog := catalogWithKinds(t, map[arsenal.Kind]int{
		arsenal.KindPrimary:   12,
		arsenal.KindSecondary: 12,
	}, []arsenal.Kind{arsenal.KindPrimary, arsenal.KindSecondary})

	merged := searchInlineItems(catalog, []arsenal.Kind{arsenal.KindPrimary, arsenal.KindSecondary}, "")
	if len(merged) != inlineResultLimit {
		t.Fatalf("合并后应截断到 %d 条，实际 %d 条", inlineResultLimit, len(merged))
	}
	for _, item := range merged {
		if item.Kind != arsenal.KindPrimary {
			t.Fatalf("前 %d 条应全部来自第一个类别（主武器），实际混进了 %s", inlineResultLimit, item.Kind)
		}
	}

	// 同一类别写两遍不该出现重复条目。
	repeated := searchInlineItems(catalog, []arsenal.Kind{arsenal.KindSecondary, arsenal.KindSecondary}, "")
	if len(repeated) != inlineResultLimit {
		t.Fatalf("重复类别应去重后仍截断到 %d 条，实际 %d 条", inlineResultLimit, len(repeated))
	}
	seen := map[string]bool{}
	for _, item := range repeated {
		if seen[item.ID] {
			t.Fatalf("结果里出现了重复条目 %s", item.ID)
		}
		seen[item.ID] = true
	}
}

// ---- 候选拼装 ----

// TestGearInlineResults 校验装备候选：标题带型号、描述写英文名/类别/获取方式、缩略图与回填指令。
func TestGearInlineResults(t *testing.T) {
	catalog := mustCatalog(t)
	gear := &fakeGear{catalog: catalog, thumbs: map[string]string{"ar-2-coyote": "https://example.com/ar-2.png"}}
	item := fixtureItem(t, "ar-2-coyote")
	results := gearInlineResults(context.Background(), gear, catalog, []arsenal.Item{item})
	if len(results) != 1 {
		t.Fatalf("应有 1 条候选，实际 %d 条", len(results))
	}
	article := articleOf(t, results[0])
	if article.ID != inlineResultIDPrefix+"ar-2-coyote" {
		t.Errorf("候选 ID 应带前缀与装备 id，实际 %q", article.ID)
	}
	if article.Title != "野狼（AR-2）" {
		t.Errorf("标题应是中文名加型号，实际 %q", article.Title)
	}
	wantDesc := "AR-2 Coyote ｜ 主武器 ｜ 债券「沙漠魔影」 · 第 1 页 · 35 勋章"
	if article.Description != wantDesc {
		t.Errorf("描述应为 %q，实际 %q", wantDesc, article.Description)
	}
	if article.ThumbURL != "https://example.com/ar-2.png" {
		t.Errorf("缩略图地址错误：%q", article.ThumbURL)
	}
	// 行内结果的回填指令必须真的注册过（main 的接线用例会核对这一点）：装备类别决定回填哪条指令。
	if got := textOf(t, article); got != "/gun 野狼" {
		t.Errorf("武器候选应回填 /gun，实际 %q", got)
	}
}

// TestGearInlineResultsWithoutThumb 校验没有可用缩略图（SVG 装备）时只是没有图，条目照常给出。
func TestGearInlineResultsWithoutThumb(t *testing.T) {
	catalog := mustCatalog(t)
	gear := &fakeGear{catalog: catalog}
	results := gearInlineResults(context.Background(), gear, catalog, []arsenal.Item{fixtureItem(t, "a-m-12-mortar-sentry")})
	article := articleOf(t, results[0])
	if article.ThumbURL != "" {
		t.Errorf("没有图时缩略图地址应为空，实际 %q", article.ThumbURL)
	}
	if !strings.Contains(article.Description, "战备") {
		t.Errorf("描述里应写明类别：%q", article.Description)
	}
	if got := textOf(t, article); got != "/strat 迫击哨戒炮" {
		t.Errorf("战备候选应回填 /strat，实际 %q", got)
	}
}

// TestGearInlineResultsEmpty 校验没有命中时返回非 nil 的空切片（nil 会被序列化成 null，Telegram 会拒收）。
func TestGearInlineResultsEmpty(t *testing.T) {
	catalog := mustCatalog(t)
	results := gearInlineResults(context.Background(), &fakeGear{catalog: catalog}, catalog, nil)
	if results == nil {
		t.Fatal("空结果必须是非 nil 的空切片")
	}
	if len(results) != 0 {
		t.Fatalf("应回 0 条，实际 %d 条", len(results))
	}
}

// TestCommandForKind 校验类别到回填指令的映射：四条装备指令各管自己的类别。
func TestCommandForKind(t *testing.T) {
	cases := map[arsenal.Kind]string{
		arsenal.KindPrimary:     "gun",
		arsenal.KindSecondary:   "gun",
		arsenal.KindSupport:     "gun",
		arsenal.KindArmor:       "armor",
		arsenal.KindGrenade:     "grenade",
		arsenal.KindStratagem:   "strat",
		arsenal.Kind("mystery"): "gun",
	}
	for kind, want := range cases {
		if got := commandForKind(kind); got != want {
			t.Errorf("%s 应回填 /%s，实际 /%s", kind, want, got)
		}
	}
}

// TestInlineDescriptionSkipsRepeatedEnglishName 校验中文名与英文名相同时不重复写一遍。
func TestInlineDescriptionSkipsRepeatedEnglishName(t *testing.T) {
	got := inlineDescription(arsenal.Item{Kind: arsenal.KindGrenade, NameZh: "G-16 Impact", NameEn: "G-16 Impact"}, nil)
	if got != "手雷" {
		t.Fatalf("中英文名相同时描述里只该有类别，实际 %q", got)
	}
}

// ---- 债券候选 ----

// TestSearchInlineWarbonds 校验债券的筛选：空关键字给整份名单，关键字命中中文名、英文名或上游 id
// 都算数（大小写不敏感），没命中就是空名单。
func TestSearchInlineWarbonds(t *testing.T) {
	catalog := mustCatalog(t)
	cases := []struct {
		name    string
		keyword string
		want    []string
	}{
		{"空关键字给整份名单", "", []string{"mobilize", "dust-devils"}},
		{"按中文名查", "沙漠", []string{"dust-devils"}},
		{"按英文名查且忽略大小写", "DUST", []string{"dust-devils"}},
		{"按上游 id 查", "mobilize", []string{"mobilize"}},
		{"没命中就是空名单", "不存在的本", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			books := searchInlineWarbonds(catalog, c.keyword)
			if len(books) != len(c.want) {
				t.Fatalf("应命中 %d 本，实际 %d 本", len(c.want), len(books))
			}
			for i, id := range c.want {
				if books[i].ID != id {
					t.Errorf("第 %d 本应是 %s，实际 %s", i+1, id, books[i].ID)
				}
			}
		})
	}
}

// TestSearchInlineWarbondsCapsAtLimit 校验名单也受候选上限约束：24 本全塞进输入框，用户翻不完，
// 而 Telegram 的一次应答也只该给一小撮候选。
func TestSearchInlineWarbondsCapsAtLimit(t *testing.T) {
	entries := make([]string, 0, inlineResultLimit+2)
	for i := 0; i < inlineResultLimit+2; i++ {
		entries = append(entries, fmt.Sprintf(`{"id": "book-%02d", "nameZh": "第%02d本", "nameEn": "Book %02d"}`, i, i, i))
	}
	// 目录解析要求至少有一件装备，这里放一件不属任何债券的占位装备。
	raw := `{"warbonds": [` + strings.Join(entries, ",") +
		`], "items": [{"id": "placeholder-1", "productKind": "primary-weapon", "nameZh": "占位武器", "nameEn": "Placeholder"}]}`
	catalog, err := arsenal.ParseCatalog([]byte(raw), nil)
	if err != nil {
		t.Fatalf("造目录失败：%v", err)
	}
	// 空关键字（整份名单）与命中一大片的关键字（24 本全中）都得截到上限。
	for _, keyword := range []string{"", "本"} {
		if got := len(searchInlineWarbonds(catalog, keyword)); got != inlineResultLimit {
			t.Errorf("关键字 %q 最多给 %d 本，实际 %d 本", keyword, inlineResultLimit, got)
		}
	}
}

// TestWarbondInlineResults 校验债券候选：标题用中文名、描述写英文名/件数/价格，选中回填 /warbonds。
func TestWarbondInlineResults(t *testing.T) {
	catalog := mustCatalog(t)
	results := warbondInlineResults(catalog, "沙漠")
	if len(results) != 1 {
		t.Fatalf("关键字「沙漠」应命中 1 本，实际 %d 条", len(results))
	}
	article := articleOf(t, results[0])
	if article.ID != inlineResultIDPrefix+"warbond-dust-devils" {
		t.Errorf("候选 ID 应带前缀与债券 id，实际 %q", article.ID)
	}
	if article.Title != "沙漠魔影" {
		t.Errorf("标题应用中文名，实际 %q", article.Title)
	}
	if article.Description != "Dust Devils ｜ 1 件装备 ｜ 1,000 超级货币" {
		t.Errorf("描述错误：%q", article.Description)
	}
	if got := textOf(t, article); got != "/warbonds 沙漠魔影" {
		t.Errorf("债券候选应回填 /warbonds，实际 %q", got)
	}
	// 首发那本不单卖（superCredits 为 null）：描述里要写「不单卖」，而不是留个 0。
	free := articleOf(t, warbondInlineResults(catalog, "总动员")[0])
	if !strings.Contains(free.Description, "不单卖") {
		t.Errorf("不单卖的债券描述里应写明，实际 %q", free.Description)
	}
}

// TestEnemyInlineResults 校验敌人候选：标题用中文名、描述写英文名/阵营/体型/血量、回填 /enemy。
func TestEnemyInlineResults(t *testing.T) {
	enemies := []bestiary.Enemy{
		{Title: "Bile Titan", NameZh: "胆汁泰坦", Faction: bestiary.FactionTerminid, FactionRaw: "Terminids", Size: 3, Health: 6500, IconURL: "https://example.com/bile.png"},
		{Title: "Scavenger", Faction: bestiary.FactionTerminid, FactionRaw: "Terminids", Size: bestiary.SizeUnknown, Health: 60},
	}
	results := enemyInlineResults(enemies)
	if len(results) != 2 {
		t.Fatalf("应有 2 条候选，实际 %d 条", len(results))
	}
	first := articleOf(t, results[0])
	if first.ID != inlineResultIDPrefix+"Bile_Titan" {
		t.Errorf("候选 ID 应把标题里的空格换成下划线，实际 %q", first.ID)
	}
	if first.Title != "胆汁泰坦" {
		t.Errorf("标题应用中文名，实际 %q", first.Title)
	}
	if first.Description != "Bile Titan ｜ 终结族 ｜ 巨型 ｜ 血量 6,500" {
		t.Errorf("描述错误：%q", first.Description)
	}
	if first.ThumbURL != "https://example.com/bile.png" {
		t.Errorf("缩略图应直接用图鉴里的图标地址，实际 %q", first.ThumbURL)
	}
	if got := textOf(t, first); got != "/enemy 胆汁泰坦" {
		t.Errorf("敌人候选应回填 /enemy，实际 %q", got)
	}
	second := articleOf(t, results[1])
	if second.Description != "Scavenger ｜ 终结族 ｜ 血量 60" {
		t.Errorf("缺体型时描述里不该出现体型一栏：%q", second.Description)
	}
}

// TestInlineIDPart 校验结果 ID 片段：只折叠空白，不引入哈希（否则同一只敌人的 ID 不可复现）。
func TestInlineIDPart(t *testing.T) {
	if got := inlineIDPart("Ground All-Terrain Extraction Rig (GATER)"); got != "Ground_All-Terrain_Extraction_Rig_(GATER)" {
		t.Errorf("空白应折成下划线，实际 %q", got)
	}
	if got := inlineIDPart("  Bile   Titan  "); got != "Bile_Titan" {
		t.Errorf("连续空白应折成一个下划线，实际 %q", got)
	}
}

// ---- 端到端应答 ----

// TestAnswerInlineGearPrefix 校验装备前缀的完整流程：切关键字 → 检索 → 应答（回填对应指令）。
func TestAnswerInlineGearPrefix(t *testing.T) {
	catalog := mustCatalog(t)
	answerer := &recordingAnswerer{}
	gear := &fakeGear{catalog: catalog, thumbs: map[string]string{"a-35-recon": "https://example.com/a-35.png"}}
	handler := InlineHandler(gear, nil, answerer, InlineSpec{Prefix: "护甲-", Label: "护甲", Kinds: []arsenal.Kind{arsenal.KindArmor}})

	if err := handler(tgbotapi.InlineQuery{ID: "q-1", Query: "护甲-侦察"}); err != nil {
		t.Fatalf("应答失败：%v", err)
	}
	if len(answerer.queryIDs) != 1 || answerer.queryIDs[0] != "q-1" {
		t.Fatalf("应应答查询 q-1，实际 %v", answerer.queryIDs)
	}
	if len(answerer.results[0]) != 1 {
		t.Fatalf("关键字「侦察」应命中 1 件护甲，实际 %d 条", len(answerer.results[0]))
	}
	article := articleOf(t, answerer.results[0][0])
	if article.Title != "A-35“侦察者”（A-35）" {
		t.Errorf("标题错误：%q", article.Title)
	}
	if got := textOf(t, article); got != "/armor A-35“侦察者”" {
		t.Errorf("护甲候选应回填 /armor，实际 %q", got)
	}
}

// TestAnswerInlineEnemyPrefix 校验敌人前缀走的是图鉴而不是装备目录。
func TestAnswerInlineEnemyPrefix(t *testing.T) {
	answerer := &recordingAnswerer{}
	beasts := &fakeBeasts{list: []bestiary.Enemy{{Title: "Scavenger", NameZh: "食腐虫", Faction: bestiary.FactionTerminid, FactionRaw: "Terminids", Size: 0, Health: 60}}}
	handler := InlineHandler(nil, beasts, answerer, InlineSpec{Prefix: "敌人-", Label: "敌人", Enemy: true})

	if err := handler(tgbotapi.InlineQuery{ID: "q-2", Query: "敌人-食腐"}); err != nil {
		t.Fatalf("应答失败：%v", err)
	}
	if len(answerer.results[0]) != 1 {
		t.Fatalf("应命中 1 只敌人，实际 %d 条", len(answerer.results[0]))
	}
	article := articleOf(t, answerer.results[0][0])
	if article.Title != "食腐虫" {
		t.Errorf("标题应用中文名，实际 %q", article.Title)
	}
	if got := textOf(t, article); got != "/enemy 食腐虫" {
		t.Errorf("敌人候选应回填 /enemy，实际 %q", got)
	}
}

// TestAnswerInlineWarbondPrefix 校验债券前缀走名单而不是装备目录。
func TestAnswerInlineWarbondPrefix(t *testing.T) {
	answerer := &recordingAnswerer{}
	spec := InlineSpec{Prefix: "债券-", Label: "债券", Command: "warbonds", Warbond: true}
	handler := InlineHandler(&fakeGear{catalog: mustCatalog(t)}, nil, answerer, spec)

	if err := handler(tgbotapi.InlineQuery{ID: "q-3", Query: "债券-"}); err != nil {
		t.Fatalf("应答失败：%v", err)
	}
	if len(answerer.results[0]) != 2 {
		t.Fatalf("只打前缀应给整份名单（2 本），实际 %d 条", len(answerer.results[0]))
	}
	article := articleOf(t, answerer.results[0][0])
	if article.Title != "绝地潜兵总动员！" {
		t.Errorf("标题应用中文名，实际 %q", article.Title)
	}
	if got := textOf(t, article); got != "/warbonds 绝地潜兵总动员！" {
		t.Errorf("债券候选应回填 /warbonds，实际 %q", got)
	}
}

// TestAnswerInlineWithoutDataSource 校验没有数据源时回空结果（客户端显示「无结果」）而不是卡住。
func TestAnswerInlineWithoutDataSource(t *testing.T) {
	cases := []struct {
		name string
		spec InlineSpec
	}{
		{"装备前缀没有装备数据层", InlineSpec{Prefix: "武器-", Label: "武器", Kinds: []arsenal.Kind{arsenal.KindPrimary}}},
		{"敌人前缀没有图鉴数据层", InlineSpec{Prefix: "敌人-", Label: "敌人", Enemy: true}},
		{"债券前缀没有装备数据层", InlineSpec{Prefix: "债券-", Label: "债券", Command: "warbonds", Warbond: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			logs := plugtest.CaptureLog(t)
			answerer := &recordingAnswerer{}
			handler := InlineHandler(nil, nil, answerer, c.spec)
			if err := handler(tgbotapi.InlineQuery{ID: "q-3", Query: c.spec.Prefix + "随便"}); err != nil {
				t.Fatalf("没有数据源时不该上抛错误：%v", err)
			}
			if len(answerer.results) != 1 {
				t.Fatalf("应应答一次，实际 %d 次", len(answerer.results))
			}
			if answerer.results[0] == nil || len(answerer.results[0]) != 0 {
				t.Fatalf("应回非 nil 的空结果，实际 %#v", answerer.results[0])
			}
			// 日志里要留下线索：线上没人能看见客户端的「无结果」，只能靠日志分辨「没数据源」还是「上游挂了」。
			if !strings.Contains(logs.String(), c.spec.Prefix) {
				t.Errorf("日志应带上出问题前缀，实际：%s", logs.String())
			}
		})
	}
}

// TestAnswerInlineUpstreamFailure 校验上游报错时回空结果并记日志（行内查询不把错误抛给用户）。
func TestAnswerInlineUpstreamFailure(t *testing.T) {
	logs := plugtest.CaptureLog(t)
	answerer := &recordingAnswerer{}
	gear := &fakeGear{err: errors.New("上游 502")}
	handler := InlineHandler(gear, &fakeBeasts{}, answerer, InlineSpec{Prefix: "手雷-", Label: "手雷", Kinds: []arsenal.Kind{arsenal.KindGrenade}})

	if err := handler(tgbotapi.InlineQuery{ID: "q-4", Query: "手雷-"}); err != nil {
		t.Fatalf("上游失败时不该上抛错误：%v", err)
	}
	if len(answerer.results) != 1 || len(answerer.results[0]) != 0 {
		t.Fatalf("应回空结果，实际 %#v", answerer.results)
	}
	if !strings.Contains(logs.String(), "上游 502") {
		t.Errorf("日志应带上失败原因，实际：%s", logs.String())
	}

	// 敌人那边同理。
	answerer = &recordingAnswerer{}
	handler = InlineHandler(nil, &fakeBeasts{err: errors.New("wiki 超时")}, answerer, InlineSpec{Prefix: "敌人-", Label: "敌人", Enemy: true})
	if err := handler(tgbotapi.InlineQuery{ID: "q-5", Query: "敌人-"}); err != nil {
		t.Fatalf("上游失败时不该上抛错误：%v", err)
	}
	if len(answerer.results[0]) != 0 {
		t.Fatalf("应回空结果，实际 %#v", answerer.results[0])
	}
}

// TestAnswerInlinePropagatesSendFailure 校验只有「应答发不出去」才上抛错误，且带上查询 id 便于排查。
func TestAnswerInlinePropagatesSendFailure(t *testing.T) {
	catalog := mustCatalog(t)
	answerer := &recordingAnswerer{err: errors.New("网络断了")}
	handler := InlineHandler(&fakeGear{catalog: catalog}, nil, answerer, InlineSpec{Prefix: "手雷-", Label: "手雷", Kinds: []arsenal.Kind{arsenal.KindGrenade}})

	err := handler(tgbotapi.InlineQuery{ID: "q-6", Query: "手雷-"})
	if err == nil {
		t.Fatal("应答失败应上抛错误")
	}
	if !strings.Contains(err.Error(), "q-6") || !strings.Contains(err.Error(), "网络断了") {
		t.Fatalf("错误信息应带上查询 id 与原因，实际：%v", err)
	}
}
