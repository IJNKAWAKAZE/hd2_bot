// 验证流程的用例：两条模式的主干路径、超时、广告词直拦、管理员按钮、以及各种「不该动手」的情形。
//
// 全部用假实现：Telegram 动作只记录调用，渲染器返回固定字节，超时用假调度器手动触发。
package gatekeeper

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/ijnkawakaze/telegram-bot-api"

	"hd2_bot/src/arsenal"
	"hd2_bot/src/render"
)

// ---- 假实现 ----

type sentMessage struct {
	ChatID    int64
	MessageID int64
	Text      string
	Caption   string
	Buttons   int
	// Rows 记下每一行按钮的文案（空位记成 ""），用来断言九宫格的摆位。
	Rows [][]string
}

// buttonRows 把键盘转成「每行按钮文案」的形状，空位统一记成 ""。
func buttonRows(markup tgbotapi.InlineKeyboardMarkup) [][]string {
	rows := make([][]string, 0, len(markup.InlineKeyboard))
	for _, row := range markup.InlineKeyboard {
		labels := make([]string, 0, len(row))
		for _, button := range row {
			switch button.Text {
			case blankButtonText, "", " ":
				labels = append(labels, "")
			default:
				labels = append(labels, button.Text)
			}
		}
		rows = append(rows, labels)
	}
	return rows
}

type callbackAnswer struct {
	QueryID string
	Text    string
	Alert   bool
}

type fakeTelegram struct {
	targetChat int64
	bios       map[int64]string
	admins     map[int64]bool
	rights     *RightsForTest

	restricted   []int64
	unrestricted []int64
	banned       []int64
	unbanned     []int64
	approved     []int64
	declined     []int64
	photos       []sentMessage
	prompts      []sentMessage // 验证提示（带按钮）
	texts        []sentMessage // 普通文本（欢迎、审计）
	temps        []sentMessage
	tempDelay    time.Duration
	edits        []sentMessage
	deleted      [][2]int64
	answers      []callbackAnswer
	nextID       int64
}

// RightsForTest 只是把 bot.Rights 的字段搬过来，避免测试文件依赖 bot 包的具体类型。
type RightsForTest struct {
	CanRestrict bool
	CanDelete   bool
}

func newFakeTelegram(targetChat int64) *fakeTelegram {
	return &fakeTelegram{
		targetChat: targetChat,
		bios:       map[int64]string{},
		admins:     map[int64]bool{},
		nextID:     1000,
	}
}

func (f *fakeTelegram) takeID() int64 {
	f.nextID++
	return f.nextID
}

func (f *fakeTelegram) RestrictChatMember(chatID, userID int64) error {
	f.restricted = append(f.restricted, userID)
	return nil
}

func (f *fakeTelegram) UnrestrictChatMember(chatID, userID int64) error {
	f.unrestricted = append(f.unrestricted, userID)
	return nil
}

func (f *fakeTelegram) BanMember(chatID, userID int64) error {
	f.banned = append(f.banned, userID)
	return nil
}

func (f *fakeTelegram) UnbanMember(chatID, userID int64) error {
	f.unbanned = append(f.unbanned, userID)
	return nil
}

func (f *fakeTelegram) ApproveJoinRequest(chatID, userID int64) error {
	f.approved = append(f.approved, userID)
	return nil
}

func (f *fakeTelegram) DeclineJoinRequest(chatID, userID int64) error {
	f.declined = append(f.declined, userID)
	return nil
}

func (f *fakeTelegram) MemberIsAdmin(chatID, userID int64) (bool, error) {
	return f.admins[userID], nil
}

func (f *fakeTelegram) UserBio(userID int64) string { return f.bios[userID] }

// SendPhotoWithKeyboardID 记一条「图 + 说明 + 按钮」的验证消息（验证消息的正常形态）。
func (f *fakeTelegram) SendPhotoWithKeyboardID(chatID int64, png []byte, caption string,
	markup tgbotapi.InlineKeyboardMarkup) (int64, error) {
	id := f.takeID()
	f.photos = append(f.photos, sentMessage{ChatID: chatID, MessageID: id, Caption: caption, Buttons: len(markup.InlineKeyboard), Rows: buttonRows(markup)})
	return id, nil
}

func (f *fakeTelegram) SendTextWithKeyboardID(chatID int64, text string,
	markup tgbotapi.InlineKeyboardMarkup) (int64, error) {
	id := f.takeID()
	f.prompts = append(f.prompts, sentMessage{ChatID: chatID, MessageID: id, Text: text, Buttons: len(markup.InlineKeyboard), Rows: buttonRows(markup)})
	return id, nil
}

func (f *fakeTelegram) SendText(chatID int64, text string) error {
	f.texts = append(f.texts, sentMessage{ChatID: chatID, Text: text})
	return nil
}

func (f *fakeTelegram) SendTemporaryText(chatID int64, text string, delay time.Duration) error {
	f.temps = append(f.temps, sentMessage{ChatID: chatID, Text: text})
	f.tempDelay = delay
	return nil
}

func (f *fakeTelegram) EditText(chatID, messageID int64, text string,
	markup tgbotapi.InlineKeyboardMarkup) error {
	// 记下按钮行数：刷新进度时键盘必须跟着传回去，否则 Telegram 会把按钮删掉。
	f.edits = append(f.edits, sentMessage{ChatID: chatID, MessageID: messageID, Text: text, Buttons: len(markup.InlineKeyboard), Rows: buttonRows(markup)})
	return nil
}

// EditCaption 与 EditText 同源：图形态下刷新进度走这条（记录到同一个 edits 列表里）。
func (f *fakeTelegram) EditCaption(chatID, messageID int64, caption string,
	markup tgbotapi.InlineKeyboardMarkup) error {
	f.edits = append(f.edits, sentMessage{ChatID: chatID, MessageID: messageID, Caption: caption, Buttons: len(markup.InlineKeyboard), Rows: buttonRows(markup)})
	return nil
}

func (f *fakeTelegram) DeleteMessage(chatID, messageID int64) error {
	if messageID != 0 {
		f.deleted = append(f.deleted, [2]int64{chatID, messageID})
	}
	return nil
}

func (f *fakeTelegram) AnswerCallback(queryID, text string, alert bool) {
	f.answers = append(f.answers, callbackAnswer{QueryID: queryID, Text: text, Alert: alert})
}

func (f *fakeTelegram) IsTargetChat(chatID int64) bool {
	return f.targetChat == 0 || chatID == f.targetChat
}

func (f *fakeTelegram) TargetChatID() int64 { return f.targetChat }

// fakeScheduler 收集延时任务，测试里手动触发。
type fakeScheduler struct {
	delay time.Duration
	tasks []func()
}

func (s *fakeScheduler) schedule(delay time.Duration, fn func()) {
	s.delay = delay
	s.tasks = append(s.tasks, fn)
}

func (s *fakeScheduler) run() {
	tasks := s.tasks
	s.tasks = nil
	for _, fn := range tasks {
		fn()
	}
}

// fakeRenderer 返回固定字节或固定错误。
type fakeRenderer struct {
	png []byte
	err error
}

func (f fakeRenderer) Render(context.Context, render.Card) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.png, nil
}

// fakeImages 是题面图的数据源：返回固定字节（或固定错误），并记下被请求过的装备。
type fakeImages struct {
	body []byte
	err  error
	asks []string
}

