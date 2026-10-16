// 本文件是默认数据源：helldivers-api（https://helldivers-api.dionatha.com.br）。
//
// 它是社区里一个每天定时抓 helldivers.wiki.gg 的静态 JSON API（抓取任务在 GitHub Actions 上跑，
// 见上游仓库 DionathaGoulart/Helldivers-api 的 .github/workflows/scrape.yml），
// 因此游戏出新债券 / 新装备后，目录能自己跟上，不需要我们发版。
//
// 为什么要「转换」而不是直接改解析层：上游 schema 与本项目的目录 schema 差别很大
// （数值结构、枚举取值、获取方式字段都不一样）。这里把新源的数据**在本地转成 HD2Tool 形态的
// catalog JSON**，于是解析、索引、检索、磁盘快照、降级、插件层全都不用动——
// 快照文件也还是同一份格式，切回老源或读旧快照都不会坏。
package arsenal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const (
	// sourceKindHelldiversAPI / sourceKindHD2Tool 是 arsenal.source 的两个取值。
	sourceKindHelldiversAPI = "helldivers-api"
	sourceKindHD2Tool       = "hd2tool"

	// defaultAPIBase 是默认源地址（无 key、静态文件不限量，见上游 README）。
	defaultAPIBase = "https://helldivers-api.dionatha.com.br"
	// legacyImagePrefix 是老源（HD2Tool）快照里的图片路径前缀：那是 HD2Tool 仓库内的相对路径
	// （assets/wiki/xxx.png），与安装源无关，取图要回 GitHub —— 见 apiSource.ImageURLs。
	legacyImagePrefix = "assets/"
	// defaultZhWikiBase 是中文名术语表的地址（社区中文维基，GitHub Pages）。
	defaultZhWikiBase = "https://jerry114514.github.io/HD2_Wiki/data/wiki/zh"
	// apiMaxBytes 是单个集合的下载上限。实测最大的是 stratagems.json（~850KB），给 16MB 足够富余。
	apiMaxBytes = 16 << 20
	// zhWikiMaxBytes 是术语表上限。实测 ~220KB。
	zhWikiMaxBytes = 8 << 20
	// imageLicense 是新源图片的许可标注：上游仓库按 CC BY-NC-SA 4.0 发布其内容（图片取自 wiki.gg）。
	imageLicense = "License/CC-BY-NC-SA"
)

// apiSource 用「拉集合 → 转换 → 输出目录 JSON」的方式实现 Source。
type apiSource struct {
	store *Store
	base  string
	zhURL string
}

// newAPISource 装配新源；地址与超时都来自 Store 的配置。
func newAPISource(store *Store) *apiSource {
	base := strings.TrimSpace(store.cfg.APIBase)
	if base == "" {
		base = defaultAPIBase
	}
	zh := strings.TrimSpace(store.cfg.ZhWikiBase)
	switch zh {
	case "-":
		zh = ""
	case "":
		zh = defaultZhWikiBase
	}
	return &apiSource{store: store, base: strings.TrimSuffix(base, "/"), zhURL: strings.TrimSuffix(zh, "/")}
}

func (s *apiSource) Name() string { return sourceKindHelldiversAPI }

// AliasesJSON 返回 nil：新源没有社区外号表，磁盘上的旧外号表继续沿用。
func (s *apiSource) AliasesJSON(context.Context) []byte { return nil }

// ImageURLs 把目录里的图片地址展开成候选地址。
//
// 三种情况：
//   - 绝对地址（新源自己的 CDN）：原样用，只有一个候选；
//   - 站内相对地址（/images/v1/...）：拼上游站点；
//   - 仓库内相对地址（assets/wiki/...）：这是**老源（HD2Tool）快照**里的写法。
//     刚换源时磁盘快照还是老格式，把它拼到新站点上只会 404 ——
//     线上表现就是日志里一连串「下载装备图失败：HTTP 404」、群里卡片全没图。
//     所以这种路径交给老源的取图规则（GitHub 原始地址 + 镜像），等目录刷新成新格式后自然消失。
func (s *apiSource) ImageURLs(imagePath string) []string {
	trimmed := strings.TrimSpace(imagePath)
	if trimmed == "" {
		return nil
	}
	if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
		return []string{trimmed}
	}
	if strings.HasPrefix(trimmed, legacyImagePrefix) {
		return s.store.rawURLs("public/" + trimmed)
	}
	return []string{s.base + "/" + strings.TrimPrefix(trimmed, "/")}
}

