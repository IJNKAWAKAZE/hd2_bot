package planets

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	tgbotapi "github.com/ijnkawakaze/telegram-bot-api"

	"hd2_bot/src/core/bot"
	"hd2_bot/src/hd2"
	"hd2_bot/src/hd2/glossary"
	"hd2_bot/src/plugins/plugutil"
)

// 编译期断言：真实机器人的 AnswerInline 满足行内查询需要的能力。
var _ InlineAnswerer = (*bot.Bot)(nil)

// fakeAnswerer 记录每一次行内应答，用来验证回调确实走 AnswerInline，
// 而不是另开一条发送路径（行内结果的缓存时间由 bot.AnswerInline 固定为 0，绕开它就绕开了这个约定）。
type fakeAnswerer struct {
	ids     []string
	results [][]interface{}
	err     error
}

// AnswerInline 记录一次应答；err 非空时模拟 Telegram 侧失败。
func (f *fakeAnswerer) AnswerInline(queryID string, results []interface{}) error {
	f.ids = append(f.ids, queryID)
	f.results = append(f.results, results)
	return f.err
}

// inlineFixturePlanets 是行内查询用例的固定星球列表：两颗中文名与分区名都有译名的星球。
func inlineFixturePlanets() []hd2.Planet {
	return []hd2.Planet{
		{Index: 22, Name: "Acamar IV", Sector: "Valdis"},
		{Index: 216, Name: "Peacock", Sector: "Jin Xi"},
	}
}

// inlineService 构造只带星球列表的假服务（行内查询只用得到 Planets）。
func inlineService(list []hd2.Planet) *fakeService {
	return &fakeService{planets: hd2.Result[[]hd2.Planet]{Value: list}}
}

// runInline 执行一次行内查询并返回应答的结果列表，同时校验应答走的是 AnswerInline 且带上同一个查询 ID。
func runInline(t *testing.T, svc PlanetsService, a *fakeAnswerer, prefix, query string) []interface{} {
	t.Helper()
	if err := InlineHandler(svc, a, prefix)(tgbotapi.InlineQuery{ID: "query-1", Query: query}); err != nil {
		t.Fatalf("行内查询 %q 处理失败：%v", query, err)
	}
	if len(a.ids) != 1 || a.ids[0] != "query-1" {
		t.Fatalf("应通过 AnswerInline 应答一次并带上查询 ID，实际 %v", a.ids)
	}
	return a.results[0]
}

// articleAt 取出第 i 条结果并断言它是文章式结果。
func articleAt(t *testing.T, results []interface{}, i int) tgbotapi.InlineQueryResultArticle {
	t.Helper()
	if i >= len(results) {
		t.Fatalf("结果条数不足：要第 %d 条，只有 %d 条", i+1, len(results))
	}
	article, ok := results[i].(tgbotapi.InlineQueryResultArticle)
	if !ok {
		t.Fatalf("第 %d 条结果应为 InlineQueryResultArticle，实际 %T", i+1, results[i])
	}
	return article
}

// inputTextOf 取出结果被选中后回填的指令文本。
func inputTextOf(t *testing.T, article tgbotapi.InlineQueryResultArticle) string {
	t.Helper()
	content, ok := article.InputMessageContent.(tgbotapi.InputTextMessageContent)
	if !ok {
		t.Fatalf("结果应带 InputTextMessageContent，实际 %T", article.InputMessageContent)
	}
	return content.Text
}

// sameResults 比较两次应答是否逐条一致（ID、标题、回填文本）。
func sameResults(a, b []interface{}) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		articleA, okA := a[i].(tgbotapi.InlineQueryResultArticle)
		articleB, okB := b[i].(tgbotapi.InlineQueryResultArticle)
		if !okA || !okB {
			return false
		}
		textA, okA := articleA.InputMessageContent.(tgbotapi.InputTextMessageContent)
		textB, okB := articleB.InputMessageContent.(tgbotapi.InputTextMessageContent)
		if !okA || !okB {
			return false
		}
		if articleA.ID != articleB.ID || articleA.Title != articleB.Title || textA.Text != textB.Text {
			return false
		}
	}
	return true
}

