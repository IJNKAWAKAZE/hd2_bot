// hd2_bot 是《绝地潜兵 2》Telegram 情报机器人：
// 启动顺序为：读配置 → 状态存储 → 数据层（限流/客户端/补充源/聚合）→ 渲染引擎 → Telegram 机器人 →
// 插件命令与行内查询 → 定时任务 → 本地 HTTP 服务，最后阻塞在长轮询上，等待退出信号。
// 信息类命令默认渲染成图片卡片（见 src/render），渲染失败或关闭渲染时回退 MarkdownV2 纯文本。
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	tgbotapi "github.com/ijnkawakaze/telegram-bot-api"

	"hd2_bot/src/arsenal"
	"hd2_bot/src/bestiary"
	"hd2_bot/src/config"
	"hd2_bot/src/core/bot"
	"hd2_bot/src/core/cron"
	"hd2_bot/src/core/web"
	"hd2_bot/src/hd2"
	"hd2_bot/src/plugins/codex"
	"hd2_bot/src/plugins/orders"
	"hd2_bot/src/plugins/planets"
	"hd2_bot/src/plugins/station"
	"hd2_bot/src/plugins/system"
	"hd2_bot/src/plugins/war"
	"hd2_bot/src/push"
	"hd2_bot/src/render"
	"hd2_bot/src/state"
	"hd2_bot/src/translate"
	"hd2_bot/src/wikigg"
)

// statePath 是 bbolt 状态文件的路径（相对进程工作目录）：保存数据快照、
// 推送去重记录与元数据。配置结构里没有这一项（Task 2 的 Config 只含 bot/api/cache/limit/http），
// 因此固定在入口声明，目录由 state.Open 自动创建。
const statePath = "data/hd2.db"

// shutdownTimeout 是停机时等待 HTTP 在途请求结束的最长时间。
const shutdownTimeout = 5 * time.Second

// testZoneOffset 是加载不到 Asia/Shanghai 时使用的固定东八区偏移（秒）。
const testZoneOffset = 8 * 3600

// pushTimeout 是一轮推送的总预算（取数 + 翻译 + 出图 + 发送）。
//
// 为什么必须在这里自控超时：本仓库用的 robfig cron 每个 tick 新开一个 goroutine，
// 同一任务的多次触发**会自我重叠**（没有任何 chain 包装替我们串行化同一任务），
// 所以「上一轮还没跑完」时下一轮照样会开始，堆积起来只会越来越慢。
// 60 秒明显小于默认的 5 分钟轮询间隔，而正常一轮（含出图）在 10 秒内就能跑完。
const pushTimeout = 60 * time.Second

// pushIntervalCeiling 是「缓存 TTL 是否过长」的判定阈值：默认轮询是每 5 分钟一轮，
// TTL 不小于这个值时，中间的变化会被缓存吃掉，推送就会漏掉它们。
const pushIntervalCeiling = 5 * time.Minute

// ready 标记服务是否可以对外服务：全部组件启动成功后置真，停机一开始置假。
// /healthz 依赖它区分「进程活着」与「服务可用」。
var ready atomic.Bool

func main() {
	log.SetFlags(log.Lshortfile | log.Ldate | log.Ltime)
	os.Exit(run())
}

