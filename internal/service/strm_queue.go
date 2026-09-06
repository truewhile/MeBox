// STRM 元数据下载/上传队列 worker。
//
// 下载队列：远端网盘 → 本地输出目录（nfo/图片/字幕）；上传队列：本地 → 远端。
// 任务持久化在 DB，worker 轮询认领；失败按指数退避重试，超过上限标记 failed。
package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/helper"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/service/cloud"
	"github.com/truewhile/MeBox/internal/service/cloud115"
)

const (
	strmMaxTaskRetry = 3
)

// downloadWorker 下载队列 worker：认领 → 解析直链 → 下载 → 落盘。
//
// 采用「批量认领 + 按网盘类型限流」：一次认领数个任务，115 与 WebDAV/OpenList
// 使用独立信号量（115 固定 3 防风控；OpenList/CloudDrive2 并发由 download_threads 控制）。
func (s *StrmService) downloadWorker(ctx context.Context) {
	const claimBatch = 12 // 每次批量认领的任务数
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		default:
		}
		tasks, err := s.repo.StrmDownload.ClaimPendingDownload(ctx, claimBatch)
		if err != nil {
			s.log.Warn("claim strm download task failed", zap.Error(err))
			sleepContext(ctx, 3*time.Second)
			continue
		}
		if len(tasks) == 0 {
			sleepContext(ctx, 2*time.Second)
			continue
		}
		// 处于 WAF 冷却的 115 任务先退回，剩余任务在派发前按账号批量换链：
		// downurl 支持逗号分隔多个 pick_code，整批任务一次请求即可完成解析，
		// 显著减少全局 QPS 限流下的换链请求量。
		runnable := make([]*model.StrmDownloadTask, 0, len(tasks))
		for i := range tasks {
			task := &tasks[i]
			if task.Provider == model.StrmProvider115 && s.wafCooldownLeft() > 0 {
				s.requeueDownloadTask(task)
				continue
			}
			runnable = append(runnable, task)
		}
		if len(runnable) == 0 {
			continue
		}
		resolved := s.batchResolve115Links(ctx, runnable)
		var wg sync.WaitGroup
		for i := range runnable {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				task := runnable[i]
				if !s.acquireDownloadSlot(ctx, task.Provider) {
					s.requeueDownloadTask(task)
					return
				}
				defer s.releaseDownloadSlot(task.Provider)
				// 单个任务 panic 不应拖垮整个下载 worker，且 panic 时任务
				// 会永远停在 running：兜底走失败重试路径。
				completed := false
				helper.Run(s.log, "strm.downloadTask", func() {
					defer func() {
						if !completed {
							s.downloadTaskFailWithRetry(task, "任务执行异常中断")
						}
					}()
					s.processDownloadTask(ctx, task, resolved)
					completed = true
				})
			}(i)
		}
		wg.Wait()
	}
}

// dlResolveKey 构造批量换链结果 map 的键（按账号隔离，避免极端情况下不同
// 账号的引用串扰）。
func dlResolveKey(accountID, fileRef string) string {
	return accountID + "|" + fileRef
}

// batchResolve115Links 在派发执行前对 115 下载任务做批量换链。官方 downurl
// 接口支持逗号分隔多个 pick_code（文档《获取文件下载地址》），按账号把整批
// 任务的 pickcode 合并换取，减少 QPS 限流下的换链请求量。解析结果写入
// pickcode 直链缓存供任务执行时命中；批量失败只记日志并触发风控冷却判定，
// 未解析成功的任务在执行时回退到逐个 Resolve，不影响任务本身。
func (s *StrmService) batchResolve115Links(ctx context.Context, tasks []*model.StrmDownloadTask) map[string]*cloud.DirectLink {
	byAcct := map[string][]string{}
	seenRef := map[string]map[string]struct{}{}
	for _, task := range tasks {
		if task.Provider != model.StrmProvider115 {
			continue
		}
		ref := strings.TrimSpace(task.RemoteRef)
		if ref == "" {
			continue
		}
		if seenRef[task.AccountID] == nil {
			seenRef[task.AccountID] = map[string]struct{}{}
		}
		if _, dup := seenRef[task.AccountID][ref]; dup {
			continue
		}
		seenRef[task.AccountID][ref] = struct{}{}
		byAcct[task.AccountID] = append(byAcct[task.AccountID], ref)
	}
	resolved := map[string]*cloud.DirectLink{}
	for acctID, refs := range byAcct {
		acct, err := s.repo.StrmAccount.FindByID(ctx, acctID)
		if err != nil || acct == nil {
			continue
		}
		provider, err := s.providerFor(ctx, acct)
		if err != nil {
			continue
		}
		batch, ok := provider.(cloud.BatchResolver)
		if !ok {
			continue
		}
		links, err := batch.ResolveBatch(ctx, refs)
		if err != nil {
			if is115Blocked(err) {
				s.triggerWAFCooldown()
			}
			s.log.Warn("batch resolve 115 download links failed; fall back to per-task resolve",
				zap.String("account_id", acctID), zap.Int("refs", len(refs)), zap.Error(err))
		}
		for ref, link := range links {
			if link == nil || link.URL == "" {
				continue
			}
			resolved[dlResolveKey(acctID, ref)] = link
		}
	}
	return resolved
}

