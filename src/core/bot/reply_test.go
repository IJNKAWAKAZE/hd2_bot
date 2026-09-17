package bot

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tgbotapi "github.com/ijnkawakaze/telegram-bot-api"
)

// fakeTelegram 记录机器人发出的请求，替代真实 Telegram 服务器，
// 让 Reply（分片、MarkdownV2、自动删除）能在没有 token 的情况下被验证。
type fakeTelegram struct {
	mu         sync.Mutex
	methods    []string
	bodies     []url.Values
	sendCode   int    // 非 0 时 sendMessage 返回该状态码，用于模拟发送失败
	deleteCode int    // 非 0 时 deleteMessage 返回该状态码，用于模拟删除失败
	deleteDesc string // 删除失败响应里的 description，用于模拟 Telegram 的具体错误文案
	// deleteFailFirst 大于 0 时，前 deleteFailFirst 次 deleteMessage 返回失败、之后成功，
	// 用于模拟「网络抖一下就好」的传输错误（此时 deleteCode 应为 0，失败码默认 400）。
	deleteFailFirst int
	// photoCode 非 0 时 sendPhoto 返回该状态码，用于模拟发图失败。
	photoCode int
	// actionCode 非 0 时 sendChatAction 返回该状态码，用于模拟状态提示失败。
	actionCode int
	// updates、updateCalls、updatesHold 支撑「让测试机器人真的跑长轮询」的用例：
	// 第一次 getUpdates 返回 updates 里的更新，之后的请求阻塞在 updatesHold 上，
	// 避免接收协程在测试进程剩余时间里空转刷请求。
	updates     []string
	updateCalls int
	updatesHold chan struct{}
}

// ServeHTTP 实现假 Telegram Bot API。
func (f *fakeTelegram) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 上传类请求（sendPhoto）是 multipart/form-data，ParseForm 拿不到它的字段，
	// 必须显式解析 multipart；先解析 multipart 再把结果并进 PostForm，供断言直接读取。
	_ = r.ParseMultipartForm(32 << 20)
	_ = r.ParseForm()
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]

	f.mu.Lock()
	f.methods = append(f.methods, method)
	f.bodies = append(f.bodies, r.PostForm)
	code := 0
	desc := "bad request"
	switch method {
	case "sendMessage":
		code = f.sendCode
	case "sendPhoto":
		code = f.photoCode
	case "sendChatAction":
		code = f.actionCode
	case "deleteMessage":
		switch {
		case f.deleteFailFirst > 0:
			f.deleteFailFirst--
			code = f.deleteCode
			if code == 0 {
				code = http.StatusBadRequest
			}
		default:
			code = f.deleteCode
		}
		if f.deleteDesc != "" {
			desc = f.deleteDesc
		}
	}
	f.mu.Unlock()

	// 长轮询要单独伺候：它返回的是更新数组，而不是通用的布尔结果。
	if method == "getUpdates" {
		f.serveGetUpdates(w)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if code != 0 {
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":` + strconv.Itoa(code) + `,"description":"` + desc + `"}`))
		return
	}
	switch method {
	case "getMe":
		_, _ = w.Write([]byte(`{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"测试","username":"test_bot"}}`))
	case "sendMessage":
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":777,"date":1,"chat":{"id":-100},"text":"ok"}}`))
	case "sendPhoto":
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":888,"date":1,"chat":{"id":-100}}}`))
	default:
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}
}

// count 返回某方法被调用的次数。
func (f *fakeTelegram) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, m := range f.methods {
		if m == method {
			n++
		}
	}
	return n
}

// firstIndex 返回某方法第一次被调用的位置，没调用过返回 -1。
func (f *fakeTelegram) firstIndex(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, m := range f.methods {
		if m == method {
			return i
		}
	}
	return -1
}

// order 返回目前为止收到的全部方法名，按调用顺序排列。
func (f *fakeTelegram) order() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.methods...)
}

// setPhotoCode 设置 sendPhoto 的失败状态码；与假服务器并发读写同一字段，必须加锁。
func (f *fakeTelegram) setPhotoCode(code int) {
	f.mu.Lock()
	f.photoCode = code
	f.mu.Unlock()
}

// setActionCode 设置 sendChatAction 的失败状态码；与假服务器并发读写同一字段，必须加锁。
func (f *fakeTelegram) setActionCode(code int) {
	f.mu.Lock()
	f.actionCode = code
	f.mu.Unlock()
}

