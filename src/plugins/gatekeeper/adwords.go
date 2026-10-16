// adwords.go 是入群广告号的粗筛：昵称、用户名、简介里命中这些词就直接踢掉/拒绝，不浪费一次验证。
//
// 为什么值得单独做一层：广告号的昵称几乎总是带联系方式（TG 引流、加V、返佣…），
// 而它们本来也不会认真点验证。先把这批拦掉，验证卡片留给真人。
package gatekeeper

import (
	"strings"
	"unicode"
)

// defaultAdWords 是内置广告词表。都写小写（匹配时两边都折叠大小写）。
//
// 取值原则：宁可漏一点，不要误伤正常玩家。像 "tg"、"vx"、"qq" 这种短词按「整词」匹配
// （前后不能紧贴字母数字），避免 "setting"、"vxworks" 这类正常昵称被误判。
var defaultAdWords = []string{
	// 联系方式引流
	"tg", "telegram", "推特", "whatsapp", "wsapp", "line群", "qq群", "微信群", "加v", "加微",
	"vx", "v信", "威信", "扣扣", "飞机号", "纸飞机",
	// 变现与拉人话术
	"返佣", "返水", "包赔", "跑分", "刷单", "兼职", "日结", "躺赚", "带单", "代理招募",
	"usdt", "trc20", "erc20", "虚拟币", "搬砖",
	// 博彩与色情
	"博彩", "赌场", "彩票", "真人视讯", "棋牌", "外围", "约炮", "上门服务", "福利姬",
	// 常见垃圾词
	"免费领取", "点击领取", "点击链接", "私聊我", "限时优惠", "无门槛", "零成本",
}

// adWordMatcher 判断一段文字是否命中广告词。
type adWordMatcher struct {
	words []string
}

func newAdWordMatcher(extra []string) adWordMatcher {
	words := make([]string, 0, len(defaultAdWords)+len(extra))
	seen := make(map[string]bool, len(defaultAdWords)+len(extra))
	for _, word := range append(append([]string{}, defaultAdWords...), extra...) {
		cleaned := strings.ToLower(strings.TrimSpace(word))
		if cleaned == "" || seen[cleaned] {
			continue
		}
		seen[cleaned] = true
		words = append(words, cleaned)
	}
	return adWordMatcher{words: words}
}

// match 在若干段文字里找第一个命中的词；命中返回该词与 true。
func (m adWordMatcher) match(texts ...string) (string, bool) {
	if len(m.words) == 0 {
		return "", false
	}
	for _, text := range texts {
		folded := strings.ToLower(strings.TrimSpace(text))
		if folded == "" {
			continue
		}
		for _, word := range m.words {
			if containsAdWord(folded, word) {
				return word, true
			}
		}
	}
	return "", false
}

// containsAdWord 判断 folded（已折叠大小写）里是否含 word。
//
// 纯 ASCII 的短词按「整词」匹配：前后不能紧贴字母或数字，
// 这样 "tg" 命中 "tg：@xxx" 与 "联系tg"，但不会命中 "setting"。
// 含非 ASCII 的词（中文）直接子串匹配：中文没有词边界，硬做边界只会漏判。
func containsAdWord(folded, word string) bool {
	if !isASCIIWord(word) {
		return strings.Contains(folded, word)
	}
	from := 0
	for {
		index := strings.Index(folded[from:], word)
		if index < 0 {
			return false
		}
		start := from + index
		end := start + len(word)
		if (start == 0 || !isASCIIAlphaNum(rune(folded[start-1]))) &&
			(end >= len(folded) || !isASCIIAlphaNum(rune(folded[end]))) {
			return true
		}
		from = start + 1
		if from >= len(folded) {
			return false
		}
	}
}

// isASCIIWord 判断词是不是「只由 ASCII 字母数字组成」（这类词需要词边界）。
func isASCIIWord(word string) bool {
	if word == "" {
		return false
	}
	for _, r := range word {
		if r > unicode.MaxASCII || !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func isASCIIAlphaNum(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
}
