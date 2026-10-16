// 本文件是入群验证要用到的那几项 Telegram 能力：禁言、踢出、封禁、批准/拒绝入群申请、
// 带按钮发图、改消息、查成员身份与用户资料，以及三个额外更新类型（新成员、入群申请、按钮回调）的注册。
//
// 为什么单独成文件：bot.go 负责消息发送与命令注册，dispatch.go 负责更新分发；
// 这里的动作全是「管理群成员」，跟卡片渲染无关。插件只通过 gatekeeper 包定义的接口调用它们，
// 不直接接触 Telegram 客户端，测试里注入假实现即可。
package bot

import (
	"fmt"
	"log"
	"time"

	tgbotapi "github.com/ijnkawakaze/telegram-bot-api"
)

// captionLimit 是 Telegram 图片说明（caption）的长度上限（实体解析后 1024 个字符）。
const captionLimit = 1024

// 成员身份里「能管理群」的两种状态。
const (
	statusCreator       = "creator"
	statusAdministrator = "administrator"
)

// RestrictChatMember 禁言成员：收回全部发言权限，只留读消息（新成员进群后的默认状态）。
func (b *Bot) RestrictChatMember(chatID, userID int64) error {
	if _, err := b.api.RestrictChatMember(chatID, userID, tgbotapi.NoMessagesPermission); err != nil {
		return fmt.Errorf("禁言成员失败（user=%d）：%w", userID, err)
	}
	return nil
}

// UnrestrictChatMember 解除禁言：恢复群默认权限。
func (b *Bot) UnrestrictChatMember(chatID, userID int64) error {
	if _, err := b.api.RestrictChatMember(chatID, userID, tgbotapi.AllPermissions); err != nil {
		return fmt.Errorf("解除禁言失败（user=%d）：%w", userID, err)
	}
	return nil
}

// BanMember 封禁成员（保持封禁状态，不解封）。
func (b *Bot) BanMember(chatID, userID int64) error {
	if _, err := b.api.BanChatMember(chatID, userID); err != nil {
		return fmt.Errorf("封禁成员失败（user=%d）：%w", userID, err)
	}
	return nil
}

// UnbanMember 解除封禁。与 BanMember 配对使用：踢出是「先封禁、过一会儿再解封」两步，
// 中间那段封禁期就是防秒回的冷却（延迟由调用方决定，见 gatekeeper 的 unban_delay）。
func (b *Bot) UnbanMember(chatID, userID int64) error {
	if _, err := b.api.UnbanChatMember(chatID, userID); err != nil {
		return fmt.Errorf("解封成员失败（user=%d）：%w", userID, err)
	}
	return nil
}

// ApproveJoinRequest 批准入群申请。
func (b *Bot) ApproveJoinRequest(chatID, userID int64) error {
	if _, err := b.api.ApproveChatJoinRequest(chatID, userID); err != nil {
		return fmt.Errorf("批准入群申请失败（user=%d）：%w", userID, err)
	}
	return nil
}

// DeclineJoinRequest 拒绝入群申请。
func (b *Bot) DeclineJoinRequest(chatID, userID int64) error {
	if _, err := b.api.Request(tgbotapi.DeclineChatJoinRequest{
		ChatConfig: tgbotapi.ChatConfig{ChatID: chatID},
		UserID:     userID,
	}); err != nil {
		return fmt.Errorf("拒绝入群申请失败（user=%d）：%w", userID, err)
	}
	return nil
}

// MemberIsAdmin 判断成员是不是管理员或群主。验证卡片上的「放行 / 封禁」按钮靠它挡住普通群友。
func (b *Bot) MemberIsAdmin(chatID, userID int64) (bool, error) {
	member, err := b.api.GetChatMember(tgbotapi.GetChatMemberConfig{
		ChatConfigWithUser: tgbotapi.ChatConfigWithUser{ChatID: chatID, UserID: userID},
	})
	if err != nil {
		return false, fmt.Errorf("查询成员身份失败（user=%d）：%w", userID, err)
	}
	return member.Status == statusCreator || member.Status == statusAdministrator, nil
}

