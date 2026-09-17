// smoke_test.go 是真浏览器冒烟：把图鉴卡片实际渲染成 PNG 落到 tmp/screenshots，
// 用来眼看排版有没有被 CSS 挤坏（只跑 render.HTML 的用例看不出这个）。
//
// 装备侧用仓库里的真目录（data/arsenal/catalog.json）与真装备图：预览图与线上一致；
// 敌人侧用内联夹具——本机连不通 helldivers.wiki.gg，图鉴取不到真数据。
// 默认跳过，只有 HD2_RENDER_SMOKE=1 时才跑（CI/无浏览器环境不该依赖 Chromium）。
package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hd2_bot/src/arsenal"
	"hd2_bot/src/atlas"
	"hd2_bot/src/bestiary"
	"hd2_bot/src/render"
	"hd2_bot/src/wikigg"
)

// TestCardsSmokeWithRealBrowser 渲染六张图鉴卡片：五类装备各一张 + 敌人一张。
func TestCardsSmokeWithRealBrowser(t *testing.T) {
	if os.Getenv("HD2_RENDER_SMOKE") != "1" {
		t.Skip("未开启 HD2_RENDER_SMOKE")
	}
	catalog := smokeCatalog(t)
	images := smokeImages(t)

	engine, err := render.New(render.Config{
		Width: 900, Scale: 2, Timeout: 20 * time.Second, Format: render.FormatPNG,
	})
	if err != nil {
		t.Fatalf("创建渲染引擎失败：%v", err)
	}
	defer engine.Close()

	at := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		file string
		card render.Card
	}{
		{"hd2_gun_card.png", smokeEquipment(t, catalog, images, gunSpec, "野狼", at)},
		{"hd2_gun_multi_card.png", smokeEquipment(t, catalog, images, gunSpec, "解放者", at)},
		{"hd2_strat_card.png", smokeEquipment(t, catalog, images, stratSpec, "哨戒炮", at)},
		{"hd2_armor_card.png", smokeEquipment(t, catalog, images, armorSpec, "侦察者", at)},
		{"hd2_grenade_card.png", smokeEquipment(t, catalog, images, grenadeSpec, "榴弹", at)},
		{"hd2_enemy_card.png", render.Card{Name: enemyCardName, Data: smokeEnemyCard(t, at)}},
	}
	for _, tc := range cases {
		png, err := engine.Render(context.Background(), tc.card)
		if err != nil {
			t.Fatalf("渲染卡片 %s 失败：%v", tc.card.Name, err)
		}
		path := filepath.Join("..", "..", "..", "tmp", "screenshots", tc.file)
		if err := os.WriteFile(path, png, 0o644); err != nil {
			t.Fatalf("写出图片失败：%v", err)
		}
		t.Logf("%s：%s（%d 字节，宽 %d）", tc.card.Name, path, len(png), pngWidth(t, png))
	}
}

// smokeEquipment 用真目录跑一次精确查询并构造卡片。
func smokeEquipment(t *testing.T, catalog *arsenal.Catalog, images map[string]string,
	spec EquipmentSpec, keyword string, at time.Time) render.Card {
	t.Helper()
	result := catalog.Search(arsenal.Query{Kinds: spec.Kinds, Keyword: keyword, Limit: searchLimit})
	if len(result.Items) == 0 {
		t.Fatalf("真目录里查不到「%s」：预制数据可能变了，选个新的关键字", keyword)
	}
	return render.Card{
		Name: equipmentCardName,
		Data: BuildEquipmentCard(spec, result, catalog, images, keyword, at),
	}
}

