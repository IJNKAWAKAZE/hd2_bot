package bot

import (
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	tgbotapi "github.com/ijnkawakaze/telegram-bot-api"
)

// updateWith 构造一条来自指定会话的文本消息更新。
func updateWith(chatID int64, text string) tgbotapi.Update {
	return tgbotapi.Update{
		Message: &tgbotapi.Message{
			MessageID: 1,
			Chat:      &tgbotapi.Chat{ID: chatID},
			Text:      text,
		},
	}
}

// resetQueues 清空全局会话队列，避免用例之间互相影响。
func resetQueues() {
	queueMu.Lock()
	chatQueues = make(map[int64]*chatQueue)
	queueMu.Unlock()
}

// TestSplitTextShort 校验短文本不会被拆分。
func TestSplitTextShort(t *testing.T) {
	got := SplitText("你好", 4096)
	if len(got) != 1 || got[0] != "你好" {
		t.Fatalf("短文本不应拆分：%v", got)
	}
}

// TestSplitTextLong 校验超长文本被拆成多片且每片不超过上限。
func TestSplitTextLong(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 200; i++ {
		sb.WriteString("这是一行比较长的内容，用来验证分片逻辑是否正确。\n")
	}
	parts := SplitText(sb.String(), 100)
	if len(parts) < 2 {
		t.Fatalf("超长文本应被拆分，实际 %d 片", len(parts))
	}
	for i, p := range parts {
		if len([]rune(p)) > 100 {
			t.Fatalf("第 %d 片长度 %d 超过上限 100", i+1, len([]rune(p)))
		}
		if !utf8.ValidString(p) {
			t.Fatalf("第 %d 片不是合法 UTF-8，分片切断了多字节字符", i+1)
		}
	}
	if strings.Join(parts, "") != sb.String() {
		t.Fatal("分片内容拼接后应与原文一致")
	}
}

// TestSplitTextSingleLineTooLong 校验单行超长时按字符硬切。
func TestSplitTextSingleLineTooLong(t *testing.T) {
	text := strings.Repeat("a", 250)
	parts := SplitText(text, 100)
	if len(parts) != 3 {
		t.Fatalf("期望 3 片，实际 %d 片", len(parts))
	}
	for _, p := range parts {
		if len([]rune(p)) > 100 {
			t.Fatalf("分片超长：%d", len([]rune(p)))
		}
		if !utf8.ValidString(p) {
			t.Fatalf("分片不是合法 UTF-8：%q", p)
		}
	}
}

// TestEscape 校验 MarkdownV2 特殊字符被转义。
func TestEscape(t *testing.T) {
	got := Escape("1.003.400-win!")
	if !strings.Contains(got, `\.`) || !strings.Contains(got, `\-`) || !strings.Contains(got, `\!`) {
		t.Fatalf("特殊字符未转义：%s", got)
	}
}

