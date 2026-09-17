package translate

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// fixedNow 是用例里注入的固定时钟起点。
var fixedNow = time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

// fakeBackend 是翻译后端的记录型假实现：按 batch 把输入整段转成预设后缀，
// 也可以直接返回错误，用来覆盖「后端失败」「编号不齐」等分支。
type fakeBackend struct {
	mu       sync.Mutex
	calls    int
	inputs   []string
	out      func(text string) (string, error)
	lastErr  error
	nameText string
}

func (b *fakeBackend) Name() string {
	if b.nameText == "" {
		return "假后端"
	}
	return b.nameText
}

func (b *fakeBackend) Translate(_ context.Context, texts []string) ([]string, error) {
	b.mu.Lock()
	b.calls++
	b.inputs = append(b.inputs, texts...)
	b.mu.Unlock()
	if b.lastErr != nil {
		return nil, b.lastErr
	}
	out := make([]string, 0, len(texts))
	for _, text := range texts {
		translated, err := b.out(text)
		if err != nil {
			return nil, err
		}
		out = append(out, translated)
	}
	return out, nil
}

func (b *fakeBackend) callCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

// echoNumbered 是「照着编号标记原样翻译」的默认后端行为：把每段的正文加上前缀「中:」。
// 它模拟一个守规矩的模型：保留标记、逐段返回。
func echoNumbered(text string) (string, error) {
	parts := splitNumbered(text, strings.Count(text, "[[HD2_"))
	if parts == nil {
		return "", errors.New("假后端不会数编号")
	}
	out := make([]string, len(parts))
	for i, part := range parts {
		out[i] = "中:" + part
	}
	return numberTexts(out), nil
}

// memStore 是 CacheStore 的内存实现，统计读写次数。
type memStore struct {
	mu     sync.Mutex
	data   map[string]string
	gets   int
	puts   int
	getErr error
	putErr error
}

func newMemStore() *memStore { return &memStore{data: map[string]string{}} }

func (m *memStore) PutKV(kind, key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.puts++
	if m.putErr != nil {
		return m.putErr
	}
	m.data[kind+":"+key] = value
	return nil
}

func (m *memStore) GetKV(kind, key string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gets++
	if m.getErr != nil {
		return "", false, m.getErr
	}
	value, ok := m.data[kind+":"+key]
	return value, ok, nil
}

// testConfig 返回一份「翻译开着、无冷却、无限速」的配置。
func testConfig() Config {
	return Config{
		Enabled:    true,
		Mode:       "auto",
		Model:      "test-model",
		TargetLang: "zh-CN",
		Timeout:    time.Second,
		MaxTokens:  100,
	}
}

// newTestTranslator 组装一个注入假后端/假存储/固定时钟的翻译层。
func newTestTranslator(t *testing.T, backend Backend, store CacheStore, cfg Config) Translator {
	t.Helper()
	tr, err := New(cfg, Deps{Backend: backend, Store: store, Now: func() time.Time { return fixedNow }, Logger: func(string, ...any) {}})
	if err != nil {
		t.Fatalf("构造翻译层失败：%v", err)
	}
	return tr
}

// TestPassthroughReturnsInput 校验透传实现原样返回（enabled=false 与「没有可用后端」都走它）。
func TestPassthroughReturnsInput(t *testing.T) {
	tr := Passthrough()
	got, err := tr.Translate(context.Background(), []string{"Hello", "World"})
	if err != nil {
		t.Fatalf("透传不应报错：%v", err)
	}
	if len(got) != 2 || got[0] != "Hello" || got[1] != "World" {
		t.Fatalf("透传应原样返回，实际 %v", got)
	}
}

// TestDisabledReturnsPassthrough 校验 enabled=false 时 New 返回透传实现，调用方不必判断配置。
func TestDisabledReturnsPassthrough(t *testing.T) {
	backend := &fakeBackend{out: echoNumbered}
	cfg := testConfig()
	cfg.Enabled = false
	tr := newTestTranslator(t, backend, newMemStore(), cfg)
	got, err := tr.Translate(context.Background(), []string{"Hold the line"})
	if err != nil {
		t.Fatalf("关闭翻译不应报错：%v", err)
	}
	if got[0] != "Hold the line" {
		t.Fatalf("关闭翻译应原样返回，实际 %q", got[0])
	}
	if backend.callCount() != 0 {
		t.Fatalf("关闭翻译不应打后端，实际调用 %d 次", backend.callCount())
	}
}