func (f *fakeImages) Image(_ context.Context, item arsenal.Item) ([]byte, error) {
	f.asks = append(f.asks, item.ID)
	if f.err != nil {
		return nil, f.err
	}
	if len(f.body) == 0 {
		return nil, fmt.Errorf("没有配图")
	}
	return f.body, nil
}

// fakeRegistrar 记录注册了哪些处理器。
type fakeRegistrar struct {
	members   int
	lefts     int
	requests  int
	callbacks int
	prefix    string
}

func (f *fakeRegistrar) RegisterMemberWatcher(func(tgbotapi.Update) error)     { f.members++ }
func (f *fakeRegistrar) RegisterLeftMemberWatcher(func(tgbotapi.Update) error) { f.lefts++ }
func (f *fakeRegistrar) RegisterJoinRequests(func(tgbotapi.Update) error)      { f.requests++ }
func (f *fakeRegistrar) RegisterCallback(prefix string, _ func(tgbotapi.Update) error) {
	f.callbacks++
	f.prefix = prefix
}

// ---- 测试脚手架 ----

type harness struct {
	telegram  *fakeTelegram
	scheduler *fakeScheduler
	handler   *Handler
	images    *fakeImages
	logs      []string
}

// testImageBytes 是最小的「认得出格式」的图：题面图链路的用例不需要真图
// （渲染器是假的，只断言这条链路被走过）。
func testImageBytes() []byte {
	return []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
}

func newHarness(t *testing.T, cfg Config, renderer Renderer) *harness {
	t.Helper()
	if cfg.Mode == "" {
		cfg.Mode = ModeDirect
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 3 * time.Minute
	}
	if cfg.Failure == "" {
		cfg.Failure = FailureKick
	}
	h := &harness{telegram: newFakeTelegram(100), scheduler: &fakeScheduler{}, images: &fakeImages{body: testImageBytes()}}
	h.handler = New(cfg, Deps{
		Telegram: h.telegram,
		Catalog:  newTestCatalog(t),
		Renderer: renderer,
		Images:   h.images,
		Logger:   func(format string, args ...any) { h.logs = append(h.logs, fmt.Sprintf(format, args...)) },
		Schedule: h.scheduler.schedule,
		Rand:     rand.New(rand.NewSource(7)),
	})
	return h
}

func (h *harness) session(t *testing.T, chatID, userID int64) *Session {
	t.Helper()
	session, ok := h.handler.sessions.get(chatID, userID)
	if !ok {
		t.Fatalf("应当有进行中的验证：chat=%d user=%d", chatID, userID)
	}
	return session
}

func (h *harness) click(t *testing.T, chatID, messageID int64, from tgbotapi.User, action string) {
	t.Helper()
	h.clickAs(t, chatID, messageID, from.ID, from, action)
}

// clickAs 模拟「owner 的验证卡片被 clicker 点击」：按钮 data 里带的始终是被验证者的 id。
func (h *harness) clickAs(t *testing.T, chatID, messageID, ownerID int64, clicker tgbotapi.User, action string) {
	t.Helper()
	update := tgbotapi.Update{CallbackQuery: &tgbotapi.CallbackQuery{
		ID:      "cb",
		From:    &clicker,
		Data:    fmt.Sprintf("%s,%d,%s", callbackPrefix, ownerID, action),
		Message: &tgbotapi.Message{MessageID: messageID, Chat: &tgbotapi.Chat{ID: chatID}},
	}}
	if err := h.handler.OnCallback(update); err != nil {
		t.Fatalf("处理按钮点击失败：%v", err)
	}
}

func newMemberUpdate(chatID int64, member tgbotapi.User, inviter *tgbotapi.User) tgbotapi.Update {
	return tgbotapi.Update{Message: &tgbotapi.Message{
		MessageID:      55,
		Chat:           &tgbotapi.Chat{ID: chatID},
		From:           inviter,
		NewChatMembers: []tgbotapi.User{member},
	}}
}

func joinRequestUpdate(chatID int64, user tgbotapi.User, bio string) tgbotapi.Update {
	// 默认私聊标识就是用户 id：大多数申请的 user_chat_id 与用户 id 相同。
	return joinRequestUpdatePM(chatID, user, user.ID, bio)
}

// joinRequestUpdatePM 造一条带独立 user_chat_id 的入群申请：
// Telegram 规定机器人要用这个标识联系申请人（申请人多半从没主动开过机器人的私聊）。
func joinRequestUpdatePM(chatID int64, user tgbotapi.User, userChatID int64, bio string) tgbotapi.Update {
	return tgbotapi.Update{ChatJoinRequest: &tgbotapi.ChatJoinRequest{
		Chat:       tgbotapi.Chat{ID: chatID},
		From:       user,
		UserChatID: userChatID,
		Bio:        bio,
	}}
}

func (h *harness) logsContain(fragment string) bool {
	return strings.Contains(strings.Join(h.logs, "\n"), fragment)
}

// ---- 用例 ----

// TestRegisterHooksAllProcessors 校验三类更新都注册上了，回调前缀正确。
func TestRegisterHooksAllProcessors(t *testing.T) {
	h := newHarness(t, Config{}, fakeRenderer{png: []byte("png")})
	registrar := &fakeRegistrar{}
	h.handler.Register(registrar)
	if registrar.members != 1 || registrar.requests != 1 || registrar.callbacks != 1 {
		t.Fatalf("三类处理器都应注册：%+v", registrar)
	}
	if registrar.prefix != callbackPrefix {
		t.Errorf("回调前缀应为 %q，实际 %q", callbackPrefix, registrar.prefix)
	}
}

// TestGroupVerificationPasses 校验群内验证的正路：禁言 → 发一条「题面图 + 说明 + 按钮」→
// 点完指令 → 解除禁言并清理消息。
func TestGroupVerificationPasses(t *testing.T) {
	h := newHarness(t, Config{Mode: ModeDirect, ShowAnswer: true, Attempts: 1}, fakeRenderer{png: []byte("png")})
	member := tgbotapi.User{ID: 7, FirstName: "新兵"}

	if err := h.handler.OnNewMembers(newMemberUpdate(100, member, nil)); err != nil {
		t.Fatalf("处理新成员失败：%v", err)
	}
	if len(h.telegram.restricted) != 1 {
		t.Fatalf("新成员应先被禁言，实际 %v", h.telegram.restricted)
	}
	// 一条消息搞定：题面图 + 提示（说明）+ 按钮，不再分两条发。
	if len(h.telegram.photos) != 1 {
		t.Fatalf("应发一条带说明的题面消息，实际 %d 条", len(h.telegram.photos))
	}
	if len(h.telegram.prompts) != 0 {
		t.Fatalf("合并成一条后不该再有单独的提示文本，实际 %d 条", len(h.telegram.prompts))
	}
	prompt := h.telegram.photos[0]
	if prompt.ChatID != 100 || prompt.Buttons != 4 {
		t.Errorf("题面消息应发在目标群并带四行按钮（九宫格 + 管理员）：%+v", prompt)
	}
	if !strings.Contains(prompt.Caption, "进度") || !strings.Contains(prompt.Caption, "剩余机会") {
		t.Errorf("说明里应有进度与剩余机会：%q", prompt.Caption)
	}
	if strings.Contains(prompt.Caption, "召唤指令：") {
		// 图在，答案就印在图里；说明里不该再重复一遍答案。
		t.Errorf("有题面图时不该在说明里重复答案：%q", prompt.Caption)
	}

	session := h.session(t, 100, member.ID)
	if !session.HasPhoto {
		t.Error("会话应记住这条验证消息是「图 + 说明」形态")
	}
	if len(h.images.asks) != 1 || h.images.asks[0] != session.Challenge.Target().ID {
		t.Errorf("题面图应取自这道题的战备，实际 %v", h.images.asks)
	}
	for _, dir := range session.Challenge.Target().Steps {
		h.click(t, 100, session.MessageID, member, dir)
	}

	if len(h.telegram.unrestricted) != 1 {
		t.Fatalf("通过后应解除禁言，实际 %v", h.telegram.unrestricted)
	}
	if len(h.telegram.banned) != 0 {
		t.Errorf("通过不该踢人：%v", h.telegram.banned)
	}
	if _, ok := h.handler.sessions.get(100, member.ID); ok {
		t.Error("通过后会话应被清理")
	}
	// 验证消息与入群系统消息两条都要删掉。
	if len(h.telegram.deleted) < 2 {
		t.Errorf("应删掉验证消息与入群系统消息，实际 %v", h.telegram.deleted)
	}
	if !h.logsContain("验证通过") {
		t.Errorf("审计里应有「验证通过」：%v", h.logs)
	}
}

