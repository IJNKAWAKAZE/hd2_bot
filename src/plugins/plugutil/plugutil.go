// Package plugutil 放各指令插件共用的展示层小件：查询超时、数字格式化、
// 上游错误到中文提示的映射、展示时区，以及限流文案。
//
// 抽出来的理由：这些东西原先在 war 与 planets 里各有一份逐字相同的实现，
// 文案或时区规则一改就得改好几处，漏一处就会出现「同一个错误两种说法」。
// 这里只放与业务无关的通用件，以及被多个插件（含推送卡片）共用的上游枚举文案：
// 派系中文名与配色、战术行动译名与状态。后者原先在 planets 与 station 里各抄了一份，
// 两处一旦改岔就会让同一颗星球在两张卡片上叫两个名字。
// 各插件自己的领域文案（例如「暂时获取不到战况数据」）仍然留在各自插件里，
// 避免把某条命令的说法强加给别的命令。
package plugutil

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"

	tgbotapi "github.com/ijnkawakaze/telegram-bot-api"

	"hd2_bot/src/core/bot"
	"hd2_bot/src/hd2"
	"hd2_bot/src/hd2/glossary"
	"hd2_bot/src/render"
)

// Timeout 是单条查询指令允许的最长执行时间，取数与出图共用这一个预算：
// 取数慢的时候渲染自然缩短，不会出现「取数很快、渲染又等一整个超时」的叠加等待。
const Timeout = 20 * time.Second

const (
	// RateLimitedReply 是上游限流时给群友看的中文提示：说明原因，而不是笼统地说「失败了」。
	RateLimitedReply = "上游接口限流中，请稍后再试。"

	// FailureReply 是取数失败的通用兜底文案，供没有专属说法的插件使用。
	FailureReply = "暂时获取不到数据，请稍后再试。"
)

// 展示层的两种空值占位。分工是固定的，别混用：
//   - UnknownText（「未知」）表示这个值本该有、但上游没给，例如截止时间；
//   - DashText（「—」）表示这个字段本来就可能没有内容，例如上游没给名字的空间站。
//
// 抽出来的理由与格式化辅助相同：同一个缺失状态在卡片与文本回退里必须是同一句话。
const (
	// UnknownText 是「本该有、但上游没给」的占位文案。
	UnknownText = "未知"
	// DashText 是「没有值」的占位文案。
	DashText = "—"
)

// ErrorReply 把上游错误转成给群友看的中文提示：限流单独说明原因，其它错误用调用方给的领域文案。
// failureReply 为空（或只有空白）时退回 FailureReply，保证任何情况下都有一句能发出去的话。
func ErrorReply(err error, failureReply string) string {
	if errors.Is(err, hd2.ErrRateLimited) {
		return RateLimitedReply
	}
	if strings.TrimSpace(failureReply) == "" {
		return FailureReply
	}
	return failureReply
}

// FormatInt 给整数加千分位，便于阅读（例如 23162 → 23,162）。
// 展示层所有整数都走这里，卡片与文本回退的数字格式因此永远一致。
func FormatInt(v int64) string {
	s := strconv.FormatInt(v, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	parts = append([]string{s}, parts...)
	out := strings.Join(parts, ",")
	if neg {
		out = "-" + out
	}
	return out
}

// FormatDataTime 把数据时间格式化成卡片右上角与文本回退共用的格式（2006-01-02 15:04:05）。
// 零值返回空串：卡片模板会隐藏这一行，调用方也不必为了「没有时间」再写一次判断。
func FormatDataTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02 15:04:05")
}

// DataTimeText 把数据时间转成卡片上的文案；零值（快照没有时间）返回「未知」，不显示 0001 年。
// 与 FormatDataTime 的分工：那个返回空串，供「没有时间就整行隐藏」的卡片外壳用；
// 这个总有一句话可说，供「数据时间」是正文一行的卡片（例如战况卡）用。
func DataTimeText(t time.Time) string {
	return DefaultText(FormatDataTime(t), UnknownText)
}

// MinuteTimeText 把时间格式化到展示时区（精确到分钟，例如 2026-09-16 20:39）；零值返回 fallback。
// loc 为 nil 时原样使用时间自带的时区。fallback 由调用方给（DashText 或 UnknownText）：
// 「没有值」和「不知道」是两种意思，不该由这里替调用方决定，但格式必须是同一份实现。
func MinuteTimeText(t time.Time, loc *time.Location, fallback string) string {
	if t.IsZero() {
		return fallback
	}
	return minuteTimeText(t, loc)
}

