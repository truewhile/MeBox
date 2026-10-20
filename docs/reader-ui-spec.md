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

**MeBox 的「影视 / 阅读」首页模式**（`stores/readerSettings.homeMode` + 顶栏 `LayoutReaderModeToggle`）
- 切换入口在**顶栏**，位于搜索框与账号菜单之间，**只显示图标不显示文字**：影视模式显示场记板图标，阅读模式显示书图标（并带品牌色高亮），点一下切到另一个模块、图标随之变化。悬停提示写明「当前是 X 模式，点击切换到 Y」，不在首页时点击会先跳回 `/`。
- 两种模式共用一个首页路由 `/`：影视模式渲染媒体首页，阅读模式渲染书架（`ReaderHomeContent embedded`）。首页内容区不再放分段式「影视 / 阅读」开关（原 `ReaderModeSwitch` 仅留给独立布局的 `/reader` 首页兜底）。
- 切到阅读模式时，顶部搜索框换成**书搜索**（`LayoutHeaderBookSearch`，占位「搜索书籍…」）：聚焦即下拉书架（本地即时过滤书名/作者，点条目进阅读器），回车优先打开书架首条；下拉底部固定一行「在书源中搜索「xxx」」，带 `?key=` 跳到 `/reader/search` 由多源聚合搜索页自动开搜。账号/主题菜单照常显示。
- 阅读模式下移动端底部导航（首页/媒体库/收藏/列表/更多）隐藏，避免影视导航混进书架。
- `/reader/*` 本来就是独立全屏布局（不套影视 Layout），不受影响。

**MeBox 书架已实现的布局与信息（`ReaderHomeContent.tsx` + `BookshelfCard.tsx` + `BookshelfSettingsDialog.tsx`）**
- 三套布局，设置在顶栏「书架设置」弹窗里（对应 legado dialog_bookshelf_config），存 `stores/readerSettings.ts`：
  - 网格：封面宫格，列数 2–6 或「自适应」；窄屏自动降到 2–3 列兜底。卡片含封面 + 本地角标 + 未读章数徽标 + 双行书名 + 详情/移出按钮。
  - 列表：66×90 封面 + 书名 + 作者 + 更新时间 + 读到（当前章）+ 最新章节；对应 legado `item_bookshelf_list.xml` 的四行信息。
  - 紧凑列表：48×64 小封面 + 书名 +「作者 · 读到」，一屏放更多书（对应 legado `layout_list_compact`）。
- 排序六档，对齐 legado `AppConfig.getBookSortByGroupId`：最近阅读（dur_chapter_time，默认）/ 最近更新（latest_chapter_time）/ 综合 / 按书名 / 按作者 / 手动顺序（order）。顶栏有快捷排序下拉，弹窗里也可选。
- 显示项开关：未读章数徽标、更新时间。
- 「更新目录」（对应 legado `menu_update_toc`）：`POST /reader/shelf/refresh-toc` 并发重抓书架内网络书籍的目录，覆盖章节缓存；末章标题变化时刷新 `latest_chapter_time`，用于「最近更新」排序与更新时间展示。本地书籍与无书源信息的书跳过。返回 `total/updated/unchanged/failed` 四档——抓到但章数没变（完结书）记 `unchanged`「已是最新」，只有抓取或写入真的失败才记 `failed`，避免把正常结果报成「更新失败」；失败会打 Warn 日志（含书名与书源）。
- 未实现（与 legado 的差距）：书籍二级分组网格（进入分组后的封面网格）、导出/导入书架、离线下载管理界面（服务端批量缓存接口与窗口预取已可用，见下）。
- 章节预取：阅读页对当前章之后默认 3 章走 `POST /reader/books/:id/content-batch` 批量抓取（服务端并发 + 持久缓存），翻到后续章直接命中；单章串行预取在高延迟书源上赶不上连续翻章。
- 书源按用户独立：书源管理（列表/导入/启停/删除）与搜索都只作用于当前用户自己的书源。同一份书源不同用户可以各导入一份，各自启停、排序、改规则与变量，互不可见；Web 端无需额外交互，接口按登录用户自动隔离。

