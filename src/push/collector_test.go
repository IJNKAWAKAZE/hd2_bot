package push

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"hd2_bot/src/hd2"
	"hd2_bot/src/plugins/plugutil/plugtest"
	"hd2_bot/src/render"
	"hd2_bot/src/translate"
)

// testChatID 是用例里的推送目标群（负数，与 Telegram 超级群一致）。
const testChatID int64 = -1003899233716

// fakeService 是取数能力的假实现：四个端点各自可以被设成返回错误或降级数据。
type fakeService struct {
	planets    hd2.Result[[]hd2.Planet]
	campaigns  hd2.Result[[]hd2.Campaign]
	stations   hd2.Result[[]hd2.SpaceStation]
	dispatches hd2.Result[[]hd2.Dispatch]
	err        error
	calls      int
	// failCall 指定「第几次取数返回 err」；0 表示每次都失败（默认，与 err 一起用）。
	failCall int
	// cancel / cancelAfter 用来模拟「取数中途被 ctx 取消」：第 cancelAfter 次取数后取消 ctx。
	cancel      context.CancelFunc
	cancelAfter int
}

// beforeCall 记录一次取数，并在需要时取消 ctx；ctx 已取消时像真实实现那样返回它的错误。
func (f *fakeService) beforeCall(ctx context.Context) error {
	f.calls++
	if f.cancel != nil && f.calls == f.cancelAfter {
		f.cancel()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.err != nil && (f.failCall == 0 || f.failCall == f.calls) {
		return f.err
	}
	return nil
}

func (f *fakeService) Planets(ctx context.Context) (hd2.Result[[]hd2.Planet], error) {
	if err := f.beforeCall(ctx); err != nil {
		return hd2.Result[[]hd2.Planet]{}, err
	}
	return f.planets, nil
}

func (f *fakeService) Campaigns(ctx context.Context) (hd2.Result[[]hd2.Campaign], error) {
	if err := f.beforeCall(ctx); err != nil {
		return hd2.Result[[]hd2.Campaign]{}, err
	}
	return f.campaigns, nil
}

func (f *fakeService) Stations(ctx context.Context) (hd2.Result[[]hd2.SpaceStation], error) {
	if err := f.beforeCall(ctx); err != nil {
		return hd2.Result[[]hd2.SpaceStation]{}, err
	}
	return f.stations, nil
}

func (f *fakeService) Dispatches(ctx context.Context) (hd2.Result[[]hd2.Dispatch], error) {
	if err := f.beforeCall(ctx); err != nil {
		return hd2.Result[[]hd2.Dispatch]{}, err
	}
	return f.dispatches, nil
}

// recorder 记录跨假实现的动作顺序。
// 「先写去重表 → 再发送 → 最后写基线」这条约定横跨 Store 与 Sender 两个假对象，
// 只看单个假对象的调用序列断言不出来，因此这里共用一个有序步骤表。
type recorder struct {
	mu    sync.Mutex
	steps []string
}

// add 追加一个动作。
func (r *recorder) add(step string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steps = append(r.steps, step)
}

// all 返回动作序列副本。
func (r *recorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.steps...)
}

// reset 清空动作序列：跨轮次的用例跑完第一轮后清一次，后面的顺序断言才不会被上一轮的动作干扰。
func (r *recorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steps = nil
}

// index 返回某动作第一次出现的下标；没有时返回 -1。
func (r *recorder) index(step string) int {
	for i, s := range r.all() {
		if s == step {
			return i
		}
	}
	return -1
}

// assertOrder 断言 a 出现在 b 之前（两者都必须出现过）。
func (r *recorder) assertOrder(t *testing.T, a, b string) {
	t.Helper()
	ia, ib := r.index(a), r.index(b)
	if ia < 0 || ib < 0 {
		t.Fatalf("动作序列里缺少 %q 或 %q：%v", a, b, r.all())
	}
	if ia > ib {
		t.Fatalf("%q 应发生在 %q 之前，实际序列 %v", a, b, r.all())
	}
}

// fakeStore 是状态能力的假实现：记录 MarkPushed / PutSnapshot / GetSnapshot 的调用顺序。
type fakeStore struct {
	mu       sync.Mutex
	events   []string
	pushed   map[string]bool
	baseline []byte
	markErr  error
	getErr   error
	putErr   error
	rec      *recorder
}

// newFakeStore 造一个空状态：没有基线、去重表为空。
func newFakeStore(rec *recorder) *fakeStore { return &fakeStore{pushed: map[string]bool{}, rec: rec} }

// PutSnapshot 记录一次基线写入。
func (f *fakeStore) PutSnapshot(_ string, data []byte, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, "put-snapshot")
	if f.rec != nil {
		f.rec.add("put-snapshot")
	}
	if f.putErr != nil {
		return f.putErr
	}
	f.baseline = append([]byte(nil), data...)
	return nil
}

// GetSnapshot 返回当前基线；putErr 不影响读。
func (f *fakeStore) GetSnapshot(string) ([]byte, time.Time, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, "get-snapshot")
	if f.rec != nil {
		f.rec.add("get-snapshot")
	}
	if f.getErr != nil {
		return nil, time.Time{}, false, f.getErr
	}
	if f.baseline == nil {
		return nil, time.Time{}, false, nil
	}
	return f.baseline, fixedAt, true, nil
}

// MarkPushed 记录一次去重表写入：第一次返回 true，重复写入返回 false。
// 去重键刻意是 kind + ":" + id —— 与 state.Store 的真实实现保持同一形态，id 里混进冒号会在这里露出来。
func (f *fakeStore) MarkPushed(kind, id string, _ time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, "mark-pushed")
	if f.rec != nil {
		f.rec.add("mark-pushed")
	}
	if f.markErr != nil {
		return false, f.markErr
	}
	key := kind + ":" + id
	if f.pushed[key] {
		return false, nil
	}
	f.pushed[key] = true
	return true, nil
}

// trace 返回按调用顺序记录的动作序列。
func (f *fakeStore) trace() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.events...)
}

// recSender 在假 Sender 之外，把每次发送再记进共享的动作序列。
type recSender struct {
	*plugtest.Sender
	rec *recorder
}

// Reply 记录一次文本发送。
func (s recSender) Reply(chatID int64, text string, replyTo int64) error {
	if s.rec != nil {
		s.rec.add("send-text")
	}
	return s.Sender.Reply(chatID, text, replyTo)
}

// SendPhoto 记录一次图片发送。
func (s recSender) SendPhoto(chatID int64, png []byte, replyTo int64) error {
	if s.rec != nil {
		s.rec.add("send-photo")
	}
	return s.Sender.SendPhoto(chatID, png, replyTo)
}

// harness 把一轮轮询要用的假依赖与编排器打包在一起。
type harness struct {
	rec       *recorder
	svc       *fakeService
	store     *fakeStore
	sender    *plugtest.Sender
	renderer  *plugtest.Renderer
	collector *Collector
}

// newHarness 组装一套注入假依赖的编排器（渲染器默认为「渲染成功」）。
func newHarness(t *testing.T, trans translate.Translator) *harness {
	t.Helper()
	return newHarnessWith(t, Config{ChatID: testChatID, MaxItems: 5}, trans, &plugtest.Renderer{Img: []byte("png")})
}

// newHarnessWith 允许指定推送参数与渲染器；renderer 传 nil 表示没有渲染能力，走纯文本路径。
func newHarnessWith(t *testing.T, cfg Config, trans translate.Translator, renderer render.Renderer) *harness {
	t.Helper()
	rec := &recorder{}
	svc := &fakeService{
		planets:    okPlanets(),
		campaigns:  okCampaigns(),
		stations:   okStations(),
		dispatches: okDispatches(),
	}
	store := newFakeStore(rec)
	sender := &plugtest.Sender{}
	h := &harness{rec: rec, svc: svc, store: store, sender: sender}
	if r, ok := renderer.(*plugtest.Renderer); ok {
		h.renderer = r
	}
	h.collector = New(cfg, Deps{
		Service:    svc,
		Store:      store,
		Sender:     recSender{Sender: sender, rec: rec},
		Renderer:   renderer,
		Translator: trans,
		Now:        func() time.Time { return fixedAt },
		Logger:     func(string, ...any) {},
	})
	return h
}

