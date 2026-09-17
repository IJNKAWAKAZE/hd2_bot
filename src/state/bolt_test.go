package state

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

// 测试用的固定时间点，便于断言时间字段的往返。
var testAt = time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

// openTest 在临时目录里打开一个状态文件，并在用例结束时关闭。
func openTest(t *testing.T, name string) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("打开状态文件失败：%v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestSnapshotRoundTrip 校验快照写入与读取。
func TestSnapshotRoundTrip(t *testing.T) {
	s := openTest(t, filepath.Join("data", "hd2.db"))
	if err := s.PutSnapshot("war", []byte(`{"now":"2026-09-16T12:00:00Z"}`), testAt); err != nil {
		t.Fatalf("写入快照失败：%v", err)
	}
	data, fetchedAt, ok, err := s.GetSnapshot("war")
	if err != nil || !ok {
		t.Fatalf("读取快照失败：ok=%v err=%v", ok, err)
	}
	if string(data) != `{"now":"2026-09-16T12:00:00Z"}` {
		t.Errorf("快照内容错误：%s", data)
	}
	if !fetchedAt.Equal(testAt) {
		t.Errorf("抓取时间错误：期望 %v，实际 %v", testAt, fetchedAt)
	}
}

// TestSnapshotCreatesParentDir 校验父目录不存在时会自动创建。
func TestSnapshotCreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deeper", "hd2.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("打开状态文件失败：%v", err)
	}
	defer func() { _ = s.Close() }()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("状态文件未创建：%v", err)
	}
}

// TestSnapshotMissing 校验读取不存在的快照时 ok 为 false。
func TestSnapshotMissing(t *testing.T) {
	s := openTest(t, "hd2.db")
	if _, _, ok, err := s.GetSnapshot("planets"); ok || err != nil {
		t.Fatalf("不存在的快照应返回 ok=false，实际 ok=%v err=%v", ok, err)
	}
}

// TestSnapshotNoAlias 校验读取到的快照是副本，调用方改写不会污染存储。
func TestSnapshotNoAlias(t *testing.T) {
	s := openTest(t, "hd2.db")
	if err := s.PutSnapshot("war", []byte(`{"a":1}`), testAt); err != nil {
		t.Fatalf("写入快照失败：%v", err)
	}
	data, _, _, err := s.GetSnapshot("war")
	if err != nil {
		t.Fatalf("读取快照失败：%v", err)
	}
	for i := range data {
		data[i] = 'x'
	}
	again, _, _, err := s.GetSnapshot("war")
	if err != nil {
		t.Fatalf("再次读取快照失败：%v", err)
	}
	if string(again) != `{"a":1}` {
		t.Errorf("快照被外部改写污染：%s", again)
	}
}

// TestSnapshotOverwrite 校验同名快照会被覆盖，且抓取时间同步更新。
func TestSnapshotOverwrite(t *testing.T) {
	s := openTest(t, "hd2.db")
	if err := s.PutSnapshot("war", []byte(`{"a":1}`), testAt); err != nil {
		t.Fatalf("写入快照失败：%v", err)
	}
	later := testAt.Add(time.Minute)
	if err := s.PutSnapshot("war", []byte(`{"a":2}`), later); err != nil {
		t.Fatalf("覆盖快照失败：%v", err)
	}
	data, fetchedAt, ok, err := s.GetSnapshot("war")
	if err != nil || !ok {
		t.Fatalf("读取快照失败：ok=%v err=%v", ok, err)
	}
	if string(data) != `{"a":2}` || !fetchedAt.Equal(later) {
		t.Errorf("覆盖后内容或时间错误：data=%s at=%v", data, fetchedAt)
	}
}

// TestPutSnapshotInvalidJSON 校验非法 JSON 会被拒绝，不写入脏数据。
func TestPutSnapshotInvalidJSON(t *testing.T) {
	s := openTest(t, "hd2.db")
	if err := s.PutSnapshot("war", []byte(`{"a":`), testAt); err == nil {
		t.Fatal("非法 JSON 应返回错误")
	}
	if _, _, ok, err := s.GetSnapshot("war"); ok || err != nil {
		t.Fatalf("非法 JSON 不应写入快照，实际 ok=%v err=%v", ok, err)
	}
}

// TestSnapshotCorruptPayload 校验桶内数据损坏时报错而不是返回脏数据。
func TestSnapshotCorruptPayload(t *testing.T) {
	s := openTest(t, "hd2.db")
	err := s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketSnapshot).Put([]byte("war"), []byte("不是 JSON"))
	})
	if err != nil {
		t.Fatalf("写入损坏数据失败：%v", err)
	}
	if _, _, ok, err := s.GetSnapshot("war"); err == nil || ok {
		t.Fatalf("损坏数据应返回错误，实际 ok=%v err=%v", ok, err)
	}
}

