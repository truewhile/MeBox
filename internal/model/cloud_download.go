// 115 云下载（离线下载）任务数据模型。
//
// 一条 CloudDownloadTask 表示用户提交的一个离线下载来源（磁力 / BT 种子 /
// ed2k / HTTP），并记录它在「115 下载 → STRM 同步 → 媒体库扫描 → 刮削」
// 链路上的位置。下载状态（Status）与后处理阶段（Stage）刻意分成两组字段：
// STRM 同步或刮削失败不能把 115 侧的「已下载成功」改写掉，用户重做后处理时
// 也不需要重新下载。
package model

import "time"

// 云下载任务状态（115 离线下载侧）。
const (
	CloudDownloadQueued      = "queued"      // 本地已建行，尚未提交 115
	CloudDownloadSubmitted   = "submitted"   // 115 已受理
	CloudDownloadDownloading = "downloading" // 115 正在下载（status 0/1）
	CloudDownloadSucceeded   = "succeeded"   // 115 下载完成（status 2）
	CloudDownloadFailed      = "failed"      // 提交被拒 / status -1 / 超时
	CloudDownloadCanceled    = "canceled"    // 用户取消
)

// 云下载后处理阶段（STRM 同步 → 扫描 → 刮削）。
const (
	CloudDownloadStagePending     = "pending"      // 尚未开始（下载未成功）
	CloudDownloadStageWaitingSync = "waiting_sync" // 已成功，等待去抖窗口或等待目录空闲
	CloudDownloadStageSyncing     = "syncing"      // STRM 增量同步中
	CloudDownloadStageScanning    = "scanning"     // 媒体库扫描中
	CloudDownloadStageScraping    = "scraping"     // 刮削已入队
	CloudDownloadStageDone        = "done"         // 全链路完成
	CloudDownloadStageNA          = "na"           // 无法继续（同步目录/媒体库不存在），不算失败
	CloudDownloadStageFailed      = "failed"       // 阶段失败，可用「重做后处理」重试
)

// 云下载来源类型。
const (
	CloudDownloadSourceMagnet  = "magnet"
	CloudDownloadSourceTorrent = "torrent"
	CloudDownloadSourceED2K    = "ed2k"
	CloudDownloadSourceHTTP    = "http"
	CloudDownloadSourceFTP     = "ftp"
)

// CloudDownloadTask 是一条云下载任务。
type CloudDownloadTask struct {
	Base

	AccountID  string `gorm:"size:36;index" json:"account_id"`   // 115 网盘账号（model.StrmAccount）
	SyncPathID string `gorm:"size:36;index" json:"sync_path_id"` // 绑定的 STRM 同步目录
	LibraryID  string `gorm:"size:36;index" json:"library_id"`   // 由本地目录反查得到，可为空
	Provider   string `gorm:"size:32" json:"provider"`           // 固定 cloud115

	SourceType string `gorm:"size:16;index" json:"source_type"` // CloudDownloadSource*
	SourceURL  string `gorm:"type:text" json:"source_url"`      // 原始链接；.torrent 时存上传的文件名
	// SourceKey 是轮询匹配用的归一化键：BT 类任务存大写 info_hash，链接类任务
	// 存 trim 后的 url。提交时即写入，避免每轮重新计算。
	SourceKey string `gorm:"size:512;index" json:"source_key"`
	InfoHash  string `gorm:"size:64;index" json:"info_hash,omitempty"`

	TorrentName string `gorm:"size:512" json:"torrent_name,omitempty"`
	TorrentSha1 string `gorm:"size:64" json:"torrent_sha1,omitempty"` // .torrent 提交路径用
	PickCode    string `gorm:"size:128" json:"pick_code,omitempty"`   // .torrent 提交路径用

	WpPathID string `gorm:"size:64" json:"wp_path_id"`           // 115 目标目录 ID（= syncPath.RemotePath）
	SavePath string `gorm:"size:512" json:"save_path,omitempty"` // 相对 wp_path_id 的子路径（预留）

	Status    string `gorm:"size:16;index" json:"status"` // CloudDownload*
	Percent   int    `json:"percent"`
	SizeBytes int64  `json:"size_bytes"`
	Error     string `gorm:"size:1024" json:"error"`

	Stage        string `gorm:"size:16;index" json:"stage"` // CloudDownloadStage*
	StageError   string `gorm:"size:1024" json:"stage_error"`
	StrmRecordID string `gorm:"size:36" json:"strm_record_id,omitempty"`
	ScanAdded    int    `json:"scan_added"`
	ScrapeQueued int    `json:"scrape_queued"`

	RetryCount  int        `json:"retry_count"`
	LastPollAt  *time.Time `json:"last_poll_at,omitempty"`
	NextPollAt  *time.Time `gorm:"index" json:"next_poll_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}