// CatalogJSON 拉取各集合、合并中文名、转换成目录 JSON。
func (s *apiSource) CatalogJSON(ctx context.Context) ([]byte, error) {
	dataset, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	resolver := newZhResolver(loadLocalZhTables(s.store.zhNamesPath()), dataset.glossary)
	catalog, stats, err := buildCatalogJSON(dataset, resolver, s.base)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(catalog)
	if err != nil {
		return nil, fmt.Errorf("序列化目录失败：%w", err)
	}
	// 把「跳过了什么」写进日志：群里问「怎么查不到某件装备」时，这一行就能回答。
	// 未上线的装备与债券会收进目录但标成「未上线」（不算跳过），所以只有后两类是「已跳过」。
	s.store.log("[arsenal] 目录已生成：%d 件装备 / %d 本债券（数据版本 %s，源 %s；未上线 %d 件已收录并标注、已跳过：任务类战备 %d 件、civilian 类 %d 件）",
		len(catalog.Items), len(catalog.Warbonds), catalog.Meta.DataVersion, s.base,
		stats.upcoming, stats.missionish, stats.civilian)
	return raw, nil
}

// apiDataset 是一次刷新拉到的全部集合。
type apiDataset struct {
	weapons    apiEnvelope[apiWeapon]
	stratagems apiEnvelope[apiStratagem]
	armors     apiEnvelope[apiArmor]
	warbonds   apiEnvelope[apiWarbond]
	passives   apiEnvelope[apiPassive]
	glossary   map[string]string
}

// load 并行拉取各集合。任何一个失败都算失败（宁可让 Store 用磁盘旧快照，
// 也不要拿半份目录去替换一份完整的旧快照）。
// 中文名术语表是锦上添花：失败只记日志，不影响目录。
func (s *apiSource) load(ctx context.Context) (*apiDataset, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	dataset := &apiDataset{}
	jobs := []struct {
		path string
		into any
	}{
		{"weapons.json", &dataset.weapons},
		{"stratagems.json", &dataset.stratagems},
		{"armors.json", &dataset.armors},
		{"warbonds.json", &dataset.warbonds},
		{"passives.json", &dataset.passives},
	}

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	for _, job := range jobs {
		wg.Add(1)
		go func(path string, into any) {
			defer wg.Done()
			url := s.base + "/v1/" + path
			if err := s.getJSON(ctx, url, apiMaxBytes, into); err != nil {
				mu.Lock()
				if firstErr == nil {
					// 报错带上**完整地址**：api_base 写错（多一段路径、域名写错）时，
					// 只看「下载 warbonds.json 失败」根本查不出请求打到了哪儿去。
					firstErr = fmt.Errorf("下载 %s 失败：%w", url, err)
				}
				mu.Unlock()
				cancel()
			}
		}(job.path, job.into)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if len(dataset.weapons.Data) == 0 || len(dataset.warbonds.Data) == 0 {
		return nil, fmt.Errorf("上游返回的目录是空的（武器 %d / 债券 %d）", len(dataset.weapons.Data), len(dataset.warbonds.Data))
	}
	dataset.glossary = s.loadGlossary(ctx)
	return dataset, nil
}

// loadGlossary 拉一次中文维基的术语表（英文名 → 中文名）；失败返回 nil。
func (s *apiSource) loadGlossary(ctx context.Context) map[string]string {
	if s.zhURL == "" {
		return nil
	}
	var parsed glossaryFile
	if err := s.getJSON(ctx, s.zhURL+"/terms.json", zhWikiMaxBytes, &parsed); err != nil {
		s.store.log("[arsenal] 中文名术语表拉取失败（新装备暂时显示英文名）：%v", err)
		return nil
	}
	glossary := parseGlossaryTerms(parsed)
	if len(glossary) == 0 {
		s.store.log("[arsenal] 中文名术语表是空的，当作没有")
		return nil
	}
	return glossary
}

// getJSON 下载并解析一个 JSON 地址，超过 limit 的响应直接判失败。
func (s *apiSource) getJSON(ctx context.Context, rawURL string, limit int64, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := s.store.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return err
	}
	if int64(len(raw)) > limit {
		return fmt.Errorf("响应超过 %d 字节上限", limit)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return fmt.Errorf("解析 JSON 失败：%w", err)
	}
	return nil
}

// ---- 上游 schema（只声明用得上的字段） ----

// apiNumber 是「上游写数字、写数字字符串还是 null 都认」的数值类型。
// 外部数据源随时可能把某个字段写成字符串，一个脏值不该让整份目录转换失败。
type apiNumber float64

func (n *apiNumber) UnmarshalJSON(raw []byte) error {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" {
		return nil
	}
	if text[0] == '"' {
		unquoted, err := strconv.Unquote(text)
		if err != nil {
			return nil
		}
		text = strings.TrimSpace(unquoted)
	}
	if f, err := strconv.ParseFloat(text, 64); err == nil {
		*n = apiNumber(f)
	}
	return nil
}

// Int 取整（小数四舍五入）；零值就是「上游没给」。
func (n apiNumber) Int() int { return int(math.Round(float64(n))) }

// Float 取浮点原值。
func (n apiNumber) Float() float64 { return float64(n) }

// Set 表示上游确实给了这个值（非零）。
func (n apiNumber) Set() bool { return float64(n) != 0 }

type apiMeta struct {
	DataVersion string `json:"dataVersion"`
	GeneratedAt string `json:"generatedAt"`
	Count       int    `json:"count"`
}