// Rights 是机器人在某个群里的关键权限，用于启动自检。
type Rights struct {
	CanRestrict bool // 能禁言与踢人（can_restrict_members）
	CanDelete   bool // 能删除别人的消息（can_delete_messages）
	// 能邀请新成员（can_invite_users）。申请加入模式必须要有：Telegram 规定没有它
	// 机器人收不到入群申请更新，也批准/拒绝不了申请。
	CanInvite bool
}

// BotRights 查询机器人自己在群里的权限。机器人不是管理员时全部为 false（不报错）。
func (b *Bot) BotRights(chatID int64) (Rights, error) {
	member, err := b.api.GetChatMember(tgbotapi.GetChatMemberConfig{
		ChatConfigWithUser: tgbotapi.ChatConfigWithUser{ChatID: chatID, UserID: b.api.Self.ID},
	})
	if err != nil {
		return Rights{}, fmt.Errorf("查询机器人权限失败：%w", err)
	}
	switch member.Status {
	case statusCreator:
		return Rights{CanRestrict: true, CanDelete: true, CanInvite: true}, nil
	case statusAdministrator:
		return Rights{
			CanRestrict: member.CanRestrictMembers,
			CanDelete:   member.CanDeleteMessages,
			CanInvite:   member.CanInviteUsers,
		}, nil
	default:
		// 普通成员没有这些权限。
		return Rights{}, nil
	}
}

// UserBio 读用户的简介（getChat 对私聊返回 bio）。查不到不算错误，返回空串：
// 简介只是广告词的加分项，拿不到时不该拦住整条验证流程。
func (b *Bot) UserBio(userID int64) string {
	chat, err := b.api.GetChatInfo(userID)
	if err != nil {
		log.Printf("入群验证：读取用户简介失败 user=%d err=%v", userID, err)
		return ""
	}
	return chat.Bio
}

// SendTemporaryText 发一条到点自动删除的 MarkdownV2 文本（delay <= 0 时按普通文本发，不删除）。
// 用途是入群验证的欢迎消息：群里不想留痕时可以配一个删除延迟。
func (b *Bot) SendTemporaryText(chatID int64, text string, delay time.Duration) error {
	if delay <= 0 {
		return b.SendText(chatID, text)
	}
	return b.sendTextWithKeyboard(chatID, text, tgbotapi.InlineKeyboardMarkup{}, delay)
}

// SendText 按 chat id 发一条 MarkdownV2 文本：可以发给群、用户私聊或审计频道。
func (b *Bot) SendText(chatID int64, text string) error {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = tgbotapi.ModeMarkdownV2
	if _, err := b.api.Send(msg); err != nil {
		return fmt.Errorf("发送消息失败（chat=%d）：%w", chatID, err)
	}
	return nil
}

// SendPhotoWithKeyboardID 发一张带说明与内联键盘的图片，返回消息 id。
// 入群验证用它把「题面图 + 提示文本 + 按钮」发成一条消息（说明就是提示文本，进度也编辑在这条上）。
func (b *Bot) SendPhotoWithKeyboardID(chatID int64, png []byte, caption string,
	markup tgbotapi.InlineKeyboardMarkup) (int64, error) {
	b.SendChatAction(chatID, actionUploadPhoto)

	photo := tgbotapi.NewPhoto(chatID, tgbotapi.FileBytes{Bytes: png})
	photo.Caption = clampCaption(caption)
	photo.ParseMode = tgbotapi.ModeMarkdownV2
	if len(markup.InlineKeyboard) > 0 {
		photo.ReplyMarkup = markup
	}
	sent, err := b.api.Send(photo)
	if err != nil {
		return 0, fmt.Errorf("发送图片消息失败：%w", err)
	}
	return sent.MessageID, nil
}

// SendTextWithKeyboardID 与 SendTextWithKeyboard 相同，但把消息 id 交给调用方：
// 入群验证的提示文本要反复编辑这条消息来显示进度。
func (b *Bot) SendTextWithKeyboardID(chatID int64, text string, markup tgbotapi.InlineKeyboardMarkup) (int64, error) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = tgbotapi.ModeMarkdownV2
	if len(markup.InlineKeyboard) > 0 {
		msg.ReplyMarkup = markup
	}
	sent, err := b.api.Send(msg)
	if err != nil {
		return 0, fmt.Errorf("发送带按钮的消息失败：%w", err)
	}
	return sent.MessageID, nil
}

