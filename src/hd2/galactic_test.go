package hd2

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestGalacticEffectOf 校验行动变量对照表的查询：收录的效果拿到中文名与说明，
// 作战限制类标成负面，同族别名（对照表没收录的那个编号）拿到同族的名字但不带说明，
// 完全查不到的 ID 返回 false（卡片写「未知行动变量」）。
func TestGalacticEffectOf(t *testing.T) {
	predator, ok := GalacticEffectOf(1243)
	if !ok {
		t.Fatal("1243（掠食变种）应在对照表里")
	}
	if predator.Name != "掠食变种" || predator.Category != "终结族变种" {
		t.Errorf("掠食变种的对照条目错误：%+v", predator)
	}
	if !strings.Contains(predator.Desc, "攻击性") {
		t.Errorf("掠食变种应带中文说明，实际 %q", predator.Desc)
	}
	if predator.Negative {
		t.Error("终结族变种不是作战限制，不该标成负面")
	}

	restriction, ok := GalacticEffectOf(1313)
	if !ok || !restriction.Negative || restriction.Category != "作战限制" {
		t.Errorf("作战限制类的效果应标成负面：%+v（ok=%v）", restriction, ok)
	}

	// 同族别名：1272（reinforce_Reduction2）与 1318（预算削减 / reinforce_Reduction3）同族，
	// 只借名字与分类；说明里写着具体数值（-1 增援），照抄会张冠李戴，所以别名不带说明。
	alias, ok := GalacticEffectOf(1272)
	if !ok || alias.Name != "预算削减" || alias.Category != "作战限制" || !alias.Negative {
		t.Errorf("同族别名应拿到族名与分类：%+v（ok=%v）", alias, ok)
	}
	if alias.Desc != "" {
		t.Errorf("别名不该带说明（数值可能不同），实际 %q", alias.Desc)
	}
	if delay, ok := GalacticEffectOf(1363); !ok || delay.Name != "班机延误" || delay.Desc != "" {
		t.Errorf("1363（extract_PilotShortage8）应为「班机延误」且不带说明：%+v（ok=%v）", delay, ok)
	}

	if _, ok := GalacticEffectOf(999999); ok {
		t.Error("对照表外的 ID 应返回 false")
	}
	if _, ok := GalacticEffectOf(1358); ok {
		t.Error("只有内部代号、没有同族中文名的效果应返回 false")
	}
}

// TestDecodePlanetEffects 校验从补充源整包数据里取行动变量：
// 跳过脏数据、同一颗星球的重复效果只留一条、输出按（星球编号，效果 ID）排序。
func TestDecodePlanetEffects(t *testing.T) {
	body := []byte(`{
		"appVersion": "1.0",
		"warStatus": {
			"planetStatus": [{"index": 1}],
			"planetActiveEffects": [
				{"index": 268, "galacticEffectId": 1303},
				{"index": 256, "galacticEffectId": 1190},
				{"index": 268, "galacticEffectId": 1303},
				{"index": 0, "galacticEffectId": 1188},
				{"index": 99, "galacticEffectId": 0},
				{"index": 256, "galacticEffectId": 1243}
			]
		}
	}`)
	got, err := DecodePlanetEffects(body)
	if err != nil {
		t.Fatalf("解析行动变量失败：%v", err)
	}
	want := []PlanetEffect{
		{PlanetIndex: 256, EffectID: 1190},
		{PlanetIndex: 256, EffectID: 1243},
		{PlanetIndex: 268, EffectID: 1303},
	}
	if len(got) != len(want) {
		t.Fatalf("条数错误：期望 %+v，实际 %+v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 条错误：期望 %+v，实际 %+v", i+1, want[i], got[i])
		}
	}
}

// TestDecodePlanetEffectsEmpty 校验整包里没有 planetActiveEffects 时返回空切片而不是报错：
// 上游某天不再下发这个字段时，卡片应该写「暂无已知行动变量」，而不是判定数据源故障。
func TestDecodePlanetEffectsEmpty(t *testing.T) {
	got, err := DecodePlanetEffects([]byte(`{"warStatus":{"campaigns":[]}}`))
	if err != nil {
		t.Fatalf("缺字段不该报错：%v", err)
	}
	if len(got) != 0 {
		t.Errorf("期望空结果，实际 %+v", got)
	}
}

