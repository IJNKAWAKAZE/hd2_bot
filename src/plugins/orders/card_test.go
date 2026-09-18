package orders

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"hd2_bot/src/core/bot"
	"hd2_bot/src/hd2"
	"hd2_bot/src/plugins/plugutil"
	"hd2_bot/src/plugins/plugutil/plugtest"
	"hd2_bot/src/render"
	"hd2_bot/src/translate"
)

// TestCleanGameText 校验上游文本里的游戏内标记与多余空白被清掉，正文本身保持完整。
//
// 清洗规则自 S3 起由 translate.CleanGameText 提供（命令卡片、推送卡片与送译输入共用一份），
// 本包不再自带实现。用例仍留在这里：它钉的是「订单卡片展示的正文长什么样」。
// 游戏内高亮（<i=3>…</i>）这几条是刻意的：规则只有一条「尖括号里的一切都删掉」，
// 评审据此删掉了单独的游戏标记正则；将来谁把规则收窄成「只删已知 HTML 标签」，这里立刻会红。
func TestCleanGameText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"游戏内高亮标记带数值", "<i=3>MAJOR ORDER WON</i>\n\n正文", "MAJOR ORDER WON\n\n正文"},
		{"游戏内高亮标记不带数值", "<i>强调</i>", "强调"},
		{"游戏内标记嵌在多段正文里", "第一段\n<i=5>结果</i>\n第三段", "第一段\n结果\n第三段"},
		{"数值标记", "消灭 <i=1>200,000,000</i> 名敌人", "消灭 200,000,000 名敌人"},
		{"HTML 片段", "正文<span data-ah=\"1\">重点</span>结束", "正文重点结束"},
		{"多余空行", "开头\n\n\n\n结尾", "开头\n\n结尾"},
		{"行尾空格", "第一行   \n第二行", "第一行\n第二行"},
		{"纯空白", "   \n  ", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := translate.CleanGameText(c.in); got != c.want {
				t.Errorf("CleanGameText 期望 %q，实际 %q", c.want, got)
			}
		})
	}
}

// TestTruncateRunes 校验按字符截断：中文不会被切坏，超出时补省略号并返回 true。
func TestTruncateRunes(t *testing.T) {
	if got, cut := truncateRunes("短文本", 10); got != "短文本" || cut {
		t.Errorf("未超长不应截断：%q，%v", got, cut)
	}
	got, cut := truncateRunes(strings.Repeat("字", 10), 4)
	if !cut || got != "字字字字…" {
		t.Errorf("截断结果错误：%q，%v", got, cut)
	}
	// 按 rune 截断：4 个中文字符是 12 字节，按字节截会切出乱码。
	if len([]rune(got)) != 5 {
		t.Errorf("截断后应有 4 个字符加省略号，实际 %q", got)
	}
	if _, cut := truncateRunes("   ", 3); cut {
		t.Error("纯空白不应算作被截断")
	}
	// max <= 0 的契约：按 0 字预算处理，返回空串；非空原文一律标记为「已截断」。
	// 注意 plugins/planets 里的 truncateText 语义正好相反（不限制、原样返回），
	// 那条契约由该包的 TestTruncateTextBounds 钉住。
	if got, cut := truncateRunes("还有正文", 0); got != "" || !cut {
		t.Errorf("max<=0 时应返回空串并标记已截断，实际 %q，%v", got, cut)
	}
	if got, cut := truncateRunes("", -1); got != "" || cut {
		t.Errorf("空原文在 max<=0 时不该算被截断，实际 %q，%v", got, cut)
	}
}

// TestBuildTranslatedDispatchesChineseBodyNotFlagged 校验「正文本来就是中文」时不写翻译说明。
//
// 翻译层对不含拉丁字母的段落根本不会发请求，原样回显是正常结果；
// 过去按「译文 == 原文」一律判成没翻动，于是纯中文简报的卡片上会多一句
// 「翻译暂不可用，以下为英文原文」——正文里一个英文都没有，线上这是假告警。
func TestBuildTranslatedDispatchesChineseBodyNotFlagged(t *testing.T) {
	list := []hd2.Dispatch{
		{ID: 2, Published: testFetchedAt().UTC(), Message: "清剿机器人。"},
		{ID: 1, Published: testFetchedAt().UTC().Add(-time.Hour), Message: "第二段简报。"},
	}
	// 回显替身：逐段原样返回入参（模拟透传后端 / 翻译层没送这两段）。
	trans := &plugtest.Translator{}
	card := BuildTranslatedDispatches(context.Background(), list, 2, testFetchedAt(), false, trans)

	for i, want := range []string{"清剿机器人。", "第二段简报。"} {
		if got := card.Items[i].Message; got != want {
			t.Errorf("第 %d 条应保持中文原文，实际 %q", i+1, got)
		}
	}
	if card.TranslateNote != "" {
		t.Errorf("中文正文不该被标成翻译故障，实际 %q", card.TranslateNote)
	}
	if text := FormatDispatchesText(card); strings.Contains(text, "翻译暂不可用") {
		t.Errorf("文本回退里也不该出现翻译说明：\n%s", text)
	}
	html, err := render.HTML(render.Card{Name: dispatchesCardName, Data: card})
	if err != nil {
		t.Fatalf("渲染简报卡片 HTML 失败：%v", err)
	}
	if strings.Contains(html, translateUnavailableNote) {
		t.Errorf("卡片 HTML 里不该出现翻译说明：\n%s", html)
	}

	// 同一批里夹一条真英文（层会真的送它）时，回显就必须被认成没翻动：修复不能把失败也吞掉。
	mixed := append([]hd2.Dispatch{{ID: 3, Published: testFetchedAt().UTC().Add(-2 * time.Hour), Message: "Victory."}}, list...)
	echo := BuildTranslatedDispatches(context.Background(), mixed, 3, testFetchedAt(), false, &plugtest.Translator{})
	if echo.TranslateNote != translateUnavailableNote {
		t.Fatalf("英文正文回显仍应注明翻译不可用，实际 %q", echo.TranslateNote)
	}
}

