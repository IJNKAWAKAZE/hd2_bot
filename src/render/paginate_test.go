package render

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

// pageCall 记录一次「第几页、共几页、条目区间」的构造请求。
type pageCall struct {
	page, pages, from, to, total int
}

// scriptRenderer 是 Paginate 的假渲染器：按「这一页装了几个条目」算高度，超过安全线就回「卡片过高」。
//
// 为什么用高度模型而不是「第 N 次调用返回超高」：分页器的行为取决于每一页实际装了多少条目，
// 按调用序号写死的脚本会在「先试 1 页、再试 2 页」这种多轮重试里失去意义。
// 假渲染器只做高度判定，引擎内部用真图片判高的那一段由 limits_test.go 覆盖。
type scriptRenderer struct {
	calls      []pageCall
	itemHeight int // 每个条目占的高度（px）
	failAt     int // 第几次调用返回真实错误（0 表示不失败）
}

func (s *scriptRenderer) Render(_ context.Context, card Card) ([]byte, error) {
	req, ok := card.Data.(pageCall)
	if !ok {
		return nil, fmt.Errorf("卡片数据不是 pageCall：%T", card.Data)
	}
	s.calls = append(s.calls, req)
	if n := len(s.calls); n == s.failAt {
		return nil, errors.New("渲染坏了")
	}
	height := (req.to - req.from) * s.itemHeight
	if height > MaxCardHeightPx {
		return nil, heightError(height)
	}
	return []byte("image"), nil
}

func (s *scriptRenderer) Close() error { return nil }

// buildFor 把页码与区间塞进卡片，方便断言分页器给出的切分方案。
func buildFor(page, pages, from, to, total int) Card {
	return Card{Name: "test", Data: pageCall{page: page, pages: pages, from: from, to: to, total: total}}
}

// TestPaginateSinglePageWhenItFits 是最常见的情况：第一页就装得下，只渲染一次、只发一张图。
func TestPaginateSinglePageWhenItFits(t *testing.T) {
	r := &scriptRenderer{itemHeight: 1000}
	imgs, err := Paginate(context.Background(), r, 5, buildFor)
	if err != nil {
		t.Fatalf("不该失败：%v", err)
	}
	if len(imgs) != 1 {
		t.Fatalf("应只出一张图，实际 %d", len(imgs))
	}
	want := []pageCall{{page: 1, pages: 1, from: 0, to: 5, total: 5}}
	if !reflect.DeepEqual(r.calls, want) {
		t.Fatalf("构造请求应为 %+v，实际 %+v", want, r.calls)
	}
}

// TestPaginateSplitsWhenFirstPageTooTall 是分页的核心用例：
// 10 条 × 1000px = 10000px 超过安全线 → 拆成 2 页（每页 5 条 = 5000px）→ 出两张图，区间不重不漏。
func TestPaginateSplitsWhenFirstPageTooTall(t *testing.T) {
	r := &scriptRenderer{itemHeight: 1000}
	imgs, err := Paginate(context.Background(), r, 10, buildFor)
	if err != nil {
		t.Fatalf("不该失败：%v", err)
	}
	if len(imgs) != 2 {
		t.Fatalf("应出两张图，实际 %d", len(imgs))
	}
	want := []pageCall{
		{page: 1, pages: 1, from: 0, to: 10, total: 10},
		{page: 1, pages: 2, from: 0, to: 5, total: 10},
		{page: 2, pages: 2, from: 5, to: 10, total: 10},
	}
	if !reflect.DeepEqual(r.calls, want) {
		t.Fatalf("构造请求应为 %+v，实际 %+v", want, r.calls)
	}
}

// TestPaginateKeepsSplittingUntilFits 覆盖「2 页还不够、3 页才过」：
// 9 条 × 2000px → 1 页 18000、2 页每页 5 条 10000 都超标，3 页每页 3 条 6000 才通过。
func TestPaginateKeepsSplittingUntilFits(t *testing.T) {
	r := &scriptRenderer{itemHeight: 2000}
	imgs, err := Paginate(context.Background(), r, 9, buildFor)
	if err != nil {
		t.Fatalf("不该失败：%v", err)
	}
	if len(imgs) != 3 {
		t.Fatalf("应出三张图，实际 %d", len(imgs))
	}
	last := r.calls[len(r.calls)-1]
	if last != (pageCall{page: 3, pages: 3, from: 6, to: 9, total: 9}) {
		t.Fatalf("最后一页的区间应是 [6,9)、页码 3/3，实际 %+v", last)
	}
	if !reflect.DeepEqual(r.calls[len(r.calls)-3:], []pageCall{
		{page: 1, pages: 3, from: 0, to: 3, total: 9},
		{page: 2, pages: 3, from: 3, to: 6, total: 9},
		{page: 3, pages: 3, from: 6, to: 9, total: 9},
	}) {
		t.Fatalf("最后一轮应当是 3 页均分，实际 %+v", r.calls[len(r.calls)-3:])
	}
}

