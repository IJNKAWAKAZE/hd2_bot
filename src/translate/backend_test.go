package translate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestSelectBackendName 校验 auto 规则与显式 mode（spec §4.1）。
func TestSelectBackendName(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{"auto 且地址与 key 齐全且以 /v1 结尾", Config{Mode: "auto", APIURL: "https://api.openai.com/v1", APIKey: "sk-x"}, "openai"},
		{"auto 且地址以 /chat/completions 结尾", Config{Mode: "auto", APIURL: "https://proxy.example/chat/completions", APIKey: "sk-x"}, "openai"},
		{"auto 但缺 key", Config{Mode: "auto", APIURL: "https://api.openai.com/v1"}, "free"},
		{"auto 但地址不是 /v1 结尾", Config{Mode: "auto", APIURL: "https://api.example/translate", APIKey: "sk-x"}, "free"},
		{"mode 为空按 auto", Config{APIURL: "https://api.openai.com/v1", APIKey: "sk-x"}, "openai"},
		{"显式 free", Config{Mode: "free", APIURL: "https://api.openai.com/v1", APIKey: "sk-x"}, "free"},
		{"显式 openai", Config{Mode: "openai"}, "openai"},
		{"大小写与空白归一", Config{Mode: "  AUTO ", APIURL: "https://api.openai.com/v1", APIKey: "sk-x"}, "openai"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := selectBackendName(c.cfg); got != c.want {
				t.Fatalf("selectBackendName = %q，期望 %q", got, c.want)
			}
		})
	}
}

// TestNewBackendSelectsImplementation 校验 newBackend 按 selectBackendName 的结果建出对应实现：
// 缺地址或 mode 未知时返回 nil 并留下一条中文警告，调用方据此回退透传（不阻止启动）。
func TestNewBackendSelectsImplementation(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want string // 期望的后端名；空串表示「没有可用后端」
	}{
		{"显式 openai", Config{Mode: "openai", APIURL: "https://api.example/v1", APIKey: "sk-x"}, "OpenAI 兼容接口"},
		{"显式 free", Config{Mode: "free", FreeAPIURL: "https://api.example/translate"}, "免费翻译接口"},
		{"openai 缺地址", Config{Mode: "openai"}, ""},
		{"free 缺地址", Config{Mode: "free"}, ""},
		{"未知 mode", Config{Mode: "gpt"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var lines []string
			logf := func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
			got := newBackend(c.cfg, logf)
			if c.want == "" {
				if got != nil {
					t.Fatalf("不应建出后端，实际 %T", got)
				}
				if len(lines) == 0 {
					t.Fatal("没有可用后端时应记一条中文警告")
				}
				return
			}
			if got == nil {
				t.Fatalf("应建出 %q，实际 nil", c.want)
			}
			if got.Name() != c.want {
				t.Fatalf("后端名错误：%q，期望 %q", got.Name(), c.want)
			}
		})
	}
}

// TestNewFallsBackToPassthroughWithoutBackend 校验没有可用后端时只记警告、回退透传，不阻止启动。
func TestNewFallsBackToPassthroughWithoutBackend(t *testing.T) {
	var lines []string
	logf := func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	cfg := testConfig()
	cfg.Mode = "free"
	cfg.FreeAPIURL = ""
	tr, err := New(cfg, Deps{Logger: logf})
	if err != nil {
		t.Fatalf("没有后端不应报错（只警告）：%v", err)
	}
	got, err := tr.Translate(context.Background(), []string{"Hold the line"})
	if err != nil || got[0] != "Hold the line" {
		t.Fatalf("应回退透传，实际 %v err=%v", got, err)
	}
	if len(lines) == 0 {
		t.Fatal("没有后端时应记一条中文警告")
	}
}

