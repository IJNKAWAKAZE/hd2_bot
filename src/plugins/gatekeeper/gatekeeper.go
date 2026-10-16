// Package gatekeeper 是入群验证：新成员进群前先做一次「呼叫演练」——
// 机器人出一件战备，让他在时限内按顺序点出这件战备的召唤指令（↑↓←→ 按钮）。
//
// 两种模式对应群的两种加群设置：
//
//   - direct（群里是「直接加入」）：新成员一进群就被禁言，机器人把验证卡片发在群里，
//     点对了解除禁言；超时或点错用光机会就踢出（还能重新申请）；
//   - approval（群里是「申请加入」）：机器人收到入群申请后私聊发验证卡片，
//     点对了 approveChatJoinRequest，超时或答错则 declineChatJoinRequest；
//   - auto：两种都监听，按实际收到的更新走 —— 群里拨一下加群设置不用同步改配置。
//
// 为什么用召唤指令出题：游戏里第一次呼叫战备就是把这串箭头敲进去，对玩家零解释成本；
// 全程只点按钮（中文用户不必切输入法），而且要点好几步 —— 只会在群里发广告的机器人过不来。
//
// 这个包只依赖三个外部能力：Telegram 动作、装备目录（出题用）、卡片渲染器（可缺省，缺了走纯文本）。
package gatekeeper

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/ijnkawakaze/telegram-bot-api"

	"hd2_bot/src/arsenal"
	"hd2_bot/src/core/bot"
	"hd2_bot/src/render"
)

// 模式与失败处理的取值（与 config.VerifyConfig 的取值一致）。
//
// 没有「自动」档：两种模式对应的 Telegram 动作完全不同（禁言 vs 批准入群申请），
// 靠更新类型去猜，猜错了验证会跑到错误的会话里（群里没人被禁言 / 私聊根本发不出去）。
const (
	ModeDirect   = "direct"
	ModeApproval = "approval"

	FailureKick = "kick"
	FailureBan  = "ban"
)

// cardTemplateName 是验证卡片模板名（src/render/templates/verify.tmpl）。
const cardTemplateName = "verify"

// recentTargets 是「最近出过的题」的记忆长度：连着出同一道题很没意思，记住最近几条避开。
const recentTargets = 5

// 题面图与卡片的超时：都是「拿不到就少张图」的尽力而为，不该让新成员干等。
const (
	imageTimeout  = 8 * time.Second
	renderTimeout = 20 * time.Second
)

// Telegram 是验证用到的 Telegram 能力（由 *bot.Bot 实现；测试注入假实现）。
type Telegram interface {
	RestrictChatMember(chatID, userID int64) error
	UnrestrictChatMember(chatID, userID int64) error
	BanMember(chatID, userID int64) error
	UnbanMember(chatID, userID int64) error
	ApproveJoinRequest(chatID, userID int64) error
	DeclineJoinRequest(chatID, userID int64) error
	MemberIsAdmin(chatID, userID int64) (bool, error)
	UserBio(userID int64) string

	SendPhotoID(chatID int64, png []byte, replyTo int64) (int64, error)
	SendTextWithKeyboardID(chatID int64, text string, markup tgbotapi.InlineKeyboardMarkup) (int64, error)
	SendText(chatID int64, text string) error
	// SendTemporaryText 发一条到点自动删除的文本（欢迎消息可配自动清理）。
	SendTemporaryText(chatID int64, text string, delay time.Duration) error
	EditText(chatID int64, messageID int64, text string, markup tgbotapi.InlineKeyboardMarkup) error
	DeleteMessage(chatID, messageID int64) error
	AnswerCallback(queryID, text string, alert bool)

	// IsTargetChat 说明这条更新是不是来自本机器人服务的那一个群。
	IsTargetChat(chatID int64) bool
	// TargetChatID 返回配置的目标群 id（bot.group_id），事件来自别的群时用来在日志里对账。
	TargetChatID() int64
}

// Registrar 是注册「新成员 / 退群 / 入群申请 / 按钮回调」四类更新的能力（由 *bot.Bot 实现）。
type Registrar interface {
	RegisterMemberWatcher(fn func(tgbotapi.Update) error)
	RegisterLeftMemberWatcher(fn func(tgbotapi.Update) error)
	RegisterJoinRequests(fn func(tgbotapi.Update) error)
	RegisterCallback(prefix string, fn func(tgbotapi.Update) error)
}