// planetsFromGlossary 按关键字从译名表里取 n 颗星球，构造「上游返回这些星球」的假列表。
// 行内结果只可能命中译名表里有的名字，所以造数据必须用真实译名，不能自己编。
func planetsFromGlossary(t *testing.T, keyword string, n int) []hd2.Planet {
	t.Helper()
	hits := glossary.SearchPlanets(keyword, n)
	if len(hits) < n {
		t.Fatalf("用例前提不成立：关键字 %q 只命中 %d 颗星球，需要 %d 颗", keyword, len(hits), n)
	}
	list := make([]hd2.Planet, 0, n)
	for i, hit := range hits {
		list = append(list, hd2.Planet{Index: i + 1, Name: hit.English, Sector: "Akira"})
	}
	return list
}

// TestInlineHandlerMatchesChineseAndEnglishKeywords 校验中英文关键字都能命中同一颗星球，
// 标题用中文译名、描述带英文名与分区中文名、选中后回填 /planet <中文名>。
func TestInlineHandlerMatchesChineseAndEnglishKeywords(t *testing.T) {
	cases := []struct {
		name  string
		query string
	}{
		{name: "中文关键字", query: "星球-天园"},
		{name: "英文关键字", query: "星球-acamar"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			results := runInline(t, inlineService(inlineFixturePlanets()), &fakeAnswerer{}, "星球-", c.query)
			if len(results) != 1 {
				t.Fatalf("关键字 %q 应命中 1 条结果，实际 %d 条", c.query, len(results))
			}
			article := articleAt(t, results, 0)
			if article.Type != "article" {
				t.Errorf("结果类型应为 article，实际 %q", article.Type)
			}
			if article.Title != "天园六IV" {
				t.Errorf("标题应为中文译名「天园六IV」，实际 %q", article.Title)
			}
			if article.Description != "Acamar IV ｜ 瓦尔迪斯分区" {
				t.Errorf("描述应为「英文名 ｜ 分区中文名」，实际 %q", article.Description)
			}
			if got := inputTextOf(t, article); got != "/planet 天园六IV" {
				t.Errorf("回填文本应为 /planet 天园六IV，实际 %q", got)
			}
			if article.ThumbURL != "" {
				t.Errorf("没有公网图床，不应设置缩略图，实际 %q", article.ThumbURL)
			}
			if article.ID != "planet-22" {
				t.Errorf("结果 ID 应稳定可复现（planet-编号），实际 %q", article.ID)
			}
		})
	}
}

// TestInlineHandlerRespectsTenLimit 校验行内结果最多 10 条。
func TestInlineHandlerRespectsTenLimit(t *testing.T) {
	// 关键字 a 在译名表里命中上百颗星球，这里先取 20 颗做成假的上游列表，保证候选足够多。
	list := planetsFromGlossary(t, "a", 20)

	results := runInline(t, inlineService(list), &fakeAnswerer{}, "星球-", "星球-a")
	// 上限写死成数字而不是引用实现里的常量：常量被改大时这条用例必须失败。
	if len(results) != 10 {
		t.Fatalf("结果应被截断到 10 条，实际 %d 条", len(results))
	}
}

// TestInlineHandlerEmptyKeywordAnswersDefaults 校验只打了前缀（或只有空格）时给默认候选，
// 而不是一片空白（用户 2026-09-17 要求：与图鉴前缀一样，点开就能看到一些东西）。
// 默认候选的顺序必须与卡片热点一致：都按「有事件 → 有进攻行动 → 在线士兵降序 → 编号」排。
func TestInlineHandlerEmptyKeywordAnswersDefaults(t *testing.T) {
	list := []hd2.Planet{
		{Index: 22, Name: "Acamar IV", Sector: "Valdis", Statistics: hd2.PlanetStats{PlayerCount: 10}},
		{Index: 268, Name: "Luxuriant", Sector: "Jin Xi", Statistics: hd2.PlanetStats{PlayerCount: 20},
			Event: &hd2.PlanetEvent{EventType: 1, Faction: "Terminids", Health: 1, MaxHealth: 2}},
		{Index: 216, Name: "Peacock", Sector: "Jin Xi", Statistics: hd2.PlanetStats{PlayerCount: 30},
			Attacking: []int{268}},
	}
	for _, query := range []string{"星球-", "星球- ", "   "} {
		t.Run(query, func(t *testing.T) {
			svc := inlineService(list)
			results := runInline(t, svc, &fakeAnswerer{}, "星球-", query)
			if len(results) != 3 {
				t.Fatalf("空关键字应给出全部 3 颗星球的默认候选，实际 %d 条", len(results))
			}
			if svc.planetsGot != 1 {
				t.Errorf("默认候选取一次星球列表，实际调用 %d 次", svc.planetsGot)
			}
			// 有事件的排最前，其余按在线士兵降序。
			wantOrder := []string{"/planet 富源", "/planet 孔雀十一", "/planet 天园六IV"}
			for i, want := range wantOrder {
				if got := inputTextOf(t, articleAt(t, results, i)); got != want {
					t.Errorf("第 %d 条默认候选应是 %q，实际 %q", i+1, want, got)
				}
			}
		})
	}
}

