// 行内查询：把图鉴的检索能力接到 Telegram 的输入框里。
//
// 前缀与检索范围（写进 /help，也是本文件里 InlineSpecs 的唯一来源）：
//
//	武器-  主武器 / 副武器 / 支援武器
//	战备-  其它战备
//	护甲-  护甲
//	手雷-  手雷
//	债券-  24 本战争债券（名单，选中后回填 /warbonds <名称>）
//	敌人-  敌人图鉴
//
// 两条约定：
//   - 只打前缀、不打关键字时给默认列表（各表按目录顺序的前 N 条），而不是空结果——
//     用户点开输入框时先看到东西，才知道该往里打什么；
//   - 缩略图用上游的公网地址（Telegram 只认 URL）。SVG 一概不带图：Telegram 不接受 SVG，
//     而站点也没有把 SVG 转成位图的能力（实测），所以这类条目不设 ThumbURL。
package codex

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	tgbotapi "github.com/ijnkawakaze/telegram-bot-api"

	"hd2_bot/src/arsenal"
	"hd2_bot/src/bestiary"
	"hd2_bot/src/core/bot"
	"hd2_bot/src/plugins/plugutil"
)

const (
	// inlineResultLimit 是一次行内查询最多回几条候选。输入框里同时只看得见几条，10 条足够翻，
	// 也能挡住「护甲-a」这种单字关键字把上百件装备塞进客户端。
	inlineResultLimit = 10
	// inlineQueryTimeout 是单次行内查询的应答期限。取 8 秒而不是指令用的 20 秒：
	// 行内查询是边打字边触发的，Telegram 那边只等十秒上下，超时之后应答会被丢弃。
	inlineQueryTimeout = 8 * time.Second
	// inlineResultIDPrefix 是行内结果 ID 的前缀，后面接装备 id 或敌人标题。
	// ID 必须在一次应答内唯一且不超过 64 字节。
	inlineResultIDPrefix = "codex-"
)

// InlineAnswerer 是行内查询需要的应答能力；*bot.Bot 实现它，测试注入假实现。
type InlineAnswerer = plugutil.InlineAnswerer

// 编译期断言：真实的机器人客户端满足行内查询需要的能力。
var _ InlineAnswerer = (*bot.Bot)(nil)

// InlineSpec 描述一个行内前缀负责查什么。
type InlineSpec struct {
	Prefix  string         // 前缀（含结尾的短横线），也是唯一的分流依据
	Label   string         // 中文名，写进日志、帮助与搜索按钮
	Command string         // 对应的指令名（不带斜杠）：不带参数时就是靠它找前缀
	Kinds   []arsenal.Kind // 装备类前缀要查的类别
	Enemy   bool           // true 表示这是敌人前缀（查图鉴而不是装备目录）
	Warbond bool           // true 表示这是债券前缀（查 24 本名单而不是装备）
}

// InlineSpecs 是本插件负责的全部行内前缀。main 按它逐个注册，
// /help 的说明也照着念，不会出现「注册了但帮助里没写」；
// 六条指令不带参数时也按这里的 Command 找自己那个前缀，不会出现「按钮点了跳空查询」。
var InlineSpecs = []InlineSpec{
	{Prefix: "武器-", Label: "武器", Command: "gun", Kinds: arsenal.Weapons},
	{Prefix: "战备-", Label: "战备", Command: "strat", Kinds: []arsenal.Kind{arsenal.KindStratagem}},
	{Prefix: "护甲-", Label: "护甲", Command: "armor", Kinds: []arsenal.Kind{arsenal.KindArmor}},
	{Prefix: "手雷-", Label: "手雷", Command: "grenade", Kinds: []arsenal.Kind{arsenal.KindGrenade}},
	{Prefix: "债券-", Label: "债券", Command: "warbonds", Warbond: true},
	{Prefix: "敌人-", Label: "敌人", Command: "enemy", Enemy: true},
}

// inlineSpecForCommand 按指令名找它对应的行内前缀。
// 不带参数的六条指令都要弹一个「查这一类」的按钮，前缀必须与注册时用的完全一致，
// 所以统一回这张表里查，而不是在插件里另抄一份对应关系。
func inlineSpecForCommand(command string) (InlineSpec, bool) {
	for _, spec := range InlineSpecs {
		if spec.Command == command {
			return spec, true
		}
	}
	return InlineSpec{}, false
}

// inlineGear 是装备类行内查询要用到的能力（Catalog 检索 + ThumbURL 取缩略图）。
type inlineGear interface {
	Catalog(ctx context.Context) (*arsenal.Catalog, error)
	ThumbURL(ctx context.Context, item arsenal.Item) string
}

