// 本文件属于外部测试包 render_test：断言「插件视图模型 → 卡片 HTML」这条跨包链路，
// 必须 import src/plugins/war，写进包内会形成 import 环（war 依赖 render）。
package render_test

import (
	"strings"
	"testing"
	"time"

	"hd2_bot/src/hd2"
	"hd2_bot/src/plugins/planets"
	"hd2_bot/src/plugins/war"
	"hd2_bot/src/render"
)

// warTestData 返回一份固定样本战况，字段覆盖卡片上的每个数字。
func warTestData() *hd2.War {
	return &hd2.War{
		ClientVersion: "1.003.400",
		Statistics: hd2.Statistics{
			MissionsWon:   217853802,
			MissionsLost:  25121616,
			PlayerCount:   23162,
			TerminidKills: 12000000,
		},
	}
}

// warTestTime 是样本数据的展示时间（东八区 20:00）。
func warTestTime() time.Time {
	return time.Date(2026, 9, 16, 20, 0, 0, 0, time.FixedZone("CST", 8*3600))
}

// TestWarCardHTML 校验战况卡片模板把视图模型排成预期内容。
// 走 render.HTML 而不是真浏览器：漏字段、数字没格式化到位、徽标没内联，这里就能发现，
// 不必为了排版断言起一个 Chromium。
func TestWarCardHTML(t *testing.T) {
	html, err := render.HTML(render.Card{
		Name: "war",
		Data: war.BuildWarCard(warTestData(), warTestTime(), false),
	})
	if err != nil {
		t.Fatalf("渲染战况卡片 HTML 失败：%v", err)
	}

	for _, want := range []string{
		"银河战况",                 // 标题，来自内嵌的 render.Meta
		"2026-09-16 20:00:00",  // 数据时间
		`class="card__emblem"`, // 超级地球徽标（游戏原生 SVG）内联进卡片并挂上 class
		"在线士兵", "23,162",       // KPI 一：在线士兵，千分位
		"任务成功率", "89.7%", // KPI 二：成功率保留一位小数
		"217,853,802", "25,121,616", // KPI 三：胜负场次
		"终结族", "12,000,000", // 三族击杀
		"机器人", "光能者",
		"任务胜负", // KPI 三的标签：换成两个同字号数字后，这里才有「任务胜负」四个字
	} {
		if !strings.Contains(html, want) {
			t.Errorf("战况卡片 HTML 缺少 %q", want)
		}
	}
	// 客户端版本已按用户 2026-09-17 的要求下线。
	if strings.Contains(html, "客户端版本") || strings.Contains(html, "1.003.400") {
		t.Error("战况卡片不该再出现客户端版本")
	}
	if strings.Contains(html, "数据可能已过期") {
		t.Error("实时数据不应出现过期角标")
	}
}

// TestWarCardHTMLShowsStaleBadge 校验只有降级数据才显示过期角标。
func TestWarCardHTMLShowsStaleBadge(t *testing.T) {
	stale, err := render.HTML(render.Card{
		Name: "war",
		Data: war.BuildWarCard(warTestData(), warTestTime(), true),
	})
	if err != nil {
		t.Fatalf("渲染战况卡片 HTML 失败：%v", err)
	}
	if !strings.Contains(stale, "数据可能已过期") {
		t.Error("降级数据应出现过期角标")
	}
}

// planetTestTime 是星球卡片样本数据的展示时间（东八区 20:00）。
func planetTestTime() time.Time {
	return time.Date(2026, 9, 17, 20, 0, 0, 0, time.FixedZone("CST", 8*3600))
}

// TestPlanetCardHTMLRendersBiomeLandmark 校验 /planet 卡片把群系实景图内联进星球卡的 .pc-landmark 图块
// （与社区站点 HD2 真理部的星球卡同一版式：阵营色卡框 + 群系实景图 + 战役状态与星球名）。
func TestPlanetCardHTMLRendersBiomeLandmark(t *testing.T) {
	html, err := render.HTML(render.Card{
		Name: "planet",
		Data: planets.BuildPlanetCard(hd2.Planet{
			Index: 268, Name: "Luxuriant", Sector: "Jin Xi", CurrentOwner: "Humans",
			Biome: hd2.Biome{Name: "Deciduous Forest"},
		}, planetTestTime(), false),
	})
	if err != nil {
		t.Fatalf("渲染单星球卡片 HTML 失败：%v", err)
	}

	for _, want := range []string{
		`class="planet-card planet-detail-card f-hum"`, // 我方控制 → 超级地球蓝的卡框
		`class="pc-landmark"`,                          // 群系实景图块
		"data:image/png;base64,",                       // 群系图内联成 data URI（不依赖任何外部请求）
		`<span class="pc-name">富源</span>`,
		"Luxuriant",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("单星球卡片 HTML 缺少 %q", want)
		}
	}
}

