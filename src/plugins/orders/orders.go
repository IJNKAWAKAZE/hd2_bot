// Package orders 提供 /assignments 重要指令与 /dispatches 战役简报两条查询指令。
//
// 输出形态：优先渲染成图片卡片（见 src/render），渲染失败或未启用渲染时回退 MarkdownV2 纯文本。
// 展示口径以实测上游数据为准：
//   - 上游当前没有进行中的重要指令（实测 /assignments 返回 []），此时渲染「暂无重要指令」占位卡，
//     这不是错误，也不会回一句「查询失败」；
//   - 简报正文里的游戏内标记（<i=3>…</i>）在展示前清理，超过上限的正文截断并在卡片上注明；
//   - 简报正文默认翻成中文（见 BuildTranslatedDispatches）：翻译不可用时保留英文并在卡片上注明，
//     翻译失败不算命令失败，照常出图；
//   - 任务里的枚举（类型/阵营/目标值/星球索引）按社区口径解码成中文，依据见 decode.go 文件头；
//     表里没有的枚举与奖励类型仍按原值给数字，不猜。
package orders

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
	"hd2_bot/src/plugins/plugutil"
	"hd2_bot/src/render"
	"hd2_bot/src/translate"
)

const (
	// defaultDispatchCount 是 /dispatches 不带参数时展示的条数。
	defaultDispatchCount = 3
	// maxDispatchCount 是 /dispatches 一次最多展示的条数：再多会把卡片拉长到看不清，
	// 用户要「100 条」时也只给 10 条（夹到上界），而不是报错或画一张超长图。
	maxDispatchCount = 10
)

// 两条指令各自的失败文案：数据来源不同，说法也不同；限流文案与判定逻辑共用 plugutil.ErrorReply。
const (
	assignmentsFailureReply = "暂时获取不到重要指令，请稍后再试。"
	dispatchesFailureReply  = "暂时获取不到战役简报，请稍后再试。"
)

// Sender 是两条指令需要的回消息能力最小集；*bot.Bot 实现了它，测试注入假实现即可，
// 不必为只发文本与图片的指令去实现整份 bot.Sender。
type Sender interface {
	// Reply 以 MarkdownV2 回复文本。
	Reply(chatID int64, text string, replyTo int64) error
	// SendPhoto 发送一张图片卡片。
	SendPhoto(chatID int64, png []byte, replyTo int64) error
	// IsTargetChat 判断会话是否是配置里指定的群。
	IsTargetChat(chatID int64) bool
}

// OrdersService 是两条指令需要的领域层能力；*hd2.Service 实现了它，测试可注入假实现。
type OrdersService interface {
	// Assignments 返回重要指令；没有进行中的指令时是空切片（上游实测如此）。
	Assignments(ctx context.Context) (hd2.Result[[]hd2.Assignment], error)
	// Dispatches 返回战役简报，上游按最新在前返回。
	Dispatches(ctx context.Context) (hd2.Result[[]hd2.Dispatch], error)
	// Planets 返回星球列表：任务里的星球索引（valueType 12）要靠它翻成星球名。
	// 只在真的出现星球索引时才会被调用，取数失败也不影响指令查询（见 handler.planetNames）。
	Planets(ctx context.Context) (hd2.Result[[]hd2.Planet], error)
}

// Translator 是正文翻译能力；为 nil 时不做翻译（等价于 translate.enabled=false）。
// 定义成别名是为了让本包的签名不必到处写 translate.Translator，同时不新增一层包装类型。
type Translator = translate.Translator

// Handlers 返回本插件提供的指令。
//   - b 用于回消息；
//   - renderer 为 nil 表示未启用渲染（config.render.enabled=false 时 main 会传一个「渲染必失败」的实现），
//     两条指令都会直接走文本回退；
//   - loc 是展示时区，为 nil 或加载失败时按 Asia/Shanghai 处理；
//   - trans 是正文翻译层：为 nil 时正文保持英文（例如配置里关掉了翻译），
//     调用方给 translate.Passthrough() 也能启动，但那会被判成「没翻动」而在卡片上注明翻译不可用，
//     所以「关掉翻译」请传 nil。
//
// 机器人只服务配置里指定的群，其它会话的指令一律忽略。
func Handlers(b Sender, svc OrdersService, renderer render.Renderer, loc *time.Location, trans translate.Translator) []bot.Handler {
	h := &handler{
		sender:   b,
		svc:      svc,
		renderer: renderer,
		display:  plugutil.DisplayLocation(loc),
		trans:    trans,
	}
	return []bot.Handler{
		{Name: "assignments", Run: h.assignments},
		{Name: "dispatches", Run: h.dispatches},
	}
}