// Renderer 渲染验证卡片（由 render.Engine 实现）；为 nil 时整条流程走纯文本。
type Renderer interface {
	Render(ctx context.Context, card render.Card) ([]byte, error)
}

// Images 提供装备图（由 arsenal.Store 实现）：验证的题面图就是战备图标。
type Images interface {
	Image(ctx context.Context, item arsenal.Item) ([]byte, error)
}

// Config 是验证的配置（由 config.VerifyConfig 换算）。
type Config struct {
	Mode            string
	ShowAnswer      bool
	Timeout         time.Duration
	Attempts        int
	Failure         string
	SkipInvited     bool
	UnbanDelay      time.Duration // 踢出后隔多久解封；0 = 立刻
	Welcome         bool          // 通过后是否发文本欢迎
	WelcomeText     string        // 自定义欢迎文案（{mention} / {name}）；空串用内置
	WelcomeDelDelay time.Duration // 欢迎消息自动删除延迟；0 = 不删
	AdWords         []string
	AuditChannel    int64
}

// Deps 是外部依赖：Telegram 与目录必填，其余可留空（留空走默认实现）。
type Deps struct {
	Telegram Telegram
	Catalog  CatalogProvider
	Renderer Renderer
	Images   Images
	Logger   func(format string, args ...any)
	Schedule func(delay time.Duration, fn func())
	Rand     *rand.Rand
}

// Handler 是入群验证插件。
type Handler struct {
	cfg       Config
	telegram  Telegram
	catalog   CatalogProvider
	renderer  Renderer
	images    Images
	log       func(format string, args ...any)
	schedule  func(delay time.Duration, fn func())
	sessions  *sessionStore
	adWords   adWordMatcher
	targetsMu sync.Mutex
	rnd       *rand.Rand
	recent    []string
}

