// Package planets 提供 /planets 战线总览与 /planet 单星球详情两条查询指令。
//
// 输出形态：优先渲染成图片卡片（见 src/render），渲染失败或未启用渲染时回退 MarkdownV2 纯文本。
// 展示口径一律以实测上游数据为准，不做猜测：
//   - 星球名与分区名用官方简中译名（src/hd2/glossary），没有译名时显示上游原文；
//   - 控制方按上游 currentOwner 原文归一（实测有 Humans / Terminids / Automaton / Illuminate）；
//   - 事件类型直接显示上游 eventType 数字，上游没有公布枚举含义；
//   - 「在线士兵」取自 War.Statistics.PlayerCount，战况拿不到时显示「—」，不阻塞总览。
package planets

import (
	"context"
	"errors"
	"log"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/ijnkawakaze/telegram-bot-api"

	"hd2_bot/src/core/bot"
	"hd2_bot/src/hd2"
	"hd2_bot/src/hd2/glossary"
	"hd2_bot/src/plugins/plugutil"
	"hd2_bot/src/render"
	"hd2_bot/src/translate"
)

// failureReply 是 /planets 与 /planet 的失败文案：领域文案留在插件里（不同命令的
// 数据来源不同、说法也不同），限流判定与限流文案则共用 plugutil.ErrorReply。
const failureReply = "暂时获取不到星球数据，请稍后再试。"

// InlinePrefix 是固定的星球行内搜索前缀；所有行内前缀统一由代码登记，不放进配置文件。
const InlinePrefix = "星球-"

// 行内搜索提示：点按钮后 Telegram 会用配置里的前缀预填行内查询，用户在输入框里继续打关键字。
// 这两条文案是常量、不经过 bot.Escape，所以不能含 MarkdownV2 特殊字符（见 planets_test.go 的用例）。
const (
	inlineHintText   = "点下面的按钮搜索星球（支持中文名、英文名或编号）"
	inlineHintButton = "选择星球"
)

// maxCandidates 是歧义候选的最大条数：再多就不是「让人挑一颗」，而是刷屏。
const maxCandidates = 5

// Sender 是两条指令需要的回消息能力最小集；*bot.Bot 实现了它，测试注入假实现即可。
// 比 /war 多一条 SendTextWithKeyboard：/planet 不带参数时要发一条带行内搜索按钮的提示。
type Sender interface {
	// Reply 以 MarkdownV2 回复文本。
	Reply(chatID int64, text string, replyTo int64) error
	// SendPhoto 发送一张图片卡片。
	SendPhoto(chatID int64, png []byte, replyTo int64) error
	// SendTextWithKeyboard 发送带内联键盘的文本。
	SendTextWithKeyboard(chatID int64, text string, markup tgbotapi.InlineKeyboardMarkup) error
	SendTemporaryTextWithKeyboard(chatID int64, text string, markup tgbotapi.InlineKeyboardMarkup, delay time.Duration) error
	DeleteMessage(chatID, messageID int64) error
	// IsTargetChat 判断会话是否是配置里指定的群。
	IsTargetChat(chatID int64) bool
}

// PlanetsService 是两条指令需要的领域层能力；*hd2.Service 实现了它，测试可注入假实现。
type PlanetsService interface {
	// Planets 返回全部星球；/planets 与 /planet 都只调它一次（缓存由领域层负责）。
	Planets(ctx context.Context) (hd2.Result[[]hd2.Planet], error)
	// War 只用来取「在线士兵」一行；拿不到时总览把这一项显示成「—」。
	War(ctx context.Context) (hd2.Result[*hd2.War], error)
}

// Handlers 返回本插件提供的指令。
//   - b 用于回消息；svc 取数；
//   - renderer 为 nil 表示未启用渲染，两条指令直接走文本回退（config.render.enabled=false 时 main 会传一个「渲染必失败」的实现）；
//   - loc 是展示时区，为 nil 或加载失败时按 Asia/Shanghai 处理；
//   - inlinePrefix 是行内搜索前缀，为空时不发搜索按钮（按钮点了也只会跳出空查询）；
//   - trans 是环境信息的翻译能力，为 nil 时不做翻译（等价于 translate.enabled=false，正文保持英文）。
//
// 机器人只服务配置里指定的群，其它会话的指令一律忽略。
func Handlers(b Sender, svc PlanetsService, renderer render.Renderer, loc *time.Location, inlinePrefix string, trans translate.Translator) []bot.Handler {
	h := &handler{
		sender:       b,
		svc:          svc,
		renderer:     renderer,
		display:      plugutil.DisplayLocation(loc),
		inlinePrefix: inlinePrefix,
		trans:        trans,
	}
	return []bot.Handler{
		{Name: "planets", Run: h.planets},
		{Name: "planet", Run: h.planet},
	}
}

// handler 把两条指令共用的依赖收在一起，避免每个闭包各带一份参数。
type handler struct {
	sender       Sender
	svc          PlanetsService
	renderer     render.Renderer
	display      *time.Location
	inlinePrefix string
	trans        translate.Translator
}