// TestFreeBackendUapisPayload 校验免费接口的请求形态：/ai/translate 路径带 target_lang 查询参数，
// 请求体是参照实现里的 {text, style, context, preserve_format}。
func TestFreeBackendUapisPayload(t *testing.T) {
	var gotPath, gotQuery, gotMethod, gotCT string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		gotMethod, gotCT = r.Method, r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("解析请求体失败：%v", err)
		}
		_, _ = w.Write([]byte(`{"translatedText":"你好"}`))
	}))
	defer server.Close()

	backend := newFreeBackend(testConfig(), server.URL+"/api/v1/ai/translate")
	out, err := backend.Translate(context.Background(), []string{"Hello"})
	if err != nil || len(out) != 1 || out[0] != "你好" {
		t.Fatalf("翻译结果错误：%v err=%v", out, err)
	}
	if gotPath != "/api/v1/ai/translate" {
		t.Errorf("请求路径错误：%q", gotPath)
	}
	// 必须精确等于 zh：只做子串匹配时 "target_lang=zh-CN" 也「包含 zh」，映射写错照样绿。
	values, err := url.ParseQuery(gotQuery)
	if err != nil {
		t.Fatalf("查询参数解析失败：%v", err)
	}
	if got := values.Get("target_lang"); got != "zh" {
		t.Errorf("查询参数应带 target_lang=zh（zh-CN 会映射成 zh），实际 %q", got)
	}
	if gotBody["text"] != "Hello" || gotBody["preserve_format"] != true {
		t.Errorf("请求体不符合参照实现的形态：%v", gotBody)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("请求方法应为 POST，实际 %q", gotMethod)
	}
	if !strings.HasPrefix(gotCT, "application/json") {
		t.Errorf("Content-Type 应为 application/json，实际 %q", gotCT)
	}
}

// TestFreeBackendUapisPayloadToleratesTrailingSlash 校验 /ai/translate/ 这类带尾斜杠的地址
// 仍被认成 uapis 形态：不归一尾斜杠就会静默退回通用请求体，连 target_lang 都不带。
func TestFreeBackendUapisPayloadToleratesTrailingSlash(t *testing.T) {
	var gotPath, gotQuery string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"translatedText":"你好"}`))
	}))
	defer server.Close()

	backend := newFreeBackend(testConfig(), server.URL+"/api/v1/ai/translate/")
	out, err := backend.Translate(context.Background(), []string{"Hello"})
	if err != nil || out[0] != "你好" {
		t.Fatalf("翻译结果错误：%v err=%v", out, err)
	}
	if gotPath != "/api/v1/ai/translate/" {
		t.Errorf("请求路径错误：%q", gotPath)
	}
	values, err := url.ParseQuery(gotQuery)
	if err != nil {
		t.Fatalf("查询参数解析失败：%v", err)
	}
	if got := values.Get("target_lang"); got != "zh" {
		t.Errorf("尾斜杠地址也应走 uapis 形态并带 target_lang=zh，实际 %q（查询 %q）", got, gotQuery)
	}
	if _, ok := gotBody["preserve_format"]; !ok {
		t.Errorf("尾斜杠地址应走 uapis 请求体，实际 %v", gotBody)
	}
}

// TestFreeBackendGenericPayload 校验非 /ai/translate 地址走通用请求体。
func TestFreeBackendGenericPayload(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"translation":"你好"}`))
	}))
	defer server.Close()

	backend := newFreeBackend(testConfig(), server.URL+"/translate")
	out, err := backend.Translate(context.Background(), []string{"Hello"})
	if err != nil || out[0] != "你好" {
		t.Fatalf("翻译结果错误：%v err=%v", out, err)
	}
	if gotBody["sourceLanguage"] != "auto" || gotBody["targetLanguage"] != "zh-CN" {
		t.Errorf("通用请求体不正确：%v", gotBody)
	}
}

// TestFreeBackendExtractsNestedEnvelope 校验响应信封的兼容：译文可能在 data 里。
func TestFreeBackendExtractsNestedEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":{"translated_text":"你好"}}`))
	}))
	defer server.Close()

	backend := newFreeBackend(testConfig(), server.URL+"/translate")
	out, err := backend.Translate(context.Background(), []string{"Hello"})
	if err != nil || out[0] != "你好" {
		t.Fatalf("应能从 data 信封里取出译文：%v err=%v", out, err)
	}
}

