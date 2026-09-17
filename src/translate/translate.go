// Package translate 是正文翻译层：把上游的英文正文翻成中文，失败一律回退原文。
//
// 设计要点（详见 docs/superpowers/specs/2026-09-16-hd2-bot-s3-design.md §4）：
//   - Translator 是唯一对外接口；enabled=false 或没有任何可用后端时拿到的是「原样返回」的实现，
//     调用方不需要判断「有没有开翻译」；但「原样返回」只是内部兜底，不代表用户想要英文——
//     装配阶段要用 IsPassthrough 把这两种兜底情况归一成 nil（理由见该函数的注释）；
//   - 翻译失败不算错误：正文永远有内容可显示，err 只用于日志与卡片上的提示；
//   - 结果写进 bbolt 的通用键值桶（kind=translate），同一段文本只翻一次。
//
// 本包不依赖 push / plugins / render：它只认 config、hd2/glossary 与一个两方法的缓存接口。
package translate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// batchSize 是一次请求最多送几段文本；超过则分批串行。
	batchSize = 16
	// cacheKind 是翻译缓存在 state.Store 通用键值桶里的域。
	cacheKind = "translate"
	// protocolVersion 是「编号协议 + 提示词」的版本号：改了协议或提示词必须 +1。
	// 它参与缓存指纹，改版后旧译文不会被复用（否则用户看到的永远是新协议下的旧译文）。
	protocolVersion = "v1"
	// defaultTimeout 是 Config.Timeout <= 0 时的兜底超时，同时是两个后端 HTTP 客户端的
	// 兜底超时（backend.go newHTTPClient 用它）：一个包里只留这一个默认值，避免两处数字漂移。
	// Timeout 的单位是 time.Duration，调用方漏了「秒 → Duration」换算（例如直接写 20）会得到 20ns，
	// 所有请求都会瞬间超时；宁可退回默认值并记一条中文告警。
	defaultTimeout = 20 * time.Second
)

// Translator 把英文文本翻成中文。
// 返回的切片与输入等长：某段翻译失败时该段原样返回，保证调用方永远有内容可显示；
// err 只用于日志与「是否要在卡片上注明翻译不可用」，不代表结果不可用。
//
// 并发契约：同一个 Translator 实例必须能被推送轮询协程与命令处理协程同时调用。
// 实现内部用一把互斥锁保护「冷却截止时间」与「限速名额」，并让后端请求串行预约：
// 并发调用既不能绕过 min_interval（绕过会换来 429 与整段冷却期），也不能把译文串到别的条目上。
// 缓存与后端因此只按串行语义被访问，实现方不必自带锁。
type Translator interface {
	Translate(ctx context.Context, texts []string) ([]string, error)
}

// Backend 是翻译后端：Name 用于日志与缓存指纹，Translate 把一段文本翻成目标语言。
//
// 返回值必须与入参等长（当前两个后端都是「一段一次请求」，上层因此每批只传一段）；
// 失败时返回错误，上层据此回退原文并（对 429/5xx）进入冷却。
type Backend interface {
	Name() string
	Translate(ctx context.Context, texts []string) ([]string, error)
}

// CacheStore 是翻译缓存需要的最小存储能力；*state.Store 实现了它。
// 定义为接口是为了让单测不用真的开一个 bbolt 文件。
type CacheStore interface {
	PutKV(kind, key, value string) error
	GetKV(kind, key string) (string, bool, error)
}

// Config 是翻译层的运行参数，由 config.TranslateConfig 换算而来。
type Config struct {
	Enabled       bool          // 关闭时返回透传实现
	Mode          string        // auto / openai / free
	APIURL        string        // OpenAI 兼容地址
	APIKey        string        // 只从配置文件读取，绝不写进日志
	Model         string        // OpenAI 兼容后端使用的模型名
	TargetLang    string        // 目标语言，例如 zh-CN
	Timeout       time.Duration // 单次请求超时；单位是 Duration，<= 0 时 New 兜底成 defaultTimeout
	MaxTokens     int           // 译文输出上限；0 表示不发送该字段
	MinInterval   time.Duration // 相邻请求最小间隔；0 表示不限速
	ErrorCooldown time.Duration // 429/5xx 后的冷却时长
	FreeAPIURL    string        // 免费翻译接口地址
}