// requeueDownloadTask 把已认领但未实际执行的任务退回 pending，避免长期停留在 running。
// 退回时必须设置 NextTryAt（WAF 冷却剩余时间）：claim 只过滤 next_try_at
// 已过期的任务，不设会让同一批任务被立刻再认领，形成 claim/requeue
// 热循环（占用 SQLite 写锁并饿死上传队列）。
func (s *StrmService) requeueDownloadTask(task *model.StrmDownloadTask) {
	task.Status = model.StrmTaskPending
	task.StartedAt = nil
	task.NextTryAt = nil
	if task.Provider == model.StrmProvider115 {
		if left := s.wafCooldownLeft(); left > 0 {
			next := time.Now().Add(left)
			task.NextTryAt = &next
		}
	}
	if err := s.repo.StrmDownload.Update(context.Background(), task); err != nil {
		s.log.Warn("requeue strm download task failed", zap.Error(err), zap.String("id", task.ID))
	}
}

// processDownloadTask 处理单个下载任务：解析直链（优先使用批量换链预取的
// 结果，未命中时逐个 Resolve）→ 下载 → 落盘。
func (s *StrmService) processDownloadTask(ctx context.Context, task *model.StrmDownloadTask, resolved map[string]*cloud.DirectLink) {
	cleanPath := sanitizeLocalPath(task.LocalPath)
	if cleanPath != "" && cleanPath != task.LocalPath {
		task.LocalPath = cleanPath
		_ = s.repo.StrmDownload.Update(context.Background(), task)
	}
	finish := func(status, message string) {
		now := time.Now()
		task.Status = status
		task.Error = message
		task.FinishedAt = &now
		// 条件化收尾：用户取消会直接把 running 改为 canceled，无条件
		// Update 会把已取消任务覆盖回 done。
		if ok, err := s.repo.StrmDownload.UpdateIfRunning(context.Background(), task.ID, map[string]any{
			"status":      status,
			"error":       message,
			"finished_at": &now,
		}); err != nil {
			s.log.Warn("update strm download task failed", zap.Error(err))
		} else if !ok {
			s.log.Info("strm download task already closed elsewhere", zap.String("id", task.ID))
		}
	}
	acct, err := s.repo.StrmAccount.FindByID(ctx, task.AccountID)
	if err != nil || acct == nil {
		finish(model.StrmTaskFailed, "网盘账号不存在")
		return
	}
	provider, err := s.providerFor(ctx, acct)
	if err != nil {
		s.downloadTaskFailWithRetry(task, err.Error())
		return
	}
	link, ok := resolved[dlResolveKey(task.AccountID, task.RemoteRef)]
	if !ok || link == nil || link.URL == "" {
		link, err = provider.Resolve(ctx, task.RemoteRef)
		if err != nil {
			if is115Blocked(err) {
				s.triggerWAFCooldown()
			}
			s.downloadTaskFailWithRetry(task, "解析下载地址失败："+err.Error())
			return
		}
	}
	if err := downloadToFile(ctx, link, task.LocalPath, s.http); err != nil {
		// 直链失效（403/404/410 等）：清掉缓存让下一轮重新换取
		if isHTTPDownloadFailure(err) && task.Provider == model.StrmProvider115 {
			cloud115.ClearDownloadURLCache(task.RemoteRef)
		}
		s.downloadTaskFailWithRetry(task, "下载失败："+err.Error())
		return
	}
	finish(model.StrmTaskDone, "")
}

