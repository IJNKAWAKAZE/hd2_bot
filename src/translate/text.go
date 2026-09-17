package translate

import (
	"regexp"
	"strings"
	"unicode"
)

// 上游文本里的标记：简报正文用 <i=3>…</i> 表示游戏内高亮（实测形如 "<i=3>MAJOR ORDER WON</i>"），
// 偶尔还夹带 HTML 片段。这些标记在群聊里只会变成尖括号噪声，展示与送译前统一清掉。
//
// 这三条规则原本在 plugins/orders 里，S3 起由翻译层持有：命令卡片、推送卡片与送译输入
// 必须用同一份清洗规则，否则同一段正文会出现两种写法。
var (
	htmlTagRe    = regexp.MustCompile(`<[^>]*>`)
	blankLinesRe = regexp.MustCompile(`\n{3,}`)
	trailingSpRe = regexp.MustCompile(`[ \t]+\n`)
	spaceRe      = regexp.MustCompile(`\s+`)
)

// CleanGameText 清掉游戏内标记与多余空行，返回可以直接展示或送译的纯文本。
func CleanGameText(text string) string {
	s := htmlTagRe.ReplaceAllString(text, "")
	s = trailingSpRe.ReplaceAllString(s, "\n")
	s = blankLinesRe.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

// Summarize 把长正文压成一行摘要：把换行与连续空白压成单个空格，再按 rune 截断到 max 个字符。
// 按 rune 截断而不是按字节：中文不会被切坏。max <= 0 时返回空串。
func Summarize(text string, max int) string {
	if max <= 0 {
		return ""
	}
	flat := spaceRe.ReplaceAllString(strings.TrimSpace(text), " ")
	runes := []rune(flat)
	if len(runes) <= max {
		return flat
	}
	return string(runes[:max]) + "…"
}

// cleanTranslated 收拾后端返回的脏值，返回「可以写进卡片与缓存」的译文。
//
// 实测会遇到的脏值有三类：
//   - 整段被套进代码围栏（```json … ```）；
//   - 只带尾围栏（多段模式下，模型把结尾的 ``` 混进最后一段的正文）；
//   - 只剩围栏或符号（后端回了 "```"、"---" 这种没有任何可读内容的垃圾）。
//
// 返回空串表示「这段不能当译文用」：调用方据此保留原文并且不写缓存（下次还会再试）。
// 只剥首尾围栏，正文中间的 ``` 原样保留（它可能是正文的一部分，且没有可靠办法分辨）。
func cleanTranslated(text string) string {
	cleaned := stripCodeFences(text)
	if !hasReadableRune(cleaned) {
		return ""
	}
	return cleaned
}

// stripCodeFences 剥掉首尾的代码围栏，并丢掉首行可能出现的语言标记（```json、```text）。
//
// 尾围栏先剥：```json``` 这种单行写法要先去掉尾部，才认得出开头是语言标记；
// 而多段模式下结尾的 ``` 常常正好落在最后一段里（"第二段译文\n```"）。
func stripCodeFences(text string) string {
	cleaned := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(text), "```"))
	if strings.HasPrefix(cleaned, "```") {
		cleaned = strings.TrimSpace(strings.TrimPrefix(cleaned, "```"))
		if idx := strings.IndexByte(cleaned, '\n'); idx >= 0 {
			if isFenceLanguageTag(cleaned[:idx]) {
				cleaned = strings.TrimSpace(cleaned[idx+1:])
			}
		} else if isFenceLanguageTag(cleaned) {
			cleaned = "" // 围栏里只剩语言标记，没有任何正文
		}
	}
	return strings.TrimSpace(cleaned)
}

// fenceLanguageTags 列出围栏首行可能出现的语言标记。
// 用白名单而不是「纯字母短词」判定：万一译文本身就是一个英文单词（还套了围栏），
// 白名单不会把译文当成标记丢掉。
var fenceLanguageTags = map[string]bool{
	"json": true, "text": true, "plain": true, "plaintext": true, "markdown": true,
	"md": true, "html": true, "xml": true, "yaml": true, "yml": true,
}

// isFenceLanguageTag 判断围栏首行是不是已知的语言标记（大小写与首尾空白都折叠）。
func isFenceLanguageTag(line string) bool {
	return fenceLanguageTags[strings.ToLower(strings.TrimSpace(line))]
}

// hasReadableRune 判断文本里还有没有可读内容（字母或数字）。
// 全是围栏、标点或空白时返回 false，调用方据此按「后端没翻动」处理。
func hasReadableRune(text string) bool {
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return true
		}
	}
	return false
}
