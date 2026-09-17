package translate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// systemPromptTemplate 是送 OpenAI 兼容后端的系统提示词模板（%s 处填目标语言名）：
// 要求保留编号标记、只输出译文。
//
// 目标语言必须进提示词：配置里的 target_lang 对 OpenAI 后端同样生效，写死「简体中文」
// 会让配了别的语言的用户拿不到想要的译文。目标语言本身已经参与缓存指纹，
// 所以换语言不会复用旧译文；但改这段模板的措辞仍必须把 protocolVersion +1。
const systemPromptTemplate = `你是《绝地潜兵 2》情报文本的翻译员。把用户给你的英文原文翻译成%s，要求：
1. 每一段的编号标记（形如 [[HD2_1]]、[[HD2_2]]）必须原样保留在对应译文的开头，不能增删、不能改号、不能漏段；
2. 只输出译文本身，不要解释、不要前言后语、不要输出 JSON 或代码块；
3. 数字、代号、单位保持原样；已经是目标语言的片段保持不动。`

// targetLanguageName 把配置里的语言标签换成提示词里好读的名称。
// 认不出来的标签原样使用（模型多半也认 BCP-47 标签），留空按简体中文处理。
func targetLanguageName(lang string) string {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "", "zh", "zh-cn", "zh-hans", "zh-sg":
		return "简体中文"
	case "zh-hant", "zh-tw", "zh-hk":
		return "繁体中文"
	default:
		return strings.TrimSpace(lang)
	}
}

// openAICompat 是 OpenAI 兼容后端：POST {base}/chat/completions。
//
// 边界：apiKey 只用于 Authorization 头，绝不进日志、缓存指纹或状态文件；
// 逐段请求（每段一次 HTTP），temperature 压低以减少模型自行加戏。
type openAICompat struct {
	url       string
	apiKey    string
	model     string
	maxTokens int
	prompt    string // 已经填好目标语言的系统提示词
	client    *http.Client
}

// newOpenAIBackend 创建 OpenAI 兼容后端。
func newOpenAIBackend(cfg Config) *openAICompat {
	return &openAICompat{
		url:       chatCompletionsURL(cfg.APIURL),
		apiKey:    strings.TrimSpace(cfg.APIKey),
		model:     strings.TrimSpace(cfg.Model),
		maxTokens: cfg.MaxTokens,
		prompt:    fmt.Sprintf(systemPromptTemplate, targetLanguageName(cfg.TargetLang)),
		client:    newHTTPClient(cfg.Timeout),
	}
}

// Name 返回后端名，用于日志与缓存指纹（不带地址与 key，避免把敏感信息写进状态文件）。
func (b *openAICompat) Name() string { return "OpenAI 兼容接口" }

// Translate 翻译「一批 = 一个编号文本」并返回等长的结果。
//
// 入参契约（调用方必须遵守）：texts 恰好 1 个元素。上层 translate.go 按整批编号后
// 只发一次请求（backend.Translate(ctx, []string{numbered})），限速名额也是按「一批一次请求」
// 预约的；一次传多段会让实际请求数与预约次数对不上（限速形同虚设），因此这里显式拒绝。
func (b *openAICompat) Translate(ctx context.Context, texts []string) ([]string, error) {
	if len(texts) != 1 {
		return nil, fmt.Errorf("OpenAI 兼容接口后端一次只接受 1 段文本，收到 %d 段", len(texts))
	}
	content, err := b.request(ctx, texts[0])
	if err != nil {
		return nil, err
	}
	return []string{content}, nil
}

// request 发一次 chat/completions 请求并取回助手回复。
func (b *openAICompat) request(ctx context.Context, text string) (string, error) {
	payload := map[string]any{
		"model": b.model,
		"messages": []map[string]string{
			{"role": "system", "content": b.prompt},
			{"role": "user", "content": text},
		},
		"temperature": 0.3,
		"stream":      false,
	}
	if b.maxTokens > 0 {
		// 不设上限时免费模型容易自行加戏（一次几百毫秒的请求变成几十秒）。
		payload["max_tokens"] = b.maxTokens
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("序列化翻译请求失败：%w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("构造翻译请求失败：%w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if b.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+b.apiKey)
	}

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
	var decoded struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return "", fmt.Errorf("解析翻译响应失败：%w", err)
	}
	if len(decoded.Choices) == 0 {
		return "", fmt.Errorf("翻译响应里没有 choices")
	}
	// finish_reason == "length" 表示译文被 max_tokens 截断：内容看着「有」，实际是半句话，
	// 采纳会把半截译文贴到卡片上（单段场景尤其危险），所以按失败处理，让整批回退英文。
	if reason := strings.TrimSpace(decoded.Choices[0].FinishReason); strings.EqualFold(reason, "length") {
		return "", fmt.Errorf("翻译响应被 max_tokens 截断（finish_reason=length），请调大 translate.max_tokens")
	}
	content := stripFences(decoded.Choices[0].Message.Content)
	if content == "" {
		return "", fmt.Errorf("翻译响应里的正文为空")
	}
	return content, nil
}

// chatCompletionsURL 拼出 chat/completions 的完整地址：
// 路径已以 /chat/completions 结尾时原样使用，否则在路径末尾追加；
// 查询串原样保留（Azure 之类的网关靠 ?api-version=... 认版本，拼在路径里会直接把 URL 拼坏）。
func chatCompletionsURL(base string) string {
	trimmed := strings.TrimSpace(base)
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" {
		// 解析不出主机名（地址写成了相对路径或含非法字符）：退回纯字符串拼接，
		// 至少保留「已以 /chat/completions 结尾时原样使用」的行为，也不 panic。
		trimmed = strings.TrimRight(trimmed, "/")
		if strings.HasSuffix(strings.ToLower(trimmed), "/chat/completions") {
			return trimmed
		}
		return trimmed + "/chat/completions"
	}
	path := strings.TrimRight(parsed.Path, "/")
	if !strings.HasSuffix(strings.ToLower(path), "/chat/completions") {
		path += "/chat/completions"
	}
	parsed.Path = path
	parsed.RawPath = "" // 路径改了，旧的转义形式不再对应，清掉让 url 包重新转义
	return parsed.String()
}

// stripFences 剥掉模型偶尔套上的代码围栏与首尾引号（有些模型会把整段译文包起来）。
func stripFences(content string) string {
	text := strings.TrimSpace(content)
	if strings.HasPrefix(text, "```") {
		text = strings.TrimPrefix(text, "```")
		if idx := strings.IndexByte(text, '\n'); idx >= 0 {
			text = text[idx+1:] // 去掉语言标记那一行
		}
		text = strings.TrimSuffix(strings.TrimSpace(text), "```")
	}
	text = strings.TrimSpace(text)
	if len(text) >= 2 && strings.HasPrefix(text, "\"") && strings.HasSuffix(text, "\"") {
		text = strings.TrimSpace(text[1 : len(text)-1])
	}
	return text
}
