// assets_test.go 覆盖素材内联（data URI）、缓存与「素材表 ↔ 内置文件」一致性校验。
package render

import (
	"bytes"
	"encoding/base64"
	"html/template"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

func TestFactionEmblemColors(t *testing.T) {
	for name, color := range map[string]string{
		"emblem.super_earth": "#5BA3D0", "emblem.automaton": "#E74C3C",
		"emblem.terminids": "#F5C518", "emblem.illuminate": "#CF64F8",
	} {
		if !strings.Contains(string(iconFunc(name, "card__emblem")), "fill:"+color) {
			t.Errorf("%s missing faction color %s", name, color)
		}
	}
}

// TestAssetFuncReturnsDataURI 校验模板里的 asset 函数输出 data URI 且命中缓存。
// 素材既有位图也有矢量（emblem.automaton 是游戏原生 SVG），所以只断言「是 data URI」。
func TestAssetFuncReturnsDataURI(t *testing.T) {
	uri := assetDataURI("emblem.automaton")
	if !strings.HasPrefix(uri, svgDataURIPrefix) {
		t.Fatalf("应输出 SVG 的 data URI，实际 %q", uri)
	}
	if png := assetDataURI("biome.magma_base"); !strings.HasPrefix(png, pngDataURIPrefix) {
		t.Fatalf("位图应输出 PNG 的 data URI，实际 %q", png)
	}
	if assetDataURI("emblem.automaton") != uri {
		t.Fatal("同一资源应命中缓存")
	}
	if assetDataURI("不存在") != "" {
		t.Fatal("未知资源应返回空串而不是 panic")
	}
}

// TestAssetDataURIMatchesEmbeddedFile 校验 data URI 就是内置文件的 base64，而不是别的图。
// 位图与矢量各验一次：两者的前缀不同，前缀写错在模板里会表现为图片不显示。
func TestAssetDataURIMatchesEmbeddedFile(t *testing.T) {
	cases := []struct {
		name   string
		prefix string
		png    bool
	}{
		{"biome.magma_base", pngDataURIPrefix, true},
		{"emblem.terminids", svgDataURIPrefix, false},
	}
	for _, tc := range cases {
		raw, err := fs.ReadFile(assetFS, assetFiles[tc.name])
		if err != nil {
			t.Fatalf("内置素材 %s 应可读：%v", tc.name, err)
		}
		if tc.png && !bytes.HasPrefix(raw, pngMagic) {
			t.Fatalf("%s 应是 PNG", assetFiles[tc.name])
		}
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(assetDataURI(tc.name), tc.prefix))
		if err != nil {
			t.Fatalf("data URI 里的 base64 应可解码：%v", err)
		}
		if !bytes.Equal(decoded, raw) {
			t.Fatalf("data URI 内容应与 %s 完全一致", assetFiles[tc.name])
		}
	}
}

// TestAssetFuncIsTrustedURLInTemplate 校验模板里引用的素材不会被 html/template 拦成 #ZgotmplZ。
// 这是 “data:“ URL 的坑：html/template 只放行 http/https/mailto，字符串形式的 data URI
// 会被替换成 #ZgotmplZ，所以 asset 函数必须返回 template.URL。
func TestAssetFuncIsTrustedURLInTemplate(t *testing.T) {
	tmpl := template.Must(template.New("t").Funcs(templateFuncs()).
		Parse(`<img src="{{asset "emblem.super_earth"}}"><img src="{{datauri "emblem.super_earth"}}"><img src="{{asset "不存在"}}">`))

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, nil); err != nil {
		t.Fatalf("模板执行应成功，实际 %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "#ZgotmplZ") {
		t.Fatalf("素材不能被替换成 #ZgotmplZ，实际输出：%s", out)
	}
	if got := strings.Count(out, `src="data:image/`); got != 2 {
		t.Fatalf("asset 与 datauri 都应输出 data URI，实际命中 %d 次：%s", got, out)
	}
	if !strings.Contains(out, `src=""`) {
		t.Fatalf("未知素材应输出空 src，实际：%s", out)
	}
}

