// STRM 管理子系统数据模型。
//
// 参考 QMediaSync 的 STRM 同步设计：网盘账号（account）+ 同步目录（sync path）
// 生成 .strm 文件（内容为指向本服务播放端点的一行 URL），视频元数据文件经
// 下载/上传队列与远端网盘双向同步。
package model

import "time"

// Strm 提供方类型。cloud.Local 常量保持一致。
const (
	StrmProvider115        = "cloud115"    // 115 网盘（Cookie + 二维码登录）
	StrmProviderCloudDrive = "clouddrive2" // CloudDrive2（WebDAV 桥接）
	StrmProviderOpenList   = "openlist"    // OpenList / AList 兼容桥接
	StrmProviderLocal      = "local"       // 本地目录（无账号）
	StrmProviderEmbyRemote = "emby_remote" // 远程 Emby 服务器（API 网关聚合挂载，不走 STRM 同步）
)

// StrmAccount 是一个网盘账号（STRM 同步数据源凭据）。
type StrmAccount struct {
	Base
	Name           string     `gorm:"size:128" json:"name"`             // 展示名
	Provider       string     `gorm:"size:32;index" json:"provider"`    // StrmProvider*
	Config         string     `gorm:"type:text" json:"-"`               // JSON：cookie / url / username / password / token
	Enabled        bool       `gorm:"default:true" json:"enabled"`      // 是否启用（禁用后同步跳过）
	LastTestAt     *time.Time `json:"last_test_at"`                     // 最近一次连通性测试
	LastTestResult string     `gorm:"size:512" json:"last_test_result"` // ok 或错误信息
	LastTestOK     bool       `json:"last_test_ok"`
}

// StrmSyncPath 是一条 STRM 同步目录配置：把网盘（或本地）某目录下的视频生成
// .strm 文件写到 LocalPath，元数据按需下载/上传。
type StrmSyncPath struct {
	Base
	Name       string `gorm:"size:128" json:"name"`            // 展示名
	AccountID  string `gorm:"size:36;index" json:"account_id"` // StrmAccount.ID；local 为空
	Provider   string `gorm:"size:32" json:"provider"`         // StrmProvider*（冗余，便于列表展示）
	RemotePath string `gorm:"size:1024" json:"remote_path"`    // 远端目录：115=目录ID，OpenList/CD2=路径，local=源目录
	// RemoteDisplayPath 是远端目录的完整展示路径（如 /电影/剧集）。115 的
	// RemotePath 是目录 ID，用户无法辨认，浏览选择或按 ID 反查时把人类可读
	// 路径存到这里；路径型网盘（CD2/OpenList）与 local 留空（RemotePath 即路径）。
	RemoteDisplayPath string `gorm:"size:1024" json:"remote_display_path"`
	LocalPath         string `gorm:"size:1024" json:"local_path"` // STRM/元数据本地输出目录
	// STRM 链接配置（空值继承全局 strm.* 设置）
	StrmBaseURL     string     `gorm:"size:512" json:"strm_base_url"`                  // 覆盖 strm.base_url
	VideoExt        string     `gorm:"size:512" json:"video_ext"`                      // 逗号分隔，覆盖 strm.video_ext
	MetaExt         string     `gorm:"size:512" json:"meta_ext"`                       // 逗号分隔，覆盖 strm.meta_ext
	ExcludeName     string     `gorm:"size:512" json:"exclude_name"`                   // 逗号分隔，文件名包含即跳过
	MinVideoSizeMB  int64      `json:"min_video_size_mb"`                              // 小于该大小（MB）的视频不生成 STRM
	AddPath         int        `json:"add_path"`                                       // STRM 链接 path 参数：1=完整远端路径 2=仅文件名 3=不带
	DownloadMeta    bool       `gorm:"default:true" json:"download_meta"`              // 同步时下载元数据文件（nfo/图片/字幕）
	UploadMeta      bool       `json:"upload_meta"`                                    // 同步时把本地元数据上传到远端
	DeleteDir       bool       `json:"delete_dir"`                                     // 清理多余文件时删除空目录
	// KeepExt=true 时为每个视频生成 name.mkv.strm / name.mp4.strm（保留全部版本）；
	// false（默认）时同名不同扩展只择优生成一条 name.strm，避免互相覆盖与来回抖动。
	KeepExt bool `json:"keep_ext"`
	Cron            string     `gorm:"size:128" json:"cron"`                           // 5 段 cron 表达式（可选）
	EnableCron      bool       `json:"enable_cron"`                                    // 是否按 Cron 定时同步
	SyncMode        string     `gorm:"size:32;default:'incremental'" json:"sync_mode"` // 默认同步模式：incremental / full
	Enabled         bool       `gorm:"default:true" json:"enabled"`
	LastSyncAt      *time.Time `json:"last_sync_at"`
	LastSyncStatus  string     `gorm:"size:16" json:"last_sync_status"` // idle/running/ok/error/canceled
	LastSyncMessage string     `gorm:"size:1024" json:"last_sync_message"`
}

