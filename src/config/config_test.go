package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeTemp 在临时目录写一个配置文件，返回其路径。
func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "hd2.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写临时配置失败：%v", err)
	}
	return path
}

const validYAML = `
bot:
  name: hd2_test_bot
  token: 123:abc
  owner: 10086
  group_id: -1001234567890
  msg_del_delay: 10
api:
  client: hd2_bot
  contact: dev@example.com
  user_agent: hd2_bot/0.1
  timeout: 30
cache:
  war_ttl: 30
limit:
  rate: 4
  window: 10
`

// TestLoadValid 校验合法配置能被正确解析。
func TestLoadValid(t *testing.T) {
	cfg, err := Load(writeTemp(t, validYAML))
	if err != nil {
		t.Fatalf("加载配置失败：%v", err)
	}
	if cfg.Bot.GroupID != -1001234567890 {
		t.Errorf("group_id 解析错误，期望 -1001234567890，实际 %d", cfg.Bot.GroupID)
	}
	if cfg.Limit.Rate != 4 {
		t.Errorf("limit.rate 解析错误，期望 4，实际 %d", cfg.Limit.Rate)
	}
	if got := cfg.Cache.WarTTL.Duration(); got != 30*time.Second {
		t.Errorf("cache.war_ttl 解析错误，期望 30s，实际 %v", got)
	}
}

// TestLoadDefaults 校验未写的字段落到默认值。
func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(writeTemp(t, `
bot:
  token: 123:abc
  group_id: -1001234567890
api:
  client: hd2_bot
  contact: dev@example.com
`))
	if err != nil {
		t.Fatalf("加载配置失败：%v", err)
	}
	if cfg.API.UserAgent != "hd2_bot/0.1" {
		t.Errorf("user_agent 默认值错误：%s", cfg.API.UserAgent)
	}
	if cfg.API.Timeout != 30 {
		t.Errorf("timeout 默认值错误：%d", cfg.API.Timeout)
	}
	if cfg.Cache.PlanetsTTL.Duration() != 60*time.Second {
		t.Errorf("planets_ttl 默认值错误：%v", cfg.Cache.PlanetsTTL.Duration())
	}
	if cfg.Limit.Cooldown != 20 {
		t.Errorf("cooldown 默认值错误：%d", cfg.Limit.Cooldown)
	}
	if cfg.Limit.Reserve != 1 {
		t.Errorf("reserve 默认值错误：%d", cfg.Limit.Reserve)
	}
	if cfg.HTTP.Port != 25555 {
		t.Errorf("http.port 默认值错误：%d", cfg.HTTP.Port)
	}
}

// TestLoadMissingGroupID 校验缺少群 id 时直接报错。
func TestLoadMissingGroupID(t *testing.T) {
	_, err := Load(writeTemp(t, `
bot:
  token: 123:abc
api:
  client: hd2_bot
  contact: dev@example.com
`))
	if err == nil {
		t.Fatal("缺少 bot.group_id 时应当报错，实际通过")
	}
}

// TestLoadMissingContact 校验缺少上游联系方式时直接报错。
func TestLoadMissingContact(t *testing.T) {
	_, err := Load(writeTemp(t, `
bot:
  token: 123:abc
  group_id: -1001234567890
api:
  client: hd2_bot
`))
	if err == nil {
		t.Fatal("缺少 api.contact 时应当报错，实际通过")
	}
}

// TestLoadFileNotFound 校验显式路径指向不存在的文件时报错。
func TestLoadFileNotFound(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not_exist.yaml")
	if _, err := Load(missing); err == nil {
		t.Fatal("配置文件不存在时应当报错，实际通过")
	}
}

// TestLoadInvalidYAML 校验内容不是合法 YAML 时报错。
func TestLoadInvalidYAML(t *testing.T) {
	if _, err := Load(writeTemp(t, "bot: [unclosed")); err == nil {
		t.Fatal("非法 YAML 应当报错，实际通过")
	}
}