// TestBuildDispatchesCardOrder 校验简报表按发布时间倒序排列，没有时间的排最后。
func TestBuildDispatchesCardOrder(t *testing.T) {
	at := testFetchedAt().UTC()
	list := []hd2.Dispatch{
		{ID: 1, Published: at.Add(-72 * time.Hour)},
		{ID: 2, Published: time.Time{}}, // 上游没给时间：应排在最后
		{ID: 3, Published: at},
		{ID: 4, Published: at.Add(-24 * time.Hour)},
	}
	card := BuildDispatchesCard(list, 10, testFetchedAt(), false)

	var got []int64
	for _, item := range card.Items {
		got = append(got, publishedIDs(item))
	}
	want := []int64{3, 4, 1, 2}
	if len(got) != len(want) {
		t.Fatalf("条数错误：%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("排序错误：期望 %v，实际 %v", want, got)
		}
	}
	if card.Items[3].Published != plugutil.DashText {
		t.Errorf("没有发布时间的简报应显示 %q，实际 %q", plugutil.DashText, card.Items[3].Published)
	}
}

// publishedIDs 把某一行的序号换回它的 ID（发布时间的顺序唯一对应 ID，用于断言排序）。
// 只吃 DispatchItem：卡片本身对这个映射没有用，之前的 card 参数一直没被使用。
func publishedIDs(item DispatchItem) int64 {
	return dispatchIDByOrdinal[item.Ordinal]
}

// dispatchIDByOrdinal 让排序用例把序号映射回 ID；序号从 1 开始。
var dispatchIDByOrdinal = map[string]int64{"1": 3, "2": 4, "3": 1, "4": 2}

// truncatedItems 统计卡片里被截断的条数。
// 卡片上不再带这个计数字段（用户 2026-09-17 要求删掉「本次展示」那组统计），用例自己数，
// 顺带把「卡片不再多存一份统计」这件事钉住。
func truncatedItems(card DispatchesCard) int {
	n := 0
	for _, item := range card.Items {
		if item.Truncated {
			n++
		}
	}
	return n
}

// TestBuildDispatchesCardCleansAndTruncates 校验正文里的游戏内标记被清理、超长正文被截断并计数。
func TestBuildDispatchesCardCleansAndTruncates(t *testing.T) {
	long := strings.Repeat("字", maxDispatchRunes+50)
	list := []hd2.Dispatch{
		{ID: 2, Published: testFetchedAt().UTC().Add(-time.Hour), Message: long},
		{ID: 1, Published: testFetchedAt().UTC(), Message: "<i=3>标题</i>\n\n正文"},
	}
	card := BuildDispatchesCard(list, 2, testFetchedAt(), false)

	if strings.Contains(card.Items[0].Message, "<i=3>") {
		t.Errorf("游戏内标记应被清理：%q", card.Items[0].Message)
	}
	if !strings.Contains(card.Items[0].Message, "标题") {
		t.Errorf("清理不应吃掉正文：%q", card.Items[0].Message)
	}
	if !card.Items[1].Truncated {
		t.Error("超长正文应标记为已截断")
	}
	if len([]rune(strings.TrimSuffix(card.Items[1].Message, "…"))) != maxDispatchRunes {
		t.Errorf("截断长度应为 %d 个字符：%d", maxDispatchRunes, len([]rune(card.Items[1].Message)))
	}
	if got := truncatedItems(card); got != 1 {
		t.Errorf("截断条数应为 1，实际 %d", got)
	}
}

