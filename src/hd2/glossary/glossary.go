// Package glossary 提供《绝地潜兵 2》官方简中术语表的只读查询能力，
// 供「行内查询搜星球」与「卡片中文显示」等上层功能共用。
//
// 数据来源与许可：data/*.json 取自开源项目 astrbot_plugin_Helldivers
// （MIT License，作者 fiatlux2333）的 assets/glossary/ 目录，是可查询的
// 扁平 {英文小写: 中文} 译名表，原样使用、未做修改。
// 《Helldivers》及相关名称是 Arrowhead Game Studios / Sony Interactive Entertainment
// 的商标或知识产权；本仓库是非官方粉丝作品，与上述公司无关联。
//
// 加载策略：数据用 go:embed 打进二进制，在包 init 阶段一次性全部加载（fail-fast）。
// 这里刻意不用 sync.Once 懒加载：同步机制在第一次 panic 之后已置为 done，后续查询会
// 拿着「只加载了一部分」的词表静默退回英文名，与本包「不静默降级」的约定冲突。
// 因此任一数据文件读取或解析失败都直接 panic 并带中文原因，属于开发期错误。
// 加载完成后词表只读，本包不做任何网络请求，也不支持运行时替换数据。
package glossary

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// dataFS 把四张译名表打进二进制，仓库里删掉文件会在编译期就报错。
//
//go:embed data/*.json
var dataFS embed.FS

// defaultSearchLimit 是 SearchPlanets 在 limit <= 0 时使用的默认条数上限。
const defaultSearchLimit = 10

// placeholderSuffixes 列出数据文件里的占位条目后缀。
// 上游同一颗星球会同时给出正式键与占位键（如 uvp alpha / uvp alpha (void) /
// uvp alpha (unused) 都译作 UVP阿尔法），检索时若不过滤会让同一颗星球重复出现。
// 这里选择只在检索时过滤、不改动数据文件，以便词表与上游保持一致。
var placeholderSuffixes = []string{" (void)", " (unused)"}

var (
	planets    map[string]string
	sectors    map[string]string
	enemies    map[string]string
	stratagems map[string]string
)

// init 在包初始化阶段同步加载四张词表，任一文件读取或解析失败立即 panic。
func init() {
	planets = mustLoadTable(dataFS, "planets.json")
	sectors = mustLoadTable(dataFS, "sectors.json")
	enemies = mustLoadTable(dataFS, "enemies.json")
	stratagems = mustLoadTable(dataFS, "stratagems.json")
}

// mustLoadTable 从给定文件系统读取并解析单个译名表文件（位于 data/ 目录下）。
// 文件缺失、JSON 损坏或词表为空都会 panic，panic 值是以「译名表加载失败」开头的中文说明；
// 传入 fs.FS 而不是直接用 dataFS，是为了让单测能注入损坏数据覆盖这些失败分支。
func mustLoadTable(fsys fs.FS, name string) map[string]string {
	path := "data/" + name
	raw, err := fs.ReadFile(fsys, path)
	if err != nil {
		panic(fmt.Sprintf("译名表加载失败：无法读取内置文件 %s：%v", path, err))
	}
	table := make(map[string]string)
	if err := json.Unmarshal(raw, &table); err != nil {
		panic(fmt.Sprintf("译名表加载失败：解析 %s 出错：%v", path, err))
	}
	if len(table) == 0 {
		panic(fmt.Sprintf("译名表加载失败：%s 没有任何词条", path))
	}
	return table
}

// Planet 返回星球名的官方简中译名；未收录时返回入参原文（绝不返回空串，
// 否则卡片与行内结果会出现空白名字）。匹配忽略首尾空白与大小写。
func Planet(name string) string {
	return translate(planets, name)
}

// Sector 返回分区名的官方简中译名；未收录时返回入参原文。匹配忽略首尾空白与大小写。
func Sector(name string) string {
	return translate(sectors, name)
}

// Enemy 返回敌人（单位）名的官方简中译名；未收录时返回入参原文。匹配忽略首尾空白与大小写。
func Enemy(name string) string {
	return translate(enemies, name)
}

// Stratagem 返回战术配备名的官方简中译名；未收录时返回入参原文。匹配忽略首尾空白与大小写。
func Stratagem(name string) string {
	return translate(stratagems, name)
}

// translate 在给定词表里做一次精确查表：折叠首尾空白与大小写后命中即返回中文，
// 未命中返回入参原文。空词条按未命中处理，避免返回空白译名。
func translate(table map[string]string, name string) string {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		return name
	}
	if chinese, ok := table[key]; ok && chinese != "" {
		return chinese
	}
	return name
}

// isPlaceholder 判断英文键是否为上游数据里的占位条目（"(void)" / "(unused)" 结尾）。
func isPlaceholder(english string) bool {
	for _, suffix := range placeholderSuffixes {
		if strings.HasSuffix(english, suffix) {
			return true
		}
	}
	return false
}

// PlanetHit 是一条星球检索命中项，同时带英文名与官方简中译名，供行内查询构造文案。
type PlanetHit struct {
	English string
	Chinese string
}

// hasPrefix 判断该命中项是否以折叠后的关键字开头；英文名与中文名都算
// （中文名里含 "IV"、"UVP" 之类的拉丁片段，因此中文侧同样要折叠大小写）。
func (h PlanetHit) hasPrefix(keyword string) bool {
	return strings.HasPrefix(h.English, keyword) || strings.HasPrefix(strings.ToLower(h.Chinese), keyword)
}

// SearchPlanets 按关键字检索行星名：中文关键字匹配中文名、英文关键字匹配英文名，
// 两侧都做首尾空白裁剪与大小写折叠（中文名里的 "IV"、"UVP" 等拉丁片段同样折叠）。
//
// 关键字裁剪后为空时返回空切片（不会返回整张表，避免行内结果刷屏）。
// 结果会跳过上游的 "(void)" / "(unused)" 占位条目，避免同一颗星球重复出现。
// 排序规则为前缀命中优先、其次英文名升序，保证同一关键字的结果顺序稳定。
// limit 是最大返回条数，limit <= 0 视为 10；命中不足时按实际条数返回。
func SearchPlanets(keyword string, limit int) []PlanetHit {
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	hits := make([]PlanetHit, 0, limit)
	kw := strings.ToLower(strings.TrimSpace(keyword))
	if kw == "" {
		return hits
	}
	for english, chinese := range planets {
		if isPlaceholder(english) {
			continue
		}
		if !strings.Contains(english, kw) && !strings.Contains(strings.ToLower(chinese), kw) {
			continue
		}
		hits = append(hits, PlanetHit{English: english, Chinese: chinese})
	}
	sort.Slice(hits, func(i, j int) bool {
		prefixI := hits[i].hasPrefix(kw)
		prefixJ := hits[j].hasPrefix(kw)
		if prefixI != prefixJ {
			return prefixI
		}
		return hits[i].English < hits[j].English
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

// Terms 返回全部译名条目（英文原词 → 简体中文），供「送译前的专有名词预替换」使用。
// 返回的是副本：调用方改写不会影响内置词表。空词条不会出现在结果里。
func Terms() map[string]string {
	total := len(planets) + len(sectors) + len(enemies) + len(stratagems)
	out := make(map[string]string, total)
	for _, table := range []map[string]string{planets, sectors, enemies, stratagems} {
		for english, chinese := range table {
			if english == "" || chinese == "" {
				continue
			}
			out[english] = chinese
		}
	}
	return out
}