// TestAssetLogicalNamesCoverCardNeeds 校验卡片要用的逻辑名都在素材表里，
// 名字写错在渲染时只会表现为「图片不显示」，所以在这里钉住。
func TestAssetLogicalNamesCoverCardNeeds(t *testing.T) {
	for _, name := range []string{
		"emblem.super_earth", "emblem.automaton", "emblem.terminids", "emblem.illuminate",
		"emblem.defense", "emblem.major_order", "emblem.dss",
		"event.super_earth_flag", "tactical.eagle_storm", "tactical.orbital_napalm",
		"arrow.up", "arrow.down", "arrow.left", "arrow.right",
		"ui.reinforce", fontAssetName,
	} {
		if assetDataURI(name) == "" {
			t.Errorf("逻辑名 %s 应能在素材表里找到对应文件", name)
		}
	}
}

// TestMustValidateAssetsAcceptsRealAssets 校验真实素材表与内置文件一一对应（init 也跑同样的校验）。
func TestMustValidateAssetsAcceptsRealAssets(t *testing.T) {
	if got := panicText(func() { mustValidateAssets(assetFS, assetFiles) }); got != "" {
		t.Fatalf("真实素材表应通过校验，实际 panic：%s", got)
	}
}

// TestMustValidateAssetsFailFast 校验素材表与内置文件不一致时在加载阶段就 panic。
func TestMustValidateAssetsFailFast(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		table map[string]string
		want  string
	}{
		{
			name:  "素材表为空",
			files: map[string]string{"assets/emblems/a.png": "png"},
			table: map[string]string{},
			want:  "素材表为空",
		},
		{
			name:  "逻辑名指向不存在的文件",
			files: map[string]string{"assets/emblems/a.png": "png"},
			table: map[string]string{"emblem.a": "assets/emblems/没这个文件.png"},
			want:  "无法从内置文件系统读出",
		},
		{
			name:  "内置文件没有登记逻辑名",
			files: map[string]string{"assets/emblems/a.png": "png", "assets/emblems/b.png": "png"},
			table: map[string]string{"emblem.a": "assets/emblems/a.png"},
			want:  "没有登记逻辑名",
		},
		{
			name: "一层目录下的素材没有登记逻辑名",
			// 旧实现只扫 assets/*/*，这个文件会被静默漏掉，卡片上就少一张图
			files: map[string]string{"assets/emblems/a.png": "png", "assets/orphan.png": "png"},
			table: map[string]string{"emblem.a": "assets/emblems/a.png"},
			want:  "assets/orphan.png",
		},
		{
			name:  "登记了不受支持的格式",
			files: map[string]string{"assets/emblems/a.webp": "webp"},
			table: map[string]string{"emblem.a": "assets/emblems/a.webp"},
			want:  "不是受支持的素材格式",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fsys := fstest.MapFS{}
			for path, content := range tc.files {
				fsys[path] = &fstest.MapFile{Data: []byte(content)}
			}
			got := panicText(func() { mustValidateAssets(fsys, tc.table) })
			if !strings.Contains(got, tc.want) {
				t.Fatalf("panic 内容应包含 %q，实际 %q", tc.want, got)
			}
		})
	}
}

// TestMustValidateAssetsIgnoresNonPNGFiles 校验 assets 下的非素材文件（如许可说明）不算素材，
// 不需要登记逻辑名、也不会因为「没登记」而 panic。
func TestMustValidateAssetsIgnoresNonPNGFiles(t *testing.T) {
	fsys := fstest.MapFS{
		"assets/emblems/a.png": &fstest.MapFile{Data: []byte("png")},
		"assets/LICENSES.md":   &fstest.MapFile{Data: []byte("许可说明")},
	}
	if got := panicText(func() {
		mustValidateAssets(fsys, map[string]string{"emblem.a": "assets/emblems/a.png"})
	}); got != "" {
		t.Fatalf("非素材文件不该被当成素材，实际 panic：%s", got)
	}
}

