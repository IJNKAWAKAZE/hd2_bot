// codex_test.go 覆盖六条指令的端到端流程：守卫、出图、文本回退与上游失败。
// 数据层用假实现（装备目录、敌人图鉴都是预设的），渲染器用 plugtest 的记录型假实现，
// 因此这里不碰网络也不开浏览器，跑的就是「群里打了指令会发生什么」。
package codex

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"

	"hd2_bot/src/arsenal"
	"hd2_bot/src/bestiary"
	"hd2_bot/src/hd2"
	"hd2_bot/src/plugins/plugutil"
	"hd2_bot/src/plugins/plugutil/plugtest"

	tgbotapi "github.com/ijnkawakaze/telegram-bot-api"
)

// 编译期断言：真实现满足本插件要的接口（main 装配时依赖它们）。
var (
	_ ArsenalService  = (*arsenal.Store)(nil)
	_ BestiaryService = (*bestiary.Store)(nil)
)

// tinyPNG 造一张 2×2 的 PNG：装备图必须是真的位图，渲染层才认得出格式并内联成 data URI。
func tinyPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatalf("造 PNG 失败：%v", err)
	}
	return buf.Bytes()
}

// tgbotapiUpdateWithCallback 造一个「非消息」更新（回调按钮），用来验证守卫会忽略它。
func tgbotapiUpdateWithCallback() tgbotapi.Update {
	return tgbotapi.Update{CallbackQuery: &tgbotapi.CallbackQuery{ID: "1"}}
}

// TestHandlersRegistered 校验六条图鉴指令都已注册（少一条群里就打不出来，帮助里也会对不上）。
func TestHandlersRegistered(t *testing.T) {
	names := map[string]bool{}
	for _, h := range Handlers(&plugtest.Sender{}, nil, nil, nil, nil) {
		names[h.Name] = true
	}
	for _, want := range []string{"gun", "strat", "armor", "grenade", "warbonds", "enemy"} {
		if !names[want] {
			t.Errorf("缺少 /%s 指令", want)
		}
	}
}

// TestHandlersIgnoreOtherChatAndNonMessage 校验守卫：只服务配置里指定的群，非消息更新一律忽略。
func TestHandlersIgnoreOtherChatAndNonMessage(t *testing.T) {
	gear := &fakeGear{catalog: mustCatalog(t)}
	beasts := &fakeBeasts{}
	commands := []string{"gun", "strat", "armor", "grenade", "warbonds", "enemy"}

	for _, name := range commands {
		s := &plugtest.Sender{GroupID: -100}
		handlers := Handlers(s, gear, beasts, &plugtest.Renderer{Img: []byte("假图片")}, time.UTC)
		if err := plugtest.RunHandler(t, handlers, name, plugtest.MessageUpdate(-200, "/"+name)); err != nil {
			t.Fatalf("执行 /%s 失败：%v", name, err)
		}
		if len(s.Calls) != 0 {
			t.Errorf("非目标群不该有任何回复，/%s 实际回复了 %v", name, s.Calls)
		}
	}

	s := &plugtest.Sender{GroupID: -100}
	handlers := Handlers(s, gear, beasts, &plugtest.Renderer{Img: []byte("假图片")}, time.UTC)
	callback := tgbotapiUpdateWithCallback()
	for _, name := range commands {
		if err := plugtest.RunHandler(t, handlers, name, callback); err != nil {
			t.Fatalf("执行 /%s 失败：%v", name, err)
		}
	}
	if len(s.Calls) != 0 {
		t.Errorf("非消息更新不该有任何回复，实际 %v", s.Calls)
	}
}