// Deps 是翻译层的依赖；除 Backend 外的字段都可以留空（用系统时钟、静默日志、不缓存）。
type Deps struct {
	Backend Backend    // 为 nil 时按 Config 自动选择
	Store   CacheStore // 为 nil 时不缓存
	Now     func() time.Time
	Logger  func(format string, args ...any)
}

// Passthrough 返回一个原样返回输入的 Translator（enabled=false 或没有可用后端时 New 也会返回它）。
// 存在的意义：调用方与测试永远拿到一个可用的 Translator，不必到处写 nil 判断。
func Passthrough() Translator { return passthrough{} }

// passthrough 是原样返回输入的实现。
type passthrough struct{}

// Translate 返回输入副本。
func (passthrough) Translate(_ context.Context, texts []string) ([]string, error) {
	out := make([]string, len(texts))
	copy(out, texts)
	return out, nil
}

// IsPassthrough 判断一个 Translator 是不是「原样返回」的透传实现。
//
// 为什么装配侧需要它：透传实现只在两种情况下出现——用户主动关掉翻译（enabled=false），
// 或开着翻译却选不出可用后端（mode=openai 但 api_url 没填、free_api_url 写错……）。
// 两种都是「内部兜底、正文保持英文」，不是翻译出了故障；而插件与推送把「译文与原文逐字相同」
// 判成「这一段没翻动」，把透传实现直接交给它们，每张卡片都会挂一句假的「翻译暂不可用」，
// 与日志里那句「翻译整体停用」自相矛盾。装配阶段因此要用它把这两种情况一起归一成 nil
// （nil 才是「不翻译、也不注明」的哨兵值，见 main.pluginTranslator）。
//
// 判据是类型断言：Passthrough() 与 New 的「没有可用后端」分支返回的是同一个值类型 passthrough。
// nil 返回 false——nil 不是透传，它是更强的语义「根本没有翻译层」。
func IsPassthrough(t Translator) bool {
	_, ok := t.(passthrough)
	return ok
}