// cancelAfterCollect 让「四个端点刚取完就把 ctx 取消」发生：now() 是 collect 的最后一步
// （FetchedAt 取它），在这里取消正好卡在「数据都拿到了、还没开始送译与渲染」的位置上，
// 于是可以单独验证后面两步拿到的 ctx 是不是上层给的那一个。
//
// 注意要在第一轮（只写基线的那个空跑轮次）之后再调用：第一轮就取消的话，
// 取数阶段会直接失败，整轮都到不了送译与渲染。
func (h *harness) cancelAfterCollect(cancel context.CancelFunc) {
	h.collector.now = func() time.Time {
		cancel()
		return fixedAt
	}
}

// run 跑一轮并断言不报错。
func (h *harness) run(t *testing.T) {
	t.Helper()
	if err := h.collector.Run(context.Background()); err != nil {
		t.Fatalf("本轮推送不应报错：%v", err)
	}
}

// addDispatch 往本轮数据里塞一条新的英文简报（发布时间比基线新一小时）。
func (h *harness) addDispatch(id int64, message string) {
	h.svc.dispatches.Value = append([]hd2.Dispatch{{
		ID: id, Published: fixedAt.Add(time.Hour), Message: message,
	}}, h.svc.dispatches.Value...)
}

// okPlanets 造一份「一轮正常数据」里的星球列表。
func okPlanets() hd2.Result[[]hd2.Planet] {
	return hd2.Result[[]hd2.Planet]{FetchedAt: fixedAt, Value: []hd2.Planet{
		{Index: 7, Name: "Acamar IV", CurrentOwner: "Humans"},
	}}
}

// okCampaigns 造一份「一轮正常数据」里的战役列表。
func okCampaigns() hd2.Result[[]hd2.Campaign] {
	return hd2.Result[[]hd2.Campaign]{FetchedAt: fixedAt, Value: []hd2.Campaign{
		{ID: 100, Planet: hd2.PlanetRef{Index: 7, Name: "Acamar IV"}, Type: 0},
	}}
}

// okStations 造一份「一轮正常数据」里的空间站列表。
func okStations() hd2.Result[[]hd2.SpaceStation] {
	return hd2.Result[[]hd2.SpaceStation]{FetchedAt: fixedAt, Value: []hd2.SpaceStation{
		{ID32: 749875195, Flags: 1, TacticalActions: []hd2.TacticalAction{{Name: "EAGLE STORM", Status: 2}}},
	}}
}

// okDispatches 造一份「一轮正常数据」里的简报列表。
func okDispatches() hd2.Result[[]hd2.Dispatch] {
	return hd2.Result[[]hd2.Dispatch]{FetchedAt: fixedAt, Value: []hd2.Dispatch{
		{ID: 1048, Published: fixedAt, Message: "Older dispatch."},
	}}
}

// dispatchMessage 是一条典型的英文简报正文（带游戏内高亮标记）。
const dispatchMessage = "<i=3>MAJOR ORDER WON</i> A great victory for Super Earth."

// shortTranslator 是「返回段数少于入参」的假翻译器：接口约定等长，
// 只有后端违约时才会走到这条路径，而 plugtest.Translator 保证等长，因此这里单独造一个违约者。
type shortTranslator struct{ out []string }

// Translate 返回一段故意短于入参的译文。
func (s shortTranslator) Translate(context.Context, []string) ([]string, error) {
	return s.out, nil
}

// TestRunFirstRoundOnlyWritesBaseline 校验首次运行（没有基线）只写基线、不发消息。
func TestRunFirstRoundOnlyWritesBaseline(t *testing.T) {
	trans := &plugtest.Translator{}
	h := newHarness(t, trans)
	if err := h.collector.Run(context.Background()); err != nil {
		t.Fatalf("首次运行不应报错：%v", err)
	}
	if len(h.sender.Calls) != 0 {
		t.Fatalf("首次运行不该发任何消息，实际 %v", h.sender.Calls)
	}
	if len(h.store.baseline) == 0 {
		t.Fatal("首次运行应写入基线")
	}
	if trans.Calls() != 0 {
		t.Fatal("首次运行没有事件，不该调翻译层")
	}
}

// TestRunNoChangeSendsNothing 校验没有变化时不发消息。
func TestRunNoChangeSendsNothing(t *testing.T) {
	h := newHarness(t, translate.Passthrough())
	h.run(t)
	h.run(t)
	if len(h.sender.Calls) != 0 {
		t.Fatalf("没有变化时不该发消息，实际 %v", h.sender.Calls)
	}
}

// TestRunNoChangeSkipsCardAndTranslation 校验「数据正常但零事件」时：
// 不建卡片、不发消息、不调翻译层，但仍然推进基线（否则下一轮会把这一轮当首次运行）。
func TestRunNoChangeSkipsCardAndTranslation(t *testing.T) {
	trans := &plugtest.Translator{}
	h := newHarness(t, trans)
	h.run(t)
	h.run(t)
	if len(h.renderer.Cards) != 0 {
		t.Fatalf("零事件不该出图，实际 %d 张", len(h.renderer.Cards))
	}
	if len(h.sender.Calls) != 0 {
		t.Fatalf("零事件不该发消息，实际 %v", h.sender.Calls)
	}
	if trans.Calls() != 0 {
		t.Fatalf("零事件不该调翻译层，实际调用 %d 次", trans.Calls())
	}
	written := 0
	for _, step := range h.store.trace() {
		if step == "put-snapshot" {
			written++
		}
	}
	if written != 2 {
		t.Fatalf("两轮都该推进基线（写两次），实际 %d 次：%v", written, h.store.trace())
	}
}

// TestRunSendsPhotoAndWritesBaseline 校验检测到变化时发图片卡片，并且顺序是「先去重 → 再发送 → 最后写基线」。
func TestRunSendsPhotoAndWritesBaseline(t *testing.T) {
	trans := &plugtest.Translator{Out: []string{"重大指令达成", "超级地球取得了一次重大胜利。"}}
	h := newHarness(t, trans)
	h.run(t)

	h.addDispatch(9999, dispatchMessage)
	h.rec.reset()
	h.run(t)

	if len(h.sender.Photos) != 1 {
		t.Fatalf("应发出一张卡片，实际 %v", h.sender.Calls)
	}
	if h.sender.Calls[0] != "photo" {
		t.Fatalf("应走图片路径，实际 %v", h.sender.Calls)
	}
	if h.sender.PhotoChats[0] != testChatID || h.sender.PhotoReplyTo[0] != 0 {
		t.Fatalf("图片应发给配置里的群且不引用消息，实际 chat=%d replyTo=%d",
			h.sender.PhotoChats[0], h.sender.PhotoReplyTo[0])
	}
	if len(h.renderer.Cards) != 1 || h.renderer.Cards[0].Name != "push" {
		t.Fatalf("应渲染 push 卡片，实际 %+v", h.renderer.Cards)
	}
	trace := h.store.trace()
	if last := trace[len(trace)-1]; last != "put-snapshot" {
		t.Fatalf("基线应在发送之后写，实际调用序列 %v", trace)
	}
	// 去重表必须写在发送之前：进程在发送后立刻退出时，靠它挡住下一轮的重复推送。
	h.rec.assertOrder(t, "mark-pushed", "send-photo")
	h.rec.assertOrder(t, "send-photo", "put-snapshot")
}

// TestRunFallsBackToTextWhenRenderFails 校验渲染失败时回退 MarkdownV2 文本，内容与卡片同口径。
func TestRunFallsBackToTextWhenRenderFails(t *testing.T) {
	h := newHarnessWith(t, Config{ChatID: testChatID, MaxItems: 5}, translate.Passthrough(), plugtest.FailRenderer())
	h.run(t)
	h.addDispatch(9999, dispatchMessage)
	h.run(t)

	if len(h.sender.Photos) != 0 {
		t.Fatal("渲染失败时不应发图")
	}
	if len(h.sender.Replies) != 1 || !strings.Contains(h.sender.Replies[0], "战况播报") {
		t.Fatalf("应回退文本，实际 %v", h.sender.Replies)
	}
	if h.sender.Chats[0] != testChatID {
		t.Fatalf("文本回退也该发给配置里的群，实际 %d", h.sender.Chats[0])
	}
}

