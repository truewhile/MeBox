# 阅读模块（Reading Module）设计与实施方案

> 状态：设计稿，待评审
> 目标版本：v0.2.0（分期落地，见第 9 节）
> 关联现有子系统：媒体库 / 网盘存储 / 权限体系 / 任务队列

---

## 1. 目标与范围

### 1.1 已确认的产品决策

| 维度 | 决策 |
| --- | --- |
| 内容类型 | **电子书 + 漫画统一书架**（EPUB / TXT / PDF / MOBI 与 CBZ / CBR / 图片文件夹） |
| 书源 | **独立书库**（不复用影视媒体库）+ **网盘直链阅读** |
| 首版范围 | **完整版**：多用户书库权限 + 阅读统计 |
| 阅读形态 | **滚动流式与分页翻页双模式**，用户可切换并持久化偏好 |

### 1.2 明确的非目标

- **不接入 Emby/Jellyfin 协议。** Emby 的 `Items` / `Views` / `PlaybackInfo` 语义围绕音视频构建，没有书籍章节与阅读进度的对应概念。强行映射会污染 `internal/service/emby_*.go` 与 `internal/handler/emby_*.go` 的既有兼容层，收益极低。阅读能力只通过 MeBox 自己的 Web UI 提供。
- **不复用 `model.Library` / `LibraryRoot`。** `Library.Type` 的取值域是 `movie/tv/anime/music`，且被海报墙轮播（`CarouselEnabled`）、自动整理管线、Emby 视图、首页预览等链路消费。把书库塞进去会导致这些链路需要到处加 `type != "book"` 判断。
- 首版不做：听书 TTS、在线书源（笔趣阁类）、社交分享、跨设备同步批注冲突合并。

---

## 2. 总体架构

### 2.1 分层落位

完全沿用现有分层，不引入新模式：

```
web/src/pages/Books*.tsx            ← 页面
web/src/components/Book*.tsx         ← 阅读器与书架组件
web/src/api/books.ts                 ← axios 封装（仿 web/src/api/library.ts）
        ↓ /api/books/*
internal/handler/books*.go           ← 反序列化 + 权限校验 + 响应
internal/service/book_*.go           ← 业务策略（扫描、解析、进度、统计）
internal/repository/book_*.go        ← 纯持久化
internal/model/book.go               ← GORM 模型，注册进 model.AllModels()
```

新增路由注册走 `internal/handler/routes_authenticated_features.go` 的既有范式，新增一个 `registerAuthedBookRoutes(authed, svc)`，在 `registerAuthenticatedRoutes` 链上挂载。`service.Container` 与 `repository.Container` 各追加一个字段。

### 2.2 与现有能力的复用点

| 现有部件 | 复用方式 |
| --- | --- |
| `service.StreamService.ServeFile`（`internal/service/stream_file.go`） | 已用 `http.ServeContent` 处理 HEAD / Range / If-Modified-Since，**PDF 与原始文件流直接照搬这条路径** |
| `cloud.Provider.Resolve(ctx, fileRef) (*DirectLink, error)`（`internal/service/cloud/cloud.go`） | 网盘书源的直链解析入口，`DirectLink.Proxy` 决定 302 还是反代 |
| `model.StorageConfig`（`internal/model/storage_assistant.go`） | 直接复用为网盘书源的账号凭据载体，**不新建凭据表** |
| `service.ImageProxy`（`internal/service/image_proxy*.go`） | 漫画页与封面的磁盘缓存 + 远程拉取 + 缩放，复用其缓存目录与命名思路 |
| `service.PruneImageCache` / `PruneImageCachePools`（`internal/service/cache_cleanup.go`） | 现成的「按池做 LRU 淘汰 + 按保留时长淘汰」助手，书籍缓存淘汰直接复用它 |
| `service/scheduler_local_jobs.go` | 本地定时任务的挂载点，书籍缓存清理与每日统计汇总都注册在这里 |
| `config.CacheConfig`（`internal/config/types.go`） | 已有 `CacheDir` / `ImagesMaxSizeMB` / `ImagesOriginalsMaxSizeMB` / `ImagesOriginalsTTLHours` / `MemoryMaxSizeMB`，书籍缓存容量配置直接挂进去 |
| `service.FileManager`（`internal/service/filemanager.go`） | 本地书源目录浏览，前端复用 `LocalDirBrowserDialog.tsx` |
| `service.Scheduler` | 书库定时扫描（默认关闭，管理员可开） |
| `model.UserPermission` | 新增阅读权限位，见第 7 节 |
| `helper.Go` / `Container.stopCtx` | 后台扫描任务的生命周期管理 |

---

## 3. 数据模型

新增文件 `internal/model/book.go`，并在 `internal/model/model.go` 的 `AllModels()` 中追加。所有表继承 `model.Base`（UUID 主键 + 时间戳 + 软删除）。

**表名约定**：`internal/model` 全包**没有任何 `TableName()` 覆盖**，一律使用 GORM 默认复数化（例如 `PlaybackHistory` → `playback_histories`，可从 `internal/database/schema_migration.go` 的裸 SQL 印证）。新表沿用该约定，不引入例外。因此模型命名要保证复数化结果干净：

| 模型 | 表名 |
| --- | --- |
| `Book` | `books` |
| `BookLibrary` | `book_libraries` |
| `BookSource` | `book_sources` |
| `BookChapter` | `book_chapters` |
| `BookProgress` | `book_progresses` |
| `BookAnnotation` | `book_annotations` |
| `BookFavorite` | `book_favorites` |
| `BookReadingSession` | `book_reading_sessions` |
| `BookDailyStat` | `book_daily_stats` |

（刻意用 `BookDailyStat` 而不是 `BookStatDaily`——后者复数化会得到 `book_stat_dailies`。）

### 3.1 书库与书源

