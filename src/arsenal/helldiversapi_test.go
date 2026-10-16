// 新源（helldivers-api）的用例：重点是「上游集合 → 目录 JSON」这一步转换，
// 以及转换出来的目录能不能被 ParseCatalog 正常解析（schema 兼容是这套设计的地基）。
package arsenal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- 上游样本：覆盖债券武器、手雷、支援武器、哨戒炮、任务战备、未上线装备、civilian 与护甲 ----

const fixtureWeapons = `{
  "meta": {"apiVersion": "v1", "dataVersion": "2026-10-15.test", "generatedAt": "2026-10-15T23:39:39Z"},
  "data": [
    {
      "id": "ar-11-arbitrator", "name": "AR-11 Arbitrator", "upcoming": false,
      "image": {"url": "/images/v1/weapons/ar-11-arbitrator.ce5b887c.webp", "wikiFile": "AR-11_Arbitrator_Primary_Render.png"},
      "wiki": {"title": "AR-11 Arbitrator", "url": "https://helldivers.wiki.gg/wiki/AR-11_Arbitrator"},
      "category": "primary", "subcategory": "assault_rifle",
      "firearm": {"firingModes": ["auto", "burst", "other"], "fireRateRpm": [1200], "capacity": 45, "spareMagazines": 7, "recoil": 13.25},
      "throwable": null,
      "attacks": [
        {"name": "AR-11 P", "kind": "projectile",
         "damage": {"standard": 70, "durable": 20, "type": "Ballistic"},
         "penetration": {"direct": "light"}, "forces": {"demolition": 10, "stagger": 15, "push": 12}},
        {"name": "P4", "kind": "projectile",
         "damage": {"standard": 50, "durable": 12, "type": "Damage"},
         "penetration": {"direct": "medium"}, "forces": {"demolition": 10, "stagger": 20, "push": 20}}
      ],
      "statsRaw": {"Weapon Type": "Assault Rifles"},
      "source": {"type": "warbond", "label": "Ironclad Democracy P1", "warbondId": "ironclad-democracy", "page": 1, "cost": {"currency": "medals", "amount": 35}}
    },
    {
      "id": "g-8-immolation", "name": "G-8 Immolation", "upcoming": false,
      "image": {"url": "/images/v1/weapons/g-8-immolation.3c5e3c03.webp", "wikiFile": "G-8_Immolation_Throwable_Render.png"},
      "wiki": {"title": "G-8 Immolation", "url": "https://helldivers.wiki.gg/wiki/G-8_Immolation"},
      "category": "throwable", "subcategory": "special",
      "firearm": null, "throwable": {"capacity": 6, "startingCount": 4},
      "attacks": [
        {"name": "G-8 IMMOLATION E", "kind": "explosion",
         "damage": {"standard": 20, "durable": 20, "type": "Explosion"},
         "penetration": {"direct": null}, "forces": {"demolition": 0, "stagger": 40, "push": 0}}
      ],
      "statsRaw": {},
      "source": {"type": "warbond", "label": "Ironclad Democracy P1", "warbondId": "ironclad-democracy", "page": 1, "cost": {"currency": "medals", "amount": 35}}
    },
    {
      "id": "ar-23-liberator", "name": "AR-23 Liberator", "upcoming": false,
      "image": {"url": "/images/v1/weapons/ar-23-liberator.932ff63d.webp"},
      "wiki": {"url": "https://helldivers.wiki.gg/wiki/AR-23_Liberator"},
      "category": "primary", "subcategory": "assault_rifle", "firearm": {"capacity": 45},
      "throwable": null, "attacks": [], "statsRaw": {},
      "source": {"type": "default", "label": "Starter Equipment", "cost": {"currency": "", "amount": null}}
    },
    {
      "id": "sg-97-sweeper", "name": "SG-97 Sweeper", "upcoming": true,
      "image": {"url": "/images/v1/weapons/sg-97-sweeper.png"}, "wiki": {"url": "https://w/x"},
      "category": "primary", "firearm": null, "throwable": null, "attacks": [], "statsRaw": {},
      "source": {"type": "warbond", "warbondId": "scope-of-authority", "page": 1, "cost": {"currency": "medals", "amount": 40}}
    },
    {
      "id": "break-action-shotgun-civilian", "name": "Break Action Shotgun", "upcoming": false,
      "image": {"url": "/images/v1/weapons/civilian.png"}, "wiki": {"url": "https://w/civilian"},
      "category": "civilian", "firearm": null, "throwable": null, "attacks": [], "statsRaw": {},
      "source": {"type": "other", "label": "", "cost": {}}
    },
    {
      "id": "zz-9-test-tool", "name": "ZZ-9 Test Tool", "upcoming": false,
      "image": {"url": "/images/v1/weapons/zz-9-test-tool.webp"}, "wiki": {"url": "https://w/zz9"},
      "category": "primary", "subcategory": "special", "firearm": null, "throwable": null,
      "attacks": [], "statsRaw": {},
      "source": {"type": "superstore", "label": "Superstore", "cost": {"currency": "super_credits", "amount": 200}}
    }
  ]
}`

