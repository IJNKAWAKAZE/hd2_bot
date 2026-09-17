package hd2

import (
	"strings"
	"testing"
	"time"
)

// TestDecodeWar 校验战况解析，特别是上游拼写错误的 accurracy 字段与缺失字段容错。
func TestDecodeWar(t *testing.T) {
	body := []byte(`{
		"started": "2026-02-08T10:00:00Z",
		"now": "2026-09-16T06:00:00Z",
		"clientVersion": "1.003.400",
		"factions": ["Humans", "Terminids"],
		"impactMultiplier": 0.000098,
		"statistics": {
			"missionsWon": 217853802,
			"missionsLost": 25121616,
			"bulletsFired": 7149830438,
			"bulletsHit": 4430772631,
			"accurracy": 61.97,
			"playerCount": 23162
		}
	}`)
	war, err := DecodeWar(body)
	if err != nil {
		t.Fatalf("解析战况失败：%v", err)
	}
	if war.Statistics.MissionsWon != 217853802 {
		t.Errorf("missionsWon 解析错误：%d", war.Statistics.MissionsWon)
	}
	if war.Statistics.Accurracy != 61.97 {
		t.Errorf("accurracy 解析错误：%v", war.Statistics.Accurracy)
	}
	if war.Statistics.PlayerCount != 23162 {
		t.Errorf("playerCount 解析错误：%d", war.Statistics.PlayerCount)
	}
	if got := war.Now.UTC().Format(time.RFC3339); got != "2026-09-16T06:00:00Z" {
		t.Errorf("now 解析错误：%s", got)
	}
	if len(war.Factions) != 2 {
		t.Errorf("factions 解析错误：%v", war.Factions)
	}
}

// TestDecodeWarMissingFields 校验上游缺少字段时不报错，零值可用。
func TestDecodeWarMissingFields(t *testing.T) {
	war, err := DecodeWar([]byte(`{"started":"2026-02-08T10:00:00Z"}`))
	if err != nil {
		t.Fatalf("缺字段时不应报错：%v", err)
	}
	if war.Statistics.MissionsWon != 0 || war.ClientVersion != "" {
		t.Errorf("缺字段应取零值，实际 %+v", war)
	}
}

// TestDecodeWarNullBodyReportsError 校验单对象端点的响应为 null 时直接报错，
// 而不是静默返回零值对象让上层展示错误数据。
func TestDecodeWarNullBodyReportsError(t *testing.T) {
	war, err := DecodeWar([]byte(`null`))
	if err == nil {
		t.Fatalf("war 端点返回 null 应报错，实际解析成功：%+v", war)
	}
	if !strings.Contains(err.Error(), "war") {
		t.Errorf("错误信息应包含数据名 war，实际 %v", err)
	}
}

// TestDecodeStatisticsAccurracySpellings 校验命中率兼容上游两种拼写：
// 只给 accurracy（历史/文档拼写）、只给 accuracy（当前线上拼写）以及两者都缺失的情况。
func TestDecodeStatisticsAccurracySpellings(t *testing.T) {
	cases := []struct {
		name string
		body string
		want float64
	}{
		{"仅 accurracy 拼写", `{"statistics":{"accurracy":61.97}}`, 61.97},
		{"仅 accuracy 拼写", `{"statistics":{"accuracy":42.5}}`, 42.5},
		{"两种拼写都缺失", `{"statistics":{"playerCount":3}}`, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			war, err := DecodeWar([]byte(c.body))
			if err != nil {
				t.Fatalf("解析战况失败：%v", err)
			}
			if war.Statistics.Accurracy != c.want {
				t.Errorf("Accurracy 期望 %v，实际 %v", c.want, war.Statistics.Accurracy)
			}
		})
	}
}

// TestDecodeWarEmptyTimeStrings 校验 started/ended/now 为空串或 null 时取零值而不报错。
func TestDecodeWarEmptyTimeStrings(t *testing.T) {
	war, err := DecodeWar([]byte(`{"started":"","ended":null,"now":"2026-09-16T06:00:00Z"}`))
	if err != nil {
		t.Fatalf("空串或 null 时间不应报错：%v", err)
	}
	if !war.Started.IsZero() {
		t.Errorf("started 为空串时应取零值，实际 %s", war.Started.Format(time.RFC3339))
	}
	if !war.Ended.IsZero() {
		t.Errorf("ended 为 null 时应取零值，实际 %s", war.Ended.Format(time.RFC3339))
	}
	if got := war.Now.UTC().Format(time.RFC3339); got != "2026-09-16T06:00:00Z" {
		t.Errorf("now 解析错误：%s", got)
	}
}