// uploadWorker 上传队列 worker：认领 → WebDAV/OpenList 上传 → 收尾。
func (s *StrmService) uploadWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		default:
		}
		tasks, err := s.repo.StrmUpload.ClaimPendingUpload(ctx, 1)
		if err != nil {
			s.log.Warn("claim strm upload task failed", zap.Error(err))
			sleepContext(ctx, 3*time.Second)
			continue
		}
		if len(tasks) == 0 {
			sleepContext(ctx, 2*time.Second)
			continue
		}
		for i := range tasks {
			t := &tasks[i]
			// 与下载侧一致：单任务 panic 不损失 worker 线程，且兜底走
			// 失败重试路径（否则任务永久 running）。
			completed := false
			helper.Run(s.log, "strm.uploadTask", func() {
				defer func() {
					if !completed {
						s.uploadTaskFailWithRetry(t, "任务执行异常中断")
					}
				}()
				s.processUploadTask(ctx, t)
				completed = true
			})
		}
	}
}

func (s *StrmService) processUploadTask(ctx context.Context, task *model.StrmUploadTask) {
	finish := func(status, message string) {
		now := time.Now()
		task.Status = status
		task.Error = message
		task.FinishedAt = &now
		// 条件化收尾：与下载侧一致，防止覆盖已取消任务。
		if ok, err := s.repo.StrmUpload.UpdateIfRunning(context.Background(), task.ID, map[string]any{
			"status":      status,
			"error":       message,
			"finished_at": &now,
		}); err != nil {
			s.log.Warn("update strm upload task failed", zap.Error(err))
		} else if !ok {
			s.log.Info("strm upload task already closed elsewhere", zap.String("id", task.ID))
		}
	}
	if task.Provider == model.StrmProvider115 {
		s.processUpload115(ctx, task)
		return
	}
	acct, err := s.repo.StrmAccount.FindByID(ctx, task.AccountID)
	if err != nil || acct == nil {
		finish(model.StrmTaskFailed, "网盘账号不存在")
		return
	}
	cfg, err := s.strmAccountConfig(acct)
	if err == nil && task.Provider == model.StrmProviderOpenList && cfg["token"] == "" && cfg["password"] == "" {
		finish(model.StrmTaskFailed, "OpenList 账号需要配置 Token 或密码才能上传")
		return
	}
	provider, err := s.providerFor(ctx, acct)
	if err != nil {
		s.uploadTaskFailWithRetry(task, err.Error())
		return
	}
	putter, ok := provider.(interface {
		PutFile(ctx context.Context, remotePath string, r io.Reader) error
	})
	if !ok {
		finish(model.StrmTaskFailed, "该网盘不支持元数据上传")
		return
	}
	f, err := os.Open(task.LocalPath)
	if err != nil {
		s.uploadTaskFailWithRetry(task, "打开本地文件失败："+err.Error())
		return
	}
	if err := putter.PutFile(ctx, task.RemotePath, f); err != nil {
		_ = f.Close()
		s.uploadTaskFailWithRetry(task, "上传失败："+err.Error())
		return
	}
	_ = f.Close()
	finish(model.StrmTaskDone, "")
}

