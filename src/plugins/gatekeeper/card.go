// card.go 组「呼叫演练」的两样东西：
//
//   - 题面图（templates/verify.tmpl）：战备图标 + 名字（型号）+ 召唤指令，纯静态，点按钮不会变；
//   - 提示文本：谁、限时、进度、剩余机会，挂在同一条消息上，每次点击都编辑它。
//
// 分开的原因：图片说明（caption）长度受限、排版也死板，而进度需要频繁更新；
// 题面只负责「看清是哪件战备、指令是什么」，交互状态全在文本里。
package gatekeeper

import (
	"fmt"
	"strconv"
	"strings"

	tgbotapi "github.com/ijnkawakaze/telegram-bot-api"

	"hd2_bot/src/core/bot"
	"hd2_bot/src/render"
)

// callbackPrefix 是按钮回调的命名空间（库按它分发到本插件）。
const callbackPrefix = "verify"

// Step 是题面图上的一格召唤指令。
type Step struct {
	Asset string // 内置箭头素材的逻辑名；空串时模板退回显示 Glyph
	Glyph string
}

// Card 是 templates/verify.tmpl 的视图模型：只有上中下三段（战备图 / 中文名 / 召唤指令），
// 不含任何交互状态，也没有标题栏（Meta.Bare）。
type Card struct {
	render.Meta
	Image      string // 战备图标的 data URI；空串时模板不画图
	TargetName string // 战备中文名
	ShowSteps  bool   // 是否把召唤指令印在图上（verify.show_answer）
	Steps      []Step
}

// buildCard 组题面图。名字会渲染进图片，所以不需要 Markdown 转义。
//
// 不设 Meta 里的任何字段：这张图就是题面本身，外壳因此不画标题栏（规则见 render.Meta 的注释）。
func (h *Handler) buildCard(target Target) Card {
	card := Card{
		TargetName: target.NameZh,
		ShowSteps:  h.cfg.ShowAnswer,
	}
	if h.cfg.ShowAnswer {
		card.Steps = stepsOf(target.Steps)
	}
	return card
}

// stepsOf 把方向序列翻成图上的箭头格。
func stepsOf(dirs []string) []Step {
	steps := make([]Step, 0, len(dirs))
	for _, dir := range dirs {
		steps = append(steps, Step{Asset: Asset(dir), Glyph: Glyph(dir)})
	}
	return steps
}

// progressText 把进度画成一串符号：点对的方向用箭头，剩下的用「?」。
func progressText(challenge *Challenge) string {
	progress := challenge.Progress()
	parts := make([]string, 0, challenge.Total())
	for _, dir := range progress {
		parts = append(parts, Emoji(dir))
	}
	for i := len(progress); i < challenge.Total(); i++ {
		parts = append(parts, "?")
	}
	return strings.Join(parts, " ")
}

// promptText 是提示文本（走 MarkdownV2，昵称与战备名都要转义）。
//
// 题面图没发出去时（没配渲染器、图下载失败、发图失败），答案补在这条文本里：
// 显示答案只有一个落点 —— 图在就印在图里，图不在就写在文本里，绝不两处都写。
func (h *Handler) promptText(session *Session) string {
	challenge := session.Challenge
	target := challenge.Target()
	var b strings.Builder

	b.WriteString(fmt.Sprintf("新兵 %s，呼叫演练开始。\n", bot.Escape(session.Name)))
	b.WriteString(fmt.Sprintf("请在 %s内按顺序点出「%s」的召唤指令。\n\n",
		bot.Escape(h.timeoutText()), bot.Escape(target.NameZh)))
	if h.cfg.ShowAnswer && !session.HasPhoto {
		b.WriteString(fmt.Sprintf("召唤指令：%s\n\n", bot.Escape(dirGlyphs(target.Steps))))
	}
	b.WriteString(fmt.Sprintf("进度：%s（%d/%d）\n",
		bot.Escape(progressText(challenge)), len(challenge.Progress()), challenge.Total()))
	b.WriteString(bot.Escape(fmt.Sprintf("剩余机会：%d 次", challenge.Remaining())))
	return b.String()
}

// dirGlyphs 把方向序列拼成「⬆️ ⬆️ ⬇️」。
func dirGlyphs(dirs []string) string {
	glyphs := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		glyphs = append(glyphs, Emoji(dir))
	}
	return strings.Join(glyphs, " ")
}

// timeoutText 把超时翻成「3 分钟」或「45 秒」。
func (h *Handler) timeoutText() string {
	if h.cfg.Timeout%60 == 0 {
		return fmt.Sprintf("%d 分钟", int(h.cfg.Timeout.Minutes()))
	}
	return fmt.Sprintf("%d 秒", int(h.cfg.Timeout.Seconds()))
}

// keyboard 组按钮：一行四个方向；群内模式再加一行给管理员用的放行/封禁。
func (h *Handler) keyboard(session *Session) tgbotapi.InlineKeyboardMarkup {
	rows := [][]tgbotapi.InlineKeyboardButton{tgbotapi.NewInlineKeyboardRow(
		dirButton(session, DirUp),
		dirButton(session, DirDown),
		dirButton(session, DirLeft),
		dirButton(session, DirRight),
	)}
	if !session.Private {
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			adminButton(session, "pass", "✅ 放行"),
			adminButton(session, "ban", "🚫 封禁"),
		))
	}
	return tgbotapi.NewInlineKeyboardMarkup(rows...)
}

func dirButton(session *Session, dir string) tgbotapi.InlineKeyboardButton {
	return tgbotapi.NewInlineKeyboardButtonData(Emoji(dir), callbackData(session, dir))
}

func adminButton(session *Session, action, text string) tgbotapi.InlineKeyboardButton {
	return tgbotapi.NewInlineKeyboardButtonData(text, callbackData(session, action))
}

// callbackData 拼按钮数据：verify,<用户 id>,<动作>。库按第一个逗号段分发到本插件，
// 用户 id 用来挡住「点别人的验证」，动作是方向或 pass/ban。
func callbackData(session *Session, action string) string {
	return callbackPrefix + "," + strconv.FormatInt(session.UserID, 10) + "," + action
}