const fixtureStratagems = `{
  "meta": {"apiVersion": "v1", "dataVersion": "2026-10-15.test", "generatedAt": "2026-10-15T23:39:39Z"},
  "data": [
    {
      "id": "mg-43-machine-gun", "name": "MG-43 Machine Gun", "upcoming": false,
      "image": {"url": "/images/v1/stratagems/mg-43-machine-gun.d3665a8a.webp", "wikiFile": "Machine_Gun_Stratagem_Icon_Background.svg"},
      "wiki": {"url": "https://helldivers.wiki.gg/wiki/MG-43_Machine_Gun"},
      "availability": "loadout", "kind": "support_weapon",
      "code": ["down", "left", "down", "up", "right"],
      "cooldownS": 480, "callInTimeS": 7.75, "unlockLevel": 1,
      "supportWeapon": {"firingModes": ["auto"], "fireRateRpm": [630, 760], "capacity": 175, "spareMagazines": 3, "recoil": 21.25},
      "attacks": [{"name": "MG-43 P", "kind": "projectile",
        "damage": {"standard": 90, "durable": 23, "type": "Ballistic"},
        "penetration": {"direct": "medium"}, "forces": {"demolition": 10, "stagger": 20, "push": 12}}],
      "statsRaw": {"Weapon Type": "Heavy Weapons"},
      "source": {"type": "requisition", "label": "Patriotic Administration Center", "cost": {"currency": "requisition", "amount": 6000}}
    },
    {
      "id": "a-ac-8-autocannon-sentry", "name": "A/AC-8 Autocannon Sentry", "upcoming": false,
      "image": {"url": "/images/v1/stratagems/a-ac-8-autocannon-sentry.d5826d49.webp"},
      "wiki": {"url": "https://helldivers.wiki.gg/wiki/A/AC-8_Autocannon_Sentry"},
      "availability": "loadout", "kind": "sentry",
      "code": ["down", "up", "right", "up", "left", "up"],
      "cooldownS": 150, "callInTimeS": 7.75, "unlockLevel": 13,
      "supportWeapon": null,
      "attacks": [{"name": "40mm APHE CANNON P", "kind": "projectile",
        "damage": {"standard": 450, "durable": 450, "type": "Damage"},
        "penetration": {"direct": "anti_tank_i"}, "forces": {"demolition": 30, "stagger": 40, "push": 10}}],
      "statsRaw": {},
      "source": {"type": "requisition", "label": "Robotics Workshop", "cost": {"currency": "requisition", "amount": 6000}}
    },
    {
      "id": "reinforce", "name": "Reinforce", "upcoming": false,
      "image": {"url": "/images/v1/stratagems/reinforce.webp"}, "wiki": {"url": "https://w/reinforce"},
      "availability": "mission", "kind": "mission", "code": ["up", "down", "right", "left", "up"],
      "cooldownS": 0, "callInTimeS": 0, "supportWeapon": null, "attacks": [], "statsRaw": {},
      "source": {"type": "other", "label": "", "cost": {}}
    },
    {
      "id": "m-104-incinerator-frv", "name": "M-104 Incinerator FRV", "upcoming": false,
      "image": {"url": "/images/v1/stratagems/m-104-incinerator-frv.58c79032.webp"},
      "wiki": {"url": "https://helldivers.wiki.gg/wiki/M-104_Incinerator_FRV"},
      "availability": "unavailable", "kind": "vehicle", "code": ["down", "up", "left", "down", "up", "right"],
      "cooldownS": 480, "callInTimeS": 5, "supportWeapon": null, "attacks": [], "statsRaw": {},
      "source": {"type": "other", "label": "N/A", "cost": {}}
    }
  ]
}`