// processUpload115 115 元数据上传：task.RemotePath 存的是父目录 cid，FileName 为远端文件名。
func (s *StrmService) processUpload115(ctx context.Context, task *model.StrmUploadTask) {
	finish := func(status, message string) {
		now := time.Now()
		task.Status = status
		task.Error = message
		task.FinishedAt = &now
		// 条件化收尾：与下载侧一致，防止覆盖已取消任务。
		if ok, err := s.repo.StrmUpload.UpdateIfRunning(context.Background(), task.ID, map[string]any{
			"status":      status,
			"error":       message,
			"finished_at": &now,
		}); err != nil {
			s.log.Warn("update strm upload task failed", zap.Error(err))
		} else if !ok {
			s.log.Info("strm upload task already closed elsewhere", zap.String("id", task.ID))
		}
	}
	acct, err := s.repo.StrmAccount.FindByID(ctx, task.AccountID)
	if err != nil || acct == nil {
		finish(model.StrmTaskFailed, "网盘账号不存在")
		return
	}
	provider, err := s.providerFor(ctx, acct)
	if err != nil {
		s.uploadTaskFailWithRetry(task, err.Error())
		return
	}
	named, ok := provider.(interface {
		PutFileNamed(ctx context.Context, parentCID, fileName string, r io.Reader) error
	})
	if !ok {
		finish(model.StrmTaskFailed, "该网盘不支持元数据上传")
		return
	}
	// 以本地为准：网盘端已有同名但内容不同的旧元数据时，先尝试批量删除所有旧副本再上传。
	// 115 的上传接口不保证同名覆盖，直接上传可能产生同名重复文件。
	// 删除失败时不中止任务——继续上传新文件，旧副本交由下次同步的 cleanupBatchRedundantFiles
	// 按目录批量清理（下次同步会看到新旧两个版本，命中新版本后把旧版本 cid 收入 pendingDeletes
	// 异步删除）。这样避免了「删旧失败 → 任务重试 → 再次删旧失败 → 永远无法上传」的死循环。
	if task.RemoteRef != "" {
		open115, ok := provider.(cloud.OpenAPI115Provider)
		if !ok {
			finish(model.StrmTaskFailed, "该网盘不支持删除远端旧元数据")
			return
		}
		refs := strings.Split(task.RemoteRef, ",")
			if err := open115.OpenClient().DeleteFiles(ctx, task.RemotePath, refs...); err != nil {
				s.log.Warn("删除网盘旧元数据失败，跳过删除继续上传新文件",
					zap.String("task_id", task.ID),
					zap.String("local_path", task.LocalPath),
					zap.Error(err))
				// 不 return：继续上传新文件，旧副本由下次同步清理
			}
		}
		// 优先使用直接本地文件上传接口，零拷贝且彻底根除并发临时文件同名碰撞
		if localUploader, ok := provider.(interface {
			PutLocalFile(ctx context.Context, parentCID, localPath string) error
		}); ok {
			if err := localUploader.PutLocalFile(ctx, task.RemotePath, task.LocalPath); err != nil {
				s.uploadTaskFailWithRetry(task, "上传失败："+err.Error())
				return
			}
			finish(model.StrmTaskDone, "")
			return
		}

		f, err := os.Open(task.LocalPath)
		if err != nil {
			s.uploadTaskFailWithRetry(task, "打开本地文件失败："+err.Error())
			return
		}
		if err := named.PutFileNamed(ctx, task.RemotePath, task.FileName, f); err != nil {
			_ = f.Close()
			s.uploadTaskFailWithRetry(task, "上传失败："+err.Error())
			return
		}
		_ = f.Close()
		finish(model.StrmTaskDone, "")
	}

// downloadTaskFailWithRetry 下载失败任务按退避重试，超过上限标记 failed。
func (s *StrmService) downloadTaskFailWithRetry(task *model.StrmDownloadTask, message string) {
	if !retryTask(&task.RetryCount, &task.Status, &task.Error, &task.NextTryAt, &task.FinishedAt, message) {
		return
	}
	// 条件化写入：任务已被取消（DB 中不再是 running）时不得覆盖回 pending，
	// 否则用户刚取消的任务会“复活”并自动重试。
	if ok, err := s.repo.StrmDownload.UpdateIfRunning(context.Background(), task.ID, map[string]any{
		"status":      task.Status,
		"error":       task.Error,
		"retry_count": task.RetryCount,
		"next_try_at": task.NextTryAt,
		"finished_at": task.FinishedAt,
	}); err != nil {
		s.log.Warn("fail strm download task failed", zap.Error(err), zap.String("id", task.ID))
	} else if !ok {
		s.log.Info("strm download task already closed elsewhere, skip retry overwrite", zap.String("id", task.ID))
	}
}

// uploadTaskFailWithRetry 上传失败任务按退避重试，超过上限标记 failed。
func (s *StrmService) uploadTaskFailWithRetry(task *model.StrmUploadTask, message string) {
	if !retryTask(&task.RetryCount, &task.Status, &task.Error, &task.NextTryAt, &task.FinishedAt, message) {
		return
	}
	if ok, err := s.repo.StrmUpload.UpdateIfRunning(context.Background(), task.ID, map[string]any{
		"status":      task.Status,
		"error":       task.Error,
		"retry_count": task.RetryCount,
		"next_try_at": task.NextTryAt,
		"finished_at": task.FinishedAt,
	}); err != nil {
		s.log.Warn("fail strm upload task failed", zap.Error(err), zap.String("id", task.ID))
	} else if !ok {
		s.log.Info("strm upload task already closed elsewhere, skip retry overwrite", zap.String("id", task.ID))
	}
}