**书架分组（`/reader/book-groups`，仿影视模块的媒体库标签）**
- 按用户存服务端：`reader_book_groups` 表每个用户一条，`groups` 列是 `[{name, book_ids}]` 的 JSON。结构、语义与影视的媒体库标签（`User.LibraryTags`）同构——组名 → 成员 ID、整份替换、一个成员只归一个组、组内顺序即展示顺序。
- 接口：`GET /reader/book-groups`（没有分组返回空数组）、`PUT /reader/book-groups`（整份替换）。服务端落库前做两条需要「知道书是否存在」的收敛：只保留该用户书架上的书（书移出书架后分组里不留死 ID）、一本书只归一个组（越靠前的分组优先）。空分组保留，方便先建组再放书。
- 与影视的差异：书架有两个内置页签「全部」「未分组」（未分组是「没有被任何分组认领」的视图，不持久化，只有存在分组时才出现，且固定在分组栏最后，避免挤开用户自己拖拽排好的分组顺序）；书籍不进管理弹窗逐个分配，而是在卡片上用「分组」按钮指定（书架可能有上千本，列出来既慢又难找）。
- 前端：`utils/readerBookGroups.ts`（纯逻辑）+ `hooks/useBookGroups.ts`（乐观更新、串行提交、失败回滚，接口不可用时退回 localStorage，照搬 `useLibraryTags`）+ `components/BookGroupBar.tsx`（分组栏）+ `ManageBookGroupsDialogView.tsx`（新建/重命名/删除/拖拽排序）+ `BookGroupPickerDialog.tsx`（移到分组）。

**阅读器偏好按账号同步（跨设备）**
- 主题 / 夜间模式 / 排版（字号、行距、段距）/ 翻页或滚动模式 / 听书倍速与定时 / 书架布局·排序·显示项，原先只存浏览器 localStorage（设备级，换设备就丢），现在按用户落库到 `reader_profiles`（`model.ReaderProfile`，`user_id` 唯一索引），接口 `GET|PUT /reader/profile`。
- 服务端是权威来源，localStorage（zustand persist 的 `mebox-reader-settings`）退化成首屏缓存：登录后 `hydrateReaderSettings()` 拉一次覆盖本地，之后本地改动防抖 600ms 回写（`utils/readerSettingsSync.ts`）。
- 账号还没有偏好记录时 `GET` 返回 `null`，前端把本地现值推上去「播种」，升级前在本机调好的设置不会被重置。
- 服务端只做存储 + 范围收敛（数值夹区间、枚举未知回落默认、主题标识限长），不做语义解释；前端另有本地 clamp，两处范围需同步（见 `service/reader/profile.go` 顶部注释）。
- 首页的「影视 / 阅读」模式（homeMode）是设备级偏好，故意不参与同步。

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

