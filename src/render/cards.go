package render

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"path"
	"strings"
)

// templatesFS 把卡片模板打进二进制；模板文件被删掉会在编译期就报错。
//
//go:embed templates/*.tmpl
var templatesFS embed.FS

const (
	// templatesDir 是模板在内置文件系统里的目录。
	templatesDir = "templates"
	// baseTemplateName 既是外壳模板名，也是 base.tmpl 的文件名（去掉扩展名）。
	baseTemplateName = "base"
	// cardBodyTemplateName 是卡片内容片段的固定名字，每个卡片文件都必须定义它。
	cardBodyTemplateName = "body"
)

// cardTemplates 是「卡片名 → 模板集合」的注册表，卡片名就是模板文件名（smoke.tmpl → "smoke"）。
// 在包初始化阶段一次性加载，模板缺失或语法错误直接 panic（与 glossary 的 fail-fast 一致）：
// 少了 body 片段这类问题，宁可进程起不来，也不要等到某个命令被使用时才崩。
var cardTemplates = mustLoadCards(templatesFS)

// Card 是一张待渲染的卡片：Name 决定用哪个模板，Data 是视图模型（文案已本地化）。
// Data 必须实现 CardShell（内嵌 Meta 即可），因为 base.tmpl 的标题与数据时间从它取。
type Card struct {
	Name string
	Data any
}

// Meta 是卡片外壳需要的公共字段；卡片视图模型内嵌它即可满足 CardShell，
// base.tmpl 里的 .Title / .Subtitle / .DataTime / .Stale / .Emblem 都来自这里。
type Meta struct {
	Title    string // 卡片标题，例如「银河战况」
	Subtitle string // 可选副标题，空串不显示
	DataTime string // 数据时间，调用方已按本地时区格式化；空串不显示
	Stale    bool   // 数据是否来自过期快照，true 时显示「数据可能已过期」角标
	Emblem   string // 可选徽标的素材逻辑名（见 assets.go 的 assetFiles），空串不显示
}

// meta 让 Meta 满足 CardShell；刻意不导出，避免别的包用自定义实现伪造外壳字段。
func (m Meta) meta() Meta { return m }

// CardShell 是卡片视图模型必须满足的约定：内嵌 Meta 就自动满足。
// 有这道检查，视图模型写错会得到一句「必须内嵌 render.Meta」的中文错误，
// 而不是模板执行时报出的 can't evaluate field Title。
type CardShell interface {
	meta() Meta
}

// SmokeCard 是 templates/smoke.tmpl 的视图模型：用最少的字段把整条渲染链路跑通，
// 供冒烟用例和「排查是环境问题还是卡片问题」时使用。
type SmokeCard struct {
	Meta
	Lines []string // 逐行文本，用来验证转义与列表排版
}

// mustLoadCards 从内置模板目录构建卡片注册表。
//
// 模板约定（写新卡片时必须遵守）：
//   - 一张卡片一个文件，文件名就是卡片名（war.tmpl → 卡片 "war"）；
//   - 每个卡片文件必须定义 {{define "body"}}，base.tmpl 通过它把外壳套在卡片内容上；
//   - 外壳、样式与通用 class 只在 base.tmpl 里维护，卡片只输出内容片段。
//
// 传入 fs.FS 而不是直接用 templatesFS，是为了让单测能注入残缺的模板集合覆盖失败分支。
func mustLoadCards(fsys fs.FS) map[string]*template.Template {
	files, err := fs.Glob(fsys, templatesDir+"/*.tmpl")
	if err != nil {
		panic(fmt.Sprintf("模板加载失败：扫描 %s 目录出错：%v", templatesDir, err))
	}

	basePath := path.Join(templatesDir, baseTemplateName+".tmpl")
	cards := make(map[string]*template.Template)
	for _, file := range files {
		name := strings.TrimSuffix(path.Base(file), ".tmpl")
		if name == baseTemplateName {
			continue // base.tmpl 是外壳，不是卡片
		}

		// 先单独解析卡片文件：既能提前暴露语法错误，也能拦住「卡片里重复定义 base」
		// 这种会静默覆盖外壳、让所有卡片同时走样的写法。
		// 带上 templateFuncs：卡片模板会直接用 {{asset "…"}} 这类函数，
		// 探针若没有函数表会报「function not defined」，把正常模板误判成坏模板。
		probe, err := template.New("probe").Funcs(templateFuncs()).ParseFS(fsys, file)
		if err != nil {
			panic(fmt.Sprintf("模板加载失败：解析 %s 出错：%v", file, err))
		}
		if probe.Lookup(baseTemplateName) != nil {
			panic(fmt.Sprintf("模板加载失败：%s 不应重新定义 %q 外壳（外壳只在 %s 里维护）", file, baseTemplateName, basePath))
		}

		tmpl, err := template.New(baseTemplateName).Funcs(templateFuncs()).ParseFS(fsys, basePath, file)
		if err != nil {
			panic(fmt.Sprintf("模板加载失败：解析 %s 出错：%v", file, err))
		}
		if tmpl.Lookup(baseTemplateName) == nil {
			panic(fmt.Sprintf("模板加载失败：%s 缺少 base 外壳", basePath))
		}
		if tmpl.Lookup(cardBodyTemplateName) == nil {
			panic(fmt.Sprintf("模板加载失败：%s 必须定义 {{define %q}} 的内容片段", file, cardBodyTemplateName))
		}
		cards[name] = tmpl
	}

	if len(cards) == 0 {
		panic("模板加载失败：" + templatesDir + " 目录下没有任何卡片模板")
	}
	return cards
}
