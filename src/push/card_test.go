package push

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hd2_bot/src/config"
	"hd2_bot/src/render"
	"hd2_bot/src/translate"
)

// cardTime 是卡片上的数据时间（东八区 20:00）。
func cardTime() time.Time {
	return time.Date(2026, 9, 16, 20, 0, 0, 0, time.FixedZone("CST", 8*3600))
}

// TestBuildCardGroupsSectionsInFixedOrder 校验四类事件按固定顺序分节，没有事件的节整块省略。
func TestBuildCardGroupsSectionsInFixedOrder(t *testing.T) {
	events := []Event{
		{Kind: KindStation, ID: "1", Title: "空间站状态变更", Detail: "标记 1 → 2"},
		{Kind: KindDispatch, ID: "1049", Title: "MAJOR ORDER WON", Detail: "摘要"},
		{Kind: KindOwner, ID: "7", Title: "天园六IV（Acamar IV）失守", Detail: "控制方：超级地球 → 终结族"},
	}
	card := BuildCard(events, cardTime(), 5)

	if card.Title != "战况播报" {
		t.Fatalf("卡片标题应为「战况播报」，实际 %q", card.Title)
	}
	want := []string{"重大指令与新简报", "星球易主", "空间站与 DSS"}
	got := make([]string, 0, len(card.Sections))
	for _, s := range card.Sections {
		got = append(got, s.Title)
	}
	if len(got) != len(want) {
		t.Fatalf("节数与顺序错误：%v，期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 节应为 %q，实际 %q", i+1, want[i], got[i])
		}
	}
	if !strings.Contains(card.Subtitle, "3") {
		t.Fatalf("副标题应说明本次检测到几条变化，实际 %q", card.Subtitle)
	}
	if card.DataTime != "2026-09-16 20:00:00" {
		t.Fatalf("数据时间格式错误：%q", card.DataTime)
	}
}

// TestBuildCardTruncatesPerSection 校验每节最多 maxItems 条，超出的写「另有 N 条」。
func TestBuildCardTruncatesPerSection(t *testing.T) {
	events := make([]Event, 0, 8)
	for i := 0; i < 8; i++ {
		events = append(events, Event{Kind: KindOwner, ID: string(rune('a' + i)), Title: "星球失守", Detail: "控制方：超级地球 → 终结族"})
	}
	card := BuildCard(events, cardTime(), 5)
	if len(card.Sections) != 1 {
		t.Fatalf("应只有一节，实际 %d", len(card.Sections))
	}
	if len(card.Sections[0].Items) != 5 {
		t.Fatalf("每节最多 5 条，实际 %d", len(card.Sections[0].Items))
	}
	if card.Sections[0].More != "另有 3 条未显示" {
		t.Fatalf("截断说明错误：%q", card.Sections[0].More)
	}
}

// TestBuildCardDefaultsMaxItems 校验 maxItems <= 0 时用默认 5，不会画出「一条都没有」的卡片。
func TestBuildCardDefaultsMaxItems(t *testing.T) {
	events := make([]Event, 0, 6)
	for i := 0; i < 6; i++ {
		events = append(events, Event{Kind: KindOwner, ID: string(rune('a' + i)), Title: "星球失守"})
	}
	card := BuildCard(events, cardTime(), 0)
	if len(card.Sections) != 1 || len(card.Sections[0].Items) != defaultMaxItems {
		t.Fatalf("maxItems<=0 时应使用默认 %d 条，实际 %+v", defaultMaxItems, card.Sections)
	}
}

// TestFormatTextMatchesCard 校验文本回退与卡片同口径：同一份视图模型，节顺序与条目内容一致。
func TestFormatTextMatchesCard(t *testing.T) {
	events := []Event{
		{Kind: KindDispatch, ID: "1049", Title: "MAJOR ORDER WON", Detail: "超级地球取得了一次重大胜利。"},
		{Kind: KindOwner, ID: "7", Title: "天园六IV（Acamar IV）失守", Detail: "控制方：超级地球 → 终结族"},
	}
	card := BuildCard(events, cardTime(), 5)
	text := FormatText(card)

	// 这里逐条列出两张节里的正文与补充说明：只断言标题的话，把「· 标题 ｜ 说明」改成只输出标题
	// 也不会变红，等于「文本回退悄悄丢内容」没有任何守卫。
	for _, want := range []string{
		"战况播报",
		"重大指令与新简报",
		"MAJOR ORDER WON",
		"超级地球取得了一次重大胜利。", // dispatch 的 Detail
		"星球易主",
		"天园六IV",
		"控制方：超级地球 → 终结族",               // owner 的 Detail
		"数据时间：2026\\-09\\-16 20:00:00", // MarkdownV2 会转义 '-'
	} {
		if !strings.Contains(text, want) {
			t.Errorf("文本回退缺少 %q，实际：\n%s", want, text)
		}
	}
}

