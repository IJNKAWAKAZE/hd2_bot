package war

import (
	"strings"
	"testing"
	"time"

	"hd2_bot/src/hd2"
	"hd2_bot/src/plugins/plugutil"
)

// testWar 返回一份固定样本战况，供格式化用例共用。
func testWar() *hd2.War {
	return &hd2.War{
		ClientVersion: "1.003.400",
		Statistics: hd2.Statistics{
			MissionsWon:   217853802,
			MissionsLost:  25121616,
			PlayerCount:   23162,
			Accurracy:     61.97,
			TerminidKills: 12000000,
		},
	}
}

// TestFormatWar 校验战况文本包含关键数据且正确转义 MarkdownV2 特殊字符。
func TestFormatWar(t *testing.T) {
	at := time.Date(2026, 9, 16, 20, 0, 0, 0, time.FixedZone("CST", 8*3600))
	war := &hd2.War{
		ClientVersion: "1.003.400",
		Statistics: hd2.Statistics{
			MissionsWon:   217853802,
			MissionsLost:  25121616,
			PlayerCount:   23162,
			Accurracy:     61.97,
			TerminidKills: 12000000,
		},
	}
	got := FormatWar(war, at, false)

	// 客户端版本不再进文本（用户 2026-09-17 要求），这里只认可信字段。
	for _, want := range []string{"23,162", `89\.7%`, "20:00"} {
		if !strings.Contains(got, want) {
			t.Errorf("输出缺少 %q\n实际输出：\n%s", want, got)
		}
	}
	if strings.Contains(got, "数据可能已过期") {
		t.Error("实时数据不应出现过期提示")
	}
}

// TestFormatWarStale 校验降级数据带上过期提示。
func TestFormatWarStale(t *testing.T) {
	at := time.Now()
	war := &hd2.War{}
	got := FormatWar(war, at, true)
	if !strings.Contains(got, "数据可能已过期") {
		t.Fatalf("降级数据应提示可能过期：\n%s", got)
	}
}

// TestFormatWarNil 校验 war 为 nil 时不 panic 且仍给出可读文本。
func TestFormatWarNil(t *testing.T) {
	got := FormatWar(nil, time.Now(), false)
	if !strings.Contains(got, "银河战况") {
		t.Fatalf("空数据也应给出标题：%s", got)
	}
}

// TestFormatWarZeroMissions 校验没有胜负记录时成功率显示 0.0%，不做除零运算。
func TestFormatWarZeroMissions(t *testing.T) {
	got := FormatWar(&hd2.War{}, time.Now(), false)
	if !strings.Contains(got, `0\.0%`) {
		t.Fatalf("零场次应显示 0.0%%：\n%s", got)
	}
}

// TestFormatWarOmitsClientVersion 守住「客户端版本不再出现在文本回退里」这条口径：
// 上游版本里带 MarkdownV2 保留字符（括号、加号），以前正是转义用例的主角。
func TestFormatWarOmitsClientVersion(t *testing.T) {
	got := FormatWar(&hd2.War{ClientVersion: "1.0(测试)+"}, time.Now(), false)
	if strings.Contains(got, "客户端版本") || strings.Contains(got, "1.0") {
		t.Fatalf("文本回退里不该再出现客户端版本：\n%s", got)
	}
}

// TestFormatInt 校验 /war 用的千分位格式化（实现已抽到 plugutil，这里确认接线后行为没变）。
// 更全的边界（负数、超长数字）在 plugutil 自己的用例里。
func TestFormatInt(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1,000"},
		{23162, "23,162"},
		{1234567, "1,234,567"},
		{-1234, "-1,234"},
	}
	for _, c := range cases {
		if got := plugutil.FormatInt(c.in); got != c.want {
			t.Errorf("FormatInt(%d)：期望 %q，实际 %q", c.in, c.want, got)
		}
	}
}

// TestMissionSuccessRate 校验成功率计算。
func TestMissionSuccessRate(t *testing.T) {
	cases := []struct {
		name string
		war  *hd2.War
		want float64
	}{
		{"正常", testWar(), 89.66},
		{"全胜", &hd2.War{Statistics: hd2.Statistics{MissionsWon: 10}}, 100},
		{"无场次", &hd2.War{}, 0},
	}
	for _, c := range cases {
		got := missionSuccessRate(c.war)
		if got < c.want-0.01 || got > c.want+0.01 {
			t.Errorf("%s：期望约 %.2f，实际 %.2f", c.name, c.want, got)
		}
	}
}

// TestFormatWarZeroTime 校验数据时间缺失时给出可读兜底，而不是 0001 年。
func TestFormatWarZeroTime(t *testing.T) {
	got := FormatWar(&hd2.War{}, time.Time{}, false)
	if !strings.Contains(got, "数据时间：未知") {
		t.Fatalf("缺少数据时间时应给出兜底文案：\n%s", got)
	}
}

// TestFormatWarOmitsUnreliableFields 校验不展示实测口径矛盾的字段。
func TestFormatWarOmitsUnreliableFields(t *testing.T) {
	war := &hd2.War{
		Now:   time.Date(1972, 8, 2, 0, 0, 0, 0, time.UTC),
		Ended: time.Date(2028, 2, 8, 0, 0, 0, 0, time.UTC),
		Statistics: hd2.Statistics{
			Accurracy:    100,
			BulletsFired: 10,
			BulletsHit:   20,
		},
	}
	got := FormatWar(war, time.Now(), false)
	for _, unwanted := range []string{"命中率", "1972", "2028"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("不应展示不可信字段 %q：\n%s", unwanted, got)
		}
	}
}

// TestFormatWarPrefersTrustedFields 校验可信字段排在可疑字段之前。
func TestFormatWarPrefersTrustedFields(t *testing.T) {
	got := FormatWar(testWar(), time.Now(), false)
	playerIdx := strings.Index(got, "在线士兵")
	wonIdx := strings.Index(got, "任务完成")
	if playerIdx < 0 || wonIdx < 0 || playerIdx > wonIdx {
		t.Fatalf("玩家数与胜负场次应排在最前：\n%s", got)
	}
}

// TestFormatWarFullText 固化 /war 的完整文案，改动格式时这里会先失败。
func TestFormatWarFullText(t *testing.T) {
	at := time.Date(2026, 9, 16, 20, 0, 0, 0, time.FixedZone("CST", 8*3600))
	want := `*银河战况*
在线士兵：23,162
任务完成：217,853,802 胜 / 25,121,616 负
任务成功率：89\.7%
击杀：终结族 12,000,000 ｜ 机器人 0 ｜ 光能者 0
数据时间：2026\-09\-16 20:00:00`
	if got := FormatWar(testWar(), at, false); got != want {
		t.Fatalf("文案不一致\n期望：\n%s\n实际：\n%s", want, got)
	}
	if got := FormatWar(testWar(), at, true); got != want+"\n（数据可能已过期，上游接口暂时不可用）" {
		t.Fatalf("降级文案不一致\n实际：\n%s", got)
	}
}
