# 卡片素材来源与许可

本目录下的 PNG 素材来自两个开源来源，**原样复制、未做任何修改**。

第四、五节是后来新增的两个来源（装备目录与敌人图鉴）：它们的文件**不进仓库、也不进二进制**，
是运行时下载后缓存到 `data/` 下的，因此单独列出来说明出处与许可。

## 一、`astrbot_plugin_Helldivers`（MIT）

取自开源项目 **`astrbot_plugin_Helldivers`**（作者 fiatlux2333，MIT License）的 `assets/` 目录。

该项目自身的 `assets/LICENSES.md` 声明：除特别注明外，这些图形是作者为该插件**自绘**的几何风格粉丝作品
（由多边形、椭圆、线条、环形、星形、盾形等矢量图元构成），**不包含**《绝地潜兵 2》的游戏截图、官方 logo 文件、
从游戏提取的贴图，也没有描摹第三方作品。中文译名表（见 `src/hd2/glossary/data/`）同样来自该项目的
`assets/glossary/`，是《绝地潜兵 2》的官方简体中文术语对照数据。

**2026-09-17 起，本目录不再使用该项目的图形素材**：它那批自绘徽标（超级地球盾徽、派系徽记、
战术行动方块等）与游戏里的图形对不上，用户明确要求改用社区站点里的**游戏内原生图标**（见下一节），
所以 `emblems/`、`tactical/`、`dss/`、`global_events/` 下的那批 PNG 已一并删除，
只剩下译名表仍在用（`src/hd2/glossary/data/`）。

## 二、社区站点「HD2 真理部」的群系实景图（游戏内画面）

取自社区站点 **`jerry114514/HD2-Galatic_war-Map`** 的 `assets/biomes/` 目录，共 **26 张**，
存放于本目录的 `biomes/` 子目录下（文件名与上游一致），用作 `/planet` 的群系头图与 `/planets` 热点行的群系缩略图。
素材逻辑名与上游群系名的映射表见 `src/render/biome.go`。

**许可口径（2026-09-17，用户决定）**：这些图片是从《绝地潜兵 2》游戏画面中截取的实景图。
用户在这一天明确表示「版权问题不用关注，可以使用游戏画面」，因此本项目**不再遵守**此前
「卡片一律不使用游戏截图/官方图」的自设限制；如后续要公开发布，请自行重新评估这一步的风险。

| 本地路径（`biomes/`） | 上游路径 | 对应群系（上游 `biome.name`） |
| --- | --- | --- |
| `Arctic_glacier_base_Landscape.png` | `assets/biomes/` 同名 | Icy Glaciers |
| `Arctic_glacier_coldrocky_Landscape.png` | 同名 | Boneyard |
| `Autumn_Forest_Biome_Header.png` | 同名 | Deciduous Autumn Forest |
| `Bug_hiveworld_Landscape.png` | 同名 | Hive World |
| `Cyberstan_landscape.png` | 同名 | Cyberstan Megafactory |
| `Deciduous_Grove_Biome_Header.png` | 同名 | Deciduous Forest |
| `Magma_Base_Landscape.png` | 同名 | Magma |
| `Moor_arid_Landscape.png` | 同名 | Scorched Moor |
| `Moor_baseplanet_Landscape.png` | 同名 | Plains |
| `Moor_red_Landscape.png` | 同名 | Ionic Crimson |
| `Moor_tundra_Landscape.png` | 同名 | Tundra |
| `Primordial_base_Landscape.png` | 同名 | Volcanic Jungle |
| `Primordial_blue_Landscape.png` | 同名 | Ionic Jungle |
| `Primordial_dead_Landscape.png` | 同名 | Deadlands |
| `Primordial_purple_Landscape.png` | 同名 | Ethereal Jungle |
| `Rift_active_landscape.png` | 同名 | Void Source Forest |
| `Sandy_acid_Landscape.png` | 同名 | Acidic Badlands |
| `Sandy_base_Landscape.png` | 同名 | Desert Dunes |
| `Sandy_mineral_Landscape.png` | 同名 | Rocky Canyons |
| `Sandy_moon_Landscape.png` | 同名 | Moon |
| `Sandy_spiky_Landscape.png` | 同名 | Desert Cliffs |
| `Super_Earth_landscape.png` | 同名 | Super Earth |
| `Supercolony_Landscape.png` | 同名 | Supercolony |
| `Swamp_base_Landscape.png` | 同名 | Basic Swamp |
| `Swamp_haunted_Landscape.png` | 同名 | Haunted Swamp |
| `Tropical_Oasis_Biome_Header.png` | 同名 | Desert Oasis |