// TestEquipmentCommandsSendCard 校验四条装备指令带上关键字后都出一张图：
// 卡片名、标题与「画的是命令的那一件」都要对上。
// （不带关键字的行为见 TestCommandsWithoutKeywordSendInlineButton：只发一条带按钮的提示。）
func TestEquipmentCommandsSendCard(t *testing.T) {
	catalog := mustCatalog(t)
	// 每条指令查自己那一类里的夹具装备：关键字只命中一件，卡片上画的就是它。
	cases := []struct {
		spec    EquipmentSpec
		keyword string
		name    string
	}{
		{gunSpec, "野狼", "野狼"},
		{stratSpec, "迫击", "迫击哨戒炮"},
		{armorSpec, "侦察者", "A-35\u201c侦察者\u201d"},
		{grenadeSpec, "撞击", "撞击手雷"},
	}
	for _, c := range cases {
		spec := c.spec
		t.Run(spec.Command, func(t *testing.T) {
			s := &plugtest.Sender{GroupID: -100}
			r := &plugtest.Renderer{Img: []byte("假图片")}
			gear := &fakeGear{catalog: catalog, images: map[string][]byte{"ar-2-coyote": tinyPNG(t)}}
			handlers := Handlers(s, gear, nil, r, time.UTC)

			update := plugtest.MessageUpdate(-100, "/"+spec.Command+" "+c.keyword)
			if err := plugtest.RunHandler(t, handlers, spec.Command, update); err != nil {
				t.Fatalf("执行 /%s 失败：%v", spec.Command, err)
			}
			if len(s.Photos) != 1 || len(s.Replies) != 0 {
				t.Fatalf("/%s 应只发一张图，实际 %d 张图 / %d 条文本", spec.Command, len(s.Photos), len(s.Replies))
			}
			if s.PhotoChats[0] != -100 || s.PhotoReplyTo[0] != plugtest.DefaultMessageID {
				t.Errorf("发图应回给触发命令的群并引用原消息：%v / %v", s.PhotoChats, s.PhotoReplyTo)
			}
			if len(r.Cards) != 1 || r.Cards[0].Name != equipmentCardName {
				t.Fatalf("应渲染一张 %s 卡片，实际 %+v", equipmentCardName, r.Cards)
			}
			card, ok := r.Cards[0].Data.(EquipmentCard)
			if !ok {
				t.Fatalf("卡片数据应是多少装备卡片，实际 %T", r.Cards[0].Data)
			}
			if card.Meta.Title != spec.Title {
				t.Errorf("标题应为 %q，实际 %q", spec.Title, card.Meta.Title)
			}
			if card.Detail == nil || card.Detail.Name != c.name {
				t.Errorf("应把命中那一件画在卡片上（期望 %q），实际 %+v", c.name, card.Detail)
			}
			if card.Meta.DataTime == "" {
				t.Error("上游给了采集时间时应写在卡片上")
			}
		})
	}
}

// TestEquipmentSingleHitUsesDetailCard 校验关键字只命中一件时改出单件详情卡：
// 参考站点把「武器信息」放在右侧栏，这里把它并进主栏，整张卡只有一列、一张图就装得下。
func TestEquipmentSingleHitUsesDetailCard(t *testing.T) {
	catalog := mustCatalog(t)
	s := &plugtest.Sender{GroupID: -100}
	r := &plugtest.Renderer{Img: []byte("假图片")}
	gear := &fakeGear{catalog: catalog, images: map[string][]byte{"ar-2-coyote": tinyPNG(t)}}
	handlers := Handlers(s, gear, nil, r, time.UTC)

	if err := plugtest.RunHandler(t, handlers, "gun", plugtest.MessageUpdate(-100, "/gun 野狼")); err != nil {
		t.Fatalf("执行 /gun 失败：%v", err)
	}
	card, ok := r.Cards[0].Data.(EquipmentCard)
	if !ok {
		t.Fatalf("卡片数据应是多少装备卡片，实际 %T", r.Cards[0].Data)
	}
	if card.Detail == nil {
		t.Fatal("只命中一件时应改出单件详情卡")
	}
	if card.Detail.Name != "野狼" || card.Detail.Image == "" {
		t.Errorf("详情卡应带名称与装备图：%+v", card.Detail)
	}
	if card.Intro != "" {
		t.Errorf("精确命中时详情卡开头不该有多余的话：%q", card.Intro)
	}
}