// TestGroupVerificationKicksAfterWrongClicks 校验答错用光机会后踢出：
// 踢出 = 先封禁，隔 unban_delay 再解封（这里配 60 秒，用假调度器把时钟推过去）。
func TestGroupVerificationKicksAfterWrongClicks(t *testing.T) {
	h := newHarness(t, Config{Mode: ModeDirect, Attempts: 1, Failure: FailureKick, UnbanDelay: time.Minute}, fakeRenderer{png: []byte("png")})
	member := tgbotapi.User{ID: 8, FirstName: "手滑"}
	if err := h.handler.OnNewMembers(newMemberUpdate(100, member, nil)); err != nil {
		t.Fatal(err)
	}
	session := h.session(t, 100, member.ID)
	wrong := wrongDir(session.Challenge.Target().Steps[0])

	h.click(t, 100, session.MessageID, member, wrong)
	if len(h.telegram.banned) != 0 {
		t.Fatal("第一次答错还有机会，不该踢人")
	}
	if len(h.telegram.edits) == 0 {
		t.Error("答错后进度应被刷新（清零）")
	}
	h.click(t, 100, session.MessageID, member, wrong)

	if len(h.telegram.banned) != 1 {
		t.Fatalf("第二次答错应踢出，实际 %v", h.telegram.banned)
	}
	if len(h.telegram.unbanned) != 0 {
		t.Fatalf("踢出后不该立刻解封（要隔 unban_delay）：%v", h.telegram.unbanned)
	}
	if len(h.telegram.restricted) != 1 {
		t.Errorf("踢出前不该有其它限制动作：%v", h.telegram.restricted)
	}
	if !h.logsContain("踢出") {
		t.Errorf("审计里应有踢出记录：%v", h.logs)
	}

	// 冷却时间到 → 解封（假调度器手动触发）。
	h.scheduler.run()
	if len(h.telegram.unbanned) != 1 {
		t.Fatalf("到点应解封，实际 %v", h.telegram.unbanned)
	}
}

// TestGroupVerificationBansWhenConfigured 校验 failure=ban 时走封禁：封禁并且**不解封**。
func TestGroupVerificationBansWhenConfigured(t *testing.T) {
	h := newHarness(t, Config{Mode: ModeDirect, Attempts: 0, Failure: FailureBan}, fakeRenderer{png: []byte("png")})
	member := tgbotapi.User{ID: 9, FirstName: "广告"}
	if err := h.handler.OnNewMembers(newMemberUpdate(100, member, nil)); err != nil {
		t.Fatal(err)
	}
	session := h.session(t, 100, member.ID)
	h.click(t, 100, session.MessageID, member, wrongDir(session.Challenge.Target().Steps[0]))

	if len(h.telegram.banned) != 1 {
		t.Fatalf("failure=ban 时应封禁：%v", h.telegram.banned)
	}
	h.scheduler.run()
	if len(h.telegram.unbanned) != 0 {
		t.Fatalf("封禁不该自动解封：%v", h.telegram.unbanned)
	}
}

// TestGroupVerificationTimesOut 校验超时结算走失败分支。
func TestGroupVerificationTimesOut(t *testing.T) {
	h := newHarness(t, Config{Mode: ModeDirect, Timeout: 2 * time.Minute}, fakeRenderer{png: []byte("png")})
	member := tgbotapi.User{ID: 10, FirstName: "潜水员"}
	if err := h.handler.OnNewMembers(newMemberUpdate(100, member, nil)); err != nil {
		t.Fatal(err)
	}
	if h.scheduler.delay != 2*time.Minute {
		t.Errorf("超时应按配置安排：%s", h.scheduler.delay)
	}
	h.scheduler.run()
	if len(h.telegram.banned) != 1 {
		t.Fatalf("超时应踢出，实际 %v", h.telegram.banned)
	}
	if !h.logsContain("没有完成验证") {
		t.Errorf("审计应说明是超时：%v", h.logs)
	}
}

// TestApprovalVerificationApproves 校验申请模式：私聊发卡片，点对后批准入群。
func TestApprovalVerificationApproves(t *testing.T) {
	h := newHarness(t, Config{Mode: ModeApproval, ShowAnswer: true}, fakeRenderer{png: []byte("png")})
	user := tgbotapi.User{ID: 11, FirstName: "申请人"}

	if err := h.handler.OnJoinRequest(joinRequestUpdate(100, user, "喜欢打虫子")); err != nil {
		t.Fatal(err)
	}
	if len(h.telegram.photos) != 1 {
		t.Fatalf("应私聊发一条「图 + 说明」验证消息，实际 %d 条", len(h.telegram.photos))
	}
	if got := h.telegram.photos[0].ChatID; got != user.ID {
		t.Errorf("申请模式的验证消息应发在用户私聊：%+v", h.telegram.photos[0])
	}
	if len(h.telegram.prompts) != 0 {
		t.Fatalf("合并成一条后不该再有单独的提示文本：%+v", h.telegram.prompts)
	}
	prompt := h.telegram.photos[0]
	if prompt.ChatID != user.ID || prompt.Buttons != 3 {
		t.Errorf("私聊验证只该有九宫格三行，不该有管理员按钮：%+v", prompt)
	}
	if len(h.telegram.restricted) != 0 {
		t.Error("申请模式不该对申请人做禁言（他还不在群里）")
	}

	session := h.session(t, 100, user.ID)
	for _, dir := range session.Challenge.Target().Steps {
		h.click(t, user.ID, session.MessageID, user, dir)
	}
	if len(h.telegram.approved) != 1 {
		t.Fatalf("通过后应批准入群，实际 %v", h.telegram.approved)
	}
}

// TestApprovalVerificationDeclinesOnFailure 校验申请模式答错后用光机会即拒绝申请。
func TestApprovalVerificationDeclinesOnFailure(t *testing.T) {
	h := newHarness(t, Config{Mode: ModeApproval, Attempts: 0}, fakeRenderer{png: []byte("png")})
	user := tgbotapi.User{ID: 12, FirstName: "机器人"}
	if err := h.handler.OnJoinRequest(joinRequestUpdate(100, user, "")); err != nil {
		t.Fatal(err)
	}
	session := h.session(t, 100, user.ID)
	h.click(t, user.ID, session.MessageID, user, wrongDir(session.Challenge.Target().Steps[0]))

	if len(h.telegram.declined) != 1 {
		t.Fatalf("答错应拒绝入群申请，实际 %v", h.telegram.declined)
	}
	if !h.logsContain("拒绝申请") {
		t.Errorf("审计里应有拒绝记录：%v", h.logs)
	}
}

