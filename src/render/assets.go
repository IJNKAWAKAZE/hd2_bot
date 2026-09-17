package render

import (
	"embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"path"
	"regexp"
	"strings"
	"sync"
)

// assetFS 把卡片素材打进二进制；素材文件被删掉会在编译期就报错。
//
//go:embed assets
var assetFS embed.FS

// 素材的三种内联前缀。素材表在加载阶段校验过扩展名，所以这里可以直接写死 MIME。
//
// 为什么要分三种：位图（PNG）走 <img src>，矢量图标（SVG）既能走 <img> 也能内联成
// 可由 CSS 着色的元素（参考站点的阵营图标就是纯黑剪影，必须靠 CSS 上色才看得见），
// 字体（TTF）只能进 CSS 的 @font-face。
const (
	pngDataURIPrefix = "data:image/png;base64,"
	svgDataURIPrefix = "data:image/svg+xml;base64,"
	ttfDataURIPrefix = "data:font/ttf;base64,"
)

// 允许登记的素材扩展名：写错扩展名在加载阶段就 panic，而不是渲染出来才发现少一张图。
var assetExts = map[string]bool{".png": true, ".svg": true, ".ttf": true}

// fontAssetName 是卡片标题字体（ZZZ-thick）的逻辑名，base.tmpl 的 @font-face 用它。
const fontAssetName = "font.title"

// assetFiles 是「逻辑名 → assets/ 下相对路径」的映射表。
// 逻辑名形如 <类别>.<名字>，模板里用 {{icon "emblem.automaton"}} 或 {{asset "..."}} 引用。
// 这张表必须与实际文件一一对应：init 阶段会双向校验（漏登记或多登记都会 panic）。
//
// 图标一族（emblem.* / tactical.* / event.*）自 2026-09-17 起改用社区站点
// HD2 真理部（jerry114514/HD2_Galactic_War_Map）里的**游戏内原生图标**：
// 阵营剪影、战役标记、DSS 战术行动图标都是 SVG，由页面的 fill 继承着色；
// 取代了原先来自 astrbot_plugin_Helldivers 的自绘徽标（那批图与游戏里的图形对不上）。
// 关于/许可见 assets/LICENSES.md。
var assetFiles = map[string]string{
	// —— 游戏内原生图标（社区站点 HD2 真理部，见 LICENSES.md 第二节）——
	"emblem.super_earth":        "assets/game/faction/humans.svg",
	"emblem.automaton":          "assets/game/faction/automatons.svg",
	"emblem.terminids":          "assets/game/faction/terminids.svg",
	"emblem.illuminate":         "assets/game/faction/illuminate.svg",
	"emblem.defense":            "assets/game/ui/defense_campaign.svg",
	"emblem.major_order":        "assets/game/ui/liberation_campaign.svg",
	"emblem.dss":                "assets/game/effect/dss.svg",
	"event.super_earth_flag":    "assets/game/ui/super_earth_flag.svg",
	"tactical.eagle_storm":      "assets/game/effect/dss_eagle_storm.svg",
	"tactical.heavy_ordnance":   "assets/game/effect/dss_heavy_ordnance.svg",
	"tactical.orbital_blockade": "assets/game/effect/dss_orbital_blockade.svg",
	// 上游的 DSS 战术行动里没有与「轨道燃烧弹幕」一一对应的图标，这里取同一套里语义最近的
	// 行星轰炸，而不是继续留着自绘的红色方块图。
	"tactical.orbital_napalm": "assets/game/effect/dss_planetary_bombardment.svg",
	// 召唤指令箭头（与参考站点的 assets/arrows 同一批图形）
	"arrow.up":    "assets/game/arrow/up.svg",
	"arrow.down":  "assets/game/arrow/down.svg",
	"arrow.left":  "assets/game/arrow/left.svg",
	"arrow.right": "assets/game/arrow/right.svg",
	// 增援标记（星球卡上的在线士兵数）
	"ui.reinforce": "assets/game/ui/reinforce.svg",
	// 卡片标题字体
	"font.title": "assets/fonts/ZZZ-thick.ttf",
	// 群系实景图（2026-09-17 从社区站点 HD2 真理部的 assets/biomes/ 取回，来源与许可见 LICENSES.md）
	"biome.arctic_glacier_base":          "assets/biomes/Arctic_glacier_base_Landscape.png",
	"biome.arctic_glacier_coldrocky":     "assets/biomes/Arctic_glacier_coldrocky_Landscape.png",
	"biome.autumn_forest_biome_header":   "assets/biomes/Autumn_Forest_Biome_Header.png",
	"biome.bug_hiveworld":                "assets/biomes/Bug_hiveworld_Landscape.png",
	"biome.cyberstan_landscape":          "assets/biomes/Cyberstan_landscape.png",
	"biome.deciduous_grove_biome_header": "assets/biomes/Deciduous_Grove_Biome_Header.png",
	"biome.magma_base":                   "assets/biomes/Magma_Base_Landscape.png",
	"biome.moor_arid":                    "assets/biomes/Moor_arid_Landscape.png",
	"biome.moor_baseplanet":              "assets/biomes/Moor_baseplanet_Landscape.png",
	"biome.moor_red":                     "assets/biomes/Moor_red_Landscape.png",
	"biome.moor_tundra":                  "assets/biomes/Moor_tundra_Landscape.png",
	"biome.primordial_base":              "assets/biomes/Primordial_base_Landscape.png",
	"biome.primordial_blue":              "assets/biomes/Primordial_blue_Landscape.png",
	"biome.primordial_dead":              "assets/biomes/Primordial_dead_Landscape.png",
	"biome.primordial_purple":            "assets/biomes/Primordial_purple_Landscape.png",
	"biome.rift_active":                  "assets/biomes/Rift_active_landscape.png",
	"biome.sandy_acid":                   "assets/biomes/Sandy_acid_Landscape.png",
	"biome.sandy_base":                   "assets/biomes/Sandy_base_Landscape.png",
	"biome.sandy_mineral":                "assets/biomes/Sandy_mineral_Landscape.png",
	"biome.sandy_moon":                   "assets/biomes/Sandy_moon_Landscape.png",
	"biome.sandy_spiky":                  "assets/biomes/Sandy_spiky_Landscape.png",
	"biome.super_earth_landscape":        "assets/biomes/Super_Earth_landscape.png",
	"biome.supercolony":                  "assets/biomes/Supercolony_Landscape.png",
	"biome.swamp_base":                   "assets/biomes/Swamp_base_Landscape.png",
	"biome.swamp_haunted":                "assets/biomes/Swamp_haunted_Landscape.png",
	"biome.tropical_oasis_biome_header":  "assets/biomes/Tropical_Oasis_Biome_Header.png",
}

