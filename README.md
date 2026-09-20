# hd2_bot

《绝地潜兵 2》（HELLDIVERS 2）的 Telegram 情报机器人：把银河战况、星球、重要指令、战役简报、星球事件、民主空间站，
以及武器 / 战备 / 护甲 / 手雷 / 债券 / 敌人图鉴，渲染成图片卡片发到群里，并每 5 分钟把战况变化自动播报一次。

> 非官方粉丝作品，与 Arrowhead Game Studios、Sony Interactive Entertainment 无任何关联。
> 代码以 MIT 许可发布（见 `LICENSE`）；卡片素材与数据来源的出处、许可见 `src/render/assets/LICENSES.md`。

## 功能

- **指令查询**：15 条指令覆盖战况、星球、指令、简报、事件、空间站与图鉴。
- **行内查询**：在任意聊天里输入 `@机器人名 星球-天园` 这类前缀即可边打边搜，结果可以直接发出。
- **图片卡片**：信息类命令默认渲染成图片；渲染失败或 `render.enabled=false` 时自动回退 MarkdownV2 纯文本，功能不受影响。
- **定时播报**：每 5 分钟比对一次数据指纹，只在真的有变化时发卡片，分四类——新简报 / 重要指令、战役开始与结束、星球易主、空间站与 DSS 状态变更。
- **限流友好**：上游每 IP 每分钟只允许 5 次请求且所有端点共用额度，机器人用滑动窗口限流加 429 全局冷却把请求压在额度内，并给用户命令预留额度（`limit.reserve`），推送不会把额度占满导致命令查不动。
- **降级兜底**：上游不可用时改用磁盘快照（`data/hd2.db`），卡片上标出「数据可能已过期」，而不是直接报错。
- **中文化**：内置星球 / 星区 / 敌人 / 战备的官方简体中文译名表，上游正文可由翻译层翻成中文（`translate.enabled` 可关闭）。

## 指令一览

| 指令 | 作用 |
| --- | --- |
| `/war` | 银河战况总览：在线士兵、任务胜负、三族击杀 |
| `/planets` | 战线总览：在线士兵、星球总数与进攻中数量 |
| `/planet <星球名或编号>` | 单颗星球：控制方、解放进度 / 血量、事件，以及战略情报分析、行动变量与兴趣点 |
| `/assignments` | 重要指令（Major Order）：任务、奖励与截止时间 |
| `/dispatches [条数]` | 战役简报：最新几条游戏内公告 |
| `/events` | 星球事件：正在进行的防守战及进度 |
| `/stations` | 民主空间站：状态、当前停靠位置、战术行动与截止时间 |
| `/gun <关键字>` | 武器图鉴：主武器 / 副武器 / 支援武器 |
| `/strat <关键字>` | 战备图鉴：哨戒炮 / 轨道 / 飞鹰 / 背包等 |
| `/armor <关键字>` | 护甲图鉴：等级、护甲值、速度与被动 |
| `/grenade <关键字>` | 手雷图鉴：投掷物与手雷 |
| `/warbonds [关键字]` | 债券：不带关键字给搜索按钮，带关键字看某一本明细 |
| `/enemy <关键字>` | 敌人图鉴：血量、阵营、体型与伤害 |
| `/help` | 显示帮助卡片 |
| `/ping` | 检查在线状态；指令与回复会自动清理 |

行内前缀：`星球-`、`武器-`、`战备-`、`护甲-`、`手雷-`、`债券-`、`敌人-`。
前缀固定在代码里登记，不放进配置文件。

## 卡片风格

真理部战术 HUD：纯黑底加琥珀强调色，硬描边、45 度斜纹与网格分割，长内容自动分页。
所有卡片共用 `src/render/templates/base.tmpl` 外壳，数据时间与「数据可能已过期」角标来自卡片视图模型内嵌的 `Meta`。

## 数据来源

| 来源 | 用途 | 说明 |
| --- | --- | --- |
| `api.helldivers2.dev` | 主源 | `/api/v1` 的 war、planets、campaigns、assignments、dispatches、events；空间站只在 `/api/v2/space-stations` 上。要求带 `X-Super-Client` 与 `X-Super-Contact`（即 `api.contact`）|
| `helldiverscompanion.com` | 补充源 | 行动变量整包（只有 `/planet` 会用到），不占主源额度 |
| `helldivers.wiki.gg` | 数值与图标 | 敌人图鉴数据与图标，内容按 CC BY-NC-SA 授权，运行时缓存到 `data/bestiary` |
| 社区中文维基 `HD2_Wiki` | 图鉴详细资料 | 内置在 `src/atlas/data`，随二进制一起发布 |
| 社区站点 `HD2-Galatic_war-Map` | 群系实景图 | `src/render/assets/biomes/` |
| GitHub 镜像 `gh-proxy.org` | 装备目录兜底 | 只在主源连不上时使用（`arsenal.mirror`，填 `-` 表示不用）|

## 快速开始

