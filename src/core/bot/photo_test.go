package bot

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/ijnkawakaze/telegram-bot-api"
)

// testPNG 是占位图片字节：发送路径不做图片解码，内容只用于让请求有体积。
var testPNG = []byte{0x89, 0x50, 0x4E, 0x47}

func TestTemporaryKeyboardUsesConfiguredDelay(t *testing.T) {
	b, fake := newPhotoTestBot(t, 20*time.Millisecond)
	if err := b.SendTemporaryTextWithKeyboard(-100, "选择星球", planetMarkup("星球-"), 0); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for fake.count("deleteMessage") == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if fake.count("deleteMessage") != 1 {
		t.Fatal("temporary keyboard was not deleted")
	}
}

func TestPhotoPreparationStartsImmediatelyAndStops(t *testing.T) {
	b, fake := newPhotoTestBot(t, 0)
	stop := b.StartPhotoPreparation(-100)
	if fake.count("sendChatAction") != 1 {
		t.Error("preparation must signal before work starts")
	}
	stop()
}

// newPhotoTestBot 创建测试机器人。
func newPhotoTestBot(t *testing.T, msgDelay time.Duration) (*Bot, *fakeTelegram) {
	t.Helper()
	b, fake := newTestBot(t, msgDelay, 0, 0)
	return b, fake
}

// planetMarkup 造一个「选择星球」按钮，按钮里带的行内查询前缀由调用方给出。
func planetMarkup(prefix string) tgbotapi.InlineKeyboardMarkup {
	p := prefix
	return tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(
		tgbotapi.InlineKeyboardButton{Text: "选择星球", SwitchInlineQueryCurrentChat: &p},
	))
}

// decodeMarkup 解析 sendMessage 请求里的 reply_markup，解析失败直接结束用例。
func decodeMarkup(t *testing.T, form url.Values) tgbotapi.InlineKeyboardMarkup {
	t.Helper()
	raw := form.Get("reply_markup")
	if raw == "" {
		t.Fatal("消息上没有 reply_markup")
	}
	var markup tgbotapi.InlineKeyboardMarkup
	if err := json.Unmarshal([]byte(raw), &markup); err != nil {
		t.Fatalf("解析 reply_markup 失败：%v（原始值 %s）", err, raw)
	}
	return markup
}

// TestSendPhotoKeepsPhotoWhenDelayZero 验证图片保留。
// 图片始终保留。
func TestSendPhotoKeepsPhotoWhenDelayZero(t *testing.T) {
	b, fake := newPhotoTestBot(t, 0)
	if err := b.SendPhoto(-100, testPNG, 42); err != nil {
		t.Fatalf("发送图片失败：%v", err)
	}
	if got := fake.count("sendPhoto"); got != 1 {
		t.Fatalf("期望 1 次 sendPhoto，实际 %d 次", got)
	}
	// 等一段明显长于删除触发窗口的时间，确认确实没有注册删除。
	time.Sleep(200 * time.Millisecond)
	if got := fake.count("deleteMessage"); got != 0 {
		t.Fatalf("图片不应被删除，实际删除 %d 次", got)
	}
}

// TestSendChatActionCalledBeforePhoto 校验发图前先发 upload_photo 状态提示。
func TestSendChatActionCalledBeforePhoto(t *testing.T) {
	b, fake := newPhotoTestBot(t, 0)
	if err := b.SendPhoto(-100, testPNG, 0); err != nil {
		t.Fatalf("发送图片失败：%v", err)
	}
	actionAt, photoAt := fake.firstIndex("sendChatAction"), fake.firstIndex("sendPhoto")
	if actionAt < 0 || photoAt < 0 {
		t.Fatalf("缺少状态提示或发图请求，实际顺序 %v", fake.order())
	}
	if actionAt > photoAt {
		t.Fatalf("状态提示应先于发图，实际顺序 %v", fake.order())
	}
	if got := fake.forms("sendChatAction")[0].Get("action"); got != "upload_photo" {
		t.Errorf("状态提示应为 upload_photo，实际 %q", got)
	}
}

// TestSendPhotoReplyToAndNoCaption 校验 replyTo 透传，且图片不带 caption / parse_mode
// （卡片文案已经渲染进图片里，图片本身不该再挂文字）。
func TestSendPhotoReplyToAndNoCaption(t *testing.T) {
	b, fake := newPhotoTestBot(t, 0)
	if err := b.SendPhoto(-100, testPNG, 42); err != nil {
		t.Fatalf("发送图片失败：%v", err)
	}
	form := fake.forms("sendPhoto")[0]
	if got := form.Get("reply_to_message_id"); got != "42" {
		t.Errorf("replyTo 未透传，实际 %q", got)
	}
	if got := form.Get("caption"); got != "" {
		t.Errorf("图片不应带 caption，实际 %q", got)
	}
	if got := form.Get("parse_mode"); got != "" {
		t.Errorf("图片不应带 parse_mode，实际 %q", got)
	}

	// replyTo 为 0 时不引用任何消息
	b2, fake2 := newPhotoTestBot(t, 0)
	if err := b2.SendPhoto(-100, testPNG, 0); err != nil {
		t.Fatalf("发送图片失败：%v", err)
	}
	if got := fake2.forms("sendPhoto")[0].Get("reply_to_message_id"); got != "" {
		t.Errorf("replyTo 为 0 时不应引用消息，实际 %q", got)
	}
}