// TestExtractFreeTranslationVariants 校验各种信封形态与「没有译文」的判定：
// 字段名变体、非字符串、空白串、data 不是对象都算「没译文」，由调用方按失败处理。
func TestExtractFreeTranslationVariants(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"顶层 translatedText", `{"translatedText":"你好"}`, "你好"},
		{"顶层 translated_text", `{"translated_text":"你好"}`, "你好"},
		{"顶层 translation", `{"translation":"你好"}`, "你好"},
		{"顶层 result", `{"result":"你好"}`, "你好"},
		{"data 嵌套且去空白", `{"code":0,"data":{"translated_text":" 你好 "}}`, "你好"},
		{"字段是数字", `{"translatedText":123}`, ""},
		{"字段是空白串", `{"translatedText":"   "}`, ""},
		{"data 不是对象", `{"data":["你好"]}`, ""},
		{"啥都没有", `{"code":0,"message":"ok"}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var envelope map[string]any
			if err := json.Unmarshal([]byte(c.body), &envelope); err != nil {
				t.Fatalf("用例本身的 JSON 有问题：%v", err)
			}
			if got := extractFreeTranslation(envelope); got != c.want {
				t.Fatalf("extractFreeTranslation = %q，期望 %q", got, c.want)
			}
		})
	}
}

// TestTopKeysIsSortedAndCapped 校验排查用的键名清单有序且最多 8 个：
// 上游换了响应形态时我们要能一眼看出键名，但也不能把整个脏响应塞进日志。
func TestTopKeysIsSortedAndCapped(t *testing.T) {
	envelope := map[string]any{}
	for _, key := range []string{"k9", "k3", "k1", "k7", "k2", "k8", "k0", "k5", "k4", "k6"} {
		envelope[key] = 1
	}
	got := topKeys(envelope)
	if len(got) != 8 {
		t.Fatalf("应最多返回 8 个键，实际 %d 个：%v", len(got), got)
	}
	if !sort.StringsAreSorted(got) {
		t.Fatalf("键名应排好序，实际 %v", got)
	}
}

// TestFreeBackendReportsStatusErrors 校验 429/5xx 走 statusError（翻译层据此冷却），
// 其它状态码与「响应里没有译文」都只是普通错误。
func TestFreeBackendReportsStatusErrors(t *testing.T) {
	cases := []struct {
		code     int
		cooldown bool
	}{
		{429, true}, {503, true}, {400, false}, {200, false},
	}
	for _, c := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(c.code)
			_, _ = w.Write([]byte(`{"message":"nope"}`))
		}))
		backend := newFreeBackend(testConfig(), server.URL+"/translate")
		_, err := backend.Translate(context.Background(), []string{"Hello"})
		server.Close()
		if err == nil {
			t.Fatalf("状态码 %d 时应报错", c.code)
		}
		if got := isCooldownError(err); got != c.cooldown {
			t.Fatalf("状态码 %d 的冷却判定错误：%v", c.code, got)
		}
	}
}

// TestStatusErrorMessageIsTerse 校验 statusError 的文案只带后端名与状态码：
// 响应体可能含用户内容或供应商提示，日志与卡片提示里都不该出现。
func TestStatusErrorMessageIsTerse(t *testing.T) {
	err := error(&statusError{code: 503, backend: "免费翻译接口"})
	got := err.Error()
	if !strings.Contains(got, "503") || !strings.Contains(got, "免费翻译接口") {
		t.Fatalf("文案应带后端名与状态码：%q", got)
	}
}

// TestFreeBackendRejectsNonJSONResponse 校验响应不是 JSON 时返回错误（而不是 panic 或空译文）。
func TestFreeBackendRejectsNonJSONResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html>502 Bad Gateway</html>`))
	}))
	defer server.Close()

	backend := newFreeBackend(testConfig(), server.URL+"/translate")
	out, err := backend.Translate(context.Background(), []string{"Hello"})
	if err == nil {
		t.Fatalf("非 JSON 响应应报错，实际返回 %v", out)
	}
	if isCooldownError(err) {
		t.Fatalf("解析失败不该触发冷却（本次不巧而已）：%v", err)
	}
}

// TestFreeBackendToleratesOversizedResponse 校验超长响应：只读前 1 MiB，
// 截断后 JSON 不完整就报错，既不 panic 也不会把内存吃满。
func TestFreeBackendToleratesOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"translatedText":"`))
		chunk := strings.Repeat("a", 4096)
		for i := 0; i < 512; i++ { // 约 2 MiB，远超 1 MiB 上限
			_, _ = w.Write([]byte(chunk))
		}
		_, _ = w.Write([]byte(`"}`))
	}))
	defer server.Close()

	backend := newFreeBackend(testConfig(), server.URL+"/translate")
	if _, err := backend.Translate(context.Background(), []string{"Hello"}); err == nil {
		t.Fatal("被截断的响应应报错")
	}
}

