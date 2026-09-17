package render

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // 注册 JPEG 解码器：render.format 可以是 jpeg，高度判定要能读它的头部
	_ "image/png"  // 注册 PNG 解码器
)

// MaxCardHeightPx 是卡片图片的高度安全线（像素）。
//
// 为什么需要它：卡片正文从 S4 起不再按「两行摘要」截断，长内容会把图片拉高（实测 1800px 宽、
// 满屏中文时约 49px/行）。真实内容离这条线并不远，不能说「随便怎么画都撞不到」：
// 实测最坏情况（推送卡 5 条 × 807 个中文字符，即单条简报的实测最长正文）= 6490px，只剩 7% 余量；
// 评审构造的「2 节 × 4 条、每节都带 385 rune 长标题」更是量到 10188px，直接顶穿。
// 所以它是**终局防线**：正常内容靠上游字段上限（最长正文 807 rune、首行 385 rune）与各插件/推送的
// 字数预算（见 src/push/card.go、src/plugins/orders/card.go）压在安全线内，闸门只负责挡异常增长。
//
// 为什么是 7000：Telegram 对单张图片的尺寸限制比文件大小更早生效，社区口径是「宽 + 高 ≤ 10000」，
// 而卡片宽度固定 1800px，因此高度上限只有 8200px；取 7000 再留一道余量，同时把 PNG 体积压在 3MB 以内。
//
// 超过安全线时**不截断内容**，而是让调用方回退成完整文本（bot.SplitText 会自动分片，一字不少）。
const MaxCardHeightPx = 7000

// tooTallError 把「高度超标」包成一个可辨认的错误，日志里一眼能看出不是渲染崩了。
var errCardTooTall = errors.New("卡片过高")

// checkCardHeight 检查截图结果的高度；超过安全线时返回中文错误。
//
// 放宽的两处（都是刻意的）：
//   - 解不出图片头（例如测试里的假驱动回了非图片字节）时不判定高度：引擎的职责是出图，
//     尺寸判定失败不该把一张能用的图变成错误；
//   - 正好等于 MaxCardHeightPx 不算超标（边界含上界），免得「刚好卡线」的卡片来回跳变。
func checkCardHeight(img []byte) error {
	if len(img) == 0 {
		return nil
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(img))
	if err != nil {
		return nil
	}
	return heightError(cfg.Height)
}

// heightError 是 height -> error 的纯函数，单独拆出来是为了让边界（6999/7000/7001）能被用例钉住。
func heightError(height int) error {
	if height <= MaxCardHeightPx {
		return nil
	}
	return fmt.Errorf("%w：%dpx 超过安全线 %dpx，请改用文本发送（内容不会丢）", errCardTooTall, height, MaxCardHeightPx)
}

// TooTallError 构造一个「卡片超过高度安全线」的错误。
//
// 引擎内部用它；测试与自定义 Renderer 实现（例如在别处判高、或把引擎错误再包一层）也用它，
// 这样 IsCardTooTall 与 Paginate 才认得出「这张图太大」而不是「渲染坏了」。
func TooTallError(height int) error { return heightError(height) }

// IsCardTooTall 判断错误是否来自「卡片超过高度安全线」。
// 供测试与将来的调用方辨认，目前生产代码里没有调用方：所有调用方（各插件与推送编排）本来就对
// 「渲染失败」统一回退完整文本，不需要区分原因；日志里靠错误文案本身区分「图太大」与「渲染坏了」。
func IsCardTooTall(err error) bool { return errors.Is(err, errCardTooTall) }
