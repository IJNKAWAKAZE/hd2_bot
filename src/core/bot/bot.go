package bot

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	tgbotapi "github.com/ijnkawakaze/telegram-bot-api"
)

// maxMessageLen 是单条 Telegram 文本消息的长度上限。
// Telegram 按 UTF-16 码元计数，表情等非 BMP 字符算 2 个，所以分片也按码元算。
const maxMessageLen = 4096

// Handler 描述一个指令处理器。
type Handler struct {
	Name string // 指令名，不含斜杠
	Run  func(tgbotapi.Update) error
}

// Sender 是插件回消息所需的最小接口；*Bot 实现了它，测试可以注入假实现。
// 插件只能通过它发消息，不允许直接接触 Telegram 客户端。
type Sender interface {
	// Reply 以 MarkdownV2 回复文本，超长自动分片；replyTo 为 0 时不引用消息。
	Reply(chatID int64, text string, replyTo int64) error
	// SendPhoto 发送一张图片卡片；replyTo 为 0 时不引用消息。
	// 图片是否延迟删除由 bot.photo_del_delay 决定，默认（0）留在群里。
	SendPhoto(chatID int64, png []byte, replyTo int64) error
	// SendTextWithKeyboard 以 MarkdownV2 发送文本，并把内联键盘只挂在第一片上。
	SendTextWithKeyboard(chatID int64, text string, markup tgbotapi.InlineKeyboardMarkup) error
	// SendChatAction 发送「正在上传图片」之类的状态提示；失败只记日志，不上抛错误，
	// 因此没有返回值：所有调用方（包括本包的发图流程）都只会把它当成一次尽力而为的提示。
	SendChatAction(chatID int64, action string)
	// IsTargetChat 判断会话是否是配置里指定的群；机器人只服务该群，其它会话一律不回应。
	IsTargetChat(chatID int64) bool
}

// Bot 封装 Telegram 客户端、命令注册与消息发送。
type Bot struct {
	api      *tgbotapi.Bot
	groupID  int64
	msgDelay time.Duration
	// photoDelay 是图片消息的自动删除延迟；0 表示不删图片。
	// 默认不删是用户决策：群里看图需要时间，只有显式配置了 photo_del_delay 才跟着文本一起清理。
	photoDelay time.Duration
	// delAttempts、delRetryDelay 是删除消息的最大尝试次数与两次尝试之间的间隔；
	// 零值使用 deleteAttemptsDefault / deleteRetryDelayDefault，测试里可以注入更小的值。
	delAttempts   int
	delRetryDelay time.Duration
}

// 建立 Telegram 连接的重试参数：库的 NewBotAPI 会先请求 getMe，实测本机代理抖动时
// 这一步会直接报 EOF / unexpected EOF，让进程刚启动就退出，所以这里做有限次重试。
const (
	initAttemptsDefault   = 3
	initRetryDelayDefault = 3 * time.Second
)

// 声明为变量便于测试注入：newTelegramAPI 造客户端，initAttempts / initRetryDelay 控制重试。
var (
	newTelegramAPI = func(token string) (*tgbotapi.BotAPI, error) { return tgbotapi.NewBotAPI(token) }
	initAttempts   = initAttemptsDefault
	initRetryDelay = initRetryDelayDefault
)

// New 创建机器人实例；groupID 是唯一服务的群。
// msgDelay 是文本消息的自动删除延迟，photoDelay 是图片消息的自动删除延迟，0 均表示不删除
// （图片默认不删：群里看图需要时间，只有显式配置 photo_del_delay 才清理）。
// ownerID 只影响库里的 RequireOwner 包装，而命令是用 NewCommandProcessor 注册的，
// 因此它对 /ping、/help、/war 都不起作用（任何群成员都能用）；配置里的 owner 目前是预留项。
func New(token string, ownerID, groupID int64, msgDelay, photoDelay time.Duration) (*Bot, error) {
	api, err := connectTelegram(token)
	if err != nil {
		return nil, fmt.Errorf("初始化 Telegram 机器人失败：%w", err)
	}
	handle := api.AddHandle()
	handle.SetOwnerID(ownerID)
	return &Bot{api: handle, groupID: groupID, msgDelay: msgDelay, photoDelay: photoDelay}, nil
}

// connectTelegram 建立 Telegram 连接，失败按间隔重试，重试用尽返回最后一次的错误。
func connectTelegram(token string) (*tgbotapi.BotAPI, error) {
	var lastErr error
	for attempt := 1; attempt <= initAttempts; attempt++ {
		api, err := newTelegramAPI(token)
		if err == nil {
			return api, nil
		}
		lastErr = err
		if attempt < initAttempts {
			log.Printf("连接 Telegram 失败（第 %d 次），%s 后重试：%v", attempt, initRetryDelay, err)
			time.Sleep(initRetryDelay)
		}
	}
	return nil, lastErr
}