// TestTranslateCachesAndSkipsRepeatedRequests 校验缓存命中后不再打后端。
func TestTranslateCachesAndSkipsRepeatedRequests(t *testing.T) {
	backend := &fakeBackend{out: echoNumbered}
	store := newMemStore()
	tr := newTestTranslator(t, backend, store, testConfig())

	first, err := tr.Translate(context.Background(), []string{"Hold the line"})
	if err != nil {
		t.Fatalf("首次翻译失败：%v", err)
	}
	if first[0] != "中:Hold the line" {
		t.Fatalf("首次译文错误：%q", first[0])
	}
	second, err := tr.Translate(context.Background(), []string{"Hold the line"})
	if err != nil {
		t.Fatalf("二次翻译失败：%v", err)
	}
	if second[0] != first[0] {
		t.Fatalf("二次翻译应与缓存一致，实际 %q", second[0])
	}
	if backend.callCount() != 1 {
		t.Fatalf("第二次应命中缓存不打后端，实际调用 %d 次", backend.callCount())
	}
	if store.puts != 1 {
		t.Fatalf("只应写一次缓存，实际 %d 次", store.puts)
	}
}

// TestTranslateSkipsTextWithoutLatinLetters 校验全中文/纯数字的段落不送译：
// 上游文本经术语预替换后可能已经是中文，再送一次只会浪费配额。
func TestTranslateSkipsTextWithoutLatinLetters(t *testing.T) {
	backend := &fakeBackend{out: echoNumbered}
	tr := newTestTranslator(t, backend, newMemStore(), testConfig())
	got, err := tr.Translate(context.Background(), []string{"天园六IV", "12345", ""})
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	if got[0] != "天园六IV" || got[1] != "12345" || got[2] != "" {
		t.Fatalf("不含拉丁字母的段落应原样返回，实际 %v", got)
	}
	if backend.callCount() != 0 {
		t.Fatalf("不应打后端，实际调用 %d 次", backend.callCount())
	}
}

// TestTranslateBatchesBySize 校验超过 batchSize 段时按批切分（每批一次请求）。
func TestTranslateBatchesBySize(t *testing.T) {
	backend := &fakeBackend{out: echoNumbered}
	texts := make([]string, batchSize+1)
	for i := range texts {
		texts[i] = "Segment " + string(rune('A'+i%26))
	}
	tr := newTestTranslator(t, backend, newMemStore(), testConfig())
	if _, err := tr.Translate(context.Background(), texts); err != nil {
		t.Fatalf("翻译失败：%v", err)
	}
	if backend.callCount() != 2 {
		t.Fatalf("应分 2 批请求，实际 %d 次", backend.callCount())
	}
}

// TestTranslateFallsBackWhenBackendFails 校验后端报错时整批回退原文，并返回一个用于日志的错误。
func TestTranslateFallsBackWhenBackendFails(t *testing.T) {
	backend := &fakeBackend{lastErr: errors.New("网络炸了")}
	tr := newTestTranslator(t, backend, newMemStore(), testConfig())
	got, err := tr.Translate(context.Background(), []string{"Hold the line"})
	if err == nil {
		t.Fatal("后端失败时 err 应非空（供日志与卡片提示）")
	}
	if got[0] != "Hold the line" {
		t.Fatalf("失败时应回退原文，实际 %q", got[0])
	}
}

// TestTranslateFallsBackWhenNumberingBroken 校验编号不齐时整批回退原文，绝不把错位的译文贴到别的条目上。
func TestTranslateFallsBackWhenNumberingBroken(t *testing.T) {
	backend := &fakeBackend{out: func(string) (string, error) {
		// 只回一段，缺编号
		return "[[HD2_1]] 只有第一段", nil
	}}
	tr := newTestTranslator(t, backend, newMemStore(), testConfig())
	got, err := tr.Translate(context.Background(), []string{"First line", "Second line"})
	if err == nil {
		t.Fatal("编号不齐时 err 应非空")
	}
	if got[0] != "First line" || got[1] != "Second line" {
		t.Fatalf("编号不齐应整批回退原文，实际 %v", got)
	}
}

// TestTranslateCooldownSkipsRequests 校验 429/5xx 后进入冷却：冷却期内的调用直接回退原文、不打接口。
func TestTranslateCooldownSkipsRequests(t *testing.T) {
	backend := &fakeBackend{lastErr: &statusError{code: 429, backend: "假后端"}}
	cfg := testConfig()
	cfg.ErrorCooldown = 120 * time.Second
	tr := newTestTranslator(t, backend, newMemStore(), cfg)

	if _, err := tr.Translate(context.Background(), []string{"Hold the line"}); err == nil {
		t.Fatal("首次调用应因 429 报错")
	}
	if backend.callCount() != 1 {
		t.Fatalf("首次调用应打一次后端，实际 %d 次", backend.callCount())
	}
	got, err := tr.Translate(context.Background(), []string{"Another line"})
	if err == nil {
		t.Fatal("冷却期内的调用也应返回错误（供日志）")
	}
	if backend.callCount() != 1 {
		t.Fatalf("冷却期内不应再打后端，实际 %d 次", backend.callCount())
	}
	if got[0] != "Another line" {
		t.Fatalf("冷却期内应回退原文，实际 %q", got[0])
	}
}