// TestInlineHandlerEmptyKeywordCapsAtTen 校验默认候选同样受条数上限约束：
// 点开输入框先甩出 273 颗星球，客户端那边只会让人觉得卡。
func TestInlineHandlerEmptyKeywordCapsAtTen(t *testing.T) {
	list := make([]hd2.Planet, 0, 12)
	for i := 0; i < 12; i++ {
		list = append(list, hd2.Planet{Index: i, Name: fmt.Sprintf("Planet %02d", i)})
	}
	results := runInline(t, inlineService(list), &fakeAnswerer{}, "星球-", "星球-")
	// 上限写死成数字而不是引用实现里的常量：常量被改大时这条用例必须失败。
	if len(results) != 10 {
		t.Fatalf("默认候选应截断到 10 条，实际 %d 条", len(results))
	}
}

// TestInlineHandlerEmptyResultsAreEmptySlice 校验没有命中时也照样应答，且结果是空切片而不是 nil
// （nil 会被序列化成 null，而 Telegram 要求 results 是数组）。
func TestInlineHandlerEmptyResultsAreEmptySlice(t *testing.T) {
	results := runInline(t, inlineService(inlineFixturePlanets()), &fakeAnswerer{}, "星球-", "星球-不存在的星球")
	if results == nil {
		t.Fatal("空结果也必须是空切片，不能是 nil")
	}
	if len(results) != 0 {
		t.Fatalf("查不到时应回空结果，实际 %d 条", len(results))
	}
}

// TestInlineHandlerStripsPrefix 校验前缀被去掉：带前缀与不带前缀的查询给出完全一样的结果。
func TestInlineHandlerStripsPrefix(t *testing.T) {
	svc := inlineService(inlineFixturePlanets())
	withPrefix := runInline(t, svc, &fakeAnswerer{}, "星球-", "星球-acamar")
	if len(withPrefix) != 1 {
		t.Fatalf("带前缀的查询应命中 1 条结果，实际 %d 条（前缀可能没被去掉）", len(withPrefix))
	}
	// 没有前缀时按整串当关键字：正常不会走到这里（bot 只把带前缀的查询交给我们），属防御分支。
	without := runInline(t, svc, &fakeAnswerer{}, "星球-", "acamar")
	if !sameResults(withPrefix, without) {
		t.Fatalf("带前缀与不带前缀的结果应一致，实际 %v 与 %v", withPrefix, without)
	}
	// 前缀配置为空时同样按整串搜索，不能因为切前缀把关键字吃掉。
	emptyPrefix := runInline(t, svc, &fakeAnswerer{}, "", "acamar")
	if !sameResults(withPrefix, emptyPrefix) {
		t.Fatalf("空前缀配置下结果应一致，实际 %v", emptyPrefix)
	}
}

// TestInlineHandlerUpstreamErrorAnswersNoResults 校验上游出错时回空结果、不上抛错误（不刷日志）。
func TestInlineHandlerUpstreamErrorAnswersNoResults(t *testing.T) {
	svc := &fakeService{planetsErr: errors.New("上游限流")}
	results := runInline(t, svc, &fakeAnswerer{}, "星球-", "星球-天园")
	if results == nil || len(results) != 0 {
		t.Fatalf("上游出错时应回空结果，实际 %v", results)
	}
}

// TestInlineHandlerEmptyPlanetListAnswersNoResults 校验上游返回空列表时回空结果、不报错。
func TestInlineHandlerEmptyPlanetListAnswersNoResults(t *testing.T) {
	results := runInline(t, inlineService(nil), &fakeAnswerer{}, "星球-", "星球-天园")
	if results == nil || len(results) != 0 {
		t.Fatalf("上游返回空列表时应回空结果，实际 %v", results)
	}
}