// TestFreeBackendHonorsContextDeadline 校验上下文超时：请求立刻返回错误，
// 且错误可用 errors.Is 判定成 context.DeadlineExceeded（上层据此区分「被取消」与「后端限流」）。
// 客户端超时故意设得远大于 ctx：请求若没带 ctx（NewRequest 而不是 NewRequestWithContext），
// 就会一直等到客户端超时，这条用例必须变红。
func TestFreeBackendHonorsContextDeadline(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		// 挂住不响应，直到用例收尾：模拟「接口卡死」。不能只等 r.Context().Done()——
		// 客户端超时后服务端的请求上下文未必立刻取消，会在 server.Close() 时把用例挂死。
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close() // 先注册、后执行
	defer close(release) // 后注册、先执行：先放行 handler，server.Close 才不会卡住

	cfg := testConfig()
	cfg.Timeout = 10 * time.Second // 远大于下面 ctx 的 50ms：请求若不带 ctx 就得等满 10 秒
	backend := newFreeBackend(cfg, server.URL+"/translate")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := backend.Translate(ctx, []string{"Hello"})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("ctx 超时应返回错误")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("错误应可用 errors.Is 判定为超时，实际 %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("请求没有跟着 ctx 取消，耗时 %s", elapsed)
	}
}

// TestFreeBackendErrorHidesQuerySecrets 校验地址里的敏感查询参数不进错误文案：
// 免费接口常把 key 放进查询串，而错误文案会进日志与卡片提示。
func TestFreeBackendErrorHidesQuerySecrets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	endpoint := server.URL + "/translate?api_key=SECRET-TOKEN"
	server.Close() // 关掉服务端，让这次请求必然失败

	backend := newFreeBackend(testConfig(), endpoint)
	_, err := backend.Translate(context.Background(), []string{"Hello"})
	if err == nil {
		t.Fatal("连不上时应报错")
	}
	if strings.Contains(err.Error(), "SECRET-TOKEN") {
		t.Fatalf("错误文案里出现了敏感查询参数：%v", err)
	}
}