// TestBuildDispatchesCardClampsCount 校验条数在 Builder 里也会被夹到合法范围：
// 直接调用 Builder（绕过 /dispatches 的参数解析）也不会画出超长卡片。
func TestBuildDispatchesCardClampsCount(t *testing.T) {
	var list []hd2.Dispatch
	for i := 0; i < maxDispatchCount+5; i++ {
		list = append(list, hd2.Dispatch{ID: int64(i), Published: testFetchedAt().UTC().Add(-time.Duration(i) * time.Minute), Message: "正文"})
	}
	if got := len(BuildDispatchesCard(list, 99, testFetchedAt(), false).Items); got != maxDispatchCount {
		t.Errorf("超上界应夹到 %d 条，实际 %d 条", maxDispatchCount, got)
	}
	if got := len(BuildDispatchesCard(list, 0, testFetchedAt(), false).Items); got != defaultDispatchCount {
		t.Errorf("0 应退回默认 %d 条，实际 %d 条", defaultDispatchCount, got)
	}
	if got := len(BuildDispatchesCard(list, -3, testFetchedAt(), false).Items); got != defaultDispatchCount {
		t.Errorf("负数应退回默认 %d 条，实际 %d 条", defaultDispatchCount, got)
	}
}

// TestBuildAssignmentsCardFields 校验指令卡片的字段格式化与排序（快过期的在前，没截止时间的最后）。
func TestBuildAssignmentsCardFields(t *testing.T) {
	card := BuildAssignmentsCard(testAssignments(), testFetchedAt(), false, nil)

	if card.Count != "2" || card.Empty {
		t.Fatalf("条数/空标记错误：%+v", card)
	}
	if len(card.Items) != 2 {
		t.Fatalf("应有 2 条指令，实际 %d 条", len(card.Items))
	}
	first := card.Items[0]
	if first.Title != "清剿机器人" {
		t.Errorf("有截止时间的指令应排在最前，实际 %q", first.Title)
	}
	if first.Expiration != "2026-09-19 20:00" {
		t.Errorf("截止时间应按展示时区格式化，实际 %q", first.Expiration)
	}
	if len(first.Tasks) != 1 {
		t.Fatalf("样本指令应有 1 条任务，实际 %d 条", len(first.Tasks))
	}
	// 任务不再写成「类型 3（数值 …）」：阵营进任务名，目标值与当前进度一起进数值。
	if first.Tasks[0].Title != "消灭机器人敌人" {
		t.Errorf("任务名应解出阵营，实际 %q", first.Tasks[0].Title)
	}
	if first.Tasks[0].Numbers != "1,234,567 / 200,000,000（0.6%）" {
		t.Errorf("任务数值应为「当前 / 目标（百分比）」，实际 %q", first.Tasks[0].Numbers)
	}
	if !first.Tasks[0].Bar || first.Tasks[0].Percent != 0.6 {
		t.Errorf("有当前值也有目标值时应画进度条，实际 bar=%v percent=%v", first.Tasks[0].Bar, first.Tasks[0].Percent)
	}
	if first.Reward != "勋章 ×400" {
		t.Errorf("奖励应按类别编号认成货币名（主源不给 id32），实际 %q", first.Reward)
	}
	if len(card.Items[1].Tasks) != 0 {
		t.Errorf("上游没给任务时不该凭空造出任务：%+v", card.Items[1].Tasks)
	}
	if card.Items[1].Expiration != "未知" {
		t.Errorf("没有截止时间应显示「未知」，实际 %q", card.Items[1].Expiration)
	}
}

// TestRewardText 校验奖励文案：先按 id32 认货币名（补充源才给这个字段），没有 id32 时按类别编号认，
// 两级都认不出才退回上游原值，完全没给时写「—」。
// 之前这里印的是「数量 400（类型 0）」——「类型 0」是上游的奖励类别数字，群里没人读得懂。
func TestRewardText(t *testing.T) {
	cases := []struct {
		name   string
		reward hd2.Reward
		want   string
	}{
		// 主源实测形态：只有 type/amount（2026-09-18 响应 reward = {type: 1, amount: 40}）。
		{"主源只给类别时认勋章", hd2.Reward{Type: 1, Amount: 40}, "勋章 ×40"},
		{"按 id32 认勋章", hd2.Reward{Type: 1, Amount: 40, ID32: 897894480}, "勋章 ×40"},
		{"按 id32 认样本", hd2.Reward{Type: 1, Amount: 250, ID32: 2985106497}, "稀有样本 ×250"},
		{"没收录的类别退回原值", hd2.Reward{Type: 7, Amount: 3}, "数量 3（类型 7）"},
		{"认不出的 id32 退回原值", hd2.Reward{Type: 7, Amount: 3, ID32: 424242}, "数量 3（类型 7）"},
		{"上游没给奖励", hd2.Reward{}, plugutil.DashText},
	}
	for _, c := range cases {
		if got := rewardText(c.reward); got != c.want {
			t.Errorf("%s：rewardText = %q，期望 %q", c.name, got, c.want)
		}
	}
}

