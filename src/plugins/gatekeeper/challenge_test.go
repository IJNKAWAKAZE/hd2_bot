// 呼叫演练这道题本身的用例：状态机（点对/点错/用完机会/点完）与出题筛选。
package gatekeeper

import (
	"context"
	"errors"
	"math/rand"
	"strings"
	"testing"

	"hd2_bot/src/arsenal"
)

// fixtureCatalog 是一份最小目录：一件可出题的战备、一件指令太短的支援武器、
// 一件已下架的战备、一件没有召唤指令的护甲，以及一件指令正常的哨戒炮。
const fixtureCatalog = `{
  "meta": {"dataVersion": "test", "capturedAt": "2026-10-16T00:00:00Z"},
  "warbonds": [],
  "items": [
    {"id": "orbital-precision-strike", "productKind": "other-stratagem", "nameZh": "轨道精准攻击",
     "nameEn": "Orbital Precision Strike",
     "image": {"path": "assets/wiki/orbital-precision-strike.png"},
     "acquisition": {"kind": "requisition"},
     "deployment": {"type": "Orbital", "code": ["up", "up", "down"], "cooldownSeconds": 100}},
    {"id": "mg-43-machine-gun", "productKind": "support-weapon", "nameZh": "机枪",
     "nameEn": "MG-43 Machine Gun", "model": "MG-43",
     "acquisition": {"kind": "requisition"},
     "deployment": {"type": "Support Weapon", "code": ["down", "left"], "cooldownSeconds": 480}},
    {"id": "orbital-illumination-flare", "productKind": "other-stratagem", "nameZh": "轨道照明弹",
     "nameEn": "Orbital Illumination Flare",
     "acquisition": {"kind": "unavailable"},
     "deployment": {"type": "Orbital", "code": ["down", "up", "right", "up"], "cooldownSeconds": 60}},
    {"id": "a-ac-8-autocannon-sentry", "productKind": "other-stratagem", "nameZh": "自动哨戒炮",
     "nameEn": "A/AC-8 Autocannon Sentry", "model": "A/AC-8",
     "image": {"path": "assets/wiki/a-ac-8-autocannon-sentry.svg"},
     "acquisition": {"kind": "requisition"},
     "deployment": {"type": "Sentry", "code": ["down", "up", "right", "up", "left", "up"], "cooldownSeconds": 150}},
    {"id": "a-35-recon", "productKind": "body-armor", "nameZh": "A-35“侦察者”", "nameEn": "A-35 Recon",
     "acquisition": {"kind": "superstore"}, "armor": {"class": "Medium", "rating": 100}},
    {"id": "las-44-nova", "productKind": "other-stratagem", "nameZh": "LAS-44 新星",
     "nameEn": "LAS-44 Nova",
     "acquisition": {"kind": "upcoming"},
     "deployment": {"type": "Support Weapon", "code": ["up", "down", "left"], "cooldownSeconds": 300}}
  ]
}`

// fakeCatalog 是最小目录的提供者。
type fakeCatalog struct {
	raw []byte
	err error
}

func (f fakeCatalog) Catalog(context.Context) (*arsenal.Catalog, error) {
	if f.err != nil {
		return nil, f.err
	}
	return arsenal.ParseCatalog(f.raw, nil)
}

func newTestCatalog(t *testing.T) fakeCatalog {
	t.Helper()
	if _, err := arsenal.ParseCatalog([]byte(fixtureCatalog), nil); err != nil {
		t.Fatalf("测试目录本身有问题：%v", err)
	}
	return fakeCatalog{raw: []byte(fixtureCatalog)}
}

// TestNewChallengeRejectsBadTarget 校验题目数据不对时直接报错，而不是出一个没法验证的题。
func TestNewChallengeRejectsBadTarget(t *testing.T) {
	cases := map[string]Target{
		"没有指令":   {ID: "x", Steps: nil},
		"认不出的方向": {ID: "x", Steps: []string{"up", "sideways"}},
	}
	for name, target := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewChallenge(target, 1); err == nil {
				t.Fatal("应当报错")
			}
		})
	}
}

