package bestiary

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hd2_bot/src/wikigg"
)

// fakeSource 是假上游：按需返回行、图标或错误，并统计调用次数，
// 用来验证「TTL 内不联网」「图标失败不影响数值」这类约定。
type fakeSource struct {
	rows          []wikigg.EnemyRow
	icons         map[string]string
	fileThumbs    map[string]string
	enemiesErr    error
	iconsErr      error
	fileThumbsErr error
	imageErr      error
	base          string

	enemiesCalls   int
	iconsCalls     int
	fileThumbCalls int
	iconTitles     []string
	fileThumbFiles []string
	imageBodies    map[string][]byte
	imageCalls     []string
}

func (f *fakeSource) Enemies(context.Context) ([]wikigg.EnemyRow, error) {
	f.enemiesCalls++
	if f.enemiesErr != nil {
		return nil, f.enemiesErr
	}
	return f.rows, nil
}

func (f *fakeSource) EnemyIcons(_ context.Context, titles []string, width int) (map[string]string, error) {
	f.iconsCalls++
	f.iconTitles = titles
	if f.iconsErr != nil {
		return nil, f.iconsErr
	}
	if width != iconWidth {
		return nil, errors.New("宽度参数不对")
	}
	return f.icons, nil
}

func (f *fakeSource) FileThumbURLs(_ context.Context, files []string, width int) (map[string]string, error) {
	f.fileThumbCalls++
	f.fileThumbFiles = files
	if f.fileThumbsErr != nil {
		return nil, f.fileThumbsErr
	}
	if width != iconWidth {
		return nil, errors.New("宽度参数不对")
	}
	return f.fileThumbs, nil
}

func (f *fakeSource) BaseURL() string { return f.base }

// ImageBytes 返回预设的图片字节，并记下请求过的地址。
func (f *fakeSource) ImageBytes(_ context.Context, url string, _ int64) ([]byte, error) {
	f.imageCalls = append(f.imageCalls, url)
	if f.imageErr != nil {
		return nil, f.imageErr
	}
	return f.imageBodies[url], nil
}

// fixtureRows 是两行可用的上游数据（一行终结族、一行机器人）。
func fixtureRows() []wikigg.EnemyRow {
	return []wikigg.EnemyRow{
		{Title: "Bile Titan", Faction: "Terminids", Size: "3", Health: "6,500"},
		{Title: "Hulk", Faction: "Automatons", Size: "", Health: "2,000 HP + 400 Constitution"},
	}
}

// newStore 构造一个用固定时钟、假上游的取数入口；缓存目录是 t.TempDir()。
func newStore(t *testing.T, src Source, now *time.Time, logf func(string, ...any)) *Store {
	t.Helper()
	dir := t.TempDir()
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return New(Config{Dir: dir}, Deps{
		Source: src,
		Now:    func() time.Time { return *now },
		Logger: logf,
	})
}

// TestStoreListFetchesAndCaches 校验首次取数会拉上游并落盘，之后内存缓存命中不再联网。
func TestStoreListFetchesAndCaches(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	src := &fakeSource{rows: fixtureRows(), icons: map[string]string{"Bile Titan": "https://w/bile.png"}, base: "https://w"}
	store := newStore(t, src, &now, nil)

	list, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("首次取数失败：%v", err)
	}
	if len(list) != 2 {
		t.Fatalf("应取到 2 只敌人，实际 %d 只", len(list))
	}
	if list[0].IconURL != "https://w/bile.png" {
		t.Errorf("图标应合并进条目，实际 %q", list[0].IconURL)
	}
	if _, err := os.Stat(filepath.Join(store.Dir(), enemyFileName)); err != nil {
		t.Errorf("应把结果落盘：%v", err)
	}
	if len(src.iconTitles) != 2 {
		t.Errorf("取图标应把全部标题传下去，实际 %v", src.iconTitles)
	}

	// 第二次读内存缓存：TTL 还没到，不该再联网。
	now = now.Add(time.Hour)
	if _, err := store.List(context.Background()); err != nil {
		t.Fatalf("二次取数失败：%v", err)
	}
	if src.enemiesCalls != 1 || src.iconsCalls != 1 {
		t.Errorf("TTL 内不该重复联网，实际 enemies=%d icons=%d", src.enemiesCalls, src.iconsCalls)
	}
}

