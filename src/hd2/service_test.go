package hd2

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// fakeStore 是内存版快照存储，用于验证降级逻辑。
type fakeStore struct {
	mu       sync.Mutex
	data     map[string][]byte
	at       map[string]time.Time
	putErr   error
	getErr   error
	putCalls int
}

func newFakeStore() *fakeStore {
	return &fakeStore{data: map[string][]byte{}, at: map[string]time.Time{}}
}

func (f *fakeStore) PutSnapshot(name string, data []byte, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.putCalls++
	if f.putErr != nil {
		return f.putErr
	}
	f.data[name], f.at[name] = data, at
	return nil
}

func (f *fakeStore) GetSnapshot(name string) ([]byte, time.Time, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, time.Time{}, false, f.getErr
	}
	data, ok := f.data[name]
	return data, f.at[name], ok, nil
}

const warBody = `{"clientVersion":"1.003.400","statistics":{"missionsWon":100,"playerCount":1234}}`

// newTestService 创建指向假服务器的 Service；now 为 nil 时使用系统时钟。
func newTestService(t *testing.T, handler http.Handler, store SnapshotStore, now func() time.Time) *Service {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := NewClient(ClientConfig{
		BaseURL:       server.URL,
		Timeout:       5 * time.Second,
		Client:        "hd2_bot_test",
		Contact:       "dev@example.com",
		UserAgent:     "hd2_bot_test/0.1",
		RetryMax:      1,
		RetryBase:     10 * time.Millisecond,
		RetryMaxDelay: 20 * time.Millisecond,
	}, NewLimiter(100, time.Second, time.Second))
	return NewService(ServiceConfig{
		Client: client,
		Store:  store,
		// Planets 故意留零值，用于验证「TTL 为零即不缓存」。
		TTL: TTLConfig{War: time.Minute},
		Now: now,
	})
}

// TestServiceWarSuccess 校验正常取数并写入快照。
func TestServiceWarSuccess(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(warBody))
	}), store, nil)

	res, err := svc.War(context.Background())
	if err != nil {
		t.Fatalf("取战况失败：%v", err)
	}
	if res.Stale {
		t.Error("正常取数不应标记为过期数据")
	}
	if res.Value.Statistics.MissionsWon != 100 || res.Value.Statistics.PlayerCount != 1234 {
		t.Errorf("战况数据错误：%+v", res.Value.Statistics)
	}
	if _, ok := store.data[string(EndpointWar)]; !ok {
		t.Error("成功取数后应写入快照")
	}
}

// TestServiceEndpointsMapping 校验每个查询方法打的是对应的上游端点。
func TestServiceEndpointsMapping(t *testing.T) {
	bodies := map[string]string{
		"/war":                   warBody,
		"/planets":               `[]`,
		"/campaigns":             `[]`,
		"/assignments":           `[]`,
		"/dispatches":            `[]`,
		"/planet-events":         `[]`,
		"/api/v2/space-stations": `[]`,
	}
	var mu sync.Mutex
	var paths []string
	svc := newTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		body, ok := bodies[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(body))
	}), newFakeStore(), nil)

	ctx := context.Background()
	calls := []struct {
		name string
		path string
		run  func() error
	}{
		{"war", "/war", func() error { _, err := svc.War(ctx); return err }},
		{"planets", "/planets", func() error { _, err := svc.Planets(ctx); return err }},
		{"campaigns", "/campaigns", func() error { _, err := svc.Campaigns(ctx); return err }},
		{"assignments", "/assignments", func() error { _, err := svc.Assignments(ctx); return err }},
		{"dispatches", "/dispatches", func() error { _, err := svc.Dispatches(ctx); return err }},
		{"events", "/planet-events", func() error { _, err := svc.Events(ctx); return err }},
		{"stations", "/api/v2/space-stations", func() error { _, err := svc.Stations(ctx); return err }},
	}
	for _, c := range calls {
		if err := c.run(); err != nil {
			t.Errorf("%s 查询失败：%v", c.name, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != len(calls) {
		t.Fatalf("期望 %d 次请求，实际 %d 次：%v", len(calls), len(paths), paths)
	}
	for i, c := range calls {
		if paths[i] != c.path {
			t.Errorf("第 %d 次请求路径错误：期望 %s，实际 %s", i+1, c.path, paths[i])
		}
	}
}

// TestServiceFallsBackToSnapshot 校验上游失败时用快照降级并标记 Stale。
func TestServiceFallsBackToSnapshot(t *testing.T) {
	store := newFakeStore()
	at := time.Date(2026, 9, 16, 11, 0, 0, 0, time.UTC)
	if err := store.PutSnapshot(string(EndpointWar), []byte(warBody), at); err != nil {
		t.Fatalf("预置快照失败：%v", err)
	}
	svc := newTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}), store, nil)

	res, err := svc.War(context.Background())
	if err != nil {
		t.Fatalf("有快照时不应报错：%v", err)
	}
	if !res.Stale {
		t.Error("降级数据应标记 Stale")
	}
	if !res.FetchedAt.Equal(at) {
		t.Errorf("抓取时间应为快照时间 %v，实际 %v", at, res.FetchedAt)
	}
	if res.Value.Statistics.PlayerCount != 1234 {
		t.Errorf("快照数据解析错误：%+v", res.Value.Statistics)
	}
}