type apiEnvelope[T any] struct {
	Meta apiMeta `json:"meta"`
	Data []T     `json:"data"`
}

type apiImage struct {
	URL      string `json:"url"`
	WikiFile string `json:"wikiFile"`
}

type apiWiki struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

type apiCost struct {
	Currency string    `json:"currency"`
	Amount   apiNumber `json:"amount"`
}

type apiOrigin struct {
	Type      string    `json:"type"`
	Label     string    `json:"label"`
	WarbondID string    `json:"warbondId"`
	Page      apiNumber `json:"page"`
	Cost      apiCost   `json:"cost"`
}

// apiFirearm 是枪械通用数值：武器的 firearm、支援武器的 supportWeapon 都是这个形状。
type apiFirearm struct {
	FiringModes    []string  `json:"firingModes"`
	FireRateRPM    []float64 `json:"fireRateRpm"`
	Capacity       apiNumber `json:"capacity"`
	SpareMagazines apiNumber `json:"spareMagazines"`
	Recoil         apiNumber `json:"recoil"`
}

type apiThrowable struct {
	Capacity apiNumber `json:"capacity"`
}

type apiAttack struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Damage struct {
		Standard apiNumber `json:"standard"`
		Durable  apiNumber `json:"durable"`
		Type     string    `json:"type"`
	} `json:"damage"`
	Penetration struct {
		Direct string `json:"direct"`
	} `json:"penetration"`
	Forces struct {
		Demolition apiNumber `json:"demolition"`
		Stagger    apiNumber `json:"stagger"`
		Push       apiNumber `json:"push"`
	} `json:"forces"`
}

type apiWeapon struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Upcoming  bool              `json:"upcoming"`
	Image     apiImage          `json:"image"`
	Wiki      apiWiki           `json:"wiki"`
	Category  string            `json:"category"`
	Firearm   *apiFirearm       `json:"firearm"`
	Throwable *apiThrowable     `json:"throwable"`
	Attacks   []apiAttack       `json:"attacks"`
	StatsRaw  map[string]string `json:"statsRaw"`
	Source    apiOrigin         `json:"source"`
}

type apiStratagem struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Upcoming      bool              `json:"upcoming"`
	Image         apiImage          `json:"image"`
	Wiki          apiWiki           `json:"wiki"`
	Availability  string            `json:"availability"`
	Kind          string            `json:"kind"`
	Code          []string          `json:"code"`
	CooldownS     apiNumber         `json:"cooldownS"`
	CallInTimeS   apiNumber         `json:"callInTimeS"`
	UnlockLevel   apiNumber         `json:"unlockLevel"`
	SupportWeapon *apiFirearm       `json:"supportWeapon"`
	Attacks       []apiAttack       `json:"attacks"`
	StatsRaw      map[string]string `json:"statsRaw"`
	Source        apiOrigin         `json:"source"`
}

type apiArmor struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Upcoming     bool      `json:"upcoming"`
	Image        apiImage  `json:"image"`
	Wiki         apiWiki   `json:"wiki"`
	Weight       string    `json:"weight"`
	ArmorRating  apiNumber `json:"armorRating"`
	Speed        apiNumber `json:"speed"`
	StaminaRegen apiNumber `json:"staminaRegen"`
	PassiveID    string    `json:"passiveId"`
	Source       apiOrigin `json:"source"`
}

type apiPassive struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type apiWarbond struct {
	ID             string           `json:"id"`
	Name           string           `json:"name"`
	Upcoming       bool             `json:"upcoming"`
	Image          apiImage         `json:"image"`
	Wiki           apiWiki          `json:"wiki"`
	Type           string           `json:"type"`
	ReleaseDate    string           `json:"releaseDate"`
	Cost           apiCost          `json:"cost"`
	MedalsAllPages apiNumber        `json:"medalsAllPages"`
	Pages          []apiWarbondPage `json:"pages"`
}

type apiWarbondPage struct {
	Number apiNumber `json:"number"`
}

// ---- 目标 schema：HD2Tool 形态的目录 ----

type outCatalog struct {
	Meta     outMeta      `json:"meta"`
	Warbonds []outWarbond `json:"warbonds"`
	Items    []outItem    `json:"items"`
}

type outMeta struct {
	DataVersion string `json:"dataVersion"`
	CapturedAt  string `json:"capturedAt"`
}

type outWiki struct {
	URL string `json:"url"`
}

type outPage struct {
	Page             int `json:"page"`
	CumulativeMedals int `json:"cumulativeMedals"`
}

type outWarbond struct {
	ID           string    `json:"id"`
	NameZh       string    `json:"nameZh,omitempty"`
	NameEn       string    `json:"nameEn"`
	SuperCredits int       `json:"superCredits"`
	Pages        []outPage `json:"pages"`
	// Upcoming 表示这本债券还没上线（上游 upcoming: true）：卡片上标「未上线」，
	// 内容照收，因为群里问的往往就是「新债券出了没、有什么」。
	Upcoming bool    `json:"upcoming,omitempty"`
	Wiki     outWiki `json:"wiki"`
}

