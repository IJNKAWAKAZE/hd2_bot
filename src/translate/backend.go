package translate

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// 本文件是翻译后端的公共部分：接口约定、后端选择、HTTP 客户端的超时兜底与「哪些失败该冷却」。
// 两个具体后端在 free.go（社区免费接口）与 openai.go（OpenAI 兼容接口）。

// statusError 表示翻译后端返回了非 200 状态码。
// 只有 429/5xx 会被 isCooldownError 认定为「该冷却」，其它状态码按普通失败处理。
type statusError struct {
	code    int
	backend string
}

// Error 实现 error；不打印响应体（里面可能含用户内容或供应商提示，日志越短越好）。
func (e *statusError) Error() string {
	return fmt.Sprintf("%s 返回状态码 %d", e.backend, e.code)
}

// isCooldownError 判断错误是否应触发冷却：只有限流（429）与服务端错误（5xx）才说明
// 「配额用完或服务不可用」；网络超时/解析失败只是本次不巧，冷却反而会让后续几轮都拿不到译文。
func isCooldownError(err error) bool {
	var se *statusError
	if errors.As(err, &se) {
		return se.code == http.StatusTooManyRequests || se.code >= 500
	}
	return false
}

// selectBackendName 实现 auto 规则（spec §4.1）：地址与 key 都填了，
// 且地址路径以 /v1 或 /chat/completions 结尾时用 OpenAI 兼容后端，否则用免费接口。
func selectBackendName(cfg Config) string {
	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	if mode != "" && mode != "auto" {
		return mode
	}
	if strings.TrimSpace(cfg.APIURL) != "" && strings.TrimSpace(cfg.APIKey) != "" {
		path := strings.ToLower(strings.TrimRight(urlPath(cfg.APIURL), "/"))
		if strings.HasSuffix(path, "/v1") || strings.HasSuffix(path, "/chat/completions") {
			return "openai"
		}
	}
	return "free"
}

// newBackend 按配置创建后端；没有任何可用后端时返回 nil，由调用方回退透传并记一条中文警告。
// 这里只做「配了没有」的兜底告警，不校验地址本身是否可达（可达性由第一次请求暴露）。
// 注意：日志里绝不出现 api_key，也不打印完整地址（地址可能把 key 放在查询串里）。
func newBackend(cfg Config, logf func(string, ...any)) Backend {
	switch selectBackendName(cfg) {
	case "openai":
		if strings.TrimSpace(cfg.APIURL) == "" {
			logf("translate.mode 选择了 OpenAI 兼容接口，但 translate.api_url 为空：翻译整体停用，正文保持英文")
			return nil
		}
		return newOpenAIBackend(cfg)
	case "free":
		if strings.TrimSpace(cfg.FreeAPIURL) == "" {
			logf("translate.mode 选择了免费接口，但 translate.free_api_url 为空：翻译整体停用，正文保持英文")
			return nil
		}
		return newFreeBackend(cfg, cfg.FreeAPIURL)
	default:
		logf("translate.mode 取值未知（%q）：翻译整体停用，正文保持英文", cfg.Mode)
		return nil
	}
}

// urlPath 取地址的路径部分；解析失败时返回原地址（只用于判断形态，不用于外发）。
func urlPath(raw string) string {
	trimmed := strings.TrimSpace(raw)
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Path == "" {
		return trimmed
	}
	return parsed.Path
}

// newHTTPClient 按配置的超时建一个客户端；timeout <= 0 时用 defaultTimeout 兜底
// （该常量定义在 translate.go，与「Config.Timeout 的兜底值」是同一个，避免两处数字漂移）。
func newHTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	// 每个后端各自持有一份 Transport，不用共享的 http.DefaultTransport：
	// 共享时任何一处（甚至别的库）调连接池参数都会同时改变翻译请求的行为，排查起来没有线索。
	// 只显式写出与翻译场景相关的几项，其余保持默认值。
	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment, // 用户环境里配了代理时，翻译请求也要跟着走
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:        4,
		MaxIdleConnsPerHost: 2,
		IdleConnTimeout:     30 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	return &http.Client{Timeout: timeout, Transport: transport}
}

// wrapRequestError 把 net/http 的错误改成「不含敏感地址信息」的形态。
//
// net/http 的 *url.Error 默认会把完整 URL 写进文案，而地址里可能藏着密钥
// （查询串 ?api_key=... 或路径段 /v1/<key>/translate），这些内容绝不能进日志或卡片提示；
// 这里只保留操作名与「协议 + 主机」（见 redactURL）。
// 底层原因用 %w 保留，上层才能继续用 errors.Is 判定 context.DeadlineExceeded / context.Canceled。
func wrapRequestError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("请求翻译接口失败（%s %s）：%w", ue.Op, redactURL(ue.URL), ue.Err)
	}
	return fmt.Errorf("请求翻译接口失败：%w", err)
}

// redactURL 把地址裁成「协议://主机 + 固定说明」：路径与查询参数一律去掉。
// 只脱敏查询串是不够的——有的网关把 key 放在路径段里（/v1/<key>/translate），
// 路径进了日志同样等于泄钥。解析不出主机名时给占位文案，宁可不完整也不泄露。
func redactURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return "（地址已隐去）"
	}
	return parsed.Scheme + "://" + parsed.Host + "（路径与参数已隐去）"
}