// TestPaginateGivesUpAfterMaxPages 说明页数上限是硬约束：
// 12 条 × 3000px 怎么分都超标 → 试到 3 页就停手，报一个「卡片过高」的错误让调用方回退完整文本。
func TestPaginateGivesUpAfterMaxPages(t *testing.T) {
	if MaxPaginationPages != 3 {
		t.Fatalf("本用例按 3 页上限写死断言，实际上限为 %d", MaxPaginationPages)
	}
	r := &scriptRenderer{itemHeight: 3000}
	imgs, err := Paginate(context.Background(), r, 12, buildFor)
	if err == nil {
		t.Fatalf("装不下时应当报错，实际拿到 %d 张图", len(imgs))
	}
	if !IsCardTooTall(err) {
		t.Fatalf("错误应可辨认成「卡片过高」（调用方据此回退文本）：%v", err)
	}
	for _, c := range r.calls {
		if c.pages > MaxPaginationPages {
			t.Fatalf("试过的页数不该超过上限：%+v", c)
		}
	}
	if len(r.calls) != MaxPaginationPages {
		t.Fatalf("每轮第一页就超标时会立刻收手，应当只渲染 %d 次（每轮一次），实际 %d 次",
			MaxPaginationPages, len(r.calls))
	}
}

// TestPaginatePropagatesRealError 说明「渲染坏了」不重试：加页解决不了浏览器崩溃之类的问题，
// 应当立刻上抛让调用方回退文本。
func TestPaginatePropagatesRealError(t *testing.T) {
	r := &scriptRenderer{itemHeight: 1000, failAt: 1}
	_, err := Paginate(context.Background(), r, 6, buildFor)
	if err == nil || err.Error() != "渲染坏了" {
		t.Fatalf("应原样上抛渲染错误，实际 %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("真实错误不该重试，实际渲染 %d 次", len(r.calls))
	}
	if IsCardTooTall(err) {
		t.Fatal("渲染坏了不该被当成「卡片过高」")
	}
}

// TestPaginateCapsPagesAtItemCount 钉住「页数不超过条目数」：
// 只有 2 条内容时最多分 2 页，不会出现「共 3 页」却只发得出 2 张的错位。
func TestPaginateCapsPagesAtItemCount(t *testing.T) {
	r := &scriptRenderer{itemHeight: 5000}
	imgs, err := Paginate(context.Background(), r, 2, buildFor)
	if err != nil {
		t.Fatalf("两条各占一页应当成功：%v", err)
	}
	if len(imgs) != 2 {
		t.Fatalf("应出两张图，实际 %d", len(imgs))
	}
	for _, c := range r.calls {
		if c.pages > 2 {
			t.Fatalf("页数不该超过条目数：%+v", c)
		}
	}
}

// TestPaginateZeroItemsRendersOnePage 说明空内容也出一张卡：占位卡（例如「暂无重要指令」）走同一条路径。
func TestPaginateZeroItemsRendersOnePage(t *testing.T) {
	r := &scriptRenderer{itemHeight: 1000}
	imgs, err := Paginate(context.Background(), r, 0, buildFor)
	if err != nil {
		t.Fatalf("不该失败：%v", err)
	}
	if len(imgs) != 1 {
		t.Fatalf("应出一张占位图，实际 %d", len(imgs))
	}
	if r.calls[0] != (pageCall{page: 1, pages: 1, from: 0, to: 0, total: 0}) {
		t.Fatalf("占位页的区间应为 [0,0)，实际 %+v", r.calls[0])
	}
}

// TestPaginateNilRenderer 说明没有渲染能力时不去猜结果，直接报错让调用方发文本。
func TestPaginateNilRenderer(t *testing.T) {
	if _, err := Paginate(context.Background(), nil, 3, buildFor); err == nil {
		t.Fatal("渲染器为空时应当报错")
	}
}

// TestPageLabelAndWithPageNote 钉住页码文案：单页不加页脚，多页才显示，且与既有说明文案合并。
func TestPageLabelAndWithPageNote(t *testing.T) {
	cases := []struct {
		name        string
		note        string
		page, pages int
		want        string
	}{
		{"单页不带说明", "", 1, 1, ""},
		{"单页有说明", "正文过长，已截断显示。", 1, 1, "正文过长，已截断显示。"},
		{"多页无说明", "", 2, 3, "第 2/3 页"},
		{"多页有说明", "翻译暂不可用，以上正文为英文原文。", 1, 3, "翻译暂不可用，以上正文为英文原文。；第 1/3 页"},
		{"多页空白说明", "   ", 1, 2, "第 1/2 页"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := WithPageNote(c.note, c.page, c.pages); got != c.want {
				t.Fatalf("WithPageNote(%q,%d,%d) = %q，期望 %q", c.note, c.page, c.pages, got, c.want)
			}
		})
	}
	if got := PageLabel(1, 1); got != "" {
		t.Fatalf("单页的页码文案应为空，实际 %q", got)
	}
	if got := PageLabel(3, 3); got != "第 3/3 页" {
		t.Fatalf("页码文案应为「第 3/3 页」，实际 %q", got)
	}
}
