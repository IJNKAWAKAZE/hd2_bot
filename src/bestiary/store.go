// 敌人图鉴的取数与本地缓存：与 src/arsenal 用同一套策略——
// 内存缓存 → 磁盘缓存（TTL 内直接用）→ 拉上游；拉不到但有旧缓存时降级用旧缓存，
// 两者都没有才报错。数据一天一更，因此 TTL 同样是 24 小时。
package bestiary

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"hd2_bot/src/wikigg"
)

const (
	// defaultDir 是缓存目录（相对进程工作目录），与 state 的 data/hd2.db 放在同一棵树下。
	defaultDir = "data/bestiary"
	// defaultTTL 是条目缓存的有效期：站点数据是社区维护的，一天一更足够新。
	defaultTTL = 24 * time.Hour
	// enemyFileName 是缓存文件名。
	enemyFileName = "enemies.json"
	// iconWidth 是取图标时请求的宽度（px）。Telegram 在行内结果里只显示小图标，480 足够清楚。
	iconWidth = 480
	// iconDirName 是图标缓存的子目录名。
	iconDirName = "icons"
	// maxIconBytes 是单张图标的体积上限（4 MiB）：防的是异常响应把内存拖垮。
	maxIconBytes = 4 << 20
)

// Source 是取敌人数据的上游能力；*wikigg.Client 实现了它，测试注入假实现即可。
type Source interface {
	// Enemies 返回 Enemies 表的全部行（未过滤、未清洗）。
	Enemies(ctx context.Context) ([]wikigg.EnemyRow, error)
	// FileThumbURLs 返回「图片文件名 → 公网缩略图地址」的映射；缺键表示这个文件没有可用的位图。
	FileThumbURLs(ctx context.Context, files []string, width int) (map[string]string, error)
	// EnemyIcons 按页面取图标（pageimages），返回「标题 → 公网图标地址」的映射；
	// 只在条目没给图片文件名时兜底用（站点多半回的是阵营 SVG 图标，实际很少命中）。
	EnemyIcons(ctx context.Context, titles []string, width int) (map[string]string, error)
	// BaseURL 是站点根地址，用来拼条目的 wiki 地址。
	BaseURL() string
	// ImageBytes 下载一张图片的原始字节（卡片上的敌人图标）；limit 是体积上限。
	ImageBytes(ctx context.Context, url string, limit int64) ([]byte, error)
}

// Config 是数据层的配置；零值可用（全部走默认值）。
type Config struct {
	Dir string        // 缓存目录；空串用 defaultDir
	TTL time.Duration // 缓存有效期；<=0 用 defaultTTL
}

// Deps 是外部依赖。Source 必填（为空时 List 返回中文错误），其余都可以留空。
type Deps struct {
	Source Source
	Now    func() time.Time
	Logger func(format string, args ...any)
}

// Store 是敌人图鉴的取数入口。并发安全：命令与行内查询会同时用到它。
type Store struct {
	cfg    Config
	source Source
	log    func(format string, args ...any)
	now    func() time.Time

	mu       sync.Mutex
	enemies  []Enemy
	loadedAt time.Time
}

// New 构造取数入口；与其它包一致，构造本身不做任何网络请求。
func New(cfg Config, deps Deps) *Store {
	if strings.TrimSpace(cfg.Dir) == "" {
		cfg.Dir = defaultDir
	}
	if cfg.TTL <= 0 {
		cfg.TTL = defaultTTL
	}
	logf := deps.Logger
	if logf == nil {
		logf = func(string, ...any) {}
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	return &Store{cfg: cfg, source: deps.Source, log: logf, now: now}
}

// LoadedAt 返回最近一次加载图鉴的时间（内存命中、磁盘命中或现拉都算）；从没加载过时是零值。
//
// 展示层用它当卡片的「数据时间」：图鉴是现取的数据，这个时间说的是「这份图鉴什么时候拿到的」，
// 而不是站点上数据本身的版本时间——用当前时刻顶替会让「拿到的其实是 20 小时前的缓存」看起来一样新。
func (s *Store) LoadedAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadedAt
}

// Dir 返回缓存目录（供启动日志与测试用）。
func (s *Store) Dir() string { return s.cfg.Dir }