// retryTask 失败状态机：重试次数不足则回 pending 并设置退避时间，否则 failed。
// 返回 false 表示无需再次落库（每次都会通过 Update 落库，因此恒返回 true）。
func retryTask(retryCount *int, status *string, errMsg *string, nextTryAt **time.Time, finishedAt **time.Time, message string) bool {
	now := time.Now()
	if *retryCount >= strmMaxTaskRetry {
		*status = model.StrmTaskFailed
		*errMsg = message
		*finishedAt = &now
		return true
	}
	*retryCount++
	*status = model.StrmTaskPending
	*errMsg = message
	next := now.Add(time.Duration(*retryCount) * 30 * time.Second)
	*nextTryAt = &next
	*finishedAt = nil
	return true
}

// downloadToFile 把直链内容下载到目标文件（临时文件 + 原子改名）。
func downloadToFile(ctx context.Context, link *cloud.DirectLink, target string, client *http.Client) error {
	target = sanitizeLocalPath(target)
	if link == nil || link.URL == "" {
		return errors.New("空下载地址")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link.URL, nil)
	if err != nil {
		return err
	}
	for k, v := range link.Headers {
		req.Header.Set(k, v)
	}
	// 115 的 CDN 直链要求与换取链接时相同的浏览器 UA，否则返回 403；
	// 其他网盘（WebDAV/OpenList）对该值不敏感，统一兜底设置。
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", cloud115.DefaultUA)
	}
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	tmp := target + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, resp.Body)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	return os.Rename(tmp, target)
}

// queueCleanupLoop 定期清理 7 天前的完成/失败/取消任务。
func (s *StrmService) queueCleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case <-ticker.C:
			before := time.Now().AddDate(0, 0, -7)
			if err := s.repo.StrmDownload.DeleteFinishedOlderThan(ctx, before); err != nil {
				s.log.Warn("clean old strm download tasks failed", zap.Error(err))
			}
			if err := s.repo.StrmUpload.DeleteFinishedOlderThan(ctx, before); err != nil {
				s.log.Warn("clean old strm upload tasks failed", zap.Error(err))
			}
		}
	}
}

// ─── 队列查询与操作（handler 使用） ─────────────────────────────────────────────

// StrmQueueCounts 队列统计。
type StrmQueueCounts struct {
	Pending  int64 `json:"pending"`
	Running  int64 `json:"running"`
	Done     int64 `json:"done"`
	Failed   int64 `json:"failed"`
	Canceled int64 `json:"canceled"`
}

// StrmQueueSnapshot 队列快照（统计 + 任务明细，分页）。
type StrmQueueSnapshot struct {
	Counts   StrmQueueCounts `json:"counts"`
	Tasks    []strmTaskView  `json:"tasks"`
	Total    int64           `json:"total"`     // 当前过滤条件下任务总数
	Page     int             `json:"page"`      // 当前页码（从 1 开始）
	PageSize int             `json:"page_size"` // 单页大小
}

// strmTaskView 队列任务统一视图（下载/上传共用）。
type strmTaskView struct {
	ID         string  `json:"id"`
	Kind       string  `json:"kind"` // download / upload
	SyncPathID string  `json:"sync_path_id"`
	AccountID  string  `json:"account_id"`
	Provider   string  `json:"provider"`
	FileName   string  `json:"file_name"`
	LocalPath  string  `json:"local_path"`
	RemotePath string  `json:"remote_path"`
	Size       int64   `json:"size"`
	Status     string  `json:"status"`
	Error      string  `json:"error"`
	RetryCount int     `json:"retry_count"`
	CreatedAt  string  `json:"created_at"`
	StartedAt  *string `json:"started_at"`
	FinishedAt *string `json:"finished_at"`
}