// TestEquipmentMultipleHits 校验名字没写全时卡片仍只画最接近的那一件，
// 且不报「匹配到几件」——列表形态只存在于行内查询的候选里。
func TestEquipmentMultipleHits(t *testing.T) {
	catalog := mustCatalog(t)
	s := &plugtest.Sender{GroupID: -100}
	r := &plugtest.Renderer{Img: []byte("假图片")}
	gear := &fakeGear{catalog: catalog}
	handlers := Handlers(s, gear, nil, r, time.UTC)

	// 两把武器的型号都以 AR- 开头：一个关键字命中两件。
	if err := plugtest.RunHandler(t, handlers, "gun", plugtest.MessageUpdate(-100, "/gun AR-")); err != nil {
		t.Fatalf("执行 /gun 失败：%v", err)
	}
	card := r.Cards[0].Data.(EquipmentCard)
	if card.Detail == nil || card.Detail.Name != "野狼" {
		t.Fatalf("多命中时仍应画排序第一的那件，实际 %+v", card.Detail)
	}
	if !strings.Contains(card.Intro, "不是完整名字") {
		t.Errorf("开头应说明这不是完整名字：%q", card.Intro)
	}
	if strings.Contains(card.Intro, "匹配到") {
		t.Errorf("开头不该再写匹配到几件：%q", card.Intro)
	}
}

// TestCommandsWithoutKeywordSendInlineButton 校验六条图鉴指令不带关键字时都只发一条带行内搜索按钮的提示：
// 不出图、也不查上游，按钮带的前缀必须与注册时用的完全一致（前缀对不上，点开就是一次搜不出东西的空查询）。
func TestCommandsWithoutKeywordSendInlineButton(t *testing.T) {
	seen := make(map[string]bool, len(InlineSpecs))
	for _, spec := range InlineSpecs {
		t.Run(spec.Command, func(t *testing.T) {
			s := &plugtest.Sender{GroupID: -100}
			r := &plugtest.Renderer{Img: []byte("假图片")}
			// 两个数据源都换成「一查就报错」的替身：不带关键字时压根不该碰它们。
			gear := &fakeGear{err: errors.New("不带关键字时不该查装备目录")}
			beasts := &fakeBeasts{err: errors.New("不带关键字时不该查敌人图鉴")}
			handlers := Handlers(s, gear, beasts, r, time.UTC)

			if err := plugtest.RunHandler(t, handlers, spec.Command, plugtest.MessageUpdate(-100, "/"+spec.Command)); err != nil {
				t.Fatalf("执行 /%s 失败：%v", spec.Command, err)
			}
			if len(r.Cards) != 0 || len(s.Photos) != 0 {
				t.Fatalf("不带关键字时不该出图，实际 %d 张卡片 / %d 张图", len(r.Cards), len(s.Photos))
			}
			if len(s.Calls) != 1 || s.Calls[0] != "hint" {
				t.Fatalf("应只发一条带按钮的文本，实际调用 %v", s.Calls)
			}
			if !strings.Contains(s.Replies[0], spec.Label) {
				t.Errorf("提示文案应写明能搜什么，实际 %q", s.Replies[0])
			}
			// 这条文案是拼出来的短句、不经过 bot.Escape：一旦带上 MarkdownV2 保留字符，整条消息会被 Telegram 拒收。
			for _, ch := range []string{"_", "*", "[", "]", "(", ")", "~", "`", ">", "#", "+", "-", "=", "|", "{", "}", ".", "!"} {
				if strings.Contains(s.Replies[0], ch) {
					t.Errorf("提示文案不该含 MarkdownV2 保留字符 %q：%q", ch, s.Replies[0])
				}
			}
			button := s.Markups[0].InlineKeyboard[0][0]
			if button.SwitchInlineQueryCurrentChat == nil || *button.SwitchInlineQueryCurrentChat != spec.Prefix {
				t.Fatalf("按钮应带行内查询前缀 %q，实际 %v", spec.Prefix, button.SwitchInlineQueryCurrentChat)
			}
			if strings.TrimSpace(button.Text) == "" {
				t.Error("按钮文案不能为空，否则 Telegram 会拒收")
			}
			seen[spec.Command] = true
		})
	}
	// 六条指令一条都不能漏：新增指令时如果忘了接上按钮，这里会红。
	for _, want := range []string{"gun", "strat", "armor", "grenade", "warbonds", "enemy"} {
		if !seen[want] {
			t.Errorf("缺少 /%s 的不带关键字用例", want)
		}
	}
}