// smokeEnemyCard 用内联图鉴夹具 + 内置详情里的真实图地址构造敌人卡片。
//
// 敌人图鉴本身要走 wiki 的 Cargo 查询，冒烟时不依赖它；但外观图与部位示意图的地址来自
// 随仓库发布的内置详情数据（见 src/atlas），能连上站点就真下下来——这样截图里能看到真图，
// 连不上就退回无图（卡片照样出数值），不会让冒烟用例挂掉。
func smokeEnemyCard(t *testing.T, at time.Time) EnemyCard {
	t.Helper()
	list := []bestiary.Enemy{
		{Title: "Bile Titan", NameZh: "胆汁泰坦", Faction: bestiary.FactionTerminid, FactionRaw: "Terminids",
			Size: 3, Health: 6500, Damage: "【酸液】950 ｜ 【近战】1000"},
		{Title: "Titan Variant", NameZh: "变异泰坦", Faction: bestiary.FactionTerminid, FactionRaw: "Terminids",
			Size: 2, Health: 1200},
	}
	result := bestiary.Search(list, bestiary.Query{Keyword: "泰坦", Limit: searchLimit})
	entry := MatchEnemyDetail(result.Enemies[0])
	images := smokeEnemyImages(t, entry)
	return BuildEnemyCard(result, "泰坦", images, RelatedEnemies(list, result.Enemies[0], maxRelatedEnemies), at)
}

// smokeEnemyImages 下载这只敌人的外观图与部位示意图（最多 maxPartImages 张）。
func smokeEnemyImages(t *testing.T, entry *atlas.Entry) EnemyImages {
	t.Helper()
	if entry == nil {
		return EnemyImages{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	wiki := wikigg.New(wikigg.Config{Timeout: 20 * time.Second}, wikigg.Deps{})

	images := EnemyImages{}
	if url := strings.TrimSpace(entry.Image); url != "" {
		if raw, err := wiki.ImageBytes(ctx, url, 8<<20); err == nil {
			images.Icon = render.ThumbDataURI(raw, enemyImageWidth)
		} else {
			t.Logf("冒烟：外观图没下下来（%v），这张卡不带图", err)
		}
	}
	parts := make(map[string]string, maxPartImages)
	missing := 0
	for _, part := range entry.Parts {
		if len(parts) >= maxPartImages {
			break
		}
		if strings.TrimSpace(part.Image) == "" {
			continue
		}
		raw, err := wiki.ImageBytes(ctx, part.Image, 8<<20)
		if err != nil {
			missing++
			continue
		}
		if uri := render.ThumbDataURI(raw, partImageWidth); uri != "" {
			parts[part.Name] = uri
		}
	}
	if missing > 0 {
		t.Logf("冒烟：有 %d 张部位示意图没下下来", missing)
	}
	images.Parts = parts
	return images
}

// smokeCatalog 读仓库里的真装备目录。
func smokeCatalog(t *testing.T) *arsenal.Catalog {
	t.Helper()
	path := filepath.Join("..", "..", "..", "data", "arsenal", "catalog.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("没有本地目录缓存，跳过：%v", err)
	}
	catalog, err := arsenal.ParseCatalog(raw, nil)
	if err != nil {
		t.Fatalf("解析真目录失败：%v", err)
	}
	return catalog
}

// smokeImages 把 data/arsenal/images 下已下载的装备图读成「id → data URI」。
func smokeImages(t *testing.T) map[string]string {
	t.Helper()
	dir := filepath.Join("..", "..", "..", "data", "arsenal", "images")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("没有本地装备图缓存，跳过：%v", err)
	}
	images := make(map[string]string, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		id := strings.TrimSuffix(name, filepath.Ext(name))
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		if uri := render.ThumbDataURI(raw, gearImageWidth); uri != "" {
			images[id] = uri
		}
	}
	return images
}

// pngWidth 读 PNG 宽度（8 字节签名 + 4 字节长度 + 4 字节 "IHDR" + 4 字节宽）；不是 PNG 直接失败。
func pngWidth(t *testing.T, data []byte) int {
	t.Helper()
	if len(data) < 24 || !bytesHasPNGSignature(data) {
		t.Fatalf("输出不是合法 PNG：长度 %d", len(data))
	}
	return int(data[16])<<24 | int(data[17])<<16 | int(data[18])<<8 | int(data[19])
}

// bytesHasPNGSignature 判断开头是不是 PNG 的 8 字节签名。
func bytesHasPNGSignature(data []byte) bool {
	signature := []byte("\x89PNG\r\n\x1a\n")
	if len(data) < len(signature) {
		return false
	}
	for i, b := range signature {
		if data[i] != b {
			return false
		}
	}
	return true
}