// TestBuildAssignmentsCardEmpty 校验空列表生成占位卡数据，而不是空指针或错误。
func TestBuildAssignmentsCardEmpty(t *testing.T) {
	card := BuildAssignmentsCard(nil, testFetchedAt(), false, nil)
	if !card.Empty {
		t.Error("空列表应标记 Empty")
	}
	if card.Count != "0" {
		t.Errorf("空列表条数应为 0，实际 %q", card.Count)
	}
	if len(card.Items) != 0 {
		t.Errorf("空列表不应有内容：%+v", card.Items)
	}
	if card.Title != "重要指令" {
		t.Errorf("占位卡标题错误：%q", card.Title)
	}
}

// TestOrdersCardHTML 校验模板把视图模型排成预期内容。
// 走 render.HTML 而不是真浏览器：漏字段、数字没格式化到位、正文没转义，这里就能发现。
func TestOrdersCardHTML(t *testing.T) {
	dispatches, err := render.HTML(render.Card{
		Name: dispatchesCardName,
		Data: BuildDispatchesCard(testDispatches(), defaultDispatchCount, testFetchedAt(), false),
	})
	if err != nil {
		t.Fatalf("渲染简报卡片 HTML 失败：%v", err)
	}
	for _, want := range []string{
		"战役简报",                 // 标题，来自内嵌的 render.Meta
		"2026-09-16 20:00:00",  // 数据时间
		`class="card__emblem"`, // 徽标（游戏原生 SVG）内联进卡片
		"第 1 条", "第 3 条",       // 序号
		"MAJOR ORDER WON", // 正文（游戏内标记已清理）
		"2026-09-16",      // 每条发布时间
	} {
		if !strings.Contains(dispatches, want) {
			t.Errorf("简报卡片 HTML 缺少 %q", want)
		}
	}
	// 「本次展示」那组统计不再上卡（用户 2026-09-17 要求）。
	for _, unwanted := range []string{"展示条数", "上游简报总数", "已截断正文", "本次展示"} {
		if strings.Contains(dispatches, unwanted) {
			t.Errorf("简报卡片不该再出现 %q", unwanted)
		}
	}
	if strings.Contains(dispatches, "<i=3>") {
		t.Error("卡片上不应出现游戏内标记")
	}
	if strings.Contains(dispatches, "数据可能已过期") {
		t.Error("实时数据不应出现过期角标")
	}

	assignments, err := render.HTML(render.Card{
		Name: assignmentsCardName,
		Data: BuildAssignmentsCard(testAssignments(), testFetchedAt(), true, nil),
	})
	if err != nil {
		t.Fatalf("渲染指令卡片 HTML 失败：%v", err)
	}
	for _, want := range []string{
		"重要指令", "清剿机器人",
		"消灭机器人敌人", "1,234,567 / 200,000,000（0.6%）", // 任务已解码，不再是「类型 3（数值 …）」
		`class="bar__fill"`, // 有当前值也有目标值：卡片上要有一条进度条
		"勋章 ×400", "2026-09-19 20:00", "数据可能已过期",
	} {
		if !strings.Contains(assignments, want) {
			t.Errorf("指令卡片 HTML 缺少 %q", want)
		}
	}
}

// TestOrdersCardHTMLEscapesUpstreamText 校验上游字段里的标签与脚本被转义，不会注入卡片。
// 标题字段不做标记清理（它不是游戏内富文本），所以正是注入的入口，必须靠模板转义挡住。
func TestOrdersCardHTMLEscapesUpstreamText(t *testing.T) {
	injection := `<script>alert(1)</script>`
	list := []hd2.Assignment{{ID: 1, Title: injection, Briefing: "正文"}}
	html, err := render.HTML(render.Card{Name: assignmentsCardName, Data: BuildAssignmentsCard(list, testFetchedAt(), false, nil)})
	if err != nil {
		t.Fatalf("渲染指令卡片 HTML 失败：%v", err)
	}
	if strings.Contains(html, "<script>") {
		t.Error("上游字段里的脚本标签必须被转义")
	}
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Errorf("应保留转义后的标题：%s", html)
	}
}

// TestDispatchRunesPerItem 校验每条正文的上限随展示条数缩放：条数少时给满，条数多时按预算摊薄。
//
// 期望值刻意写字面量（而不是直接引用常量）：这条用例的职责是把 S4 的定值钉死
// （1 条 → 1500、3 条 → 1400、5 条 → 840、10 条 → 420），把常量改回旧值必须变红。
func TestDispatchRunesPerItem(t *testing.T) {
	cases := []struct{ count, want int }{
		{0, 1500}, // 空列表按 1 条算，避免除零
		{1, 1500},
		{2, 1500}, // 4200/2 = 2100 超过单条上限，夹到 1500
		{3, 1400},
		{4, 1050},
		{5, 840},
		{10, 420},
		{99, 420}, // 一页最多 10 条，超出按 10 条算
	}
	for _, c := range cases {
		if got := dispatchRunesPerItem(c.count); got != c.want {
			t.Errorf("%d 条时每条上限应为 %d，实际 %d", c.count, c.want, got)
		}
	}
	// 单条上限必须够装真实上游最长正文（实测 807 字），否则默认 3 条时就会出现截断。
	if maxDispatchRunes < 807 {
		t.Errorf("单条上限 %d 应不小于真实最长正文 807 字", maxDispatchRunes)
	}
}