// TestLoadExplicitOverride 校验显式配置会覆盖默认值。
func TestLoadExplicitOverride(t *testing.T) {
	cfg, err := Load(writeTemp(t, `
bot:
  token: 123:abc
  group_id: -1001234567890
api:
  client: hd2_bot
  contact: dev@example.com
  timeout: 5
cache:
  war_ttl: 7
`))
	if err != nil {
		t.Fatalf("加载配置失败：%v", err)
	}
	if got := cfg.Cache.WarTTL.Duration(); got != 7*time.Second {
		t.Errorf("cache.war_ttl 覆盖失败，期望 7s，实际 %v", got)
	}
	if cfg.API.Timeout != 5 {
		t.Errorf("api.timeout 覆盖失败，期望 5，实际 %d", cfg.API.Timeout)
	}
}

// TestLoadJoinsAllErrors 校验多个字段非法时错误被汇集返回，而不是只报第一个。
func TestLoadJoinsAllErrors(t *testing.T) {
	_, err := Load(writeTemp(t, `
bot:
  token: 123:abc
  group_id: -1001234567890
api:
  client: hd2_bot
  contact: dev@example.com
  timeout: 0
http:
  port: 70000
`))
	if err == nil {
		t.Fatal("api.timeout 与 http.port 非法时应当报错，实际通过")
	}
	msg := err.Error()
	if !strings.Contains(msg, "api.timeout") {
		t.Errorf("错误信息应包含 api.timeout，实际：%s", msg)
	}
	if !strings.Contains(msg, "http.port") {
		t.Errorf("错误信息应包含 http.port，实际：%s", msg)
	}
}

// TestGetKeepsLastGoodConfig 校验加载失败时 Get 仍保留上一次成功加载的配置。
func TestGetKeepsLastGoodConfig(t *testing.T) {
	good, err := Load(writeTemp(t, validYAML))
	if err != nil {
		t.Fatalf("加载合法配置失败：%v", err)
	}
	if got := Get(); got == nil {
		t.Fatal("加载成功后 Get() 不应为 nil")
	} else if got.Bot.GroupID != good.Bot.GroupID {
		t.Errorf("Get() 的群 id 与加载结果不一致，期望 %d，实际 %d", good.Bot.GroupID, got.Bot.GroupID)
	}

	if _, err := Load(writeTemp(t, `
bot:
  token: 123:abc
  group_id: -1001234567890
api:
  client: hd2_bot
  contact: dev@example.com
http:
  port: 70000
`)); err == nil {
		t.Fatal("非法配置应当报错，实际通过")
	}
	got := Get()
	if got == nil {
		t.Fatal("加载失败后 Get() 不应为 nil")
	}
	if got.Bot.GroupID != good.Bot.GroupID {
		t.Errorf("加载失败后应保留上一次配置的群 id，期望 %d，实际 %d", good.Bot.GroupID, got.Bot.GroupID)
	}
}

// minYAML 是能通过校验的最小配置，供只关心新增字段的用例复用。
const minYAML = `
bot:
  token: 123:abc
  group_id: -1001234567890
api:
  client: hd2_bot
  contact: dev@example.com
`

// baseYAML 是新增字段齐全的配置骨架，用例通过 renderCaseYAML 替换占位符构造变体，
// 避免同一 YAML 段落被重复定义而解析失败。
const baseYAML = `
api:
  client: hd2_bot
  contact: dev@example.com
bot:
  token: 123:abc
  group_id: -1001234567890
render:
  enabled: {enabled}
  width: {width}
  scale: {scale}
  timeout: {timeout}
  format: {format}
`