// TestRunWithoutRendererUsesText 校验没有渲染能力（Renderer 为 nil）时直接发文本，不碰渲染引擎。
func TestRunWithoutRendererUsesText(t *testing.T) {
	h := newHarnessWith(t, Config{ChatID: testChatID, MaxItems: 5}, translate.Passthrough(), nil)
	h.run(t)
	h.addDispatch(9999, dispatchMessage)
	h.run(t)

	if len(h.sender.Photos) != 0 {
		t.Fatal("没有渲染能力时不应发图")
	}
	if len(h.sender.Replies) != 1 || !strings.Contains(h.sender.Replies[0], "战况播报") {
		t.Fatalf("应发文本，实际 %v", h.sender.Replies)
	}
}

// TestRunSkipsRoundWhenUpstreamFails 校验上游出错时跳过本轮：不发消息、不写基线、不写去重表。
func TestRunSkipsRoundWhenUpstreamFails(t *testing.T) {
	h := newHarness(t, translate.Passthrough())
	h.svc.err = errors.New("上游炸了")

	err := h.collector.Run(context.Background())
	if err == nil {
		t.Fatal("上游失败应返回错误（由调用方记日志）")
	}
	if !strings.Contains(err.Error(), "星球列表") {
		t.Fatalf("错误里应说清是哪个端点失败，实际 %v", err)
	}
	if len(h.sender.Calls) != 0 {
		t.Fatalf("上游失败不该发消息，实际 %v", h.sender.Calls)
	}
	if h.store.baseline != nil {
		t.Fatal("上游失败不该写基线")
	}
	if len(h.store.trace()) != 0 {
		t.Fatalf("上游失败不该碰状态，实际 %v", h.store.trace())
	}
}

// TestRunSkipsRoundWhenDataIsStale 校验降级数据（快照兜底）时跳过本轮：
// 用过期快照做变化检测会把「上游不可用」误报成战况变化；四个端点各挡一次。
func TestRunSkipsRoundWhenDataIsStale(t *testing.T) {
	cases := []struct {
		name string
		set  func(*fakeService)
	}{
		{"星球列表降级", func(s *fakeService) { s.planets.Stale = true }},
		{"战役列表降级", func(s *fakeService) { s.campaigns.Stale = true }},
		{"战况简报降级", func(s *fakeService) { s.dispatches.Stale = true }},
		{"空间站降级", func(s *fakeService) { s.stations.Stale = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, translate.Passthrough())
			tc.set(h.svc)

			err := h.collector.Run(context.Background())
			if err == nil {
				t.Fatal("降级数据应返回错误（跳过本轮）")
			}
			if !strings.Contains(err.Error(), "过期快照") {
				t.Fatalf("错误里应说明是过期快照，实际 %v", err)
			}
			if len(h.sender.Calls) != 0 || h.store.baseline != nil {
				t.Fatalf("降级数据既不该发消息也不该写基线，实际 %v", h.sender.Calls)
			}
			if h.rec.index("mark-pushed") >= 0 {
				t.Fatalf("降级时不该写去重表，实际 %v", h.rec.all())
			}
		})
	}
}

// TestRunSkipsRoundWhenContextCanceled 校验 ctx 在取数中途被取消时：立刻收摊，
// 不发消息、不写基线、不写去重表，错误上抛给调用方（cron 任务据此记日志）。
func TestRunSkipsRoundWhenContextCanceled(t *testing.T) {
	h := newHarness(t, translate.Passthrough())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// 第 2 次取数（战役列表）之后取消：此时星球列表已经拿到了，仍然必须整轮放弃。
	h.svc.cancel, h.svc.cancelAfter = cancel, 2

	err := h.collector.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("应把 ctx 取消的错误上抛，实际 %v", err)
	}
	if h.svc.calls != 2 {
		t.Fatalf("取消后不该继续取数，实际取了 %d 次", h.svc.calls)
	}
	if len(h.sender.Calls) != 0 {
		t.Fatalf("取消后不该发消息，实际 %v", h.sender.Calls)
	}
	if h.store.baseline != nil || len(h.store.trace()) != 0 {
		t.Fatalf("取消后不该碰状态，实际 %v", h.store.trace())
	}
}

// TestRunDropsAlreadyPushedEvents 校验已推送过的事件不再推：先记录后发送，
// 进程在发送后立刻退出也不会重复推。
func TestRunDropsAlreadyPushedEvents(t *testing.T) {
	h := newHarness(t, translate.Passthrough())
	h.run(t)
	h.addDispatch(9999, "Victory.")

	// 人为把基线清掉，模拟「基线丢了但去重表还在」：不应该重播这条已推过的简报
	h.store.baseline = nil
	h.store.pushed["dispatch:9999"] = true
	h.sender.Calls = nil
	h.run(t)
	if len(h.sender.Calls) != 0 {
		t.Fatalf("已推送过的事件不该重复推送，实际 %v", h.sender.Calls)
	}
}

// TestRunDropsPushedEventWhenBaselineIsStale 复现「记完去重表就崩了、基线还没写」的现场：
// 下一轮即使再次检测到同一条简报，也必须靠去重表挡住，而不是把旧闻重播一遍。
func TestRunDropsPushedEventWhenBaselineIsStale(t *testing.T) {
	trans := &plugtest.Translator{Out: []string{"重大指令达成", "超级地球取得了一次重大胜利。"}}
	h := newHarness(t, trans)
	h.run(t)
	base := append([]byte(nil), h.store.baseline...)

	h.addDispatch(9999, dispatchMessage)
	h.run(t)
	if len(h.sender.Photos) != 1 {
		t.Fatalf("第二轮应推送新简报，实际 %v", h.sender.Calls)
	}

	// 基线回退成第一轮的样子（等价于「基线没写成」），去重表保持不动。
	h.store.baseline = base
	h.sender.Calls = nil
	h.run(t)
	if len(h.sender.Calls) != 0 {
		t.Fatalf("去重表里有这条简报时不该重播，实际 %v", h.sender.Calls)
	}
}

// TestRunTranslatesDispatchBody 校验简报正文会被翻译，并把译文摘要写进卡片。
func TestRunTranslatesDispatchBody(t *testing.T) {
	trans := &plugtest.Translator{Out: []string{"重大指令达成", "超级地球取得了一次重大胜利。"}}
	h := newHarness(t, trans)
	h.run(t)
	h.addDispatch(9999, dispatchMessage)
	h.run(t)

	if len(h.renderer.Cards) != 1 {
		t.Fatalf("应渲染一张卡片，实际 %d", len(h.renderer.Cards))
	}
	card, ok := h.renderer.Cards[0].Data.(Card)
	if !ok {
		t.Fatalf("卡片视图模型类型错误：%T", h.renderer.Cards[0].Data)
	}
	if len(card.Sections) == 0 || len(card.Sections[0].Items) == 0 {
		t.Fatalf("卡片里应有简报条目：%+v", card.Sections)
	}
	item := card.Sections[0].Items[0]
	if item.Title != "重大指令达成" {
		t.Fatalf("标题应使用译文，实际 %q", item.Title)
	}
	if !strings.Contains(item.Detail, "重大胜利") {
		t.Fatalf("摘要应使用译文，实际 %q", item.Detail)
	}
	if trans.Calls() != 1 || len(trans.Texts[0]) != 2 {
		t.Fatalf("应一次把标题与正文一起送译，实际 %v", trans.Texts)
	}
	if card.Note != "" {
		t.Fatalf("翻译成功时不该有说明文案，实际 %q", card.Note)
	}
}

// TestRunKeepsRealUpstreamDispatchBody 校验真实上游最长简报正文走完整路径后一字不差。
// 807 rune 来自 2026-09-17 的真实上游统计（1048 条简报里清洗后最长的正文，id=3639）：
// 摘要预算（detailRunes）与终局防线（maxDetailRunes）都要装下它，
// 图片路径与文本回退路径共用同一份视图模型，两条路都不该出现省略号。
func TestRunKeepsRealUpstreamDispatchBody(t *testing.T) {
	// 正文段按实测最长长度造（真实那条是「标题行 + 正文」合计 807 rune，这里把 807 全放在正文段上，更严一点）。
	body := strings.Repeat("潜兵", 403) + "。" // 806 + 1 = 807 rune
	if got := len([]rune(body)); got != realUpstreamMaxDetailRunes {
		t.Fatalf("夹具应是 %d rune 的真实长度，实际 %d", realUpstreamMaxDetailRunes, got)
	}

	h := newHarness(t, &plugtest.Translator{})
	h.run(t) // 第一轮只写基线
	h.addDispatch(9999, "<i=3>MAJOR ORDER WON</i>\n"+body)
	h.run(t)

	if len(h.renderer.Cards) != 1 {
		t.Fatalf("应渲染一张卡片，实际 %d", len(h.renderer.Cards))
	}
	card := h.renderer.Cards[0].Data.(Card)
	item := card.Sections[0].Items[0]
	if item.Detail != body {
		t.Fatalf("真实最长正文应完整进卡片，实际 %d 字符：%q", len([]rune(item.Detail)), item.Detail)
	}
	// 图片与文本回退共用同一份视图模型：两条路都不该出现省略号。
	if text := FormatText(card); strings.Contains(text, "…") || !strings.Contains(text, body) {
		t.Fatalf("文本回退里也应是完整正文、不带省略号：\n%s", text)
	}
}

