// 115 云下载（离线下载）服务：提交链接、轮询 115 任务、驱动后处理管线
// （STRM 增量同步 → 媒体库扫描 → 自动刮削）。
//
// 职责边界：
//   - 115 协议细节在 internal/service/cloud115/offline.go；
//   - 本服务只做编排与状态机，不直接写 .strm 文件、不直接解析媒体；
//   - 115 客户端必须经 StrmService.Open115Client 取得，禁止自建（见该方法注释）。
package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/helper"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
	"github.com/truewhile/MeBox/internal/service/cloud115"
)

// 云下载设置键（存于 Setting 表，cloud_download.* 前缀）。
const (
	CloudDownloadSettingPollInterval = "cloud_download.poll_interval_sec"
	CloudDownloadSettingSyncDebounce = "cloud_download.sync_debounce_sec"
	CloudDownloadSettingAutoScrape   = "cloud_download.auto_scrape"
	CloudDownloadSettingTimeoutHours = "cloud_download.task_timeout_hours"
	CloudDownloadSettingTorrentMaxMB = "cloud_download.torrent_max_mb"
	CloudDownloadSettingMaxPages     = "cloud_download.max_pages"
)

// 默认值与取值范围。
const (
	cloudDownloadDefaultPollInterval = 60
	cloudDownloadDefaultDebounce     = 30
	cloudDownloadDefaultTimeoutHours = 72
	cloudDownloadDefaultTorrentMaxMB = 20
	cloudDownloadDefaultMaxPages     = 10

	cloudDownloadMinPollInterval = 30
	cloudDownloadMaxPollInterval = 600
	cloudDownloadMaxDebounce     = 300

	// cloudDownloadPipelineTick 是后处理推进的扫描周期（去抖窗口以秒计，
	// 15s 足够把「等窗口结束」的延迟控制在可接受范围）。
	cloudDownloadPipelineTick = 15 * time.Second

	// cloudDownloadTorrentTempPrefix 是种子临时文件前缀（上传后立即删除）。
	cloudDownloadTorrentTempPrefix = "mebox-clouddl-"
)

// CloudDownloadService 提供云下载任务的全生命周期管理。
type CloudDownloadService struct {
	log     *zap.Logger
	repo    *repository.Container
	strm    *StrmService
	scanner *ScannerService
	scraper *ScraperService

	stopOnce sync.Once
	stopCh   chan struct{}
	baseCtx  context.Context

	// 去抖状态：同一同步目录在窗口内的多次完成合并成一次同步。
	debounceMu  sync.Mutex
	lastTrigger map[string]time.Time
}

// NewCloudDownloadService 构造云下载服务。
// strm/scanner/scraper 必须已构造完成（见 service_builder 的装配顺序）。
func NewCloudDownloadService(
	log *zap.Logger,
	repo *repository.Container,
	strm *StrmService,
	scanner *ScannerService,
	scraper *ScraperService,
) *CloudDownloadService {
	return &CloudDownloadService{
		log:         log,
		repo:        repo,
		strm:        strm,
		scanner:     scanner,
		scraper:     scraper,
		stopCh:      make(chan struct{}),
		baseCtx:     context.Background(),
		lastTrigger: map[string]time.Time{},
	}
}

// Start 启动后处理循环，并把上次进程退出时停在中间阶段的任务重置为待推进。
func (s *CloudDownloadService) Start(ctx context.Context) {
	s.baseCtx = ctx
	if s.repo != nil && s.repo.CloudDownload != nil {
		if n, err := s.repo.CloudDownload.ResetStaleStages(ctx); err != nil {
			s.log.Warn("reset stale cloud download stages failed", zap.Error(err))
		} else if n > 0 {
			s.log.Info("cloud download stages reset after restart", zap.Int64("count", n))
		}
	}
	helper.Go(s.log, "cloudDownload.pipelineLoop", func() {
		ticker := time.NewTicker(cloudDownloadPipelineTick)
		defer ticker.Stop()
		for {
			select {
			case <-s.stopCh:
				return
			case <-ticker.C:
				s.runPipelineOnce(s.baseCtx)
			}
		}
	})
}

