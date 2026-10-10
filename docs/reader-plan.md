# MeBox 阅读功能实施计划（legado 书源兼容）

> 分支：`feature/reading`
> 目标：在首页增加「影视 / 阅读」模式切换，阅读模式完整兼容阅读 3.0（legado）书源体系，覆盖 **文本（bookSourceType=0）、音频（=1）、漫画/图片（=2）** 三类源。
> 实现方式（用户明确要求）：**样式与逻辑全部仿造 refgd/legado 本体**，不参考其他重实现项目；相当于用 Go + React 18 + TypeScript 5 重写该项目。
> 界面与交互的唯一规格：`docs/reader-ui-spec.md`（从 legado 源码逐屏调研产出）。
> 规则引擎的唯一语义基准：legado `app/src/main/java/io/legado/app/model/analyzeRule/` 源码，Go 侧逐方法移植对拍（源码克隆在 `C:\MyProject\_ref\legado`，仅作对照，不进入构建）。

## 1. 范围

**做：**
- 首页「影视/阅读」切换，阅读模式下有独立首页（书架/搜索/发现/最近阅读）
- legado 书源导入与管理（URL 导入、文本/JSON 粘贴导入、启停、分组、排序）
- 三类书源的完整链路：搜索 → 详情 → 目录 → 正文/播放列表/图片列表
- Go 侧规则引擎：CSS(jsoup 风格) / JSONPath / XPath / 正则 / 内嵌 JS 五种语法及其组合
- 文本阅读器、音频播放器、漫画阅读器三套前端 UI
- 追更、阅读进度同步（服务端存储，多端一致）、换源、替换净化规则

**不做（本期明确排除）：**
- `webView` 类规则（需要无头浏览器，识别后标记该源为不兼容并提示）
- `webView` 真人校验类登录（loginUrl 走 WebView 人工过验证码的场景；
  纯 JS / 表单类登录已支持，见 P4.5）
- RSS/订阅源、TTS 朗读、文件类型源（bookSourceType=3）
- 本地 TXT/EPUB 导入（列为后续可选）

## 2. 总体架构

沿用 MeBox 现有分层，全部新增代码集中在：

```
internal/
  model/            # 新增 5 张表，注册进 AllModels() 自动迁移
  repository/       # reader 相关 GORM 封装
  service/reader/   # 规则引擎 + 书源业务（核心新增，预计占全部后端代码 70%）
  handler/          # /api/reader/* 路由组
web/src/
  pages/reader/     # 阅读端独立页面群（懒加载路由）
  components/reader/
```

**基建复用**：`internal/helper/http.go`（浏览器 UA + 代理回退 HTTP 客户端）、`internal/service/runtime_cache.go`（影视侧 JSON/对象缓存）、`internal/service/image_proxy*`（封面与漫画图片代理）、`internal/handler/ws.go` 的 WSHub（搜索进度、追更任务推送，新增 `reader:*` topic）。

**数据流**：书源 JSON 存库 → 搜索/发现时按启用的源并发抓取（errgroup + 信号量限流，超时熔断）→ 结果聚合 → 前端。正文、播放地址、图片列表由服务端组装（含 `nextContentUrl` 翻页合并）后带 TTL 缓存下发；音频流与漫画图片按需经服务端代理补 UA/Referer 头。

## 3. 规则引擎（核心工作）

语义基准：gedoor/legado `app/src/main/java/io/legado/app/model/analyzeRule/` 下的 AnalyzeRule / AnalyzeByJSoup / AnalyzeByJSonPath / AnalyzeByRegex / AnalyzeUrl，逐项对拍测试。

### 3.1 组件与选型

| 组件 | 选型 | 说明 |
|---|---|---|
| HTML/CSS | `PuerkitoBio/goquery` | jsoup 等价物；jsoup 特有语法（class.x / id.x / tag.x / text.x / children / @text / @textNodes / @html / 属性选择）自己包一层 |
| XPath | `antchfx/htmlquery` | 对齐 JsoupXpath 语义 |
| JSONPath | `PaesslerAG/jsonpath`（备选 ohler55/ojg） | Jayway 语义 + 自实现 `\|\|`/`&&` 合并层，选型阶段需验证 |
| 正则 | Go regexp（RE2） | legado 部分源用 Java 正则语法，回退换 `dlclark/regexp2` |
| JS | `dop251/goja` | ES2017+，跑书源内嵌 JS |
| 字符集 | `golang.org/x/text` | GBK/GB18030 解码 |

### 3.2 引擎能力清单

