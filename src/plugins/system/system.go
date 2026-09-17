// Package system 提供 /help 与 /ping 指令。
//
// /help 优先渲染成图片卡片（上面列出全部指令、一句话说明与示例），渲染失败或未启用渲染时
// 回退 MarkdownV2 文本；两种形态的命令清单都由同一张表生成，不会出现「卡片里有、文本里没有」。
package system

import (
	"context"
	"fmt"
	"log"
	"strings"

	tgbotapi "github.com/ijnkawakaze/telegram-bot-api"

	"hd2_bot/src/core/bot"
	"hd2_bot/src/plugins/plugutil"
	"hd2_bot/src/render"
)

// helpCardName 是帮助卡片的模板名，必须与 src/render/templates/help.tmpl 的文件名一致。
const helpCardName = "help"

// helpEmblem 是帮助卡头栏的徽标素材逻辑名（见 src/render/assets.go 的登记表）。
const helpEmblem = "emblem.super_earth"

// Sender 是本插件需要的回消息能力最小集；*bot.Bot 实现了它，测试注入假实现即可，
// 不必为 /help 去实现整份 bot.Sender。
type Sender interface {
	Ping(chatID, commandID int64) error
	// Reply 以 MarkdownV2 回复文本。
	Reply(chatID int64, text string, replyTo int64) error
	// SendPhoto 发送一张图片卡片。
	SendPhoto(chatID int64, png []byte, replyTo int64) error
	// IsTargetChat 判断会话是否是配置里指定的群。
	IsTargetChat(chatID int64) bool
}

// HelpCommand 是帮助里的一条指令：名称、一句话说明与一个可直接照着打的示例。
// 字段都是现成的中文文案，模板只排版，不做拼接。
type HelpCommand struct {
	Name    string // 指令名（不带斜杠），与 bot.Handler.Name 一致
	Desc    string // 一句话说明
	Example string // 示例（含参数的命令在这里给出可复制的用法）
}

// HelpCard 是 templates/help.tmpl 的视图模型。
type HelpCard struct {
	render.Meta
	Commands []HelpCommand // 全部指令，顺序即卡片上的展示顺序
}

// commands 是命令清单的唯一来源：帮助卡、文本回退与「帮助有没有漏掉一条命令」的用例都用它。
// 新增指令时只改这一处，三种形态同时跟上；main 的接线用例会拿它和实际注册的处理器比对。
var commands = []HelpCommand{
	{Name: "ping", Desc: "检查机器人在线状态，指令与回复自动清理", Example: "/ping"},
	{Name: "war", Desc: "银河战况总览：在线士兵、任务胜负、三族击杀", Example: "/war"},
	{Name: "planets", Desc: "战线总览：在线士兵、星球总数与进攻中数量", Example: "/planets"},
	{Name: "planet", Desc: "查一颗星球：编号、控制方、血量/解放进度与事件", Example: "/planet 天园六IV"},
	{Name: "assignments", Desc: "重要指令（Major Order）：任务、奖励与截止时间", Example: "/assignments"},
	{Name: "dispatches", Desc: "战役简报：最新几条游戏内公告", Example: "/dispatches 5"},
	{Name: "events", Desc: "星球事件：正在进行的防守战及进度", Example: "/events"},
	{Name: "stations", Desc: "民主空间站：状态、战术行动与截止时间", Example: "/stations"},
	{Name: "gun", Desc: "武器图鉴：主武器 / 副武器 / 支援武器", Example: "/gun 焦土"},
	{Name: "strat", Desc: "战备图鉴：哨戒炮 / 轨道 / 飞鹰 / 背包等", Example: "/strat 轨道"},
	{Name: "armor", Desc: "护甲图鉴：等级、护甲值、速度与被动", Example: "/armor 侦察"},
	{Name: "grenade", Desc: "手雷图鉴：投掷物与手雷", Example: "/grenade 燃烧"},
	{Name: "warbonds", Desc: "债券：不带关键字弹搜索按钮，带关键字看某一本明细", Example: "/warbonds 民主爆破"},
	{Name: "enemy", Desc: "敌人图鉴：血量、阵营、体型与伤害", Example: "/enemy 胆汁"},
	{Name: "help", Desc: "显示本帮助", Example: "/help"},
}