type outImage struct {
	Path     string `json:"path"`
	Alt      string `json:"alt,omitempty"`
	FilePage string `json:"filePage,omitempty"`
	License  string `json:"license,omitempty"`
}

type outAcq struct {
	Kind              string `json:"kind"`
	WarbondID         string `json:"warbondId,omitempty"`
	Page              int    `json:"page,omitempty"`
	ItemMedals        int    `json:"itemMedals,omitempty"`
	LevelRequired     int    `json:"levelRequired,omitempty"`
	RequisitionPoints int    `json:"requisitionPoints,omitempty"`
}

type outComponentFields struct {
	StandardDamage   int `json:"standardDamage,omitempty"`
	DurableDamage    int `json:"durableDamage,omitempty"`
	DemolitionForce  int `json:"demolitionForce,omitempty"`
	Stagger          int `json:"stagger,omitempty"`
	Push             int `json:"push,omitempty"`
	ArmorPenetration struct {
		Value   int    `json:"value,omitempty"`
		LabelZh string `json:"labelZh,omitempty"`
	} `json:"armorPenetration"`
}

type outComponent struct {
	Label  string             `json:"label"`
	Fields outComponentFields `json:"fields"`
}

type outCombat struct {
	Components         []outComponent `json:"components"`
	PrimaryComponentID string         `json:"primaryComponentId,omitempty"`
}

type outHandling struct {
	Magazine       int      `json:"magazine,omitempty"`
	SpareMagazines int      `json:"spareMagazines,omitempty"`
	FireRate       int      `json:"fireRate,omitempty"`
	Recoil         float64  `json:"recoil,omitempty"`
	FiringModes    []string `json:"firingModes,omitempty"`
}

type outArmor struct {
	Class        string `json:"class"`
	Rating       int    `json:"rating,omitempty"`
	Speed        int    `json:"speed,omitempty"`
	StaminaRegen int    `json:"staminaRegen,omitempty"`
	Passive      string `json:"passive,omitempty"`
}

type outDeployment struct {
	Type            string   `json:"type,omitempty"`
	Code            []string `json:"code,omitempty"`
	CooldownSeconds int      `json:"cooldownSeconds,omitempty"`
	CallInSeconds   float64  `json:"callInSeconds,omitempty"`
}

type outItem struct {
	ID          string         `json:"id"`
	ProductKind string         `json:"productKind"`
	Model       string         `json:"model,omitempty"`
	NameZh      string         `json:"nameZh,omitempty"`
	NameEn      string         `json:"nameEn"`
	WeaponType  string         `json:"weaponType,omitempty"`
	Image       outImage       `json:"image"`
	Wiki        outWiki        `json:"wiki"`
	Acquisition outAcq         `json:"acquisition"`
	Combat      *outCombat     `json:"combat,omitempty"`
	Handling    *outHandling   `json:"handling,omitempty"`
	Armor       *outArmor      `json:"armor,omitempty"`
	Deployment  *outDeployment `json:"deployment,omitempty"`
}

// conversionStats 记下「转换时跳过了多少条目」，只用于日志。
type conversionStats struct {
	upcoming   int // 还没上线的装备与债券
	missionish int // 任务流程里的战备（增援、补给、上传数据…）
	civilian   int // 不在目录范围内的武器（civilian 类，以及缺 id/名字的脏数据）
}