（2026-09-17 更正）此前以为站点自己也没给 `Void Source Forest` 配图，所以才不登记；拿到对方仓库源码后
发现 `assets/biomes/Rift_active_landscape.png` 就是这一条对应的图，于是补进来。现在对方 `index.json` 里的
26 个群系本项目全部收录；映射不到时仍然整块不渲染头图，不拿别的群系的图顶上。

卡片皮肤的调色板（纯黑底 + 琥珀强调 + 45° 斜纹底纹）同样参考了该站点的 `assets/css/tokens.css`，
属于**设计参考**，没有复制对方的 CSS 代码。

## 三、`SalmonC/HD2Tool`（装备目录与装备图，MIT）

`/gun`、`/strat`、`/armor`、`/grenade`、`/warbonds` 的目录数据与装备图取自开源项目
**`SalmonC/HD2Tool`**（MIT License）：

| 上游文件 | 用途 | 本地形态 |
| --- | --- | --- |
| `src/data/catalog.json` | 装备 / 债券目录（298 件） | 运行时下载 → `data/arsenal/catalog.json` |
| `src/data/community-aliases.json` | 玩家外号表（38 条） | 运行时下载 → `data/arsenal/community-aliases.json` |
| `public/assets/wiki/<id>.png\|svg` | 装备图（240 PNG + 58 SVG） | 按需下载 → `data/arsenal/images/` |

上游仓库的 MIT 覆盖**代码与数据编排**；装备图自身另有许可标注（上游目录里逐件的 `image.license`，
实测 188 张 `License/CC-BY-NC-SA`、109 张 `License/Arrowhead`、1 张 `License/Fairuse`），
也就是说这些图是**游戏内素材或 wiki 截图**，不是 HD2Tool 自绘。
用户已知悉这一点，并明确表示「版权问题不用关注，可以使用游戏画面」（2026-09-17）。

## 四、`helldivers.wiki.gg`（敌人图鉴数值与图标）

`/enemy` 的数值与图标来自 **https://helldivers.wiki.gg** 的 MediaWiki API：

| API 调用 | 用途 | 本地形态 |
| --- | --- | --- |
| `action=cargoquery&tables=Enemies` | 敌人标题、阵营、体型、血量、伤害 | 运行时下载 → `data/bestiary/enemies.json` |
| `prop=pageimages` | 敌人图标地址（行内查询的缩略图） | 只存网址，Telegram 直接取用 |

站点内容（数值表与图标）按 wiki.gg 的通行许可 **CC BY-NC-SA** 发布（署名 + 非商业 + 相同方式共享），
卡片上不再注明数据来源（用户 2026-09-17 要求删掉），完整口径只保留在本文件里；如要商用，请自行核对站点当时的授权条款。
图标里有一部分是 SVG：站点不提供 SVG 的位图缩略图（实测缩略图地址仍是 `.svg`，Telegram 不接受），
这类条目在行内结果里没有缩略图，卡片上不受影响（本地渲染能画 SVG）。

## 五、游戏内原生图标与标题字体（2026-09-17 新增）

取自社区站点 **`jerry114514/Jerry114514.github.io`**：

- 图标来自 `HD2-Galatic_war-Map/assets/`（`faction-icons/`、`ui-icons/`、`effect-icons/`）；
- 召唤指令的箭头来自 `HD2_Wiki/assets/arrows/`（与前者同一套游戏内图形）；
- 标题字体 `fonts/ZZZ-thick.ttf`（约 1.6 MB，随站点分发的粗黑标题字）。

这些图形是《绝地潜兵 2》**游戏内的原生图标**（阵营徽记、战术行动标记、增援标记、超级地球旗标等），
直接从游戏资源或游戏画面中提取，而站点自己的代码与数据编排按上游仓库的许可发布。
**许可口径与第二节的群系实景图一致**（用户 2026-09-17 决定：版权问题不用关注，可以使用游戏内素材）；
如要公开发布，请自行重新评估这一步的风险。字体同样按上游站点分发的形态内联进二进制。

SVG 图标在卡片里是**内联**的（不是 `<img>`）：它们多是纯黑剪影，靠 CSS 的 `fill` 继承上色，
颜色由卡片主题决定（阵营卡取阵营色、头栏徽标取琥珀色）。内联时会去掉 XML 声明与写死的宽高、
并给内部的 id（上游 SVG 里短到只有 `a`）加上逻辑名前缀，避免同一张卡片上多个图标互相串。
实现见 `src/render/assets.go` 的 `iconFunc`。

