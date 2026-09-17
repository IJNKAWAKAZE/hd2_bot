// 群系实景图：把上游的 biome.name（英文自由文本，例如 "Deciduous Forest"）映射到内置素材。
//
// 素材与出处见 assets/LICENSES.md：2026-09-17 起，用户明确同意项目直接使用游戏画面，
// 于是这 26 张群系实景图（取自社区站点 HD2 真理部的 assets/biomes/）作为 /planet 头图与
// /planets 缩略图使用。
//
// 为什么要有显式映射表：上游给的是一串自由文本，映射不到时**不能瞎猜**——把沙漠的图画到冰原上，
// 比没有图更糟。所以策略是三级：精确匹配 → 关键词兜底（只放「看一眼就知道该配哪张」的词根）→ 不挂图。
// 模板按空串跳过整张图，卡片其余部分照常渲染。
package render

import "strings"

// biomeAssets 是「上游群系名 → 素材逻辑名」。键名取自社区站点 assets/biomes/index.json
// （对方与本项目用同一套上游数据，键名即 biome.name 原值）；对方 index.json 里的 26 条已全部收录，
// 上游实测出现的群系名（2026-09-17 拉全量 273 颗星球）除占位值 "ACCESS DENIED" 外一个不漏。
var biomeAssets = map[string]string{
	"Acidic Badlands":         "biome.sandy_acid",
	"Basic Swamp":             "biome.swamp_base",
	"Boneyard":                "biome.arctic_glacier_coldrocky",
	"Cyberstan Megafactory":   "biome.cyberstan_landscape",
	"Deadlands":               "biome.primordial_dead",
	"Deciduous Autumn Forest": "biome.autumn_forest_biome_header",
	"Deciduous Forest":        "biome.deciduous_grove_biome_header",
	"Desert Cliffs":           "biome.sandy_spiky",
	"Desert Dunes":            "biome.sandy_base",
	"Desert Oasis":            "biome.tropical_oasis_biome_header",
	"Ethereal Jungle":         "biome.primordial_purple",
	"Haunted Swamp":           "biome.swamp_haunted",
	"Hive World":              "biome.bug_hiveworld",
	"Icy Glaciers":            "biome.arctic_glacier_base",
	"Ionic Crimson":           "biome.moor_red",
	"Ionic Jungle":            "biome.primordial_blue",
	"Magma":                   "biome.magma_base",
	"Moon":                    "biome.sandy_moon",
	"Plains":                  "biome.moor_baseplanet",
	"Rocky Canyons":           "biome.sandy_mineral",
	"Scorched Moor":           "biome.moor_arid",
	"Super Earth":             "biome.super_earth_landscape",
	"Supercolony":             "biome.supercolony",
	"Tundra":                  "biome.moor_tundra",
	"Void Source Forest":      "biome.rift_active",
	"Volcanic Jungle":         "biome.primordial_base",
}

// keywordBiomeAssets 是关键词兜底：上游出现表里没有的新群系名时，按词根挑一张最接近的图。
//
// 顺序有意义：先出现的先命中，所以更具体的词根要排在更泛的前面（例如 "volcanic jungle" 必须
// 落在 jungle 上，而不是被 volcan 抢走）。词根一律小写，匹配前会把上游名字转成小写。
var keywordBiomeAssets = []struct {
	keyword string
	asset   string
}{
	{"void source", "biome.rift_active"},
	{"volcanic jungle", "biome.primordial_blue"},
	{"jungle", "biome.primordial_blue"},
	{"volcan", "biome.magma_base"},
	{"magma", "biome.magma_base"},
	{"swamp", "biome.swamp_base"},
	{"hive", "biome.bug_hiveworld"},
	{"tundra", "biome.moor_tundra"},
	{"glacier", "biome.arctic_glacier_base"},
	{"arctic", "biome.arctic_glacier_base"},
	{"frozen", "biome.arctic_glacier_base"},
	{"canyon", "biome.sandy_mineral"},
	{"desert", "biome.sandy_base"},
	{"dune", "biome.sandy_base"},
	{"moon", "biome.sandy_moon"},
	{"acid", "biome.sandy_acid"},
	{"autumn", "biome.autumn_forest_biome_header"},
	{"forest", "biome.deciduous_grove_biome_header"},
	{"oasis", "biome.tropical_oasis_biome_header"},
	{"super earth", "biome.super_earth_landscape"},
	{"colony", "biome.supercolony"},
	{"megafactory", "biome.cyberstan_landscape"},
	{"cyberstan", "biome.cyberstan_landscape"},
	{"boneyard", "biome.primordial_dead"},
	{"dead", "biome.primordial_dead"},
	{"moor", "biome.moor_arid"},
}

// BiomeAsset 返回群系名对应的素材逻辑名；没有合适配图时返回空串（模板据此不渲染图）。
//
// 匹配顺序：原样精确匹配 → 去空白后精确匹配 → 小写关键词兜底。
// 大小写不敏感是刻意的：上游偶尔会把群系名里的词改大小写（"Deciduous Forest" / "deciduous forest"）。
func BiomeAsset(name string) string {
	key := strings.TrimSpace(name)
	if key == "" {
		return ""
	}
	if asset, ok := biomeAssets[key]; ok {
		return asset
	}
	lower := strings.ToLower(key)
	for entry, asset := range biomeAssets {
		if strings.ToLower(entry) == lower {
			return asset
		}
	}
	for _, kw := range keywordBiomeAssets {
		if strings.Contains(lower, kw.keyword) {
			return kw.asset
		}
	}
	return ""
}