const fixtureArmors = `{
  "meta": {"apiVersion": "v1", "dataVersion": "2026-10-15.test", "generatedAt": "2026-10-15T23:39:39Z"},
  "data": [
    {
      "id": "bfm-220-ironclad", "name": "BFM-220 Ironclad", "upcoming": false,
      "image": {"url": "/images/v1/armors/bfm-220-ironclad.be6426db.webp", "wikiFile": "BFM-220_Ironclad_Armor_Render.png"},
      "wiki": {"url": "https://helldivers.wiki.gg/wiki/BFM-220_Ironclad"},
      "weight": "heavy", "armorRating": 175, "speed": 450, "staminaRegen": 50,
      "passiveId": "blunt-force-mitigation", "setIds": ["bfm-220-ironclad"],
      "source": {"type": "warbond", "label": "Ironclad DemocracyP2", "warbondId": "ironclad-democracy", "page": 2, "cost": {"currency": "medals", "amount": 55}}
    }
  ]
}`

const fixtureWarbonds = `{
  "meta": {"apiVersion": "v1", "dataVersion": "2026-10-15.test", "generatedAt": "2026-10-15T23:39:39Z"},
  "data": [
    {
      "id": "helldivers-mobilize", "name": "Helldivers Mobilize", "upcoming": false,
      "image": {"url": "/images/v1/warbonds/mobilize.webp"}, "wiki": {"url": "https://w/mobilize"},
      "type": "standard", "releaseDate": "2024-02-08", "cost": {"currency": "super_credits", "amount": 0},
      "medalsAllPages": 100, "pages": [{"number": 1}, {"number": 2}]
    },
    {
      "id": "ironclad-democracy", "name": "Ironclad Democracy", "upcoming": false,
      "image": {"url": "/images/v1/warbonds/ironclad-democracy.f6ff792b.webp"},
      "wiki": {"url": "https://helldivers.wiki.gg/wiki/Ironclad_Democracy_Premium_Warbond"},
      "type": "premium", "releaseDate": "2026-09-22", "cost": {"currency": "super_credits", "amount": 1000},
      "medalsAllPages": 360, "pages": [{"number": 1}, {"number": 2}, {"number": 3}]
    },
    {
      "id": "scope-of-authority", "name": "Scope of Authority", "upcoming": true,
      "image": {"url": "/images/v1/warbonds/scope.webp"}, "wiki": {"url": "https://w/scope"},
      "type": "premium", "releaseDate": "2026-10-20", "cost": {"currency": "super_credits", "amount": 1000},
      "medalsAllPages": 0, "pages": [{"number": 1}]
    }
  ]
}`

const fixturePassives = `{
  "meta": {"apiVersion": "v1", "dataVersion": "2026-10-15.test"},
  "data": [
    {"id": "blunt-force-mitigation", "name": "Blunt Force Mitigation"},
    {"id": "acclimated", "name": "Acclimated"}
  ]
}`

const fixtureGlossary = `{
  "updated_at": "2026-10-03T12:53:02Z",
  "terms": [
    {"en": "Ironclad Democracy", "zh": "民主铁壁"},
    {"en": "ZZ-9 Test Tool", "zh": "测试工具"}
  ]
}`