// TestFormatTextEscapesMarkdownV2 校验文本回退做了 MarkdownV2 转义（否则 Telegram 会报解析失败）。
func TestFormatTextEscapesMarkdownV2(t *testing.T) {
	card := BuildCard([]Event{
		{Kind: KindOwner, ID: "7", Title: "天园六IV（Acamar IV）失守", Detail: "控制方：超级地球 → 终结族"},
	}, cardTime(), 5)
	text := FormatText(card)
	if strings.Contains(text, "（Acamar IV）") {
		t.Fatalf("括号必须转义，实际：%s", text)
	}
	if !strings.Contains(text, "\\(") {
		t.Fatalf("括号必须转义，实际：%s", text)
	}
}

// TestFormatTextIncludesMoreAndNote 校验「另有 N 条」与底部说明都出现在文本回退里。
func TestFormatTextIncludesMoreAndNote(t *testing.T) {
	events := make([]Event, 0, 7)
	for i := 0; i < 7; i++ {
		events = append(events, Event{Kind: KindStation, ID: string(rune('a' + i)), Title: "空间站状态变更", Detail: "标记 1 → 2"})
	}
	card := BuildCard(events, cardTime(), 5)
	card.Note = translateUnavailableNote
	text := FormatText(card)
	if !strings.Contains(text, "另有 2 条") {
		t.Errorf("截断说明缺失：%s", text)
	}
	if !strings.Contains(text, "翻译暂不可用") {
		t.Errorf("翻译不可用说明缺失：%s", text)
	}
}

// TestPushCardTemplateRenders 校验模板本身能渲染出预期内容（不开浏览器，只走 render.HTML）。
func TestPushCardTemplateRenders(t *testing.T) {
	card := BuildCard([]Event{
		{Kind: KindDispatch, ID: "1049", Title: "MAJOR ORDER WON", Detail: "超级地球取得了一次重大胜利。"},
		{Kind: KindOwner, ID: "7", Title: "天园六IV（Acamar IV）失守", Detail: "控制方：超级地球 → 终结族"},
	}, cardTime(), 5)

	html, err := render.HTML(render.Card{Name: "push", Data: card})
	if err != nil {
		t.Fatalf("渲染推送卡片 HTML 失败：%v", err)
	}
	for _, want := range []string{
		"战况播报",
		"重大指令与新简报",
		"MAJOR ORDER WON",
		"星球易主",
		"天园六IV",
		`class="card__emblem"`, // 徽标（游戏原生 SVG）内联进卡片
		"2026-09-16 20:00:00",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("卡片 HTML 缺少 %q", want)
		}
	}
	if strings.Contains(html, "数据可能已过期") {
		t.Error("推送卡片用的是本轮数据，不应出现过期角标")
	}
}

// TestPushCardTemplateHidesEmptySection 校验模板不会为「条目为空」的节渲染出空标题。
func TestPushCardTemplateHidesEmptySection(t *testing.T) {
	card := BuildCard([]Event{{Kind: KindOwner, ID: "7", Title: "星球失守"}}, cardTime(), 5)
	html, err := render.HTML(render.Card{Name: "push", Data: card})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	if strings.Contains(html, "空间站与 DSS") {
		t.Error("没有空间站事件时不应出现该节标题")
	}
	// 这条事件没有 Detail：模板若少了 {{if .Detail}} 守卫，会渲染出一个空 div。
	assertNoEmptyBlocks(t, html)
}

// —— 以下为补充用例：计划里的 8 条没覆盖到的分支与边界 ——

// TestPushCardNameMatchesTemplate 校验常量 pushCardName 与模板文件名一致（防「改了模板名忘了改常量」）。
func TestPushCardNameMatchesTemplate(t *testing.T) {
	card := BuildCard([]Event{{Kind: KindOwner, ID: "7", Title: "星球失守"}}, cardTime(), 5)
	if _, err := render.HTML(render.Card{Name: pushCardName, Data: card}); err != nil {
		t.Fatalf("常量 pushCardName 与模板不一致：%v", err)
	}
}