// TestPersistence 校验关闭重开后数据仍在。
func TestPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hd2.db")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("打开状态文件失败：%v", err)
	}
	if err := s.PutSnapshot("war", []byte(`{"a":1}`), testAt); err != nil {
		t.Fatalf("写入快照失败：%v", err)
	}
	if _, err := s.MarkPushed("dispatch", "12345", testAt); err != nil {
		t.Fatalf("记录推送失败：%v", err)
	}
	if err := s.SetMeta("last_poll", "2026-09-16T12:00:00Z"); err != nil {
		t.Fatalf("写入元数据失败：%v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("关闭状态文件失败：%v", err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("重新打开状态文件失败：%v", err)
	}
	defer func() { _ = s2.Close() }()

	if _, _, ok, _ := s2.GetSnapshot("war"); !ok {
		t.Error("重开后快照丢失")
	}
	if pushed, _ := s2.HasPushed("dispatch", "12345"); !pushed {
		t.Error("重开后推送状态丢失")
	}
	if v, ok, _ := s2.GetMeta("last_poll"); !ok || v != "2026-09-16T12:00:00Z" {
		t.Errorf("重开后元数据错误：%s ok=%v", v, ok)
	}
}

// TestMarkPushed 校验首次记录返回 true、重复记录返回 false。
func TestMarkPushed(t *testing.T) {
	s := openTest(t, "hd2.db")
	first, err := s.MarkPushed("dispatch", "1", testAt)
	if err != nil || !first {
		t.Fatalf("首次记录应返回 true：first=%v err=%v", first, err)
	}
	second, err := s.MarkPushed("dispatch", "1", testAt.Add(time.Minute))
	if err != nil || second {
		t.Fatalf("重复记录应返回 false：second=%v err=%v", second, err)
	}
	if pushed, err := s.HasPushed("dispatch", "1"); err != nil || !pushed {
		t.Fatalf("已推送的事件应命中：pushed=%v err=%v", pushed, err)
	}
}

// TestMarkPushedKindIsolation 校验不同 kind 的同名 id 互不影响。
func TestMarkPushedKindIsolation(t *testing.T) {
	s := openTest(t, "hd2.db")
	if first, err := s.MarkPushed("dispatch", "7", testAt); err != nil || !first {
		t.Fatalf("首次记录应返回 true：first=%v err=%v", first, err)
	}
	if first, err := s.MarkPushed("event", "7", testAt); err != nil || !first {
		t.Fatalf("不同 kind 应视为新事件：first=%v err=%v", first, err)
	}
}

// TestHasPushedMissing 校验未推送过的事件返回 false 且不报错。
func TestHasPushedMissing(t *testing.T) {
	s := openTest(t, "hd2.db")
	pushed, err := s.HasPushed("dispatch", "999")
	if err != nil || pushed {
		t.Fatalf("未推送的事件应返回 false：pushed=%v err=%v", pushed, err)
	}
}

// TestMarkPushedConcurrent 校验并发记录同一事件时只有一个调用拿到 true。
func TestMarkPushedConcurrent(t *testing.T) {
	s := openTest(t, "hd2.db")
	const n = 32
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		first int
		fails []error
	)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			ok, err := s.MarkPushed("dispatch", "42", testAt)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				fails = append(fails, err)
				return
			}
			if ok {
				first++
			}
		}()
	}
	wg.Wait()
	if len(fails) > 0 {
		t.Fatalf("并发记录出错：%v", fails[0])
	}
	if first != 1 {
		t.Errorf("并发记录应恰好一次返回 true，实际 %d 次", first)
	}
}

// TestMeta 校验元数据的写入、覆盖与缺失。
func TestMeta(t *testing.T) {
	s := openTest(t, "hd2.db")
	if _, ok, err := s.GetMeta("last_poll"); ok || err != nil {
		t.Fatalf("缺失的元数据应返回 ok=false：ok=%v err=%v", ok, err)
	}
	if err := s.SetMeta("last_poll", "t1"); err != nil {
		t.Fatalf("写入元数据失败：%v", err)
	}
	if err := s.SetMeta("last_poll", "t2"); err != nil {
		t.Fatalf("覆盖元数据失败：%v", err)
	}
	v, ok, err := s.GetMeta("last_poll")
	if err != nil || !ok || v != "t2" {
		t.Fatalf("元数据读取错误：v=%s ok=%v err=%v", v, ok, err)
	}
	if _, ok, err := s.GetMeta("other"); ok || err != nil {
		t.Fatalf("缺失的元数据应返回 ok=false：ok=%v err=%v", ok, err)
	}
}