// fakeAPI 是一台假的上游：/v1/<集合>.json 返回样本，/terms.json 返回术语表。
type fakeAPI struct {
	server   *httptest.Server
	mu       sync.Mutex
	requests map[string]int
	fail     map[string]bool
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	api := &fakeAPI{requests: map[string]int{}, fail: map[string]bool{}}
	bodies := map[string]string{
		"/v1/weapons.json":    fixtureWeapons,
		"/v1/stratagems.json": fixtureStratagems,
		"/v1/armors.json":     fixtureArmors,
		"/v1/warbonds.json":   fixtureWarbonds,
		"/v1/passives.json":   fixturePassives,
		"/terms.json":         fixtureGlossary,
	}
	api.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		api.requests[r.URL.Path]++
		failed := api.fail[r.URL.Path]
		api.mu.Unlock()
		if failed {
			http.Error(w, "上游炸了", http.StatusInternalServerError)
			return
		}
		body, ok := bodies[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(api.server.Close)
	return api
}

func (a *fakeAPI) hit(path string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.requests[path]
}

func (a *fakeAPI) failPath(path string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.fail[path] = true
}

// newAPIStore 构造一个指向假上游、时钟固定的新源 Store。
func newAPIStore(t *testing.T, api *fakeAPI, dir string, now time.Time) *Store {
	t.Helper()
	return New(Config{
		Dir:        dir,
		TTL:        time.Hour,
		Timeout:    5 * time.Second,
		Source:     sourceKindHelldiversAPI,
		APIBase:    api.server.URL,
		ZhWikiBase: api.server.URL,
	}, Deps{Now: fixedNow(now), Logger: func(string, ...any) {}})
}

// mustCatalogFromAPI 走完整链路（拉集合 → 转换 → 落盘 → 解析）并返回目录。
func mustCatalogFromAPI(t *testing.T, api *fakeAPI) *Catalog {
	t.Helper()
	store := newAPIStore(t, api, t.TempDir(), time.Now())
	catalog, err := store.Catalog(context.Background())
	if err != nil {
		t.Fatalf("取目录失败：%v", err)
	}
	return catalog
}

// TestAPISourceBuildsParsableCatalog 校验转换出来的目录能被 ParseCatalog 解析，且字段映射正确。
func TestAPISourceBuildsParsableCatalog(t *testing.T) {
	catalog := mustCatalogFromAPI(t, newFakeAPI(t))

	weapon, ok := catalog.Item("ar-11-arbitrator")
	if !ok {
		t.Fatal("债券武器应进目录")
	}
	if weapon.Kind != KindPrimary || weapon.Model != "AR-11" || weapon.NameZh != "AR-11 仲裁者" {
		t.Errorf("武器基本字段不对：kind=%s model=%q nameZh=%q", weapon.Kind, weapon.Model, weapon.NameZh)
	}
	if weapon.WeaponType != "Assault Rifles" {
		t.Errorf("武器类型应取 statsRaw：%q", weapon.WeaponType)
	}
	if weapon.Acq.Kind != "warbond" || weapon.Acq.WarbondID != "ironclad-democracy" || weapon.Acq.Page != 1 || weapon.Acq.ItemMedals != 35 {
		t.Errorf("获取方式映射不对：%+v", weapon.Acq)
	}
	if weapon.Handling.Magazine != 45 || weapon.Handling.FireRate != 1200 || weapon.Handling.Recoil != 13.25 {
		t.Errorf("操作性数值映射不对：%+v", weapon.Handling)
	}
	if len(weapon.Handling.FiringModes) != 2 {
		t.Errorf("射击模式应去掉上游的 other：%+v", weapon.Handling.FiringModes)
	}
	if len(weapon.Components) != 2 {
		t.Fatalf("应有两个伤害部件：%+v", weapon.Components)
	}
	if weapon.Components[0].ArmorPenLabel != "轻型" || weapon.Components[0].ArmorPenValue != 2 {
		t.Errorf("穿甲映射不对：%+v", weapon.Components[0])
	}
	// 第二段伤害的 type 是通用值 Damage，应退回攻击类型 projectile。
	if weapon.Components[1].Label != "projectile" {
		t.Errorf("通用伤害类型应退回攻击类型，实际 %q", weapon.Components[1].Label)
	}
	if !strings.HasPrefix(weapon.ImagePath, "http") || !strings.HasSuffix(weapon.ImagePath, ".webp") {
		t.Errorf("图片应是上游的绝对地址：%q", weapon.ImagePath)
	}
	if !strings.Contains(weapon.ImageFilePage, "File:AR-11_Arbitrator_Primary_Render.png") {
		t.Errorf("应带上 wiki 文件页（行内缩略图要用）：%q", weapon.ImageFilePage)
	}

	grenade, ok := catalog.Item("g-8-immolation")
	if !ok || grenade.Kind != KindGrenade {
		t.Fatalf("投掷物应归入手雷：%+v", grenade)
	}
	if grenade.Handling.Magazine != 6 {
		t.Errorf("手雷的携带数应来自 throwable.capacity：%+v", grenade.Handling)
	}

	support, ok := catalog.Item("mg-43-machine-gun")
	if !ok || support.Kind != KindSupport {
		t.Fatalf("支援武器应归入支援武器：%+v", support)
	}
	if support.Deploy == nil || support.Deploy.Type != "Support Weapon" || support.Deploy.CooldownSeconds != 480 {
		t.Errorf("部署信息映射不对：%+v", support.Deploy)
	}
	if len(support.Deploy.Code) != 5 || support.Deploy.Code[0] != "down" {
		t.Errorf("召唤指令映射不对：%+v", support.Deploy.Code)
	}
	if support.Acq.Kind != "requisition" || support.Acq.RequisitionPoints != 6000 || support.Acq.LevelRequired != 1 {
		t.Errorf("征用点获取方式映射不对：%+v", support.Acq)
	}
	if support.Handling.Magazine != 175 {
		t.Errorf("支援武器的操作性数值应取 supportWeapon：%+v", support.Handling)
	}

	sentry, ok := catalog.Item("a-ac-8-autocannon-sentry")
	if !ok || sentry.Kind != KindStratagem {
		t.Fatalf("哨戒炮应归入战备：%+v", sentry)
	}
	if sentry.Deploy == nil || sentry.Deploy.Type != "Sentry" {
		t.Errorf("哨戒炮的部署类型不对：%+v", sentry.Deploy)
	}
	if sentry.Components[0].ArmorPenLabel != "反坦克 I" {
		t.Errorf("反坦克穿甲标注不对：%+v", sentry.Components[0])
	}

	armor, ok := catalog.Item("bfm-220-ironclad")
	if !ok || armor.Kind != KindArmor {
		t.Fatalf("护甲应进目录：%+v", armor)
	}
	if armor.Armor == nil || armor.Armor.Class != "Heavy" || armor.Armor.Rating != 175 || armor.Armor.Speed != 450 {
		t.Errorf("护甲数值映射不对：%+v", armor.Armor)
	}
	if armor.Armor.Passive != "Blunt Force Mitigation" {
		t.Errorf("被动名应由 passiveId 反查：%q", armor.Armor.Passive)
	}
	// 内置表里没有的装备，中文名由运行期术语表补上。
	testTool, ok := catalog.Item("zz-9-test-tool")
	if !ok || testTool.NameZh != "测试工具" {
		t.Errorf("术语表应补上内置表没有的中文名：%+v", testTool.NameZh)
	}
	if testTool.Acq.Kind != "superstore" {
		t.Errorf("超级商店获取方式映射不对：%+v", testTool.Acq)
	}
}

// TestAPISourceKeepsUpcomingWithLabel 校验未上线（upcoming）的装备与债券照收，但标成「未上线」：
// 群里问的往往就是还没上线、但已经公布的那本债券与几件装备，直接丢掉等于「查不到」。
// 真正不该进目录的是任务流程战备与 civilian 武器。
func TestAPISourceKeepsUpcomingWithLabel(t *testing.T) {
	catalog := mustCatalogFromAPI(t, newFakeAPI(t))

	sweeper, ok := catalog.Item("sg-97-sweeper")
	if !ok {
		t.Fatal("未上线的装备应保留在目录里")
	}
	if sweeper.Acq.Kind != "upcoming" {
		t.Errorf("未上线的装备应标成 upcoming，实际 %+v", sweeper.Acq)
	}
	if got := AcqLabel(sweeper.Acq.Kind); got != "未上线" {
		t.Errorf("获取方式应显示「未上线」，实际 %q", got)
	}

	scope, ok := catalog.Warbond("scope-of-authority")
	if !ok {
		t.Fatal("未上线的债券应保留在名单里")
	}
	if !scope.Upcoming {
		t.Error("未上线的债券要带上 Upcoming 标记，卡片据此显示「未上线」")
	}

	for _, id := range []string{"reinforce", "break-action-shotgun-civilian"} {
		if _, ok := catalog.Item(id); ok {
			t.Errorf("%s 不该进目录", id)
		}
	}
	// 已下线的战备要保留（老目录里也有，卡片上标「已下架」）。
	frv, ok := catalog.Item("m-104-incinerator-frv")
	if !ok || frv.Acq.Kind != "unavailable" {
		t.Errorf("已下线的战备应保留并标成 unavailable：%+v", frv.Acq)
	}
}

// TestAPISourceWarbondFieldsAndOrder 校验债券字段与排序：按上线日期，老本在前，未上线的排最后。
func TestAPISourceWarbondFieldsAndOrder(t *testing.T) {
	catalog := mustCatalogFromAPI(t, newFakeAPI(t))

	books := catalog.Warbonds()
	if len(books) != 3 {
		t.Fatalf("应有 3 本债券（含未上线那本），实际 %d", len(books))
	}
	if books[0].ID != "helldivers-mobilize" || books[1].ID != "ironclad-democracy" || books[2].ID != "scope-of-authority" {
		t.Errorf("债券应按上线日期排序，实际 %s → %s → %s", books[0].ID, books[1].ID, books[2].ID)
	}
	ironclad := books[1]
	if ironclad.NameZh != "民主铁壁" || ironclad.Pages != 3 || ironclad.MedalsTotal != 360 || ironclad.SuperCredits != 1000 {
		t.Errorf("债券字段映射不对：%+v", ironclad)
	}
	if ironclad.ItemCount != 3 {
		t.Errorf("债券件数应反查得到 3 件（两件武器 + 一件护甲）：%d", ironclad.ItemCount)
	}
	if ironclad.Upcoming {
		t.Error("已上线的债券不该带未上线标记")
	}
}

// TestAPISourceOrderIsStablePerWarbond 校验债券装备按「页码 → 勋章价」排在目录前部。
func TestAPISourceOrderIsStablePerWarbond(t *testing.T) {
	catalog := mustCatalogFromAPI(t, newFakeAPI(t))

	items := catalog.Items()
	if len(items) < 2 || items[0].ID != "ar-23-liberator" {
		// 第一件是非债券装备（default），债券装备在它后面按债券顺序排。
		t.Logf("目录前几件：%s", firstIDs(items, 4))
	}
	index := make(map[string]int, len(items))
	for i, item := range items {
		index[item.ID] = i
	}
	if index["ar-11-arbitrator"] > index["ar-23-liberator"] {
		t.Errorf("债券装备应排在非债券装备之前：%s=%d %s=%d",
			"ar-11-arbitrator", index["ar-11-arbitrator"], "ar-23-liberator", index["ar-23-liberator"])
	}
	if index["g-8-immolation"] < index["ar-11-arbitrator"] {
		t.Errorf("同一页内应按勋章价升序：%s=%d %s=%d",
			"g-8-immolation", index["g-8-immolation"], "ar-11-arbitrator", index["ar-11-arbitrator"])
	}
}

func firstIDs(items []Item, n int) string {
	ids := make([]string, 0, n)
	for i, item := range items {
		if i >= n {
			break
		}
		ids = append(ids, item.ID)
	}
	return strings.Join(ids, ", ")
}

// readCacheFile 读缓存目录下的文件；读不到直接让用例失败。
func readCacheFile(t *testing.T, dir, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("读缓存文件失败：%v", err)
	}
	return raw
}