// DownloadQueueSnapshot 下载队列快照（分页）。
func (s *StrmService) DownloadQueueSnapshot(ctx context.Context, status string, page, pageSize int) (*StrmQueueSnapshot, error) {
	page, pageSize = normalizeStrmTaskPage(page, pageSize)
	tasks, total, err := s.repo.StrmDownload.List(ctx, status, page, pageSize)
	if err != nil {
		return nil, err
	}
	counts, err := s.repo.StrmDownload.CountByStatus(ctx)
	if err != nil {
		return nil, err
	}
	snap := &StrmQueueSnapshot{Counts: strmTaskCountsFrom(counts), Tasks: make([]strmTaskView, 0, len(tasks)), Total: total, Page: page, PageSize: pageSize}
	for i := range tasks {
		t := &tasks[i]
		snap.Tasks = append(snap.Tasks, strmTaskView{
			ID:         t.ID,
			Kind:       "download",
			SyncPathID: t.SyncPathID,
			AccountID:  t.AccountID,
			Provider:   t.Provider,
			FileName:   t.FileName,
			LocalPath:  t.LocalPath,
			RemotePath: t.RemoteDir + "/" + t.FileName,
			Size:       t.Size,
			Status:     t.Status,
			Error:      t.Error,
			RetryCount: t.RetryCount,
			CreatedAt:  t.CreatedAt.Local().Format(time.RFC3339),
			StartedAt:  timePtrString(t.StartedAt),
			FinishedAt: timePtrString(t.FinishedAt),
		})
	}
	return snap, nil
}

// UploadQueueSnapshot 上传队列快照（分页）。
func (s *StrmService) UploadQueueSnapshot(ctx context.Context, status string, page, pageSize int) (*StrmQueueSnapshot, error) {
	page, pageSize = normalizeStrmTaskPage(page, pageSize)
	tasks, total, err := s.repo.StrmUpload.List(ctx, status, page, pageSize)
	if err != nil {
		return nil, err
	}
	counts, err := s.repo.StrmUpload.CountByStatus(ctx)
	if err != nil {
		return nil, err
	}
	snap := &StrmQueueSnapshot{Counts: strmTaskCountsFrom(counts), Tasks: make([]strmTaskView, 0, len(tasks)), Total: total, Page: page, PageSize: pageSize}
	for i := range tasks {
		t := &tasks[i]
		snap.Tasks = append(snap.Tasks, strmTaskView{
			ID:         t.ID,
			Kind:       "upload",
			SyncPathID: t.SyncPathID,
			AccountID:  t.AccountID,
			Provider:   t.Provider,
			FileName:   t.FileName,
			LocalPath:  t.LocalPath,
			RemotePath: t.RemotePath,
			Size:       t.Size,
			Status:     t.Status,
			Error:      t.Error,
			RetryCount: t.RetryCount,
			CreatedAt:  t.CreatedAt.Local().Format(time.RFC3339),
			StartedAt:  timePtrString(t.StartedAt),
			FinishedAt: timePtrString(t.FinishedAt),
		})
	}
	return snap, nil
}

func strmTaskCountsFrom(m map[string]int64) StrmQueueCounts {
	return StrmQueueCounts{
		Pending:  m[model.StrmTaskPending],
		Running:  m[model.StrmTaskRunning],
		Done:     m[model.StrmTaskDone],
		Failed:   m[model.StrmTaskFailed],
		Canceled: m[model.StrmTaskCanceled],
	}
}

// normalizeStrmTaskPage 钳制队列分页参数（与 repository 一致，保证回显正确）。
func normalizeStrmTaskPage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 50
	}
	return page, pageSize
}

func timePtrString(t *time.Time) *string {
	if t == nil {
		return nil
	}
	v := t.Local().Format(time.RFC3339)
	return &v
}

// CancelDownloadTask 取消一个排队/进行中的下载任务。
func (s *StrmService) CancelDownloadTask(ctx context.Context, id string) error {
	task, err := s.repo.StrmDownload.FindByID(ctx, id)
	if err != nil || task == nil {
		return errNotFoundOr(err, "下载任务不存在")
	}
	if task.Status != model.StrmTaskPending && task.Status != model.StrmTaskRunning {
		return errors.New("任务已结束，无法取消")
	}
	now := time.Now()
	task.Status = model.StrmTaskCanceled
	task.Error = "已取消"
	task.FinishedAt = &now
	return s.repo.StrmDownload.Update(ctx, task)
}