// TestEquipmentCommandCarriesImages 校验装备图按 id 填到卡片上；下载不到图的那件照常出卡、只是不带图。
func TestEquipmentCommandCarriesImages(t *testing.T) {
	catalog := mustCatalog(t)
	s := &plugtest.Sender{GroupID: -100}
	r := &plugtest.Renderer{Img: []byte("假图片")}
	gear := &fakeGear{catalog: catalog, images: map[string][]byte{"ar-2-coyote": tinyPNG(t)}}
	handlers := Handlers(s, gear, nil, r, time.UTC)

	if err := plugtest.RunHandler(t, handlers, "gun", plugtest.MessageUpdate(-100, "/gun 野狼")); err != nil {
		t.Fatalf("执行 /gun 失败：%v", err)
	}
	card := r.Cards[0].Data.(EquipmentCard)
	if card.Detail == nil || card.Detail.Name != "野狼" || card.Detail.Image == "" {
		t.Fatalf("命中那一件应带上装备图：%+v", card.Detail)
	}

	// 用例只给「野狼」准备了图：查另一件时下载必然失败，卡片照样出，只是不带图。
	s2 := &plugtest.Sender{GroupID: -100}
	r2 := &plugtest.Renderer{Img: []byte("假图片")}
	handlers2 := Handlers(s2, gear, nil, r2, time.UTC)
	if err := plugtest.RunHandler(t, handlers2, "gun", plugtest.MessageUpdate(-100, "/gun 解放者")); err != nil {
		t.Fatalf("执行 /gun 失败：%v", err)
	}
	other := r2.Cards[0].Data.(EquipmentCard)
	if other.Detail == nil || other.Detail.Name != "解放者" {
		t.Fatalf("应画出命中的那一件，实际 %+v", other.Detail)
	}
	if other.Detail.Image != "" {
		t.Errorf("下载不到图时应留空，而不是留一个坏链接：%q", other.Detail.Image)
	}
}

// TestEquipmentFallsBackToTextOnRenderFailure 校验渲染失败时回退文本（功能不受影响）并留下日志线索。
func TestEquipmentFallsBackToTextOnRenderFailure(t *testing.T) {
	logs := plugtest.CaptureLog(t)
	s := &plugtest.Sender{GroupID: -100}
	handlers := Handlers(s, &fakeGear{catalog: mustCatalog(t)}, nil, plugtest.FailRenderer(), time.UTC)

	if err := plugtest.RunHandler(t, handlers, "gun", plugtest.MessageUpdate(-100, "/gun 野狼")); err != nil {
		t.Fatalf("执行 /gun 失败：%v", err)
	}
	if len(s.Photos) != 0 || len(s.Replies) != 1 {
		t.Fatalf("渲染失败时应回一条文本，实际 %d 张图 / %d 条文本", len(s.Photos), len(s.Replies))
	}
	if !strings.Contains(s.Replies[0], "*武器图鉴*") {
		t.Errorf("文本回退应带上卡片标题：%s", s.Replies[0])
	}
	plugtest.AssertLogFields(t, logs, "/gun 渲染失败", -100)
}

