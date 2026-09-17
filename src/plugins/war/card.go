package war

import (
	"fmt"
	"time"

	"hd2_bot/src/hd2"
	"hd2_bot/src/plugins/plugutil"
	"hd2_bot/src/render"
)

// warCardName 是 /war 使用的卡片模板名，必须与 src/render/templates/war.tmpl 的文件名一致。
const warCardName = "war"

// warEmblem 是卡片头栏的徽标素材逻辑名（超级地球），对应 src/render/assets/game/faction/humans.svg。
const warEmblem = "emblem.super_earth"

// WarCard 是 war.tmpl 的视图模型：字段全是已经格式化、本地化好的字符串，
// 模板只负责排版，不做任何数值计算——格式规则只留一份实现，模板与文本才不会各说一套。
//
// 数据时间与过期标记统一放在内嵌的 render.Meta 里（卡片外壳负责显示），这里刻意不再各存一份：
// 同一个状态存两处，外壳与正文就可能显示成两个不同的值。
//
// 刻意不展示的字段（实测不可信，放上卡片只会误导群友）：
//   - War.Now / War.Ended：实测分别返回 1972 与 2028 年，要「数据时间」请用 Meta.DataTime；
//   - Statistics.Accurracy：口径自相矛盾（bulletsHit > bulletsFired、accuracy 恒为 100）；
//   - Statistics.Revives：口径不明，与群友实际看到的战局对不上。
type WarCard struct {
	render.Meta

	PlayerCount     string // 在线士兵，千分位
	MissionsWon     string // 任务胜利场次，千分位
	MissionsLost    string // 任务失败场次，千分位
	SuccessRate     string // 任务成功率，保留一位小数，例如 89.7%
	TerminidKills   string // 终结族击杀数，千分位
	AutomatonKills  string // 机器人击杀数，千分位
	IlluminateKills string // 光能者击杀数，千分位
}

// BuildWarCard 把领域模型转成卡片视图模型。
// 数字格式复用共享的 plugutil.FormatInt 与 FormatWar 的 missionSuccessRate，保证文本与图片两种形态的数字完全一致。
//
// fetchedAt 必须是已转换到展示时区的时间：Result.FetchedAt 在实时取数时是本地时区、
// 快照降级时是 UTC，不统一转换会出现同一条指令前后差 8 小时。零值表示快照没有时间，
// 显示「未知」而不是 0001 年。war 为 nil 时按空数据渲染，不会 panic。
func BuildWarCard(war *hd2.War, fetchedAt time.Time, stale bool) WarCard {
	if war == nil {
		war = &hd2.War{}
	}
	stat := war.Statistics

	return WarCard{
		Meta: render.Meta{
			Title:    "银河战况",
			DataTime: plugutil.DataTimeText(fetchedAt),
			Stale:    stale,
			Emblem:   warEmblem,
		},
		PlayerCount:     plugutil.FormatInt(stat.PlayerCount),
		MissionsWon:     plugutil.FormatInt(stat.MissionsWon),
		MissionsLost:    plugutil.FormatInt(stat.MissionsLost),
		SuccessRate:     fmt.Sprintf("%.1f%%", missionSuccessRate(war)),
		TerminidKills:   plugutil.FormatInt(stat.TerminidKills),
		AutomatonKills:  plugutil.FormatInt(stat.AutomatonKills),
		IlluminateKills: plugutil.FormatInt(stat.IlluminateKills),
	}
}
