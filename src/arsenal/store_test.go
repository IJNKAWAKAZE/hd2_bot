// store_test.go 覆盖取数与缓存：磁盘缓存、内存缓存、TTL、镜像回退、降级，以及装备图与行内缩略图。
//
// 全部走 httptest 假上游并显式注入时钟：真实网络与真实时间都会让「TTL 到没到」这类判断变成不可复现的。
package arsenal

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeUpstream 是一个假的上游：按路径返回目录、外号表与一张假图片，并统计各路径被请求了多少次。
type fakeUpstream struct {
	server    *httptest.Server
	catalog   atomic.Int32
	aliases   atomic.Int32
	image     atomic.Int32
	failAll   atomic.Bool
	imageBody string
}

// newFakeUpstream 起一个假上游；imageBody 为空时用一小段 PNG 头当图片内容。
func newFakeUpstream(t *testing.T) *fakeUpstream {
	t.Helper()
	f := &fakeUpstream{imageBody: "\x89PNG\r\n\x1a\n假图片内容"}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.failAll.Load() {
			http.Error(w, "上游挂了", http.StatusInternalServerError)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "catalog.json"):
			f.catalog.Add(1)
			_, _ = w.Write([]byte(fixtureJSON))
		case strings.HasSuffix(r.URL.Path, "community-aliases.json"):
			f.aliases.Add(1)
			_, _ = w.Write([]byte(fixtureAliases))
		case strings.Contains(r.URL.Path, "/public/assets/wiki/"):
			f.image.Add(1)
			_, _ = w.Write([]byte(f.imageBody))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

// counterResolver 是 ThumbResolver 的假实现：记录调用次数并返回固定地址。
type counterResolver struct {
	calls atomic.Int32
	url   string
	err   error
}

func (c *counterResolver) FileThumbURL(context.Context, string, int) (string, error) {
	c.calls.Add(1)
	if c.err != nil {
		return "", c.err
	}
	return c.url, nil
}

// fixedNow 返回一个固定的时钟函数。
func fixedNow(at time.Time) func() time.Time { return func() time.Time { return at } }

// newStore 构造一个指向假上游、时钟固定的取数入口。mirror 为空时表示不用镜像。
func newStore(t *testing.T, upstream *fakeUpstream, dir string, now time.Time, mirror string, resolver ThumbResolver) *Store {
	t.Helper()
	return New(Config{
		Dir:          dir,
		TTL:          time.Hour,
		Timeout:      5 * time.Second,
		SourcePrefix: upstream.server.URL + "/",
		MirrorPrefix: mirror,
	}, Deps{Now: fixedNow(now), Logger: func(string, ...any) {}, Thumb: resolver})
}

// TestCatalogDownloadsAndCachesToDisk 校验首次查询会下载并落盘，落盘后第二次调用不再发请求。
func TestCatalogDownloadsAndCachesToDisk(t *testing.T) {
	upstream := newFakeUpstream(t)
	dir := t.TempDir()
	store := newStore(t, upstream, dir, time.Now(), "-", nil)

	catalog, err := store.Catalog(context.Background())
	if err != nil {
		t.Fatalf("首次取目录失败：%v", err)
	}
	if catalog.Len() != 3 {
		t.Fatalf("装备件数错误：%d", catalog.Len())
	}
	if upstream.catalog.Load() != 1 || upstream.aliases.Load() != 1 {
		t.Errorf("首次应各下载一次：catalog=%d aliases=%d", upstream.catalog.Load(), upstream.aliases.Load())
	}
	if _, err := os.Stat(filepath.Join(dir, "catalog.json")); err != nil {
		t.Errorf("目录应落盘：%v", err)
	}

	// 第二次：TTL 内走内存缓存，请求数不变。
	if _, err := store.Catalog(context.Background()); err != nil {
		t.Fatalf("第二次取目录失败：%v", err)
	}
	if upstream.catalog.Load() != 1 {
		t.Errorf("TTL 内不该重复下载，实际 %d 次", upstream.catalog.Load())
	}
}

// TestCatalogUsesFreshDiskCacheWithoutNetwork 校验磁盘缓存新鲜时完全不联网
// （进程重启后第一件事就是查装备，此时不该因为上游慢而卡住）。
func TestCatalogUsesFreshDiskCacheWithoutNetwork(t *testing.T) {
	upstream := newFakeUpstream(t)
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "catalog.json"), []byte(fixtureJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "community-aliases.json"), []byte(fixtureAliases), 0o644); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	store := newStore(t, upstream, dir, now, "-", nil)
	catalog, err := store.Catalog(context.Background())
	if err != nil {
		t.Fatalf("读缓存失败：%v", err)
	}
	if catalog.Len() != 3 {
		t.Fatalf("缓存解析错误：%d 件", catalog.Len())
	}
	if upstream.catalog.Load() != 0 || upstream.aliases.Load() != 0 {
		t.Errorf("缓存新鲜时不该发请求：catalog=%d aliases=%d", upstream.catalog.Load(), upstream.aliases.Load())
	}
	// 缓存文件里的外号也应该被读进来。
	sentry, _ := catalog.Item("a-m-12-mortar-sentry")
	if len(sentry.Aliases) != 2 {
		t.Errorf("外号应从缓存读入：%+v", sentry.Aliases)
	}
}

