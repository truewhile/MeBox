package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/helper"
	"github.com/truewhile/MeBox/internal/model"
)

type ScrapeQueueCounts struct {
	Pending  int64 `json:"pending"`
	Running  int64 `json:"running"`
	Done     int64 `json:"done"`
	Failed   int64 `json:"failed"`
	Canceled int64 `json:"canceled"`
}

type ScrapeQueueSnapshot struct {
	Counts   ScrapeQueueCounts  `json:"counts"`
	Tasks    []model.ScrapeTask `json:"tasks"`
	Total    int64              `json:"total"`
	Page     int                `json:"page"`
	PageSize int                `json:"page_size"`
}

// Start 启动刮削任务队列的后台消费者。
func (s *ScraperService) Start(ctx context.Context) {
	if s == nil {
		return
	}
	// 启动自愈：进程中断遗留的 running 任务重置为 pending，否则永久卡死
	// （ClaimPending 只认 pending，重试按钮也拒绝 running）。
	if n, err := s.repo.ScrapeTask.ResetRunningToPending(ctx); err == nil && n > 0 && s.log != nil {
		s.log.Warn("scrape tasks reset from running to pending after restart", zap.Int64("count", n))
	} else if err != nil && s.log != nil {
		s.log.Warn("reset running scrape tasks failed", zap.Error(err))
	}
	go s.queueWorker(ctx)
}

func (s *ScraperService) queueWorker(ctx context.Context) {
	const claimBatch = 4
	sem := make(chan struct{}, 2) // 最大并发刮削数：2

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		tasks, err := s.repo.ScrapeTask.ClaimPending(ctx, claimBatch)
		if err != nil {
			if s.log != nil {
				s.log.Warn("claim pending scrape task failed", zap.Error(err))
			}
			sleepContext(ctx, 3*time.Second)
			continue
		}
		if len(tasks) == 0 {
			sleepContext(ctx, 2*time.Second)
			continue
		}

		var wg sync.WaitGroup
		for i := range tasks {
			wg.Add(1)
			go func(t *model.ScrapeTask) {
				defer wg.Done()
				select {
				case <-ctx.Done():
					// 任务已被 Claim 置为 running：停机前回写 pending，
					// 避免留下永久卡死的任务。
					s.requeueClaimedScrapeTask(t)
					return
				case sem <- struct{}{}:
				}
				defer func() { <-sem }()

				// 刮削要解析远端元数据响应，单个任务 panic 不应拖垮队列 worker。
				helper.Run(s.log, "scraper.task", func() {
					s.processScrapeTask(ctx, t)
				})
			}(&tasks[i])
		}
		wg.Wait()
	}
}

// requeueClaimedScrapeTask 把已认领但未开始执行的任务回写为 pending。
func (s *ScraperService) requeueClaimedScrapeTask(t *model.ScrapeTask) {
	t.Status = model.ScrapeTaskPending
	t.StartedAt = nil
	if err := s.repo.ScrapeTask.Update(context.Background(), t); err != nil && s.log != nil {
		s.log.Warn("requeue claimed scrape task failed", zap.Error(err), zap.String("id", t.ID))
	}
}

func (s *ScraperService) processScrapeTask(ctx context.Context, task *model.ScrapeTask) {
	media, err := s.repo.Media.FindByID(ctx, task.MediaID)
	if err != nil || media == nil {
		now := time.Now()
		task.Status = model.ScrapeTaskFailed
		task.Error = "媒体项已不存在或被删除"
		task.FinishedAt = &now
		_ = s.repo.ScrapeTask.Update(ctx, task)
		return
	}

	epArtwork := task.EpisodeImages
	usedProvider := ""
	options := ScrapeOptions{
		EpisodeArtwork: &epArtwork,
		IncludeMatched: task.RefreshMatched,
		RetryNoMatch:   true,
		resultProvider: &usedProvider,
	}

	enrichErr := s.EnrichOneWithOptions(ctx, media, options)
	now := time.Now()
	task.FinishedAt = &now

	refreshed, _ := s.repo.Media.FindByID(ctx, media.ID)
	if refreshed != nil && refreshed.ScrapeStatus == "matched" {
		task.Status = model.ScrapeTaskDone
		task.Error = ""
		task.MatchedTitle = refreshed.Title
		task.MatchedYear = refreshed.Year
		task.PosterURL = refreshed.PosterURL
		task.BackdropURL = refreshed.BackdropURL
		task.Provider = strings.ToLower(strings.TrimSpace(usedProvider))
		if task.Provider == "" {
			task.Provider = "unknown"
		}
	} else {
		task.Status = model.ScrapeTaskFailed
		if enrichErr != nil {
			task.Error = enrichErr.Error()
		} else if refreshed != nil && refreshed.ScrapeStatus == "no_match" {
			task.Error = "未搜索到匹配的元数据"
		} else {
			task.Error = "刮削未完成匹配"
		}
	}

	_ = s.repo.ScrapeTask.Update(ctx, task)

	if s.hub != nil {
		s.hub.Publish("scraper_queue", map[string]any{
			"task_id": task.ID,
			"status":  task.Status,
			"title":   task.MediaTitle,
		})
	}
}