// assertNoEmptyBlocks 断言 HTML 里没有「空的内容块」。
// 模板用 {{if .More}} / {{if .Note}} / {{if .Detail}} 控制可选内容的渲染；
// 去掉这些守卫后 HTML 里会留下一串空 div，肉眼看不出、但会让卡片多出空白行，
// 所以这里直接盯住「空 div」这个可观察后果。
func assertNoEmptyBlocks(t *testing.T, html string) {
	t.Helper()
	for _, empty := range []string{`<div class="muted"></div>`, `<div class="strong"></div>`} {
		if strings.Contains(html, empty) {
			t.Errorf("HTML 里出现空内容块 %s，说明模板少了 {{if}} 守卫", empty)
		}
	}
}

// TestBuildCardAllFourSectionsOrder 校验四类事件齐全时的节顺序与归属
// （计划里的用例只放了三类事件，把「战役动态」和「空间站」对调也测不出来）。
func TestBuildCardAllFourSectionsOrder(t *testing.T) {
	events := []Event{
		{Kind: KindStation, ID: "1", Title: "空间站状态变更", Detail: "标记 1 → 2"},
		{Kind: KindCampaign, ID: "9", Title: "新战役：天园六IV", Detail: "防守战"},
		{Kind: KindOwner, ID: "7", Title: "天园六IV 失守", Detail: "控制方：超级地球 → 终结族"},
		{Kind: KindDispatch, ID: "1049", Title: "MAJOR ORDER WON"},
	}
	card := BuildCard(events, cardTime(), 5)
	wantTitles := []string{"重大指令与新简报", "星球易主", "战役动态", "空间站与 DSS"}
	if len(card.Sections) != len(wantTitles) {
		t.Fatalf("应分 4 节，实际 %d：%+v", len(card.Sections), card.Sections)
	}
	for i, want := range wantTitles {
		if card.Sections[i].Title != want {
			t.Errorf("第 %d 节应为 %q，实际 %q", i+1, want, card.Sections[i].Title)
		}
	}
	wantItems := []string{"MAJOR ORDER WON", "天园六IV 失守", "新战役：天园六IV", "空间站状态变更"}
	for i, want := range wantItems {
		if len(card.Sections[i].Items) != 1 || card.Sections[i].Items[0].Title != want {
			t.Errorf("第 %d 节条目错误：%+v", i+1, card.Sections[i].Items)
		}
	}
}

// TestPushCardTemplateShowsMore 校验模板会渲染「另有 N 条未显示」。
// 计划里的 TestFormatTextIncludesMoreAndNote 只覆盖文本回退，模板漏掉 More 时不会变红，所以单列一条。
func TestPushCardTemplateShowsMore(t *testing.T) {
	events := make([]Event, 0, 8)
	for i := 0; i < 8; i++ {
		events = append(events, Event{Kind: KindStation, ID: string(rune('a' + i)), Title: "空间站状态变更", Detail: "标记 1 → 2"})
	}
	card := BuildCard(events, cardTime(), 5)
	html, err := render.HTML(render.Card{Name: pushCardName, Data: card})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	if !strings.Contains(html, "另有 3 条未显示") {
		t.Errorf("模板应渲染截断说明，实际 HTML：\n%s", html)
	}
	if strings.Contains(html, "另有 0 条未显示") {
		t.Error("没有截断时不应出现截断说明")
	}
	// 没有截断时 More 是空串：模板若少了 {{if .More}} 守卫，这里会留下一个空 div。
	short := BuildCard(events[:5], cardTime(), 5)
	shortHTML, err := render.HTML(render.Card{Name: pushCardName, Data: short})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	assertNoEmptyBlocks(t, shortHTML)
}

// TestPushCardTemplateShowsNote 校验模板会渲染底部说明（翻译不可用时的口径说明）。
func TestPushCardTemplateShowsNote(t *testing.T) {
	card := BuildCard([]Event{{Kind: KindDispatch, ID: "1049", Title: "MAJOR ORDER WON"}}, cardTime(), 5)
	card.Note = translateUnavailableNote
	html, err := render.HTML(render.Card{Name: pushCardName, Data: card})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	if !strings.Contains(html, "翻译暂不可用") {
		t.Errorf("模板应渲染底部说明，实际 HTML：\n%s", html)
	}
}

// TestPushCardTemplateHidesNoteWhenEmpty 校验没有说明时模板不渲染空的一段。
func TestPushCardTemplateHidesNoteWhenEmpty(t *testing.T) {
	card := BuildCard([]Event{{Kind: KindDispatch, ID: "1049", Title: "MAJOR ORDER WON"}}, cardTime(), 5)
	html, err := render.HTML(render.Card{Name: pushCardName, Data: card})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	if strings.Contains(html, "翻译暂不可用") {
		t.Error("Note 为空时不应渲染底部说明")
	}
}