// setUpdates 预置第一次 getUpdates 返回的更新；之后的请求会一直阻塞到 release 关闭。
// 用例负责在 t.Cleanup 里关闭 release：它注册得比 newTestBot 的 server.Close 晚，
// 因此会先执行，服务器不会因为挂起的请求而卡住。
func (f *fakeTelegram) setUpdates(t *testing.T, release chan struct{}, updates ...tgbotapi.Update) {
	t.Helper()
	raw := make([]string, 0, len(updates))
	for _, update := range updates {
		data, err := json.Marshal(update)
		if err != nil {
			t.Fatalf("序列化测试更新失败：%v", err)
		}
		raw = append(raw, string(data))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updatesHold = release
	f.updates = raw
}

// serveGetUpdates 服务 getUpdates：第一次返回预置更新，之后阻塞并返回失败，
// 让库的接收协程退避 3 秒，而不是在测试进程里空转。
func (f *fakeTelegram) serveGetUpdates(w http.ResponseWriter) {
	f.mu.Lock()
	first := f.updateCalls == 0
	f.updateCalls++
	updates, hold := f.updates, f.updatesHold
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if !first {
		if hold != nil {
			<-hold
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":500,"description":"没有更多更新"}`))
		return
	}
	_, _ = w.Write([]byte(`{"ok":true,"result":[` + strings.Join(updates, ",") + `]}`))
}

// forms 返回某方法收到的全部请求参数，按调用顺序排列。
func (f *fakeTelegram) forms(method string) []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []url.Values
	for i, m := range f.methods {
		if m == method {
			out = append(out, f.bodies[i])
		}
	}
	return out
}

// waitForMethod 等待某个方法被调用；超时说明机器人没有发出预期请求。
func (f *fakeTelegram) waitForMethod(t *testing.T, method string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if f.count(method) > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("未收到 %s 请求", method)
}

// newTestBot 造一个指向假 Telegram 的机器人实例。
func newTestBot(t *testing.T, delay time.Duration, sendCode, deleteCode int) (*Bot, *fakeTelegram) {
	t.Helper()
	fake := &fakeTelegram{sendCode: sendCode, deleteCode: deleteCode}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)

	api, err := tgbotapi.NewBotAPIWithClient("TOKEN", server.URL+"/bot%s/%s", server.Client())
	if err != nil {
		t.Fatalf("创建测试机器人失败：%v", err)
	}
	// 重试间隔取小值让用例跑得快；尝试次数保持产品默认值（3 次）。
	return &Bot{api: api.AddHandle(), groupID: -100, msgDelay: delay, delRetryDelay: 5 * time.Millisecond}, fake
}

// TestReplySendsMarkdownV2 校验按 MarkdownV2 发送并引用原消息。
func TestReplySendsMarkdownV2(t *testing.T) {
	b, fake := newTestBot(t, 0, 0, 0)
	if err := b.Reply(-100, "你好", 42); err != nil {
		t.Fatalf("发送失败：%v", err)
	}
	forms := fake.forms("sendMessage")
	if len(forms) != 1 {
		t.Fatalf("期望 1 次 sendMessage，实际 %d 次", len(forms))
	}
	if forms[0].Get("parse_mode") != "MarkdownV2" {
		t.Errorf("parse_mode 错误：%q", forms[0].Get("parse_mode"))
	}
	if forms[0].Get("text") != "你好" {
		t.Errorf("文本错误：%q", forms[0].Get("text"))
	}
	if forms[0].Get("reply_to_message_id") != "42" {
		t.Errorf("未引用原消息：%q", forms[0].Get("reply_to_message_id"))
	}
	if fake.count("deleteMessage") != 0 {
		t.Error("msg_del_delay 为 0 时不应删除消息")
	}
}

// TestReplyNotReplyWhenReplyToZero 校验 replyTo 为 0 时不引用消息。
func TestReplyNotReplyWhenReplyToZero(t *testing.T) {
	b, fake := newTestBot(t, 0, 0, 0)
	if err := b.Reply(-100, "你好", 0); err != nil {
		t.Fatalf("发送失败：%v", err)
	}
	if got := fake.forms("sendMessage")[0].Get("reply_to_message_id"); got != "" {
		t.Errorf("不应引用消息，实际 %q", got)
	}
}

