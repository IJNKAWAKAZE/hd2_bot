package planets

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/ijnkawakaze/telegram-bot-api"

	"hd2_bot/src/core/bot"
	"hd2_bot/src/hd2"
	"hd2_bot/src/hd2/glossary"
	"hd2_bot/src/plugins/plugutil"
)

// inlineSearchLimit 是行内查询一次最多返回的候选条数。
// 输入框里同时只看得见几条，10 条足够翻；上限也挡住「星球-a」这类单字关键字把上百颗星球塞进客户端。
const inlineSearchLimit = 10

// inlineSearchCandidates 是检索译名表时先取多少条候选。
// 取回后还要剔除上游已下线的星球（译名表里有、上游没有，例如 uvp 系列与火星），
// 所以查表时要多留些余量；只按 10 条查表的话，前面混进几条已下线的，可用候选就不足 10 条了。
const inlineSearchCandidates = 50

// inlineQueryTimeout 是单次行内查询的应答期限。
// 取 8 秒而不是指令用的 20 秒（plugutil.Timeout）：行内查询是边打字边触发的，
// Telegram 那边只等十秒上下，超时之后应答会被丢弃，等满 20 秒只是白占一个处理槽位。
// 声明成变量而不是常量：用例会把它临时改短、让取数一直阻塞，验证这个期限真的被用上了；
// 只对常量的值做断言的话，实现里改用别的期限测试也发现不了。
var inlineQueryTimeout = 8 * time.Second

// inlineResultIDPrefix 是行内结果 ID 的前缀，后面接星球编号。
// ID 必须在一次应答内唯一且不超过 64 字节；用编号而不是随机数，结果才是可复现的。
const inlineResultIDPrefix = "planet-"

// planetCommand 是结果被选中后回填的指令名，与 Handlers 注册的 /planet 保持一致。
const planetCommand = "planet"

// InlineAnswerer 是行内查询需要的应答能力；*bot.Bot 实现它，测试注入假实现。
// 与 plugutil.InlineAnswerer 是同一个契约，这里保留别名是为了插件内的用例读起来短一点。
type InlineAnswerer = plugutil.InlineAnswerer

// 编译期断言：真实的机器人客户端满足行内查询需要的能力。
var _ InlineAnswerer = (*bot.Bot)(nil)

// InlineHandler 返回行内查询回调，由 main 交给 bot.RegisterInline 注册：
//
//	tgBot.RegisterInline(cfg.Inline.Prefix, planets.InlineHandler(svc, tgBot, cfg.Inline.Prefix))
//
// prefix 必须与注册时用的前缀一致：回调会把查询文本里的这一段切掉，剩下的才是检索关键字。
// 行内查询的更新里只有发起人、没有会话，所以这里没法按群过滤，前缀本身就是它的作用范围。
func InlineHandler(svc PlanetsService, answerer InlineAnswerer, prefix string) func(tgbotapi.InlineQuery) error {
	return func(query tgbotapi.InlineQuery) error {
		return answerInline(svc, answerer, prefix, query)
	}
}

// answerInline 处理一次行内查询：取星球列表 → 按关键字检索译名表 → 应答候选。
// 只打了前缀（没打关键字）时给默认候选而不是一片空白（用户 2026-09-17 要求：与图鉴前缀一样，
// 点开输入框先看到东西，才知道该往里打什么）：默认列表就是热点排序下的前 inlineSearchLimit 颗。
// 上游出错、返回空列表一律回空结果（Telegram 显示「无结果」）并只记一条中文日志：
// 行内查询是用户边打字边触发的，把错误上抛只会刷日志，也帮不到用户。
func answerInline(svc PlanetsService, answerer InlineAnswerer, prefix string, query tgbotapi.InlineQuery) error {
	keyword := inlineKeyword(query.Query, prefix)

	ctx, cancel := context.WithTimeout(context.Background(), inlineQueryTimeout)
	defer cancel()

	res, err := svc.Planets(ctx)
	if err != nil {
		log.Printf("行内查询取星球数据失败 query=%s keyword=%q err=%v", query.ID, keyword, err)
		return answerInlineResults(answerer, query.ID, nil)
	}
	if len(res.Value) == 0 {
		// 关键字为空时不记日志：那是正常用法（刚点开按钮），不是异常。
		if keyword != "" {
			log.Printf("行内查询上游返回空星球列表 query=%s keyword=%q", query.ID, keyword)
		}
		return answerInlineResults(answerer, query.ID, nil)
	}
	if keyword == "" {
		return answerInlineResults(answerer, query.ID, defaultInlineResults(res.Value))
	}
	return answerInlineResults(answerer, query.ID, buildInlineResults(res.Value, keyword))
}

