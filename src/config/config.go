// Package config 负责读取 hd2.yaml、提供默认值并校验必要字段。
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/spf13/viper"
)

// Second 表示以秒为单位的时长，YAML 里写数字即可。
type Second int

// Duration 把秒转换为 time.Duration。
func (s Second) Duration() time.Duration { return time.Duration(s) * time.Second }

// Config 是机器人的全部配置。
type Config struct {
	Bot       BotConfig       `mapstructure:"bot"`
	API       APIConfig       `mapstructure:"api"`
	Cache     CacheConfig     `mapstructure:"cache"`
	Limit     LimitConfig     `mapstructure:"limit"`
	HTTP      HTTPConfig      `mapstructure:"http"`
	Render    RenderConfig    `mapstructure:"render"`
	Translate TranslateConfig `mapstructure:"translate"`
	Push      PushConfig      `mapstructure:"push"`
	Verify    VerifyConfig    `mapstructure:"verify"`
	Arsenal   ArsenalConfig   `mapstructure:"arsenal"`
	Bestiary  BestiaryConfig  `mapstructure:"bestiary"`
}

// BotConfig 机器人自身与目标群相关配置。
type BotConfig struct {
	Name        string  `mapstructure:"name"`
	Token       string  `mapstructure:"token"`
	Owner       int64   `mapstructure:"owner"`
	GroupID     int64   `mapstructure:"group_id"`
	MsgDelDelay float64 `mapstructure:"msg_del_delay"`
	Debug       bool    `mapstructure:"debug"`
}

// APIConfig 上游接口相关配置。
type APIConfig struct {
	Client         string `mapstructure:"client"`
	Contact        string `mapstructure:"contact"`
	UserAgent      string `mapstructure:"user_agent"`
	Timeout        int    `mapstructure:"timeout"`
	BaseHelldivers string `mapstructure:"base_helldivers"`
	BaseCompanion  string `mapstructure:"base_companion"`
}

// CacheConfig 各数据的缓存时长（秒）。
type CacheConfig struct {
	WarTTL         Second `mapstructure:"war_ttl"`
	PlanetsTTL     Second `mapstructure:"planets_ttl"`
	CampaignsTTL   Second `mapstructure:"campaigns_ttl"`
	AssignmentsTTL Second `mapstructure:"assignments_ttl"`
	DispatchesTTL  Second `mapstructure:"dispatches_ttl"`
	EventsTTL      Second `mapstructure:"events_ttl"`
	StationsTTL    Second `mapstructure:"stations_ttl"`
	EffectsTTL     Second `mapstructure:"effects_ttl"` // 行动变量（补充源，只在单星球卡上展示）
}

// LimitConfig 限流与重试参数。
type LimitConfig struct {
	Rate int `mapstructure:"rate"`
	// Reserve 是只留给用户命令的窗口额度：后台轮询（定时推送）最多只能用 rate-Reserve 个。
	// 默认 1（60 秒的窗口里给命令留一个）：一轮推送要连发 3 个请求，不预留的话它跑完时
	// 同一分钟内的 /planet（最多 4 个端点）就会撞上限流。填 0 表示不预留（回到旧行为）。
	Reserve int `mapstructure:"reserve"`
	// Window 是滑动窗口长度；上游是 5 次/分钟，所以这里的窗口也应当是 60 秒。
	Window        Second  `mapstructure:"window"`
	Cooldown      Second  `mapstructure:"cooldown"`
	RetryMax      int     `mapstructure:"retry_max"`
	RetryBase     float64 `mapstructure:"retry_base"`
	RetryMaxDelay float64 `mapstructure:"retry_max_delay"`
}

// HTTPConfig 本地 HTTP 服务监听配置。
type HTTPConfig struct {
	Host string `mapstructure:"host"`
	Port int    `mapstructure:"port"`
}