// TestEquipmentWithoutRendererFallsBackToText 校验没有渲染器（未启用渲染）时同样走文本回退。
func TestEquipmentWithoutRendererFallsBackToText(t *testing.T) {
	s := &plugtest.Sender{GroupID: -100}
	handlers := Handlers(s, &fakeGear{catalog: mustCatalog(t)}, nil, nil, time.UTC)

	if err := plugtest.RunHandler(t, handlers, "grenade", plugtest.MessageUpdate(-100, "/grenade 燃烧")); err != nil {
		t.Fatalf("执行 /grenade 失败：%v", err)
	}
	if len(s.Photos) != 0 || len(s.Replies) != 1 {
		t.Fatalf("没有渲染器时应回一条文本，实际 %d 张图 / %d 条文本", len(s.Photos), len(s.Replies))
	}
	if !strings.Contains(s.Replies[0], "*手雷图鉴*") {
		t.Errorf("文本回退应带上卡片标题：%s", s.Replies[0])
	}
}

// TestEquipmentCatalogFailureRepliesError 校验取目录失败时回中文失败文案；限流则改说「限流」。
func TestEquipmentCatalogFailureRepliesError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"上游普通失败", errors.New("上游 502"), equipmentFailureReply},
		{"上游限流", hd2.ErrRateLimited, plugutil.RateLimitedReply},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			logs := plugtest.CaptureLog(t)
			s := &plugtest.Sender{GroupID: -100}
			handlers := Handlers(s, &fakeGear{err: c.err}, nil, &plugtest.Renderer{Img: []byte("假图片")}, time.UTC)

			if err := plugtest.RunHandler(t, handlers, "armor", plugtest.MessageUpdate(-100, "/armor 侦察")); err != nil {
				t.Fatalf("执行 /armor 失败：%v", err)
			}
			if len(s.Replies) != 1 || s.Replies[0] != c.want {
				t.Fatalf("应回 %q，实际 %v", c.want, s.Replies)
			}
			plugtest.AssertLogFields(t, logs, "/armor 取装备目录失败", -100)
		})
	}
}

// TestWarbondsSendsCardAndDetail 校验军需簿：带关键字命中时出那一本的明细；
// 不带关键字只发一条带按钮的提示（按钮带的是「军需簿-」前缀，名单在行内候选里看）。
func TestWarbondsSendsCardAndDetail(t *testing.T) {
	catalog := mustCatalog(t)
	s := &plugtest.Sender{GroupID: -100}
	r := &plugtest.Renderer{Img: []byte("假图片")}
	handlers := Handlers(s, &fakeGear{catalog: catalog}, nil, r, time.UTC)

	if err := plugtest.RunHandler(t, handlers, "warbonds", plugtest.MessageUpdate(-100, "/warbonds")); err != nil {
		t.Fatalf("执行 /warbonds 失败：%v", err)
	}
	if len(s.Photos) != 0 || len(r.Cards) != 0 {
		t.Fatalf("不带关键字时不该出图，实际 %d 张图 / %d 张卡片", len(s.Photos), len(r.Cards))
	}
	if len(s.Markups) != 1 || s.Markups[0].InlineKeyboard[0][0].SwitchInlineQueryCurrentChat == nil {
		t.Fatalf("不带关键字时应发一条带行内搜索按钮的提示，实际 %v", s.Markups)
	}

	if err := plugtest.RunHandler(t, handlers, "warbonds", plugtest.MessageUpdate(-100, "/warbonds 沙漠魔影")); err != nil {
		t.Fatalf("执行 /warbonds 失败：%v", err)
	}
	detail := r.Cards[0].Data.(WarbondsCard)
	if detail.Detail == nil || detail.Detail.Name != "沙漠魔影" {
		t.Fatalf("带关键字命中时应出明细，实际 %+v", detail.Detail)
	}
	if len(s.Photos) != 1 {
		t.Fatalf("带关键字时应发一张图，实际 %d 张", len(s.Photos))
	}
}