// TestInlineHandlerSkipsPlanetsMissingUpstream 校验译名表里有、上游当前没有的星球不进结果：
// 结果被选中后会回填 /planet <名字>，而 /planet 是按上游列表解析的，
// 给出这种结果只会让人点完得到「找不到」。
func TestInlineHandlerSkipsPlanetsMissingUpstream(t *testing.T) {
	// 「火星」在译名表里，但上游实测的 273 颗星球里没有它。
	results := runInline(t, inlineService(inlineFixturePlanets()), &fakeAnswerer{}, "星球-", "星球-火星")
	if len(results) != 0 {
		t.Fatalf("上游没有的星球不应出现在结果里，实际命中 %d 条", len(results))
	}

	// 反证：把火星放进上游列表，同一个关键字就能命中。
	list := append(inlineFixturePlanets(), hd2.Planet{Index: 999, Name: "Mars", Sector: "Sol"})
	hit := runInline(t, inlineService(list), &fakeAnswerer{}, "星球-", "星球-火星")
	if len(hit) != 1 {
		t.Fatalf("上游有火星时应命中 1 条，实际 %d 条", len(hit))
	}
	article := articleAt(t, hit, 0)
	if article.Title != "火星" || inputTextOf(t, article) != "/planet 火星" {
		t.Errorf("火星结果的标题与回填文本不正确：%q / %q", article.Title, inputTextOf(t, article))
	}
}

// TestInlineHandlerResultIDsAreUniqueAndShort 校验结果 ID 唯一、在 1-64 字节内，且同一个查询两次结果一致（可复现）。
func TestInlineHandlerResultIDsAreUniqueAndShort(t *testing.T) {
	list := planetsFromGlossary(t, "a", 20)
	first := runInline(t, inlineService(list), &fakeAnswerer{}, "星球-", "星球-a")
	second := runInline(t, inlineService(list), &fakeAnswerer{}, "星球-", "星球-a")

	seen := make(map[string]bool, len(first))
	for i := range first {
		id := articleAt(t, first, i).ID
		if len(id) == 0 || len(id) > 64 {
			t.Errorf("结果 ID 应在 1-64 字节之间，实际 %q（%d 字节）", id, len(id))
		}
		if seen[id] {
			t.Errorf("结果 ID 重复：%q", id)
		}
		seen[id] = true
	}
	if !sameResults(first, second) {
		t.Fatalf("同一个查询两次结果应完全一致（ID 不能是随机数）：%v 与 %v", first, second)
	}
}