// RenderConfig 图片卡片渲染相关配置；Enabled 为 false 时所有命令直接走纯文本，
// 其余字段允许缺省，因此校验只在启用渲染时进行。
type RenderConfig struct {
	Enabled bool    `mapstructure:"enabled"`
	Width   int     `mapstructure:"width"`   // 卡片 CSS 宽度（px）
	Scale   int     `mapstructure:"scale"`   // 设备缩放，2 表示二倍图
	Timeout float64 `mapstructure:"timeout"` // 单张渲染超时（秒）
	Format  string  `mapstructure:"format"`  // 输出格式，只允许 png 或 jpeg
}

// TranslateConfig 翻译层配置；Enabled 为 false 时所有正文保持英文，其余字段不再校验，
// 这样用户临时关掉翻译（或只想跑离线模式）不会被后端细节卡住启动。
type TranslateConfig struct {
	Enabled bool   `mapstructure:"enabled"`
	Mode    string `mapstructure:"mode"`    // 选路规则：auto=填了 api_url 就走 OpenAI 兼容接口、否则走 free_api_url；openai / free 则强制指定对应后端
	APIURL  string `mapstructure:"api_url"` // OpenAI 兼容地址（例如 https://api.openai.com/v1），只在 api_key 有效时可用
	APIKey  string `mapstructure:"api_key"` // 只写在 hd2.yaml 里，不进仓库、不进日志
	Model   string `mapstructure:"model"`   // OpenAI 兼容后端使用的模型名
	// TargetLang 目标语言，送译时作为 prompt 参数。
	TargetLang string  `mapstructure:"target_lang"`
	Timeout    float64 `mapstructure:"timeout"`    // 单次请求超时（秒）
	MaxTokens  int     `mapstructure:"max_tokens"` // 译文输出上限，防免费模型自行加戏
	// MinInterval 相邻请求最小间隔，只写整数秒（配额 3 次/分钟就填 20）；0 表示不限速。
	// 字段类型是整数秒，写小数会被静默截断（0.4 会变成 0，语义直接翻转成「不限速」），
	// 需要亚秒粒度就得先改字段类型。
	MinInterval Second `mapstructure:"min_interval"`
	// ErrorCooldown 429/5xx 之后的冷却，只写整数秒；0 表示不冷却。
	// 同样是整数秒类型，写小数会被静默截断（0.4 会变成 0，变成「完全不冷却」）。
	ErrorCooldown Second `mapstructure:"error_cooldown"`
	// FreeAPIURL 免费翻译接口地址。注意合规边界：mode 选到 free 时，待翻译的上游正文
	// 会原样发给这个第三方免费接口，介意正文外发的用户应改用 openai 兼容后端或关掉翻译。
	FreeAPIURL string `mapstructure:"free_api_url"`
}

// ArsenalConfig 是装备图鉴（/gun、/strat、/armor、/grenade、/warbonds）的数据层配置。
//
// 数据源默认是社区维护的 helldivers-api（每天自动抓 wiki.gg，游戏出新债券能自己跟上）；
// hd2tool 是建站时用的静态目录，已停止更新（数据停在 2026-08-14），只在需要时切回来用。
type ArsenalConfig struct {
	Dir     string `mapstructure:"dir"`     // 缓存目录（相对进程工作目录），留空用 data/arsenal
	TTL     Second `mapstructure:"ttl"`     // 目录缓存有效期（秒），0 表示用默认的 24 小时
	Timeout Second `mapstructure:"timeout"` // 单次下载超时（秒）
	// Source 选数据源：helldivers-api（默认）或 hd2tool。
	Source string `mapstructure:"source"`
	// APIBase 是 helldivers-api 的站点地址，留空用内置默认值；自建镜像时改这里。
	APIBase string `mapstructure:"api_base"`
	// ZhWikiBase 是补中文名的术语表地址（社区中文维基），留空用内置默认值；
	// 填 "-" 表示不拉术语表，只用内置中文名表。
	ZhWikiBase string `mapstructure:"zh_wiki"`
	// ZhNames 是本地中文名覆盖表（id → 中文名），留空用缓存目录下的 zh-names.local.json；
	// 文件不存在是正常情况。
	ZhNames string `mapstructure:"zh_names"`
	// Mirror 是老源（hd2tool）的 GitHub 镜像前缀，只在主源（raw.githubusercontent.com）
	// 连不上时使用；填 "-" 表示不用镜像（例如能直连 GitHub 的机器）。
	Mirror string `mapstructure:"mirror"`
}