// TestTranslateMinIntervalPacesRequests 校验 min_interval 会让相邻两批请求之间等待。
func TestTranslateMinIntervalPacesRequests(t *testing.T) {
	backend := &fakeBackend{out: echoNumbered}
	cfg := testConfig()
	cfg.MinInterval = 120 * time.Millisecond

	// 时钟随真实时间前进：本次用例需要一个会走的时钟，才能观察到「等了一会儿」。
	tr, err := New(cfg, Deps{Backend: backend, Store: newMemStore(), Logger: func(string, ...any) {}})
	if err != nil {
		t.Fatalf("构造翻译层失败：%v", err)
	}
	texts := make([]string, batchSize+1)
	for i := range texts {
		texts[i] = "Segment " + string(rune('A'+i%26))
	}
	start := time.Now()
	if _, err := tr.Translate(context.Background(), texts); err != nil {
		t.Fatalf("翻译失败：%v", err)
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Fatalf("两批之间应至少有 min_interval 的间隔，实际只用了 %s", elapsed)
	}
}

// TestTranslateLogsNeverContainAPIKey 校验日志里不会出现 API key（key 只从配置里读，绝不能进日志）。
//
// 两个断言缺一不可：只查「不包含」的话，一个压根不打日志的实现也会通过；
// 所以假 Logger 必须用 Sprintf 把参数拼进文本（key 常常是作为参数传进来的），并断言至少打了一行。
func TestTranslateLogsNeverContainAPIKey(t *testing.T) {
	var lines []string
	backend := &fakeBackend{lastErr: errors.New("网络炸了")}
	cfg := testConfig()
	cfg.APIKey = "sk-secret-should-not-appear"
	tr, err := New(cfg, Deps{
		Backend: backend,
		Store:   newMemStore(),
		Now:     func() time.Time { return fixedNow },
		Logger:  func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) },
	})
	if err != nil {
		t.Fatalf("构造翻译层失败：%v", err)
	}
	if _, err := tr.Translate(context.Background(), []string{"Hold the line"}); err == nil {
		t.Fatal("应返回错误")
	}
	if len(lines) == 0 {
		t.Fatal("翻译失败时至少应记一行日志")
	}
	// 只断言「有日志」不够：New 的启动日志就能满足它，改坏了失败日志照样绿。
	// 这里要求日志里明确出现失败字样，失败日志被删或不再打时用例必须变红。
	if !strings.Contains(strings.Join(lines, "\n"), "失败") {
		t.Fatalf("翻译失败时应有一条说明失败的中文日志，实际 %q", lines)
	}
	for _, line := range lines {
		if strings.Contains(line, cfg.APIKey) {
			t.Fatalf("日志里出现了 API key：%q", line)
		}
	}
}

// TestTranslateSurvivesCacheErrors 校验缓存读写失败不影响翻译结果（缓存只是加速手段）。
//
// 除了结果，还要断言 store 真的被读过、也被尝试写过：否则把 loadCache 改成「恒未命中」、
// 或干脆不写缓存，这条用例照样绿。
func TestTranslateSurvivesCacheErrors(t *testing.T) {
	backend := &fakeBackend{out: echoNumbered}
	store := newMemStore()
	store.getErr = errors.New("读缓存炸了")
	store.putErr = errors.New("写缓存炸了")
	tr := newTestTranslator(t, backend, store, testConfig())
	got, err := tr.Translate(context.Background(), []string{"Hold the line"})
	if err != nil {
		t.Fatalf("缓存故障不应让翻译报错：%v", err)
	}
	if got[0] != "中:Hold the line" {
		t.Fatalf("缓存故障时仍应返回译文，实际 %q", got[0])
	}
	if store.gets == 0 {
		t.Fatal("查缓存失败前应该真的调用过 store.GetKV")
	}
	if store.puts == 0 {
		t.Fatal("写缓存失败也应调用过 store.PutKV（失败只记日志，不影响结果）")
	}
}

// TestSplitNumbered 校验编号解析的边界：缺号、重号、顺序错位、空段一律返回 nil；
// 只有单段时允许后端丢掉标记（免费接口偶尔会吃掉不认识的标记）。
func TestSplitNumbered(t *testing.T) {
	cases := []struct {
		name string
		text string
		want int
		ok   bool
	}{
		{"正常两段", "[[HD2_1]] 甲\n[[HD2_2]] 乙", 2, true},
		{"单段带标记", "[[HD2_1]] 甲", 1, true},
		{"单段无标记", "甲", 1, true},
		{"缺一段", "[[HD2_1]] 甲", 2, false},
		{"编号跳号", "[[HD2_1]] 甲\n[[HD2_3]] 丙", 2, false},
		{"顺序错位", "[[HD2_2]] 乙\n[[HD2_1]] 甲", 2, false},
		{"空段", "[[HD2_1]] 甲\n[[HD2_2]]   ", 2, false},
		{"多段无标记", "甲\n乙", 2, false},
		{"空文本", "", 1, false},
		{"want 为 0", "[[HD2_1]] 甲", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := splitNumbered(c.text, c.want)
			if c.ok && got == nil {
				t.Fatalf("应解析成功，实际 nil")
			}
			if !c.ok && got != nil {
				t.Fatalf("应解析失败，实际 %v", got)
			}
		})
	}
}