**MeBox 文本阅读器已实现的排版/菜单细节**（`ReaderViewPage.tsx`）
- 正文留边：左右 16px、上下 8px（对齐 legado 默认左右16/上下6），分页列宽按留边后的视口宽计算，正文不贴屏幕边。
- 菜单打开时正文整体下移一个顶栏高度（用 `transform`，不改视口高度、不触发重新分页），顶栏不再压住开头 1–2 行。
- 界面面板的字号/行距/段距三个调节组用 `flex-wrap`，窄屏自动折行，不会把「段距」挤出屏幕。
- 点击分区：翻页模式渲染一层 `absolute inset-0` 的三分区按钮（左 30% 上一页 / 中 40% 呼出菜单 / 右 30% 下一页）；**滚动模式不能渲染这层覆盖层**——它不是滚动容器的子节点，手机上手指落在覆盖层按钮上时浏览器找不到可滚动的祖先，纵向滑动完全失效、只剩点左右能翻屏。滚动模式改为在滚动容器自身的 `onClick` 上按 x 坐标分区（`handleZoneTap`），手指拖动不会产生 click，原生纵向滚动照常。
- 手机横向滑动翻页（`hooks/useHorizontalSwipe.ts`）：翻页模式（文本/漫画）在三分区覆盖层上再接一层触摸手势——**从左往右滑=上一页、从右往左滑=下一页**，并且**跟手**：位移超过 12px 且横向明显大于纵向（1.2 倍）之后，内容就跟着手指走，松手再按「位移 ≥45px」决定翻页还是回弹（跟手手势不再看手势时长，慢慢拖过去同样是拖动）。跟手手势一律吞掉随之而来的 click，否则短距离拖动会「回弹 + 再翻一页」；没走跟手的手势仍按原来的「≥45px + 时长 ≤800ms」判定一次滑动，短距离当点击交给分区按钮。`consumeSwipe()` 让三个分区按钮的 `onClick` 先判断这次 click 是否已被手势吃掉，标记在每次 `touchstart` 重置。
- 跟手拖拽平移的是两个不同的元素。文本：正文列外面单挂一层「跟手层」（`pageDragRef`），分页平移由 `applyTextPageTransform` 命令式写在里层（`contentRef`），跟手层只管手指位移；松手时两层同时动（跟手位移归零 + 页码变化），合起来就是一次连续滑动。正文用 `ReaderTextPageBody` memo，避免长章每次 `setPage` 重渲染整棵段落树（手机会先卡一下再翻页）。漫画：平移 `ReaderComic` 的舞台，舞台左右各摆一页（见下一条）。
- 漫画单页翻页的跟手轨道与预取（`ReaderComic` 的 `draggable` 分支）：翻页模式且没有并排铺开两页时，当前页左右各摆一页，手指横滑整条舞台跟着走。单元格按图片序号做 key、位置用 `left` 百分比铺开（不是固定三个槽位换内容），所以翻页时变的只是 `left`，`<img>` 的 src 不变，浏览器不会重新解码、也不会先闪一下转圈。松手要翻页时先把新页瞬移到手指离开的位置（这一步必须关掉过渡，否则会先反着滑一段），读一次布局把这一帧定成过渡起点，再打开过渡滑回 0；同时页码 +1/-1，单元格的 `left` 在同一次提交里跟着换，两边相抵，屏幕上看不出切换。**两侧那两页同时就是下一页/上一页的预取**（带 `eager`：它们被 `overflow-hidden` 裁在屏幕外，懒加载会认为「离视口还远」而不去取，横滑过去就会看到转圈）——这正是「手机上翻一页要等一秒、电脑上（双页开着会顺带往后探测 8 张）不卡」的根因。
- 滚动模式的滚轮（`useSmoothWheelScroll`）：把滚轮格数累加成一个目标位置，再逐帧向它逼近，滚动连续、松手后自己滑行一段，仿手机上下滑动，而不是浏览器整格跳变。只在文本/漫画的上下滚动模式接管；翻页模式仍走下面的翻页监听器。按真实帧间隔换算逼近比例，120Hz 屏上速度不会翻倍；`ctrl/cmd+滚轮` 保留缩放；手指按下（滑动、拖滚动条、点分区）立刻停掉动画把控制权交还用户。
- 漫画双页铺开（`hooks/useComicSpreads.ts` + `utils/comicSpread.ts`，legado 无对应项，是桌面端补充）：窗口宽度 ≥900px 且漫画处于翻页模式时，一屏并排显示两页（`settings.comicDoublePage` 为设备级偏好，默认开，可在菜单里关；窗口不够宽时自动退回单页）。分组规则同 Tachiyomi 的 dual page——宽度明显大于高度（比值 ≥1.1）的图视为「跨页宽图」独占一屏，其余连续的竖版页两两配对；列表接口只给 URL，尺寸靠 `new Image()` 探测（`<img>` 渲染时顺带回填），只探测当前页往后 8 张的窗口，避免整章预载。进度仍按图片序号记，翻页时换算成「第几屏」，所以关掉再开双页进度不会错位。菜单动作行改为 `flex` 等分，容纳「双页/单页」入口（`grid-cols-N` 得写死列数字面量，多一个按钮无法复用）。
- 漫画图片显示尺寸（`utils/comicImageFit.ts`，仅上下滚动模式）：legado 的漫画阅读有「缩放」，Web 版原先只有一种写死的宽度（正文列封顶 900px），桌面端放不大也缩不小，所以补上一组档位，在界面面板的「图片尺寸」一行切换。`default` 就是老样子（900px 居中），`width` 适应宽度（正文列不再封顶、铺满窗口），`height` 适应高度（一屏一页），`long` 适应长边（整页完整可见），`original` 原图（原始像素 1:1，超出部分横向滚动）。`default`/`width` 由外层列宽决定，其余三个由 `<img>` 自身约束，**所以后三种必须放开 900px 列宽上限**，否则会被列再压回去。`height`/`long` 要按「一屏多高」换算，所以量的是滚动容器本身而不是 `window.innerHeight`（正文区是 root 里的 `flex-1`，另挂上下条之后就不等于窗口高度了），并用 `ResizeObserver` 跟随；只在启用了自尺寸档位时才挂观察器，默认档位不白白重渲染整章。`settings.comicImageFit` 是设备级偏好（不同屏幕和源分辨率合适的尺寸不同），和 `comicDoublePage` 一样只存本机、不参与账号同步。翻页模式是整页缩放进视口，本来就不存在「太大/太小」，档位对它不生效、也不显示入口。`ctrl/cmd+滚轮` 依然留给浏览器缩放，本功能只用档位做粗调。
- **文本型漫画源按图片下发**（`internal/service/reader` 的 `imageMarkersOnly`）：拷贝漫画这类书源 `bookSourceType=0`（文本），正文规则直接给 `<img>`，服务端把整行标签转成 `[img]<签名地址>` 标记行后类型仍是 `text`，前端就会走文本阅读器——分页把每张图当成一列，一屏只看得到一张，桌面端也谈不上双页铺开。现在 `GetContentForBook` / `LocalChapterContent` 在标记化之后判断「非空行全部是 `[img]` 标记」，是则把 `type` 改成 `image` 并给出 `images`，交由漫画阅读器渲染（整页缩放、双页铺开、进度按图片序号）。判定只认「全是标记」：混了正文的章节一律保持 `text`，孤立的 HTML 标签行（`<div>`/`</div>`）与空行当作无内容忽略，绝不吞掉可读文字。
- 鼠标滚轮翻页（仅翻页模式）：向上滚=上一页，向下滚=下一页；菜单打开时也不翻页。鼠标滚轮一格一页（间隔至少 220ms，与翻页动画对齐），触控板小步长累计到阈值翻一页且一次手势只翻一页（避免惯性连翻）。`ctrl/cmd+滚轮` 保留浏览器缩放。
- **浏览器全屏（沉浸式）**：阅读页本身已经是 `fixed inset-0` 的独立布局（`/reader/*` 不套影视 Layout），所以屏幕上的留白只剩浏览器自带的地址栏/工具栏。底部动作行加「全屏 / 还原」，对 `readerRef` 调标准 Fullscreen API（`requestFullscreen` / `exitFullscreen`），进全屏前先收起菜单。按钮只在 `document.fullscreenEnabled === true` 时出现——**iOS 上的 Safari 至今不支持元素全屏**（只有 `<video>` 能全屏），所以那里不显示按钮，而不是显示一个点了没反应的死按钮；iPhone 想彻底去掉浏览器界面只能「添加到主屏幕」。离开阅读页时卸载 effect 主动 `exitFullscreen`，否则返回书架后浏览器仍停在全屏、整站都被罩住。全屏状态由 `fullscreenchange` 事件回写，不做乐观更新（用户按 Esc / 系统手势退出时也要跟着变）。
- **手机端双击中间区域进出全屏**（`hooks/useDoubleTap.ts` + `hooks/useIsTouchDevice.ts`）：中间区域单击本来就是呼出菜单，现在双击再切一次全屏，两个动作在同一块屏幕上，得先等一小会儿看有没有第二下。`useDoubleTap` 把这段窗口做成纯函数 `decideDoubleTap`（第一下进 `tap` 窗口等第二下；第二下到达取消单击、改判双击），**双击回调在第二次点击的事件里同步执行**——浏览器只认用户手势上下文里的 `requestFullscreen`，挪进定时器再调会被直接拒绝。只在「触摸设备（`(hover: none)`）+ 支持元素全屏」时启用：桌面端根本不装这套，单击立刻生效，点中间呼出菜单的手感与以前完全一致；iOS 也不装（它不支持元素全屏）。三条中间点击路径（翻页模式的三分区覆盖层、文本滚动模式的 `handleZoneTap`、漫画滚动模式的 `onZone`）统一走 `centerTap()`，左右分区点击会 `cancel()` 掉待判定的单击，免得点完翻页又弹出菜单。菜单打开时整屏遮罩先吃掉那次点击，遮罩在中间区域关闭菜单的同时 `arm()` 一个双击窗口，「双击中间切全屏」在菜单开着时也成立。手机端再给阅读根节点挂 `touch-action: manipulation`，关掉浏览器自带的双击缩放（纵向滚动与双指缩放不受影响），避免同一手势被浏览器抢着缩一次。
- 目录：整屏面板（顶部返回 + 书名 + 章数，Virtuoso 虚拟列表，定位并高亮当前章，点章跳转）。**必须渲染在底部菜单之外**：菜单带 `backdrop-blur`，会成为 `fixed` 后代的包含块，放里面 `h-full` 只能拿到菜单高度；历史上它写的是 `top-0 + bottom-full`，两者同时存在时高度被算成 0，整块目录完全看不见。