// planets 处理 /planets：取全量星球 → 出总览卡片（汇总数字 + 热点星球）。
// 上游出错时回中文提示；渲染失败回文本总览，不让群友一无所获。
// 这条指令不发行内搜索按钮（用户 2026-09-17 要求）：总览卡本身就是结果，
// 要搜某一颗星球走 /planet，不带参数时那里会给按钮。
func (h *handler) planets(update tgbotapi.Update) error {
	// 非消息更新、没有会话、不是配置里指定的群：一律忽略（守卫共用 plugutil.TargetMessage）。
	msg, ok := plugutil.TargetMessage(update, h.sender)
	if !ok {
		return nil
	}
	chatID := msg.Chat.ID

	ctx, cancel := context.WithTimeout(context.Background(), plugutil.Timeout)
	defer cancel()

	res, err := h.svc.Planets(ctx)
	if err != nil {
		log.Printf("/planets 查询失败 chat=%d err=%v", chatID, err)
		return h.sender.Reply(chatID, plugutil.ErrorReply(err, failureReply), msg.MessageID)
	}
	if len(res.Value) == 0 {
		// 上游返回空列表说明响应异常（实测正常情况下是 273 条），渲染一张空卡片只会让人误会。
		log.Printf("/planets 上游返回空星球列表 chat=%d", chatID)
		return h.sender.Reply(chatID, failureReply, msg.MessageID)
	}

	// Result.FetchedAt 的时区不统一（实时取数是本地时区、快照降级是 UTC），先统一到展示时区再格式化。
	// 这份时间同时是防守战保卫倒计时的起点（见 defenseTimerText）：用数据时间而不是 now，
	// 上游数据自身的滞后才不会被算成额外的剩余时间。
	fetchedAt := res.FetchedAt.In(h.display)
	summary := SummarizePlanets(res.Value, h.warOrNil(ctx, chatID), fetchedAt)

	png, rerr := h.render(ctx, render.Card{Name: "planets", Data: BuildPlanetsCard(summary, fetchedAt, res.Stale)})
	if rerr != nil {
		log.Printf("/planets 渲染失败 chat=%d err=%v", chatID, rerr)
		return h.sender.Reply(chatID, FormatPlanetsText(summary, fetchedAt, res.Stale), msg.MessageID)
	}
	return h.sender.SendPhoto(chatID, png, msg.MessageID)
}

// planet 处理 /planet <参数>：定位一颗星球并出单星球卡片；歧义、查无此星球只回文本。
func (h *handler) planet(update tgbotapi.Update) error {
	// 非消息更新、没有会话、不是配置里指定的群：一律忽略（守卫共用 plugutil.TargetMessage）。
	msg, ok := plugutil.TargetMessage(update, h.sender)
	if !ok {
		return nil
	}
	chatID := msg.Chat.ID

	arg := plugutil.CommandArg(msg.Text)
	if arg == "" {
		// 不带参数时不再回一句用法，而是给一个行内搜索按钮（与明日方舟机器人的 /operator 一个套路）：
		// 点一下就在输入框里带出「星球-」，用户在候选里挑，不必自己拼星球名。
		if err := h.sendInlineHint(chatID, "planet"); err != nil {
			return err
		}
		if err := h.sender.DeleteMessage(chatID, msg.MessageID); err != nil {
			log.Printf("/planet 删除指令失败 chat=%d msg=%d err=%v", chatID, msg.MessageID, err)
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), plugutil.Timeout)
	defer cancel()

	res, err := h.svc.Planets(ctx)
	if err != nil {
		log.Printf("/planet 查询失败 chat=%d arg=%q err=%v", chatID, arg, err)
		return h.sender.Reply(chatID, plugutil.ErrorReply(err, failureReply), msg.MessageID)
	}
	if len(res.Value) == 0 {
		log.Printf("/planet 上游返回空星球列表 chat=%d", chatID)
		return h.sender.Reply(chatID, failureReply, msg.MessageID)
	}

	planet, candidates, ok := ResolvePlanet(res.Value, arg)
	if !ok {
		if len(candidates) > 0 {
			return h.sender.Reply(chatID, FormatPlanetCandidates(arg, candidates), msg.MessageID)
		}
		return h.sender.Reply(chatID, FormatPlanetNotFound(arg), msg.MessageID)
	}

	// 卡片与文本回退共用这一份视图模型：渲染失败时回退的文本与图片口径一致。
	fetchedAt := res.FetchedAt.In(h.display)
	card := BuildLocalizedPlanetCard(ctx, planet, fetchedAt, res.Stale, h.trans)
	// 翻译可能耗尽取数上下文；渲染使用独立、有上限的预算，仍保留已得到的译文。
	renderCtx, renderCancel := context.WithTimeout(context.Background(), plugutil.Timeout)
	defer renderCancel()
	png, rerr := h.render(renderCtx, render.Card{Name: "planet", Data: card})
	if rerr != nil {
		log.Printf("/planet 渲染失败 chat=%d name=%s err=%v", chatID, planet.Name, rerr)
		return h.sender.Reply(chatID, FormatPlanetText(card), msg.MessageID)
	}
	return h.sender.SendPhoto(chatID, png, msg.MessageID)
}