// STRM 同步类型。
const (
	StrmSyncTypeIncremental = "incremental"
	StrmSyncTypeFull        = "full"
)

// StrmSyncRecord 是一次同步执行的记录。
const (
	StrmSyncRecordPending  = "pending"
	StrmSyncRecordRunning  = "running"
	StrmSyncRecordDone     = "done"
	StrmSyncRecordFailed   = "failed"
	StrmSyncRecordCanceled = "canceled"
)

type StrmSyncRecord struct {
	Base
	SyncPathID string     `gorm:"size:36;index" json:"sync_path_id"`
	SyncType   string     `gorm:"size:32;default:'incremental'" json:"sync_type"` // incremental / full
	Status     string     `gorm:"size:16;index" json:"status"`
	Total      int64      `json:"total"`    // 远端发现的文件总数
	NewStrm    int64      `json:"new_strm"` // 本次新建/更新的 strm 数
	NewMeta    int64      `json:"new_meta"` // 本次入队的元数据下载数
	Uploaded   int64      `json:"uploaded"` // 本次入队的上传数
	Pruned     int64      `json:"pruned"`   // 本次清理的本地多余文件数
	Skipped    int64      `json:"skipped"`  // 内容未变化跳过的 strm 数
	Message    string     `gorm:"size:1024" json:"message"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

// STRM 任务状态。
const (
	StrmTaskPending  = "pending"
	StrmTaskRunning  = "running"
	StrmTaskDone     = "done"
	StrmTaskFailed   = "failed"
	StrmTaskCanceled = "canceled"
)

// StrmDownloadTask 是 strm 元数据下载队列任务（远端网盘 → 本地）。
type StrmDownloadTask struct {
	Base
	SyncPathID string     `gorm:"size:36;index" json:"sync_path_id"`
	AccountID  string     `gorm:"size:36;index" json:"account_id"`
	Provider   string     `gorm:"size:32" json:"provider"`
	FileName   string     `gorm:"size:512" json:"file_name"`
	RemoteRef  string     `gorm:"size:1024" json:"remote_ref"` // 远端文件引用（pickcode / 路径）
	RemoteDir  string     `gorm:"size:1024" json:"remote_dir"`
	LocalPath  string     `gorm:"size:1024" json:"local_path"` // 本地目标文件
	Size       int64      `json:"size"`
	Status     string     `gorm:"size:16;index" json:"status"`
	Error      string     `gorm:"size:1024" json:"error"`
	RetryCount int        `json:"retry_count"`
	NextTryAt  *time.Time `json:"next_try_at"` // 失败重试退避；空或已过则可被认领
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

// StrmUploadTask 是 strm 元数据上传队列任务（本地 → 远端网盘）。
type StrmUploadTask struct {
	Base
	SyncPathID string     `gorm:"size:36;index" json:"sync_path_id"`
	AccountID  string     `gorm:"size:36;index" json:"account_id"`
	Provider   string     `gorm:"size:32" json:"provider"`
	FileName   string     `gorm:"size:512" json:"file_name"`
	LocalPath  string     `gorm:"size:1024" json:"local_path"`  // 本地源文件
	RemotePath string     `gorm:"size:1024" json:"remote_path"` // 远端目标路径
	RemoteRef  string     `gorm:"size:1024" json:"remote_ref"`  // 上传前：远端同名旧文件 ID（逗号分隔，覆盖前先删）；上传成功后：新文件 ID
	Size       int64      `json:"size"`
	Status     string     `gorm:"size:16;index" json:"status"`
	Error      string     `gorm:"size:1024" json:"error"`
	RetryCount int        `json:"retry_count"`
	NextTryAt  *time.Time `json:"next_try_at"` // 失败重试退避；空或已过则可被认领
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

// StrmDirCache 缓存远端网盘目录 ID 与相对路径映射（支持 115 增量同步秒级寻址）。
type StrmDirCache struct {
	Base
	SyncPathID string `gorm:"size:36;index:idx_strm_dir_cache,priority:1" json:"sync_path_id"`
	DirID      string `gorm:"size:128;index:idx_strm_dir_cache,priority:2" json:"dir_id"`
	Path       string `gorm:"size:1024" json:"path"` // 相对根目录的路径
}
