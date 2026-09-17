package planets

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"hd2_bot/src/core/bot"
	"hd2_bot/src/hd2"
	"hd2_bot/src/plugins/plugutil"
	"hd2_bot/src/render"
)

// planetUsageText 是不带参数或参数无法解析时的用法说明。
// 这条文案是常量、不经过 bot.Escape，所以刻意不用尖括号占位：MarkdownV2 的保留字符是
// "_*[]()~`>#+-=|{}.!"（'>' 在列，'<' 不在），用中文写清楚比记这套规则更不容易出错。
const planetUsageText = "用法：/planet 星球编号 或 星球名（中文、英文都行），例如 /planet 天园六IV"

// FormatPlanetsText 把战线总览渲染成 MarkdownV2 文本，用于渲染失败或未启用渲染时的回退。
// 数字与卡片完全一致（同一个 PlanetsSummary），群友不会看到两套口径。
// fetchedAt 必须是已转换到展示时区的时间。
func FormatPlanetsText(summary PlanetsSummary, fetchedAt time.Time, stale bool) string {
	var b strings.Builder
	b.WriteString("*战线总览*\n")
	fmt.Fprintf(&b, "在线士兵：%s\n", bot.Escape(summary.PlayerCount))
	fmt.Fprintf(&b, "进攻中：%s ｜ 有事件：%s ｜ 星球总数：%s\n",
		bot.Escape(summary.Contested), bot.Escape(summary.EventCount), bot.Escape(summary.Total))
	// 热点星球与卡片同一份顺序、同一套字段：图片与文本不会出现「图上有、文本里没有」。
	if len(summary.Hotspots) > 0 {
		b.WriteString("\n*热点星球*\n")
		for i, row := range summary.Hotspots {
			fmt.Fprintf(&b, "%d\\. %s\n", i+1, formatRowText(row))
		}
	}

	b.WriteString(plugutil.DataTimeLine(render.Meta{DataTime: plugutil.FormatDataTime(fetchedAt), Stale: stale}))
	return strings.TrimRight(b.String(), "\n")
}

// FormatPlanetText 把单颗星球渲染成 MarkdownV2 文本，用于渲染失败或未启用渲染时的回退。
// 直接吃卡片视图模型（BuildLocalizedPlanetCard 的产物）：字段、译文、截断与数据时间两条路径共用同一份，
// 顺序与卡片一致（卡面头 → 进度条 → 星球情报 → 环境危害 → 星球事件），
// 不会出现「图片里是中文、文本里是英文」或两边截断长度不同的情况。
func FormatPlanetText(card PlanetCard) string {
	var b strings.Builder
	fmt.Fprintf(&b, "*%s*\n", bot.Escape(card.NameChinese))
	fmt.Fprintf(&b, "%s\n", bot.Escape(card.Status))
	if card.NameEnglish != "" {
		fmt.Fprintf(&b, "%s\n", bot.Escape(card.NameEnglish))
	}
	fmt.Fprintf(&b, "派系：%s ｜ 在线士兵：%s\n", bot.Escape(card.Faction), bot.Escape(card.PlayerCount))
	fmt.Fprintf(&b, "编号：%s ｜ 分区：%s\n", bot.Escape(card.Index), bot.Escape(card.Sector))
	fmt.Fprintf(&b, "生物群系：%s ｜ 危害：%s\n", bot.Escape(card.Biome), bot.Escape(card.Hazards))
	if card.BiomeDesc != "" {
		fmt.Fprintf(&b, "群系说明：%s\n", bot.Escape(card.BiomeDesc))
	}
	for _, item := range card.EnvItems {
		if item.Description == "" {
			fmt.Fprintf(&b, "环境危害：%s\n", bot.Escape(item.Name))
			continue
		}
		fmt.Fprintf(&b, "环境危害：%s ｜ %s\n", bot.Escape(item.Name), bot.Escape(item.Description))
	}
	if card.EnvNote != "" {
		fmt.Fprintf(&b, "%s\n", bot.Escape(card.EnvNote))
	}
	fmt.Fprintf(&b, "控制方：%s（初始 %s）\n", bot.Escape(card.Owner), bot.Escape(card.InitialOwner))
	fmt.Fprintf(&b, "血量：%s ｜ 每秒回复：%s\n", bot.Escape(card.HealthText), bot.Escape(card.Regen))
	if card.BarLabel != "" {
		fmt.Fprintf(&b, "%s：%s\n", bot.Escape(card.BarLabel), bot.Escape(card.BarText))
	}
	if card.Timer != "" {
		fmt.Fprintf(&b, "保卫剩余：%s\n", bot.Escape(card.Timer))
	}
	if card.Attacking != "" {
		fmt.Fprintf(&b, "进攻目标：%s\n", bot.Escape(card.Attacking))
	}
	if card.Event != nil {
		fmt.Fprintf(&b, "星球事件：进攻方 %s\n", bot.Escape(card.Event.Faction))
		fmt.Fprintf(&b, "起止：%s 至 %s\n", bot.Escape(card.Event.StartTime), bot.Escape(card.Event.EndTime))
		if card.Event.BarText != "" {
			fmt.Fprintf(&b, "防守剩余：%s\n", bot.Escape(card.Event.BarText))
		}
		fmt.Fprintf(&b, "防守血量：%s\n", bot.Escape(card.Event.HealthText))
	}

	b.WriteString(plugutil.DataTimeLine(card.Meta))
	return strings.TrimRight(b.String(), "\n")
}

// FormatPlanetCandidates 生成「命中多条」的候选列表文案。
// 候选由 ResolvePlanet 截断到最多 maxCandidates 条，这里只负责排版。
func FormatPlanetCandidates(arg string, candidates []hd2.Planet) string {
	var b strings.Builder
	fmt.Fprintf(&b, "*找到多颗匹配「%s」的星球*\n", bot.Escape(arg))
	b.WriteString("请用更精确的名称或编号重试：\n")
	for i, p := range candidates {
		fmt.Fprintf(&b, "%d\\. %s ｜ 编号 %s\n",
			i+1, bot.Escape(plugutil.PlanetDisplayName(p.Name)), bot.Escape(strconv.Itoa(p.Index)))
	}
	return strings.TrimRight(b.String(), "\n")
}

// FormatPlanetNotFound 生成查无此星球的提示，并附带用法说明。
func FormatPlanetNotFound(arg string) string {
	return fmt.Sprintf("找不到「%s」对应的星球。\n%s", bot.Escape(arg), planetUsageText)
}

// formatRowText 拼总览里一行星球的文案：名称、分区、控制方、在线人数与进度。
func formatRowText(row PlanetRow) string {
	parts := []string{
		bot.Escape(plugutil.PlanetDisplayName(row.NameEnglish)),
		bot.Escape(row.Sector),
		bot.Escape(row.Owner),
		"在线 " + bot.Escape(row.PlayerCount),
	}
	if row.BarLabel != "" {
		parts = append(parts, bot.Escape(row.BarLabel)+" "+bot.Escape(row.BarText))
	}
	// 保卫倒计时与卡片同口径：卡片上有这一行，文本回退里也要有，两条路径不能一个有、一个没有。
	if row.Timer != "" {
		parts = append(parts, "保卫剩余 "+bot.Escape(row.Timer))
	}
	if row.Status != "" {
		parts = append(parts, bot.Escape(row.Status))
	}
	return strings.Join(parts, " ｜ ")
}