// TestServiceNoSnapshotReturnsError 校验既无上游又无快照时返回错误。
func TestServiceNoSnapshotReturnsError(t *testing.T) {
	svc := newTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}), newFakeStore(), nil)

	if _, err := svc.War(context.Background()); err == nil {
		t.Fatal("无数据来源时应返回错误")
	}
}

// TestServiceNoStoreReturnsError 校验未配置快照存储时直接返回上游错误。
func TestServiceNoStoreReturnsError(t *testing.T) {
	svc := newTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}), nil, nil)

	if _, err := svc.War(context.Background()); err == nil {
		t.Fatal("未配置快照存储时应返回错误")
	}
}

// TestServiceSnapshotReadErrorReturnsUpstreamError 校验读快照失败时返回上游错误。
func TestServiceSnapshotReadErrorReturnsUpstreamError(t *testing.T) {
	store := newFakeStore()
	store.getErr = errors.New("读盘失败")
	svc := newTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}), store, nil)

	if _, err := svc.War(context.Background()); err == nil {
		t.Fatal("读快照失败时应返回上游错误")
	}
}

// TestServiceCorruptSnapshotReturnsUpstreamError 校验快照损坏时返回上游错误。
func TestServiceCorruptSnapshotReturnsUpstreamError(t *testing.T) {
	store := newFakeStore()
	if err := store.PutSnapshot(string(EndpointWar), []byte("不是 JSON"), time.Now()); err != nil {
		t.Fatalf("预置快照失败：%v", err)
	}
	svc := newTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}), store, nil)

	if _, err := svc.War(context.Background()); err == nil {
		t.Fatal("快照损坏时应返回上游错误")
	}
}

// TestServiceSnapshotWriteErrorIgnored 校验写快照失败不影响本次查询结果。
func TestServiceSnapshotWriteErrorIgnored(t *testing.T) {
	store := newFakeStore()
	store.putErr = errors.New("写盘失败")
	svc := newTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(warBody))
	}), store, nil)

	res, err := svc.War(context.Background())
	if err != nil {
		t.Fatalf("写快照失败不应影响查询：%v", err)
	}
	if res.Stale || res.Value.Statistics.MissionsWon != 100 {
		t.Errorf("查询结果错误：%+v", res)
	}
	if store.putCalls != 1 {
		t.Errorf("应尝试写一次快照，实际 %d 次", store.putCalls)
	}
}

// TestServiceCacheAvoidsUpstream 校验 10 次查询只打一次上游。
func TestServiceCacheAvoidsUpstream(t *testing.T) {
	var mu sync.Mutex
	var calls int
	svc := newTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		_, _ = w.Write([]byte(warBody))
	}), newFakeStore(), nil)

	for i := 0; i < 10; i++ {
		if _, err := svc.War(context.Background()); err != nil {
			t.Fatalf("第 %d 次查询失败：%v", i+1, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("10 次查询应只请求上游 1 次，实际 %d 次", calls)
	}
}

// TestServiceFetchedAtFollowsCache 校验缓存命中时数据时间是当初抓取的时间，不是本次请求时间。
func TestServiceFetchedAtFollowsCache(t *testing.T) {
	var mu sync.Mutex
	var calls int
	at := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)
	now := at
	svc := newTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		_, _ = w.Write([]byte(warBody))
	}), newFakeStore(), func() time.Time { return now })

	first, err := svc.War(context.Background())
	if err != nil {
		t.Fatalf("首次查询失败：%v", err)
	}
	if !first.FetchedAt.Equal(at) {
		t.Fatalf("首次查询的数据时间应为 %v，实际 %v", at, first.FetchedAt)
	}

	now = at.Add(30 * time.Second) // TTL 内，应命中缓存
	second, err := svc.War(context.Background())
	if err != nil {
		t.Fatalf("第二次查询失败：%v", err)
	}
	if !second.FetchedAt.Equal(at) {
		t.Errorf("缓存命中时数据时间应保持 %v，实际 %v", at, second.FetchedAt)
	}

	now = at.Add(61 * time.Second) // 超过 TTL，应重新取数
	third, err := svc.War(context.Background())
	if err != nil {
		t.Fatalf("第三次查询失败：%v", err)
	}
	if !third.FetchedAt.Equal(now) {
		t.Errorf("缓存过期后数据时间应更新为 %v，实际 %v", now, third.FetchedAt)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Errorf("应请求上游 2 次，实际 %d 次", calls)
	}
}