// RetryDownloadTask 重试一个失败的下载任务。
func (s *StrmService) RetryDownloadTask(ctx context.Context, id string) error {
	task, err := s.repo.StrmDownload.FindByID(ctx, id)
	if err != nil || task == nil {
		return errNotFoundOr(err, "下载任务不存在")
	}
	if task.Status != model.StrmTaskFailed && task.Status != model.StrmTaskCanceled {
		return errors.New("只有失败/已取消的任务可以重试")
	}
	task.Status = model.StrmTaskPending
	task.Error = ""
	task.RetryCount = 0
	task.NextTryAt = nil
	task.FinishedAt = nil
	return s.repo.StrmDownload.Update(ctx, task)
}

// CancelUploadTask 取消一个排队/进行中的上传任务。
func (s *StrmService) CancelUploadTask(ctx context.Context, id string) error {
	task, err := s.repo.StrmUpload.FindByID(ctx, id)
	if err != nil || task == nil {
		return errNotFoundOr(err, "上传任务不存在")
	}
	if task.Status != model.StrmTaskPending && task.Status != model.StrmTaskRunning {
		return errors.New("任务已结束，无法取消")
	}
	now := time.Now()
	task.Status = model.StrmTaskCanceled
	task.Error = "已取消"
	task.FinishedAt = &now
	return s.repo.StrmUpload.Update(ctx, task)
}

// RetryUploadTask 重试一个失败的上传任务。
func (s *StrmService) RetryUploadTask(ctx context.Context, id string) error {
	task, err := s.repo.StrmUpload.FindByID(ctx, id)
	if err != nil || task == nil {
		return errNotFoundOr(err, "上传任务不存在")
	}
	if task.Status != model.StrmTaskFailed && task.Status != model.StrmTaskCanceled {
		return errors.New("只有失败/已取消的任务可以重试")
	}
	task.Status = model.StrmTaskPending
	task.Error = ""
	task.RetryCount = 0
	task.NextTryAt = nil
	task.FinishedAt = nil
	return s.repo.StrmUpload.Update(ctx, task)
}

// ─── 下载队列批量操作（handler 使用） ─────────────────────────────────────────

// DeleteDownloadTask 删除一个下载任务记录。
func (s *StrmService) DeleteDownloadTask(ctx context.Context, id string) error {
	return s.repo.StrmDownload.Delete(ctx, id)
}

// DeleteUploadTask 删除一个上传任务记录。
func (s *StrmService) DeleteUploadTask(ctx context.Context, id string) error {
	return s.repo.StrmUpload.Delete(ctx, id)
}

// BatchActionDownloadTasks 对选中的下载任务执行批量操作（delete / retry / cancel）。
func (s *StrmService) BatchActionDownloadTasks(ctx context.Context, action string, ids []string) (int64, error) {
	switch action {
	case "delete":
		return s.repo.StrmDownload.DeleteBatch(ctx, ids)
	case "retry":
		return s.repo.StrmDownload.RetryBatch(ctx, ids)
	case "cancel":
		return s.repo.StrmDownload.CancelBatch(ctx, ids)
	default:
		return 0, fmt.Errorf("不支持的批量操作: %s", action)
	}
}

// BatchActionUploadTasks 对选中的上传任务执行批量操作（delete / retry / cancel）。
func (s *StrmService) BatchActionUploadTasks(ctx context.Context, action string, ids []string) (int64, error) {
	switch action {
	case "delete":
		return s.repo.StrmUpload.DeleteBatch(ctx, ids)
	case "retry":
		return s.repo.StrmUpload.RetryBatch(ctx, ids)
	case "cancel":
		return s.repo.StrmUpload.CancelBatch(ctx, ids)
	default:
		return 0, fmt.Errorf("不支持的批量操作: %s", action)
	}
}

// ClearDoneDownloadTasks 清空全部已完成下载记录，返回删除数量。
func (s *StrmService) ClearDoneDownloadTasks(ctx context.Context) (int64, error) {
	return s.repo.StrmDownload.ClearDone(ctx)
}

// ClearFinishedDownloadTasks 清空全部已完成与失败的下载记录，返回删除数量。
func (s *StrmService) ClearFinishedDownloadTasks(ctx context.Context) (int64, error) {
	return s.repo.StrmDownload.ClearFinished(ctx)
}