// handler 把两条指令共用的依赖收在一起，避免每个闭包各带一份参数。
type handler struct {
	sender   Sender
	svc      OrdersService
	renderer render.Renderer
	display  *time.Location
	trans    translate.Translator
}

// assignments 处理 /assignments：取重要指令 → 出图（上游没有指令时出占位卡）。
func (h *handler) assignments(update tgbotapi.Update) error {
	// 非消息更新、没有会话、不是配置里指定的群：一律忽略（守卫共用 plugutil.TargetMessage）。
	msg, ok := plugutil.TargetMessage(update, h.sender)
	if !ok {
		return nil
	}
	chatID := msg.Chat.ID

	ctx, cancel := context.WithTimeout(context.Background(), plugutil.Timeout)
	defer cancel()

	res, err := h.svc.Assignments(ctx)
	if err != nil {
		log.Printf("/assignments 查询失败 chat=%d err=%v", chatID, err)
		return h.sender.Reply(chatID, plugutil.ErrorReply(err, assignmentsFailureReply), msg.MessageID)
	}

	// 空列表不是错误（实测上游就是返回 []）：照常出一张「暂无重要指令」的占位卡。
	fetchedAt := res.FetchedAt.In(h.display)
	// 星球名要在构建卡片前拿到：任务行里的星球是索引，不取列表就只能显示编号。
	// 这一步是尽力而为的（见 planetNames），拿不到也照常出卡。
	planets := h.planetNames(ctx, res.Value, chatID)
	card := BuildTranslatedAssignments(ctx, res.Value, fetchedAt, res.Stale, h.trans, planets)

	renderCtx, renderCancel := context.WithTimeout(context.Background(), plugutil.Timeout)
	defer renderCancel()
	img, rerr := h.render(renderCtx, render.Card{Name: assignmentsCardName, Data: card})
	if rerr != nil {
		log.Printf("/assignments 渲染失败 chat=%d err=%v", chatID, rerr)
		return h.sender.Reply(chatID, FormatAssignmentsText(card), msg.MessageID)
	}
	return h.sender.SendPhoto(chatID, img, msg.MessageID)
}

// planetNames 取「星球索引 → 星球原名」，供任务里的星球索引（valueType 12）查名。
//
// 只有任务里真的出现星球索引时才去抓星球列表：常见的「消灭敌人」型指令不带星球，
// 不该为它多打一次上游。
// 抓不到不算 /assignments 失败——指令照常出图，只是这些任务退回「星球 #N」：
// 星球名缺失远没有「整张卡片报错」严重，两者不能绑在一起。
func (h *handler) planetNames(ctx context.Context, list []hd2.Assignment, chatID int64) PlanetNames {
	if !needsPlanetNames(list) {
		return nil
	}
	res, err := h.svc.Planets(ctx)
	if err != nil {
		log.Printf("/assignments 取星球列表失败 chat=%d err=%v", chatID, err)
		return nil
	}
	names := make(PlanetNames, len(res.Value))
	for _, p := range res.Value {
		names[p.Index] = p.Name
	}
	return names
}