// TestIsCooldownError 校验只有 429/5xx 才触发冷却：网络超时与解析失败只是本次不巧。
func TestIsCooldownError(t *testing.T) {
	if !isCooldownError(&statusError{code: 429, backend: "x"}) {
		t.Error("429 应触发冷却")
	}
	if !isCooldownError(&statusError{code: 503, backend: "x"}) {
		t.Error("5xx 应触发冷却")
	}
	if isCooldownError(&statusError{code: 400, backend: "x"}) {
		t.Error("400 不应触发冷却")
	}
	if isCooldownError(errors.New("timeout")) {
		t.Error("普通错误不应触发冷却")
	}
}

// TestCacheKeyDependsOnBackend 校验缓存键含后端指纹：换了后端（实现、模型或地址）之后
// 旧译文一律不复用，否则新后端永远只是读到上一个后端的译文。
func TestCacheKeyDependsOnBackend(t *testing.T) {
	store := newMemStore()

	first := &fakeBackend{out: echoNumbered, nameText: "后端A"}
	trA := newTestTranslator(t, first, store, testConfig())
	if _, err := trA.Translate(context.Background(), []string{"Hold the line"}); err != nil {
		t.Fatalf("后端 A 翻译失败：%v", err)
	}
	if first.callCount() != 1 {
		t.Fatalf("后端 A 应被调用一次，实际 %d 次", first.callCount())
	}

	second := &fakeBackend{out: echoNumbered, nameText: "后端B"}
	trB := newTestTranslator(t, second, store, testConfig())
	if _, err := trB.Translate(context.Background(), []string{"Hold the line"}); err != nil {
		t.Fatalf("后端 B 翻译失败：%v", err)
	}
	if second.callCount() != 1 {
		t.Fatalf("换后端后不应命中旧缓存，后端 B 应被调用一次，实际 %d 次", second.callCount())
	}
}

// TestTranslateIsSafeForConcurrentCallers 校验翻译层能被推送协程与命令协程同时调用：
// 冷却与限速状态（cooldownUntil / nextRequestAt）只在锁内读写，缓存与后端也要求线程安全，
// 因此并发调用既不能串结果，也不能 panic（有 -race 时再跑一遍更有意义）。
func TestTranslateIsSafeForConcurrentCallers(t *testing.T) {
	backend := &fakeBackend{out: echoNumbered}
	tr := newTestTranslator(t, backend, newMemStore(), testConfig())

	texts := []string{"Hold the line", "Extract the civilians", "Defend the flag"}
	const workers = 8
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := tr.Translate(context.Background(), texts)
			if err != nil {
				t.Errorf("并发调用不应报错：%v", err)
				return
			}
			for j, text := range got {
				// 期望值要过一遍 PreReplace：送译前的术语预替换（Task 4）会把 civilians 这类
				// 专有名词换成官方译名，后端回显的是替换后的文本。
				if want := "中:" + PreReplace(texts[j]); text != want {
					t.Errorf("第 %d 段结果串了：%q（期望 %q）", j, text, want)
				}
			}
		}()
	}
	wg.Wait()
}