// VerifyConfig 是入群验证（gatekeeper）的配置：默认关闭，关掉时整个功能不注册任何处理器。
//
// 两种模式对应群的两种加群设置：
//   - direct：群为「直接加入」，新成员进群后机器人先禁言、再在群里发题面与提示；
//   - approval：群为「申请加入」，机器人收到入群申请后私聊发题面与提示，通过才批准。
//
// 没有「自动」档：两种流程差别很大（一个要禁言、一个要 approveChatJoinRequest），
// 猜错的后果是把验证发到错误的会话里（群里没人被禁言 / 私聊根本发不出去），不如让人写清楚。
type VerifyConfig struct {
	Enabled bool   `mapstructure:"enabled"` // 总开关
	Mode    string `mapstructure:"mode"`    // direct | approval
	// Challenge 是验证形式；目前只有 code（呼叫演练：按顺序点出战备的召唤指令）。
	Challenge string `mapstructure:"challenge"`
	// ShowAnswer 为 true 时把召唤指令印在题面图上（照抄即可，不劝退新兵）；
	// 为 false 时只给战备名，凭记忆点 —— 硬核模式。
	ShowAnswer bool   `mapstructure:"show_answer"`
	Timeout    Second `mapstructure:"timeout"`  // 验证超时（秒），超时按失败处理
	Attempts   int    `mapstructure:"attempts"` // 允许答错的次数，超过即失败
	Failure    string `mapstructure:"failure"`  // kick（踢出，还能重新申请）| ban（封禁）
	// SkipInvited 为 true 时，被其它成员邀请入群的人免验证（只有自己点链接进群的才验证）。
	SkipInvited bool `mapstructure:"skip_invited"`
	// UnbanDelay 是踢出后过多久解封（秒）；0 表示立刻解封。
	// 留一点延迟是给「秒回」一点阻力，也让管理员有时间看到记录后手动封禁。
	UnbanDelay Second `mapstructure:"unban_delay"`
	// Welcome 为 true 时，通过验证后在群里发一条文本欢迎。
	Welcome bool `mapstructure:"welcome"`
	// WelcomeText 是自定义欢迎文案，可用 {mention}（@他）与 {name}（昵称）两个占位符；
	// 留空用内置文案。按 MarkdownV2 发送，写普通文字即可，不用自己转义。
	WelcomeText string `mapstructure:"welcome_text"`
	// WelcomeDelDelay 是欢迎消息的自动删除延迟（秒）；0 表示不删。
	WelcomeDelDelay Second `mapstructure:"welcome_del_delay"`
	// AdWords 是昵称/简介里命中就直接拦下的关键词；留空用内置表（TG、加v、返佣…）。
	AdWords []string `mapstructure:"ad_words"`
	// AuditChannel 是审计日志频道 id（把入群/拒绝事件也发一份到频道）；0 表示只写本地日志。
	AuditChannel int64 `mapstructure:"audit_channel"`
}

// BestiaryConfig 是敌人图鉴（/enemy）的数据层配置。
type BestiaryConfig struct {
	Dir     string `mapstructure:"dir"`      // 缓存目录，留空用 data/bestiary
	TTL     Second `mapstructure:"ttl"`      // 图鉴缓存有效期（秒），0 表示用默认的 24 小时
	Timeout Second `mapstructure:"timeout"`  // 单次请求超时（秒）
	WikiAPI string `mapstructure:"wiki_api"` // wiki.gg 的站点地址，留空用 https://helldivers.wiki.gg
}

