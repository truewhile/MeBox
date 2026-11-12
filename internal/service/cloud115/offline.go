// 115 开放平台「云下载」（离线下载）API 封装。
//
// 文档（仓库内离线副本）：115doc/115开放平台/API列表/云下载/
//
// 能力边界（已核对官方文档，勿凭客户端印象假设）：
//   - add_task_urls 支持 HTTP(S)/FTP/磁力/电驴，一次最多 115 条，无「选择文件」参数；
//   - 选择文件只能走 BT 路径：torrent（解析已上传种子）→ add_task_bt（wanted 传索引）；
//   - torrent 的入参是「种子文件在 115 的 SHA1 + 提取码」，不接受 magnet。
//
// 本文件只做协议封装：不碰数据库、不做编排、不判断业务状态，方便单测。
package cloud115

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// 云下载接口路径。
const (
	OfflinePathAddTaskURLs = "/open/offline/add_task_urls"
	OfflinePathTorrent     = "/open/offline/torrent"
	OfflinePathAddTaskBT   = "/open/offline/add_task_bt"
	OfflinePathTaskList    = "/open/offline/get_task_list"
	OfflinePathQuotaInfo   = "/open/offline/get_quota_info"
	OfflinePathDelTask     = "/open/offline/del_task"
	OfflinePathClearTask   = "/open/offline/clear_task"
)

// OfflineMaxURLsPerBatch 是 add_task_urls 单批链接上限（官方文档：1000011 超 115 个）。
const OfflineMaxURLsPerBatch = 115

// 云下载业务错误码。
const (
	OfflineCodeOK              = 0
	OfflineCodeQuotaUsedUp     = 1000012 // 云下载配额已用完
	OfflineCodeStorageFull     = 91006   // 存储空间不足
	OfflineCodeTorrentMissing  = 20018   // 文件不存在或已删除（种子无效）
	OfflineCodeTooManyURLs     = 1000011 // 链接数量超过 115 个
	OfflineCodeParamError      = 990002  // 参数错误
	OfflineCodeOperationFailed = 980004  // 操作失败，可重试
)

// 云下载任务状态（get_task_list → data.tasks[].status）。
const (
	OfflineTaskStatusFailed      = -1 // 下载失败
	OfflineTaskStatusAllocating  = 0  // 分配中
	OfflineTaskStatusDownloading = 1  // 下载中
	OfflineTaskStatusDone        = 2  // 下载成功
)

// 清空任务的类型（clear_task → flag）。
const (
	OfflineClearDone         = 0
	OfflineClearAll          = 1
	OfflineClearFailed       = 2
	OfflineClearRunning      = 3
	OfflineClearDoneWithFile = 4
	OfflineClearAllWithFile  = 5
)

// OfflineTaskURLResult 是 add_task_urls 返回的逐条结果。
type OfflineTaskURLResult struct {
	State    bool   `json:"state"`
	Code     int    `json:"code"`
	Message  string `json:"message"`
	InfoHash string `json:"info_hash"`
	URL      string `json:"url"`
}

// OfflineTorrentFile 是种子解析结果中的单个文件。
type OfflineTorrentFile struct {
	Size   int64  `json:"size"`
	Path   string `json:"path"`
	Wanted int    `json:"wanted"` // 115 认为该文件是否默认选中
}

// OfflineTorrentInfo 是 /offline/torrent 的解析结果。
type OfflineTorrentInfo struct {
	FileSize  int64                `json:"file_size"`
	Name      string               `json:"torrent_name"`
	FileCount int                  `json:"file_count"`
	InfoHash  string               `json:"info_hash"`
	Files     []OfflineTorrentFile `json:"torrent_filelist"`
}

// OfflineTask 是云下载任务列表中的一条任务。
type OfflineTask struct {
	InfoHash     string `json:"info_hash"`
	AddTime      int64  `json:"add_time"`
	PercentDone  int    `json:"percentDone"` // 注意：官方字段是驼峰，其余为 snake_case
	Size         int64  `json:"size"`
	Name         string `json:"name"`
	LastUpdate   int64  `json:"last_update"`
	FileID       string `json:"file_id"`
	DeleteFileID string `json:"delete_file_id"`
	Status       int    `json:"status"`
	URL          string `json:"url"`
	WpPathID     string `json:"wp_path_id"`
	Def2         int    `json:"def2"`
	PlayLong     int    `json:"play_long"`
	CanAppeal    int    `json:"can_appeal"`
}