// TestBuildCardNoMoreAtExactLimit 校验「正好等于上限」时不写截断说明（截断判定的边界）。
func TestBuildCardNoMoreAtExactLimit(t *testing.T) {
	events := make([]Event, 0, 5)
	for i := 0; i < 5; i++ {
		events = append(events, Event{Kind: KindOwner, ID: string(rune('a' + i)), Title: "星球失守"})
	}
	card := BuildCard(events, cardTime(), 5)
	if len(card.Sections) != 1 || len(card.Sections[0].Items) != 5 {
		t.Fatalf("应保留 5 条，实际 %+v", card.Sections)
	}
	if card.Sections[0].More != "" {
		t.Fatalf("正好 5 条时不应有截断说明，实际 %q", card.Sections[0].More)
	}
}

// TestBuildCardKeepsSectionInnerOrder 校验节内顺序沿用传入顺序（Detect 已经排好，Builder 不得重排）。
func TestBuildCardKeepsSectionInnerOrder(t *testing.T) {
	events := []Event{
		{Kind: KindOwner, ID: "3", Title: "第三"},
		{Kind: KindOwner, ID: "1", Title: "第一"},
		{Kind: KindOwner, ID: "2", Title: "第二"},
	}
	card := BuildCard(events, cardTime(), 5)
	got := make([]string, 0, 3)
	for _, item := range card.Sections[0].Items {
		got = append(got, item.Title)
	}
	want := []string{"第三", "第一", "第二"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("节内顺序应原样保留，实际 %v", got)
		}
	}
}

// TestBuildCardSubtitleCountsAllEvents 校验副标题统计的是检测到的总条数，而不是截断后显示的条数。
func TestBuildCardSubtitleCountsAllEvents(t *testing.T) {
	events := make([]Event, 0, 8)
	for i := 0; i < 8; i++ {
		events = append(events, Event{Kind: KindOwner, ID: string(rune('a' + i)), Title: "星球失守"})
	}
	card := BuildCard(events, cardTime(), 5)
	if card.Subtitle != "本次检测到 8 条变化" {
		t.Fatalf("副标题应统计全部事件，实际 %q", card.Subtitle)
	}
}

// TestBuildCardWithoutEvents 校验没有事件时是一张「有标题、无节」的卡片，而不是零值。
func TestBuildCardWithoutEvents(t *testing.T) {
	card := BuildCard(nil, cardTime(), 5)
	if card.Title != cardTitle {
		t.Fatalf("标题应为 %q，实际 %q", cardTitle, card.Title)
	}
	if len(card.Sections) != 0 {
		t.Fatalf("没有事件时不应有节，实际 %+v", card.Sections)
	}
	if card.DataTime != "2026-09-16 20:00:00" {
		t.Fatalf("没有事件也要给出数据时间，实际 %q", card.DataTime)
	}
}

// TestFormatTextFallsBackToDefaultTitle 校验视图模型没设标题时文本回退仍可用（手工构造的卡片兜底）。
func TestFormatTextFallsBackToDefaultTitle(t *testing.T) {
	text := FormatText(Card{})
	if !strings.HasPrefix(text, "*"+cardTitle+"*") {
		t.Fatalf("应回退到默认标题，实际：%s", text)
	}
	if !strings.Contains(text, "数据时间：未知") {
		t.Fatalf("没有数据时间时应写「未知」，实际：%s", text)
	}
}

// TestFormatTextOmitsSeparatorWhenDetailEmpty 校验没有补充说明时不画分隔符。
func TestFormatTextOmitsSeparatorWhenDetailEmpty(t *testing.T) {
	card := BuildCard([]Event{{Kind: KindOwner, ID: "7", Title: "星球失守"}}, cardTime(), 5)
	text := FormatText(card)
	if strings.Contains(text, "｜") {
		t.Fatalf("Detail 为空时不应出现分隔符，实际：\n%s", text)
	}
	if !strings.Contains(text, "· 星球失守") {
		t.Fatalf("应渲染条目，实际：\n%s", text)
	}
}

// markdownV2Specials 是 Telegram MarkdownV2 要求转义的全部半角特殊字符。
const markdownV2Specials = "_*[]()~`>#+-=|{}.!"