// TestAPISourceSurvivesGlossaryFailure 校验术语表拉不到时目录照常生成（中文名退回内置表/英文名）。
func TestAPISourceSurvivesGlossaryFailure(t *testing.T) {
	api := newFakeAPI(t)
	api.failPath("/terms.json")

	catalog := mustCatalogFromAPI(t, api)
	weapon, ok := catalog.Item("ar-11-arbitrator")
	if !ok {
		t.Fatal("术语表挂掉不该影响目录")
	}
	if weapon.NameZh != "AR-11 仲裁者" {
		t.Errorf("内置中文名表应兜住：%q", weapon.NameZh)
	}
	// 只有术语表里才有的新装备这时回退英文名，而不是空标题。
	if tool, ok := catalog.Item("zz-9-test-tool"); !ok || tool.NameZh != "ZZ-9 Test Tool" {
		t.Errorf("没有术语表时应回退英文名：%+v", tool.NameZh)
	}
}

// TestAPISourceFailsWhenCollectionFails 校验集合拉不到就整体失败（宁可让 Store 用旧快照）。
func TestAPISourceFailsWhenCollectionFails(t *testing.T) {
	api := newFakeAPI(t)
	api.failPath("/v1/warbonds.json")

	store := newAPIStore(t, api, t.TempDir(), time.Now())
	if _, err := store.Catalog(context.Background()); err == nil {
		t.Fatal("集合缺失时应报错")
	} else if !strings.Contains(err.Error(), "warbonds.json") {
		t.Errorf("错误应说明是哪个集合失败：%v", err)
	} else if !strings.Contains(err.Error(), "http") {
		// 报错要带上完整地址：配置里 api_base 写错（多一段路径、少个域名）时，
		// 只看「下载 warbonds.json 失败」查不出打到了哪儿去。
		t.Errorf("错误应带上实际请求的地址：%v", err)
	}
}

