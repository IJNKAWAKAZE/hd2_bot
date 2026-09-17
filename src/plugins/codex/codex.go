// Package codex 提供图鉴类查询：/gun、/strat、/armor、/grenade、/warbonds、/enemy，
// 以及六个行内查询前缀（武器- / 战备- / 护甲- / 手雷- / 债券- / 敌人-；星球- 仍由 planets 插件负责）。
//
// 输出形态：六条指令不带关键字时发一条带行内搜索按钮的提示（与 /planet 一个套路，用户不必自己记名字），
// 带上关键字才查。查询本身是「按名字精确查一件——图鉴卡片上只有这一件的详情，
// 列表形态只存在于行内查询的候选里；渲染失败或未启用渲染时回退 MarkdownV2 文本。
//
// 数据来源与许可：装备目录与装备图取自开源项目 SalmonC/HD2Tool（MIT 覆盖代码与数据编排，
// 图片本身是游戏内素材或 wiki 截图）；敌人数值与图标取自 helldivers.wiki.gg。完整口径见
// src/render/assets/LICENSES.md。
package codex

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/ijnkawakaze/telegram-bot-api"

	"hd2_bot/src/arsenal"
	"hd2_bot/src/atlas"
	"hd2_bot/src/bestiary"
	"hd2_bot/src/core/bot"
	"hd2_bot/src/plugins/plugutil"
	"hd2_bot/src/render"
)

const (
	// equipmentFailureReply 与 bestiaryFailureReply 是两条数据链路各自的失败文案：
	// 数据来源不同、说法也不同；限流判定与限流文案则共用 plugutil.ErrorReply。
	equipmentFailureReply = "暂时获取不到装备目录，请稍后再试。"
	bestiaryFailureReply  = "暂时获取不到敌人图鉴，请稍后再试。"

	// gearImageWidth 是卡片里装备图的最大宽度（px）。卡片上的图标框是 72 CSS px，
	// 二倍图下占 144 设备像素，取 240 既够清晰又能把内联体积压住。
	gearImageWidth = 240
	// enemyImageWidth 是敌人外观图的最大宽度（px）。参照站详情页的外观图是整块大图，
	// 卡片主栏够宽，240 会糊；640 能在宽度与体积之间取平。
	enemyImageWidth = 640
	// partImageWidth 是部位示意图的最大宽度（px）：表格里那格只有几十 px，200 足够。
	partImageWidth = 200
	// maxPartImages 是一次最多内联几张部位图：一只敌人十几个部位，全下会把 HTML 撑大，
	// 而且 Telegram 与渲染引擎都怕大图。先画前 12 个部位，剩下的按空图处理。
	maxPartImages = 12
	// maxRelatedEnemies 是侧栏「同阵营敌人」最多列几条。
	maxRelatedEnemies = 8
	// gearImageWorkers 是并发下载装备图的协程数。第一次查某件装备要现下载，
	// 十几件按顺序下会明显拖长首屏等待；对上游来说一次只发几张图，也不构成压力。
	gearImageWorkers = 4
)

// errRenderDisabled 是「渲染未启用」的内部哨兵：renderer 为 nil 时让调用方统一走文本回退，
// 而不是在每个处理器里各判一次。文案与其它插件的口径一致。
var errRenderDisabled = errors.New("渲染未启用")

// Sender 是六条指令需要的回消息能力最小集；*bot.Bot 实现了它，测试注入假实现即可。
type Sender interface {
	// Reply 以 MarkdownV2 回复文本。
	Reply(chatID int64, text string, replyTo int64) error
	// SendPhoto 发送一张图片卡片。
	SendPhoto(chatID int64, png []byte, replyTo int64) error
	// SendTextWithKeyboard 发送带内联键盘的文本（不带关键字时发的那条行内搜索提示）。
	SendTemporaryTextWithKeyboard(chatID int64, text string, markup tgbotapi.InlineKeyboardMarkup, delay time.Duration) error
	DeleteMessage(chatID, messageID int64) error
	// IsTargetChat 判断会话是否是配置里指定的群。
	IsTargetChat(chatID int64) bool
}