// run 组装并启动所有组件，返回进程退出码：启动失败返回 1，正常停机返回 0。
// 组装顺序即依赖顺序，任何一步失败都会释放已拿到的资源（尤其是状态文件锁）再退出。
func run() int {
	cfg, err := config.Load("")
	if err != nil {
		log.Printf("启动失败：%v", err)
		return 1
	}
	// bot.name 与 bot.debug 只用于启动日志：S1 没有需要区分的运行模式。
	log.Printf("配置加载完成：bot=%s owner=%d group=%d debug=%v http=%s:%d db=%s",
		cfg.Bot.Name, cfg.Bot.Owner, cfg.Bot.GroupID, cfg.Bot.Debug, cfg.HTTP.Host, cfg.HTTP.Port, statePath)
	if cfg.Render.Enabled {
		log.Print("检查 Playwright 驱动和 Chromium，缺失时自动安装，首次启动可能需要等待下载")
		if err := render.PrepareRuntime(); err != nil {
			log.Printf("图片运行环境准备失败：%v；继续启动，渲染失败时回退文本", err)
		} else {
			log.Print("Playwright 驱动和 Chromium 已就绪")
		}
	}

	// 状态存储：Service 用它保存快照做降级兜底，S4 的推送去重也会用它。
	store, err := state.Open(statePath)
	if err != nil {
		log.Printf("启动失败：%v", err)
		return 1
	}

	// 数据层：主源与补充源各用一个限流器（补充源不占上游额度），所有请求参数取自配置。
	limiter := hd2.NewLimiter(cfg.Limit.Rate, cfg.Limit.Window.Duration(), cfg.Limit.Cooldown.Duration())
	client := hd2.NewClient(buildClientConfig(cfg), limiter)
	companionLimiter := hd2.NewLimiter(cfg.Limit.Rate, cfg.Limit.Window.Duration(), cfg.Limit.Cooldown.Duration())
	companion := hd2.NewCompanion(cfg.API.BaseCompanion, cfg.API.UserAgent,
		time.Duration(cfg.API.Timeout)*time.Second, companionLimiter, log.Printf)

	svc := hd2.NewService(hd2.ServiceConfig{
		Client:    client,
		Companion: companion,
		Store:     store,
		TTL:       buildTTLConfig(cfg),
		Logger:    log.Printf,
	})

	// 渲染引擎：render.enabled 为 false 时拿到的是「必定渲染失败」的实现，
	// 卡片命令因此统一回退纯文本，插件不用各自去判断配置。
	renderer, err := buildRenderer(cfg)
	if err != nil {
		log.Printf("启动失败：%v", err)
		closeState(store)
		return 1
	}

	// Telegram 机器人：只服务配置里指定的群；仅 /ping 清理指令与回复，其他消息保留。
	tgBot, err := bot.New(cfg.Bot.Token, cfg.Bot.Owner, cfg.Bot.GroupID,
		time.Duration(cfg.Bot.MsgDelDelay*float64(time.Second)))
	if err != nil {
		log.Printf("启动失败：%v", err)
		closeState(store)
		return 1
	}

	// 翻译层：关闭或没有可用后端时它返回「原样返回」的实现，构造本身不会失败（失败也只在日志里提示）。
	trans, err := newTranslator(cfg, store)
	if err != nil {
		log.Printf("启动失败：%v", err)
		closeState(store)
		return 1
	}
	// 提示性检查：TTL 过长会漏检变化，但用户把轮询调稀一点时同样的 TTL 就不算长，所以只提示、不阻止启动。
	warnIfCacheTTLTooLong(cfg, log.Printf)

	// 图鉴数据层：装备目录与装备图来自 GitHub（HD2Tool），敌人数值与图标来自 wiki.gg。
	// wiki 客户端同时作为装备目录的缩略图解析器注入——SVG 装备在行内结果里需要换成位图。
	wiki := buildWikiClient(cfg)
	gearStore := buildArsenalStore(cfg, wiki)
	beastStore := buildBestiaryStore(cfg, wiki)
	log.Printf("图鉴数据层就绪：装备缓存=%s 图鉴缓存=%s wiki=%s", gearStore.Dir(), beastStore.Dir(), wiki.BaseURL())

	// 插件命令与行内查询：命令用 /ping 这类消息能力，卡片命令还需要数据层、渲染引擎与展示时区。
	// displayLocation 只算一次，避免各插件拿到不同的时区对象。
	display := displayLocation()
	// 交给插件与推送的翻译层要经 pluginTranslator 过一道：关闭翻译时给 nil，
	// 而不是把「用户主动关掉」说成「翻译暂不可用」（理由见该函数的注释）。
	pluginTrans := pluginTranslator(cfg, trans)
	registerPlugins(tgBot, svc, renderer, display, pluginTrans, gearStore, beastStore)

	// 定时任务：心跳 + （可选）战况推送。推送轮询与心跳错开 15 秒，避免同一秒并发取数。
	jobs := buildPushJobs(cfg, svc, store, tgBot, renderer, pluginTrans, display, log.Printf)
	cronRunner := cron.New(cron.Options{Logger: log.Printf, Jobs: jobs})
	if err := cronRunner.Start(); err != nil {
		log.Printf("启动失败：%v", err)
		closeState(store)
		return 1
	}
	server, err := web.Start(buildWebOptions(cfg, &ready))
	if err != nil {
		log.Printf("启动失败：%v", err)
		cronRunner.Stop()
		closeState(store)
		return 1
	}

	// 所有组件都起来了，健康检查才开始返回 ok。
	ready.Store(true)

	// 优雅退出：Ctrl+C / SIGTERM 后按固定顺序释放资源，正常停机退出码为 0。
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("收到退出信号 %v，开始停机", sig)
		if err := shutdownAll(log.Printf, shutdownSteps(shutdownDeps{
			markNotReady: func() { ready.Store(false) },
			cronStop:     cronRunner.Stop,
			server:       server,
			renderer:     renderer,
			store:        store,
		})); err != nil {
			log.Printf("停机过程出现错误：%v", err)
		}
		log.Println("停机完成")
		os.Exit(0)
	}()

	log.Println("机器人启动完成")
	tgBot.Run() // 阻塞：长轮询直到进程退出
	return 0
}

