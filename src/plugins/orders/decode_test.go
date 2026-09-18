package orders

import (
	"testing"

	"hd2_bot/src/hd2"
)

// liveTask 造一条实测形状的上游任务：十位 valueTypes，与 values 按下标对齐。
// 实测口径：消灭敌人（type=3）的阵营在 valueType 1、目标值在 valueType 3、星球索引在 valueType 12；
// 其它类型的阵营在 valueType 6。用例统一用它造数据，免得每处各写一份数字。
func liveTask(taskType int, faction, goal, planet int64) hd2.Task {
	return hd2.Task{
		Type:       taskType,
		Values:     []int64{faction, 0, goal, 0, 0, 0, 0, 0, 0, planet},
		ValueTypes: []int{1, 2, 3, 4, 6, 5, 8, 9, 11, 12},
	}
}

// TestDecodeAssignmentTaskKillEnemies 校验最常见的「消灭敌人」：
// 阵营进任务名，目标值与当前进度一起进数值，并画进度条。
func TestDecodeAssignmentTaskKillEnemies(t *testing.T) {
	current := int64(149671345)
	item := decodeAssignmentTask(liveTask(3, 2, 1500000000, 0), &current, nil)

	if item.Title != "消灭终结族敌人" {
		t.Errorf("任务名应带阵营，实际 %q", item.Title)
	}
	if item.Numbers != "149,671,345 / 1,500,000,000（10.0%）" {
		t.Errorf("数值应为「当前 / 目标（百分比）」，实际 %q", item.Numbers)
	}
	if !item.Bar || item.Percent != 10 {
		t.Errorf("有当前值也有目标值时应画进度条，实际 bar=%v percent=%v", item.Bar, item.Percent)
	}
}

// TestDecodeAssignmentTaskFactionFromValueTypeSix 校验非「消灭敌人」的任务从 valueType 6 取阵营：
// type≠3 时 valueType 1 是「标记」，当成阵营就会把任务目标解错。
func TestDecodeAssignmentTaskFactionFromValueTypeSix(t *testing.T) {
	task := hd2.Task{
		Type:       11,
		Values:     []int64{0, 0, 0, 0, 4, 0, 0, 0, 0, 0},
		ValueTypes: []int{1, 2, 3, 4, 6, 5, 8, 9, 11, 12},
	}
	item := decodeAssignmentTask(task, nil, nil)

	if item.Title != "解放星球 · 目标：光能者" {
		t.Errorf("阵营应读 valueType 6，实际 %q", item.Title)
	}
	// 解放星球（type=11）的 values 不是目标值：社区口径是目标恒为 100。
	if item.Numbers != "目标 100" {
		t.Errorf("解放星球的目标值应为 100，实际 %q", item.Numbers)
	}
	if item.Bar {
		t.Error("上游没给当前值时不该画进度条")
	}
}

// TestDecodeAssignmentTaskPlanetIndex 校验星球索引（valueType 12）翻成简中名，
// 索引 0 与没有星球数据两种情况都不能编名字。
func TestDecodeAssignmentTaskPlanetIndex(t *testing.T) {
	planets := PlanetNames{5: "Malevelon Creek"}

	if got := decodeAssignmentTask(liveTask(2, 0, 1000, 5), nil, planets).Title; got != "采集样本 · 星球：麦拉芬蒙河" {
		t.Errorf("星球名应取官方简中译名，实际 %q", got)
	}
	// 索引 0 = 不限星球（实测「消灭敌人」全给 0），不能翻成 index 0 的那颗星球。
	if got := decodeAssignmentTask(liveTask(2, 0, 1000, 0), nil, planets).Title; got != "采集样本" {
		t.Errorf("星球索引为 0 时不该带星球，实际 %q", got)
	}
	// 这次没抓到星球列表（planets 为 nil）时退回编号。
	if got := decodeAssignmentTask(liveTask(2, 0, 1000, 5), nil, nil).Title; got != "采集样本 · 星球：星球 #5" {
		t.Errorf("没有星球数据时应显示编号，实际 %q", got)
	}
}