// TestBuildDispatchesCardKeepsUpstreamLongestMessage 校验真实上游最长的简报正文（807 字）
// 在默认 3 条时完整进卡片：不算截断、没有截断计数、也没有「正文过长，已截断显示。」。
//
// 依据：真实上游实测最长正文 807 字（S4 设计 §2），而 3 条时每条上限 1400 字。
func TestBuildDispatchesCardKeepsUpstreamLongestMessage(t *testing.T) {
	const longest = 807
	body := strings.Repeat("字", longest)
	list := make([]hd2.Dispatch, 0, defaultDispatchCount)
	for i := 0; i < defaultDispatchCount; i++ {
		list = append(list, hd2.Dispatch{
			ID:        int64(100 - i),
			Published: testFetchedAt().UTC().Add(-time.Duration(i) * time.Hour),
			Message:   body,
		})
	}
	card := BuildDispatchesCard(list, defaultDispatchCount, testFetchedAt(), false)

	for i, item := range card.Items {
		if item.Truncated {
			t.Errorf("第 %d 条不该被截断（807 字在每条预算之内）", i+1)
		}
		if got := len([]rune(item.Message)); got != longest {
			t.Errorf("第 %d 条应完整保留 %d 字，实际 %d 字", i+1, longest, got)
		}
	}
	if got := truncatedItems(card); got != 0 {
		t.Errorf("没有截断时计数应为 0，实际 %d", got)
	}
	if card.Note != "" {
		t.Errorf("没有截断时不该出现口径说明，实际 %q", card.Note)
	}
	if text := FormatDispatchesText(card); strings.Contains(text, "正文过长") || strings.Contains(text, "已截断") {
		t.Errorf("文本回退里不该出现截断提示：\n%s", text)
	}
	html, err := render.HTML(render.Card{Name: dispatchesCardName, Data: card})
	if err != nil {
		t.Fatalf("渲染简报卡片 HTML 失败：%v", err)
	}
	if strings.Contains(html, "正文过长，已截断显示。") {
		t.Error("卡片模板里不该出现截断提示")
	}
}

// TestDispatchRunesTerminalLimitBoundary 校验单条正文的终局防线边界：
// 正好 1500 字不截、1501 字才截。这是 S4 唯一保留的截断路径（正常内容碰不到），边界必须钉住。
func TestDispatchRunesTerminalLimitBoundary(t *testing.T) {
	build := func(text string) DispatchesCard {
		list := []hd2.Dispatch{{ID: 1, Published: testFetchedAt().UTC(), Message: text}}
		return BuildDispatchesCard(list, 1, testFetchedAt(), false)
	}

	exact := build(strings.Repeat("字", 1500))
	if exact.Items[0].Truncated || truncatedItems(exact) != 0 || exact.Note != "" {
		t.Fatalf("正好 1500 字不该截断：truncated=%v count=%d note=%q",
			exact.Items[0].Truncated, truncatedItems(exact), exact.Note)
	}
	if got := len([]rune(exact.Items[0].Message)); got != 1500 {
		t.Fatalf("正好 1500 字应原样保留，实际 %d 字", got)
	}

	over := build(strings.Repeat("字", 1501))
	if !over.Items[0].Truncated {
		t.Fatal("1501 字应触发终局截断")
	}
	// 截断结果是 1500 字 + 省略号。
	if got := len([]rune(over.Items[0].Message)); got != 1501 {
		t.Fatalf("截断后应为 1500 字加省略号，实际 %d 字", got)
	}
	if truncatedItems(over) != 1 || !strings.Contains(over.Note, "1500 字") {
		t.Fatalf("应写清每条上限与截断条数，实际 count=%d note=%q", truncatedItems(over), over.Note)
	}
}

// TestBuildAssignmentBriefingTerminalLimitBoundary 校验指令简报的终局防线边界：
// 正好 1500 字不截、1501 字才截。真实上游简报只有两百多字，这条防线只为异常数据存在。
func TestBuildAssignmentBriefingTerminalLimitBoundary(t *testing.T) {
	exact := buildAssignmentItem(hd2.Assignment{Briefing: strings.Repeat("字", 1500)}, testZone(), nil)
	if got := len([]rune(exact.Briefing)); got != 1500 {
		t.Fatalf("正好 1500 字的指令简报应原样保留，实际 %d 字", got)
	}
	over := []rune(buildAssignmentItem(hd2.Assignment{Briefing: strings.Repeat("字", 1501)}, testZone(), nil).Briefing)
	if len(over) != 1501 || over[len(over)-1] != 0x2026 {
		t.Fatalf("1501 字的指令简报应截到 1500 字并补省略号，实际 %d 字", len(over))
	}
}