// MinuteTimePtrText 是 MinuteTimeText 的指针版本：上游的可选时间字段是 *time.Time，
// nil 与零值同样按「没给」处理，调用方不必先解引用再判空。
func MinuteTimePtrText(t *time.Time, loc *time.Location, fallback string) string {
	if t == nil {
		return fallback
	}
	return MinuteTimeText(*t, loc, fallback)
}

// minuteTimeText 把非零时间转到展示时区并格式化到分钟。
func minuteTimeText(t time.Time, loc *time.Location) string {
	if loc != nil {
		t = t.In(loc)
	}
	return t.Format("2006-01-02 15:04")
}

// DefaultText 返回第一个非空（去掉首尾空白后）的字符串；都为空时返回 fallback。
// 展示层用它兜住上游的空字段，避免卡片上出现空白让人误以为数据是 0。
func DefaultText(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

// CommandArg 取出指令后面的参数：兼容「/planet 参数」「/planet@机器人名 参数」与多余空格。
// 按第一个空白字符切分而不是 strings.Fields：英文星球名本身带空格（acamar iv），不能按空格拆词。
// 没有参数（或文本不是指令）时返回空串。
func CommandArg(text string) string {
	s := strings.TrimSpace(text)
	if !strings.HasPrefix(s, "/") {
		return ""
	}
	idx := strings.IndexFunc(s, unicode.IsSpace)
	if idx < 0 {
		return "" // 只有指令本身，没有参数
	}
	return strings.TrimSpace(s[idx:])
}

// DisplayLocation 返回展示用时区：优先用调用方传入的时区，其次按 Asia/Shanghai 加载，
// 都不可用时回落到固定东八区（中国没有夏令时，两者结果一致）。
//
// 存在的意义是「调用链上任何一环传了 nil 也不会翻车」：插件拿到的可能是没配时区的调用方，
// Windows 上还可能因为缺 tzdata 加载失败。
func DisplayLocation(loc *time.Location) *time.Location {
	if loc != nil {
		return loc
	}
	if shanghai, err := time.LoadLocation("Asia/Shanghai"); err == nil {
		return shanghai
	}
	return time.FixedZone("CST", 8*3600)
}

// InlineSearchMarkup 构造「点一下就在输入框里带出前缀」的行内搜索按钮。
// 各指令用自己那一个前缀（武器- / 星球- …），按钮文案由调用方给：
// 文案与前缀必须对得上，否则用户点了按钮却跳到别的前缀上。
func InlineSearchMarkup(buttonText, prefix string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(
		tgbotapi.InlineKeyboardButton{Text: buttonText, SwitchInlineQueryCurrentChat: &prefix},
	))
}

// InlineAnswerer 是行内查询需要的应答能力；*bot.Bot 实现它，测试注入假实现。
type InlineAnswerer interface {
	// AnswerInline 应答一次行内查询；results 允许为空，表示「没有匹配结果」。
	AnswerInline(queryID string, results []interface{}) error
}

// AnswerInline 应答一次行内查询，必要时把 nil 结果归一化成空数组。
//
// 两处口径在这里一次定死，各插件的行内查询因此不会出现「有的命令能应答、有的报 null」：
//   - nil 必须换成空切片：nil 会被序列化成 null，而 Telegram 要求 results 是数组；
//   - 必须走 answerer 的 AnswerInline：bot 层在那里固定了 cache_time=0，战况常变，
//     不能吃 Telegram 的默认缓存（绕开它就绕开了这个约定）。
//
// 应答失败上抛给调用方（上游数据问题已经在调用方那边消化掉了，这里只有真的发不出去才会报错）。
func AnswerInline(answerer InlineAnswerer, queryID string, results []interface{}) error {
	if results == nil {
		results = []interface{}{}
	}
	if err := answerer.AnswerInline(queryID, results); err != nil {
		return fmt.Errorf("应答行内查询失败 query=%s：%w", queryID, err)
	}
	return nil
}

// ChatChecker 是「这个会话要不要服务」的最小依赖：插件侧的回消息接口都包含 IsTargetChat。
type ChatChecker interface {
	// IsTargetChat 判断会话是否是配置里指定的群。
	IsTargetChat(chatID int64) bool
}

