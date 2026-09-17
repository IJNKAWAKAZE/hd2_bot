package station

import (
	"fmt"
	"strings"

	"hd2_bot/src/core/bot"
	"hd2_bot/src/plugins/plugutil"
)

// FormatEventsText 把星球事件渲染成 MarkdownV2 文本，用于渲染失败或未启用渲染时的回退。
// 直接吃卡片视图模型：条数、顺序与截断两条路径共用同一份，不会出现「图里 5 起、文本里 8 起」。
func FormatEventsText(card EventsCard) string {
	var b strings.Builder
	b.WriteString("*星球事件*\n")

	if card.Empty {
		b.WriteString("暂无星球事件。\n")
		b.WriteString("当前没有正在进行的防守战或其它星球事件。\n")
	} else {
		for _, item := range card.Items {
			fmt.Fprintf(&b, "\n*%s*\n", bot.Escape(item.PlanetName))
			fmt.Fprintf(&b, "进攻方：%s\n", bot.Escape(item.Faction))
			fmt.Fprintf(&b, "起止：%s 至 %s\n", bot.Escape(item.StartTime), bot.Escape(item.EndTime))
			if item.BarText != "" {
				fmt.Fprintf(&b, "防守剩余：%s（%s）\n", bot.Escape(item.BarText), bot.Escape(item.HealthText))
			}
		}
		if card.MoreText != "" {
			fmt.Fprintf(&b, "\n%s\n", bot.Escape(card.MoreText))
		}
	}
	b.WriteString(plugutil.DataTimeLine(card.Meta))
	return strings.TrimRight(b.String(), "\n")
}

// FormatStationsText 把民主空间站渲染成 MarkdownV2 文本，用于渲染失败或未启用渲染时的回退。
func FormatStationsText(card StationsCard) string {
	var b strings.Builder
	b.WriteString("*民主空间站*\n")

	if card.Empty {
		b.WriteString("暂无空间站数据。\n")
		b.WriteString("上游没有返回民主空间站信息。\n")
	} else {
		for _, item := range card.Items {
			fmt.Fprintf(&b, "\n*%s*\n", bot.Escape(item.Name))
			fmt.Fprintf(&b, "选举 / 跃迁截止：%s\n", bot.Escape(item.ElectionEnd))
			if len(item.Actions) == 0 {
				b.WriteString("战术行动：暂无\n")
				continue
			}
			b.WriteString("战术行动：\n")
			for _, action := range item.Actions {
				fmt.Fprintf(&b, "· %s（%s）\n", bot.Escape(action.Name), bot.Escape(action.Status))
			}
		}
	}
	b.WriteString(plugutil.DataTimeLine(card.Meta))
	return strings.TrimRight(b.String(), "\n")
}