// TestAPISourceNormalizesBase 校验 api_base 末尾多写斜杠也能用：
// 配置里手写地址时最容易多带一个 /，不该因此多一条斜杠就报错。
func TestAPISourceNormalizesBase(t *testing.T) {
	store := New(Config{
		Dir:     t.TempDir(),
		Source:  sourceKindHelldiversAPI,
		APIBase: "https://api.test/",
	}, Deps{})
	source := store.src

	if got := source.ImageURLs("/images/v1/weapons/a.webp"); len(got) != 1 || got[0] != "https://api.test/images/v1/weapons/a.webp" {
		t.Errorf("末尾斜杠应被裁掉，实际：%+v", got)
	}
}

// TestAPISourceUsesDiskSnapshotAndTTL 校验新源也走同一套磁盘缓存：TTL 内不联网，过期后重新拉。
func TestAPISourceUsesDiskSnapshotAndTTL(t *testing.T) {
	api := newFakeAPI(t)
	dir := t.TempDir()
	now := time.Now()
	store := newAPIStore(t, api, dir, now)

	if _, err := store.Catalog(context.Background()); err != nil {
		t.Fatalf("首次取目录失败：%v", err)
	}
	first := api.hit("/v1/weapons.json")

	// 换一个 Store（模拟重启），TTL 内应直接读磁盘快照。
	restarted := newAPIStore(t, api, dir, now.Add(time.Minute))
	if _, err := restarted.Catalog(context.Background()); err != nil {
		t.Fatalf("重启后取目录失败：%v", err)
	}
	if api.hit("/v1/weapons.json") != first {
		t.Error("TTL 内不该重新联网")
	}

	// 快照文件必须是目录 JSON，而不是上游集合（换源时旧快照要能继续用）。
	raw := readCacheFile(t, dir, catalogFileName)
	var parsed rawCatalog
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("快照应是目录 JSON：%v", err)
	}
	if len(parsed.Items) == 0 || parsed.Meta.DataVersion != "2026-10-15.test" {
		t.Errorf("快照内容不对：items=%d version=%q", len(parsed.Items), parsed.Meta.DataVersion)
	}
}

