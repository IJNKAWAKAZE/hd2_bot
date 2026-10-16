// challenge.go 是「呼叫演练」这道题本身：从装备目录里挑一件战备，让新成员按顺序点出它的召唤指令。
//
// 为什么用召唤指令：游戏里第一次呼叫战备，就是把这串箭头按顺序敲进去——对群里的人来说，
// 这是最没有门槛的「你是不是玩过这个游戏」，而且全程只要点按钮，不用打字。
//
// 状态机与目录抽开，是为了让「点对 / 点错 / 点完 / 用光机会」这几种结果能单独测。
package gatekeeper

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"

	"hd2_bot/src/arsenal"
)

// 四个方向：取值与装备目录里的 deployment.code 一致。
const (
	DirUp    = "up"
	DirDown  = "down"
	DirLeft  = "left"
	DirRight = "right"
)

// directionGlyphs 是方向 → 纯文本符号。只用在题面图里当兜底
// （箭头素材缺失时才会显示），以及测试与日志里；群里的文字一律用下面的 emoji。
var directionGlyphs = map[string]string{
	DirUp: "↑", DirDown: "↓", DirLeft: "←", DirRight: "→",
}

// directionEmoji 是方向 → emoji 箭头。按钮文案、进度行、纯文本题面都用它：
// 卡片里的箭头是游戏内的图形素材（跟着 HUD 皮肤走），而 Telegram 的按钮与正文里
// emoji 更醒目、更好点，也不会被字体吃掉。
var directionEmoji = map[string]string{
	DirUp: "⬆️", DirDown: "⬇️", DirLeft: "⬅️", DirRight: "➡️",
}

// directionAssets 是方向 → 卡片素材逻辑名（见 render/assets.go 的 assetFiles）。
var directionAssets = map[string]string{
	DirUp: "arrow.up", DirDown: "arrow.down", DirLeft: "arrow.left", DirRight: "arrow.right",
}

// Target 是一次验证的答案：战备标识、显示名、召唤指令，以及这件装备本身
// （题面图要用它的图标地址去下载）。
type Target struct {
	ID     string
	NameZh string
	Model  string
	Steps  []string
	Item   arsenal.Item
}

// minSteps / maxSteps 限定出题的指令长度：太短一眼就能试出来，太长点得烦。
const (
	minSteps = 3
	maxSteps = 6
)

// Challenge 是一次验证的状态机。
type Challenge struct {
	target     Target
	index      int      // 已点对的位置
	remaining  int      // 还能答错几次
	progresses []string // 已点对的方向（用于在卡片上回显进度）
}

// StepResult 是一次点击的结果。
type StepResult int

const (
	// StepProgress 点对了，但还没点完。
	StepProgress StepResult = iota
	// StepPassed 全部点对，验证通过。
	StepPassed
	// StepWrong 点错了，还有机会（进度清零，重头再来）。
	StepWrong
	// StepFailed 点错了且没有机会了。
	StepFailed
)

// NewChallenge 用一件战备开局；attempts 是允许答错的次数（0 表示一次都不许错）。
func NewChallenge(target Target, attempts int) (*Challenge, error) {
	if len(target.Steps) == 0 {
		return nil, errors.New("这道题没有召唤指令")
	}
	for _, step := range target.Steps {
		if _, ok := directionGlyphs[step]; !ok {
			return nil, fmt.Errorf("召唤指令里有认不出的方向 %q", step)
		}
	}
	if attempts < 0 {
		attempts = 0
	}
	steps := make([]string, len(target.Steps))
	copy(steps, target.Steps)
	target.Steps = steps
	return &Challenge{target: target, remaining: attempts}, nil
}

// Click 处理一次方向点击。
func (c *Challenge) Click(dir string) StepResult {
	dir = strings.ToLower(strings.TrimSpace(dir))
	if dir == c.target.Steps[c.index] {
		c.progresses = append(c.progresses, dir)
		c.index++
		if c.index >= len(c.target.Steps) {
			return StepPassed
		}
		return StepProgress
	}
	// 点错：进度清零。还有机会就原地重来，没有就判失败。
	c.progresses = c.progresses[:0]
	c.index = 0
	if c.remaining > 0 {
		c.remaining--
		return StepWrong
	}
	return StepFailed
}

// Target 返回这道题的战备。
func (c *Challenge) Target() Target { return c.target }

// Progress 返回已点对的方向（副本）。
func (c *Challenge) Progress() []string {
	out := make([]string, len(c.progresses))
	copy(out, c.progresses)
	return out
}

// Remaining 返回还剩几次答错机会。
func (c *Challenge) Remaining() int { return c.remaining }

// Total 返回指令总步数。
func (c *Challenge) Total() int { return len(c.target.Steps) }

// CatalogProvider 提供装备目录（由 arsenal.Store 实现）。
type CatalogProvider interface {
	Catalog(ctx context.Context) (*arsenal.Catalog, error)
}

// PickTarget 从目录里随机挑一件适合出题的战备。
//
// 筛选口径：召唤类战备或支援武器、指令长度在 3~6 步、且不是已下架的条目。
// avoid 里的 id 会尽量避开（连续两次出同一道题很没意思），但候选不够时还是会用。
func PickTarget(ctx context.Context, provider CatalogProvider, rnd *rand.Rand, avoid map[string]bool) (Target, error) {
	catalog, err := provider.Catalog(ctx)
	if err != nil {
		return Target{}, err
	}
	candidates := make([]Target, 0, 32)
	fallback := make([]Target, 0, 32)
	for _, item := range catalog.Items() {
		if item.Deploy == nil || item.Acq.Kind == "unavailable" {
			continue
		}
		switch item.Kind {
		case arsenal.KindStratagem, arsenal.KindSupport:
		default:
			continue
		}
		steps := normalizedSteps(item.Deploy.Code)
		if len(steps) < minSteps || len(steps) > maxSteps {
			continue
		}
		target := Target{ID: item.ID, NameZh: item.NameZh, Model: item.Model, Steps: steps, Item: item}
		fallback = append(fallback, target)
		if !avoid[item.ID] {
			candidates = append(candidates, target)
		}
	}
	if len(fallback) == 0 {
		return Target{}, errors.New("装备目录里没有可用的召唤指令（数据源刚换过？）")
	}
	pool := candidates
	if len(pool) == 0 {
		pool = fallback
	}
	return pool[rnd.Intn(len(pool))], nil
}

// normalizedSteps 归一化召唤指令：方向小写、丢掉认不出的取值。
func normalizedSteps(code []string) []string {
	out := make([]string, 0, len(code))
	for _, step := range code {
		cleaned := strings.ToLower(strings.TrimSpace(step))
		if _, ok := directionGlyphs[cleaned]; ok {
			out = append(out, cleaned)
		}
	}
	return out
}

// Glyph 返回方向的纯文本符号；认不出时原样返回。
func Glyph(dir string) string {
	if glyph, ok := directionGlyphs[dir]; ok {
		return glyph
	}
	return dir
}

// Emoji 返回方向的 emoji 箭头；认不出时退回纯文本符号。
func Emoji(dir string) string {
	if emoji, ok := directionEmoji[dir]; ok {
		return emoji
	}
	return Glyph(dir)
}

// Asset 返回方向的箭头素材逻辑名；认不出时返回空串（模板据此退回文本）。
func Asset(dir string) string { return directionAssets[dir] }
