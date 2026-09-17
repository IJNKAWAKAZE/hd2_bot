package hd2

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Cache 是带 TTL 的内存缓存，并对同一 key 的并发请求做单飞合并。
//
// 语义约定：
//   - 只有成功结果才写入缓存；取数失败或 panic 都不写，下次调用会重新取数；
//   - 同一 key 的并发调用共享同一次取数结果（单飞），等待者拿到与 leader 相同的结果或错误；
//   - ttl <= 0 表示不写缓存，每次调用都会重新取数；
//   - Invalidate 会让正在进行中的取数结果作废，不会把旧值回填进缓存。
type Cache struct {
	now func() time.Time

	mu          sync.Mutex
	entries     map[string]cacheEntry
	inflight    map[string]*flight
	generations map[string]uint64
}

type cacheEntry struct {
	value    any
	cachedAt time.Time // 写入缓存的时间，供展示层标注「数据时间」
	expire   time.Time
}

// flight 表示一次进行中的取数，等待者通过 done 通道共享结果。
type flight struct {
	done chan struct{}
	// value/err 是本次取数的结果；leaderCanceled 标记失败是否源自 leader 的 ctx 取消或超时，
	// 供 ctx 仍然有效的等待者据此重新发起一次取数。
	value          any
	err            error
	leaderCanceled bool
}

// NewCache 创建缓存，使用系统时钟。
func NewCache() *Cache {
	return &Cache{
		now:         time.Now,
		entries:     make(map[string]cacheEntry),
		inflight:    make(map[string]*flight),
		generations: make(map[string]uint64),
	}
}

// Fetch 读取缓存，未命中则执行 fn 并写入缓存；同一 key 的并发调用只会执行一次 fn。
// fn panic 时不会让本次取数卡死，而是返回错误并照常清理，后续调用可以重新取数。
//
// 注意：返回值与缓存条目共享底层数据（切片与指针不会被复制），调用方不得就地修改，
// 否则会污染后续所有调用者；需要改数据的调用方请自己复制一份，Service 就是这么做的。
func Fetch[T any](c *Cache, ctx context.Context, key string, ttl time.Duration, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	v, err := c.do(ctx, key, ttl, func(ctx context.Context) (any, error) { return fn(ctx) })
	if err != nil {
		return zero, err
	}
	tv, ok := v.(T)
	if !ok {
		// 同一个 key 被不同类型复用：删掉这条脏数据，避免该 key 之后一直取到类型不匹配的错误。
		c.mu.Lock()
		delete(c.entries, key)
		c.mu.Unlock()
		return zero, fmt.Errorf("缓存中的数据类型与调用方不一致：期望 %T，实际 %T，key=%s", zero, v, key)
	}
	return tv, nil
}

// Invalidate 主动失效某个 key，下次取值会重新请求上游。
// 进行中的取数不会被打断，其结果会因为世代号变化而不再写回缓存；
// 但注意：在旧取数完成之前到达的调用仍会加入那一次取数并拿到失效前的旧值
// （只是不写回缓存），需要「立刻拿到新值」的调用方应自己加锁或稍后重试。
func (c *Cache) Invalidate(key string) {
	c.mu.Lock()
	delete(c.entries, key)
	c.generations[key]++
	c.mu.Unlock()
}

// FetchedAt 返回 key 当前缓存条目的写入时间；未命中或已过期时 ok 为 false。
// 展示层用它标注「数据时间」，避免把「本次请求时间」误当成数据时间。
func (c *Cache) FetchedAt(key string) (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || !c.now().Before(e.expire) {
		return time.Time{}, false
	}
	return e.cachedAt, true
}

// do 是 Fetch 的无类型实现，负责 TTL 判定、单飞合并与结果写回。
func (c *Cache) do(ctx context.Context, key string, ttl time.Duration, fn func(context.Context) (any, error)) (value any, err error) {
	for attempt := 0; ; attempt++ {
		c.mu.Lock()
		if e, ok := c.entries[key]; ok && c.now().Before(e.expire) {
			c.mu.Unlock()
			return e.value, nil
		}
		if f, ok := c.inflight[key]; ok {
			c.mu.Unlock()
			select {
			case <-f.done:
				// leader 的 ctx 被取消或超时时，只要等待者自己的 ctx 还有效，就重新发起一次取数，
				// 避免一个命令超时把同 key 的其它命令一起拖失败；最多重试一次，防止无限循环。
				if f.err != nil && f.leaderCanceled && ctx.Err() == nil && attempt == 0 {
					continue
				}
				return f.value, f.err
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		gen := c.generations[key]
		f := &flight{done: make(chan struct{})}
		c.inflight[key] = f
		c.mu.Unlock()

		// 无论正常返回、出错还是 panic，都必须清掉 inflight 并唤醒等待者，
		// 否则该 key 会永久卡死（之后不再取数、等待者永久阻塞）。
		// finished 说明 fn 是否正常返回：panic(nil) 与 runtime.Goexit() 这两条路径
		// recover() 都返回 nil，只看它会误把 nil 当成正常结果写进缓存。
		finished := false
		defer func() {
			if !finished {
				if r := recover(); r != nil {
					err = fmt.Errorf("取数函数 panic：key=%s: %v", key, r)
				} else {
					err = fmt.Errorf("取数函数未正常返回（panic(nil) 或 runtime.Goexit）：key=%s", key)
				}
			}
			c.mu.Lock()
			delete(c.inflight, key)
			// 失败结果不缓存；ttl <= 0 不缓存；期间的 Invalidate 会让世代号变化，旧值同样丢弃。
			if err == nil && ttl > 0 && c.generations[key] == gen {
				at := c.now()
				c.entries[key] = cacheEntry{value: value, cachedAt: at, expire: at.Add(ttl)}
			}
			c.mu.Unlock()
			f.value, f.err = value, err
			f.leaderCanceled = err != nil && ctx.Err() != nil
			close(f.done)
		}()

		value, err = fn(ctx)
		finished = true
		return
	}
}