// TestBuildDispatchesCardShrinksLimitWithCount 校验 10 条时正文按更小的上限截断，并把这套口径
// 同时写进卡片与文本回退，两处说同一句话。
//
// S4 口径：安全线是 render.MaxCardHeightPx = 7000px，条数自适应只为把最坏情况压在安全线内
// （真浏览器实测 10 条 × 420 字 = 6112px，远低于安全线；10 条 × 807 字会画到 9500px 上下），
// 真实内容不再被截。
func TestBuildDispatchesCardShrinksLimitWithCount(t *testing.T) {
	long := strings.Repeat("字", maxDispatchRunes+50)
	list := make([]hd2.Dispatch, 0, maxDispatchCount)
	for i := 0; i < maxDispatchCount; i++ {
		list = append(list, hd2.Dispatch{
			ID:        int64(100 - i),
			Published: testFetchedAt().UTC().Add(-time.Duration(i) * time.Hour),
			Message:   long,
		})
	}
	card := BuildDispatchesCard(list, maxDispatchCount, testFetchedAt(), false)

	if got := truncatedItems(card); got != 10 {
		t.Fatalf("10 条超长正文都应计入截断，实际 %d", got)
	}
	for i, item := range card.Items {
		if !item.Truncated {
			t.Fatalf("第 %d 条应标记为已截断", i+1)
		}
		if got := len([]rune(strings.TrimSuffix(item.Message, "…"))); got != minDispatchRunes {
			t.Errorf("第 %d 条应截到 %d 字，实际 %d 字", i+1, minDispatchRunes, got)
		}
	}
	if !strings.Contains(card.Note, "420 字") || !strings.Contains(card.Note, "10 条") {
		t.Errorf("卡片应说明本次每条最多多少字、截断了几条，实际 %q", card.Note)
	}
	if text := FormatDispatchesText(card); !strings.Contains(text, bot.Escape(card.Note)) {
		t.Errorf("文本回退应与卡片同一口径：\n%s", text)
	}
	html, err := render.HTML(render.Card{Name: dispatchesCardName, Data: card})
	if err != nil {
		t.Fatalf("渲染简报卡片 HTML 失败：%v", err)
	}
	if !strings.Contains(html, "本次每条最多显示 420 字") {
		t.Error("卡片上没有出现截断口径说明")
	}

	// 没有截断时不该留这行说明（否则每次都要读一句无关的提示）。
	short := BuildDispatchesCard(testDispatches(), defaultDispatchCount, testFetchedAt(), false)
	if short.Note != "" {
		t.Errorf("没有截断时不应有口径说明：%q", short.Note)
	}
}

// TestBuildTranslatedDispatchesTruncatesTranslatedText 校验译文也会按同一预算截断：
// 中文信息密度高，但长度不受控的译文同样会把卡片拉长。
func TestBuildTranslatedDispatchesTruncatesTranslatedText(t *testing.T) {
	list := []hd2.Dispatch{{ID: 1, Published: testFetchedAt().UTC(), Message: strings.Repeat("Long English text. ", 40)}}
	trans := &plugtest.Translator{Out: []string{strings.Repeat("很长的中文译文。", 200)}}
	card := BuildTranslatedDispatches(context.Background(), list, 1, testFetchedAt(), false, trans)
	if len(card.Items) != 1 {
		t.Fatalf("应有 1 条，实际 %d", len(card.Items))
	}
	if !card.Items[0].Truncated {
		t.Fatal("超长译文应被标记为已截断")
	}
	if runes := len([]rune(card.Items[0].Message)); runes > maxDispatchRunes+1 {
		t.Fatalf("译文长度应受同一预算限制，实际 %d 字", runes)
	}
}

