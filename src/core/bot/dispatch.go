// Package bot 封装 Telegram 客户端：异步分发指令、注册命令、发送消息。
// 本包是唯一直接依赖 telegram-bot-api 的包，插件通过 Handler 与 Sender 与它交互。
package bot

import (
	"log"
	"runtime/debug"
	"sync"

	tgbotapi "github.com/ijnkawakaze/telegram-bot-api"
)

// maxConcurrentHandlers 限制同时执行的指令数量，避免大量请求同时占用资源。
const maxConcurrentHandlers = 4

// maxQueuePerChat 是单个会话等待执行的任务上限。
// 队列满时丢弃新来的指令并记日志：机器人只服务一个固定群，正常使用远达不到这个上限，
// 一旦达到说明有人在刷指令，此时宁可丢掉后来的指令，也不能让等待队列无限增长。
const maxQueuePerChat = 64

var (
	queueMu    sync.Mutex
	chatQueues = make(map[int64]*chatQueue)
	workerSem  = make(chan struct{}, maxConcurrentHandlers)
)

// chatQueue 保证同一个会话内的指令按顺序串行执行，不同会话之间互不影响。
// 消息库的 Run 循环本身是串行的，一个慢指令会堵住所有人，这里改成异步分发。
type chatQueue struct {
	mu      sync.Mutex
	tasks   []func()
	running bool
}

// chatIDOf 取出更新所属的会话，用于隔离不同会话的执行顺序；取不到时返回 0。
func chatIDOf(update tgbotapi.Update) int64 {
	switch {
	case update.Message != nil && update.Message.Chat != nil:
		return update.Message.Chat.ID
	case update.CallbackQuery != nil:
		if update.CallbackQuery.Message != nil && update.CallbackQuery.Message.Chat != nil {
			return update.CallbackQuery.Message.Chat.ID
		}
		if update.CallbackQuery.From != nil {
			return update.CallbackQuery.From.ID
		}
	case update.InlineQuery != nil && update.InlineQuery.From != nil:
		return update.InlineQuery.From.ID
	}
	return 0
}

// async 把处理器放到后台执行，消息循环可以立刻继续处理下一个更新。
// 处理器内部的 panic 会被兜住并记日志，不会带走进程，也不会堵死该会话的后续指令。
// 返回的闭包始终返回 nil：错误只记日志，不再上抛给消息库。
func async(handler func(tgbotapi.Update) error) func(tgbotapi.Update) error {
	return func(update tgbotapi.Update) error {
		enqueue(chatIDOf(update), func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("指令执行崩溃：%v\n%s", r, string(debug.Stack()))
				}
			}()
			workerSem <- struct{}{}
			defer func() { <-workerSem }()
			if err := handler(update); err != nil {
				log.Printf("指令执行失败：%v", err)
			}
		})
		return nil
	}
}

// enqueue 把任务加入指定会话的队列，队列空闲时启动执行。
// 队列已满时丢弃新任务并记日志：调用方是消息接收循环，阻塞它会连收消息都做不到。
func enqueue(id int64, task func()) {
	queueMu.Lock()
	q := chatQueues[id]
	if q == nil {
		q = &chatQueue{}
		chatQueues[id] = q
	}
	queueMu.Unlock()

	q.mu.Lock()
	if len(q.tasks) >= maxQueuePerChat {
		q.mu.Unlock()
		log.Printf("会话 %d 的指令队列已满（上限 %d），丢弃新指令", id, maxQueuePerChat)
		return
	}
	q.tasks = append(q.tasks, task)
	start := !q.running
	if start {
		q.running = true
	}
	q.mu.Unlock()

	if start {
		go q.run()
	}
}

// run 串行执行队列中的任务，执行完自动退出（不留常驻 goroutine）。
func (q *chatQueue) run() {
	for {
		q.mu.Lock()
		if len(q.tasks) == 0 {
			q.running = false
			q.mu.Unlock()
			return
		}
		task := q.tasks[0]
		q.tasks[0] = nil
		q.tasks = q.tasks[1:]
		q.mu.Unlock()

		task()
	}
}