// TestFreeBackendUapisPayloadKeepsExistingQuery 校验地址本身已带查询参数时用 & 连接，
// 不会把调用方配好的参数顶掉（免费接口常要求 token 之类的查询参数）。
func TestFreeBackendUapisPayloadKeepsExistingQuery(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"translatedText":"你好"}`))
	}))
	defer server.Close()

	backend := newFreeBackend(testConfig(), server.URL+"/api/v1/ai/translate?token=abc")
	if _, err := backend.Translate(context.Background(), []string{"Hello"}); err != nil {
		t.Fatalf("翻译失败：%v", err)
	}
	// 精确比较参数对：只做子串匹配时，用 "?" 硬拼出的 "token=abc?target_lang=zh" 照样「包含」两个子串。
	values, err := url.ParseQuery(gotQuery)
	if err != nil {
		t.Fatalf("查询参数解析失败：%v", err)
	}
	if values.Get("token") != "abc" {
		t.Fatalf("已有查询参数应原样保留，实际 token=%q（查询 %q）", values.Get("token"), gotQuery)
	}
	if values.Get("target_lang") != "zh" {
		t.Fatalf("应追加 target_lang=zh，实际 %q（查询 %q）", values.Get("target_lang"), gotQuery)
	}
	// RawQuery 里不该再出现 ?：用 "?" 硬拼会得到 "token=abc?target_lang=zh"。
	if strings.Contains(gotQuery, "?") {
		t.Fatalf("已有查询串时必须用 & 连接，实际 %q", gotQuery)
	}
}

// TestUapiTargetLangMapping 校验 target_lang 的换算表：简中几种写法都映射成 zh，其它语言原样透传。
func TestUapiTargetLangMapping(t *testing.T) {
	cases := map[string]string{
		"zh-CN":   "zh",
		"ZH-Hans": "zh",
		"zh-SG":   "zh",
		"  ":      "zh",
		"en":      "en",
		"ja-JP":   "ja-JP",
	}
	for in, want := range cases {
		if got := uapiTargetLang(in); got != want {
			t.Errorf("uapiTargetLang(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// TestRedactURLHidesQueryAndUserInfo 校验地址脱敏：只保留「协议://主机」，路径与查询参数一律去掉
// （路径段里同样可能放 key），解析不出主机名时给占位文案。
func TestRedactURLHidesQueryAndUserInfo(t *testing.T) {
	const hidden = "（路径与参数已隐去）"
	cases := map[string]string{
		"https://user:pass@example.com/v1?api_key=SECRET": "https://example.com" + hidden,
		"http://example.com/translate":                    "http://example.com" + hidden,
		"https://gateway.example/v1/sk-SECRET/translate":  "https://gateway.example" + hidden,
		"http://[::1": "（地址已隐去）",
	}
	for in, want := range cases {
		got := redactURL(in)
		if got != want {
			t.Errorf("redactURL(%q) = %q，期望 %q", in, got, want)
		}
		if strings.Contains(got, "SECRET") {
			t.Errorf("脱敏后仍带敏感内容：%q", got)
		}
	}
}

// TestWrapRequestErrorKeepsCause 校验错误包装的两条分支：
// 非 net/http 错误照常包装；*url.Error 的地址被裁成协议+主机+路径，底层原因仍可用 errors.Is 判定。
func TestWrapRequestErrorKeepsCause(t *testing.T) {
	err := wrapRequestError(fmt.Errorf("底层：%w", context.Canceled))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("原因应被保留（%%w）：%v", err)
	}
	if !strings.Contains(err.Error(), "请求翻译接口失败") {
		t.Fatalf("文案应说明是请求失败：%v", err)
	}
	wrapped := wrapRequestError(&url.Error{Op: "Post", URL: "https://example.com/v1?api_key=SECRET", Err: context.DeadlineExceeded})
	if strings.Contains(wrapped.Error(), "SECRET") {
		t.Fatalf("地址里的查询参数不该出现：%v", wrapped)
	}
	if !errors.Is(wrapped, context.DeadlineExceeded) {
		t.Fatalf("url.Error 的原因应被保留：%v", wrapped)
	}
}

// TestOpenAIBackendSendsChatRequest 校验 OpenAI 兼容后端的请求形态与响应解析。
func TestOpenAIBackendSendsChatRequest(t *testing.T) {
	var gotPath, gotAuth, gotMethod, gotCT string
	var gotBody struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		Messages  []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		gotMethod, gotCT = r.Method, r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"  \"你好\"  "}}]}`))
	}))
	defer server.Close()

	cfg := testConfig()
	cfg.APIURL = server.URL + "/v1"
	cfg.APIKey = "sk-test"
	backend := newOpenAIBackend(cfg)
	out, err := backend.Translate(context.Background(), []string{"Hello"})
	if err != nil || out[0] != "你好" {
		t.Fatalf("翻译结果错误（应顺带剥掉引号）：%v err=%v", out, err)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("应在 /v1 后追加 chat/completions，实际 %q", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("缺少 Authorization 头：%q", gotAuth)
	}
	if gotBody.Model != cfg.Model || gotBody.MaxTokens != cfg.MaxTokens {
		t.Errorf("请求体模型/上限不对：%+v", gotBody)
	}
	if len(gotBody.Messages) != 2 || gotBody.Messages[0].Role != "system" || gotBody.Messages[1].Content != "Hello" {
		t.Errorf("消息结构不对：%+v", gotBody.Messages)
	}
	if !strings.Contains(gotBody.Messages[0].Content, "[[HD2_1]]") {
		t.Error("系统提示词必须写明编号标记的保留要求")
	}
	if gotMethod != http.MethodPost {
		t.Errorf("请求方法应为 POST，实际 %q", gotMethod)
	}
	if !strings.HasPrefix(gotCT, "application/json") {
		t.Errorf("Content-Type 应为 application/json，实际 %q", gotCT)
	}
}

