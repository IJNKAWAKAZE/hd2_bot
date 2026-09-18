package plugutil

import (
	"errors"
	"fmt"
	"testing"
	"time"

	tgbotapi "github.com/ijnkawakaze/telegram-bot-api"

	"hd2_bot/src/hd2"
	"hd2_bot/src/render"
)

// TestTimeout 校验查询超时是 20 秒。
// 这里刻意写死秒数而不是引用 Timeout 自己：拿常量跟自己比，改大改小都会一起变，等于没测。
func TestTimeout(t *testing.T) {
	if got := Timeout; got != 20*time.Second {
		t.Errorf("查询超时应为 20s，实际 %s", got)
	}
}

// TestFormatInt 校验千分位与负数处理。
func TestFormatInt(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{7, "7"},
		{999, "999"},
		{1000, "1,000"},
		{23162, "23,162"},
		{217853802, "217,853,802"},
		{-1234567, "-1,234,567"},
		{1234567890123, "1,234,567,890,123"},
	}
	for _, c := range cases {
		if got := FormatInt(c.in); got != c.want {
			t.Errorf("FormatInt(%d)：期望 %q，实际 %q", c.in, c.want, got)
		}
	}
}

// TestErrorReply 校验限流与其它错误分别给出对应的中文提示，
// 且包装过的限流错误也能认出来（上游错误常常经过 fmt.Errorf 包装）。
func TestErrorReply(t *testing.T) {
	cases := []struct {
		name         string
		err          error
		failureReply string
		want         string
	}{
		{"限流错误", hd2.ErrRateLimited, "暂时获取不到战况数据，请稍后再试。", "上游接口限流中，请稍后再试。"},
		{"包装过的限流错误", fmt.Errorf("查询 war 失败：%w", hd2.ErrRateLimited), "随便什么", "上游接口限流中，请稍后再试。"},
		{"其它错误用领域文案", errors.New("网络不通"), "暂时获取不到星球数据，请稍后再试。", "暂时获取不到星球数据，请稍后再试。"},
		{"领域文案为空时用通用兜底", errors.New("网络不通"), "", FailureReply},
		{"领域文案只有空白时用通用兜底", errors.New("网络不通"), "   ", FailureReply},
		{"nil 错误也走领域文案", nil, "暂时获取不到简报，请稍后再试。", "暂时获取不到简报，请稍后再试。"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ErrorReply(c.err, c.failureReply); got != c.want {
				t.Errorf("ErrorReply 期望 %q，实际 %q", c.want, got)
			}
		})
	}
}

// TestDisplayLocation 校验传入的时区原样返回，nil 时回落到东八区。
// 回落分支不能依赖本机 tzdata：Windows 上 LoadLocation 可能失败，
// 但只要结果仍是 +08:00 的时区，展示时间就是对的。
func TestDisplayLocation(t *testing.T) {
	pass := time.FixedZone("UTC+3", 3*3600)
	if got := DisplayLocation(pass); got != pass {
		t.Errorf("传入的时区应原样返回，实际 %v", got)
	}

	fallback := DisplayLocation(nil)
	if fallback == nil {
		t.Fatal("nil 时区应回落到东八区，不能返回 nil")
	}
	_, offset := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC).In(fallback).Zone()
	if offset != 8*3600 {
		t.Errorf("回落时区应为东八区（偏移 28800 秒），实际 %d", offset)
	}
}

// TestFormatDataTime 校验数据时间的展示格式：零值返回空串（调用方据此隐藏这一行）。
func TestFormatDataTime(t *testing.T) {
	if got := FormatDataTime(time.Time{}); got != "" {
		t.Errorf("零值时间应返回空串，实际 %q", got)
	}
	at := time.Date(2026, 9, 16, 20, 0, 0, 0, time.FixedZone("CST", 8*3600))
	if got := FormatDataTime(at); got != "2026-09-16 20:00:00" {
		t.Errorf("数据时间格式错误：%q", got)
	}
}

// TestDefaultText 校验空值兜底：空白字符串算空，非空值去掉首尾空白后返回。
func TestDefaultText(t *testing.T) {
	cases := []struct {
		value    string
		fallback string
		want     string
	}{
		{"", "—", "—"},
		{"   ", "—", "—"},
		{"\t\n ", "—", "—"},
		{"  Peacock  ", "—", "Peacock"},
		{"值", "—", "值"},
	}
	for _, c := range cases {
		if got := DefaultText(c.value, c.fallback); got != c.want {
			t.Errorf("DefaultText(%q, %q)：期望 %q，实际 %q", c.value, c.fallback, c.want, got)
		}
	}
}

