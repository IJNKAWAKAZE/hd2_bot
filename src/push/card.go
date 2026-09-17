package push

import (
	"fmt"
	"log"
	"strings"
	"time"

	"hd2_bot/src/core/bot"
	"hd2_bot/src/plugins/plugutil"
	"hd2_bot/src/render"
	"hd2_bot/src/translate"
)

// pushCardName 必须与 src/render/templates/push.tmpl 的文件名一致（写错会在启动期 panic）。
const pushCardName = "push"

// pushEmblem 是卡片头栏的徽标素材逻辑名；取值必须来自 src/render/assets.go 的 assetFiles 登记表。
// 选超级地球徽标：这张卡片四类事件混排，没有任何一方的专属徽标能代表全部。
const pushEmblem = "emblem.super_earth"

// cardTitle 是推送卡片的标题，图片与文本回退共用同一个常量，避免两条路径的标题写岔。
const cardTitle = "战况播报"

// defaultMaxItems 是每节最多列几条，必须与配置里 push.max_items 的默认值（src/config/config.go 的
// setDefaults）保持一致：配置没写这一项时用它，BuildCard 被单独调用（测试、将来的命令路径）时也用它。
// 两者写岔了不会报错，只会让「配置没写」和「没走配置」两条路径画出不同的卡片，
// 所以 card_test.go 里有一条用例把配置加载出来的默认值和这个常量钉在一起。
const defaultMaxItems = 5

// maxTitleRunes / maxDetailRunes 是条目文本的终局长度上限（按 rune 计），是**兜底防线**而不是展示预算。
//
// 展示预算由调用方掌握，口径是「装下真实上游内容」：简报标题按 dispatchTitleRunes 截、
// 正文摘要按 detailRunes 截、空间站摘要由 stationDiff 按战术行动条数拼接，正常内容到这里已经是完整的。
// 这两个常量只在异常数据上生效（上游突然给出远超历史的正文、或译文标题暴涨），所以按
// 「真实上游上限的约 1.5 倍」取值：2026-09-17 实测最长首行 385 rune、最长正文 807 rune，
// 于是定 600 / 1200。超过防线的内容在这里被截一次并补省略号收口，
// 而不是把图片拉成一张超出 render.MaxCardHeightPx 的长图（那条路会让整轮改发文本）。
//
// BuildCard 必须能独立保证这一点：任何一个调用方漏截（译文标题就是漏网的一条）都不该让卡片变形。
const (
	maxTitleRunes  = 600
	maxDetailRunes = 1200
)

// translateUnavailableNote 是翻译不可用时卡片底部的说明。
// 有了它，群里看到英文正文时能知道「不是机器人坏了，是翻译暂时不可用」。
const translateUnavailableNote = "翻译暂不可用，以上正文为英文原文。"

// Item 是卡片上的一条事件。
type Item struct {
	Title  string // 一行摘要
	Detail string // 补充说明（可为空）
}

// Section 是卡片上的一节。
type Section struct {
	Title string // 节标题
	Items []Item // 本节条目（已截断）
	More  string // 「另有 N 条未显示」；没有截断时为空串
}

// Card 是 templates/push.tmpl 的视图模型。
type Card struct {
	render.Meta
	Sections []Section // 按固定顺序排列，没有事件的节不存在
	Note     string    // 卡片底部的口径说明（例如翻译不可用）
}

// sectionOrder 是四类事件在卡片上的节标题与顺序（spec §7）。
var sectionOrder = []struct {
	kind  Kind
	title string
}{
	{KindDispatch, "重大指令与新简报"},
	{KindOwner, "星球易主"},
	{KindCampaign, "战役动态"},
	{KindStation, "空间站与 DSS"},
}

// BuildCard 把事件列表转成卡片视图模型。
//
// 节内顺序沿用 Detect 给出的顺序（每类内部已经排好），只在节内截断；
// maxItems <= 0 时用 defaultMaxItems（配置校验也拦了 0，这里再兜一次，避免 Builder 被单独调用时画出空卡）。
// fetchedAt 必须是已转换到展示时区的时间（Result.FetchedAt 的时区不统一）；
// 零值时 DataTime 为空串，卡片外壳会整行隐藏（推送轮询只有在拿到数据后才会调本函数，正常不会走到）。
//
// events 为空时返回一张「有标题、无节」的卡片：调用方据此也能看出「本轮没有变化」，
// 而不是拿到一个连标题都没有的零值。
//
// 长度契约：调用方应当先按业务预算把 Title / Detail 截好（本函数不猜业务含义），
// 但本函数是最后一道防线——超过 maxTitleRunes / maxDetailRunes 的内容会被压平换行后按 rune 截断，
// 免得任何一个调用方漏截就让图片高到几千像素（译文标题、stationDiff 拼接串都曾漏网）。
// 副标题统计的是**输入条数**：即使有事件的类别不在 sectionOrder 里（卡片上画不出来），
// 也照样计数，同时打一条日志提醒（静默丢掉一条变化比数字对不上更糟）。
func BuildCard(events []Event, fetchedAt time.Time, maxItems int) Card {
	if maxItems <= 0 {
		maxItems = defaultMaxItems
	}
	total := len(events)
	// 预分配容量取「每节上限」与「事件总数」的较小值：maxItems 直接来自配置，
	// 而 config 只校验 > 0、没有上界，照它建切片会被一个手滑的大数值（1<<40 之类）
	// 直接顶到 makeslice 崩溃或按四个节各吃掉几十 GB。
	capHint := maxItems
	if capHint > total {
		capHint = total
	}
	sections := make([]Section, 0, len(sectionOrder))
	for _, def := range sectionOrder {
		items := make([]Item, 0, capHint)
		more := 0
		for _, e := range events {
			if e.Kind != def.kind {
				continue
			}
			if len(items) >= maxItems {
				more++
				continue
			}
			items = append(items, Item{
				Title:  clampItemText(e.Title, maxTitleRunes),
				Detail: clampItemText(e.Detail, maxDetailRunes),
			})
		}
		if len(items) == 0 {
			continue
		}
		section := Section{Title: def.title, Items: items}
		if more > 0 {
			section.More = fmt.Sprintf("另有 %d 条未显示", more)
		}
		sections = append(sections, section)
	}
	if n := countUnknownKinds(events); n > 0 {
		log.Printf("推送卡片：%d 条事件的类别不在展示范围内，已被省略（多半是 Detect 新增了类别而 sectionOrder 没跟上）", n)
	}

	return Card{
		Meta: render.Meta{
			Title:    cardTitle,
			Subtitle: fmt.Sprintf("本次检测到 %d 条变化", total),
			DataTime: plugutil.FormatDataTime(fetchedAt),
			Emblem:   pushEmblem,
		},
		Sections: sections,
	}
}