// TestRunOnlyEnglishSegmentsAreTranslated 校验四类事件混排时：
// 只有英文段落被送译（中文标题的事件不占额度、不错位译文队列），节的顺序与 BuildCard 的口径一致。
func TestRunOnlyEnglishSegmentsAreTranslated(t *testing.T) {
	trans := &plugtest.Translator{Out: []string{"重大指令达成", "超级地球取得了一次重大胜利。"}}
	h := newHarness(t, trans)
	h.svc.planets = hd2.Result[[]hd2.Planet]{FetchedAt: fixedAt, Value: []hd2.Planet{
		{Index: 7, Name: "Acamar IV", CurrentOwner: "Humans"},
		{Index: 9, Name: "Turing", CurrentOwner: "Humans"},
		{Index: 12, Name: "Turing", CurrentOwner: "Humans"},
	}}
	h.svc.campaigns = hd2.Result[[]hd2.Campaign]{FetchedAt: fixedAt, Value: []hd2.Campaign{
		{ID: 100, Planet: hd2.PlanetRef{Index: 9, Name: "Turing"}, Type: 0},
	}}
	h.run(t)

	// 第二轮：四类变化各来一条。
	h.addDispatch(9999, dispatchMessage)
	h.svc.planets.Value[0].CurrentOwner = "Terminids"
	h.svc.campaigns.Value = append(h.svc.campaigns.Value, hd2.Campaign{
		ID: 300, Planet: hd2.PlanetRef{Index: 12, Name: "Turing"}, Type: 4,
	})
	h.svc.stations.Value[0].Flags = 3
	h.run(t)

	if len(h.renderer.Cards) != 1 {
		t.Fatalf("应渲染一张卡片，实际 %d", len(h.renderer.Cards))
	}
	card := h.renderer.Cards[0].Data.(Card)
	want := []string{"重大指令与新简报", "星球易主", "战役动态", "空间站与 DSS"}
	got := make([]string, 0, len(card.Sections))
	for _, s := range card.Sections {
		got = append(got, s.Title)
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("节的顺序应与 BuildCard 口径一致，实际 %v", got)
	}
	if card.Note != "" {
		t.Fatalf("四类事件都翻好时不该有说明文案，实际 %q", card.Note)
	}
	if trans.Calls() != 1 || len(trans.Texts[0]) != 2 {
		t.Fatalf("只该把英文的简报标题与正文送译（一次请求两段），实际 %v", trans.Texts)
	}
	if trans.Texts[0][0] != "MAJOR ORDER WON" {
		t.Fatalf("送译的第一段应是简报标题，实际 %q", trans.Texts[0][0])
	}
	if card.Sections[0].Items[0].Title != "重大指令达成" {
		t.Fatalf("简报标题应换成译文，实际 %q", card.Sections[0].Items[0].Title)
	}
	if !strings.Contains(card.Sections[1].Items[0].Title, "失守") {
		t.Fatalf("易主事件应保留检测阶段的中文标题，实际 %q", card.Sections[1].Items[0].Title)
	}
	if !strings.Contains(card.Sections[1].Items[0].Detail, "终结族") {
		t.Fatalf("易主说明应保留中文，实际 %q", card.Sections[1].Items[0].Detail)
	}
	if card.Sections[2].Items[0].Detail != "防守战" {
		t.Fatalf("战役说明应保留中文，实际 %q", card.Sections[2].Items[0].Detail)
	}
	if !strings.Contains(card.Sections[3].Items[0].Detail, "标记") {
		t.Fatalf("空间站说明应保留中文，实际 %q", card.Sections[3].Items[0].Detail)
	}
}

// TestRunKeepsEnglishWhenTranslationFails 校验翻译失败时正文保留英文，并在卡片上注明。
func TestRunKeepsEnglishWhenTranslationFails(t *testing.T) {
	trans := &plugtest.Translator{Err: errors.New("翻译后端挂了")}
	h := newHarness(t, trans)
	h.run(t)
	h.addDispatch(9999, dispatchMessage)
	h.run(t)

	if len(h.renderer.Cards) != 1 {
		t.Fatalf("翻译失败也要出卡片（正文退化成英文），实际 %d 张", len(h.renderer.Cards))
	}
	card := h.renderer.Cards[0].Data.(Card)
	if card.Note != translateUnavailableNote {
		t.Fatalf("翻译失败时卡片应注明，实际 %q", card.Note)
	}
	if card.Sections[0].Items[0].Title != "MAJOR ORDER WON" {
		t.Fatalf("翻译失败时标题应保留英文，实际 %q", card.Sections[0].Items[0].Title)
	}
	if !strings.Contains(card.Sections[0].Items[0].Detail, "great victory") {
		t.Fatalf("翻译失败时摘要应保留英文，实际 %q", card.Sections[0].Items[0].Detail)
	}
}

// TestRunKeepsEnglishWhenTranslationIsBlank 校验译文整段空白时按「没翻动」处理：
// 保留英文原文而不是把标题写成空串，同时注明翻译不可用。
func TestRunKeepsEnglishWhenTranslationIsBlank(t *testing.T) {
	trans := &plugtest.Translator{Out: []string{"", "   "}}
	h := newHarness(t, trans)
	h.run(t)
	h.addDispatch(9999, dispatchMessage)
	h.run(t)

	card := h.renderer.Cards[0].Data.(Card)
	if card.Note != translateUnavailableNote {
		t.Fatalf("空白译文应注明翻译不可用，实际 %q", card.Note)
	}
	if card.Sections[0].Items[0].Title != "MAJOR ORDER WON" {
		t.Fatalf("空白译文时应保留英文标题，实际 %q", card.Sections[0].Items[0].Title)
	}
	if !strings.Contains(card.Sections[0].Items[0].Detail, "great victory") {
		t.Fatalf("空白译文时应保留英文摘要，实际 %q", card.Sections[0].Items[0].Detail)
	}
}

// TestRunKeepsEnglishWhenTranslationLengthMismatch 校验翻译层违约（返回段数不符）时整批保留英文：
// 宁可这一轮全是英文，也不能把 A 的译文贴到 B 上。
func TestRunKeepsEnglishWhenTranslationLengthMismatch(t *testing.T) {
	h := newHarness(t, shortTranslator{out: []string{"只翻了一段"}})
	h.run(t)
	h.addDispatch(9999, dispatchMessage)
	h.run(t)

	card := h.renderer.Cards[0].Data.(Card)
	if card.Note != translateUnavailableNote {
		t.Fatalf("段数不符应注明翻译不可用，实际 %q", card.Note)
	}
	if card.Sections[0].Items[0].Title != "MAJOR ORDER WON" {
		t.Fatalf("段数不符时应保留英文标题，实际 %q", card.Sections[0].Items[0].Title)
	}
	// 整批回退时正文摘要也要补上：否则卡片上只剩标题，简报正文静默消失。
	if strings.TrimSpace(card.Sections[0].Items[0].Detail) == "" {
		t.Fatal("段数不符整批回退时，正文摘要不能为空（否则简报正文在卡片上消失了）")
	}
}