// 素材缓存：编码结果分「data URI」与「内联 SVG」两份。
// 用 sync.Map 而不是普通 map+锁，是因为这里读多写少；
// 并发首次渲染可能同时编码同一张图，结果一致，属于可接受的重复计算。
var (
	assetCache sync.Map // 逻辑名 → data URI
	svgCache   sync.Map // 逻辑名 → 内联 SVG 源码（已注入 class 之前的原始形态）
)

// init 在包初始化阶段校验素材表，属于开发期错误检查：路径写错只会表现为
// 「卡片上少一张图」，很难在使用中发现，所以宁可在启动时就带中文原因 panic。
func init() {
	mustValidateAssets(assetFS, assetFiles)
	if assetDataURI(fontAssetName) == "" {
		panic("素材加载失败：缺少卡片标题字体 " + fontAssetName)
	}
}

// mustValidateAssets 校验素材表与内置文件一一对应。
// 双向校验：表里登记的文件必须存在且扩展名受支持，内置素材也必须都被登记过。
// 传入 fs.FS 而不是直接用 assetFS，是为了让单测能注入残缺的素材表覆盖失败分支。
func mustValidateAssets(fsys fs.FS, table map[string]string) {
	if len(table) == 0 {
		panic("素材加载失败：素材表为空")
	}

	registered := make(map[string]bool, len(table))
	for name, dst := range table {
		if !assetExts[strings.ToLower(path.Ext(dst))] {
			panic(fmt.Sprintf("素材加载失败：逻辑名 %s 指向的 %s 不是受支持的素材格式（只认 .png / .svg / .ttf）", name, dst))
		}
		if _, err := fs.ReadFile(fsys, dst); err != nil {
			panic(fmt.Sprintf("素材加载失败：逻辑名 %s 指向的 %s 无法从内置文件系统读出：%v", name, dst, err))
		}
		registered[dst] = true
	}

	// 递归走完 assets 整棵树，而不是只扫 assets/*/*：漏掉一层文件（assets/xxx.png）的后果
	// 同样是「卡片上少一张图」，很难在使用中发现，多走一层目录的成本可以忽略。
	// 非素材文件（例如 assets/LICENSES.md）不算素材，跳过不校验。
	err := fs.WalkDir(fsys, "assets", func(dst string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !assetExts[strings.ToLower(path.Ext(dst))] {
			return nil
		}
		if !registered[dst] {
			panic(fmt.Sprintf("素材加载失败：内置素材 %s 没有登记逻辑名（见 assets.go 的 assetFiles）", dst))
		}
		return nil
	})
	if err != nil {
		panic(fmt.Sprintf("素材加载失败：扫描 assets 目录出错：%v", err))
	}
}

