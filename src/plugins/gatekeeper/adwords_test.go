// 广告词匹配的用例：中文子串命中、短 ASCII 词按整词命中、以及别误伤正常昵称。
package gatekeeper

import "testing"

func TestAdWordMatchChinese(t *testing.T) {
	matcher := newAdWordMatcher(nil)
	cases := map[string]bool{
		"刷单返佣日结":      true,
		"加V看资料":       true,
		"USDT 换汇，私聊我": true,
		"老潜兵 1984":    false,
		"民主官":         false,
	}
	for text, want := range cases {
		if _, got := matcher.match(text); got != want {
			t.Errorf("match(%q) = %v，期望 %v", text, got, want)
		}
	}
}

func TestAdWordMatchASCIIWordBoundary(t *testing.T) {
	matcher := newAdWordMatcher(nil)
	cases := map[string]struct {
		want  bool
		words []string
	}{
		"联系 tg：@spam": {true, nil},
		"我的TG 是 @x":   {true, nil},
		"setting":     {false, nil},
		"vxworks 工程师": {false, nil},
		"来 vx 一起打":    {true, nil},
	}
	for text, expect := range cases {
		_, got := matcher.match(text)
		if got != expect.want {
			t.Errorf("match(%q) = %v，期望 %v", text, got, expect.want)
		}
	}
}

func TestAdWordMatchMultipleTexts(t *testing.T) {
	matcher := newAdWordMatcher(nil)
	// 昵称干净、用户名带引流。
	if word, hit := matcher.match("普通昵称", "@spam_tg", ""); !hit || word != "tg" {
		t.Fatalf("用户名里应命中 tg，实际 %q %v", word, hit)
	}
	// 只有简介脏。
	if _, hit := matcher.match("普通昵称", "", "加微信：abc"); !hit {
		t.Fatal("简介里应命中「加微」")
	}
	// 全空。
	if _, hit := matcher.match("", "", ""); hit {
		t.Fatal("空文本不该命中")
	}
}

func TestAdWordExtraWords(t *testing.T) {
	matcher := newAdWordMatcher([]string{" 代练 ", "代练", "", "本群专属优惠"})
	// 自定义词生效，且重复与空白被清理。
	if _, hit := matcher.match("顶级代练 上分"); !hit {
		t.Fatal("自定义广告词应生效")
	}
	if _, hit := matcher.match("本群专属优惠 点我"); !hit {
		t.Fatal("自定义长词应生效")
	}
	if len(matcher.words) != len(defaultAdWords)+2 {
		t.Errorf("内置词 + 2 个去重后的自定义词，实际 %d 个", len(matcher.words))
	}
}

func TestAdWordZeroMatcher(t *testing.T) {
	empty := adWordMatcher{}
	if _, hit := empty.match("刷单返佣"); hit {
		t.Fatal("没有词时不该命中")
	}
}