// TestCatalogRefreshesAfterTTL 校验缓存过期后会重新下载。
func TestCatalogRefreshesAfterTTL(t *testing.T) {
	upstream := newFakeUpstream(t)
	dir := t.TempDir()
	old := time.Now().Add(-48 * time.Hour)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	catalogPath := filepath.Join(dir, "catalog.json")
	if err := os.WriteFile(catalogPath, []byte(`{"items": [{"id": "stale", "nameEn": "Stale"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(catalogPath, old, old); err != nil {
		t.Fatal(err)
	}

	store := newStore(t, upstream, dir, time.Now(), "-", nil)
	catalog, err := store.Catalog(context.Background())
	if err != nil {
		t.Fatalf("过期后刷新失败：%v", err)
	}
	if upstream.catalog.Load() != 1 {
		t.Errorf("缓存过期后应重新下载，实际 %d 次", upstream.catalog.Load())
	}
	if catalog.Len() != 3 || catalog.Items()[0].ID == "stale" {
		t.Errorf("应换成新下载的目录：%+v", catalog.Items())
	}
}

// TestCatalogFallsBackToStaleCache 校验下载失败时用旧缓存兜底：
// 群里能查到「一天前的那份目录」比直接报错有用。
func TestCatalogFallsBackToStaleCache(t *testing.T) {
	upstream := newFakeUpstream(t)
	dir := t.TempDir()
	old := time.Now().Add(-48 * time.Hour)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	catalogPath := filepath.Join(dir, "catalog.json")
	if err := os.WriteFile(catalogPath, []byte(fixtureJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(catalogPath, old, old); err != nil {
		t.Fatal(err)
	}
	upstream.failAll.Store(true)

	store := newStore(t, upstream, dir, time.Now(), "-", nil)
	catalog, err := store.Catalog(context.Background())
	if err != nil {
		t.Fatalf("下载失败时应回退旧缓存，实际报错：%v", err)
	}
	if catalog.Len() != 3 {
		t.Errorf("旧缓存应可用：%d 件", catalog.Len())
	}
}

// TestCatalogFailsWithoutCacheOrNetwork 校验既没有缓存又下载失败时报中文错误。
func TestCatalogFailsWithoutCacheOrNetwork(t *testing.T) {
	upstream := newFakeUpstream(t)
	upstream.failAll.Store(true)
	store := newStore(t, upstream, t.TempDir(), time.Now(), "-", nil)

	_, err := store.Catalog(context.Background())
	if err == nil {
		t.Fatal("既无缓存又下载失败时应报错")
	}
	if !strings.Contains(err.Error(), "下载装备目录失败") {
		t.Errorf("错误文案应说明是目录下载失败，实际 %v", err)
	}
}

// TestCatalogTriesMirrorAfterPrimaryFails 校验主源失败后会试镜像。
func TestCatalogTriesMirrorAfterPrimaryFails(t *testing.T) {
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "主源不可达", http.StatusBadGateway)
	}))
	t.Cleanup(broken.Close)
	upstream := newFakeUpstream(t)

	dir := t.TempDir()
	store := New(Config{
		Dir:          dir,
		TTL:          time.Hour,
		SourcePrefix: broken.URL + "/",
		// 镜像前缀拼在完整上游地址之前，与 gh-proxy 的用法一致。
		MirrorPrefix: upstream.server.URL + "/",
	}, Deps{Now: fixedNow(time.Now()), Logger: func(string, ...any) {}})

	catalog, err := store.Catalog(context.Background())
	if err != nil {
		t.Fatalf("镜像可用时不该失败：%v", err)
	}
	if catalog.Len() != 3 {
		t.Errorf("镜像返回的目录应被采用：%d 件", catalog.Len())
	}
}

// TestCatalogRedownloadsWhenCacheBroken 校验缓存文件坏掉（半截 JSON）时当成没有缓存，重新下载。
func TestCatalogRedownloadsWhenCacheBroken(t *testing.T) {
	upstream := newFakeUpstream(t)
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "catalog.json"), []byte(`{"items": [`), 0o644); err != nil {
		t.Fatal(err)
	}

	store := newStore(t, upstream, dir, time.Now(), "-", nil)
	catalog, err := store.Catalog(context.Background())
	if err != nil {
		t.Fatalf("缓存坏掉时应重新下载：%v", err)
	}
	if catalog.Len() != 3 || upstream.catalog.Load() != 1 {
		t.Errorf("应重新下载：len=%d 请求=%d", catalog.Len(), upstream.catalog.Load())
	}
}

// TestCatalogToleratesBrokenAliasFile 校验外号文件坏掉不影响目录本身：少几条别名，命令照常能用。
func TestCatalogToleratesBrokenAliasFile(t *testing.T) {
	upstream := newFakeUpstream(t)
	upstream.failAll.Store(false)
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "catalog.json"), []byte(fixtureJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "community-aliases.json"), []byte("不是 JSON"), 0o644); err != nil {
		t.Fatal(err)
	}

	store := newStore(t, upstream, dir, time.Now(), "-", nil)
	catalog, err := store.Catalog(context.Background())
	if err != nil {
		t.Fatalf("外号文件坏掉不该影响目录：%v", err)
	}
	sentry, _ := catalog.Item("a-m-12-mortar-sentry")
	if len(sentry.Aliases) != 0 {
		t.Errorf("坏掉的外号表应按空处理：%+v", sentry.Aliases)
	}
}

// TestImageDownloadsAndCaches 校验装备图首次下载、之后读本地缓存，不再请求上游。
func TestImageDownloadsAndCaches(t *testing.T) {
	upstream := newFakeUpstream(t)
	dir := t.TempDir()
	store := newStore(t, upstream, dir, time.Now(), "-", nil)
	catalog := mustCatalog(t)
	weapon, _ := catalog.Item("ar-2-coyote")

	first, err := store.Image(context.Background(), weapon)
	if err != nil {
		t.Fatalf("首次取图失败：%v", err)
	}
	if string(first) != upstream.imageBody {
		t.Errorf("图片内容应与上游一致：%q", first)
	}
	if _, err := os.Stat(filepath.Join(dir, "images", "ar-2-coyote.png")); err != nil {
		t.Errorf("图片应落盘：%v", err)
	}

	second, err := store.Image(context.Background(), weapon)
	if err != nil {
		t.Fatalf("第二次取图失败：%v", err)
	}
	if string(second) != upstream.imageBody {
		t.Errorf("第二次应读到同样的内容")
	}
	if upstream.image.Load() != 1 {
		t.Errorf("命中缓存时不该重复下载，实际 %d 次", upstream.image.Load())
	}
}

// TestImageReportsFailures 校验取图失败的两种情形都返回可读的中文错误（调用方据此渲染无图卡片）。
func TestImageReportsFailures(t *testing.T) {
	upstream := newFakeUpstream(t)
	store := newStore(t, upstream, t.TempDir(), time.Now(), "-", nil)

	// 上游没给配图。
	if _, err := store.Image(context.Background(), Item{ID: "no-image"}); err == nil {
		t.Error("没有配图时应报错")
	}
	// 下载失败。
	upstream.failAll.Store(true)
	_, err := store.Image(context.Background(), Item{ID: "x", ImagePath: "assets/wiki/x.png"})
	if err == nil {
		t.Fatal("下载失败时应报错")
	}
	if !strings.Contains(err.Error(), "下载装备图失败") {
		t.Errorf("错误文案应说明是装备图下载失败，实际 %v", err)
	}
}

// TestFetchOneRejectsOversizedBody 校验超过体积上限的响应被拒绝，而不是被静默截断成坏数据。
func TestFetchOneRejectsOversizedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 100)))
	}))
	t.Cleanup(server.Close)

	store := New(Config{Dir: t.TempDir(), Timeout: time.Second}, Deps{Logger: func(string, ...any) {}})
	if _, err := store.fetchOne(context.Background(), server.URL, 10); err == nil {
		t.Fatal("超过上限的响应应报错")
	}
	if raw, err := store.fetchOne(context.Background(), server.URL, 100); err != nil || len(raw) != 100 {
		t.Errorf("刚好等于上限应放行：len=%d err=%v", len(raw), err)
	}
}

// TestThumbURLForPNGUsesUpstreamAddress 校验 PNG 装备的缩略图直接用上游地址，且优先主源。
func TestThumbURLForPNGUsesUpstreamAddress(t *testing.T) {
	upstream := newFakeUpstream(t)
	store := newStore(t, upstream, t.TempDir(), time.Now(), "-", nil)
	weapon, _ := mustCatalog(t).Item("ar-2-coyote")

	got := store.ThumbURL(context.Background(), weapon)
	want := upstream.server.URL + "/public/assets/wiki/ar-2-coyote.png"
	if got != want {
		t.Errorf("缩略图地址错误：%q，期望 %q", got, want)
	}
}

// TestThumbURLForSVGUsesResolverAndCaches 校验 SVG 装备走解析器换成 PNG，并且结果被缓存
// （行内查询是边打字边触发的，每次都问一次 wiki.gg 会既慢又费）。
func TestThumbURLForSVGUsesResolverAndCaches(t *testing.T) {
	upstream := newFakeUpstream(t)
	dir := t.TempDir()
	resolver := &counterResolver{url: "https://helldivers.wiki.gg/images/thumb/x.svg/240px-x.svg.png"}
	store := newStore(t, upstream, dir, time.Now(), "-", resolver)
	sentry, _ := mustCatalog(t).Item("a-m-12-mortar-sentry")

	first := store.ThumbURL(context.Background(), sentry)
	if first != resolver.url {
		t.Fatalf("SVG 应换成解析器给的 PNG 地址，实际 %q", first)
	}
	if second := store.ThumbURL(context.Background(), sentry); second != first {
		t.Errorf("第二次应命中缓存，实际 %q", second)
	}
	if resolver.calls.Load() != 1 {
		t.Errorf("解析器应只被调用一次，实际 %d 次", resolver.calls.Load())
	}

	// 换个进程（新的 Store）也应从磁盘缓存读到，不再问上游。
	restarted := newStore(t, upstream, dir, time.Now(), "-", &counterResolver{err: errors.New("不该被调用")})
	if got := restarted.ThumbURL(context.Background(), sentry); got != first {
		t.Errorf("重启后应从磁盘缓存读到同一个地址，实际 %q", got)
	}
}

// TestThumbURLWithoutResolverOrFilePage 校验两种「换不了 PNG」的情况都返回空串（这条结果就不带图标）。
func TestThumbURLWithoutResolverOrFilePage(t *testing.T) {
	upstream := newFakeUpstream(t)
	store := newStore(t, upstream, t.TempDir(), time.Now(), "-", nil)

	svgNoResolver := Item{ID: "x", ImagePath: "assets/wiki/x.svg", ImageIsSVG: true,
		ImageFilePage: "https://helldivers.wiki.gg/wiki/File:X.svg"}
	if got := store.ThumbURL(context.Background(), svgNoResolver); got != "" {
		t.Errorf("没有解析器时应返回空串，实际 %q", got)
	}

	withResolver := newStore(t, upstream, t.TempDir(), time.Now(), "-", &counterResolver{url: "https://x"})
	noFilePage := Item{ID: "y", ImagePath: "assets/wiki/y.svg", ImageIsSVG: true}
	if got := withResolver.ThumbURL(context.Background(), noFilePage); got != "" {
		t.Errorf("没有 File: 页时应返回空串，实际 %q", got)
	}

	if got := withResolver.ThumbURL(context.Background(), Item{ID: "z"}); got != "" {
		t.Errorf("没有配图的装备应返回空串，实际 %q", got)
	}
}

// TestThumbURLNonBitmapIsRemembered 校验「上游对 SVG 不开位图缩略图」这个结论只问一次：
// 解析器回的还是 .svg 时，后续查询直接跳过，不再为同一个文件请求 wiki
// （行内查询是边打字边触发的，每次多打一个请求既慢又费；日志里那也是正常情况，不是错误）。
func TestThumbURLNonBitmapIsRemembered(t *testing.T) {
	upstream := newFakeUpstream(t)
	// 站点没开 SVG 渲染：拿回来的还是 .svg 地址。
	resolver := &counterResolver{url: "https://helldivers.wiki.gg/images/HMG_Emplacement_Stratagem_Icon_Background.svg?204f0f"}
	store := newStore(t, upstream, t.TempDir(), time.Now(), "-", resolver)
	item, ok := mustCatalog(t).Item("a-m-12-mortar-sentry")
	if !ok {
		t.Fatal("夹具里应有哨戒炮")
	}

	for i := 1; i <= 3; i++ {
		if got := store.ThumbURL(context.Background(), item); got != "" {
			t.Fatalf("第 %d 次应返回空串（这条结果不带图标），实际 %q", i, got)
		}
	}
	if resolver.calls.Load() != 1 {
		t.Errorf("只该问上游一次，实际 %d 次", resolver.calls.Load())
	}

	// 临时错误不记进「没有位图」名单：下次仍要再问一次，免得一次网络抖动让图标永久消失。
	failing := &counterResolver{err: errors.New("wiki.gg 抽了一下")}
	broken := newStore(t, upstream, t.TempDir(), time.Now(), "-", failing)
	for i := 0; i < 2; i++ {
		if got := broken.ThumbURL(context.Background(), item); got != "" {
			t.Fatalf("解析失败时应返回空串，实际 %q", got)
		}
	}
	if failing.calls.Load() != 2 {
		t.Errorf("临时错误不该被缓存，应问两次，实际 %d 次", failing.calls.Load())
	}
}

// TestThumbURLResolverFailureIsSoft 校验解析失败只记日志、返回空串，不把错误抛给行内查询。
func TestThumbURLResolverFailureIsSoft(t *testing.T) {
	upstream := newFakeUpstream(t)
	resolver := &counterResolver{err: errors.New("wiki.gg 挂了")}
	store := newStore(t, upstream, t.TempDir(), time.Now(), "-", resolver)
	sentry, _ := mustCatalog(t).Item("a-m-12-mortar-sentry")

	if got := store.ThumbURL(context.Background(), sentry); got != "" {
		t.Errorf("解析失败时应返回空串，实际 %q", got)
	}
	if resolver.calls.Load() != 1 {
		t.Errorf("失败也要记一次调用（不做无意义的重复重试），实际 %d 次", resolver.calls.Load())
	}
}

// TestThumbCacheRoundTrip 校验缩略图缓存的序列化：脏条目被丢掉，空缓存输出 `{}`。
func TestThumbCacheRoundTrip(t *testing.T) {
	if got := parseThumbCache([]byte("不是 JSON")); len(got) != 0 {
		t.Errorf("坏 JSON 应按空缓存处理，实际 %+v", got)
	}
	raw := `{"a": "https://x", "b": "", "": "https://y"}`
	got := parseThumbCache([]byte(raw))
	if len(got) != 1 || got["a"] != "https://x" {
		t.Errorf("只应保留键值都非空的条目，实际 %+v", got)
	}
	data, err := marshalThumbCache(nil)
	if err != nil || string(data) != "{}" {
		t.Errorf("空缓存应序列化成 {}：%s / %v", data, err)
	}
}

// TestWriteFileAtomicLeavesNoTempFile 校验原子写不留临时文件（.tmp 残留会让人误以为缓存有两份）。
func TestWriteFileAtomicLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "x.json")
	if err := writeFileAtomic(path, []byte("{}")); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("不该留下临时文件：%v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("目标文件应存在：%v", err)
	}
}

// TestImageFilePathUsesUpstreamExtension 校验图片缓存文件名沿用上游扩展名、缺扩展名时兜底 .png。
func TestImageFilePathUsesUpstreamExtension(t *testing.T) {
	store := New(Config{Dir: "d"}, Deps{})
	if got := store.imageFilePath(Item{ID: "a", ImagePath: "assets/wiki/a.SVG"}); got != filepath.Join("d", "images", "a.svg") {
		t.Errorf("扩展名应小写保留：%q", got)
	}
	if got := store.imageFilePath(Item{ID: "b"}); got != filepath.Join("d", "images", "b.png") {
		t.Errorf("缺扩展名时兜底 png：%q", got)
	}
	_ = fmt.Sprint() // 保留 fmt 依赖，便于将来加断言
}