// renderCaseYAML 按 占位符/值 交替给出的替换对生成一份完整配置；
// 同一占位符出现多次时以最后一次给出的值为准（因此用例可以覆盖默认骨架里的值）。
func renderCaseYAML(pairs ...string) string {
	keys := make([]string, 0, len(pairs)/2)
	values := make(map[string]string, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		if _, ok := values[pairs[i]]; !ok {
			keys = append(keys, pairs[i])
		}
		values[pairs[i]] = pairs[i+1]
	}
	replacer := make([]string, 0, len(keys)*2)
	for _, key := range keys {
		replacer = append(replacer, key, values[key])
	}
	return strings.NewReplacer(replacer...).Replace(baseYAML)
}

// renderAndInlineDefaults 是新增字段齐全时的合法占位符取值，作为用例的默认骨架。
var renderAndInlineDefaults = []string{
	"{enabled}", "true",
	"{width}", "900",
	"{scale}", "2",
	"{timeout}", "15",
	"{format}", "png",
}

// overrideDefaults 把用例给出的覆盖项叠加到默认占位符表之上，后出现的值覆盖先出现的值。
func overrideDefaults(pairs ...string) []string {
	return append(append([]string{}, renderAndInlineDefaults...), pairs...)
}

// TestRenderAndInlineDefaults 校验渲染、图片删除延迟与行内查询前缀的默认值。
func TestRenderAndInlineDefaults(t *testing.T) {
	cfg, err := Load(writeTemp(t, minYAML))
	if err != nil {
		t.Fatalf("加载配置失败：%v", err)
	}
	if !cfg.Render.Enabled {
		t.Error("render.enabled 默认值应为 true")
	}
	if cfg.Render.Width != 900 {
		t.Errorf("render.width 默认值错误，期望 900，实际 %d", cfg.Render.Width)
	}
	if cfg.Render.Scale != 2 {
		t.Errorf("render.scale 默认值错误，期望 2，实际 %d", cfg.Render.Scale)
	}
	if cfg.Render.Timeout != 15 {
		t.Errorf("render.timeout 默认值错误，期望 15，实际 %v", cfg.Render.Timeout)
	}
	if cfg.Render.Format != "png" {
		t.Errorf("render.format 默认值错误，期望 png，实际 %q", cfg.Render.Format)
	}
}

// TestValidateRejectsInvalidRenderFields 表驱动校验非法值都会报出对应字段名。
func TestValidateRejectsInvalidRenderFields(t *testing.T) {
	cases := []struct {
		name  string
		pairs []string
		field string
	}{
		{"width 为 0", []string{"{width}", "0"}, "render.width"},
		{"scale 为 0", []string{"{scale}", "0"}, "render.scale"},
		{"timeout 为 0", []string{"{timeout}", "0"}, "render.timeout"},
		{"format 非法", []string{"{format}", "xml"}, "render.format"},
		{"width 为负", []string{"{width}", "-100"}, "render.width"},
		{"scale 为负", []string{"{scale}", "-1"}, "render.scale"},
		{"timeout 为负", []string{"{timeout}", "-5"}, "render.timeout"},
		{"format 为空串", []string{"{format}", `""`}, "render.format"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeTemp(t, renderCaseYAML(overrideDefaults(tc.pairs...)...)))
			if err == nil {
				t.Fatalf("%s 非法时应当报错，实际通过", tc.field)
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Errorf("错误信息应包含 %s，实际：%s", tc.field, err.Error())
			}
		})
	}
}

// TestValidateJoinsRenderErrors 校验多个新增字段同时非法时错误被聚合返回。
func TestValidateJoinsRenderErrors(t *testing.T) {
	_, err := Load(writeTemp(t, renderCaseYAML(overrideDefaults(
		"{width}", "0",
		"{timeout}", "0",
		"{format}", "bmp",
	)...)))
	if err == nil {
		t.Fatal("多个渲染配置非法时应当报错，实际通过")
	}
	msg := err.Error()
	for _, field := range []string{"render.width", "render.timeout", "render.format"} {
		if !strings.Contains(msg, field) {
			t.Errorf("聚合错误信息应包含 %s，实际：%s", field, msg)
		}
	}
}