// TestAdWordBlocksBeforeChallenge 校验命中广告词时直接拦下，不出题、不占群里的版面。
func TestAdWordBlocksBeforeChallenge(t *testing.T) {
	h := newHarness(t, Config{Mode: ModeDirect}, fakeRenderer{png: []byte("png")})
	member := tgbotapi.User{ID: 13, FirstName: "刷单返佣日结"}
	if err := h.handler.OnNewMembers(newMemberUpdate(100, member, nil)); err != nil {
		t.Fatal(err)
	}
	if len(h.telegram.banned) != 1 {
		t.Fatalf("广告号应直接踢出，实际 %v", h.telegram.banned)
	}
	if len(h.telegram.photos) != 0 || len(h.telegram.restricted) != 0 {
		t.Error("广告号不该走到禁言与出题这一步")
	}
	if !h.logsContain("广告词") {
		t.Errorf("审计应写明命中广告词：%v", h.logs)
	}

	// 简介命中同理（昵称正常、bio 带联系方式）。
	second := newHarness(t, Config{Mode: ModeDirect}, fakeRenderer{png: []byte("png")})
	second.telegram.bios[14] = "联系 tg：@spam"
	clean := tgbotapi.User{ID: 14, FirstName: "普通昵称"}
	if err := second.handler.OnNewMembers(newMemberUpdate(100, clean, nil)); err != nil {
		t.Fatal(err)
	}
	if len(second.telegram.banned) != 1 {
		t.Fatalf("简介命中广告词也应踢出：%v", second.telegram.banned)
	}
}

// TestInvitedMemberSkipsWhenConfigured 校验 skip_invited=true 时被拉进群的人免验证。
func TestInvitedMemberSkipsWhenConfigured(t *testing.T) {
	h := newHarness(t, Config{Mode: ModeDirect, SkipInvited: true}, fakeRenderer{png: []byte("png")})
	inviter := &tgbotapi.User{ID: 99, FirstName: "老潜兵"}
	member := tgbotapi.User{ID: 15, FirstName: "朋友"}
	if err := h.handler.OnNewMembers(newMemberUpdate(100, member, inviter)); err != nil {
		t.Fatal(err)
	}
	if len(h.telegram.restricted) != 0 || len(h.telegram.photos) != 0 {
		t.Fatal("被邀请入群的人按配置应免验证")
	}
	if !h.logsContain("邀请入群") {
		t.Errorf("审计应说明是邀请入群：%v", h.logs)
	}

	// 关掉 skip_invited 时，被邀请的人也要验证。
	strict := newHarness(t, Config{Mode: ModeDirect, SkipInvited: false}, fakeRenderer{png: []byte("png")})
	if err := strict.handler.OnNewMembers(newMemberUpdate(100, member, inviter)); err != nil {
		t.Fatal(err)
	}
	if len(strict.telegram.restricted) != 1 {
		t.Fatal("skip_invited=false 时被邀请的人也要验证")
	}
}

// TestWelcomeAfterPass 校验通过后发文本欢迎：@ 到本人、带上昵称、发在群里。
func TestWelcomeAfterPass(t *testing.T) {
	h := newHarness(t, Config{Mode: ModeDirect, Welcome: true}, fakeRenderer{png: []byte("png")})
	member := tgbotapi.User{ID: 31, FirstName: "阿力"}
	if err := h.handler.OnNewMembers(newMemberUpdate(100, member, nil)); err != nil {
		t.Fatal(err)
	}
	session := h.session(t, 100, member.ID)
	for _, dir := range session.Challenge.Target().Steps {
		h.click(t, 100, session.MessageID, member, dir)
	}
	if len(h.telegram.texts) == 0 {
		t.Fatal("通过后应发一条欢迎")
	}
	welcome := h.telegram.texts[len(h.telegram.texts)-1]
	if welcome.ChatID != 100 {
		t.Errorf("欢迎应发在群里，实际 chat=%d", welcome.ChatID)
	}
	if !strings.Contains(welcome.Text, "阿力") || !strings.Contains(welcome.Text, "tg://user?id=31") {
		t.Errorf("欢迎应 @ 到本人：%q", welcome.Text)
	}
}

// TestWelcomeDisabledAndDelayed 校验欢迎开关与自动删除延迟。
func TestWelcomeDisabledAndDelayed(t *testing.T) {
	// 关掉：一条都不发。
	off := newHarness(t, Config{Mode: ModeDirect, Welcome: false}, fakeRenderer{png: []byte("png")})
	member := tgbotapi.User{ID: 32, FirstName: "阿力"}
	if err := off.handler.OnNewMembers(newMemberUpdate(100, member, nil)); err != nil {
		t.Fatal(err)
	}
	session := off.session(t, 100, member.ID)
	for _, dir := range session.Challenge.Target().Steps {
		off.click(t, 100, session.MessageID, member, dir)
	}
	if len(off.telegram.texts) != 0 || len(off.telegram.temps) != 0 {
		t.Fatalf("关掉欢迎后不该发消息：texts=%d temps=%d", len(off.telegram.texts), len(off.telegram.temps))
	}

	// 配了删除延迟：走自动清理那条路径。
	delayed := newHarness(t, Config{Mode: ModeDirect, Welcome: true, WelcomeDelDelay: 2 * time.Minute}, fakeRenderer{png: []byte("png")})
	other := tgbotapi.User{ID: 33, FirstName: "阿力"}
	if err := delayed.handler.OnNewMembers(newMemberUpdate(100, other, nil)); err != nil {
		t.Fatal(err)
	}
	session = delayed.session(t, 100, other.ID)
	for _, dir := range session.Challenge.Target().Steps {
		delayed.click(t, 100, session.MessageID, other, dir)
	}
	if len(delayed.telegram.temps) != 1 || delayed.telegram.tempDelay != 2*time.Minute {
		t.Fatalf("配了延迟的欢迎应走自动删除：%+v delay=%s", delayed.telegram.temps, delayed.telegram.tempDelay)
	}
}

// TestRenderWelcomeTemplate 校验自定义欢迎文案的占位符与转义：
// 配置里写普通文字即可，MarkdownV2 的特殊字符由代码转义，认不出的花括号当普通文字。
func TestRenderWelcomeTemplate(t *testing.T) {
	cases := []struct {
		name     string
		template string
		want     string
	}{
		{"两个占位符", "欢迎 {name}（{mention}）入伍！", `欢迎 阿力（[阿力](tg://user?id=7)）入伍！`},
		{"特殊字符被转义", "新兵 {name} - 报到!", `新兵 阿力 \- 报到\!`},
		{"认不出的占位符原样保留", "欢迎 {unknown} 与 {name}", `欢迎 \{unknown\} 与 阿力`},
		{"没有占位符", "全体都有", "全体都有"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := renderWelcomeTemplate(c.template, "[阿力](tg://user?id=7)", "阿力")
			if got != c.want {
				t.Errorf("渲染结果不对：\n实际 %q\n期望 %q", got, c.want)
			}
		})
	}
}