// OfflineTaskPage 是分页任务列表。
type OfflineTaskPage struct {
	Page      int           `json:"page"`
	PageCount int           `json:"page_count"`
	Count     int           `json:"count"`
	Tasks     []OfflineTask `json:"tasks"`
}

// OfflineQuotaPackage 是某一类配额的使用情况。
type OfflineQuotaPackage struct {
	Surplus    int                  `json:"surplus"`
	Used       int                  `json:"used"`
	Count      int                  `json:"count"`
	Name       string               `json:"name"`
	ExpireInfo []OfflineQuotaExpire `json:"expire_info"`
}

// OfflineQuotaExpire 是配额过期明细。
type OfflineQuotaExpire struct {
	Surplus    int   `json:"surplus"`
	ExpireTime int64 `json:"expire_time"`
}

// OfflineQuota 是云下载配额总览。
type OfflineQuota struct {
	Package []OfflineQuotaPackage `json:"package"`
	Count   int                   `json:"count"`
	Surplus int                   `json:"surplus"`
	Used    int                   `json:"used"`
}

// OfflineBTRequest 是 add_task_bt 的请求参数。
type OfflineBTRequest struct {
	InfoHash    string
	Wanted      string // 半角逗号分隔的文件索引；官方文档：「可以为 0，但不能为空」
	SavePath    string // 相对 wp_path_id 的保存路径，可为空
	TorrentSha1 string
	PickCode    string
	WpPathID    string
}

// AddTaskURLs 批量添加链接任务（HTTP/FTP/磁力/电驴），单批 ≤115 条。
// 返回逐条结果：整体请求成功但个别链接失败是常态，调用方必须逐条判断 State。
// 注意：data[].info_hash 只在受理成功时才有值，链接类任务可能为空。
func (c *OpenClient) AddTaskURLs(ctx context.Context, urls []string, wpPathID string) ([]OfflineTaskURLResult, error) {
	cleaned := make([]string, 0, len(urls))
	for _, u := range urls {
		if u = strings.TrimSpace(u); u != "" {
			cleaned = append(cleaned, u)
		}
	}
	if len(cleaned) == 0 {
		return nil, fmt.Errorf("115: 云下载链接为空")
	}
	if len(cleaned) > OfflineMaxURLsPerBatch {
		return nil, fmt.Errorf("115: 单批云下载链接最多 %d 条，当前 %d 条", OfflineMaxURLsPerBatch, len(cleaned))
	}
	if wpPathID = strings.TrimSpace(wpPathID); wpPathID == "" {
		wpPathID = "0"
	}
	fields := map[string]string{
		"urls":       strings.Join(cleaned, "\n"),
		"wp_path_id": wpPathID,
	}
	resp, err := c.doAuthMultipart(ctx, "POST", ProAPIBase+OfflinePathAddTaskURLs, fields, 1)
	if err != nil {
		return nil, err
	}
	var results []OfflineTaskURLResult
	if err := unmarshalOfflineData(resp, &results); err != nil {
		return nil, err
	}
	return results, nil
}