// TestPlanetCardHTMLUsesPlaceholderWithoutAsset 校验群系名映射不到配图时改用斜纹占位块：
// 宁可缺一张图，也不要留一个空框或配错一张图（上游随时可能给出新群系）。
func TestPlanetCardHTMLUsesPlaceholderWithoutAsset(t *testing.T) {
	html, err := render.HTML(render.Card{
		Name: "planet",
		Data: planets.BuildPlanetCard(hd2.Planet{
			Index: 7, Name: "Unknown Rock", CurrentOwner: "Humans",
			Biome: hd2.Biome{Name: "Uncharted Wastes"},
		}, planetTestTime(), false),
	})
	if err != nil {
		t.Fatalf("渲染单星球卡片 HTML 失败：%v", err)
	}
	// 注意断言的是「带图的那个块」而不是 class 名：pc-nobiome 的样式定义本来就写在页面 <style> 里。
	if !strings.Contains(html, `<div class="pc-landmark pc-nobiome"></div>`) {
		t.Error("映射不到群系配图时应有斜纹占位块")
	}
	if strings.Contains(html, `<div class="pc-landmark"><img`) {
		t.Error("映射不到群系配图时不该挂图")
	}
}

// TestPlanetsCardHTMLShowsHotspots 校验 /planets 卡片上的热点星球（用户 2026-09-17 要求加回来）：
// 阵营色卡框、派系徽记、群系实景图、星球名与进度条都在，且防守战跟着入侵方走色。
func TestPlanetsCardHTMLShowsHotspots(t *testing.T) {
	list := []hd2.Planet{
		{Index: 268, Name: "Luxuriant", Sector: "Jin Xi", CurrentOwner: "Humans",
			Attacking: []int{216}, Biome: hd2.Biome{Name: "Deciduous Forest"},
			Statistics: hd2.PlanetStats{PlayerCount: 1846}},
		{Index: 7, Name: "Unknown Rock", Sector: "Jin Xi", CurrentOwner: "Automaton",
			Attacking: []int{9}, Biome: hd2.Biome{Name: "Uncharted Wastes"},
			Statistics: hd2.PlanetStats{PlayerCount: 12}},
	}
	summary := planets.SummarizePlanets(list, nil, planetTestTime())
	html, err := render.HTML(render.Card{
		Name: "planets",
		Data: planets.BuildPlanetsCard(summary, planetTestTime(), false),
	})
	if err != nil {
		t.Fatalf("渲染战线总览卡片 HTML 失败：%v", err)
	}

	// 夹具里两颗星球都只有进攻行动、没有事件，所以卡面跟着控制方走色（Luxuriant 是超级地球）。
	for _, want := range []string{
		"热点星球", "planet-grid", `class="planet-card f-hum"`, "pc-ficon", "pc-landmark",
		"富源", "Luxuriant", "进攻中", "width: 100%",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("/planets 卡片缺少 %q", want)
		}
	}
	// 没有群系配图的那颗星球走斜纹占位，而不是留一个空框。
	if !strings.Contains(html, "pc-nobiome") {
		t.Error("映射不到群系配图时应有占位块")
	}
}

// TestCardShellHasNoSourceFooter 校验公共外壳里不再有「数据来源 / 非官方粉丝作品」那两行：
// 用户 2026-09-17 要求所有卡片都删掉它们；卡片外壳只有一份，所以这一条用例守住的是整批卡片。
func TestCardShellHasNoSourceFooter(t *testing.T) {
	html, err := render.HTML(render.Card{Name: "smoke", Data: render.SmokeCard{
		Meta:  render.Meta{Title: "页脚自检", DataTime: "2026-09-17 20:00:00"},
		Lines: []string{"一行"},
	}})
	if err != nil {
		t.Fatalf("渲染冒烟卡片 HTML 失败：%v", err)
	}
	for _, unwanted := range []string{"数据来源", "非官方粉丝作品", "Arrowhead", `class="card__footer"`} {
		if strings.Contains(html, unwanted) {
			t.Errorf("卡片外壳不该再出现 %q", unwanted)
		}
	}
}

// TestCardSkinKeepsHardEdges 校验皮肤基线（零圆角 / 零阴影 / 琥珀强调）没有被无意改回去。
// 这条用例盯的是 base.tmpl 的皮肤约定，不是某一类卡片：卡片样式只在 base.tmpl 里维护。
func TestCardSkinKeepsHardEdges(t *testing.T) {
	html, err := render.HTML(render.Card{Name: "smoke", Data: render.SmokeCard{
		Meta:  render.Meta{Title: "皮肤自检", DataTime: "2026-09-17 20:00:00"},
		Lines: []string{"一行"},
	}})
	if err != nil {
		t.Fatalf("渲染冒烟卡片 HTML 失败：%v", err)
	}

	if strings.Contains(html, "border-radius") {
		t.Error("皮肤规则是零圆角，模板里不该再出现 border-radius")
	}
	if strings.Contains(html, "box-shadow") {
		t.Error("皮肤规则是零阴影，模板里不该再出现 box-shadow")
	}
	for _, want := range []string{"#FFB000", "--stripe", "border-left: 3px solid var(--yellow)"} {
		if !strings.Contains(html, want) {
			t.Errorf("卡片皮肤缺少 %q", want)
		}
	}
}