// TestOpenAIBackendOmitsEmptyFields 校验可省字段：max_tokens=0 时不发该字段，
// api_key 为空时不发 Authorization 头（发一个空的 Bearer 反而会让网关直接 401）。
func TestOpenAIBackendOmitsEmptyFields(t *testing.T) {
	var raw map[string]any
	var authHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&raw)
		authHeader = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"你好"}}]}`))
	}))
	defer server.Close()

	cfg := testConfig()
	cfg.APIURL = server.URL + "/v1"
	cfg.APIKey = ""
	cfg.MaxTokens = 0
	backend := newOpenAIBackend(cfg)
	if _, err := backend.Translate(context.Background(), []string{"Hello"}); err != nil {
		t.Fatalf("翻译失败：%v", err)
	}
	if _, ok := raw["max_tokens"]; ok {
		t.Errorf("max_tokens 为 0 时不应发送该字段：%v", raw)
	}
	if authHeader != "" {
		t.Errorf("api_key 为空时不应发送 Authorization 头：%q", authHeader)
	}
}

// TestOpenAIBackendHandlesDegenerateResponses 校验畸形响应的确定行为：
// 缺 choices、内容为空、甚至不是 JSON，都只报错，不 panic、不返回空译文。
func TestOpenAIBackendHandlesDegenerateResponses(t *testing.T) {
	responses := []string{
		`{"choices":[]}`,
		`{}`,
		`{"choices":[{"message":{"content":"   "}}]}`,
		`<html>nope</html>`,
	}
	for _, body := range responses {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		cfg := testConfig()
		cfg.APIURL = server.URL
		cfg.APIKey = "sk-test"
		out, err := newOpenAIBackend(cfg).Translate(context.Background(), []string{"Hello"})
		server.Close()
		if err == nil {
			t.Fatalf("响应 %q 时应报错，实际返回 %v", body, out)
		}
	}
}

// TestChatCompletionsURL 校验地址拼接：路径归位、查询串原样保留（拼在路径里会把 URL 拼坏）。
func TestChatCompletionsURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://api.openai.com/v1", "https://api.openai.com/v1/chat/completions"},
		{"https://api.openai.com/v1/", "https://api.openai.com/v1/chat/completions"},
		{"https://proxy.example/chat/completions", "https://proxy.example/chat/completions"},
		{"https://proxy.example/openai", "https://proxy.example/openai/chat/completions"},
		{"https://proxy.example/v1?api-version=2024-02-01", "https://proxy.example/v1/chat/completions?api-version=2024-02-01"},
		{"https://proxy.example/deployments/d/chat/completions?api-version=1", "https://proxy.example/deployments/d/chat/completions?api-version=1"},
		{"https://proxy.example/v1/?api-version=1", "https://proxy.example/v1/chat/completions?api-version=1"},
		// 解析不出主机名（写成相对路径）时退回纯拼接：不 panic，也不把地址丢掉。
		{"/v1", "/v1/chat/completions"},
		{"/v1/", "/v1/chat/completions"},
		{"/v1/chat/completions", "/v1/chat/completions"},
		{"", "/chat/completions"},
	}
	for _, c := range cases {
		if got := chatCompletionsURL(c.in); got != c.want {
			t.Errorf("chatCompletionsURL(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// TestOpenAIBackendHandlesFinishReason 校验 finish_reason=length（被 max_tokens 截断）视为失败：
// 截断的响应看着「有内容」，采纳就会把半句话贴到卡片上。
func TestOpenAIBackendHandlesFinishReason(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{"stop 正常", `{"choices":[{"finish_reason":"stop","message":{"content":"你好"}}]}`, false},
		{"字段缺失按正常", `{"choices":[{"message":{"content":"你好"}}]}`, false},
		{"length 截断", `{"choices":[{"finish_reason":"length","message":{"content":"[[HD2_1]] 半截的译"}}]}`, true},
		{"length 大写也认", `{"choices":[{"finish_reason":" LENGTH ","message":{"content":"半截"}}]}`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(c.body))
			}))
			defer server.Close()
			cfg := testConfig()
			cfg.APIURL = server.URL + "/v1"
			cfg.APIKey = "sk-test"
			out, err := newOpenAIBackend(cfg).Translate(context.Background(), []string{"Hello"})
			if c.wantErr {
				if err == nil {
					t.Fatalf("应报错，实际返回 %v", out)
				}
				if !strings.Contains(err.Error(), "截断") {
					t.Fatalf("错误文案应说明译文被截断：%v", err)
				}
				return
			}
			if err != nil || len(out) != 1 || out[0] != "你好" {
				t.Fatalf("应正常返回译文：%v err=%v", out, err)
			}
		})
	}
}

// TestOpenAIPromptCarriesTargetLanguage 校验目标语言进了系统提示词，且与缓存指纹里的
// TargetLang 一致：提示词写死「简体中文」时，配了别的目标语言的用户拿不到想要的译文。
func TestOpenAIPromptCarriesTargetLanguage(t *testing.T) {
	cases := []struct{ lang, want string }{
		{"zh-CN", "简体中文"},
		{"", "简体中文"},
		{"zh-TW", "繁体中文"},
		{"ja-JP", "ja-JP"},
	}
	for _, c := range cases {
		cfg := testConfig()
		cfg.TargetLang = c.lang
		backend := newOpenAIBackend(cfg)
		if !strings.Contains(backend.prompt, c.want) {
			t.Errorf("target_lang=%q 的提示词里应出现 %q，实际 %q", c.lang, c.want, backend.prompt)
		}
		if !strings.Contains(backend.prompt, "[[HD2_1]]") {
			t.Errorf("提示词必须写明编号标记的保留要求，实际 %q", backend.prompt)
		}
	}
}

// TestOpenAIBackendHonorsContextDeadline 校验 OpenAI 后端的请求是带 ctx 发出的、能被及时取消：
// 把 NewRequestWithContext 换成 NewRequest 后，这里会一直等到客户端超时（用例必须变红）。
func TestOpenAIBackendHonorsContextDeadline(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close() // 先注册、后执行
	defer close(release) // 后注册、先执行：先放行 handler，server.Close 才不会卡住

	cfg := testConfig()
	cfg.APIURL = server.URL + "/v1"
	cfg.APIKey = "sk-test"
	cfg.Timeout = 10 * time.Second // 远大于下面 ctx 的 50ms：请求若不带 ctx 就得等满 10 秒
	backend := newOpenAIBackend(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := backend.Translate(ctx, []string{"Hello"})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("ctx 超时应返回错误")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("错误应可用 errors.Is 判定为超时，实际 %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("请求没有跟着 ctx 取消，耗时 %s", elapsed)
	}
}

// TestBackendsRequireSingleText 校验两个后端的入参契约：一次只接受 1 段文本。
// 上层是「整批编号后只发一次请求」，限速名额也按「一批一次请求」预约；
// 一个批次塞多段会让实际请求数与预约次数对不上。
func TestBackendsRequireSingleText(t *testing.T) {
	// 假服务端同时认识两种响应形态；一旦被请求到就把计数加一——
	// 只断言「返回了错误」不够：契约被删掉时请求会真的发出去，拿到的多半是别的错误而照样绿。
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{"translatedText":"你好","choices":[{"finish_reason":"stop","message":{"content":"你好"}}]}`))
	}))
	defer server.Close()

	cfg := testConfig()
	cfg.APIURL = server.URL + "/v1"
	cfg.APIKey = "sk-x"
	cfg.FreeAPIURL = server.URL + "/translate"
	backends := map[string]Backend{
		"OpenAI 兼容接口": newOpenAIBackend(cfg),
		"免费翻译接口":      newFreeBackend(cfg, cfg.FreeAPIURL),
	}
	for name, backend := range backends {
		t.Run(name, func(t *testing.T) {
			if _, err := backend.Translate(context.Background(), []string{"A", "B"}); err == nil {
				t.Fatal("多段入参应被拒绝")
			}
			if _, err := backend.Translate(context.Background(), nil); err == nil {
				t.Fatal("空入参应被拒绝")
			}
			if got := atomic.LoadInt32(&hits); got != 0 {
				t.Fatalf("入参不合法时不该发出任何请求，实际发了 %d 次", got)
			}
		})
	}
}