// TestApprovalModeWelcomesJoiner 校验申请模式下真进群时欢迎（机器人不干预审批）。
func TestApprovalModeWelcomesJoiner(t *testing.T) {
	h := newHarness(t, Config{Mode: ModeApproval, Welcome: true}, fakeRenderer{png: []byte("png")})
	member := tgbotapi.User{ID: 34, FirstName: "申请人"}
	if err := h.handler.OnNewMembers(newMemberUpdate(100, member, nil)); err != nil {
		t.Fatal(err)
	}
	if len(h.telegram.restricted) != 0 || len(h.telegram.photos) != 0 {
		t.Fatal("申请模式下不该禁言或出题")
	}
	if len(h.telegram.texts) != 1 || !strings.Contains(h.telegram.texts[0].Text, "tg://user?id=34") {
		t.Fatalf("申请模式进群应欢迎：%+v", h.telegram.texts)
	}
}

// TestInvitedMemberWelcomed 校验被邀请入群（免验证）的人也欢迎。
func TestInvitedMemberWelcomed(t *testing.T) {
	h := newHarness(t, Config{Mode: ModeDirect, SkipInvited: true, Welcome: true}, fakeRenderer{png: []byte("png")})
	inviter := &tgbotapi.User{ID: 99, FirstName: "老潜兵"}
	member := tgbotapi.User{ID: 35, FirstName: "朋友"}
	if err := h.handler.OnNewMembers(newMemberUpdate(100, member, inviter)); err != nil {
		t.Fatal(err)
	}
	if len(h.telegram.texts) != 1 || !strings.Contains(h.telegram.texts[0].Text, "tg://user?id=35") {
		t.Fatalf("被邀请入群的人也该欢迎：%+v", h.telegram.texts)
	}
}

// TestLeaveMessageCleanup 校验退群的系统消息被删掉（口径同参考项目）。
func TestLeaveMessageCleanup(t *testing.T) {
	h := newHarness(t, Config{Mode: ModeDirect}, fakeRenderer{png: []byte("png")})
	update := tgbotapi.Update{Message: &tgbotapi.Message{
		MessageID:      77,
		Chat:           &tgbotapi.Chat{ID: 100},
		LeftChatMember: &tgbotapi.User{ID: 36, FirstName: "离开的人"},
	}}
	if err := h.handler.OnLeftMember(update); err != nil {
		t.Fatal(err)
	}
	if len(h.telegram.deleted) != 1 || h.telegram.deleted[0] != [2]int64{100, 77} {
		t.Fatalf("应删掉退群系统消息：%v", h.telegram.deleted)
	}

	// 别的群不碰。
	other := newHarness(t, Config{Mode: ModeDirect}, fakeRenderer{png: []byte("png")})
	update.Message.Chat.ID = 200
	if err := other.handler.OnLeftMember(update); err != nil {
		t.Fatal(err)
	}
	if len(other.telegram.deleted) != 0 {
		t.Fatal("非目标群的退群消息不该删")
	}
}

// TestApprovalVerificationUsesUserChatID 校验申请模式的私聊消息发往入群申请里带的 user_chat_id，
// 而不是普通用户 id：Telegram 只保证机器人能用这个标识联系申请人 —— 申请人多半从没主动
// 给机器人发过消息，直接按用户 id 发会被 Telegram 拒掉，表现就是「申请了却没收到任何私聊」。
// 批准/拒绝走的仍是群 + 用户 id，那是群接口，不吃这个限制。
func TestApprovalVerificationUsesUserChatID(t *testing.T) {
	h := newHarness(t, Config{Mode: ModeApproval, Attempts: 1}, fakeRenderer{png: []byte("png")})
	applicant := tgbotapi.User{ID: 7, FirstName: "申请人"}
	if err := h.handler.OnJoinRequest(joinRequestUpdatePM(100, applicant, 70007, "")); err != nil {
		t.Fatal(err)
	}
	if len(h.telegram.photos) != 1 || h.telegram.photos[0].ChatID != 70007 {
		t.Fatalf("验证消息（图 + 说明）应发到 user_chat_id=70007：%+v", h.telegram.photos)
	}
	if len(h.telegram.prompts) != 0 {
		t.Fatalf("合并成一条后不该再有单独的提示文本：%+v", h.telegram.prompts)
	}
	if !strings.Contains(h.telegram.photos[0].Caption, "进度") {
		t.Errorf("说明里应有进度：%q", h.telegram.photos[0].Caption)
	}

	// 在私聊里点完整串指令；回调来自 user_chat_id 那个会话，按用户 id 仍要能找到验证。
	session := h.session(t, 100, applicant.ID)
	if session.MessageChat() != 70007 {
		t.Fatalf("会话应记住用 user_chat_id 联系申请人，实际 %d", session.MessageChat())
	}
	for _, dir := range session.Challenge.Target().Steps {
		h.click(t, 70007, session.MessageID, applicant, dir)
	}
	if len(h.telegram.approved) != 1 || h.telegram.approved[0] != applicant.ID {
		t.Fatalf("批准申请应按群 + 用户 id 走群接口：%v", h.telegram.approved)
	}
	removed := false
	for _, item := range h.telegram.deleted {
		if item == [2]int64{70007, session.MessageID} {
			removed = true
		}
	}
	if !removed {
		t.Errorf("私聊里的验证消息（id=%d）应从 user_chat_id=70007 删除：%v", session.MessageID, h.telegram.deleted)
	}
}

// TestJoinRequestInDirectModeIsNotSilent 校验 direct 模式收到入群申请时给出明确指引而不是无声吞掉：
// 「群里拨成了申请加入、配置却还是 direct」正是最容易出现的错配，静默跳过只会让人以为机器人卡死了。
func TestJoinRequestInDirectModeIsNotSilent(t *testing.T) {
	h := newHarness(t, Config{Mode: ModeDirect}, fakeRenderer{png: []byte("png")})
	if err := h.handler.OnJoinRequest(joinRequestUpdate(100, tgbotapi.User{ID: 60, FirstName: "x"}, "")); err != nil {
		t.Fatal(err)
	}
	if len(h.telegram.photos) != 0 || len(h.telegram.prompts) != 0 {
		t.Fatal("direct 模式不该对入群申请出题")
	}
	if !h.logsContain("verify.mode") || !h.logsContain("approval") {
		t.Errorf("应有「把 verify.mode 改成 approval」的指引日志：%v", h.logs)
	}
}

// TestForeignChatEventsAreNotSilent 校验非目标群的事件留一行日志：群 id 对不上时最容易
// 「看起来卡住了」，日志里得能看到事件来过、以及配置的目标群到底是哪个。
func TestForeignChatEventsAreNotSilent(t *testing.T) {
	h := newHarness(t, Config{Mode: ModeApproval}, fakeRenderer{png: []byte("png")})
	if err := h.handler.OnJoinRequest(joinRequestUpdate(200, tgbotapi.User{ID: 61, FirstName: "x"}, "")); err != nil {
		t.Fatal(err)
	}
	if len(h.telegram.photos) != 0 || len(h.telegram.prompts) != 0 {
		t.Fatal("非目标群的事件不该处理")
	}
	if !h.logsContain("200") || !h.logsContain("100") || !h.logsContain("bot.group_id") {
		t.Errorf("应记下事件来源群与配置的目标群：%v", h.logs)
	}
}

