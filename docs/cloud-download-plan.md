# 115 云下载 → STRM 同步 → 自动刮削 · 实施方案

| 项 | 内容 |
| :--- | :--- |
| 状态 | 设计已定稿，待实现（P0 未开工） |
| 适用版本 | MeBox 当前主线（`internal/service/strm_*`、`internal/service/cloud115/*`） |
| 关联文档 | `115doc/115开放平台/API列表/云下载/*`（仓库内离线文档） |
| 最后核对 | 本文所有代码事实均按仓库当前代码逐条核对，行号仅供定位参考 |

---

## 1. 目标与范围

### 1.1 目标

在「115 媒体库映射」（即 STRM 管理中的一条 `StrmSyncPath`：115 目录 → 本地目录 .strm）之上提供云下载入口，用户提交磁力 / BT 种子 / ed2k / HTTP 链接后：

1. 任务提交到 115 离线（云下载）；
2. 后台轮询 115 任务状态；
3. 任务完成后**自动触发**该同步目录的 STRM 增量同步；
4. 同步完成后**自动扫描**对应媒体库，生成 Media 行；
5. **自动刮削**新增媒体，无需用户再点一次。

全程无需离开 MeBox，链路上的每个阶段在任务列表里可见。

### 1.2 明确不做（本期范围外）

| 项 | 决定 | 原因 |
| :--- | :--- | :--- |
| 磁力链接选择部分文件下载 | **不做** | 开放平台 `/open/offline/add_task_urls` 无文件选择参数，且 `/open/offline/torrent` 只接受已上传种子的 `torrent_sha1` + `pick_code`，无法解析 magnet。见第 6 节 |
| `.torrent` 勾选文件 UI | **不做** | 用户已确认本期不做选文件；种子按全选提交 |
| 磁力转种子（DHT 抓 metadata） | **不做** | 需引入 torrent 依赖与常驻 DHT 网络行为，本期不引入新依赖 |
| 非 115 网盘的离线下载 | **不做** | CD2 / OpenList / 本地目录无对应能力 |
| 普通用户自助使用 | **不做** | 首期仅管理员（沿用 `AdminRequired` + `can_manage_strm` 语义），权限位见 9.3 |
| 下载完成后自动整理/重命名（Organize） | **不做** | 与 STRM 映射模型冲突，`.strm` 必须与远端路径一一对应 |

### 1.3 非目标（但需保证不被破坏）

- 现有 STRM 手动同步 / Cron 同步 / 下载队列 / 上传队列行为不变；
- 115 令牌自动刷新机制不得被第二个客户端实例破坏（见 3.3 关键约束 K1）；
- 刮削不重复刷已 `matched` 的媒体（沿用 `EnqueueLibrary` 的候选过滤）。

---

## 2. 现状核实

### 2.1 已具备

| 能力 | 位置 | 说明 |
| :--- | :--- | :--- |
| 115 OpenAPI 客户端 | `internal/service/cloud115/client.go`、`const.go:16` | `OpenClient`，`ProAPIBase = https://proapi.115.com`，`doJSON` 内建重试与令牌刷新 |
| 115 授权（PKCE/扫码）与令牌落库 | `cloud115/oauth.go`、`service/strm_115_oauth.go` | `OnTokenRefreshed` → `persist115Tokens` 写回 `StrmAccount.Config` |
| 115 文件上传 / 秒传 | `cloud115/upload.go:217` | `Upload(ctx, filePath, parentCID, signKey, signVal)` → `UploadCompleteResult{FileId, PickCode, Sha1}` |
| 115 目录创建 / 查找 | `cloud115/upload.go:318`（`MkDir`）、`cloud115/open.go:126`（`FindNamedContentInParent`） | 可用于建「云下载/种子文件」暂存目录 |
| 115 全局限流/熔断 | `cloud115/throttle_manager.go` | `GetGlobalThrottleManager()` |
| STRM 同步（增量/全量） | `service/strm_sync.go:82` `StartSync` | 已运行时返回「该目录正在同步中」，不排队 |
| STRM 目录/记录/队列数据层 | `model/strm.go`、`repository/strm_repository.go` | — |
| 扫描 + 自动刮削入口 | `service/scanner_scan.go`、`scanner_post_scan.go:19` | `startAutoScrape` 受 `scrape.auto_on_scan` 全局开关约束 |
| 刮削入队 | `service/scraper_queue.go:219` `EnqueueLibrary` | 仅挑 `scrape_status` 为空的 `pending` 行，天然幂等、不会重刷已匹配 |
| 周期任务框架 | `service/scheduler.go:118` `scheduledJob` | 新增 job 只需在 `Start` 里 append |
| 服务装配 | `service/service_builder.go:148` | `NewStrmService` 之后才有 `Cloud115`，可作为依赖注入范式 |

### 2.2 缺口（本方案要补的）

| 缺口 | 影响 |
| :--- | :--- |
| 全仓无任何离线/云下载代码（`grep -r "offline" internal/service/cloud115/` 无命中） | 需从零封装 4 个 API |
| `finishSync` 结束后**没有任何回调**（`strm_sync.go:323`） | 「同步完成 → 扫描」链路是断的，必须新增 hook |
| 无「本地路径 → 媒体库」反查 | 需新增 repository 查询 |
| 无 `.torrent` multipart 上传端点（现有仅 reader 书源、DB 恢复用到 `FormFile`） | 需新增端点 + 上传大小限制 |
| 无云下载任务表 | 需新增模型 + 仓储 + 迁移注册 |