// TestTranslatePacesConcurrentRequests 校验并发调用下的限速：多个协程同时送译时，
// 相邻两次后端请求至少要隔一个 min_interval。
//
// 旧实现（读→解锁→等→再写）的预约不是原子的：并发调用会看到同一个空档而同时放行，
// 实测 4 个协程里有 3 个几乎同时发出请求，换来 429 与整段冷却期。
func TestTranslatePacesConcurrentRequests(t *testing.T) {
	const (
		interval = 100 * time.Millisecond
		workers  = 4
	)
	var (
		mu     sync.Mutex
		stamps []time.Time
	)
	backend := &fakeBackend{out: func(text string) (string, error) {
		mu.Lock()
		stamps = append(stamps, time.Now())
		mu.Unlock()
		return echoNumbered(text)
	}}
	cfg := testConfig()
	cfg.MinInterval = interval
	tr, err := New(cfg, Deps{Backend: backend, Store: newMemStore(), Logger: func(string, ...any) {}})
	if err != nil {
		t.Fatalf("构造翻译层失败：%v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// 每个协程送一段不同的文本：命中缓存就跳过后端了，测不出限速。
			if _, err := tr.Translate(context.Background(), []string{fmt.Sprintf("Hold the line %d", i)}); err != nil {
				t.Errorf("并发翻译失败：%v", err)
			}
		}(i)
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(stamps) != workers {
		t.Fatalf("应有 %d 次请求，实际 %d 次", workers, len(stamps))
	}
	sort.Slice(stamps, func(i, j int) bool { return stamps[i].Before(stamps[j]) })
	// 名额本身是严格按 interval 递增的，但「前一次请求的实际发出时刻」可能被调度延后，
	// 从而压缩观测到的间隔，所以留一半余量；旧实现下并发请求几乎同时发出，间隔接近 0，照样被抓住。
	minGap := interval / 2
	for i := 1; i < len(stamps); i++ {
		if gap := stamps[i].Sub(stamps[i-1]); gap < minGap {
			t.Fatalf("第 %d 次请求距上一次只有 %s（< %s），限速在并发下失效", i+1, gap, minGap)
		}
	}
}

// TestTranslateReturnsWhenContextCanceledWhilePacing 校验排队等限速名额时被取消：
// 尽快返回（错误可用 errors.Is 判定为 ctx 取消）、回退原文、不打接口。
func TestTranslateReturnsWhenContextCanceledWhilePacing(t *testing.T) {
	backend := &fakeBackend{out: echoNumbered}
	cfg := testConfig()
	cfg.MinInterval = 3 * time.Second // 够长：第二段一定排在未来的名额上，取消后不该再等下去
	tr, err := New(cfg, Deps{Backend: backend, Store: newMemStore(), Logger: func(string, ...any) {}})
	if err != nil {
		t.Fatalf("构造翻译层失败：%v", err)
	}
	// 第一次调用把当前名额占掉（不等待）。
	if _, err := tr.Translate(context.Background(), []string{"First line"}); err != nil {
		t.Fatalf("首次翻译失败：%v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	got, err := tr.Translate(ctx, []string{"Second line"})
	if err == nil {
		t.Fatal("被取消的调用应返回错误")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("错误应能判定为 ctx 取消，实际 %v", err)
	}
	if got[0] != "Second line" {
		t.Fatalf("取消时应回退原文，实际 %q", got[0])
	}
	if backend.callCount() != 1 {
		t.Fatalf("被取消的那批不应打后端，实际共调用 %d 次", backend.callCount())
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("取消后应尽快返回，实际等了 %s", elapsed)
	}
}

// TestTranslateCleansDirtyBackendValues 校验采纳前的脏值清洗：
// 围栏不能进缓存与卡片；只剩围栏或符号的返回值按「后端没翻动」处理（保留原文、不写缓存）。
func TestTranslateCleansDirtyBackendValues(t *testing.T) {
	t.Run("尾围栏被剥掉", func(t *testing.T) {
		backend := &fakeBackend{out: func(string) (string, error) {
			// 多段模式下，结尾的 ``` 常常正好落在最后一段的正文里
			return "[[HD2_1]] 第一段译文\n[[HD2_2]] 第二段译文\n```", nil
		}}
		store := newMemStore()
		tr := newTestTranslator(t, backend, store, testConfig())
		got, err := tr.Translate(context.Background(), []string{"First line", "Second line"})
		if err != nil {
			t.Fatalf("翻译失败：%v", err)
		}
		if got[1] != "第二段译文" {
			t.Fatalf("尾围栏应被剥掉，实际 %q", got[1])
		}
		for key, value := range store.data {
			if strings.Contains(value, "```") {
				t.Fatalf("缓存里不应出现围栏：%s = %q", key, value)
			}
		}
	})

	t.Run("整段围栏与语言标记被剥掉", func(t *testing.T) {
		backend := &fakeBackend{out: func(string) (string, error) {
			return "```json\n[[HD2_1]] 译文\n```", nil
		}}
		tr := newTestTranslator(t, backend, newMemStore(), testConfig())
		got, err := tr.Translate(context.Background(), []string{"Hold the line"})
		if err != nil {
			t.Fatalf("翻译失败：%v", err)
		}
		if got[0] != "译文" {
			t.Fatalf("整段围栏与语言标记都应被剥掉，实际 %q", got[0])
		}
	})

	t.Run("只剩围栏或符号视为没翻动", func(t *testing.T) {
		for _, dirty := range []string{"```", "```json```", "---"} {
			backend := &fakeBackend{out: func(string) (string, error) { return dirty, nil }}
			store := newMemStore()
			tr := newTestTranslator(t, backend, store, testConfig())
			got, err := tr.Translate(context.Background(), []string{"Hold the line"})
			if err != nil {
				t.Fatalf("后端只回了 %q 时不应报错：%v", dirty, err)
			}
			if got[0] != "Hold the line" {
				t.Fatalf("后端只回了 %q 时应保留原文，实际 %q", dirty, got[0])
			}
			if store.puts != 0 {
				t.Fatalf("后端只回了 %q 时不应写缓存，实际写了 %d 次", dirty, store.puts)
			}
		}
	})
}

// TestTranslateFallsBackWhenBackendReturnsExtraSegments 校验后端多回段时的处置：
// 只请求 1 段却回来 2 段时，既不能越界 panic，也不能把多余的译文贴到别的条目上，
// 必须整批回退原文并报错。
func TestTranslateFallsBackWhenBackendReturnsExtraSegments(t *testing.T) {
	backend := &fakeBackend{out: func(string) (string, error) {
		return "[[HD2_1]] 第一段译文\n[[HD2_2]] 多出来的一段", nil
	}}
	tr := newTestTranslator(t, backend, newMemStore(), testConfig())
	got, err := tr.Translate(context.Background(), []string{"First line"})
	if err == nil {
		t.Fatal("段数与请求不符时 err 应非空")
	}
	if got[0] != "First line" {
		t.Fatalf("段数不符应整批回退原文，实际 %q", got[0])
	}
}

// TestTranslateKeepsOriginalWhenBackendEchoes 校验后端把原文原样返回时不写缓存：
// 写进去等于把「这段没翻动」定死成结论，以后永远不会再试。
func TestTranslateKeepsOriginalWhenBackendEchoes(t *testing.T) {
	backend := &fakeBackend{out: func(text string) (string, error) { return text, nil }}
	store := newMemStore()
	tr := newTestTranslator(t, backend, store, testConfig())
	got, err := tr.Translate(context.Background(), []string{"Hold the line"})
	if err != nil {
		t.Fatalf("翻译失败：%v", err)
	}
	if got[0] != "Hold the line" {
		t.Fatalf("后端没翻动时应保留原文，实际 %q", got[0])
	}
	if store.puts != 0 {
		t.Fatalf("没翻动不应写缓存，实际写了 %d 次", store.puts)
	}
}

// TestTranslateSendsNumberedProtocol 校验送译请求体的真实形态：每段一行，行首是 [[HD2_n]] 加一个空格。
// 这条协议是「后端把多段揉成一段」时唯一能把译文切回各自条目的凭据，改动必须被用例挡住。
func TestTranslateSendsNumberedProtocol(t *testing.T) {
	backend := &fakeBackend{out: echoNumbered}
	tr := newTestTranslator(t, backend, newMemStore(), testConfig())
	if _, err := tr.Translate(context.Background(), []string{"First line", "Second line"}); err != nil {
		t.Fatalf("翻译失败：%v", err)
	}
	if len(backend.inputs) != 1 {
		t.Fatalf("两段应合成一次请求，实际发出 %d 次", len(backend.inputs))
	}
	const want = "[[HD2_1]] First line\n[[HD2_2]] Second line"
	if backend.inputs[0] != want {
		t.Fatalf("请求体形态不符：\n实际 %q\n期望 %q", backend.inputs[0], want)
	}
}

// TestTranslateEmptyInput 校验空输入：返回空切片、不报错、不打后端
// （命令协程可能拿着一份空清单进来）。
func TestTranslateEmptyInput(t *testing.T) {
	backend := &fakeBackend{out: echoNumbered}
	tr := newTestTranslator(t, backend, newMemStore(), testConfig())
	for _, texts := range [][]string{nil, {}} {
		got, err := tr.Translate(context.Background(), texts)
		if err != nil {
			t.Fatalf("空输入不应报错：%v", err)
		}
		if len(got) != 0 {
			t.Fatalf("空输入应返回空切片，实际 %v", got)
		}
	}
	if backend.callCount() != 0 {
		t.Fatalf("空输入不应打后端，实际调用 %d 次", backend.callCount())
	}
}

// TestNewWithoutBackendKeepsWorking 校验 enabled=true 但没有任何可用后端时的路径：
// New 不报错、返回可用的透传实现，并留下一条中文警告（这是后端没配好时的主路径，不能 panic）。
func TestNewWithoutBackendKeepsWorking(t *testing.T) {
	var lines []string
	// testConfig 里没填任何后端地址：newBackend 会返回 nil。
	tr, err := New(testConfig(), Deps{Logger: func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	}})
	if err != nil {
		t.Fatalf("没有可用后端不应报错（只警告）：%v", err)
	}
	if _, ok := tr.(passthrough); !ok {
		t.Fatalf("没有可用后端时应回退透传实现，实际 %T", tr)
	}
	got, err := tr.Translate(context.Background(), []string{"Hold the line"})
	if err != nil || got[0] != "Hold the line" {
		t.Fatalf("透传应原样返回，实际 %v err=%v", got, err)
	}
	if len(lines) == 0 {
		t.Fatal("没有可用后端时应记一条中文警告")
	}
	if !strings.Contains(strings.Join(lines, "\n"), "翻译") {
		t.Fatalf("警告应是中文且说明翻译已停用，实际 %q", lines)
	}
}

// TestNewAcceptsEmptyDeps 校验 Deps 零值可构造：Now / Logger 都不填时走系统时钟与静默日志，不 panic。
func TestNewAcceptsEmptyDeps(t *testing.T) {
	backend := &fakeBackend{out: echoNumbered}
	tr, err := New(testConfig(), Deps{Backend: backend})
	if err != nil {
		t.Fatalf("零值 Deps 不应报错：%v", err)
	}
	got, err := tr.Translate(context.Background(), []string{"Hold the line"})
	if err != nil {
		t.Fatalf("零值 Deps 下翻译失败：%v", err)
	}
	if got[0] != "中:Hold the line" {
		t.Fatalf("译文错误：%q", got[0])
	}
}

// TestNewDefaultsNonPositiveTimeout 校验 timeout 兜底：Config.Timeout 是 time.Duration，
// 裸写「秒」（Timeout: 20 → 20ns）会让所有请求瞬间超时，因此 <= 0 时换成默认值并告警。
func TestNewDefaultsNonPositiveTimeout(t *testing.T) {
	var lines []string
	backend := &fakeBackend{out: echoNumbered}
	cfg := testConfig()
	cfg.Timeout = 0
	tr, err := New(cfg, Deps{
		Backend: backend,
		Store:   newMemStore(),
		Logger:  func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) },
	})
	if err != nil {
		t.Fatalf("构造翻译层失败：%v", err)
	}
	if got := tr.(*translator).cfg.Timeout; got != defaultTimeout {
		t.Fatalf("timeout 未兜底：实际 %s，期望 %s", got, defaultTimeout)
	}
	if len(lines) == 0 {
		t.Fatal("timeout 兜底时应记一条中文告警")
	}
	if got, err := tr.Translate(context.Background(), []string{"Hold the line"}); err != nil || got[0] != "中:Hold the line" {
		t.Fatalf("兜底后仍应能正常翻译：%v err=%v", got, err)
	}
}