// TestApprovalModeWarnsLongTimeout 校验申请模式超时超过 5 分钟时启动日志会提醒：
// Telegram 只允许机器人在申请提交后的 5 分钟内联系申请人，超时设太长等于白等。
func TestApprovalModeWarnsLongTimeout(t *testing.T) {
	h := newHarness(t, Config{Mode: ModeApproval, Timeout: 6 * time.Minute}, fakeRenderer{png: []byte("png")})
	h.handler.Register(&fakeRegistrar{})
	if !h.logsContain("5 分钟") {
		t.Errorf("超时超过私聊窗口时应有提醒：%v", h.logs)
	}

	direct := newHarness(t, Config{Mode: ModeDirect, Timeout: 6 * time.Minute}, fakeRenderer{png: []byte("png")})
	direct.handler.Register(&fakeRegistrar{})
	if direct.logsContain("5 分钟") {
		t.Errorf("direct 模式没有私聊窗口这回事，不该提醒：%v", direct.logs)
	}
}

// TestUnknownModeBehavesAsDirect 校验认不出的 mode（配置残留的 auto、手拼的字符串）按 direct 兜底：
// 宁可不出题并留下「把 mode 改成 approval」的指引，也不能既不拦申请也不出题地悬着。
func TestUnknownModeBehavesAsDirect(t *testing.T) {
	h := newHarness(t, Config{Mode: "auto"}, fakeRenderer{png: []byte("png")})
	if h.handler.cfg.Mode != ModeDirect {
		t.Fatalf("认不出的 mode 应兜底成 direct，实际 %q", h.handler.cfg.Mode)
	}
	if err := h.handler.OnJoinRequest(joinRequestUpdate(100, tgbotapi.User{ID: 62, FirstName: "x"}, "")); err != nil {
		t.Fatal(err)
	}
	if len(h.telegram.photos) != 0 || len(h.telegram.prompts) != 0 {
		t.Fatal("兜底成 direct 后不该对入群申请出题")
	}
	if !h.logsContain("verify.mode") {
		t.Errorf("兜底后也应有改配置的指引日志：%v", h.logs)
	}
}

// TestApprovalModeManualApproveCancelsVerification 校验管理员抢在验证完成前手动批准时的收尾：
// 人已经进群了，那条还在走的私聊验证必须立刻作废 —— 否则它到点后会去 decline 一个已经在群里的人
// （Telegram 直接报错），群里也会看到一条莫名其妙的失败记录。
func TestApprovalModeManualApproveCancelsVerification(t *testing.T) {
	h := newHarness(t, Config{Mode: ModeApproval, Timeout: 2 * time.Minute}, fakeRenderer{png: []byte("png")})
	applicant := tgbotapi.User{ID: 50, FirstName: "申请人"}
	if err := h.handler.OnJoinRequest(joinRequestUpdate(100, applicant, "")); err != nil {
		t.Fatal(err)
	}
	session := h.session(t, 100, applicant.ID)
	pmMessageID := session.MessageID

	// 管理员手动批准：Telegram 会送来一条「新成员进群」。
	if err := h.handler.OnNewMembers(newMemberUpdate(100, applicant, nil)); err != nil {
		t.Fatal(err)
	}
	if _, pending := h.handler.sessions.get(100, applicant.ID); pending {
		t.Error("人已经进群，那条私聊验证应被作废")
	}
	if len(h.telegram.declined) != 0 {
		t.Errorf("不该拒绝申请（人已经在群里了）：%v", h.telegram.declined)
	}
	removed := false
	for _, item := range h.telegram.deleted {
		if item == [2]int64{applicant.ID, pmMessageID} {
			removed = true
		}
	}
	if !removed {
		t.Errorf("私聊里那条消息（id=%d）应被清理：%v", pmMessageID, h.telegram.deleted)
	}

	// 验证的定时器到点也不该再动作。
	h.scheduler.run()
	if len(h.telegram.declined) != 0 || h.logsContain("失败") {
		t.Errorf("作废后的定时器不该再结算：declined=%v logs=%v", h.telegram.declined, h.logs)
	}
}

// TestKeyboardIsDPadGrid 校验方向键摆成九宫格：上居中、下居中、左右各占中间一行两侧，
// 其余位置是没有功能的空位占位按钮。群内模式多一行管理员按钮，私聊模式没有。
func TestKeyboardIsDPadGrid(t *testing.T) {
	h := newHarness(t, Config{Mode: ModeDirect}, fakeRenderer{png: []byte("png")})
	member := tgbotapi.User{ID: 70, FirstName: "新兵"}
	if err := h.handler.OnNewMembers(newMemberUpdate(100, member, nil)); err != nil {
		t.Fatal(err)
	}
	grid := h.telegram.photos[0].Rows
	want := [][]string{
		{"", Emoji(DirUp), ""},
		{Emoji(DirLeft), "", Emoji(DirRight)},
		{"", Emoji(DirDown), ""},
		{"✅ 放行", "🚫 封禁"},
	}
	if len(grid) != len(want) {
		t.Fatalf("键盘应是九宫格 + 管理员行，实际 %d 行：%v", len(grid), grid)
	}
	for i := range want {
		if len(grid[i]) != len(want[i]) {
			t.Fatalf("第 %d 行按钮数不对：%v", i, grid[i])
		}
		for j := range want[i] {
			if grid[i][j] != want[i][j] {
				t.Errorf("第 %d 行第 %d 列应是 %q，实际 %q（整行：%v）", i, j, want[i][j], grid[i][j], grid[i])
			}
		}
	}

	// 私聊模式：同样三行九宫格，但没有管理员行。
	private := newHarness(t, Config{Mode: ModeApproval}, fakeRenderer{png: []byte("png")})
	applicant := tgbotapi.User{ID: 71, FirstName: "申请人"}
	if err := private.handler.OnJoinRequest(joinRequestUpdate(100, applicant, "")); err != nil {
		t.Fatal(err)
	}
	privateGrid := private.telegram.photos[0].Rows
	if len(privateGrid) != 3 {
		t.Fatalf("私聊键盘应只有九宫格三行，实际 %d 行：%v", len(privateGrid), privateGrid)
	}
	if privateGrid[1][0] != Emoji(DirLeft) || privateGrid[1][1] != "" || privateGrid[1][2] != Emoji(DirRight) {
		t.Errorf("左右键应在中间一行两侧、中间留空：%v", privateGrid)
	}
}

// TestNoopCellDoesNothing 校验点九宫格的空位什么也不发生：进度不动、不算答错、不弹提示、不结算。
func TestNoopCellDoesNothing(t *testing.T) {
	h := newHarness(t, Config{Mode: ModeDirect, Attempts: 1}, fakeRenderer{png: []byte("png")})
	member := tgbotapi.User{ID: 72, FirstName: "新兵"}
	if err := h.handler.OnNewMembers(newMemberUpdate(100, member, nil)); err != nil {
		t.Fatal(err)
	}
	session := h.session(t, 100, member.ID)
	steps := session.Challenge.Target().Steps

	h.click(t, 100, session.MessageID, member, noopAction)

	if len(session.Challenge.Progress()) != 0 || session.Challenge.Remaining() != 1 {
		t.Errorf("空位不该改变进度与机会：progress=%v remaining=%d", session.Challenge.Progress(), session.Challenge.Remaining())
	}
	if len(h.telegram.edits) != 0 {
		t.Errorf("空位不该刷新任何东西：%+v", h.telegram.edits)
	}
	if len(h.telegram.banned) != 0 || len(h.telegram.unrestricted) != 0 {
		t.Error("空位不该触发任何结算")
	}
	if len(h.telegram.answers) != 1 || h.telegram.answers[0].Text != "" || h.telegram.answers[0].Alert {
		t.Errorf("空位只需静默应答一次（不弹窗）：%+v", h.telegram.answers)
	}
	// 空位点完还能正常继续答题。
	h.click(t, 100, session.MessageID, member, steps[0])
	if len(session.Challenge.Progress()) != 1 {
		t.Errorf("空位之后应能正常答题：%v", session.Challenge.Progress())
	}
}

