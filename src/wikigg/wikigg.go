// Package wikigg 是 helldivers.wiki.gg 的 MediaWiki 薄客户端：只做本项目真正需要的三件事——
// 把 File: 页解析成一张能直接交给 Telegram 的图地址、取 Enemies 表（Cargo 查询）、取条目的页面图标。
// 站点上还有搜索、正文、分类等能力，这里一律不做：用不到的能力只会变成没人维护的死代码。
//
// 数据来源与许可：wiki.gg 上的《绝地潜兵 2》内容按 CC BY-NC-SA 授权（站点页脚声明）。
// 本项目只把它当作数值与图标的来源，卡片与行内结果里注明出处；完整口径见 src/render/assets/LICENSES.md。
//
// 两点设计取舍：
//  1. 这个包只负责 HTTP 与 JSON 解析，「清洗富文本、过滤 HD1 条目、落盘缓存」交给 src/bestiary，
//     这样清洗规则能纯函数化测试，不必连 HTTP 一起测；
//  2. 所有请求都带 User-Agent（实测不带 UA 会被站点直接 403），并限制响应体积。
package wikigg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// defaultBaseURL 是 wiki.gg 上《绝地潜兵 2》的站点根地址。
	defaultBaseURL = "https://helldivers.wiki.gg"
	// defaultTimeout 是单次请求超时：一次查询只回几十到几百 KB，20 秒对慢网也够。
	defaultTimeout = 20 * time.Second
	// defaultUserAgent 是默认请求头。站点对空 UA 返回 403，所以这里必须给一个能识别来源的串。
	defaultUserAgent = "hd2_bot/0.1 (MediaWiki client)"
	// maxResponseBytes 是单次响应的体积上限：防的是上游异常响应把进程内存拖垮
	// （实测 Enemies 全表约 60KB、单次 imageinfo 响应只有几 KB）。
	maxResponseBytes = 8 << 20
	// enemyQueryLimit 是 Enemies 表一次取多少行。实测全表 123 行，500 是一次取全的余量。
	enemyQueryLimit = 500
	// titleBatchSize 是 pageimages 一次最多查多少个标题（MediaWiki 的 titles 上限是 50）。
	titleBatchSize = 50
	// redirectHopLimit 是标题重定向链的跟随上限：站点上的重定向没有环，这里只是防死循环。
	redirectHopLimit = 5
	// defaultIconWidth 是取页面图标时的默认缩放宽度（px）。行内结果里的图标很小，480 足够清楚。
	defaultIconWidth = 480
	// defaultImageLimit 是单张图片的体积上限（4 MiB）：站点上的图标都在几百 KB 以内。
	defaultImageLimit = 4 << 20
)

// Config 是客户端配置；零值可用（全部走默认值）。
type Config struct {
	BaseURL   string        // 站点根地址，空串用 defaultBaseURL
	Timeout   time.Duration // 单次请求超时；<=0 用 defaultTimeout
	UserAgent string        // 请求头里的 User-Agent；空串用 defaultUserAgent
}

// Deps 是外部依赖。客户端自己不记日志：所有失败都以 error 返回，
// 由调用方（bestiary / arsenal）按自己的上下文决定记什么。
type Deps struct {
	HTTP *http.Client // 自定义客户端；nil 时按 Config.Timeout 新建
}

// Client 是 MediaWiki 客户端。方法并发安全：内部只有只读配置与一个 http.Client，没有可变状态。
type Client struct {
	base string
	ua   string
	http *http.Client
}

// New 构造客户端；与其它包一致，构造本身不做任何网络请求。
func New(cfg Config, deps Deps) *Client {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		base = defaultBaseURL
	}
	ua := strings.TrimSpace(cfg.UserAgent)
	if ua == "" {
		ua = defaultUserAgent
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	client := deps.HTTP
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	return &Client{base: base, ua: ua, http: client}
}

// BaseURL 返回实际使用的站点根地址（供启动日志与测试断言用）。
func (c *Client) BaseURL() string { return c.base }

// EnemyRow 是 Enemies 表的一行原文：上游给什么就是什么，不做任何清洗与转换。
// health 里带千分位与多余说明（例如 "2,000 HP + 400 Constitution"），size 是空串或 "0".."3"，
// damage 是 wiki 富文本（含 HTML 标签与 [[链接]]）：这些都由 bestiary 负责解释。
type EnemyRow struct {
	Title   string
	Faction string
	Size    string
	Health  string
	Class   string
	Damage  string
	// Image 是站点上这张条目的图片文件名（例如 "Bile Titan Enemy Icon.png"），可能为空。
	// 它比 pageimages 靠谱得多：pageimages 对敌人页多半回的是阵营图标（而且是 SVG，被挡掉），
	// 这一列才是逐只的 PNG 图标（实测 2026-09-17 全表 123 行都有）。
	Image string
}