// GroupID 返回配置里指定的目标群 id。
func (b *Bot) GroupID() int64 { return b.groupID }

// IsTargetChat 判断会话是否是需要服务的群；groupID 为 0 时表示不限制（仅离线调试用）。
func (b *Bot) IsTargetChat(chatID int64) bool {
	return b.groupID == 0 || chatID == b.groupID
}

// Register 批量注册指令；同名指令后注册者生效。指令名不含斜杠，例如 "war" 对应 /war。
func (b *Bot) Register(handlers []Handler) {
	for _, h := range handlers {
		b.api.NewCommandProcessor(h.Name, async(h.Run))
		log.Printf("已注册指令 /%s", h.Name)
	}
}

// Run 开始阻塞监听消息，直到进程退出。
func (b *Bot) Run() { b.api.Run() }

// Reply 以 MarkdownV2 回复文本，超长自动分片；replyTo 为 0 时不引用消息。
// 只有第一片引用原消息，后续分片单独发送；msgDelay 大于 0 时发送成功的消息会被延迟删除，
// 删除失败只记日志，不影响本次回复。
func (b *Bot) Reply(chatID int64, text string, replyTo int64) error {
	for i, chunk := range SplitText(text, maxMessageLen) {
		var (
			msg tgbotapi.Message
			err error
		)
		if i == 0 && replyTo != 0 {
			msg, err = b.api.SendMarkdownV2(chatID, chunk, replyTo)
		} else {
			msg, err = b.api.SendMarkdownV2(chatID, chunk)
		}
		if err != nil {
			return fmt.Errorf("发送消息失败：%w", err)
		}
		if b.msgDelay > 0 && msg.MessageID != 0 {
			b.deleteLater(chatID, msg.MessageID)
		}
	}
	return nil
}

// SendPhoto 发送一张图片卡片；replyTo 非 0 时引用原消息。
// 发送前先发 upload_photo 状态提示，让群友知道机器人正在出图；
// 图片不带 caption 与 ParseMode：卡片文案已经渲染进图片里，再挂文本只会重复。
// 成功后只在 photoDelay > 0 时注册延迟删除：默认（photo_del_delay=0）不删图片，
// 因为群里看图需要时间，删早了群友就只剩「图片已删除」。
// 失败返回带中文上下文的错误，由调用方决定是否回退纯文本。
// actionUploadPhoto 是 Telegram 的「正在上传图片」状态提示名：SendPhoto 发图前先发它，
// 让群友知道机器人正在出图（渲染一张卡片要几百毫秒）。
const actionUploadPhoto = "upload_photo"

func (b *Bot) SendPhoto(chatID int64, png []byte, replyTo int64) error {
	b.SendChatAction(chatID, actionUploadPhoto)

	photo := tgbotapi.NewPhoto(chatID, tgbotapi.FileBytes{Bytes: png})
	if replyTo != 0 {
		photo.ReplyToMessageID = replyTo
	}
	msg, err := b.api.Send(photo)
	if err != nil {
		return fmt.Errorf("发送图片失败：%w", err)
	}
	if b.photoDelay > 0 && msg.MessageID != 0 {
		b.deleteLaterAfter(b.photoDelay, chatID, msg.MessageID)
	}
	return nil
}

// SendTextWithKeyboard 以 MarkdownV2 发送文本，并把内联键盘只挂在第一片上。
// 超长文本复用 SplitText 分片（Telegram 单条上限是按 UTF-16 码元算的 4096）；
// 按钮只挂第一片：多片重复挂既冗余，前面几片被自动删除后还会留下解释不清的按钮。
// 空键盘（一行按钮都没有）会被 Telegram 拒收，此时等价于普通文本消息。
// msgDelay 大于 0 时发送成功的分片会被延迟删除，删除失败只记日志。
func (b *Bot) SendTextWithKeyboard(chatID int64, text string, markup tgbotapi.InlineKeyboardMarkup) error {
	for i, chunk := range SplitText(text, maxMessageLen) {
		msg := tgbotapi.NewMessage(chatID, chunk)
		msg.ParseMode = tgbotapi.ModeMarkdownV2
		if i == 0 && len(markup.InlineKeyboard) > 0 {
			msg.ReplyMarkup = markup
		}
		sent, err := b.api.Send(msg)
		if err != nil {
			return fmt.Errorf("发送带按钮的消息失败：%w", err)
		}
		if b.msgDelay > 0 && sent.MessageID != 0 {
			b.deleteLater(chatID, sent.MessageID)
		}
	}
	return nil
}