// ClearCanceledDownloadTasks 清空全部已取消的下载记录，返回删除数量。
func (s *StrmService) ClearCanceledDownloadTasks(ctx context.Context) (int64, error) {
	return s.repo.StrmDownload.ClearCanceled(ctx)
}

// ClearFailedDownloadTasks 清空全部已失败的下载记录，返回删除数量。
func (s *StrmService) ClearFailedDownloadTasks(ctx context.Context) (int64, error) {
	return s.repo.StrmDownload.ClearFailed(ctx)
}

// ClearCanceledUploadTasks 清空全部已取消的上传记录，返回删除数量。
func (s *StrmService) ClearCanceledUploadTasks(ctx context.Context) (int64, error) {
	return s.repo.StrmUpload.ClearCanceled(ctx)
}

// ClearFailedUploadTasks 清空全部已失败的上传记录，返回删除数量。
func (s *StrmService) ClearFailedUploadTasks(ctx context.Context) (int64, error) {
	return s.repo.StrmUpload.ClearFailed(ctx)
}

// ClearDoneUploadTasks 清空全部已完成上传记录，返回删除数量。
func (s *StrmService) ClearDoneUploadTasks(ctx context.Context) (int64, error) {
	return s.repo.StrmUpload.ClearDone(ctx)
}

// ClearFinishedUploadTasks 清空全部已完成与失败的上传记录，返回删除数量。
func (s *StrmService) ClearFinishedUploadTasks(ctx context.Context) (int64, error) {
	return s.repo.StrmUpload.ClearFinished(ctx)
}

// RetryAllFailedDownloadTasks 批量重试所有失败下载任务，返回重新入队数量。
func (s *StrmService) RetryAllFailedDownloadTasks(ctx context.Context) (int64, error) {
	return s.repo.StrmDownload.RetryAllFailed(ctx)
}

// RetryAllFailedUploadTasks 批量重试所有失败上传任务，返回重新入队数量。
func (s *StrmService) RetryAllFailedUploadTasks(ctx context.Context) (int64, error) {
	return s.repo.StrmUpload.RetryAllFailed(ctx)
}

// CancelPendingDownloadTasks 批量取消所有排队下载任务，返回取消数量。
func (s *StrmService) CancelPendingDownloadTasks(ctx context.Context) (int64, error) {
	return s.repo.StrmDownload.CancelPending(ctx)
}

// CancelPendingUploadTasks 批量取消所有排队上传任务，返回取消数量。
func (s *StrmService) CancelPendingUploadTasks(ctx context.Context) (int64, error) {
	return s.repo.StrmUpload.CancelPending(ctx)
}

func sleepContext(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// ─── 115 风控/限流熔断 ────────────────────────────────────────────────────────

// triggerWAFCooldown 检测到 115 风控/限流后触发冷却，仅影响 115 下载任务。
// 冷却时间取最大值，避免连续触发时缩短等待。
func (s *StrmService) triggerWAFCooldown() {
	s.mu.Lock()
	defer s.mu.Unlock()
	until := time.Now().Add(strmWAFCooldown)
	if until.After(s.wafUntil) {
		s.wafUntil = until
		s.log.Warn("115 风控/限流，下载队列进入冷却", zap.Duration("cooldown", strmWAFCooldown))
	}
}

// wafCooldownLeft 返回剩余冷却时间（0 表示无需冷却）。
func (s *StrmService) wafCooldownLeft() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wafUntil.After(time.Now()) {
		return time.Until(s.wafUntil)
	}
	return 0
}

// is115Blocked 判断错误是否来自 115 的风控/限流（WAF 405 拦截页或限流错误码）。
// 覆盖两层文案：HTTP 层（doJSON 的"接口触发频控/安全拦截（HTTP 405）"）与
// 业务错误码层（OpenAPIError 的"115 接口错误（406/770004）"）。
func is115Blocked(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "115 接口返回 http 405") ||
		strings.Contains(msg, "115 接口触发频控/安全拦截") ||
		strings.Contains(msg, "访问被阻断") ||
		strings.Contains(msg, "request has been blocked") ||
		strings.Contains(msg, "115 接口错误（770004") ||
		strings.Contains(msg, "115 接口错误（406")
}

// isHTTPDownloadFailure 判断下载是否因 HTTP 状态码失败（直链失效需清缓存重取）。
func isHTTPDownloadFailure(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "http 4") || strings.Contains(msg, "http 5")
}