// TestAPISourceImageURLs 校验图片候选地址：绝对地址原样用，相对地址拼上游站点。
func TestAPISourceImageURLs(t *testing.T) {
	store := New(Config{Dir: t.TempDir(), Source: sourceKindHelldiversAPI, APIBase: "https://api.test/"}, Deps{})
	source := store.src

	if got := source.ImageURLs("https://cdn.test/a.webp"); len(got) != 1 || got[0] != "https://cdn.test/a.webp" {
		t.Errorf("绝对地址应原样返回：%+v", got)
	}
	if got := source.ImageURLs("/images/v1/weapons/a.webp"); len(got) != 1 || got[0] != "https://api.test/images/v1/weapons/a.webp" {
		t.Errorf("相对地址应拼上游站点：%+v", got)
	}
	if got := source.ImageURLs("  "); got != nil {
		t.Errorf("空地址不该给出候选：%+v", got)
	}
}

// TestAPISourceImageURLsKeepLegacyPaths 校验「换到新源、磁盘快照还是老源的」这段混搭期取图仍走老路。
//
// 老快照里图片路径是仓库内相对路径（assets/wiki/xxx.png），把它拼到新源站点上只会 404 ——
// 线上表现就是日志里一条条 `下载装备图失败：HTTP 404`、群里卡片全没图。
func TestAPISourceImageURLsKeepLegacyPaths(t *testing.T) {
	store := New(Config{Dir: t.TempDir(), Source: sourceKindHelldiversAPI, APIBase: "https://api.test/"}, Deps{})
	source := store.src

	got := source.ImageURLs("assets/wiki/ar-2-coyote.png")
	if len(got) == 0 {
		t.Fatal("老快照的图片路径也要给出候选地址")
	}
	for _, url := range got {
		if strings.Contains(url, "api.test") {
			t.Errorf("老路径不该拼到新源站点上（那是 404 的来源）：%+v", got)
		}
	}
	if !strings.Contains(got[0], "ar-2-coyote.png") {
		t.Errorf("候选地址里应包含原文件名：%+v", got)
	}
	if len(got) < 2 {
		t.Errorf("老路径应同时给主地址与镜像地址：%+v", got)
	}
}