// CommandNames 返回帮助里列出的全部指令名（不带斜杠）。
// 供 main 的接线用例断言「实际注册的指令与帮助里写的一致」，避免加了命令忘了写进帮助。
func CommandNames() []string {
	names := make([]string, 0, len(commands))
	for _, c := range commands {
		names = append(names, c.Name)
	}
	return names
}

// Handlers 返回本插件提供的指令；b 用于回消息，renderer 用于把帮助渲染成图片
// （渲染失败或未启用渲染时回退文本），inlinePrefixes 是帮助里要列出的行内查询前缀
// （传实际注册的那批，见 main 的 registeredInlinePrefixes）。
// 机器人只服务配置里指定的群，其它会话的指令一律忽略。
func Handlers(b Sender, renderer render.Renderer) []bot.Handler {
	h := &handler{sender: b, renderer: renderer}
	return []bot.Handler{
		{Name: "help", Run: h.help},
		{Name: "ping", Run: h.ping},
	}
}

// ping 只在目标群执行，删除规则由发送层的专用方法负责。
func (h *handler) ping(update tgbotapi.Update) error {
	msg, ok := plugutil.TargetMessage(update, h.sender)
	if !ok {
		return nil
	}
	return h.sender.Ping(msg.Chat.ID, msg.MessageID)
}

// handler 把两个指令共用的依赖收在一起，避免每个闭包各带一份参数。
type handler struct {
	sender   Sender
	renderer render.Renderer
}

// help 处理 /help：优先出图，渲染失败（含未启用渲染）时回退与卡片口径一致的文本。
func (h *handler) help(update tgbotapi.Update) error {
	// 非消息更新、没有会话、不是配置里指定的群：一律忽略（守卫共用 plugutil.TargetMessage）。
	msg, ok := plugutil.TargetMessage(update, h.sender)
	if !ok {
		return nil
	}
	chatID := msg.Chat.ID

	ctx, cancel := context.WithTimeout(context.Background(), plugutil.Timeout)
	defer cancel()

	if h.renderer != nil {
		img, err := h.renderer.Render(ctx, render.Card{Name: helpCardName, Data: BuildHelpCard()})
		if err == nil {
			return h.sender.SendPhoto(chatID, img, msg.MessageID)
		}
		log.Printf("/help 渲染失败 chat=%d err=%v", chatID, err)
	}
	return h.sender.Reply(chatID, helpText(), msg.MessageID)
}

// BuildHelpCard 构造帮助卡片视图模型。帮助是静态内容，没有「数据时间」，
// 因此 Meta.DataTime 留空（模板会隐藏这一行），也不会出现过期角标。
func BuildHelpCard() HelpCard {
	list := make([]HelpCommand, len(commands))
	copy(list, commands)
	return HelpCard{
		Meta: render.Meta{
			Title:  "指令一览",
			Emblem: helpEmblem,
		},
		Commands: list,
	}
}

// helpText 由命令清单生成 MarkdownV2 文本，用于渲染失败或未启用渲染时的回退。
// 与卡片同源：新增指令只改 commands 一处，两边同时生效。
func helpText() string {
	var b strings.Builder
	for _, c := range commands {
		// 指令名固定由字母组成，不需要转义；说明与示例交给 bot.Escape，
		// 免得哪天写了带 '-'、'.' 的说明就把整条消息弄成非法 MarkdownV2。
		fmt.Fprintf(&b, "/%s \\- %s\n", c.Name, bot.Escape(c.Desc))
		fmt.Fprintf(&b, "  示例：%s\n", bot.Escape(c.Example))
	}
	return b.String()
}