// buildCatalogJSON 把新源的数据转成目录。upcoming 的条目（还没上线）直接不收：
// 群里查到的应该是「游戏里能拿到的东西」，等上线后上游把 upcoming 翻成 false，下一轮刷新自然就有了。
// base 是上游站点地址，用来把站内图片路径补成绝对地址。
func buildCatalogJSON(dataset *apiDataset, zh *zhResolver, base string) (*outCatalog, *conversionStats, error) {
	stats := &conversionStats{}
	catalog := &outCatalog{}
	catalog.Meta.DataVersion = dataset.weapons.Meta.DataVersion
	catalog.Meta.CapturedAt = dataset.weapons.Meta.GeneratedAt

	passiveNames := make(map[string]string, len(dataset.passives.Data))
	for _, passive := range dataset.passives.Data {
		if id := strings.TrimSpace(passive.ID); id != "" {
			passiveNames[id] = strings.TrimSpace(passive.Name)
		}
	}

	// 债券：按上线日期排序（没写日期的排最后），并记下顺序供装备排序用。
	// 还没上线的也收，只是在卡片上标「未上线」：群里问的往往就是「新债券出了没、有什么」。
	warbonds := make([]apiWarbond, 0, len(dataset.warbonds.Data))
	for _, warbond := range dataset.warbonds.Data {
		if warbond.Upcoming {
			stats.upcoming++
		}
		warbonds = append(warbonds, warbond)
	}
	sort.SliceStable(warbonds, func(i, j int) bool {
		left, right := strings.TrimSpace(warbonds[i].ReleaseDate), strings.TrimSpace(warbonds[j].ReleaseDate)
		switch {
		case left == "" && right == "":
			return warbonds[i].Name < warbonds[j].Name
		case left == "":
			return false
		case right == "":
			return true
		case left != right:
			return left < right
		default:
			return warbonds[i].Name < warbonds[j].Name
		}
	})
	warbondOrder := make(map[string]int, len(warbonds))
	for index, warbond := range warbonds {
		id := strings.TrimSpace(warbond.ID)
		if id == "" {
			continue
		}
		warbondOrder[id] = index
		pages := make([]outPage, 0, len(warbond.Pages))
		for _, page := range warbond.Pages {
			pages = append(pages, outPage{Page: page.Number.Int()})
		}
		// 上游只给「解锁全部页」的总勋章，没有逐页累计值；解析层取的是「各页累计值的最大值」，
		// 所以把总数写在最后一页，语义与老目录一致。
		if len(pages) > 0 && warbond.MedalsAllPages.Set() {
			pages[len(pages)-1].CumulativeMedals = warbond.MedalsAllPages.Int()
		}
		nameEn := strings.TrimSpace(warbond.Name)
		out := outWarbond{
			ID:           id,
			NameZh:       zh.Warbond(id, nameEn),
			NameEn:       nameEn,
			SuperCredits: superCreditsOf(warbond.Cost),
			Pages:        pages,
			Upcoming:     warbond.Upcoming,
			Wiki:         outWiki{URL: warbond.Wiki.URL},
		}
		catalog.Warbonds = append(catalog.Warbonds, out)
	}

	records := make([]itemRecord, 0, len(dataset.weapons.Data)+len(dataset.stratagems.Data)+len(dataset.armors.Data))
	appendItem := func(item outItem) {
		order, isWarbond := warbondOrder[item.Acquisition.WarbondID]
		if !isWarbond {
			order = -1
		}
		records = append(records, itemRecord{
			item:    item,
			wbOrder: order,
			page:    item.Acquisition.Page,
			cost:    item.Acquisition.ItemMedals,
			kindSeq: kindSequence(item.ProductKind),
		})
	}

	for _, weapon := range dataset.weapons.Data {
		item, ok := weaponItemOf(weapon, zh, base)
		if !ok {
			stats.civilian++
			continue
		}
		markUpcoming(&item, weapon.Upcoming, stats)
		appendItem(item)
	}
	for _, stratagem := range dataset.stratagems.Data {
		item, ok := stratagemItemOf(stratagem, zh, base)
		if !ok {
			stats.missionish++
			continue
		}
		markUpcoming(&item, stratagem.Upcoming, stats)
		appendItem(item)
	}
	for _, armor := range dataset.armors.Data {
		item := armorItemOf(armor, zh, passiveNames, base)
		markUpcoming(&item, armor.Upcoming, stats)
		appendItem(item)
	}

	if len(records) == 0 {
		return nil, nil, fmt.Errorf("转换后目录为空：上游数据可能换了结构")
	}
	sort.SliceStable(records, func(i, j int) bool { return itemRecordLess(records[i], records[j]) })
	catalog.Items = make([]outItem, 0, len(records))
	for _, record := range records {
		catalog.Items = append(catalog.Items, record.item)
	}
	return catalog, stats, nil
}

// markUpcoming 把还没上线的装备标成「未上线」（获取方式 kind=upcoming）。
//
// 为什么收而不是丢：群里问的往往就是「新装备/新债券出了没」，丢掉等于「查不到」。
// 但获取方式不再是 warbond/requisition，所以入群验证的题库会自然跳过它 ——
// 不能让新成员去点一件游戏里还点不出来的东西。
func markUpcoming(item *outItem, upcoming bool, stats *conversionStats) {
	if !upcoming {
		return
	}
	stats.upcoming++
	item.Acquisition.Kind = "upcoming"
}

// superCreditsOf 取超级货币价（老目录的 superCredits 字段）。
func superCreditsOf(cost apiCost) int {
	if strings.EqualFold(strings.TrimSpace(cost.Currency), "super_credits") {
		return cost.Amount.Int()
	}
	return 0
}

// weaponItemOf 把一件武器（主武器 / 副武器 / 手雷）转成目录条目。
// 第二个返回值是 false 表示这件装备不在目录范围内（例如 civilian 类）。
func weaponItemOf(weapon apiWeapon, zh *zhResolver, base string) (outItem, bool) {
	var kind Kind
	switch strings.ToLower(strings.TrimSpace(weapon.Category)) {
	case "primary":
		kind = KindPrimary
	case "secondary":
		kind = KindSecondary
	case "throwable":
		kind = KindGrenade
	default:
		return outItem{}, false
	}
	nameEn := strings.TrimSpace(weapon.Name)
	id := strings.TrimSpace(weapon.ID)
	if id == "" || nameEn == "" {
		return outItem{}, false
	}
	item := outItem{
		ID:          id,
		ProductKind: string(kind),
		Model:       modelOf(nameEn),
		NameZh:      zh.Item(id, nameEn),
		NameEn:      nameEn,
		WeaponType:  strings.TrimSpace(weapon.StatsRaw["Weapon Type"]),
		Image:       outImageOf(weapon.Image, nameEn, base),
		Wiki:        outWiki{URL: weapon.Wiki.URL},
		Acquisition: acquisitionOf(weapon.Source, 0),
		Combat:      combatOf(weapon.Attacks),
	}
	if weapon.Firearm != nil {
		item.Handling = handlingOf(weapon.Firearm)
	} else if weapon.Throwable != nil && weapon.Throwable.Capacity.Set() {
		item.Handling = &outHandling{Magazine: weapon.Throwable.Capacity.Int()}
	}
	return item, true
}