- 规则模式识别：`@css:` / `$.`(JSONPath) / `@XPath:`或`//` / `<js>`与`@js:` / `##` 正则替换段
- 列表组合符 `&&` / `||` / `%%`，变量存取 `@put:{}` / `@get:{}`，内嵌 JS `{{ }}`
- jsoup 分析器、JSONPath 分析器、XPath 分析器、正则分析器，以及混合规则的链式解析（AnalyzeRule 的分段执行语义）
- AnalyzeUrl：`{{key}}`/`{{page}}` 变量、`<js>` 生成 URL、URL 后 `,{...}` 选项（method/body/charset/headers/retry/timeout/type/proxy/js/webView）
- JS 沙箱：goja 运行时 + 执行超时中断 + 禁止直接 IO；上下文注入 `java`、`source`、`book`、`baseUrl`、`result` 等对象
- `java.*` 桥接函数（按书源实际使用频率分批实现）：
  - 网络：ajax / ajaxAll / connect / get / post / head
  - 编解码：base64Decode/Encode（含 URL-safe）、hexDecode、encodeURI/decodeURI、htmlDecode
  - 加解密：md5(16/32)、sha1/sha256、AES/DES/3DES/RSA（CBC/ECB + 常见 padding/key 语义，legado 源里最常见的坑）
  - 字符串与时间：replaceAll/substring/正则族、timeFormat 等
  - 规则回调：`java.getString/getElement` 等，桥回 Go 规则引擎（JS 与规则互相嵌套的关键）

### 3.3 兼容策略

- 引擎按能力分层实现，每个能力配真实书源样本的单测（fixtures 放 `internal/service/reader/testdata/`）
- 提供 CLI 冒烟工具（如 `cmd/reader-smoke`）：对批量导入的公开书源集跑 搜索/详情/目录/正文 全链路，输出成功率报告，作为每个阶段验收依据
- 含 `webView` 选项的源直接判定不兼容并在书源管理页标注

## 4. 数据模型（新增表）

| 表 | 关键字段 |
|---|---|
| book_sources | name, group, type(0/1/2), source_url, json(原文), enabled, custom_order, last_check_at, comment |
| books（书架） | source_url, book_url, name, author, cover_url, intro, kind, type(文本/音频/图片), latest_chapter, total_chapters, last_read_chapter_index, last_read_at |
| book_chapters | book_id, index, title, url, is_volume, update_time |
| read_progress | book_id(唯一), chapter_index, position(滚动/秒/图片序), updated_at |
| replace_rules | name, find, replace, scope, is_regex, enabled, order |

阅读器显示设置（主题/字体/翻页方式）存前端 localStorage，不上服务端。

## 5. API 设计（/api/reader/*）

- 书源：`GET/POST/DELETE /sources`、`POST /sources/import`（URL 或 JSON/base64 文本，自动识别格式与类型）、`PATCH /sources/:id`（启停/排序）
- 搜索：`POST /search {keyword}` → 后台聚合任务，结果经 WS `reader:search` 增量推送；结果可一键加入书架
- 发现：`GET /explore?source=&group=`（解析 exploreUrl 的 `分组名::url` 结构）
- 书架：`GET/POST/DELETE /books`、`GET /books/:id/info`、`GET /books/:id/toc`、`POST /books/:id/refresh`（追更）
- 本地书籍：`POST /local/books`（multipart 上传 TXT/EPUB，导入即入书架）
- 内容：`GET /books/:id/chapters/:idx/content` —— 按书籍类型返回：
  - 文本：`{type:"text", content:"..."}`（服务端已合并 nextContentUrl 翻页、已应用替换规则）
  - 音频：`{type:"audio", tracks:[{url,title}]}`（含代理路径与所需请求头）
  - 图片：`{type:"image", images:[{url, style}]}`（同样经代理）
- 进度：`PUT /books/:id/progress`
- 替换规则：`/replace-rules` CRUD
- 调试：`POST /debug {source_id, rule, url}`（书源调试器后端）
- 图片/流代理：复用现有 image_proxy / stream_proxy 模式，按源配置注入 UA/Referer

## 6. 前端设计

- **首页切换**：`HomePage.tsx` 顶部加分段控件（仿 `LibraryTagBar` tab 模式），「阅读」切到阅读首页；选择持久化（zustand + localStorage）
- **阅读首页**：继续阅读横排 + 书架封面网格 + 搜索入口 + 追更提示
- **页面群**（懒加载，仿 `appRoutes.tsx`）：`/reader`（首页）、`/reader/search`（多源并发搜索 + 实时进度）、`/reader/explore`、`/reader/book/:id`（详情 + 目录 + 换源）、`/reader/sources`（书源管理 + 调试器）
- **三套阅读器**：
  - 文本：滚动 + 分页双模式（CSS 分栏测量分页）、主题（含夜间）、字体/行距/边距、点击翻页区、章节预加载、进度上报
  - 音频：`hls.js`（已是依赖）+ `<audio>` 兜底，播放列表、倍速、记忆进度、锁屏/息屏策略
  - 漫画：上下滚动 / 左右翻页双模式、相邻图片预加载、`imageStyle` 支持