---

## 3. 总体设计

### 3.1 链路时序

```
[前端] 提交（多行链接 / .torrent 文件）+ 选择目标 STRM 同步目录
   │
   ▼
[CloudDownloadService.Submit]
   │ 1) 校验账号可用、同步目录启用
   │ 2) 配额预检 GET /open/offline/get_quota_info
   │ 3) .torrent: 上传到「云下载/种子文件」→ POST /open/offline/torrent 拿 info_hash
   │ 4) POST /open/offline/add_task_urls（批量 ≤115/批）或 /open/offline/add_task_bt
   ▼
[DB] CloudDownloadTask: status=submitted（逐条记录每个链接的受理结果）
   │
   ▼
[Scheduler: cloud_download_poll 每 60s]
   │ GET /open/offline/get_task_list?page=N 逐页
   │ 按 info_hash（优先）/ url（回退）匹配本地任务
   │ status 0/1 → downloading(percentDone)  |  2 → succeeded  |  -1 → failed
   ▼
[Pipeline: succeeded 任务]
   │ A. 收集受影响 syncPathID（30s 去抖合并）
   │ B. StrmService.StartSync(syncPathID, incremental)
   ▼
[StrmService.finishSync] → SetSyncFinishHook(ok)
   │ C. 反查 LocalPath 对应的媒体库 → Scanner.ScanLibrary(libraryID)
   │ D. Scraper.EnqueueLibrary(libraryID, 默认 options)（仅 pending 行）
   ▼
[DB] 回写 Stage=done；前端列表展示各阶段结果
```

### 3.2 模块职责

| 模块 | 职责 | 明确不负责 |
| :--- | :--- | :--- |
| `cloud115/offline.go` | 纯 API 封装：参数拼装、响应解析、错误码映射 | 不碰数据库、不做编排 |
| `CloudDownloadService` | 输入解析、校验、提交、状态机、轮询、编排 | 不直接读写 .strm 文件 |
| `StrmService` | 现有同步逻辑 + 新增完成回调 | 不感知云下载概念 |
| `ScannerService` / `ScraperService` | 现有扫描/刮削逻辑 | 不感知云下载概念 |

### 3.3 关键约束（实现时必须遵守）

**K1 — 必须复用 StrmService 已缓存的 115 客户端，禁止自行 `NewOpenClient`。**
`strm_service.go:524` 的 `newProvider` 里给 `OpenClient` 挂了 `OnTokenRefreshed` → `persist115Tokens`。115 的 `refresh_token` 是一次性轮转的：若云下载服务另建一个客户端，两边各自刷新会互相作废，最终把账号刷成「授权已失效」。

落地方式：给 `StrmService` 增加导出方法

```go
// Open115Client 返回账号已缓存的 115 客户端（含令牌自动刷新与落库）。
func (s *StrmService) Open115Client(ctx context.Context, accountID string) (*cloud115.OpenClient, error)
```

内部复用 `providerFor` + `cloud.OpenAPI115Provider` 断言（与 `strm_sync.go:379` 的用法一致）。

**K2 — 去抖。** `walk115Flat`（`strm_sync.go:682`）是整树自适应拉取，不是只看新目录。多个任务在短时间内完成时，同一 syncPath 只应同步一次。

**K3 — 阶段失败隔离。** STRM 同步失败不得把 115 下载状态改成失败；反之亦然。下载状态与后处理阶段分别落在不同字段（见 4.1）。

**K4 — 同步不排队。** `StartSync` 在运行中直接报错，Pipeline 必须自己实现「等待 + 重投」，不能靠重试打满日志。

**K5 — 所有 115 调用走全局 throttle。** 轮询在无活跃任务时不得发起请求。

---

## 4. 数据模型

### 4.1 `model.CloudDownloadTask`（新表 `cloud_download_tasks`）

```go
// internal/model/cloud_download.go
type CloudDownloadTask struct {
    Base
    AccountID  string `gorm:"size:36;index" json:"account_id"`
    SyncPathID string `gorm:"size:36;index" json:"sync_path_id"` // 绑定的 STRM 同步目录
    LibraryID  string `gorm:"size:36;index" json:"library_id"`   // 由 LocalPath 反查得到，可为空
    Provider   string `gorm:"size:32" json:"provider"`           // 固定 cloud115

    SourceType string `gorm:"size:16;index" json:"source_type"` // magnet / torrent / ed2k / http / ftp
    SourceURL  string `gorm:"type:text" json:"source_url"`      // 原始链接（.torrent 时存文件名）
    SourceKey  string `gorm:"size:512;index" json:"source_key"` // 归一化后的匹配键：大写 info_hash 或 trim 后的 url
    InfoHash   string `gorm:"size:64;index" json:"info_hash"`
    TorrentName string `gorm:"size:512" json:"torrent_name,omitempty"`
    TorrentSha1 string `gorm:"size:64" json:"torrent_sha1,omitempty"` // .torrent 路径用
    PickCode    string `gorm:"size:128" json:"pick_code,omitempty"`   // .torrent 路径用

    WpPathID string `gorm:"size:64" json:"wp_path_id"`        // 115 目标目录 ID（= syncPath.RemotePath）
    SavePath string `gorm:"size:512" json:"save_path,omitempty"` // 相对 wp_path_id 的子路径（本期留空）

    Status       string `gorm:"size:16;index" json:"status"` // queued/submitted/downloading/succeeded/failed/canceled
    Percent      int    `json:"percent"`
    SizeBytes    int64  `json:"size_bytes"`
    Error        string `gorm:"size:1024" json:"error"`

    Stage         string `gorm:"size:16;index" json:"stage"` // pending/waiting_sync/syncing/scanning/scraping/done/na
    StageError    string `gorm:"size:1024" json:"stage_error"`
    StrmRecordID  string `gorm:"size:36" json:"strm_record_id,omitempty"`
    ScanAdded     int    `json:"scan_added"`
    ScrapeQueued  int    `json:"scrape_queued"`

    RetryCount int        `json:"retry_count"`
    LastPollAt *time.Time `json:"last_poll_at,omitempty"`
    NextPollAt *time.Time `gorm:"index" json:"next_poll_at,omitempty"`
    CompletedAt *time.Time `json:"completed_at,omitempty"`
}
```