// New 构造验证插件；参数缺失时按默认值补齐，构造本身不发任何请求。
func New(cfg Config, deps Deps) *Handler {
	if strings.TrimSpace(cfg.Mode) == "" {
		cfg.Mode = ModeDirect
	}
	if cfg.Mode != ModeDirect && cfg.Mode != ModeApproval {
		// 认不出的 mode（配置残留的 auto、手拼的字符串）按 direct 兜底：
		// 对入群申请不出题、只留一条「把 mode 改成 approval」的指引，
		// 比悬在两种模式之间、既不拦也不出题强。配置层会在启动时把非法取值拦下来。
		cfg.Mode = ModeDirect
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 3 * time.Minute
	}
	if cfg.Attempts < 0 {
		cfg.Attempts = 0
	}
	if strings.TrimSpace(cfg.Failure) == "" {
		cfg.Failure = FailureKick
	}
	logf := deps.Logger
	if logf == nil {
		logf = func(string, ...any) {}
	}
	schedule := deps.Schedule
	if schedule == nil {
		schedule = func(delay time.Duration, fn func()) { time.AfterFunc(delay, fn) }
	}
	rnd := deps.Rand
	if rnd == nil {
		rnd = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	return &Handler{
		cfg:      cfg,
		telegram: deps.Telegram,
		catalog:  deps.Catalog,
		renderer: deps.Renderer,
		images:   deps.Images,
		log:      logf,
		schedule: schedule,
		sessions: newSessionStore(),
		adWords:  newAdWordMatcher(cfg.AdWords),
		rnd:      rnd,
	}
}

// requestChatWindow 是 Telegram 给「入群申请私聊」的窗口：申请提交后 5 分钟内
// 机器人才被允许联系申请人，过期发私聊会失败。验证超时设得比它长没有意义。
const requestChatWindow = 5 * time.Minute

// Register 把四类更新的处理器挂上机器人（新成员、退群、入群申请、按钮回调）。
func (h *Handler) Register(registrar Registrar) {
	registrar.RegisterMemberWatcher(h.OnNewMembers)
	registrar.RegisterLeftMemberWatcher(h.OnLeftMember)
	registrar.RegisterJoinRequests(h.OnJoinRequest)
	registrar.RegisterCallback(callbackPrefix, h.OnCallback)
	h.log("[gatekeeper] 入群验证已启用：模式=%s 超时=%s 容错=%d 次 失败处理=%s 踢出后解封=%s 欢迎=%v",
		h.cfg.Mode, h.cfg.Timeout, h.cfg.Attempts, h.cfg.Failure, h.unbanText(), h.cfg.Welcome)
	if h.cfg.Mode == ModeApproval && h.cfg.Timeout > requestChatWindow {
		h.log("[gatekeeper] 提醒：申请模式的私聊窗口只有 5 分钟（Telegram 规定，从申请人提交算起），"+
			"超时设到 %s 的话验证后半段可能刷不动进度、也删不掉消息；建议把 verify.timeout 压到 300 秒以内",
			h.cfg.Timeout)
	}
}

// unbanText 把解封延迟翻成人话（启动日志与审计用）。
func (h *Handler) unbanText() string {
	if h.cfg.UnbanDelay <= 0 {
		return "立即"
	}
	return h.cfg.UnbanDelay.String() + "后"
}

// OnNewMembers 处理「新成员进群」。
func (h *Handler) OnNewMembers(update tgbotapi.Update) error {
	message := update.Message
	if message == nil || len(message.NewChatMembers) == 0 {
		return nil
	}
	chatID := message.Chat.ID
	if !h.telegram.IsTargetChat(chatID) {
		h.logForeignChat("进群事件", chatID)
		return nil
	}
	// 审批模式下，能进群的都是申请已被批准的人（机器人批准的，或管理员手动批准的）：
	// 这里不再验证，只记审计并欢迎 —— 与参考项目的口径一致。
	if h.cfg.Mode == ModeApproval {
		for _, member := range message.NewChatMembers {
			if member.IsBot {
				continue
			}
			reason := "审批模式：入群申请已通过"
			// 管理员可以抢在自动验证完成前手动批准。这时那条私聊验证必须立刻作废：
			// 否则它到点后会去 decline 一个已经在群里的人（Telegram 直接报错），
			// 群里还会多出一条莫名其妙的失败记录。取走会话同时也就撤掉了它的超时定时器。
			if session, pending := h.sessions.take(chatID, member.ID); pending {
				reason = "审批模式：管理员在验证完成前手动批准，验证作废"
				h.cleanup(session)
			}
			h.audit(chatID, member.ID, member.FullName(), auditJoined, reason)
			h.sendWelcome(chatID, member.ID, member.FullName())
		}
		return nil
	}
	for _, member := range message.NewChatMembers {
		if member.IsBot {
			continue // 机器人（包括本机器人被拉进群）不验证
		}
		invited := message.From != nil && message.From.ID != member.ID
		if invited && h.cfg.SkipInvited {
			h.audit(chatID, member.ID, member.FullName(), auditInvited, "由其它成员邀请入群，按配置免验证")
			h.sendWelcome(chatID, member.ID, member.FullName())
			continue
		}
		h.startGroupVerification(chatID, member, message.MessageID)
	}
	return nil
}

// OnLeftMember 处理「成员退群」：删掉那条「XX 离开了群组」的系统消息（口径同参考项目）。
// 只动这一条系统消息，群友自己发的告别消息不碰。
func (h *Handler) OnLeftMember(update tgbotapi.Update) error {
	message := update.Message
	if message == nil || message.LeftChatMember == nil {
		return nil
	}
	if !h.telegram.IsTargetChat(message.Chat.ID) {
		h.logForeignChat("退群事件", message.Chat.ID)
		return nil
	}
	h.deleteMessage(message.Chat.ID, message.MessageID)
	return nil
}

// OnJoinRequest 处理「入群申请」。
func (h *Handler) OnJoinRequest(update tgbotapi.Update) error {
	request := update.ChatJoinRequest
	if request == nil {
		return nil
	}
	chatID := request.Chat.ID
	if !h.telegram.IsTargetChat(chatID) {
		h.logForeignChat("入群申请", chatID)
		return nil
	}
	if h.cfg.Mode == ModeDirect {
		// 不再静默吞掉：群里拨成「申请加入」而配置还是 direct，是最容易出现的错配。
		h.log("[gatekeeper] 收到群 %d 的入群申请，但 verify.mode=direct —— 群里若是「申请加入」，"+
			"请把 verify.mode 改成 approval，否则每次申请都会被忽略（本次已忽略）", chatID)
		return nil
	}
	userID := request.From.ID
	name := request.From.FullName()
	if _, exists := h.sessions.get(chatID, userID); exists {
		// Telegram 会重发同一条申请更新，已经在验证中就不再出第二遍题。
		return nil
	}
	if word, blocked := h.blockedByAdWords(name, request.From.UserName, request.Bio); blocked {
		h.audit(chatID, userID, name, auditDeclined, "昵称或简介命中广告词「"+word+"」")
		h.decline(chatID, userID, name)
		return nil
	}

	// 私聊标识用申请里带的 user_chat_id：申请人多半从没主动给机器人发过消息，
	// 直接按用户 id 发私聊会被 Telegram 拒掉 —— 这正是申请模式的验证发不出去的常见原因。
	pmChatID := request.UserChatID
	if pmChatID == 0 {
		pmChatID = userID
	}
	session, err := h.newSession(chatID, userID, name, true, 0, pmChatID)
	if err != nil {
		h.log("[gatekeeper] 出题失败：%v", err)
		h.audit(chatID, userID, name, auditDeclined, "题库不可用（装备目录里没有可用的召唤指令）")
		h.decline(chatID, userID, name)
		return nil
	}
	if err := h.sendChallenge(session); err != nil {
		h.log("[gatekeeper] 私聊发送验证卡片失败：%v", err)
		h.audit(chatID, userID, name, auditDeclined, "私聊发送验证卡片失败（申请人可能屏蔽了机器人，或已错过申请后的 5 分钟私聊窗口）")
		h.sessions.remove(chatID, userID)
		h.decline(chatID, userID, name)
		return nil
	}
	h.audit(chatID, userID, name, auditRequested, "进入私聊验证流程")
	h.scheduleTimeout(session)
	return nil
}

// logForeignChat 记一条「事件来自别的群」的日志。宁可多嘴也不静默：
// 群 id 对不上时最容易「看起来卡住了」，日志里得能看到事件来过、以及配置的目标群是哪个。
func (h *Handler) logForeignChat(kind string, eventChat int64) {
	h.log("[gatekeeper] 收到群 %d 的%s，但配置的目标群是 %d，已忽略（核对 bot.group_id）",
		eventChat, kind, h.telegram.TargetChatID())
}

// OnCallback 处理按钮点击：方向键走验证，放行/封禁走管理员操作。
func (h *Handler) OnCallback(update tgbotapi.Update) error {
	query := update.CallbackQuery
	if query == nil || query.Message == nil || query.From == nil {
		return nil
	}
	parts := strings.Split(query.Data, ",")
	if len(parts) != 3 || parts[0] != callbackPrefix {
		return nil
	}
	userID, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	if err != nil {
		return nil
	}
	action := strings.ToLower(strings.TrimSpace(parts[2]))
	chatID := query.Message.Chat.ID

	// 群内验证的回调发生在群里；私聊验证的回调发生在用户私聊里，那时按用户 id 反查会话。
	session, ok := h.sessions.get(chatID, userID)
	if !ok {
		session, ok = h.sessions.findByUser(userID)
	}
	if !ok {
		h.telegram.AnswerCallback(query.ID, "这次验证已经结束了。", true)
		return nil
	}
	// 「这不是你的验证」只挡方向键：放行/封禁是给管理员用的，点击的人本来就不是被验证者，
	// 那两个动作走的是管理员权限校验（见 handleAdminAction）。
	switch action {
	case "pass", "ban":
		return h.handleAdminAction(query, session, action)
	case DirUp, DirDown, DirLeft, DirRight:
		if query.From.ID != userID {
			h.telegram.AnswerCallback(query.ID, "这不是你的验证。", true)
			return nil
		}
		return h.handleStep(query, session, action)
	default:
		return nil
	}
}

// handleStep 处理一次方向点击。
func (h *Handler) handleStep(query *tgbotapi.CallbackQuery, session *Session, dir string) error {
	switch session.Challenge.Click(dir) {
	case StepProgress:
		h.refreshPrompt(session)
		h.telegram.AnswerCallback(query.ID, fmt.Sprintf("已输入 %d/%d", len(session.Challenge.Progress()), session.Challenge.Total()), false)
	case StepPassed:
		h.sessions.take(session.ChatID, session.UserID)
		h.pass(session, "呼叫指令输入正确")
		h.telegram.AnswerCallback(query.ID, "验证通过，欢迎加入。", true)
	case StepWrong:
		h.refreshPrompt(session)
		h.telegram.AnswerCallback(query.ID, fmt.Sprintf("点错了，进度清零；还剩 %d 次机会。", session.Challenge.Remaining()), true)
	case StepFailed:
		h.sessions.take(session.ChatID, session.UserID)
		h.fail(session, "呼叫指令连续点错")
		h.telegram.AnswerCallback(query.ID, "验证失败。", true)
	}
	return nil
}

// handleAdminAction 处理管理员在验证卡片上点「放行 / 封禁」。
func (h *Handler) handleAdminAction(query *tgbotapi.CallbackQuery, session *Session, action string) error {
	if session.Private {
		h.telegram.AnswerCallback(query.ID, "私聊验证不能人工放行。", true)
		return nil
	}
	isAdmin, err := h.telegram.MemberIsAdmin(session.ChatID, query.From.ID)
	if err != nil {
		h.log("[gatekeeper] 校验管理员身份失败：%v", err)
		h.telegram.AnswerCallback(query.ID, "查不到你的身份，稍后再试。", true)
		return nil
	}
	if !isAdmin {
		h.telegram.AnswerCallback(query.ID, "只有管理员能操作。", true)
		return nil
	}
	h.sessions.take(session.ChatID, session.UserID)
	if action == "pass" {
		h.pass(session, "管理员 "+query.From.FullName()+" 放行")
		h.telegram.AnswerCallback(query.ID, "已放行。", true)
		return nil
	}
	h.ban(session, "管理员 "+query.From.FullName()+" 封禁")
	h.telegram.AnswerCallback(query.ID, "已封禁。", true)
	return nil
}

// startGroupVerification 走群内验证：广告词直拦 → 禁言 → 出题发卡 → 排超时。
func (h *Handler) startGroupVerification(chatID int64, member tgbotapi.User, joinMessageID int64) {
	name := member.FullName()
	if word, blocked := h.blockedByAdWords(name, member.UserName, h.telegram.UserBio(member.ID)); blocked {
		h.audit(chatID, member.ID, name, auditKicked, "昵称或简介命中广告词「"+word+"」")
		h.kick(chatID, member.ID, name)
		h.deleteMessage(chatID, joinMessageID)
		return
	}
	if err := h.telegram.RestrictChatMember(chatID, member.ID); err != nil {
		h.log("[gatekeeper] 禁言失败 chat=%d user=%d err=%v", chatID, member.ID, err)
		h.audit(chatID, member.ID, name, auditSkipped, "禁言失败（机器人缺少限制成员权限？）")
		return
	}
	session, err := h.newSession(chatID, member.ID, name, false, joinMessageID, 0)
	if err != nil {
		h.log("[gatekeeper] 出题失败：%v", err)
		h.audit(chatID, member.ID, name, auditSkipped, "题库不可用（装备目录里没有可用的召唤指令）")
		h.unrestrict(chatID, member.ID, name)
		return
	}
	if err := h.sendChallenge(session); err != nil {
		h.log("[gatekeeper] 发送验证卡片失败：%v", err)
		h.audit(chatID, member.ID, name, auditSkipped, "验证卡片发送失败")
		h.sessions.remove(chatID, member.ID)
		h.unrestrict(chatID, member.ID, name)
		return
	}
	h.audit(chatID, member.ID, name, auditJoined, "进入验证流程")
	h.scheduleTimeout(session)
}

// newSession 出题并建会话；同一用户已经在验证中时返回错误。
func (h *Handler) newSession(chatID, userID int64, name string, private bool, joinMessageID, pmChatID int64) (*Session, error) {
	target, err := h.pickTarget()
	if err != nil {
		return nil, err
	}
	challenge, err := NewChallenge(target, h.cfg.Attempts)
	if err != nil {
		return nil, err
	}
	session := &Session{
		ChatID:    chatID,
		UserID:    userID,
		Name:      name,
		Private:   private,
		JoinID:    joinMessageID,
		PMChatID:  pmChatID,
		Challenge: challenge,
	}
	if !h.sessions.add(session) {
		return nil, errors.New("这个成员已经在验证里了")
	}
	return session, nil
}

// pickTarget 挑一道题，尽量避开最近出过的战备。
func (h *Handler) pickTarget() (Target, error) {
	h.targetsMu.Lock()
	defer h.targetsMu.Unlock()
	avoid := make(map[string]bool, len(h.recent))
	for _, id := range h.recent {
		avoid[id] = true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	target, err := PickTarget(ctx, h.catalog, h.rnd, avoid)
	if err != nil {
		return Target{}, err
	}
	h.recent = append(h.recent, target.ID)
	if len(h.recent) > recentTargets {
		h.recent = h.recent[len(h.recent)-recentTargets:]
	}
	return target, nil
}

// sendChallenge 发一次验证：先发题面图（战备图标 + 名字 + 召唤指令），
// 再把「提示 + 进度 + 按钮」作为单独一条文本消息发出去。
//
// 为什么分两条：图只当题面（不承担交互状态），进度、剩余机会、按钮都在文本消息上，
// 编辑文本比编辑图片说明更自由，也不会受 caption 1024 字的限制。
// 图取不到或渲染失败只影响题面，提示与按钮照发 —— 验证流程不依赖图片。
func (h *Handler) sendChallenge(session *Session) error {
	chatID := session.MessageChat()
	if png, err := h.renderChallenge(session); err != nil {
		h.log("[gatekeeper] 题面图不可用，只发提示文本：%v", err)
	} else if messageID, err := h.telegram.SendPhotoID(chatID, png, 0); err != nil {
		h.log("[gatekeeper] 题面图发送失败，只发提示文本：%v", err)
	} else {
		session.PhotoID = messageID
	}
	messageID, err := h.telegram.SendTextWithKeyboardID(chatID, h.promptText(session), h.keyboard(session))
	if err != nil {
		return err
	}
	session.MessageID = messageID
	return nil
}

// renderChallenge 出题面图：下载战备图标 → 渲染成卡片。
func (h *Handler) renderChallenge(session *Session) ([]byte, error) {
	if h.renderer == nil {
		return nil, errors.New("未配置渲染器")
	}
	target := session.Challenge.Target()
	card := h.buildCard(target)
	if h.images != nil && strings.TrimSpace(target.Item.ImagePath) != "" {
		ctx, cancel := context.WithTimeout(context.Background(), imageTimeout)
		defer cancel()
		raw, err := h.images.Image(ctx, target.Item)
		if err != nil {
			return nil, fmt.Errorf("下载战备图标失败：%w", err)
		}
		uri := render.ImageDataURI(raw)
		if uri == "" {
			return nil, fmt.Errorf("战备图标格式认不出（%s）", target.Item.ImagePath)
		}
		card.Image = uri
	}
	ctx, cancel := context.WithTimeout(context.Background(), renderTimeout)
	defer cancel()
	return h.renderer.Render(ctx, render.Card{Name: cardTemplateName, Data: card})
}

// refreshPrompt 刷新提示文本上的进度，并把键盘一起传回去。
//
// Telegram 的 editMessageText 不带 reply_markup 时会把已有键盘删掉，所以这里必须每次都把
// 方向按钮重新挂上 —— 否则点一下整排按钮就消失（第一版踩过这个坑）。
// 刷新失败只记日志：用户还能继续点，只是看不到进度。
func (h *Handler) refreshPrompt(session *Session) {
	if session.MessageID == 0 {
		return
	}
	if err := h.telegram.EditText(session.MessageChat(), session.MessageID, h.promptText(session), h.keyboard(session)); err != nil {
		h.log("[gatekeeper] 刷新验证进度失败：%v", err)
	}
}

// cleanup 删掉这次验证留下的消息：题面图、提示文本，以及群里那条入群系统消息。
func (h *Handler) cleanup(session *Session) {
	h.deleteMessage(session.MessageChat(), session.PhotoID)
	h.deleteMessage(session.MessageChat(), session.MessageID)
	h.deleteMessage(session.ChatID, session.JoinID)
}

// scheduleTimeout 安排超时结算。
func (h *Handler) scheduleTimeout(session *Session) {
	key := session.Key()
	h.schedule(h.cfg.Timeout, func() {
		timedOut, ok := h.sessions.takeByKey(key)
		if !ok {
			return // 已经通过或已经被处理掉了
		}
		h.fail(timedOut, fmt.Sprintf("%s 内没有完成验证", h.timeoutText()))
	})
}

// pass 结算「通过」：群内解除禁言并欢迎，私聊批准入群申请（欢迎等他人真进群时再发）。
func (h *Handler) pass(session *Session, reason string) {
	if session.Private {
		h.audit(session.ChatID, session.UserID, session.Name, auditPassed, reason)
		if err := h.telegram.ApproveJoinRequest(session.ChatID, session.UserID); err != nil {
			h.log("[gatekeeper] 批准入群失败 chat=%d user=%d err=%v", session.ChatID, session.UserID, err)
		}
		h.cleanup(session)
		return
	}
	h.audit(session.ChatID, session.UserID, session.Name, auditPassed, reason)
	if err := h.telegram.UnrestrictChatMember(session.ChatID, session.UserID); err != nil {
		h.log("[gatekeeper] 解除禁言失败 chat=%d user=%d err=%v（该成员仍处于禁言，需要人工处理）",
			session.ChatID, session.UserID, err)
	}
	h.cleanup(session)
	h.sendWelcome(session.ChatID, session.UserID, session.Name)
}

// sendWelcome 发文本欢迎；欢迎消息可以配自动删除（verify.welcome_del_delay）。
// 欢迎只是体验优化：发不出去只记日志，绝不影响验证本身的结算结果。
func (h *Handler) sendWelcome(chatID, userID int64, name string) {
	if !h.cfg.Welcome {
		return
	}
	text := h.welcomeText(userID, name)
	var err error
	if h.cfg.WelcomeDelDelay > 0 {
		err = h.telegram.SendTemporaryText(chatID, text, h.cfg.WelcomeDelDelay)
	} else {
		err = h.telegram.SendText(chatID, text)
	}
	if err != nil {
		h.log("[gatekeeper] 欢迎消息发送失败 chat=%d user=%d err=%v", chatID, userID, err)
	}
}

// defaultWelcomeText 是内置欢迎文案：@ 到本人，带一点游戏味。
const defaultWelcomeText = "欢迎新兵 {mention} 通过呼叫演练，正式入伍！为了民主，为了超级地球。"

// welcomeText 组欢迎文案（MarkdownV2）。
//
// 自定义文案里的 {mention} 与 {name} 是占位符，其余部分由本函数负责转义 ——
// 配置里写普通中文即可，不用担心 MarkdownV2 的特殊字符把整条消息打回去。
func (h *Handler) welcomeText(userID int64, name string) string {
	template := strings.TrimSpace(h.cfg.WelcomeText)
	if template == "" {
		template = defaultWelcomeText
	}
	mention := fmt.Sprintf("[%s](tg://user?id=%d)", bot.Escape(name), userID)
	return renderWelcomeTemplate(template, mention, bot.Escape(name))
}

// renderWelcomeTemplate 把模板里的占位符换掉，其余文本按 MarkdownV2 转义。
// 逐段处理而不是先替换再整体转义：整体转义会把占位符本身的花括号也转义掉，替换就失效了。
func renderWelcomeTemplate(template, mention, escapedName string) string {
	var out strings.Builder
	rest := template
	for len(rest) > 0 {
		index := strings.IndexByte(rest, '{')
		if index < 0 {
			out.WriteString(bot.Escape(rest))
			break
		}
		out.WriteString(bot.Escape(rest[:index]))
		switch {
		case strings.HasPrefix(rest[index:], "{mention}"):
			out.WriteString(mention)
			rest = rest[index+len("{mention}"):]
		case strings.HasPrefix(rest[index:], "{name}"):
			out.WriteString(escapedName)
			rest = rest[index+len("{name}"):]
		default:
			// 认不出的花括号当普通文字，原样转义输出一个字符，继续往下扫。
			out.WriteString(bot.Escape("{"))
			rest = rest[index+1:]
		}
	}
	return out.String()
}

// fail 结算「失败」：按配置踢出或封禁；私聊模式则拒绝入群申请。
func (h *Handler) fail(session *Session, reason string) {
	if session.Private {
		h.audit(session.ChatID, session.UserID, session.Name, auditDeclined, reason)
		h.decline(session.ChatID, session.UserID, session.Name)
		h.cleanup(session)
		return
	}
	if h.cfg.Failure == FailureBan {
		h.ban(session, reason)
		return
	}
	h.audit(session.ChatID, session.UserID, session.Name, auditKicked, reason)
	h.kick(session.ChatID, session.UserID, session.Name)
	h.cleanup(session)
}

// ban 封禁并记审计。
func (h *Handler) ban(session *Session, reason string) {
	h.audit(session.ChatID, session.UserID, session.Name, auditBanned, reason)
	h.kickOrBan(session.ChatID, session.UserID, session.Name, true)
	h.cleanup(session)
}

// kick 踢出（封禁后立刻解封，人还能重新申请）并记审计。
func (h *Handler) kick(chatID, userID int64, name string) {
	h.kickOrBan(chatID, userID, name, false)
}

// kickOrBan 执行「踢出」（封禁 + 延迟解封）或「封禁」（保持封禁）。
//
// 踢出为什么不是封禁后立刻解封：留一小段时间的封禁（verify.unban_delay，默认 60 秒）
// 能挡住「被踢了就秒回」的骚扰号，也给管理员一个看到审计后手动改判的机会。
func (h *Handler) kickOrBan(chatID, userID int64, name string, ban bool) {
	if ban {
		if err := h.telegram.BanMember(chatID, userID); err != nil {
			h.log("[gatekeeper] 封禁成员失败 chat=%d user=%d name=%q err=%v", chatID, userID, name, err)
		}
		return
	}
	if err := h.telegram.BanMember(chatID, userID); err != nil {
		h.log("[gatekeeper] 踢出成员失败 chat=%d user=%d name=%q err=%v", chatID, userID, name, err)
		return
	}
	if h.cfg.UnbanDelay <= 0 {
		h.unban(chatID, userID, name)
		return
	}
	h.schedule(h.cfg.UnbanDelay, func() { h.unban(chatID, userID, name) })
}

// unban 解封（踢出的后半步）。
func (h *Handler) unban(chatID, userID int64, name string) {
	if err := h.telegram.UnbanMember(chatID, userID); err != nil {
		h.log("[gatekeeper] 解封失败 chat=%d user=%d name=%q err=%v（该用户会被一直封禁，需要人工解封）",
			chatID, userID, name, err)
	}
}

// unrestrict 解除禁言（出题或发卡失败时的兜底：不能把成员卡在禁言里）。
func (h *Handler) unrestrict(chatID, userID int64, name string) {
	if err := h.telegram.UnrestrictChatMember(chatID, userID); err != nil {
		h.log("[gatekeeper] 兜底解除禁言失败 chat=%d user=%d name=%q err=%v（需要人工处理）", chatID, userID, name, err)
	}
}

// decline 拒绝入群申请。
func (h *Handler) decline(chatID, userID int64, name string) {
	if err := h.telegram.DeclineJoinRequest(chatID, userID); err != nil {
		h.log("[gatekeeper] 拒绝入群申请失败 chat=%d user=%d name=%q err=%v", chatID, userID, name, err)
	}
}

// deleteMessage 删一条消息（失败只记日志：消息太旧、已被删都会被 Telegram 拒绝）。
func (h *Handler) deleteMessage(chatID, messageID int64) {
	if messageID == 0 {
		return
	}
	if err := h.telegram.DeleteMessage(chatID, messageID); err != nil {
		h.log("[gatekeeper] 删除消息失败 chat=%d message=%d err=%v", chatID, messageID, err)
	}
}

// blockedByAdWords 检查昵称、用户名与简介。
func (h *Handler) blockedByAdWords(name, username, bio string) (string, bool) {
	texts := []string{name}
	if username != "" {
		texts = append(texts, "@"+username, username)
	}
	if bio != "" {
		texts = append(texts, bio)
	}
	return h.adWords.match(texts...)
}

// Pending 返回进行中的验证数量（启动日志与测试用）。
func (h *Handler) Pending() int { return h.sessions.len() }