// TestDecodeWarInvalidTimeReportsError 校验时间格式非法时返回错误而不是静默归零，
// 这样上层可以走快照降级，错误信息同时带上数据名便于日志定位。
func TestDecodeWarInvalidTimeReportsError(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		fragment string
	}{
		{"now 格式非法", `{"now":"oops"}`, "oops"},
		{"now 值不是字符串", `{"now":12345}`, "12345"},
		{"started 格式非法", `{"started":"2026-13-99"}`, "started"},
		{"ended 格式非法", `{"ended":"x"}`, "ended"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			war, err := DecodeWar([]byte(c.body))
			if err == nil {
				t.Fatalf("时间格式非法应返回错误，实际解析成功：%+v", war)
			}
			if !strings.Contains(err.Error(), "war") {
				t.Errorf("错误信息应包含数据名 war，实际 %v", err)
			}
			if !strings.Contains(err.Error(), c.fragment) {
				t.Errorf("错误信息应包含原始片段 %q，实际 %v", c.fragment, err)
			}
		})
	}
}

// TestDecodeWarTimeZoneOffset 校验带时区偏移的时间被正确换算成同一时刻。
func TestDecodeWarTimeZoneOffset(t *testing.T) {
	war, err := DecodeWar([]byte(`{"now":"2026-09-16T14:00:00+08:00"}`))
	if err != nil {
		t.Fatalf("解析带偏移的时间失败：%v", err)
	}
	if got := war.Now.UTC().Format(time.RFC3339); got != "2026-09-16T06:00:00Z" {
		t.Errorf("时区换算错误：%s", got)
	}
}

// TestDecodePlanets 校验星球列表解析与 null 事件、null 结束时间。
func TestDecodePlanets(t *testing.T) {
	body := []byte(`[{
		"index": 22,
		"name": "Vernen Wells",
		"sector": "Celeste",
		"biome": {"name": "Desert", "description": "干燥"},
		"hazards": [{"name": "Fire Tornadoes", "description": "火焰龙卷"}],
		"currentOwner": "Automaton",
		"initialOwner": "Humans",
		"health": 500000,
		"maxHealth": 1000000,
		"regenPerSecond": 0.5,
		"waypoints": [1, 2],
		"attacking": [3],
		"event": {
			"id": 9001,
			"eventType": 1,
			"faction": "Automaton",
			"health": 100,
			"maxHealth": 200,
			"startTime": "2026-09-16T05:00:00Z",
			"endTime": null,
			"campaignId": 77,
			"planetIndex": 22
		},
		"statistics": {"playerCount": 12345}
	}, {
		"index": 5,
		"name": "Angel's Venture",
		"event": null
	}]`)
	planets, err := DecodePlanets(body)
	if err != nil {
		t.Fatalf("解析星球列表失败：%v", err)
	}
	if len(planets) != 2 {
		t.Fatalf("星球数量错误：%d", len(planets))
	}
	if planets[0].Biome.Name != "Desert" || planets[0].Statistics.PlayerCount != 12345 {
		t.Errorf("首个星球解析错误：%+v", planets[0])
	}
	if planets[0].CurrentOwner != "Automaton" || planets[0].InitialOwner != "Humans" {
		t.Errorf("星球所有者解析错误：当前 %q，初始 %q", planets[0].CurrentOwner, planets[0].InitialOwner)
	}
	if planets[0].Event == nil || planets[0].Event.EndTime != nil {
		t.Errorf("事件或结束时间解析错误：%+v", planets[0].Event)
	}
	if planets[0].Event.CampaignID != 77 || planets[0].Event.PlanetIndex != 22 {
		t.Errorf("事件关联字段解析错误：%+v", planets[0].Event)
	}
	if planets[1].Event != nil {
		t.Errorf("event 为 null 时应为 nil：%+v", planets[1].Event)
	}
}