// TestOpenLocked 校验同一文件被占用时第二次打开会报错。
func TestOpenLocked(t *testing.T) {
	// 把等待锁的时间调小，避免用例白等 3 秒。
	old := openTimeout
	openTimeout = 10 * time.Millisecond
	t.Cleanup(func() { openTimeout = old })

	path := filepath.Join(t.TempDir(), "hd2.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("打开状态文件失败：%v", err)
	}
	defer func() { _ = s.Close() }()

	if _, err := Open(path); err == nil {
		t.Fatal("同一文件被占用时第二次打开应返回错误")
	}
}

// TestOpenMkdirFailed 校验父路径被文件占位时给出明确错误。
func TestOpenMkdirFailed(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("准备占位文件失败：%v", err)
	}
	if _, err := Open(filepath.Join(blocker, "hd2.db")); err == nil {
		t.Fatal("父路径被文件占位时应返回错误")
	}
}

// TestMethodsAfterClose 校验关闭后所有读写方法都返回错误而不是 panic。
func TestMethodsAfterClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hd2.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("打开状态文件失败：%v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("关闭状态文件失败：%v", err)
	}

	if err := s.PutSnapshot("war", []byte(`{"a":1}`), testAt); err == nil {
		t.Error("关闭后写入快照应返回错误")
	}
	if _, _, _, err := s.GetSnapshot("war"); err == nil {
		t.Error("关闭后读取快照应返回错误")
	}
	if _, err := s.MarkPushed("dispatch", "1", testAt); err == nil {
		t.Error("关闭后记录推送应返回错误")
	}
	if _, err := s.HasPushed("dispatch", "1"); err == nil {
		t.Error("关闭后查询推送应返回错误")
	}
	if err := s.SetMeta("k", "v"); err == nil {
		t.Error("关闭后写入元数据应返回错误")
	}
	if _, _, err := s.GetMeta("k"); err == nil {
		t.Error("关闭后读取元数据应返回错误")
	}
	// 通用键值桶同样要在关闭后返回错误而不是 panic：Task 9 的推送协程会在停机时撞上这条路径。
	if err := s.PutKV("translate", "abc", "v"); err == nil {
		t.Error("关闭后写入键值应返回错误")
	}
	if _, _, err := s.GetKV("translate", "abc"); err == nil {
		t.Error("关闭后读取键值应返回错误")
	}
}

