package wikigg

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestClient 起一个假 wiki 站点，把请求交给 handler，并返回指向它的客户端。
// 同时记录每次请求的 User-Agent：站点对空 UA 直接 403，这条约束必须有用例盯着。
func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	var mu sync.Mutex
	seenUA := make([]string, 0, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenUA = append(seenUA, r.Header.Get("User-Agent"))
		mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	client := New(Config{BaseURL: server.URL}, Deps{HTTP: server.Client()})
	return client, server
}

// TestFileTitleVariants 校验四种文件页写法都能归一化成 MediaWiki 标题，空地址报错。
func TestFileTitleVariants(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"完整地址", "https://helldivers.wiki.gg/wiki/File:AR-2_Coyote_Primary_Render.png", "File:AR-2 Coyote Primary Render.png"},
		{"带版本参数", "https://helldivers.wiki.gg/wiki/File:Terminid_Icon.svg?c5bd41", "File:Terminid Icon.svg"},
		{"已归一化的标题", "File:Hulk.png", "File:Hulk.png"},
		{"裸文件名", "Autocannon_Sentry.svg", "File:Autocannon Sentry.svg"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := FileTitle(c.in)
			if err != nil {
				t.Fatalf("解析 %q 失败：%v", c.in, err)
			}
			if got != c.want {
				t.Errorf("标题应为 %q，实际 %q", c.want, got)
			}
		})
	}
	if _, err := FileTitle("   "); err == nil {
		t.Error("空地址应报错")
	}
}

// TestUsableImageURLRejectsSVG 校验 SVG 被挡掉：站点没有开 SVG 渲染，SVG 地址交给 Telegram 只会失败。
func TestUsableImageURLRejectsSVG(t *testing.T) {
	cases := map[string]string{
		"https://helldivers.wiki.gg/images/Terminid_Icon.svg?c5bd41": "",
		"https://helldivers.wiki.gg/images/hulk.png?371e2b":          "https://helldivers.wiki.gg/images/hulk.png?371e2b",
		"": "",
	}
	for in, want := range cases {
		if got := usableImageURL(in); got != want {
			t.Errorf("usableImageURL(%q) 应为 %q，实际 %q", in, want, got)
		}
	}
}

// TestResolveTitleFollowsRedirectChain 校验重定向链能被跟到终点，且不会因为自环打转。
func TestResolveTitleFollowsRedirectChain(t *testing.T) {
	aliases := map[string]string{"A": "B", "B": "C"}
	if got := resolveTitle("A", aliases); got != "C" {
		t.Errorf("应跟到链尾 C，实际 %q", got)
	}
	if got := resolveTitle("C", aliases); got != "C" {
		t.Errorf("没有重定向时应返回原标题，实际 %q", got)
	}
	if got := resolveTitle("X", map[string]string{"X": "X"}); got != "X" {
		t.Errorf("自环应停下并返回原标题，实际 %q", got)
	}
}

// TestFileThumbURLReturnsThumbURL 校验请求参数与返回值：标题归一化、宽度参数带上、优先用 thumburl。
func TestFileThumbURLReturnsThumbURL(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("action") != "query" || q.Get("prop") != "imageinfo" {
			t.Errorf("请求参数不对：%v", q)
		}
		if got := q.Get("titles"); got != "File:AR-2 Coyote Primary Render.png" {
			t.Errorf("标题应为归一化后的 File: 标题，实际 %q", got)
		}
		if got := q.Get("iiurlwidth"); got != "240" {
			t.Errorf("宽度参数应为 240，实际 %q", got)
		}
		if ua := r.Header.Get("User-Agent"); ua == "" {
			t.Error("请求必须带 User-Agent（站点对空 UA 返回 403）")
		}
		fmt.Fprint(w, `{"query":{"pages":{"1":{"title":"File:X.png","imageinfo":[{"thumburl":"https://w/images/thumb/X.png/240px-X.png","url":"https://w/images/X.png"}]}}}}`)
	})

	got, err := client.FileThumbURL(context.Background(),
		"https://helldivers.wiki.gg/wiki/File:AR-2_Coyote_Primary_Render.png", 240)
	if err != nil {
		t.Fatalf("解析缩略图失败：%v", err)
	}
	if got != "https://w/images/thumb/X.png/240px-X.png" {
		t.Errorf("应返回 thumburl，实际 %q", got)
	}
}