// TestDecodePlanetEventWithEndTime 校验事件结束时间有值时解析成非空指针。
func TestDecodePlanetEventWithEndTime(t *testing.T) {
	body := []byte(`[{"index":22,"event":{"id":9001,"eventType":1,"startTime":"2026-09-16T05:00:00Z","endTime":"2026-09-16T07:00:00Z"}}]`)
	planets, err := DecodePlanets(body)
	if err != nil {
		t.Fatalf("解析星球列表失败：%v", err)
	}
	if len(planets) != 1 || planets[0].Event == nil {
		t.Fatalf("事件解析错误：%+v", planets)
	}
	end := planets[0].Event.EndTime
	if end == nil {
		t.Fatal("endTime 有值时应解析为非空指针")
	}
	if got := end.UTC().Format(time.RFC3339); got != "2026-09-16T07:00:00Z" {
		t.Errorf("endTime 解析错误：%s", got)
	}
}

// TestDecodeAssignmentsEmpty 校验重要指令为空数组时返回空切片而非 nil。
func TestDecodeAssignmentsEmpty(t *testing.T) {
	list, err := DecodeAssignments([]byte(`[]`))
	if err != nil {
		t.Fatalf("解析空数组失败：%v", err)
	}
	if list == nil {
		t.Fatal("空数组应返回空切片")
	}
	if len(list) != 0 {
		t.Fatalf("期望 0 条重要指令，实际 %d", len(list))
	}
}

// TestDecodeAssignments 校验重要指令解析。
func TestDecodeAssignments(t *testing.T) {
	body := []byte(`[{
		"id": 1,
		"title": "MAJOR ORDER",
		"briefing": "解放 Vernen Wells",
		"tasks": [{"type": 11, "values": [22, 1000000], "valueTypes": [3, 3]}],
		"reward": {"type": 1, "amount": 50, "id32": 3992382197},
		"expiration": "2026-09-18T06:00:00Z",
		"progress": [500000, 1000000],
		"flags": 0
	}]`)
	list, err := DecodeAssignments(body)
	if err != nil {
		t.Fatalf("解析重要指令失败：%v", err)
	}
	if len(list) != 1 {
		t.Fatalf("重要指令数量错误：%d", len(list))
	}
	if list[0].Reward.Amount != 50 || list[0].Tasks[0].Values[0] != 22 {
		t.Errorf("重要指令字段解析错误：%+v", list[0])
	}
	if list[0].Reward.ID32 != 3992382197 {
		t.Errorf("reward.id32 解析错误：%d", list[0].Reward.ID32)
	}
	if list[0].Expiration == nil || list[0].Expiration.UTC().Format(time.RFC3339) != "2026-09-18T06:00:00Z" {
		t.Errorf("expiration 解析错误：%+v", list[0].Expiration)
	}
}

// TestDecodeAssignmentNullExpiration 校验重要指令没有截止时间时 expiration 为 nil。
func TestDecodeAssignmentNullExpiration(t *testing.T) {
	list, err := DecodeAssignments([]byte(`[{"id":1,"title":"MAJOR ORDER","expiration":null}]`))
	if err != nil {
		t.Fatalf("expiration 为 null 时不应报错：%v", err)
	}
	if len(list) != 1 {
		t.Fatalf("重要指令数量错误：%d", len(list))
	}
	if list[0].Expiration != nil {
		t.Errorf("expiration 为 null 时应为 nil，实际 %+v", list[0].Expiration)
	}
}

// TestDecodeCampaigns 校验战役解析，含嵌套的星球引用。
func TestDecodeCampaigns(t *testing.T) {
	body := []byte(`[{"id": 8001, "planet": {"index": 22, "name": "Vernen Wells"}, "type": 0, "count": 1, "faction": "Automaton"}]`)
	list, err := DecodeCampaigns(body)
	if err != nil {
		t.Fatalf("解析战役失败：%v", err)
	}
	if len(list) != 1 {
		t.Fatalf("战役数量错误：%d", len(list))
	}
	if list[0].ID != 8001 || list[0].Planet.Index != 22 || list[0].Planet.Name != "Vernen Wells" {
		t.Errorf("战役字段解析错误：%+v", list[0])
	}
}