// TestRunWithoutTranslatorSkipsNote 校验「用户关掉了翻译」（传 nil）时不在卡片上写「翻译暂不可用」：
// 那是用户自己的选择，不是故障；同时正文摘要照旧要有（关掉翻译不等于关掉正文）。
//
// 这里刻意用「标题 + 正文」两段式的简报（而不是过去那句只有一行、与标题完全相同的 "Victory."）：
// Detect 现在会把与标题重复的正文首行剥掉，单行简报的正文会因此变成空串，
// 那样就测不到「正文摘要有保留」这一条了。
func TestRunWithoutTranslatorSkipsNote(t *testing.T) {
	h := newHarness(t, nil)
	h.run(t)
	h.addDispatch(9999, dispatchMessage)
	h.run(t)

	card := h.renderer.Cards[0].Data.(Card)
	if card.Note != "" {
		t.Fatalf("关闭翻译时不应有说明文案，实际 %q", card.Note)
	}
	if card.Sections[0].Items[0].Title != "MAJOR ORDER WON" {
		t.Fatalf("关闭翻译时应原样展示英文，实际 %q", card.Sections[0].Items[0].Title)
	}
	if !strings.Contains(card.Sections[0].Items[0].Detail, "great victory") {
		t.Fatalf("关闭翻译时摘要也应原样展示英文，实际 %q", card.Sections[0].Items[0].Detail)
	}
}

// TestRunKeepsEventWhenMarkPushedFails 明确「去重表写不进去」时的行为：
// 事件照推（宁可重复一条，也不要因为写状态失败而丢掉战况），并且不算本轮失败。
func TestRunKeepsEventWhenMarkPushedFails(t *testing.T) {
	h := newHarness(t, translate.Passthrough())
	h.run(t)
	h.store.markErr = errors.New("状态文件只读")

	h.addDispatch(9999, dispatchMessage)
	if err := h.collector.Run(context.Background()); err != nil {
		t.Fatalf("去重表写入失败不该让整轮失败：%v", err)
	}
	if len(h.sender.Photos) != 1 {
		t.Fatalf("去重表写入失败时仍应推送，实际 %v", h.sender.Calls)
	}
	if h.store.baseline == nil {
		t.Fatal("去重表写入失败也应该推进基线")
	}
}

// TestRunReturnsSendError 校验发送失败会上抛给调用方（由 cron 任务记日志），且基线照写。
func TestRunReturnsSendError(t *testing.T) {
	h := newHarness(t, translate.Passthrough())
	h.run(t)
	h.sender.Err = errors.New("Telegram 挂了")
	h.addDispatch(9999, "Victory.")
	h.rec.reset()

	if err := h.collector.Run(context.Background()); err == nil {
		t.Fatal("发送失败应上抛错误")
	}
	if len(h.sender.Calls) != 1 || h.sender.Calls[0] != "photo" {
		t.Fatalf("应尝试发图并失败，实际 %v", h.sender.Calls)
	}
	if h.store.baseline == nil {
		t.Fatal("发送失败也要写基线：否则下一轮会把同一件事再推一次")
	}
	h.rec.assertOrder(t, "send-photo", "put-snapshot")
}

// TestRunMaxItemsZeroOrDefault 校验 MaxItems 写成 0 或负数时不会画出「一条都没有」的卡片。
func TestRunMaxItemsZeroOrDefault(t *testing.T) {
	cases := []struct {
		name     string
		maxItems int
	}{
		{"零值", 0},
		{"负数", -3},
		{"正常值", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarnessWith(t, Config{ChatID: testChatID, MaxItems: tc.maxItems},
				translate.Passthrough(), &plugtest.Renderer{Img: []byte("png")})
			h.run(t)
			h.addDispatch(9999, dispatchMessage)
			h.run(t)

			card := h.renderer.Cards[0].Data.(Card)
			if len(card.Sections) == 0 || len(card.Sections[0].Items) == 0 {
				t.Fatalf("MaxItems=%d 时卡片不该是空的：%+v", tc.maxItems, card.Sections)
			}
		})
	}
}

// TestRunTreatsBrokenBaselineAsFirstRound 校验基线读不出来时按首次运行处理：
// 写一份新基线、不推送（宁可漏一轮，也不要重播现状）。
func TestRunTreatsBrokenBaselineAsFirstRound(t *testing.T) {
	h := newHarness(t, translate.Passthrough())
	h.store.getErr = errors.New("状态文件损坏")

	if err := h.collector.Run(context.Background()); err != nil {
		t.Fatalf("基线读不出来不该让整轮失败：%v", err)
	}
	if len(h.sender.Calls) != 0 {
		t.Fatalf("基线读不出来时不该推送，实际 %v", h.sender.Calls)
	}
	if len(h.store.baseline) == 0 {
		t.Fatal("基线读不出来时应写一份新基线")
	}
	if h.svc.calls != 4 {
		t.Fatalf("四个端点都应取过数，实际取了 %d 次", h.svc.calls)
	}
}

// TestRunReturnsErrorWhenBaselineWriteFails 校验基线写不进去时把错误上抛：
// 首轮失败等于「下一轮还是首轮」，发送后失败等于「下一轮会重新比较一遍」。
func TestRunReturnsErrorWhenBaselineWriteFails(t *testing.T) {
	t.Run("首轮写基线失败", func(t *testing.T) {
		h := newHarness(t, translate.Passthrough())
		h.store.putErr = errors.New("磁盘满了")

		if err := h.collector.Run(context.Background()); err == nil {
			t.Fatal("写基线失败应上抛错误")
		}
		if len(h.sender.Calls) != 0 || h.store.baseline != nil {
			t.Fatalf("首轮写基线失败时不该有别的副作用，实际 %v", h.sender.Calls)
		}
	})

	t.Run("发送后写基线失败", func(t *testing.T) {
		h := newHarness(t, translate.Passthrough())
		h.run(t)
		base := append([]byte(nil), h.store.baseline...)
		h.store.putErr = errors.New("磁盘满了")
		h.addDispatch(9999, dispatchMessage)

		if err := h.collector.Run(context.Background()); err == nil {
			t.Fatal("发送后写基线失败仍应上抛错误（由调用方记日志）")
		}
		if len(h.sender.Photos) != 1 {
			t.Fatalf("写基线失败不影响本轮发送，实际 %v", h.sender.Calls)
		}
		if string(h.store.baseline) != string(base) {
			t.Fatal("写基线失败时旧基线应保持原样")
		}
	})
}

// TestNewFillsDefaults 校验缺省值：关闭翻译（nil 翻译层）时的语义、默认时区、系统时钟与空日志。
func TestNewFillsDefaults(t *testing.T) {
	c := New(Config{ChatID: 1}, Deps{})
	if c.translateEnabled {
		t.Fatal("没给翻译层时应视为「用户关掉了翻译」，而不是「翻译坏了」")
	}
	if c.trans == nil {
		t.Fatal("缺翻译层时应回退到透传实现，不能是 nil")
	}
	out, err := c.trans.Translate(context.Background(), []string{"Acamar"})
	if err != nil || len(out) != 1 || out[0] != "Acamar" {
		t.Fatalf("缺翻译层时应原样返回输入，实际 %v / %v", out, err)
	}
	if c.display == nil || c.display.String() != "CST" {
		t.Fatalf("缺展示时区时应默认东八区，实际 %v", c.display)
	}
	if c.now == nil || c.now().IsZero() {
		t.Fatal("缺 Now 时应默认用系统时钟")
	}
	if c.logf == nil {
		t.Fatal("缺日志函数时应给一个空实现，不能是 nil")
	}
	c.logf("这条日志不该让用例失败：%d", 1)
}

// TestRunSkipsRoundWhenEachEndpointFails 逐个端点验证取数失败：哪一个端点挂了，
// 错误里就必须说清是哪一个（四个端点的取数顺序是有依赖的，日志里分不清就没法排查）。
func TestRunSkipsRoundWhenEachEndpointFails(t *testing.T) {
	cases := []struct {
		name     string
		failCall int
		want     string
	}{
		{"星球列表失败", 1, "获取星球列表失败"},
		{"战役列表失败", 2, "获取战役列表失败"},
		{"战况简报失败", 3, "获取战况简报失败"},
		{"空间站失败", 4, "获取空间站数据失败"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, translate.Passthrough())
			h.svc.err, h.svc.failCall = errors.New("上游炸了"), tc.failCall

			err := h.collector.Run(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误里应说明是哪个端点失败（想要 %q），实际 %v", tc.want, err)
			}
			if h.svc.calls != tc.failCall {
				t.Fatalf("失败后就该收摊，实际取了 %d 次", h.svc.calls)
			}
			if len(h.sender.Calls) != 0 || h.store.baseline != nil || len(h.store.trace()) != 0 {
				t.Fatalf("端点失败时不该发消息、不该碰状态：%v / %v", h.sender.Calls, h.store.trace())
			}
		})
	}
}