## 7. 阶段划分

| 阶段 | 内容 | 交付物 |
|---|---|---|
| P0 引擎地基 ✅ | 规则引擎核心（四分析器 + 规则拆分/组合/变量）+ AnalyzeUrl v1（GET/POST/charset/headers/变量/页码模式）+ 表结构 + 书源导入/管理 API + 搜索/详情/目录/正文/书架/进度/调试 API | 已完成：`internal/service/reader/rule/`（规则引擎，~2800 行，对齐 AnalyzeRule/AnalyzeByJSoup/AnalyzeByJSonPath/AnalyzeByXPath/AnalyzeByRegex/AnalyzeUrl/RuleAnalyzer）+ 服务层 + `/api/reader/*` 路由 + 单测/端到端测试全绿 |
| P1 文本源全链路 + 首页切换 | 搜索聚合（WS 进度）/详情/目录/正文（nextContentUrl 合并、缓存）+ 前端首页切换、书架、搜索、详情、文本阅读器 v1（阅读器样式仿 legado：9 宫格点击、主题、翻页动画） | 用纯规则型文本源完成「搜书→加入→阅读」全流程 |
| P2 JS 与兼容率爬坡 ✅ | goja 接入 + `java.*` 桥（网络/编解码/摘要/对称加密全家桶/规则回调，函数名对齐 JsExtensions）+ URL 规则 JS（analyzeJs/{{}}/js/bodyJs）+ cookie jar + 用户替换净化规则（含正则超时保护）+ 替换净化页 + 结构化冒烟链路（SmokeChain）+ `cmd/reader-smoke` 冒烟 CLI | JS 源可用；冒烟 CLI 跑公开书源集出各阶段通过率报告 |
| P3 音频源 ✅ | 正文按音频类型返回播放列表（绝对化）+ `/api/reader/media` 签名媒体代理（HMAC 防滥用、Range 透传支持拖动、m3u8 分片/密钥地址重写）+ 听书面板（hls.js 播 m3u8、直链 `<audio>`、上一章/-15s/播放暂停/+15s/下一章、章节选择抽屉、定时关闭 0–180 分钟、倍速 0.5–3.0、跳过片头片尾按书持久化 `PUT /api/reader/books/:id/audio-config`、进度按秒记忆、播完/片尾到点自动下一章） | 音频源可听 |
| P4 漫画/图片源 ✅ | 图片列表绝对化 + 经签名代理（带书源 Referer 防盗链头）+ 漫画阅读器（上下滚动/左右单页双模式、图片懒加载与加载失败占位、点击分区翻页/呼菜单、菜单进度条按图片序号、进度按图片序号记忆、下一章预取）+ imageStyle 透传 | 漫画源可看 |
| P4.5 书源登录 ✅ | `jsLib` 一次装载（对应 legado SharedJsScope）+ `source.*` 会话方法（getVariable/setVariable/getLoginInfo/putLoginInfo/getLoginHeader/putLoginHeader/get/put）+ `cookie.*`（getCookie/setCookie/replaceCookie/removeCookie/getKey，按 eTLD+1 隔离）+ 请求自动携带 Cookie 与 loginHeader + `loginUrl`/`loginUi`（解析表单 → 按钮 action 拼在 loginUrl 后执行 → result 为表单值）+ `loginCheckJs`（会话失效自动重登/重取）+ 源变量落库 + 登录信息/Cookie 加密存储 + 服务端 toast/startBrowser 回传前端 + 登录面板与变量编辑器 | 登录类书源可登录、可留存登录态 |
| P4.6 本地书籍 ✅ | `POST /api/reader/local/books` 上传 TXT / EPUB（上限 64MB）→ 落盘 `data/reader/local/<bookID>.<txt\|epub>` + 解析目录入 `reader_chapters` + 落库为 `origin=""`、`is_local=true` 的书架条目；TXT 自动识别 BOM/UTF-8/GBK/Big5/UTF-16 并按 legado 默认 TXT 目录规则切章（RE2 无 lookbehind，改行首锚定 + 句子启发式过滤），章定位信息存字节区间 `start:end`，读章只读该区间；EPUB 走 container.xml → OPF spine，标题优先取 NCX/NAV，正文去标签与实体；移出书架同步删落盘文件；同名重复导入覆盖更新并尽量保留进度 | 本地书可上传、可读、可删 |
| P4.10 书源按用户独立 ✅ | 书源从全局唯一改为按用户独立：`ReaderBookSource` 增加 `user_id`，唯一键改为 `(user_id, source_url)`；导入/列表/启停/删除/搜索全部按当前用户限定；老库迁移自动删旧唯一索引、按「谁书架上有这本书」回填归属并给多引用用户复制副本、清理软删历史行；删除改为物理删除（软删会占住唯一键导致「删掉再导入」失败）；正文缓存键随之改为「书源行 ID + 书本地址」，不同用户改过规则的同名书源不再共享缓存 | 用户的启停/规则/变量/删除互不影响 |
| P4.7 正文持久缓存 ✅ | 服务端正文持久缓存（`reader_content_cache.go`：书源侧产物 + 索引落库 + 内容落盘 `cache_dir/reader-content/`、TTL/容量 LRU、目录刷新 remap、换源/移出书架清理、读穿透 singleflight）+ 批量取正文接口 `POST /api/reader/books/:id/content-batch`（对应 CacheBook 协议）+ 前端窗口预取（默认 3 章） | 慢源连续翻章不再每章等待；重复阅读零网络 |
| P4.8 图片/字体兼容 ✅ | `coverDecodeJs`（封面解密代理，含搜索结果未入库场景）/ `ruleContent.imageDecode`（正文图片解密）+ 图片 URL 尾部 `,{headers}` options 逐图应用（options 优先于书源 header 与 Cookie）+ `java.queryTTF` / `queryBase64TTF` / `replaceFont`（sfnt cmap 0/4/6、loca 短/长、glyf 简单+复合字形） | 加密图与字体混淆正文可读 |
| P4.9 规则稳定性 ✅ | `@put` 回写书源变量（修复跨请求丢失）；`book.getVariable/putVariable` 真正注入书籍上下文并落库（对应 Book.upVariable）；全部书源 `regexp2` 统一 3s 匹配超时；解压输出 32MB 上限；非 UTF-8 TXT 托管副本按 UTF-8 落盘（外部引用读时解码）；换源单事务；JS `cache` 按书源命名空间隔离并落盘；HLS 双端识别（服务端 `#EXTM3U` 嗅探 + `hls` 标志下发） | 与阅读 App 行为对齐，坏源不再拖死服务 |
| P5 体验完善 | 换源（ChangeBookSourceDialog 四档排序）、追更（定时刷新目录 + 缓存清理）、发现页（exploreUrl 标签条）、阅读器高级设置（页眉页脚提示、点击区域自定义）、书源编辑器六 Tab、备份导出 | 完整体验 |

