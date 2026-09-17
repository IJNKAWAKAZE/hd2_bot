package translate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// freeAPI 是免费翻译接口后端：一次只吃一段文本。
// 请求体与响应解析形态取自社区参照实现 astrbot_plugin_Helldivers 的 translator.py（MIT）：
// 该实现同时兼容「uapis 的 /ai/translate 形态」与「通用 {text, sourceLanguage, targetLanguage} 形态」。
//
// 边界：接口通常按 IP 限流，429 会被上层判成「该冷却」；逐段请求（每段一次 HTTP）是刻意的取舍——
// 免费接口对多段拼接的保留格式支持不可靠，逐段换来的确定行为更值钱。
type freeAPI struct {
	url        string
	targetLang string
	client     *http.Client
}

// newFreeBackend 创建免费接口后端。
func newFreeBackend(cfg Config, endpoint string) *freeAPI {
	return &freeAPI{
		url:        strings.TrimSpace(endpoint),
		targetLang: strings.TrimSpace(cfg.TargetLang),
		client:     newHTTPClient(cfg.Timeout),
	}
}

// Name 返回后端名，用于日志与缓存指纹。
func (b *freeAPI) Name() string { return "免费翻译接口" }

// Translate 翻译「一批 = 一个编号文本」并返回等长的结果。
//
// 入参契约（调用方必须遵守）：texts 恰好 1 个元素。上层 translate.go 按整批编号后
// 只发一次请求（backend.Translate(ctx, []string{numbered})），限速名额也是按「一批一次请求」
// 预约的；一次传多段会让实际请求数与预约次数对不上（限速形同虚设），因此这里显式拒绝。
func (b *freeAPI) Translate(ctx context.Context, texts []string) ([]string, error) {
	if len(texts) != 1 {
		return nil, fmt.Errorf("免费翻译接口后端一次只接受 1 段文本，收到 %d 段", len(texts))
	}
	translated, err := b.request(ctx, texts[0])
	if err != nil {
		return nil, err
	}
	return []string{translated}, nil
}

// request 发一次请求并取出译文。
func (b *freeAPI) request(ctx context.Context, text string) (string, error) {
	endpoint := b.url
	var payload any
	// 先归一尾部斜杠：/ai/translate/ 是同一个端点，不归一就会静默退回通用请求体，
	// 既不报错也不带 target_lang，排起来毫无线索。
	if strings.HasSuffix(strings.ToLower(strings.TrimRight(urlPath(endpoint), "/")), "/ai/translate") {
		// uapis 形态：target_lang 走查询参数，正文放请求体，顺带声明文体与「保留格式」。
		endpoint = withQuery(endpoint, "target_lang", uapiTargetLang(b.targetLang))
		payload = map[string]any{
			"text":            strings.TrimSpace(text),
			"style":           "professional",
			"context":         "entertainment",
			"preserve_format": true,
		}
	} else {
		payload = map[string]any{
			"text":           strings.TrimSpace(text),
			"sourceLanguage": "auto",
			"targetLanguage": b.targetLang,
		}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("序列化翻译请求失败：%w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("构造翻译请求失败：%w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := b.client.Do(req)
	if err != nil {
		// 不直接透出 err：*url.Error 会带上完整地址（可能含 key 查询参数）。
		return "", wrapRequestError(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", &statusError{code: resp.StatusCode, backend: b.Name()}
	}
	// 只读前 1 MiB：上游异常时可能回一坨 HTML 或超长内容，读满会白占内存。
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("读取翻译响应失败：%w", err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return "", fmt.Errorf("解析翻译响应失败：%w", err)
	}
	translated := extractFreeTranslation(envelope)
	if translated == "" {
		// 带上顶层键名：上游改了响应形态时，日志里能直接看出是哪种形态。
		return "", fmt.Errorf("翻译响应里没有译文（顶层键 %v）", topKeys(envelope))
	}
	return translated, nil
}

// extractFreeTranslation 从响应里取译文：兼容常见的几种信封（顶层或 data 里）。
func extractFreeTranslation(envelope map[string]any) string {
	keys := []string{"translatedText", "translated_text", "translation", "result"}
	for _, key := range keys {
		if value, ok := readString(envelope, key); ok {
			return value
		}
	}
	if nested, ok := envelope["data"].(map[string]any); ok {
		for _, key := range keys {
			if value, ok := readString(nested, key); ok {
				return value
			}
		}
	}
	return ""
}

// readString 取出字符串字段并去掉首尾空白；非字符串或空串都算「没有」。
func readString(m map[string]any, key string) (string, bool) {
	value, ok := m[key]
	if !ok {
		return "", false
	}
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return "", false
	}
	return strings.TrimSpace(text), true
}

// topKeys 返回响应的顶层键名（排好序、最多 8 个），便于排查「响应形态变了」。
func topKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > 8 {
		keys = keys[:8]
	}
	return keys
}

// uapiTargetLang 把 target_lang 换算成该接口认的取值：zh-CN / zh-Hans / zh-SG 都映射成 zh。
func uapiTargetLang(lang string) string {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "zh-cn", "zh-hans", "zh-sg":
		return "zh"
	case "":
		return "zh"
	default:
		return strings.TrimSpace(lang)
	}
}

// withQuery 在地址上追加一个查询参数（地址已带参数时用 & 连接）。
func withQuery(endpoint, key, value string) string {
	separator := "?"
	if strings.Contains(endpoint, "?") {
		separator = "&"
	}
	return endpoint + separator + url.QueryEscape(key) + "=" + url.QueryEscape(value)
}