// TestRefreshKeepsKeyboard 盯住那个踩过的坑：刷新进度走的是 editMessageText，
// 而 Telegram 只要请求里不带 reply_markup 就会把已有键盘删掉 —— 表现是新成员点一下，
// 整排方向按钮消失，验证再也点不下去。所以每次刷新都必须把键盘原样带回去。
func TestRefreshKeepsKeyboard(t *testing.T) {
	h := newHarness(t, Config{Mode: ModeDirect, Attempts: 3}, fakeRenderer{png: []byte("png")})
	member := tgbotapi.User{ID: 40, FirstName: "新兵"}
	if err := h.handler.OnNewMembers(newMemberUpdate(100, member, nil)); err != nil {
		t.Fatal(err)
	}
	session := h.session(t, 100, member.ID)

	// 点对一步：刷新一次进度，键盘必须还在（群内模式四行：九宫格三行 + 管理员一行）。
	h.click(t, 100, session.MessageID, member, session.Challenge.Target().Steps[0])
	if len(h.telegram.edits) != 1 {
		t.Fatalf("点对一步应刷新一次进度，实际 %d 次", len(h.telegram.edits))
	}
	if got := h.telegram.edits[0].Buttons; got != 4 {
		t.Errorf("刷新后键盘行数应为 4（九宫格三行 + 管理员一行），实际 %d —— 键盘被 Telegram 删掉了", got)
	}

	// 点错也刷新，同样不能丢键盘。
	h.click(t, 100, session.MessageID, member, wrongDir(session.Challenge.Target().Steps[1]))
	last := h.telegram.edits[len(h.telegram.edits)-1]
	if last.Buttons != 4 {
		t.Errorf("答错刷新后键盘行数应为 4，实际 %d", last.Buttons)
	}

	// 申请模式（私聊）只有九宫格三行，没有管理员行。
	private := newHarness(t, Config{Mode: ModeApproval, Attempts: 3}, fakeRenderer{png: []byte("png")})
	applicant := tgbotapi.User{ID: 41, FirstName: "申请人"}
	if err := private.handler.OnJoinRequest(joinRequestUpdate(100, applicant, "")); err != nil {
		t.Fatal(err)
	}
	privateSession := private.session(t, 100, applicant.ID)
	private.click(t, applicant.ID, privateSession.MessageID, applicant, privateSession.Challenge.Target().Steps[0])
	if len(private.telegram.edits) != 1 {
		t.Fatalf("私聊验证点对一步也应刷新，实际 %d 次", len(private.telegram.edits))
	}
	if got := private.telegram.edits[0].Buttons; got != 3 {
		t.Errorf("私聊验证的键盘应只有九宫格 3 行，实际 %d", got)
	}
}

// TestModeFiltersUpdates 校验模式与更新类型的对应关系：两种模式各管各的更新，互不越界。
func TestModeFiltersUpdates(t *testing.T) {
	// approval 模式：进群的人只是被欢迎，不验证（审批在申请阶段就完成了）。
	approval := newHarness(t, Config{Mode: ModeApproval}, fakeRenderer{png: []byte("png")})
	if err := approval.handler.OnNewMembers(newMemberUpdate(100, tgbotapi.User{ID: 16, FirstName: "x"}, nil)); err != nil {
		t.Fatal(err)
	}
	if len(approval.telegram.restricted) != 0 || len(approval.telegram.photos) != 0 {
		t.Error("approval 模式不该对进群的人禁言或出题")
	}

	// direct 模式下，入群申请更新不该被处理。
	direct := newHarness(t, Config{Mode: ModeDirect}, fakeRenderer{png: []byte("png")})
	if err := direct.handler.OnJoinRequest(joinRequestUpdate(100, tgbotapi.User{ID: 17, FirstName: "y"}, "")); err != nil {
		t.Fatal(err)
	}
	if len(direct.telegram.photos) != 0 || len(direct.telegram.declined) != 0 {
		t.Error("direct 模式不该处理入群申请")
	}
}

// TestOtherChatsIgnored 校验别的会话（私聊、其它群）一概不处理。
func TestOtherChatsIgnored(t *testing.T) {
	h := newHarness(t, Config{Mode: ModeDirect}, fakeRenderer{png: []byte("png")})
	member := tgbotapi.User{ID: 18, FirstName: "路人"}
	if err := h.handler.OnNewMembers(newMemberUpdate(200, member, nil)); err != nil {
		t.Fatal(err)
	}
	if len(h.telegram.restricted) != 0 {
		t.Error("非目标群的消息不该处理")
	}
	if err := h.handler.OnJoinRequest(joinRequestUpdate(200, member, "")); err != nil {
		t.Fatal(err)
	}
	if len(h.telegram.photos) != 0 {
		t.Error("非目标群的入群申请不该处理")
	}
}

// TestCallbackRejectsOtherUsers 校验别人点不了你的验证。
func TestCallbackRejectsOtherUsers(t *testing.T) {
	h := newHarness(t, Config{Mode: ModeDirect}, fakeRenderer{png: []byte("png")})
	member := tgbotapi.User{ID: 19, FirstName: "本人"}
	if err := h.handler.OnNewMembers(newMemberUpdate(100, member, nil)); err != nil {
		t.Fatal(err)
	}
	session := h.session(t, 100, member.ID)
	other := tgbotapi.User{ID: 20, FirstName: "路人"}
	h.clickAs(t, 100, session.MessageID, member.ID, other, session.Challenge.Target().Steps[0])

	if len(h.telegram.unrestricted) != 0 || h.logsContain("验证通过") {
		t.Fatal("别人点击不该让验证通过")
	}
	if len(h.telegram.answers) == 0 || !strings.Contains(h.telegram.answers[len(h.telegram.answers)-1].Text, "不是你的验证") {
		t.Errorf("应提示这不是他的验证：%+v", h.telegram.answers)
	}
	if len(session.Challenge.Progress()) != 0 {
		t.Error("别人的点击不该改动进度")
	}
}

// TestAdminButtonsRejectStrangers 校验「放行/封禁」按钮对普通群友不生效。
func TestAdminButtonsRejectStrangers(t *testing.T) {
	h := newHarness(t, Config{Mode: ModeDirect}, fakeRenderer{png: []byte("png")})
	member := tgbotapi.User{ID: 21, FirstName: "新人"}
	if err := h.handler.OnNewMembers(newMemberUpdate(100, member, nil)); err != nil {
		t.Fatal(err)
	}
	session := h.session(t, 100, member.ID)
	stranger := tgbotapi.User{ID: 30, FirstName: "路人"}
	h.clickAs(t, 100, session.MessageID, member.ID, stranger, "pass")
	if len(h.telegram.unrestricted) != 0 {
		t.Fatal("非管理员点「放行」不该生效")
	}
	if !strings.Contains(h.telegram.answers[len(h.telegram.answers)-1].Text, "只有管理员") {
		t.Errorf("应提示只有管理员能操作：%+v", h.telegram.answers)
	}
	if _, ok := h.handler.sessions.get(100, member.ID); !ok {
		t.Fatal("被拒后会话应保留")
	}
}

