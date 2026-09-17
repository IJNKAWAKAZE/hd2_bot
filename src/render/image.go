// 运行期图片通道：把「不在二进制里、运行时才拿到的图」送进卡片。
//
// 为什么需要它：render/assets.go 那套 assetFiles 逻辑名只覆盖编译进二进制的素材（徽标、群系图等），
// 而装备图、敌人图是命令执行时按需下载到本地缓存的——数量太多（几百张）不适合塞进二进制，
// 内容也会随上游更新变化。这类图由插件下载后交给 ImageDataURI 转成 data URI，
// 卡片模板用 {{dataimg ...}} 塞进 <img src>。
//
// 安全边界：返回值一律是 data URI，卡片 HTML 不会因此产生任何外部请求（截图时不依赖网络，
// 也不会把上游地址暴露给渲染进程）；认不出格式时返回空串，模板据此不挂图。
package render

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"math"
)

// 图片格式的 data URI 前缀。只认这三种：卡片用到的图片来源固定（内置素材与上游目录都是 PNG/SVG，
// 渲染输出格式另说），多认几种格式只会让「认错了」变得更难发现。
const (
	imgPNGURI  = "data:image/png;base64,"
	imgJPEGURI = "data:image/jpeg;base64,"
	imgSVGURI  = "data:image/svg+xml;base64,"
)

// 各格式的魔数/开头标记。
var (
	imgPNGMagic   = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	imgJPEGMagic  = []byte{0xff, 0xd8, 0xff}
	imgSVGOpenTag = []byte("<svg")
	imgXMLDecl    = []byte("<?xml")
)

// ImageDataURI 把运行期拿到的图片字节转成 data URI；空字节或认不出的格式返回空串。
//
// SVG 也支持：Chromium 能直接渲染 <img src="data:image/svg+xml;base64,...">，
// 而上游的战备图标有一批就是 SVG（实测 298 件装备里 58 件）。
// 注意 SVG 只在**卡片**里能用：Telegram 的行内缩略图不接受 SVG，那条路要另想办法（见 arsenal 包）。
func ImageDataURI(raw []byte) string {
	switch detectImageMime(raw) {
	case "png":
		return imgPNGURI + base64.StdEncoding.EncodeToString(raw)
	case "jpeg":
		return imgJPEGURI + base64.StdEncoding.EncodeToString(raw)
	case "svg":
		return imgSVGURI + base64.StdEncoding.EncodeToString(raw)
	default:
		return ""
	}
}

// detectImageMime 按内容判断图片类型，返回 "png" / "jpeg" / "svg"，认不出返回空串。
//
// 只看开头几个字节，不做完整解码：几百 KB 的图每次渲染都全量解码没有意义，
// 而「是不是图」这个判断错了的后果也只是卡片上少一张图（下面还有 <img> 的兜底）。
func detectImageMime(raw []byte) string {
	switch {
	case bytes.HasPrefix(raw, imgPNGMagic):
		return "png"
	case bytes.HasPrefix(raw, imgJPEGMagic):
		return "jpeg"
	}
	// SVG 是文本，开头可能是 XML 声明、注释或直接的 <svg>，前面还可能有 UTF-8 BOM 与空白。
	head := bytes.TrimLeft(bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf}), " \t\r\n")
	if bytes.HasPrefix(bytes.ToLower(head), imgSVGOpenTag) || bytes.HasPrefix(bytes.ToLower(head), imgXMLDecl) {
		if bytes.Contains(head, imgSVGOpenTag) {
			return "svg"
		}
	}
	return ""
}

// ThumbDataURI 把图片字节转成适合内联进卡片的 data URI：位图先等比缩到 maxWidth 以内再编码成 PNG。
//
// 为什么要缩：卡片的缩略图位置只有几十 CSS px，而上游的装备图都是几百 px 的原图
// （实测单张 33~138KB）。一张卡片可能挂十几张图，原图内联会把 HTML 撑到几 MB，
// 截图出来的 PNG 也跟着变大——而 Telegram 对图片体积是有上限的。
//
// 三种情况不做缩放，直接内联原图：
//   - maxWidth <= 0（调用方明确不要缩放）；
//   - SVG：标准库没有 SVG 光栅化器，原样内联交给浏览器缩放，反而更小更清晰；
//   - 解码失败或已经是小图：宁可原样内联，也不要把一张能显示的图变成不显示。
func ThumbDataURI(raw []byte, maxWidth int) string {
	if len(raw) == 0 || maxWidth <= 0 || detectImageMime(raw) == "svg" {
		return ImageDataURI(raw)
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return ImageDataURI(raw)
	}
	if img.Bounds().Dx() <= maxWidth {
		return ImageDataURI(raw)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, scaleToWidth(img, maxWidth)); err != nil {
		return ImageDataURI(raw)
	}
	return ImageDataURI(buf.Bytes())
}

// scaleToWidth 用区域平均（box filter）把图片等比缩到指定宽度。
//
// 为什么不是最近邻：装备图与图标线条细，最近邻缩到三分之一会有明显锯齿。
// 为什么按「非预乘」通道平均：图标多数带透明背景，RGBA() 返回的是预乘值，
// 直接平均会让半透明边缘发黑（透明像素的预乘值是 0）。
func scaleToWidth(src image.Image, width int) *image.NRGBA {
	bounds := src.Bounds()
	srcW, srcH := bounds.Dx(), bounds.Dy()
	if width <= 0 || srcW <= 0 || srcH <= 0 {
		return image.NewNRGBA(image.Rect(0, 0, 1, 1))
	}
	height := int(math.Round(float64(srcH) * float64(width) / float64(srcW)))
	if height < 1 {
		height = 1
	}
	dst := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		// 目标像素覆盖的源区间：至少一个像素宽，避免目标比源大时切出空区间。
		y0, y1 := boxRange(bounds.Min.Y, srcH, y, height)
		for x := 0; x < width; x++ {
			x0, x1 := boxRange(bounds.Min.X, srcW, x, width)
			var sumR, sumG, sumB, sumA, count uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					c := color.NRGBAModel.Convert(src.At(sx, sy)).(color.NRGBA)
					sumR += uint64(c.R)
					sumG += uint64(c.G)
					sumB += uint64(c.B)
					sumA += uint64(c.A)
					count++
				}
			}
			if count == 0 {
				continue
			}
			dst.SetNRGBA(x, y, color.NRGBA{
				R: uint8(sumR / count),
				G: uint8(sumG / count),
				B: uint8(sumB / count),
				A: uint8(sumA / count),
			})
		}
	}
	return dst
}

// boxRange 返回目标第 i 个像素（共 total 个）对应的源区间 [from, to)，区间一定非空。
func boxRange(min, size, i, total int) (int, int) {
	from := min + i*size/total
	to := min + (i+1)*size/total
	if to <= from {
		to = from + 1
		if to > min+size {
			to = min + size
			from = to - 1
		}
	}
	return from, to
}