// pluginHandlers 汇总全部插件指令：system 的 /ping 与 /help、war 的 /war、
// planets 的 /planets 与 /planet、orders 的 /assignments 与 /dispatches、
// station 的 /events 与 /stations、codex 的六条图鉴指令。
// 抽成独立函数是为了让接线用例能拿到「实际注册了哪些指令」，不必连真实的 Telegram 客户端；
// 帮助卡用的是 system 里同一份清单，所以这里漏接一条命令会被 main_test.go 抓到。
//
// trans 是正文与环境信息的翻译层，由装配阶段按 pluginTranslator 的口径注入：
// 各插件必须拿到同一个实例（共用缓存与限速），用户关掉翻译时必须是 nil。
// gear / beasts 是两个图鉴数据层入口（本机器人只有一份缓存，所有命令共用）。
func pluginHandlers(b bot.Sender, svc *hd2.Service, renderer render.Renderer, loc *time.Location,
	trans translate.Translator,
	gear codex.ArsenalService, beasts codex.BestiaryService) []bot.Handler {
	handlers := system.Handlers(b, renderer)
	handlers = append(handlers, war.Handlers(b, svc, renderer, loc)...)
	handlers = append(handlers, planets.Handlers(b, svc, renderer, loc, planets.InlinePrefix, trans)...)
	handlers = append(handlers, orders.Handlers(b, svc, renderer, loc, trans)...)
	handlers = append(handlers, station.Handlers(b, svc, renderer, loc)...)
	handlers = append(handlers, codex.Handlers(b, gear, beasts, renderer, loc)...)
	return handlers
}

// registerer 是「把命令与行内查询注册到机器人上」需要的最小能力：*bot.Bot 实现它，
// 装配用例用假实现记录实际注册了什么（真机器人构造要连 Telegram，进不了单测）。
type registerer interface {
	bot.Sender
	Register(handlers []bot.Handler)
	RegisterInline(prefix string, fn func(query tgbotapi.InlineQuery) error)
	AnswerInline(queryID string, results []interface{}) error
}

// registerPlugins 把命令与行内查询一起交给机器人：
//   - 命令走 pluginHandlers 的清单；
//   - 行内查询注册两组前缀：planets 的星球前缀（群里点 /planets 的按钮后 Telegram 按这个前缀
//     把查询交过来，选中结果回填成 /planet <名字>），以及 codex 的五个图鉴前缀
//     （选中结果回填成对应的图鉴指令）。
//
// 前缀为空时 bot.RegisterInline 会记一条警告并忽略注册（按钮也不会发出去），
// 此时那条行内搜索不可用，但不影响其它命令与其它前缀。
// 合成一个函数是为了让装配用例一次就能断言「命令齐 + 各前缀都注册了」：少了行内那半，
// 群里点按钮搜不出任何东西，而工厂函数各自单测都还是绿的。
// trans 的语义与 pluginHandlers 一致：各插件共享同一个翻译层实例。
func registerPlugins(tg registerer, svc *hd2.Service, renderer render.Renderer, loc *time.Location,
	trans translate.Translator,
	gear codex.ArsenalService, beasts codex.BestiaryService) {
	tg.Register(pluginHandlers(tg, svc, renderer, loc, trans, gear, beasts))
	tg.RegisterInline(planets.InlinePrefix, planets.InlineHandler(svc, tg, planets.InlinePrefix))
	for _, registration := range codex.InlineHandlers(gear, beasts, tg) {
		tg.RegisterInline(registration.Prefix, registration.Handler)
	}
}