// PushConfig 战况推送配置；关闭后定时任务不注册，其余字段不再校验。
type PushConfig struct {
	Enabled  bool   `mapstructure:"enabled"`
	Cron     string `mapstructure:"cron"`      // 六段式 cron 表达式（秒 分 时 日 月 周），默认与心跳错开 15 秒
	MaxItems int    `mapstructure:"max_items"` // 卡片每一节最多列几条
}

// current 保存最近一次成功加载的配置，供全局读取。
var current atomic.Pointer[Config]

// Load 读取配置文件并校验；path 为空时按 hd2.yaml、src/hd2.yaml 的顺序查找。
func Load(path string) (*Config, error) {
	v := viper.New()
	v.SetConfigType("yaml")
	if path != "" {
		v.SetConfigFile(path)
	} else {
		found := ""
		for _, candidate := range []string{"hd2.yaml", filepath.Join("src", "hd2.yaml")} {
			if _, err := os.Stat(candidate); err == nil {
				found = candidate
				break
			}
		}
		if found == "" {
			return nil, errors.New("未找到配置文件 hd2.yaml，请复制 hd2.example.yaml 并填写后再启动")
		}
		v.SetConfigFile(found)
	}
	setDefaults(v)
	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("读取配置文件失败：%w", err)
	}
	cfg := &Config{}
	if err := v.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("解析配置失败：%w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	current.Store(cfg)
	return cfg, nil
}

// Get 返回最近一次成功加载的配置；未加载过时返回 nil。
// 返回的 *Config 视为只读，调用方不得修改其中的字段；加载失败时保留上一次成功加载的值。
func Get() *Config { return current.Load() }

// setDefaults 写入所有可省略字段的默认值。
func setDefaults(v *viper.Viper) {
	v.SetDefault("bot.msg_del_delay", 10)
	v.SetDefault("api.user_agent", "hd2_bot/0.1")
	v.SetDefault("api.timeout", 30)
	v.SetDefault("api.base_helldivers", "https://api.helldivers2.dev/api/v1")
	v.SetDefault("api.base_companion", "https://helldiverscompanion.com/api")
	v.SetDefault("cache.war_ttl", 60)
	v.SetDefault("cache.planets_ttl", 60)
	v.SetDefault("cache.campaigns_ttl", 120)
	v.SetDefault("cache.assignments_ttl", 300)
	v.SetDefault("cache.dispatches_ttl", 600)
	v.SetDefault("cache.events_ttl", 300)
	v.SetDefault("cache.stations_ttl", 120)
	v.SetDefault("cache.effects_ttl", 300)
	v.SetDefault("limit.rate", 4)
	v.SetDefault("limit.reserve", 1)
	// 窗口默认 60 秒：上游实测是「5 次/分钟/IP」（响应头 x-ratelimit-limit: 5，
	// 第 6 次请求直接 429，且各端点共用一个额度）。窗口写小了等于自己把额度打爆——
	// 实测 window=10 + rate=4（=24 次/分钟）时，连续几次请求后所有命令都会拿到 429。
	v.SetDefault("limit.window", 60)
	v.SetDefault("limit.cooldown", 20)
	v.SetDefault("limit.retry_max", 3)
	v.SetDefault("limit.retry_base", 5)
	v.SetDefault("limit.retry_max_delay", 45)
	v.SetDefault("http.host", "127.0.0.1")
	v.SetDefault("http.port", 25555)
	v.SetDefault("render.enabled", true)
	v.SetDefault("render.width", 900)
	v.SetDefault("render.scale", 2)
	v.SetDefault("render.timeout", 15)
	v.SetDefault("render.format", "png")
	v.SetDefault("translate.enabled", true)
	v.SetDefault("translate.mode", "auto")
	v.SetDefault("translate.model", "gpt-4o-mini")
	v.SetDefault("translate.target_lang", "zh-CN")
	v.SetDefault("translate.timeout", 20)
	v.SetDefault("translate.max_tokens", 1200)
	v.SetDefault("translate.min_interval", 0)
	v.SetDefault("translate.error_cooldown", 120)
	v.SetDefault("translate.free_api_url", "https://uapis.cn/api/v1/ai/translate")
	v.SetDefault("push.enabled", true)
	v.SetDefault("push.cron", "15 */5 * * * *")
	v.SetDefault("push.max_items", 5)
	v.SetDefault("verify.enabled", false)
	v.SetDefault("verify.mode", "direct")
	v.SetDefault("verify.challenge", "code")
	v.SetDefault("verify.show_answer", true)
	v.SetDefault("verify.timeout", 180)
	v.SetDefault("verify.attempts", 1)
	v.SetDefault("verify.failure", "kick")
	v.SetDefault("verify.skip_invited", true)
	v.SetDefault("verify.unban_delay", 60)
	v.SetDefault("verify.welcome", true)
	v.SetDefault("verify.welcome_text", "")
	v.SetDefault("verify.welcome_del_delay", 0)
	v.SetDefault("verify.audit_channel", 0)
	v.SetDefault("arsenal.dir", "data/arsenal")
	v.SetDefault("arsenal.ttl", 86400)
	v.SetDefault("arsenal.timeout", 30)
	v.SetDefault("arsenal.source", "helldivers-api")
	v.SetDefault("arsenal.api_base", "https://helldivers-api.dionatha.com.br")
	v.SetDefault("arsenal.zh_wiki", "https://jerry114514.github.io/HD2_Wiki/data/wiki/zh")
	v.SetDefault("arsenal.zh_names", "")
	v.SetDefault("arsenal.mirror", "https://gh-proxy.org/")
	v.SetDefault("bestiary.dir", "data/bestiary")
	v.SetDefault("bestiary.ttl", 86400)
	v.SetDefault("bestiary.timeout", 20)
	v.SetDefault("bestiary.wiki_api", "https://helldivers.wiki.gg")
}