// TestReplySplitsLongText 校验超长文本分片发送，且只有第一片引用原消息。
func TestReplySplitsLongText(t *testing.T) {
	b, fake := newTestBot(t, 0, 0, 0)
	if err := b.Reply(-100, strings.Repeat("战", maxMessageLen+10), 42); err != nil {
		t.Fatalf("发送失败：%v", err)
	}
	forms := fake.forms("sendMessage")
	if len(forms) != 2 {
		t.Fatalf("期望分 2 片，实际 %d 片", len(forms))
	}
	if forms[0].Get("reply_to_message_id") != "42" {
		t.Errorf("第一片应引用原消息：%q", forms[0].Get("reply_to_message_id"))
	}
	if forms[1].Get("reply_to_message_id") != "" {
		t.Errorf("第二片不应引用原消息：%q", forms[1].Get("reply_to_message_id"))
	}
	total := 0
	for _, f := range forms {
		total += len([]rune(f.Get("text")))
	}
	if total != maxMessageLen+10 {
		t.Errorf("分片后原文丢失：%d", total)
	}
}

// TestPingEscapesMarkdownAndDeletesAfterDelay 验证发送给 Telegram 的转义正文与延迟清理。
func TestPingEscapesMarkdownAndDeletesAfterDelay(t *testing.T) {
	b, fake := newTestBot(t, 30*time.Millisecond, 0, 0)
	if err := b.Ping(-100, 0); err != nil {
		t.Fatalf("发送失败：%v", err)
	}
	form := fake.forms("sendMessage")[0]
	if form.Get("parse_mode") != "MarkdownV2" || form.Get("text") != `Pong\!` {
		t.Fatalf("ping 必须发送合法的 MarkdownV2 正文，实际 %v", form)
	}
	fake.waitForMethod(t, "deleteMessage")
	if got := fake.forms("deleteMessage")[0].Get("message_id"); got != "777" {
		t.Errorf("删除的消息 id 错误：%q", got)
	}
}

// TestReplySendError 校验发送失败返回带上下文的错误。
func TestReplySendError(t *testing.T) {
	b, _ := newTestBot(t, 0, http.StatusBadRequest, 0)
	err := b.Ping(-100, 0)
	if err == nil {
		t.Fatal("发送失败应返回错误")
	}
	if !strings.Contains(err.Error(), "发送消息失败") {
		t.Fatalf("错误信息缺少上下文：%v", err)
	}
}

// TestGroupID 校验配置里的目标群 id 可以读回。
func TestGroupID(t *testing.T) {
	b, _ := newTestBot(t, 0, 0, 0)
	if b.GroupID() != -100 {
		t.Fatalf("GroupID 错误：%d", b.GroupID())
	}
	if !b.IsTargetChat(-100) || b.IsTargetChat(-200) {
		t.Fatal("IsTargetChat 判断错误")
	}
}

// syncBuffer 是并发安全的日志缓冲，供后台 goroutine 与用例同时读写。
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write 实现 io.Writer。
func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// String 返回已写入的日志内容。
func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLog 临时接管标准日志，返回缓冲供用例断言。
func captureLog(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	oldWriter, oldFlags := log.Writer(), log.Flags()
	log.SetOutput(buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
	})
	return buf
}

// TestReplyDeleteSuccessLogsNothing 校验删除成功时不会刷出假的失败日志。
func TestReplyDeleteSuccessLogsNothing(t *testing.T) {
	b, fake := newTestBot(t, 10*time.Millisecond, 0, 0)
	// 等上一个用例的后台删除 goroutine 收尾，避免它的日志串到本用例的缓冲里
	time.Sleep(150 * time.Millisecond)
	logs := captureLog(t)

	if err := b.Ping(-100, 0); err != nil {
		t.Fatalf("发送失败：%v", err)
	}
	fake.waitForMethod(t, "deleteMessage")
	time.Sleep(100 * time.Millisecond)

	if strings.Contains(logs.String(), "删除消息失败") {
		t.Fatalf("删除成功却记了失败日志：\n%s", logs.String())
	}
}

