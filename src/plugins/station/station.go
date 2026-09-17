// Package station 提供 /events 星球事件与 /stations 民主空间站两条查询指令。
//
// 输出形态：优先渲染成图片卡片（见 src/render），渲染失败或未启用渲染时回退 MarkdownV2 纯文本。
// 展示口径以实测上游数据为准：
//   - /events 的数据来自 /planet-events，上游返回的是「星球对象数组」（事件嵌在里面），
//     领域层解析时已把所属星球的编号与名字补进事件，卡片因此能写出「哪颗星球出事了」；
//   - 事件类型、空间站的 flags 与战术行动 status 都是上游的枚举数字，含义未公布的一律按原值显示，
//     只有社区参照实现里有一致用法的那几种（战术行动状态）才翻译，并在卡片上说明来源；
//   - 上游当前下线了 /api/v1/space-stations（实测恒定 404），数据只在 v2 上，见 hd2 的端点表。
package station

import (
	"context"
	"errors"
	"log"
	"time"

	tgbotapi "github.com/ijnkawakaze/telegram-bot-api"

	"hd2_bot/src/core/bot"
	"hd2_bot/src/hd2"
	"hd2_bot/src/plugins/plugutil"
	"hd2_bot/src/render"
)

// 两条指令各自的失败文案：数据来源不同，说法也不同；限流文案与判定逻辑共用 plugutil.ErrorReply。
const (
	eventsFailureReply   = "暂时获取不到星球事件，请稍后再试。"
	stationsFailureReply = "暂时获取不到空间站数据，请稍后再试。"
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

// StationService 是两条指令需要的领域层能力；*hd2.Service 实现了它，测试可注入假实现。
type StationService interface {
	// Events 返回进行中的星球事件；上游没有事件时是空切片。
	Events(ctx context.Context) (hd2.Result[[]hd2.PlanetEvent], error)
	// Stations 返回民主空间站；上游返回空列表时是空切片。
	Stations(ctx context.Context) (hd2.Result[[]hd2.SpaceStation], error)
}

// Handlers 返回本插件提供的指令。
//   - b 用于回消息；
//   - renderer 为 nil 表示未启用渲染（config.render.enabled=false 时 main 会传一个「渲染必失败」的实现），
//     两条指令都会直接走文本回退；
//   - loc 是展示时区，为 nil 或加载失败时按 Asia/Shanghai 处理。
//
// 机器人只服务配置里指定的群，其它会话的指令一律忽略。
func Handlers(b Sender, svc StationService, renderer render.Renderer, loc *time.Location) []bot.Handler {
	h := &handler{
		sender:   b,
		svc:      svc,
		renderer: renderer,
		display:  plugutil.DisplayLocation(loc),
	}
	return []bot.Handler{
		{Name: "events", Run: h.events},
		{Name: "stations", Run: h.stations},
	}
}

// handler 把两条指令共用的依赖收在一起，避免每个闭包各带一份参数。
type handler struct {
	sender   Sender
	svc      StationService
	renderer render.Renderer
	display  *time.Location
}

// events 处理 /events：取星球事件 → 出图（没有事件时出占位卡）。
func (h *handler) events(update tgbotapi.Update) error {
	// 非消息更新、没有会话、不是配置里指定的群：一律忽略（守卫共用 plugutil.TargetMessage）。
	msg, ok := plugutil.TargetMessage(update, h.sender)
	if !ok {
		return nil
	}
	chatID := msg.Chat.ID

	ctx, cancel := context.WithTimeout(context.Background(), plugutil.Timeout)
	defer cancel()

	res, err := h.svc.Events(ctx)
	if err != nil {
		log.Printf("/events 查询失败 chat=%d err=%v", chatID, err)
		return h.sender.Reply(chatID, plugutil.ErrorReply(err, eventsFailureReply), msg.MessageID)
	}

	// 空列表不是错误：照常出一张「暂无星球事件」的占位卡。
	fetchedAt := res.FetchedAt.In(h.display)
	card := BuildEventsCard(res.Value, fetchedAt, res.Stale)

	img, rerr := h.render(ctx, render.Card{Name: eventsCardName, Data: card})
	if rerr != nil {
		log.Printf("/events 渲染失败 chat=%d err=%v", chatID, rerr)
		return h.sender.Reply(chatID, FormatEventsText(card), msg.MessageID)
	}
	return h.sender.SendPhoto(chatID, img, msg.MessageID)
}

// stations 处理 /stations：取空间站 → 出图（上游返回空列表时出占位卡）。
func (h *handler) stations(update tgbotapi.Update) error {
	// 非消息更新、没有会话、不是配置里指定的群：一律忽略（守卫共用 plugutil.TargetMessage）。
	msg, ok := plugutil.TargetMessage(update, h.sender)
	if !ok {
		return nil
	}
	chatID := msg.Chat.ID

	ctx, cancel := context.WithTimeout(context.Background(), plugutil.Timeout)
	defer cancel()

	res, err := h.svc.Stations(ctx)
	if err != nil {
		log.Printf("/stations 查询失败 chat=%d err=%v", chatID, err)
		return h.sender.Reply(chatID, plugutil.ErrorReply(err, stationsFailureReply), msg.MessageID)
	}

	fetchedAt := res.FetchedAt.In(h.display)
	card := BuildStationsCard(res.Value, fetchedAt, res.Stale)

	img, rerr := h.render(ctx, render.Card{Name: stationsCardName, Data: card})
	if rerr != nil {
		log.Printf("/stations 渲染失败 chat=%d err=%v", chatID, rerr)
		return h.sender.Reply(chatID, FormatStationsText(card), msg.MessageID)
	}
	return h.sender.SendPhoto(chatID, img, msg.MessageID)
}

// render 调用渲染引擎；未启用渲染（renderer 为 nil）时返回错误，让调用方走文本回退。
func (h *handler) render(ctx context.Context, card render.Card) ([]byte, error) {
	if h.renderer == nil {
		return nil, errors.New("渲染未启用")
	}
	return h.renderer.Render(ctx, card)
}