// TestChallengeClickFlow 校验状态机：逐步点对、点错清零并扣机会、用光机会判失败。
func TestChallengeClickFlow(t *testing.T) {
	target := Target{ID: "orbital", NameZh: "轨道精准攻击", Steps: []string{DirUp, DirDown, DirRight}}
	challenge, err := NewChallenge(target, 1)
	if err != nil {
		t.Fatalf("构造题目失败：%v", err)
	}
	if challenge.Total() != 3 || challenge.Remaining() != 1 {
		t.Fatalf("初始状态不对：total=%d remaining=%d", challenge.Total(), challenge.Remaining())
	}

	if got := challenge.Click(DirUp); got != StepProgress {
		t.Fatalf("第一步点对应是「进行中」，实际 %v", got)
	}
	if got := progressText(challenge); got != "⬆️ ? ?" {
		t.Errorf("进度应显示已点与未点（emoji 箭头 + ?），实际 %q", got)
	}

	// 点错：进度清零、机会减一。
	if got := challenge.Click(DirLeft); got != StepWrong {
		t.Fatalf("点错且还有机会应为「答错」，实际 %v", got)
	}
	if len(challenge.Progress()) != 0 || challenge.Remaining() != 0 {
		t.Fatalf("答错后应清零并扣机会：progress=%v remaining=%d", challenge.Progress(), challenge.Remaining())
	}

	// 重新点对前两步。
	if challenge.Click(DirUp) != StepProgress || challenge.Click(DirDown) != StepProgress {
		t.Fatal("重来时应能继续点对")
	}
	// 再点错就是失败。
	if got := challenge.Click(DirUp); got != StepFailed {
		t.Fatalf("没有机会时点错应为「失败」，实际 %v", got)
	}
}