// assertOnlyAllowedUnescaped 断言 text 里每个 MarkdownV2 特殊字符都紧跟在反斜杠之后，
// 只有星星（*，本卡片用来自行加粗的排版标记）允许不转义。
// 比逐个 Contains("\\x") 更严：能发现「转义了但只转了一部分」的情况。
func assertOnlyAllowedUnescaped(t *testing.T, text string) {
	t.Helper()
	runes := []rune(text)
	rawStars, rawOthers := 0, 0
	for i, r := range runes {
		if r != '*' && !strings.ContainsRune(markdownV2Specials, r) {
			continue
		}
		if i > 0 && runes[i-1] == '\\' && (i < 2 || runes[i-2] != '\\') {
			continue
		}
		if r == '*' {
			rawStars++
			continue
		}
		rawOthers++
	}
	if rawOthers != 0 {
		t.Errorf("有 %d 个未转义的特殊字符：\n%s", rawOthers, text)
	}
	if rawStars%2 != 0 {
		t.Errorf("未转义的星号应为成对的加粗标记，实际 %d 个：\n%s", rawStars, text)
	}
}

// TestFormatTextEscapesAllMarkdownV2Specials 校验文本回退转义了整张 MarkdownV2 特殊字符表。
func TestFormatTextEscapesAllMarkdownV2Specials(t *testing.T) {
	card := BuildCard([]Event{{
		Kind:   KindOwner,
		ID:     "7",
		Title:  "A_B*C[D]E(F)G~H`",
		Detail: "#1 + 2 = 3 > 4 | 5 . 6 !",
	}}, cardTime(), 5)
	text := FormatText(card)
	assertOnlyAllowedUnescaped(t, text)
	// 排版标记本身不能被转义掉，否则加粗失效、观感全变。
	if !strings.Contains(text, "*"+cardTitle+"*") || !strings.Contains(text, "· ") {
		t.Fatalf("排版标记不应被转义，实际：\n%s", text)
	}
}

// TestFormatTextNormalizesFullWidthBrackets 校验全角括号被归一成已转义的半角括号
// （不归一的话同一段文案里会出现「半角已转义、全角未转义」两种括号，Telegram 端行为不一致）。
func TestFormatTextNormalizesFullWidthBrackets(t *testing.T) {
	card := BuildCard([]Event{{
		Kind:  KindOwner,
		ID:    "7",
		Title: "天园六IV（Acamar IV）［测试］｛测试｝失守",
	}}, cardTime(), 5)
	text := FormatText(card)
	for _, raw := range []string{"（", "）", "［", "］", "｛", "｝"} {
		if strings.Contains(text, raw) {
			t.Errorf("全角括号 %q 应被归一，实际：\n%s", raw, text)
		}
	}
	for _, want := range []string{`\(`, `\)`, `\[`, `\]`, `\{`, `\}`} {
		if !strings.Contains(text, want) {
			t.Errorf("缺少已转义的 %q，实际：\n%s", want, text)
		}
	}
}

// TestFormatTextEscapesNoteAndMore 校验底部说明与截断说明同样过转义（漏掉任何一段都会被 Telegram 拒收）。
func TestFormatTextEscapesNoteAndMore(t *testing.T) {
	events := make([]Event, 0, 6)
	for i := 0; i < 6; i++ {
		events = append(events, Event{Kind: KindOwner, ID: string(rune('a' + i)), Title: "星球失守"})
	}
	card := BuildCard(events, cardTime(), 5)
	card.Note = "翻译（不可用）！"
	text := FormatText(card)
	if strings.Contains(text, "（不可用）") {
		t.Fatalf("底部说明里的全角括号未归一/未转义：\n%s", text)
	}
	if !strings.Contains(text, `\(不可用\)`) {
		t.Fatalf("底部说明缺少已转义的括号：\n%s", text)
	}
	if !strings.Contains(text, "另有 1 条未显示") {
		t.Fatalf("截断说明缺失：\n%s", text)
	}
}

// TestBuildCardKeepsDetail 校验事件的补充说明会原样进入视图模型（图片路径靠它显示「控制方：A → B」）。
func TestBuildCardKeepsDetail(t *testing.T) {
	card := BuildCard([]Event{
		{Kind: KindOwner, ID: "7", Title: "星球失守", Detail: "控制方：超级地球 → 终结族"},
		{Kind: KindStation, ID: "1", Title: "空间站状态变更", Detail: "标记 1 → 2"},
	}, cardTime(), 5)
	if len(card.Sections) != 2 {
		t.Fatalf("应有 2 节，实际 %d", len(card.Sections))
	}
	if card.Sections[0].Items[0].Detail != "控制方：超级地球 → 终结族" {
		t.Fatalf("易主节的说明丢失：%+v", card.Sections[0].Items[0])
	}
	if card.Sections[1].Items[0].Detail != "标记 1 → 2" {
		t.Fatalf("空间站节的说明丢失：%+v", card.Sections[1].Items[0])
	}
}

