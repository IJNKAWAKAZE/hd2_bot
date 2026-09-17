// Package plugtest 放各指令插件测试共用的假实现与小工具：记录调用行为的假 Sender、
// 假渲染器，以及日志捕获、构造更新、按名字跑处理器这几件事。
//
// 为什么单独一个包：这五份测试原先各抄了一份约 40 行的假 Sender、一份假渲染器与
// 四份 captureLog，改一处得改五遍。它是**只被 _test.go 引用**的测试辅助包，
// 不参与生产装配（main 不引用它），所以不放进 core/bot 污染生产代码的依赖方向。
package plugtest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/ijnkawakaze/telegram-bot-api"

	"hd2_bot/src/core/bot"
	"hd2_bot/src/render"
)

// DefaultMessageID 是 MessageUpdate 构造的更新里的消息 id。
// 定成一个常量，用例断言 replyTo 时不必各写一个数字（写错了也看不出是笔误还是实现错了）。
const DefaultMessageID = 42

// RenderErr 是渲染失败用例共用的错误原因：浏览器不可用，与线上真会遇到的失败同类。
var RenderErr = errors.New("浏览器启动失败：找不到 Chromium")

// Sender 是回消息能力的记录型假实现：把每次调用记进切片，供用例断言
// 「发了什么、发了几次、发给谁、什么顺序」。它是各插件回消息接口的并集，
// 插件测试直接用同一个实现，不必再各写一份。
type Sender struct {
	// GroupID 是唯一服务的群；0 表示不限制（与 bot.Bot 的语义一致）。
	GroupID int64
	// Calls 按调用顺序记录方法名（reply / photo / hint），
	// 用来断言「先发图再发搜索提示」这类只看分类切片验证不了的约定。
	Calls []string
	// Replies 记录文本回复与带按钮的文本（两者都是 MarkdownV2 文本消息）。
	Replies []string
	// Chats 记录每次发送的会话 id，与 Calls 一一对应。
	Chats []int64
	// ReplyTo 记录每次 Reply 引用的消息 id。
	ReplyTo []int64
	// Photos 记录发出的图片字节。
	Photos [][]byte
	// PhotoChats 记录每次发图的会话 id。
	PhotoChats []int64
	// PhotoReplyTo 记录每次发图引用的消息 id。
	PhotoReplyTo []int64
	// Markups 记录带按钮文本上的内联键盘。
	Markups []tgbotapi.InlineKeyboardMarkup
	// Err 非空时所有发送方法都返回它，用来验证发送失败被上抛给分发层。
	Err error
}

// Reply 记录一次文本回复。
func (s *Sender) Reply(chatID int64, text string, replyTo int64) error {
	s.Calls = append(s.Calls, "reply")
	s.Replies = append(s.Replies, text)
	s.Chats = append(s.Chats, chatID)
	s.ReplyTo = append(s.ReplyTo, replyTo)
	return s.Err
}

// Ping 记录专用的自动清理回复。
func (s *Sender) Ping(chatID, commandID int64) error {
	s.Calls = append(s.Calls, "ping")
	s.Chats = append(s.Chats, chatID)
	s.ReplyTo = append(s.ReplyTo, commandID)
	return s.Err
}

func (s *Sender) DeleteMessage(chatID, messageID int64) error {
	s.Calls = append(s.Calls, "delete")
	s.Chats = append(s.Chats, chatID)
	return s.Err
}

func (s *Sender) SendTemporaryTextWithKeyboard(chatID int64, text string, markup tgbotapi.InlineKeyboardMarkup, _ time.Duration) error {
	return s.SendTextWithKeyboard(chatID, text, markup)
}

// SendPhoto 记录一次图片发送。
func (s *Sender) SendPhoto(chatID int64, png []byte, replyTo int64) error {
	s.Calls = append(s.Calls, "photo")
	s.Photos = append(s.Photos, png)
	s.PhotoChats = append(s.PhotoChats, chatID)
	s.PhotoReplyTo = append(s.PhotoReplyTo, replyTo)
	return s.Err
}

// SendTextWithKeyboard 记录一次带按钮的文本发送。
func (s *Sender) SendTextWithKeyboard(chatID int64, text string, markup tgbotapi.InlineKeyboardMarkup) error {
	s.Calls = append(s.Calls, "hint")
	s.Replies = append(s.Replies, text)
	s.Chats = append(s.Chats, chatID)
	s.Markups = append(s.Markups, markup)
	return s.Err
}

// IsTargetChat 只服务配置里指定的群；GroupID 为 0 表示不限制。
func (s *Sender) IsTargetChat(chatID int64) bool {
	return s.GroupID == 0 || chatID == s.GroupID
}

// Renderer 是 render.Renderer 的记录型假实现：记下收到的卡片，返回预设图片或错误，
// 让用例不开浏览器也能覆盖「渲染成功发图」与「渲染失败回退文本」两条路径。
type Renderer struct {
	// Img 是渲染成功时返回的图片字节。
	Img []byte
	// Err 非空时模拟渲染失败。
	Err error
	// Cards 按顺序记录交给渲染引擎的卡片。
	Cards []render.Card
	// Closed 记录 Close 是否被调用。
	Closed bool
}

// Render 记录卡片并返回预设结果。
func (r *Renderer) Render(_ context.Context, card render.Card) ([]byte, error) {
	r.Cards = append(r.Cards, card)
	if r.Err != nil {
		return nil, r.Err
	}
	return r.Img, nil
}

// Close 记录关闭调用；假实现没有需要释放的资源。
func (r *Renderer) Close() error {
	r.Closed = true
	return nil
}

// FailRenderer 返回一个「渲染必失败」的假渲染器，用来验证文本回退路径。
func FailRenderer() *Renderer { return &Renderer{Err: RenderErr} }

// MessageUpdate 构造一条群内文本消息更新（消息 id 固定为 DefaultMessageID）。
func MessageUpdate(chatID int64, text string) tgbotapi.Update {
	return tgbotapi.Update{
		Message: &tgbotapi.Message{
			MessageID: DefaultMessageID,
			Chat:      &tgbotapi.Chat{ID: chatID},
			Text:      text,
		},
	}
}

// RunHandler 从 handlers 里取出指定名字的处理器并执行。
func RunHandler(t *testing.T, handlers []bot.Handler, name string, update tgbotapi.Update) error {
	t.Helper()
	for _, h := range handlers {
		if h.Name == name {
			return h.Run(update)
		}
	}
	t.Fatalf("未注册 /%s 指令", name)
	return nil
}

// AssertLogFields 断言一条日志里带「命令名 + 群 id」这两个排查必需字段。
// 只断言前缀（例如「/war 渲染失败」）的话，把 chat=%d 删掉也照样全绿，
// 线上多群并发时就分不清是哪条指令、哪个群出的问题。
func AssertLogFields(t *testing.T, logs *bytes.Buffer, wantPrefix string, chatID int64) {
	t.Helper()
	got := logs.String()
	if !strings.Contains(got, wantPrefix) {
		t.Errorf("日志应包含 %q，实际：%s", wantPrefix, got)
	}
	if want := fmt.Sprintf("chat=%d", chatID); !strings.Contains(got, want) {
		t.Errorf("日志应带上群 id（%s），实际：%s", want, got)
	}
}

// CaptureLog 把标准日志重定向到缓冲区，用例结束时还原。
// 断言日志内容必须用它：只看返回值的话，「日志里有没有 chat 与命令名」这种排查必需的信息没人守。
func CaptureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	oldWriter, oldFlags := log.Writer(), log.Flags()
	log.SetOutput(buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
	})
	return buf
}
