# legado UI/交互仿制规格（React 18 + Tailwind Web 版实现依据）

> 来源：对 `refgd/legado`（克隆于 `C:\MyProject\_ref\legado`）源码的调研。
> 路径缩写：`<src>` = `app/src/main/java/io/legado/app/`，布局在 `app/src/main/res/layout/`。

## 1. 首页/书架

文件：`<src>ui/main/bookshelf/`（BookshelfFragment、BooksAdapterGrid/List）、`fragment_bookshelf1/2.xml`、`item_bookshelf_grid*.xml`、`item_bookshelf_list*.xml`。主界面底部 BottomNavigation（书架/发现/我的）+ 右上悬浮搜索按钮。

**布局结构（从上到下）**
- style1（标签页风格）：标题行（书架标题 + 下拉箭头，点开分组切换菜单）→ 搜索按钮 + 更多按钮 → 一级分组 tab 条（tab 长按可删分组）→ 次级分组扩展标签 → ViewPager（每 tab 一个书籍列表/网格）。
- style2（单 RecyclerView 混排）：title_bar → SwipeRefresh → 书籍列表（根分组时「分组网格项 + 书籍项」混排，进入分组只显示书籍）→ 空态文案。
- 网格项：封面 + 本地角标 + 未读数角标 + 加载中 + 书名 + 长按遮罩。
- 列表项：封面 + 本地图标 + 有更新标记 + 未读数 + 书名 + 作者 + 最后更新时间 + 当前读到（章节名）+ 最新章节（章节名）+ 长按遮罩。

**交互逻辑**
- 内置分组 id：全部(IdRoot=-100)、本地(IdLocal=-2)、未分组(IdUngrouped=-4)，其余用户分组。
- 单击书 → 直接进入阅读；单击分组 → 进入该分组。
- 长按书 → 跳转书籍详情页；长按分组 → 重命名/删除对话框；style1 长按 tab 删除分组。
- 右上菜单：搜索、Wi-Fi 传书、刷新目录、书架布局切换、分组管理、导出/导入书架、下载离线、本地导入、网址添加、日志。

## 2. 阅读界面（重点）

