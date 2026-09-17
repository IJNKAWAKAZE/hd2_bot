package translate

import (
	"regexp"
	"sort"
	"strings"
	"sync"

	"hd2_bot/src/hd2/glossary"
)

// 术语预替换：送译前把专有名词（星球 / 分区 / 敌人 / 战术配备）换成官方简中译名，
// 避免模型把它们音译或翻错。词表来自 src/hd2/glossary（官方简中译名，见该包的许可说明）。
var (
	termOnce   sync.Once
	termRe     *regexp.Regexp // 首尾都是单词字符的词条（绝大多数）
	termTailRe *regexp.Regexp // 尾部不是单词字符的词条：上游的 "senge 23 (void)" / "uvp alpha (unused)" 等占位条目
	termIndex  map[string]string
)

// initTerms 构建两条匹配正则（只建一次）并填充 termIndex：
//
//   - 按长度降序排列，保证长词优先（否则 "spore charger" 会被拆成 "spore" + "强袭虫"）；
//   - 大小写不敏感，并允许词尾多一个 s（上游正文里 Chargers 这类复数很常见）；
//   - 边界按词条自己的首/尾字符决定要不要加 \b：\b 要求那一侧紧挨单词字符，
//     而 "senge 23 (void)" 的末尾是 ")"，加了尾部 \b 就会变成永远命不中的死条目；
//     反过来，首字符不是单词字符的词条也不能加前边界。加边界是为了不碰普通词里的子串
//     （例如 supercharger 里的 charger），只在确实成立时才加。
func initTerms() {
	termOnce.Do(func() {
		table := glossary.Terms()
		termIndex = make(map[string]string, len(table))
		var wordEnd, tailEnd []string
		for english, chinese := range table {
			key := strings.ToLower(strings.TrimSpace(english))
			if key == "" || strings.TrimSpace(chinese) == "" {
				continue
			}
			termIndex[key] = chinese
			if endsWithWordRune(key) {
				wordEnd = append(wordEnd, termPattern(key))
				continue
			}
			tailEnd = append(tailEnd, termPattern(key))
		}
		sortAlternatives(wordEnd)
		sortAlternatives(tailEnd)
		termRe = regexp.MustCompile(`(?i)(` + strings.Join(wordEnd, "|") + `)`)
		if len(tailEnd) > 0 {
			termTailRe = regexp.MustCompile(`(?i)(` + strings.Join(tailEnd, "|") + `)`)
		}
	})
}

// termPattern 拼一条候选词：按词条自己的首/尾字符决定要不要加 \b 边界——
// \b 要求那一侧紧挨单词字符，"senge 23 (void)" 这种以 ")" 结尾的词条加了尾部 \b 就永远命中不了。
// 尾部是单词字符时还会允许词尾多一个 s（上游正文里 Chargers 这类复数很常见）。
//
// 边界按字节判断：词表全是 ASCII，非 ASCII 首尾字符一律按「非单词字符」处理（即不加边界）。
func termPattern(key string) string {
	var b strings.Builder
	if startsWithWordRune(key) {
		b.WriteString(`\b`)
	}
	b.WriteString(regexp.QuoteMeta(key))
	if endsWithWordRune(key) {
		b.WriteString(pluralSuffix)
		b.WriteString(`\b`)
	}
	return b.String()
}

// pluralSuffix 让词尾多一个 s 的写法也能命中单数词条（Chargers → 强袭虫）。
// 正则在选区里就吃掉这个 s，译文用单数词条的中文。
const pluralSuffix = "s?"

// startsWithWordRune 判断词条首字节是不是单词字符（字母 / 数字 / 下划线）。
func startsWithWordRune(key string) bool {
	return isWordByte(key[0])
}

// endsWithWordRune 判断词条尾字节是不是单词字符（字母 / 数字 / 下划线）。
func endsWithWordRune(key string) bool {
	return isWordByte(key[len(key)-1])
}

// isWordByte 是「单词字符」的判定：与 regexp 里 \b 的口径一致（ASCII 的字母、数字与下划线）。
func isWordByte(c byte) bool {
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// sortAlternatives 把候选排成「长的在前」：择一匹配取最左最先命中，长词不排前面就会被短词切走。
// 等长时按字典序，保证每次构建出的正则文本一致（便于排查与断言）。
func sortAlternatives(alternatives []string) {
	sort.Slice(alternatives, func(i, j int) bool {
		if len(alternatives[i]) != len(alternatives[j]) {
			return len(alternatives[i]) > len(alternatives[j])
		}
		return alternatives[i] < alternatives[j]
	})
}

// PreReplace 把文本里的专有名词替换成官方简中译名（整词、大小写不敏感、长词优先）。
// 词表里没有的词原样保留：宁可让模型翻，也不要乱猜。
//
// 尾部放宽的那批（"(void)" / "(unused)" 占位条目）必须先生效：否则 "senge 23" 会先把前半截
// 换成中文，剩下的 "(void)" 再也匹配不上，用户看到的就是半中半英的 "新戈23 (void)"。
func PreReplace(text string) string {
	initTerms()
	if termTailRe != nil {
		text = replaceTerms(text, termTailRe)
	}
	return replaceTerms(text, termRe)
}

// replaceTerms 用给定正则做一次整词替换：命中即换中文，只多一个复数 s 的形式回退到单数词条。
func replaceTerms(text string, re *regexp.Regexp) string {
	return re.ReplaceAllStringFunc(text, func(match string) string {
		key := strings.ToLower(match)
		if chinese, ok := termIndex[key]; ok {
			return chinese
		}
		if trimmed := strings.TrimSuffix(key, "s"); trimmed != key {
			if chinese, ok := termIndex[trimmed]; ok {
				return chinese
			}
		}
		return match
	})
}
