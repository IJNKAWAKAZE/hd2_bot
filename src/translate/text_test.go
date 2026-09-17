package translate

import "testing"

// TestCleanGameText 校验游戏内标记与多余空行被清掉（这份规则全项目只有一份，改动会同时影响命令与推送）。
func TestCleanGameText(t *testing.T) {
	cases := []struct{ in, want string }{
		{"<i=3>MAJOR ORDER WON</i> The Helldivers won.", "MAJOR ORDER WON The Helldivers won."},
		{"第一行\n\n\n\n第二行", "第一行\n\n第二行"},
		{"末尾有空格   \n下一行", "末尾有空格\n下一行"},
		{"  前后空白  ", "前后空白"},
		{"带 <b>粗体</b> 的文本", "带 粗体 的文本"},
	}
	for _, c := range cases {
		if got := CleanGameText(c.in); got != c.want {
			t.Errorf("CleanGameText(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// TestSummarize 校验摘要按 rune 截断（中文不会被切坏），并且把换行压成空格。
func TestSummarize(t *testing.T) {
	if got := Summarize("第一行\n第二行", 100); got != "第一行 第二行" {
		t.Errorf("换行应压成空格，实际 %q", got)
	}
	if got := Summarize("一二三四五", 3); got != "一二三…" {
		t.Errorf("超长应截断并补省略号，实际 %q", got)
	}
	if got := Summarize("一二三", 3); got != "一二三" {
		t.Errorf("刚好等于上限不应截断，实际 %q", got)
	}
	if got := Summarize("一二三", 0); got != "" {
		t.Errorf("max<=0 应返回空串，实际 %q", got)
	}
}

// TestCleanTranslated 校验采纳前的脏值清洗：围栏与语言标记被剥掉，只剩围栏/符号的值返回空串
// （空串在翻译层表示「后端没翻动」：保留原文、也不写缓存）。
func TestCleanTranslated(t *testing.T) {
	cases := []struct{ in, want string }{
		{"  译文  ", "译文"},
		{"```json\n译文\n```", "译文"},
		{"译文\n```", "译文"},
		{"```json```", ""},
		{"```", ""},
		{"---", ""},
		{"", ""},
		{"中文与 ``` 中间围栏", "中文与 ``` 中间围栏"},
	}
	for _, c := range cases {
		if got := cleanTranslated(c.in); got != c.want {
			t.Errorf("cleanTranslated(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}