// ParseTorrent 解析已上传到 115 的 BT 种子文件。
// torrentSha1/pickCode 必须来自该账号空间内的种子文件（上传接口返回值）。
func (c *OpenClient) ParseTorrent(ctx context.Context, torrentSha1, pickCode string) (*OfflineTorrentInfo, error) {
	torrentSha1 = strings.TrimSpace(torrentSha1)
	pickCode = strings.TrimSpace(pickCode)
	if torrentSha1 == "" || pickCode == "" {
		return nil, fmt.Errorf("115: 解析种子需要 torrent_sha1 与 pick_code")
	}
	fields := map[string]string{
		"torrent_sha1": torrentSha1,
		"pick_code":    pickCode,
	}
	resp, err := c.doAuthMultipart(ctx, "POST", ProAPIBase+OfflinePathTorrent, fields, 1)
	if err != nil {
		return nil, err
	}
	var info OfflineTorrentInfo
	if err := unmarshalOfflineData(resp, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// AddTaskBT 根据已解析的种子添加云下载 BT 任务。
// wanted 传文件索引（半角逗号分隔）；官方文档允许传 "0"，但语义未在文档中写明，
// 因此调用方应优先传解析结果推导出的索引列表（见 service 层）。
func (c *OpenClient) AddTaskBT(ctx context.Context, req OfflineBTRequest) error {
	infoHash := strings.TrimSpace(req.InfoHash)
	torrentSha1 := strings.TrimSpace(req.TorrentSha1)
	pickCode := strings.TrimSpace(req.PickCode)
	if infoHash == "" || torrentSha1 == "" || pickCode == "" {
		return fmt.Errorf("115: 添加 BT 任务缺少 info_hash/torrent_sha1/pick_code")
	}
	wanted := strings.TrimSpace(req.Wanted)
	if wanted == "" {
		wanted = "0"
	}
	wpPathID := strings.TrimSpace(req.WpPathID)
	if wpPathID == "" {
		wpPathID = "0"
	}
	fields := map[string]string{
		"info_hash":    infoHash,
		"wanted":       wanted,
		"save_path":    strings.TrimSpace(req.SavePath),
		"torrent_sha1": torrentSha1,
		"pick_code":    pickCode,
		"wp_path_id":   wpPathID,
	}
	if _, err := c.doAuthMultipart(ctx, "POST", ProAPIBase+OfflinePathAddTaskBT, fields, 1); err != nil {
		return err
	}
	return nil
}

// GetOfflineTaskList 分页获取云下载任务列表（page 从 1 开始）。
func (c *OpenClient) GetOfflineTaskList(ctx context.Context, page int) (*OfflineTaskPage, error) {
	if page < 1 {
		page = 1
	}
	resp, err := c.doAuthJSON(ctx, "GET", ProAPIBase+OfflinePathTaskList, map[string]string{
		"page": fmt.Sprint(page),
	}, 1)
	if err != nil {
		return nil, err
	}
	var list OfflineTaskPage
	if err := unmarshalOfflineData(resp, &list); err != nil {
		return nil, err
	}
	return &list, nil
}

// GetOfflineQuota 获取云下载配额信息。
func (c *OpenClient) GetOfflineQuota(ctx context.Context) (*OfflineQuota, error) {
	resp, err := c.doAuthJSON(ctx, "GET", ProAPIBase+OfflinePathQuotaInfo, nil, 1)
	if err != nil {
		return nil, err
	}
	var quota OfflineQuota
	if err := unmarshalOfflineData(resp, &quota); err != nil {
		return nil, err
	}
	return &quota, nil
}

// DeleteOfflineTask 删除指定云下载任务（按 info_hash）。
// delSourceFile=true 时同时删除已下载的源文件。
func (c *OpenClient) DeleteOfflineTask(ctx context.Context, infoHash string, delSourceFile bool) error {
	infoHash = strings.TrimSpace(infoHash)
	if infoHash == "" {
		return fmt.Errorf("115: 删除云下载任务需要 info_hash")
	}
	del := "0"
	if delSourceFile {
		del = "1"
	}
	fields := map[string]string{
		"info_hash":       infoHash,
		"del_source_file": del,
	}
	_, err := c.doAuthMultipart(ctx, "POST", ProAPIBase+OfflinePathDelTask, fields, 1)
	return err
}

// ClearOfflineTasks 按类型清空云下载任务。
func (c *OpenClient) ClearOfflineTasks(ctx context.Context, flag int) error {
	fields := map[string]string{"flag": fmt.Sprint(flag)}
	_, err := c.doAuthMultipart(ctx, "POST", ProAPIBase+OfflinePathClearTask, fields, 1)
	return err
}

// unmarshalOfflineData 解析 data 字段。115 的 /offline/* 接口在 state=true 时
// 仍可能给出空 data（例如 add_task_bt 成功时 data 为空数组），调用方需容忍零值。
func unmarshalOfflineData(resp *RespBase, out any) error {
	if resp == nil || len(resp.Data) == 0 {
		return nil
	}
	if err := json.Unmarshal(resp.Data, out); err != nil {
		return fmt.Errorf("115: 解析云下载响应失败：%w", err)
	}
	return nil
}