// assetPrefix 返回素材路径对应的 data URI 前缀；扩展名不受支持时返回空串。
func assetPrefix(dst string) string {
	switch strings.ToLower(path.Ext(dst)) {
	case ".png":
		return pngDataURIPrefix
	case ".svg":
		return svgDataURIPrefix
	case ".ttf":
		return ttfDataURIPrefix
	default:
		return ""
	}
}

// assetDataURI 返回素材的 data URI（string 形式，便于 Go 侧复用与断言）。
// 未知逻辑名或读取失败都返回空串：模板里表现为这张图不显示，而不是让整张卡片渲染失败——
// 一张徽标不值得把整个命令拖垮。
func assetDataURI(name string) string {
	if cached, ok := assetCache.Load(name); ok {
		return cached.(string)
	}
	raw, ok := readAsset(name)
	if !ok {
		return ""
	}
	uri := assetPrefix(assetFiles[name]) + base64.StdEncoding.EncodeToString(raw)
	assetCache.Store(name, uri)
	return uri
}

// readAsset 读内置素材；逻辑名没登记或文件读不出来时返回 false（调用方按「没有这张图」处理）。
func readAsset(name string) ([]byte, bool) {
	dst, ok := assetFiles[name]
	if !ok {
		return nil, false
	}
	raw, err := fs.ReadFile(assetFS, dst)
	if err != nil {
		// init 已经校验过路径可读，走到这里说明内置素材被改坏了；只记日志不 panic。
		log.Printf("[render] 读取内置素材 %s（逻辑名 %s）失败：%v", dst, name, err)
		return nil, false
	}
	return raw, true
}

// assetFunc 是模板里 asset / datauri 的实现：返回素材的 data URI。
//
// 必须返回 template.URL 而不是 string：html/template 只放行 http/https/mailto 这几种 scheme，
// 字符串形式的 data: URL 在 src 属性里会被替换成 #ZgotmplZ，图片直接不显示。
func assetFunc(name string) template.URL {
	return template.URL(assetDataURI(name))
}

// dataImgFunc 是模板里 {{dataimg ...}} 的实现：入参是插件用 ImageDataURI 算好的 data URI。
// 与 assetFunc 同理，必须返回 template.URL —— 字符串形式的 data: URL 在 src 属性里会被
// html/template 替换成 #ZgotmplZ，图片直接不显示。空串原样返回，模板据此不渲染这张图。
func dataImgFunc(uri string) template.URL {
	return template.URL(uri)
}

// 内联 SVG 时用到的正则。参考站点的图标都是 wiki 导出的 SVG：
// 可能带 XML 声明与 DOCTYPE，根元素上可能写死 width/height（如 1024×1024），
// 图内还会用 clipPath id="a" 这种极短 id —— 同一张卡片上内联多个图标就会互相串。
var (
	svgDeclRE      = regexp.MustCompile(`(?is)<\?xml.*?\?>`)
	svgDocTypeRE   = regexp.MustCompile(`(?is)<!DOCTYPE[^>]*>`)
	svgRootRE      = regexp.MustCompile(`(?is)<svg\b[^>]*>`)
	svgIDAttrRE    = regexp.MustCompile(`\bid="([^"]*)"`)
	svgURLRefRE    = regexp.MustCompile(`url\(#([^)]*)\)`)
	svgSizeAttrRE  = regexp.MustCompile(`(?i)\s+(width|height)="[^"]*"`)
	svgClassAttrRE = regexp.MustCompile(`(?i)\s+class="[^"]*"`)
)