// TestDecodeEvents 校验星球事件列表解析。
// 上游 /planet-events 返回的是星球对象数组（事件嵌在对象里），所以用例用真实形态的响应体，
// 并断言事件被补上了所属星球的编号与名字。
func TestDecodeEvents(t *testing.T) {
	body := []byte(`[{"index":268,"name":"LUXURIANT","currentOwner":"Humans","event":{"id":9001,"eventType":1,"faction":"Terminids","health":100,"maxHealth":200,"startTime":"2026-09-16T05:00:00Z","campaignId":77}}]`)
	list, err := DecodeEvents(body)
	if err != nil {
		t.Fatalf("解析事件失败：%v", err)
	}
	if len(list) != 1 {
		t.Fatalf("事件数量错误：%d", len(list))
	}
	if list[0].ID != 9001 || list[0].CampaignID != 77 {
		t.Errorf("事件字段解析错误：%+v", list[0])
	}
	if list[0].PlanetIndex != 268 || list[0].PlanetName != "LUXURIANT" {
		t.Errorf("事件应带上所属星球：%+v", list[0])
	}
	if got := list[0].StartTime.UTC().Format(time.RFC3339); got != "2026-09-16T05:00:00Z" {
		t.Errorf("startTime 解析错误：%s", got)
	}
}

// TestDecodeEventsSkipsPlanetsWithoutEvent 校验没有事件的星球被跳过，不会产生一条空事件。
func TestDecodeEventsSkipsPlanetsWithoutEvent(t *testing.T) {
	body := []byte(`[{"index":22,"name":"Vernen Wells","event":null},{"index":268,"name":"LUXURIANT","event":{"id":9001}}]`)
	list, err := DecodeEvents(body)
	if err != nil {
		t.Fatalf("解析事件失败：%v", err)
	}
	if len(list) != 1 || list[0].ID != 9001 || list[0].PlanetIndex != 268 {
		t.Fatalf("应只保留带事件的星球：%+v", list)
	}
}

// TestDecodeEventsEmptyBecomesEmptySlice 校验空数组（实测无事件时的响应）返回空切片而不是 nil。
func TestDecodeEventsEmptyBecomesEmptySlice(t *testing.T) {
	list, err := DecodeEvents([]byte(`[]`))
	if err != nil {
		t.Fatalf("解析空数组失败：%v", err)
	}
	if list == nil {
		t.Fatal("空数组应解码为空切片而非 nil")
	}
	if len(list) != 0 {
		t.Fatalf("期望 0 条事件，实际 %d", len(list))
	}
}

// TestDecodeDispatches 校验简报解析。
func TestDecodeDispatches(t *testing.T) {
	body := []byte(`[{"id": 12345, "published": "2026-09-15T10:00:00Z", "type": 0, "message": "士兵们，准备空投。"}]`)
	list, err := DecodeDispatches(body)
	if err != nil {
		t.Fatalf("解析简报失败：%v", err)
	}
	if len(list) != 1 || list[0].Message != "士兵们，准备空投。" {
		t.Fatalf("简报解析错误：%+v", list)
	}
}

// TestDecodeStations 校验空间站解析。
func TestDecodeStations(t *testing.T) {
	body := []byte(`[{"id32": 749875195, "id": "dss-1", "name": "民主空间站", "flags": 1, "tacticalActions": [{"id32": 1, "name": "轨道封锁", "status": 2}]}]`)
	list, err := DecodeStations(body)
	if err != nil {
		t.Fatalf("解析空间站失败：%v", err)
	}
	if len(list) != 1 || list[0].Name != "民主空间站" || list[0].TacticalActions[0].Name != "轨道封锁" {
		t.Fatalf("空间站解析错误：%+v", list)
	}
	if list[0].ID32 != 749875195 {
		t.Errorf("空间站 id32 解析错误：%d", list[0].ID32)
	}
}