```go
// BookLibrary 是独立于影视媒体库的书库。
type BookLibrary struct {
    Base
    Name        string `gorm:"size:128;not null" json:"name"`
    Kind        string `gorm:"size:16;not null;default:mixed" json:"kind"` // ebook / comic / mixed
    CoverURL    string `gorm:"size:1024" json:"cover_url,omitempty"`
    Enabled     bool   `gorm:"default:true" json:"enabled"`
    SortOrder   int    `gorm:"index;default:0" json:"sort_order"`
    LastScanAt  *time.Time `json:"last_scan_at,omitempty"`
    ScanStatus  string `gorm:"size:16;default:idle" json:"scan_status"` // idle / scanning / error
    ScanMessage string `gorm:"size:512" json:"scan_message,omitempty"`
}

// BookSource 是书库下的一条挂载来源：本地目录或网盘路径。
type BookSource struct {
    Base
    LibraryID       string `gorm:"index;size:36;not null" json:"library_id"`
    Name            string `gorm:"size:128" json:"name,omitempty"`
    StorageKind     string `gorm:"size:16;not null;default:local" json:"storage_kind"` // local / cloud
    Path            string `gorm:"size:1024;not null" json:"path"`                     // 本地绝对路径 / 网盘内路径
    StorageConfigID string `gorm:"index;size:36" json:"storage_config_id,omitempty"`   // 复用 model.StorageConfig
    Depth           int    `gorm:"default:3" json:"depth"`                             // 扫描递归深度上限
    Enabled         bool   `gorm:"default:true" json:"enabled"`
    SortOrder       int    `gorm:"default:0" json:"sort_order"`
}
```

`StorageKind = cloud` 时，`StorageConfigID` 指向一条 `StorageConfig`（`Type` ∈ `cloud115 / clouddrive2 / openlist / emby_remote`）。凭据解密沿用 `service.CryptoService`。

### 3.2 书籍与章节

```go
type Book struct {
    Base
    LibraryID   string `gorm:"index;size:36;not null" json:"library_id"`
    SourceID    string `gorm:"uniqueIndex:uniq_book_source_path,priority:1;index;size:36;not null" json:"source_id"`
    // SourcePath 在本地源是绝对路径，在网盘源是「网盘内路径」，两者都用
    // (source_id, source_path) 做唯一键，天然隔离两个 ID 空间。
    SourcePath  string `gorm:"uniqueIndex:uniq_book_source_path,priority:2;size:1024;not null" json:"source_path"`
    SourceRef   string `gorm:"size:256" json:"source_ref,omitempty"` // 网盘 file id / pickcode
    Title       string `gorm:"size:512;not null" json:"title"`
    Author      string `gorm:"size:256;index" json:"author,omitempty"`
    SeriesName  string `gorm:"size:256;index" json:"series_name,omitempty"`
    Volume      int    `json:"volume"`
    Format      string `gorm:"size:16;not null" json:"format"` // epub/txt/pdf/mobi/cbz/cbr/folder
    MediaKind   string `gorm:"size:16;not null;default:ebook" json:"media_kind"` // ebook / comic
    SizeBytes   int64  `json:"size_bytes"`
    FileHash    string `gorm:"index;size:64" json:"file_hash,omitempty"` // 大小+首尾采样，去重
    CoverURL    string `gorm:"size:1024" json:"cover_url,omitempty"`
    Description string `gorm:"type:text" json:"description,omitempty"`
    Language    string `gorm:"size:32" json:"language,omitempty"`
    Tags        string `gorm:"type:text" json:"tags,omitempty"` // 逗号分隔
    ChapterCount int   `json:"chapter_count"`
    WordCount    int64 `json:"word_count"`
    PageCount    int   `json:"page_count"` // 漫画总页数 / PDF 页数
    ParseStatus  string `gorm:"size:16;default:pending" json:"parse_status"` // pending/ok/failed
    ParseMessage string `gorm:"size:512" json:"parse_message,omitempty"`
    NSFW         bool  `gorm:"default:false" json:"nsfw"`
    AddedAt      time.Time `json:"added_at"`
}
```

**唯一键说明**：`SourcePath` 上的 `uniqueIndex` 需与 `SourceID` 组成复合键（`uniq_book_source_path`，priority 1 = `source_id`）。同一本书被两个书源包含时允许重复入库，这是符合预期的（用户可能故意如此）。

```go
// BookChapter 只存索引，不存正文（见 3.4 的取舍）。
type BookChapter struct {
    Base
    BookID       string `gorm:"index:idx_book_chapter,priority:1;size:36;not null" json:"book_id"`
    Index        int    `gorm:"index:idx_book_chapter,priority:2" json:"index"`
    Title        string `gorm:"size:512" json:"title"`
    Level        int    `gorm:"default:1" json:"level"` // 目录嵌套层级，1 = 顶级
    // 电子书定位：二选一
    Href         string `gorm:"size:1024" json:"href,omitempty"`      // EPUB zip 内条目路径
    StartOffset  int64  `json:"start_offset"`                          // TXT 字节区间
    EndOffset    int64  `json:"end_offset"`
    // 漫画/PDF 定位
    PageStart    int    `json:"page_start"`
    PageEnd      int    `json:"page_end"`
    CharCount    int    `json:"char_count"`
}
```

### 3.3 进度、批注、收藏、统计