// Stop 停止后处理循环。
func (s *CloudDownloadService) Stop() {
	s.stopOnce.Do(func() { close(s.stopCh) })
}

// ─── 设置读取 ─────────────────────────────────────────────────────────────────

func (s *CloudDownloadService) settingInt(key string, fallback, min, max int) int {
	if s.repo == nil || s.repo.Setting == nil {
		return fallback
	}
	raw, err := s.repo.Setting.Get(context.Background(), key)
	if err != nil || strings.TrimSpace(raw) == "" {
		return fallback
	}
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func (s *CloudDownloadService) settingBool(key string, fallback bool) bool {
	if s.repo == nil || s.repo.Setting == nil {
		return fallback
	}
	raw, err := s.repo.Setting.Get(context.Background(), key)
	if err != nil {
		return fallback
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return fallback
	case "1", "true", "yes", "on", "enabled":
		return true
	default:
		return false
	}
}

// pollInterval 轮询 115 任务列表的间隔。
func (s *CloudDownloadService) pollInterval() time.Duration {
	sec := s.settingInt(CloudDownloadSettingPollInterval, cloudDownloadDefaultPollInterval,
		cloudDownloadMinPollInterval, cloudDownloadMaxPollInterval)
	return time.Duration(sec) * time.Second
}

// debounceWindow 同一同步目录的完成去抖窗口。
func (s *CloudDownloadService) debounceWindow() time.Duration {
	sec := s.settingInt(CloudDownloadSettingSyncDebounce, cloudDownloadDefaultDebounce, 0, cloudDownloadMaxDebounce)
	return time.Duration(sec) * time.Second
}

// autoScrapeEnabled 是否在同步完成后自动刮削。
func (s *CloudDownloadService) autoScrape() bool {
	return s.settingBool(CloudDownloadSettingAutoScrape, true)
}

// taskTimeout 下载超时判定。
func (s *CloudDownloadService) taskTimeout() time.Duration {
	hours := s.settingInt(CloudDownloadSettingTimeoutHours, cloudDownloadDefaultTimeoutHours, 1, 24*30)
	return time.Duration(hours) * time.Hour
}

// torrentMaxBytes 种子上传大小上限。
func (s *CloudDownloadService) torrentMaxBytes() int64 {
	mb := s.settingInt(CloudDownloadSettingTorrentMaxMB, cloudDownloadDefaultTorrentMaxMB, 1, 200)
	return int64(mb) << 20
}

// maxPollPages 单轮轮询最多翻页数。
func (s *CloudDownloadService) maxPollPages() int {
	return s.settingInt(CloudDownloadSettingMaxPages, cloudDownloadDefaultMaxPages, 1, 100)
}

// ─── 查询与管理 ───────────────────────────────────────────────────────────────

// CloudDownloadTaskView 是列表返回的任务视图（附带展示用的派生字段）。
type CloudDownloadTaskView struct {
	model.CloudDownloadTask
	SyncPathName      string `json:"sync_path_name,omitempty"`
	RemoteDisplayPath string `json:"remote_display_path,omitempty"`
	LocalPath         string `json:"local_path,omitempty"`
}

// CloudDownloadList 是分页列表结果。
type CloudDownloadList struct {
	Items    []CloudDownloadTaskView `json:"items"`
	Total    int64                   `json:"total"`
	Page     int                     `json:"page"`
	PageSize int                     `json:"page_size"`
}

// List 分页查询云下载任务，并补充同步目录的展示信息。
func (s *CloudDownloadService) List(ctx context.Context, q repository.CloudDownloadQuery) (*CloudDownloadList, error) {
	rows, total, err := s.repo.CloudDownload.List(ctx, q)
	if err != nil {
		return nil, err
	}
	paths := map[string]*model.StrmSyncPath{}
	items := make([]CloudDownloadTaskView, 0, len(rows))
	for i := range rows {
		v := CloudDownloadTaskView{CloudDownloadTask: rows[i]}
		if p, ok := paths[rows[i].SyncPathID]; ok {
			if p != nil {
				v.SyncPathName, v.RemoteDisplayPath, v.LocalPath = p.Name, p.RemoteDisplayPath, p.LocalPath
			}
		} else if s.repo.StrmSyncPath != nil {
			p, err := s.repo.StrmSyncPath.FindByID(ctx, rows[i].SyncPathID)
			if err == nil {
				paths[rows[i].SyncPathID] = p
				if p != nil {
					v.SyncPathName, v.RemoteDisplayPath, v.LocalPath = p.Name, p.RemoteDisplayPath, p.LocalPath
				}
			}
		}
		items = append(items, v)
	}
	page := q.Page
	if page < 1 {
		page = 1
	}
	pageSize := q.PageSize
	if pageSize <= 0 {
		pageSize = 20
	}
	return &CloudDownloadList{Items: items, Total: total, Page: page, PageSize: pageSize}, nil
}

// Get 按 ID 取单条任务。
func (s *CloudDownloadService) Get(ctx context.Context, id string) (*model.CloudDownloadTask, error) {
	t, err := s.repo.CloudDownload.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, errors.New("云下载任务不存在")
	}
	return t, nil
}