// TestDecodeStationsPlanetFieldTolerated 校验空间站的 planet 字段容错解析：
// 实测上游给过两种形态——字符串 "?"（早先的实测）与 {"index":100,...} 整个星球对象（2026-09 实测），
// 另外数字、数字字符串也要认；问号 / 缺失 / null / 认不出的对象一律取 0 且不报错
// （整批空间站不能因为一个字段的脏值解码失败）。
func TestDecodeStationsPlanetFieldTolerated(t *testing.T) {
	body := []byte(`[
		{"id32": 1, "name": "甲", "planet": "?"},
		{"id32": 2, "name": "乙", "planet": 42},
		{"id32": 3, "name": "丙", "planet": "7"},
		{"id32": 4, "name": "丁"},
		{"id32": 5, "name": "戊", "planet": {"index": 100, "name": "TRANDOR"}},
		{"id32": 6, "name": "己", "planet": {"name": "TRANDOR"}},
		{"id32": 7, "name": "庚", "planet": {"index": "9"}}
	]`)
	list, err := DecodeStations(body)
	if err != nil {
		t.Fatalf("planet 为问号或缺失时不应报错：%v", err)
	}
	want := []int{0, 42, 7, 0, 100, 0, 9}
	if len(list) != len(want) {
		t.Fatalf("应解析出 %d 座空间站，实际 %d", len(want), len(list))
	}
	for i, index := range want {
		if list[i].PlanetIndex != index {
			t.Errorf("第 %d 座空间站的 PlanetIndex 应为 %d，实际 %d", i+1, index, list[i].PlanetIndex)
		}
	}
}

// TestDecodeStationsPlanetNull 校验 planet 为 null 时取 0（与缺失同义），不报错。
func TestDecodeStationsPlanetNull(t *testing.T) {
	list, err := DecodeStations([]byte(`[{"id32": 1, "planet": null}]`))
	if err != nil {
		t.Fatalf("planet 为 null 时不应报错：%v", err)
	}
	if len(list) != 1 || list[0].PlanetIndex != 0 {
		t.Fatalf("planet 为 null 应取 0，实际 %+v", list)
	}
}

// TestDecodeNullReturnsEmptySlice 校验上游返回 null 时解码函数给出非 nil 空切片，
// 这样调用方可以直接 range/len 而不必判空。
func TestDecodeNullReturnsEmptySlice(t *testing.T) {
	planets, err := DecodePlanets([]byte(`null`))
	if err != nil {
		t.Fatalf("null 不应报错：%v", err)
	}
	if planets == nil {
		t.Fatal("null 应解码为空切片而非 nil")
	}
	if len(planets) != 0 {
		t.Fatalf("期望 0 个元素，实际 %d", len(planets))
	}

	assignments, err := DecodeAssignments([]byte(`null`))
	if err != nil {
		t.Fatalf("null 不应报错：%v", err)
	}
	if assignments == nil {
		t.Fatal("null 应解码为空切片而非 nil")
	}
}

// TestDecodeWarMalformedBodyReportsError 校验 body 结构本身不对时（不是对象、
// statistics 不是对象）也会报错并带上下文，覆盖两处内部 json.Unmarshal 的失败分支。
func TestDecodeWarMalformedBodyReportsError(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		fragment string
	}{
		{"war 不是对象", `5`, "war 字段格式非法"},
		{"statistics 不是对象", `{"statistics":5}`, "statistics 字段格式非法"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			war, err := DecodeWar([]byte(c.body))
			if err == nil {
				t.Fatalf("结构非法的 body 应报错，实际解析成功：%+v", war)
			}
			if !strings.Contains(err.Error(), "war") {
				t.Errorf("错误信息应包含数据名 war，实际 %v", err)
			}
			if !strings.Contains(err.Error(), c.fragment) {
				t.Errorf("错误信息应包含 %q，实际 %v", c.fragment, err)
			}
		})
	}
}

// TestDecodeBadJSONIncludesName 校验坏响应体（例如网关返回的 HTML 错误页）
// 会被解码函数报错，且错误信息带上数据名，方便日志定位是哪个端点。
func TestDecodeBadJSONIncludesName(t *testing.T) {
	const badBody = `<html>502</html>`
	cases := []struct {
		name   string
		decode func([]byte) error
	}{
		{"war", func(b []byte) error { _, err := DecodeWar(b); return err }},
		{"planets", func(b []byte) error { _, err := DecodePlanets(b); return err }},
		{"campaigns", func(b []byte) error { _, err := DecodeCampaigns(b); return err }},
		{"assignments", func(b []byte) error { _, err := DecodeAssignments(b); return err }},
		{"dispatches", func(b []byte) error { _, err := DecodeDispatches(b); return err }},
		{"planet-events", func(b []byte) error { _, err := DecodeEvents(b); return err }},
		{"space-stations", func(b []byte) error { _, err := DecodeStations(b); return err }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.decode([]byte(badBody))
			if err == nil {
				t.Fatal("坏响应体应返回错误")
			}
			if !strings.Contains(err.Error(), c.name) {
				t.Errorf("错误信息应包含数据名 %q，实际 %v", c.name, err)
			}
		})
	}
}

