package push

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"hd2_bot/src/hd2"
	"hd2_bot/src/render"
	"hd2_bot/src/translate"
)

// Service 是推送需要的取数能力；*hd2.Service 实现了它。
// 四个端点各自返回 hd2.Result：Stale 为 true 表示这轮是拿过期快照兜底的降级数据。
type Service interface {
	Planets(ctx context.Context) (hd2.Result[[]hd2.Planet], error)
	Campaigns(ctx context.Context) (hd2.Result[[]hd2.Campaign], error)
	Dispatches(ctx context.Context) (hd2.Result[[]hd2.Dispatch], error)
	Stations(ctx context.Context) (hd2.Result[[]hd2.SpaceStation], error)
}

// Store 是推送需要的持久化能力；*state.Store 实现了它。
//   - 快照桶（PutSnapshot / GetSnapshot）：存基线，用来比出「这一轮变了什么」；
//   - 推送去重表（MarkPushed）：保证「发送后立刻重启也不会重复推送」——
//     它是永久表，所以事件的去重键必须带上「这一次变化的方向」（见 detect.go 的键构造）。
type Store interface {
	PutSnapshot(name string, data []byte, at time.Time) error
	GetSnapshot(name string) (data []byte, fetchedAt time.Time, ok bool, err error)
	MarkPushed(kind, id string, at time.Time) (bool, error)
}

// Sender 是推送需要的发送能力；*bot.Bot 实现了它。
// Reply 发 MarkdownV2 文本，SendPhoto 发图片；两者都不带 ctx（发送接口本身不支持取消）。
type Sender interface {
	Reply(chatID int64, text string, replyTo int64) error
	SendPhoto(chatID int64, png []byte, replyTo int64) error
}

// Config 是推送编排的运行参数。
// 超时不在这里配：一轮轮询的总预算由调用方（main 的 cron 任务）在建 context 时给定，
// 免得同一个数值在两处各写一遍、改了一处忘了另一处。
type Config struct {
	ChatID   int64 // 推送目标群（取自 bot.group_id）
	MaxItems int   // 卡片每节最多几条；<=0 时由 BuildCard 取默认值
}

// Deps 是推送编排的依赖。
type Deps struct {
	Service    Service              // 取数（必填）
	Store      Store                // 基线与去重表（必填）
	Sender     Sender               // 发送（必填）
	Renderer   render.Renderer      // 出图；nil 表示没有渲染能力，直接发文本
	Translator translate.Translator // 翻译；nil 表示用户关掉了翻译（正文保持英文且不写说明文案）
	Display    *time.Location       // 卡片展示时间用的时区；nil 用东八区
	Now        func() time.Time     // 「本次抓取时间」的时钟；nil 用系统时钟
	Logger     func(format string, args ...any)
}

// Collector 是推送流水线的编排：一轮 = 取数 → 检测 → 去重 → 翻译 → 出图/文本 → 发送 → 写基线。
type Collector struct {
	cfg      Config
	svc      Service
	store    Store
	sender   Sender
	renderer render.Renderer
	trans    translate.Translator
	// translateEnabled 为 false 表示「用户关掉了翻译」：正文保持英文，但卡片上不写「翻译暂不可用」。
	translateEnabled bool
	display          *time.Location
	now              func() time.Time
	logf             func(string, ...any)
}

// New 创建编排器；缺 Logger / Now / Display 时用默认值，缺 Translator 时用透传（正文保持英文）。
//
// 「没给翻译层」与「给了透传翻译层」是两种不同的语义：前者说明用户关掉了翻译（不写说明文案），
// 后者说明翻译开着但这一轮没翻动（卡片上会注明「翻译暂不可用」）。所以两者必须分开记录。
func New(cfg Config, deps Deps) *Collector {
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	logf := deps.Logger
	if logf == nil {
		logf = func(string, ...any) {}
	}
	trans := deps.Translator
	translateEnabled := trans != nil
	if trans == nil {
		trans = translate.Passthrough()
	}
	display := deps.Display
	if display == nil {
		display = time.FixedZone("CST", 8*3600)
	}
	return &Collector{
		cfg:              cfg,
		svc:              deps.Service,
		store:            deps.Store,
		sender:           deps.Sender,
		renderer:         deps.Renderer,
		trans:            trans,
		translateEnabled: translateEnabled,
		display:          display,
		now:              now,
		logf:             logf,
	}
}

