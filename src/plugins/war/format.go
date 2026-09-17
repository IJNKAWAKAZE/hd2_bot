// Package war 提供 /war 战况查询指令。
//
// 展示层只使用实测可信的字段：在线士兵、任务胜/败、三族击杀与数据时间。
// 刻意不用 Statistics.Accurracy（实测口径自相矛盾：bulletsHit > bulletsFired、accuracy 恒为 100），
// 也不用 War.Now / War.Ended（实测分别返回 1972 与 2028 年，上游值不可信）。
package war

import (
	"fmt"
	"strings"
	"time"

	"hd2_bot/src/core/bot"
	"hd2_bot/src/hd2"
	"hd2_bot/src/plugins/plugutil"
	"hd2_bot/src/render"
)

// FormatWar 把战况渲染成 MarkdownV2 文本。
// fetchedAt 必须是已转换到展示时区（Asia/Shanghai）的时间：Result.FetchedAt 在实时取数时
// 是本地时区、快照降级时是 UTC，不统一转换会出现差 8 小时的观感。
// stale 为 true 时追加过期提示；war 为 nil 时按空数据渲染，不会 panic。
func FormatWar(war *hd2.War, fetchedAt time.Time, stale bool) string {
	if war == nil {
		war = &hd2.War{}
	}
	var b strings.Builder
	b.WriteString("*银河战况*\n")
	// 玩家数、胜负场次与击杀数按实测可信，放在最前面。
	fmt.Fprintf(&b, "在线士兵：%s\n", plugutil.FormatInt(war.Statistics.PlayerCount))
	fmt.Fprintf(&b, "任务完成：%s 胜 / %s 负\n",
		plugutil.FormatInt(war.Statistics.MissionsWon),
		plugutil.FormatInt(war.Statistics.MissionsLost),
	)
	fmt.Fprintf(&b, "任务成功率：%s\n", bot.Escape(fmt.Sprintf("%.1f%%", missionSuccessRate(war))))
	fmt.Fprintf(&b, "击杀：终结族 %s ｜ 机器人 %s ｜ 光能者 %s\n",
		plugutil.FormatInt(war.Statistics.TerminidKills),
		plugutil.FormatInt(war.Statistics.AutomatonKills),
		plugutil.FormatInt(war.Statistics.IlluminateKills),
	)
	// 数据时间与过期提示统一走 plugutil.DataTimeLine：卡片外壳、文本回退与别的指令都是同一句话，
	// 快照没有时间时写「未知」，不显示 0001 年。
	b.WriteString(plugutil.DataTimeLine(render.Meta{DataTime: plugutil.FormatDataTime(fetchedAt), Stale: stale}))
	return b.String()
}

// missionSuccessRate 由胜负场次计算任务成功率（百分比）；没有场次时返回 0。
func missionSuccessRate(war *hd2.War) float64 {
	total := war.Statistics.MissionsWon + war.Statistics.MissionsLost
	if total <= 0 {
		return 0
	}
	return float64(war.Statistics.MissionsWon) / float64(total) * 100
}