// TestDecodePlanetEffectsBadJSON 校验非法响应体报中文错误。
func TestDecodePlanetEffectsBadJSON(t *testing.T) {
	if _, err := DecodePlanetEffects([]byte(`{"warStatus":`)); err == nil {
		t.Fatal("非法 JSON 应报错")
	} else if !strings.Contains(err.Error(), "行动变量") {
		t.Errorf("错误信息应说明是行动变量解析失败，实际 %v", err)
	}
}

// companionEffectsServer 起一个只服务行动变量整包的假补充源。
// 返回服务器、请求计数函数与关闭函数（由 t.Cleanup 兜底）。
func companionEffectsServer(t *testing.T, body string, status int) (*httptest.Server, func() int) {
	t.Helper()
	var mu sync.Mutex
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != CompanionPathAPIData {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		mu.Lock()
		calls++
		mu.Unlock()
		if status != 0 && status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server, func() int {
		mu.Lock()
		defer mu.Unlock()
		return calls
	}
}

// TestServicePlanetEffects 校验行动变量走补充源、命中缓存后不重复取数、且不写快照。
func TestServicePlanetEffects(t *testing.T) {
	body := `{"warStatus":{"planetActiveEffects":[{"index":256,"galacticEffectId":1190}]}}`
	server, calls := companionEffectsServer(t, body, http.StatusOK)
	store := newFakeStore()
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	svc := NewService(ServiceConfig{
		Companion: NewCompanion(server.URL, "hd2_bot_test/0.1", 5*time.Second, nil, nil),
		Store:     store,
		TTL:       TTLConfig{Effects: time.Minute},
		Now:       clock,
	})

	res, err := svc.PlanetEffects(context.Background())
	if err != nil {
		t.Fatalf("取行动变量失败：%v", err)
	}
	if len(res.Value) != 1 || res.Value[0].PlanetIndex != 256 || res.Value[0].EffectID != 1190 {
		t.Fatalf("行动变量数据错误：%+v", res.Value)
	}
	if res.Stale {
		t.Error("正常取数不该标记为过期数据")
	}
	if _, ok := store.data[namePlanetEffects]; ok {
		t.Error("行动变量不该写快照（整包 400KB，存下来只为降级不划算）")
	}

	// 第二次查询命中缓存：假补充源的请求数不增加。
	if _, err := svc.PlanetEffects(context.Background()); err != nil {
		t.Fatalf("第二次取行动变量失败：%v", err)
	}
	if got := calls(); got != 1 {
		t.Errorf("缓存命中时不该重复取数，实际请求 %d 次", got)
	}

	// 缓存过期后重新取数（补充源不参与主源的限流器，这里只验证重新取数这条路径）。
	now = now.Add(2 * time.Minute)
	if _, err := svc.PlanetEffects(context.Background()); err != nil {
		t.Fatalf("缓存过期后取行动变量失败：%v", err)
	}
	if got := calls(); got != 2 {
		t.Errorf("缓存过期后应重新取数，实际请求 %d 次", got)
	}
}

// TestServicePlanetEffectsWithoutCompanion 校验未配置补充源时返回错误（卡片据此写「暂不可用」）。
func TestServicePlanetEffectsWithoutCompanion(t *testing.T) {
	svc := NewService(ServiceConfig{TTL: TTLConfig{Effects: time.Minute}})
	if _, err := svc.PlanetEffects(context.Background()); err == nil {
		t.Fatal("未配置补充源时应返回错误")
	}
}

// TestServicePlanetEffectsUpstreamError 校验补充源出错时把错误上抛，且缓存里不留脏数据。
func TestServicePlanetEffectsUpstreamError(t *testing.T) {
	server, _ := companionEffectsServer(t, "", http.StatusServiceUnavailable)
	svc := NewService(ServiceConfig{
		Companion: NewCompanion(server.URL, "hd2_bot_test/0.1", 5*time.Second, nil, nil),
		TTL:       TTLConfig{Effects: time.Minute},
	})
	if _, err := svc.PlanetEffects(context.Background()); err == nil {
		t.Fatal("补充源 503 时应返回错误")
	}
}
