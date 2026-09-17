// 本文件是「上游键名 → 中文标签」与「值替换」的唯一一份表。
// 键名取自参照站详情数据里实际出现过的全部取值（实测扫描 89 件武器 + 109 件战备的 detailed_stats），
// 没收录的键原样显示：上游随时会加新字段，宁可显示英文键名，也不要猜错含义。
package atlas

import "strings"

// statLabels 是详情数值的键名中文表。
var statLabels = map[string]string{
	// 武器基础
	"capacity":           "弹匣容量",
	"spare_magazines":    "备用弹匣",
	"starting_magazines": "初始弹匣",
	"mags_from_supply":   "补给弹匣",
	"mags_from_ammo_box": "弹药箱弹匣",
	"fire_rate":          "射速",
	"recoil":             "后坐力",
	"recoil_horizontal":  "水平后坐力",
	"recoil_vertical":    "垂直后坐力",
	"spread":             "散布",
	"sway":               "晃动",
	"ergonomics":         "人体工学",
	// 弹体
	"projectile":           "弹体",
	"mass":                 "弹体质量",
	"initial_velocity":     "初速度",
	"drag_factor":          "阻力系数",
	"gravity_factor":       "重力系数",
	"penetration_slowdown": "穿透减速",
	"explosion_on_impact":  "命中引爆",
	"explode_after":        "延时引爆",
	"lifetime":             "存活时间",
	"arc":                  "弹道弧度",
	"pellets":              "弹丸数",
	// 伤害
	"damage":          "伤害",
	"damage_standard": "标准伤害",
	"damage_durable":  "耐久伤害",
	"damage_element":  "伤害类型",
	"element":         "伤害类型",
	"inner_radius":    "内圈伤害",
	"outer_radius":    "外圈伤害",
	"inner_durable":   "内圈耐久伤害",
	"outer_durable":   "外圈耐久伤害",
	"main_health":     "主体生命值",
	"main_armor":      "主体装甲",
	"aoe_duration":    "范围持续时间",
	"shrapnel":        "破片",
	"shrapnel_count":  "破片数量",
	// 穿透
	"penetration":       "穿透",
	"pen_direct":        "直射穿透",
	"pen_slight_angle":  "小角度穿透",
	"pen_large_angle":   "大角度穿透",
	"pen_extreme_angle": "极端穿透",
	"pen_aoe":           "范围穿透",
	// 特殊效果与手感
	"special_effects":  "特殊效果",
	"demolition_force": "拆毁值",
	"stagger_force":    "踉跄力",
	"push_force":       "推力",
	// 范围与投放
	"area_of_effect":   "范围效果",
	"radius_inner":     "内圈半径",
	"radius_outer":     "外圈半径",
	"radius_shockwave": "冲击波半径",
	"bombardment_area": "炮击区域范围",
	"bombs":            "炸弹数量",
	"salvos":           "齐射次数",
	"cooldown":         "冷却时间",
	"uses":             "使用次数",
	// 状态效果
	"status_effects":  "状态效果",
	"status":          "状态效果",
	"status_strength": "状态强度",
	"status_duration": "状态持续时间",
	"effect_type":     "状态类型",
	"second_status":   "第二状态",
	"third_status":    "第三状态",
	// 上游在攻击条目里给的名字与类型（不是数值，但摊平时会用到）
	"name": "名称",
	"type": "类型",
}

// labelOf 返回键名的中文标签；没收录时原样返回（下划线换成空格），绝不返回空串。
func labelOf(key string) string {
	if label, ok := statLabels[key]; ok {
		return label
	}
	return strings.ReplaceAll(key, "_", " ")
}

// categoryNames 是武器大类的中文名：上游 weapons.json 的 category 是英文键
// （primary / secondary / throwables），战备那边给的已经是中文分类标签，这里只管武器。
var categoryNames = map[string]string{
	"primary":    "主武器",
	"secondary":  "副武器",
	"throwables": "投掷物",
	"grenade":    "投掷物",
}

// categoryName 返回武器大类的中文名；认不出时原样返回。
func categoryName(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if zh, ok := categoryNames[strings.ToLower(trimmed)]; ok {
		return zh
	}
	return trimmed
}

// componentTypeNames 是攻击条目 type 的中文名。
var componentTypeNames = map[string]string{
	"projectile":  "弹道",
	"explosion":   "爆炸",
	"beam":        "激光",
	"arc":         "电弧",
	"gas":         "毒气",
	"fire":        "火焰",
	"melee":       "近战",
	"shrapnel":    "破片",
	"status":      "状态",
	"projectile_": "弹道",
}

// componentTypeName 返回攻击类型的中文名；认不出时原样返回。
func componentTypeName(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if zh, ok := componentTypeNames[strings.ToLower(trimmed)]; ok {
		return zh
	}
	return trimmed
}

// valueReplacements 是数值里常见英文词的中文替换（长词在前，避免「Anti-Tank」被「Tank」这类短词先吃掉）。
var valueReplacements = [][2]string{
	{"Requisition Slips", "申购点"},
	{"Anti-Tank", "反坦克"},
	{"Ballistic", "弹道"},
	{"Explosion", "爆炸"},
	{"Arc", "电弧"},
	{"Gas", "毒气"},
	{"Acid", "酸液"},
	{"Fire", "火焰"},
	{"Laser", "激光"},
	{"Melee", "近战"},
	{"Unarmored", "无装甲"},
	{"Light Armor", "轻甲"},
	{"Medium Armor", "中甲"},
	{"Heavy Armor", "重甲"},
	{"Light", "轻甲"},
	{"Medium", "中甲"},
	{"Heavy", "重甲"},
	{"Medals", "勋章"},
	{"None", "无"},
}
