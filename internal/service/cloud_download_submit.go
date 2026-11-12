// 云下载提交：校验目标、配额预检、批量提交链接、.torrent 上传解析后提交。
package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/service/cloud115"
)

// cloudDownloadSeedDirName 是 115 上用于暂存 .torrent 的目录（两级）。
// 官方文档建议把种子放进「云下载/种子文件」，但不是硬性要求；不存在时自动创建。
var cloudDownloadSeedDirPath = []string{"云下载", "种子文件"}

// CloudDownloadSubmit 是链接提交请求。
// AccountID 允许为空：始终以同步目录绑定的账号为准，避免前端传错账号导致
// 「任务挂在 A 账号、文件落到 B 目录」这种无法自动恢复的错配。
type CloudDownloadSubmit struct {
	AccountID  string   `json:"account_id"`
	SyncPathID string   `json:"sync_path_id"`
	URLs       []string `json:"urls"`
}

// CloudDownloadTorrentRequest 是 .torrent 提交请求（文件内容由 handler 读入）。
type CloudDownloadTorrentRequest struct {
	AccountID  string
	SyncPathID string
	FileName   string
	Data       []byte
}

// CloudDownloadTaskResult 是单条来源的提交结果。
type CloudDownloadTaskResult struct {
	TaskID string `json:"task_id,omitempty"`
	Source string `json:"source"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
}

// CloudDownloadSubmitResult 是批量提交结果：部分成功也返回 200 + 明细。
type CloudDownloadSubmitResult struct {
	Accepted int                       `json:"accepted"`
	Rejected int                       `json:"rejected"`
	Items    []CloudDownloadTaskResult `json:"items"`
	Skipped  []cloudDownloadSkipped    `json:"skipped,omitempty"`
}

// Submit 提交一批链接任务。
func (s *CloudDownloadService) Submit(ctx context.Context, req CloudDownloadSubmit) (*CloudDownloadSubmitResult, error) {
	p, err := s.resolveTarget(ctx, req.SyncPathID)
	if err != nil {
		return nil, err
	}
	links, skipped := parseCloudDownloadLinks(req.URLs)
	res := &CloudDownloadSubmitResult{Skipped: skipped}
	if len(links) == 0 {
		for _, sk := range skipped {
			res.Items = append(res.Items, CloudDownloadTaskResult{Source: sk.Raw, Error: sk.Reason})
			res.Rejected++
		}
		return res, nil
	}
	if err := s.ensureQuota(ctx, p.AccountID); err != nil {
		return nil, err
	}
	submitRes := s.submitLinks(ctx, p.AccountID, p.ID, p.RemotePath, links, "")
	submitRes.Skipped = skipped
	return submitRes, nil
}

// SubmitTorrent 上传 .torrent 到 115、解析后提交为 BT 任务。
//
// 本期不做「选择部分文件」：115 开放平台的磁力链路没有文件选择能力，
// .torrent 链路虽然支持 wanted，但产品上暂不提供勾选 UI（见 docs/cloud-download-plan.md）。
// 因此 wanted 传 "0"（官方文档唯一明确允许的取值），语义待真机验证。
func (s *CloudDownloadService) SubmitTorrent(ctx context.Context, req CloudDownloadTorrentRequest) (*CloudDownloadSubmitResult, error) {
	p, err := s.resolveTarget(ctx, req.SyncPathID)
	if err != nil {
		return nil, err
	}
	if len(req.Data) == 0 {
		return nil, errors.New("种子文件为空")
	}
	if int64(len(req.Data)) > s.torrentMaxBytes() {
		return nil, fmt.Errorf("种子文件超过大小上限（%d MB）", s.torrentMaxBytes()>>20)
	}
	if err := s.ensureQuota(ctx, p.AccountID); err != nil {
		return nil, err
	}
	client, err := s.strm.Open115Client(ctx, p.AccountID)
	if err != nil {
		return nil, err
	}

	// 1. 写入临时文件：115 上传接口只接受本地路径。
	tmp, err := os.CreateTemp("", cloudDownloadTorrentTempPrefix+"*.torrent")
	if err != nil {
		return nil, fmt.Errorf("创建种子临时文件失败：%w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(req.Data); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("写入种子临时文件失败：%w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("关闭种子临时文件失败：%w", err)
	}

	// 2. 上传到「云下载/种子文件」；建目录失败时回落到同步目录，避免整条链路失败。
	parentCID, derr := s.ensureSeedDir(ctx, client)
	if derr != nil {
		s.log.Warn("cloud download: ensure seed dir failed, fallback to sync path",
			zap.String("path_id", p.ID), zap.Error(derr))
		parentCID = p.RemotePath
	}
	uploaded, err := client.Upload(ctx, tmpPath, parentCID, "", "")
	if err != nil {
		return nil, fmt.Errorf("上传种子到 115 失败：%w", err)
	}
	if strings.TrimSpace(uploaded.Sha1) == "" || strings.TrimSpace(uploaded.PickCode) == "" {
		return nil, errors.New("115 上传种子未返回 sha1/pick_code")
	}

	// 3. 解析种子（拿 info_hash 与任务名）。
	info, err := client.ParseTorrent(ctx, uploaded.Sha1, uploaded.PickCode)
	if err != nil {
		return nil, fmt.Errorf("解析 BT 种子失败：%w", err)
	}
	infoHash := strings.ToUpper(strings.TrimSpace(info.InfoHash))
	if infoHash == "" {
		return nil, errors.New("115 解析种子未返回 info_hash")
	}

	// 4. 提交 BT 任务。
	if err := client.AddTaskBT(ctx, cloud115.OfflineBTRequest{
		InfoHash:    infoHash,
		Wanted:      "0",
		TorrentSha1: uploaded.Sha1,
		PickCode:    uploaded.PickCode,
		WpPathID:    p.RemotePath,
	}); err != nil {
		return nil, fmt.Errorf("提交 BT 任务失败：%w", err)
	}

	// 5. 落库。
	task := &model.CloudDownloadTask{
		AccountID:   p.AccountID,
		SyncPathID:  p.ID,
		Provider:    model.StrmProvider115,
		SourceType:  model.CloudDownloadSourceTorrent,
		SourceURL:   req.FileName,
		SourceKey:   infoHash,
		InfoHash:    infoHash,
		TorrentName: info.Name,
		TorrentSha1: uploaded.Sha1,
		PickCode:    uploaded.PickCode,
		WpPathID:    p.RemotePath,
		Status:      model.CloudDownloadSubmitted,
		SizeBytes:   info.FileSize,
		Stage:       model.CloudDownloadStagePending,
	}
	if err := s.repo.CloudDownload.Create(ctx, task); err != nil {
		return nil, err
	}
	return &CloudDownloadSubmitResult{
		Accepted: 1,
		Items: []CloudDownloadTaskResult{{
			TaskID: task.ID,
			Source: req.FileName,
			OK:     true,
		}},
	}, nil
}

// resolveTarget 校验同步目录并返回它。云下载只支持 115 同步目录，
// 且远端必须是有效目录 ID（0 表示网盘根目录，落到根目录的离线文件永远扫不到）。
func (s *CloudDownloadService) resolveTarget(ctx context.Context, syncPathID string) (*model.StrmSyncPath, error) {
	syncPathID = strings.TrimSpace(syncPathID)
	if syncPathID == "" {
		return nil, errors.New("请选择目标 STRM 同步目录")
	}
	p, err := s.repo.StrmSyncPath.FindByID(ctx, syncPathID)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, errors.New("STRM 同步目录不存在")
	}
	if !p.Enabled {
		return nil, errors.New("STRM 同步目录已禁用")
	}
	if p.Provider != model.StrmProvider115 {
		return nil, errors.New("只有 115 网盘同步目录支持云下载")
	}
	if strings.TrimSpace(p.AccountID) == "" {
		return nil, errors.New("该同步目录未关联 115 账号")
	}
	wp := strings.TrimSpace(p.RemotePath)
	if wp == "" || wp == "0" {
		return nil, errors.New("同步目录的远端目录无效（不能是网盘根目录）")
	}
	return p, nil
}

// ensureQuota 提交前的配额闸门。配额接口失败不阻断提交：
// 部分应用可能未开放该接口权限，真正的失败由 add_task_* 的错误码兜底。
func (s *CloudDownloadService) ensureQuota(ctx context.Context, accountID string) error {
	client, err := s.strm.Open115Client(ctx, accountID)
	if err != nil {
		return err
	}
	quota, err := client.GetOfflineQuota(ctx)
	if err != nil {
		s.log.Warn("cloud download: quota probe failed, continue submit",
			zap.String("account_id", accountID), zap.Error(err))
		return nil
	}
	if quota != nil && quota.Surplus <= 0 {
		return errors.New("115 云下载配额已用尽，请在 115 端获取更多配额后重试")
	}
	return nil
}

// submitLinks 分批提交链接，并逐条落库。
// existingTaskID 非空时表示「重试单条历史任务」，此时更新原记录而不是新建。
func (s *CloudDownloadService) submitLinks(
	ctx context.Context,
	accountID, syncPathID, wpPathID string,
	links []cloudDownloadParsedLink,
	existingTaskID string,
) *CloudDownloadSubmitResult {
	res := &CloudDownloadSubmitResult{}
	active := map[string]bool{}

	if existingTaskID == "" {
		keys := make([]string, 0, len(links))
		for _, l := range links {
			keys = append(keys, l.Key)
		}
		if dedup, err := s.repo.CloudDownload.FindActiveBySourceKeys(ctx, keys); err == nil {
			active = dedup
		}
		filtered := make([]cloudDownloadParsedLink, 0, len(links))
		for _, l := range links {
			if active[l.Key] {
				res.Items = append(res.Items, CloudDownloadTaskResult{
					Source: l.URL,
					Error:  "该链接已有进行中的任务",
				})
				res.Rejected++
				continue
			}
			filtered = append(filtered, l)
		}
		links = filtered
	}

	if len(links) == 0 {
		return res
	}

	client, err := s.strm.Open115Client(ctx, accountID)
	if err != nil {
		for _, l := range links {
			res.Items = append(res.Items, CloudDownloadTaskResult{Source: l.URL, Error: err.Error()})
			res.Rejected++
			s.persistSubmitFailure(ctx, accountID, syncPathID, wpPathID, l, err.Error(), existingTaskID)
		}
		return res
	}

	for start := 0; start < len(links); start += cloud115.OfflineMaxURLsPerBatch {
		end := start + cloud115.OfflineMaxURLsPerBatch
		if end > len(links) {
			end = len(links)
		}
		batch := links[start:end]
		urls := make([]string, 0, len(batch))
		for _, l := range batch {
			urls = append(urls, l.URL)
		}
		results, reqErr := client.AddTaskURLs(ctx, urls, wpPathID)
		if reqErr != nil {
			msg := reqErr.Error()
			for _, l := range batch {
				res.Items = append(res.Items, CloudDownloadTaskResult{Source: l.URL, Error: msg})
				res.Rejected++
				s.persistSubmitFailure(ctx, accountID, syncPathID, wpPathID, l, msg, existingTaskID)
			}
			continue
		}
		// 115 不保证结果顺序与入参一致：先按 url 建索引，缺失项再按顺序兜底。
		byURL := make(map[string]cloud115.OfflineTaskURLResult, len(results))
		for _, r := range results {
			if u := strings.TrimSpace(r.URL); u != "" {
				byURL[u] = r
			}
		}
		for i, l := range batch {
			r, ok := byURL[l.URL]
			if !ok && i < len(results) {
				r = results[i]
			}
			if !r.State {
				msg := offlineErrorMessage(r.Code, r.Message)
				res.Items = append(res.Items, CloudDownloadTaskResult{Source: l.URL, Error: msg})
				res.Rejected++
				s.persistSubmitFailure(ctx, accountID, syncPathID, wpPathID, l, msg, existingTaskID)
				continue
			}
			infoHash := strings.ToUpper(strings.TrimSpace(r.InfoHash))
			if infoHash == "" {
				infoHash = l.InfoHash
			}
			key := l.Key
			if infoHash != "" {
				key = infoHash
			}
			taskID, perr := s.persistSubmitSuccess(ctx, accountID, syncPathID, wpPathID, l, key, infoHash, existingTaskID)
			if perr != nil {
				res.Items = append(res.Items, CloudDownloadTaskResult{Source: l.URL, Error: "115 已受理，但本地记录失败：" + perr.Error()})
				res.Rejected++
				continue
			}
			res.Items = append(res.Items, CloudDownloadTaskResult{TaskID: taskID, Source: l.URL, OK: true})
			res.Accepted++
		}
	}
	return res
}

// persistSubmitSuccess 写入一条已受理的任务，返回任务 ID。
func (s *CloudDownloadService) persistSubmitSuccess(
	ctx context.Context, accountID, syncPathID, wpPathID string,
	link cloudDownloadParsedLink, key, infoHash, existingTaskID string,
) (string, error) {
	if existingTaskID != "" {
		return existingTaskID, s.repo.CloudDownload.UpdateFields(ctx, existingTaskID, map[string]any{
			"status":       model.CloudDownloadSubmitted,
			"error":        "",
			"percent":      0,
			"info_hash":    infoHash,
			"source_key":   key,
			"last_poll_at": nil,
			"stage":        model.CloudDownloadStagePending,
			"stage_error":  "",
		})
	}
	task := &model.CloudDownloadTask{
		AccountID:  accountID,
		SyncPathID: syncPathID,
		Provider:   model.StrmProvider115,
		SourceType: link.SourceType,
		SourceURL:  link.URL,
		SourceKey:  key,
		InfoHash:   infoHash,
		WpPathID:   wpPathID,
		Status:     model.CloudDownloadSubmitted,
		Stage:      model.CloudDownloadStagePending,
	}
	if err := s.repo.CloudDownload.Create(ctx, task); err != nil {
		return "", err
	}
	return task.ID, nil
}

// persistSubmitFailure 记录一条提交失败的任务，供用户在列表里看到原因并重试。
func (s *CloudDownloadService) persistSubmitFailure(
	ctx context.Context, accountID, syncPathID, wpPathID string,
	link cloudDownloadParsedLink, msg, existingTaskID string,
) {
	if existingTaskID != "" {
		_ = s.repo.CloudDownload.UpdateFields(ctx, existingTaskID, map[string]any{
			"status": model.CloudDownloadFailed,
			"error":  msg,
		})
		return
	}
	task := &model.CloudDownloadTask{
		AccountID:  accountID,
		SyncPathID: syncPathID,
		Provider:   model.StrmProvider115,
		SourceType: link.SourceType,
		SourceURL:  link.URL,
		SourceKey:  link.Key,
		InfoHash:   link.InfoHash,
		WpPathID:   wpPathID,
		Status:     model.CloudDownloadFailed,
		Error:      msg,
		Stage:      model.CloudDownloadStagePending,
	}
	if err := s.repo.CloudDownload.Create(ctx, task); err != nil {
		s.log.Warn("cloud download: persist failed task failed", zap.Error(err))
	}
}

// ensureSeedDir 返回 115 上「云下载/种子文件」目录的 cid，不存在则创建。
func (s *CloudDownloadService) ensureSeedDir(ctx context.Context, client *cloud115.OpenClient) (string, error) {
	parent := "0"
	for _, name := range cloudDownloadSeedDirPath {
		cid, err := s.findOrCreateDir(ctx, client, parent, name)
		if err != nil {
			return "", err
		}
		parent = cid
	}
	return parent, nil
}

// findOrCreateDir 在 parentCID 下按名称查找子目录，未命中则创建。
// 先查后建的原因：115 的 folder/add 对已存在同名目录的行为文档未定义，
// 直接创建可能得到重复目录或错误。
func (s *CloudDownloadService) findOrCreateDir(ctx context.Context, client *cloud115.OpenClient, parentCID, name string) (string, error) {
	const pageSize = 1150
	offset := 0
	for {
		files, _, err := client.GetFsList(ctx, parentCID, offset, pageSize)
		if err != nil {
			return "", err
		}
		for _, f := range files {
			if f.Category == cloud115.TypeDir && strings.EqualFold(strings.TrimSpace(f.FileName), name) {
				return f.FileId, nil
			}
		}
		if len(files) < pageSize {
			break
		}
		offset += len(files)
	}
	return client.MkDir(ctx, parentCID, name)
}