// translateConfig 把配置段换算成翻译层参数（秒 → time.Duration 在这里统一换算）。
// 零值不在这里兜底：超时与限速的默认值由 translate.New 决定，配置层只负责「原样搬运」。
func translateConfig(cfg *config.Config) translate.Config {
	return translate.Config{
		Enabled:       cfg.Translate.Enabled,
		Mode:          cfg.Translate.Mode,
		APIURL:        cfg.Translate.APIURL,
		APIKey:        cfg.Translate.APIKey,
		Model:         cfg.Translate.Model,
		TargetLang:    cfg.Translate.TargetLang,
		Timeout:       time.Duration(cfg.Translate.Timeout * float64(time.Second)),
		MaxTokens:     cfg.Translate.MaxTokens,
		MinInterval:   cfg.Translate.MinInterval.Duration(),
		ErrorCooldown: cfg.Translate.ErrorCooldown.Duration(),
		FreeAPIURL:    cfg.Translate.FreeAPIURL,
	}
}

// newTranslator 构造翻译层；store 为 nil 时不缓存（测试路径）。
// 没有可用后端时只记警告并回退透传，不阻止启动——正文保持英文总比机器人起不来强。
func newTranslator(cfg *config.Config, store *state.Store) (translate.Translator, error) {
	var cache translate.CacheStore
	if store != nil {
		cache = store
	}
	return translate.New(translateConfig(cfg), translate.Deps{Store: cache, Logger: log.Printf})
}

// pluginTranslator 决定交给插件与推送的翻译层：用户主动关掉翻译、或开着却选不出可用后端时都给 nil。
//
// 为什么不能直接把 translate.New 的返回值传下去：这两种情况下它返回的都是「逐字回显原文」的
// 透传实现，而插件与推送把「译文与原文相同」判成「这一段没翻动」，于是每张卡片都会挂一句假的
// 「翻译暂不可用」——用户明明只是忘了填 api_url，日志里写的却是「翻译整体停用，正文保持英文」，
// 两处说法自相矛盾。nil 才是「不翻译也不注明」的哨兵值，所以这里用 IsPassthrough 把两种兜底情况
// 一起归一成 nil（只判 Enabled 会漏掉「开着翻译但没有可用后端」那一种）。
// 开着翻译且后端可用时必须原样透传同一个实例：包一层壳会让下游拿不到真正的缓存与限速。
func pluginTranslator(cfg *config.Config, trans translate.Translator) translate.Translator {
	if !cfg.Translate.Enabled || translate.IsPassthrough(trans) {
		return nil
	}
	return trans
}

// pushJob 把推送编排包装成 cron 任务。
// cron 任务没有返回错误的通道，所以失败在这里统一记日志：群和日志各司其职，
// 群只用来发战况，错误不往群里刷。
// timeout 是这一轮的总预算（见 pushTimeout）：cron 不会替我们串行化同一任务，必须靠 ctx 封顶。
func pushJob(spec string, collector *push.Collector, timeout time.Duration, logf func(string, ...any)) cron.Job {
	return cron.Job{
		Name: "战况推送",
		Spec: spec,
		Run: func() {
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			if err := collector.Run(ctx); err != nil {
				logf("战况推送失败：%v", err)
			}
		},
	}
}