// TestExtractFreeTranslationPrefersTopLevel 校验优先级：顶层字段优先于 data.* 里的同名字段。
func TestExtractFreeTranslationPrefersTopLevel(t *testing.T) {
	envelope := map[string]any{
		"translatedText": "顶层译文",
		"data":           map[string]any{"translated_text": "嵌套译文"},
	}
	if got := extractFreeTranslation(envelope); got != "顶层译文" {
		t.Fatalf("应优先取顶层字段，实际 %q", got)
	}
	// 顶层没有可用值时仍然要能取到 data 里的译文。
	nestedOnly := map[string]any{"code": 0, "data": map[string]any{"translated_text": "嵌套译文"}}
	if got := extractFreeTranslation(nestedOnly); got != "嵌套译文" {
		t.Fatalf("顶层没有译文时应回落到 data，实际 %q", got)
	}
}

// TestFreeBackendKeepsNonASCIIRequestBody 校验非 ASCII 文本经 HTTP 往返后仍然一致：
// 请求体编码出问题（丢掉中文）时，送译的输入会悄悄变味，译文却看不出错。
func TestFreeBackendKeepsNonASCIIRequestBody(t *testing.T) {
	const text = "Hello 你好，绝地潜兵！"
	var gotText string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("读取请求体失败：%v", err)
		}
		if !strings.Contains(string(raw), text) {
			t.Errorf("请求体里应原样保留非 ASCII 文本，实际 %q", string(raw))
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("请求体不是合法 JSON：%v", err)
		}
		gotText, _ = body["text"].(string)
		_, _ = w.Write([]byte(`{"translatedText":"你好"}`))
	}))
	defer server.Close()

	backend := newFreeBackend(testConfig(), server.URL+"/translate")
	if _, err := backend.Translate(context.Background(), []string{text}); err != nil {
		t.Fatalf("翻译失败：%v", err)
	}
	if gotText != text {
		t.Fatalf("请求体里的文本被改动了：%q", gotText)
	}
}