// TestCommandArg 校验指令参数解析：带不带机器人后缀、多余空格、英文名里的空格都要正确。
func TestCommandArg(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{"普通参数", "/planet 天园六IV", "天园六IV"},
		{"英文名含空格", "/planet acamar iv", "acamar iv"},
		{"带机器人后缀", "/planet@maa_remote_bot Peacock", "Peacock"},
		{"多余空格", "  /planet   Peacock  ", "Peacock"},
		{"只有指令", "/planet", ""},
		{"带机器人后缀且无参数", "/planet@maa_remote_bot", ""},
		{"不是指令", "planet Peacock", ""},
		{"空串", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CommandArg(c.text); got != c.want {
				t.Errorf("CommandArg(%q)：期望 %q，实际 %q", c.text, c.want, got)
			}
		})
	}
}

// TestPercentOfAndText 校验百分比计算与文案：夹在 0-100、保留一位小数、分母为 0 时不除零。
func TestPercentOfAndText(t *testing.T) {
	cases := []struct {
		value, total int64
		wantPercent  float64
		wantText     string
	}{
		{0, 0, 0, "0.0%"},
		{5, 0, 0, "0.0%"},
		{-5, 10, 0, "0.0%"},
		{5, 10, 50, "50.0%"},
		{1, 3, 33.3, "33.3%"},
		{2, 3, 66.7, "66.7%"},
		{15, 10, 100, "100.0%"},
	}
	for _, c := range cases {
		got := PercentOf(c.value, c.total)
		if got != c.wantPercent {
			t.Errorf("PercentOf(%d, %d)：期望 %v，实际 %v", c.value, c.total, c.wantPercent, got)
		}
		if text := PercentText(got); text != c.wantText {
			t.Errorf("PercentText(%v)：期望 %q，实际 %q", got, c.wantText, text)
		}
	}
}

// TestDataTimeLine 校验文本回退的「数据时间」一行：时间缺失写「未知」，过期时追加提示。
func TestDataTimeLine(t *testing.T) {
	cases := []struct {
		name string
		meta render.Meta
		want string
	}{
		{"有时间", render.Meta{DataTime: "2026-09-16 20:00:00"}, "数据时间：2026\\-09\\-16 20:00:00"},
		{"没有时间", render.Meta{}, "数据时间：未知"},
		{"过期数据", render.Meta{DataTime: "2026-09-16 20:00:00", Stale: true}, "数据时间：2026\\-09\\-16 20:00:00\n（数据可能已过期，上游接口暂时不可用）"},
		{"没有时间且过期", render.Meta{Stale: true}, "数据时间：未知\n（数据可能已过期，上游接口暂时不可用）"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DataTimeLine(c.meta); got != c.want {
				t.Errorf("DataTimeLine 期望 %q，实际 %q", c.want, got)
			}
		})
	}
}

// TestPlanetDisplayName 校验星球名拼法：中英都有时「中文（English）」，
// 译名缺失时退回英文名，中英同名时只显示一遍，两边都空时给「未知」且绝不返回空串。
func TestPlanetDisplayName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"中英都有译名", "acamar iv", "天园六IV（acamar iv）"},
		{"译名缺失退回英文名", "ZZZ Unknown Planet", "ZZZ Unknown Planet"},
		{"译名表里中英同名只显示一遍", "k", "K"},
		{"两边都空给未知", "   ", "未知"},
		{"空串给未知", "", "未知"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := PlanetDisplayName(c.in)
			if got != c.want {
				t.Errorf("PlanetDisplayName(%q)：期望 %q，实际 %q", c.in, c.want, got)
			}
			if got == "" {
				t.Error("任何输入都不该返回空串：卡片上会留出空白让人误以为数据是 0")
			}
		})
	}
}

// TestHealthText 校验血量文案「当前 / 上限」：上限缺失或为 0 时给「—」，
// 否则两个数都带千分位（除零只会得到 NaN，所以上限是硬条件）。
func TestHealthText(t *testing.T) {
	cases := []struct {
		name              string
		health, maxHealth int64
		want              string
	}{
		{"上限为 0", 100, 0, "—"},
		{"上限为负", 100, -1, "—"},
		{"上限为零且血量也为 0", 0, 0, "—"},
		{"正常血量", 1497963, 1500000, "1,497,963 / 1,500,000"},
		{"血量小于千", 0, 1000, "0 / 1,000"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := HealthText(c.health, c.maxHealth); got != c.want {
				t.Errorf("HealthText(%d, %d)：期望 %q，实际 %q", c.health, c.maxHealth, c.want, got)
			}
		})
	}
}