// TestPushCardTemplateShowsDetail 校验模板确实把条目的补充说明画进了 HTML。
// Go 侧的 TestBuildCardKeepsDetail 只能证明视图模型里有值，证明不了模板渲染了它。
func TestPushCardTemplateShowsDetail(t *testing.T) {
	card := BuildCard([]Event{
		{Kind: KindOwner, ID: "7", Title: "天园六IV 失守", Detail: "控制方：超级地球 → 终结族"},
	}, cardTime(), 5)
	html, err := render.HTML(render.Card{Name: pushCardName, Data: card})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	if !strings.Contains(html, "控制方：超级地球 → 终结族") {
		t.Errorf("模板应渲染条目的补充说明，实际 HTML：\n%s", html)
	}
}

// TestFormatTextGolden 用整段文本比对钉住文本回退的排版：标题、副标题、节、条目、数据时间的顺序。
// 只写 Contains 的话，「副标题挪到末尾」这类改动照样全绿（文本还是那些文本，顺序变了）。
func TestFormatTextGolden(t *testing.T) {
	card := BuildCard([]Event{{
		Kind:   KindDispatch,
		ID:     "1049",
		Title:  "MAJOR ORDER WON",
		Detail: "超级地球取得了一次重大胜利。",
	}}, cardTime(), 5)

	want := strings.Join([]string{
		"*战况播报*",
		"本次检测到 1 条变化",
		"",
		"*重大指令与新简报*",
		"· MAJOR ORDER WON ｜ 超级地球取得了一次重大胜利。",
		"数据时间：2026\\-09\\-16 20:00:00",
	}, "\n")
	if got := FormatText(card); got != want {
		t.Errorf("文本回退排版与预期不一致：\n实际：\n%s\n期望：\n%s", got, want)
	}
}

// TestBuildCardHugeMaxItemsDoesNotPanic 校验 maxItems 取极大值时不会 panic、也不会按它预分配内存。
// maxItems 直接来自配置（config 只校验 > 0，没有上界），照它建切片会被一个手滑的大数值顶到
// makeslice 崩溃，或者按四个节各吃掉几十 GB。
func TestBuildCardHugeMaxItemsDoesNotPanic(t *testing.T) {
	events := []Event{
		{Kind: KindOwner, ID: "1", Title: "星球失守", Detail: "控制方：超级地球 → 终结族"},
		{Kind: KindDispatch, ID: "2", Title: "MAJOR ORDER WON", Detail: "胜利"},
	}
	huge := BuildCard(events, cardTime(), 1<<40)
	tight := BuildCard(events, cardTime(), len(events))
	if len(huge.Sections) != len(tight.Sections) {
		t.Fatalf("节数应与按事件数建卡一致：%d / %d", len(huge.Sections), len(tight.Sections))
	}
	for i := range tight.Sections {
		if len(huge.Sections[i].Items) != len(tight.Sections[i].Items) {
			t.Errorf("第 %d 节的条数应与按事件数建卡一致：%d / %d",
				i+1, len(huge.Sections[i].Items), len(tight.Sections[i].Items))
		}
		if huge.Sections[i].More != tight.Sections[i].More {
			t.Errorf("第 %d 节的截断说明应与按事件数建卡一致：%q / %q",
				i+1, huge.Sections[i].More, tight.Sections[i].More)
		}
	}
}