// iconFunc 是模板里的 {{icon "逻辑名" "class"}}：按素材类型输出 <img>（位图）或内联 <svg>（矢量）。
//
// 为什么要内联 SVG：参考站点的阵营/战术图标是**纯黑剪影**，放进 <img> 在黑底卡片上等于看不见；
// 内联之后 fill 由 CSS 继承（.emblem { fill: var(--yellow) }），才能按卡片主题着色。
// class 可选：位图写在 <img> 上，矢量写在 <svg> 根元素上。
// 未知逻辑名返回空串（页面少一个图标，而不是整张卡片渲染失败）。
func iconFunc(name string, class ...string) template.HTML {
	dst, ok := assetFiles[name]
	if !ok {
		return ""
	}
	cls := ""
	if len(class) > 0 {
		cls = strings.TrimSpace(class[0])
	}
	// 卡片头徽标按阵营着色；不要把一个通用 CSS fill 套到所有徽标上。
	fill := map[string]string{
		"emblem.super_earth": "#5BA3D0",
		"emblem.automaton":   "#E74C3C",
		"emblem.terminids":   "#F5C518",
		"emblem.illuminate":  "#CF64F8",
	}[name]
	if strings.ToLower(path.Ext(dst)) != ".svg" {
		// 位图：data URI 走 <img>，class 直接写在标签上。
		uri := assetDataURI(name)
		if uri == "" {
			return ""
		}
		if cls == "" {
			return template.HTML(`<img src="` + uri + `" alt="">`)
		}
		return template.HTML(`<img class="` + cls + `" src="` + uri + `" alt="">`)
	}
	inline, ok := inlineSVG(name)
	if !ok {
		return ""
	}
	if cls == "" {
		return template.HTML(inline)
	}
	// class 注入到根 <svg> 上：向量图标不能像 <img> 那样在外面套 class（那管不到 fill 继承）。
	root := renameSVGRoot(inline, cls)
	if fill != "" && strings.Contains(cls, "card__emblem") {
		root = strings.Replace(root, "<svg ", `<svg style="fill:`+fill+`" `, 1)
	}
	return template.HTML(root)
}

// inlineSVG 返回去掉了 XML 声明/DOCTYPE、并把内部 id 加上逻辑名前缀的 SVG 源码。
// id 加前缀是为了让同一张卡片上内联的多个图标不打架（上游 SVG 里有 id="a" 这种极短名字）。
func inlineSVG(name string) (string, bool) {
	if cached, ok := svgCache.Load(name); ok {
		return cached.(string), true
	}
	raw, ok := readAsset(name)
	if !ok {
		return "", false
	}
	text := svgDeclRE.ReplaceAllString(string(raw), "")
	text = svgDocTypeRE.ReplaceAllString(text, "")
	prefix := "ic-" + svgNamePrefix(name) + "-"
	text = svgIDAttrRE.ReplaceAllString(text, `id="`+prefix+`$1"`)
	text = svgURLRefRE.ReplaceAllString(text, "url(#"+prefix+"$1)")
	text = strings.TrimSpace(text)
	svgCache.Store(name, text)
	return text, true
}

// renameSVGRoot 把根 <svg> 标签重写成「去掉写死的 width/height、挂上调用方给的 class」。
// 尺寸交给 CSS 控制，否则 SVG 会按自带尺寸（可能 1024×1024）渲染，把卡片撑爆。
func renameSVGRoot(svg string, cls string) string {
	return svgRootRE.ReplaceAllStringFunc(svg, func(tag string) string {
		cleaned := svgSizeAttrRE.ReplaceAllString(tag, "")
		cleaned = svgClassAttrRE.ReplaceAllString(cleaned, "")
		return strings.TrimSuffix(cleaned, ">") + ` class="` + cls + `">`
	})
}

// svgNamePrefix 把逻辑名转成能安全放进 id 的前缀（点号换成横杠）。
func svgNamePrefix(name string) string {
	return strings.NewReplacer(".", "-", "_", "-").Replace(name)
}

// fontFaceFunc 是模板里的 {{fontface}}：把随二进制发布的标题字体（ZZZ-thick）内联成 @font-face。
//
// 为什么必须内联：渲染走 page.SetContent，页面没有 base URL，外链字体取不到；
// 而 system 字体里没有这个字形（它是参考站点随站点一起分发的标题字）。
// 返回值必须是 template.CSS，否则 <style> 里的 url(data:...) 会被 html/template 的 CSS 过滤器吃掉。
func fontFaceFunc() template.CSS {
	uri := assetDataURI(fontAssetName)
	if uri == "" {
		return ""
	}
	return template.CSS(`@font-face{font-family:"ZZZ-thick";font-style:normal;font-weight:400;src:url("` + uri + `") format("truetype");}`)
}

// templateFuncs 返回所有卡片模板共用的函数表。
//   - asset / datauri：素材的 data URI（位图与矢量都能用，写 <img src> 时用它）；
//   - icon：位图输出 <img>、矢量内联 <svg>（需要 CSS 着色时用它）；
//   - dataimg：运行时才拿到的图（见 image.go），入参已经是完整的 data URI；
//   - fontface：标题字体的 @font-face 片段。
func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"asset":    assetFunc,
		"datauri":  assetFunc,
		"icon":     iconFunc,
		"dataimg":  dataImgFunc,
		"fontface": fontFaceFunc,
	}
}