// TestRenderDisabledSkipsFieldValidation 校验关闭渲染后其余渲染字段允许为 0 或空。
func TestRenderDisabledSkipsFieldValidation(t *testing.T) {
	cfg, err := Load(writeTemp(t, renderCaseYAML(overrideDefaults(
		"{enabled}", "false",
		"{width}", "0",
		"{scale}", "0",
		"{timeout}", "0",
		"{format}", `""`,
	)...)))
	if err != nil {
		t.Fatalf("render.enabled=false 时不应报错，实际：%v", err)
	}
	if cfg.Render.Enabled {
		t.Error("render.enabled 应被覆盖为 false")
	}
	if cfg.Render.Width != 0 || cfg.Render.Scale != 0 || cfg.Render.Timeout != 0 {
		t.Errorf("render 数值字段应保持显式写入的 0，实际：%+v", cfg.Render)
	}
	if cfg.Render.Format != "" {
		t.Errorf("render.format 应保持显式写入的空串，实际 %q", cfg.Render.Format)
	}
}

// TestRenderFormatIsCaseInsensitive 校验 format 大小写不敏感并归一化为小写存储。
func TestRenderFormatIsCaseInsensitive(t *testing.T) {
	cfg, err := Load(writeTemp(t, renderCaseYAML(overrideDefaults("{format}", "PNG")...)))
	if err != nil {
		t.Fatalf("format: PNG 应当被接受，实际：%v", err)
	}
	if cfg.Render.Format != "png" {
		t.Errorf("format 应归一化为 png，实际 %q", cfg.Render.Format)
	}
}

// loadFromYAML 把给定 YAML 文本写入临时文件后加载，返回校验通过的配置；
// 加载失败会直接终止用例，因此只关心字段值的用例不必自己处理错误。
func loadFromYAML(t *testing.T, content string) *Config {
	t.Helper()
	cfg, err := Load(writeTemp(t, content))
	if err != nil {
		t.Fatalf("加载配置失败：%v", err)
	}
	return cfg
}

// validateFromYAML 只做「写临时文件 + Load」并原样返回错误；
// 用例既用它断言非法值被拦下，也用它断言开关关闭后同类字段被放行（返回 nil）。
func validateFromYAML(t *testing.T, content string) error {
	t.Helper()
	_, err := Load(writeTemp(t, content))
	return err
}

// TestTranslateConfigDefaults 校验翻译段的默认值：不写任何 translate 配置也能启动，且默认走 auto + 免费接口。
func TestTranslateConfigDefaults(t *testing.T) {
	cfg := loadFromYAML(t, minYAML)
	if !cfg.Translate.Enabled {
		t.Error("translate.enabled 默认应为 true")
	}
	if cfg.Translate.Mode != "auto" {
		t.Errorf("translate.mode 默认应为 auto，实际 %q", cfg.Translate.Mode)
	}
	if cfg.Translate.TargetLang != "zh-CN" {
		t.Errorf("translate.target_lang 默认应为 zh-CN，实际 %q", cfg.Translate.TargetLang)
	}
	if cfg.Translate.Timeout != 20 {
		t.Errorf("translate.timeout 默认应为 20 秒，实际 %v", cfg.Translate.Timeout)
	}
	if cfg.Translate.Model != "gpt-4o-mini" {
		t.Errorf("translate.model 默认应为 gpt-4o-mini，实际 %q", cfg.Translate.Model)
	}
	if cfg.Translate.MaxTokens != 1200 {
		t.Errorf("translate.max_tokens 默认应为 1200，实际 %d", cfg.Translate.MaxTokens)
	}
	if cfg.Translate.MinInterval != 0 {
		t.Errorf("translate.min_interval 默认应为 0（不限速），实际 %v", cfg.Translate.MinInterval)
	}
	const wantFreeAPI = "https://uapis.cn/api/v1/ai/translate"
	if cfg.Translate.FreeAPIURL != wantFreeAPI {
		t.Errorf("translate.free_api_url 默认应为 %q，实际 %q", wantFreeAPI, cfg.Translate.FreeAPIURL)
	}
	if cfg.Translate.ErrorCooldown != 120 {
		t.Errorf("translate.error_cooldown 默认应为 120 秒，实际 %v", cfg.Translate.ErrorCooldown)
	}
}