// render 调用渲染引擎；未启用渲染（renderer 为 nil）时返回错误，让调用方走文本回退。
func (h *handler) render(ctx context.Context, card render.Card) ([]byte, error) {
	if h.renderer == nil {
		return nil, errors.New("渲染未启用")
	}
	return h.renderer.Render(ctx, card)
}

// warOrNil 取战况用于「在线士兵」一行；拿不到只记一条日志并返回 nil，不影响总览出图。
func (h *handler) warOrNil(ctx context.Context, chatID int64) *hd2.War {
	res, err := h.svc.War(ctx)
	if err != nil {
		log.Printf("/planets 战况查询失败，在线士兵显示为 %s chat=%d err=%v", plugutil.DashText, chatID, err)
		return nil
	}
	return res.Value
}

// sendInlineHint 发送带行内搜索按钮的提示；前缀没配时不发（按钮点了也只会跳出空查询）。
// command 只用于日志，让日志能看出是哪条指令触发的（目前只有 /planet 走这条路）。
func (h *handler) sendInlineHint(chatID int64, command string) error {
	if strings.TrimSpace(h.inlinePrefix) == "" {
		log.Printf("行内查询前缀为空，跳过 /%s 的搜索按钮 chat=%d", command, chatID)
		return nil
	}
	return h.sender.SendTemporaryTextWithKeyboard(chatID, inlineHintText,
		plugutil.InlineSearchMarkup(inlineHintButton, h.inlinePrefix), 0)
}

// ResolvePlanet 按参数定位一颗星球，解析顺序固定为：
//  1. 纯数字 → 按 Planet.Index（此时不再退回名字匹配：「99999」就是查无此星球，而不是去名字里找 9）；
//  2. 英文名精确匹配（忽略大小写与首尾空白）；
//  3. 英文名前缀匹配；
//  4. 中文译名精确匹配（忽略大小写：中文名里含 "IV"、"UVP" 之类的拉丁片段）；
//  5. 中文译名包含匹配。
//
// 返回值语义：
//   - 唯一命中：返回那颗星球、候选为空、ok 为 true；
//   - 命中多条：返回零值星球、候选列表（按在线士兵降序、同人数按编号升序，最多 maxCandidates 条）、ok 为 false；
//   - 完全查不到：返回零值星球、空候选、ok 为 false。
//
// 判定「唯一命中」而不是「随便挑一颗」是刻意的：宁可回候选让用户换个写法，也不要把 A 星球的数据
// 当成 B 星球报出去。Task 8 的行内查询同样复用本函数。
func ResolvePlanet(list []hd2.Planet, arg string) (hd2.Planet, []hd2.Planet, bool) {
	keyword := strings.TrimSpace(arg)
	if keyword == "" || len(list) == 0 {
		return hd2.Planet{}, nil, false
	}

	if index, err := strconv.Atoi(keyword); err == nil {
		matched := filterPlanets(list, func(p hd2.Planet) bool { return p.Index == index })
		if len(matched) == 1 {
			return matched[0], nil, true
		}
		return hd2.Planet{}, nil, false
	}

	lower := strings.ToLower(keyword)
	rules := []func(hd2.Planet) bool{
		func(p hd2.Planet) bool { return strings.EqualFold(strings.TrimSpace(p.Name), keyword) },
		func(p hd2.Planet) bool { return strings.HasPrefix(strings.ToLower(strings.TrimSpace(p.Name)), lower) },
		func(p hd2.Planet) bool { return strings.EqualFold(glossary.Planet(p.Name), keyword) },
		func(p hd2.Planet) bool {
			return strings.Contains(strings.ToLower(glossary.Planet(p.Name)), lower)
		},
	}
	for _, keep := range rules {
		matched := filterPlanets(list, keep)
		switch len(matched) {
		case 0:
			continue // 这条规则没命中，继续下一条
		case 1:
			return matched[0], nil, true
		default:
			return hd2.Planet{}, rankCandidates(matched), false
		}
	}
	return hd2.Planet{}, nil, false
}

// filterPlanets 返回满足条件的星球（保持上游顺序）。
func filterPlanets(list []hd2.Planet, keep func(hd2.Planet) bool) []hd2.Planet {
	matched := make([]hd2.Planet, 0, len(list))
	for _, p := range list {
		if keep(p) {
			matched = append(matched, p)
		}
	}
	return matched
}

// rankCandidates 把候选排序后截断到 maxCandidates 条，排序直接复用热点用的 sortPlanets
// （有事件优先 → 有进攻行动优先 → 在线士兵降序 → 编号升序）：正在打的、人多的星球更可能是
// 用户想查的那颗，编号兜底保证同一个关键字每次给出同样的顺序。
func rankCandidates(matched []hd2.Planet) []hd2.Planet {
	sortPlanets(matched)
	if len(matched) > maxCandidates {
		matched = matched[:maxCandidates]
	}
	return matched
}