注册点：`model.AllModels()`（`internal/model/model.go:37`）、`repository.Container`（`internal/repository/repository.go:11`）、`service.Container`（`internal/service/service.go:19`）。

> **不复用 `StrmDownloadTask`**：那是「远端元数据 → 本地」的文件搬运队列（`model/strm.go`），语义完全不同，混用会让 `/admin/strm/downloads` 列表出现云下载任务。

### 4.2 状态机

**Status（115 下载侧）**

| 值 | 进入条件 | 终态 |
| :--- | :--- | :--- |
| `queued` | 本地已建行，尚未提交 115 | 否 |
| `submitted` | 115 已受理（`data[].state=true`） | 否 |
| `downloading` | `get_task_list` 返回 status 0 或 1 | 否 |
| `succeeded` | status 2 | **是**，进入 Pipeline |
| `failed` | status -1 / 提交被拒 / 超时（默认 72h） | **是** |
| `canceled` | 用户取消 | **是** |

**Stage（后处理侧）**

| 值 | 含义 |
| :--- | :--- |
| `pending` | 尚未开始（未成功） |
| `waiting_sync` | 已成功，等待去抖窗口结束或等待同一目录的同步结束 |
| `syncing` | STRM 增量同步执行中 |
| `scanning` | 媒体库扫描中 |
| `scraping` | 刮削已入队 |
| `done` | 全链路完成 |
| `na` | 同步目录已删除/禁用，跳过后续（不算失败） |
| `failed` | 阶段失败，`StageError` 记录原因 |

> Stage 失败**不回滚** Status；用户可对 Stage 失败的任务点「重做后处理」。

---

## 5. 115 开放平台对接

### 5.1 接口清单

| 用途 | 方法 | 路径 | 关键参数 | 关键返回 |
| :--- | :--- | :--- | :--- | :--- |
| 添加链接任务 | POST | `/open/offline/add_task_urls` | `urls`（换行分隔，≤115 条）、`wp_path_id` | `data[]{state,code,message,info_hash,url}` |
| 解析 BT 种子 | POST | `/open/offline/torrent` | `torrent_sha1`、`pick_code` | `data{file_size,torrent_name,file_count,info_hash,torrent_filelist[]{size,path,wanted}}` |
| 添加 BT 任务 | POST | `/open/offline/add_task_bt` | `info_hash`、`wanted`、`save_path`、`torrent_sha1`、`pick_code`、`wp_path_id` | `data[]`（成功为空数组） |
| 任务列表 | GET | `/open/offline/get_task_list` | `page` | `data{page,page_count,count,tasks[]{...}}` |
| 配额信息 | GET | `/open/offline/get_quota_info` | 无 | `data{package[]{surplus,used,count,name,expire_info[]},count,surplus,used}` |
| 删除任务（P2） | POST | `/open/offline/del_task` | `info_hash`、`del_source_file` | `data[]` |
| 清空任务（P2） | POST | `/open/offline/clear_task` | `flag`(0~5) | `data[]` |

请求形态：`add_task_*` / `del_task` / `clear_task` / `torrent` 均为 **multipart/form-data**；`get_*` 为 GET query。全部走 `Authorization: Bearer <access_token>`，即现有 `doAuthJSON` 通道。

### 5.2 任务列表字段（轮询用）

| 字段 | 类型 | 用途 |
| :--- | :--- | :--- |
| `info_hash` | string | 首选匹配键 |
| `url` | string | 回退匹配键（链接任务） |
| `status` | int | -1 失败 / 0 分配中 / 1 下载中 / 2 成功 |
| `percentDone` | int | 进度（**注意驼峰**，见 5.4） |
| `size` | int | 任务总字节 |
| `name` | string | 任务名，用于 UI |
| `file_id` / `delete_file_id` | string | 完成后源文件/文件夹 ID（P2 裁剪用） |
| `wp_path_id` | string | 落盘父目录，可用于校验目录一致性 |
| `add_time` / `last_update` | int | 超时判断 |

### 5.3 错误码映射（要落到用户可读文案）