// TestPushConfigDefaults 校验推送段的默认值：默认开启、每 5 分钟一轮且与心跳错开、每节最多 5 条。
func TestPushConfigDefaults(t *testing.T) {
	cfg := loadFromYAML(t, minYAML)
	if !cfg.Push.Enabled {
		t.Error("push.enabled 默认应为 true")
	}
	if cfg.Push.Cron != "15 */5 * * * *" {
		t.Errorf("push.cron 默认应为 15 */5 * * * *，实际 %q", cfg.Push.Cron)
	}
	if cfg.Push.MaxItems != 5 {
		t.Errorf("push.max_items 默认应为 5，实际 %d", cfg.Push.MaxItems)
	}
}

// TestTranslateAndPushValidation 校验两段的非法值会被聚合报错，且错误信息带字段名。
func TestTranslateAndPushValidation(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{"未知 mode", "translate:\n  mode: baidu\n", "translate.mode"},
		{"timeout 非正", "translate:\n  timeout: 0\n", "translate.timeout"},
		{"max_tokens 非正", "translate:\n  max_tokens: 0\n", "translate.max_tokens"},
		{"cron 段数不对", "push:\n  cron: \"*/5 * * * *\"\n", "push.cron"},
		{"cron 为空", "push:\n  cron: \"\"\n", "push.cron"},
		{"max_items 非正", "push:\n  max_items: 0\n", "push.max_items"},
		{"min_interval 为负", "translate:\n  min_interval: -1\n", "translate.min_interval"},
		{"error_cooldown 为负", "translate:\n  error_cooldown: -1\n", "translate.error_cooldown"},
		{"target_lang 为空", "translate:\n  target_lang: \"\"\n", "translate.target_lang"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// 直接把追加段拼在最小骨架后面。前提是 minYAML 里不能出现 translate: / push: 等同名键，
			// 否则拼出来的 YAML 会有重复键，用例测到的就不再是字段校验，而是解析错误。
			err := validateFromYAML(t, minYAML+"\n"+c.yaml)
			if err == nil {
				t.Fatalf("非法配置应报错：\n%s", c.yaml)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("错误信息应包含字段名 %q，实际：%v", c.want, err)
			}
		})
	}
}

// TestTranslateDisabledSkipsValidation 校验关闭翻译时不校验后端细节（用户想临时改回英文模式不该被卡住启动）。
func TestTranslateDisabledSkipsValidation(t *testing.T) {
	if err := validateFromYAML(t, minYAML+"\ntranslate:\n  enabled: false\n  mode: baidu\n  timeout: 0\n"); err != nil {
		t.Fatalf("关闭翻译时不应因后端字段报错，实际：%v", err)
	}
}