// ArsenalService 是装备图鉴的数据能力；*arsenal.Store 实现了它。
type ArsenalService interface {
	// Catalog 返回装备目录（缓存由数据层负责）。
	Catalog(ctx context.Context) (*arsenal.Catalog, error)
	// Image 返回一件装备的图片字节（按需下载并落盘）。
	Image(ctx context.Context, item arsenal.Item) ([]byte, error)
	// ThumbURL 返回行内结果能用的公网缩略图地址；没有可用图时是空串。
	ThumbURL(ctx context.Context, item arsenal.Item) string
}

// BestiaryService 是敌人图鉴的数据能力；*bestiary.Store 实现了它。
type BestiaryService interface {
	// List 返回全部敌人条目（缓存由数据层负责）。
	List(ctx context.Context) ([]bestiary.Enemy, error)
	// LoadedAt 返回这份图鉴的加载时间（卡片上的「数据时间」）；没加载过时是零值。
	LoadedAt() time.Time
	// Icon 返回一只敌人的图标字节（按需下载并落盘）。
	Icon(ctx context.Context, enemy bestiary.Enemy) ([]byte, error)
	// Image 按公网地址下载一张图（按地址缓存）：内置详情里的外观图与部位示意图走这条路。
	Image(ctx context.Context, url string) ([]byte, error)
}

// Handlers 返回本插件提供的指令。
//   - b 用于回消息；gear 取装备目录与装备图；beasts 取敌人图鉴；
//   - renderer 为 nil 表示未启用渲染（config.render.enabled=false 时 main 会传一个「渲染必失败」的实现），
//     六条指令都会直接走文本回退；
//   - loc 是展示时区，为 nil 或加载失败时按 Asia/Shanghai 处理。
//
// 机器人只服务配置里指定的群，其它会话的指令一律忽略。
func Handlers(b Sender, gear ArsenalService, beasts BestiaryService, renderer render.Renderer, loc *time.Location) []bot.Handler {
	h := &handler{
		sender:   b,
		gear:     gear,
		beasts:   beasts,
		renderer: renderer,
		display:  plugutil.DisplayLocation(loc),
	}
	return []bot.Handler{
		{Name: "gun", Run: h.gun},
		{Name: "strat", Run: h.strat},
		{Name: "armor", Run: h.armor},
		{Name: "grenade", Run: h.grenade},
		{Name: "warbonds", Run: h.warbonds},
		{Name: "enemy", Run: h.enemy},
	}
}

// handler 把六条指令共用的依赖收在一起，避免每个闭包各带一份参数。
type handler struct {
	sender   Sender
	gear     ArsenalService
	beasts   BestiaryService
	renderer render.Renderer
	display  *time.Location
}

// gun 处理 /gun [关键字]：一次查主武器、副武器与支援武器。
func (h *handler) gun(update tgbotapi.Update) error { return h.equipment(update, gunSpec) }

// strat 处理 /strat [关键字]：其它战备（哨戒炮、轨道打击、飞鹰等）。
func (h *handler) strat(update tgbotapi.Update) error { return h.equipment(update, stratSpec) }

// armor 处理 /armor [关键字]：护甲。
func (h *handler) armor(update tgbotapi.Update) error { return h.equipment(update, armorSpec) }

// grenade 处理 /grenade [关键字]：手雷。
func (h *handler) grenade(update tgbotapi.Update) error { return h.equipment(update, grenadeSpec) }