```go
// BookProgress 每个用户每本书一行（复合唯一键，仿 model.PlaybackHistory 的 uniq_user_history 模式）。
type BookProgress struct {
    Base
    UserID       string `gorm:"uniqueIndex:uniq_user_book,priority:1;size:36;not null" json:"user_id"`
    BookID       string `gorm:"uniqueIndex:uniq_user_book,priority:2;size:36;not null" json:"book_id"`
    ChapterIndex int    `gorm:"default:0" json:"chapter_index"`
    ChapterTitle string `gorm:"size:512" json:"chapter_title,omitempty"`
    CharOffset   int    `json:"char_offset"`  // 章内字符偏移（电子书）
    PageIndex    int    `json:"page_index"`   // 页码（漫画 / PDF）
    Percent      float64 `json:"percent"`      // 全书百分比，书架进度条展示用
    ScrollRatio  float64 `json:"scroll_ratio"` // 章内滚动比例，跨端还原更精确
    ReaderMode   string `gorm:"size:16;default:scroll" json:"reader_mode"` // scroll / paged
    Finished     bool   `gorm:"default:false" json:"finished"`
    TotalSeconds int64  `json:"total_seconds"`
    LastReadAt   time.Time `gorm:"index" json:"last_read_at"`
}

type BookAnnotation struct {
    Base
    UserID       string `gorm:"index:idx_book_anno,priority:1;size:36;not null" json:"user_id"`
    BookID       string `gorm:"index:idx_book_anno,priority:2;size:36;not null" json:"book_id"`
    ChapterIndex int    `json:"chapter_index"`
    Type         string `gorm:"size:16;not null" json:"type"` // bookmark / highlight / note
    StartOffset  int    `json:"start_offset"`
    EndOffset    int    `json:"end_offset"`
    SelectedText string `gorm:"size:2048" json:"selected_text,omitempty"`
    Note         string `gorm:"type:text" json:"note,omitempty"`
    Color        string `gorm:"size:16" json:"color,omitempty"`
}

type BookFavorite struct {
    Base
    UserID string `gorm:"uniqueIndex:uniq_user_book_fav,priority:1;size:36;not null" json:"user_id"`
    BookID string `gorm:"uniqueIndex:uniq_user_book_fav,priority:2;size:36;not null" json:"book_id"`
}

// BookReadingSession 由前端心跳驱动，服务端按小时聚合，避免行数爆炸。
type BookReadingSession struct {
    Base
    UserID      string    `gorm:"index:idx_book_stat,priority:1;size:36;not null" json:"user_id"`
    BookID      string    `gorm:"index;size:36;not null" json:"book_id"`
    BucketStart time.Time `gorm:"index:idx_book_stat,priority:2" json:"bucket_start"` // 截断到小时
    Seconds     int64     `json:"seconds"`
    CharsRead   int64     `json:"chars_read"`
    PagesRead   int       `json:"pages_read"`
}

// BookDailyStat 每日汇总，供热力图与「年度阅读报告」查询，避免实时扫 session 表。
type BookDailyStat struct {
    Base
    UserID  string `gorm:"uniqueIndex:uniq_user_book_daily,priority:1;size:36;not null" json:"user_id"`
    Day     string `gorm:"uniqueIndex:uniq_user_book_daily,priority:2;size:10;not null" json:"day"` // YYYY-MM-DD
    Seconds int64  `json:"seconds"`
    Chars   int64  `json:"chars"`
    Pages   int    `json:"pages"`
    Books   int    `json:"books"` // 当日有阅读记录的书数
}
```

### 3.4 关键取舍：正文不入库

**决策：DB 只存章节索引（偏移量 / zip 内路径 / 页码区间），正文按需从源文件读取。**

理由：
1. 网文 TXT 常见 5–50MB，漫画单册 100–800MB。入库会让 SQLite 单文件膨胀到数十 GB，直接冲击 `docker-compose.simple.yml` 的「单文件数据库好备份」定位，也会拖慢全库 VACUUM / 备份 / 数据库迁移（`internal/service/database_admin.go`）。
2. 源文件本来就是权威副本，重复存储没有收益。
3. EPUB 与 CBZ 本质上都是 zip，**随机读取 zip 内单个条目成本极低**（读中央目录 + 解压目标条目），不需要把整本解压落盘。

代价是每次打开章节都要读源文件。缓解手段：
- 本地源：`os.Open` + `io.SectionReader`，代价可忽略。
- 网盘源：见 4.3 的本地缓存策略，且对已缓存的章节走本地。

### 3.5 用户级字段（挂在 `model.User` 上）

沿用 `AllowedLibraryIDs` 的 JSON-in-text 模式（见 `internal/model/user.go`），**不复用影视库字段**，避免两个 ID 空间交叉：

```go
// 追加到 model.User
ReaderSettings        string `gorm:"type:text" json:"-"`               // 阅读器偏好 JSON
AllowedBookLibraryIDs string `gorm:"type:text" json:"-"`               // 空 = 不限制
AllowedBookLibraryList []string `gorm:"-" json:"allowed_book_library_ids,omitempty"`
```

`ReaderSettings` 结构（前端读写，服务端仅透传与长度校验）：

```json
{
  "mode": "scroll|paged",
  "fontSize": 18,
  "lineHeight": 1.8,
  "fontFamily": "serif|sans|custom",
  "contentWidth": 720,
  "theme": "light|sepia|dark|black",
  "pageAnimation": "slide|fade|none",
  "comicLayout": "single|double|auto",
  "comicDirection": "ltr|rtl",
  "hideScrollbar": true
}
```

放在 `User` 行内（而非新表）的理由：与 `PlayerVolume` / `DanmakuFontSize` 等既有播放器偏好一致，读取时随用户信息一并返回，无需额外查询。

---

## 4. 书源与内容读取管线

### 4.1 扫描流程

```
POST /api/books/libraries/:id/scan
  → BookScannerService.ScanLibrary(ctx, libraryID)
      1. 置 ScanStatus=scanning，通过 SSEHub 广播进度（复用 service.SSEHub）
      2. 遍历启用的 BookSource
         - local: filepath.WalkDir，按扩展名白名单过滤，超过 Depth 停止递归
         - cloud: cloud.New(cfg.Type, cfg, client).List(ctx, dirID) 递归列目录
      3. 对每个候选文件调 BookParser.ParseMeta(reader) 拿元信息 + 目录
      4. Upsert 到 books / book_chapters（source_id + source_path 为幂等键）
      5. 源上已消失的书标记软删除（与影视库扫描语义保持一致）
      6. 置 ScanStatus=idle，记录 LastScanAt
```

扩展名白名单：`.epub .txt .pdf .mobi .azw3 .cbz .cbr .zip .rar`（`.zip/.rar` 仅当目录内全是图片时按漫画处理，否则跳过，防止误吞压缩包）。

并发：复用 `internal/service` 现有的 worker 池写法，默认 2–4 并发解析（解析要读文件，IO 密集）。