// TestBuildTranslatedDispatchesBudgetBoundary 校验译文截断的边界：恰好等于预算不截断、多一个字才截断；
// 条数变多时预算同样按条数收缩（与英文侧同一套口径，两处不能各算一套）。
func TestBuildTranslatedDispatchesBudgetBoundary(t *testing.T) {
	perItem := dispatchRunesPerItem(1) // 1 条时是上限 maxDispatchRunes
	list := []hd2.Dispatch{{ID: 1, Published: testFetchedAt().UTC(), Message: "Hello."}}

	exact := BuildTranslatedDispatches(context.Background(), list, 1, testFetchedAt(), false,
		&plugtest.Translator{Out: []string{strings.Repeat("字", perItem)}})
	if exact.Items[0].Truncated {
		t.Fatalf("译文恰好 %d 字时不应截断", perItem)
	}
	if got := len([]rune(exact.Items[0].Message)); got != perItem {
		t.Fatalf("恰好等于预算时应原样保留译文，实际 %d 字", got)
	}
	if exact.TranslateNote != "" || exact.Note != "" || truncatedItems(exact) != 0 {
		t.Fatalf("没有截断也没有缺失时不该有说明：note=%q translate=%q count=%d",
			exact.Note, exact.TranslateNote, truncatedItems(exact))
	}

	over := BuildTranslatedDispatches(context.Background(), list, 1, testFetchedAt(), false,
		&plugtest.Translator{Out: []string{strings.Repeat("字", perItem+1)}})
	if !over.Items[0].Truncated {
		t.Fatalf("译文 %d 字时应截断", perItem+1)
	}
	if got := len([]rune(over.Items[0].Message)); got != perItem+1 {
		t.Fatalf("截断后应为 %d 个字（含省略号），实际 %d", perItem+1, got)
	}
	// 译文的截断发生在英文截断之后：被截断条数与说明必须重算，否则卡片上留着英文侧算出的数字。
	if truncatedItems(over) != 1 || over.Note == "" {
		t.Fatalf("译后被截断时应重算条数与说明，实际 count=%d note=%q", truncatedItems(over), over.Note)
	}

	// 多条时预算收缩：10 条时每条最多 minDispatchRunes 字，超长译文同样被压到这个上限。
	many := make([]hd2.Dispatch, 0, maxDispatchCount)
	long := make([]string, 0, maxDispatchCount)
	for i := 0; i < maxDispatchCount; i++ {
		many = append(many, hd2.Dispatch{
			ID:        int64(100 - i),
			Published: testFetchedAt().UTC().Add(-time.Duration(i) * time.Hour),
			Message:   "Hello.",
		})
		long = append(long, strings.Repeat("字", maxDispatchRunes))
	}
	shrunk := BuildTranslatedDispatches(context.Background(), many, maxDispatchCount, testFetchedAt(), false,
		&plugtest.Translator{Out: long})
	if got := truncatedItems(shrunk); got != 10 {
		t.Fatalf("10 条超长译文都应计入截断，实际 %d", got)
	}
	for i, item := range shrunk.Items {
		if !item.Truncated {
			t.Fatalf("第 %d 条应标记为已截断", i+1)
		}
		if got := len([]rune(strings.TrimSuffix(item.Message, "…"))); got != minDispatchRunes {
			t.Errorf("第 %d 条应截到 %d 字，实际 %d 字", i+1, minDispatchRunes, got)
		}
	}
}

// TestBuildTranslatedDispatchesDegenerateInputs 校验退化输入：nil 翻译层、空列表、条数非法都不 panic，
// 且不该白白调用翻译层——一条简报都没有时还去请求翻译，只是给后端添乱。
func TestBuildTranslatedDispatchesDegenerateInputs(t *testing.T) {
	// 空列表：没有正文可送，一次也不该调用翻译层。
	empty := &plugtest.Translator{Out: []string{"不该被用到"}}
	if card := BuildTranslatedDispatches(context.Background(), nil, 3, testFetchedAt(), false, empty); len(card.Items) != 0 || card.TranslateNote != "" {
		t.Fatalf("空列表应返回空卡片且不注明，实际 %+v", card)
	}
	if empty.Calls() != 0 {
		t.Fatalf("空列表不该调用翻译层，实际调用 %d 次", empty.Calls())
	}

	// nil 翻译层：等价于「关掉翻译」，正文保持英文且不注明（不能把「没开翻译」说成「翻译坏了」）。
	nilCard := BuildTranslatedDispatches(context.Background(), testDispatches(), 1, testFetchedAt(), false, nil)
	if nilCard.TranslateNote != "" {
		t.Fatalf("nil 翻译层不应注明翻译不可用，实际 %q", nilCard.TranslateNote)
	}
	if !strings.Contains(nilCard.Items[0].Message, "MAJOR ORDER WON") {
		t.Fatalf("nil 翻译层应出英文原文，实际 %q", nilCard.Items[0].Message)
	}

	// 条数非法：与 BuildDispatchesCard 同口径（<=0 退回默认 3 条，超过上界夹到 10 条），
	// 翻译层拿到的段数必须跟着展示条数走，不能把整批简报都送译。
	for _, c := range []struct {
		name  string
		count int
		want  int
	}{
		{"零条退回默认 3 条", 0, defaultDispatchCount},
		{"负数退回默认 3 条", -5, defaultDispatchCount},
		{"超过上界夹到 10 条", 100, maxDispatchCount},
	} {
		t.Run(c.name, func(t *testing.T) {
			trans := &plugtest.Translator{}
			card := BuildTranslatedDispatches(context.Background(), testDispatches(), c.count, testFetchedAt(), false, trans)
			want := c.want
			if want > len(testDispatches()) {
				want = len(testDispatches()) // 上游只有 5 条，要 10 条也只能给 5 条
			}
			if len(card.Items) != want {
				t.Fatalf("应展示 %d 条，实际 %d 条", want, len(card.Items))
			}
			if trans.Calls() != 1 {
				t.Fatalf("应只调用翻译层一次，实际 %d 次", trans.Calls())
			}
			if got := len(trans.Texts[0]); got != want {
				t.Fatalf("送译段数应与展示条数一致（%d 段），实际 %d 段", want, got)
			}
			// 替身没给译文（Out 为空），plugtest.Translator 会把每段原样回显（段数仍然等长），
			// 这条「译文与原文相同」的退化必须被认成翻译不可用而不是照译。
			if card.TranslateNote != translateUnavailableNote {
				t.Fatalf("段数不符时应注明翻译不可用，实际 %q", card.TranslateNote)
			}
		})
	}
}