// equipment 是四条装备指令的共用流程：取目录 → 逐类检索 → 下载装备图 → 出图（失败回文本）。
func (h *handler) equipment(update tgbotapi.Update, spec EquipmentSpec) error {
	// 非消息更新、没有会话、不是配置里指定的群：一律忽略（守卫共用 plugutil.TargetMessage）。
	msg, ok := plugutil.TargetMessage(update, h.sender)
	if !ok {
		return nil
	}
	chatID := msg.Chat.ID
	keyword := plugutil.CommandArg(msg.Text)
	if keyword == "" {
		// 不带关键字时不再列目录：给一个行内搜索按钮，用户在输入框里挑（用户 2026-09-17 的要求）。
		return h.sendInlineHint(chatID, msg.MessageID, spec.Command)
	}

	ctx, cancel := context.WithTimeout(context.Background(), plugutil.Timeout)
	defer cancel()

	catalog, err := h.gear.Catalog(ctx)
	if err != nil {
		log.Printf("/%s 取装备目录失败 chat=%d err=%v", spec.Command, chatID, err)
		return h.sender.Reply(chatID, plugutil.ErrorReply(err, equipmentFailureReply), msg.MessageID)
	}

	// 一次跨类别检索（/gun 要同时看主/副/支援三类）：结果已按匹配度排序，
	// 第一条就是卡片上那件；其余命中只在「还匹配到」那行里列名字。
	result := catalog.Search(arsenal.Query{Kinds: spec.Kinds, Keyword: keyword, Limit: searchLimit})
	images := h.gearImages(ctx, spec.Command, chatID, firstItem(result))
	card := BuildEquipmentCard(spec, result, catalog, images, keyword, catalogDataTime(catalog, h.display))

	png, rerr := h.render(ctx, render.Card{Name: equipmentCardName, Data: card})
	if rerr != nil {
		log.Printf("/%s 渲染失败 chat=%d keyword=%q err=%v", spec.Command, chatID, keyword, rerr)
		return h.sender.Reply(chatID, FormatEquipmentText(card), msg.MessageID)
	}
	return h.sender.SendPhoto(chatID, png, msg.MessageID)
}

// warbonds 处理 /warbonds [关键字]：不带关键字发一条带行内搜索按钮的提示，带了就给那一本的明细。
func (h *handler) warbonds(update tgbotapi.Update) error {
	// 非消息更新、没有会话、不是配置里指定的群：一律忽略（守卫共用 plugutil.TargetMessage）。
	msg, ok := plugutil.TargetMessage(update, h.sender)
	if !ok {
		return nil
	}
	chatID := msg.Chat.ID
	keyword := plugutil.CommandArg(msg.Text)
	if keyword == "" {
		// 不带关键字时列全部 24 本已经没用了：改成行内搜索按钮，名单在候选里也一样看得见。
		return h.sendInlineHint(chatID, msg.MessageID, "warbonds")
	}

	ctx, cancel := context.WithTimeout(context.Background(), plugutil.Timeout)
	defer cancel()

	catalog, err := h.gear.Catalog(ctx)
	if err != nil {
		log.Printf("/warbonds 取装备目录失败 chat=%d err=%v", chatID, err)
		return h.sender.Reply(chatID, plugutil.ErrorReply(err, equipmentFailureReply), msg.MessageID)
	}

	// 查不到某本时不是错误：照常出名单，并在卡片开头说明没命中。
	var detail *WarbondDetail
	if book, found := FindWarbond(catalog, keyword); found {
		detail = BuildWarbondDetail(book, catalog.ItemsOfWarbond(book.ID), catalog)
	}
	card := BuildWarbondsCard(catalog.Warbonds(), detail, keyword, catalogDataTime(catalog, h.display))

	png, rerr := h.render(ctx, render.Card{Name: warbondsCardName, Data: card})
	if rerr != nil {
		log.Printf("/warbonds 渲染失败 chat=%d keyword=%q err=%v", chatID, keyword, rerr)
		return h.sender.Reply(chatID, FormatWarbondsText(card), msg.MessageID)
	}
	return h.sender.SendPhoto(chatID, png, msg.MessageID)
}