| 码 | 115 文案 | MeBox 提示 |
| :--- | :--- | :--- |
| `1000012` | 云下载配额已用完 | 「115 云下载配额已用尽，请在 115 端购买/领取配额后重试」 |
| `91006` | 存储空间不足 | 「115 存储空间不足」 |
| `20018` | 文件不存在或已删除 | 「种子文件找不到（可能已被清理），请重新上传 .torrent」 |
| `1000011` | 链接数量超过 115 个 | 本地已按 115 条切批，正常不应触发；触发则记 warning 并重切 |
| `990002` | 参数错误 | 「提交参数不被 115 接受」，附原始 message |
| `980004` | 操作失败 | 可重试，走退避 |
| 无权限/未开通 | —— | 「当前 115 应用未开通云下载权限」，并提示到开放平台申请（见风险 R1） |

### 5.4 实现陷阱（必须按此实现）

1. **`percentDone` 是驼峰**，`get_task_list` 其余字段是 snake_case。Go struct tag 必须写 `json:"percentDone"`，写错会永远 0 进度。
2. **`data[].info_hash` 仅任务成功受理时返回**，可能为空 → 必须同时保存提交时的原始 `url` 做回退匹配，并对 url 做归一化（去首尾空白、统一小写协议头、去掉 `&` 后跟踪参数？**不做过度归一化**，仅 trim，避免误合并）。
3. **`wanted` 索引基准未确认**（见 11 节实测项）。本期不做选文件，实现统一传 `wanted="0"`（官方文档唯一明确允许的取值），待真机确认后再决定是否改为全索引拼接。
4. **`add_task_urls` 单批 ≤115**，超了服务端报 `1000011`；本地必须切批。
5. **`wp_path_id=0` 表示根目录**，绝不能把「未选择目录」当 0 提交，否则文件落到 115 根目录，STRM 永远扫不到 → 提交前强校验 `WpPathID != "" && != "0"`。
6. **`get_task_list` 只支持 page，无 page_size**，需按 `page_count` 翻页；轮询设页数上限（默认 10 页）防止账号任务堆积把一轮轮询拖爆。

---

## 6. 为什么磁力不能选文件（结论留档）

| 接口 | 能否选择文件 | 依据 |
| :--- | :--- | :--- |
| `add_task_urls`（磁力/ed2k/http） | ❌ 无 wanted 参数 | `115doc/.../添加云下载链接任务.md` 请求参数仅 `urls`、`wp_path_id` |
| `torrent`（解析种子） | ✅ 返回 `torrent_filelist[].wanted` | `解析BT种子.md` 返回字段表 |
| `add_task_bt`（提交 BT） | ✅ `wanted` 必填 | `添加云下载BT任务.md` 请求参数表 |

`torrent` 的入参是 **`torrent_sha1` + `pick_code`**，只能解析「已上传到本人 115 空间的 .torrent 文件」，不接受 magnet / info_hash。开放平台没有「解析磁力 → 文件列表」这一步，因此磁力链路从入口就没有选文件能力。

**若要支持磁力选文件**，唯一路径是服务端自行把 magnet 抓成 .torrent（DHT metadata），属新增外部依赖与常驻网络行为，本期不做，记录为 P3 候选。

---

## 7. 后端实现

### 7.1 文件清单

**新增**

| 文件 | 内容 |
| :--- | :--- |
| `internal/model/cloud_download.go` | `CloudDownloadTask` 模型 + 状态常量 |
| `internal/repository/cloud_download_repository.go` | CRUD、按状态取活跃任务、按阶段取待推进任务、统计 |
| `internal/service/cloud115/offline.go` | 4+2 个 API 封装、响应结构体、错误码常量与 `OfflineError` 类型 |
| `internal/service/cloud_download.go` | `CloudDownloadService` 主体：构造、提交、取消、重试、列表、配额 |
| `internal/service/cloud_download_input.go` | 输入解析（多行/类型判定/去重/切批 ≤115）、匹配键归一化 |
| `internal/service/cloud_download_submit.go` | 链接提交 + `.torrent` 上传解析 → `add_task_bt`、暂存目录查找/创建 |
| `internal/service/cloud_download_poll.go` | 轮询、状态映射、超时熔断、远程任务与本地记录对账 |
| `internal/service/cloud_download_pipeline.go` | Stage 编排、去抖、等待同步、扫描、刮削、失败隔离 |
| `internal/service/scheduler_cloud_download.go` | `jobCloudDownloadPoll` |
| `internal/handler/cloud_download.go` | REST handler + multipart 种子上传 |
| `internal/handler/routes_cloud_download.go` | 路由注册（在 `registerAdminStrmRoutes` 同级新增 `registerAdminCloudDownloadRoutes`） |
| `web/src/types/cloudDownload.ts` | 前端类型 |
| `web/src/api/cloudDownload.ts` | 前端 API |
| `web/src/pages/CloudDownloadPage.tsx` | 页面（提交面板 + 任务列表 + 配额） |

**修改**