// TestWarbondsFallsBackToText 校验军需簿渲染失败时回退文本。
func TestWarbondsFallsBackToText(t *testing.T) {
	logs := plugtest.CaptureLog(t)
	s := &plugtest.Sender{GroupID: -100}
	handlers := Handlers(s, &fakeGear{catalog: mustCatalog(t)}, nil, plugtest.FailRenderer(), time.UTC)

	if err := plugtest.RunHandler(t, handlers, "warbonds", plugtest.MessageUpdate(-100, "/warbonds 尘卷风")); err != nil {
		t.Fatalf("执行 /warbonds 失败：%v", err)
	}
	if len(s.Replies) != 1 || !strings.Contains(s.Replies[0], "*军需簿*") {
		t.Fatalf("应回一条军需簿文本，实际 %v", s.Replies)
	}
	plugtest.AssertLogFields(t, logs, "/warbonds 渲染失败", -100)
}

// TestEnemySendsCardWithDataTime 校验敌人图鉴出一张图，并把图鉴的加载时间写在卡片上。
func TestEnemySendsCardWithDataTime(t *testing.T) {
	loaded := time.Date(2026, 8, 14, 3, 25, 0, 0, time.UTC)
	beasts := &fakeBeasts{
		list: []bestiary.Enemy{
			{Title: "Bile Titan", NameZh: "胆汁泰坦", Faction: bestiary.FactionTerminid, FactionRaw: "Terminids", Size: 3, Health: 6500},
			{Title: "Scavenger", NameZh: "食腐虫", Faction: bestiary.FactionTerminid, FactionRaw: "Terminids", Size: 0, Health: 60},
		},
		loadedAt: loaded,
	}
	s := &plugtest.Sender{GroupID: -100}
	r := &plugtest.Renderer{Img: []byte("假图片")}
	handlers := Handlers(s, nil, beasts, r, time.UTC)

	if err := plugtest.RunHandler(t, handlers, "enemy", plugtest.MessageUpdate(-100, "/enemy 泰坦")); err != nil {
		t.Fatalf("执行 /enemy 失败：%v", err)
	}
	if len(s.Photos) != 1 || len(s.Replies) != 0 {
		t.Fatalf("应只发一张图，实际 %d 张图 / %d 条文本", len(s.Photos), len(s.Replies))
	}
	if r.Cards[0].Name != enemyCardName {
		t.Fatalf("卡片名应为 %s，实际 %s", enemyCardName, r.Cards[0].Name)
	}
	card := r.Cards[0].Data.(EnemyCard)
	if card.Enemy == nil || card.Enemy.Name != "胆汁泰坦" {
		t.Fatalf("应画出命中的那一只，实际 %+v", card.Enemy)
	}
	if !strings.Contains(card.Intro, "不是完整名字") {
		t.Errorf("开头应说清命中几只：%q", card.Intro)
	}
	// 数据时间取图鉴的加载时间（可能来自本地缓存），而不是「现在」。
	if card.Meta.DataTime != "2026-08-14 03:25" {
		t.Errorf("数据时间应为图鉴加载时间，实际 %q", card.Meta.DataTime)
	}
}