### 4.2 各格式解析策略

| 格式 | 元信息 | 章节 / 页 | 正文读取 |
| --- | --- | --- | --- |
| **EPUB** | zip → `META-INF/container.xml` → OPF → `dc:title/dc:creator/dc:language/dc:description`；封面取 OPF `meta[name=cover]` 指向项，退化到 `guide` | 按 spine 顺序，标题取每个 XHTML 的 `<title>` 或首个 `h1..h3`；`Level` 由 nav/ncx 的嵌套深度推断 | `archive/zip` 定位 `Href` 条目，读出 XHTML → 服务端清洗后返回 |
| **TXT** | 文件名（`书名 - 作者.txt` 模式）+ 编码探测 | 正则切分：`第[一二三四五六七八九十百千零两0-9]+[章节卷回篇]`、`Chapter\s+\d+`、`^\s*\d+\s*$`；命中不足 3 个则按固定字节窗口切片 | `io.SectionReader` 读 `[StartOffset, EndOffset)` → 按探测到的编码转 UTF-8 |
| **PDF** | 首页/元数据（页数、标题）；封面渲染首屏，失败则留空 | 单章「正文」，`PageStart/PageEnd` = 全书页 | 原始文件流（Range），前端 pdf.js 自己解析 |
| **CBZ / CBR** | zip/rar 条目自然排序，第一张图做封面 | 单章，页区间 = 图片条目序号 | 按页解压单条目，走图片响应路径 |
| **图片文件夹** | 目录名 | 单章，页区间 = 排序后图片序号 | 直接读本地文件 |
| **MOBI / AZW3** | PalmDOC / KF8 头 | 首版**只入库展示、不支持在线阅读**，详情页给出「下载原文件」入口 | — |

实现细节提示：
- 编码探测用 `golang.org/x/text`（已是 `go.mod` 间接依赖）。GBK/Big5/UTF-16LE 都要覆盖，中文网文 TXT 大量是 GBK。
- CBR 需要 RAR 解压。建议引入纯 Go 的 `github.com/nwaples/rardecode`；若不接受新依赖，首版把 CBR 归入「只入库、不可读」。
- EPUB XHTML 清洗**必须在服务端做**：剔除 `<script>`、`on*` 事件属性、`<iframe>`、外部 `http(s)` 资源引用，把 `src/href` 重写为 `/api/books/:id/res/*`。前端再叠一层 DOMPurify 作为纵深防御。

### 4.3 网盘书籍的读取策略

网盘直链的核心约束：**EPUB / CBZ 的解析必须能读到文件尾部**（zip 中央目录在末尾），但 `cloud.Provider.Resolve` 返回的是短时效 URL，且 115 直链依赖 UA/Cookie（`DirectLink.Headers`），浏览器无法直接携带。

因此分两条路径：

**A. 解析阶段 —— 完整拉取到缓存目录**

```
<CacheDir>/books/<sourceID>/<hash>.<ext>
```

`BookParser` 通过 `DirectLink` 拉全量文件到缓存后再解析。缓存目录复用 `config.CacheConfig.CacheDir`（默认 `<DataDir>/cache`，容器里是 `/cache`），容量上限新加一项 `CacheConfig.BooksMaxSizeMB`（默认 2GB），走 LRU 淘汰。缓存命中的书后续正文读取也直接走本地，不再回网盘。

**B. 阅读阶段 —— 优先本地缓存，未命中走代理流**

未缓存时由服务端反代目标 URL（`DirectLink.Proxy=true` 时同样反代），并把 `Content-Type: image/*` 或 `application/pdf` 透传给前端。反代实现直接参照 `internal/service/cloud115_hls_proxy.go` 的响应头透传白名单（`Content-Type/Content-Length/Content-Range/Accept-Ranges/ETag/Last-Modified`）。

**C. 阅读进度与文件解耦** —— 代码里区分「源」「位置」：

```go
type BookLocator struct {
    Kind        string `json:"kind"` // local / cloud
    LocalPath   string `json:"local_path,omitempty"`
    CloudConfig string `json:"cloud_config,omitempty"`
    CloudRef    string `json:"cloud_ref,omitempty"`
    Href        string `json:"href,omitempty"` // zip 内条目
    StartOffset int64  `json:"start_offset,omitempty"`
    EndOffset   int64  `json:"end_offset,omitempty"`
}
```

被缓存的书 `Kind` 仍报 `cloud`（进度不绑物理位置），这样缓存被淘汰后进度依然有效。这是不把 `Book.Path` 直接存成本地缓存路径的原因。

### 4.4 磁盘与容器

书籍目录需要在 compose 里挂载，并在 README 的部署档位表补充说明。新缓存目录复用现有 `MEBOX_CACHE_CACHE_DIR`（`docker-compose.simple.yml` 中为 `/cache`），无需新增环境变量。

---

## 5. HTTP API 设计

全部挂在 `/api/books/*`，注册在 `registerAuthedBookRoutes`。响应统一走 `internal/handler/response.go` 的既有助手。