// stratagemItemOf 把一件战备转成目录条目；支援武器与其它战备分成两个类别，与老目录一致。
func stratagemItemOf(stratagem apiStratagem, zh *zhResolver, base string) (outItem, bool) {
	availability := strings.ToLower(strings.TrimSpace(stratagem.Availability))
	switch availability {
	case "loadout", "unavailable":
		// loadout 是能进配装的战备；unavailable 是已经拿不到的（老目录里也保留，只是标成已下架）。
	default:
		// mission / objective 是任务流程里的按钮（增援、补给、上传数据…），不是配装战备，目录不收。
		return outItem{}, false
	}
	id := strings.TrimSpace(stratagem.ID)
	nameEn := strings.TrimSpace(stratagem.Name)
	if id == "" || nameEn == "" {
		return outItem{}, false
	}
	// 支援武器在图鉴里归入「支援武器」，但其近战/其它战备归入「战备」。
	kind := KindStratagem
	if strings.EqualFold(strings.TrimSpace(stratagem.Kind), "support_weapon") {
		kind = KindSupport
	}
	item := outItem{
		ID:          id,
		ProductKind: string(kind),
		Model:       modelOf(nameEn),
		NameZh:      zh.Item(id, nameEn),
		NameEn:      nameEn,
		WeaponType:  strings.TrimSpace(stratagem.StatsRaw["Weapon Type"]),
		Image:       outImageOf(stratagem.Image, nameEn, base),
		Wiki:        outWiki{URL: stratagem.Wiki.URL},
		Acquisition: acquisitionOf(stratagem.Source, stratagem.UnlockLevel.Int()),
		Deployment: &outDeployment{
			Type:            deployTypeOf(stratagem.Kind),
			Code:            summonCodeOf(stratagem.Code),
			CooldownSeconds: stratagem.CooldownS.Int(),
			CallInSeconds:   round1(stratagem.CallInTimeS.Float()),
		},
		Combat: combatOf(stratagem.Attacks),
	}
	if kind == KindSupport && stratagem.SupportWeapon != nil {
		item.Handling = handlingOf(stratagem.SupportWeapon)
	}
	return item, true
}

// armorItemOf 把一件护甲转成目录条目。
func armorItemOf(armor apiArmor, zh *zhResolver, passiveNames map[string]string, base string) outItem {
	id := strings.TrimSpace(armor.ID)
	nameEn := strings.TrimSpace(armor.Name)
	return outItem{
		ID:          id,
		ProductKind: string(KindArmor),
		Model:       modelOf(nameEn),
		NameZh:      zh.Item(id, nameEn),
		NameEn:      nameEn,
		Image:       outImageOf(armor.Image, nameEn, base),
		Wiki:        outWiki{URL: armor.Wiki.URL},
		Acquisition: acquisitionOf(armor.Source, 0),
		Armor: &outArmor{
			Class:        armorClassOf(armor.Weight),
			Rating:       armor.ArmorRating.Int(),
			Speed:        armor.Speed.Int(),
			StaminaRegen: armor.StaminaRegen.Int(),
			Passive:      passiveNames[strings.TrimSpace(armor.PassiveID)],
		},
	}
}

// itemRecord 是排序用的中间结构：债券装备按债券顺序 → 页码 → 勋章价，其余按类别 → 英文名。
type itemRecord struct {
	item    outItem
	wbOrder int
	page    int
	cost    int
	kindSeq int
}

func itemRecordLess(a, b itemRecord) bool {
	leftWarbond, rightWarbond := a.wbOrder >= 0, b.wbOrder >= 0
	switch {
	case leftWarbond != rightWarbond:
		return leftWarbond
	case leftWarbond && a.wbOrder != b.wbOrder:
		return a.wbOrder < b.wbOrder
	case a.page != b.page:
		return a.page < b.page
	case a.cost != b.cost:
		return a.cost < b.cost
	case a.kindSeq != b.kindSeq:
		return a.kindSeq < b.kindSeq
	default:
		return a.item.NameEn < b.item.NameEn
	}
}

// kindSequence 是默认列表里的类别顺序（与 /gun 一次查三类时的顺序一致）。
func kindSequence(productKind string) int {
	switch Kind(productKind) {
	case KindPrimary:
		return 0
	case KindSecondary:
		return 1
	case KindSupport:
		return 2
	case KindGrenade:
		return 3
	case KindArmor:
		return 4
	default:
		return 5
	}
}