// TestStoreListUsesFreshDiskCache 校验磁盘缓存新鲜时不联网（新进程重启后的第一条命令就走这条路）。
func TestStoreListUsesFreshDiskCache(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	src := &fakeSource{rows: fixtureRows(), base: "https://w"}
	first := newStore(t, src, &now, nil)
	if _, err := first.List(context.Background()); err != nil {
		t.Fatalf("首次取数失败：%v", err)
	}

	// 换一个 Store（模拟重启），沿用同一个缓存目录与时钟。
	second := New(Config{Dir: first.Dir()}, Deps{Source: src, Now: func() time.Time { return now }, Logger: func(string, ...any) {}})
	if _, err := second.List(context.Background()); err != nil {
		t.Fatalf("重启后取数失败：%v", err)
	}
	if src.enemiesCalls != 1 {
		t.Errorf("磁盘缓存新鲜时不该再联网，实际调用 %d 次", src.enemiesCalls)
	}
}

// TestStoreListRefreshesAfterTTL 校验 TTL 过期后重新拉取并覆盖缓存。
func TestStoreListRefreshesAfterTTL(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	src := &fakeSource{rows: fixtureRows(), base: "https://w"}
	store := newStore(t, src, &now, nil)
	if _, err := store.List(context.Background()); err != nil {
		t.Fatalf("首次取数失败：%v", err)
	}

	now = now.Add(defaultTTL + time.Minute)
	if _, err := store.List(context.Background()); err != nil {
		t.Fatalf("过期后取数失败：%v", err)
	}
	if src.enemiesCalls != 2 {
		t.Errorf("TTL 过期后应重新拉取，实际 %d 次", src.enemiesCalls)
	}
}

// TestStoreListFallsBackToStaleCache 校验拉取失败但有旧缓存时用旧缓存，并记一条中文日志。
func TestStoreListFallsBackToStaleCache(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	src := &fakeSource{rows: fixtureRows(), base: "https://w"}
	store := newStore(t, src, &now, nil)
	if _, err := store.List(context.Background()); err != nil {
		t.Fatalf("首次取数失败：%v", err)
	}

	logged := make([]string, 0, 4)
	now = now.Add(defaultTTL + time.Minute)
	src.enemiesErr = errors.New("网络炸了")
	restarted := New(Config{Dir: store.Dir()}, Deps{
		Source: src,
		Now:    func() time.Time { return now },
		Logger: func(format string, args ...any) { logged = append(logged, format) },
	})
	list, err := restarted.List(context.Background())
	if err != nil {
		t.Fatalf("有旧缓存时不该报错：%v", err)
	}
	if len(list) != 2 {
		t.Errorf("应返回旧缓存里的 2 只敌人，实际 %d 只", len(list))
	}
	joined := strings.Join(logged, "\n")
	if !strings.Contains(joined, "改用本地旧缓存") {
		t.Errorf("降级应记一条中文日志，实际 %v", logged)
	}
}

// TestStoreListReportsErrorWithoutCache 校验既没有缓存、上游又失败时返回中文错误。
func TestStoreListReportsErrorWithoutCache(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	src := &fakeSource{enemiesErr: errors.New("网络炸了"), base: "https://w"}
	store := newStore(t, src, &now, nil)
	_, err := store.List(context.Background())
	if err == nil {
		t.Fatal("没有缓存时上游失败应报错")
	}
	if !strings.Contains(err.Error(), "拉取敌人数据失败") {
		t.Errorf("错误文案应说明是拉取失败，实际 %v", err)
	}
}

// TestStoreListToleratesIconFailure 校验图标取不到时不影响数值（图标只是行内结果的辅助信息）。
func TestStoreListToleratesIconFailure(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	logged := make([]string, 0, 4)
	src := &fakeSource{rows: fixtureRows(), iconsErr: errors.New("wiki 挂了"), base: "https://w"}
	store := newStore(t, src, &now, func(format string, args ...any) { logged = append(logged, format) })

	list, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("图标失败不该让整次取数失败：%v", err)
	}
	if len(list) != 2 || list[0].IconURL != "" {
		t.Errorf("数值应照常返回、图标留空，实际 %+v", list)
	}
	if !strings.Contains(strings.Join(logged, "\n"), "拉取敌人图标失败") {
		t.Errorf("图标失败应记一条中文日志，实际 %v", logged)
	}
}