// TestEscapeAllSpecials 校验 MarkdownV2 全部保留字符都被转义。
func TestEscapeAllSpecials(t *testing.T) {
	specials := "_*[]()~`>#+-=|{}.!\\"
	got := Escape(specials)
	for _, r := range specials {
		if !strings.Contains(got, `\`+string(r)) {
			t.Errorf("字符 %q 未被转义：%s", r, got)
		}
	}
}

// TestSplitTextNeverCutsEscapePair 校验分片不会把 MarkdownV2 转义序列切成两半。
// 分片末尾留下孤立的反斜杠会让 Telegram 拒绝整条消息，因此任何一片都不能以反斜杠结尾。
func TestSplitTextNeverCutsEscapePair(t *testing.T) {
	text := "." + strings.Repeat(`\.`, 120)
	parts := SplitText(text, 50)
	if strings.Join(parts, "") != text {
		t.Fatal("分片内容拼接后应与原文一致")
	}
	for i, p := range parts {
		if len([]rune(p)) > 50 {
			t.Fatalf("第 %d 片超长：%d", i+1, len([]rune(p)))
		}
		if strings.HasSuffix(p, `\`) {
			t.Fatalf("第 %d 片以孤立反斜杠结尾，转义序列被切断", i+1)
		}
		if !utf8.ValidString(p) {
			t.Fatalf("第 %d 片不是合法 UTF-8", i+1)
		}
	}
}

// TestSplitTextCountsRunes 校验按字符而不是字节计算长度。
// 2000 个汉字占 6000 字节但只有 2000 字符，Telegram 的上限按字符算，不应该分片。
func TestSplitTextCountsRunes(t *testing.T) {
	text := strings.Repeat("战", 2000)
	parts := SplitText(text, 4096)
	if len(parts) != 1 || parts[0] != text {
		t.Fatalf("2000 个汉字未超过 4096 字符上限，不应分片，实际 %d 片", len(parts))
	}
}

// TestChatIDOf 校验各类更新都能取出所属会话。
func TestChatIDOf(t *testing.T) {
	cases := []struct {
		name string
		in   tgbotapi.Update
		want int64
	}{
		{"消息", updateWith(-100, "hi"), -100},
		{"回调", tgbotapi.Update{CallbackQuery: &tgbotapi.CallbackQuery{
			From:    &tgbotapi.User{ID: 7},
			Message: &tgbotapi.Message{Chat: &tgbotapi.Chat{ID: -200}},
		}}, -200},
		{"内联", tgbotapi.Update{InlineQuery: &tgbotapi.InlineQuery{From: &tgbotapi.User{ID: 9}}}, 9},
		{"空更新", tgbotapi.Update{}, 0},
	}
	for _, c := range cases {
		if got := chatIDOf(c.in); got != c.want {
			t.Errorf("%s：期望 %d，实际 %d", c.name, c.want, got)
		}
	}
}

// TestAsyncSerialPerChat 校验同一个会话的指令严格串行执行。
func TestAsyncSerialPerChat(t *testing.T) {
	resetQueues()
	defer resetQueues()

	var mu sync.Mutex
	var order []string
	started := make(chan string, 4)
	release := make(chan struct{})

	handler := async(func(u tgbotapi.Update) error {
		mu.Lock()
		order = append(order, u.Message.Text)
		mu.Unlock()
		started <- u.Message.Text
		<-release
		return nil
	})

	handler(updateWith(1, "a"))
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("第一条指令未开始执行")
	}

	handler(updateWith(1, "b"))
	select {
	case v := <-started:
		t.Fatalf("同一会话的指令必须串行执行，%q 提前启动", v)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("第一条指令结束后第二条未执行")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "a" || order[1] != "b" {
		t.Fatalf("执行顺序错误：%v", order)
	}
}

// TestAsyncParallelAcrossChats 校验不同会话之间互不阻塞。
func TestAsyncParallelAcrossChats(t *testing.T) {
	resetQueues()
	defer resetQueues()

	started := make(chan string, 4)
	release := make(chan struct{})
	handler := async(func(u tgbotapi.Update) error {
		started <- u.Message.Text
		<-release
		return nil
	})

	handler(updateWith(1, "a"))
	handler(updateWith(2, "b"))

	got := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case v := <-started:
			got[v] = true
		case <-time.After(2 * time.Second):
			t.Fatalf("不同会话应并发执行，当前已启动 %v", got)
		}
	}
	close(release)
}

// TestAsyncRecoversPanic 校验指令崩溃不会堵死该会话的后续指令。
func TestAsyncRecoversPanic(t *testing.T) {
	resetQueues()
	defer resetQueues()

	done := make(chan string, 2)
	handler := async(func(u tgbotapi.Update) error {
		if u.Message.Text == "boom" {
			panic("测试用崩溃")
		}
		done <- u.Message.Text
		return nil
	})

	handler(updateWith(1, "boom"))
	handler(updateWith(1, "ok"))

	select {
	case v := <-done:
		if v != "ok" {
			t.Fatalf("意外的任务：%q", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("崩溃的指令堵死了后续指令")
	}
}

// TestEnqueueDropsWhenFull 校验单个会话队列满时丢弃新任务且队列不超上限。
func TestEnqueueDropsWhenFull(t *testing.T) {
	resetQueues()
	defer resetQueues()

	release := make(chan struct{})
	blocked := make(chan struct{})
	enqueue(1, func() {
		close(blocked)
		<-release
	})
	select {
	case <-blocked:
	case <-time.After(2 * time.Second):
		t.Fatal("占位任务未开始执行")
	}

	for i := 0; i < maxQueuePerChat+10; i++ {
		enqueue(1, func() {})
	}

	queueMu.Lock()
	q := chatQueues[1]
	queueMu.Unlock()
	if q == nil {
		t.Fatal("会话队列不存在")
	}
	q.mu.Lock()
	pending := len(q.tasks)
	q.mu.Unlock()
	if pending > maxQueuePerChat {
		t.Fatalf("队列未设上限：%d > %d", pending, maxQueuePerChat)
	}
	if pending == 0 {
		t.Fatal("队列被清空，正常任务不应被丢弃")
	}
	close(release)
}

// TestIsTargetChat 校验只有配置里指定的群会被服务。
func TestIsTargetChat(t *testing.T) {
	b := &Bot{groupID: -100}
	if !b.IsTargetChat(-100) {
		t.Error("目标群应被服务")
	}
	if b.IsTargetChat(-200) {
		t.Error("非目标群不应被服务")
	}
	if (&Bot{}).IsTargetChat(123) != true {
		t.Error("groupID 为 0 表示不限制")
	}
}

// TestSplitTextTinyLimit 校验 limit 为 1 时不会死循环，且内容不丢失。
func TestSplitTextTinyLimit(t *testing.T) {
	text := `\a`
	parts := SplitText(text, 1)
	if strings.Join(parts, "") != text {
		t.Fatalf("分片内容拼接后应与原文一致：%v", parts)
	}
	if len(parts) != 2 {
		t.Fatalf("期望 2 片，实际 %d 片", len(parts))
	}
}

// TestQueueFifoOrder 校验同一会话的指令严格按到达顺序执行（队列是 FIFO）。
// 这条顺序保证很关键：同一个人连发多条指令时，回复顺序不能乱。
func TestQueueFifoOrder(t *testing.T) {
	const chatID = int64(-424242)
	resetQueues()

	var (
		mu    sync.Mutex
		order []string
	)
	firstStarted := make(chan struct{})
	release := make(chan struct{})

	handler := func(name string, block bool) func(tgbotapi.Update) error {
		return func(tgbotapi.Update) error {
			if block {
				close(firstStarted)
				<-release
			}
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			return nil
		}
	}

	update := updateWith(chatID, "/x")
	if err := async(handler("a", true))(update); err != nil {
		t.Fatalf("分发第一条指令失败：%v", err)
	}
	<-firstStarted // 第一条已经在执行，后面的只能排队

	for _, name := range []string{"b", "c", "d"} {
		if err := async(handler(name, false))(update); err != nil {
			t.Fatalf("分发第 %s 条指令失败：%v", name, err)
		}
	}
	// 等队列真的积压到 3 条再放行，不用固定 sleep 猜时序。
	waitUntil(t, func() bool {
		queueMu.Lock()
		q := chatQueues[chatID]
		queueMu.Unlock()
		if q == nil {
			return false
		}
		q.mu.Lock()
		defer q.mu.Unlock()
		return len(q.tasks) == 3
	}, "队列没有积压到 3 条指令")
	close(release)

	waitUntil(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(order) == 4
	}, "指令没有全部执行完")

	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(order, ","); got != "a,b,c,d" {
		t.Errorf("同一会话的指令应按到达顺序执行，实际顺序 %s", got)
	}
}

// waitUntil 轮询等待条件成立，超时则报错；比固定 sleep 稳定。
func waitUntil(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal(msg)
}

// TestSplitTextCountsUTF16 校验按 UTF-16 码元计数：表情占 2 个，3000 个表情（6000 码元）必须分片。
// Telegram 的 4096 上限是按码元算的，按 rune 算会让纯表情长消息被整条拒收。
func TestSplitTextCountsUTF16(t *testing.T) {
	text := strings.Repeat("🙂", 3000)
	parts := SplitText(text, maxMessageLen)
	if len(parts) < 2 {
		t.Fatalf("3000 个表情共 6000 码元，应至少分成 2 片，实际 %d 片", len(parts))
	}
	if strings.Join(parts, "") != text {
		t.Fatal("分片内容拼接后应与原文一致")
	}
	for i, p := range parts {
		if units := len([]rune(p)) * 2; units > maxMessageLen {
			t.Fatalf("第 %d 片有 %d 个码元，超过上限 %d", i+1, units, maxMessageLen)
		}
	}
}
