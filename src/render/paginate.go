// 卡片分页：一张图装不下时，先试着把条目拆到多页，只有连分页也装不下才回退文本。
//
// 为什么需要它：S4 取消了卡片内的正文截断，改用「超过高度安全线 MaxCardHeightPx 就改发完整文本」兜底。
// 文本虽然一字不少，但用户要的是图。分页补上中间那一档：先按更少的条目重排成 2~3 页图，
// 把「退文本」留给分页也救不回来的极端情况（例如上游给出异常长的单条内容）。
//
// 分页的代价是渲染次数：每次试一个页数都要把该页全部渲染一遍，所以页数从 1 开始逐级试、
// 且页数上限很低（MaxPaginationPages）。正常内容第一页就过，只多花一次「本来也要做」的渲染。

package render

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// MaxPaginationPages 是分页的页数上限：再多就说明内容本身异常（或者卡片设计已经不适合塞进图里），
// 直接回退文本更合适，也免得一次推送往群里刷十几张图。
const MaxPaginationPages = 3

// PageBuilder 为「第 page 页（共 pages 页）」构造卡片，条目区间是 [from, to)、总条目数为 total。
//
// 页码与区间都由分页器给定，调用方只管把它们写进卡片：条目怎么切由分页器决定，
// 页码文案用 PageLabel / WithPageNote。
type PageBuilder func(page, pages, from, to, total int) Card

// Paginate 把 total 个条目渲染成 1..MaxPaginationPages 张图片。
//
// 返回值：
//   - 图片切片：非空，长度等于实际页数（正常内容只有 1 张）；
//   - 错误：渲染真的坏了（非高度问题）→ 立即上抛；连 MaxPaginationPages 页都装不下 →
//     返回可被 IsCardTooTall 辨认的错误，调用方据此回退完整文本。
//
// 页数上限优先于条目数：条目比页数还少时按条目数分（不会出现空页，也不会出现「共 3 页」却只发 2 张）。
func Paginate(ctx context.Context, r Renderer, total int, build PageBuilder) ([][]byte, error) {
	if r == nil {
		return nil, errors.New("渲染引擎为空")
	}
	pages := 1
	if total > 1 {
		pages = minInt(MaxPaginationPages, total)
	}
	for n := 1; n <= pages; n++ {
		imgs, tooTall, err := renderPaged(ctx, r, total, n, build)
		if err != nil {
			return nil, err
		}
		if !tooTall {
			return imgs, nil
		}
	}
	return nil, fmt.Errorf("%w：分页到 %d 页仍超过安全线 %dpx，请改用文本发送（内容不会丢）",
		errCardTooTall, pages, MaxCardHeightPx)
}

// renderPaged 按 pages 页渲染一轮：返回图片、是否「有页面超过安全线」、以及真实错误。
//
// 「超过安全线」不算错误：它只是告诉我们条目要再切细一点，由调用方加页重试。
func renderPaged(ctx context.Context, r Renderer, total, pages int, build PageBuilder) ([][]byte, bool, error) {
	per := (total + pages - 1) / pages
	if per < 1 {
		per = 1
	}
	// 实际页数由条目数与每页条数算出来：total 为 0（占位卡）时也要渲染一页，
	// 不能因为「没有条目」就一张图都不出。
	count := 1
	if total > 0 {
		count = (total + per - 1) / per
	}
	imgs := make([][]byte, 0, count)
	for page := 1; page <= count; page++ {
		from := (page - 1) * per
		to := minInt(from+per, total)
		img, err := r.Render(ctx, build(page, pages, from, to, total))
		if err != nil {
			if IsCardTooTall(err) {
				return nil, true, nil
			}
			return nil, false, err
		}
		imgs = append(imgs, img)
	}
	return imgs, false, nil
}

// PageLabel 是分页卡片的页码文案；只有真的分了多页才需要显示，单页返回空串。
func PageLabel(page, pages int) string {
	if pages <= 1 {
		return ""
	}
	return fmt.Sprintf("第 %d/%d 页", page, pages)
}

// WithPageNote 把页码并进卡片底部的说明：说明为空时只有页码，已有说明时用「；」接在后面，
// 单页时原样返回（单页卡片不该多出一行没人看得懂的页码）。
func WithPageNote(note string, page, pages int) string {
	label := PageLabel(page, pages)
	if label == "" {
		return note
	}
	if strings.TrimSpace(note) == "" {
		return label
	}
	return note + "；" + label
}

// minInt 取较小值；本包只为分页页数用一次，不引入排序/数学依赖。
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