// outImageOf 组图片信息。图片地址会补成绝对地址（上游给的是 /images/… 这种站内路径），
// filePage 指向 wiki.gg 的原文件页——行内查询的缩略图要走 wiki 的 PNG
// （Telegram 对 webp 直链没把握），这条链路见 Store.ThumbURL。
func outImageOf(image apiImage, alt, base string) outImage {
	path := strings.TrimSpace(image.URL)
	if path == "" {
		return outImage{}
	}
	if !strings.HasPrefix(path, "http://") && !strings.HasPrefix(path, "https://") {
		path = strings.TrimSuffix(base, "/") + "/" + strings.TrimPrefix(path, "/")
	}
	out := outImage{Path: path, Alt: alt, License: imageLicense}
	if file := strings.TrimSpace(image.WikiFile); file != "" {
		out.FilePage = "https://helldivers.wiki.gg/wiki/File:" + url.PathEscape(strings.ReplaceAll(file, " ", "_"))
	}
	return out
}

// acquisitionOf 把上游的获取方式翻成目录里的取值。
//
// 上游取值比老目录多两个：pre_order（预购奖励，归入版本奖励）与 other（得看 label：
// 「Minor Places of Interest」是地图拾取，其余是不再可得的战备）。
func acquisitionOf(origin apiOrigin, unlockLevel int) outAcq {
	kind := strings.ToLower(strings.TrimSpace(origin.Type))
	switch kind {
	case "pre_order":
		kind = "edition"
	case "other":
		if strings.Contains(strings.ToLower(origin.Label), "interest") {
			kind = "poi"
		} else {
			kind = "unavailable"
		}
	case "":
		kind = "unavailable"
	}
	acq := outAcq{Kind: kind}
	switch kind {
	case "warbond":
		acq.WarbondID = strings.TrimSpace(origin.WarbondID)
		if origin.Page.Set() {
			acq.Page = origin.Page.Int()
		}
		if strings.EqualFold(strings.TrimSpace(origin.Cost.Currency), "medals") {
			acq.ItemMedals = origin.Cost.Amount.Int()
		}
	case "requisition":
		if strings.EqualFold(strings.TrimSpace(origin.Cost.Currency), "requisition") {
			acq.RequisitionPoints = origin.Cost.Amount.Int()
		}
		acq.LevelRequired = unlockLevel
	}
	return acq
}

// handlingOf 把枪械数值转成操作性数值；射速取第一档（上游给的是「按升级/档位」的数组）。
func handlingOf(firearm *apiFirearm) *outHandling {
	if firearm == nil {
		return nil
	}
	handling := &outHandling{
		Magazine:       firearm.Capacity.Int(),
		SpareMagazines: firearm.SpareMagazines.Int(),
		Recoil:         round2(firearm.Recoil.Float()),
		FiringModes:    firingModesOf(firearm.FiringModes),
	}
	if len(firearm.FireRateRPM) > 0 {
		handling.FireRate = int(math.Round(firearm.FireRateRPM[0]))
	}
	if handling.Magazine == 0 && handling.SpareMagazines == 0 && handling.FireRate == 0 &&
		handling.Recoil == 0 && len(handling.FiringModes) == 0 {
		return nil
	}
	return handling
}

// firingModesOf 归一化射击模式：首字母大写交给卡片层翻译，去掉上游的占位值 "other"。
func firingModesOf(modes []string) []string {
	out := make([]string, 0, len(modes))
	seen := make(map[string]bool, len(modes))
	for _, mode := range modes {
		cleaned := strings.TrimSpace(mode)
		if cleaned == "" || strings.EqualFold(cleaned, "other") || strings.EqualFold(cleaned, "unknown") {
			continue
		}
		cleaned = strings.ToUpper(cleaned[:1]) + strings.ToLower(cleaned[1:])
		key := strings.ToLower(cleaned)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, cleaned)
	}
	return out
}

// combatOf 把攻击数组转成伤害部件。
func combatOf(attacks []apiAttack) *outCombat {
	components := make([]outComponent, 0, len(attacks))
	for _, attack := range attacks {
		fields := outComponentFields{
			StandardDamage:  attack.Damage.Standard.Int(),
			DurableDamage:   attack.Damage.Durable.Int(),
			DemolitionForce: attack.Forces.Demolition.Int(),
			Stagger:         attack.Forces.Stagger.Int(),
			Push:            attack.Forces.Push.Int(),
		}
		if pen, ok := armorPenetrationOf(attack.Penetration.Direct); ok {
			fields.ArmorPenetration.Value = pen.value
			fields.ArmorPenetration.LabelZh = pen.label
		}
		components = append(components, outComponent{
			Label:  componentLabelOf(attack.Kind, attack.Damage.Type),
			Fields: fields,
		})
	}
	if len(components) == 0 {
		return nil
	}
	return &outCombat{Components: components, PrimaryComponentID: ""}
}