// defaultInlineResults 拼默认候选：按热点顺序（有事件 → 有进攻行动 → 在线士兵降序 → 编号升序）
// 取前 inlineSearchLimit 颗。排序直接复用总览热点的 sortPlanets，用户在输入框里看到的顺序
// 与卡片上的热点顺序一致，不会出现两套「谁更热」的说法。
func defaultInlineResults(list []hd2.Planet) []interface{} {
	ranked := make([]hd2.Planet, len(list))
	copy(ranked, list)
	sortPlanets(ranked)
	if len(ranked) > inlineSearchLimit {
		ranked = ranked[:inlineSearchLimit]
	}
	results := make([]interface{}, 0, len(ranked))
	for _, p := range ranked {
		results = append(results, inlineResultForPlanet(p, glossary.Planet(p.Name)))
	}
	return results
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

// buildInlineResults 按关键字检索译名表并拼出行内候选：
// 标题用中文译名（缺译名时用上游英文名），描述是「英文名 ｜ 分区中文名」，
// 选中后回填 /planet <名字>，由 /planet 渲染单星球卡片。
//
// 只保留上游当前存在的星球：译名表里有几条上游已经没有的条目（例如 uvp 系列、火星），
// 选中它们只会得到「找不到」，不如不显示。
func buildInlineResults(list []hd2.Planet, keyword string) []interface{} {
	hits := glossary.SearchPlanets(keyword, inlineSearchCandidates)
	byName := make(map[string]hd2.Planet, len(list))
	for _, p := range list {
		if name := normalizePlanetName(p.Name); name != "" {
			byName[name] = p
		}
	}

	// 显式构造空切片：nil 切片会被序列化成 null，而 Telegram 要求 results 是数组；
	// 空结果本身就是合法应答（客户端显示「无结果」）。
	results := make([]interface{}, 0, len(hits))
	for _, hit := range hits {
		p, ok := byName[normalizePlanetName(hit.English)]
		if !ok {
			continue
		}
		results = append(results, inlineResultFor(p, hit))
		// 剔完已下线的星球后可能不足 10 条，这里到底就停，不多查表也不多拼结果。
		if len(results) >= inlineSearchLimit {
			break
		}
	}
	return results
}

// normalizePlanetName 把星球名归一化成查表用的键：上游写的是「Acamar IV」这种形式，
// 译名表的键是全小写，两边都折叠大小写后再比对。
func normalizePlanetName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// inlineResultFor 组装一条文章式结果：标题优先用译名表里的中文名，表里没写就退回上游英文名。
func inlineResultFor(p hd2.Planet, hit glossary.PlanetHit) tgbotapi.InlineQueryResultArticle {
	return inlineResultForPlanet(p, strings.TrimSpace(hit.Chinese))
}

// inlineResultForPlanet 组装一条文章式结果，标题由调用方给（空串时退回上游英文名）。
// 默认候选与关键字检索共用这一份：两种入口给出的标题、描述与回填指令必须逐字相同。
func inlineResultForPlanet(p hd2.Planet, chinese string) tgbotapi.InlineQueryResultArticle {
	// 绝不给出空标题：译名表里没有这颗星球时用上游英文名。
	title := strings.TrimSpace(chinese)
	if title == "" {
		title = strings.TrimSpace(p.Name)
	}
	english := strings.TrimSpace(p.Name)
	if english == "" {
		english = title
	}
	return tgbotapi.InlineQueryResultArticle{
		Type:  "article",
		ID:    inlineResultIDPrefix + strconv.Itoa(p.Index),
		Title: title,
		// 描述里再带上英文名与分区，用户能确认选中的是哪一颗。
		Description: fmt.Sprintf("%s ｜ %s", english, sectorText(p.Sector)),
		// 刻意不设置 ThumbURL：Telegram 要求缩略图是公网可达地址，本项目没有图床，
		// 也不会把 bot token 拼进 api.telegram.org/file/... 暴露出去（spec §6）。
		InputMessageContent: tgbotapi.InputTextMessageContent{
			Text: "/" + planetCommand + " " + title,
		},
	}
}

// answerInlineResults 应答一次行内查询。口径（空切片、走 AnswerInline、错误文案）统一在
// plugutil.AnswerInline 里，与新增的图鉴行内查询共用同一份实现。
func answerInlineResults(answerer InlineAnswerer, queryID string, results []interface{}) error {
	return plugutil.AnswerInline(answerer, queryID, results)
}