### 实施记录：书源会话状态仍是跨用户共享（下一步）

书源本身已按用户独立，但 `ReaderSourceState`（Cookie / 登录信息 / loginHeader / 源变量）
仍以 `source_url` 为唯一键、跨用户共享一份。两个用户导入同一书源时：

- 一方登录后，另一方复用同一份 Cookie；一方的登出会清掉另一方的登录态。

彻底隔离需要给状态表也加 `user_id`（或按书源行 ID 组织），涉及登录面板与变量读写
的一批调用点，留作单独一轮处理。

### 实施记录：关于「宽松 @put 触发 panic」的复核

前期评估曾报告 `sourcerule.go` 的宽松 `@put` 回退正则含 RE2 不支持的反向引用、可能 panic。
逐行复核后该结论**不成立**：`splitPutRule` 的下标全部由固定捕获分组数保证。
真实缺陷是另外两个并已修复：

- `AnalyzeRule.Put` 没有 `sourcePutter` 分支，`@put` 只写进一次性的 `bookVars`，跨请求丢失；
- `applyBookContext` 的三处调用点都在传 `book=nil`，`book.getVariable/putVariable` 与 `book.name` 长期是死代码。

P0–P2 是主体（约全部工作量 60–70%），P3/P4 相对独立可并行。

## 8. 风险与对策

- **书源质量参差**：单源解析全程超时与重试上限；正文翻页循环设页数上限防死循环
- **goja 性能**：复杂 JS 源解析慢 → 正文/目录强缓存、单源并发限 1、全局执行预算
- **Java 正则/JSoup 细节差异**：以对拍测试驱动修正，regex 备选 regexp2
- **热源防盗链**：图片与音频一律走带请求头的代理
- **合规**：不内置任何书源，全部由用户自行导入，管理页附免责说明

## 9. 参考

- **唯一语义与样式基准**：https://github.com/refgd/legado （规则引擎 analyzeRule 源码 + UI 布局/交互，见 docs/reader-ui-spec.md）
- 书源规则教程：https://mgz0227.github.io/The-tutorial-of-Legado/