// inlineBeasts 是敌人行内查询要用到的能力。
type inlineBeasts interface {
	List(ctx context.Context) ([]bestiary.Enemy, error)
}

// InlineHandlers 按 InlineSpecs 生成「前缀 → 回调」的注册表，交给 main 逐个注册：
//
//	for _, item := range codex.InlineHandlers(gear, beasts, tgBot) {
//		tgBot.RegisterInline(item.Prefix, item.Handler)
//	}
//
// 返回切片而不是 map：注册顺序要稳定（日志与用例都看得到），map 的遍历顺序是随机的。
type InlineRegistration struct {
	Prefix  string
	Handler func(query tgbotapi.InlineQuery) error
}

// InlineHandlers 生成全部前缀的注册项。
func InlineHandlers(gear inlineGear, beasts inlineBeasts, answerer InlineAnswerer) []InlineRegistration {
	registrations := make([]InlineRegistration, 0, len(InlineSpecs))
	for _, spec := range InlineSpecs {
		registrations = append(registrations, InlineRegistration{
			Prefix:  spec.Prefix,
			Handler: InlineHandler(gear, beasts, answerer, spec),
		})
	}
	return registrations
}

// InlineHandler 返回某个前缀的行内查询回调。
// prefix 必须与注册时用的前缀一致：回调会把查询文本里的这一段切掉，剩下的才是检索关键字。
func InlineHandler(gear inlineGear, beasts inlineBeasts, answerer InlineAnswerer, spec InlineSpec) func(tgbotapi.InlineQuery) error {
	return func(query tgbotapi.InlineQuery) error {
		return answerInline(gear, beasts, answerer, spec, query)
	}
}

// answerInline 处理一次行内查询：取数据 → 按关键字检索（空关键字给默认列表）→ 应答候选。
//
// 上游出错、取不到数据一律回空结果（客户端显示「无结果」）并只记一条中文日志：
// 行内查询是用户边打字边触发的，把错误上抛只会刷日志，也帮不到用户。
func answerInline(gear inlineGear, beasts inlineBeasts, answerer InlineAnswerer, spec InlineSpec, query tgbotapi.InlineQuery) error {
	keyword := inlineKeyword(query.Query, spec.Prefix)

	ctx, cancel := context.WithTimeout(context.Background(), inlineQueryTimeout)
	defer cancel()

	var results []interface{}
	if spec.Enemy {
		if beasts == nil {
			log.Printf("行内查询 %s 没有敌人数据源，回空结果 query=%s", spec.Prefix, query.ID)
			return plugutil.AnswerInline(answerer, query.ID, nil)
		}
		list, err := beasts.List(ctx)
		if err != nil {
			log.Printf("行内查询取敌人图鉴失败 prefix=%s query=%s err=%v", spec.Prefix, query.ID, err)
			return plugutil.AnswerInline(answerer, query.ID, nil)
		}
		hits := bestiary.Search(list, bestiary.Query{Keyword: keyword, Limit: inlineResultLimit})
		results = enemyInlineResults(hits.Enemies)
	} else if spec.Warbond {
		// 债券前缀查的是 24 本名单而不是装备目录：不带关键字时把整份名单当候选。
		if gear == nil {
			log.Printf("行内查询 %s 没有装备数据源，回空结果 query=%s", spec.Prefix, query.ID)
			return plugutil.AnswerInline(answerer, query.ID, nil)
		}
		catalog, err := gear.Catalog(ctx)
		if err != nil {
			log.Printf("行内查询取装备目录失败 prefix=%s query=%s err=%v", spec.Prefix, query.ID, err)
			return plugutil.AnswerInline(answerer, query.ID, nil)
		}
		results = warbondInlineResults(catalog, keyword)
	} else {
		if gear == nil {
			log.Printf("行内查询 %s 没有装备数据源，回空结果 query=%s", spec.Prefix, query.ID)
			return plugutil.AnswerInline(answerer, query.ID, nil)
		}
		catalog, err := gear.Catalog(ctx)
		if err != nil {
			log.Printf("行内查询取装备目录失败 prefix=%s query=%s err=%v", spec.Prefix, query.ID, err)
			return plugutil.AnswerInline(answerer, query.ID, nil)
		}
		items := searchInlineItems(catalog, spec.Kinds, keyword)
		results = gearInlineResults(ctx, gear, catalog, items)
	}
	return plugutil.AnswerInline(answerer, query.ID, results)
}