// Run 执行一轮推送。
//
// 顺序是有讲究的，改动前先读清楚：
//  1. 取数：任一端点报错或返回降级数据就整轮放弃（不用过期数据做变化检测）；
//  2. 读基线：读不出来时按首次运行处理（写新基线、不推送，宁可漏一轮也不重播现状）；
//  3. 先写去重表、再发送：进程在「写去重」与「发送」之间崩掉只会漏一条，绝不会重复推；
//  4. 基线无论发送成败都要写：否则下一轮会把同一件事再推一次。
//
// 出错时只返回错误（由调用方记日志），绝不在群里发错误消息：群是给人看战况的，不是看机器人报错的。
// 返回值语义：
//   - nil：本轮正常结束（可能发了消息，也可能没有变化）；
//   - 非 nil：本轮跳过或发送失败，日志里有具体原因。
//
// ctx 影响取数、翻译与渲染；一旦卡片已经拼好，就一定要尽力发出去——事件此刻已经记进去重表，
// 半途放弃等于把它永久丢掉（发送接口本身也不支持取消）。
func (c *Collector) Run(ctx context.Context) error {
	input, err := c.collect(ctx)
	if err != nil {
		return err
	}

	prev, err := c.loadBaseline()
	if err != nil {
		c.logf("读取推送基线失败，本轮不推送：%v", err)
		prev = Snapshot{}
	}

	events := c.markPushed(Detect(prev, input))
	if len(events) == 0 {
		if err := c.saveBaseline(input); err != nil {
			return err
		}
		c.logf("推送轮询：无变化（星球 %d 颗，战役 %d 场，简报 %d 条，空间站 %d 座）",
			len(input.Planets), len(input.Campaigns), len(input.Dispatches), len(input.Stations))
		return nil
	}

	events, translated := c.fillTranslations(ctx, events)
	c.logf("推送轮询：检测到 %d 条变化，准备发送", len(events))

	sendErr := c.send(ctx, events, input.FetchedAt.In(c.display), translated)
	// 基线无论发送成败都要写：去重表已经保证重启后不重复推送，
	// 这里再失败一次也只是让下一轮重新比较一遍而已。
	if err := c.saveBaseline(input); err != nil {
		return err
	}
	return sendErr
}

// collect 取四个端点的数据；任一端点出错或返回降级数据时放弃本轮。
// 降级数据（Stale）会让变化检测把「上游不可用」误判成战况变化，因此宁可跳过这一轮。
// 展示时间一律用「本次抓取时间」：上游的 War.Now 不可信（S1 已定）。
func (c *Collector) collect(ctx context.Context) (Input, error) {
	planets, err := c.svc.Planets(ctx)
	if err != nil {
		return Input{}, fmt.Errorf("获取星球列表失败：%w", err)
	}
	if planets.Stale {
		return Input{}, errors.New("星球列表来自过期快照，跳过本轮推送")
	}
	campaigns, err := c.svc.Campaigns(ctx)
	if err != nil {
		return Input{}, fmt.Errorf("获取战役列表失败：%w", err)
	}
	if campaigns.Stale {
		return Input{}, errors.New("战役列表来自过期快照，跳过本轮推送")
	}
	dispatches, err := c.svc.Dispatches(ctx)
	if err != nil {
		return Input{}, fmt.Errorf("获取战况简报失败：%w", err)
	}
	if dispatches.Stale {
		return Input{}, errors.New("战况简报来自过期快照，跳过本轮推送")
	}
	stations, err := c.svc.Stations(ctx)
	if err != nil {
		return Input{}, fmt.Errorf("获取空间站数据失败：%w", err)
	}
	if stations.Stale {
		return Input{}, errors.New("空间站数据来自过期快照，跳过本轮推送")
	}
	return Input{
		FetchedAt:  c.now(),
		Planets:    planets.Value,
		Campaigns:  campaigns.Value,
		Dispatches: dispatches.Value,
		Stations:   stations.Value,
	}, nil
}

// loadBaseline 读取上一轮基线；没有基线时返回零值快照（Detect 会据此判定「首次运行」）。
func (c *Collector) loadBaseline() (Snapshot, error) {
	raw, _, ok, err := c.store.GetSnapshot(Baseline)
	if err != nil {
		return Snapshot{}, err
	}
	if !ok || len(raw) == 0 {
		return Snapshot{}, nil
	}
	return Unmarshal(raw)
}

// saveBaseline 把本轮数据写成下一轮的基线。
func (c *Collector) saveBaseline(input Input) error {
	raw, err := SnapshotOf(input).Marshal()
	if err != nil {
		return err
	}
	if err := c.store.PutSnapshot(Baseline, raw, input.FetchedAt); err != nil {
		return fmt.Errorf("写入推送基线失败：%w", err)
	}
	return nil
}