// enemy 处理 /enemy [关键字]：不带关键字发行内搜索按钮；带了就取图鉴 → 检索 → 出图（条目多时自动分页）。
//
// 查不到关键字命中时不报错：出一张「命中 0 只」的卡片，比回一句「没找到」更清楚
// （卡片上仍然写着搜索范围与数据来源）。
func (h *handler) enemy(update tgbotapi.Update) error {
	// 非消息更新、没有会话、不是配置里指定的群：一律忽略（守卫共用 plugutil.TargetMessage）。
	msg, ok := plugutil.TargetMessage(update, h.sender)
	if !ok {
		return nil
	}
	chatID := msg.Chat.ID
	keyword := plugutil.CommandArg(msg.Text)
	if keyword == "" {
		// 80 只一次列不完（要分 3 页），不带关键字时改成行内搜索按钮更省事。
		return h.sendInlineHint(chatID, msg.MessageID, "enemy")
	}

	ctx, cancel := context.WithTimeout(context.Background(), plugutil.Timeout)
	defer cancel()

	list, err := h.beasts.List(ctx)
	if err != nil {
		log.Printf("/enemy 取敌人图鉴失败 chat=%d err=%v", chatID, err)
		return h.sender.Reply(chatID, plugutil.ErrorReply(err, bestiaryFailureReply), msg.MessageID)
	}
	// 图鉴查询是按名字精确查一只：不再分页、也不出列表（列表形态只在行内查询的候选里）。
	result := bestiary.Search(list, bestiary.Query{Keyword: keyword, Limit: searchLimit})
	// 卡片上的「数据时间」用图鉴的加载时间：它可能来自本地缓存，写成当前时刻会显得比实际新。
	dataTime := h.beasts.LoadedAt().In(h.display)

	// 图与侧栏都要先下载/整理好再交给卡片层：卡片层只拿 data URI 与现成的名字列表。
	var images EnemyImages
	var related []EnemyLink
	if len(result.Enemies) > 0 {
		enemy := result.Enemies[0]
		entry := MatchEnemyDetail(enemy)
		images = EnemyImages{
			Icon:  h.enemyImage(ctx, chatID, enemy, entry),
			Parts: h.enemyPartImages(ctx, chatID, entry),
		}
		related = RelatedEnemies(list, enemy, maxRelatedEnemies)
	}
	card := BuildEnemyCard(result, keyword, images, related, dataTime)

	png, rerr := h.render(ctx, render.Card{Name: enemyCardName, Data: card})
	if rerr != nil {
		log.Printf("/enemy 渲染失败 chat=%d keyword=%q err=%v", chatID, keyword, rerr)
		return h.sender.Reply(chatID, FormatEnemyText(card), msg.MessageID)
	}
	return h.sender.SendPhoto(chatID, png, msg.MessageID)
}

// sendInlineHint 发一条带行内搜索按钮的提示：用户点一下，输入框里就带出这一类的前缀，
// 接着打关键字就能挑，选中候选会回填成对应指令（与 planets 插件的 /planet 一个套路）。
//
// 前缀从 InlineSpecs 里按指令名查：查不到只记一条日志、不报错——按钮只是入口，指令本身仍然可用。
func (h *handler) sendInlineHint(chatID, commandID int64, command string) error {
	spec, ok := inlineSpecForCommand(command)
	if !ok {
		log.Printf("/%s 没有登记行内前缀，跳过搜索按钮 chat=%d", command, chatID)
		return nil
	}
	// 文案是常量式的短句、不经过 bot.Escape，所以不能含 MarkdownV2 保留字符（有用例盯着）。
	text := "点下面的按钮搜索" + spec.Label + "（支持中文名或英文名）"
	if err := h.sender.SendTemporaryTextWithKeyboard(chatID, text,
		plugutil.InlineSearchMarkup("选择"+spec.Label, spec.Prefix), 0); err != nil {
		return err
	}
	if err := h.sender.DeleteMessage(chatID, commandID); err != nil {
		log.Printf("删除查询指令失败 chat=%d err=%v", chatID, err)
	}
	return nil
}

// render 调用渲染引擎；未启用渲染（renderer 为 nil）时返回错误，让调用方走文本回退。
func (h *handler) render(ctx context.Context, card render.Card) ([]byte, error) {
	if h.renderer == nil {
		return nil, errRenderDisabled
	}
	return h.renderer.Render(ctx, card)
}

// firstItem 取检索结果里的第一条：卡片只画这一件，装备图也只下载这一件的。
// 返回的是切片而不是单个元素，调用方（gearImages）只接切片。
func firstItem(result arsenal.Result) []arsenal.Item {
	if len(result.Items) == 0 {
		return nil
	}
	return result.Items[:1]
}