**本地书籍（对应 legado 本地 TXT / EPUB）**
- 入口：书架页右上「本地导入」按钮（书架为空时另有「上传本地书籍」），支持 TXT / EPUB，单文件上限 64MB，上传后自动入库并直接进入阅读页。
- 书架卡片：本地书打「本地」角标，右上角有删除按钮（二次确认），删除会连服务器上的文件一起清掉。
- 存储：正文落盘 `data/reader/local/<bookID>.<txt|epub>`；目录信息与网络书共用 `reader_chapters`，用 `Tag` 记定位：TXT 存 UTF-8 规范化后文件内的字节区间 `start:end`，EPUB 存 zip 内的 XHTML 条目路径。读章只取所需区间/条目，不整本载入内存。
- TXT：自动识别 BOM(UTF-8/UTF-16) 与 UTF-8 / GBK / Big5，统一转 UTF-8 落盘；目录按 legado 默认 TXT 规则切章（`第X章/节/卷/集/部/篇`、序章、楔子、番外等），并对「第一章的正文内容」这类正文行做启发式过滤，切不出章名时整本当一章「全文」，章前内容（书名/简介）并入第一章。
- EPUB：`META-INF/container.xml` → OPF → `spine` 顺序出章。目录标题优先取 NCX / EPUB3 NAV，**逐 token 走并用栈收任意层级的 navPoint**——Epubor 等工具导出的 EPUB 常漏 `</navPoint>`，标题会整棵嵌进上一个节点，按固定层级解会丢掉一大半标题；取不到时依次退回正文首行 → 封面页（文件名含 cover 且该页只有图）标「封面」→ `<title>`（过滤 Cover/Table of Contents 这类无信息量的）→ `第 N 章`。
- EPUB 图片：正文里的 `<img>` 在转纯文本时就地换成 `[img]<zip 条目>` 标记行（相对路径按该 XHTML 所在目录解析），下发前把标记换成签名地址 `/api/reader/local/asset?b=&p=&s=`（HMAC，`<img>` 带不了 JWT），前端把 `[img]` 开头的行渲染成居中图片（`max-width:100%` + `max-height:70vh`，保证不撑破分栏）。网络书正文不受影响。
- 进度：与网络书同一套 `durChapter*` 字段，跨端一致；同名文件重复上传按覆盖更新处理（章数不变则保留进度）。