// Enemies 取 Enemies 表的全部行（含《绝地潜兵 1》的条目，过滤交给 bestiary）。
func (c *Client) Enemies(ctx context.Context) ([]EnemyRow, error) {
	params := url.Values{
		"action": {"cargoquery"},
		"tables": {"Enemies"},
		"fields": {"title,faction,size,health,class,damage,image"},
		"limit":  {strconv.Itoa(enemyQueryLimit)},
		"format": {"json"},
	}
	var env cargoEnvelope
	if err := c.getJSON(ctx, params, &env); err != nil {
		return nil, err
	}
	if env.Error != nil {
		return nil, fmt.Errorf("wiki 拒绝了 Enemies 查询：%s", env.Error.describe())
	}
	rows := make([]EnemyRow, 0, len(env.CargoQuery))
	for _, entry := range env.CargoQuery {
		fields := entry.Fields
		rows = append(rows, EnemyRow{
			Title:   asString(fields["title"]),
			Faction: asString(fields["faction"]),
			Size:    asString(fields["size"]),
			Health:  asString(fields["health"]),
			Class:   asString(fields["class"]),
			Damage:  asString(fields["damage"]),
			Image:   asString(fields["image"]),
		})
	}
	return rows, nil
}

// EnemyIcons 取一批条目的页面图标地址（键是入参里的标题）。
//
// 拿到的是公网地址，可以直接交给 Telegram 当缩略图。三种情况会缺键：站点上没有这个条目、
// 条目没有配图、配图是 SVG（站点没有开 SVG 渲染，返回的地址点开还是 .svg，Telegram 不接受）——
// 缺键就表示这条结果不带图标。
func (c *Client) EnemyIcons(ctx context.Context, titles []string, width int) (map[string]string, error) {
	unique := make([]string, 0, len(titles))
	seen := make(map[string]bool, len(titles))
	for _, title := range titles {
		title = strings.TrimSpace(title)
		if title == "" || seen[title] {
			continue
		}
		seen[title] = true
		unique = append(unique, title)
	}
	if width <= 0 {
		width = defaultIconWidth
	}
	icons := make(map[string]string, len(unique))
	for start := 0; start < len(unique); start += titleBatchSize {
		end := start + titleBatchSize
		if end > len(unique) {
			end = len(unique)
		}
		found, err := c.iconsForBatch(ctx, unique[start:end], width)
		if err != nil {
			return nil, err
		}
		for title, icon := range found {
			icons[title] = icon
		}
	}
	return icons, nil
}

// iconsForBatch 查一批标题的图标：MediaWiki 一次最多接受 50 个标题，分批由调用方负责。
func (c *Client) iconsForBatch(ctx context.Context, titles []string, width int) (map[string]string, error) {
	params := url.Values{
		"action":      {"query"},
		"prop":        {"pageimages"},
		"piprop":      {"thumbnail|original"},
		"pithumbsize": {strconv.Itoa(width)},
		"titles":      {strings.Join(titles, "|")},
		"redirects":   {"1"},
		"format":      {"json"},
	}
	var env queryEnvelope
	if err := c.getJSON(ctx, params, &env); err != nil {
		return nil, err
	}
	if env.Error != nil {
		return nil, fmt.Errorf("wiki 拒绝了图标查询：%s", env.Error.describe())
	}

	// 入参标题与页面标题基本一致，但站点会把下划线改成空格（normalized）、把别名页跳转成正式页
	// （redirects），所以先建一张「入参标题 → 页面标题」的对照表，再用它把结果映射回去。
	byPage := make(map[string]string, len(env.Query.Pages))
	for _, page := range env.Query.Pages {
		if icon := page.iconURL(); icon != "" {
			byPage[page.Title] = icon
		}
	}
	aliases := make(map[string]string, len(env.Query.Normalized)+len(env.Query.Redirects))
	for _, item := range env.Query.Normalized {
		aliases[item.From] = item.To
	}
	for _, item := range env.Query.Redirects {
		aliases[item.From] = item.To
	}

	icons := make(map[string]string, len(titles))
	for _, title := range titles {
		if icon, ok := byPage[resolveTitle(title, aliases)]; ok {
			icons[title] = icon
		}
	}
	return icons, nil
}