// New 创建翻译层。error 目前恒为 nil：配置本身的矛盾（mode 非法、free_api_url 没填……）
// 由 config 包校验，这里只做兜底与中文告警；签名保留 error 是为了装配代码不必随实现变化。
// 「没有可用后端」只记一条中文警告并回退透传，不阻止机器人启动（正文保持英文）。
//
// 单位契约：Config 里的 Timeout / MinInterval / ErrorCooldown 都是 time.Duration，
// 而配置文件写的是「秒」，换算由调用方（main.translateConfig）负责；Timeout <= 0 时这里
// 兜底成 defaultTimeout 并告警，避免裸写 Timeout: 20（= 20ns）让所有请求瞬间超时。
func New(cfg Config, deps Deps) (Translator, error) {
	logf := deps.Logger
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if !cfg.Enabled {
		logf("翻译层已关闭（translate.enabled=false），所有正文保持英文")
		return passthrough{}, nil
	}
	if cfg.Timeout <= 0 {
		logf("translate.timeout 未设置或过小（%v），按默认 %s 处理（单位是 time.Duration，注意从秒换算）", cfg.Timeout, defaultTimeout)
		cfg.Timeout = defaultTimeout
	}
	backend := deps.Backend
	if backend == nil {
		backend = newBackend(cfg, logf)
	}
	if backend == nil {
		return passthrough{}, nil
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	logf("翻译层已启用：后端=%s 目标语言=%s 缓存=%v", backend.Name(), cfg.TargetLang, deps.Store != nil)
	return &translator{cfg: cfg, backend: backend, cache: deps.Store, now: now, logf: logf}, nil
}

// translator 是 Translator 的默认实现：批量切分 + 缓存 + 冷却与限速都收在这里。
type translator struct {
	cfg     Config
	backend Backend
	cache   CacheStore
	now     func() time.Time
	logf    func(string, ...any)

	mu            sync.Mutex
	cooldownUntil time.Time // 429/5xx 后的冷却截止时间
	nextRequestAt time.Time // min_interval 计算出的「下一批请求最早发出时间」
}

// Translate 批量翻译；任一环节失败都只影响对应段落的回退，不中断整批。
func (t *translator) Translate(ctx context.Context, texts []string) ([]string, error) {
	out := make([]string, len(texts))
	copy(out, texts)
	if len(texts) == 0 {
		return out, nil
	}

	// 1) 清洗：游戏内标记先清掉（送译的原文与缓存键都用清洗后的文本，标记不该影响译文）。
	prepared := make([]string, len(texts))
	for i, text := range texts {
		prepared[i] = CleanGameText(text)
	}

	// 2) 查缓存：命中的直接填回，只有未命中的才需要请求。
	pending := make([]int, 0, len(texts))
	for i, text := range prepared {
		if !needsTranslation(text) {
			continue
		}
		if cached, ok := t.loadCache(text); ok {
			out[i] = cached
			continue
		}
		pending = append(pending, i)
	}
	if len(pending) == 0 {
		return out, nil
	}

	// 3) 分批请求：每批最多 batchSize 段，某批失败时这一批全部回退原文。
	failures := 0
	var firstErr error // 第一个失败原因：聚合错误的文案要带上它，否则排查时分不清是限流还是被取消
	for start := 0; start < len(pending); start += batchSize {
		end := start + batchSize
		if end > len(pending) {
			end = len(pending)
		}
		batch := make([]string, 0, end-start)
		for _, idx := range pending[start:end] {
			batch = append(batch, prepared[idx])
		}
		translated, err := t.translateBatch(ctx, batch)
		if err != nil {
			failures++
			if firstErr == nil {
				firstErr = err
			}
			t.logf("翻译失败，本批 %d 段保留英文：%v", len(batch), err)
			continue
		}
		for j, idx := range pending[start:end] {
			text := prepared[idx]
			// 采纳前先清一遍脏值：模型偶尔把译文套进代码围栏、或只回一个围栏。
			// 这类值直接采纳会写进缓存与卡片（用户看到一行 ```），所以按「没翻动」处理：
			// 保留原文，也不写缓存（下次还会再试）。
			cleaned := cleanTranslated(translated[j])
			if cleaned == "" || cleaned == text {
				continue
			}
			out[idx] = cleaned
			t.saveCache(text, cleaned)
		}
	}
	if failures > 0 {
		// 保留首个失败原因（%w）：ctx 取消/超时与「后端限流」的处置完全不同，
		// 只报一句「N 批请求失败」会让排查方向跑偏。
		if errors.Is(firstErr, context.Canceled) || errors.Is(firstErr, context.DeadlineExceeded) {
			return out, fmt.Errorf("翻译被取消或超时：%d 批请求失败：%w", failures, firstErr)
		}
		return out, fmt.Errorf("翻译暂不可用：%d 批请求失败：%w", failures, firstErr)
	}
	return out, nil
}

// translateBatch 送一批文本（已清洗、已确认需要翻译），返回与入参等长的译文。
//
// 送译前做一次术语预替换（PreReplace）：官方译名先落地，模型就不会把星球/分区/敌人名
// 音译或漏译。注意缓存键仍用清洗后的原文（预替换之前的文本），
// 这样更新词表只会让新文本用上新词表，不会让整库旧缓存失效。
func (t *translator) translateBatch(ctx context.Context, batch []string) ([]string, error) {
	if err := t.waitTurn(ctx); err != nil {
		return nil, err
	}
	prepared := make([]string, len(batch))
	for i, text := range batch {
		prepared[i] = PreReplace(text)
	}
	numbered := numberTexts(prepared)
	resp, err := t.backend.Translate(ctx, []string{numbered})
	if err != nil {
		if isCooldownError(err) {
			t.enterCooldown()
		}
		return nil, err
	}
	if len(resp) != 1 {
		return nil, fmt.Errorf("后端返回了 %d 段，期望 1 段", len(resp))
	}
	parts := splitNumbered(resp[0], len(batch))
	if parts == nil {
		return nil, fmt.Errorf("后端返回的编号不齐（期望 %d 段）", len(batch))
	}
	return parts, nil
}

// waitTurn 处理冷却与限速；返回非 nil 时本批直接回退原文，不打接口。
//
// 限速的「名额」在一个临界区里原子预约：谁把 nextRequestAt 推到更晚，谁就排到了那个时刻，
// 后面来的调用只能再往后排（因此相邻请求至少间隔 min_interval）。
// 旧实现是「读→解锁→等→再写」，预约不是原子的：并发调用会同时看到同一个空档，
// 4 个协程同时送译就会同时发出 3 个请求，换来 429 与整段冷却期。
//
// 等待自己的名额期间可以被 ctx 取消（推送轮询不该被限速拖住），返回的错误可用 errors.Is 判定。
func (t *translator) waitTurn(ctx context.Context) error {
	t.mu.Lock()
	now := t.now()
	if remaining := t.cooldownUntil.Sub(now); remaining > 0 {
		t.mu.Unlock()
		return fmt.Errorf("翻译后端冷却中，还剩 %s", remaining.Round(time.Second))
	}
	slot := t.nextRequestAt
	if slot.Before(now) {
		slot = now
	}
	// 名额就在这个临界区里被占用：MinInterval <= 0 时 nextRequestAt 停在 now，等于不限速。
	t.nextRequestAt = slot.Add(t.cfg.MinInterval)
	t.mu.Unlock()

	if wait := slot.Sub(now); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			// 用 %w 包住原因：调用方要能区分「被取消」与「后端不可用」。
			return fmt.Errorf("等待翻译限速名额时被取消：%w", ctx.Err())
		case <-timer.C:
		}
	}
	return nil
}