// TestEnemyCommandCarriesImagesAndRelated 校验 /enemy 把图挂到卡片上：
// 图鉴条目自己没给图标时用内置详情里的外观图兜底，部位示意图与同阵营邻居也一并带上。
func TestEnemyCommandCarriesImagesAndRelated(t *testing.T) {
	beasts := &fakeBeasts{
		list: []bestiary.Enemy{
			{Title: "Bile Titan", NameZh: "胆汁泰坦", Faction: bestiary.FactionTerminid, FactionRaw: "Terminids", Size: 3, Health: 6500},
			{Title: "Scavenger", NameZh: "食腐虫", Faction: bestiary.FactionTerminid, FactionRaw: "Terminids", Size: 0, Health: 60},
			{Title: "Hulk", NameZh: "浩克", Faction: bestiary.FactionAutomaton, FactionRaw: "Automatons", Size: 2, Health: 2000},
		},
		// 只给内置详情里那张外观图与一张部位图准备了字节：其余下载失败应当只是不带图。
		images: map[string][]byte{},
	}
	entry := MatchEnemyDetail(beasts.list[0])
	if entry == nil {
		t.Fatal("内置详情里应有胆汁泰坦")
	}
	beasts.images[entry.Image] = tinyPNG(t)
	if len(entry.Parts) == 0 || entry.Parts[0].Image == "" {
		t.Fatal("内置详情里应有带示意图的部位")
	}
	beasts.images[entry.Parts[0].Image] = tinyPNG(t)

	s := &plugtest.Sender{GroupID: -100}
	r := &plugtest.Renderer{Img: []byte("假图片")}
	handlers := Handlers(s, nil, beasts, r, time.UTC)

	if err := plugtest.RunHandler(t, handlers, "enemy", plugtest.MessageUpdate(-100, "/enemy Bile Titan")); err != nil {
		t.Fatalf("执行 /enemy 失败：%v", err)
	}
	card := r.Cards[0].Data.(EnemyCard)
	if card.Enemy == nil || card.Enemy.Image == "" {
		t.Fatalf("图鉴条目没给图标时应用内置详情里的外观图兜底：%+v", card.Enemy)
	}
	if got := card.Enemy.Parts[0].Image; got == "" {
		t.Error("下到的部位示意图应挂到那一行")
	}
	// 同阵营只列同一族的另两只之外的那一只（浩克是机器人）：这里只该出现食腐虫。
	if len(card.Enemy.Related) != 1 || card.Enemy.Related[0].Name != "食腐虫" {
		t.Errorf("同阵营应只列食腐虫：%+v", card.Enemy.Related)
	}
	if !strings.Contains(card.Enemy.Source, "wiki") {
		t.Errorf("侧栏来源应是上游页面地址，实际 %q", card.Enemy.Source)
	}
}

// TestEnemyCommandWithoutAtlasDetail 校验内置详情里没有这只时，图与那几段都留空、卡片照样出。
func TestEnemyCommandWithoutAtlasDetail(t *testing.T) {
	beasts := &fakeBeasts{list: []bestiary.Enemy{{
		Title: "Unknown Straggler", NameZh: "未知游荡者", Faction: bestiary.FactionTerminid,
		FactionRaw: "Terminids", Size: 1, Health: 300,
	}}}
	s := &plugtest.Sender{GroupID: -100}
	r := &plugtest.Renderer{Img: []byte("假图片")}
	handlers := Handlers(s, nil, beasts, r, time.UTC)

	if err := plugtest.RunHandler(t, handlers, "enemy", plugtest.MessageUpdate(-100, "/enemy 未知游荡者")); err != nil {
		t.Fatalf("执行 /enemy 失败：%v", err)
	}
	card := r.Cards[0].Data.(EnemyCard)
	if card.Enemy == nil {
		t.Fatal("应画出命中的那一只")
	}
	if card.Enemy.Image != "" || card.Enemy.Source != "" || len(card.Enemy.Parts) != 0 {
		t.Errorf("内置详情里没有这只时那几段应为空：%+v", card.Enemy)
	}
	if len(beasts.imageURLs) != 0 {
		t.Errorf("没有图地址时不该去下载，实际请求了 %v", beasts.imageURLs)
	}
}

