package orders

import (
	"fmt"
	"strings"

	"hd2_bot/src/core/bot"
	"hd2_bot/src/plugins/plugutil"
)

// FormatAssignmentsText 把重要指令渲染成 MarkdownV2 文本，用于渲染失败或未启用渲染时的回退。
// 直接吃卡片视图模型：正文、截断与占位文案两条路径共用同一份，不会出现「图里有、文本里没有」。
func FormatAssignmentsText(card AssignmentsCard) string {
	var b strings.Builder
	b.WriteString("*重要指令*\n")

	if card.Empty {
		// 空列表是正常状态（实测上游就返回 []），文本回退也要这么说，不能写成查询失败。
		b.WriteString("暂无重要指令。\n")
		b.WriteString("上游当前没有进行中的重要指令，这是正常状态。\n")
	} else {
		fmt.Fprintf(&b, "进行中：%s 条\n", bot.Escape(card.Count))
		for _, item := range card.Items {
			fmt.Fprintf(&b, "\n*%s*\n", bot.Escape(item.Title))
			fmt.Fprintf(&b, "%s\n", bot.Escape(item.Briefing))
			fmt.Fprintf(&b, "任务：%s ｜ 奖励：%s\n", bot.Escape(item.Tasks), bot.Escape(item.Reward))
			fmt.Fprintf(&b, "截止：%s\n", bot.Escape(item.Expiration))
			if item.Progress != "" {
				fmt.Fprintf(&b, "进度：%s\n", bot.Escape(item.Progress))
			}
		}
	}
	for _, note := range card.Notes {
		fmt.Fprintf(&b, "%s\n", bot.Escape(note))
	}
	b.WriteString(plugutil.DataTimeLine(card.Meta))
	return strings.TrimRight(b.String(), "\n")
}

// FormatDispatchesText 把战役简报渲染成 MarkdownV2 文本，用于渲染失败或未启用渲染时的回退。
// 正文已经过清理与截断（与卡片用的是同一份），过长的整体消息由 bot.Reply 负责分片。
func FormatDispatchesText(card DispatchesCard) string {
	var b strings.Builder
	b.WriteString("*战役简报*\n")

	if len(card.Items) == 0 {
		b.WriteString("暂无战役简报。\n")
	} else {
		for _, item := range card.Items {
			fmt.Fprintf(&b, "\n*第 %s 条* ｜ %s\n", bot.Escape(item.Ordinal), bot.Escape(item.Published))
			fmt.Fprintf(&b, "%s\n", bot.Escape(item.Message))
			if item.Truncated {
				b.WriteString("（正文过长，已截断显示）\n")
			}
		}
		// 截断口径与卡片同源：群里看到半句话时能知道这是卡片容量限制，不是上游只给了这些。
		if card.Note != "" {
			fmt.Fprintf(&b, "%s\n", bot.Escape(card.Note))
		}
		// 翻译说明与卡片同源：文本回退时也要说清「正文为什么是英文」。
		if card.TranslateNote != "" {
			fmt.Fprintf(&b, "%s\n", bot.Escape(card.TranslateNote))
		}
	}
	b.WriteString(plugutil.DataTimeLine(card.Meta))
	return strings.TrimRight(b.String(), "\n")
}