// clampItemText 是条目文本的终局兜底：先压平空白与换行，再按 rune 截断并补省略号。
//
// 为什么压平换行：文本回退里一条条目就是一行（「· 标题 ｜ 说明」），Detail 里带裸换行会让
// 后续行丢掉项目符号与缩进，图片路径同样会多出一行；而正文摘要本来就该是一行。
// 为什么要在这里再截一次：调用方各有各的预算，本函数是唯一不依赖调用方自觉的地方。
func clampItemText(text string, max int) string {
	return translate.Summarize(text, max)
}

// countUnknownKinds 数出不在 sectionOrder 里的事件条数（它们画不进任何一节）。
// 它只是给日志用的：副标题仍然统计输入条数，卡片数字与正文对不上时靠这条日志定位。
func countUnknownKinds(events []Event) int {
	n := 0
	for _, e := range events {
		known := false
		for _, def := range sectionOrder {
			if e.Kind == def.kind {
				known = true
				break
			}
		}
		if !known {
			n++
		}
	}
	return n
}

// 全角括号归一表：只收「成对出现、语义与半角一致」的几种。
// 刻意不动书名号《》与中文引号「」“”：它们不是括号，替换后反而读不懂。
var fullWidthBrackets = strings.NewReplacer(
	"（", "(", "）", ")",
	"［", "[", "］", "]",
	"｛", "{", "｝", "}",
)

// escapeText 是文本回退统一的转义入口：先把全角括号归一成半角，再做 MarkdownV2 转义。
//
// 为什么要归一全角括号（诚实版理由，别写成「不归一会被 Telegram 拒收」——那是错的）：
// MarkdownV2 的保留字符表里没有全角字符，bot.Escape("（全角）") 会原样返回，消息照样能发出去。
// 归一的目的只是口径统一：文本回退里出现「已转义的半角括号」与「原样透传的全角括号」两种字形时，
// 同一段文案看着像两套排版；推送卡片的文本回退与 /dispatches、/planet 保持一致（后两者不归一，
// 见 orders/planets 的 bot.Escape 调用），图片路径不受影响（HTML 里照旧显示全角）。
// 任何来自数据或翻译结果的字符串都必须过这个函数，漏一处就可能让整条消息被 Telegram 拒收
// （真正的拒收来源是半角保留字符没转义：_ * [ ] ( ) ~ ` > # + - = | { } . !）。
func escapeText(text string) string {
	return bot.Escape(fullWidthBrackets.Replace(text))
}

// FormatText 生成 MarkdownV2 文本回退（渲染失败或未启用渲染时用）。
//
// 直接吃卡片视图模型：节顺序、截断与文案两条路径共用同一份，不会出现「图里有、文本里没有」。
// 所有来自数据的字段都过 escapeText（全角括号归一 + MarkdownV2 转义）：漏掉任何一个字符
// 都会让 Telegram 直接拒绝整条消息（上游正文里括号、感叹号、连字符都很常见）。
// 返回的文本不含结尾换行，可直接交给发送层。
func FormatText(card Card) string {
	title := card.Title
	if title == "" {
		title = cardTitle
	}
	var b strings.Builder
	fmt.Fprintf(&b, "*%s*\n", escapeText(title))
	if card.Subtitle != "" {
		fmt.Fprintf(&b, "%s\n", escapeText(card.Subtitle))
	}
	for _, section := range card.Sections {
		fmt.Fprintf(&b, "\n*%s*\n", escapeText(section.Title))
		for _, item := range section.Items {
			if item.Detail == "" {
				fmt.Fprintf(&b, "· %s\n", escapeText(item.Title))
				continue
			}
			fmt.Fprintf(&b, "· %s ｜ %s\n", escapeText(item.Title), escapeText(item.Detail))
		}
		if section.More != "" {
			fmt.Fprintf(&b, "%s\n", escapeText(section.More))
		}
	}
	if card.Note != "" {
		fmt.Fprintf(&b, "\n%s\n", escapeText(card.Note))
	}
	b.WriteString(plugutil.DataTimeLine(card.Meta))
	return strings.TrimRight(b.String(), "\n")
}