// Validate 校验必要字段，收集全部问题后一次性返回；全部合法时返回 nil。
// 校验过程中会把 render.format 归一化为小写，因此调用方拿到的配置字段始终是规范化后的值。
func (c *Config) Validate() error {
	var errs []error

	// 必填字符串与数量类字段。
	if c.Bot.Token == "" {
		errs = append(errs, errors.New("bot.token 不能为空，请填写 BotFather 下发的 token"))
	}
	if c.Bot.GroupID == 0 {
		errs = append(errs, errors.New("bot.group_id 不能为空，请填写机器人服务的群 id"))
	}
	if c.API.Client == "" {
		errs = append(errs, errors.New("api.client 不能为空（上游要求的 X-Super-Client）"))
	}
	if c.API.Contact == "" {
		errs = append(errs, errors.New("api.contact 不能为空（上游要求的 X-Super-Contact）"))
	}
	if c.Limit.Rate <= 0 {
		errs = append(errs, errors.New("limit.rate 必须大于 0"))
	}
	if c.Limit.Reserve < 0 {
		errs = append(errs, errors.New("limit.reserve 不能小于 0"))
	}
	if c.Limit.Window <= 0 {
		errs = append(errs, errors.New("limit.window 必须大于 0"))
	}
	if c.Limit.RetryMax <= 0 {
		errs = append(errs, errors.New("limit.retry_max 必须大于 0"))
	}

	// 以秒为单位的时长字段，按固定顺序检查，保证错误信息顺序稳定。
	for _, item := range []struct {
		name  string
		value Second
	}{
		{"cache.war_ttl", c.Cache.WarTTL},
		{"cache.planets_ttl", c.Cache.PlanetsTTL},
		{"cache.campaigns_ttl", c.Cache.CampaignsTTL},
		{"cache.assignments_ttl", c.Cache.AssignmentsTTL},
		{"cache.dispatches_ttl", c.Cache.DispatchesTTL},
		{"cache.events_ttl", c.Cache.EventsTTL},
		{"cache.stations_ttl", c.Cache.StationsTTL},
		{"cache.effects_ttl", c.Cache.EffectsTTL},
		{"limit.cooldown", c.Limit.Cooldown},
	} {
		if item.value <= 0 {
			errs = append(errs, fmt.Errorf("%s 必须大于 0（秒）", item.name))
		}
	}

	// 秒为单位的浮点字段。
	if c.Limit.RetryBase <= 0 {
		errs = append(errs, errors.New("limit.retry_base 必须大于 0（秒）"))
	}
	if c.Limit.RetryMaxDelay <= 0 {
		errs = append(errs, errors.New("limit.retry_max_delay 必须大于 0（秒）"))
	}

	// 超时时间为 0 会让 http.Client 永不超时，查询可能永久挂起。
	if c.API.Timeout <= 0 {
		errs = append(errs, errors.New("api.timeout 必须大于 0（秒）"))
	}

	// 监听端口必须是合法端口号。
	if c.HTTP.Port < 1 || c.HTTP.Port > 65535 {
		errs = append(errs, errors.New("http.port 必须在 1..65535 之间"))
	}

	// render.format 的大小写归一化与 enabled 无关：关闭渲染时下游仍可能读到该字段，
	// 只在启用分支里归一化会让关闭渲染的配置把 "PNG" 原样传给调用方，埋下大小写不一致的坑。
	c.Render.Format = strings.ToLower(strings.TrimSpace(c.Render.Format))

	// 渲染参数：关闭渲染时全部放行，启用时必须是能真正出图的值。
	if c.Render.Enabled {
		if c.Render.Width <= 0 {
			errs = append(errs, errors.New("render.width 必须大于 0（px）"))
		}
		if c.Render.Scale <= 0 {
			errs = append(errs, errors.New("render.scale 必须大于 0"))
		}
		if c.Render.Timeout <= 0 {
			errs = append(errs, errors.New("render.timeout 必须大于 0（秒）"))
		}
		switch c.Render.Format {
		case "png", "jpeg":
		default:
			errs = append(errs, fmt.Errorf("render.format 只能是 png 或 jpeg（大小写不敏感），实际为 %q", c.Render.Format))
		}
	}

	// 翻译段：关闭时全部放行；开启时校验成一个「能真的发得出去请求」的配置。
	// 字段归一化与 render.format 的处理保持一致：无论开关都先规整字段，理由有两条——
	// 一是从别处粘贴地址、模型名时很容易带上首尾空格，直接拿去拼 URL 或填请求体只会得到
	// 一个看不懂的失败（甚至把空格写进 HTTP 头）；二是关闭翻译的配置也不该把 "AUTO"
	// 这种原样值传给下游，埋下大小写不一致的坑。这里只归一化，不替用户做同义词替换。
	c.Translate.Mode = strings.ToLower(strings.TrimSpace(c.Translate.Mode))
	c.Translate.TargetLang = strings.TrimSpace(c.Translate.TargetLang)
	c.Translate.APIURL = strings.TrimSpace(c.Translate.APIURL)
	c.Translate.Model = strings.TrimSpace(c.Translate.Model)
	c.Translate.FreeAPIURL = strings.TrimSpace(c.Translate.FreeAPIURL)
	if c.Translate.Enabled {
		switch c.Translate.Mode {
		case "auto", "openai", "free":
		default:
			errs = append(errs, fmt.Errorf("translate.mode 只能是 auto、openai 或 free（大小写不敏感），实际为 %q", c.Translate.Mode))
		}
		if c.Translate.TargetLang == "" {
			errs = append(errs, errors.New("translate.target_lang 不能为空（例如 zh-CN）"))
		}
		if c.Translate.Timeout <= 0 {
			errs = append(errs, errors.New("translate.timeout 必须大于 0（秒）"))
		}
		if c.Translate.MaxTokens <= 0 {
			errs = append(errs, errors.New("translate.max_tokens 必须大于 0（0 会让免费模型自行加戏）"))
		}
		if c.Translate.MinInterval < 0 {
			errs = append(errs, errors.New("translate.min_interval 不能为负数（0 表示不限速）"))
		}
		if c.Translate.ErrorCooldown < 0 {
			errs = append(errs, errors.New("translate.error_cooldown 不能为负数（0 表示不冷却）"))
		}
	}

	// 图鉴两段：缓存目录留空是有意义的（走默认值），因此不校验；
	// TTL 为 0 也表示用默认值，负数没有意义。
	c.Arsenal.Dir = strings.TrimSpace(c.Arsenal.Dir)
	c.Arsenal.Mirror = strings.TrimSpace(c.Arsenal.Mirror)
	c.Arsenal.Source = normalizeArsenalSource(c.Arsenal.Source)
	c.Arsenal.APIBase = strings.TrimSpace(c.Arsenal.APIBase)
	c.Arsenal.ZhWikiBase = strings.TrimSpace(c.Arsenal.ZhWikiBase)
	c.Arsenal.ZhNames = strings.TrimSpace(c.Arsenal.ZhNames)
	c.Bestiary.Dir = strings.TrimSpace(c.Bestiary.Dir)
	c.Bestiary.WikiAPI = strings.TrimSpace(c.Bestiary.WikiAPI)
	if c.Arsenal.TTL < 0 {
		errs = append(errs, errors.New("arsenal.ttl 不能为负数（0 表示用默认的 24 小时）"))
	}
	if c.Arsenal.Timeout <= 0 {
		errs = append(errs, errors.New("arsenal.timeout 必须大于 0（秒）"))
	}
	switch c.Arsenal.Source {
	case "", "helldivers-api", "hd2tool":
	default:
		errs = append(errs, fmt.Errorf("arsenal.source 只能是 helldivers-api 或 hd2tool，实际为 %q", c.Arsenal.Source))
	}
	if c.Arsenal.APIBase != "" && !strings.HasPrefix(c.Arsenal.APIBase, "http") {
		errs = append(errs, fmt.Errorf("arsenal.api_base 必须是以 http 开头的站点地址，实际为 %q", c.Arsenal.APIBase))
	}
	if c.Arsenal.ZhWikiBase != "" && c.Arsenal.ZhWikiBase != "-" && !strings.HasPrefix(c.Arsenal.ZhWikiBase, "http") {
		errs = append(errs, fmt.Errorf("arsenal.zh_wiki 必须是以 http 开头的站点地址、留空或填 \"-\"，实际为 %q", c.Arsenal.ZhWikiBase))
	}
	if c.Bestiary.TTL < 0 {
		errs = append(errs, errors.New("bestiary.ttl 不能为负数（0 表示用默认的 24 小时）"))
	}
	if c.Bestiary.Timeout <= 0 {
		errs = append(errs, errors.New("bestiary.timeout 必须大于 0（秒）"))
	}
	if c.Bestiary.WikiAPI != "" && !strings.HasPrefix(c.Bestiary.WikiAPI, "http") {
		errs = append(errs, fmt.Errorf("bestiary.wiki_api 必须是以 http 开头的站点地址，实际为 %q", c.Bestiary.WikiAPI))
	}

	// 推送段：cron 在这里只校验「六段式」这个形状，真正的解析交给 cron 包在启动时做
	// （那里的报错更准确，也避免 config 为了解析表达式去依赖调度库）。
	c.Push.Cron = strings.TrimSpace(c.Push.Cron)
	if c.Push.Enabled {
		if fields := strings.Fields(c.Push.Cron); len(fields) != 6 {
			errs = append(errs, fmt.Errorf("push.cron 必须是六段式 cron 表达式（秒 分 时 日 月 周），实际为 %q", c.Push.Cron))
		}
		if c.Push.MaxItems <= 0 {
			errs = append(errs, errors.New("push.max_items 必须大于 0（每节最多列几条）"))
		}
	}

	// 入群验证段：关闭时字段不再校验（别人可能先填好配置、等群里设置改好了再打开）。
	if c.Verify.Enabled {
		c.Verify.Mode = normalizeChoice(c.Verify.Mode, "direct")
		c.Verify.Challenge = normalizeChoice(c.Verify.Challenge, "code")
		c.Verify.Failure = normalizeChoice(c.Verify.Failure, "kick")
		switch c.Verify.Mode {
		case "direct", "approval":
		case "auto":
			errs = append(errs, errors.New("verify.mode 不再支持 auto：请按群的加群设置写 direct（直接加入）或 approval（申请加入）"))
		default:
			errs = append(errs, fmt.Errorf("verify.mode 只能是 direct 或 approval，实际为 %q", c.Verify.Mode))
		}
		switch c.Verify.Challenge {
		case "code":
		default:
			errs = append(errs, fmt.Errorf("verify.challenge 目前只支持 code（呼叫演练），实际为 %q", c.Verify.Challenge))
		}
		switch c.Verify.Failure {
		case "kick", "ban":
		default:
			errs = append(errs, fmt.Errorf("verify.failure 只能是 kick 或 ban，实际为 %q", c.Verify.Failure))
		}
		if c.Verify.Timeout <= 0 {
			errs = append(errs, errors.New("verify.timeout 必须大于 0（秒）"))
		} else if c.Verify.Timeout > maxVerifyTimeout {
			// 上限不是技术限制，而是运营约束：验证卡片在群里挂太久等于没人管。
			errs = append(errs, fmt.Errorf("verify.timeout 不能超过 %d 秒（验证卡片在群里挂太久就是没人管）", int(maxVerifyTimeout)))
		}
		if c.Verify.Attempts < 0 || c.Verify.Attempts > maxVerifyAttempts {
			errs = append(errs, fmt.Errorf("verify.attempts 应在 0 到 %d 之间（0 表示一次都不许错）", maxVerifyAttempts))
		}
		if c.Verify.UnbanDelay < 0 || c.Verify.UnbanDelay > maxVerifyTimeout {
			errs = append(errs, fmt.Errorf("verify.unban_delay 应在 0 到 %d 秒之间（0 表示立刻解封）", int(maxVerifyTimeout)))
		}
		if c.Verify.WelcomeDelDelay < 0 || c.Verify.WelcomeDelDelay > maxVerifyTimeout {
			errs = append(errs, fmt.Errorf("verify.welcome_del_delay 应在 0 到 %d 秒之间（0 表示不删除）", int(maxVerifyTimeout)))
		}
		c.Verify.WelcomeText = strings.TrimSpace(c.Verify.WelcomeText)
		c.Verify.AdWords = trimNonEmpty(c.Verify.AdWords)
	}

	return errors.Join(errs...)
}

// 入群验证的取值上限：超时最多 1 小时，答错次数最多 5 次。
const (
	maxVerifyTimeout  Second = 3600
	maxVerifyAttempts        = 5
)

// normalizeChoice 归一化枚举取值：大小写与空白不敏感，留空用默认值。
// 取值合法性由 Validate 判定，这里只保证 "KICK"、" kick " 这类写法能被认出来。
func normalizeChoice(value, fallback string) string {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	if trimmed == "" {
		return fallback
	}
	return trimmed
}

// trimNonEmpty 去掉首尾空白并丢弃空串与重复项，保持原有顺序。
func trimNonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	return out
}

// normalizeArsenalSource 归一化数据源取值：大小写与空白不敏感，留空用默认源。
// 取值合法性由 Validate 判定，这里只做规范化，避免 "HD2Tool" 这种写法被当成未知源。
func normalizeArsenalSource(source string) string {
	trimmed := strings.ToLower(strings.TrimSpace(source))
	switch trimmed {
	case "", "helldivers-api":
		return "helldivers-api"
	case "hd2tool":
		return "hd2tool"
	default:
		return trimmed
	}
}