// armorPenetrationValue 是穿甲等级：数值沿用老目录的取值（与游戏内一致），标签用简中。
type armorPenetrationValue struct {
	value int
	label string
}

// armorPenetrations 是上游枚举 → 穿甲等级。老目录里 2/3/4 分别是轻/中/重甲，
// 反坦克 I 从 5 起，这里保持同一把尺子。
var armorPenetrations = map[string]armorPenetrationValue{
	"unarmored":     {0, "无装甲"},
	"very_light":    {1, "极轻装甲"},
	"light":         {2, "轻型"},
	"medium":        {3, "中型"},
	"heavy":         {4, "重型"},
	"anti_tank_i":   {5, "反坦克 I"},
	"anti_tank_ii":  {6, "反坦克 II"},
	"anti_tank_iii": {7, "反坦克 III"},
	"anti_tank_iv":  {8, "反坦克 IV"},
	"anti_tank_v":   {9, "反坦克 V"},
	"anti_tank_vi":  {10, "反坦克 VI"},
}

func armorPenetrationOf(raw string) (armorPenetrationValue, bool) {
	pen, ok := armorPenetrations[strings.ToLower(strings.TrimSpace(raw))]
	return pen, ok
}

// componentKindLabels 是攻击类型 → 部件标签：上游只在伤害类型是通用值（Damage/空）时才需要它。
var componentKindLabels = map[string]string{
	"projectile": "projectile",
	"explosion":  "Explosion",
	"spray":      "spray",
	"beam":       "Beam",
	"arc":        "Arc",
	"melee":      "Melee",
	"status":     "Status",
	"other":      "Other",
}

// componentLabelOf 取部件标签：优先用伤害类型（Ballistic / Explosion / Fire / Gas…），
// 通用值 Damage 与空值退回攻击类型（projectile / spray / Beam…）；卡片层会把两者都翻成中文。
func componentLabelOf(kind, damageType string) string {
	cleaned := strings.TrimSpace(damageType)
	if cleaned != "" && !strings.EqualFold(cleaned, "damage") && !strings.EqualFold(cleaned, "none") {
		return cleaned
	}
	if label, ok := componentKindLabels[strings.ToLower(strings.TrimSpace(kind))]; ok {
		return label
	}
	return "Other"
}

// deployTypes 是上游战备类型 → 老目录的部署类型（卡片层再翻成中文）。
var deployTypes = map[string]string{
	"support_weapon": "Support Weapon",
	"backpack":       "Backpack",
	"orbital":        "Orbital",
	"sentry":         "Sentry",
	"emplacement":    "Emplacement",
	"vehicle":        "Vehicle",
	"eagle":          "Eagle",
	"other":          "Other",
}

func deployTypeOf(kind string) string {
	if label, ok := deployTypes[strings.ToLower(strings.TrimSpace(kind))]; ok {
		return label
	}
	return "Other"
}

// armorClasses 是上游重量 → 老目录的护甲等级。
var armorClasses = map[string]string{
	"light":  "Light",
	"medium": "Medium",
	"heavy":  "Heavy",
}

func armorClassOf(weight string) string {
	if class, ok := armorClasses[strings.ToLower(strings.TrimSpace(weight))]; ok {
		return class
	}
	return "Unknown"
}

// summonCodeOf 归一化召唤指令：上游给的是 down/up/left/right，与老目录一致；
// 认不出的值原样留着，卡片层会当文本画出来，而不是悄悄丢一段指令。
func summonCodeOf(code []string) []string {
	out := make([]string, 0, len(code))
	for _, step := range code {
		if cleaned := strings.ToLower(strings.TrimSpace(step)); cleaned != "" {
			out = append(out, cleaned)
		}
	}
	return out
}

// modelOf 从英文名里取出型号：「AR-11 Arbitrator」→「AR-11」、「A/AC-8 Autocannon Sentry」→「A/AC-8」。
// 规则：从第一个词开始，只收「同时含字母与数字、且只有字母数字与 / - . 」的词，遇到第一个不符合的就停。
// 这样「Eagle Airstrike」「100 Super Credits」这类没有型号的名字不会硬凑出一个型号来。
func modelOf(name string) string {
	fields := strings.Fields(name)
	model := make([]string, 0, 2)
	for _, field := range fields {
		if !isModelToken(field) {
			break
		}
		model = append(model, field)
	}
	return strings.Join(model, " ")
}

func isModelToken(token string) bool {
	if token == "" {
		return false
	}
	hasDigit, hasLetter := false, false
	for _, r := range token {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z'):
			hasLetter = true
		case r == '/', r == '-', r == '.':
		default:
			return false
		}
	}
	return hasDigit && hasLetter
}

// round1 / round2 把上游的小数位数收一收，避免卡片上出现 7.750000000000001 这种数。
func round1(v float64) float64 { return math.Round(v*10) / 10 }

func round2(v float64) float64 { return math.Round(v*100) / 100 }