需要 Go 1.22 或更高版本；首次出图会下载 Playwright 驱动与 Chromium，需要能访问外网。

```bash
git clone <仓库地址> hd2_bot
cd hd2_bot
cp hd2.example.yaml hd2.yaml       # Windows: copy hd2.example.yaml hd2.yaml
```

至少填好 `bot.token`、`bot.group_id`、`api.contact` 三项（要用翻译再填 `translate.api_key`），然后：

```bash
go run .
```

配置文件按 `hd2.yaml`、`src/hd2.yaml` 的顺序查找。`hd2.yaml`、`data/`、`tmp/` 已在 `.gitignore` 里，不会进仓库。
启动后可在群里用 `/help` 或 `/planets` 检查出图是否正常。

## 配置说明

`hd2.example.yaml` 是带逐行中文注释的完整配置，字段含义以它为准；这里只说各段分工：

| 段 | 作用 |
| --- | --- |
| `bot` | token、拥有者、目标群、`/ping` 回复延迟、调试开关 |
| `api` | 上游地址、超时，以及上游要求的 `X-Super-Client` / `X-Super-Contact` |
| `cache` | 各类数据的缓存时长（秒），按数据变化快慢分别配置 |
| `limit` | 限流：每窗口请求数、给用户命令预留的额度、窗口秒数、429 冷却、重试次数与退避 |
| `http` | 本地 HTTP 服务（`/healthz` 健康检查）的监听地址与端口 |
| `render` | 出图开关、卡片宽度、缩放、超时与图片格式 |
| `translate` | 正文翻译：OpenAI 兼容接口或免费接口、模型、冷却与限速 |
| `arsenal` / `bestiary` | 装备目录与敌人图鉴的缓存目录、有效期、超时与来源 |
| `push` | 定时播报：cron 表达式、每节最多几条 |

几个容易踩的点：

- `limit.window` 应当写 `60`：上游是「5 次 / 分钟 / IP」且所有端点共用，写小了会频繁拿到 429。
- `limit.rate` 默认 4 而不是 5，是给 429 留余量；`limit.reserve`（默认 1）是只留给用户命令的额度，定时推送最多用 `rate - reserve` 个。
- `cache` 里的 TTL 不要大于推送间隔（默认 5 分钟），否则中间的变化会被缓存吃掉，播报会漏。
- `render.enabled=false` 时所有命令直接走纯文本，适合没有浏览器的环境。

## 部署

**Linux 二进制**（服务器上直接跑）：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o hd2_bot .
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o playwright github.com/mxschmitt/playwright-go/cmd/playwright
```

主程序与 `playwright` 二进制必须来自同一份 `go.mod`（版本锁定在 `go.mod` 里）。
上线时把新二进制放到部署目录再重启进程即可；只改配置只要重启，改了代码才需要重新编译。

**Docker**（Debian 12 容器，Chromium 与系统依赖都装在镜像里）：见 `deploy/docker/README.md`，
其中包含从旧部署迁移、日常操作与镜像内构建的完整步骤。

## 目录结构

```text
main.go                     入口：装配各组件、注册指令与定时任务、停机序列
hd2.example.yaml            配置模板（真实配置 hd2.yaml 不进仓库）
src/config/                 配置读取与校验
src/hd2/                    数据层：主源客户端、补充源、限流、缓存、快照降级、聚合服务
src/hd2/glossary/           星球 / 星区 / 敌人 / 战备的官方简中译名
src/arsenal/                装备目录（武器、战备、护甲、手雷、债券）与装备图缓存
src/bestiary/ src/wikigg/   敌人图鉴与 wiki.gg 客户端
src/atlas/                  图鉴详细资料（社区中文维基数据，随二进制发布）
src/translate/              正文翻译层（失败一律回退原文）
src/plugins/                指令插件：war、planets、orders、station、codex、system 等
src/render/                 卡片渲染：模板、素材、Chromium 引擎与高度限制
src/push/                   定时播报：取数、指纹比对、变化检测与推送卡片
src/state/                  bbolt 状态库：快照、推送去重、翻译缓存
src/core/                   Telegram 客户端封装、定时任务、本地 HTTP 服务
deploy/docker/              容器部署文件与说明
```

## 开发与测试

```bash
go test ./...      # 全量测试
gofmt -l .         # 格式检查
```

真浏览器冒烟测试默认跳过，本地有 Chromium 时打开开关运行，卡片会写到 `tmp/screenshots/`：

```bash
HD2_RENDER_SMOKE=1 go test ./src/plugins/...
```

## 已知限制

- 上游额度是每 IP 每分钟 5 次且所有端点共用，所以缓存与限流不能去掉；`limit.reserve` 是在「推送及时」与「命令可用」之间取的折中。
- 行动变量只有补充源提供，取一次要下整包，因此缓存比其他数据长（`cache.effects_ttl`）。
- 空间站数据只存在于上游 `/api/v2`，端点一旦变化需要同步改 `src/hd2/client.go` 里的端点表。