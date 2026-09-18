package orders

import (
	"fmt"
	"strings"

	"hd2_bot/src/hd2"
	"hd2_bot/src/hd2/glossary"
	"hd2_bot/src/plugins/plugutil"
)

// 本文件把重要指令里的枚举数字解成中文：任务类型、values/valueTypes 的对位关系、
// 阵营编号与星球索引。
//
// 依据：上游 /assignments 只给数字，没有官方文档说明含义。下面几张表取自社区长期实测口径，
// 与两个参考项目一致：
//   - HD2-Galatic_war-Map/index.html 的 TASK_TYPE_CN 与 decodeTask()（注释写明「来自社区经验」）；
//   - HD2 Companion 前端（该站在重要指令区块用同一套口径解码）。
// 两处都把「消灭敌人」的阵营放在 valueType 1、把其它类型的阵营放在 valueType 6，本文件照此实现。
//
// 底线不变：表里没有的枚举一律按原值展示（见 decodeAssignmentTask 的兜底分支），不猜。
// 上游哪天改了语义，改的应该是这里的表，而不是在渲染层临时拼字符串。

// 任务类型编号（上游 Task.type）。
const (
	taskTypeCapturePlanet    = 1  // 解放/攻占星球
	taskTypeCollectSamples   = 2  // 采集样本
	taskTypeKillEnemies      = 3  // 消灭敌人（最常见的 MO）
	taskTypeCompleteMissions = 9  // 完成任务数
	taskTypeLiberatePlanet   = 11 // 解放星球
	taskTypeRaiseFlag        = 12 // 升起/引爆信号弹
)

// values/valueTypes 里真正参与展示的几位。上游一次任务会给十来个 valueType
// （2 阵营相关值、4 max、5 次要索引、8/9 数量、11 物品状态……），没用到的不在这里列：
// 少写几位没人用的解码，比写错一位更安全。
const (
	valueTypeFaction = 1  // 消灭敌人（type=3）时表示阵营
	valueTypeGoal    = 3  // 目标值（要消灭/解放/采集多少）
	valueTypeRace    = 6  // 阵营（1=人类 2=终结族 3=机器人 4=光能族）
	valueTypePlanet  = 12 // 星球索引（0 表示不限星球，不是超级地球）
)

// assignmentTaskTypeNames 是任务类型 → 中文名；不在表里的类型按「未知任务类型 N」原值展示。
var assignmentTaskTypeNames = map[int]string{
	taskTypeCapturePlanet:    "解放/攻占星球",
	taskTypeCollectSamples:   "采集样本",
	taskTypeKillEnemies:      "消灭敌人",
	taskTypeCompleteMissions: "完成任务数",
	taskTypeLiberatePlanet:   "解放星球",
	taskTypeRaiseFlag:        "升起/引爆信号弹",
}

// assignmentFactionKeys 是阵营编号 → plugutil 派系表的 key。
// 复用派系表（而不是在这里再抄一份中文名）的理由：星球卡、事件卡与指令卡说的是同一批敌人，
// 两处各维护一份迟早会出现「同一派系两种叫法」。
var assignmentFactionKeys = map[int64]string{
	1: "Humans",
	2: "Terminids",
	3: "Automaton",
	4: "Illuminate",
}

// enemyFactionIDs 是能写进任务标题的阵营编号。编号 1（人类）不算：参考项目对 type=3
// 只在阵营为 2/3/4 时才拼「消灭X敌人」，否则卡片上会出现「消灭超级地球敌人」这种话。
var enemyFactionIDs = map[int64]bool{2: true, 3: true, 4: true}

// needsPlanetNames 判断这批指令里有没有任务带星球索引（valueType 12 且不为 0）：
// 没有就不必为了画卡片再去抓一次星球列表——多数「消灭敌人」型指令都不带星球。
func needsPlanetNames(list []hd2.Assignment) bool {
	for _, a := range list {
		for _, task := range a.Tasks {
			if index, ok := taskValue(task, valueTypePlanet); ok && index != 0 {
				return true
			}
		}
	}
	return false
}

// decodeAssignmentTasks 逐条解码任务，并把上游的进度数组按同样的下标对上：
// 实测 progress[i] 就是 tasks[i] 的当前值（例如进度 [149671345, 88572031] 对应两条「消灭敌人」任务）。
// 进度数组比任务短或为 nil 时，对应任务只是没有当前值，不影响别的任务。
// 没有任务时返回 nil：模板与文本回退据此不渲染任务区。
func decodeAssignmentTasks(tasks []hd2.Task, progress []int64, planets PlanetNames) []AssignmentTaskItem {
	if len(tasks) == 0 {
		return nil
	}
	items := make([]AssignmentTaskItem, 0, len(tasks))
	for i, task := range tasks {
		var current *int64
		if i < len(progress) {
			v := progress[i]
			current = &v
		}
		items = append(items, decodeAssignmentTask(task, current, planets))
	}
	return items
}