// TestDecodeAssignmentTaskUnknownType 校验表里没有的任务类型不猜：任务名写明原值，
// 上游给的数值原样列出，也不画进度条。
func TestDecodeAssignmentTaskUnknownType(t *testing.T) {
	task := hd2.Task{Type: 99, Values: []int64{7, 8}, ValueTypes: []int{1, 2}}
	item := decodeAssignmentTask(task, nil, nil)

	if item.Title != "未知任务类型 99" {
		t.Errorf("未知类型应写明原值，实际 %q", item.Title)
	}
	if item.Numbers != "原始数值 7、8" {
		t.Errorf("未知类型应原样列出数值，实际 %q", item.Numbers)
	}
	if item.Bar {
		t.Error("未知类型不该画进度条")
	}
}

// TestDecodeAssignmentTasksPairsProgress 校验进度数组与任务按下标一一对应：
// progress[i] 属于 tasks[i]，进度比任务短时只是对应任务没有当前值，不影响别的任务。
func TestDecodeAssignmentTasksPairsProgress(t *testing.T) {
	tasks := []hd2.Task{liveTask(3, 2, 1000, 0), liveTask(3, 3, 2000, 0)}
	items := decodeAssignmentTasks(tasks, []int64{500}, nil)

	if len(items) != 2 {
		t.Fatalf("应解出 2 条任务，实际 %d 条", len(items))
	}
	if items[0].Numbers != "500 / 1,000（50.0%）" {
		t.Errorf("第一条应拿到 progress[0]，实际 %q", items[0].Numbers)
	}
	if items[1].Numbers != "目标 2,000" {
		t.Errorf("进度缺失的任务只写目标值，实际 %q", items[1].Numbers)
	}
	if items[1].Bar {
		t.Error("没有当前值时不该画进度条")
	}
	if got := decodeAssignmentTasks(nil, nil, nil); got != nil {
		t.Errorf("没有任务时应返回 nil（模板据此不渲染任务区），实际 %#v", got)
	}
}

// TestDecodeAssignmentTasksLivePayload 用 2026-09-18 实测的 /assignments 原样数据钉住真机形状：
// 三条 type=3 任务，阵营分别是 2/3/4，目标值都在 values[2]，进度数组与任务按下标对应。
// 这条用例的意义是「上游明天换了数值也不会让解码悄悄错位」——真数据一旦对不上，这里先红。
func TestDecodeAssignmentTasksLivePayload(t *testing.T) {
	valueTypes := []int{1, 2, 3, 4, 6, 5, 8, 9, 11, 12}
	tasks := []hd2.Task{
		{Type: 3, Values: []int64{2, 0, 1500000000, 0, 0, 0, 0, 0, 0, 0}, ValueTypes: valueTypes},
		{Type: 3, Values: []int64{3, 0, 500000000, 0, 0, 0, 0, 0, 0, 0}, ValueTypes: valueTypes},
		{Type: 3, Values: []int64{4, 0, 500000000, 0, 0, 0, 0, 0, 0, 0}, ValueTypes: valueTypes},
	}
	items := decodeAssignmentTasks(tasks, []int64{149671345, 88572031, 23745553}, nil)

	want := []struct{ title, numbers string }{
		{"消灭终结族敌人", "149,671,345 / 1,500,000,000（10.0%）"},
		{"消灭机器人敌人", "88,572,031 / 500,000,000（17.7%）"},
		{"消灭光能者敌人", "23,745,553 / 500,000,000（4.7%）"},
	}
	for i, w := range want {
		if items[i].Title != w.title || items[i].Numbers != w.numbers {
			t.Errorf("第 %d 条任务解码错误：得到 %q / %q，期望 %q / %q",
				i+1, items[i].Title, items[i].Numbers, w.title, w.numbers)
		}
	}
}

// TestNeedsPlanetNames 校验只有任务真的带星球索引时才去抓星球列表：
// 多数「消灭敌人」型指令不带星球，不该为它多打一次上游。
func TestNeedsPlanetNames(t *testing.T) {
	withPlanet := []hd2.Assignment{{Tasks: []hd2.Task{liveTask(11, 0, 0, 5)}}}
	if !needsPlanetNames(withPlanet) {
		t.Error("任务带星球索引时应去取星球列表")
	}
	// 星球索引为 0（不限星球）不算带星球。
	globalOnly := []hd2.Assignment{{Tasks: []hd2.Task{liveTask(3, 2, 1000, 0)}}}
	if needsPlanetNames(globalOnly) {
		t.Error("星球索引为 0 时不该去取星球列表")
	}
	if needsPlanetNames(nil) {
		t.Error("没有指令时不该去取星球列表")
	}
}
