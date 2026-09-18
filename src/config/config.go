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
// 数据来源是固定的两个上游地址，因此这里只配缓存与镜像，不开放改源（改源需要同时改解析逻辑）。
type ArsenalConfig struct {
	Dir     string `mapstructure:"dir"`     // 缓存目录（相对进程工作目录），留空用 data/arsenal
	TTL     Second `mapstructure:"ttl"`     // 目录缓存有效期（秒），0 表示用默认的 24 小时
	Timeout Second `mapstructure:"timeout"` // 单次下载超时（秒）
	// Mirror 是 GitHub 镜像前缀，只在主源（raw.githubusercontent.com）连不上时使用；
	// 填 "-" 表示不用镜像（例如能直连 GitHub 的机器）。
	Mirror string `mapstructure:"mirror"`
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
	v.SetDefault("arsenal.dir", "data/arsenal")
	v.SetDefault("arsenal.ttl", 86400)
	v.SetDefault("arsenal.timeout", 30)
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

	// 图鉴两段：这两个数据源的地址不开放配置，所以只校验「能不能真的发得出去请求」的那几项。
	// 缓存目录留空是有意义的（走默认值），因此不校验；TTL 为 0 也表示用默认值，负数没有意义。
	c.Arsenal.Dir = strings.TrimSpace(c.Arsenal.Dir)
	c.Arsenal.Mirror = strings.TrimSpace(c.Arsenal.Mirror)
	c.Bestiary.Dir = strings.TrimSpace(c.Bestiary.Dir)
	c.Bestiary.WikiAPI = strings.TrimSpace(c.Bestiary.WikiAPI)
	if c.Arsenal.TTL < 0 {
		errs = append(errs, errors.New("arsenal.ttl 不能为负数（0 表示用默认的 24 小时）"))
	}
	if c.Arsenal.Timeout <= 0 {
		errs = append(errs, errors.New("arsenal.timeout 必须大于 0（秒）"))
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

	return errors.Join(errs...)
}
