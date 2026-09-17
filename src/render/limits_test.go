package render

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/png"
	"strings"
	"testing"
)

// tallPNG 生成一张指定高度、可被 image.DecodeConfig 判读的最小 PNG。
func tallPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, width, height))); err != nil {
		t.Fatalf("生成测试 PNG 失败：%v", err)
	}
	return buf.Bytes()
}

// TestMaxCardHeightPxIsSevenThousand 把「安全线取 7000」这个决定钉住：
// 它不是随手挑的数（设计文档 §2 有实测依据），改小会误伤真实卡片，改大会碰 Telegram 的尺寸上限。
func TestMaxCardHeightPxIsSevenThousand(t *testing.T) {
	if MaxCardHeightPx != 7000 {
		t.Fatalf("高度安全线应为 7000px（设计 §2：1800px 宽的卡片在 Telegram 的宽+高 ≤ 10000 之内留三成余量），实际 %d", MaxCardHeightPx)
	}
}

// TestHeightErrorBoundary 钉住边界语义：等于安全线算通过，超一像素就报错，错误里要能读出实际高度。
func TestHeightErrorBoundary(t *testing.T) {
	cases := []struct {
		name    string
		height  int
		wantErr bool
	}{
		{"很小的图", 120, false},
		{"安全线下一像素", MaxCardHeightPx - 1, false},
		{"正好等于安全线", MaxCardHeightPx, false},
		{"安全线上一个像素", MaxCardHeightPx + 1, true},
		{"远超安全线", 9506, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := heightError(c.height)
			if c.wantErr && err == nil {
				t.Fatalf("高度 %d 应当判为超标", c.height)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("高度 %d 不该判为超标：%v", c.height, err)
			}
			if err == nil {
				return
			}
			if !IsCardTooTall(err) {
				t.Fatalf("错误应能被 IsCardTooTall 认出：%v", err)
			}
			for _, want := range []string{"卡片过高", "改用文本发送"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("错误文案缺少 %q：%v", want, err)
				}
			}
			if !strings.Contains(err.Error(), "px") {
				t.Fatalf("错误文案应带上高度与单位 px：%v", err)
			}
		})
	}
}

// TestCheckCardHeightTreatsUndecodableImageAsPass 说明「判定失败不影响出图」：
// 引擎的职责是出图，读不出尺寸（假驱动、异常字节）时不拦。
func TestCheckCardHeightTreatsUndecodableImageAsPass(t *testing.T) {
	if err := checkCardHeight(fakeImage); err != nil {
		t.Fatalf("非图片字节不该被当成超标：%v", err)
	}
	if err := checkCardHeight(nil); err != nil {
		t.Fatalf("空图不该被当成超标：%v", err)
	}
}

// TestCheckCardHeightReadsRealPNGHeader 用真 PNG 头验证高度确实被读出来了。
func TestCheckCardHeightReadsRealPNGHeader(t *testing.T) {
	if err := checkCardHeight(tallPNG(t, 4, MaxCardHeightPx)); err != nil {
		t.Fatalf("正好安全线的图不该报错：%v", err)
	}
	err := checkCardHeight(tallPNG(t, 4, MaxCardHeightPx+12))
	if err == nil {
		t.Fatal("超过安全线的图应当报错")
	}
	if !strings.Contains(err.Error(), "7012") {
		t.Fatalf("错误里应写出实际高度 7012：%v", err)
	}
}

// TestRenderRejectsTooTallCardWithoutRetry 是这条闸门的端到端用例：
// 驱动回一张超高的真 PNG → Render 报「卡片过高」→ 且没有把它当成掉线去重建驱动（只截了一次）。
func TestRenderRejectsTooTallCardWithoutRetry(t *testing.T) {
	drv := newFakeDriver()
	drv.screenshotFn = func(context.Context, string) ([]byte, error) {
		return tallPNG(t, 4, MaxCardHeightPx+100), nil
	}
	installFakeFactory(t, drv)
	eng := mustNewEngine(t, Config{})

	_, err := eng.Render(context.Background(), newSmokeCard())
	if err == nil {
		t.Fatal("超高卡片应当返回错误")
	}
	if !IsCardTooTall(err) {
		t.Fatalf("错误应可辨认成「卡片过高」：%v", err)
	}
	if got := drv.shotCount(); got != 1 {
		t.Fatalf("不该重试截图，实际截了 %d 次", got)
	}
}

