package orders

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"hd2_bot/src/hd2"
	"hd2_bot/src/plugins/plugutil"
	"hd2_bot/src/render"
	"hd2_bot/src/translate"
)

// 卡片名必须与 src/render/templates/<名字>.tmpl 的文件名一致（写错会在启动期 panic）。
const (
	assignmentsCardName = "assignments"
	dispatchesCardName  = "dispatches"
)

// 卡片头栏的徽标素材逻辑名；取值必须来自 src/render/assets.go 的 assetFiles 登记表。
const (
	assignmentsEmblem = "emblem.major_order" // assets/game/ui/liberation_campaign.svg（游戏原生图标）
	dispatchesEmblem  = "emblem.super_earth" // assets/game/faction/humans.svg（超级地球徽标，游戏原生图标）
)

// 简报正文的展示预算（S4 口径）：正文不再按「两行摘要」截断，真实上游内容要完整画进卡片。
//
// 高度安全线是 render.MaxCardHeightPx = 7000px（见 src/render/limits.go），而「条数 × 每条字数」
// 不能同时拉满：真浏览器实测（1800px 宽、满屏中文，约 49px/行）3 条 × 807 字 = 3254px、
// 10 条 × 420 字 = 6112px；而 10 条 × 807 字按同样的每行高度换算会画出 9500px 上下的图，直接顶穿。
// 所以每条的字数上限仍按本次展示条数反比缩放：整卡正文预算 dispatchRunesBudget 字摊到每条，
// 下限 minDispatchRunes、上限 maxDispatchRunes。效果：1~2 条时上限 1500 字、3 条时 1400 字
// （真实最长正文 807 字，一条都截不到）、5 条时每条 840 字、10 条时每条 420 字。
//
// 取值依据（真实上游实测 1048 条简报）：最长正文 807 字，所以单条上限取 1500（约 1.9 倍余量）；
// 条数拉满的实测高度——10 条 × 420 字 6112px、默认 3 条 × 真实最长正文 807 字 3254px（冒烟用例
// TestOrdersCardsSmokeWithRealBrowser 的实测值），都远低于安全线。条数自适应只为把最坏情况压在
// 安全线内，正常内容不触发任何截断：真要被截断，说明上游给出了异常长的内容，那属于终局防线的职责。
const (
	maxDispatchRunes    = 1500
	minDispatchRunes    = 420
	dispatchRunesBudget = 4200
)

// maxAssignmentRunes 是一条重要指令简报的最大字数：真实上游简报最长两百多字，
// 1500 字是终局防线，只为挡住「上游突然给出公告体长文」，正常简报不会被它截断。
const maxAssignmentRunes = 1500

// DispatchItem 是简报列表里的一条。
type DispatchItem struct {
	Ordinal   string // 序号，从 1 开始（仅为方便在群里说「第 2 条」）
	Published string // 发布时间（展示时区），上游没给时显示「—」
	Message   string // 已清理游戏内标记并按 maxDispatchRunes 截断的正文
	Truncated bool   // 正文是否被截断（卡片上会标出来）
}

// DispatchesCard 是 templates/dispatches.tmpl 的视图模型，字段都是格式化好的字符串，模板只排版。
type DispatchesCard struct {
	render.Meta
	Note          string         // 截断口径说明（每条最多多少字）；没有截断时为空
	TranslateNote string         // 正文翻译不可用时的说明；翻译成功或未开启翻译时为空
	Items         []DispatchItem // 已按发布时间倒序并截断
}

// AssignmentItem 是一条重要指令。（实测 /assignments 返回 []），
// 所以字段设计以「上游真给了数据也能看」为准，不为了占位卡做额外假设。
type AssignmentItem struct {
	Title      string // 标题；上游没给时「未命名指令」
	Briefing   string // 简报（缺省时退回 description），已清理标记并截断
	Tasks      string // 任务明细；上游只给枚举值与数值，含义未公布，按「类型 N（数值 X）」展示
	Reward     string // 奖励；上游只给类型与数量，含义未公布
	Expiration string // 截止时间（展示时区），上游没给时「未知」
	Progress   string // 进度数值；没有时不显示这一行
}

// AssignmentsCard 是 templates/assignments.tmpl 的视图模型。
type AssignmentsCard struct {
	render.Meta
	Count string           // 进行中的指令条数
	Empty bool             // 上游返回空列表：渲染占位卡，这不是错误
	Items []AssignmentItem // 已按截止时间升序（没有截止时间的排在最后）
	Notes []string         // 卡片底部的口径说明（例如「枚举含义上游未公布」）
}