func (s *ScraperService) mediaKind(m *model.Media, lib *model.Library) string {
	if m == nil {
		return ""
	}
	if lib != nil && lib.Type != "" {
		return lib.Type
	}
	if mediaIsEpisodic(m, lib) {
		return "tv"
	}
	return "movie"
}

// EnqueueMedia 把单个媒体项放入刮削队列。
func (s *ScraperService) EnqueueMedia(ctx context.Context, mediaID string, options ScrapeOptions) (*model.ScrapeTask, error) {
	if s == nil || s.repo == nil {
		return nil, errors.New("scraper service not initialized")
	}
	media, err := s.repo.Media.FindByID(ctx, mediaID)
	if err != nil || media == nil {
		return nil, errors.New("media not found")
	}

	if active, _ := s.repo.ScrapeTask.FindActiveByMediaID(ctx, mediaID); active != nil {
		return active, nil
	}

	libName := ""
	var lib *model.Library
	if strings.TrimSpace(media.LibraryID) != "" {
		lib, _ = s.repo.Library.FindByID(ctx, media.LibraryID)
		if lib != nil {
			libName = lib.Name
		}
	}

	task := &model.ScrapeTask{
		MediaID:        media.ID,
		LibraryID:      media.LibraryID,
		LibraryName:    libName,
		MediaTitle:     media.Title,
		MediaPath:      media.Path,
		MediaType:      s.mediaKind(media, lib),
		Status:         model.ScrapeTaskPending,
		EpisodeImages:  options.episodeArtworkEnabled(),
		RefreshMatched: options.IncludeMatched || options.RefreshWeakMatched,
	}

	if err := s.repo.ScrapeTask.Create(ctx, task); err != nil {
		return nil, err
	}
	return task, nil
}

// EnqueueLibrary 把指定媒体库内的所有候选媒体批量推入刮削队列。
func (s *ScraperService) EnqueueLibrary(ctx context.Context, libraryID string, options ScrapeOptions) (int, error) {
	if s == nil || s.repo == nil {
		return 0, errors.New("scraper service not initialized")
	}

	lib, err := s.repo.Library.FindByID(ctx, libraryID)
	if err != nil || lib == nil {
		return 0, errors.New("library not found")
	}

	rows, err := s.scrapeCandidateRows(ctx, libraryID, options)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}

	// 去重：排除已有 pending/running 任务的媒体，防止"先单集入队再点
	// 整库刮削"产生重复任务并被并发双刮（同一 Media 行被并发写两次）。
	mediaIDs := make([]string, 0, len(rows))
	for _, m := range rows {
		mediaIDs = append(mediaIDs, m.ID)
	}
	activeByMedia, err := s.repo.ScrapeTask.FindActiveByMediaIDs(ctx, mediaIDs)
	if err != nil {
		activeByMedia = nil // 去重查询失败不阻塞入队，仅退化为不去重
	}

	tasks := make([]model.ScrapeTask, 0, len(rows))
	for _, m := range rows {
		if activeByMedia != nil && activeByMedia[m.ID] {
			continue
		}
		tasks = append(tasks, model.ScrapeTask{
			MediaID:        m.ID,
			LibraryID:      lib.ID,
			LibraryName:    lib.Name,
			MediaTitle:     m.Title,
			MediaPath:      m.Path,
			MediaType:      s.mediaKind(&m, lib),
			Status:         model.ScrapeTaskPending,
			EpisodeImages:  options.episodeArtworkEnabled(),
			RefreshMatched: options.IncludeMatched || options.RefreshWeakMatched,
		})
	}

	if err := s.repo.ScrapeTask.CreateBatch(ctx, tasks); err != nil {
		return 0, err
	}
	return len(tasks), nil
}

// EnqueueAll 把所有已启用媒体库的媒体推入刮削队列。
func (s *ScraperService) EnqueueAll(ctx context.Context, options ScrapeOptions) (int, error) {
	libs, err := s.repo.Library.List(ctx)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, lib := range libs {
		if !lib.Enabled {
			continue
		}
		n, err := s.EnqueueLibrary(ctx, lib.ID, options)
		if err != nil {
			if s.log != nil {
				s.log.Warn("enqueue library for scrape failed", zap.String("library", lib.ID), zap.Error(err))
			}
			continue
		}
		total += n
	}
	return total, nil
}