// TestCacheKeyUsesPreReplacedText 校验缓存键用的是「预替换前」的清洗原文：
// 词表更新后旧缓存仍然复用（否则每加一个词条就要把整库重翻一遍）。
func TestCacheKeyUsesPreReplacedText(t *testing.T) {
	store := newMemStore()
	backend := &fakeBackend{out: echoNumbered}
	tr := newTestTranslator(t, backend, store, testConfig())
	const text = "Extract the civilians"
	if got := PreReplace(text); got == text {
		t.Fatalf("用例前提不成立：%q 没有被词表替换", text)
	}
	if _, err := tr.Translate(context.Background(), []string{text}); err != nil {
		t.Fatalf("翻译失败：%v", err)
	}
	key := tr.(*translator).cacheKey
	if _, ok := store.data[cacheKind+":"+key(text)]; !ok {
		t.Fatal("缓存键应基于预替换前的原文")
	}
	if _, ok := store.data[cacheKind+":"+key(PreReplace(text))]; ok {
		t.Fatal("缓存键不该基于预替换后的文本（词表一变整库缓存就失效）")
	}
	// 只断言写进去的键还不够：把「查键」也改成预替换后的文本时，键仍然写对了，
	// 但每次都会查不中，缓存形同虚设。再翻一次同一段，必须命中缓存、不再打后端。
	if _, err := tr.Translate(context.Background(), []string{text}); err != nil {
		t.Fatalf("第二次翻译失败：%v", err)
	}
	if calls := backend.callCount(); calls != 1 {
		t.Fatalf("第二次应命中缓存，后端实际被调用 %d 次（查键与写键必须用同一份原文）", calls)
	}
}

// TestStripFences 校验模型偶尔套的代码围栏被剥掉。
func TestStripFences(t *testing.T) {
	if got := stripFences("```json\n[[HD2_1]] 你好\n```"); got != "[[HD2_1]] 你好" {
		t.Errorf("代码围栏未剥离：%q", got)
	}
	if got := stripFences("纯文本"); got != "纯文本" {
		t.Errorf("没有围栏时不应改动：%q", got)
	}
}

// TestTranslatePreReplacesGlossaryTerms 校验送译前先做术语预替换：后端收到的文本里已是中文译名。
func TestTranslatePreReplacesGlossaryTerms(t *testing.T) {
	backend := &fakeBackend{out: echoNumbered}
	tr := newTestTranslator(t, backend, newMemStore(), testConfig())
	if _, err := tr.Translate(context.Background(), []string{"Drop on Acamar IV"}); err != nil {
		t.Fatalf("翻译失败：%v", err)
	}
	if len(backend.inputs) == 0 {
		t.Fatal("后端应收到请求")
	}
	if !strings.Contains(backend.inputs[0], "天园六IV") {
		t.Fatalf("送译文本应已替换成官方译名，实际 %q", backend.inputs[0])
	}
	if strings.Contains(backend.inputs[0], "Acamar") {
		t.Fatalf("送译文本里不应残留英文原名，实际 %q", backend.inputs[0])
	}
}

// TestFreeBackendSmoke 真实调用一次免费接口；默认跳过，用 HD2_TRANSLATE_SMOKE=1 打开。
// 只在 Task 13 的真实验收里跑一次，平时保持离线全绿。
func TestFreeBackendSmoke(t *testing.T) {
	if os.Getenv("HD2_TRANSLATE_SMOKE") != "1" {
		t.Skip("未设置 HD2_TRANSLATE_SMOKE=1，跳过真实翻译冒烟")
	}
	cfg := testConfig()
	cfg.FreeAPIURL = "https://uapis.cn/api/v1/ai/translate"
	// testConfig 里的 1 秒是本机假服务端的余量，真实接口经常要几秒，这里放宽到 20 秒。
	cfg.Timeout = 20 * time.Second
	backend := newFreeBackend(cfg, cfg.FreeAPIURL)
	out, err := backend.Translate(context.Background(), []string{"[[HD2_1]] The Helldivers liberated Acamar IV."})
	if err != nil {
		t.Fatalf("真实接口调用失败：%v", err)
	}
	if len(out) != 1 || strings.TrimSpace(out[0]) == "" {
		t.Fatalf("真实接口返回了空文本：%v", out)
	}
	if out[0] == "[[HD2_1]] The Helldivers liberated Acamar IV." {
		t.Fatalf("真实接口把原文原样退了回来：%q", out[0])
	}
	t.Logf("真实接口返回：%q", out[0])
}
