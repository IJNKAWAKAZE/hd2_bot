package render

import (
	"strings"
	"testing"
)

// TestBiomeAssetExactMatch 钉住「上游群系名 → 素材」的精确匹配（键取自社区站点的 index.json）。
func TestBiomeAssetExactMatch(t *testing.T) {
	cases := map[string]string{
		"Deciduous Forest":      "biome.deciduous_grove_biome_header",
		"Desert Dunes":          "biome.sandy_base",
		"Icy Glaciers":          "biome.arctic_glacier_base",
		"Hive World":            "biome.bug_hiveworld",
		"Super Earth":           "biome.super_earth_landscape",
		"Cyberstan Megafactory": "biome.cyberstan_landscape",
		// 站点 index.json 里有 26 条，此前唯一漏掉的就是这条（曾误以为对方也没配图）
		"Void Source Forest": "biome.rift_active",
	}
	for name, want := range cases {
		if got := BiomeAsset(name); got != want {
			t.Errorf("BiomeAsset(%q) = %q，期望 %q", name, got, want)
		}
	}
}

// TestBiomeAssetToleratesCaseAndSpaces 说明匹配对大小写与首尾空白不敏感：
// 上游偶尔会把群系名的词改大小写，卡上少一张图这种事不该靠运气。
func TestBiomeAssetToleratesCaseAndSpaces(t *testing.T) {
	want := BiomeAsset("Deciduous Forest")
	if want == "" {
		t.Fatal("夹具失效：Deciduous Forest 应当有配图")
	}
	for _, name := range []string{"deciduous forest", "DECIDUOUS FOREST", "  Deciduous Forest  ", "Deciduous  Forest"} {
		if got := BiomeAsset(name); got != want {
			t.Errorf("BiomeAsset(%q) = %q，期望与标准写法一致（%q）", name, got, want)
		}
	}
}

// TestBiomeAssetKeywordFallback 说明表里没有的新群系名会按词根兜底，且更具体的词根优先：
// 更具体的词根优先："Volcanic Jungle" 这类新名字要落在 jungle 上，而不是被 volcan 抢成岩浆图。
func TestBiomeAssetKeywordFallback(t *testing.T) {
	cases := map[string]string{
		"Volcanic": "biome.magma_base",
		// 精确表里有它（站点 index.json 就是配 Primordial_base），此时不该走关键词兜底
		"Volcanic Jungle":           "biome.primordial_base",
		"Some Unknown Jungle World": "biome.primordial_blue",
		"Acidic Marshlands":         "biome.sandy_acid",
		"Frozen Expanse":            "biome.arctic_glacier_base",
		"Gas Giant Moon":            "biome.sandy_moon",
		"Terminid Hive Moon":        "biome.bug_hiveworld",
		"Autumn Forest":             "biome.autumn_forest_biome_header",
	}
	for name, want := range cases {
		if got := BiomeAsset(name); got != want {
			t.Errorf("BiomeAsset(%q) = %q，期望 %q", name, got, want)
		}
	}
}

// TestBiomeAssetUnknownReturnsEmpty 说明匹配不上时返回空串而不是硬塞一张图：
// 配错图（沙漠画到冰原上）比没有图更糟，模板见到空串会整块跳过。
func TestBiomeAssetUnknownReturnsEmpty(t *testing.T) {
	for _, name := range []string{"", "   ", "None", "Rift Rift", "???"} {
		if got := BiomeAsset(name); got != "" {
			t.Errorf("BiomeAsset(%q) = %q，期望空串", name, got)
		}
	}
}

// TestBiomeAssetsPointToRegisteredFiles 是这张表最容易犯的错：逻辑名写错一个字母。
// 后果只是「卡片上少一张图」，跑起来很难发现，所以在这里一次性钉死。
func TestBiomeAssetsPointToRegisteredFiles(t *testing.T) {
	if len(biomeAssets) < 20 {
		t.Fatalf("映射表只有 %d 条，像是被误删了（站点 index.json 里有 26 条）", len(biomeAssets))
	}
	for name, asset := range biomeAssets {
		if _, ok := assetFiles[asset]; !ok {
			t.Errorf("群系 %q 指向的逻辑名 %q 没有登记在 assetFiles 里", name, asset)
		}
		if !strings.HasPrefix(asset, "biome.") {
			t.Errorf("群系 %q 指向的 %q 不是群系图（逻辑名应以 biome. 开头）", name, asset)
		}
	}
	for _, kw := range keywordBiomeAssets {
		if _, ok := assetFiles[kw.asset]; !ok {
			t.Errorf("关键词 %q 指向的逻辑名 %q 没有登记在 assetFiles 里", kw.keyword, kw.asset)
		}
	}
}

// TestBiomeAssetFilesAreRegistered 反向检查：内置的群系图都得能被某个群系名（或关键词）用到，
// 免得往 assets/biomes 里丢了一张谁都映射不到的图。
func TestBiomeAssetFilesAreRegistered(t *testing.T) {
	used := make(map[string]bool, len(biomeAssets))
	for _, asset := range biomeAssets {
		used[asset] = true
	}
	unused := make([]string, 0)
	for name := range assetFiles {
		if strings.HasPrefix(name, "biome.") && !used[name] {
			unused = append(unused, name)
		}
	}
	if len(unused) > 0 {
		t.Fatalf("这些群系图没有任何群系名映射到：%v", unused)
	}
}