// TestEnemyFallsBackToTextOnRenderFailure 校验渲染失败时回退文本：命中的那一只照旧写在文本里。
func TestEnemyFallsBackToTextOnRenderFailure(t *testing.T) {
	logs := plugtest.CaptureLog(t)
	beasts := &fakeBeasts{list: []bestiary.Enemy{{
		Title: "Bile Titan", NameZh: "胆汁泰坦", Faction: bestiary.FactionTerminid, FactionRaw: "Terminids",
		Size: 3, Health: 6500, Damage: "【酸液】950 ｜ 【近战】1000",
	}}}
	s := &plugtest.Sender{GroupID: -100}
	handlers := Handlers(s, nil, beasts, plugtest.FailRenderer(), time.UTC)

	if err := plugtest.RunHandler(t, handlers, "enemy", plugtest.MessageUpdate(-100, "/enemy 泰坦")); err != nil {
		t.Fatalf("执行 /enemy 失败：%v", err)
	}
	if len(s.Photos) != 0 || len(s.Replies) != 1 {
		t.Fatalf("渲染失败时应回一条文本，实际 %d 张图 / %d 条文本", len(s.Photos), len(s.Replies))
	}
	for _, want := range []string{"*敌人图鉴*", "胆汁泰坦", "Bile Titan", "巨型", "6,500"} {
		if !strings.Contains(s.Replies[0], want) {
			t.Errorf("文本回退缺少 %q：%s", want, s.Replies[0])
		}
	}
	plugtest.AssertLogFields(t, logs, "/enemy 渲染失败", -100)
}

// TestEnemyListFailureRepliesError 校验取图鉴失败时回中文失败文案。
func TestEnemyListFailureRepliesError(t *testing.T) {
	logs := plugtest.CaptureLog(t)
	s := &plugtest.Sender{GroupID: -100}
	handlers := Handlers(s, nil, &fakeBeasts{err: errors.New("wiki 超时")}, &plugtest.Renderer{Img: []byte("假图片")}, time.UTC)

	if err := plugtest.RunHandler(t, handlers, "enemy", plugtest.MessageUpdate(-100, "/enemy 敌人")); err != nil {
		t.Fatalf("执行 /enemy 失败：%v", err)
	}
	if len(s.Replies) != 1 || s.Replies[0] != bestiaryFailureReply {
		t.Fatalf("应回 %q，实际 %v", bestiaryFailureReply, s.Replies)
	}
	plugtest.AssertLogFields(t, logs, "/enemy 取敌人图鉴失败", -100)
}

// TestCatalogDataTime 校验目录采集时间的解析：带毫秒的 RFC3339 与纯日期都认，坏值与空值返回零值。
func TestCatalogDataTime(t *testing.T) {
	cases := []struct {
		name      string
		captured  string
		wantZero  bool
		wantYear  int
		wantMonth time.Month
		wantDay   int
	}{
		{name: "带毫秒的 RFC3339", captured: "2026-08-14T03:25:51.159Z", wantYear: 2026, wantMonth: time.August, wantDay: 14},
		{name: "纯日期", captured: "2026-08-14", wantYear: 2026, wantMonth: time.August, wantDay: 14},
		{name: "空值", captured: "", wantZero: true},
		{name: "坏值", captured: "昨天", wantZero: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := catalogDataTime(&arsenal.Catalog{CapturedAt: c.captured}, time.UTC)
			if c.wantZero {
				if !got.IsZero() {
					t.Fatalf("应返回零值，实际 %v", got)
				}
				return
			}
			if got.Year() != c.wantYear || got.Month() != c.wantMonth || got.Day() != c.wantDay {
				t.Fatalf("解析结果错误：%v", got)
			}
		})
	}
	if got := catalogDataTime(nil, time.UTC); !got.IsZero() {
		t.Errorf("目录为空时应返回零值，实际 %v", got)
	}
}

// TestCatalogDataTimeUsesDisplayZone 校验采集时间换算到展示时区（上游是 UTC，群里看到的是东八区）。
func TestCatalogDataTimeUsesDisplayZone(t *testing.T) {
	shanghai := time.FixedZone("CST", 8*3600)
	got := catalogDataTime(&arsenal.Catalog{CapturedAt: "2026-08-14T03:25:51.159Z"}, shanghai)
	if got.Hour() != 11 {
		t.Fatalf("应换算成东八区的 11 点，实际 %v", got)
	}
}
