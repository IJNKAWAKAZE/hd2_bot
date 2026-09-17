// format_test.go 覆盖文本回退（渲染失败或未启用渲染时发的 MarkdownV2）：
// 内容必须与卡片同源（同一份视图模型、同一个顺序与说法），且每个来自数据的字符串都要转义——
// 漏一个下划线或星号，Telegram 会整条拒收，群里看到的是「什么都没有」。
package codex

import (
	"strings"
	"testing"
	"time"

	"hd2_bot/src/arsenal"
	"hd2_bot/src/core/bot"
	"hd2_bot/src/render"
)

// TestFormatEquipmentTextDetail 校验装备文本：标题、开头、名称/型号/英文名/标签/获取/召唤指令、
// 各小节的数值与攻击部件都在，且都转过义。
func TestFormatEquipmentTextDetail(t *testing.T) {
	card := EquipmentCard{
		Meta:  render.Meta{Title: "武器图鉴", DataTime: "2026-08-14 11:25"},
		Intro: "关键字「野狼」精确命中 1 件。",
		Detail: &EquipmentDetail{
			Name:    "野狼_试作型",
			English: "AR-2 Coyote",
			Model:   "AR-2",
			Tags:    []string{"主武器", "突击步枪"},
			Acquire: "军需簿「沙漠魔影」",
			Arrows: []ArrowStep{
				{Asset: "arrow.down", Glyph: "↓"},
				{Asset: "arrow.up", Glyph: "↑"},
				{Asset: "arrow.left", Glyph: "←"},
			},
			Sections: []DetailSection{{
				Title: "操作性",
				Cells: []Field{{Label: "弹匣", Value: "45 发"}, {Label: "射速", Value: "600 发/分"}},
			}},
			Attacks: []AttackBlock{{
				Title: "攻击 #1 · 弹道",
				Tag:   "Ballistic",
				Cells: []Field{{Label: "标准伤害", Value: "75"}},
			}},
		},
		Notes: []string{"数值取自社区维护的装备目录。"},
	}
	text := FormatEquipmentText(card)
	for _, want := range []string{
		"*武器图鉴*",
		bot.Escape("关键字「野狼」精确命中 1 件。"),
		bot.Escape("野狼_试作型"),
		bot.Escape("AR-2"),
		bot.Escape("AR-2 Coyote"),
		bot.Escape("主武器 ｜ 突击步枪"),
		bot.Escape("获取：军需簿「沙漠魔影」"),
		bot.Escape("召唤指令 ↓ ↑ ←"),
		bot.Escape("操作性"),
		bot.Escape("弹匣 45 发 ｜ 射速 600 发/分"),
		bot.Escape("攻击 #1 · 弹道（Ballistic）"),
		bot.Escape("标准伤害 75"),
		bot.Escape("数值取自社区维护的装备目录。"),
		"数据时间：2026",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("文本缺少 %q：\n%s", want, text)
		}
	}
	// 下划线必须转义成 \_，否则整条消息会被 Telegram 拒收。
	if !strings.Contains(text, `\_`) {
		t.Errorf("下划线应被转义：\n%s", text)
	}
}

// TestFormatEquipmentTextMatchesCard 校验文本与卡片同源：卡片上那一件在文本里一字不少。
func TestFormatEquipmentTextMatchesCard(t *testing.T) {
	catalog := mustCatalog(t)
	result := catalog.Search(arsenal.Query{Kinds: gunSpec.Kinds, Keyword: "野狼", Limit: searchLimit})
	card := BuildEquipmentCard(gunSpec, result, catalog, nil, "野狼", time.Time{})
	if card.Detail == nil {
		t.Fatal("夹具里的「野狼」应该命中，用例本身没跑起来")
	}
	text := FormatEquipmentText(card)
	if !strings.Contains(text, bot.Escape(card.Detail.Name)) {
		t.Errorf("文本里缺少卡片上的 %q：\n%s", card.Detail.Name, text)
	}
	if !strings.Contains(text, bot.Escape(card.Intro)) {
		t.Errorf("文本里缺少卡片开头的 %q：\n%s", card.Intro, text)
	}
}