// TestStoreListRejectsBrokenCacheAndRefetches 校验坏掉的缓存当成没有缓存，重新拉取后覆盖。
func TestStoreListRejectsBrokenCacheAndRefetches(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	src := &fakeSource{rows: fixtureRows(), base: "https://w"}
	store := newStore(t, src, &now, nil)
	path := filepath.Join(store.Dir(), enemyFileName)
	if err := os.WriteFile(path, []byte("{半截 JSON"), 0o644); err != nil {
		t.Fatalf("写入坏缓存失败：%v", err)
	}

	list, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("缓存坏掉时应重新拉取：%v", err)
	}
	if len(list) != 2 {
		t.Errorf("应拿到重新拉取的 2 只敌人，实际 %d 只", len(list))
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读回缓存失败：%v", err)
	}
	var restored []Enemy
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Errorf("缓存应被新数据覆盖为合法 JSON：%v", err)
	}
}

// TestStoreListRejectsEmptyUpstreamData 校验上游只剩被过滤掉的条目时报错，而不是缓存一张空表。
func TestStoreListRejectsEmptyUpstreamData(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	src := &fakeSource{rows: []wikigg.EnemyRow{{Title: "Bug Warrior", Faction: "Bugs"}}, base: "https://w"}
	store := newStore(t, src, &now, nil)
	_, err := store.List(context.Background())
	if err == nil {
		t.Fatal("过滤后为空应报错")
	}
	if !strings.Contains(err.Error(), "解析敌人数据失败") {
		t.Errorf("错误文案应说明是解析失败，实际 %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(store.Dir(), enemyFileName)); statErr == nil {
		t.Error("坏数据不该落盘（下次启动会读到一张空表）")
	}
}

// TestStoreListWithoutSource 校验没有数据源时返回中文错误，而不是 panic。
func TestStoreListWithoutSource(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	store := newStore(t, nil, &now, nil)
	_, err := store.List(context.Background())
	if err == nil || !strings.Contains(err.Error(), "数据源未配置") {
		t.Fatalf("应返回「数据源未配置」，实际 %v", err)
	}
}

// TestStoreWritesNoTempFile 校验原子写不会留下 .tmp 残留。
func TestStoreWritesNoTempFile(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	src := &fakeSource{rows: fixtureRows(), base: "https://w"}
	store := newStore(t, src, &now, nil)
	if _, err := store.List(context.Background()); err != nil {
		t.Fatalf("首次取数失败：%v", err)
	}
	entries, err := os.ReadDir(store.Dir())
	if err != nil {
		t.Fatalf("读取缓存目录失败：%v", err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Errorf("不该留下临时文件：%s", entry.Name())
		}
	}
}

// TestStoreSearch 校验 Search 走的是同一份缓存与同一套排序口径。
func TestStoreSearch(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	src := &fakeSource{rows: fixtureRows(), base: "https://w"}
	store := newStore(t, src, &now, nil)

	res, err := store.Search(context.Background(), Query{Keyword: "吐酸", Limit: 5})
	if err != nil {
		t.Fatalf("检索失败：%v", err)
	}
	if len(res.Enemies) != 1 || res.Enemies[0].Title != "Bile Titan" {
		t.Fatalf("应按中文名命中吐酸泰坦，实际 %+v", res.Enemies)
	}
	if src.enemiesCalls != 1 {
		t.Errorf("检索应复用同一次取数，实际拉取 %d 次", src.enemiesCalls)
	}
}

// TestStoreDirDefaults 校验缓存目录默认值。
func TestStoreDirDefaults(t *testing.T) {
	store := New(Config{}, Deps{Source: &fakeSource{}})
	if store.Dir() != defaultDir {
		t.Errorf("默认缓存目录应为 %s，实际 %s", defaultDir, store.Dir())
	}
}

// TestStorePrefersCargoImageFile 校验图标优先来自 Enemies.image 那一列（逐只的 PNG）：
// pageimages 对敌人页多半回阵营 SVG 图标，实测 10 条只有 1 条能用，所以能用文件名就不要再问它。
func TestStorePrefersCargoImageFile(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	rows := []wikigg.EnemyRow{
		{Title: "Bile Titan", Faction: "Terminids", Size: "3", Health: "6,500", Image: "Bile Titan Enemy Icon.png"},
		{Title: "Scavenger", Faction: "Terminids", Size: "0", Health: "60", Image: "Scavenger Enemy Icon.png"},
	}
	src := &fakeSource{
		rows:  rows,
		icons: map[string]string{"Bile Titan": "https://w/faction-icon.svg"},
		fileThumbs: map[string]string{
			"Bile Titan Enemy Icon.png": "https://w/thumb/bile.png",
			"Scavenger Enemy Icon.png":  "https://w/thumb/scavenger.png",
		},
		base: "https://w",
	}
	store := newStore(t, src, &now, nil)

	list, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("取数失败：%v", err)
	}
	if len(src.fileThumbFiles) != 2 {
		t.Errorf("应把两行给的图片文件名都传下去，实际 %v", src.fileThumbFiles)
	}
	if src.iconsCalls != 0 {
		t.Errorf("有图片文件名时不该再问 pageimages，实际问了 %d 次", src.iconsCalls)
	}
	if list[0].IconURL != "https://w/thumb/bile.png" || list[1].IconURL != "https://w/thumb/scavenger.png" {
		t.Errorf("图标应来自图片文件名，实际 %q / %q", list[0].IconURL, list[1].IconURL)
	}
}

