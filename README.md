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
- **入群验证**（可选，默认关闭）：新成员先做一次「呼叫演练」——按顺序点出战备的召唤指令才放行；群为申请模式时改在私聊里验证，通过才批准入群。

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

## 入群验证（可选，默认关闭）

`verify.enabled: true` 打开后，新成员要先做一次**呼叫演练**：机器人出一件战备，
他按顺序点出这件战备的召唤指令（↑↓←→ 四个按钮）才算通过。

一次验证发**一条消息**：

- **图片说明**部分就是提示文本：谁、限时、实时进度、剩余机会；按钮挂在同一条消息上，
  每次点击都用 `editMessageCaption` 在它上面刷新进度；
- **图片本身**是题面：战备图标、战备中文名（型号）、召唤指令，没有标题栏与其他说明。

题面图取不到（没配渲染器、图下载失败、渲染失败、发图失败）时退成**纯文本消息**：
内容与按钮完全一样，答案这时补在文本里（上传图时答案只在图里，不重复），
刷新进度改走 `editMessageText` —— 验证流程不依赖图片。

两种模式对应群的两种加群设置，`verify.mode` 决定（**没有自动档**，必须按群的设置写清楚）：

| 模式 | 触发 | 流程 |
| --- | --- | --- |
| `direct` | 群是「直接加入」，成员自己点链接进群 | 先进群 → 机器人禁言 → 群里发题面图与提示 → 点对了解除禁言并发欢迎 |
| `approval` | 群是「申请加入」，成员提交申请 | 机器人私聊发题面图与提示 → 点对了 `approveChatJoinRequest` 批准入群，他真进群时再发欢迎 |

为什么不自动判断：两种模式对应的 Telegram 动作完全不同（禁言 vs 批准入群申请），
按更新类型去猜，猜错了验证会跑到错误的会话里（群里没人被禁言 / 私聊根本发不出去）。

结算方式：超时（`verify.timeout`，默认 180 秒）或点错用光次数（`verify.attempts`，默认 1 次）即失败，
按 `verify.failure` 踢出（默认；踢出 = 封禁 + `verify.unban_delay` 秒后解封，默认 60 秒）或封禁；
申请模式则拒绝申请（`declineChatJoinRequest`）。
提示文本底部还挂着「✅ 放行 / 🚫 封禁」两个按钮，管理员可以直接人工处理。

其他行为：

- 昵称 / 用户名 / 简介命中广告词（`verify.ad_words`，留空用内置表）**直接拦下**，不浪费一次验证；
- `verify.skip_invited: true`（默认）时，被其它成员拉进群的人免验证，但仍会收到欢迎；
- **通过后发一条文本欢迎**（`verify.welcome`，可用 `welcome_text` 自定义文案，占位符 `{mention}` / `{name}`；
  `welcome_del_delay` 可让它到点自动删除）；
- 题面图、提示文本、「XX 加入了群组」的系统消息，以及退群时那条「XX 离开了群组」，都会由机器人删掉；
- 审计：每个新成员的归宿都记一行日志（谁、从哪条路来、结果、为什么），可用 `verify.audit_channel` 发一份到频道；
- 启动时会自检机器人有没有「限制成员 / 删除消息」权限，缺了直接在日志里说明后果。

题面图取不到（`render.enabled=false`、图下载失败、发图失败）时只发提示文本，答案这时补在文本里 —— 流程不受影响。

## 卡片风格

真理部战术 HUD：纯黑底加琥珀强调色，硬描边、45 度斜纹与网格分割，长内容自动分页。
所有卡片共用 `src/render/templates/base.tmpl` 外壳，数据时间与「数据可能已过期」角标来自卡片视图模型内嵌的 `Meta`。

## 数据来源

| 来源 | 用途 | 说明 |
| --- | --- | --- |
| `api.helldivers2.dev` | 主源 | `/api/v1` 的 war、planets、campaigns、assignments、dispatches、events；空间站只在 `/api/v2/space-stations` 上。要求带 `X-Super-Client` 与 `X-Super-Contact`（即 `api.contact`）|
| `helldiverscompanion.com` | 补充源 | 行动变量整包（只有 `/planet` 会用到），不占主源额度 |
| `helldivers.wiki.gg` | 数值与图标 | 敌人图鉴数据与图标，内容按 CC BY-NC-SA 授权，运行时缓存到 `data/bestiary` |
| `helldivers-api.dionatha.com.br` | **装备目录主源** | 社区项目 `DionathaGoulart/Helldivers-api`，每天定时抓 wiki.gg 后发布静态 JSON，因此游戏出新债券 / 新装备能自己跟上。目录在本地转成项目自己的 catalog JSON 再落盘 |
| 社区中文维基 `HD2_Wiki` | 图鉴详细资料 | 内置在 `src/atlas/data`，随二进制一起发布 |
| 社区中文维基 `HD2_Wiki` 术语表 | 装备中文名 | `terms.json`（1700+ 条中英对照）：新源只有英文名，每次刷新目录时顺带拉一次补中文名 |
| `SalmonC/HD2Tool` | 中文名基线 + 备用源 | 官方简中译名表已内置（`src/arsenal/data/zh-names.json`）。**该仓库自 2026-08-14 起不再更新数据**，所以从 v0.1.5 起降级为备用源：`arsenal.source: hd2tool` 可以切回去，镜像 `arsenal.mirror` 也只在切回时生效 |
| 社区站点 `HD2-Galatic_war-Map` | 群系实景图 | `src/render/assets/biomes/` |
| GitHub 镜像 `gh-proxy.org` | 备用源的兜底 | 只在 `arsenal.source: hd2tool` 且主源连不上时使用 |

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
| `arsenal` / `bestiary` | 装备目录与敌人图鉴的缓存目录、有效期、超时与来源（装备侧含 `source` / `api_base` / `zh_wiki` / `zh_names`） |
| `push` | 定时播报：cron 表达式、每节最多几条 |
| `verify` | 入群验证：开关、模式（`auto`/`direct`/`approval`）、难度、超时、容错、失败处理、广告词与审计频道 |