// TestRenderAcceptsCardAtHeightLimit 是上一条的反面：正好卡线仍然出图。
func TestRenderAcceptsCardAtHeightLimit(t *testing.T) {
	drv := newFakeDriver()
	drv.screenshotFn = func(context.Context, string) ([]byte, error) {
		return tallPNG(t, 4, MaxCardHeightPx), nil
	}
	installFakeFactory(t, drv)
	eng := mustNewEngine(t, Config{})

	img, err := eng.Render(context.Background(), newSmokeCard())
	if err != nil {
		t.Fatalf("正好安全线的卡片应当正常出图：%v", err)
	}
	if len(img) == 0 {
		t.Fatal("应当返回图片字节")
	}
}

// tallJPEGBase64 是一张 1×7101 的灰度 JPEG（1040 字节）的 base64，评审缺陷 D2 的夹具。
// 为什么把它内嵌进来而不是现场生成：这个用例要验证**生产代码里那行 `_ "image/jpeg"` 注册**还在。
// 一旦在测试文件里 import image/jpeg，解码器就会被顺带注册，把那行删掉用例照样绿（假防线），
// 所以这里只能喂一段预先编码好的字节。
const tallJPEGBase64 = "/9j/2wCEABQODxIPDRQSEBIXFRQYHjIhHhwcHj0sLiQySUBMS0dARkVQWnNiUFVtVkVGZIhlbXd7gYKBTmCNl4x9lnN+gXwBFRcXHhoeOyEhO3xTRlN8fHx8fHx8fHx8fHx8fHx8fHx8fHx8fHx8fHx8fHx8fHx8fHx8fHx8fHx8fHx8fHx8fP/AAAsIG70AAQEBEQD/xADSAAABBQEBAQEBAQAAAAAAAAAAAQIDBAUGBwgJCgsQAAIBAwMCBAMFBQQEAAABfQECAwAEEQUSITFBBhNRYQcicRQygZGhCCNCscEVUtHwJDNicoIJChYXGBkaJSYnKCkqNDU2Nzg5OkNERUZHSElKU1RVVldYWVpjZGVmZ2hpanN0dXZ3eHl6g4SFhoeIiYqSk5SVlpeYmZqio6Slpqeoqaqys7S1tre4ubrCw8TFxsfIycrS09TV1tfY2drh4uPk5ebn6Onq8fLz9PX29/j5+v/aAAgBAQAAPwDjKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKK/9k="

// TestCheckCardHeightReadsRealJPEGHeader 是 JPEG 格式的闸门用例。
//
// 背景（评审 D2）：删掉 limits.go 里 `_ "image/jpeg"` 那行后，原有 4 个包 156 个用例全绿——
// 因为所有夹具都是 PNG，走的全是 png 解码器。而 hd2.yaml 的 render.format 明确支持 jpeg
// （config.go 里有校验），一旦配置成 jpeg，7100px 的图就会完全绕过闸门。这条用例把它钉住。
func TestCheckCardHeightReadsRealJPEGHeader(t *testing.T) {
	img, err := base64.StdEncoding.DecodeString(tallJPEGBase64)
	if err != nil {
		t.Fatalf("夹具 base64 解不开：%v", err)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(img))
	if err != nil {
		t.Fatalf("夹具不是可判读的图片（说明 JPEG 解码器没注册）：%v", err)
	}
	if format != "jpeg" || cfg.Height != MaxCardHeightPx+101 {
		t.Fatalf("夹具期望 1×%d 的 jpeg，实际 %s %d×%d", MaxCardHeightPx+101, format, cfg.Width, cfg.Height)
	}
	err = checkCardHeight(img)
	if err == nil {
		t.Fatal("7101px 的 JPEG 应当被判为超标；判不出来说明生产代码少了 image/jpeg 注册")
	}
	if !IsCardTooTall(err) {
		t.Fatalf("错误应可辨认成「卡片过高」：%v", err)
	}
	if !strings.Contains(err.Error(), "7101") {
		t.Fatalf("错误里应写出实际高度 7101：%v", err)
	}
}