// TestAssetDataURIUsesCache 校验第二次取同一素材走缓存、不再读文件。
// 做法：第一次取出 data URI 后把素材表里的路径改坏，第二次仍应拿到同样的内容；
// 去掉缓存（每次重新读文件）的实现会因为路径坏掉而返回空串，从而被这个用例抓住。
func TestAssetDataURIUsesCache(t *testing.T) {
	const name = "emblem.defense"
	raw, ok := assetFiles[name]
	if !ok {
		t.Fatalf("前置条件：素材表里应有 %s", name)
	}
	assetCache.Delete(name)
	t.Cleanup(func() {
		assetCache.Delete(name)
		assetFiles[name] = raw
	})

	first := assetDataURI(name)
	if !strings.HasPrefix(first, svgDataURIPrefix) {
		t.Fatalf("首次应编码出 data URI，实际 %q", first)
	}
	cached, ok := assetCache.Load(name)
	if !ok {
		t.Fatalf("首次编码后应写入缓存：%s", name)
	}
	if cached.(string) != first {
		t.Fatal("缓存里的内容应与返回值一致")
	}

	assetFiles[name] = "assets/game/ui/缓存用例故意写坏的路径.svg"
	if got := assetDataURI(name); got != first {
		t.Fatalf("第二次应命中缓存（不再读文件），实际 %q", got)
	}
}

// TestIconFuncInlinesSVG 校验矢量图标走内联 <svg>：带 class、去掉写死的宽高、内部 id 加了前缀。
// 这三条都是「内联才有的坑」：class 挂不上就上不了色、宽高不去掉会把卡片撑爆、
// id 不区分会让同一张卡片上的多个图标互相串（上游的 clipPath id 就是短到只有 "a"）。
func TestIconFuncInlinesSVG(t *testing.T) {
	first := string(iconFunc("emblem.automaton", "card__emblem"))
	second := string(iconFunc("emblem.terminids", "card__emblem"))
	for _, got := range []string{first, second} {
		if !strings.HasPrefix(got, "<svg") {
			t.Fatalf("矢量图标应内联成 <svg>，实际 %q", got[:min(40, len(got))])
		}
		if !strings.Contains(got, `class="card__emblem"`) {
			t.Fatalf("class 应写到根 <svg> 上：%q", got[:min(80, len(got))])
		}
		if strings.Contains(got, `width="1024"`) || strings.Contains(got, `<?xml`) {
			t.Fatalf("应去掉 XML 声明与写死的宽高：%q", got[:min(120, len(got))])
		}
	}
	// 上游 SVG 里的 clipPath id 短到只有 "a"，同一张卡片上内联两个图标就会互相串；
	// 内联时统一加了逻辑名前缀，这里钉住这条规则（超人的旗标 SVG 正好带 clipPath id="a"）。
	flag := string(iconFunc("event.super_earth_flag", "card__emblem"))
	if !strings.Contains(flag, "url(#ic-event-super-earth-flag-a)") {
		t.Fatalf("内部 id 引用应加上逻辑名前缀，实际：%q", flag[:min(200, len(flag))])
	}
	if strings.Contains(flag, `url(#a)`) {
		t.Fatal("不应残留未经改写的 id 引用")
	}
}

// TestIconFuncRendersBitmapAsImg 校验位图仍走 <img>（内联 base64 会白涨体积），class 写在 img 上。
func TestIconFuncRendersBitmapAsImg(t *testing.T) {
	got := string(iconFunc("biome.magma_base", "hero__img"))
	if !strings.HasPrefix(got, `<img class="hero__img" src="data:image/png;base64,`) {
		t.Fatalf("位图应输出带 class 的 <img>，实际 %q", got[:min(60, len(got))])
	}
	if string(iconFunc("不存在")) != "" {
		t.Fatal("未知逻辑名应返回空串（页面少一张图，而不是渲染失败）")
	}
}

// TestFontFaceFuncEmbedsTitleFont 校验标题字体内联成 @font-face（渲染页没有 base URL，外链字体取不到）。
func TestFontFaceFuncEmbedsTitleFont(t *testing.T) {
	got := string(fontFaceFunc())
	if !strings.Contains(got, "@font-face") || !strings.Contains(got, ttfDataURIPrefix) {
		t.Fatalf("应内联 @font-face，实际 %q", got[:min(60, len(got))])
	}
	if !strings.Contains(got, "ZZZ-thick") {
		t.Fatal("字体族名应与 base.tmpl 里的 --font-title 一致")
	}
}