// buildPushJobs 构造推送任务；push.enabled=false 时返回空列表
// （要停推就改配置重启，本阶段不做群内开关命令）。
func buildPushJobs(cfg *config.Config, svc *hd2.Service, store *state.Store, sender push.Sender,
	renderer render.Renderer, trans translate.Translator, display *time.Location,
	logf func(string, ...any)) []cron.Job {
	if !cfg.Push.Enabled {
		logf("战况推送已关闭（push.enabled=false）")
		return nil
	}
	collector := push.New(push.Config{
		ChatID:   cfg.Bot.GroupID,
		MaxItems: cfg.Push.MaxItems,
	}, push.Deps{
		Service:    svc,
		Store:      store,
		Sender:     sender,
		Renderer:   renderer,
		Translator: trans,
		Display:    display,
		Logger:     logf,
	})
	logf("战况推送已启用：表达式 %q，每节最多 %d 条", cfg.Push.Cron, cfg.Push.MaxItems)
	return []cron.Job{pushJob(cfg.Push.Cron, collector, pushTimeout, logf)}
}

// warnIfCacheTTLTooLong 在推送启用时提示缓存 TTL 可能过长：只警告、不阻止启动
// （用户把 push.cron 调稀一点时同样的 TTL 就不算长了，程序不该替他做这个决定）。
//
// 简报（cache.dispatches_ttl）刻意不参与检查：它的默认值本来就比轮询间隔长，
// 而缓存命中的后果只是「新简报晚一轮被发现」——下一轮基线还没推进，不会漏掉。
func warnIfCacheTTLTooLong(cfg *config.Config, logf func(string, ...any)) {
	if !cfg.Push.Enabled {
		return
	}
	for _, item := range []struct {
		name string
		ttl  config.Second
	}{
		{"cache.planets_ttl", cfg.Cache.PlanetsTTL},
		{"cache.campaigns_ttl", cfg.Cache.CampaignsTTL},
		{"cache.stations_ttl", cfg.Cache.StationsTTL},
	} {
		if item.ttl.Duration() >= pushIntervalCeiling {
			logf("提示：%s（%s）不小于推送轮询间隔（%s），可能漏检中间变化", item.name, item.ttl.Duration(), pushIntervalCeiling)
		}
	}
}

// buildClientConfig 把配置换算成主数据源客户端参数；
// limit.retry_base 与 limit.retry_max_delay 以秒为单位的浮点数存在，这里换算成时长。
func buildClientConfig(cfg *config.Config) hd2.ClientConfig {
	return hd2.ClientConfig{
		BaseURL:       cfg.API.BaseHelldivers,
		Timeout:       time.Duration(cfg.API.Timeout) * time.Second,
		Client:        cfg.API.Client,
		Contact:       cfg.API.Contact,
		UserAgent:     cfg.API.UserAgent,
		RetryMax:      cfg.Limit.RetryMax,
		RetryBase:     time.Duration(cfg.Limit.RetryBase * float64(time.Second)),
		RetryMaxDelay: time.Duration(cfg.Limit.RetryMaxDelay * float64(time.Second)),
		Logger:        log.Printf,
	}
}

// buildTTLConfig 把配置里的缓存时长（秒）换算成领域层的 TTL；零值表示不缓存。
func buildTTLConfig(cfg *config.Config) hd2.TTLConfig {
	return hd2.TTLConfig{
		War:         cfg.Cache.WarTTL.Duration(),
		Planets:     cfg.Cache.PlanetsTTL.Duration(),
		Campaigns:   cfg.Cache.CampaignsTTL.Duration(),
		Assignments: cfg.Cache.AssignmentsTTL.Duration(),
		Dispatches:  cfg.Cache.DispatchesTTL.Duration(),
		Events:      cfg.Cache.EventsTTL.Duration(),
		Stations:    cfg.Cache.StationsTTL.Duration(),
	}
}

// buildWebOptions 把配置换算成 HTTP 服务参数；就绪状态由调用方传入的标记决定，
// 未就绪时 /healthz 返回 503。
func buildWebOptions(cfg *config.Config, ready *atomic.Bool) web.Options {
	return web.Options{
		Host:   cfg.HTTP.Host,
		Port:   cfg.HTTP.Port,
		Ready:  ready.Load,
		Logger: log.Printf,
	}
}