// markPushed 逐条记录「已推送」并丢掉重复的事件。
//
// 先记后发：进程在发送之后立刻退出也不会重复推送。反过来（先发后记）一旦在中间崩掉，
// 下一轮会把同一件事再推一次，群里看到的是重复消息，比漏一条更刺眼。
// 记录失败时保留事件：宁可发一条可能重复的消息，也不要因为写状态失败而丢掉战况。
func (c *Collector) markPushed(events []Event) []Event {
	if len(events) == 0 {
		return nil
	}
	kept := make([]Event, 0, len(events))
	for _, e := range events {
		first, err := c.store.MarkPushed(string(e.Kind), e.ID, c.now())
		if err != nil {
			c.logf("记录推送状态失败（本次仍会推送）：%v", err)
			kept = append(kept, e)
			continue
		}
		if first {
			kept = append(kept, e)
		}
	}
	return kept
}

// fillTranslations 把事件里需要翻译的英文送进翻译层，并把译文回填到 Title / Detail。
//
// 返回的布尔值表示「有没有拿到完整译文」：false 时卡片底部会注明「翻译暂不可用」。
// 三条口径：
//   - 只送需要翻译的段落（见 needsTranslation）：易主 / 战役 / 空间站的标题本来就是中文，
//     把它们也送过去既浪费额度，又会让译文队列错位；
//   - 翻不动的段落一律保留英文原文（后端原样返回或返回空白时，卡片上不能出现空标题）；
//   - 翻译层的段数必须与入参一致，不一致就整批保留英文：宁可这一轮全英文，也不能把 A 的译文贴到 B 上。
func (c *Collector) fillTranslations(ctx context.Context, events []Event) ([]Event, bool) {
	out := make([]Event, len(events))
	copy(out, events)
	if !c.translateEnabled {
		// 用户关掉了翻译：正文保持英文（仍然摘一段进卡片），也不在卡片上写「翻译暂不可用」。
		return summarizeBodies(out), true
	}
	texts := make([]string, 0, len(events)*2)
	// plan[i] 是第 i 个事件在 texts 里的下标（-1 表示这一段不需要翻译）：title 在前，body 在后。
	plan := make([][2]int, len(events))
	for i, e := range events {
		plan[i] = [2]int{-1, -1}
		if needsTranslation(e.Title) {
			plan[i][0] = len(texts)
			texts = append(texts, e.Title)
		}
		if needsTranslation(e.Body) {
			plan[i][1] = len(texts)
			texts = append(texts, e.Body)
		}
	}
	if len(texts) == 0 {
		// 这一轮的变化全是中文（例如只有易主）：一次请求都不该发。
		return summarizeBodies(out), true
	}

	translated, err := c.trans.Translate(ctx, texts)
	if err != nil {
		c.logf("推送正文翻译失败，保留英文：%v", err)
	}
	if len(translated) != len(texts) {
		c.logf("翻译层返回了 %d 段，期望 %d 段，整批保留英文", len(translated), len(texts))
		return summarizeBodies(out), false
	}

	complete := true
	for i := range out {
		if idx := plan[i][0]; idx >= 0 {
			if translate.Moved(texts[idx], translated[idx]) {
				out[i].Title = translated[idx]
			} else {
				complete = false // 标题没翻动：保留英文标题，并在卡片上说明翻译不可用
			}
		}
		idx := plan[i][1]
		if idx < 0 {
			// 正文不需要翻译（本来就是中文）：不参与「翻没翻动」的判断，摘要稍后统一补。
			continue
		}
		if translate.Moved(texts[idx], translated[idx]) {
			out[i].Detail = translate.Summarize(translated[idx], detailRunes)
			continue
		}
		complete = false
		out[i].Detail = translate.Summarize(events[i].Body, detailRunes)
	}
	// 没参与翻译的正文补一段原文摘要：简报正文不该因为「不需要翻译」就整条消失。
	for i := range out {
		if plan[i][1] < 0 && strings.TrimSpace(events[i].Body) != "" {
			out[i].Detail = translate.Summarize(events[i].Body, detailRunes)
		}
	}
	return out, complete
}

// summarizeBodies 给「这一轮不打算翻译」的正文补一段原文摘要。
// 关掉翻译、正文本来就是中文、或整批回退英文时都走它：卡片上只剩标题看不出发生了什么。
func summarizeBodies(events []Event) []Event {
	for i := range events {
		if strings.TrimSpace(events[i].Body) != "" {
			events[i].Detail = translate.Summarize(events[i].Body, detailRunes)
		}
	}
	return events
}