// TestBucketNamesAndValueFormats 校验桶名与 pushstate 的值格式。
// 这是跨版本的持久化契约：改名或改格式会让旧文件里的推送记录读不到，重启后会重复推送。
func TestBucketNamesAndValueFormats(t *testing.T) {
	s := openTest(t, "hd2.db")
	if err := s.db.View(func(tx *bolt.Tx) error {
		for _, name := range []string{"snapshot", "pushstate", "meta", "kv"} {
			if tx.Bucket([]byte(name)) == nil {
				t.Errorf("缺少桶：%s", name)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("读取桶失败：%v", err)
	}

	if _, err := s.MarkPushed("dispatch", "9", testAt); err != nil {
		t.Fatalf("记录推送失败：%v", err)
	}
	if _, err := s.MarkPushed("dispatch", "9", testAt.Add(time.Hour)); err != nil {
		t.Fatalf("重复记录推送失败：%v", err)
	}

	var raw []byte
	if err := s.db.View(func(tx *bolt.Tx) error {
		raw = append([]byte(nil), tx.Bucket(bucketPush).Get([]byte("dispatch:9"))...)
		return nil
	}); err != nil {
		t.Fatalf("读取推送记录失败：%v", err)
	}
	got, err := time.Parse(time.RFC3339, string(raw))
	if err != nil {
		t.Fatalf("pushstate 的值应可被 RFC3339 解析：%v（原值 %q）", err, raw)
	}
	if !got.Equal(testAt) {
		t.Errorf("重复记录不应改写时间戳：期望 %v，实际 %v", testAt, got)
	}
}

// TestSnapshotNormalizesUTC 校验快照时间统一按 UTC 存储，不受调用方时区影响。
func TestSnapshotNormalizesUTC(t *testing.T) {
	s := openTest(t, "hd2.db")
	cst := time.FixedZone("CST", 8*3600)
	at := time.Date(2026, 9, 16, 20, 30, 0, 0, cst)
	if err := s.PutSnapshot("war", []byte(`{"a":1}`), at); err != nil {
		t.Fatalf("写入快照失败：%v", err)
	}
	_, fetchedAt, ok, err := s.GetSnapshot("war")
	if err != nil || !ok {
		t.Fatalf("读取快照失败：ok=%v err=%v", ok, err)
	}
	if !fetchedAt.Equal(at) {
		t.Errorf("时间点错误：期望 %v，实际 %v", at, fetchedAt)
	}
	if fetchedAt.Location() != time.UTC {
		t.Errorf("应统一按 UTC 存储，实际时区 %v", fetchedAt.Location())
	}
}

// TestKV 校验通用键值桶的读写：同键覆盖、不同 kind 互不干扰、缺键返回 ok=false。
func TestKV(t *testing.T) {
	store := openTest(t, "hd2.db")

	if _, ok, err := store.GetKV("translate", "缺的键"); err != nil || ok {
		t.Fatalf("缺键应返回 ok=false 且无错误，实际 ok=%v err=%v", ok, err)
	}
	if err := store.PutKV("translate", "abc", "你好"); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	got, ok, err := store.GetKV("translate", "abc")
	if err != nil || !ok || got != "你好" {
		t.Fatalf("读取结果错误：value=%q ok=%v err=%v", got, ok, err)
	}
	// 同 kind 同 key 覆盖写
	if err := store.PutKV("translate", "abc", "世界"); err != nil {
		t.Fatalf("覆盖写失败：%v", err)
	}
	if got, _, _ := store.GetKV("translate", "abc"); got != "世界" {
		t.Fatalf("同键应覆盖，实际 %q", got)
	}
	// 不同 kind 是不同记录
	if _, ok, _ := store.GetKV("其它域", "abc"); ok {
		t.Fatal("不同 kind 之间不应互相看见")
	}
}

// TestKVRoutesToKVBucket 钉死通用键值的宿主桶：记录必须落在 bucketKV 的 "kind:key" 键上。
// 评审发现把路由改到 bucketMeta 时全仓用例仍然全绿，所以这里直接读原始字节，
// 并顺带确认老桶（meta / pushstate / snapshot）里不会冒出这条记录——
// 桶写错会让 Task 9 的去重数据与翻译缓存互相踩踏，且重启后才能发现。
func TestKVRoutesToKVBucket(t *testing.T) {
	store := openTest(t, "hd2.db")
	if err := store.PutKV("translate", "abc", "v"); err != nil {
		t.Fatalf("写入失败：%v", err)
	}

	var raw []byte
	if err := store.db.View(func(tx *bolt.Tx) error {
		raw = append([]byte(nil), tx.Bucket(bucketKV).Get([]byte("translate:abc"))...)
		return nil
	}); err != nil {
		t.Fatalf("读取 kv 桶失败：%v", err)
	}
	if string(raw) != "v" {
		t.Errorf("kv 桶里的 translate:abc 应为 %q，实际 %q", "v", raw)
	}

	// 带前缀的键与裸 key 都不允许出现在其它桶里（裸 key 是为了防「前缀被丢掉」的路由改动）。
	if err := store.db.View(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{bucketMeta, bucketPush, bucketSnapshot} {
			for _, key := range []string{"translate:abc", "abc"} {
				if tx.Bucket(name).Get([]byte(key)) != nil {
					t.Errorf("桶 %s 里不应出现键 %s", name, key)
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("读取其它桶失败：%v", err)
	}
}

// TestKVRejectsColonInKey 校验 kind 与 key 都不允许含冒号：冒号是桶内键的分隔符，
// 放过去会让 (a:b, c) 与 (a, b:c) 撞成同一条记录。
// 两侧空白（空串、纯空格、纯制表符）也一律拒绝，避免出现看起来写进去了、实际查不到的记录。
func TestKVRejectsColonInKey(t *testing.T) {
	store := openTest(t, "hd2.db")
	for _, c := range []struct{ kind, key string }{
		{"a:b", "c"}, {"a", "b:c"}, {"", "c"}, {"a", ""}, {"  ", "c"}, {"a", "  "}, {"a", "\t"},
	} {
		if err := store.PutKV(c.kind, c.key, "v"); err == nil {
			t.Errorf("PutKV(%q,%q) 应报错", c.kind, c.key)
		}
		if _, _, err := store.GetKV(c.kind, c.key); err == nil {
			t.Errorf("GetKV(%q,%q) 应报错", c.kind, c.key)
		}
	}
}