// buildWikiClient 装配 wiki.gg 客户端。它有两个身份：敌人图鉴的数据源，
// 以及装备目录的缩略图解析器（行内结果里 SVG 装备需要换成位图，见 arsenal.ThumbResolver）。
func buildWikiClient(cfg *config.Config) *wikigg.Client {
	return wikigg.New(wikigg.Config{
		BaseURL: cfg.Bestiary.WikiAPI,
		Timeout: cfg.Bestiary.Timeout.Duration(),
	}, wikigg.Deps{})
}

// buildArsenalStore 装配装备图鉴的数据层。镜像前缀原样透传：
// 配置里写 "-" 表示只用主源，留空表示用内置镜像（口径在 arsenal 包里）。
func buildArsenalStore(cfg *config.Config, thumb arsenal.ThumbResolver) *arsenal.Store {
	return arsenal.New(arsenal.Config{
		Dir:          cfg.Arsenal.Dir,
		TTL:          cfg.Arsenal.TTL.Duration(),
		Timeout:      cfg.Arsenal.Timeout.Duration(),
		MirrorPrefix: cfg.Arsenal.Mirror,
	}, arsenal.Deps{Logger: log.Printf, Thumb: thumb})
}

// buildBestiaryStore 装配敌人图鉴的数据层：数据源就是上面那个 wiki 客户端。
func buildBestiaryStore(cfg *config.Config, source bestiary.Source) *bestiary.Store {
	return bestiary.New(bestiary.Config{
		Dir: cfg.Bestiary.Dir,
		TTL: cfg.Bestiary.TTL.Duration(),
	}, bestiary.Deps{Source: source, Logger: log.Printf})
}

// disabledRenderer 是「配置里关掉渲染」时注入的渲染器：任何卡片都渲染失败，
// 命令因此按约定回退纯文本。用一个小实现代替在各插件里判断 render.enabled，
// 插件只需要认识 render.Renderer 这一个契约。
type disabledRenderer struct{}

// Render 永远返回错误，表示这张卡片出不了图。
func (disabledRenderer) Render(context.Context, render.Card) ([]byte, error) {
	return nil, errors.New("渲染已关闭（render.enabled=false）")
}

// Close 没有需要释放的资源：这个实现不会启动浏览器。
func (disabledRenderer) Close() error { return nil }

// buildRenderer 按配置装配卡片渲染引擎：关闭渲染时返回「必定失败」的实现，
// 启用时构造 Chromium 引擎（浏览器仍然是第一次渲染时才懒启动，不会白起进程）。
func buildRenderer(cfg *config.Config) (render.Renderer, error) {
	if !cfg.Render.Enabled {
		return disabledRenderer{}, nil
	}
	eng, err := render.New(render.Config{
		Width:   cfg.Render.Width,
		Scale:   cfg.Render.Scale,
		Timeout: time.Duration(cfg.Render.Timeout * float64(time.Second)),
		Format:  cfg.Render.Format,
	})
	if err != nil {
		return nil, fmt.Errorf("构造渲染引擎失败：%w", err)
	}
	return eng, nil
}

// displayLocation 返回展示用时区：优先 Asia/Shanghai（Windows 上没有 tzdata 时会失败），
// 失败则回落到固定东八区（中国没有夏令时，两者结果一致）。
func displayLocation() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		log.Printf("加载时区 Asia/Shanghai 失败：%v，改用固定东八区", err)
		return time.FixedZone("CST", testZoneOffset)
	}
	return loc
}

// httpShutdowner 与 stateCloser 是停机序列需要的最小能力，便于测试注入假实现。
type (
	httpShutdowner interface {
		Shutdown(ctx context.Context) error
	}
	stateCloser interface{ Close() error }
)