文件：`<src>ui/book/read/`（ReadBookActivity、ReadMenu、SearchMenu、MangaMenu、config/*Dialog）、`ui/book/read/page/`（ReadView、PageView、ContentTextView、ChapterProvider）。

**布局结构（单 FrameLayout 栈）**：read_view 正文 → 文字选择光标 → read_menu 主菜单 → search_menu 全文搜索 → 朗读回原文浮条。

**正文渲染**
- ReadView 内含 3 个 PageView（prev/cur/next 缓存），ContentTextView 纯 Canvas 自绘：按字号/行距/段距/缩进/两端对齐排版，Web 版用 CSS multi-column 或 JS 分页等效实现。
- 翻页动画（0–5）：0 覆盖、1 平移、2 仿真、3 上下滚动、4 无、5 平移覆盖；速度可调（默认 300ms）。
- 滚动模式支持背景跟随、自动翻页（定时滚动/定时翻页）。
- 图片/漫画书切换独立配置集 + MangaMenu：顶栏 + 底部胶囊（上一页/页码进度条/下一页），支持横滚、缩放。

**点击区域（9 宫格，边缘留 pageTouchClick px）**
- 区块：左上/中上/右上/左中/中/右中/左下/中下/右下。
- 默认：左列与上中=上一页；右列与下中=下一页；正中=呼出菜单。
- 全部可配动作：0 呼出菜单、1 下一页、2 上一页、3 下一章、4 上一章、7 书签、10 目录、11 搜索、12 同步进度等。

**主菜单 ReadMenu（自上而下）**
- 半透明遮罩（点击收起）。
- 顶部 title_bar：返回 + 书名（点击进详情）+ 章节名/章节链接（点击打开原页面）+ 书源按钮（弹菜单：编辑书源/禁用书源等）。
- 底部 bottom_menu（圆角面板）：上一章 | 进度条 | 下一章 → 亮度条 + 自动亮度 → 快捷按钮行（搜索、自动翻页、替换规则、夜间/日间切换）→ 动作面板行（目录、朗读、界面、设置）。
- 菜单配色可跟随页面背景或用主题底色，透明度 35–100。

**设置面板**
- 「界面」三 Tab：文本（字体文件、字重、字号 5–50、字距、行距 0–20、段距、缩进、下划线、阴影）；样式（白天/夜间/E-Ink 三态主题：文字色、背景色/图、强调色、菜单底色、菜单透明度、背景透明度，可恢复/管理/分享）；页面（6 种翻页动画、状态栏深浅、滚动背景跟随、四边距、页眉页脚提示、简繁转换）。
- 「设置」：屏幕方向、保持亮屏、隐藏状态栏、两端/底部对齐、竖排、双页、进度条行为（按页/按章）、音量键/滚轮翻页、点击区域配置、动画速度、自动换源等。
- 页眉页脚提示项：无/章节名/时间/电量/电量百分比/页码/总进度/页码+总页/时间+电量/书名；页眉默认 左=时间/右=电量，页脚默认 左=章节名/右=页码/总页。

**内置主题与配色（assets/defaultData/readConfig.json，6 套 + 自定义）**

| 名称 | 背景(日/夜) | 文字(日/夜) | 强调(日/夜) | 字号 |
|---|---|---|---|---|
| 微信读书 | #FFC0EDC6 / #000000 | #FF0B0B0B / #ADADAD | #E53935 / #FE4D55 | 24 |
| 预设1 | #FFFFFF / #000000 | #000000 / #FFFFFF | #E53935 / #FE4D55 | 20 |
| 预设2(羊皮纸) | #DDC090 / #3C3F43 | #3E3422 / #DCDFE1 | #834E00 / #FE4D55 | 20 |
| 预设3(护眼绿) | #C2D8AA / #3C3F43 | #596C44 / #88C16F | #E53935 / #FE4D55 | 20 |
| 预设4(粉紫) | #DBB8E2 / #3C3F43 | #68516C / #F6AEAE | #801314 / #90BFF5 | 20 |
| 预设5(淡蓝) | #ABCEE0 / #3C3F43 | #3D4C54 / #90BFF5 | #E53935 / #FE4D55 | 20 |

默认排版：textSize 20、letterSpacing 0.1、lineSpacingExtra 12、paragraphSpacing 2、缩进「　　」、padding 上下6/左右16、页脚线 true。未选样式时默认：bg #EEEEEE / 夜 #000000 / E-Ink #FFFFFF，文字 #3E3D3B / 夜 #ADADAD，强调 #E53935 / 夜 #FE4D55。

**音频书播放条（ReadAloudDialog 底部弹层）**
- transport 行：上一章 | 上一个/播放暂停/停止/下一个 | 下一章。
- 定时面板：定时关闭 + 进度条；TTS 语速面板（跟随系统 + 减/加 + 语速条）。
- 底部动作行：目录、主菜单、后台播放、设置。
- 音频播放参数存 Book.readConfig：playMode(0 顺序)、playSpeed(1.0)、openCredits/closeCredits(片头片尾章数)。

## 3. 搜索

文件：`<src>ui/book/search/` + `<src>model/webBook/SearchModel.kt`。

- 布局：顶部 SearchView + 转圈进度条 → 结果流 → 书架命中提示卡 → 搜索历史 chips + 清空 → 右下 开始/停止 悬浮按钮。
- 结果项：封面、在书架角标、书源数角标（可换源数）、书名、作者、分类标签、最新章节、简介。
- 多源并发：所有启用书源（按搜索范围过滤），固定线程池并发（全局设置 threadCount），单源 30s 超时。
- 实时结果流：每源返回即刷新列表；结束后 onSearchFinish(isEmpty, hasMore)（任一源有结果即可翻页，searchPage++）。
- 聚合去重 mergeItems 四档：书名或作者等于关键词 > kind 含关键词 > 书名/作者含关键词 > 其他；同名同作者合并为一项并 addOrigin（角标显示可换源数）；档内按书源数降序。
- 顶部菜单：精准搜索开关、搜索范围（分组/全选）、书源管理、日志。点结果 → 换源对话框/直接打开。

## 4. 发现

`<src>ui/main/explore/` + `ui/book/explore/`。布局：标题栏 → SwipeRefresh → 源选择行（当前源名 + 下拉、源内搜索、标签筛选、更多）→ 二级筛选条 → 分类标签条（横滑 chip，可展开）→ 书籍网格（封面+书名）→ 兜底列表。数据来自启用书源的 exploreUrl + ruleExplore；切源刷新标签；点标签拼 URL 加载、分页加载更多。

## 5. 书籍详情

`<src>ui/book/info/BookInfoActivity.kt`。布局：模糊封面背景 → 标题栏（返回/刷新/菜单）→ 下拉刷新 → 滚动区：封面 + 书名 + 作者 + 最新章节（点击刷新目录）+ 阅读时长 + 分类标签 + 分组行 + 书源行（换源）+ 目录入口行 → 双 Tab（简介 / 目录预览，可全屏、点章节跳读）→ 底部动作区：tv_shelf 加入/移出书架（描边按钮）+ tv_read 开始阅读/继续阅读（实心主色按钮）。菜单含置顶、去书源网页、登录、删除（勾选删缓存）、分享。

## 6. 目录页

`<src>ui/book/toc/`。布局：标题栏（返回 + 书名 + SearchView 搜章节）→ Tab（章节/书签）→ 章节 RecyclerView + 底部信息条（当前位置 + 跳顶/跳底）。菜单：倒序开关、使用净化替换、加载字数、长章分卷合并、TXT 目录规则。倒序即列表反转；卷名行可折叠；已读章节变色；点击章节回传 index 跳读。

## 7. 书源管理

- **主列表**：标题栏（SearchView 过滤 + 菜单）→ RecyclerView（item：域名、复选、启停 Switch、编辑、更多菜单、发现、快速调试、响应耗时条）→ 多选操作栏。菜单：排序（手动/自动/名称/URL/更新时间/响应时间/启用）、分组筛选（启用/禁用/需登录/无分组/启用发现/禁用发现）、添加书源（本地/网络/二维码）。
- **编辑**：标题栏（保存/调试入口）→ 类型下拉 + 六个开关（启用/启用发现/CookieJar/段评/事件监听/自定义按钮）→ 六个 Tab：基本/搜索/发现/详情/目录/正文，每 Tab 为「标签 + 输入框 + 帮助弹层」表单。
- **调试**：顶部 SearchView 输入关键词 → 串行执行 搜索→详情→目录→正文，日志逐条流式显示；可查看各阶段原始 HTML、切换探索分类调试。

## 8. 替换规则

- 列表页：标题栏（搜索 + 菜单：分组管理、启停筛选、添加、导入）→ RecyclerView（名称、分组、内容摘要、启停 Switch）→ 多选操作栏。
- 编辑页字段：规则名 → 分组 → 替换规则（+ 正则开关 + 帮助）→ 替换为 → 作用于标题 / 作用于正文 → 作用范围(书名) → 排除范围 → 超时毫秒（order 隐含）。

## 9. 数据实体字段全集

**BookSource.kt**（rule* 为内嵌对象）

| 字段 | 类型 | 默认值 |
|---|---|---|
| bookSourceUrl | String | "" |
| bookSourceName | String | "" |
| bookSourceGroup | String? | null |
| bookSourceType | Int (0文本/1音频/2图片/3文件/4视频) | 0 |
| bookUrlPattern | String? | null |
| customOrder | Int | 0 |
| enabled | Boolean | true |
| jsLib | String? | null |
| enabledExplore | Boolean | true |
| enabledCookieJar | Boolean? | true |
| concurrentRate | String? | null |
| header | String? | null |
| loginUrl / loginUi / loginCheckJs / coverDecodeJs | String? | null |
| bookSourceComment | String? | null |
| variableComment | String? | null |
| lastUpdateTime | Long | 0 |
| respondTime | Long | 180000 |
| weight | Int | 0 |
| exploreUrl / exploreScreen | String? | null |
| ruleExplore / ruleSearch / ruleBookInfo / ruleToc / ruleContent / ruleReview | 对象 | null |
| eventListener / customButton | Boolean | false |

**Book.kt**

| 字段 | 类型 | 默认值 |
|---|---|---|
| bookUrl (PK) / tocUrl | String | "" / "" |
| origin / originName | String | localTag / "" |
| name / author | String | "" / "" |
| kind / customTag / coverUrl / customCoverUrl / intro / customIntro / charset | String? | null |
| type | Int (BookType) | text |
| group | Long | 0 |
| latestChapterTitle | String? | null |
| latestChapterTime / lastCheckTime | Long | now |
| lastCheckCount / totalChapterNum | Int | 0 |
| durChapterTitle | String? | null |
| durChapterIndex / durVolumeIndex / chapterInVolumeIndex / durChapterPos | Int | 0 |
| durChapterTime | Long | now |
| wordCount | String? | null |
| canUpdate | Boolean | true |
| order / originOrder | Int | 0 |
| variable | String? | null |
| readConfig（ReadConfig 内嵌） | reverseToc、pageAnim、reSegment、imageStyle、useReplaceRule、delTag、ttsEngine、splitLongChapter=true、readSimulating、startDate、startChapter、dailyChapters=3、openCredits=0、closeCredits=0、playMode=0、playSpeed=1.0、manga* 系列、mangaPageAnim | |

**BookChapter.kt**

| 字段 | 类型 | 默认值 |
|---|---|---|
| url / title | String | "" / "" |
| isVolume | Boolean | false |
| baseUrl / bookUrl | String | "" / "" |
| index | Int | 0 |
| isVip / isPay | Boolean | false |
| resourceUrl / tag / wordCount | String? | null |
| start / end | Long? | null |
| startFragmentId / endFragmentId / variable / imgUrl | String? | null |

**ReplaceRule.kt**

| 字段 | 类型 | 默认值 |
|---|---|---|
| id | Long | now |
| name | String | "" |
| group | String? | null |
| pattern | String | "" |
| replacement | String | "" |
| scope | String? | null |
| scopeTitle | Boolean | false |
| scopeContent | Boolean | true |
| excludeScope | String? | null |
| isEnabled | Boolean | true |
| isRegex | Boolean | true |
| timeoutMillisecond | Long | 3000 |
| order | Int | Int.MIN_VALUE |

## 10. 主题与视觉

- 全局：主背景 `#F7F7FA`、主文字 `#DE000000`、品牌色 `#FFF6FBF8`（浅绿白调）。
- 界面主题由 ThemeConfig（themes.json）控制 6 套；阅读菜单/底栏用 bottomBackground（跟随所选界面主题），沉浸模式（readBarStyleFollowPage）直接取页面背景/文字色。
- 强调色 accentColor 用于选中态、光标、Tab 选中；E-Ink 模式菜单改描边。
- 视觉风格：大圆角面板、1dp 描边、玻璃拟态半透明条、图标+文字标签的动作面板。

## Web 版实现要点

- 正文分页仿 ReadView 三页缓存 + 9 宫格点击映射。
- 主题模型照抄 ReadBookConfig.Config（含日/夜/E-Ink 三态字段），内置 6 套主题色值直接使用上表。
- 搜索并发用 Promise 池 + 流式合并，合并逻辑照抄 mergeItems 四档排序。
- 书源/替换编辑器按第 7/8 节 Tab/字段一一对应。
