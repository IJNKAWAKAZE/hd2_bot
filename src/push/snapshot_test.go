package push

import (
	"testing"
	"time"

	"hd2_bot/src/hd2"
)

// fixedAt 是用例里的固定抓取时间（UTC）。
var fixedAt = time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

// testInput 造一份最小可用的一轮数据。
func testInput() Input {
	return Input{
		FetchedAt: fixedAt,
		Planets: []hd2.Planet{
			{Index: 7, Name: "Acamar IV", CurrentOwner: "Humans"},
			{Index: 9, Name: "Turing", CurrentOwner: "Terminids"},
		},
		Campaigns: []hd2.Campaign{
			{ID: 100, Planet: hd2.PlanetRef{Index: 9, Name: "Turing"}, Type: 0},
		},
		Stations: []hd2.SpaceStation{
			{ID32: 749875195, Flags: 1, TacticalActions: []hd2.TacticalAction{
				{Name: "EAGLE STORM", Status: 2},
				{Name: "ORBITAL BLOCKADE", Status: 3},
			}},
		},
		Dispatches: []hd2.Dispatch{
			{ID: 1048, Published: fixedAt},
			{ID: 1047, Published: fixedAt.Add(-48 * time.Hour)},
		},
	}
}

// TestSnapshotOf 校验快照把关键变化点都记下来了（少一项就会漏一类事件）。
func TestSnapshotOf(t *testing.T) {
	snap := SnapshotOf(testInput())
	if snap.FetchedAt.IsZero() {
		t.Fatal("快照应记录抓取时间")
	}
	if snap.Owners["7"] != "Humans" || snap.Owners["9"] != "Terminids" {
		t.Fatalf("星球控制方未记录：%v", snap.Owners)
	}
	if snap.PlanetNames["9"] != "Turing" {
		t.Fatalf("星球名未记录：%v", snap.PlanetNames)
	}
	if snap.Campaigns["100"] != "9|0" {
		t.Fatalf("战役指纹格式错误：%v", snap.Campaigns)
	}
	if len(snap.Stations) != 1 || snap.Stations["749875195"] == "" {
		t.Fatalf("空间站签名未记录：%v", snap.Stations)
	}
	if len(snap.DispatchIDs) != 2 || snap.DispatchIDs[0] != 1048 {
		t.Fatalf("简报 id 未按倒序记录：%v", snap.DispatchIDs)
	}
	if !snap.LatestDispatchPublished.Equal(fixedAt) {
		t.Fatalf("应记录本轮最新的简报发布时间，实际 %v", snap.LatestDispatchPublished)
	}
}

// TestSnapshotOfKeepsRecentDispatchIDs 校验简报 id 只保留最近 MaxDispatchIDs 条。
func TestSnapshotOfKeepsRecentDispatchIDs(t *testing.T) {
	in := testInput()
	in.Dispatches = make([]hd2.Dispatch, MaxDispatchIDs+20)
	for i := range in.Dispatches {
		in.Dispatches[i] = hd2.Dispatch{ID: int64(2000 - i)}
	}
	snap := SnapshotOf(in)
	if len(snap.DispatchIDs) != MaxDispatchIDs {
		t.Fatalf("应只保留 %d 条，实际 %d", MaxDispatchIDs, len(snap.DispatchIDs))
	}
	if snap.DispatchIDs[0] != 2000 {
		t.Fatalf("应保留最新的那些，实际首条 %d", snap.DispatchIDs[0])
	}
}

// TestSnapshotMarshalRoundTrip 校验快照能存进状态文件再读回来（字段不丢、map 不为 nil）。
func TestSnapshotMarshalRoundTrip(t *testing.T) {
	raw, err := SnapshotOf(testInput()).Marshal()
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	back, err := Unmarshal(raw)
	if err != nil {
		t.Fatalf("反序列化失败：%v", err)
	}
	if back.Owners["9"] != "Terminids" || back.Campaigns["100"] != "9|0" {
		t.Fatalf("往返后内容不一致：%+v", back)
	}
	// 这四个字段任何一个丢 tag（例如误写成 json:"-"）都会让重启后的基线「永远为空」或「永远被判成首次运行」，
	// 所以逐个断言原样往返，不能只看两个 map。
	if !back.FetchedAt.Equal(fixedAt) {
		t.Fatalf("FetchedAt 未原样往返：%v", back.FetchedAt)
	}
	if !back.LatestDispatchPublished.Equal(fixedAt) {
		t.Fatalf("LatestDispatchPublished 未原样往返：%v", back.LatestDispatchPublished)
	}
	if len(back.DispatchIDs) != 2 || back.DispatchIDs[0] != 1048 || back.DispatchIDs[1] != 1047 {
		t.Fatalf("DispatchIDs 未原样往返：%v", back.DispatchIDs)
	}
	if back.PlanetNames["9"] != "Turing" {
		t.Fatalf("PlanetNames 未原样往返：%v", back.PlanetNames)
	}
	if back.Owners == nil || back.PlanetNames == nil || back.Campaigns == nil || back.Stations == nil {
		t.Fatal("反序列化后 map 不能是 nil（否则第一次写会 panic 或静默丢数据）")
	}
}