// List 返回全部图鉴条目：内存缓存 → 磁盘缓存 → 拉上游，逐级回退。
func (s *Store) List(ctx context.Context) ([]Enemy, error) {
	s.mu.Lock()
	if s.enemies != nil && s.now().Sub(s.loadedAt) < s.cfg.TTL {
		enemies := s.enemies
		s.mu.Unlock()
		return enemies, nil
	}
	s.mu.Unlock()

	path := filepath.Join(s.cfg.Dir, enemyFileName)

	// 磁盘缓存新鲜：直接解析，不发请求。
	if s.cacheFresh(path) {
		if enemies, err := s.loadFromDisk(path); err == nil {
			s.remember(enemies)
			return enemies, nil
		} else {
			// 缓存文件坏了（半截 JSON 等）：当成没有缓存，继续走拉取。
			s.log("[bestiary] 本地缓存不可用，改为重新拉取：%v", err)
		}
	}

	if err := s.refresh(ctx, path); err != nil {
		// 拉取失败：磁盘上的旧缓存还能用就用它，让命令照常能用。
		if enemies, loadErr := s.loadFromDisk(path); loadErr == nil {
			s.log("[bestiary] 刷新图鉴失败，改用本地旧缓存：%v", err)
			s.remember(enemies)
			return enemies, nil
		}
		return nil, err
	}
	enemies, err := s.loadFromDisk(path)
	if err != nil {
		return nil, err
	}
	s.remember(enemies)
	return enemies, nil
}

// Search 按关键字检索：取全量（含缓存）后交给纯函数 Search，截断与排序口径只有一份实现。
func (s *Store) Search(ctx context.Context, q Query) (Result, error) {
	list, err := s.List(ctx)
	if err != nil {
		return Result{}, err
	}
	return Search(list, q), nil
}

// refresh 拉数据、清洗、落盘。
// 图标取不到只记日志：条目上的数值才是主体，图标只是行内结果里的辅助信息。
func (s *Store) refresh(ctx context.Context, path string) error {
	if s.source == nil {
		return fmt.Errorf("敌人图鉴数据源未配置")
	}
	rows, err := s.source.Enemies(ctx)
	if err != nil {
		return fmt.Errorf("拉取敌人数据失败：%w", err)
	}
	icons := s.iconsFor(ctx, rows)
	enemies := Parse(rows, icons, s.source.BaseURL())
	if err := Validate(enemies); err != nil {
		return fmt.Errorf("解析敌人数据失败：%w", err)
	}
	data, err := json.Marshal(enemies)
	if err != nil {
		return fmt.Errorf("序列化敌人数据失败：%w", err)
	}
	if err := writeFileAtomic(path, data); err != nil {
		return fmt.Errorf("写入敌人数据缓存失败：%w", err)
	}
	return nil
}

// iconsFor 取条目图标，返回「标题 → 公网地址」。
//
// 优先用上游那一列图片文件名（Enemies.image，实测全表都有，而且都是 PNG）：
// pageimages 对敌人页多半回的是阵营图标，而且不少是 SVG（站点没开 SVG 渲染，最终被挡掉），
// 实测 10 条里只有 1 条能用。只有站点没给文件名时才退回 pageimages。
//
// 任何一步失败都只记日志并保住已经取到的部分：数值才是主体，图标只是行内结果的辅助信息。
func (s *Store) iconsFor(ctx context.Context, rows []wikigg.EnemyRow) map[string]string {
	icons := make(map[string]string, len(rows))
	byTitle := make(map[string]string, len(rows)) // 标题 → 图片文件名
	files := make([]string, 0, len(rows))
	for _, row := range rows {
		title := strings.TrimSpace(row.Title)
		file := strings.TrimSpace(row.Image)
		if title == "" {
			continue
		}
		if file == "" {
			continue
		}
		byTitle[title] = file
		files = append(files, file)
	}
	if len(files) > 0 {
		found, err := s.source.FileThumbURLs(ctx, files, iconWidth)
		if err != nil {
			s.log("[bestiary] 拉取敌人图标失败（条目不带图标，数值照常可用）：%v", err)
		}
		for title, file := range byTitle {
			if url := found[file]; url != "" {
				icons[title] = url
			}
		}
	}

	// 站点没给文件名的条目（少数）退回 pageimages；已经拿到图标的不再重复问。
	fallback := make([]string, 0, len(rows))
	for _, row := range rows {
		title := strings.TrimSpace(row.Title)
		if title == "" || byTitle[title] != "" {
			continue
		}
		if _, ok := icons[title]; ok {
			continue
		}
		fallback = append(fallback, title)
	}
	if len(fallback) == 0 {
		return icons
	}
	found, err := s.source.EnemyIcons(ctx, fallback, iconWidth)
	if err != nil {
		s.log("[bestiary] 按页面兜底拉取敌人图标失败（这些条目不带图标）：%v", err)
		return icons
	}
	for title, url := range found {
		if url != "" {
			icons[title] = url
		}
	}
	return icons
}

