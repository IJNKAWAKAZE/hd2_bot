package war

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

// failureReply 是 /war 的失败文案。领域文案留在插件里（不同命令的数据来源不同、说法也不同），
// 只有「限流 vs 其它」的判定与限流文案是共用的，见 plugutil.ErrorReply。
const failureReply = "暂时获取不到战况数据，请稍后再试。"

// Sender 是 /war 需要的回消息能力最小集；*bot.Bot 实现了它，测试注入假实现即可，
// 不必为一个只发文本与图片的指令去实现整份 bot.Sender。
type Sender interface {
	// Reply 以 MarkdownV2 回复文本。
	Reply(chatID int64, text string, replyTo int64) error
	// SendPhoto 发送一张图片卡片。
	SendPhoto(chatID int64, png []byte, replyTo int64) error
	// IsTargetChat 判断会话是否是配置里指定的群。
	IsTargetChat(chatID int64) bool
}

// WarService 是 /war 需要的领域层能力；*hd2.Service 实现了它，测试可注入假实现。
type WarService interface {
	// War 返回银河战况；返回的错误交由调用方转成中文提示。
	War(ctx context.Context) (hd2.Result[*hd2.War], error)
}

// Handlers 返回本插件提供的指令；b 用于回消息，renderer 用于把战况渲染成图片
// （渲染失败时按约定回退纯文本），loc 用于把数据时间转换到展示时区
// （为 nil 或加载失败时按 Asia/Shanghai 处理）。机器人只服务配置里指定的群，
// 其它会话的指令一律忽略。
func Handlers(b Sender, svc WarService, renderer render.Renderer, loc *time.Location) []bot.Handler {
	display := plugutil.DisplayLocation(loc)
	return []bot.Handler{
		{
			Name: "war",
			Run: func(update tgbotapi.Update) error {
				// 非消息更新、没有会话、不是配置里指定的群：一律忽略（守卫共用 plugutil.TargetMessage）。
				msg, ok := plugutil.TargetMessage(update, b)
				if !ok {
					return nil
				}
				chatID := msg.Chat.ID

				ctx, cancel := context.WithTimeout(context.Background(), plugutil.Timeout)
				defer cancel()

				res, err := svc.War(ctx)
				if err != nil {
					log.Printf("/war 查询失败 chat=%d err=%v", chatID, err)
					return b.Reply(chatID, plugutil.ErrorReply(err, failureReply), msg.MessageID)
				}
				if res.Value == nil {
					log.Printf("/war 返回空数据 chat=%d", chatID)
					return b.Reply(chatID, failureReply, msg.MessageID)
				}

				// Result.FetchedAt 的时区不统一（实时取数是本地时区、快照降级是 UTC），
				// 必须统一转到展示时区再格式化，否则同一条指令会前后差 8 小时。
				// 注意不要用 War.Now / War.Ended：上游返回的值不可信。
				fetchedAt := res.FetchedAt.In(display)

				img, renderErr := renderWarCard(ctx, renderer, BuildWarCard(res.Value, fetchedAt, res.Stale))
				if renderErr != nil {
					// 图片只是展示形态：渲染失败不影响数据本身，按约定回退成与出图前完全一致的
					// 纯文本（同一份数据、同一个时区），保证群友总是能看到战况。
					log.Printf("/war 渲染失败 chat=%d err=%v", chatID, renderErr)
					return b.Reply(chatID, FormatWar(res.Value, fetchedAt, res.Stale), msg.MessageID)
				}
				return b.SendPhoto(chatID, img, msg.MessageID)
			},
		},
	}
}

// renderWarCard 渲染战况卡片；renderer 为 nil（未配置渲染）时按渲染失败处理，由调用方回退纯文本。
// 用查询的 ctx 而不是新开一个超时：渲染与上游查询共享同一个总预算，
// 查询慢的时候渲染自然缩短，不会出现「查询很快、渲染又等一整个超时」的叠加等待。
func renderWarCard(ctx context.Context, renderer render.Renderer, view WarCard) ([]byte, error) {
	if renderer == nil {
		return nil, errors.New("渲染未启用")
	}
	return renderer.Render(ctx, render.Card{Name: warCardName, Data: view})
}
