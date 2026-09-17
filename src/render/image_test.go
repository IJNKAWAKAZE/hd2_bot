// image_test.go 覆盖运行期图片通道：按内容识别格式、以及模板函数 dataimg 真的能把图放进 src。
//
// 本文件属于包内测试（package render）：只有包内才拿得到 templateFuncs，
// 而「{{dataimg}} 的输出会不会被 html/template 过滤成 #ZgotmplZ」恰恰是这条通道最容易坏的地方——
// 用字符串拼一段假模板去断言是测不出来的（那段模板根本不会被执行）。
package render

import (
	"bytes"
	"encoding/base64"
	"html/template"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

// testPNGBytes 造一张 1×1 的真 PNG，用来验证「按内容识别格式」而不是「按调用方说了算」。
func testPNGBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 0xff, A: 0xff})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("构造测试 PNG 失败：%v", err)
	}
	return buf.Bytes()
}

// TestImageDataURISniffsFormats 校验运行期图片按内容识别格式：PNG / JPEG / SVG 各走各的前缀。
func TestImageDataURISniffsFormats(t *testing.T) {
	cases := []struct {
		name   string
		raw    []byte
		prefix string
	}{
		{"PNG", testPNGBytes(t), "data:image/png;base64,"},
		{"JPEG", []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10}, "data:image/jpeg;base64,"},
		{"SVG 直接以标签开头", []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`), "data:image/svg+xml;base64,"},
		{"SVG 带 XML 声明", []byte("<?xml version=\"1.0\"?>\n<svg xmlns=\"http://www.w3.org/2000/svg\"/>"), "data:image/svg+xml;base64,"},
		{"SVG 带 BOM 与前导空白", append([]byte{0xef, 0xbb, 0xbf}, []byte("\n  <svg/>")...), "data:image/svg+xml;base64,"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ImageDataURI(c.raw)
			if !strings.HasPrefix(got, c.prefix) {
				t.Fatalf("data URI 前缀错误：期望 %q 开头，实际 %q", c.prefix, got)
			}
			if want := base64.StdEncoding.EncodeToString(c.raw); !strings.HasSuffix(got, want) {
				t.Error("data URI 应内联原始字节的 base64")
			}
		})
	}
}

// TestImageDataURIRejectsUnknown 校验认不出的内容返回空串：
// 卡片上少一张图，好过渲染出一张破图或带错 MIME 的 data URI。
func TestImageDataURIRejectsUnknown(t *testing.T) {
	cases := map[string][]byte{
		"空字节":            nil,
		"纯文本":            []byte("不是图片"),
		"HTML":           []byte("<html><body>x</body></html>"),
		"半个 PNG":         {0x89, 'P', 'N', 'G'},
		"自称 XML 但不是 SVG": []byte("<?xml version=\"1.0\"?><rss/>"),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if got := ImageDataURI(raw); got != "" {
				t.Errorf("认不出格式时应返回空串，实际 %q", got)
			}
		})
	}
}

// TestDataImgTemplateFuncEmitsDataURI 校验模板函数 dataimg 能把 data URI 原样写进 src：
// html/template 默认会把 data: 协议过滤成 #ZgotmplZ，这条用例盯的就是「图片真的能显示」。
func TestDataImgTemplateFuncEmitsDataURI(t *testing.T) {
	uri := ImageDataURI(testPNGBytes(t))
	if uri == "" {
		t.Fatal("测试前置失败：PNG 应能转成 data URI")
	}
	tmpl := template.Must(template.New("t").Funcs(templateFuncs()).
		Parse(`<img class="icon" src="{{dataimg .URI}}">`))

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, struct{ URI string }{URI: uri}); err != nil {
		t.Fatalf("执行模板失败：%v", err)
	}
	out := buf.String()
	if strings.Contains(out, "ZgotmplZ") {
		t.Fatal("data URI 被 html/template 过滤成了 #ZgotmplZ，图片会显示不出来")
	}
	if !strings.Contains(out, uri) {
		t.Errorf("模板输出应含完整 data URI，实际 %q", out)
	}
}

// TestDataImgTemplateFuncKeepsEmpty 校验空 data URI 原样透传（模板据此不渲染这张图），不凭空拼前缀。
func TestDataImgTemplateFuncKeepsEmpty(t *testing.T) {
	tmpl := template.Must(template.New("t").Funcs(templateFuncs()).Parse(`<img src="{{dataimg .URI}}">`))

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, struct{ URI string }{}); err != nil {
		t.Fatalf("执行模板失败：%v", err)
	}
	if got := buf.String(); got != `<img src="">` {
		t.Errorf("空 data URI 应原样输出成空 src，实际 %q", got)
	}
}

// testSolidPNG 造一张 w×h 的纯色真 PNG，用于验证缩放结果。
func testSolidPNG(t *testing.T, w, h int, c color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("构造测试 PNG 失败：%v", err)
	}
	return buf.Bytes()
}

// decodeDataURIImage 解出 data URI 里的图片，便于断言缩放后的尺寸与颜色。
func decodeDataURIImage(t *testing.T, uri string) image.Image {
	t.Helper()
	const prefix = "data:image/png;base64,"
	if !strings.HasPrefix(uri, prefix) {
		t.Fatalf("期望 PNG 的 data URI，实际是 %d 字节的 %q", len(uri), uri)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, prefix))
	if err != nil {
		t.Fatalf("解 base64 失败：%v", err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("解 PNG 失败：%v", err)
	}
	return img
}

// TestThumbDataURIScalesDownBitmap 校验超过目标宽度的位图会被等比缩小并重编码成 PNG。
func TestThumbDataURIScalesDownBitmap(t *testing.T) {
	raw := testSolidPNG(t, 12, 6, color.NRGBA{R: 0x20, G: 0x80, B: 0xC0, A: 0xFF})
	uri := ThumbDataURI(raw, 4)
	if uri == "" {
		t.Fatal("位图应能转成 data URI")
	}
	img := decodeDataURIImage(t, uri)
	if got := img.Bounds().Dx(); got != 4 {
		t.Errorf("宽度应缩到 4，实际 %d", got)
	}
	if got := img.Bounds().Dy(); got != 2 {
		t.Errorf("高度应按比例缩到 2，实际 %d", got)
	}
	got := color.NRGBAModel.Convert(img.At(1, 1)).(color.NRGBA)
	if got.R != 0x20 || got.G != 0x80 || got.B != 0xC0 || got.A != 0xFF {
		t.Errorf("纯色图缩放后颜色不该变，实际 %+v", got)
	}
}

// TestThumbDataURIKeepsSmallBitmap 校验本来就比目标宽度小的图原样内联（不重编码，省 CPU 也不掉画质）。
func TestThumbDataURIKeepsSmallBitmap(t *testing.T) {
	raw := testSolidPNG(t, 4, 4, color.NRGBA{R: 0x10, A: 0xFF})
	if got := ThumbDataURI(raw, 64); got != ImageDataURI(raw) {
		t.Error("小图应原样内联，不做重编码")
	}
}

// TestThumbDataURIPassesSVGThrough 校验 SVG 不做缩放：标准库没有 SVG 光栅化器，
// 原样内联交给浏览器缩放，反而更小更清晰。
func TestThumbDataURIPassesSVGThrough(t *testing.T) {
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="240" height="240"></svg>`)
	if got := ThumbDataURI(svg, 32); got != ImageDataURI(svg) {
		t.Error("SVG 应原样内联")
	}
}