// TestEventTime 校验事件时间格式化到展示时区（精确到分钟）：零值给「—」，
// loc 为 nil 时用时间自带的时区（不 panic），有 loc 时按 loc 换算。
func TestEventTime(t *testing.T) {
	cst := time.FixedZone("CST", 8*3600)
	utc := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		at   time.Time
		loc  *time.Location
		want string
	}{
		{"零值给破折号", time.Time{}, cst, "—"},
		{"零值且没有时区", time.Time{}, nil, "—"},
		{"按展示时区换算", utc, cst, "2026-09-16 20:00"},
		{"没有时区时用时间自带的时区", utc, nil, "2026-09-16 12:00"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := EventTime(c.at, c.loc); got != c.want {
				t.Errorf("EventTime：期望 %q，实际 %q", c.want, got)
			}
		})
	}
}

// TestMinuteTimeAndDataTimeText 校验两个时间文案入口：
// 值类型与指针类型共用同一份格式化实现，零值/nil 各自返回调用方给的占位文案。
func TestMinuteTimeAndDataTimeText(t *testing.T) {
	cst := time.FixedZone("CST", 8*3600)
	utc := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	ptr := func(t time.Time) *time.Time { return &t }

	if got := MinuteTimeText(utc, cst, DashText); got != "2026-09-16 20:00" {
		t.Errorf("值类型应按展示时区格式化，实际 %q", got)
	}
	if got := MinuteTimeText(time.Time{}, cst, UnknownText); got != UnknownText {
		t.Errorf("值类型零值应返回调用方给的占位 %q，实际 %q", UnknownText, got)
	}
	if got := MinuteTimePtrText(ptr(utc), cst, UnknownText); got != "2026-09-16 20:00" {
		t.Errorf("指针类型应按展示时区格式化，实际 %q", got)
	}
	if got := MinuteTimePtrText(nil, cst, UnknownText); got != UnknownText {
		t.Errorf("nil 指针应返回调用方给的占位 %q，实际 %q", UnknownText, got)
	}
	if got := MinuteTimePtrText(ptr(time.Time{}), cst, DashText); got != DashText {
		t.Errorf("零值指针应按缺失处理，实际 %q", got)
	}

	if got := DataTimeText(utc); got != "2026-09-16 12:00:00" {
		t.Errorf("数据时间应精确到秒，实际 %q", got)
	}
	if got := DataTimeText(time.Time{}); got != UnknownText {
		t.Errorf("数据时间零值应给 %q，实际 %q", UnknownText, got)
	}
}

// TestTargetMessage 校验统一的会话守卫：只有「群内文本消息 + 配置里指定的群」才放行，
// 非消息更新、没有会话、别的群三种情况一律忽略（九个指令共用这一份判断）。
func TestTargetMessage(t *testing.T) {
	checker := &fakeChatChecker{groupID: -100}

	cases := []struct {
		name        string
		update      tgbotapi.Update
		wantOK      bool
		wantChatID  int64
		wantMessage int64
	}{
		{
			name:        "目标群的文本消息",
			update:      tgbotapi.Update{Message: &tgbotapi.Message{MessageID: 42, Chat: &tgbotapi.Chat{ID: -100}}},
			wantOK:      true,
			wantChatID:  -100,
			wantMessage: 42,
		},
		{
			name:   "别的群的消息",
			update: tgbotapi.Update{Message: &tgbotapi.Message{MessageID: 42, Chat: &tgbotapi.Chat{ID: -200}}},
		},
		{
			name:   "没有会话的消息",
			update: tgbotapi.Update{Message: &tgbotapi.Message{MessageID: 42}},
		},
		{
			name:   "非消息更新（callback query）",
			update: tgbotapi.Update{CallbackQuery: &tgbotapi.CallbackQuery{ID: "1"}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg, ok := TargetMessage(c.update, checker)
			if ok != c.wantOK {
				t.Fatalf("是否服务该更新：期望 %v，实际 %v", c.wantOK, ok)
			}
			if !ok {
				if msg != nil {
					t.Errorf("忽略更新时应返回 nil 消息，实际 %+v", msg)
				}
				return
			}
			if msg.Chat.ID != c.wantChatID || msg.MessageID != c.wantMessage {
				t.Errorf("取到的消息错误：chat=%d msg=%d", msg.Chat.ID, msg.MessageID)
			}
		})
	}
}

// fakeChatChecker 是 ChatChecker 的最小假实现：只服务 groupID 指定的群，0 表示不限制。
type fakeChatChecker struct{ groupID int64 }