// SendChatAction 发送聊天状态提示（upload_photo、typing 等），薄封装。
// 状态提示只是体验优化：失败只记日志，不让整条指令跟着失败
// （例如机器人缺发图权限时，命令仍要继续走文本回退）。
// 因此这里没有返回值：签名恒为 nil 的 error 只会让调用方误以为「失败了要处理」，
// 而唯一的使用方（SendPhoto）本来就不能因为它失败而放弃发图。
// 这里用 Request 而不是 Send：Telegram 的 sendChatAction 返回 JSON true，
// 库的 Send 会把它当 Message 解析，成功也会报「解析失败」，日志里会刷假故障。
func (b *Bot) SendChatAction(chatID int64, action string) {
	if _, err := b.api.Request(tgbotapi.NewChatAction(chatID, action)); err != nil {
		log.Printf("发送聊天状态失败 chat=%d action=%s err=%v", chatID, action, err)
	}
}

// RegisterInline 注册行内查询处理器：查询文本以 prefix 开头时把查询交回 fn。
// 前缀为空会匹配所有人的全部行内查询（相当于把别人的搜索也吞掉），属于误配置，
// 因此只记一条中文警告并忽略注册。
// 回调放进会话队列异步执行：行内查询要查数据、渲染图片，不能堵住更新循环。
func (b *Bot) RegisterInline(prefix string, fn func(query tgbotapi.InlineQuery) error) {
	if prefix == "" {
		log.Printf("行内查询前缀为空，忽略注册：空前缀会匹配所有行内查询")
		return
	}
	b.api.NewInlineQueryProcessor(prefix, async(func(update tgbotapi.Update) error {
		if update.InlineQuery == nil {
			return nil
		}
		return fn(*update.InlineQuery)
	}))
	log.Printf("已注册行内查询前缀 %q", prefix)
}

// methodAnswerInlineQuery 是 Telegram 的 answerInlineQuery 接口名，与库的 InlineConfig.method() 一致。
const methodAnswerInlineQuery = "answerInlineQuery"

// AnswerInline 应答一次行内查询：queryID 来自触发本次查询的更新，由调用方显式传入，
// 避免用「最后一次查询」这类共享可变状态（并发查询会串台）。
// results 是 tgbotapi.InlineQueryResult* 组成的候选列表，允许为空（表示没有匹配结果）。
// 这里没有走 Send(InlineConfig{...}) 的两个原因：
//  1. Telegram 的 answerInlineQuery 返回 JSON true，库的 Send 会把它当 Message 解析，
//     成功也会报错；Request 只判断 ok 字段，成功即成功。
//  2. 库的 InlineConfig.params 用 AddNonZero 写 cache_time，值为 0 时整个字段被丢掉，
//     Telegram 会退回默认的 300 秒缓存，战况会发旧；这里显式带上 cache_time=0。
func (b *Bot) AnswerInline(queryID string, results []interface{}) error {
	if queryID == "" {
		return errors.New("应答行内查询失败：缺少查询 ID")
	}
	if results == nil {
		// Telegram 要求 results 是 JSON 数组，nil 切片会被序列化成 "null"（不是 "[]"），
		// 请求可能被拒收；这里归一化成空数组，表示「没有匹配结果」。
		results = []interface{}{}
	}
	params := tgbotapi.Params{"inline_query_id": queryID, "cache_time": "0"}
	if err := params.AddInterface("results", results); err != nil {
		return fmt.Errorf("序列化行内查询结果失败：%w", err)
	}
	if _, err := b.api.MakeRequest(methodAnswerInlineQuery, params); err != nil {
		return fmt.Errorf("应答行内查询失败：%w", err)
	}
	return nil
}

// 删除消息的重试参数：实测本机网络（代理换节点、连接被重置）会让单次 deleteMessage
// 返回 "unexpected EOF" 之类的传输错误，若只试一次，机器人消息就会永久留在群里，
// 自动删除形同失效，所以失败后按固定间隔重试几次再放弃。
const (
	deleteAttemptsDefault   = 3
	deleteRetryDelayDefault = 2 * time.Second
)

// deleteLater 延迟删除机器人自己发出的消息，保持群内整洁；失败只记日志。
// 用 time.AfterFunc 而不是「go + time.Sleep」：等待期间不占用 goroutine，消息多时也不会堆积。
func (b *Bot) deleteLater(chatID, messageID int64) {
	b.deleteLaterAfter(b.msgDelay, chatID, messageID)
}