// TestAdminActionRequiresAdmin 校验管理员按钮的权限校验（用被验证者自己的会话模拟管理员操作）。
func TestAdminActionRequiresAdmin(t *testing.T) {
	h := newHarness(t, Config{Mode: ModeDirect}, fakeRenderer{png: []byte("png")})
	member := tgbotapi.User{ID: 22, FirstName: "新人"}
	if err := h.handler.OnNewMembers(newMemberUpdate(100, member, nil)); err != nil {
		t.Fatal(err)
	}
	session := h.session(t, 100, member.ID)
	// 普通群友（非管理员）拿自己的会话点「放行」：应当被拒。
	h.click(t, 100, session.MessageID, member, "pass")
	if len(h.telegram.unrestricted) != 0 {
		t.Fatal("非管理员不该能放行")
	}
	if _, ok := h.handler.sessions.get(100, member.ID); !ok {
		t.Fatal("被拒后会话应保留，验证还能继续")
	}

	// 把他提升为管理员后放行生效。
	h.telegram.admins[member.ID] = true
	h.click(t, 100, session.MessageID, member, "pass")
	if len(h.telegram.unrestricted) != 1 {
		t.Fatalf("管理员放行应解除禁言：%v", h.telegram.unrestricted)
	}
	if !h.logsContain("管理员") {
		t.Errorf("审计应记录是管理员放行的：%v", h.logs)
	}
}

// TestImageOrRenderFailureStillPrompts 校验题面图出不来时退成纯文本验证消息：
// 渲染失败与图下载失败都只影响图，答案这时补在文本里，验证流程与按钮完全不受影响。
func TestImageOrRenderFailureStillPrompts(t *testing.T) {
	cases := []struct {
		name     string
		renderer Renderer
		images   *fakeImages
	}{
		{"渲染失败", fakeRenderer{err: fmt.Errorf("chromium 挂了")}, &fakeImages{body: testImageBytes()}},
		{"题面图下载失败", fakeRenderer{png: []byte("png")}, &fakeImages{err: fmt.Errorf("下载失败")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, Config{Mode: ModeDirect, ShowAnswer: true}, c.renderer)
			h.handler.images = c.images
			member := tgbotapi.User{ID: 23, FirstName: "新兵"}
			if err := h.handler.OnNewMembers(newMemberUpdate(100, member, nil)); err != nil {
				t.Fatal(err)
			}
			if len(h.telegram.photos) != 0 {
				t.Error("题面图不可用时不该发图")
			}
			if len(h.telegram.prompts) != 1 {
				t.Fatalf("纯文本验证消息照发，实际 %d 条", len(h.telegram.prompts))
			}
			if h.telegram.prompts[0].Buttons != 4 {
				t.Errorf("退成纯文本时按钮照旧四行（九宫格 + 管理员）：%+v", h.telegram.prompts[0])
			}
			text := h.telegram.prompts[0].Text
			if !strings.Contains(text, "呼叫演练") || !strings.Contains(text, "进度") {
				t.Errorf("提示应写清题目与进度：%q", text)
			}
			if !strings.Contains(text, "召唤指令：") {
				t.Errorf("没有题面图时答案应补在文本里：%q", text)
			}
			session := h.session(t, 100, member.ID)
			if session.HasPhoto {
				t.Error("没发出图时会话不该标记成「图 + 说明」形态")
			}
			// 点对第一步：这种形态走文本编辑刷新进度。
			h.click(t, 100, session.MessageID, member, session.Challenge.Target().Steps[0])
			if len(h.telegram.edits) != 1 || !strings.Contains(h.telegram.edits[0].Text, "进度") {
				t.Errorf("应用文本接口刷新进度：%+v", h.telegram.edits)
			}
		})
	}
}

// TestNoRendererStillWorks 校验完全没配渲染器时也能验证（配置里关掉出图的场景）。
func TestNoRendererStillWorks(t *testing.T) {
	h := newHarness(t, Config{Mode: ModeDirect, ShowAnswer: true}, nil)
	member := tgbotapi.User{ID: 24, FirstName: "新兵"}
	if err := h.handler.OnNewMembers(newMemberUpdate(100, member, nil)); err != nil {
		t.Fatal(err)
	}
	if len(h.telegram.prompts) != 1 || len(h.telegram.photos) != 0 {
		t.Fatalf("没有渲染器时应只发提示文本：texts=%d photos=%d", len(h.telegram.texts), len(h.telegram.photos))
	}
	if !strings.Contains(h.telegram.prompts[0].Text, "召唤指令：") {
		t.Errorf("没有题面图时答案应在文本里：%q", h.telegram.texts[0].Text)
	}
}

// TestHiddenAnswerStaysHidden 校验 show_answer=false 时答案既不在图上也不在文本里。
func TestHiddenAnswerStaysHidden(t *testing.T) {
	h := newHarness(t, Config{Mode: ModeDirect, ShowAnswer: false}, fakeRenderer{png: []byte("png")})
	member := tgbotapi.User{ID: 27, FirstName: "老潜兵"}
	if err := h.handler.OnNewMembers(newMemberUpdate(100, member, nil)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.telegram.photos[0].Caption, "召唤指令：") {
		t.Errorf("硬核模式不该把答案写进说明：%q", h.telegram.photos[0].Caption)
	}
	if len(h.telegram.prompts) != 0 {
		t.Errorf("图发出去了就不该再有纯文本兜底：%+v", h.telegram.prompts)
	}
	card := h.handler.buildCard(h.session(t, 100, member.ID).Challenge.Target())
	if card.ShowSteps || len(card.Steps) != 0 {
		t.Error("硬核模式的题面图不该印出召唤指令")
	}
}

// TestStaleCallbackAnsweredGracefully 校验过期会话上的点击得到友好提示而不是静默。
func TestStaleCallbackAnsweredGracefully(t *testing.T) {
	h := newHarness(t, Config{Mode: ModeDirect}, fakeRenderer{png: []byte("png")})
	user := tgbotapi.User{ID: 25, FirstName: "迟到者"}
	h.click(t, 100, 999, user, DirUp)
	if len(h.telegram.answers) == 0 {
		t.Fatal("应有一次回调应答")
	}
	if !strings.Contains(h.telegram.answers[0].Text, "已经结束") {
		t.Errorf("应提示验证已结束：%+v", h.telegram.answers[0])
	}
}

// TestApprovalWithoutCatalogDeclines 校验题库不可用时拒绝申请而不是把申请挂着。
func TestApprovalWithoutCatalogDeclines(t *testing.T) {
	telegram := newFakeTelegram(100)
	handler := New(Config{Mode: ModeApproval}, Deps{
		Telegram: telegram,
		Catalog:  fakeCatalog{err: fmt.Errorf("目录挂了")},
		Renderer: fakeRenderer{png: []byte("png")},
		Schedule: func(time.Duration, func()) {},
		Logger:   func(string, ...any) {},
	})
	user := tgbotapi.User{ID: 26, FirstName: "申请人"}
	if err := handler.OnJoinRequest(joinRequestUpdate(100, user, "")); err != nil {
		t.Fatal(err)
	}
	if len(telegram.declined) != 1 {
		t.Fatalf("出不了题时应拒绝申请，实际 %v", telegram.declined)
	}
}

// wrongDir 返回一个与给定方向不同的方向，用来构造「点错」。
func wrongDir(correct string) string {
	for _, dir := range []string{DirUp, DirDown, DirLeft, DirRight} {
		if dir != correct {
			return dir
		}
	}
	return DirUp
}