// TestInlineHandlerPropagatesAnswerError 校验应答失败时把错误交给 bot 层（只记一条日志），不静默吞掉。
func TestInlineHandlerPropagatesAnswerError(t *testing.T) {
	want := errors.New("查询已过期")
	answerer := &fakeAnswerer{err: want}
	err := InlineHandler(inlineService(inlineFixturePlanets()), answerer, "星球-")(
		tgbotapi.InlineQuery{ID: "query-1", Query: "星球-天园"})
	if !errors.Is(err, want) {
		t.Fatalf("应答失败应上抛原始错误，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "query-1") {
		t.Errorf("错误信息应带上查询 ID 便于排查，实际 %v", err)
	}
}

// TestInlineResultFallsBackToEnglishTitle 校验译名表里缺中文名时退回上游英文名，不给出空标题。
// 当前译名表每条都有译名，这里走的是防御分支，直接调 inlineResultFor 才能覆盖。
func TestInlineResultFallsBackToEnglishTitle(t *testing.T) {
	article := inlineResultFor(hd2.Planet{Index: 7, Name: "Foo", Sector: "Akira"}, glossary.PlanetHit{English: "foo"})
	if article.Title != "Foo" {
		t.Errorf("缺译名时标题应退回英文名，实际 %q", article.Title)
	}
	if article.Description != "Foo ｜ 阿基拉分区" {
		t.Errorf("描述应为「英文名 ｜ 分区中文名」，实际 %q", article.Description)
	}
	if got := inputTextOf(t, article); got != "/planet Foo" {
		t.Errorf("回填文本应为 /planet Foo，实际 %q", got)
	}
}

// TestInlineHandlerAnswersEachQueryWithItsOwnID 校验并发查询各按自己的 ID 应答。
// Telegram 的 inline_query_id 用过就失效，一次应答只能对应当前这次查询；
// 实现里若把「当前 ID」存进包级变量（先写、取完数再读），并发下就会把 A 的应答发给 B，
// 应答被丢弃的表现是「搜出来没反应」。串行调用区分不了「本次」与「上一次」，所以这里并发跑。
func TestInlineHandlerAnswersEachQueryWithItsOwnID(t *testing.T) {
	const n = 16
	svc := &blockingService{fakeService: fakeService{planets: hd2.Result[[]hd2.Planet]{Value: inlineFixturePlanets()}}}
	// 取数留出一段等待窗口，让各协程的「写 ID」与「读 ID」交错开。
	svc.delay = 5 * time.Millisecond

	answerers := make([]*fakeAnswerer, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		answerers[i] = &fakeAnswerer{}
		handler := InlineHandler(svc, answerers[i], "星球-")
		id := fmt.Sprintf("query-%d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := handler(tgbotapi.InlineQuery{ID: id, Query: "星球-acamar"}); err != nil {
				t.Errorf("行内查询 %s 处理失败：%v", id, err)
			}
		}()
	}
	wg.Wait()

	for i, a := range answerers {
		want := fmt.Sprintf("query-%d", i)
		if len(a.ids) != 1 || a.ids[0] != want {
			t.Fatalf("第 %d 个查询应答应带上自己的 ID %q，实际 %v（可能用了共享的「当前 ID」变量）", i, want, a.ids)
		}
		if len(a.results[0]) != 1 {
			t.Errorf("第 %d 个查询应答应有 1 条结果，实际 %d 条", i, len(a.results[0]))
		}
	}
}

// captureInlineLog 把标准日志换到缓冲区，返回读取已写内容的函数；用例结束后恢复原输出。
func captureInlineLog(t *testing.T) func() string {
	t.Helper()
	var buf bytes.Buffer
	oldOut, oldFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(oldOut)
		log.SetFlags(oldFlags)
	})
	return buf.String
}

// TestInlineDegradedPathLogsOnce 校验三条降级路径的日志量：
// 上游出错、上游返回空列表各只记一条中文日志（带查询 ID 与关键字），关键字为空则什么都不记。
// 行内查询是边打字边触发的，一条查询记一屏日志会把真正的问题淹掉；完全不记则出了故障没法排查。
func TestInlineDegradedPathLogsOnce(t *testing.T) {
	cases := []struct {
		name      string
		svc       PlanetsService
		query     string
		wantLines int
		wantText  string
	}{
		{
			name:      "上游出错",
			svc:       &fakeService{planetsErr: errors.New("上游限流")},
			query:     "星球-天园",
			wantLines: 1,
			wantText:  "行内查询取星球数据失败",
		},
		{
			name:      "上游返回空列表",
			svc:       inlineService(nil),
			query:     "星球-天园",
			wantLines: 1,
			wantText:  "行内查询上游返回空星球列表",
		},
		{
			name:      "空关键字不记日志",
			svc:       inlineService(inlineFixturePlanets()),
			query:     "星球-",
			wantLines: 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			logs := captureInlineLog(t)
			runInline(t, c.svc, &fakeAnswerer{}, "星球-", c.query)

			out := logs()
			if c.wantLines == 0 {
				if strings.TrimSpace(out) != "" {
					t.Fatalf("这条路径不该记日志，实际记了：%s", out)
				}
				return
			}
			lines := strings.Split(strings.TrimSpace(out), "\n")
			if len(lines) != c.wantLines {
				t.Fatalf("应只记 %d 条日志，实际 %d 条：%s", c.wantLines, len(lines), out)
			}
			if !strings.Contains(lines[0], c.wantText) {
				t.Errorf("日志应说明原因（%q），实际 %q", c.wantText, lines[0])
			}
			// 排查时要能对上「哪次查询、什么关键字」，所以这两项必须在日志里。
			if !strings.Contains(lines[0], "query-1") || !strings.Contains(lines[0], "天园") {
				t.Errorf("日志应带上查询 ID 与关键字，实际 %q", lines[0])
			}
		})
	}
}