// TestBuildTranslatedDispatchesFallsBackWhenNotTranslated 校验「没翻动」的两种退化：
// 译文与原文完全相同（后端原样回显）、译文只有空白，都保留英文原文并把整卡标成翻译不可用；
// 同一批里真正翻好的那几条照常换成中文。
func TestBuildTranslatedDispatchesFallsBackWhenNotTranslated(t *testing.T) {
	list := []hd2.Dispatch{
		{ID: 3, Published: testFetchedAt().UTC(), Message: "Echo."},
		{ID: 2, Published: testFetchedAt().UTC().Add(-time.Hour), Message: "Blank."},
		{ID: 1, Published: testFetchedAt().UTC().Add(-2 * time.Hour), Message: "Real."},
	}
	trans := &plugtest.Translator{Out: []string{"Echo.", "   ", "真的翻了。"}}
	card := BuildTranslatedDispatches(context.Background(), list, 3, testFetchedAt(), false, trans)

	for i, want := range []string{"Echo.", "Blank.", "真的翻了。"} {
		if got := card.Items[i].Message; got != want {
			t.Errorf("第 %d 条应为 %q，实际 %q", i+1, want, got)
		}
	}
	// 只要有段落没翻动，卡片就要注明：群里看到英文时能知道不是机器人坏了。
	if card.TranslateNote != translateUnavailableNote {
		t.Fatalf("有段落没翻动时应注明，实际 %q", card.TranslateNote)
	}
	if !strings.Contains(FormatDispatchesText(card), bot.Escape(translateUnavailableNote)) {
		t.Errorf("文本回退应带同口径说明：\n%s", FormatDispatchesText(card))
	}
	if truncatedItems(card) != 0 || card.Note != "" {
		t.Errorf("没有截断就不该有截断说明：count=%d note=%q", truncatedItems(card), card.Note)
	}
}

// TestDispatchesTranslateNoteInTemplate 校验翻译说明在两条渲染路径上口径一致：
// 有说明时卡片 HTML 与文本回退都出现同一句；没有说明时模板不渲染空区块（否则卡片上白占一行）。
func TestDispatchesTranslateNoteInTemplate(t *testing.T) {
	failed := BuildTranslatedDispatches(context.Background(), testDispatches(), 1, testFetchedAt(), false,
		&plugtest.Translator{Err: errors.New("翻译后端挂了")})
	if failed.TranslateNote == "" {
		t.Fatal("翻译失败时卡片应有说明")
	}
	html, err := render.HTML(render.Card{Name: dispatchesCardName, Data: failed})
	if err != nil {
		t.Fatalf("渲染简报卡片 HTML 失败：%v", err)
	}
	if !strings.Contains(html, translateUnavailableNote) {
		t.Errorf("卡片 HTML 应带上翻译说明：\n%s", html)
	}
	if !strings.Contains(FormatDispatchesText(failed), bot.Escape(translateUnavailableNote)) {
		t.Errorf("文本回退应带上同一句翻译说明：\n%s", FormatDispatchesText(failed))
	}

	ok := BuildTranslatedDispatches(context.Background(), testDispatches(), 1, testFetchedAt(), false,
		&plugtest.Translator{Out: []string{"重大指令达成：取得胜利。"}})
	if ok.TranslateNote != "" {
		t.Fatalf("翻译成功时不应有说明：%q", ok.TranslateNote)
	}
	html, err = render.HTML(render.Card{Name: dispatchesCardName, Data: ok})
	if err != nil {
		t.Fatalf("渲染简报卡片 HTML 失败：%v", err)
	}
	if strings.Contains(html, translateUnavailableNote) {
		t.Error("翻译成功时卡片上不应出现翻译说明")
	}
	if strings.Contains(html, `<div class="muted"></div>`) {
		t.Error("没有说明时不应渲染空区块")
	}
	if !strings.Contains(html, "重大指令达成：取得胜利。") {
		t.Errorf("卡片 HTML 应带上译文：\n%s", html)
	}
}