// TestFormatEquipmentTextEmpty 校验没命中时文本只出提示：没有详情、没有多余的标签行。
func TestFormatEquipmentTextEmpty(t *testing.T) {
	card := BuildEquipmentCard(gunSpec, arsenal.Result{}, nil, nil, "不存在", time.Time{})
	text := FormatEquipmentText(card)
	if !strings.Contains(text, bot.Escape(card.Empty)) {
		t.Errorf("没命中时应写清提示：\n%s", text)
	}
	if strings.Contains(text, "召唤指令") || strings.Contains(text, "获取：") {
		t.Errorf("没命中时不该留详情的标签行：\n%s", text)
	}
	// 没有数据时间时按 plugutil.DataTimeLine 的口径写「未知」（绝不显示 0001 年）。
	if !strings.Contains(text, "数据时间：未知") {
		t.Errorf("没有数据时间时应写「未知」：\n%s", text)
	}
	if strings.Contains(text, "0001") {
		t.Errorf("零值时间不该出现在文本里：\n%s", text)
	}
	if strings.Contains(text, "\n\n\n") {
		t.Errorf("不该出现连续空行：\n%s", text)
	}
}

// TestFormatWarbondsTextDetail 校验明细文本：名称、页数/勋章/价格一行、装备列表与截断说明。
func TestFormatWarbondsTextDetail(t *testing.T) {
	card := WarbondsCard{
		Meta:  render.Meta{Title: "军需簿", DataTime: "2026-08-14 11:25"},
		Intro: "关键字「沙漠魔影」命中：沙漠魔影",
		Detail: &WarbondDetail{
			Name:    "沙漠魔影",
			English: "Dust Devils",
			Pages:   "1 页",
			Medals:  "60 勋章",
			Credits: "1,000",
			Items: []WarbondItem{
				{Name: "野狼", Kind: "主武器", Acquire: "军需簿「沙漠魔影」 · 第 1 页"},
				{Name: "撞击手雷", Kind: "手雷"},
			},
			More: "另有 2 件未列出。",
		},
		Notes: []string{"勋章数是把该本全部页解锁所需的总量。"},
	}
	text := FormatWarbondsText(card)
	for _, want := range []string{
		"*军需簿*",
		bot.Escape("沙漠魔影"),
		bot.Escape("Dust Devils"),
		bot.Escape("页数：1 页 ｜ 勋章：60 勋章 ｜ 价格：1,000"),
		"装备：",
		bot.Escape("· 主武器（野狼）"),
		bot.Escape("另有 2 件未列出。"),
		bot.Escape("勋章数是把该本全部页解锁所需的总量。"),
	} {
		if !strings.Contains(text, want) {
			t.Errorf("文本缺少 %q：\n%s", want, text)
		}
	}
}

// TestFormatWarbondsTextListAndEmpty 校验名单文本与「目录里没有军需簿」的提示。
func TestFormatWarbondsTextListAndEmpty(t *testing.T) {
	card := WarbondsCard{
		Meta:  render.Meta{Title: "军需簿"},
		Intro: "全部军需簿，按目录顺序排列。",
		Books: []WarbondRow{{Name: "沙漠魔影", English: "Dust Devils", Pages: "1 页", Medals: "60 勋章", Credits: "1,000", Items: "1 件"}},
		Notes: []string{"价格栏的「—」表示这本不单卖。"},
	}
	text := FormatWarbondsText(card)
	for _, want := range []string{bot.Escape("沙漠魔影"), bot.Escape("1 页 ｜ 60 勋章 ｜ 1,000 ｜ 1 件")} {
		if !strings.Contains(text, want) {
			t.Errorf("文本缺少 %q：\n%s", want, text)
		}
	}

	empty := FormatWarbondsText(WarbondsCard{Meta: render.Meta{Title: "军需簿"}, Empty: "目录里没有军需簿数据。"})
	if !strings.Contains(empty, bot.Escape("目录里没有军需簿数据。")) {
		t.Errorf("空名单应给出提示：\n%s", empty)
	}
	if strings.Contains(empty, "装备：") {
		t.Errorf("空名单不该出现装备列表：\n%s", empty)
	}
}