| 文件 | 改动 | 风险 |
| :--- | :--- | :--- |
| `internal/model/model.go` | `AllModels()` 追加 `&CloudDownloadTask{}` | 低（AutoMigrate 新增表） |
| `internal/repository/repository.go` | `Container` 增加 `CloudDownload *CloudDownloadRepository` | 低 |
| `internal/service/service.go` | `Container` 增加 `CloudDownload *CloudDownloadService`，`Start/Stop` 联动 | 低 |
| `internal/service/strm_service.go` | 新增 `SetSyncFinishHook`、`Open115Client` | 中（新增导出方法，不改现有逻辑） |
| `internal/service/strm_sync.go` | `finishSync` 末尾触发 hook（异步） | **中**：hook 里必须自带 recover + 超时，绝不能阻塞或 panic 影响同步协程 |
| `internal/service/service_builder.go` | 构造 `CloudDownload`（在 `Strm` 之后），注入 Strm/Scan/Scraper/Tasks/Hub；回调 hook | 低 |
| `internal/service/scheduler.go` | `Start()` 里 append `cloud_download_poll` job（仅在 `CloudDownload != nil` 时注册） | 低 |
| `internal/repository/library_repository.go` | 新增 `FindEnabledRootsByLocalPath(ctx, localPath)`（归一化 + 前缀匹配） | 低 |
| `web/src/appRoutes.tsx`、`web/src/components/layoutNavigation.ts` | 路由 + 侧边栏入口 | 低 |

### 7.2 服务对外 API

```go
type CloudDownloadService struct{ /* cfg, log, repos, strm, scanner, scraper, tasks, hub */ }

func (s *CloudDownloadService) Submit(ctx context.Context, req CloudDownloadSubmit) (*CloudDownloadSubmitResult, error)
func (s *CloudDownloadService) SubmitTorrent(ctx context.Context, req CloudDownloadTorrentSubmit) (*CloudDownloadSubmitResult, error)
func (s *CloudDownloadService) List(ctx context.Context, q CloudDownloadQuery) (*CloudDownloadPage, error)
func (s *CloudDownloadService) Cancel(ctx context.Context, id string) error
func (s *CloudDownloadService) Retry(ctx context.Context, id string) error          // 失败任务重新提交
func (s *CloudDownloadService) RetryPipeline(ctx context.Context, id string) error // 仅重做后处理
func (s *CloudDownloadService) Delete(ctx context.Context, id string) error
func (s *CloudDownloadService) ClearFinished(ctx context.Context) (int64, error)
func (s *CloudDownloadService) Quota(ctx context.Context, accountID string) (*OfflineQuota, error)
func (s *CloudDownloadService) Start(ctx context.Context)  // 恢复中断任务
func (s *CloudDownloadService) Stop()
```

`CloudDownloadSubmit`：`AccountID`、`SyncPathID`、`URLs []string`（一次提交多条）。
`CloudDownloadSubmitResult`：逐条 `{source, task_id, ok, error}`，**部分成功必须返回 200 + 明细**。

### 7.3 提交校验（顺序不可乱）

1. `SyncPathID` 存在、`Enabled`、`Provider == cloud115`、`AccountID` 非空、`RemotePath` 为有效目录 ID 且 ≠ `"0"`；
2. 账号存在且 `Enabled`，`Open115Client` 可取得；
3. 配额预检：`get_quota_info` → `surplus <= 0` 时直接拒绝（批量多链接时按条数预估，无法精确预估大小，只做「配额是否耗尽」闸门）；
4. 输入解析：按行拆分 → 类型判定 `magnet:?xt=urn:btih:` / `ed2k://` / `http(s)://` / `ftp://` → trim → 去重（同批内 + 与库内 `submitted/downloading` 任务去重）；
5. 切批：每批 ≤115 条，逐批调用 `add_task_urls`；
6. 逐条落库：`data[].state=true` → `submitted`；失败 → `failed` + 错误码映射文案。

### 7.4 轮询实现要点

```
每 tick（默认 60s）：
  1. 取所有 status IN (submitted, downloading) 的任务，按 account_id 分组
  2. 无活跃任务 → 直接返回（不打 115 接口）
  3. 对每个账号：
       翻页取 get_task_list（page=1..min(page_count, 10)）
       构建索引：info_hash(大写) → task，url(trim) → task
       逐条匹配本地记录：
          - info_hash 命中优先
          - 否则 url 命中
          - 都未命中 → 记 LastPollAt，未超时前不判失败（115 受理有延迟）
       状态映射 + 更新 Percent/SizeBytes/Name
       status=2 → succeeded + CompletedAt + Stage=pending
       status=-1 → failed（Error 用 115 文案）
  4. 超时：submitted/downloading 且 add_time 早于 now-72h → failed「下载超时」
```

### 7.5 Pipeline 实现要点

```
推进循环（可挂在同一 tick 之后，也可独立每 15s）：
  A. 取 status=succeeded AND stage IN (pending, waiting_sync, failed) 的任务，按 sync_path_id 分组
     - 每组内若有任务 completed_at 在去抖窗口内（默认 30s）→ 本轮跳过（等凑批）
  B. 对每个 syncPath：
     - 若 IsSyncRunning(pathID) → 全部置 waiting_sync，下一轮再看
     - 否则 StartSync(pathID, incremental)，置 syncing，记录 StrmRecordID（取该 path 最新记录）
  C. 同步完成后（hook）：
     - 置 scanning → 反查 library（LocalPath 与 LibraryRoot.Path 归一化后相等或为其子目录）
     - 无匹配库 → Stage=na + StageError「本地目录未加入任何媒体库」
     - ScanLibrary(libraryID) → ScanAdded
     - 置 scraping → EnqueueLibrary(libraryID, ScrapeOptions{}) → ScrapeQueued
     - 置 done
  D. 任一步失败：Stage=failed + StageError，不重试（等待用户点「重做后处理」）
  E. 进程重启：Start() 时把 syncing/scanning/scraping 的僵尸阶段重置为 pending（参考 StrmService.recoverInterruptedSyncs 的写法）
```