// TestFileThumbURLFallsBackToOriginalURL 校验站点没生成缩略图时退回原图地址
// （原图本身小于请求宽度时 MediaWiki 就不给 thumburl）。
func TestFileThumbURLFallsBackToOriginalURL(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"query":{"pages":{"1":{"title":"File:X.png","imageinfo":[{"url":"https://w/images/X.png"}]}}}}`)
	})
	got, err := client.FileThumbURL(context.Background(), "File:X.png", 0)
	if err != nil {
		t.Fatalf("解析缩略图失败：%v", err)
	}
	if got != "https://w/images/X.png" {
		t.Errorf("没有 thumburl 时应退回原图地址，实际 %q", got)
	}
}

// TestFileThumbURLErrors 校验文件页不存在、接口报错、响应坏掉三种情况都返回中文错误。
func TestFileThumbURLErrors(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		want    string
	}{
		{"文件页不存在", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"query":{"pages":{"1":{"title":"File:Nope.png","missing":""}}}}`)
		}, "wiki 上没有这个文件页"},
		{"接口报错", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"error":{"code":"badvalue","info":"参数不对"}}`)
		}, "badvalue：参数不对"},
		{"响应坏掉", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"query":`)
		}, "解析 wiki 响应失败"},
		{"HTTP 500", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}, "wiki 返回 HTTP 500"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			client, _ := newTestClient(t, c.handler)
			_, err := client.FileThumbURL(context.Background(), "File:X.png", 240)
			if err == nil {
				t.Fatal("应返回错误")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("错误里应包含 %q，实际 %v", c.want, err)
			}
		})
	}
}

// TestEnemiesParsesRows 校验 Cargo 响应被解析成行：字符串字段原样、数字字段转成字符串。
func TestEnemiesParsesRows(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("action") != "cargoquery" || q.Get("tables") != "Enemies" {
			t.Errorf("请求参数不对：%v", q)
		}
		if q.Get("fields") != "title,faction,size,health,class,damage,image" {
			t.Errorf("字段清单不对：%q", q.Get("fields"))
		}
		fmt.Fprint(w, `{"cargoquery":[
			{"title":{"title":"Bile Titan","faction":"Terminids","size":3,"health":"6,500","class":"","damage":"<span>x</span>","image":"Bile Titan Enemy Icon.png"}},
			{"title":{"title":"","faction":"Illuminate","size":"","health":"","class":"","damage":"","image":""}}
		]}`)
	})

	rows, err := client.Enemies(context.Background())
	if err != nil {
		t.Fatalf("取敌人表失败：%v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("应返回 2 行，实际 %d 行", len(rows))
	}
	if rows[0].Title != "Bile Titan" || rows[0].Faction != "Terminids" || rows[0].Size != "3" {
		t.Errorf("第一行解析不对：%+v", rows[0])
	}
	if rows[0].Health != "6,500" || rows[0].Damage != "<span>x</span>" {
		t.Errorf("字段应保持原文（清洗交给 bestiary）：%+v", rows[0])
	}
	if rows[0].Image != "Bile Titan Enemy Icon.png" {
		t.Errorf("图片文件名应原样保留（它比 pageimages 靠谱，见 EnemyRow.Image 的说明）：%+v", rows[0])
	}
	if rows[1].Title != "" || rows[1].Image != "" {
		t.Errorf("空标题与空图片名应原样保留（由 bestiary 过滤）：%+v", rows[1])
	}
}