// TestFingerprintPinsCacheKeyInputs 钉住缓存指纹的组成：协议版本、后端名、模型、地址主机、目标语言。
//
// 期望值里故意写死 "v1" 与字段顺序：把 protocolVersion 从指纹里去掉、或改掉它的值，
// 都必须让这条用例变红——否则「改了编号协议却继续复用旧译文」这类线上事故没有任何用例挡着。
func TestFingerprintPinsCacheKeyInputs(t *testing.T) {
	cfg := testConfig()
	tr := newTestTranslator(t, &fakeBackend{out: echoNumbered}, newMemStore(), cfg)
	const want = "v1|假后端|test-model||zh-CN"
	if got := tr.(*translator).fingerprint(); got != want {
		t.Fatalf("缓存指纹组成变了：\n实际 %q\n期望 %q", got, want)
	}
}

// TestCacheKeyDependsOnTargetLang 校验换目标语言后不复用旧译文：
// 指纹里少了 TargetLang，用户把 target_lang 改掉以后还会一直看到旧语言的缓存译文。
func TestCacheKeyDependsOnTargetLang(t *testing.T) {
	store := newMemStore()
	backend := &fakeBackend{out: echoNumbered}
	trZH := newTestTranslator(t, backend, store, testConfig())
	if _, err := trZH.Translate(context.Background(), []string{"Hold the line"}); err != nil {
		t.Fatalf("首次翻译失败：%v", err)
	}
	other := testConfig()
	other.TargetLang = "en" // 只换目标语言
	trEN := newTestTranslator(t, backend, store, other)
	if _, err := trEN.Translate(context.Background(), []string{"Hold the line"}); err != nil {
		t.Fatalf("换语言后翻译失败：%v", err)
	}
	if backend.callCount() != 2 {
		t.Fatalf("换目标语言后不应命中旧缓存，实际调用 %d 次", backend.callCount())
	}
}

