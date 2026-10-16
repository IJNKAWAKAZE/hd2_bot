// 会话集合的用例：键的构造、去重、取出即移除、以及私聊模式的消息落点。
package gatekeeper

import "testing"

func newTestSession(chatID, userID int64, private bool) *Session {
	challenge, _ := NewChallenge(Target{ID: "x", Steps: []string{DirUp}}, 0)
	return &Session{ChatID: chatID, UserID: userID, Name: "n", Private: private, Challenge: challenge}
}

// TestSessionKeyNoCollision 校验键用分隔符拼接，(12,345) 与 (123,45) 不会撞成一个键。
func TestSessionKeyNoCollision(t *testing.T) {
	if sessionKey(12, 345) == sessionKey(123, 45) {
		t.Fatal("不同用户/群的键不该相同")
	}
}

// TestSessionStoreLifecycle 校验增删查的语义。
func TestSessionStoreLifecycle(t *testing.T) {
	store := newSessionStore()
	session := newTestSession(100, 7, false)

	if !store.add(session) {
		t.Fatal("首次加入应成功")
	}
	if store.add(newTestSession(100, 7, false)) {
		t.Fatal("同一个用户重复加入应失败（不覆盖已有会话）")
	}
	if store.len() != 1 {
		t.Fatalf("应只有 1 个会话，实际 %d", store.len())
	}
	if got, ok := store.get(100, 7); !ok || got != session {
		t.Fatal("应按 (群, 用户) 取回同一个会话")
	}
	if _, ok := store.get(100, 8); ok {
		t.Fatal("不存在的用户不该取到会话")
	}
	if got, ok := store.findByUser(7); !ok || got != session {
		t.Fatal("按用户反查应命中（私聊回调用）")
	}
	if _, ok := store.findByUser(999); ok {
		t.Fatal("没有会话的用户反查应失败")
	}

	// take 取出即移除：第二次取不到。
	if _, ok := store.take(100, 7); !ok {
		t.Fatal("take 应取到会话")
	}
	if _, ok := store.get(100, 7); ok {
		t.Fatal("take 之后不该还能取到")
	}
	if store.len() != 0 {
		t.Fatalf("取出后集合应为空，实际 %d", store.len())
	}

	// takeByKey 与 take 等价（超时任务里只有键）。
	store.add(session)
	if _, ok := store.takeByKey(session.Key()); !ok {
		t.Fatal("按键取出应成功")
	}
	store.add(session)
	store.remove(100, 7)
	if store.len() != 0 {
		t.Fatal("remove 之后集合应为空")
	}
}

// TestSessionMessageChat 校验验证卡片的落点：群内模式是群，申请模式是私聊。
func TestSessionMessageChat(t *testing.T) {
	group := newTestSession(100, 7, false)
	if got := group.MessageChat(); got != 100 {
		t.Errorf("群内模式应发在群里，实际 %d", got)
	}
	private := newTestSession(100, 7, true)
	if got := private.MessageChat(); got != 7 {
		t.Errorf("申请模式应发在用户私聊，实际 %d", got)
	}
}