// TestFormatWarbondsTextEmptyDetail 校验明细里一件装备都没有时的说明。
func TestFormatWarbondsTextEmptyDetail(t *testing.T) {
	card := WarbondsCard{
		Meta:   render.Meta{Title: "军需簿"},
		Detail: &WarbondDetail{Name: "测试本", Pages: "1 页", Medals: "1 勋章", Credits: "—"},
	}
	text := FormatWarbondsText(card)
	if !strings.Contains(text, "这本没有列出装备。") {
		t.Errorf("明细为空时应说明这本没列装备：\n%s", text)
	}
}

// TestFormatEnemyText 校验敌人文本：开头、那一只的名称/英文名/标签/基础信息（含伤害），
// 以及侧栏那几项（同阵营、来源）与卡片底部的口径说明。
func TestFormatEnemyText(t *testing.T) {
	card := EnemyCard{
		Meta:  render.Meta{Title: "敌人图鉴", DataTime: "2026-08-14 11:25"},
		Intro: "关键字「泰坦」精确命中 1 只。",
		Enemy: &EnemyItem{
			Name:    "胆汁泰坦",
			English: "Bile Titan",
			Tags:    []string{"终结族", "巨型"},
			Rows: []Field{
				{Label: "阵营", Value: "终结族", Class: "faction--terminid"},
				{Label: "体型", Value: "巨型"},
				{Label: "总生命值", Value: "6,500"},
				{Label: "伤害", Value: "【酸液】950；【近战】1000"},
			},
			Health:  "6,500",
			Related: []EnemyLink{{Name: "食腐虫", Category: "小型"}},
			Source:  "https://helldivers.wiki.gg/wiki/Bile_Titan",
		},
		Notes: []string{"派系变体（例如 Jet Brigade）沿用上游原文，加了新变体时不会被硬塞进某个阵营。"},
	}
	text := FormatEnemyText(card)
	for _, want := range []string{
		"*敌人图鉴*",
		bot.Escape("关键字「泰坦」精确命中 1 只。"),
		bot.Escape("胆汁泰坦"),
		bot.Escape("Bile Titan"),
		bot.Escape("终结族 ｜ 巨型"),
		bot.Escape("阵营 终结族 ｜ 体型 巨型 ｜ 总生命值 6,500 ｜ 伤害 【酸液】950；【近战】1000"),
		bot.Escape("同阵营：食腐虫（小型）"),
		bot.Escape("来源：https://helldivers.wiki.gg/wiki/Bile_Titan"),
		bot.Escape("派系变体（例如 Jet Brigade）沿用上游原文，加了新变体时不会被硬塞进某个阵营。"),
	} {
		if !strings.Contains(text, want) {
			t.Errorf("文本缺少 %q：\n%s", want, text)
		}
	}

	empty := FormatEnemyText(EnemyCard{
		Meta:  render.Meta{Title: "敌人图鉴"},
		Intro: "关键字「不存在」没有命中。",
		Empty: "图鉴里没有这只敌人：支持中文名与英文名，例如「追猎虫」或 Hunter。",
	})
	if !strings.Contains(empty, bot.Escape("图鉴里没有这只敌人：支持中文名与英文名，例如「追猎虫」或 Hunter。")) {
		t.Errorf("没有命中时应给出说明：\n%s", empty)
	}
	if strings.Contains(empty, "伤害：") {
		t.Errorf("没有命中时不该留伤害行：\n%s", empty)
	}
}

// TestFieldsText 校验「标签 值」拼成一行：用「｜」分隔，空列表返回空串。
func TestFieldsText(t *testing.T) {
	if got := fieldsText(nil); got != "" {
		t.Errorf("空列表应返回空串，实际 %q", got)
	}
	fields := []Field{{Label: "伤害", Value: "75"}, {Label: "弹匣", Value: "45"}}
	if got := fieldsText(fields); got != "伤害 75 ｜ 弹匣 45" {
		t.Errorf("拼接结果错误：%q", got)
	}
}