// ImageBytes 下载一张图片（敌人图标这类）的原始字节。
//
// limit 是体积上限（字节），<=0 时用 defaultImageLimit：站点上的图标都在几百 KB 以内，
// 设上限防的是异常响应把内存拖垮。非 200 一律报错，调用方按「这张图没有」处理。
func (c *Client) ImageBytes(ctx context.Context, url string, limit int64) ([]byte, error) {
	target := strings.TrimSpace(url)
	if target == "" {
		return nil, errors.New("图片地址为空")
	}
	if limit <= 0 {
		limit = defaultImageLimit
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("构造图片请求失败：%w", err)
	}
	req.Header.Set("User-Agent", c.ua)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("下载图片失败：%w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("下载图片失败：HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, fmt.Errorf("读取图片失败：%w", err)
	}
	if len(raw) == 0 {
		return nil, errors.New("图片是空的")
	}
	return raw, nil
}

// FileThumbURLs 批量把文件名解析成缩略图地址，返回值按入参原样作键（便于调用方直接回填）。
//
// 入参可以是裸文件名（"Bile Titan Enemy Icon.png"）、File: 标题或完整地址，写法由 FileTitle 归一化；
// 认不出的写法直接跳过（这条结果不带图标，而不是让整批失败）。一次请求最多 50 个标题，
// 分批由本方法自己处理。站点没有开 SVG 渲染，SVG 文件页拿到的地址还是 .svg，这类一律挡掉（usableImageURL）。
func (c *Client) FileThumbURLs(ctx context.Context, files []string, width int) (map[string]string, error) {
	byTitle := make(map[string][]string, len(files)) // 站点标题 → 入参原值（多个入参可能指向同一个文件）
	order := make([]string, 0, len(files))
	seen := make(map[string]bool, len(files))
	for _, raw := range files {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		title, err := FileTitle(raw)
		if err != nil {
			continue
		}
		byTitle[title] = append(byTitle[title], raw)
		if seen[title] {
			continue
		}
		seen[title] = true
		order = append(order, title)
	}
	if width <= 0 {
		width = defaultIconWidth
	}
	urls := make(map[string]string, len(files))
	for start := 0; start < len(order); start += titleBatchSize {
		end := start + titleBatchSize
		if end > len(order) {
			end = len(order)
		}
		found, err := c.fileThumbsForBatch(ctx, order[start:end], width)
		if err != nil {
			return nil, err
		}
		for title, url := range found {
			for _, raw := range byTitle[title] {
				urls[raw] = url
			}
		}
	}
	return urls, nil
}

// fileThumbsForBatch 查一批 File: 标题的缩略图地址；MediaWiki 一次最多接受 50 个标题，分批由调用方负责。
func (c *Client) fileThumbsForBatch(ctx context.Context, titles []string, width int) (map[string]string, error) {
	params := url.Values{
		"action":     {"query"},
		"prop":       {"imageinfo"},
		"iiprop":     {"url"},
		"iiurlwidth": {strconv.Itoa(width)},
		"titles":     {strings.Join(titles, "|")},
		"redirects":  {"1"},
		"format":     {"json"},
	}
	var env queryEnvelope
	if err := c.getJSON(ctx, params, &env); err != nil {
		return nil, err
	}
	if env.Error != nil {
		return nil, fmt.Errorf("wiki 拒绝了文件缩略图查询：%s", env.Error.describe())
	}

	// 与 pageimages 一样：站点会把下划线改成空格、把别名页跳转成正式页，先把结果映射回入参标题。
	byPage := make(map[string]string, len(env.Query.Pages))
	for _, page := range env.Query.Pages {
		if page.Missing != nil {
			continue
		}
		if thumb := page.imageThumbURL(); thumb != "" {
			byPage[page.Title] = thumb
		}
	}
	aliases := make(map[string]string, len(env.Query.Normalized)+len(env.Query.Redirects))
	for _, item := range env.Query.Normalized {
		aliases[item.From] = item.To
	}
	for _, item := range env.Query.Redirects {
		aliases[item.From] = item.To
	}

	urls := make(map[string]string, len(titles))
	for _, title := range titles {
		if url, ok := byPage[resolveTitle(title, aliases)]; ok {
			urls[title] = url
		}
	}
	return urls, nil
}