// TestSendPhotoFailureHasChineseContext 校验发图失败返回带中文上下文的错误，便于日志定位。
func TestSendPhotoFailureHasChineseContext(t *testing.T) {
	b, fake := newPhotoTestBot(t, 0)
	fake.setPhotoCode(http.StatusBadRequest)
	err := b.SendPhoto(-100, testPNG, 0)
	if err == nil || !strings.Contains(err.Error(), "发送图片失败") {
		t.Fatalf("期望带「发送图片失败」上下文的错误，实际 %v", err)
	}
}

// TestSendChatActionFailureOnlyLogs 校验状态提示失败只记日志、不上抛：
// 状态提示只是体验优化，失败了也不该让整条指令跟着失败。
func TestSendChatActionFailureOnlyLogs(t *testing.T) {
	b, fake := newPhotoTestBot(t, 0)
	fake.setActionCode(http.StatusBadRequest)
	logs := captureLog(t)

	// 没有返回值：失败只记日志，调用方（发图流程）不需要也无法处理这个错误。
	b.SendChatAction(-100, "upload_photo")
	if !strings.Contains(logs.String(), "发送聊天状态失败") {
		t.Fatalf("状态提示失败应记日志：\n%s", logs.String())
	}
}

// TestSendTextWithKeyboardHasMarkup 校验文本按 MarkdownV2 发送、按钮挂在消息上，
// 且不受 /ping 删除延迟影响。
func TestSendTextWithKeyboardHasMarkup(t *testing.T) {
	b, fake := newPhotoTestBot(t, 10*time.Millisecond)
	if err := b.SendTextWithKeyboard(-100, "点下面的按钮选星球", planetMarkup("星球-")); err != nil {
		t.Fatalf("发送带按钮的消息失败：%v", err)
	}
	forms := fake.forms("sendMessage")
	if len(forms) != 1 {
		t.Fatalf("期望 1 条消息，实际 %d 条", len(forms))
	}
	if got := forms[0].Get("parse_mode"); got != "MarkdownV2" {
		t.Errorf("parse_mode 应为 MarkdownV2，实际 %q", got)
	}
	markup := decodeMarkup(t, forms[0])
	if len(markup.InlineKeyboard) != 1 || len(markup.InlineKeyboard[0]) != 1 {
		t.Fatalf("按钮结构错误：%+v", markup.InlineKeyboard)
	}
	button := markup.InlineKeyboard[0][0]
	if button.SwitchInlineQueryCurrentChat == nil || *button.SwitchInlineQueryCurrentChat != "星球-" {
		t.Fatalf("按钮应带 switch_inline_query_current_chat=星球-，实际 %s", forms[0].Get("reply_markup"))
	}
	time.Sleep(100 * time.Millisecond)
	if fake.count("deleteMessage") != 0 {
		t.Fatal("按钮消息不应删除")
	}
}

// TestSendTextWithKeyboardMarkupOnlyOnFirstChunk 校验超长文本分片时按钮只挂在第一片上。
// 多片重复挂按钮既冗余，前面几片被删掉后还会留下无法解释的按钮。
func TestSendTextWithKeyboardMarkupOnlyOnFirstChunk(t *testing.T) {
	b, fake := newPhotoTestBot(t, 0)
	text := strings.Repeat("星球数据行\n", 1000) // 6000 个码元，必然分片
	if err := b.SendTextWithKeyboard(-100, text, planetMarkup("星球-")); err != nil {
		t.Fatalf("发送带按钮的长消息失败：%v", err)
	}
	forms := fake.forms("sendMessage")
	if len(forms) < 2 {
		t.Fatalf("超长文本应分片发送，实际 %d 片", len(forms))
	}
	if forms[0].Get("reply_markup") == "" {
		t.Fatal("第一片应带内联键盘")
	}
	for i, form := range forms[1:] {
		if form.Get("reply_markup") != "" {
			t.Fatalf("第 %d 片不应带按钮，实际 %s", i+2, form.Get("reply_markup"))
		}
	}
}