### 5.1 书库与扫描（管理端）

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/books/libraries` | `can_read_books` | 列表，按 `AllowedBookLibraryIDs` 过滤可见性 |
| POST | `/api/books/libraries` | `can_manage_book_library` | 新建/更新书库 |
| DELETE | `/api/books/libraries/:id` | `can_manage_book_library` | 删除（含级联软删 books） |
| GET | `/api/books/libraries/:id/sources` | `can_manage_book_library` | 书源列表 |
| POST | `/api/books/libraries/:id/sources` | `can_manage_book_library` | 新增书源（本地目录 / 网盘路径） |
| POST | `/api/books/libraries/:id/scan` | `can_manage_book_library` | 触发扫描，返回 task id |
| GET | `/api/books/scan/status` | `can_manage_book_library` | 扫描进度轮询 |
| GET | `/api/books/browse` | `can_manage_book_library` | 网盘路径浏览（复用 cloud Provider.List） |

### 5.2 书架与详情

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/books` | 书架列表。参数：`library_id`、`keyword`、`media_kind`、`format`、`tag`、`sort`（`title/added_at/last_read/progress`）、`page/page_size` |
| GET | `/api/books/continue-reading` | 最近在读，首页「继续阅读」区块用 |
| GET | `/api/books/:id` | 详情（元信息 + 目录树 + 当前用户进度 + 收藏态） |
| GET | `/api/books/:id/cover` | 封面。走 `ImageProxy` 的缓存与缩放，参数 `w` |
| GET | `/api/books/:id/chapters/:index` | 章节正文。电子书返回 `text/html`（已清洗）或 `application/json` 结构化段落 |
| GET | `/api/books/:id/res/*path` | EPUB 内部资源（图片/字体/CSS），路径参数为 zip 内条目 |
| GET | `/api/books/:id/pages/:index` | 漫画单页图片，`Content-Type: image/*` + 长效缓存头 |
| GET | `/api/books/:id/file` | 原始文件流（Range），pdf.js 与「下载原文件」共用 |
| POST | `/api/books/:id/favorite` | 收藏 / 取消收藏 |
| DELETE | `/api/books/:id` | 删除（`can_manage_books`） |

**章节响应格式（推荐 JSON 而非裸 HTML）**：

```json
{
  "index": 12,
  "title": "第十二章 雨夜",
  "char_count": 3820,
  "blocks": [
    { "type": "p", "text": "……" },
    { "type": "img", "src": "/api/books/xxx/res/images/1.png" }
  ],
  "next_index": 13,
  "prev_index": 11
}
```

用结构化 blocks 而非 HTML 的理由：
1. 前端可安全渲染，不必 `dangerouslySetInnerHTML`，彻底绕开 XSS 面。
2. 分页模式需要按节点测量高度做分栏，结构化的段落数组比操作 DOM 简单得多。
3. 字号/行距/主题切换只需重渲染，不碰 HTML 字符串。

保底方案：`?format=html` 仍返回清洗后的 HTML，供 EPUB 中复杂排版（表格、脚注、双向文字）回退。

### 5.3 进度、批注、统计

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/books/:id/progress` | 当前用户进度 |
| PUT | `/api/books/:id/progress` | 保存进度。前端**节流 5s + 页面卸载时 `sendBeacon`** |
| GET | `/api/books/:id/annotations` | 批注列表 |
| POST | `/api/books/:id/annotations` | 新建 |
| PATCH | `/api/books/:id/annotations/:aid` | 修改 note / color |
| DELETE | `/api/books/:id/annotations/:aid` | 删除 |
| POST | `/api/books/:id/heartbeat` | 阅读心跳，body 为 `{seconds, chars, pages}`，落 `book_reading_sessions` 小时桶 |
| GET | `/api/books/reader-settings` | 读取当前用户的阅读器偏好（`model.User.ReaderSettings`） |
| PUT | `/api/books/reader-settings` | 保存阅读器偏好（服务端只做长度与枚举校验后原样存储） |
| GET | `/api/books/stats` | 个人统计：累计时长、已读书数、在读、近 30 天热力图 |
| GET | `/api/books/stats/overview` | 管理员视角：全站阅读统计（`can_access_settings`） |

进度写入的并发安全：`uniq_user_book` 复合唯一键 + 先 `Updates` 再 `Create` 的 upsert 模式。**参照 `internal/database/schema_migration.go` 里 `dedupePlaybackHistories` 的前车之鉴**——`PlaybackHistory` 曾因 read-then-write 产生重复行导致唯一索引进不去，新表直接写 upsert，不要复制那个 bug。

---

## 6. 前端设计

### 6.1 路由与导航

`web/src/appRoutes.tsx` 新增懒加载路由：

```tsx
const BookshelfPage = lazy(() => import('./pages/BookshelfPage').then(m => ({ default: m.BookshelfPage })))
const BookDetailPage = lazy(() => import('./pages/BookDetailPage').then(m => ({ default: m.BookDetailPage })))
const BookReaderPage = lazy(() => import('./pages/BookReaderPage').then(m => ({ default: m.BookReaderPage })))
const BookStatsPage  = lazy(() => import('./pages/BookStatsPage').then(m => ({ default: m.BookStatsPage })))
const BookLibraryPage = lazy(() => import('./pages/BookLibraryPage').then(m => ({ default: m.BookLibraryPage })))
```

```
/books                书架
/books/:id            书籍详情（目录、元信息、开始阅读）
/books/:id/read       阅读器（全屏，隐藏底栏）
/books/stats          阅读统计
/books/library        书库管理（adminOnly）
```

`web/src/components/layoutNavigation.ts` 的改动：

- `MEDIA_NAV_ITEMS` 与 `MOBILE_BOTTOM_NAV_ITEMS` 加「阅读」项（`BookOpen` 图标），普通用户可见。
- 新增 `isBookReaderRoute(pathname)`，并在 `shouldShowMobileBottomNav` 中排除 `/books/:id/read`，与 `isPlayerRoute` 的处理一致。
- `resolveHeaderBack` 补 `/books/...` 的返回链。
- `LAYOUT_NAV_ITEMS` 加「书库管理」条目，`adminOnly: true`。

### 6.2 页面组成

```
web/src/pages/
  BookshelfPage.tsx            书架：筛选栏 + 网格/列表双视图 + 继续阅读横滑
  BookDetailPage.tsx           详情：封面、元信息、目录树、进度、开始/继续阅读
  BookReaderPage.tsx           阅读器外壳：顶栏 + 内容区 + 底部工具条 + 设置抽屉
  BookStatsPage.tsx            统计：热力图 + 概览卡片
  BookLibraryPage.tsx          书库管理：书库 CRUD + 书源 CRUD + 扫描触发与进度