// dispatchRunesPerItem 返回本次每条简报允许的正文长度上限：整卡正文预算按条数摊分，
// 再夹到 [minDispatchRunes, maxDispatchRunes]。条数为 0（空列表）时按 1 条算，避免除零。
func dispatchRunesPerItem(count int) int {
	if count < 1 {
		count = 1
	}
	if count > maxDispatchCount {
		count = maxDispatchCount
	}
	per := dispatchRunesBudget / count
	if per < minDispatchRunes {
		per = minDispatchRunes
	}
	if per > maxDispatchRunes {
		per = maxDispatchRunes
	}
	return per
}

// truncationNote 拼卡片上的截断口径说明；没有截断时返回空串（模板据此不显示这一行）。
// 说明里带上「每条最多多少字」：群里看到半句话时会知道这是卡片的容量限制，不是上游只给了这些。
func truncationNote(perItem, truncated int) string {
	if truncated <= 0 {
		return ""
	}
	return fmt.Sprintf("为控制卡片高度，本次每条最多显示 %d 字，其中 %d 条正文被截断。", perItem, truncated)
}

// BuildDispatchesCard 把简报列表转成卡片视图模型。
//
// count 是本次要展示的条数（调用方已按 DispatchCount 夹到 1..maxDispatchCount，
// 这里再夹一次：Builder 被单独调用时也不会画出超长卡片）。
// fetchedAt 必须是已转换到展示时区的时间（Result.FetchedAt 的时区不统一），
// 简报时间按它的时区展示，卡片上不会出现两个时区混杂的时间。
func BuildDispatchesCard(list []hd2.Dispatch, count int, fetchedAt time.Time, stale bool) DispatchesCard {
	if count < 1 {
		count = defaultDispatchCount
	}
	if count > maxDispatchCount {
		count = maxDispatchCount
	}

	// 先复制再排序：Service 返回的是副本，但展示层不应该就地改动调用方给的数据。
	ordered := make([]hd2.Dispatch, len(list))
	copy(ordered, list)
	// 上游实测按 id 倒序返回（最新在前），但顺序不能靠默契：显式按发布时间倒序排一次，
	// 时间相同时按 ID 倒序兜底，保证「最新 N 条」每次都是同一批、同一个顺序。
	sort.SliceStable(ordered, func(i, j int) bool {
		if !ordered[i].Published.Equal(ordered[j].Published) {
			return ordered[i].Published.After(ordered[j].Published)
		}
		return ordered[i].ID > ordered[j].ID
	})

	show := ordered
	if len(show) > count {
		show = show[:count]
	}

	loc := fetchedAt.Location()
	// 每条的字数上限由本次展示条数决定（见上面常量的说明）：条数越多，每条留的字越少。
	perItem := dispatchRunesPerItem(len(show))
	items := make([]DispatchItem, 0, len(show))
	truncated := 0
	for i, d := range show {
		message, cut := truncateRunes(translate.CleanGameText(d.Message), perItem)
		if cut {
			truncated++
		}
		items = append(items, DispatchItem{
			Ordinal:   strconv.Itoa(i + 1),
			Published: publishedText(d.Published, loc),
			Message:   plugutil.DefaultText(message, "（上游没有给出正文）"),
			Truncated: cut,
		})
	}

	return DispatchesCard{
		Meta: render.Meta{
			Title: "战役简报",
			// 副标题里原本写着「展示最新 N 条 ｜ 上游共 M 条」，用户 2026-09-17 要求不再展示这类统计。
			DataTime: plugutil.FormatDataTime(fetchedAt),
			Stale:    stale,
			Emblem:   dispatchesEmblem,
		},
		Note:  truncationNote(perItem, truncated),
		Items: items,
	}
}

// translateUnavailableNote 是翻译不可用时的说明文案。
// 有了它，群里看到英文正文时能知道「不是机器人坏了，是翻译暂时不可用」。
const translateUnavailableNote = "翻译暂不可用，以下为英文原文。"