// TargetMessage 从更新里取出要服务的群消息；ok 为 false 时调用方直接忽略这条更新。
// 判空顺序与各指令原先逐字相同的写法一致：非消息更新（update.Message 为 nil）、
// 没有会话（Chat 为 nil）、不是配置里指定的群，三种情况一律不回应。
// 抽出来是因为九个指令各抄了一份同样的守卫，任何一处漏判都会让机器人回应别的群。
func TargetMessage(update tgbotapi.Update, checker ChatChecker) (*tgbotapi.Message, bool) {
	msg := update.Message
	if msg == nil || msg.Chat == nil || !checker.IsTargetChat(msg.Chat.ID) {
		return nil, false
	}
	return msg, true
}

// DataTimeLine 拼文本回退里的「数据时间」一行与过期提示（MarkdownV2 文本，正文由调用方拼好）。
// 时间缺失时写「未知」，不显示 0001 年；meta.Stale 为真时追加一句过期提示。
// 时间里的 '-' 等字符会一并转义：文本回退和卡片因此对「数据是什么时候的」说同一句话。
func DataTimeLine(meta render.Meta) string {
	line := "数据时间：未知"
	if meta.DataTime != "" {
		line = "数据时间：" + bot.Escape(meta.DataTime)
	}
	if meta.Stale {
		return line + "\n（数据可能已过期，上游接口暂时不可用）"
	}
	return line
}

// PercentOf 返回 value/total 的百分比（0-100，一位小数）；total <= 0 时返回 0。
// 上游偶尔给出 health > maxHealth 之类的脏数据，结果会夹在 0-100 之间，
// 避免进度条宽度写出 style="width: 137%"。
func PercentOf(value, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return round1(clampPercent(float64(value) / float64(total) * 100))
}

// PercentText 把百分比格式化成「80.8%」。
// 进度条的宽度与文案用同一个数值，不会出现「条 80.8%、字 81%」。
func PercentText(percent float64) string {
	return fmt.Sprintf("%.1f%%", percent)
}

// clampPercent 把百分比夹在 0-100。
func clampPercent(percent float64) float64 {
	if percent < 0 {
		return 0
	}
	if percent > 100 {
		return 100
	}
	return percent
}

// round1 四舍五入到一位小数。
func round1(v float64) float64 {
	return math.Round(v*10) / 10
}

// PlanetDisplayName 按 spec §5.2 的规则拼星球名：中英都有时「中文（English）」，
// 只有一边时只显示那一个，两边都为空时给「未知」，绝不返回空串。
// 事件卡与星球卡都用它，同一个星球在两张卡片上的写法不会不一样。
func PlanetDisplayName(name string) string {
	english := strings.TrimSpace(name)
	chinese := strings.TrimSpace(glossary.Planet(name))
	if chinese == "" {
		return DefaultText(english, "未知")
	}
	if english == "" || strings.EqualFold(chinese, english) {
		return chinese // 没有译名时 glossary 会返回原文，此时不必重复显示两遍
	}
	return fmt.Sprintf("%s（%s）", chinese, english)
}

// HealthText 拼血量文案「当前 / 上限」；上限缺失或为 0 时返回「—」（除零只会得到 NaN）。
func HealthText(health, maxHealth int64) string {
	if maxHealth <= 0 {
		return "—"
	}
	return FormatInt(health) + " / " + FormatInt(maxHealth)
}

// EventTime 把事件时间格式化到展示时区（精确到分钟）；零值返回「—」（事件卡上的「没有值」），
// 不显示 0001 年。
func EventTime(t time.Time, loc *time.Location) string {
	return MinuteTimeText(t, loc, DashText)
}

// factionInfo 是派系表的一行：归一化 key、中文名与 base.tmpl 里的配色 class。
type factionInfo struct {
	key   string
	name  string
	class string
}

// 派系表：上游实测 currentOwner / faction 有 Humans / Terminids / Automaton / Illuminate（单复数混用），
// 匹配前统一折叠大小写并去掉复数词尾（见 normalizeFaction）。
// 抽到 plugutil 的理由：星球卡、事件卡、空间站卡与推送卡片都要说同一句话，
// 三份各自维护的表迟早会出现「同一颗星球在两处叫两个名字」。
var factionOrder = []factionInfo{
	{key: "human", name: "超级地球", class: "faction--humans"},
	{key: "terminid", name: "终结族", class: "faction--terminid"},
	{key: "automaton", name: "机器人", class: "faction--automaton"},
	{key: "illuminate", name: "光能者", class: "faction--illuminate"},
}