// TestDecodeListRejectsObject 校验把 JSON 对象喂给列表解码函数时报错，且错误信息带数据名。
func TestDecodeListRejectsObject(t *testing.T) {
	cases := []struct {
		name   string
		decode func([]byte) error
	}{
		{"planets", func(b []byte) error { _, err := DecodePlanets(b); return err }},
		{"campaigns", func(b []byte) error { _, err := DecodeCampaigns(b); return err }},
		{"assignments", func(b []byte) error { _, err := DecodeAssignments(b); return err }},
		{"dispatches", func(b []byte) error { _, err := DecodeDispatches(b); return err }},
		{"planet-events", func(b []byte) error { _, err := DecodeEvents(b); return err }},
		{"space-stations", func(b []byte) error { _, err := DecodeStations(b); return err }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.decode([]byte(`{"a":1}`))
			if err == nil {
				t.Fatal("对象喂给列表解码函数应返回错误")
			}
			if !strings.Contains(err.Error(), c.name) {
				t.Errorf("错误信息应包含数据名 %q，实际 %v", c.name, err)
			}
		})
	}
}

// TestDecodePlanetsEmptyEventTimes 校验星球事件的空串时间被当作「上游没有给值」：
// startTime 取零值、endTime 为 nil，个别星球的时间字段不会拖垮整批星球解析。
func TestDecodePlanetsEmptyEventTimes(t *testing.T) {
	body := []byte(`[{"index":22,"event":{"id":9001,"eventType":1,"startTime":"","endTime":""}}]`)
	planets, err := DecodePlanets(body)
	if err != nil {
		t.Fatalf("空串时间不应报错：%v", err)
	}
	if len(planets) != 1 || planets[0].Event == nil {
		t.Fatalf("事件解析错误：%+v", planets)
	}
	if !planets[0].Event.StartTime.IsZero() {
		t.Errorf("startTime 为空串时应取零值，实际 %s", planets[0].Event.StartTime.Format(time.RFC3339))
	}
	if planets[0].Event.EndTime != nil {
		t.Errorf("endTime 为空串时应为 nil，实际 %+v", planets[0].Event.EndTime)
	}
}

// TestDecodePlanetsInvalidEventTimeReportsError 校验星球事件的非法时间会报错并带上数据名。
func TestDecodePlanetsInvalidEventTimeReportsError(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		fragment string
	}{
		{"startTime 格式非法", `[{"index":22,"event":{"startTime":"oops"}}]`, "oops"},
		{"endTime 值不是字符串", `[{"index":22,"event":{"endTime":123}}]`, "123"},
		{"event 不是对象", `[{"index":22,"event":5}]`, "planetEvent 字段格式非法"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			planets, err := DecodePlanets([]byte(c.body))
			if err == nil {
				t.Fatalf("非法时间应报错，实际解析成功：%+v", planets)
			}
			if !strings.Contains(err.Error(), "planets") {
				t.Errorf("错误信息应包含数据名 planets，实际 %v", err)
			}
			if !strings.Contains(err.Error(), c.fragment) {
				t.Errorf("错误信息应包含 %q，实际 %v", c.fragment, err)
			}
		})
	}
}

// TestDecodeDispatchesEmptyPublished 校验简报发布时间为空串时取零值而不报错。
func TestDecodeDispatchesEmptyPublished(t *testing.T) {
	list, err := DecodeDispatches([]byte(`[{"id":12345,"published":"","message":"士兵们，准备空投。"}]`))
	if err != nil {
		t.Fatalf("空串发布时间不应报错：%v", err)
	}
	if len(list) != 1 {
		t.Fatalf("简报数量错误：%d", len(list))
	}
	if !list[0].Published.IsZero() {
		t.Errorf("published 为空串时应取零值，实际 %s", list[0].Published.Format(time.RFC3339))
	}
}