// FileThumbURL 把一个 File: 页解析成缩略图地址。
//
// filePage 可以是完整地址（catalog.json 里的 image.filePage 就是 URL 形式，
// 例如 https://helldivers.wiki.gg/wiki/File:AR-2_Coyote_Primary_Render.png），
// 也可以是「File:xxx.png」这样的标题或裸文件名。
// width 是期望的缩略图宽度（px），<=0 时不带该参数，由 MediaWiki 决定。
//
// 注意：站点没有开 SVG 渲染，SVG 文件页拿到的地址还是 .svg 本身（实测），
// 因此调用方仍要自己判断地址能不能用。
func (c *Client) FileThumbURL(ctx context.Context, filePage string, width int) (string, error) {
	title, err := FileTitle(filePage)
	if err != nil {
		return "", err
	}
	params := url.Values{
		"action": {"query"},
		"prop":   {"imageinfo"},
		"iiprop": {"url"},
		"titles": {title},
		"format": {"json"},
	}
	if width > 0 {
		params.Set("iiurlwidth", strconv.Itoa(width))
	}
	var env queryEnvelope
	if err := c.getJSON(ctx, params, &env); err != nil {
		return "", err
	}
	if env.Error != nil {
		return "", fmt.Errorf("wiki 拒绝了文件信息查询：%s", env.Error.describe())
	}
	for _, page := range env.Query.Pages {
		if page.Missing != nil {
			return "", fmt.Errorf("wiki 上没有这个文件页：%s", title)
		}
		if len(page.ImageInfo) == 0 {
			continue
		}
		info := page.ImageInfo[0]
		if thumb := strings.TrimSpace(info.ThumbURL); thumb != "" {
			return thumb, nil
		}
		if raw := strings.TrimSpace(info.URL); raw != "" {
			return raw, nil
		}
	}
	return "", fmt.Errorf("wiki 没有返回 %s 的图片地址", title)
}

// FileTitle 把 File: 页地址归一化成 MediaWiki 用的标题（File:名字）。
// 支持完整 URL、带 title= 的 index.php 地址、已归一化的标题与裸文件名四种写法。
func FileTitle(filePage string) (string, error) {
	raw := strings.TrimSpace(filePage)
	if raw == "" {
		return "", fmt.Errorf("文件页地址为空")
	}
	// index.php?title=... 这种形式要先取出查询参数，否则整串会被当成文件名。
	if idx := strings.Index(raw, "title="); idx >= 0 {
		raw = raw[idx+len("title="):]
		if amp := strings.IndexByte(raw, '&'); amp >= 0 {
			raw = raw[:amp]
		}
	} else if idx := strings.Index(raw, "/wiki/"); idx >= 0 {
		raw = raw[idx+len("/wiki/"):]
	}
	// 去掉 ?query 与 #fragment：wiki 的文件地址有时带版本参数（形如 ?5fee45）。
	if idx := strings.IndexAny(raw, "?#"); idx >= 0 {
		raw = raw[:idx]
	}
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		decoded = raw
	}
	decoded = strings.TrimSpace(strings.ReplaceAll(decoded, "_", " "))
	if decoded == "" {
		return "", fmt.Errorf("文件页地址里没有文件名：%q", filePage)
	}
	if !strings.HasPrefix(decoded, "File:") && !strings.HasPrefix(decoded, "文件:") {
		decoded = "File:" + decoded
	}
	return decoded, nil
}

// resolveTitle 跟随标题重定向；解析不出终点时返回原标题。
func resolveTitle(title string, aliases map[string]string) string {
	current := title
	for i := 0; i < redirectHopLimit; i++ {
		next, ok := aliases[current]
		if !ok || next == current {
			return current
		}
		current = next
	}
	return current
}

// iconURL 返回一个页面能用的图标地址：优先缩略图，没有时退回原图
// （原图本身就小于请求宽度时 MediaWiki 不生成缩略图，实测 Hunter 就是这种情况）。
func (p queryPage) iconURL() string {
	if p.Thumbnail != nil {
		if src := usableImageURL(p.Thumbnail.Source); src != "" {
			return src
		}
	}
	if p.Original != nil {
		return usableImageURL(p.Original.Source)
	}
	return ""
}