// Icon 返回一只敌人的图标字节：命中本地缓存直接读，否则下载并落盘。
//
// 失败一律上抛（调用方把「这只没有图」渲染成无图卡片，而不是让整条命令失败）。
// 文件名用条目标题去掉路径分隔符，同一只敌人只下一张。
func (s *Store) Icon(ctx context.Context, enemy Enemy) ([]byte, error) {
	if s.source == nil {
		return nil, errors.New("没有配置敌人图标的数据源")
	}
	url := strings.TrimSpace(enemy.IconURL)
	if url == "" {
		return nil, fmt.Errorf("敌人 %s 在上游没有配图", enemy.Title)
	}
	path := s.iconFilePath(enemy)
	if raw, err := os.ReadFile(path); err == nil && len(raw) > 0 {
		return raw, nil
	}
	raw, err := s.source.ImageBytes(ctx, url, maxIconBytes)
	if err != nil {
		return nil, fmt.Errorf("下载敌人图标失败（%s）：%w", enemy.Title, err)
	}
	if err := writeFileAtomic(path, raw); err != nil {
		// 落盘失败不影响本次返回：图已经拿到了，只是下次还得再下一次。
		s.log("[bestiary] 敌人图标落盘失败（本次仍会显示）：%v", err)
	}
	return raw, nil
}

// iconFilePath 是敌人图标的缓存路径：标题里可能带斜杠（"Jet Brigade/Trooper"），
// 一律换成下划线，免得写出缓存目录之外。
func (s *Store) iconFilePath(enemy Enemy) string {
	name := strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|' {
			return '_'
		}
		return r
	}, enemy.Title)
	if strings.TrimSpace(name) == "" {
		name = "enemy"
	}
	return filepath.Join(s.cfg.Dir, iconDirName, name+".png")
}

// Image 按公网地址下载一张图并落盘缓存。
//
// 两条路用得上它：图鉴条目自己没给图标、但内置详情数据里有地址；
// 以及敌人卡片上的部位示意图（一只敌人十几张，按地址缓存避免重复下载）。
func (s *Store) Image(ctx context.Context, url string) ([]byte, error) {
	if s.source == nil {
		return nil, errors.New("没有配置图片的数据源")
	}
	target := strings.TrimSpace(url)
	if target == "" {
		return nil, errors.New("图片地址为空")
	}
	path := s.imageFilePath(target)
	if raw, err := os.ReadFile(path); err == nil && len(raw) > 0 {
		return raw, nil
	}
	raw, err := s.source.ImageBytes(ctx, target, maxIconBytes)
	if err != nil {
		return nil, fmt.Errorf("下载图片失败（%s）：%w", target, err)
	}
	if err := writeFileAtomic(path, raw); err != nil {
		// 落盘失败不影响本次返回：图已经拿到了，只是下次还得再下一次。
		s.log("[bestiary] 图片落盘失败（本次仍会显示）：%v", err)
	}
	return raw, nil
}

// imageFilePath 是「按地址缓存」的图片路径：地址取 sha256 前 16 字节当文件名，
// 免得把带 / 与 ? 的整条地址写进文件名。
func (s *Store) imageFilePath(url string) string {
	sum := sha256.Sum256([]byte(url))
	return filepath.Join(s.cfg.Dir, iconDirName, "url-"+hex.EncodeToString(sum[:16])+".png")
}

// loadFromDisk 读磁盘缓存；文件缺失、坏掉或条目为空都返回中文错误。
func (s *Store) loadFromDisk(path string) ([]Enemy, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取敌人数据缓存失败：%w", err)
	}
	var enemies []Enemy
	if err := json.Unmarshal(raw, &enemies); err != nil {
		return nil, fmt.Errorf("解析敌人数据缓存失败：%w", err)
	}
	if err := Validate(enemies); err != nil {
		return nil, fmt.Errorf("解析敌人数据缓存失败：%w", err)
	}
	return enemies, nil
}

// cacheFresh 判断缓存文件是否存在且在 TTL 之内。
func (s *Store) cacheFresh(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return s.now().Sub(info.ModTime()) < s.cfg.TTL
}

// remember 记下内存缓存与加载时间。
func (s *Store) remember(enemies []Enemy) {
	s.mu.Lock()
	s.enemies = enemies
	s.loadedAt = s.now()
	s.mu.Unlock()
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