// TestReplyDeleteFailureIsLogged 校验删除失败只记日志、不上抛错误。
func TestReplyDeleteFailureIsLogged(t *testing.T) {
	b, fake := newTestBot(t, 10*time.Millisecond, 0, http.StatusBadRequest)
	logs := captureLog(t)

	if err := b.Ping(-100, 0); err != nil {
		t.Fatalf("删除失败不应让回复失败：%v", err)
	}
	fake.waitForMethod(t, "deleteMessage")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(logs.String(), "删除消息失败") {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("删除失败未记日志：\n%s", logs.String())
}

// setDeleteFailFirst 设置「前 N 次删除失败」，与假服务器并发读写同一字段，必须加锁。
func (f *fakeTelegram) setDeleteFailFirst(n int) {
	f.mu.Lock()
	f.deleteFailFirst = n
	f.mu.Unlock()
}

// setDeleteDesc 设置删除失败响应的 description。
func (f *fakeTelegram) setDeleteDesc(desc string) {
	f.mu.Lock()
	f.deleteDesc = desc
	f.mu.Unlock()
}

// waitForCount 等待某个方法至少被调用 want 次，用于观察重试行为。
func (f *fakeTelegram) waitForCount(t *testing.T, method string, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if f.count(method) >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s 调用次数未达到 %d，实际 %d", method, want, f.count(method))
}

// TestDeleteRetriesTransientFailure 校验删除遇到传输类错误会重试到成功，且不刷失败日志。
// 现场来源：真实群里 5 条回复的删除全部因 "unexpected EOF" 失败、消息永久留在群里。
func TestDeleteRetriesTransientFailure(t *testing.T) {
	// 等上一个用例的后台删除 goroutine 收尾，避免它的日志串到本用例的缓冲里
	time.Sleep(150 * time.Millisecond)
	// deleteCode 留 0：前两次失败、第三次成功，正好验证「重试后不再报错」
	b, fake := newTestBot(t, 10*time.Millisecond, 0, 0)
	fake.setDeleteFailFirst(2)
	logs := captureLog(t)

	if err := b.Ping(-100, 0); err != nil {
		t.Fatalf("发送失败：%v", err)
	}
	fake.waitForCount(t, "deleteMessage", 3)
	time.Sleep(100 * time.Millisecond)

	if got := fake.count("deleteMessage"); got != 3 {
		t.Fatalf("前两次失败后应在第 3 次停止，实际调用 %d 次", got)
	}
	if strings.Contains(logs.String(), "删除消息失败") {
		t.Fatalf("重试后成功却记了失败日志：\n%s", logs.String())
	}
}

// TestDeleteTreatsMessageGoneAsSuccess 校验「消息已不存在」按成功处理：不重试也不记失败。
func TestDeleteTreatsMessageGoneAsSuccess(t *testing.T) {
	time.Sleep(150 * time.Millisecond)
	b, fake := newTestBot(t, 10*time.Millisecond, 0, http.StatusBadRequest)
	fake.setDeleteDesc("Bad Request: message to delete not found")
	logs := captureLog(t)

	if err := b.Ping(-100, 0); err != nil {
		t.Fatalf("发送失败：%v", err)
	}
	fake.waitForMethod(t, "deleteMessage")
	time.Sleep(150 * time.Millisecond)

	if got := fake.count("deleteMessage"); got != 1 {
		t.Fatalf("消息已不存在时不应重试，实际调用 %d 次", got)
	}
	if strings.Contains(logs.String(), "删除消息失败") {
		t.Fatalf("消息已不存在却记为失败：\n%s", logs.String())
	}
}

// TestDeleteGivesUpAfterAttempts 校验重试用尽后只记一次失败日志，不再无限重试。
func TestDeleteGivesUpAfterAttempts(t *testing.T) {
	time.Sleep(150 * time.Millisecond)
	b, fake := newTestBot(t, 10*time.Millisecond, 0, http.StatusInternalServerError)
	logs := captureLog(t)

	// 删除失败不应该让回复本身失败
	if err := b.Ping(-100, 0); err != nil {
		t.Fatalf("删除失败不应让回复失败：%v", err)
	}
	fake.waitForCount(t, "deleteMessage", deleteAttemptsDefault)
	time.Sleep(100 * time.Millisecond)

	if got := fake.count("deleteMessage"); got != deleteAttemptsDefault {
		t.Fatalf("应在尝试 %d 次后放弃，实际 %d 次", deleteAttemptsDefault, got)
	}
	if !strings.Contains(logs.String(), "删除消息失败") || !strings.Contains(logs.String(), "尝试=3") {
		t.Fatalf("重试用尽后应记一次带尝试次数的失败日志：\n%s", logs.String())
	}
}