去抖细节：以 `syncPathID` 为单位，服务内存里维护 `lastTrigger[pathID] = time`；窗口内到达的任务全部 `waiting_sync`，窗口结束时由首个触发者发起同步。

### 7.6 STRM 完成回调

```go
// strm_service.go
func (s *StrmService) SetSyncFinishHook(fn func(ctx context.Context, syncPathID, status string)) { ... }
```

在 `finishSync`（`strm_sync.go:323`）**落库完成之后**调用：

```go
if hook := s.syncFinishHook(); hook != nil {
    helper.Go(s.log, "strm.syncFinishHook", func() {
        ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
        defer cancel()
        hook(ctx, p.ID, status)
    })
}
```

要点：**异步 + 自带 recover + 超时**，绝不能让 hook 的 panic 或阻塞影响同步协程；也不要在 `finishSync` 里同步调用扫描（同步耗时长，会拖住同步收尾与 Pending 记录）。

### 7.7 调度与配置项

设置项（沿用 `repo.Setting`，键名与现有 `scrape.*` / `strm.*` 风格一致）：

| 键 | 默认 | 说明 |
| :--- | :--- | :--- |
| `cloud_download.poll_interval_sec` | `60` | 轮询间隔（30~600） |
| `cloud_download.sync_debounce_sec` | `30` | 同一同步目录的去抖窗口（0~300） |
| `cloud_download.auto_scrape` | `true` | 同步后是否自动刮削 |
| `cloud_download.task_timeout_hours` | `72` | 下载超时判定 |
| `cloud_download.torrent_max_mb` | `20` | `.torrent` 上传大小上限 |
| `cloud_download.max_pages` | `10` | 单轮轮询最大翻页数 |

`auto_scrape=true` 时**不受** `scrape.auto_on_scan` 全局开关约束（用户显式提交了下载，期望链路闭环）；但若 `Scraper.AnyEnabled()==false`（无任何刮削源），Stage 直接置 `done` 并在 `StageError` 记「未配置刮削源」。

### 7.8 HTTP 路由与鉴权

挂在 `registerAdminStrmRoutes` 同组（`admin`，已带 `AuthRequired` + `AdminRequired`）：

```go
admin.GET("/cloud-downloads", listCloudDownloadsHandler(svc))
admin.POST("/cloud-downloads", createCloudDownloadsHandler(svc))
admin.POST("/cloud-downloads/torrent", uploadCloudDownloadTorrentHandler(svc)) // multipart
admin.GET("/cloud-downloads/quota", cloudDownloadQuotaHandler(svc))
admin.POST("/cloud-downloads/:id/retry", retryCloudDownloadHandler(svc))
admin.POST("/cloud-downloads/:id/retry-pipeline", retryCloudDownloadPipelineHandler(svc))
admin.POST("/cloud-downloads/:id/cancel", cancelCloudDownloadHandler(svc))
admin.DELETE("/cloud-downloads/:id", deleteCloudDownloadHandler(svc))
admin.POST("/cloud-downloads/clear-finished", clearFinishedCloudDownloadsHandler(svc))
```

multipart 端点：`file`（.torrent）+ `account_id` + `sync_path_id`；实现需 `c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxMB<<20)`，并校验文件头（BT 种子是 bencode，以 `d` 开头，含 `announce` / `info`）。

---

## 8. 前端实现

### 8.1 页面结构（`CloudDownloadPage.tsx`）

**顶部：提交面板**
- 目标同步目录下拉：复用 `GET /admin/strm/paths`，只列 `provider=cloud115 && enabled`，展示 `name + remote_display_path + local_path`；
- 账号显示：由所选同步目录自动带出（`account_id`），无需二次选择；
- 链接文本框：多行粘贴，右下角实时显示「识别到 N 条：磁力 x / ed2k y / http z」；
- `.torrent` 拖拽/选择：单文件，提交时走 multipart 端点；
- 配额条：`surplus/count`，耗尽时提交按钮禁用并提示；
- 提交结果：逐条成功/失败明细（不合并成一句「提交失败」）。

**下方：任务列表**
- 列：来源（截断显示 + tooltip 全量）、目标目录、Size、下载进度、**阶段徽标**（下载中 / 等待同步 / 同步中 / 扫描中 / 刮削中 / 完成 / 失败）、时间、操作；
- 操作：取消（仅下载中）、重试（失败）、重做后处理（Stage 失败）、删除（终态）；
- 批量：清空已完成；
- 刷新：复用现有 WS Hub（`hub.Publish("cloud_download", …)`），断线时降级为 5s 轮询。

### 8.2 路由与导航

- `appRoutes.tsx`：`{ path: 'cloud-downloads', element: <CloudDownloadPage />, adminOnly: true }`
- `layoutNavigation.ts`：`LAYOUT_NAV_ITEMS` 中 `STRM 管理` 之后加 `{ to: '/cloud-downloads', label: '云下载', icon: CloudDownload, adminOnly: true }`
- `StrmManagePage` 顶部工具区加一个「云下载」跳转按钮，形成入口闭环。

---

## 9. 权限与安全

### 9.1 鉴权

首期仅管理员：路由挂在 `admin` 组，`AdminRequired` 中间件已覆盖。