// Cancel 取消任务：先尽力删除 115 侧任务，再把本地状态置为已取消。
// 115 已成功下载的任务无法取消（文件已入库），只能删除记录。
func (s *CloudDownloadService) Cancel(ctx context.Context, id string) error {
	t, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if isCloudDownloadTerminal(t.Status) {
		return errors.New("任务已结束，无法取消")
	}
	if strings.TrimSpace(t.InfoHash) != "" {
		client, cerr := s.strm.Open115Client(ctx, t.AccountID)
		if cerr != nil {
			s.log.Warn("cloud download cancel: open 115 client failed",
				zap.String("task_id", t.ID), zap.Error(cerr))
		} else if derr := client.DeleteOfflineTask(ctx, t.InfoHash, false); derr != nil {
			// 远端删除失败不阻断本地取消：否则用户永远清不掉一条僵尸记录。
			s.log.Warn("cloud download cancel: delete remote task failed",
				zap.String("task_id", t.ID), zap.String("info_hash", t.InfoHash), zap.Error(derr))
		}
	}
	return s.repo.CloudDownload.UpdateFields(ctx, t.ID, map[string]any{
		"status":      model.CloudDownloadCanceled,
		"stage":       model.CloudDownloadStageNA,
		"stage_error": "任务已取消",
	})
}

// Retry 重新提交失败任务：链接类重新走提交接口，BT 类用已保存的种子参数重提。
func (s *CloudDownloadService) Retry(ctx context.Context, id string) error {
	t, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if t.Status != model.CloudDownloadFailed {
		return errors.New("只有失败的任务可以重试")
	}
	if t.SourceType == model.CloudDownloadSourceTorrent {
		if strings.TrimSpace(t.InfoHash) == "" || strings.TrimSpace(t.TorrentSha1) == "" || strings.TrimSpace(t.PickCode) == "" {
			return errors.New("该种子任务的提交参数不完整，请重新上传 .torrent 文件")
		}
		client, err := s.strm.Open115Client(ctx, t.AccountID)
		if err != nil {
			return err
		}
		if err := client.AddTaskBT(ctx, cloud115.OfflineBTRequest{
			InfoHash:    t.InfoHash,
			Wanted:      "0",
			TorrentSha1: t.TorrentSha1,
			PickCode:    t.PickCode,
			WpPathID:    t.WpPathID,
		}); err != nil {
			return err
		}
		return s.repo.CloudDownload.UpdateFields(ctx, t.ID, map[string]any{
			"status":      model.CloudDownloadSubmitted,
			"error":       "",
			"retry_count": t.RetryCount + 1,
		})
	}
	if strings.TrimSpace(t.SourceURL) == "" {
		return errors.New("该任务缺少原始链接，无法重试")
	}
	// 复用提交链路：新建一条记录会让历史重复，这里直接把原记录复位再提交。
	if err := s.repo.CloudDownload.UpdateFields(ctx, t.ID, map[string]any{
		"status":      model.CloudDownloadQueued,
		"error":       "",
		"percent":     0,
		"retry_count": t.RetryCount + 1,
		"stage":       model.CloudDownloadStagePending,
		"stage_error": "",
	}); err != nil {
		return err
	}
	res := s.submitLinks(ctx, t.AccountID, t.SyncPathID, t.WpPathID, []cloudDownloadParsedLink{{
		SourceType: t.SourceType,
		URL:        t.SourceURL,
		Key:        t.SourceKey,
	}}, t.ID)
	if len(res.Items) == 1 && !res.Items[0].OK {
		return errors.New(res.Items[0].Error)
	}
	return nil
}