// TestRunSkipsTranslationWhenNothingToTranslate 校验这一轮的变化全是中文（只有易主）时：
// 一次翻译请求都不发、卡片上也不写「翻译暂不可用」——没有英文要翻，不是翻译坏了。
func TestRunSkipsTranslationWhenNothingToTranslate(t *testing.T) {
	trans := &plugtest.Translator{}
	h := newHarness(t, trans)
	h.run(t)
	h.svc.planets.Value[0].CurrentOwner = "Terminids"
	h.run(t)

	if trans.Calls() != 0 {
		t.Fatalf("没有英文段落时不该调翻译层，实际调用 %d 次：%v", trans.Calls(), trans.Texts)
	}
	card := h.renderer.Cards[0].Data.(Card)
	if card.Note != "" {
		t.Fatalf("没有可翻的正文时不该写说明文案，实际 %q", card.Note)
	}
	if !strings.Contains(card.Sections[0].Items[0].Title, "失守") {
		t.Fatalf("易主标题应保持中文，实际 %q", card.Sections[0].Items[0].Title)
	}
}

// TestRunKeepsChineseDispatchBody 校验中文简报（正文与标题都是中文）不会被送译，
// 但正文摘要仍然要进卡片：不翻译不等于把这条简报的内容丢掉。
func TestRunKeepsChineseDispatchBody(t *testing.T) {
	trans := &plugtest.Translator{}
	h := newHarness(t, trans)
	h.run(t)
	h.addDispatch(9999, "<i=3>重大指令达成</i> 我们拿下了阿卡马尔IV。")
	h.run(t)

	if trans.Calls() != 0 {
		t.Fatalf("中文简报不该送译，实际调用 %d 次：%v", trans.Calls(), trans.Texts)
	}
	card := h.renderer.Cards[0].Data.(Card)
	if card.Note != "" {
		t.Fatalf("中文简报不该写「翻译暂不可用」，实际 %q", card.Note)
	}
	item := card.Sections[0].Items[0]
	if item.Title != "重大指令达成" {
		t.Fatalf("中文简报标题应原样展示，实际 %q", item.Title)
	}
	if !strings.Contains(item.Detail, "阿卡马尔IV") {
		t.Fatalf("中文简报正文应摘进卡片，实际 %q", item.Detail)
	}
}

// TestRunMixesEnglishAndChineseDispatches 校验同一轮里英文简报与中文简报混排时：
// 只把英文那条送译（中文那条不占额度、也不参与「翻没翻动」判断），两条正文都要进卡片。
func TestRunMixesEnglishAndChineseDispatches(t *testing.T) {
	trans := &plugtest.Translator{Out: []string{"重大指令达成", "超级地球取得了一次重大胜利。"}}
	h := newHarness(t, trans)
	h.run(t)

	h.svc.dispatches.Value = append([]hd2.Dispatch{
		{ID: 9998, Published: fixedAt.Add(2 * time.Hour), Message: "<i=3>重大指令达成</i> 我们拿下了阿卡马尔IV。"},
		{ID: 9999, Published: fixedAt.Add(time.Hour), Message: dispatchMessage},
	}, h.svc.dispatches.Value...)
	h.run(t)

	if trans.Calls() != 1 || len(trans.Texts[0]) != 2 {
		t.Fatalf("只该把英文那条简报送译，实际 %v", trans.Texts)
	}
	card := h.renderer.Cards[0].Data.(Card)
	if card.Note != "" {
		t.Fatalf("英文那条翻好了就不该写说明文案，实际 %q", card.Note)
	}
	items := card.Sections[0].Items
	if len(items) != 2 {
		t.Fatalf("两条简报都该进卡片，实际 %+v", items)
	}
	// 节内顺序与 Detect 一致：发布时间倒序，新的在前。
	if items[0].Title != "重大指令达成" {
		t.Fatalf("中文简报应排在前（发布时间更新），实际 %q", items[0].Title)
	}
	if !strings.Contains(items[0].Detail, "阿卡马尔IV") {
		t.Fatalf("中文简报正文应摘进卡片，实际 %q", items[0].Detail)
	}
	if items[1].Title != "重大指令达成" || !strings.Contains(items[1].Detail, "重大胜利") {
		t.Fatalf("英文简报应展示译文，实际 %+v", items[1])
	}
}

// TestRunPassesMaxItemsToCard 校验卡片每节的条数上限真的来自配置：
// 上限写小了只显示前几条，并给出「另有 N 条」的提示。
func TestRunPassesMaxItemsToCard(t *testing.T) {
	h := newHarnessWith(t, Config{ChatID: testChatID, MaxItems: 1},
		translate.Passthrough(), &plugtest.Renderer{Img: []byte("png")})
	h.run(t)
	h.svc.dispatches.Value = append([]hd2.Dispatch{
		{ID: 9999, Published: fixedAt.Add(3 * time.Hour), Message: dispatchMessage},
		{ID: 9998, Published: fixedAt.Add(2 * time.Hour), Message: dispatchMessage},
		{ID: 9997, Published: fixedAt.Add(time.Hour), Message: dispatchMessage},
	}, h.svc.dispatches.Value...)
	h.run(t)

	sec := h.renderer.Cards[0].Data.(Card).Sections[0]
	if len(sec.Items) != 1 {
		t.Fatalf("上限为 1 时每节只该留 1 条，实际 %d 条", len(sec.Items))
	}
	if !strings.Contains(sec.More, "另有 2 条") {
		t.Fatalf("截断时该给出条数提示，实际 %q", sec.More)
	}
}

// TestRunKeepsKindInDedupeKey 校验去重表是按「类别 + 事件 id」记的：不同类别的事件 id 撞车
// （实测上游里星球编号 7、战役 id 7、简报 id 7 都可能同时出现）时不能互相把对方吃掉。
func TestRunKeepsKindInDedupeKey(t *testing.T) {
	trans := &plugtest.Translator{Out: []string{"重大指令达成", "超级地球取得了一次重大胜利。"}}
	h := newHarness(t, trans)
	h.run(t)

	// 第二轮：战役 id 7 与简报 id 7 同时出现，两条事件的去重键都是 "7"。
	h.svc.campaigns.Value = append(h.svc.campaigns.Value, hd2.Campaign{
		ID: 7, Planet: hd2.PlanetRef{Index: 9, Name: "Turing"}, Type: 0,
	})
	h.svc.dispatches.Value = append([]hd2.Dispatch{
		{ID: 7, Published: fixedAt.Add(time.Hour), Message: dispatchMessage},
	}, h.svc.dispatches.Value...)
	h.run(t)

	if !h.store.pushed["dispatch:7"] {
		t.Fatalf("简报应先去重表，实际表里是 %v", h.store.pushed)
	}
	if !h.store.pushed["campaign:7"] {
		t.Fatalf("战役的去重键不该被同一 id 的简报吃掉，实际表里是 %v", h.store.pushed)
	}
	card := h.renderer.Cards[0].Data.(Card)
	if len(card.Sections) != 2 {
		t.Fatalf("两条事件都该进卡片（简报 + 战役），实际 %+v", card.Sections)
	}
}