// TestThumbDataURIFallsBack 校验认不出的字节与非正数宽度都不会把图弄丢或崩溃。
func TestThumbDataURIFallsBack(t *testing.T) {
	if got := ThumbDataURI([]byte("不是图片"), 32); got != "" {
		t.Errorf("认不出的内容应返回空串，实际 %q", got)
	}
	raw := testSolidPNG(t, 12, 6, color.NRGBA{A: 0xFF})
	if got := ThumbDataURI(raw, 0); got != ImageDataURI(raw) {
		t.Error("maxWidth <= 0 时应原样内联")
	}
	if got := ThumbDataURI(nil, 32); got != "" {
		t.Errorf("空字节应返回空串，实际 %q", got)
	}
}

// TestBoxRange 校验目标像素对应的源区间：永远是有效区间，且不会越界。
func TestBoxRange(t *testing.T) {
	cases := []struct {
		min, size, i, total int
		wantFrom, wantTo    int
	}{
		{0, 12, 0, 4, 0, 3},
		{0, 12, 3, 4, 9, 12},
		{5, 4, 0, 8, 5, 6}, // 目标比源大（放大）时也要给出至少一个像素
		{5, 1, 0, 8, 5, 6},
	}
	for _, c := range cases {
		from, to := boxRange(c.min, c.size, c.i, c.total)
		if from != c.wantFrom || to != c.wantTo {
			t.Errorf("boxRange(%d,%d,%d,%d) = [%d,%d)，期望 [%d,%d)",
				c.min, c.size, c.i, c.total, from, to, c.wantFrom, c.wantTo)
		}
		if to <= from {
			t.Errorf("区间必须非空：[%d,%d)", from, to)
		}
		if from < c.min || to > c.min+c.size {
			t.Errorf("区间越界：[%d,%d)，源区间是 [%d,%d)", from, to, c.min, c.min+c.size)
		}
	}
}