// needsTranslationCases 是共用判据的取值表：两个方向的边界都要钉住。
var needsTranslationCases = []struct {
	name string
	in   string
	want bool
}{
	{"全英文句子", "MAJOR ORDER WON", true},
	{"英文单词", "Victory", true},
	{"含空白的英文", "  Corrosive rain damages armor.  ", true},
	{"空串", "", false},
	{"纯空白", "   \n\t ", false},
	{"纯中文", "清剿机器人。", false},
	{"中文加数字", "消灭 200000000 名士兵。", false},
	{"只有两个拉丁字母（不够一段）", "OK", false},
	{"正好三个字母（达到阈值）", "ORB", true},
	{"三字母单词组成的一句话", "The war", true},
	{"短尾巴加中文（罗马数字 / 缩写）", "天园六IV 失守", false},
	{"只有数字与符号", "123 - 456", false},
	{"中英混排：层会真的送它", "MAJOR ORDER WON 士兵们，干得漂亮。", true},
}

// TestNeedsTranslationPredicate 校验共用判据与翻译层内部完全一致：
// 判中的是「这一段会不会被真的送出去」，不是「它看起来像不像中文」。
// 中英混排必须判 true——翻译层会把它送出去并翻成中文，这里判 false 会把翻好的译文丢掉。
func TestNeedsTranslationPredicate(t *testing.T) {
	for _, c := range needsTranslationCases {
		t.Run(c.name, func(t *testing.T) {
			if got := NeedsTranslation(c.in); got != c.want {
				t.Errorf("NeedsTranslation(%q) = %v，期望 %v", c.in, got, c.want)
			}
			// 导出的是内部判据的转发：两条路径必须永远同进同退。
			if got := needsTranslation(c.in); got != c.want {
				t.Errorf("内部 needsTranslation(%q) = %v，期望 %v", c.in, got, c.want)
			}
		})
	}
}

