package plugtest

import (
	"context"
)

// Translator 是翻译层的记录型测试替身。
//
// 语义（刻意照抄计划里定的口径，别改）：
//   - Out 是一队译文，按顺序被**逐个消费**：第 i 次取用拿到 Out[i]，越界时原样返回输入；
//     「越界回退原文」让「某段没翻动」的退化路径在跨调用时也有人守。
//   - Err 非 nil 时整批失败：把输入原样回传并附带该错误，
//     上层据此走「保留原文 + 卡片注明」的退化分支。
//   - Texts 按调用次序记录每次收到的文本批次，用于断言「哪些段被送译了」「送了几次」。
//
// 线程安全性：不是并发安全的（Out 的消费游标无锁）。仅用于单测，
// 需要并发调用时请自己在外层加锁或用多次独立实例。
type Translator struct {
	// Out 是预设译文队列，按调用顺序逐个消费。
	Out []string
	// Err 非 nil 时 Translate 返回「输入原样 + 该错误」。
	Err error
	// Texts 按调用次序记录每次收到的文本批次（已复制，调用方后续改动不会污染记录）。
	Texts [][]string
	// index 是 Out 的消费游标。
	index int
}

// Translate 记录输入并返回预设译文：逐段消费 Out，越界段原样返回输入。
// Err 非 nil 时原样回传输入并返回该错误（让上层走退化分支，而不是拿到半截译文）。
func (t *Translator) Translate(_ context.Context, texts []string) ([]string, error) {
	t.Texts = append(t.Texts, append([]string(nil), texts...))
	if t.Err != nil {
		return append([]string(nil), texts...), t.Err
	}
	out := make([]string, len(texts))
	for i, text := range texts {
		if t.index < len(t.Out) {
			out[i] = t.Out[t.index]
			t.index++
			continue
		}
		out[i] = text
	}
	return out, nil
}

// Calls 返回 Translate 已被调用的次数；用例据此断言「同一次送译只打一次请求」这类次数契约。
func (t *Translator) Calls() int { return len(t.Texts) }