### 9.2 输入安全

- 链接只做**字符串拼接转发**，服务端绝不主动发起对这些 URL 的请求（无 SSRF 面）；
- `.torrent` 文件仅落临时目录 → 上传 115 → 删除，不本地解析 bencode（P1 若要预览文件名需加 bencode 解析，届时必须限大小与深度）；
- 上传文件名禁用 `..` / 路径分隔符；
- `save_path` 本期固定为空（留字段），避免路径穿越争议。

### 9.3 后续（P2）

`model.UserPermission` 增加 `CanUseCloudDownload bool`，配套 handler 从 `AdminRequired` 换成权限中间件，并给普通用户做配额上限（否则一个用户可以把账号的 115 云下载配额打空）。

---

## 10. 测试计划

### 10.1 单元测试（必须）

| 目标 | 用例 |
| :--- | :--- |
| 输入解析 | 多行混合、空行、重复链接、首尾空白、大小写协议头、>115 条切批、非法行跳过并计数 |
| 状态映射 | status -1/0/1/2 → failed/downloading/downloading/succeeded；缺失 info_hash 时 url 回退命中；大小写 info_hash |
| 错误码映射 | 1000012 / 91006 / 20018 / 990002 文案与是否可重试 |
| 提交校验 | `RemotePath="0"`、账号禁用、同步目录禁用、配额耗尽 → 全部拒绝且不产生 115 请求 |
| 去抖 | 30s 内 3 个任务完成 → 只触发 1 次 `StartSync` |
| 等待同步 | `IsSyncRunning=true` → 全部 waiting_sync；结束后成功发起 |
| 阶段隔离 | 同步失败不改变 Status；扫描失败不改变 syncing 记录 |
| 僵尸恢复 | 重启后 syncing/scanning/scraping → pending |

测试风格沿用仓库现状：同包 `*_test.go`、`t.Context()`、httptest 模拟 115（参考 `internal/service/cloud115/cloud115_test.go` 的 mock 模式）。

### 10.2 集成测试

- `httptest` 起伪 115（`/open/offline/*`）：验证提交 → 轮询 → 状态流转 → pipeline 触发（Strm 用 fake hook，Scanner/Scraper 用桩）。
- 现有测试回归：`go test ./internal/service/... ./internal/repository/... ./internal/handler/...`。

### 10.3 真机验收（不可省）

| # | 步骤 | 期望 |
| :--- | :--- | :--- |
| 1 | 提交一条小体积磁力（<200MB）绑定某 115 同步目录 | 任务 5s 内变 `submitted` |
| 2 | 等 115 完成 | 任务变 `succeeded`，随后 `syncing` |
| 3 | 同步完成 | 本地目录出现新 `.strm` 文件 |
| 4 | 自动扫描 | 媒体库新出现对应条目 |
| 5 | 自动刮削 | 条目 `scrape_status` 变 `matched`，海报正确 |
| 6 | 提交 `.torrent` 文件 | 同上，且 115 端任务名与种子内任务名一致 |
| 7 | 提交 ed2k 链接 | 同上（ed2k 无 info_hash，验证 url 回退匹配） |
| 8 | 提交 120 条链接 | 自动切 2 批，逐条结果完整 |

---

## 11. 待实测确认项

| # | 项 | 影响 | 处理 |
| :--- | :--- | :--- | :--- |
| 1 | `add_task_bt` 的 `wanted` 是 0-based 还是 1-based | 后续若开放选文件，索引拼接依赖此语义 | 真机用一个 3 文件种子验证；当前实现统一传 `"0"`，不受此影响 |
| 2 | `wanted="0"` 是「全部」还是「第 0 个」 | 决定 `.torrent` 任务是否只下一个文件 | **必测**：当前实现依赖它表示「全部」 |
| 3 | `MkDir` 对已存在目录的返回 | 暂存目录重复创建 | 先 `FindNamedContentInParent` 查找，未命中再 `MkDir`；两者都失败则回退到目标同步目录 |
| 4 | 当前 115 应用是否已开通云下载权限 | 全功能可用性 | 提交前 `get_quota_info` 探测；无权限时给出「去开放平台申请」指引 |
| 5 | 磁力整体提交时 115 默认勾选哪些文件 | 是否漏下样片/字幕 | 不影响链路，只在文档说明 |
| 6 | `get_task_list` 单页实际返回条数 | 翻页策略 | 首轮实测后调整 `max_pages` 默认值 |

---

## 11.5 实现状态（P0 已落地）

已完成（后端 + 前端最小可用）：

| 层 | 文件 | 说明 |
| :--- | :--- | :--- |
| 模型 | `internal/model/cloud_download.go` | `CloudDownloadTask` + 状态/阶段常量，已注册 `AllModels()` |
| 仓储 | `internal/repository/cloud_download_repository.go` | 分页查询、活跃任务去重、阶段推进、僵尸阶段重置 |
| 115 协议 | `internal/service/cloud115/offline.go` | 7 个接口封装；`client.go` 新增 multipart 请求构造并复用限流/刷新/错误封装 |
| 服务 | `internal/service/cloud_download*.go` | 输入解析、提交、轮询、去抖、Pipeline（同步→扫描→刮削） |
| 调度 | `internal/service/scheduler_cloud_download.go` | `cloud_download_poll` 周期任务 |
| 打通 | `internal/service/strm_service.go` / `strm_sync.go` | `Open115Client` 复用缓存客户端；`SetSyncFinishHook` 异步回调 |
| HTTP | `internal/handler/cloud_download.go` | 9 个管理员接口 + multipart 种子上传 |
| 前端 | `web/src/pages/CloudDownloadPage.tsx` 等 | 提交面板、配额、任务列表、重试/取消/删除、5s 自动刷新 |