// TestServiceCacheExpiryFallsBackToSnapshot 校验缓存过期后上游不可用会回落到快照。
func TestServiceCacheExpiryFallsBackToSnapshot(t *testing.T) {
	store := newFakeStore()
	at := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)
	now := at
	var fail bool
	svc := newTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(warBody))
	}), store, func() time.Time { return now })

	if _, err := svc.War(context.Background()); err != nil {
		t.Fatalf("首次查询失败：%v", err)
	}
	if got := store.at[string(EndpointWar)]; !got.Equal(at) {
		t.Fatalf("快照时间应为 %v，实际 %v", at, got)
	}

	fail = true
	now = at.Add(2 * time.Minute)
	res, err := svc.War(context.Background())
	if err != nil {
		t.Fatalf("有快照时不应报错：%v", err)
	}
	if !res.Stale || !res.FetchedAt.Equal(at) {
		t.Errorf("应降级到快照并标记 Stale：stale=%v at=%v", res.Stale, res.FetchedAt)
	}
}

// TestServiceAssignmentsEmpty 校验重要指令为空时返回空切片且不报错。
func TestServiceAssignmentsEmpty(t *testing.T) {
	svc := newTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	}), newFakeStore(), nil)

	res, err := svc.Assignments(context.Background())
	if err != nil {
		t.Fatalf("空指令不应报错：%v", err)
	}
	if len(res.Value) != 0 {
		t.Fatalf("期望 0 条重要指令，实际 %d 条", len(res.Value))
	}
}

// TestServiceCompanionAccessor 校验补充源客户端的取用入口。
func TestServiceCompanionAccessor(t *testing.T) {
	svc := newTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), nil, nil)
	if svc.Companion() != nil {
		t.Error("未配置时应返回 nil")
	}
	c := NewCompanion("https://example.com", "", time.Second, nil, nil)
	svc2 := NewService(ServiceConfig{Companion: c})
	if svc2.Companion() != c {
		t.Error("应返回配置的补充源客户端")
	}
}

// TestServiceNoTTL 校验 TTL 为零时不使用缓存，每次查询都打上游。
func TestServiceNoTTL(t *testing.T) {
	var mu sync.Mutex
	var calls int
	svc := newTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		_, _ = w.Write([]byte(`[]`))
	}), newFakeStore(), nil)

	for i := 0; i < 3; i++ {
		if _, err := svc.Planets(context.Background()); err != nil {
			t.Fatalf("第 %d 次查询失败：%v", i+1, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 3 {
		t.Errorf("TTL 为零时应每次取数，实际 %d 次", calls)
	}
}

// TestServiceReturnedValuesAreCopies 校验上层拿到的数据是副本：就地修改不会污染缓存。
// 插件层会按玩家数等字段排序或改写切片，如果直接拿到缓存里的底层数组就会互相干扰。
func TestServiceReturnedValuesAreCopies(t *testing.T) {
	var mu sync.Mutex
	var calls int
	svc := newTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		if r.URL.Path == "/war" {
			_, _ = w.Write([]byte(warBody))
			return
		}
		_, _ = w.Write([]byte(`[{"index":1,"name":"测试星球"}]`))
	}), newFakeStore(), nil)
	svc.ttl.Planets = time.Minute // 星球也走缓存，才能验证返回的是副本

	war1, err := svc.War(context.Background())
	if err != nil {
		t.Fatalf("取战况失败：%v", err)
	}
	war1.Value.ClientVersion = "被改坏了"

	war2, err := svc.War(context.Background())
	if err != nil {
		t.Fatalf("第二次取战况失败：%v", err)
	}
	if war2.Value.ClientVersion != "1.003.400" {
		t.Errorf("第二次取到的战况被上一次的修改污染：%s", war2.Value.ClientVersion)
	}

	planets1, err := svc.Planets(context.Background())
	if err != nil {
		t.Fatalf("取星球失败：%v", err)
	}
	if len(planets1.Value) != 1 {
		t.Fatalf("期望 1 个星球，实际 %d 个", len(planets1.Value))
	}
	planets1.Value[0].Name = "被改坏了"

	planets2, err := svc.Planets(context.Background())
	if err != nil {
		t.Fatalf("第二次取星球失败：%v", err)
	}
	if planets2.Value[0].Name != "测试星球" {
		t.Errorf("第二次取到的星球被上一次的修改污染：%s", planets2.Value[0].Name)
	}

	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Errorf("战况与星球各应取数一次，实际 %d 次", calls)
	}
}