// TestInlineQueryTimeoutIsShorterThanCommandTimeout 校验行内查询用的是自己的短超时。
// 指令超时（plugutil.Timeout，20 秒）是为「取数 + 出图」定的；行内查询在 Telegram 侧只等十秒上下，
// 照搬 20 秒只会让超时的应答被丢弃、还白占一个处理槽位。
func TestInlineQueryTimeoutIsShorterThanCommandTimeout(t *testing.T) {
	if inlineQueryTimeout > 10*time.Second {
		t.Errorf("行内查询超时应不超过 Telegram 的等待时间（10s 量级），实际 %s", inlineQueryTimeout)
	}
	if inlineQueryTimeout >= plugutil.Timeout {
		t.Errorf("行内查询超时应短于指令超时 %s，实际 %s", plugutil.Timeout, inlineQueryTimeout)
	}
}

// TestInlineHandlerRefillsAfterSkippingMissingPlanet 校验剔掉上游没有的星球之后会往后补足 10 条候选。
// 译名表里有几条上游已经没有的条目（uvp 系列、火星），它们会占掉查表结果的位子；
// 查表时只取 10 条的话，被占掉的那一条就再也补不上来，用户看到的候选会莫名少一条。
// 这里把检索结果的第 1 条从上游列表里拿掉（模拟「译名表有、上游已下线」），
// 剩下的 10 条（第 2~11 条）必须全都在结果里；检索顺序是稳定的，所以这个断言是确定性的。
func TestInlineHandlerRefillsAfterSkippingMissingPlanet(t *testing.T) {
	const want = 10
	hits := glossary.SearchPlanets("a", want+1)
	if len(hits) < want+1 {
		t.Fatalf("用例前提不成立：关键字 a 只命中 %d 颗星球", len(hits))
	}
	list := make([]hd2.Planet, 0, want)
	for i, hit := range hits[1:] {
		list = append(list, hd2.Planet{Index: i + 1, Name: hit.English, Sector: "Akira"})
	}

	results := runInline(t, inlineService(list), &fakeAnswerer{}, "星球-", "星球-a")
	if len(results) != want {
		t.Fatalf("剔掉一条上游没有的星球后应补足 %d 条候选，实际 %d 条", want, len(results))
	}
	// 结果里不能出现上游没有的那一条，否则用户点进去只会得到「找不到」。
	missing := hits[0].English
	for i := range results {
		if got := articleAt(t, results, i).Title; got == missing {
			t.Fatalf("结果里出现了上游没有的星球 %q", missing)
		}
	}
}

// TestInlineHandlerHonorsQueryTimeout 校验应答期限真的取自 inlineQueryTimeout：
// 把期限改短、让上游取数一直阻塞，处理应当在期限附近结束并回空结果，而不是一直等下去。
func TestInlineHandlerHonorsQueryTimeout(t *testing.T) {
	old := inlineQueryTimeout
	inlineQueryTimeout = 50 * time.Millisecond
	defer func() { inlineQueryTimeout = old }()

	captureInlineLog(t)
	start := time.Now()
	results := runInline(t, &blockingService{}, &fakeAnswerer{}, "星球-", "星球-天园")
	elapsed := time.Since(start)

	if len(results) != 0 {
		t.Fatalf("取数一直阻塞时应回空结果，实际 %d 条", len(results))
	}
	if elapsed > 2*time.Second {
		t.Fatalf("应答应在 %s 的期限附近结束，实际用了 %s（期限可能没被用上）", inlineQueryTimeout, elapsed)
	}
}

// blockingService 的 Planets 先等一会儿，时间到了就返回预设列表；
// 不给 delay 时一直等到查询上下文结束才返回。前者拉开「取数」的窗口用于并发用例，后者用来观察应答期限。
type blockingService struct {
	fakeService
	delay time.Duration
}

// Planets 按 delay 模拟慢上游；delay 为 0 时阻塞到 ctx 被取消（或超时）再返回错误。
func (s *blockingService) Planets(ctx context.Context) (hd2.Result[[]hd2.Planet], error) {
	if s.delay <= 0 {
		<-ctx.Done()
		return hd2.Result[[]hd2.Planet]{}, ctx.Err()
	}
	select {
	case <-time.After(s.delay):
		return s.fakeService.planets, nil
	case <-ctx.Done():
		return hd2.Result[[]hd2.Planet]{}, ctx.Err()
	}
}