// deleteLaterAfter 按指定延迟删除机器人自己发出的消息；图片用 photoDelay、文本用 msgDelay。
// 注意 delay <= 0 会立即触发删除（等同于马上删），所以调用方必须先判掉「不删」的情况，
// 本函数不做这层判断，避免把「立即删除」这种合法用法也一起禁掉。
func (b *Bot) deleteLaterAfter(delay time.Duration, chatID, messageID int64) {
	time.AfterFunc(delay, func() { b.deleteMessage(chatID, messageID) })
}

// deleteMessage 删除一条消息，失败时按间隔重试，重试用尽只记日志、不上抛错误。
// 这里用 Request 而不是 Send：deleteMessage 的 result 是布尔值，Send 会把它当 Message 解析，
// 删除成功也会返回解析错误，日志里会刷出假的失败记录。
func (b *Bot) deleteMessage(chatID, messageID int64) {
	attempts, delay := b.deletePolicy()
	for attempt := 1; ; attempt++ {
		_, err := b.api.Request(tgbotapi.NewDeleteMessage(chatID, messageID))
		if err == nil {
			return
		}
		// 消息已经不在了（重复删除）属于预期结果，不该重试也不该记成失败。
		if isMessageGone(err) {
			return
		}
		if attempt >= attempts {
			log.Printf("删除消息失败 chat=%d msg=%d 尝试=%d err=%v", chatID, messageID, attempt, err)
			return
		}
		time.Sleep(delay)
	}
}

// deletePolicy 返回删除消息的尝试次数与重试间隔，零值取默认值。
func (b *Bot) deletePolicy() (int, time.Duration) {
	attempts, delay := b.delAttempts, b.delRetryDelay
	if attempts <= 0 {
		attempts = deleteAttemptsDefault
	}
	if delay <= 0 {
		delay = deleteRetryDelayDefault
	}
	return attempts, delay
}

// isMessageGone 判断错误是否为「消息不存在」：重复删除同一条消息时 Telegram 返回
// 400 Bad Request: message to delete not found。
func isMessageGone(err error) bool {
	var apiErr *tgbotapi.Error
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.Code == http.StatusBadRequest && strings.Contains(apiErr.Message, "message to delete not found")
}

// Escape 转义 MarkdownV2 特殊字符；所有动态内容拼进消息前都要经过它。
// 先补上反斜杠：库的转义表不含反斜杠本身，而它是 MarkdownV2 的转义字符，
// 字面量反斜杠要写成两个，否则反斜杠后面的内容会被当成转义序列，整条消息可能被拒绝。
func Escape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return tgbotapi.EscapeText(tgbotapi.ModeMarkdownV2, s)
}

// SplitText 按最大长度切分文本，优先在换行处断开；单行超长时按字符硬切。
// limit 是 UTF-16 码元上限（Telegram 的 4096 就是这个口径，表情等非 BMP 字符占 2 个），
// limit <= 0 时使用 maxMessageLen。返回的所有分片拼接后与原文完全一致，
// 且不会把 MarkdownV2 的转义序列切成两半（末位留下孤立反斜杠会让整条消息被拒绝）。
func SplitText(text string, limit int) []string {
	if limit <= 0 {
		limit = maxMessageLen
	}
	runes := []rune(text)
	if codeUnits(runes) <= limit {
		return []string{text}
	}
	var parts []string
	for len(runes) > 0 {
		// 先按码元上限找出能放下的最长前缀
		cut, used := 0, 0
		for cut < len(runes) {
			next := used + codeUnitsOf(runes[cut])
			if next > limit {
				break
			}
			used = next
			cut++
		}
		if cut == 0 {
			// limit 比单个字符的码元数还小时也要前进，避免死循环
			cut = 1
		}
		// 优先在换行处断开，避免把一行内容劈成两半
		for i := cut - 1; i > cut/2; i-- {
			if runes[i] == '\n' {
				cut = i + 1
				break
			}
		}
		// 退一格，避免把转义序列切成两半；只有一片时无处可退，只能硬切
		if cut > 1 && runes[cut-1] == '\\' {
			cut--
		}
		parts = append(parts, string(runes[:cut]))
		runes = runes[cut:]
	}
	return parts
}

// codeUnits 返回整个切片的 UTF-16 码元数。
func codeUnits(runes []rune) int {
	n := 0
	for _, r := range runes {
		n += codeUnitsOf(r)
	}
	return n
}

// codeUnitsOf 返回单个字符的 UTF-16 码元数：非 BMP 字符（表情等）占 2 个。
func codeUnitsOf(r rune) int {
	if r > 0xFFFF {
		return 2
	}
	return 1
}