// gearImages 并发下载本次要显示的装备图，返回「装备 id → data URI」。
//
// 单张失败只记日志并留空（那一条不带图），不让一张图把整张卡片拖垮；
// 目录里没有配图的条目直接跳过，不去发请求。
func (h *handler) gearImages(ctx context.Context, command string, chatID int64, items []arsenal.Item) map[string]string {
	images := make(map[string]string, len(items))
	if len(items) == 0 {
		return images
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, gearImageWorkers)
	for _, item := range items {
		if strings.TrimSpace(item.ImagePath) == "" {
			continue
		}
		wg.Add(1)
		go func(item arsenal.Item) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			raw, err := h.gear.Image(ctx, item)
			if err != nil {
				log.Printf("/%s 下载装备图失败 chat=%d id=%s err=%v", command, chatID, item.ID, err)
				return
			}
			uri := render.ThumbDataURI(raw, gearImageWidth)
			if uri == "" {
				log.Printf("/%s 装备图格式认不出，这条不带图 chat=%d id=%s", command, chatID, item.ID)
				return
			}
			mu.Lock()
			images[item.ID] = uri
			mu.Unlock()
		}(item)
	}
	wg.Wait()
	return images
}

// enemyImage 下载这次要显示的那只敌人的外观图，返回 data URI。
//
// 两个来源是同一张图：先问图鉴自己的图标地址，拿不到（本地条目没配图或下载失败）时，
// 退回内置详情数据里的外观图地址。都拿不到只记一条日志并返回空串——那一条不带图，其它内容照常。
func (h *handler) enemyImage(ctx context.Context, chatID int64, enemy bestiary.Enemy, entry *atlas.Entry) string {
	raw, err := h.beasts.Icon(ctx, enemy)
	if err != nil {
		log.Printf("/enemy 下载敌人图标失败 chat=%d title=%s err=%v", chatID, enemy.Title, err)
		url := ""
		if entry != nil {
			url = strings.TrimSpace(entry.Image)
		}
		if url == "" {
			return ""
		}
		raw, err = h.beasts.Image(ctx, url)
		if err != nil {
			log.Printf("/enemy 内置详情里的外观图也拿不到 chat=%d title=%s err=%v", chatID, enemy.Title, err)
			return ""
		}
	}
	uri := render.ThumbDataURI(raw, enemyImageWidth)
	if uri == "" {
		log.Printf("/enemy 敌人图格式认不出，这条不带图 chat=%d title=%s", chatID, enemy.Title)
	}
	return uri
}

// enemyPartImages 下载这只敌人各部位的示意图（最多 maxPartImages 张），返回「部位名 → data URI」。
// 拿不到的个别部位跳过即可：部位表照常出数值，只是那几行没有缩略图。
func (h *handler) enemyPartImages(ctx context.Context, chatID int64, entry *atlas.Entry) map[string]string {
	if entry == nil || len(entry.Parts) == 0 {
		return nil
	}
	images := make(map[string]string, maxPartImages)
	missing := 0
	for _, part := range entry.Parts {
		if len(images) >= maxPartImages {
			break
		}
		url := strings.TrimSpace(part.Image)
		if url == "" {
			continue
		}
		raw, err := h.beasts.Image(ctx, url)
		if err != nil {
			missing++
			continue
		}
		uri := render.ThumbDataURI(raw, partImageWidth)
		if uri == "" {
			missing++
			continue
		}
		images[part.Name] = uri
	}
	if missing > 0 {
		log.Printf("/enemy 有 %d 张部位示意图没拿到 chat=%d title=%s", missing, chatID, entry.Name)
	}
	if len(images) == 0 {
		return nil
	}
	return images
}

// catalogDataTime 解析装备目录的采集时间（上游 meta.capturedAt），换算到展示时区。
// 解析不出来时返回零值：卡片会隐藏这一行，绝不显示 0001 年。
func catalogDataTime(catalog *arsenal.Catalog, loc *time.Location) time.Time {
	if catalog == nil {
		return time.Time{}
	}
	raw := strings.TrimSpace(catalog.CapturedAt)
	if raw == "" {
		return time.Time{}
	}
	// 上游给的是带毫秒的 RFC3339（实测 2026-08-14T03:25:51.159Z），
	// 但也可能只给日期，两种都试一次。
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02"} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed.In(loc)
		}
	}
	log.Printf("装备目录的采集时间解析失败（卡片不显示数据时间）：%q", raw)
	return time.Time{}
}
