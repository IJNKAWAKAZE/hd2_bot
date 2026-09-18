package hd2

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
)

// 行动变量（galactic effect）中文对照表。
//
// 数据出处（用户 2026-09-18 要求参照社区站点 HD2 真理部星图页）：
// 由参考项目 HD2-Galatic_war-Map 的 tables/effect_id_cn.json（效果 ID → 短中文名）与
// tables/hd2_variables.json（分类 → 条目：中文名 / 说明 / effect_ids）合并而来，
// 只保留卡片需要的字段，并剔除 DSS 战略行动（tactical_action）分类——那批效果归空间站卡展示。
// 名称与说明与游戏本体一样出自 Arrowhead Game Studios；本仓库是非官方粉丝作品，与该公司无关联。
//
// 加载策略与 glossary 一致：go:embed 打进二进制、init 阶段一次性加载，解析失败直接 panic（开发期错误）。
// 表里查不到的 ID 不是错误：上游常常先于社区对照表出现新效果，卡片对这类效果写「未知行动变量」。
//
//go:embed data/galactic_effects.json
var galacticEffectsJSON []byte

// GalacticEffect 是一条行动变量的中文对照条目。
type GalacticEffect struct {
	Name     string // 短中文名（卡片 chip 上用它）
	Desc     string // 中文说明；对照表没收录说明时是空串
	Category string // 分类中文名（例如「作战限制」「环境条件」）；未收录时是空串
	Negative bool   // 负面效果（作战限制类）：卡片上用红色 chip 标出来
}

// galacticEffects 是效果 ID → 对照条目；加载完成后只读，查询入口见 GalacticEffectOf。
var galacticEffects = loadGalacticEffects()

// loadGalacticEffects 解析内嵌的对照表。
// 任何解析失败都 panic：那是仓库里的数据文件写坏了，属于开发期错误，
// 不该在运行期静默降级成「所有星球都没有行动变量」。
func loadGalacticEffects() map[int]GalacticEffect {
	raw := map[string]GalacticEffect{}
	if err := json.Unmarshal(galacticEffectsJSON, &raw); err != nil {
		panic(fmt.Sprintf("解析行动变量对照表失败：%v", err))
	}
	out := make(map[int]GalacticEffect, len(raw))
	for key, item := range raw {
		id, err := strconv.Atoi(key)
		if err != nil {
			panic(fmt.Sprintf("行动变量对照表里的 ID 不是数字：%q", key))
		}
		out[id] = item
	}
	return out
}

// galacticEffectAliases 是「对照表没收录、但能确定与已收录条目同族」的效果编号。
//
// 依据：社区补充源（Helldivers Companion 前端）里的效果枚举名。同族效果的编号只差一个后缀，
// 例如 1318 是 reinforce_Reduction3（对照表收了）、1272 是 reinforce_Reduction2；
// 1313 是 extract_PilotShortage5（对照表收了）、1363 是 extract_PilotShortage8。
//
// 只写名字、分类与负面标记，**不带说明文案**：同族不同编号的说明里写着具体数值
// （1272 的增援惩罚比 1318 重、班机延误有 30 秒与 40 秒两档），照抄会张冠李戴。
// 名字与分类同族一致，可以直接用。语义上拿不准（只有内部代号、没有同族中文名）的效果一律不写在这里，
// 那类效果在卡片上显示为「未知行动变量」，不猜名字。
var galacticEffectAliases = map[int]GalacticEffect{
	1272: {Name: "预算削减", Category: "作战限制", Negative: true},
	1363: {Name: "班机延误", Category: "作战限制", Negative: true},
}

// GalacticEffectOf 查一个效果 ID 的中文对照：先查对照表，再查同族别名表；
// 两处都没有时返回 false，由调用方决定怎么显示（卡片上是「未知行动变量」）。
func GalacticEffectOf(id int) (GalacticEffect, bool) {
	if item, ok := galacticEffects[id]; ok {
		return item, true
	}
	item, ok := galacticEffectAliases[id]
	return item, ok
}

// PlanetEffect 是一颗星球当前生效的一条行动变量。
// PlanetIndex 与 Planet.Index 同源（星球编号），EffectID 是 galactic effect 的编号。
type PlanetEffect struct {
	PlanetIndex int `json:"planetIndex"`
	EffectID    int `json:"effectId"`
}

// namePlanetEffects 是行动变量在数据层里的缓存名。
// 刻意不写成端点名：这份数据来自补充源，取数路径与主数据源不同（见 Service.PlanetEffects）。
const namePlanetEffects = "companion.planet-effects"

// DecodePlanetEffects 从补充源的整包数据里取出各星球的行动变量。
//
// 只读 warStatus.planetActiveEffects（实测形如 [{"index":256,"galacticEffectId":1190}]）：
// 主数据源的 /planets 没有这个字段，这份数据只能来自补充源。
//
// 编号或效果 ID 缺失的条目直接跳过——脏数据不是错误，但也不该在卡片上留一条空白；
// 同一颗星球重复给同一个效果时只保留一条（实测数据里出现过重复，参考站也是去重后展示的）；
// 输出按（星球编号，效果 ID）排序，同一份数据每次给出同样的顺序，卡片上的 chip 才不会跳。
func DecodePlanetEffects(b []byte) ([]PlanetEffect, error) {
	var raw struct {
		WarStatus struct {
			PlanetActiveEffects []struct {
				Index            int `json:"index"`
				GalacticEffectID int `json:"galacticEffectId"`
			} `json:"planetActiveEffects"`
		} `json:"warStatus"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(b), &raw); err != nil {
		return nil, fmt.Errorf("解析行动变量失败：%w", err)
	}
	list := raw.WarStatus.PlanetActiveEffects
	out := make([]PlanetEffect, 0, len(list))
	seen := make(map[PlanetEffect]bool, len(list))
	for _, item := range list {
		if item.Index <= 0 || item.GalacticEffectID <= 0 {
			continue
		}
		effect := PlanetEffect{PlanetIndex: item.Index, EffectID: item.GalacticEffectID}
		if seen[effect] {
			continue
		}
		seen[effect] = true
		out = append(out, effect)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PlanetIndex != out[j].PlanetIndex {
			return out[i].PlanetIndex < out[j].PlanetIndex
		}
		return out[i].EffectID < out[j].EffectID
	})
	return out, nil
}