// imageThumbURL 取 imageinfo 里的图片地址：优先缩略图（按 iiurlwidth 生成的小图），
// 没有缩略图时退回原图（原图本身就小于请求宽度时站点不生成缩略图）；SVG 一律挡掉。
func (p queryPage) imageThumbURL() string {
	if len(p.ImageInfo) == 0 {
		return ""
	}
	info := p.ImageInfo[0]
	if thumb := usableImageURL(info.ThumbURL); thumb != "" {
		return thumb
	}
	return usableImageURL(info.URL)
}

// usableImageURL 过滤掉空地址与 SVG：Telegram 不接受 SVG 缩略图。
// 判断的是「扩展名（忽略查询串）」，因为 wiki 的地址形如 /images/x.svg?c5bd41。
// 返回的是原始地址（保留查询串里的版本号，换图后能自动失效）。
func usableImageURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	path := trimmed
	if idx := strings.IndexAny(path, "?#"); idx >= 0 {
		path = path[:idx]
	}
	if strings.HasSuffix(strings.ToLower(path), ".svg") {
		return ""
	}
	return trimmed
}

// apiURL 拼出 api.php 的完整地址。
func (c *Client) apiURL(params url.Values) string {
	return c.base + "/api.php?" + params.Encode()
}

// getJSON 发一次 GET 并把响应解析进 out。
// 非 200、响应过大、JSON 坏掉都返回中文错误：调用方（bestiary / arsenal）会把它们记进日志。
func (c *Client) getJSON(ctx context.Context, params url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiURL(params), nil)
	if err != nil {
		return fmt.Errorf("构造 wiki 请求失败：%w", err)
	}
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("请求 wiki 失败：%w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("wiki 返回 HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("读取 wiki 响应失败：%w", err)
	}
	if len(raw) > maxResponseBytes {
		return fmt.Errorf("wiki 响应超过 %d 字节上限", maxResponseBytes)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("解析 wiki 响应失败：%w", err)
	}
	return nil
}

// apiError 是 MediaWiki 的错误体（两个接口都可能返回）。
type apiError struct {
	Code string `json:"code"`
	Info string `json:"info"`
}

// describe 把错误体拼成一句可读说明；两个字段都空时给出「未知原因」，绝不返回空串。
func (e *apiError) describe() string {
	code := strings.TrimSpace(e.Code)
	info := strings.TrimSpace(e.Info)
	switch {
	case code == "" && info == "":
		return "未知原因"
	case info == "":
		return code
	case code == "":
		return info
	default:
		return code + "：" + info
	}
}

// titlePair 是 normalized / redirects 两个数组的元素（字段名相同）。
type titlePair struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// queryEnvelope 是 action=query 的响应。只声明用得到的字段：站点还会回大量本项目用不上的结构，
// 不声明它们就不会因为站点小改字段而解析失败。
type queryEnvelope struct {
	Query struct {
		Normalized []titlePair          `json:"normalized"`
		Redirects  []titlePair          `json:"redirects"`
		Pages      map[string]queryPage `json:"pages"`
	} `json:"query"`
	Error *apiError `json:"error"`
}

// queryPage 是响应里的一个页面。missing 只在页面不存在时出现，用 RawMessage 判有没有这个键。
type queryPage struct {
	Title     string           `json:"title"`
	Missing   *json.RawMessage `json:"missing"`
	ImageInfo []struct {
		ThumbURL string `json:"thumburl"`
		URL      string `json:"url"`
	} `json:"imageinfo"`
	Thumbnail *imageRef `json:"thumbnail"`
	Original  *imageRef `json:"original"`
}

// imageRef 是 pageimages 返回的图片引用。
type imageRef struct {
	Source string `json:"source"`
}

// cargoEnvelope 是 action=cargoquery 的响应。
// Cargo 把每行的字段挂在该行第一个请求字段名下——本例请求了 title，所以每行形如
// {"title": {"title": "...", "faction": "...", ...}}，看起来像套了两层。
type cargoEnvelope struct {
	CargoQuery []struct {
		Fields map[string]any `json:"title"`
	} `json:"cargoquery"`
	Error *apiError `json:"error"`
}

// asString 把 Cargo 返回的任意标量转成字符串：站点有时把数字字段回成 number 而不是 string。
// 认不出的类型（对象、数组）返回空串，调用方按「上游没给」处理。
func asString(v any) string {
	switch value := v.(type) {
	case nil:
		return ""
	case string:
		return value
	case float64:
		// JSON 数字统一解析成 float64；整数去掉小数点，小数按原值输出。
		return strconv.FormatFloat(value, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(value)
	default:
		return ""
	}
}