// TestDecodeDispatchesInvalidPublishedReportsError 校验简报非法发布时间会报错并带数据名。
func TestDecodeDispatchesInvalidPublishedReportsError(t *testing.T) {
	cases := []struct{ name, body, fragment string }{
		{"published 格式非法", `[{"id":1,"published":"昨天"}]`, "昨天"},
		{"简报不是对象", `[5]`, "dispatch 字段格式非法"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			list, err := DecodeDispatches([]byte(c.body))
			if err == nil {
				t.Fatalf("非法发布时间应报错，实际解析成功：%+v", list)
			}
			if !strings.Contains(err.Error(), "dispatches") {
				t.Errorf("错误信息应包含数据名 dispatches，实际 %v", err)
			}
			if !strings.Contains(err.Error(), c.fragment) {
				t.Errorf("错误信息应包含 %q，实际 %v", c.fragment, err)
			}
		})
	}
}

// TestDecodeAssignmentsEmptyExpiration 校验重要指令截止时间为空串时取 nil 而不报错。
func TestDecodeAssignmentsEmptyExpiration(t *testing.T) {
	list, err := DecodeAssignments([]byte(`[{"id":1,"title":"MAJOR ORDER","expiration":""}]`))
	if err != nil {
		t.Fatalf("空串截止时间不应报错：%v", err)
	}
	if len(list) != 1 {
		t.Fatalf("重要指令数量错误：%d", len(list))
	}
	if list[0].Expiration != nil {
		t.Errorf("expiration 为空串时应为 nil，实际 %+v", list[0].Expiration)
	}
}

// TestDecodeAssignmentsInvalidExpirationReportsError 校验非法截止时间会报错并带数据名。
func TestDecodeAssignmentsInvalidExpirationReportsError(t *testing.T) {
	cases := []struct{ name, body, fragment string }{
		{"expiration 格式非法", `[{"id":1,"expiration":"2026-13-99"}]`, "2026-13-99"},
		{"重要指令不是对象", `[5]`, "assignment 字段格式非法"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			list, err := DecodeAssignments([]byte(c.body))
			if err == nil {
				t.Fatalf("非法截止时间应报错，实际解析成功：%+v", list)
			}
			if !strings.Contains(err.Error(), "assignments") {
				t.Errorf("错误信息应包含数据名 assignments，实际 %v", err)
			}
			if !strings.Contains(err.Error(), c.fragment) {
				t.Errorf("错误信息应包含 %q，实际 %v", c.fragment, err)
			}
		})
	}
}

// TestDecodeStationsEmptyElectionEnd 校验空间站改选结束时间为空串或 null 时取 nil 而不报错。
func TestDecodeStationsEmptyElectionEnd(t *testing.T) {
	cases := []struct{ name, body string }{
		{"空串", `[{"id":"dss-1","name":"民主空间站","electionEnd":""}]`},
		{"null", `[{"id":"dss-1","name":"民主空间站","electionEnd":null}]`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			list, err := DecodeStations([]byte(c.body))
			if err != nil {
				t.Fatalf("空串或 null 改选时间不应报错：%v", err)
			}
			if len(list) != 1 {
				t.Fatalf("空间站数量错误：%d", len(list))
			}
			if list[0].ElectionEnd != nil {
				t.Errorf("electionEnd 无值时应为 nil，实际 %+v", list[0].ElectionEnd)
			}
		})
	}
}

// TestDecodeStationsInvalidElectionEndReportsError 校验非法改选结束时间会报错并带数据名。
func TestDecodeStationsInvalidElectionEndReportsError(t *testing.T) {
	cases := []struct{ name, body, fragment string }{
		{"electionEnd 格式非法", `[{"id":"dss-1","electionEnd":"nope"}]`, "nope"},
		{"空间站不是对象", `[5]`, "spaceStation 字段格式非法"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			list, err := DecodeStations([]byte(c.body))
			if err == nil {
				t.Fatalf("非法改选时间应报错，实际解析成功：%+v", list)
			}
			if !strings.Contains(err.Error(), "space-stations") {
				t.Errorf("错误信息应包含数据名 space-stations，实际 %v", err)
			}
			if !strings.Contains(err.Error(), c.fragment) {
				t.Errorf("错误信息应包含 %q，实际 %v", c.fragment, err)
			}
		})
	}
}