// TestStoreFallsBackToPageImages 校验站点没给图片文件名的条目退回 pageimages（少数情况）。
func TestStoreFallsBackToPageImages(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	rows := []wikigg.EnemyRow{
		{Title: "Bile Titan", Faction: "Terminids", Size: "3", Health: "6,500", Image: "Bile Titan Enemy Icon.png"},
		{Title: "Hunter", Faction: "Terminids", Size: "1", Health: "160"},
	}
	src := &fakeSource{
		rows:       rows,
		icons:      map[string]string{"Hunter": "https://w/thumb/hunter.png"},
		fileThumbs: map[string]string{"Bile Titan Enemy Icon.png": "https://w/thumb/bile.png"},
		base:       "https://w",
	}
	store := newStore(t, src, &now, nil)

	list, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("取数失败：%v", err)
	}
	if len(src.iconTitles) != 1 || src.iconTitles[0] != "Hunter" {
		t.Errorf("只应给没有图片文件名的条目兜底，实际传了 %v", src.iconTitles)
	}
	if list[0].IconURL != "https://w/thumb/bile.png" || list[1].IconURL != "https://w/thumb/hunter.png" {
		t.Errorf("两行都应拿到图标，实际 %q / %q", list[0].IconURL, list[1].IconURL)
	}
}

// TestStoreIconFileFailureIsSoft 校验图片文件名那一步失败时只记日志：数值照常可用，只是没有图标。
func TestStoreIconFileFailureIsSoft(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	logs := make([]string, 0, 4)
	rows := []wikigg.EnemyRow{
		{Title: "Bile Titan", Faction: "Terminids", Size: "3", Health: "6,500", Image: "Bile Titan Enemy Icon.png"},
	}
	src := &fakeSource{rows: rows, fileThumbsErr: errors.New("wiki 502"), base: "https://w"}
	store := newStore(t, src, &now, func(format string, args ...any) {
		logs = append(logs, format)
	})

	list, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("图标失败不该让取数失败：%v", err)
	}
	if len(list) != 1 || list[0].Health != 6500 {
		t.Fatalf("数值应照常可用：%+v", list)
	}
	if list[0].IconURL != "" {
		t.Errorf("取不到图标时应留空，实际 %q", list[0].IconURL)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "拉取敌人图标失败") {
		t.Errorf("取不到图标应留下一条中文日志线索，实际 %v", logs)
	}
}

// TestStoreIconDownloadsAndCaches 校验敌人图标：第一次下载并落盘，第二次直接读缓存不再发请求。
func TestStoreIconDownloadsAndCaches(t *testing.T) {
	dir := t.TempDir()
	raw := []byte("PNG-BYTES")
	src := &fakeSource{
		rows:        fixtureRows(),
		base:        "https://w",
		icons:       map[string]string{"Bile Titan": "https://w/bile.png"},
		imageBodies: map[string][]byte{"https://w/bile.png": raw},
	}
	store := New(Config{Dir: dir}, Deps{Source: src})
	list, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List 出错：%v", err)
	}
	var titan Enemy
	for _, enemy := range list {
		if enemy.Title == "Bile Titan" {
			titan = enemy
		}
	}
	if titan.Title == "" {
		t.Fatal("夹具里应有 Bile Titan")
	}

	got, err := store.Icon(context.Background(), titan)
	if err != nil {
		t.Fatalf("Icon 出错：%v", err)
	}
	if !bytes.Equal(got, raw) {
		t.Errorf("图标字节不对：%q", got)
	}
	if len(src.imageCalls) != 1 {
		t.Fatalf("应下载一次，实际 %d 次", len(src.imageCalls))
	}
	if _, err := store.Icon(context.Background(), titan); err != nil {
		t.Fatalf("第二次 Icon 出错：%v", err)
	}
	if len(src.imageCalls) != 1 {
		t.Errorf("第二次应命中本地缓存、不再下载，实际下载 %d 次", len(src.imageCalls))
	}
}