| 逻辑名（`assetFiles`） | 本地路径 | 用途 |
| --- | --- | --- |
| `emblem.super_earth` | `game/faction/humans.svg` | 超级地球徽记 |
| `emblem.terminids` / `emblem.automaton` / `emblem.illuminate` | `game/faction/*.svg` | 三个敌对阵营徽记 |
| `emblem.defense` | `game/ui/defense_campaign.svg` | 防守战标记 |
| `emblem.major_order` | `game/ui/liberation_campaign.svg` | 重要指令 / 解放战役标记 |
| `emblem.dss` | `game/effect/dss.svg` | 民主空间站 |
| `event.super_earth_flag` | `game/ui/super_earth_flag.svg` | 超级地球旗标 |
| `ui.reinforce` | `game/ui/reinforce.svg` | 增援标记（星球卡的在线士兵数） |
| `tactical.eagle_storm` / `tactical.orbital_blockade` / `tactical.heavy_ordnance` | `game/effect/dss_*.svg` | DSS 战术行动 |
| `tactical.orbital_napalm` | `game/effect/dss_planetary_bombardment.svg` | 上游没有与「轨道燃烧弹幕」一一对应的图标，取同一套里语义最近的行星轰炸 |
| `arrow.up` / `arrow.down` / `arrow.left` / `arrow.right` | `game/arrow/*.svg` | 召唤指令的方向箭头 |
| `font.title` | `fonts/ZZZ-thick.ttf` | 卡片标题层字体（`--font-title`） |

## 六、明确不引入的素材

`astrbot_plugin_Helldivers` 的 `assets/user/` 目录下的 `steam_hero.png`（官方 Key art，
© Arrowhead Game Studios）、`map.png`、`map_template.png`、`warfront_template.png` 等图片**没有**被复制到本项目；
本项目的卡片背景、边框、斜纹底纹一律用 CSS 绘制；固化在仓库里的位图只有徽标与上面那批群系实景图，
图鉴的装备图、敌人外观图、部位示意图与敌人图标则是运行时下载的（第三、四、七节）。

## 七、社区中文维基的图鉴详情数据（`src/atlas`，2026-09-17 新增）

`/gun`、`/strat`、`/grenade`、`/enemy` 卡片上的「详细属性 / 攻击部件 / 特性 / 使用策略 / 部位数据」
来自社区站点 **`jerry114514/Jerry114514.github.io`** 的 `HD2_Wiki/data/wiki/zh/`：

| 上游文件 | 本地副本 | 内容 |
| --- | --- | --- |
| `weapons.json` | `src/atlas/data/weapons.json` | 89 件武器的详细属性（弹体、伤害、分角度穿透、特殊效果）、特性、使用策略、变体 |
| `stratagems_full.json` | `src/atlas/data/stratagems.json` | 109 件战备的详细属性、呼叫指令方向串、冷却与使用次数 |
| `enemies.json` | `src/atlas/data/enemies.json` | 95 只敌人的描述、分类、伤害类型、踉跄阈值、最低难度、来源页与逐部位数据（含部位示意图地址） |
| `ds_terms.json` | `src/atlas/data/terms.json` | 上游整理的术语中英对照（数值翻译用） |

用法是**随仓库发布的内嵌数据**（`go:embed`，见 `src/atlas/atlas.go`）：不联网、不落盘，
按中文名 / 英文名 / 条目 id 建索引，命中就给卡片补上那几段；没有对应条目时卡片只画我们自己的图鉴信息。
条目的存在与否仍由 `src/arsenal` 与 `src/bestiary` 决定，本包只补充「这条目的详细属性长什么样」。

这些 JSON 里还带着图片地址（敌人的外观图与各部位的示意图，都指向 `helldivers.wiki.gg`）：
卡片上的敌人外观图与部位示意图就是按这些地址**运行时下载**的（缓存到 `data/bestiary/icons/`），
与本文件第四节的口径一致——图片本身来自 wiki.gg（CC BY-NC-SA，多为游戏内素材），随仓库发布的只有地址。

许可口径与第四节（wiki.gg）一致：这些数值与描述整理自 **helldivers.wiki.gg**（CC BY-NC-SA），
站点自身的代码与数据编排按其仓库分发；本项目只做展示，卡片上不注明来源，完整口径只保留在本文件里。

## 版权声明

《Helldivers》及相关名称、虚构设定是 Arrowhead Game Studios / Sony Interactive Entertainment 的商标或知识产权。
本项目是非官方粉丝作品，与其没有任何关联，也未获得其认可。