// BuildTranslatedDispatches 构造 /dispatches 的最终卡片：
// 先在英文原文上做清理与截断（保持 S2 的长度预算），再把每条正文交给翻译层；
// 译文为空、与原文相同或翻译层返回长度不符时该条保留英文，并在卡片上注明。
//
// 失败不算错误：命令照常出图，正文是英文并带一句说明——这比回一句「查询失败」有用得多。
//
// 「没翻动」的判据是 translate.Moved（原文需要翻译、译文非空白且与原文不同）：
// 正文本来就是中文时不该判成翻译故障，详见 translate.NeedsTranslation 的注释。
//
// 边界：
//   - trans 为 nil（配置里关掉翻译）时直接返回英文卡片，不调用翻译层、也不写说明（那不是「翻译坏了」）；
//   - 空列表不调用翻译层：没有正文可送，不该为一次空查询去碰后端；
//   - 译文的长度不受控，必须像英文一样按 maxDispatchRunes 截断（真的超出上限时宁可截断，
//     也不要一张超过 render.MaxCardHeightPx 的图——那种图引擎会直接报错、整卡退回文本）。
func BuildTranslatedDispatches(ctx context.Context, list []hd2.Dispatch, count int, fetchedAt time.Time, stale bool, trans translate.Translator) DispatchesCard {
	card := BuildDispatchesCard(list, count, fetchedAt, stale)
	if trans == nil || len(card.Items) == 0 {
		return card
	}

	texts := make([]string, 0, len(card.Items))
	for _, item := range card.Items {
		texts = append(texts, item.Message)
	}
	translated, err := trans.Translate(ctx, texts)
	if err != nil {
		log.Printf("/dispatches 正文翻译失败，保留英文 err=%v", err)
	}
	if len(translated) != len(texts) {
		// 段数不符时宁可整批保留英文：按位置硬塞会把 A 的译文贴到 B 的正文上。
		log.Printf("/dispatches 翻译层返回 %d 段，期望 %d 段，保留英文", len(translated), len(texts))
		card.TranslateNote = translateUnavailableNote
		return card
	}

	// 每条的字数预算与英文一致：译文长度不受控，必须再截一次。
	perItem := dispatchRunesPerItem(len(card.Items))
	missed := 0
	for i := range card.Items {
		source := card.Items[i].Message
		if !translate.NeedsTranslation(source) {
			// 这一段本来就不需要翻译（正文已是中文，或上游只给了数字与符号）：
			// 翻译层根本没把它送出去，原样回显不是故障，不能计入 missed，
			// 否则每一条中文简报都会被挂上「翻译暂不可用，以下为英文原文」的假告警。
			continue
		}
		if !translate.Moved(source, translated[i]) {
			missed++ // 真的没翻动（后端回显原文或只给了空白），保留英文
			continue
		}
		trimmed, cut := truncateRunes(translated[i], perItem)
		card.Items[i].Message = trimmed
		card.Items[i].Truncated = card.Items[i].Truncated || cut
	}
	if missed > 0 {
		card.TranslateNote = translateUnavailableNote
	}
	card.Note = truncationNote(perItem, countTruncated(card.Items))
	return card
}

// countTruncated 统计被截断的条数（译文的截断发生在英文截断之后，需要重算）。
func countTruncated(items []DispatchItem) int {
	n := 0
	for _, item := range items {
		if item.Truncated {
			n++
		}
	}
	return n
}

// BuildAssignmentsCard 把重要指令转成卡片视图模型。
// list 为空时返回 Empty 卡片（渲染「暂无重要指令」占位卡），而不是报错：
// 上游实测当前就是返回 []，这是正常状态。
func BuildTranslatedAssignments(ctx context.Context, list []hd2.Assignment, fetchedAt time.Time, stale bool, trans translate.Translator) AssignmentsCard {
	card := BuildAssignmentsCard(list, fetchedAt, stale)
	if trans == nil || card.Empty {
		return card
	}
	var texts []string
	for _, item := range card.Items {
		texts = append(texts, item.Title, item.Briefing)
	}
	translated, err := trans.Translate(ctx, texts)
	if err != nil {
		log.Printf("/assignments 翻译失败，未翻译内容保留原文 err=%v", err)
	}
	if len(translated) != len(texts) {
		return card
	}
	for i := range card.Items {
		for j, field := range []*string{&card.Items[i].Title, &card.Items[i].Briefing} {
			if translate.Moved(*field, translated[2*i+j]) {
				*field, _ = truncateRunes(translate.CleanGameText(translated[2*i+j]), maxAssignmentRunes)
			}
		}
	}
	return card
}

func BuildAssignmentsCard(list []hd2.Assignment, fetchedAt time.Time, stale bool) AssignmentsCard {
	ordered := make([]hd2.Assignment, len(list))
	copy(ordered, list)
	// 按截止时间升序（快过期的排前面），没有截止时间的排在最后：群里最关心的是「还剩多久」。
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := ordered[i].Expiration, ordered[j].Expiration
		switch {
		case a == nil && b == nil:
			return false
		case a == nil:
			return false
		case b == nil:
			return true
		default:
			return a.Before(*b)
		}
	})

	loc := fetchedAt.Location()
	items := make([]AssignmentItem, 0, len(ordered))
	for _, a := range ordered {
		items = append(items, buildAssignmentItem(a, loc))
	}

	return AssignmentsCard{
		Meta: render.Meta{
			Title:    "重要指令",
			Subtitle: "Major Order",
			DataTime: plugutil.FormatDataTime(fetchedAt),
			Stale:    stale,
			Emblem:   assignmentsEmblem,
		},
		Count: plugutil.FormatInt(int64(len(items))),
		Empty: len(items) == 0,
		Items: items,
		// 上游只给枚举数字，不给含义：与其猜一个「击杀 2 亿机器人」的说法，不如把口径写在卡片上。
		Notes: []string{"任务与奖励的类型、数值含义上游未公布，卡片按原值展示，不做翻译。"},
	}
}

