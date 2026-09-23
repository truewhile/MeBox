// Package service — 片头/片尾片段（intro / recap / credits / preview）。
//
// 播放器只认本地库里的片段数据；外部提供方（当前为 TheIntroDB）在播放时按需
// 补齐并落库，因此同一部片第二次播放时不再产生任何外网请求。
package service

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

// 片段数据的缓存时长。命中过说明社区库里已有记录、数据很少变动，可以放很久；
// 未命中说明这部片还没人贡献，隔一段时间再试一次即可——负缓存是必须的，否则
// 每次播放一部没有片段数据的影片都会打一次外网。
const (
	segmentFoundTTL   = 30 * 24 * time.Hour
	segmentMissingTTL = 7 * 24 * time.Hour
)

// SegmentView 是播放器消费的最小片段结构，避免把库内字段（source 等）暴露给前端。
type SegmentView struct {
	Kind    string `json:"kind"`
	StartMs int64  `json:"start_ms"`
	EndMs   int64  `json:"end_ms"`
}

// ToSegmentViews 转换库内行为对外视图。
func ToSegmentViews(rows []model.MediaSegment) []SegmentView {
	out := make([]SegmentView, 0, len(rows))
	for _, row := range rows {
		out = append(out, SegmentView{Kind: row.Kind, StartMs: row.StartMs, EndMs: row.EndMs})
	}
	return out
}

// MediaSegmentService 负责把外部片头片尾数据补齐到本地并供播放器读取。
type MediaSegmentService struct {
	log     *zap.Logger
	repo    *repository.Container
	introdb *IntroDBService
}

// NewMediaSegmentService is the constructor.
func NewMediaSegmentService(log *zap.Logger, repo *repository.Container) *MediaSegmentService {
	return &MediaSegmentService{log: log, repo: repo}
}

// SetIntroDB wires the provider. Without it the service only reads cached rows.
func (s *MediaSegmentService) SetIntroDB(p *IntroDBService) *MediaSegmentService {
	if s != nil {
		s.introdb = p
	}
	return s
}

// ListForPlayback returns the segments known for a media item, refreshing from
// the provider when the cache is stale.
//
// 它不做任何阻塞起播的事情——调用方是在播放已经开始之后用一次独立请求进来的，
// 抓取失败也只是少一个「跳过片头」按钮，绝不能让播放报错。
func (s *MediaSegmentService) ListForPlayback(ctx context.Context, m *model.Media) ([]model.MediaSegment, error) {
	if s == nil || s.repo == nil || m == nil || m.ID == "" {
		return nil, nil
	}
	cached, err := s.repo.MediaSegment.ListByMedia(ctx, m.ID)
	if err != nil {
		return nil, err
	}
	ledger, err := s.repo.MediaSegment.GetFetch(ctx, m.ID, IntroDBSource)
	if err != nil {
		return nil, err
	}
	if ledger != nil && ledgerFresh(ledger) {
		return cached, nil
	}
	refreshed, attempted, err := s.refresh(ctx, m)
	if err != nil {
		// 社区库不可达或返回异常：沿用已有缓存，不影响播放。
		logIntroDBFailure(s.log, 0, err)
		return cached, nil
	}
	if !attempted {
		return cached, nil
	}
	return refreshed, nil
}

// refresh 向提供方查询并落库，返回 (rows, 是否真的发起过查询, error)。
//
// attempted=false 表示这部媒体缺少可查询的外部 ID（最常见的原因是还没刮削，
// 剧集也还没关联到 Series），此时刻意不写负缓存：等元数据补齐后下次播放就能查到。
func (s *MediaSegmentService) refresh(ctx context.Context, m *model.Media) ([]model.MediaSegment, bool, error) {
	if s.introdb == nil {
		return nil, false, nil
	}
	tmdbID, season, episode := s.queryIDs(ctx, m)
	if tmdbID <= 0 {
		return nil, false, nil
	}
	// 用脱离请求的 context：客户端可能在抓取完成前就离开了播放页，但结果仍然
	// 要落库，这样下一次播放直接命中缓存。
	//
	// 但 WithoutCancel 会丢掉 deadline，所以这里要主动把调用方原本愿意等待的
	// 剩余时间取回来：Emby 等第三方客户端会在起播前后同步请求片段，若调用方只
	// 打算等 5 秒，不能因为一次外网抓取把它拖到 10 秒。
	budget := introDBTimeout + 2*time.Second
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < budget {
			budget = remaining
		}
	}
	if budget <= 0 {
		// 调用方的预算已经用完：直接放弃本次抓取。返回 attempted=false，
		// 调用方保留自己的缓存，也不会写入负缓存（下次还有机会）。
		return nil, false, nil
	}
	fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), budget)
	defer cancel()

	spans, err := s.introdb.Fetch(fetchCtx, tmdbID, season, episode)
	if err != nil {
		return nil, true, err
	}
	rows := make([]model.MediaSegment, 0, len(spans))
	for _, span := range spans {
		rows = append(rows, model.MediaSegment{
			MediaID:  m.ID,
			SeriesID: m.SeriesID,
			Kind:     span.Kind,
			StartMs:  span.StartMs,
			EndMs:    span.EndMs,
			Source:   IntroDBSource,
		})
	}
	if err := s.repo.MediaSegment.ReplaceForMedia(fetchCtx, m.ID, IntroDBSource, rows); err != nil {
		return nil, true, err
	}
	if err := s.repo.MediaSegment.UpsertFetch(fetchCtx, &model.MediaSegmentFetch{
		MediaID:   m.ID,
		Source:    IntroDBSource,
		FetchedAt: time.Now(),
		Found:     len(rows) > 0,
	}); err != nil {
		return nil, true, err
	}
	return rows, true, nil
}

// queryIDs resolves the provider query key. Movies use their own TMDb id;
// episodes need the *series* TMDb id plus season/episode, because scraping
// stores the episode-level TMDb id on Media.TMDbID.
func (s *MediaSegmentService) queryIDs(ctx context.Context, m *model.Media) (tmdbID, season, episode int) {
	if m.SeasonNum > 0 || m.EpisodeNum > 0 {
		if m.SeriesID == "" || m.SeasonNum <= 0 || m.EpisodeNum <= 0 {
			return 0, 0, 0
		}
		series, err := s.repo.Series.FindByID(ctx, m.SeriesID)
		if err != nil || series == nil || series.TMDbID <= 0 {
			return 0, 0, 0
		}
		return series.TMDbID, m.SeasonNum, m.EpisodeNum
	}
	if m.TMDbID > 0 {
		return m.TMDbID, 0, 0
	}
	return 0, 0, 0
}

// ledgerFresh reports whether a previous lookup is still within its TTL.
func ledgerFresh(row *model.MediaSegmentFetch) bool {
	if row == nil {
		return false
	}
	ttl := segmentMissingTTL
	if row.Found {
		ttl = segmentFoundTTL
	}
	return time.Since(row.FetchedAt) < ttl
}