// TestAPISourceModelHeuristic 校验型号提取：带数字的型号前缀才算型号。
func TestAPISourceModelHeuristic(t *testing.T) {
	cases := map[string]string{
		"AR-11 Arbitrator":         "AR-11",
		"A/AC-8 Autocannon Sentry": "A/AC-8",
		"EXO-45 Patriot Exosuit":   "EXO-45",
		"Eagle Airstrike":          "",
		"Resupply":                 "",
		"TD-110 Maelstrom":         "TD-110",
		"FLAM-66 Torcher":          "FLAM-66",
		"Orbital 120mm HE Barrage": "",
		"MG-43 Machine Gun":        "MG-43",
		"StA-X3 W.A.S.P. Launcher": "StA-X3",
		"BFM-220 Ironclad":         "BFM-220",
		"100 Super Credits":        "",
	}
	for name, want := range cases {
		if got := modelOf(name); got != want {
			t.Errorf("modelOf(%q) = %q，期望 %q", name, got, want)
		}
	}
}

// TestAPISourceUnknownSourceKindFallsBack 校验 arsenal.source 写错时退回默认源，而不是整个图鉴挂掉。
func TestAPISourceUnknownSourceKindFallsBack(t *testing.T) {
	store := New(Config{Dir: t.TempDir(), Source: "不认识的源"}, Deps{})
	if store.Source() != sourceKindHelldiversAPI {
		t.Errorf("未知取值应退回默认源，实际 %q", store.Source())
	}
}