// TestTranslateConfigNormalization 校验翻译段的文本字段在加载时就完成归一化。
// 用例特意把值写成「从别处粘贴时容易带上的脏值」：mode 大小写混杂、其余字段带首尾空格；
// 去掉 ToLower 或任一 TrimSpace 的实现都会被这里拦下（否则错误只会在真发请求时才暴露成看不懂的失败）。
func TestTranslateConfigNormalization(t *testing.T) {
	cfg := loadFromYAML(t, minYAML+`
translate:
  mode: AUTO
  api_url: "  https://api.openai.com/v1  "
  model: "  gpt-4o-mini  "
  target_lang: "  zh-CN  "
  free_api_url: "  https://uapis.cn/api/v1/ai/translate  "
`)
	if cfg.Translate.Mode != "auto" {
		t.Errorf("mode 应归一化为小写 auto，实际 %q", cfg.Translate.Mode)
	}
	if cfg.Translate.TargetLang != "zh-CN" {
		t.Errorf("target_lang 应去掉首尾空格，实际 %q", cfg.Translate.TargetLang)
	}
	if cfg.Translate.APIURL != "https://api.openai.com/v1" {
		t.Errorf("api_url 应去掉首尾空格，实际 %q", cfg.Translate.APIURL)
	}
	if cfg.Translate.Model != "gpt-4o-mini" {
		t.Errorf("model 应去掉首尾空格，实际 %q", cfg.Translate.Model)
	}
	if cfg.Translate.FreeAPIURL != "https://uapis.cn/api/v1/ai/translate" {
		t.Errorf("free_api_url 应去掉首尾空格，实际 %q", cfg.Translate.FreeAPIURL)
	}
}

// TestTranslateModeWhitelist 校验白名单里的三种取值都能通过校验：
// auto 之外，openai 与 free 是强制指定后端的合法写法，从白名单里删掉任意一个都会被这里拦下。
func TestTranslateModeWhitelist(t *testing.T) {
	for _, mode := range []string{"auto", "openai", "free"} {
		t.Run(mode, func(t *testing.T) {
			cfg := loadFromYAML(t, minYAML+"\ntranslate:\n  mode: "+mode+"\n")
			if cfg.Translate.Mode != mode {
				t.Errorf("mode 应保持为 %q，实际 %q", mode, cfg.Translate.Mode)
			}
		})
	}
}

// TestPushDisabledSkipsValidation 校验关闭推送时不再校验 cron 形状与 max_items：
// 用户先关掉推送、cron 还留着旧的五段式写法（或 max_items 留 0）时不该被卡住启动。
func TestPushDisabledSkipsValidation(t *testing.T) {
	cfg := loadFromYAML(t, minYAML+`
push:
  enabled: false
  cron: "*/5 * * * *"
  max_items: 0
`)
	if cfg.Push.Enabled {
		t.Error("push.enabled 应被覆盖为 false")
	}
	if cfg.Push.Cron != "*/5 * * * *" {
		t.Errorf("关闭推送时 cron 应保持显式写入的值，实际 %q", cfg.Push.Cron)
	}
	if cfg.Push.MaxItems != 0 {
		t.Errorf("关闭推送时 max_items 应保持显式写入的 0，实际 %d", cfg.Push.MaxItems)
	}
}

// TestArsenalAndBestiaryDefaults 校验两个新数据段的默认值：不写这两段也能启动，
// 缓存、超时与上游地址都落到内置默认（改这些数字等于改行为，用例先红一次）。
func TestArsenalAndBestiaryDefaults(t *testing.T) {
	cfg := loadFromYAML(t, minYAML)
	if cfg.Arsenal.Dir != "data/arsenal" {
		t.Errorf("arsenal.dir 默认应为 data/arsenal，实际 %q", cfg.Arsenal.Dir)
	}
	if cfg.Arsenal.TTL != 86400 {
		t.Errorf("arsenal.ttl 默认应为 86400 秒，实际 %v", cfg.Arsenal.TTL)
	}
	if cfg.Arsenal.Timeout != 30 {
		t.Errorf("arsenal.timeout 默认应为 30 秒，实际 %v", cfg.Arsenal.Timeout)
	}
	if cfg.Arsenal.Mirror != "https://gh-proxy.org/" {
		t.Errorf("arsenal.mirror 默认应为内置 GitHub 镜像，实际 %q", cfg.Arsenal.Mirror)
	}
	if cfg.Bestiary.Dir != "data/bestiary" {
		t.Errorf("bestiary.dir 默认应为 data/bestiary，实际 %q", cfg.Bestiary.Dir)
	}
	if cfg.Bestiary.TTL != 86400 {
		t.Errorf("bestiary.ttl 默认应为 86400 秒，实际 %v", cfg.Bestiary.TTL)
	}
	if cfg.Bestiary.Timeout != 20 {
		t.Errorf("bestiary.timeout 默认应为 20 秒，实际 %v", cfg.Bestiary.Timeout)
	}
	if cfg.Bestiary.WikiAPI != "https://helldivers.wiki.gg" {
		t.Errorf("bestiary.wiki_api 默认应为 helldivers.wiki.gg，实际 %q", cfg.Bestiary.WikiAPI)
	}
}