几个容易踩的点：

- `limit.window` 应当写 `60`：上游是「5 次 / 分钟 / IP」且所有端点共用，写小了会频繁拿到 429。
- `limit.rate` 默认 4 而不是 5，是给 429 留余量；`limit.reserve`（默认 1）是只留给用户命令的额度，定时推送最多用 `rate - reserve` 个。
- `cache` 里的 TTL 不要大于推送间隔（默认 5 分钟），否则中间的变化会被缓存吃掉，播报会漏。
- `render.enabled=false` 时所有命令直接走纯文本，适合没有浏览器的环境。
- 装备目录换源（`arsenal.source`）：默认 `helldivers-api`（每天自动抓 wiki.gg，新债券不会缺）；`hd2tool` 是已停更的静态目录，只在前者整体挂掉时临时切回用。
- 新源只给英文名，中文名按「本地覆盖 → 内置表 → 运行期术语表」三层补；最新出的一两件装备（例如某套新护甲）三层都没有时会显示英文名，这是有意为之——不猜译名。想补就在 `data/arsenal/zh-names.local.json` 里写 `{"items": {"<id>": "中文名"}, "warbonds": {}}`，刷新一次缓存即可生效。
- 开入群验证前**先把机器人设为管理员**并勾上「限制成员 + 删除消息」：没有前者则禁言失败（验证形同虚设），没有后者则入群系统消息与验证卡片清理不掉。启动日志会自检这两项并写明后果。
- `verify.mode` 要与群的加群设置对上：群是「申请加入」时写 `approval` 或 `auto`，群是「直接加入」时写 `direct` 或 `auto`。写 `auto` 最省心。
- 申请模式依赖机器人给申请人**发私聊**：Telegram 允许机器人给提交入群申请的人私聊，但如果对方屏蔽了机器人，这次申请会被直接拒绝（审计里写明原因），不会卡住。

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
src/arsenal/                装备目录（武器、战备、护甲、手雷、债券）：数据源与装备图缓存
src/bestiary/ src/wikigg/   敌人图鉴与 wiki.gg 客户端
src/atlas/                  图鉴详细资料（社区中文维基数据，随二进制发布）
src/translate/              正文翻译层（失败一律回退原文）
src/plugins/                指令插件：war、planets、orders、station、codex、system 等
src/plugins/gatekeeper/     入群验证：呼叫演练的题目、会话、卡片与审计（默认关闭）
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
- 装备目录换源之后有几处口径变化，都不是 bug：
  - 装备图是上游的 **WebP**（实测单张 6~60 KB）。卡片侧不做缩放、原样内联（标准库没有 WebP 解码器，不为它加依赖），
    Chromium 能直接渲染；行内查询的缩略图仍走 wiki.gg 的 PNG 文件页。
  - 上游的攻击数据没有「持续伤害（DPS）」这一项，所以燃烧 / 毒气类部件在卡片上不再显示「持续伤害」那一行。
  - 目录只收已上线的装备：上游标了 `upcoming` 的（还没上线的新债券及其装备）不进目录，上线后下一轮刷新自动出现；
    跳过数量会写在启动日志的 `[arsenal] 目录已生成…` 一行里。
  - 新源没有玩家外号表，`data/arsenal/community-aliases.json` 会一直沿用最后一次从备用源下载的那份（跟源无关）。
  - `data/arsenal/images/` 里可能同时留着旧源的 `.png` 与新源的 `.webp`，前者是缓存残留，可以删。
- 入群验证的两条限制：
  - 只对配置里的那个群生效（`bot.group_id`）：机器人待在别的群里也不会去管人；
  - 进行中的验证**只存在内存里**：进程重启后这次验证就没了，被禁言的新成员会一直留在禁言状态，
    需要管理员手动解除（卡片上的「放行」按钮也一样失效）。重启频繁的部署要留意这一点。