// TestFillTranslationsNeverFlagsChinese 是 push 侧的「假告警」防线：
// 中英混排标题（易主 / 战役）与纯中文正文都不该被算成「翻译没翻动」。
//
// 推送自己的过滤规则（needsTranslation 里额外挡掉中日韩字符）保证这两类段落不会送出去，
// 回显替身返回的「译文」因此不是翻译结果；若这里判成没翻动，每张卡片都会挂一句
// 「翻译暂不可用，以上正文为英文原文」，而卡片正文里一个英文都没有。
func TestFillTranslationsNeverFlagsChinese(t *testing.T) {
	trans := &plugtest.Translator{} // 回显替身：逐段原样返回
	c := New(Config{ChatID: testChatID, MaxItems: 5}, Deps{Translator: trans})
	events := []Event{
		{Kind: KindOwner, ID: "7", Title: "天园六IV（Acamar IV）失守", Detail: "控制方：超级地球 → 终结族"},
		{Kind: KindCampaign, ID: "3", Title: "战役 3 结束", Body: "战役 3：超级地球获胜。", Detail: "超级地球获胜"},
		{Kind: KindDispatch, ID: "1049", Title: "重大指令达成", Body: "我们拿下了阿卡马尔IV。", Detail: "阿卡马尔IV"},
	}
	got, complete := c.fillTranslations(context.Background(), events)

	if trans.Calls() != 0 {
		t.Fatalf("这些段落都不该送译，实际调用 %d 次：%v", trans.Calls(), trans.Texts)
	}
	if !complete {
		t.Fatal("中文段落不该被算成「翻译没翻动」")
	}
	for i, want := range []string{"天园六IV（Acamar IV）失守", "战役 3 结束", "重大指令达成"} {
		if got[i].Title != want {
			t.Errorf("第 %d 条标题应保持原样，实际 %q", i+1, got[i].Title)
		}
	}
	// 复现 Run 里「没翻动就写说明」的那一步，确认卡片上不会出现这句假告警。
	card := BuildCard(got, cardTime(), 5)
	if !complete {
		card.Note = translateUnavailableNote
	}
	if text := FormatText(card); strings.Contains(text, "翻译暂不可用") {
		t.Errorf("文本回退里不该出现翻译说明：\n%s", text)
	}
}

// ---------------------------------------------------------------------------
// 收尾修复轮：ctx 传递、卡片展示时间与翻译单侧退化的测试缺口。
// 这些地方错了都不会编译失败，只会表现为「这一轮取消不掉」「时间差几个小时」
// 或「卡片上少一块内容」——只能靠用例钉住。
// ---------------------------------------------------------------------------

// longTranslator 是「返回段数多于入参」的假翻译器：接口约定等长，只有后端违约才会走到这条路。
// plugtest.Translator 与 shortTranslator 都表达不了「更长」这个方向，所以单独造一个。
type longTranslator struct{ out []string }

// Translate 返回一段故意长于入参的译文。
func (l longTranslator) Translate(context.Context, []string) ([]string, error) { return l.out, nil }

// ctxProbeTranslator 记录每次 Translate 拿到的 ctx 状态：用来钉住「编排层把上层给的 ctx 一路传下去」。
// 它不真的翻译（原样返回），因为这里要验证的是 ctx，不是译文。
type ctxProbeTranslator struct{ ctxErrs []error }

// Translate 记录 ctx.Err() 后原样回传输入。
func (p *ctxProbeTranslator) Translate(ctx context.Context, texts []string) ([]string, error) {
	p.ctxErrs = append(p.ctxErrs, ctx.Err())
	return append([]string(nil), texts...), nil
}

// ctxProbeRenderer 记录每次 Render 拿到的 ctx 状态（理由同上）。
type ctxProbeRenderer struct{ ctxErrs []error }

// Render 记录 ctx.Err() 后返回一张假图片。
func (r *ctxProbeRenderer) Render(ctx context.Context, _ render.Card) ([]byte, error) {
	r.ctxErrs = append(r.ctxErrs, ctx.Err())
	return []byte("png"), nil
}

// Close 满足 render.Renderer；本替身没有常驻资源。
func (r *ctxProbeRenderer) Close() error { return nil }

// TestRunAlreadyCanceledContextSkipsRound 校验进来就已经取消的 ctx 会让整轮立刻短路：
// 一个端点都不再往下取、状态一个都不碰、一条消息都不发。
// 上层（main 的 pushJob）用带超时的 ctx 给每轮封顶，这条保证「取消之后不再起新活儿」。
func TestRunAlreadyCanceledContextSkipsRound(t *testing.T) {
	h := newHarness(t, &plugtest.Translator{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := h.collector.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("预先取消的 ctx 应让本轮返回 context.Canceled，实际 %v", err)
	}
	if h.svc.calls != 1 {
		t.Fatalf("取消后不该继续取数（第一个端点就会失败），实际取了 %d 次", h.svc.calls)
	}
	if len(h.store.trace()) != 0 {
		t.Fatalf("取消后不该碰状态，实际 %v", h.store.trace())
	}
	if len(h.sender.Calls) != 0 {
		t.Fatalf("取消后不该发消息，实际 %v", h.sender.Calls)
	}
}

// TestRunForwardsContextToTranslateAndRender 校验取数完成之后取消 ctx 时，送译与渲染仍然带着同一个 ctx。
//
// 编排层刻意不因为「ctx 已取消」就跳过这两步（卡片缺一块比晚一秒更糟），它只是把 ctx 原样传下去，
// 由下游自己决定——真翻译层见到已取消的 ctx 会立刻失败并回退英文。这条挡住的是
// 「某一环偷偷换成 context.Background()」：那样这一轮在取消之后还会继续打翻译请求、继续起浏览器渲染，
// 上层给的超时封顶就形同虚设。
func TestRunForwardsContextToTranslateAndRender(t *testing.T) {
	trans := &ctxProbeTranslator{}
	renderer := &ctxProbeRenderer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := newHarnessWith(t, Config{ChatID: testChatID, MaxItems: 5}, trans, renderer)
	h.run(t) // 第一轮只写基线，此刻还不能取消（取消会让取数阶段直接失败）
	h.cancelAfterCollect(cancel)
	h.addDispatch(1049, dispatchMessage)

	if err := h.collector.Run(ctx); err != nil {
		t.Fatalf("取消只影响在途调用，不该让整轮报错：%v", err)
	}
	if len(trans.ctxErrs) != 1 {
		t.Fatalf("本轮应送译一次，实际 %d 次（%v）", len(trans.ctxErrs), trans.ctxErrs)
	}
	if !errors.Is(trans.ctxErrs[0], context.Canceled) {
		t.Fatalf("送译必须拿到上层给的 ctx（此刻已取消），实际 %v", trans.ctxErrs[0])
	}
	if len(renderer.ctxErrs) != 1 {
		t.Fatalf("本轮应渲染一次，实际 %d 次（%v）", len(renderer.ctxErrs), renderer.ctxErrs)
	}
	if !errors.Is(renderer.ctxErrs[0], context.Canceled) {
		t.Fatalf("渲染必须拿到上层给的 ctx（此刻已取消），实际 %v", renderer.ctxErrs[0])
	}
}

// TestRunCardTimeComesFromFetchTimeInDisplayZone 校验卡片上的数据时间 = 「本轮抓取时间 In(展示时区)」。
//
// 展示时区写错在群里表现为时间差几个小时，而 BuildCard 的单测只管「怎么格式化」，
// 不管「时间从哪来、带的是哪个时区」——只有从编排层产出的卡片上才能验证这一条。
func TestRunCardTimeComesFromFetchTimeInDisplayZone(t *testing.T) {
	display := time.FixedZone("X", 3*3600) // 刻意不用默认的东八区，时区没吃进去就会露馅
	h := newHarness(t, &plugtest.Translator{})
	h.collector.display = display
	h.run(t)
	h.addDispatch(1049, dispatchMessage)
	h.run(t)

	if len(h.renderer.Cards) != 1 {
		t.Fatalf("应渲染 1 张卡片，实际 %d", len(h.renderer.Cards))
	}
	card := h.renderer.Cards[0].Data.(Card)
	// fixedAt 是 UTC 12:00，+3 小时后是 15:00（东八区会是 20:00）。
	if card.DataTime != "2026-09-16 15:00:00" {
		t.Fatalf("卡片数据时间应为抓取时间在展示时区里的格式，实际 %q", card.DataTime)
	}
	if card.DataTime == fixedAt.In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05") {
		t.Fatal("卡片时间用的是默认东八区，说明配置里的展示时区没被吃进去")
	}
}

// TestRunNoteWhenOnlyTitleTranslated 校验「标题译好了、正文回原文」这半边退化也会注明翻译不可用。
// 过去两条单侧退化只有「标题与正文一起没翻动」有人守，删掉其中一半判断也全绿。
func TestRunNoteWhenOnlyTitleTranslated(t *testing.T) {
	// 队列按段消费：第 1 段是标题，给中文译文；第 2 段（正文）越界 ⇒ 替身原样回英文 = 没翻动。
	trans := &plugtest.Translator{Out: []string{"重大指令达成"}}
	h := newHarness(t, trans)
	h.run(t)
	h.addDispatch(9999, dispatchMessage)
	h.run(t)

	card := h.renderer.Cards[0].Data.(Card)
	item := card.Sections[0].Items[0]
	if item.Title != "重大指令达成" {
		t.Fatalf("标题有译文时应显示译文，实际 %q", item.Title)
	}
	if !strings.Contains(item.Detail, "great victory") {
		t.Fatalf("正文没翻动时应保留英文原文，实际 %q", item.Detail)
	}
	if card.Note != translateUnavailableNote {
		t.Fatalf("正文仍是英文时卡片必须注明翻译不可用，实际 %q", card.Note)
	}
}