// send 优先发图片卡片，渲染失败时回退 MarkdownV2 文本（与命令路径同一套约定）。
// 没有渲染能力（renderer 为 nil）时直接发文本，不发一条「渲染功能不可用」的告警。
//
// 分页（S4.1）：一张图装不下时先拆成 2~3 页图，只有连分页都装不下才退文本。
// 文本兜底虽然一字不少，但用户要的是图，所以「退文本」应当是最后一步而不是第一步；
// 页数上限由 render.MaxPaginationPages 兜住，不会一次往群里刷十几张。
//
// 卡片一旦拼好就尽力发出去：事件此刻已经记进去重表，半途放弃等于把它永久丢掉。
func (c *Collector) send(ctx context.Context, events []Event, fetchedAt time.Time, translated bool) error {
	if activity, ok := c.sender.(interface{ StartPhotoPreparation(int64) func() }); ok && c.renderer != nil {
		stop := activity.StartPhotoPreparation(c.cfg.ChatID)
		defer stop()
	}
	// build 按分页器给的区间与页码拼一页卡片；translateUnavailableNote 与页码都写进卡片底部的 Note。
	build := func(page, pages, from, to, total int) Card {
		card := BuildCard(events[from:to], fetchedAt, c.cfg.MaxItems)
		if !translated {
			card.Note = translateUnavailableNote
		}
		card.Note = render.WithPageNote(card.Note, page, pages)
		return card
	}
	// 整卡（单页）卡片：文本回退与「没有渲染能力」两种情况都用它，保证两种形态说同一句话。
	full := func() Card { return build(1, 1, 0, len(events), len(events)) }
	// 没有渲染能力：整卡文本，天然不受高度限制，不用分页。
	if c.renderer == nil {
		return c.sender.Reply(c.cfg.ChatID, FormatText(full()), 0)
	}
	imgs, err := render.Paginate(ctx, c.renderer, len(events),
		func(page, pages, from, to, total int) render.Card {
			return render.Card{Name: pushCardName, Data: build(page, pages, from, to, total)}
		})
	if err != nil {
		// 分页也装不下（或渲染真的坏了）：回退完整文本，内容一字不少。
		c.logf("推送卡片渲染失败，回退文本：%v", err)
		return c.sender.Reply(c.cfg.ChatID, FormatText(full()), 0)
	}
	if len(imgs) > 1 {
		c.logf("推送卡片内容超长，分 %d 页发送（未截断）", len(imgs))
	}
	for i, img := range imgs {
		if err := c.sender.SendPhoto(c.cfg.ChatID, img, 0); err != nil {
			return fmt.Errorf("发送第 %d/%d 页失败：%w", i+1, len(imgs), err)
		}
	}
	return nil
}

// 本包的两条翻译判据都收口到 src/translate，这里只保留推送特有的那层过滤：
//   - 「要不要送」用本包的 needsTranslation（比翻译层更严，见下）；
//   - 「算不算翻动了」直接用 translate.Moved——翻译层内部按同一套规则决定要不要真发请求，
//     调用方若自己再维护一份（这里原先有个字符串版 movedText），两边判据一旦错开就会互相打架：
//     层里根本没送的中文正文会被判成「翻译失败」，于是每条中文简报都挂上一句
//     「翻译暂不可用」的假告警。

// cjkRe 匹配中日韩字符；与 translate 里的判据不同，这里额外挡掉中英混排。
var cjkRe = regexp.MustCompile(`[\x{3000}-\x{303f}\x{3400}-\x{4dbf}\x{4e00}-\x{9fff}\x{ac00}-\x{d7af}\x{f900}-\x{faff}\x{ff00}-\x{ff60}]`)

// needsTranslation 判断一段文本该不该送翻译：translate.NeedsTranslation 为真，而且一个中日韩字符都没有。
//
// 为什么比翻译层的判据更严：易主 / 战役事件的标题是「阿卡马尔IV（Acamar IV）失守」这类中英混排，
// 推送卡片上它本来就是中文（星球名是我们的既有译名），送过去只会白烧一次额度，
// 还可能把星球名翻坏。全英文的简报标题与正文才是真正的目标。
// /dispatches 与 /planet 不做这层过滤（它们要的就是整段正文的中文），所以那两个包直接用
// translate.NeedsTranslation。
func needsTranslation(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	return translate.NeedsTranslation(trimmed) && !cjkRe.MatchString(trimmed)
}