// TestSnapshotEmpty 校验零值快照被判定为「没有基线」。
func TestSnapshotEmpty(t *testing.T) {
	if !(Snapshot{}).Empty() {
		t.Fatal("零值快照应判定为空")
	}
	if SnapshotOf(testInput()).Empty() {
		t.Fatal("有抓取时间的快照不应判定为空")
	}
	// FetchedAt 只是「哨兵」之一：上游时钟异常或字段缺失时它可能是零值，
	// 那时若仍判成「首次运行」，推送会永久静默且没有任何日志。
	if SnapshotOf(Input{Planets: []hd2.Planet{{Index: 7, Name: "Acamar IV"}}}).Empty() {
		t.Fatal("抓到过数据（哪怕没有时间）就不算「没有基线」")
	}
	if (Snapshot{DispatchIDs: []int64{1048}}).Empty() {
		t.Fatal("只有简报 id 的基线也不算空")
	}
}

// TestSnapshotOfTolerantOfDirtyData 校验重复编号、空列表等脏数据不会 panic，
// 且各 map 一定可写（否则第一轮记变化会静默丢数据）。
func TestSnapshotOfTolerantOfDirtyData(t *testing.T) {
	in := Input{
		FetchedAt: fixedAt,
		Planets: []hd2.Planet{
			{Index: 7, Name: "Acamar IV", CurrentOwner: "Humans"},
			{Index: 7, Name: "Acamar IV", CurrentOwner: "Humans"},
		},
		Dispatches: []hd2.Dispatch{{ID: 5}, {ID: 5}},
	}
	snap := SnapshotOf(in)
	if snap.Owners["7"] != "Humans" {
		t.Fatalf("重复星球编号应取同一个值且不 panic：%v", snap.Owners)
	}
	if snap.Owners == nil || snap.PlanetNames == nil || snap.Campaigns == nil ||
		snap.Stations == nil || snap.DispatchIDs == nil {
		t.Fatalf("没有战役/空间站/简报时也要给出可写结构：%+v", snap)
	}
	if len(snap.DispatchIDs) != 2 {
		t.Fatalf("重复的简报 id 原样记录（去重是编排层的事），实际 %v", snap.DispatchIDs)
	}

	if SnapshotOf(Input{FetchedAt: fixedAt}).Empty() {
		t.Fatal("列表全空但抓到过数据，不算「没有基线」")
	}
}

// TestUnmarshalRejectsGarbage 校验状态文件被写坏时报错而不是 panic，且 null 能解析成零值快照。
func TestUnmarshalRejectsGarbage(t *testing.T) {
	if _, err := Unmarshal([]byte("不是 JSON")); err == nil {
		t.Fatal("非法 JSON 应返回错误")
	}
	back, err := Unmarshal([]byte("null"))
	if err != nil {
		t.Fatalf("null 应解析成零值快照：%v", err)
	}
	if !back.Empty() {
		t.Fatal("null 解析出的快照应判定为「没有基线」")
	}
}

// TestMaxDispatchIDsBudget 钉住 MaxDispatchIDs 的取值本身与它的新语义：
// 它是「同一发布时间的 id 去重集」的预算，不再是全量基线（改大改小都要在这里变红）。
func TestMaxDispatchIDsBudget(t *testing.T) {
	if MaxDispatchIDs != 200 {
		t.Fatalf("MaxDispatchIDs 应为 200，实际 %d", MaxDispatchIDs)
	}
	in := testInput()
	in.Dispatches = make([]hd2.Dispatch, 1048) // 实测上游总量
	for i := range in.Dispatches {
		in.Dispatches[i] = hd2.Dispatch{ID: int64(5000 - i), Published: fixedAt.Add(-time.Duration(i) * time.Hour)}
	}
	snap := SnapshotOf(in)
	if len(snap.DispatchIDs) != 200 {
		t.Fatalf("基线里应只留 200 条 id，实际 %d", len(snap.DispatchIDs))
	}
	if !snap.LatestDispatchPublished.Equal(fixedAt) {
		t.Fatalf("最新发布时间应来自简报本体、不受截断影响，实际 %v", snap.LatestDispatchPublished)
	}
}
