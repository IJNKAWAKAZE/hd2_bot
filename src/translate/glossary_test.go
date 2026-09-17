package translate

import (
	"strings"
	"testing"
)

// TestPreReplaceUsesGlossary 校验送译前的专有名词替换：命中的词换成官方简中译名。
func TestPreReplaceUsesGlossary(t *testing.T) {
	got := PreReplace("Drop on Acamar IV and hold the line")
	if !strings.Contains(got, "天园六IV") {
		t.Fatalf("星球名应被替换成官方译名，实际 %q", got)
	}
	if strings.Contains(got, "Acamar") {
		t.Fatalf("被替换的英文名不应留在结果里，实际 %q", got)
	}
}

// TestPreReplaceIsCaseInsensitive 校验大小写折叠：上游正文里既有全大写也有正常写法。
func TestPreReplaceIsCaseInsensitive(t *testing.T) {
	if got := PreReplace("ACAMAR IV"); !strings.Contains(got, "天园六IV") {
		t.Fatalf("大写形式也应命中，实际 %q", got)
	}
}

// TestPreReplaceLongestWins 校验长词优先：否则 "spore charger" 会被拆成 "spore" + "强袭虫"。
func TestPreReplaceLongestWins(t *testing.T) {
	if got := PreReplace("spore charger"); got != "孢子强袭虫" {
		t.Fatalf("长词应优先匹配，实际 %q", got)
	}
}

// TestPreReplaceLeavesPlainWords 校验普通词不被改动：词表里没有的词必须原样保留。
func TestPreReplaceLeavesPlainWords(t *testing.T) {
	const plain = "we hold this position until the last transport leaves"
	if got := PreReplace(plain); got != plain {
		t.Fatalf("普通文本不应被改动，实际 %q", got)
	}
}

// TestPreReplaceToleratesPlural 校验词尾复数的容忍：上游正文里 Chargers 这类写法很常见，
// 少了这层容忍就会把复数整个漏给模型去音译。
func TestPreReplaceToleratesPlural(t *testing.T) {
	if got := PreReplace("Chargers"); got != "强袭虫" {
		t.Fatalf("复数形式应命中单数词条，实际 %q", got)
	}
	if got := PreReplace("Two chargers"); !strings.Contains(got, "强袭虫") {
		t.Fatalf("混在句子里也应命中，实际 %q", got)
	}
}

// TestPreReplaceMatchesKeysEndingWithPunctuation 校验尾部是括号的词条能命中：
// 上游数据里有 "senge 23 (void)"、"uvp alpha (unused)" 这类占位条目，尾部加 \b 会让它们
// 全部变成死条目（\b 要求后面紧跟单词字符，而它们以 ")" 结尾），用户看到半中半英的
// "新戈23 (void)" 而不是词表里的 "仙盖23号"。
func TestPreReplaceMatchesKeysEndingWithPunctuation(t *testing.T) {
	got := PreReplace("Drop on senge 23 (void)")
	if !strings.Contains(got, "仙盖23号") {
		t.Fatalf("尾部带括号的词条应命中，实际 %q", got)
	}
	if strings.Contains(got, "新戈") || strings.Contains(got, "(void)") {
		t.Fatalf("应整体换成更具体的长词条译名，实际 %q", got)
	}
	if got := PreReplace("uvp alpha (unused) sector"); !strings.Contains(got, "UVP阿尔法") {
		t.Fatalf("(unused) 占位条目同样要命中，实际 %q", got)
	}
}