// FactionKey 返回归一化后的派系 key（human / terminid / automaton / illuminate），
// 以及这个取值是否已经在派系表里收录。
//
// 判断「上游给的是不是已知派系」必须走这里，不要拿 FactionName 的返回值跟原文比相等：
// FactionName 为了让未收录的取值原样显示，返回的就是原文（TrimSpace 之后），
// 而原文本身就是中文名的脏数据（例如上游直接给了「超级地球」）会让那种比较误判成「已知」，
// 结果是要么把脏数据并进某个派系，要么让它从派系统计里静默消失。
//
// 未收录（含空串与纯空白）时 known 为 false，key 是归一化后的原文——它只适合当分类与
// 归并用的键，不能拿去当展示文案（展示用 FactionName）。
func FactionKey(faction string) (key string, known bool) {
	if f, ok := factionLookup(faction); ok {
		return f.key, true
	}
	return normalizeFaction(faction), false
}

// factionLookup 按归一化后的 key 查派系表；空串与未收录的取值返回 false 与零值。
func factionLookup(faction string) (factionInfo, bool) {
	normalized := normalizeFaction(faction)
	if normalized == "" {
		return factionInfo{}, false
	}
	for _, f := range factionOrder {
		if normalized == f.key {
			return f, true
		}
	}
	return factionInfo{}, false
}

// FactionName 返回派系的中文名：命中时是中文，未收录时是原文（不猜成某个派系），空串返回空串。
// 调用方负责空值占位（例如「—」），这里只做归一化与查表。
func FactionName(faction string) string {
	trimmed := strings.TrimSpace(faction)
	if trimmed == "" {
		return ""
	}
	if f, ok := factionLookup(trimmed); ok {
		return f.name
	}
	return trimmed
}

// FactionClass 返回派系在 base.tmpl 里的配色 class；未收录时返回空串（模板里退化成普通文字）。
// 四个派系各有配色（超级地球=我方蓝，2026-09-17 皮肤改版起与参考站点的阵营色对齐）。
func FactionClass(faction string) string {
	f, _ := factionLookup(faction)
	return f.class
}

// IsEnemyFaction 判断是否是敌方势力（终结族 / 机器人 / 光能者）。
// 未收录的取值按「不是敌方」处理：宁可显示「易主」，也不要编出一个「解放」。
func IsEnemyFaction(faction string) bool {
	switch normalizeFaction(faction) {
	case "terminid", "automaton", "illuminate":
		return true
	default:
		return false
	}
}

// normalizeFaction 折叠大小写并去掉复数词尾：上游同时出现 Automaton 与 Automatons（实测）。
func normalizeFaction(faction string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(faction)), "s")
}

// tacticalNames 是已知战术行动的简中译名与卡片图标素材（逻辑名见 src/render/assets.go）。
// 上游只给英文原名；译名与图标取自社区参照实现 astrbot_plugin_Helldivers 的 DSS 行动对照表（MIT）。
var tacticalNames = map[string]struct{ name, icon string }{
	"EAGLE STORM":                 {"飞鹰风暴", "tactical.eagle_storm"},
	"ORBITAL BLOCKADE":            {"轨道封锁", "tactical.orbital_blockade"},
	"HEAVY ORDNANCE DISTRIBUTION": {"重型军械分发", "tactical.heavy_ordnance"},
	"ORBITAL NAPALM BARRAGE":      {"轨道燃烧弹幕", "tactical.orbital_napalm"},
}

// TacticalActionName 返回战术行动的卡片展示名与图标素材（图标逻辑名见 src/render/assets.go）：
// 收录的行动给简中译名，未收录的给去掉首尾空白后的原文（不猜成某个已知行动）与空图标。
// 上游大小写不稳定（实测同一行动大小写混用），匹配前统一折叠成大写。
func TacticalActionName(name string) (display, icon string) {
	trimmed := strings.TrimSpace(name)
	if known, ok := tacticalNames[strings.ToUpper(trimmed)]; ok {
		return known.name, known.icon
	}
	return trimmed, ""
}

// TacticalStatus 把上游的 status 数字翻成文案与配色 class：
// 0 未激活、1 准备中、2 进行中、3 冷却中；未收录的取值显示「状态 N」，不猜成某个已知状态。
// 对照表取自社区参照实现（同一份代码库里对该字段的一致用法）；上游没有公布枚举定义。
func TacticalStatus(status int) (text, class string) {
	switch status {
	case 0:
		return "未激活", "tag"
	case 1:
		return "准备中", "tag--gold"
	case 2:
		return "进行中", "tag--win"
	case 3:
		return "冷却中", "tag"
	default:
		return fmt.Sprintf("状态 %d", status), "tag"
	}
}
