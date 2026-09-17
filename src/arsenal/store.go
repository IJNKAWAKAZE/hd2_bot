// 装备目录的取数与本地缓存：目录 JSON、玩家外号表、按需下载的装备图与缩略图地址。
//
// 缓存策略（与 hd2 包的快照降级同一套思路）：
//   - 内存缓存：解析好的目录常驻内存，命令执行时不再碰磁盘（400KB JSON 每次解析是白费 CPU）；
//   - 磁盘缓存：catalog.json / community-aliases.json 落盘，TTL 内直接用，过期才去拉新的；
//   - 降级：拉新失败但磁盘上有旧缓存时，用旧缓存并在日志里说明——群里能查到东西比「查询失败」有用；
//   - 装备图：按需下载、永久落盘（图的路径带 id，内容变了上游会换路径），失败就是这张卡不挂图。
package arsenal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// defaultDir 是缓存目录（相对进程工作目录），与 state 的 data/hd2.db 放在同一棵树下。
	defaultDir = "data/arsenal"
	// defaultTTL 是目录缓存的有效期：上游是社区维护的数据集，一天一更足够新。
	defaultTTL = 24 * time.Hour
	// defaultTimeout 是单次下载超时。目录 400KB 左右，30 秒对慢网也够。
	defaultTimeout = 30 * time.Second
	// maxCatalogBytes / maxImageBytes 是下载体积上限：防的是镜像返回异常大响应把进程内存拖垮，
	// 不是防正常数据（实测目录 ~400KB、单张装备图 33~138KB）。
	maxCatalogBytes = 16 << 20
	maxImageBytes   = 4 << 20
	// thumbWidth 是行内缩略图的宽度。Telegram 只显示小图标，240 足够且省流量。
	thumbWidth = 240
	// catalogFileName / aliasFileName / thumbFileName / imageDirName 是缓存文件名。
	catalogFileName = "catalog.json"
	aliasFileName   = "community-aliases.json"
	thumbFileName   = "thumbs.json"
	imageDirName    = "images"
)

// 上游地址。主源是 GitHub raw；镜像用于 GitHub 直连不通的网络（实测本机时通时断）。
// 两者都通过官方的 raw 路径取文件，镜像只是前缀代理，不改内容。
const (
	githubRawPrefix     = "https://raw.githubusercontent.com/SalmonC/HD2Tool/main/"
	defaultMirrorPrefix = "https://gh-proxy.org/"
)

// defaultMirror 是内置镜像前缀；Config.MirrorPrefix 可以换成别的（或置空表示只用主源）。
var defaultMirror = defaultMirrorPrefix

// ThumbResolver 把 wiki.gg 的 File: 页地址解析成一张 Telegram 能用的 PNG 缩略图地址。
// 单独抽成接口的原因：本包只用它做一件事（SVG 图标换成 PNG），测试里注入假实现就不必起 HTTP 服务，
// 生产环境由 main 注入 wiki.gg 客户端（见 src/wikigg）。
type ThumbResolver interface {
	FileThumbURL(ctx context.Context, filePage string, width int) (string, error)
}

// Config 是数据层的配置；零值可用（全部走默认值）。
type Config struct {
	Dir          string        // 缓存目录；空串用 defaultDir
	TTL          time.Duration // 目录缓存有效期；<=0 用 defaultTTL
	Timeout      time.Duration // 下载超时；<=0 用 defaultTimeout
	SourcePrefix string        // 上游地址前缀（含结尾斜杠）；空串用 GitHub raw。自建镜像与单测用得上
	MirrorPrefix string        // GitHub 镜像前缀（含结尾斜杠）；空串用内置镜像，传 "-" 表示不用镜像
}

// Deps 是外部依赖，除 HTTP 外都可以留空。
type Deps struct {
	HTTP   *http.Client // 自定义客户端；nil 时按 Config.Timeout 新建
	Now    func() time.Time
	Logger func(format string, args ...any)
	Thumb  ThumbResolver // nil 表示不做 SVG → PNG 转换（那些装备在行内结果里就没有缩略图）
}

// Store 是装备目录的取数入口。并发安全：命令与行内查询会同时用到它。
type Store struct {
	cfg   Config
	http  *http.Client
	log   func(format string, args ...any)
	now   func() time.Time
	thumb ThumbResolver

	mu        sync.Mutex
	catalog   *Catalog
	loadedAt  time.Time
	thumbURLs map[string]string // File: 页地址 → PNG 缩略图地址
	// noThumb 记下「上游这类文件就是没有位图缩略图」的 File: 页地址（站点没开 SVG 渲染）。
	// 这是一次请求才能确认的事实，而且不会自己变好；不记下来的话，行内查询每敲一个字
	// 都会为同一批装备再问一次 wiki，白白多打几十个请求。只记确定性结论，不记临时错误。
	noThumb map[string]bool
}