func (s *ScraperService) ScrapeQueueSnapshot(ctx context.Context, status string, page, pageSize int) (*ScrapeQueueSnapshot, error) {
	tasks, total, err := s.repo.ScrapeTask.List(ctx, status, page, pageSize)
	if err != nil {
		return nil, err
	}
	countsMap, err := s.repo.ScrapeTask.CountByStatus(ctx)
	if err != nil {
		return nil, err
	}
	snap := &ScrapeQueueSnapshot{
		Counts: ScrapeQueueCounts{
			Pending:  countsMap[model.ScrapeTaskPending],
			Running:  countsMap[model.ScrapeTaskRunning],
			Done:     countsMap[model.ScrapeTaskDone],
			Failed:   countsMap[model.ScrapeTaskFailed],
			Canceled: countsMap[model.ScrapeTaskCanceled],
		},
		Tasks:    tasks,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	}
	return snap, nil
}

func (s *ScraperService) CancelScrapeTask(ctx context.Context, id string) error {
	task, err := s.repo.ScrapeTask.FindByID(ctx, id)
	if err != nil || task == nil {
		return errors.New("刮削任务不存在")
	}
	if task.Status != model.ScrapeTaskPending && task.Status != model.ScrapeTaskRunning {
		return errors.New("任务已完成或已终止，无法取消")
	}
	now := time.Now()
	task.Status = model.ScrapeTaskCanceled
	task.Error = "已取消"
	task.FinishedAt = &now
	return s.repo.ScrapeTask.Update(ctx, task)
}

func (s *ScraperService) RetryScrapeTask(ctx context.Context, id string) error {
	task, err := s.repo.ScrapeTask.FindByID(ctx, id)
	if err != nil || task == nil {
		return errors.New("刮削任务不存在")
	}
	if task.Status != model.ScrapeTaskFailed && task.Status != model.ScrapeTaskCanceled {
		// running 任务仅在其长时间无进展（>1h）时允许重试，作为卡死
		// 任务的逃生通道；正常执行中的任务仍拒绝重试以防双跑。
		stale := task.Status == model.ScrapeTaskRunning &&
			(task.StartedAt == nil || time.Since(*task.StartedAt) > time.Hour)
		if !stale {
			return errors.New("只有失败或已取消的任务可以重试")
		}
		if s.log != nil {
			s.log.Warn("retrying stuck running scrape task", zap.String("id", id))
		}
	}
	task.Status = model.ScrapeTaskPending
	task.Error = ""
	task.RetryCount = 0
	task.StartedAt = nil
	task.FinishedAt = nil
	return s.repo.ScrapeTask.Update(ctx, task)
}

func (s *ScraperService) DeleteScrapeTask(ctx context.Context, id string) error {
	return s.repo.ScrapeTask.Delete(ctx, id)
}

func (s *ScraperService) BatchActionScrapeTasks(ctx context.Context, action string, ids []string) (int64, error) {
	switch action {
	case "delete":
		return s.repo.ScrapeTask.DeleteBatch(ctx, ids)
	case "retry":
		return s.repo.ScrapeTask.RetryBatch(ctx, ids)
	case "cancel":
		return s.repo.ScrapeTask.CancelBatch(ctx, ids)
	default:
		return 0, fmt.Errorf("不支持的操作: %s", action)
	}
}

func (s *ScraperService) ClearDoneScrapeTasks(ctx context.Context) (int64, error) {
	return s.repo.ScrapeTask.ClearDone(ctx)
}

func (s *ScraperService) ClearFinishedScrapeTasks(ctx context.Context) (int64, error) {
	return s.repo.ScrapeTask.ClearFinished(ctx)
}

func (s *ScraperService) ClearCanceledScrapeTasks(ctx context.Context) (int64, error) {
	return s.repo.ScrapeTask.ClearCanceled(ctx)
}

func (s *ScraperService) ClearFailedScrapeTasks(ctx context.Context) (int64, error) {
	return s.repo.ScrapeTask.ClearFailed(ctx)
}

func (s *ScraperService) RetryAllFailedScrapeTasks(ctx context.Context) (int64, error) {
	return s.repo.ScrapeTask.RetryAllFailed(ctx)
}

func (s *ScraperService) CancelPendingScrapeTasks(ctx context.Context) (int64, error) {
	return s.repo.ScrapeTask.CancelPending(ctx)
}