// inlineKeyword 取出查询里的检索关键字：带前缀时取前缀之后的部分，没有前缀时按整串处理。
// 后一种情况正常不会发生（bot 只把带前缀的查询交给我们），但万一注册用的前缀与回调传的不一致，
// 按整串搜索至少还能用，比默默回一堆空结果强。
func inlineKeyword(query, prefix string) string {
	keyword := query
	if prefix != "" {
		if _, after, found := strings.Cut(query, prefix); found {
			keyword = after
		}
	}
	return strings.TrimSpace(keyword)
}

// searchInlineItems 在给定类别里检索，合并后取前 N 条。
//
// 为什么必须先合并再截断：Telegram 的一次应答只有一个候选列表，逐类各取 10 条会让「武器-」
// 回 30 条，用户还不知道自己在翻哪一类。合并顺序刻意保持简单——按 spec.Kinds 的类别顺序拼接，
// 类别内保持 Search 的排序（完全相等 → 前缀 → 包含）：同一关键字每次给出同一份结果，
// 便于用户形成预期，也不必在这里另发明一套跨类别排序。
func searchInlineItems(catalog *arsenal.Catalog, kinds []arsenal.Kind, keyword string) []arsenal.Item {
	merged := make([]arsenal.Item, 0, inlineResultLimit*len(kinds))
	seen := make(map[string]bool, len(merged))
	for _, kind := range kinds {
		// 每类都按上限取，合并后再截断：某一类命中很多时不会把别的类别挤掉。
		result := catalog.Search(arsenal.Query{Kind: kind, Keyword: keyword, Limit: inlineResultLimit})
		for _, item := range result.Items {
			if seen[item.ID] {
				continue
			}
			seen[item.ID] = true
			merged = append(merged, item)
		}
	}
	if len(merged) > inlineResultLimit {
		merged = merged[:inlineResultLimit]
	}
	return merged
}

// gearInlineResults 把装备拼成文章式候选：标题用中文名（带型号），描述写英文名、类别与获取方式。
func gearInlineResults(ctx context.Context, gear inlineGear, catalog *arsenal.Catalog, items []arsenal.Item) []interface{} {
	// 显式构造空切片：nil 会被序列化成 null，而 Telegram 要求 results 是数组。
	results := make([]interface{}, 0, len(items))
	for _, item := range items {
		title := plugutil.DefaultText(item.NameZh, item.NameEn)
		if item.Model != "" {
			title = fmt.Sprintf("%s（%s）", title, item.Model)
		}
		results = append(results, tgbotapi.InlineQueryResultArticle{
			Type:  "article",
			ID:    inlineResultIDPrefix + item.ID,
			Title: title,
			// 描述里带上英文名、类别与获取方式，用户能确认选中的是哪一件。
			Description: inlineDescription(item, catalog),
			ThumbURL:    gear.ThumbURL(ctx, item),
			InputMessageContent: tgbotapi.InputTextMessageContent{
				Text: "/" + commandForKind(item.Kind) + " " + plugutil.DefaultText(item.NameZh, item.NameEn),
			},
		})
	}
	return results
}

// inlineDescription 拼候选的描述行：英文名 ｜ 类别 ｜ 获取方式（取不到的部分自动省略）。
func inlineDescription(item arsenal.Item, catalog *arsenal.Catalog) string {
	parts := make([]string, 0, 3)
	if english := strings.TrimSpace(item.NameEn); english != "" && !strings.EqualFold(english, strings.TrimSpace(item.NameZh)) {
		parts = append(parts, english)
	}
	parts = append(parts, arsenal.KindLabel(item.Kind))
	if acquire := acquisitionText(catalog, item); acquire != "" {
		parts = append(parts, acquire)
	}
	return strings.Join(parts, " ｜ ")
}

// commandForKind 返回「选中这条结果后回填哪条指令」：类别决定指令，用户不必自己改。
// 对应关系直接从 InlineSpecs 里查（类别 → 该前缀所属的指令），不在这里另抄一份。
func commandForKind(kind arsenal.Kind) string {
	for _, spec := range InlineSpecs {
		for _, k := range spec.Kinds {
			if k == kind {
				return spec.Command
			}
		}
	}
	// 兜底：新增类别却忘了登记前缀时，至少落到 /gun，而不是给出一条打不开的指令。
	return "gun"
}