// TestChallengePassesOnLastStep 校验最后一步点对返回通过。
func TestChallengePassesOnLastStep(t *testing.T) {
	challenge, err := NewChallenge(Target{ID: "x", Steps: []string{DirLeft, DirRight}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := challenge.Click(DirLeft); got != StepProgress {
		t.Fatalf("倒数第二步应是进行中，实际 %v", got)
	}
	if got := challenge.Click(DirRight); got != StepPassed {
		t.Fatalf("最后一步点对应是通过，实际 %v", got)
	}
}

// TestChallengeIgnoresCaseAndSpaces 校验点击方向大小写与空白不敏感。
func TestChallengeIgnoresCaseAndSpaces(t *testing.T) {
	challenge, _ := NewChallenge(Target{ID: "x", Steps: []string{DirUp}}, 0)
	if got := challenge.Click(" UP "); got != StepPassed {
		t.Fatalf("方向应折叠大小写与空白，实际 %v", got)
	}
}

// TestChallengeCopiesSteps 校验题目不会因为外部切片被改而变形。
func TestChallengeCopiesSteps(t *testing.T) {
	steps := []string{DirUp, DirDown}
	challenge, _ := NewChallenge(Target{ID: "x", Steps: steps}, 0)
	steps[0] = DirLeft
	if got := challenge.Click(DirUp); got != StepProgress {
		t.Fatal("题目应持有自己的指令副本")
	}
}

// TestPickTargetFiltersCatalog 校验出题筛选：只收带召唤指令的战备，跳过下架的与指令过短的。
func TestPickTargetFiltersCatalog(t *testing.T) {
	rnd := rand.New(rand.NewSource(1))
	seen := make(map[string]bool)
	for i := 0; i < 40; i++ {
		target, err := PickTarget(context.Background(), newTestCatalog(t), rnd, nil)
		if err != nil {
			t.Fatalf("出题失败：%v", err)
		}
		seen[target.ID] = true
		if len(target.Steps) < minSteps || len(target.Steps) > maxSteps {
			t.Fatalf("指令长度不该超出范围：%v", target.Steps)
		}
	}
	for _, banned := range []string{"orbital-illumination-flare", "mg-43-machine-gun", "a-35-recon", "las-44-nova"} {
		if seen[banned] {
			t.Errorf("%s 不该被选为题目（已下架 / 指令过短 / 没有召唤指令 / 还没上线）", banned)
		}
	}
	if !seen["orbital-precision-strike"] || !seen["a-ac-8-autocannon-sentry"] {
		t.Errorf("两件合法战备都该有机会被选中，实际只看到 %v", seen)
	}
}

// TestPickTargetAvoidsRecent 校验最近出过的题会被避开，但候选不够时仍然出题。
func TestPickTargetAvoidsRecent(t *testing.T) {
	rnd := rand.New(rand.NewSource(2))
	avoid := map[string]bool{"orbital-precision-strike": true, "a-ac-8-autocannon-sentry": true}
	target, err := PickTarget(context.Background(), newTestCatalog(t), rnd, avoid)
	if err != nil {
		t.Fatalf("全部避开时仍应能出题（退回完整候选）：%v", err)
	}
	if target.ID == "" {
		t.Fatal("应返回一个目标")
	}

	avoidOne := map[string]bool{"orbital-precision-strike": true}
	for i := 0; i < 20; i++ {
		got, err := PickTarget(context.Background(), newTestCatalog(t), rnd, avoidOne)
		if err != nil {
			t.Fatal(err)
		}
		if got.ID == "orbital-precision-strike" {
			t.Fatal("还有别的候选时不该出被避开的题")
		}
	}
}

// TestPickTargetReportsEmptyPool 校验目录里没有可用战备时给出可读错误。
func TestPickTargetReportsEmptyPool(t *testing.T) {
	empty := fakeCatalog{raw: []byte(`{"items":[{"id":"a","productKind":"body-armor","nameEn":"A","acquisition":{"kind":"default"}}]}`)}
	_, err := PickTarget(context.Background(), empty, rand.New(rand.NewSource(1)), nil)
	if err == nil || !strings.Contains(err.Error(), "没有可用") {
		t.Fatalf("应报「没有可用的召唤指令」，实际 %v", err)
	}
}

// TestPickTargetPropagatesCatalogError 校验目录取不到时错误原样上抛（由调用方兜底放行）。
func TestPickTargetPropagatesCatalogError(t *testing.T) {
	boom := errors.New("目录炸了")
	if _, err := PickTarget(context.Background(), fakeCatalog{err: boom}, rand.New(rand.NewSource(1)), nil); !errors.Is(err, boom) {
		t.Fatalf("应上抛目录错误，实际 %v", err)
	}
}

// TestNormalizedStepsDropsUnknown 校验脏指令被丢掉（上游偶尔会给出认不出的方向）。
func TestNormalizedStepsDropsUnknown(t *testing.T) {
	got := normalizedSteps([]string{"UP", " sideways ", "down", ""})
	if len(got) != 2 || got[0] != DirUp || got[1] != DirDown {
		t.Fatalf("归一化结果不对：%v", got)
	}
}

// TestGlyphAndAssetFallback 校验两套符号的对应关系与兜底：
// 群里的按钮与正文用 emoji 箭头（好认好点），题面图里用游戏内的箭头素材，素材缺失时才退回纯文本箭头。
func TestGlyphAndAssetFallback(t *testing.T) {
	if Glyph(DirUp) != "↑" || Asset(DirUp) != "arrow.up" {
		t.Fatal("已知方向应有纯文本符号与素材")
	}
	if Emoji(DirUp) != "⬆️" || Emoji(DirDown) != "⬇️" || Emoji(DirLeft) != "⬅️" || Emoji(DirRight) != "➡️" {
		t.Fatalf("四个方向都应有 emoji 箭头：%q %q %q %q",
			Emoji(DirUp), Emoji(DirDown), Emoji(DirLeft), Emoji(DirRight))
	}
	if Glyph("sideways") != "sideways" || Asset("sideways") != "" {
		t.Fatal("未知方向应退回原文与空素材")
	}
	if Emoji("sideways") != "sideways" {
		t.Error("认不出的方向应退回纯文本符号")
	}
}