// shutdownDeps 是停机序列需要的全部依赖。
// 这里刻意用结构体而不是一串位置参数：渲染引擎与状态文件都有 Close() error，
// 位置传参可以把两者悄悄传反（引擎该在状态文件之前关，传反就是先断掉 bbolt 再关浏览器），
// 而两个字段各有类型，传反会直接编译不过。
type shutdownDeps struct {
	// markNotReady 把服务标记为未就绪：/healthz 立刻返回 503，不再接新流量。
	markNotReady func()
	// cronStop 停止定时任务，不再产生新的上游请求。
	cronStop func()
	// server 是本地 HTTP 服务。
	server httpShutdowner
	// renderer 是卡片渲染引擎；Close 才释放常驻的 Chromium。允许为 nil（启动早期失败时还没构造引擎）。
	renderer render.Renderer
	// store 是状态文件（bbolt），必须最后关闭，否则在途请求写快照会失败、文件锁也不会释放。
	store stateCloser
}

// shutdownStep 是停机序列中的一步：name 用于日志，stop 负责释放资源。
type shutdownStep struct {
	name string
	stop func() error
}

// stopPolling 停止 Telegram 长轮询。
// 说明：消息库的 Run 阻塞在更新通道上，且没有能安全中断它的接口
// （BotAPI.StopReceivingUpdates 关掉的是接收协程用的通道，既唤不醒 Run，重复调用还会 panic），
// 所以轮询只能随进程退出终止；这里保留一个显式步骤，让停机顺序完整且可被测试覆盖。
// 声明为变量便于测试替换，用来断言停机顺序。
var stopPolling = func(logf func(format string, args ...any)) error {
	logf("停止 Telegram 轮询：在途指令处理完后随进程退出终止")
	return nil
}

// shutdownSteps 构造停机序列，顺序不可颠倒：
//  1. 标记服务未就绪：/healthz 立刻返回 503，不再接新流量；
//  2. 停止定时任务：不再产生新的上游请求；
//  3. 关闭 HTTP 服务：等待在途请求结束（最多 shutdownTimeout）；
//  4. 关闭渲染引擎：释放常驻 Chromium（一次都没渲染过就什么都不做）；放在停止轮询之前，
//     是因为轮询停止只意味着不再接新指令，在途指令可能还在出图；
//  5. 停止 Telegram 轮询（见 stopPolling）；
//  6. 关闭状态文件：bbolt 必须最后关，否则在途请求写快照会失败、文件锁也不会释放。
//
// renderer 允许为 nil（启动早期失败时可能还没构造引擎），此时这一步直接跳过。
func shutdownSteps(deps shutdownDeps) []shutdownStep {
	markNotReady, cronStop := deps.markNotReady, deps.cronStop
	renderer, store := deps.renderer, deps.store
	return []shutdownStep{
		{
			name: "标记服务未就绪",
			stop: func() error { markNotReady(); return nil },
		},
		{
			name: "停止定时任务",
			stop: func() error { cronStop(); return nil },
		},
		{
			name: "关闭 HTTP 服务",
			stop: func() error {
				ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
				defer cancel()
				return deps.server.Shutdown(ctx)
			},
		},
		{
			name: "关闭渲染引擎",
			stop: func() error {
				if renderer == nil {
					return nil
				}
				return renderer.Close()
			},
		},
		{
			name: "停止 Telegram 轮询",
			stop: func() error { return stopPolling(log.Printf) },
		},
		{
			name: "关闭状态文件",
			stop: func() error { return store.Close() },
		},
	}
}

// shutdownAll 按顺序执行停机步骤。每一步都会执行：前一步失败只记日志并继续，
// 保证后面的资源（尤其是 bbolt 的文件锁）一定被释放；返回值为各步骤错误的汇总。
func shutdownAll(logf func(string, ...any), steps []shutdownStep) error {
	var errs []error
	for _, step := range steps {
		if step.stop == nil {
			continue
		}
		if err := step.stop(); err != nil {
			logf("停机步骤「%s」失败：%v", step.name, err)
			errs = append(errs, fmt.Errorf("%s：%w", step.name, err))
			continue
		}
		logf("停机步骤「%s」完成", step.name)
	}
	return errors.Join(errs...)
}

// closeState 在启动失败的提前退出路径上关闭状态文件，避免留下文件锁。
func closeState(store *state.Store) {
	if store == nil {
		return
	}
	if err := store.Close(); err != nil {
		log.Printf("关闭状态文件失败：%v", err)
	}
}