// TestBuildCardClampsItemText 校验条目文本的终局兜底：超过终局防线（600 / 1200 rune）的标题与说明
// 按 rune 截断并补省略号，说明里的换行被压平（文本回退里一条条目就是一行）。
// 这里的长度刻意由常量推出：它验证的是「兜底机制本身」；防线的具体数值由
// TestPushBudgetsMatchDesign 与 TestCardFinalBudgetBoundaries 用字面量钉住。
func TestBuildCardClampsItemText(t *testing.T) {
	longTitle := strings.Repeat("标", maxTitleRunes+50)
	longDetail := strings.Repeat("说", maxDetailRunes+50)
	card := BuildCard([]Event{{
		Kind:   KindOwner,
		ID:     "7",
		Title:  longTitle,
		Detail: longDetail,
	}}, cardTime(), 5)

	item := card.Sections[0].Items[0]
	if got := []rune(item.Title); len(got) != maxTitleRunes+1 || got[len(got)-1] != '…' {
		t.Errorf("标题应截到 %d 字并以省略号结尾，实际 %d 字", maxTitleRunes, len(got))
	}
	if got := []rune(item.Detail); len(got) != maxDetailRunes+1 || got[len(got)-1] != '…' {
		t.Errorf("说明应截到 %d 字并以省略号结尾，实际 %d 字", maxDetailRunes, len(got))
	}

	// 正常长度的条目不受影响：兜底不能把内容改样。
	short := BuildCard([]Event{{Kind: KindOwner, ID: "7", Title: "星球失守", Detail: "控制方：超级地球 → 终结族"}}, cardTime(), 5)
	if short.Sections[0].Items[0].Title != "星球失守" || short.Sections[0].Items[0].Detail != "控制方：超级地球 → 终结族" {
		t.Errorf("正常长度的条目不该被改动，实际 %+v", short.Sections[0].Items[0])
	}

	// 说明里的裸换行会被压平：否则文本回退里「· 标题 ｜ 说明」的第二行会脱开缩进。
	nl := BuildCard([]Event{{Kind: KindOwner, ID: "7", Title: "星球失守", Detail: "第一行\n\n\n结束"}}, cardTime(), 5)
	if got := nl.Sections[0].Items[0].Detail; strings.Contains(got, "\n") {
		t.Errorf("说明里的换行必须被压平，实际 %q", got)
	}
	lines := strings.Split(FormatText(nl), "\n")
	for _, line := range lines {
		if line == "结束" {
			t.Fatalf("文本回退里不该出现脱开缩进的续行：\n%s", FormatText(nl))
		}
	}
	if !strings.Contains(FormatText(nl), "· 星球失守 ｜ 第一行 结束") {
		t.Errorf("说明应压成一行接在分隔符后：\n%s", FormatText(nl))
	}
}

// TestBuildCardLogsUnknownKind 校验「类别不在展示范围内」的事件会打一条日志提醒。
// 副标题按输入条数统计，卡片上却可能一节都没有，数字对不上时全靠这条日志定位。
func TestBuildCardLogsUnknownKind(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)

	card := BuildCard([]Event{{Kind: Kind("bogus"), ID: "1", Title: "X"}}, cardTime(), 5)
	if len(card.Sections) != 0 {
		t.Fatalf("未知类别不该画出任何节，实际 %+v", card.Sections)
	}
	if !strings.Contains(card.Subtitle, "1") {
		t.Errorf("副标题仍应统计输入条数，实际 %q", card.Subtitle)
	}
	if !strings.Contains(buf.String(), "不在展示范围内") {
		t.Errorf("未知类别应打一条中文日志，实际 %q", buf.String())
	}

	// 正常类别不该打日志：否则每轮推送都会刷一条无意义警告。
	buf.Reset()
	BuildCard([]Event{{Kind: KindOwner, ID: "7", Title: "星球失守"}}, cardTime(), 5)
	if buf.String() != "" {
		t.Errorf("正常类别不该打日志，实际 %q", buf.String())
	}
}

// minConfigYAML 是能通过 config 校验的最小配置；这里只用来读「配置里的默认值」。
const minConfigYAML = `
bot:
  token: 123:abc
  group_id: -1001234567890
api:
  client: hd2_bot
  contact: dev@example.com
`

// TestDefaultMaxItemsMatchesConfigDefault 校验 defaultMaxItems 与 config 里 push.max_items 的默认值一致。
// 两处各写一个 5，改一处忘另一处不会报错，只会让「配置没写」和「没走配置」画出不同的卡片。
func TestDefaultMaxItemsMatchesConfigDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hd2.yaml")
	if err := os.WriteFile(path, []byte(minConfigYAML), 0o600); err != nil {
		t.Fatalf("写临时配置失败：%v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("加载最小配置失败：%v", err)
	}
	if cfg.Push.MaxItems != defaultMaxItems {
		t.Errorf("config 的 push.max_items 默认值（%d）必须与 defaultMaxItems（%d）一致",
			cfg.Push.MaxItems, defaultMaxItems)
	}
}