// enterCooldown 记录一次冷却：冷却期内的请求直接回退原文，不再打接口。
func (t *translator) enterCooldown() {
	if t.cfg.ErrorCooldown <= 0 {
		return
	}
	until := t.now().Add(t.cfg.ErrorCooldown)
	t.mu.Lock()
	if until.After(t.cooldownUntil) {
		t.cooldownUntil = until
	}
	t.mu.Unlock()
	t.logf("翻译后端限流或异常，进入 %s 冷却", t.cfg.ErrorCooldown)
}

// loadCache 查缓存；没有缓存能力或读取失败时按「未命中」处理（缓存只是加速手段）。
func (t *translator) loadCache(text string) (string, bool) {
	if t.cache == nil {
		return "", false
	}
	value, ok, err := t.cache.GetKV(cacheKind, t.cacheKey(text))
	if err != nil {
		t.logf("读取翻译缓存失败，按未命中处理：%v", err)
		return "", false
	}
	return value, ok && value != ""
}

// saveCache 写缓存；写失败只记日志，不影响本次结果。
func (t *translator) saveCache(text, translated string) {
	if t.cache == nil {
		return
	}
	if err := t.cache.PutKV(cacheKind, t.cacheKey(text), translated); err != nil {
		t.logf("写入翻译缓存失败：%v", err)
	}
}

// cacheKey 拼缓存键：sha256(后端指纹 + 清洗后的原文)。
// 用哈希而不是原文：bbolt 的键要短且稳定，而原文可能有换行与几 KB 长度。
func (t *translator) cacheKey(text string) string {
	sum := sha256.Sum256([]byte(t.fingerprint() + "\n" + text))
	return hex.EncodeToString(sum[:])
}

// fingerprint 是后端指纹：换了协议版本、后端、模型、地址或目标语言后，旧译文都不会被复用。
// 目标语言必须在里面：否则把 target_lang 从 zh-CN 改成别的语言后，用户继续看到旧语言的缓存译文。
// 只包含非敏感信息——地址里只取主机名，API key 绝不进去。
func (t *translator) fingerprint() string {
	return strings.Join([]string{
		protocolVersion,
		t.backend.Name(),
		strings.TrimSpace(t.cfg.Model),
		hostOf(t.cfg.APIURL),
		strings.TrimSpace(t.cfg.TargetLang),
	}, "|")
}

// hostOf 取出地址的主机名；解析失败时返回原地址（只会进哈希，不会外发）。
func hostOf(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return strings.TrimSpace(raw)
	}
	return parsed.Host
}

// minLatinRun 是「值得送译」的最短连续拉丁字母个数。
// 为什么不取 1 个字母：术语预替换后的中文译名常带罗马数字与缩写尾巴（例如 天园六IV、UVP阿尔法），
// 这类片段没有任何可翻的英文，送一次只会白费一次配额；而真正的英文句子必然含 3 个以上连续字母的词。
const minLatinRun = 3

