package bot

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/ijnkawakaze/telegram-bot-api"
)

// withStartupPolicy 替换建连钩子与重试参数，用例结束后还原。
func withStartupPolicy(t *testing.T, fn func(string) (*tgbotapi.BotAPI, error), attempts int, delay time.Duration) {
	t.Helper()
	oldFn, oldAttempts, oldDelay := newTelegramAPI, initAttempts, initRetryDelay
	newTelegramAPI, initAttempts, initRetryDelay = fn, attempts, delay
	t.Cleanup(func() { newTelegramAPI, initAttempts, initRetryDelay = oldFn, oldAttempts, oldDelay })
}

// TestNewRetriesTelegramConnection 校验建连失败会重试：前两次报 EOF、第三次成功。
// 现场来源：真实运行时 NewBotAPI 内部的 getMe 撞上代理抖动直接 EOF，进程刚启动就退出。
func TestNewRetriesTelegramConnection(t *testing.T) {
	time.Sleep(150 * time.Millisecond)
	fake := &fakeTelegram{}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)

	calls := 0
	withStartupPolicy(t, func(string) (*tgbotapi.BotAPI, error) {
		calls++
		if calls <= 2 {
			return nil, errors.New("EOF")
		}
		return tgbotapi.NewBotAPIWithClient("TOKEN", server.URL+"/bot%s/%s", server.Client())
	}, 3, time.Millisecond)
	logs := captureLog(t)

	b, err := New("TOKEN", 0, -100, time.Second, 2*time.Second)
	if err != nil {
		t.Fatalf("重试后应建连成功，实际报错：%v", err)
	}
	if calls != 3 {
		t.Fatalf("应在第 3 次建连成功，实际调用 %d 次", calls)
	}
	if b.GroupID() != -100 {
		t.Fatalf("groupID 应透传，实际 %d", b.GroupID())
	}
	if b.photoDelay != 2*time.Second {
		t.Fatalf("photoDelay 应透传，实际 %s", b.photoDelay)
	}
	if !strings.Contains(logs.String(), "连接 Telegram 失败（第 1 次）") {
		t.Fatalf("建连失败应记日志便于排查：\n%s", logs.String())
	}
}

// TestNewFailsAfterAttempts 校验建连始终失败时返回带中文上下文的错误，且不再无限重试。
func TestNewFailsAfterAttempts(t *testing.T) {
	calls := 0
	withStartupPolicy(t, func(string) (*tgbotapi.BotAPI, error) {
		calls++
		return nil, errors.New("EOF")
	}, 3, time.Millisecond)

	_, err := New("TOKEN", 0, -100, 0, 0)
	if err == nil {
		t.Fatal("建连始终失败时应返回错误")
	}
	if calls != 3 {
		t.Fatalf("应尝试 3 次，实际 %d 次", calls)
	}
	if !strings.Contains(err.Error(), "初始化 Telegram 机器人失败") {
		t.Fatalf("错误应保留中文上下文，实际：%v", err)
	}
}