// searchInlineWarbonds 按关键字筛债券：中文名、英文名、上游 id 命中任意一个即可。
// 这里刻意不打分排序：一共 24 本，保持上游顺序反而更容易找。关键字为空时给整份名单（最多 inlineResultLimit 本）。
func searchInlineWarbonds(catalog *arsenal.Catalog, keyword string) []arsenal.Warbond {
	all := catalog.Warbonds()
	books := make([]arsenal.Warbond, 0, len(all))
	needle := strings.ToLower(strings.TrimSpace(keyword))
	for _, book := range all {
		if needle != "" && !warbondMatches(book, needle) {
			continue
		}
		books = append(books, book)
		if len(books) >= inlineResultLimit {
			break
		}
	}
	return books
}

// warbondMatches 判断一本债券是否命中关键字（大小写不敏感）。
func warbondMatches(book arsenal.Warbond, needle string) bool {
	for _, field := range []string{book.NameZh, book.NameEn, book.ID} {
		if strings.Contains(strings.ToLower(field), needle) {
			return true
		}
	}
	return false
}

// warbondInlineResults 把债券拼成文章式候选：标题用中文名，描述写英文名、装备件数与解锁价。
// 选中后回填成 /warbonds <名称>，与装备候选回填 /gun <名称> 是同一个套路。
func warbondInlineResults(catalog *arsenal.Catalog, keyword string) []interface{} {
	books := searchInlineWarbonds(catalog, keyword)
	results := make([]interface{}, 0, len(books))
	for _, book := range books {
		results = append(results, tgbotapi.InlineQueryResultArticle{
			Type:        "article",
			ID:          inlineResultIDPrefix + "warbond-" + book.ID,
			Title:       plugutil.DefaultText(book.NameZh, book.NameEn),
			Description: warbondInlineDescription(book),
			InputMessageContent: tgbotapi.InputTextMessageContent{
				Text: "/warbonds " + plugutil.DefaultText(book.NameZh, book.NameEn),
			},
		})
	}
	return results
}

// warbondInlineDescription 拼债券候选的描述行：英文名 ｜ 件数 ｜ 解锁价（取不到的部分自动省略）。
func warbondInlineDescription(book arsenal.Warbond) string {
	parts := make([]string, 0, 3)
	if english := strings.TrimSpace(book.NameEn); english != "" && !strings.EqualFold(english, strings.TrimSpace(book.NameZh)) {
		parts = append(parts, english)
	}
	if book.ItemCount > 0 {
		parts = append(parts, fmt.Sprintf("%d 件装备", book.ItemCount))
	}
	if book.SuperCredits > 0 {
		parts = append(parts, creditsText(book.SuperCredits)+" 超级货币")
	} else {
		parts = append(parts, "不单卖")
	}
	return strings.Join(parts, " ｜ ")
}

// enemyInlineResults 把敌人拼成文章式候选：标题用中文名，描述写英文名、阵营、体型与血量。
func enemyInlineResults(enemies []bestiary.Enemy) []interface{} {
	results := make([]interface{}, 0, len(enemies))
	for _, enemy := range enemies {
		title := plugutil.DefaultText(enemy.NameZh, enemy.Title)
		results = append(results, tgbotapi.InlineQueryResultArticle{
			Type:        "article",
			ID:          inlineResultIDPrefix + inlineIDPart(enemy.Title),
			Title:       title,
			Description: enemyDescription(enemy),
			ThumbURL:    enemy.IconURL,
			InputMessageContent: tgbotapi.InputTextMessageContent{
				Text: "/enemy " + plugutil.DefaultText(enemy.NameZh, enemy.Title),
			},
		})
	}
	return results
}

// enemyDescription 拼敌人候选的描述行。
func enemyDescription(enemy bestiary.Enemy) string {
	faction, _ := factionText(enemy)
	parts := make([]string, 0, 4)
	if english := strings.TrimSpace(enemy.Title); english != "" && !strings.EqualFold(english, strings.TrimSpace(enemy.NameZh)) {
		parts = append(parts, english)
	}
	parts = append(parts, faction)
	if label := bestiary.SizeLabel(enemy.Size); label != "" {
		parts = append(parts, label)
	}
	parts = append(parts, "血量 "+healthText(enemy.Health))
	return strings.Join(parts, " ｜ ")
}

// inlineIDPart 把标题变成能进结果 ID 的片段。
// Telegram 只要求「一次应答内唯一且不超过 64 字节」，标题里的空格、括号、撇号都能原样用，
// 所以这里只做空白折叠，不引入哈希（哈希会让同一只敌人的 ID 不可复现）。
func inlineIDPart(title string) string {
	return strings.Join(strings.Fields(title), "_")
}