// TestClampItemTextUsesSummarize 校验终局兜底复用 translate.Summarize：空白折叠、按 rune 截断。
// 直接断言它转调了共用的实现，避免两处各写一套截断规则。
func TestClampItemTextUsesSummarize(t *testing.T) {
	const text = "  第一段   第二段\n\n第三段  "
	if got, want := clampItemText(text, 100), translate.Summarize(text, 100); got != want {
		t.Errorf("clampItemText 应与 translate.Summarize 口径一致：%q / %q", got, want)
	}
}

// realUpstreamMaxTitleRunes / realUpstreamMaxDetailRunes 是 2026-09-17 用真实上游量出来的最长长度
// （1048 条简报：清洗后正文最长 807 rune、首行最长 385 rune）。用例拿这两个真实数字当夹具，
// 把研发预算改回旧值（标题 40、正文 100、终局防线 80 / 200）会让它们变红。
const (
	realUpstreamMaxTitleRunes  = 385
	realUpstreamMaxDetailRunes = 807
)

// TestPushBudgetsMatchDesign 钉住设计 §3.1 的四个预算值：两个展示预算（装下真实上游内容）
// 与两个终局防线（真实上限的约 1.5 倍，只在异常数据上触发）。任何一个被改回旧值都会让本用例变红。
func TestPushBudgetsMatchDesign(t *testing.T) {
	cases := []struct {
		name      string
		got, want int
		why       string
	}{
		{"dispatchTitleRunes", dispatchTitleRunes, 400, "实测最长首行 385 rune，取整上调"},
		{"detailRunes", detailRunes, 1000, "实测最长正文 807 rune，取整上调"},
		{"maxTitleRunes", maxTitleRunes, 600, "终局防线，约 1.5 倍于实测最长首行 385"},
		{"maxDetailRunes", maxDetailRunes, 1200, "终局防线，约 1.5 倍于实测最长正文 807"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s 应为 %d（%s），实际 %d", c.name, c.want, c.why, c.got)
		}
	}
}

// TestCardFinalBudgetKeepsRealUpstreamContent 校验终局防线不会切到真实上游内容：
// 按实测最长值（标题 385 / 正文 807 rune）建卡后必须一字不差，且不带省略号。
func TestCardFinalBudgetKeepsRealUpstreamContent(t *testing.T) {
	title := strings.Repeat("标", realUpstreamMaxTitleRunes)
	detail := strings.Repeat("说", realUpstreamMaxDetailRunes)
	card := BuildCard([]Event{{Kind: KindOwner, ID: "7", Title: title, Detail: detail}}, cardTime(), 5)

	item := card.Sections[0].Items[0]
	if item.Title != title {
		t.Errorf("实测最长标题（%d rune）应原样保留，实际 %d 字符：%q",
			realUpstreamMaxTitleRunes, len([]rune(item.Title)), item.Title)
	}
	if item.Detail != detail {
		t.Errorf("实测最长正文（%d rune）应原样保留，实际 %d 字符：%q",
			realUpstreamMaxDetailRunes, len([]rune(item.Detail)), item.Detail)
	}
	if text := FormatText(card); strings.Contains(text, "…") {
		t.Errorf("真实长度的内容不该在卡片或文本回退里出现省略号：\n%s", text)
	}
}

// TestCardFinalBudgetBoundaries 钉住终局防线的边界（字面量 600 / 1200）：
// 正好等于防线时一个字都不截，多出字符才截断并补一个省略号。
// 边界值直接写字面量，是为了让「把常量改回旧值（80 / 200）」立刻变红。
func TestCardFinalBudgetBoundaries(t *testing.T) {
	cases := []struct {
		name      string
		text      string
		max       int
		wantRunes int
		wantEllip bool
	}{
		{"标题正好 600 不截", strings.Repeat("标", 600), maxTitleRunes, 600, false},
		{"标题 602 才截", strings.Repeat("标", 602), maxTitleRunes, 601, true},
		{"正文正好 1200 不截", strings.Repeat("说", 1200), maxDetailRunes, 1200, false},
		{"正文 1201 才截", strings.Repeat("说", 1201), maxDetailRunes, 1201, true},
	}
	for _, c := range cases {
		got := clampItemText(c.text, c.max)
		runes := []rune(got)
		if len(runes) != c.wantRunes {
			t.Errorf("%s：期望 %d 个字符，实际 %d 个", c.name, c.wantRunes, len(runes))
			continue
		}
		if ellip := strings.HasSuffix(got, "…"); ellip != c.wantEllip {
			t.Errorf("%s：省略号应为 %v，实际 %v", c.name, c.wantEllip, ellip)
		}
	}
}