// buildAssignmentItem 把一条指令转成卡片上的一块内容。
func buildAssignmentItem(a hd2.Assignment, loc *time.Location) AssignmentItem {
	// 这里刻意丢掉 truncateRunes 的截断标志（不往 card.Notes 里加「简报已截断」）：
	// 指令简报本来就只有一两句话（实测远短于 maxAssignmentRunes），截断属于理论路径；
	// 为它加一句说明会让每张指令卡片都多一行小字，收益不抵噪声。真要提示再改成 Notes。
	briefing, _ := truncateRunes(translate.CleanGameText(plugutil.DefaultText(a.Briefing, a.Description)), maxAssignmentRunes)
	return AssignmentItem{
		Title:      plugutil.DefaultText(a.Title, "未命名指令"),
		Briefing:   plugutil.DefaultText(briefing, plugutil.DashText),
		Tasks:      tasksText(a.Tasks),
		Reward:     rewardText(a.Reward),
		Expiration: expirationText(a.Expiration, loc),
		Progress:   numbersText(a.Progress),
	}
}

// tasksText 拼任务明细。上游 Task 只有 type/values/valueTypes 三个数字字段，含义未公布，
// 所以只写「类型 N（数值 X）」：不把 type=1 猜成「击杀」。
func tasksText(tasks []hd2.Task) string {
	if len(tasks) == 0 {
		return plugutil.DashText
	}
	parts := make([]string, 0, len(tasks))
	for _, task := range tasks {
		if len(task.Values) == 0 {
			parts = append(parts, fmt.Sprintf("类型 %d", task.Type))
			continue
		}
		parts = append(parts, fmt.Sprintf("类型 %d（数值 %s）", task.Type, numbersText(task.Values)))
	}
	return strings.Join(parts, "、")
}

// rewardText 拼奖励。同上：Reward 只有 type/amount/id32，含义未公布，只给原值。
func rewardText(reward hd2.Reward) string {
	if reward.Amount == 0 && reward.Type == 0 && reward.ID32 == 0 {
		return plugutil.DashText // 上游没有给出奖励
	}
	return fmt.Sprintf("数量 %s（类型 %d）", plugutil.FormatInt(int64(reward.Amount)), reward.Type)
}

// numbersText 把一组数字拼成「1、2」，空集合返回空串。
func numbersText(values []int64) string {
	if len(values) == 0 {
		return ""
	}
	parts := make([]string, 0, len(values))
	for _, v := range values {
		parts = append(parts, plugutil.FormatInt(v))
	}
	return strings.Join(parts, "、")
}

// expirationText 格式化截止时间；上游没给（nil 或零值）时显示「未知」。
// 格式化与占位口径统一走 plugutil：卡片与文本回退不会对同一个字段说两种话。
func expirationText(t *time.Time, loc *time.Location) string {
	return plugutil.MinuteTimePtrText(t, loc, plugutil.UnknownText)
}

// publishedText 格式化简报发布时间；上游没给时显示「—」。
func publishedText(t time.Time, loc *time.Location) string {
	return plugutil.MinuteTimeText(t, loc, plugutil.DashText)
}

// 正文清洗（游戏内高亮 <i=3>…</i>、HTML 片段、多余空行）自 S3 起统一走 translate.CleanGameText。
// 本包原先自带一份私有实现，与翻译层各留一套规则会出现「同一段正文在卡片里是一个样、送译时是另一个样」，
// 所以这里只保留调用，不再复制第二份正则。清洗规则的用例见 card_test.go 的 TestCleanGameText。

// truncateRunes 把文本截断到 max 个字符（按 rune 截断，中文不会被切坏），
// 超出时在末尾补省略号并返回 true。
//
// max <= 0 的契约是「按 0 字预算处理」：返回空串，并把「原文是否非空」作为截断标志返回
// （即非空原文一律算被截断）。注意 plugins/planets 里有个同名的 truncateText，
// 它的 max <= 0 语义正好相反（不限制、原样返回）——两个函数名字相近、边界相反，
// 改动其中任何一个之前先看清楚包名；本次刻意不统一（会牵动两边一堆断言），
// 只在两侧各留一条用例把各自的契约钉死（本包是 TestTruncateRunes）。
func truncateRunes(text string, max int) (string, bool) {
	trimmed := strings.TrimSpace(text)
	if max <= 0 {
		return "", trimmed != ""
	}
	runes := []rune(trimmed)
	if len(runes) <= max {
		return trimmed, false
	}
	return string(runes[:max]) + "…", true
}