**音频书播放条（AudioPlayActivity + AudioPlayService，即「听书」）**
- 背景与封面：书籍封面强模糊（blur 32px）铺满做底，叠一层很淡的主题底色（opacity 0.3）保住日/夜对比度；正中圆形显示封面原图（对应 legado `upCover` 的 ivBg 模糊图 + ivCover 圆图）。封面缺失或加载失败时退回主题色圆点，不留破图。
- transport 行：上一章 | -15s | 播放暂停 | +15s | 下一章（SEEK_STEP=15s，进度按秒）。
- 动作行（常驻底部，抽屉打开时仍可点）：章节（目录选择，见下）、定时关闭、倍速、片头片尾。
- 章节选择：底部抽屉列全部章节，定位到当前章、当前章高亮、点章即跳；卷名行不可点。
- 定时关闭：0/5/10/15/30/60/90/180 分钟；暂停期间不倒计时；归零自动暂停播放；选定值持久化为下次默认（对应 AppConfig.ttsTimer 在服务启动时 setTimer）。
- 倍速：滑杆 0.5–3.0（步进 0.1）+ 0.5/0.75/1/1.25/1.5/1.75/2/2.5/3 快捷档；持久化（对应 AudioPlay.playSpeed，Android 6 以下不支持调速）。
- 跳过片头片尾：抽屉内两条滑杆（片头/片尾，秒，0 不跳过，上限 300），按书持久化（Book.openCredits/closeCredits，落库 books.open_credits / close_credits）。语义：全新开播（该章进度为 0）时 seek 到片头秒数；播放到 duration-片尾秒数即等同播完，有下一章则自动续播，末章则停在片尾处。**单位是秒，不是章数。**
- 播放进度按秒记忆（节流 5s 上报），跨端一致；播完自动下一章。
- legado 的「播放模式」（顺序/单章循环/随机/列表循环）与「音频服务唤醒锁」是客户端能力，Web 端未实现。

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
- 搜索分页：滚到底自动取下一页（`POST /reader/search` 的 `page`，从 1 开始，对应书源 `searchUrl` 的 `{{page}}`），逐页增量合并（跨页按「书名+作者」去重、并书源，见 `utils/searchBooks.ts`）。收口比 legado 严一档：**新一页没带来新书就停止翻页**——不支持分页的源每页都重复返回首页结果，照 legado「任一源有结果即可翻页」会一直转圈加载；单次搜索最多 20 页。
- 书源/替换编辑器按第 7/8 节 Tab/字段一一对应。