// RetryPipeline 重做失败的后处理阶段（不重新下载）。
func (s *CloudDownloadService) RetryPipeline(ctx context.Context, id string) error {
	t, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if t.Status != model.CloudDownloadSucceeded {
		return errors.New("只有下载成功的任务可以重做后处理")
	}
	if t.Stage != model.CloudDownloadStageFailed && t.Stage != model.CloudDownloadStageNA {
		return errors.New("该任务的后处理未处于失败状态")
	}
	if err := s.repo.CloudDownload.UpdateFields(ctx, t.ID, map[string]any{
		"stage":       model.CloudDownloadStagePending,
		"stage_error": "",
	}); err != nil {
		return err
	}
	helper.Go(s.log, "cloudDownload.retryPipeline", func() {
		s.runPipelineOnce(s.baseCtx)
	})
	return nil
}

// Delete 删除任务记录。活跃任务必须先取消，避免留下无人追问的 115 任务。
func (s *CloudDownloadService) Delete(ctx context.Context, id string) error {
	t, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if !isCloudDownloadTerminal(t.Status) {
		return errors.New("任务进行中，请先取消再删除")
	}
	return s.repo.CloudDownload.Delete(ctx, id)
}

// ClearFinished 清空失败/已取消的任务。
func (s *CloudDownloadService) ClearFinished(ctx context.Context) (int64, error) {
	return s.repo.CloudDownload.ClearFinished(ctx)
}

// Quota 查询当前账号的云下载配额。
func (s *CloudDownloadService) Quota(ctx context.Context, accountID string) (*cloud115.OfflineQuota, error) {
	client, err := s.strm.Open115Client(ctx, accountID)
	if err != nil {
		return nil, err
	}
	return client.GetOfflineQuota(ctx)
}

// ─── 工具 ─────────────────────────────────────────────────────────────────────

// isCloudDownloadTerminal 判断任务是否已到终态。
func isCloudDownloadTerminal(status string) bool {
	switch status {
	case model.CloudDownloadSucceeded, model.CloudDownloadFailed, model.CloudDownloadCanceled:
		return true
	default:
		return false
	}
}

// offlineErrorMessage 把 115 错误码翻译成可操作的中文提示。
func offlineErrorMessage(code int, message string) string {
	switch code {
	case cloud115.OfflineCodeQuotaUsedUp:
		return "115 云下载配额已用尽，请在 115 端获取更多配额后重试"
	case cloud115.OfflineCodeStorageFull:
		return "115 存储空间不足"
	case cloud115.OfflineCodeTorrentMissing:
		return "115 找不到该种子文件（可能已被清理），请重新上传 .torrent"
	case cloud115.OfflineCodeTooManyURLs:
		return "单批链接超过 115 条上限"
	case cloud115.OfflineCodeParamError:
		return "115 拒绝了提交参数：" + message
	case cloud115.OfflineCodeOperationFailed:
		return "115 操作失败，请稍后重试"
	}
	if strings.TrimSpace(message) != "" {
		return message
	}
	return fmt.Sprintf("115 返回错误码 %d", code)
}
