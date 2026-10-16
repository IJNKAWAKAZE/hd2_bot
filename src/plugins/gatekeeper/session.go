// session.go 是一次进行中的验证：谁、在哪、卡片在哪条消息上、题目进展到哪。
//
// 会话只放在内存里：验证是分钟级的短流程，进程重启后重来一遍也无所谓
// （重启后新成员还留在禁言状态，管理员手动放行即可；把会话写盘反而要处理一堆过期数据）。
package gatekeeper

import (
	"fmt"
	"sync"
)

// Session 是一次进行中的验证。
type Session struct {
	ChatID int64 // 群 id；私聊验证时仍然是群 id（验证针对的是那个群）
	UserID int64
	Name   string
	// Private 为 true 表示这是「申请加入」模式的私聊验证，卡片发在用户私聊里。
	Private bool
	// PMChatID 是联系申请人用的私聊标识，来自入群申请的 user_chat_id。
	// Telegram 只保证机器人能用它联系申请人 —— 申请人多半从没主动给机器人发过消息，
	// 直接按用户 id 发私聊会被拒掉；直接加入模式用不到，恒为 0。
	PMChatID int64
	// MessageID 是这次验证那条消息的 id：题面图、提示文本与按钮全在这一条上。
	MessageID int64
	// HasPhoto 说明那条消息是「图 + 说明」形态（true）还是纯文本形态（false）。
	// 题面图出不来时（没配渲染器、图标下载失败、渲染失败、发图失败）退成纯文本，答案这时补在
	// 文本里；刷新进度也因此分成编辑说明（caption）与编辑文本两条路径。
	HasPhoto bool
	// JoinID 是群里那条「XX 加入了群组」的系统消息，收尾时删掉它。
	JoinID int64

	Challenge *Challenge
}

// Key 是会话的键：同一个群里的同一个用户。
func (s *Session) Key() string { return sessionKey(s.ChatID, s.UserID) }

// MessageChat 返回验证消息所在的会话：群内模式是群，申请模式是申请人的私聊
// （用 user_chat_id；取不到时退回用户 id —— 老版本数据与测试里可能出现）。
func (s *Session) MessageChat() int64 {
	if s.Private {
		if s.PMChatID != 0 {
			return s.PMChatID
		}
		return s.UserID
	}
	return s.ChatID
}

// sessionKey 拼会话键。用固定分隔符而不是拼接数字，避免 (12, 345) 与 (123, 45) 撞成一个键。
func sessionKey(chatID, userID int64) string {
	return fmt.Sprintf("%d:%d", chatID, userID)
}

// sessionStore 是进行中的验证集合。处理器是并发跑的（更新队列），所以这里必须加锁。
type sessionStore struct {
	mu    sync.Mutex
	items map[string]*Session
}

func newSessionStore() *sessionStore {
	return &sessionStore{items: make(map[string]*Session)}
}

// add 放入一个会话；同一个用户已经有会话时返回 false（不覆盖，避免并发写入互相打架）。
func (s *sessionStore) add(session *Session) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := session.Key()
	if _, exists := s.items[key]; exists {
		return false
	}
	s.items[key] = session
	return true
}

// get 取一个会话（不移除）。
func (s *sessionStore) get(chatID, userID int64) (*Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.items[sessionKey(chatID, userID)]
	return session, ok
}

// take 取出并移除一个会话：通过、失败、超时都会走这里，保证只结算一次。
func (s *sessionStore) take(chatID, userID int64) (*Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := sessionKey(chatID, userID)
	session, ok := s.items[key]
	if ok {
		delete(s.items, key)
	}
	return session, ok
}

// takeByKey 与 take 相同，但用会话键直接定位（超时任务里只有键）。
func (s *sessionStore) takeByKey(key string) (*Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.items[key]
	if ok {
		delete(s.items, key)
	}
	return session, ok
}

// findByUser 按用户 id 找会话（不看群）。申请模式的回调发生在用户私聊里，
// 从回调里看不出是哪个群，而本机器人只服务一个群，所以按用户反查是安全的。
// 会话是个位数级别，线性扫一遍足够，不值得再维护一张索引。
func (s *sessionStore) findByUser(userID int64) (*Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, session := range s.items {
		if session.UserID == userID {
			return session, true
		}
	}
	return nil, false
}

// remove 移除一个会话。
func (s *sessionStore) remove(chatID, userID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, sessionKey(chatID, userID))
}

// len 返回进行中的验证数（日志与测试用）。
func (s *sessionStore) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.items)
}