// needsTranslation 判断一段文本是否需要送译：至少要有一段连续 minLatinRun 个 ASCII 字母。
// 全是中文、数字、符号（或只带 IV、UVP 这类短尾巴）的文本再送一次只会白费一次请求。
func needsTranslation(text string) bool {
	run := 0
	for _, r := range text {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			run++
			if run >= minLatinRun {
				return true
			}
			continue
		}
		run = 0
	}
	return false
}

// NeedsTranslation 判断一段原文会不会被翻译层真的送出去（也就是「值不值得送译」）。
//
// 判据与翻译层内部完全一致：至少有一段连续 minLatinRun 个 ASCII 字母。
// 判中日的字符**不是**这里的条件，这一点很关键：
//   - 层里是否真的发请求，就是按这个判据决定的（见 Translate）；
//   - 调用方要回答的问题是「后端把原文原样回显，算不算翻译失败」。只有「层真的送了、却拿回原文」
//     才是失败；层根本没送（纯中文、纯数字符号、空串）时回显是正常结果。
//   - 中英混排（"MAJOR ORDER WON 士兵们，干得漂亮。"）会被真的送出去并翻成中文，
//     所以判据里不能加「含中日韩字符就返回 false」——那会把翻好的译文丢掉，
//     让混排简报永远停在英文。
//
// 调用方拿到 false 的语义是「这一段保持原样就是正确结果」，不要记成翻译故障——
// 这正是 Moved 的一半语义，也是「明明是中文却挂一句『翻译暂不可用』」那个假告警的根因。
func NeedsTranslation(text string) bool {
	return needsTranslation(text)
}

// Moved 判断一段译文算不算「真的翻动了」。
//
// 为什么不写成简单的 src != dst：
//   - 原文本来就该保持原样时（空白、纯中文、只有数字与符号），翻译层根本不会送它，
//     后端（或透传实现）把原文原样回显是正常结果，按 src != dst 判定会让每一条中文简报
//     都挂上一句「翻译暂不可用，以下为英文原文」，而正文里一个英文都没有——线上会看到的假告警；
//   - 后端偶尔只回空白，或把原文照抄一遍（配额用完、模型抽风），这两种才是真的没翻动，
//     调用方要据此保留原文并说明。
//
// 因此判据是「原文需要翻译（NeedsTranslation）且译文非空白且与原文不同」；
// 原文不需要翻译时恒为 false：调用方据此原样保留原文，并且不把它算成故障。
func Moved(src, dst string) bool {
	if !NeedsTranslation(src) {
		return false
	}
	trimmed := strings.TrimSpace(dst)
	return trimmed != "" && trimmed != strings.TrimSpace(src)
}

// markerRe 匹配编号标记。
var markerRe = regexp.MustCompile(`\[\[HD2_(\d+)\]\]`)

// numberTexts 把一批文本拼成带编号标记的整段文本（送译的输入）。
// 编号协议的作用是：后端把多段文本当成一整段处理时，我们还能按编号把译文切回各自的条目。
func numberTexts(batch []string) string {
	var b strings.Builder
	for i, text := range batch {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "[[HD2_%d]] %s", i+1, text)
	}
	return b.String()
}

// splitNumbered 按编号标记切出 N 段译文；编号缺失、重复、越界或段数为 0 时返回 nil（整批回退原文）。
// 单段（want == 1）时允许后端把标记丢掉：只要返回了非空文本就当作这一段的结果——
// 免费接口偶尔会把不认识的标记吃掉，为一段判定整批失败代价太大。
func splitNumbered(text string, want int) []string {
	if want <= 0 || strings.TrimSpace(text) == "" {
		return nil
	}
	matches := markerRe.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		if want == 1 {
			return []string{strings.TrimSpace(text)}
		}
		return nil
	}
	if len(matches) != want {
		return nil
	}
	parts := make([]string, want)
	for i, m := range matches {
		// 编号必须是 i+1：顺序错位说明后端把段落顺序搞乱了，宁可整批回退，也不把译文贴到别的条目上。
		if number := text[m[2]:m[3]]; number != strconv.Itoa(i+1) {
			return nil
		}
		end := len(text)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}
		part := strings.TrimSpace(text[m[1]:end])
		if part == "" {
			return nil
		}
		parts[i] = part
	}
	return parts
}