// EditText 改文本与键盘，并把**改完之后要显示的键盘**原样传进来。
//
// ⚠ Telegram 的坑（实测踩过）：editMessageText 只要请求里不带 reply_markup，
// 就会把消息上已有的内联键盘**删掉**（不是「保持不动」）。所以想保留按钮就必须每次
// 把当前键盘一起传过来；想清掉按钮就传一个空键盘。
func (b *Bot) EditText(chatID int64, messageID int64, text string, markup tgbotapi.InlineKeyboardMarkup) error {
	edit := tgbotapi.EditMessageTextConfig{
		BaseEdit:  tgbotapi.BaseEdit{ChatID: chatID, MessageID: int(messageID), ReplyMarkup: &markup},
		Text:      text,
		ParseMode: tgbotapi.ModeMarkdownV2,
	}
	if _, err := b.api.Request(edit); err != nil {
		return fmt.Errorf("更新验证消息失败：%w", err)
	}
	return nil
}

// EditCaption 改图片消息的说明与键盘，语义与 EditText 完全一致（同一个 reply_markup 的坑）：
// 入群验证的题面图把提示文本放在 caption 上，刷新进度就走这里。
func (b *Bot) EditCaption(chatID int64, messageID int64, caption string, markup tgbotapi.InlineKeyboardMarkup) error {
	edit := tgbotapi.EditMessageCaptionConfig{
		BaseEdit:  tgbotapi.BaseEdit{ChatID: chatID, MessageID: int(messageID), ReplyMarkup: &markup},
		Caption:   clampCaption(caption),
		ParseMode: tgbotapi.ModeMarkdownV2,
	}
	if _, err := b.api.Request(edit); err != nil {
		return fmt.Errorf("更新验证图片说明失败：%w", err)
	}
	return nil
}

// AnswerCallback 应答一次按钮点击。失败只记日志：用户点得快、消息太旧都会失败，
// 而「提示没弹出来」不该让验证本身出问题。
func (b *Bot) AnswerCallback(queryID, text string, alert bool) {
	if queryID == "" {
		return
	}
	config := tgbotapi.CallbackConfig{CallbackQueryID: queryID, Text: text, ShowAlert: alert}
	if _, err := b.api.Request(config); err != nil {
		log.Printf("应答按钮点击失败 query=%s err=%v", queryID, err)
	}
}

// RegisterMemberWatcher 注册「新成员进群」处理器（含机器人自己被拉进群的情形，由调用方过滤）。
func (b *Bot) RegisterMemberWatcher(fn func(tgbotapi.Update) error) {
	b.api.NewMemberProcessor(async(fn))
}

// RegisterJoinRequests 注册「入群申请」处理器（群为申请模式时才有这个更新）。
func (b *Bot) RegisterJoinRequests(fn func(tgbotapi.Update) error) {
	b.api.JoinRequestProcessor(async(fn))
}

// RegisterLeftMemberWatcher 注册「成员退群」处理器。
func (b *Bot) RegisterLeftMemberWatcher(fn func(tgbotapi.Update) error) {
	b.api.LeftMemberProcessor(async(fn))
}

// RegisterCallback 注册按钮回调处理器：data 的前缀是 prefix（库按前缀分发）。
func (b *Bot) RegisterCallback(prefix string, fn func(tgbotapi.Update) error) {
	if prefix == "" {
		log.Printf("按钮回调前缀为空，忽略注册：空前缀会吞掉所有回调")
		return
	}
	b.api.NewCallBackProcessor(prefix, async(fn))
}

// clampCaption 把说明截到 Telegram 允许的长度内（按 rune 数算，够安全且不必抠代理对细节）。
func clampCaption(caption string) string {
	runes := []rune(caption)
	if len(runes) <= captionLimit {
		return caption
	}
	return string(runes[:captionLimit-1]) + "…"
}