// IsTargetChat 判断会话是否是配置里指定的群。
func (f *fakeChatChecker) IsTargetChat(chatID int64) bool {
	return f.groupID == 0 || chatID == f.groupID
}

// TestFactionName 校验派系名归一化：折叠大小写、去掉复数词尾，未收录的取值原样返回。
func TestFactionName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Humans", "超级地球"},
		{"human", "超级地球"},
		{"Terminids", "终结族"},
		{"Automaton", "机器人"},
		{"Automatons", "机器人"},
		{"Illuminate", "光能者"},
		{"  Terminid  ", "终结族"},
		{"New Faction", "New Faction"}, // 未收录：原样返回，不猜
		{"", ""},                       // 空串原样返回（占位由调用方决定）
	}
	for _, c := range cases {
		if got := FactionName(c.in); got != c.want {
			t.Errorf("FactionName(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// TestFactionKey 校验派系分类契约：已知派系给归一化 key 与 true，未收录给 false。
// 分类必须走这个函数而不是拿 FactionName 的返回值比原文（见函数注释）。
func TestFactionKey(t *testing.T) {
	cases := []struct {
		in        string
		wantKey   string
		wantKnown bool
	}{
		{"Humans", "human", true},
		{"human", "human", true},
		{"Terminids", "terminid", true},
		{"Automaton", "automaton", true},
		{"Automatons", "automaton", true},
		{"  Illuminate  ", "illuminate", true},
		// 未收录：key 是归一化后的原文，known 为 false，调用方据此原样展示、单独归类
		{"New Faction", "new faction", false},
		{"Foo", "foo", false},
		// 原文本身就是中文名的脏数据不能被当成已知派系
		{"超级地球", "超级地球", false},
		{"", "", false},
		{"   ", "", false},
	}
	for _, c := range cases {
		key, known := FactionKey(c.in)
		if key != c.wantKey || known != c.wantKnown {
			t.Errorf("FactionKey(%q) = %q/%v，期望 %q/%v", c.in, key, known, c.wantKey, c.wantKnown)
		}
	}
}

// TestFactionClass 校验派系配色 class：四个派系逐个钉住，
// 未收录的取值没有 class（模板里退化成普通文字，不猜配色）。
func TestFactionClass(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Humans", "faction--humans"}, // 我方 = 蓝色
		{"Terminids", "faction--terminid"},
		{"Automaton", "faction--automaton"},
		{"Illuminate", "faction--illuminate"},
		{"New Faction", ""}, // 未收录：不猜配色
	}
	for _, c := range cases {
		if got := FactionClass(c.in); got != c.want {
			t.Errorf("FactionClass(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// TestIsEnemyFaction 校验敌方判定：未收录的取值一律按「不是敌方」处理。
func TestIsEnemyFaction(t *testing.T) {
	for _, enemy := range []string{"Terminids", "Automatons", "Illuminate", "terminid"} {
		if !IsEnemyFaction(enemy) {
			t.Errorf("%q 应判定为敌方", enemy)
		}
	}
	for _, other := range []string{"Humans", "New Faction", ""} {
		if IsEnemyFaction(other) {
			t.Errorf("%q 不应判定为敌方", other)
		}
	}
}

// TestTacticalActionName 校验战术行动的展示名与图标：对照表逐行钉住（译名与图标都要对），
// 未收录的返回去掉首尾空白后的原文与空图标，纯空白入参返回空串（占位由调用方决定）。
func TestTacticalActionName(t *testing.T) {
	cases := []struct {
		in                 string
		wantName, wantIcon string
	}{
		{"EAGLE STORM", "飞鹰风暴", "tactical.eagle_storm"},
		{"ORBITAL BLOCKADE", "轨道封锁", "tactical.orbital_blockade"},
		{"HEAVY ORDNANCE DISTRIBUTION", "重型军械分发", "tactical.heavy_ordnance"},
		// 飞鹰封锁与星球轰炸本仓库没有独立素材，按参考站的做法复用同族图标
		{"EAGLE BLOCK", "飞鹰封锁", "tactical.eagle_storm"},
		{"PLANETARY BOMBARDMENT", "星球轰炸", "tactical.planetary_bombardment"},
		// 只译名、不单独占一行（上游从没报过它）
		{"ORBITAL NAPALM BARRAGE", "轨道燃烧弹幕", "tactical.orbital_napalm"},
		// 上游大小写不稳定，匹配前统一折叠
		{"eagle storm", "飞鹰风暴", "tactical.eagle_storm"},
		// 未收录：原样返回（TrimSpace 之后），也不给图标
		{"UNKNOWN ACTION", "UNKNOWN ACTION", ""},
		{"  UNKNOWN ACTION  ", "UNKNOWN ACTION", ""},
		// 纯空白：返回空串，占位（例如「未命名行动」）由调用方决定
		{"   ", "", ""},
	}
	for _, c := range cases {
		name, icon := TacticalActionName(c.in)
		if name != c.wantName || icon != c.wantIcon {
			t.Errorf("TacticalActionName(%q) = %q/%q，期望 %q/%q", c.in, name, icon, c.wantName, c.wantIcon)
		}
	}
}

// TestTacticalActions 校验「卡片上固定列出的战术行动」清单：顺序照参考站，
// 每项都要有译名、图标与效果编号（缺效果编号就没法跟上游数据对上号），
// 只译名不占行的行动（轨道燃烧弹幕）不该出现在清单里——把游戏里没有的行动画成「募捐中」是在编内容。
func TestTacticalActions(t *testing.T) {
	specs := TacticalActions()
	want := []string{"飞鹰风暴", "轨道封锁", "重型军械分发", "飞鹰封锁", "星球轰炸"}
	if len(specs) != len(want) {
		t.Fatalf("清单应有 %d 项，实际 %d 项：%+v", len(want), len(specs), specs)
	}
	for i, w := range want {
		spec := specs[i]
		if spec.Name != w {
			t.Errorf("第 %d 项应是 %q，实际 %q", i+1, w, spec.Name)
		}
		if spec.UpstreamName == "" || spec.Icon == "" || len(spec.EffectIDs) == 0 {
			t.Errorf("第 %d 项（%s）缺展示或匹配依据：%+v", i+1, w, spec)
		}
	}
	// 返回的是副本：调用方改它不该影响包内的表。
	specs[0].Name = "改过的名字"
	if again := TacticalActions(); again[0].Name != "飞鹰风暴" {
		t.Errorf("TacticalActions 应返回副本，包内清单被改动了：%+v", again[0])
	}
	for _, spec := range specs {
		if spec.Name == "轨道燃烧弹幕" {
			t.Error("只译名不占行的行动不该出现在固定清单里")
		}
	}
}

// TestCurrencyName 校验奖励货币对照表：收录的给中文名，没收录的返回 false（卡片退回原值）。
func TestCurrencyName(t *testing.T) {
	cases := []struct {
		id32 int64
		want string
	}{
		{897894480, "勋章"}, // 实测当前重要指令的奖励
		{3608481516, "申购单"},
		{3992382197, "普通样本"},
		{2985106497, "稀有样本"},
		{3670075867, "超级样本"},
		{3481751602, "超级信用点"},
	}
	for _, c := range cases {
		name, ok := CurrencyName(c.id32)
		if !ok || name != c.want {
			t.Errorf("CurrencyName(%d) = %q/%v，期望 %q/true", c.id32, name, ok, c.want)
		}
	}
	if _, ok := CurrencyName(12345); ok {
		t.Error("对照表外的 id32 应返回 false")
	}
}

// TestRewardTypeName 校验奖励类别对照：只收录有把握的 1（勋章），其余编号返回 false 让卡片退回原值。
// 上游主源的 reward 只有 {type, amount}，没有 id32，这张表是主源路径上唯一能认奖励的依据。
func TestRewardTypeName(t *testing.T) {
	if name, ok := RewardTypeName(1); !ok || name != "勋章" {
		t.Errorf("RewardTypeName(1) = %q/%v，期望 勋章/true", name, ok)
	}
	for _, rewardType := range []int{0, 2, 3, 7} {
		if name, ok := RewardTypeName(rewardType); ok {
			t.Errorf("未收录的类别 %d 应返回 false，实际 %q", rewardType, name)
		}
	}
}

// TestTacticalStatus 校验状态数字到文案的映射：未收录的取值显示「状态 N」，不猜成已知状态。
func TestTacticalStatus(t *testing.T) {
	cases := []struct {
		status      int
		text, class string
	}{
		{0, "未激活", "tag"},
		{1, "募捐中", "tag--gold"},
		{2, "已激活", "tag--win"},
		{3, "冷却中", "tag"},
		{9, "状态 9", "tag"},
	}
	for _, c := range cases {
		text, class := TacticalStatus(c.status)
		if text != c.text || class != c.class {
			t.Errorf("TacticalStatus(%d) = %q/%q，期望 %q/%q", c.status, text, class, c.text, c.class)
		}
	}
}