// TestEnemiesReportsAPIError 校验 Cargo 报错时把 code 与 info 一起带进错误里。
func TestEnemiesReportsAPIError(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"error":{"code":"badvalue","info":"未知的表"}}`)
	})
	_, err := client.Enemies(context.Background())
	if err == nil || !strings.Contains(err.Error(), "未知的表") {
		t.Fatalf("应把站点给的错误说明带进错误里，实际 %v", err)
	}
}

// TestEnemyIconsBatchesAndMapsTitles 校验图标查询分批发出（MediaWiki 一次最多 50 个标题）、
// 能把重定向与归一化后的标题映射回入参标题，并挡掉 SVG。
func TestEnemyIconsBatchesAndMapsTitles(t *testing.T) {
	var mu sync.Mutex
	batches := 0
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		titles := strings.Split(r.URL.Query().Get("titles"), "|")
		mu.Lock()
		batches++
		mu.Unlock()
		if len(titles) > titleBatchSize {
			t.Errorf("单次请求的标题数不能超过 %d，实际 %d", titleBatchSize, len(titles))
		}
		pages := make([]string, 0, len(titles))
		for i, title := range titles {
			switch title {
			case "Scavenger":
				// 站点只给了 SVG（阵营通用图标）：Telegram 用不了，应被挡掉。
				pages = append(pages, fmt.Sprintf(`"%d":{"title":"Scavenger","original":{"source":"https://w/images/Terminid_Icon.svg?c5bd41"}}`, i))
			case "Hunter":
				// 原图比请求宽度还小：没有 thumbnail，退回 original。
				pages = append(pages, fmt.Sprintf(`"%d":{"title":"Hunter","original":{"source":"https://w/images/Armor_AV0_Icon.png?ba82a8"}}`, i))
			case "Squadleader Soldier":
				pages = append(pages, fmt.Sprintf(`"%d":{"title":"Helldivers 1:Squadleader Soldier","thumbnail":{"source":"https://w/images/thumb/S.png/480px-S.png"}}`, i))
			default:
				pages = append(pages, fmt.Sprintf(`"%d":{"title":%q,"thumbnail":{"source":"https://w/images/thumb/%s.png/480px-%s.png"}}`, i, title, title, title))
			}
		}
		body := `{"query":{"normalized":[{"from":"Hunter","to":"Hunter"}],"redirects":[{"from":"Squadleader Soldier","to":"Helldivers 1:Squadleader Soldier"}],"pages":{` + strings.Join(pages, ",") + `}}}`
		fmt.Fprint(w, body)
	})

	titles := make([]string, 0, 52)
	titles = append(titles, "Hunter", "Scavenger", "Squadleader Soldier")
	for i := 0; len(titles) < 52; i++ {
		titles = append(titles, fmt.Sprintf("Enemy %02d", i))
	}
	icons, err := client.EnemyIcons(context.Background(), titles, 480)
	if err != nil {
		t.Fatalf("取图标失败：%v", err)
	}
	if batches != 2 {
		t.Errorf("52 个标题应分 2 次请求，实际 %d 次", batches)
	}
	if got := icons["Hunter"]; got != "https://w/images/Armor_AV0_Icon.png?ba82a8" {
		t.Errorf("没有缩略图时应退回原图，实际 %q", got)
	}
	if got := icons["Squadleader Soldier"]; got != "https://w/images/thumb/S.png/480px-S.png" {
		t.Errorf("重定向后的页面图标应映射回入参标题，实际 %q", got)
	}
	if _, ok := icons["Scavenger"]; ok {
		t.Error("SVG 图标应被挡掉（Telegram 不接受 SVG）")
	}
	if got := icons["Enemy 00"]; got != "https://w/images/thumb/Enemy 00.png/480px-Enemy 00.png" {
		t.Errorf("普通条目应拿到缩略图，实际 %q", got)
	}
}

// TestEnemyIconsSkipsEmptyTitles 校验空标题与重复标题不会发出去（避免拼出 "||" 这种坏参数）。
func TestEnemyIconsSkipsEmptyTitles(t *testing.T) {
	var mu sync.Mutex
	requested := ""
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requested = r.URL.Query().Get("titles")
		mu.Unlock()
		fmt.Fprint(w, `{"query":{"pages":{"1":{"title":"Hulk","thumbnail":{"source":"https://w/images/thumb/H.png/480px-H.png"}}}}}`)
	})
	if _, err := client.EnemyIcons(context.Background(), []string{"", "  ", "Hulk", "Hulk"}, 0); err != nil {
		t.Fatalf("取图标失败：%v", err)
	}
	if requested != "Hulk" {
		t.Errorf("空标题与重复标题都不该发出去，实际 %q", requested)
	}
}

// TestNewUsesDefaults 校验零值配置下站点地址与超时都取默认值（BaseURL 可断言、超时只断言非零）。
func TestNewUsesDefaults(t *testing.T) {
	client := New(Config{}, Deps{})
	if client.BaseURL() != defaultBaseURL {
		t.Errorf("默认站点地址应为 %s，实际 %s", defaultBaseURL, client.BaseURL())
	}
	// 末尾斜杠要去掉：带斜杠会拼出 //api.php。
	trailing := New(Config{BaseURL: "https://example.test/"}, Deps{})
	if trailing.BaseURL() != "https://example.test" {
		t.Errorf("站点地址末尾斜杠应被去掉，实际 %q", trailing.BaseURL())
	}
	if New(Config{}, Deps{}).http.Timeout != defaultTimeout {
		t.Errorf("默认超时应为 %s", defaultTimeout)
	}
	if New(Config{Timeout: time.Second}, Deps{}).http.Timeout != time.Second {
		t.Error("显式超时应生效")
	}
}

// TestFileThumbURLs 校验批量解析文件缩略图：键是入参原样、SVG 挡掉、下划线写法能被站点归一化映射回来。
func TestFileThumbURLs(t *testing.T) {
	var gotTitles []string
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("action") != "query" || q.Get("prop") != "imageinfo" {
			t.Errorf("请求参数不对：%v", q)
		}
		if q.Get("iiurlwidth") != "480" {
			t.Errorf("应带上请求宽度：%q", q.Get("iiurlwidth"))
		}
		gotTitles = strings.Split(q.Get("titles"), "|")
		fmt.Fprint(w, `{"query":{"normalized":[{"from":"File:Bile_Titan_Enemy_Icon.png","to":"File:Bile Titan Enemy Icon.png"}],
		  "pages":{
		    "1":{"title":"File:Bile Titan Enemy Icon.png","imageinfo":[{"thumburl":"https://w/images/thumb/bile.png","url":"https://w/images/bile.png"}]},
		    "2":{"title":"File:Terminid Icon.svg","imageinfo":[{"thumburl":"https://w/images/terminid.svg","url":"https://w/images/terminid.svg"}]},
		    "3":{"title":"File:Missing.png","missing":""}
		  }}}`)
	})

	urls, err := client.FileThumbURLs(context.Background(), []string{
		"Bile Titan Enemy Icon.png",
		"File:Bile_Titan_Enemy_Icon.png",
		"Terminid Icon.svg",
		"Missing.png",
		"   ",
	}, 480)
	if err != nil {
		t.Fatalf("批量取缩略图失败：%v", err)
	}
	// 四种写法归一化后剩三个标题（下划线会换成空格，因此前两个是同一条目），空白入参直接跳过。
	if len(gotTitles) != 3 {
		t.Errorf("应把 3 个去重后的标题一次问出去，实际 %v", gotTitles)
	}
	if urls["Bile Titan Enemy Icon.png"] != "https://w/images/thumb/bile.png" {
		t.Errorf("裸文件名应能查到缩略图：%v", urls)
	}
	if urls["File:Bile_Titan_Enemy_Icon.png"] != "https://w/images/thumb/bile.png" {
		t.Errorf("同一个文件的另一种写法也应拿到同一张图：%v", urls)
	}
	if _, ok := urls["Terminid Icon.svg"]; ok {
		t.Errorf("SVG 不应出现在结果里（Telegram 不接受）：%v", urls)
	}
	if _, ok := urls["Missing.png"]; ok {
		t.Errorf("站点上没有的文件不应出现在结果里：%v", urls)
	}
}

// TestFileThumbURLsErrors 校验批量查询的失败路径：站点报错与响应坏掉都返回中文错误。
func TestFileThumbURLsErrors(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		want    string
	}{
		{"接口报错", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"error":{"code":"badvalue","info":"参数不对"}}`)
		}, "badvalue：参数不对"},
		{"响应坏掉", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"query":`)
		}, "解析 wiki 响应失败"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			client, _ := newTestClient(t, c.handler)
			if _, err := client.FileThumbURLs(context.Background(), []string{"X.png"}, 240); err == nil {
				t.Fatal("应返回错误")
			} else if !strings.Contains(err.Error(), c.want) {
				t.Errorf("错误里应包含 %q，实际 %v", c.want, err)
			}
		})
	}
}

// TestFileThumbURLsWithoutUsableInput 校验没有任何可解析的入参时不发请求、回空表。
func TestFileThumbURLsWithoutUsableInput(t *testing.T) {
	var calls int
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		t.Error("没有可解析的文件名时不该发请求")
	})
	urls, err := client.FileThumbURLs(context.Background(), []string{"", "   "}, 480)
	if err != nil {
		t.Fatalf("不该报错：%v", err)
	}
	if len(urls) != 0 {
		t.Errorf("应回空表，实际 %v", urls)
	}
	if calls != 0 {
		t.Errorf("不该发请求，实际 %d 次", calls)
	}
}