// TestArsenalAndBestiaryValidation 表驱动校验两个新段的非法值都会报出对应字段名。
func TestArsenalAndBestiaryValidation(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{"arsenal ttl 为负", "arsenal:\n  ttl: -1\n", "arsenal.ttl"},
		{"arsenal timeout 为 0", "arsenal:\n  timeout: 0\n", "arsenal.timeout"},
		{"bestiary ttl 为负", "bestiary:\n  ttl: -1\n", "bestiary.ttl"},
		{"bestiary timeout 为 0", "bestiary:\n  timeout: 0\n", "bestiary.timeout"},
		{"wiki_api 不是网址", "bestiary:\n  wiki_api: helldivers.wiki.gg\n", "bestiary.wiki_api"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// 直接把追加段拼在最小骨架后面（minYAML 里没有 arsenal: / bestiary: 同名键）。
			err := validateFromYAML(t, minYAML+"\n"+c.yaml)
			if err == nil {
				t.Fatalf("非法配置应报错：\n%s", c.yaml)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("错误信息应包含字段名 %q，实际：%v", c.want, err)
			}
		})
	}
}

// TestArsenalAndBestiaryNormalization 校验文本字段在加载时就完成归一化（粘贴配置常带首尾空格）：
// 目录名带空格会创建出另一个目录，wiki_api 带空格则连不上站点，都不该等到运行时才暴露。
func TestArsenalAndBestiaryNormalization(t *testing.T) {
	cfg := loadFromYAML(t, minYAML+`
arsenal:
  dir: "  data/gear  "
  mirror: "  https://gh-proxy.org/  "
bestiary:
  dir: "  data/beasts  "
  wiki_api: "  https://helldivers.wiki.gg  "
`)
	if cfg.Arsenal.Dir != "data/gear" {
		t.Errorf("arsenal.dir 应去掉首尾空格，实际 %q", cfg.Arsenal.Dir)
	}
	if cfg.Arsenal.Mirror != "https://gh-proxy.org/" {
		t.Errorf("arsenal.mirror 应去掉首尾空格，实际 %q", cfg.Arsenal.Mirror)
	}
	if cfg.Bestiary.Dir != "data/beasts" {
		t.Errorf("bestiary.dir 应去掉首尾空格，实际 %q", cfg.Bestiary.Dir)
	}
	if cfg.Bestiary.WikiAPI != "https://helldivers.wiki.gg" {
		t.Errorf("bestiary.wiki_api 应去掉首尾空格，实际 %q", cfg.Bestiary.WikiAPI)
	}
}

// TestArsenalMirrorDashIsKept 校验 mirror 填 "-"（只用主源，不走镜像）时原样保留：
// 这个值是 arsenal 包认识的开关，配置层把它 TrimSpace 成空串就等于改成「用内置镜像」。
func TestArsenalMirrorDashIsKept(t *testing.T) {
	cfg := loadFromYAML(t, minYAML+"\narsenal:\n  mirror: \" - \"\n")
	if cfg.Arsenal.Mirror != "-" {
		t.Errorf("arsenal.mirror 填 \"-\" 时应归一化成 -，实际 %q", cfg.Arsenal.Mirror)
	}
}