web/src/components/
  book/ReaderCore.tsx          渲染内核分发（按 media_kind + format）
  book/ScrollReader.tsx        滚动流式
  book/PagedReader.tsx         分页翻页
  book/ComicReader.tsx         漫画（单页/双页/右开本/预加载）
  book/PdfReader.tsx           PDF（pdf.js）
  book/ReaderToolbar.tsx       顶栏 + 底栏（章节、进度、目录、批注、设置）
  book/ReaderSettingsPanel.tsx 阅读设置
  book/ChapterTocDrawer.tsx    目录抽屉
  book/AnnotationList.tsx      书签笔记列表
  book/ReaderProgressBar.tsx   进度条（可拖拽跳章）
web/src/api/books.ts           接口封装
```

对于 4 类内容，`ReaderCore` 的分发是第一层决策：

| `media_kind` | `format` | 内核 |
| --- | --- | --- |
| ebook | epub / txt | `ScrollReader` 或 `PagedReader`（按 `settings.mode`） |
| ebook | pdf | `PdfReader` |
| comic | cbz / cbr / folder | `ComicReader` |
| ebook | mobi / azw3 | 不提供阅读，仅详情页 |

### 6.3 渲染内核选型（关键决策）

**结论：自研内核，不引入 epub.js / foliate-js。**

对比：

| 方案 | 优点 | 缺点 |
| --- | --- | --- |
| `epub.js` | 成熟、CFI 精确定位、多列分页开箱即用 | 维护停滞；CFI 定位难以与自研进度模型（`charOffset` / `percent`）对齐；PDF/漫画仍需另做两个内核 |
| `foliate-js` | 一套 API 覆盖 EPUB/MOBI/CBZ/PDF，排版质量高 | 生态小、文档薄、非稳定 API，需要 vendored 一份并自行承担升级风险 |
| **自研（推荐）** | 进度模型完全可控、跨端一致；零新增重依赖；与既有 Tailwind 主题体系天然统一 | 需要自己实现分页测量、脏 HTML 清洗、资源重写 |

自研方案的可行性依据：分页的本质是「CSS multi-column 布局 + `transform: translateX` 平移」，foliate-js 也是这么做的，核心约 200 行；滚动模式的虚拟化可以直接复用已有的 `react-virtuoso`（已在 `web/package.json`，用于 `VirtualMediaGrid`）。

自研必须做好的三件事：

1. **HTML 清洗**：服务端为主（见 4.2），前端用 `dompurify` 兜底。这是新增的唯一运行时依赖。
2. **资源重写**：EPUB 内部图片/字体/CSS 的 `src`、`href`、`url()` 全部重写到 `/api/books/:id/res/`，否则相对路径会 404。
3. **分页测量与重排**：容器尺寸变化（窗口 resize、字号切换、横竖屏）后必须重新分页，并把「当前段落 + 段内比例」作为锚点恢复位置，不能让用户跳回章首。

### 6.4 双模式实现

**滚动模式（`ScrollReader`）**
- 章内虚拟化：单章文本通常 2k–10k 字，直接整章渲染即可；跨章用「当前章 + 前后各一章」的窗口，滚动到边界时无缝追加。
- 进度：`IntersectionObserver` 观测可视段落，映射为 `charOffset`；`scroll_ratio` 同时上报。
- 优势：移动端体验好，实现简单，长段落无分页误差。

**分页模式（`PagedReader`）**
- 章内：容器设为多列（`column-width: <contentWidth>`），`overflow: hidden`，通过 `translateX` 翻页；总页数由 `scrollWidth / containerWidth` 得出。
- 跨章：翻到本章末尾自动加载下一章首页；反向同理。章首/章尾需处理「残页合并」，避免出现半屏空白页。
- 输入：左右方向键、空格、点击左右热区、滑动手势（移动端）。`comicDirection`/`pageAnimation` 控制方向与动画。
- 进度：`chapter_index` + `page_index` 映射回 `charOffset`。

两种模式共享 `BookProgress`，切换模式时用「章 + 比率」换算，不丢位置。

### 6.5 状态与持久化

- 阅读器设置来自 `authStore` 的用户信息（`ReaderSettings` 反序列化），改动后 `PUT /api/books/reader-settings` 持久化 + 本地 `localStorage` 兜底（首屏渲染不等接口）。
- 进度本地先写 `localStorage`（key `mebook:book:<id>:pos`），再节流同步服务端；页面隐藏/卸载用 `navigator.sendBeacon` 保证不丢。
- 新增 `web/src/stores/readerSettings.ts`（zustand），与既有 `playProfile.ts` 组织方式一致。

---

## 7. 权限与多用户

`model.UserPermission` 新增 4 位（当前 18 个字段，加后 22 位）：

| 权限位 | 默认 | 含义 |
| --- | --- | --- |
| `can_read_books` | `true` | 书架、阅读、进度、批注 |
| `can_manage_book_library` | `false` | 书库 / 书源 CRUD、触发扫描、网盘浏览 |
| `can_manage_books` | `false` | 编辑书籍元信息、删除书、手动重新解析 |
| `can_view_book_stats` | `false` | 查看全站阅读统计 |

同步改动清单（**漏一处就会出现「后端有权限、前端不显示开关」的静默 bug**）：

1. `internal/model/permission.go` — 字段、`NewDefaultPermission()`、`PermissionMap()`，并更新文件头注释里的数量描述（注释目前写「19项」，实际 18 个字段，顺手修正）。
2. `web/src/types/auth.ts` — `PermissionFlags` 接口加 4 个字段。
3. `web/src/stores/permissions.ts` — 默认值对象、中文标签映射、权限分组数组。
4. `web/src/hooks/usePermission.ts` — 若其中有分组注释需同步。
5. `internal/handler/permissions.go` — 权限矩阵响应（若有枚举）。
6. `web/src/pages/AdminUsersForm.tsx` / 权限勾选 UI — 若按分组硬编码了列表。

书库可见性：

- 管理员在用户管理页勾选该用户可访问的书库，写入 `User.AllowedBookLibraryIDs`。
- 空值 = 不限制（与影视库语义一致）。
- 过滤集中在一个 `bookVisibility` 助手，与现有的 `internal/handler/visibility.go` 并列（该文件就是影视库可见性的集中判定点，并且会与 `PlayProfile.AllowedLibraryIDs` 求交集）。**阅读模块首版不接播放配置档**——`PlayProfile` 是影视播放器概念（音量、转码参数、PIN），与阅读无关；但判定入口要与它放在同一层，将来若要按配置档限制书库才不用重构。
- **服务端强制**：`GET /api/books/:id`、章节、页面、资源（`/res/*`）、封面、原始文件流，**所有**按 ID 取内容的接口都要校验 `book.LibraryID ∈ 用户可见书库`，不能只靠书架列表过滤。这是最容易漏的越权点：`/api/books/:id/res/*path` 会直接吐出书籍内部的原始资源，漏校验等于开放全库文件读取。
- 用户被取消书库授权后，其 `BookProgress` / `BookAnnotation` 保留不删（授权恢复即恢复），但接口一律按当前可见性判定，不因历史数据放行。

---

## 8. 阅读统计

- **采集**：阅读器每 30s 发一次 `heartbeat`，卸载时补发一次；服务端按 `(user_id, book_id, 小时桶)` 累加，行数上限 = 用户数 × 书数 × 阅读小时数，可控。
- **汇总**：`Scheduler` 每日 03:00 把昨天之前的 session 滚进 `BookDailyStat`（复用 `service.Scheduler` 的既有定时任务注册方式）。
- **展示**：
  - 个人页「阅读统计」：累计时长、读完本数、在读本数、近 30 天热力图（仿 GitHub 贡献图）、阅读类型分布（电子书 / 漫画）。
  - 首页新增「继续阅读」横滑区块（参照 `HomePageSections.tsx` 里既有区块的写法）。
  - 管理员视图：全站活跃度、热门书籍 Top 20（需 `can_view_book_stats`）。

隐私：统计仅对本人与管理员可见；管理员视图只出聚合数据，不暴露单个用户的阅读内容。

---

## 9. 分期实施计划

### P0 — 端到端可用（本地书库 / EPUB + TXT 电子书）

目标：能扫库、能在网页上把一本书读完、关掉浏览器再打开能续读。

| # | 交付物 |
| --- | --- |
| 1 | `internal/model/book.go` 九张表 + `AllModels()` 注册 + 迁移验证（SQLite 与 PostgreSQL 各跑一次升级） |
| 2 | `BookLibrary` / `BookSource` / `Book` / `BookChapter` / `BookProgress` 的 repository |
| 3 | `BookParser`：EPUB 与 TXT 解析（含 GBK 编码探测、章节正则切分、封面提取） |
| 4 | `BookScannerService`：本地目录扫描 + upsert + 进度广播 |
| 5 | API：书库 CRUD、书源 CRUD、扫描、书架列表、详情、章节正文、封面、进度读写、阅读器偏好读写 |
| 6 | 前端：`BookshelfPage`、`BookDetailPage`、`BookReaderPage`（仅滚动模式）、目录抽屉、阅读设置面板 |
| 7 | 权限：4 个权限位 + `AllowedBookLibraryIDs` 全链路（含服务端越权校验） |

**验收标准**
- 一个含 50 本 EPUB 与 20 本 GBK 编码 TXT 的目录，扫描后书架正确列出，标题/作者/封面/章节目录无误。
- 任意一本书可连续阅读 3 章以上，刷新页面后回到原位置（误差 < 1 段）。
- 权限为 `can_read_books=false` 的账号访问 `/api/books` 返回 403；直接请求他人书库的 `/api/books/:id/chapters/0`、`/api/books/:id/res/*`、`/api/books/:id/file` 同样被拒。
- SQLite 单文件档与 PostgreSQL 档都能从旧版本升级启动，无迁移报错。

### P1 — 漫画 + 分页模式 + 网盘直链

| # | 交付物 |
| --- | --- |
| 1 | `ComicReader`：CBZ 解析、单页/双页、右开本、相邻页预加载 |
| 2 | `PagedReader`：分页测量、resize 重排、跨章衔接、键鼠与手势输入 |
| 3 | 网盘书源：`StorageKind=cloud` 的书源配置、`cloud.Provider` 接入、本地缓存目录 + LRU 淘汰 |
| 4 | 网盘书籍的索引拉取与阅读反代（含 `Content-Range` 透传） |
| 5 | PDF：`PdfReader`（pdf.js）+ Range 文件流接口 |
| 6 | 图片文件夹型漫画 |

**验收标准**
- CBZ 单册 300 页可流畅翻阅，双页模式断页处理正确（避免跨章错配）。
- 分页模式下切换字号、resize 窗口、手机横竖屏切换后，位置不跳、不出现空白页。
- 挂在 OpenList 与 115 上的 EPUB 能正常入库并在线阅读，缓存目录达到上限后按 LRU 淘汰且不影响已有进度。
- 20MB 以上 PDF 可跳页、可缩放。

### P2 — 批注、统计与体验打磨

| # | 交付物 |
| --- | --- |
| 1 | 划线 / 书签 / 笔记：`BookAnnotation` 接口与 UI，批注列表与跳转 |
| 2 | 阅读统计：心跳采集、每日汇总任务、个人统计页、首页「继续阅读」区块 |
| 3 | 管理员统计视图 + 热门书籍排行 |
| 4 | 书库定时扫描（`Scheduler` 接入，默认关闭） |
| 5 | 书架高级筛选与排序、合集（系列）聚合视图 |
| 6 | MOBI/AZW3 元信息解析（仍不做在线阅读，仅提供下载） |
| 7 | 部署文档与 compose 注释更新（书籍目录挂载说明） |

### P3 — 可选增强
听书 TTS、跨设备批注冲突合并、书源自动整理（仿 `OrganizerService`）、EPUB 阅读器内注释锚点高亮。

---

## 10. 风险与待拍板项

### 10.1 需要你拍板的两点

**① 网盘书籍的缓存策略**
- 选项 A（本方案）：索引时完整下载到缓存目录，阅读时优先本地。省流量、体验好，但全新书首次打开有等待，且占用磁盘（默认 2GB 上限）。
- 选项 B：完全不落盘，每次按 Range/整文件从网盘拉。省磁盘，但每次打开都要重新下载，网盘限速时体验很差。
- 选项 C：折中——只对 EPUB/CBZ 缓存（解析必须读全文），漫画原图与 PDF 走流式。

我的建议是 **C**，因为它把「必须落盘」和「可以不落盘」分开了。

**② 章节正文的返回格式**
- JSON blocks（本方案推荐）：安全、便于分页测量，但复杂 EPUB 排版（表格、脚注、竖排）会降级。
- 清洗后 HTML：保真度高，但前端要 `dangerouslySetInnerHTML`，XSS 面更大。
- 我的建议是 **JSON blocks 为主 + `?format=html` 回退**，两者都实现，前端在遇到 `type: "html-block"` 时回退渲染。

### 10.2 技术风险

| 风险 | 影响 | 缓解 |
| --- | --- | --- |
| 自研分页内核的边界情况多（残页、跨章、RTL、竖排） | P1 可能超期 | P0 先只做滚动模式；分页单独立项，配套 `playerPageModel.test.ts` 那样的单测 |
| TXT 章节正则对网文变体覆盖不足 | 目录错乱 | 提供「手动重新切分」入口，规则可配（仿 `RecognitionWordsPanel` 的可配置词表模式） |
| 网盘直链失效 / 限速 / 防盗链 | 阅读中断 | 复用现有 115 换链与 `url_cache.go` 的缓存机制；失败时前端降级为「下载原文件」 |
| 大 TXT（>50MB）章节表行数过多 | SQLite 写入慢 | 章节超过阈值（如 5000 章）时按固定窗口粗切，或改为「按需切分 + 缓存到章节表」的惰性策略 |
| 缓存目录膨胀 | 磁盘打满 | 容量上限 + 复用 `service.PruneImageCache` 的 LRU 清理 + 系统设置页可见 |
| 数据库迁移对老库不兼容 | 升级失败 | 新表全部是纯新增，无列变更；不触碰 `ensurePostgresColumnCompatibility` 的既有语句 |

### 10.3 不引入的新依赖清单

| 依赖 | 用途 | 取舍 |
| --- | --- | --- |
| `dompurify` | 前端 HTML 清洗兜底 | **建议引入**（前端必需） |
| `pdfjs-dist` | PDF 渲染 | **建议引入**（P1） |
| `github.com/nwaples/rardecode` | CBR 解压 | 可选；不接受则 CBR 首版只入库 |
| `epub.js` / `foliate-js` | EPUB 渲染 | **不引入**，见 6.3 |

---

## 11. 测试策略

与项目现有测试密度对齐（`internal/service` 下大量 `_test.go`，前端有 `*.test.ts`）：

**后端**
- `book_parser_test.go`：EPUB / TXT 各准备 fixture（`testdata/` 下小体积样本），断言元信息、章节数、章节边界字节偏移、GBK 转码正确性。
- `book_scanner_test.go`：临时目录扫描 + 重复扫描幂等 + 源文件删除后软删。
- `book_progress_test.go`：并发 upsert 不产生重复行（直接复现 `dedupePlaybackHistories` 防的那类 bug）。
- `book_permission_test.go`：越权矩阵，逐接口断言非可见书库返回 403/404。
- Handler 层：仿 `internal/handler/media_test.go` 起的 `httptest` + 真实内存 SQLite。

**前端**
- `readerModel.test.ts`：模式切换时的位置换算（`scroll ↔ paged`、`charOffset ↔ pageIndex`）、百分比计算、跨章边界。
- 分页计算的纯函数抽出单测（不含 DOM），参照 `web/src/pages/playerPageModel.test.ts` 的做法——把逻辑从组件里拔出来测，是项目已有的好传统。

---

## 12. 附：改动文件清单

**后端新增**
```
internal/model/book.go
internal/repository/book_repository.go
internal/service/book_parser.go            EPUB / TXT / CBZ 解析
internal/service/book_parser_epub.go
internal/service/book_parser_txt.go
internal/service/book_parser_comic.go
internal/service/book_scanner.go
internal/service/book_reader.go            章节 / 页面 / 资源的读取与清洗
internal/service/book_progress.go
internal/service/book_stats.go
internal/service/book_cache.go             网盘缓存与 LRU
internal/handler/books.go
internal/handler/books_library.go
internal/handler/books_reader.go
internal/handler/routes_books.go
```

**后端修改**
```
internal/model/model.go                    AllModels() 追加 9 张表
internal/model/permission.go               4 个权限位
internal/model/user.go                     ReaderSettings / AllowedBookLibraryIDs
internal/repository/repository.go          Container 加字段
internal/service/service.go                Container 加字段 + Boot() 启动扫描
internal/handler/routes_authenticated.go   挂载 registerAuthedBookRoutes
internal/service/scheduler_local_jobs.go   书籍缓存清理 + 每日阅读统计汇总
internal/config/types.go                   CacheConfig 加 BooksMaxSizeMB；新增 BookConfig（扫描并发等）
docker-compose*.yml                        书籍目录挂载注释
README.md / README_EN.md                   能力表新增「阅读」
```

**前端新增**
```
web/src/api/books.ts
web/src/stores/readerSettings.ts
web/src/pages/BookshelfPage.tsx
web/src/pages/BookDetailPage.tsx
web/src/pages/BookReaderPage.tsx
web/src/pages/BookStatsPage.tsx
web/src/pages/BookLibraryPage.tsx
web/src/components/book/*.tsx
```
**前端修改**
```
web/src/appRoutes.tsx                       4 条路由
web/src/components/layoutNavigation.ts      导航项、阅读器路由判定、返回链
web/src/types/auth.ts                       权限位
web/src/stores/permissions.ts               权限位默认值 / 标签 / 分组
web/src/pages/HomePageSections.tsx          「继续阅读」区块
web/src/pages/settingsGroupBooks.ts         （新增）阅读设置分组
web/src/pages/settingsGroups.ts             把 settingsGroupBooks 加入 GROUPS 数组
```