// dispatches 处理 /dispatches [n]：取简报 → 按条数出图。
// 条数缺省、非数字或小于 1 时按默认 3 条，超过 10 条夹到 10 条（见 DispatchCount）。
func (h *handler) dispatches(update tgbotapi.Update) error {
	// 非消息更新、没有会话、不是配置里指定的群：一律忽略（守卫共用 plugutil.TargetMessage）。
	msg, ok := plugutil.TargetMessage(update, h.sender)
	if !ok {
		return nil
	}
	chatID := msg.Chat.ID
	count := DispatchCount(plugutil.CommandArg(msg.Text))

	ctx, cancel := context.WithTimeout(context.Background(), plugutil.Timeout)
	defer cancel()

	res, err := h.svc.Dispatches(ctx)
	if err != nil {
		log.Printf("/dispatches 查询失败 chat=%d err=%v", chatID, err)
		return h.sender.Reply(chatID, plugutil.ErrorReply(err, dispatchesFailureReply), msg.MessageID)
	}

	fetchedAt := res.FetchedAt.In(h.display)
	// 本次展示的条目：上游按最新在前返回，取前 count 条。
	shown := res.Value
	if len(shown) > count {
		shown = shown[:count]
	}
	// 翻译在构建卡片时进行：先按预算清洗与截断英文，再把正文送译，
	// 译文同样受 maxDispatchRunes 约束（S4：正常情况下一条都不截，
	// 只有上游给出异常长内容才会碰到终局防线）。
	//
	// 分页（S4.1）：整卡超过高度安全线时先拆成 2~3 页图，而不是直接退文本。
	// 每页只装一部分条目，分页后每页的「每条最多多少字」预算反而更宽松、截断更少。
	// 只翻译一次，分页重试和文本降级复用同一份结果，避免过期上下文二次翻译丢失译文。
	fullCard := BuildTranslatedDispatches(ctx, shown, len(shown), fetchedAt, res.Stale, h.trans)
	pageOf := func(from, to, page, pages int) DispatchesCard {
		card := fullCard
		card.Items = fullCard.Items[from:to]
		card.Note = render.WithPageNote(card.Note, page, pages)
		return card
	}
	fallback := func() error {
		card := pageOf(0, len(shown), 1, 1)
		return h.sender.Reply(chatID, FormatDispatchesText(card), msg.MessageID)
	}
	if h.renderer == nil {
		return fallback()
	}
	renderCtx, renderCancel := context.WithTimeout(context.Background(), plugutil.Timeout)
	defer renderCancel()
	imgs, rerr := render.Paginate(renderCtx, h.renderer, len(shown),
		func(page, pages, from, to, total int) render.Card {
			return render.Card{Name: dispatchesCardName, Data: pageOf(from, to, page, pages)}
		})
	if rerr != nil {
		log.Printf("/dispatches 渲染失败 chat=%d err=%v", chatID, rerr)
		return fallback()
	}
	if len(imgs) > 1 {
		log.Printf("/dispatches 内容超长，分 %d 页发送 chat=%d", len(imgs), chatID)
	}
	for i, img := range imgs {
		// 第一页回复用户的命令，后续页不再各自回复一次（免得刷出好几条「回复 XX」）。
		replyTo := int64(0)
		if i == 0 {
			replyTo = msg.MessageID
		}
		if err := h.sender.SendPhoto(chatID, img, replyTo); err != nil {
			return err
		}
	}
	return nil
}

// render 调用渲染引擎；未启用渲染（renderer 为 nil）时返回错误，让调用方走文本回退。
func (h *handler) render(ctx context.Context, card render.Card) ([]byte, error) {
	if h.renderer == nil {
		return nil, errors.New("渲染未启用")
	}
	return h.renderer.Render(ctx, card)
}

// DispatchCount 解析 /dispatches 的条数参数：
//   - 不带参数、参数不是整数（例如「/dispatches 三条」）、或者小于 1（例如「0」「-5」）→ 默认 3 条；
//   - 1..maxDispatchCount 的整数 → 按用户说的条数；
//   - 超过 maxDispatchCount → 夹到上界（用户要「100 条」时给 10 条）。
//
// 口径与 BuildDispatchesCard 完全一致：条数缺失或不合法时两边都退回默认 3 条。
// 夹到边界而不是报错：用户打错一个数字时，给一份能用结果比给一句用法说明更友好。
func DispatchCount(arg string) int {
	n, err := strconv.Atoi(strings.TrimSpace(arg))
	if err != nil || n < 1 {
		return defaultDispatchCount
	}
	if n > maxDispatchCount {
		return maxDispatchCount
	}
	return n
}