// TestMovedChineseSourceIsNeverMoved 校验「原文本来就是中文」时回显译文不算翻动了。
//
// 这是本次修复的核心：中文简报 + 透传 / 回显后端，过去会被判成「翻译失败」，
// 于是卡片上挂一句「翻译暂不可用，以下为英文原文」，而正文里一个英文都没有。
func TestMovedChineseSourceIsNeverMoved(t *testing.T) {
	for _, src := range []string{"清剿机器人。", "天园六IV 失守", "第二段简报。", "", "   "} {
		if Moved(src, src) {
			t.Errorf("中文/空白原文 %q 的回显不该算翻动", src)
		}
		if Moved(src, "完全不同的中文译文") {
			t.Errorf("原文 %q 不需要翻译，不该因为译文不同就算翻动", src)
		}
	}
}

// TestMovedEnglishEchoIsNotMoved 校验「真的送了但没翻动」仍然算失败：
// 后端原样回显、或只回空白，都必须判 false，调用方据此保留英文并注明。
func TestMovedEnglishEchoIsNotMoved(t *testing.T) {
	cases := []struct{ name, src, dst string }{
		{"原样回显", "Victory.", "Victory."},
		{"首尾空白不同的回显", "Victory.", "  Victory.  "},
		{"只回空白", "Victory.", "   "},
		{"只回换行", "Victory.", "\n\n"},
		{"回空串", "Victory.", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if Moved(c.src, c.dst) {
				t.Errorf("Moved(%q, %q) 应为 false", c.src, c.dst)
			}
		})
	}
}

// TestMovedEnglishTranslationIsMoved 校验真译出来了才算翻动。
func TestMovedEnglishTranslationIsMoved(t *testing.T) {
	if !Moved("Victory.", "胜利。") {
		t.Error("英文原文 + 中文译文应算翻动")
	}
	if !Moved("MAJOR ORDER WON 士兵们，干得漂亮。", "重大指令达成，士兵们干得漂亮。") {
		t.Error("中英混排（层会真的送它）翻出中文也应算翻动")
	}
}

// TestMovedBoundaryWhitespaceAndCase 明确 Moved 的两个「只差一点点」边界的方向，免得后人改判据时搞反：
//   - 只差首尾空白 ⇒ false：后端原样回显时前后各多了一点空白，仍属「没翻动」，调用方据此保留原文并注明；
//   - 只差大小写 ⇒ true：方向是宽松的，模型把整段话大小写改了也算动过这段文本。
//
// 为什么不用 strings.EqualFold 把大小写也收进「没翻动」：一旦收窄，后端把 "Victory." 回成 "victory."
// 就会被判成翻译失败，卡片上多挂一句「翻译暂不可用」，而正文里其实是有内容的英文——观感比照原样显示更差。
// （两边都是「宁可少报故障」，与本包「失败一律回退原文、err 只用于提示」的口径一致。）
func TestMovedBoundaryWhitespaceAndCase(t *testing.T) {
	if Moved("Victory.", "  Victory.  ") {
		t.Error("只差首尾空白应算没翻动（后端原样回显）")
	}
	if !Moved("Victory.", "victory.") {
		t.Error("只差大小写应算翻动（方向：宽松，不把大小写差异当故障）")
	}
}

// newTranslatorWithoutBackend 造一个「开着翻译但没有可用后端」的翻译层。
//
// 触发条件是真实存在的：用户配 mode=openai 却忘了填 api_url（或把 free_api_url 清空／写错），
// 这时 newBackend 返回 nil，New 只能回退透传，日志里写的是「翻译整体停用，正文保持英文」。
func newTranslatorWithoutBackend(t *testing.T) Translator {
	t.Helper()
	cfg := testConfig()
	cfg.Mode = "openai"
	cfg.APIURL = ""
	tr, err := New(cfg, Deps{Logger: func(string, ...any) {}})
	if err != nil {
		t.Fatalf("构造翻译层失败：%v", err)
	}
	return tr
}

// TestIsPassthroughTruthTable 校验「透传实现」的识别判据。
//
// 装配阶段（main.pluginTranslator）靠它在两种兜底情况下把翻译层归一成 nil：
// 用户主动关掉翻译、以及开着翻译却选不出后端。漏掉任何一种，群里每张卡片都会挂一句
// 假的「翻译暂不可用」，而日志里写的却是「翻译整体停用」。
func TestIsPassthroughTruthTable(t *testing.T) {
	disabled := testConfig()
	disabled.Enabled = false

	cases := []struct {
		name string
		tr   Translator
		want bool
	}{
		{"Passthrough() 本身", Passthrough(), true},
		{"关闭翻译时 New 的返回值", newTestTranslator(t, nil, nil, disabled), true},
		{"开着翻译但没有可用后端", newTranslatorWithoutBackend(t), true},
		{"开着翻译且有正常后端", newTestTranslator(t, &fakeBackend{out: echoNumbered}, newMemStore(), testConfig()), false},
		{"nil 不是透传（它是更强的语义）", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsPassthrough(c.tr); got != c.want {
				t.Errorf("IsPassthrough(%T) = %v，期望 %v", c.tr, got, c.want)
			}
		})
	}
}