// TestStoreImageDownloadsAndCaches 校验「按地址下载」按地址缓存：同一张图只下一次，重启后仍命中磁盘。
func TestStoreImageDownloadsAndCaches(t *testing.T) {
	dir := t.TempDir()
	raw := []byte("PART-PNG")
	const url = "https://w/images/thumb/Bile_Titan_Head_Front.png/200px-Bile_Titan_Head_Front.png?abc"
	src := &fakeSource{rows: fixtureRows(), base: "https://w", imageBodies: map[string][]byte{url: raw}}
	store := New(Config{Dir: dir}, Deps{Source: src})

	got, err := store.Image(context.Background(), url)
	if err != nil {
		t.Fatalf("Image 出错：%v", err)
	}
	if !bytes.Equal(got, raw) {
		t.Errorf("图片字节不对：%q", got)
	}
	if _, err := store.Image(context.Background(), "  "+url+"  "); err != nil {
		t.Fatalf("第二次 Image 出错：%v", err)
	}
	if len(src.imageCalls) != 1 {
		t.Errorf("同一地址应只下载一次，实际 %d 次", len(src.imageCalls))
	}

	// 换一个进程实例（模拟重启）：缓存文件在，仍然不联网。
	again := New(Config{Dir: dir}, Deps{Source: src})
	if _, err := again.Image(context.Background(), url); err != nil {
		t.Fatalf("重启后 Image 出错：%v", err)
	}
	if len(src.imageCalls) != 1 {
		t.Errorf("重启后应命中磁盘缓存，实际下载 %d 次", len(src.imageCalls))
	}
	if entries, err := os.ReadDir(filepath.Join(dir, iconDirName)); err != nil || len(entries) != 1 {
		t.Errorf("缓存目录里应只有一张图，实际 %v（%v）", entries, err)
	}
}

// TestStoreImageRejectsEmptyURL 校验地址为空时报中文错误，而不是发一个空请求。
func TestStoreImageRejectsEmptyURL(t *testing.T) {
	src := &fakeSource{rows: fixtureRows(), base: "https://w"}
	store := New(Config{Dir: t.TempDir()}, Deps{Source: src})
	if _, err := store.Image(context.Background(), "   "); err == nil {
		t.Fatal("地址为空时应报错")
	}
	if len(src.imageCalls) != 0 {
		t.Errorf("不该发请求，实际 %d 次", len(src.imageCalls))
	}
}

// TestStoreImageDownloadFailure 校验下载失败时上抛中文错误（调用方据此画无图卡片）。
func TestStoreImageDownloadFailure(t *testing.T) {
	src := &fakeSource{rows: fixtureRows(), base: "https://w", imageErr: errors.New("网络炸了")}
	store := New(Config{Dir: t.TempDir()}, Deps{Source: src})
	_, err := store.Image(context.Background(), "https://w/part.png")
	if err == nil {
		t.Fatal("下载失败时应报错")
	}
	if !strings.Contains(err.Error(), "下载图片失败") {
		t.Errorf("错误文案应说清是下载图片失败：%v", err)
	}
}

// TestStoreIconWithoutImage 校验上游没给图标地址时报中文错误，而不是去发一个空地址的请求。
func TestStoreIconWithoutImage(t *testing.T) {
	src := &fakeSource{rows: fixtureRows(), base: "https://w"}
	store := New(Config{Dir: t.TempDir()}, Deps{Source: src})
	if _, err := store.Icon(context.Background(), Enemy{Title: "无图敌人"}); err == nil {
		t.Fatal("没有图标地址时应报错")
	}
	if len(src.imageCalls) != 0 {
		t.Errorf("不该发请求，实际 %d 次", len(src.imageCalls))
	}
}

// TestStoreIconDownloadFailure 校验下载失败时上抛中文错误（调用方据此画无图卡片）。
func TestStoreIconDownloadFailure(t *testing.T) {
	src := &fakeSource{rows: fixtureRows(), base: "https://w", imageErr: errors.New("网络炸了")}
	store := New(Config{Dir: t.TempDir()}, Deps{Source: src})
	_, err := store.Icon(context.Background(), Enemy{Title: "Bile Titan", IconURL: "https://w/bile.png"})
	if err == nil {
		t.Fatal("下载失败时应报错")
	}
	if !strings.Contains(err.Error(), "下载敌人图标失败") {
		t.Errorf("错误文案应说清是下载图标失败：%v", err)
	}
}