// New 构造取数入口；与其它包一致，构造本身不做任何网络请求。
func New(cfg Config, deps Deps) *Store {
	if strings.TrimSpace(cfg.Dir) == "" {
		cfg.Dir = defaultDir
	}
	if cfg.TTL <= 0 {
		cfg.TTL = defaultTTL
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	client := deps.HTTP
	if client == nil {
		client = &http.Client{Timeout: cfg.Timeout}
	}
	logf := deps.Logger
	if logf == nil {
		logf = func(string, ...any) {}
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	return &Store{
		cfg:       cfg,
		http:      client,
		log:       logf,
		now:       now,
		thumb:     deps.Thumb,
		thumbURLs: map[string]string{},
	}
}

// Dir 返回缓存目录（供启动日志与测试用）。
func (s *Store) Dir() string { return s.cfg.Dir }

// sourcePrefix 返回实际使用的主源前缀（含结尾斜杠）。
func (s *Store) sourcePrefix() string {
	if prefix := strings.TrimSpace(s.cfg.SourcePrefix); prefix != "" {
		return prefix
	}
	return githubRawPrefix
}

// mirrorPrefix 返回实际使用的镜像前缀；Config.MirrorPrefix 传 "-" 表示只用主源。
func (s *Store) mirrorPrefix() string {
	switch {
	case s.cfg.MirrorPrefix == "-":
		return ""
	case strings.TrimSpace(s.cfg.MirrorPrefix) != "":
		return s.cfg.MirrorPrefix
	default:
		return defaultMirror
	}
}

// rawURLs 把仓库内的相对路径展开成「主源 + 镜像」的候选地址列表。
// 镜像的拼法就是「镜像前缀 + 完整上游地址」，与 gh-proxy 这类代理的用法一致。
func (s *Store) rawURLs(relPath string) []string {
	primary := s.sourcePrefix() + relPath
	urls := []string{primary}
	if mirror := s.mirrorPrefix(); mirror != "" {
		urls = append(urls, mirror+primary)
	}
	return urls
}

// Catalog 返回装备目录：内存缓存 → 磁盘缓存 → 下载，逐级回退。
func (s *Store) Catalog(ctx context.Context) (*Catalog, error) {
	s.mu.Lock()
	if s.catalog != nil && s.now().Sub(s.loadedAt) < s.cfg.TTL {
		catalog := s.catalog
		s.mu.Unlock()
		return catalog, nil
	}
	s.mu.Unlock()

	catalogPath := filepath.Join(s.cfg.Dir, catalogFileName)
	aliasPath := filepath.Join(s.cfg.Dir, aliasFileName)

	// 磁盘缓存新鲜：直接解析，不发请求。
	if s.cacheFresh(catalogPath) {
		if catalog, err := s.loadFromDisk(catalogPath, aliasPath); err == nil {
			s.remember(catalog)
			return catalog, nil
		} else {
			// 缓存文件坏了（半截 JSON 等）：当成没有缓存，继续走下载。
			s.log("[arsenal] 本地目录缓存不可用，改为重新下载：%v", err)
		}
	}

	if err := s.refresh(ctx, catalogPath, aliasPath); err != nil {
		// 下载失败：磁盘上的旧缓存还能用就用它，让命令照常能用。
		if catalog, loadErr := s.loadFromDisk(catalogPath, aliasPath); loadErr == nil {
			s.log("[arsenal] 刷新目录失败，改用本地旧缓存：%v", err)
			s.remember(catalog)
			return catalog, nil
		}
		return nil, err
	}
	catalog, err := s.loadFromDisk(catalogPath, aliasPath)
	if err != nil {
		return nil, err
	}
	s.remember(catalog)
	return catalog, nil
}

// refresh 下载目录与外号表并落盘。外号表失败不算致命：少几条别名，命令照常能用。
func (s *Store) refresh(ctx context.Context, catalogPath, aliasPath string) error {
	raw, err := s.fetch(ctx, s.rawURLs("src/data/catalog.json"), maxCatalogBytes)
	if err != nil {
		return fmt.Errorf("下载装备目录失败：%w", err)
	}
	if err := writeFileAtomic(catalogPath, raw); err != nil {
		return fmt.Errorf("写入装备目录缓存失败：%w", err)
	}

	aliases, err := s.fetch(ctx, s.rawURLs("src/data/community-aliases.json"), maxCatalogBytes)
	if err != nil {
		s.log("[arsenal] 下载玩家外号表失败（少几条别名，不影响其它功能）：%v", err)
		return nil
	}
	if err := writeFileAtomic(aliasPath, aliases); err != nil {
		s.log("[arsenal] 写入玩家外号表缓存失败（不影响目录）：%v", err)
	}
	return nil
}

// loadFromDisk 读磁盘缓存并解析成 Catalog。目录文件缺失视为错误（调用方会去下载），
// 外号文件缺失只当没有别名。
func (s *Store) loadFromDisk(catalogPath, aliasPath string) (*Catalog, error) {
	raw, err := os.ReadFile(catalogPath)
	if err != nil {
		return nil, fmt.Errorf("读取装备目录缓存失败：%w", err)
	}
	aliases := map[string][]string{}
	if aliasRaw, err := os.ReadFile(aliasPath); err == nil {
		aliases = ParseAliases(aliasRaw)
	}
	return ParseCatalog(raw, aliases)
}

// remember 记下内存缓存与加载时间。
func (s *Store) remember(catalog *Catalog) {
	s.mu.Lock()
	s.catalog = catalog
	s.loadedAt = s.now()
	s.mu.Unlock()
}

// cacheFresh 判断缓存文件是否存在且还在 TTL 内。
func (s *Store) cacheFresh(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return s.now().Sub(info.ModTime()) < s.cfg.TTL
}

// Image 返回一件装备的图片字节：命中本地缓存直接读，否则下载并落盘。
//
// 失败一律上抛（调用方把「这张图没有」渲染成无图卡片，而不是让整条命令失败）。
func (s *Store) Image(ctx context.Context, item Item) ([]byte, error) {
	if strings.TrimSpace(item.ImagePath) == "" {
		return nil, fmt.Errorf("装备 %s 在上游目录里没有配图", item.ID)
	}
	path := s.imageFilePath(item)
	if raw, err := os.ReadFile(path); err == nil {
		return raw, nil
	}
	raw, err := s.fetch(ctx, s.imageURLs(item.ImagePath), maxImageBytes)
	if err != nil {
		return nil, fmt.Errorf("下载装备图失败（%s）：%w", item.ID, err)
	}
	if err := writeFileAtomic(path, raw); err != nil {
		// 落盘失败不影响本次返回：图已经拿到了，只是下次还得再下一次。
		s.log("[arsenal] 装备图落盘失败（本次仍会显示）：%v", err)
	}
	return raw, nil
}

// imageFilePath 是装备图的缓存路径；扩展名沿用上游（png / svg）。
func (s *Store) imageFilePath(item Item) string {
	name := item.ID
	if ext := filepath.Ext(item.ImagePath); ext != "" {
		name += strings.ToLower(ext)
	} else {
		name += ".png"
	}
	return filepath.Join(s.cfg.Dir, imageDirName, name)
}

// imageURLs 展开装备图的候选地址（主源 + 镜像）。
func (s *Store) imageURLs(relPath string) []string { return s.rawURLs("public/" + relPath) }

// ThumbURL 返回行内结果可以直接交给 Telegram 的公网缩略图地址。
//
// 为什么行内要用公网地址而不是本地图：Telegram 的 answerInlineQuery 只接受一个 URL 自己去取图，
// 我们不打算为它做图床，也不该把 bot token 拼进 api.telegram.org/file/... 暴露出去。
// PNG 直接给上游地址；SVG 交给 ThumbResolver 去换位图。
//
// 实测提醒：helldivers.wiki.gg 没有开 SVG 渲染，SVG 文件页返回的地址点开还是 .svg
// （58 件 SVG 装备的文件页全是这种），因此这类装备在行内结果里**没有图标**。
// 卡片不受影响：卡片里的装备图是本地图集内联进 HTML 的，Chromium 能直接渲染 SVG。
//
// 解析不出来、或拿到的不是位图时返回空串，调用方就不带缩略图——
// 宁可只有文字，也不要一个 Telegram 取不到的坏地址。
func (s *Store) ThumbURL(ctx context.Context, item Item) string {
	if strings.TrimSpace(item.ImagePath) == "" {
		return ""
	}
	if !item.ImageIsSVG {
		return s.imageURLs(item.ImagePath)[0]
	}
	if s.thumb == nil || strings.TrimSpace(item.ImageFilePage) == "" {
		return ""
	}
	if s.thumbMissing(item.ImageFilePage) {
		return ""
	}
	if url := s.cachedThumb(item.ImageFilePage); url != "" {
		return url
	}
	url, err := s.thumb.FileThumbURL(ctx, item.ImageFilePage, thumbWidth)
	if err != nil {
		s.log("[arsenal] 解析 SVG 缩略图失败（这条结果不带图标）：%v", err)
		return ""
	}
	if !isBitmapURL(url) {
		// 站点没开 SVG 渲染：拿回来的还是 .svg（见函数注释）。不缓存地址、不返回，
		// 让调用方干净地少一个图标，而不是收到一个 Telegram 抓不动、抓到了也显示不了的地址；
		// 但把「这一页没有位图」记下来，省掉后面每次查询为同一个文件再问一次 wiki。
		s.markThumbMissing(item.ImageFilePage)
		// 上游没有提供 SVG 的位图缩略图；这是正常的无图结果，不刷错误日志。
		return ""
	}
	s.storeThumb(item.ImageFilePage, url)
	return url
}

// isBitmapURL 判断地址指向的是位图（忽略查询串里的版本号）。
// 只认扩名名：上游给的都是带扩展名的直链，按扩展名判断比按内容判断省一次请求。
func isBitmapURL(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return false
	}
	if idx := strings.IndexAny(trimmed, "?#"); idx >= 0 {
		trimmed = trimmed[:idx]
	}
	return !strings.HasSuffix(strings.ToLower(trimmed), ".svg")
}

// cachedThumb 读内存里的缩略图缓存；内存没有就尝试从磁盘恢复一次。
// thumbMissing 判断这个 File: 页是否已知没有位图缩略图（站点没开 SVG 渲染）。
func (s *Store) thumbMissing(filePage string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.noThumb[filePage]
}

// markThumbMissing 记下「这一页没有位图缩略图」这个确定性结论（只放进内存，不落盘：
// 上游哪天开了 SVG 渲染，重启后自然会重新问一次）。
func (s *Store) markThumbMissing(filePage string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.noThumb == nil {
		s.noThumb = make(map[string]bool)
	}
	s.noThumb[filePage] = true
}

func (s *Store) cachedThumb(filePage string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if url, ok := s.thumbURLs[filePage]; ok {
		return url
	}
	path := filepath.Join(s.cfg.Dir, thumbFileName)
	// 缩略图地址带上游文件哈希，会随上游换图失效，所以缓存同样按目录 TTL 判新鲜度。
	if !s.cacheFresh(path) {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	stored := parseThumbCache(data)
	for key, value := range stored {
		s.thumbURLs[key] = value
	}
	return s.thumbURLs[filePage]
}

// storeThumb 记下解析结果并落盘，避免每次行内查询都去问一次 wiki.gg。
func (s *Store) storeThumb(filePage, url string) {
	s.mu.Lock()
	s.thumbURLs[filePage] = url
	snapshot := make(map[string]string, len(s.thumbURLs))
	for key, value := range s.thumbURLs {
		snapshot[key] = value
	}
	s.mu.Unlock()

	data, err := marshalThumbCache(snapshot)
	if err != nil {
		s.log("[arsenal] 缩略图缓存序列化失败（本次仍可用）：%v", err)
		return
	}
	if err := writeFileAtomic(filepath.Join(s.cfg.Dir, thumbFileName), data); err != nil {
		s.log("[arsenal] 缩略图缓存落盘失败（不影响本次结果）：%v", err)
	}
}

// fetch 依次尝试候选地址，返回第一个成功响应的内容。
// 全部失败时把每个地址的原因合并成一条错误：多源回退最怕「只知道全失败了，不知道各自为什么失败」。
func (s *Store) fetch(ctx context.Context, urls []string, limit int64) ([]byte, error) {
	errs := make([]error, 0, len(urls))
	for _, url := range urls {
		raw, err := s.fetchOne(ctx, url, limit)
		if err == nil {
			return raw, nil
		}
		s.log("[arsenal] 下载 %s 失败：%v", url, err)
		errs = append(errs, err)
	}
	return nil, errors.Join(errs...)
}

// fetchOne 下载单个地址，并拒绝超过 limit 的响应。
func (s *Store) fetchOne(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	// 多读 1 字节：读满 limit+1 说明响应超限，直接判失败，而不是把超出部分静默截断成坏数据。
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("响应超过 %d 字节上限", limit)
	}
	return raw, nil
}

// writeFileAtomic 先写临时文件再改名：进程被杀时不会留下半截 JSON，
// 而半截 JSON 会让下次启动的「读缓存」直接解析失败。
func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