P0 范围外（保留为 P1/P2）：

- 设置项 UI（当前通过 `Setting` 表键值生效，默认轮询 60s / 去抖 30s / 超时 72h）；
- WebSocket 实时进度（当前为 5s 轮询）；
- 普通用户权限位与单用户配额上限；
- 远端任务清理（`del_task` / `clear_task`）在 UI 上无入口（取消任务时会尽力调用 `del_task`）。

验证证据：

- `go build ./...`、`go vet` 通过；
- 新增单测：`internal/service/cloud115/offline_test.go`（7 项，含 multipart 格式与 `percentDone` 解析）、`internal/service/cloud_download_test.go`（9 项，含解析/去抖/目录匹配/校验）；
- 前端 `tsc --noEmit`、`eslint`、`vite build` 通过；
- 3 个既有测试在本机 HEAD 上同样失败（Windows 文件占用与 SQLite `VACUUM INTO` 环境问题），已用独立 worktree 复核，与本次改动无关。

## 12. 分期与验收

### P0（可跑通，约 1 个迭代）

- 模型 + 仓储 + 迁移注册
- `cloud115/offline.go`（4 个接口：add_task_urls / get_task_list / get_quota_info / torrent + add_task_bt）
- 提交（磁力/ed2k/http）+ 轮询 + 状态机
- STRM finish hook + Pipeline（同步 → 扫描 → 刮削）
- 调度 job + 设置项
- 前端最小页：提交面板 + 任务列表（阶段徽标、重试、删除）
- **验收**：10.3 的步骤 1~5、7、8 通过

### P1（体验完善）

- `.torrent` 上传端点 + 全选提交（10.3 步骤 6）
- 配额条、提交结果逐条明细
- 去抖、僵尸恢复、超时熔断
- WS 实时进度
- 分页列表 + 筛选（状态/目录）

### P2（可选）

- 远端任务清理（`del_task` / `clear_task`）
- 完成后删除暂存 `.torrent`
- 普通用户权限位 + 单用户配额上限
- Telegram 完成通知

### P3（本期不做，记录候选）

- 磁力选文件（服务端 DHT 抓 .torrent）
- 完成后按 file_id 裁剪不需要的文件

---

## 13. 风险与回滚

| # | 风险 | 影响 | 缓解 |
| :--- | :--- | :--- | :--- |
| R1 | 115 应用未开通云下载权限 | 功能不可用 | 提交前探测 + 明确文案；不影响现有 STRM 功能 |
| R2 | 令牌双实例互相作废 | 账号被标「授权已失效」 | **强约束 K1**：只复用 StrmService 缓存的客户端 |
| R3 | 频繁同步打爆 CPU/115 限流 | 服务卡顿、风控 | 去抖 30s + 最小间隔 + 同步运行中等待 + throttle_manager |
| R4 | 目录错配（`wp_path_id` 不在同步目录） | 下了但永远扫不到 | 提交时强制 `RemotePath` 非空非 0；UI 只允许选同步目录，不放开自由选目录 |
| R5 | finish hook panic 拖垮同步 | 同步记录永远 running | hook 异步 + `helper.Go` + recover + 独立超时 |
| R6 | 新增表迁移失败 | 启动失败 | `AutoMigrate` 加表无破坏性；出问题回滚只需删表 + 移除注册 |
| R7 | 单页页数过多导致轮询耗时 | 轮询堆积 | `max_pages` 上限；超限记 warning 并下轮继续 |

**回滚方式**：移除路由与侧边栏入口即可（功能面隐藏）；表可保留（不影响其它子系统），或手动 `DROP TABLE cloud_download_tasks`。

---

## 14. 附录：核对依据

| 结论 | 依据位置 |
| :--- | :--- |
| 115 开放平台离线接口形态与参数 | `115doc/115开放平台/API列表/云下载/*.md` |
| STRM 同步入口与运行互斥 | `internal/service/strm_sync.go:82-138` |
| 同步收尾无回调 | `internal/service/strm_sync.go:323`（`finishSync`） |
| 115 目录整树拉取成本 | `internal/service/strm_sync.go:682`（`walk115Flat`） |
| 令牌刷新落库与单实例要求 | `internal/service/strm_service.go:524-556`、`strm_115_oauth.go:438` |
| 115 provider 断言复用范式 | `internal/service/strm_sync.go:378`（`cloud.OpenAPI115Provider`） |
| 上传种子可拿 PickCode | `internal/service/cloud115/upload.go:95-102,217` |
| 刮削候选只挑 pending | `internal/service/scraper_library.go:183-207` |
| 扫描自动刮削开关 | `internal/service/scanner_prune.go:279`、`scanner_post_scan.go:19` |
| 周期任务注册范式 | `internal/service/scheduler.go:118-210` |
| 模型迁移注册 | `internal/model/model.go:37`、`internal/database/schema_migration.go:34` |
| 服务装配顺序 | `internal/service/service_builder.go:148` |