// decodeAssignmentTask 把一条任务解成卡片上的一行。
// progress 是这条任务的当前值（nil 表示上游没给），planets 是星球索引 → 星球原名
// （nil 表示这次没拿到星球数据，星球索引会退回「星球 #N」）。
func decodeAssignmentTask(task hd2.Task, progress *int64, planets PlanetNames) AssignmentTaskItem {
	typeName, known := assignmentTaskTypeNames[task.Type]
	if !known {
		// 表里没有的类型不猜：标题写明原值，上游给的数值也原样列出来，
		// 至少让人知道「这条任务上游给了什么」，而不是把整条任务吞掉。
		return AssignmentTaskItem{
			Title:   fmt.Sprintf("未知任务类型 %d", task.Type),
			Numbers: rawNumbersText(task.Values),
		}
	}

	item := AssignmentTaskItem{Title: typeName}
	if faction, ok := taskFaction(task); ok {
		if task.Type == taskTypeKillEnemies {
			item.Title = "消灭" + faction + "敌人"
		} else {
			item.Title = typeName + " · 目标：" + faction
		}
	}
	if index, ok := taskValue(task, valueTypePlanet); ok && index != 0 {
		item.Title += " · 星球：" + planetLabel(planets, index)
	}

	goal, hasGoal := taskValue(task, valueTypeGoal)
	if hasGoal && goal <= 0 {
		hasGoal = false // 目标值 0 等于上游没给目标（实测「不限星球」型任务就是这个样子）
	}
	if task.Type == taskTypeLiberatePlanet {
		// type=11 的 values 不是目标值：社区口径是「目标恒为 100（解放度百分比）」。
		// 当前值本该由该星球当时的解放度算出来，但那是 planets 包的活，本插件拿不到星球血量，
		// 所以这里只给目标值与上游给的进度，不自己造一个百分比。
		goal, hasGoal = 100, true
	}

	switch {
	case hasGoal && progress != nil:
		item.Percent = plugutil.PercentOf(*progress, goal)
		item.Numbers = fmt.Sprintf("%s / %s（%s）",
			plugutil.FormatInt(*progress), plugutil.FormatInt(goal), plugutil.PercentText(item.Percent))
		item.Bar = true
	case hasGoal:
		item.Numbers = "目标 " + plugutil.FormatInt(goal)
	case progress != nil:
		item.Numbers = "当前 " + plugutil.FormatInt(*progress)
	}
	return item
}

// taskValue 取这条任务里某个 valueType 对应的值。
// 对位规则只有一份，在 hd2.Task.ValueOf 里（plugins/planets 的兴趣点也用它）；
// 这里保留同名包装是为了让本文件的调用点读起来更短，也保留「取不到就返回 false」的语义。
func taskValue(task hd2.Task, valueType int) (int64, bool) {
	return task.ValueOf(valueType)
}

// taskFaction 解出任务针对的阵营（中文名）。口径见文件头：
// 消灭敌人（type=3）的阵营在 valueType 1 上，其它类型在 valueType 6 上；
// 取不到、值为 0 或编号不在表里时返回 false（卡片上就不写阵营）。
func taskFaction(task hd2.Task) (string, bool) {
	id, ok := int64(0), false
	if task.Type == taskTypeKillEnemies {
		id, ok = taskValue(task, valueTypeFaction)
	}
	if !ok || id == 0 {
		id, ok = taskValue(task, valueTypeRace)
	}
	key, known := assignmentFactionKeys[id]
	if !ok || !known || !enemyFactionIDs[id] {
		return "", false
	}
	return plugutil.FactionName(key), true
}

// planetLabel 把星球索引翻成卡片上的短名：优先官方简中译名，没有译名时退回英文原名。
// 这里不用 plugutil.PlanetDisplayName（它会拼成「中文（English）」）：任务行里已经排了标题与进度，
// 再带一串英文会把一行撑长。索引不在表里（含没抓到星球数据）时退回「星球 #N」——宁可显示编号，
// 也不要编一个名字。
func planetLabel(planets PlanetNames, index int64) string {
	name := strings.TrimSpace(planets[int(index)])
	if name == "" {
		return fmt.Sprintf("星球 #%d", index)
	}
	if zh := strings.TrimSpace(glossary.Planet(name)); zh != "" {
		return zh
	}
	return name
}

// rawNumbersText 在完全不认识的类型上兜底：把上游给的数值原样列出来（「原始数值 1、2」），
// 空数组返回空串，由调用方决定不显示这一行。
func rawNumbersText(values []int64) string {
	if len(values) == 0 {
		return ""
	}
	return "原始数值 " + numbersText(values)
}

// numbersText 把一组数字拼成「1、2」，空集合返回空串。
func numbersText(values []int64) string {
	if len(values) == 0 {
		return ""
	}
	parts := make([]string, 0, len(values))
	for _, v := range values {
		parts = append(parts, plugutil.FormatInt(v))
	}
	return strings.Join(parts, "、")
}