// TestRunNoteWhenOnlyBodyTranslated 校验「正文译好了、标题回原文」这半边退化同样会注明，
// 且标题保留英文而不是被写成空串。
func TestRunNoteWhenOnlyBodyTranslated(t *testing.T) {
	// 队列按段消费：第 1 段标题故意回成原文（= 没翻动）；第 2 段正文给中文译文。
	trans := &plugtest.Translator{Out: []string{"MAJOR ORDER WON", "超级地球取得了一次重大胜利。"}}
	h := newHarness(t, trans)
	h.run(t)
	h.addDispatch(9999, dispatchMessage)
	h.run(t)

	card := h.renderer.Cards[0].Data.(Card)
	item := card.Sections[0].Items[0]
	if item.Title != "MAJOR ORDER WON" {
		t.Fatalf("标题没翻动时应保留英文原文，实际 %q", item.Title)
	}
	if !strings.Contains(item.Detail, "重大胜利") {
		t.Fatalf("正文有译文时应显示译文，实际 %q", item.Detail)
	}
	if card.Note != translateUnavailableNote {
		t.Fatalf("标题仍是英文时卡片必须注明翻译不可用，实际 %q", card.Note)
	}
}

// TestRunKeepsEnglishWhenTranslationReturnsExtraSegments 校验译文段数「多于」入参时同样整批保留英文：
// 多出来的段没有对应条目，照单接收只会把译文贴到错误的条目上。
func TestRunKeepsEnglishWhenTranslationReturnsExtraSegments(t *testing.T) {
	trans := longTranslator{out: []string{"重大指令达成", "超级地球取得了一次重大胜利。", "多出来的一段"}}
	h := newHarness(t, trans)
	h.run(t)
	h.addDispatch(9999, dispatchMessage)
	h.run(t)

	card := h.renderer.Cards[0].Data.(Card)
	if card.Note != translateUnavailableNote {
		t.Fatalf("段数不符应注明翻译不可用，实际 %q", card.Note)
	}
	item := card.Sections[0].Items[0]
	if item.Title != "MAJOR ORDER WON" {
		t.Fatalf("整批回退时应保留英文标题，实际 %q", item.Title)
	}
	if strings.TrimSpace(item.Detail) == "" {
		t.Fatal("整批回退时正文摘要不能为空")
	}
}

// TestNeedsTranslationRejectsNonLatinNonCJK 校验「既没有 ≥3 个连续拉丁字母、也没有中日韩字符」的输入不送译。
//
// 这类文本（箭头、纯数字、符号）翻译层根本不会发请求；调用方若判成「需要翻译」，
// 后端原样回显就会被误标成「翻译暂不可用」——这是「假告警」里最后一条还开着的口子。
func TestNeedsTranslationRejectsNonLatinNonCJK(t *testing.T) {
	if needsTranslation("◄►12") {
		t.Error("纯符号与数字既没有拉丁字母也没有中文，不该送译")
	}
	if !needsTranslation("Victory.") {
		t.Error("英文句子必须送译")
	}
	if needsTranslation("MAJOR ORDER WON 士兵们") {
		t.Error("中英混排由本包更严的过滤挡掉（标题本来就是中文），不该送译")
	}
}

// TestRunDispatchBodyOnlyTitleKeepsCardSane 端到端校验「正文剥完与标题重复的首行后为空」的退化：
// 卡片照常出图，标题仍然可读，说明栏为空（而不是画出空行或崩掉）。
func TestRunDispatchBodyOnlyTitleKeepsCardSane(t *testing.T) {
	trans := &plugtest.Translator{Out: []string{"新的重大命令"}}
	h := newHarness(t, trans)
	h.run(t)
	h.addDispatch(1052, "<i=3>NEW MAJOR ORDER</i>")
	h.run(t)

	if len(h.renderer.Cards) != 1 {
		t.Fatalf("应渲染 1 张卡片，实际 %d", len(h.renderer.Cards))
	}
	card := h.renderer.Cards[0].Data.(Card)
	item := card.Sections[0].Items[0]
	if item.Title != "新的重大命令" {
		t.Fatalf("标题应显示译文，实际 %q", item.Title)
	}
	if strings.TrimSpace(item.Detail) != "" {
		t.Fatalf("正文已与标题重复并被剥掉，说明栏应为空，实际 %q", item.Detail)
	}
}

// pagingRenderer 模拟「一张图装不下」：按卡片里的条目数算高度，超过安全线就回 TooTallError，
// 条目少到装得下就成功。用真浏览器验证分页要几十秒且依赖 Chromium，这里只需要分页器的行为。
type pagingRenderer struct {
	itemHeight int    // 每个条目占的高度（px）
	pages      []Card // 每次渲染拿到的推送卡片（card.Data）
}

func (r *pagingRenderer) Render(_ context.Context, card render.Card) ([]byte, error) {
	data, ok := card.Data.(Card)
	if !ok {
		return nil, errors.New("卡片数据不是 push.Card")
	}
	r.pages = append(r.pages, data)
	height := 0
	for _, s := range data.Sections {
		height += len(s.Items) * r.itemHeight
	}
	if height > render.MaxCardHeightPx {
		return nil, render.TooTallError(height)
	}
	return []byte("png"), nil
}

func (r *pagingRenderer) Close() error { return nil }

// TestSendPaginatesWhenCardTooTall 是推送分页的主用例：一轮里有 4 条变化、一张图装不下时，
// 应当分两页发两张图（而不是直接退文本），每页都带页码说明、内容一条不截。
func TestSendPaginatesWhenCardTooTall(t *testing.T) {
	r := &pagingRenderer{itemHeight: 3000}
	h := newHarnessWith(t, Config{ChatID: testChatID, MaxItems: 5}, &plugtest.Translator{}, r)
	h.run(t) // 第一轮只写基线

	for i := 0; i < 4; i++ {
		h.addDispatch(int64(1049+i), dispatchMessage)
	}
	h.run(t)

	if len(h.sender.Photos) != 2 {
		t.Fatalf("4 条 × 3000px 一张图装不下，应分两页发两张图，实际 %d 张、文本 %d 条",
			len(h.sender.Photos), len(h.sender.Replies))
	}
	if len(h.sender.Replies) != 0 {
		t.Fatalf("分页成功时不该再发文本：%v", h.sender.Replies)
	}
	// 前两次渲染是「整卡」尝试（第 1 次超标、第 2 次是第 1 页），后两次是两页的正文。
	pages := r.pages[len(r.pages)-2:]
	for i, card := range pages {
		want := render.PageLabel(i+1, 2)
		if !strings.Contains(card.Note, want) {
			t.Fatalf("第 %d 页的说明里应有 %q，实际注意=%q", i+1, want, card.Note)
		}
		if got := len(card.Sections[0].Items); got != 2 {
			t.Fatalf("第 %d 页应装 2 条，实际 %d 条", i+1, got)
		}
	}
}

// TestSendFallsBackToTextWhenPaginationExhausted 说明分页是「尽力」而不是「保证」：
// 三条各占一页都装不下时，回到完整文本兜底（内容一字不少），不往群里刷图。
func TestSendFallsBackToTextWhenPaginationExhausted(t *testing.T) {
	r := &pagingRenderer{itemHeight: render.MaxCardHeightPx + 1}
	h := newHarnessWith(t, Config{ChatID: testChatID, MaxItems: 5}, &plugtest.Translator{}, r)
	h.run(t)

	h.addDispatch(1049, dispatchMessage)
	h.run(t)

	if len(h.sender.Photos) != 0 {
		t.Fatalf("连分页都装不下时不该发图，实际 %d 张", len(h.sender.Photos))
	}
	if len(h.sender.Replies) != 1 {
		t.Fatalf("应回退一条完整文本，实际 %d 条", len(h.sender.Replies))
	}
	if !strings.Contains(h.sender.Replies[0], "重大指令与新简报") {
		t.Fatalf("回退文本应带上事件内容，实际 %q", h.sender.Replies[0])
	}
}
