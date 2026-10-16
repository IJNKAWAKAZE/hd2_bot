// smoke_test.go 是真浏览器冒烟：把「呼叫演练」的题面图实际渲染成 PNG 落到 tmp/screenshots，
// 用来眼看排版有没有被 CSS 挤坏（只跑 render.HTML 的用例看不出这个）。
//
// 题面图用的是线上同一份数据：从装备目录里取战备图标（data/arsenal 下的缓存，缺了会去下载）。
// 默认跳过，只有 HD2_RENDER_SMOKE=1 时才跑（CI/无浏览器环境不该依赖 Chromium）。
package gatekeeper

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"hd2_bot/src/arsenal"
	"hd2_bot/src/render"
)

// TestVerifyCardSmokeWithRealBrowser 渲染两种难度的题面图：
// 显示答案（默认，图上印出召唤指令）与只给名字的硬核模式。
func TestVerifyCardSmokeWithRealBrowser(t *testing.T) {
	if os.Getenv("HD2_RENDER_SMOKE") != "1" {
		t.Skip("未开启 HD2_RENDER_SMOKE")
	}
	engine, err := render.New(render.Config{Width: 900, Scale: 2, Timeout: 20 * time.Second, Format: render.FormatPNG})
	if err != nil {
		t.Fatalf("创建渲染引擎失败：%v", err)
	}
	defer engine.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := arsenal.New(arsenal.Config{
		Dir:     filepath.Join("..", "..", "..", "data", "arsenal"),
		TTL:     time.Hour,
		Timeout: 30 * time.Second,
	}, arsenal.Deps{Logger: t.Logf})

	cases := []struct {
		file       string
		showAnswer bool
	}{
		{"hd2_verify_card.png", true},
		{"hd2_verify_card_hard.png", false},
	}
	for _, tc := range cases {
		handler := New(
			Config{Mode: ModeDirect, ShowAnswer: tc.showAnswer, Timeout: 3 * time.Minute, Attempts: 1},
			Deps{
				Telegram: newFakeTelegram(100),
				Catalog:  store,
				Images:   store,
				Schedule: func(time.Duration, func()) {},
				Logger:   func(string, ...any) {},
			},
		)
		session, err := handler.newSession(100, 7, "阿力", false, 0, 0)
		if err != nil {
			t.Fatalf("出题失败：%v", err)
		}
		target := session.Challenge.Target()
		card := handler.buildCard(target)
		if raw, err := store.Image(ctx, target.Item); err != nil {
			t.Logf("题面图没取到（只画名字与指令）：%v", err)
		} else {
			card.Image = render.ImageDataURI(raw)
		}
		png, err := engine.Render(ctx, render.Card{Name: cardTemplateName, Data: card})
		if err != nil {
			t.Fatalf("渲染题面图失败：%v", err)
		}
		path := filepath.Join("..", "..", "..", "tmp", "screenshots", tc.file)
		if err := os.WriteFile(path, png, 0o644); err != nil {
			t.Fatalf("写出图片失败：%v", err)
		}
		t.Logf("题面图：%s（%d 字节，题目=%s，有图=%v）", path, len(png), card.TargetName, card.Image != "")
		// 顺带把随图发出的提示文本打出来：冒烟时一眼能看到新成员实际读到的文案。
		// 这里把 PhotoID 先填上，模拟「图已经发出去了」的状态 —— 那种情况下答案只在图里，文本不重复。
		session.PhotoID = 1
		t.Logf("提示文本（MarkdownV2 源，图已发出时）：\n%s", handler.promptText(session))
	}
}