// inlineUpdate 构造一条行内查询更新。
func inlineUpdate(id int64, query string) tgbotapi.Update {
	return tgbotapi.Update{
		UpdateID: id,
		InlineQuery: &tgbotapi.InlineQuery{
			ID:    "inline-" + strconv.FormatInt(id, 10),
			Query: query,
			From:  &tgbotapi.User{ID: 555, FirstName: "测试"},
		},
	}
}

// TestRegisterInlineDispatchesByPrefix 校验行内查询按前缀分发：命中前缀触发回调，非前缀不触发。
// 这里走完整分发路径（机器人真的跑长轮询），确保前缀匹配接在 Telegram 更新上而不是只写在注释里。
func TestRegisterInlineDispatchesByPrefix(t *testing.T) {
	resetQueues()
	defer resetQueues()

	release := make(chan struct{})
	b, fake := newPhotoTestBot(t, 0)
	t.Cleanup(func() { close(release) })

	queries := make(chan string, 4)
	b.RegisterInline("星球-", func(query tgbotapi.InlineQuery) error {
		queries <- query.Query
		return nil
	})
	fake.setUpdates(t, release,
		inlineUpdate(1, "星球-天园"),
		inlineUpdate(2, "别的-xx"),
	)
	go b.Run()

	select {
	case got := <-queries:
		if got != "星球-天园" {
			t.Fatalf("回调收到的查询错误：%q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("命中前缀的行内查询没有触发回调")
	}

	select {
	case got := <-queries:
		t.Fatalf("非前缀查询不应触发回调，实际收到 %q", got)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestRegisterInlineIgnoresEmptyPrefix 校验空前缀被忽略：
// 空前缀会匹配所有人的全部行内查询，属于误配置。
func TestRegisterInlineIgnoresEmptyPrefix(t *testing.T) {
	b, _ := newPhotoTestBot(t, 0)
	logs := captureLog(t)

	b.RegisterInline("", func(tgbotapi.InlineQuery) error {
		t.Error("空前缀不应注册回调")
		return nil
	})

	if !strings.Contains(logs.String(), "前缀为空") {
		t.Fatalf("空前缀应记一条中文警告：\n%s", logs.String())
	}
}

// TestAnswerInlineSendsQueryIDAndCacheTime 校验应答带上本次查询的 ID，并且显式要求不做客户端缓存。
// cache_time 的 Telegram 默认值是 300 秒，战况常变，缺省会发旧结果，必须显式写 0。
func TestAnswerInlineSendsQueryIDAndCacheTime(t *testing.T) {
	b, fake := newPhotoTestBot(t, 0)
	results := []interface{}{tgbotapi.NewInlineQueryResultArticle("p1", "天园", "/planet 天园六IV")}
	if err := b.AnswerInline("query-1", results); err != nil {
		t.Fatalf("应答行内查询失败：%v", err)
	}
	forms := fake.forms("answerInlineQuery")
	if len(forms) != 1 {
		t.Fatalf("期望 1 次 answerInlineQuery，实际 %d 次", len(forms))
	}
	if got := forms[0].Get("inline_query_id"); got != "query-1" {
		t.Errorf("inline_query_id 错误：%q", got)
	}
	if got := forms[0].Get("cache_time"); got != "0" {
		t.Fatalf("必须显式要求 cache_time=0（默认 300 秒会发旧战况），实际 %q", got)
	}
	if got := forms[0].Get("results"); !strings.Contains(got, `"p1"`) {
		t.Errorf("results 未带上候选条目：%q", got)
	}
}

// TestAnswerInlineRejectsEmptyQueryID 校验缺少查询 ID 时直接报错，不发无意义的请求。
func TestAnswerInlineRejectsEmptyQueryID(t *testing.T) {
	b, fake := newPhotoTestBot(t, 0)
	if err := b.AnswerInline("", nil); err == nil {
		t.Fatal("缺少查询 ID 时应返回错误")
	}
	if got := fake.count("answerInlineQuery"); got != 0 {
		t.Fatalf("缺少查询 ID 时不应发请求，实际 %d 次", got)
	}
}

// TestAnswerInlineSendsEmptyResultsArray 校验没有匹配结果时发的是空数组而不是 null。
// Telegram 要求 results 是 JSON 数组，nil 切片会被序列化成 "null"，整条请求可能被拒收；
// 空结果又没有 queryID 缺失那种提前返回，必须单独钉住。
func TestAnswerInlineSendsEmptyResultsArray(t *testing.T) {
	b, fake := newPhotoTestBot(t, 0)
	if err := b.AnswerInline("query-1", nil); err != nil {
		t.Fatalf("应答行内查询失败：%v", err)
	}
	forms := fake.forms("answerInlineQuery")
	if len(forms) != 1 {
		t.Fatalf("期望 1 次 answerInlineQuery，实际 %d 次", len(forms))
	}
	if got := forms[0].Get("results"); got != "[]" {
		t.Fatalf("空结果应序列化成 []，实际 %q", got)
	}
}
