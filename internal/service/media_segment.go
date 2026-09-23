// Package service — 片头/片尾片段（intro / recap / credits / preview）。
//
// 播放器只认本地库里的片段数据；外部提供方（当前为 TheIntroDB）在播放时按需
// 补齐并落库，因此同一部片第二次播放时不再产生任何外网请求。
package service

import (
	"context"
	"strings"
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
	// probe 负责从文件内嵌章节里提取片头/片尾（异步、落库）。未注入时只用
	// TheIntroDB。
	probe *MediaProbeService
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

// SetProbe wires the in-file chapter extractor. Without it the ffprobe source
// simply yields nothing and auto falls back to TheIntroDB.
func (s *MediaSegmentService) SetProbe(p *MediaProbeService) *MediaSegmentService {
	if s != nil {
		s.probe = p
	}
	return s
}

// SegmentsResult 是播放器一次查询的结果。
type SegmentsResult struct {
	// Segments 是按当前来源选定、可以直接用来跳过的区间。
	Segments []model.MediaSegment
	// Pending 为 true 表示 ffprobe 提取还在后台跑：这次可能还没有章节数据，
	// 客户端过几秒再拉一次就能拿到；那时如果片头还没播完，跳过按钮会自动出现。
	Pending bool
}

// SegmentsForPlayback 返回播放器该用的片段，并按需触发数据补齐。
//
// 它绝不做阻塞起播的事：TheIntroDB 的抓取沿用原来的「以调用方 deadline 为预算」，
// ffprobe 提取则完全异步。抓取或提取失败只是少一个「跳过片头」按钮、或晚几秒
// 出现，绝不能让播放报错。
func (s *MediaSegmentService) SegmentsForPlayback(ctx context.Context, m *model.Media, source string) (SegmentsResult, error) {
	if s == nil || s.repo == nil || m == nil || m.ID == "" {
		return SegmentsResult{}, nil
	}
	source = NormalizeSegmentSource(source)
	result := SegmentsResult{}
	// 只用社区库时连探测都不该触发：没必要为一次用不上的章节提取去跑 ffprobe。
	var chapterRows []model.MediaSegment
	needsProbe := false
	if source != SegmentSourceTheIntroDB {
		chapterRows, needsProbe = s.chapterSegments(ctx, m)
	}
	result.Pending = needsProbe
	// 用 defer 保证无论走哪条分支、包括中途出错，后台提取都会排上队；同时它一定在
	// 所有前台数据库读写之后才启动（见 triggerAsyncProbe 的说明）。
	defer triggerAsyncProbe(s, m, needsProbe)

	if source == SegmentSourceTheIntroDB {
		introRows, err := s.introDBSegments(ctx, m)
		if err != nil {
			return result, err
		}
		result.Segments = introRows
		return result, nil
	}
	// ffprobe 档、以及 auto 档下已经有可用章节的情况，都整体采用章节数据。
	//
	// 章节与社区库的数据刻意不合并：两边对同一集的判定会互相矛盾（同一集的片尾
	// 起点能差上百秒），只能按 media 整体二选一。auto 走到这里说明章节可用，也就
	// 不必再去打一次用不上的社区库。
	if source == SegmentSourceFFprobe || len(chapterRows) > 0 {
		result.Segments = chapterRows
		return result, nil
	}
	// auto 且没有可用章节：回落到社区库；Pending 保留，客户端会再拉一次。
	introRows, err := s.introDBSegments(ctx, m)
	if err != nil {
		return result, err
	}
	result.Segments = introRows
	return result, nil
}

// triggerAsyncProbe 在所有前台数据库读写都结束之后再起后台提取。
//
// 顺序很关键：后台探测自己也要写库，若在本次请求的写事务还没结束时启动，两个写
// 事务会抢同一把锁（SQLite 下就是 SQLITE_BUSY）。
func triggerAsyncProbe(s *MediaSegmentService, m *model.Media, needsProbe bool) {
	if !needsProbe || s == nil || s.probe == nil {
		return
	}
	s.probe.EnsureAsync(m)
}

// ListForPlayback 供 Emby / Jellyfin 兼容接口使用：按 auto 档取数据（章节优先，
// 回落社区库）。第三方客户端不会轮询，所以这里只返回当前能拿到的部分。
func (s *MediaSegmentService) ListForPlayback(ctx context.Context, m *model.Media) ([]model.MediaSegment, error) {
	result, err := s.SegmentsForPlayback(ctx, m, SegmentSourceAuto)
	if err != nil {
		return nil, err
	}
	return result.Segments, nil
}

// introDBSegments 读社区库的片段，缓存过期时按调用方的预算抓一次并落库。
func (s *MediaSegmentService) introDBSegments(ctx context.Context, m *model.Media) ([]model.MediaSegment, error) {
	cached, err := s.repo.MediaSegment.ListByMediaSource(ctx, m.ID, IntroDBSource)
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

// chapterSegments 读 ffprobe 提取出的章节区间，并报告「是否还需要等一次提取结果」。
//
// 它只读、不启动提取：调用方要等所有前台数据库读写都结束之后再起后台任务，否则
// 后台写事务会和本次请求的写事务抢同一把锁。探测失败的结果也会落库，所以不会
// 每次播放都为同一个坏源重跑。
func (s *MediaSegmentService) chapterSegments(ctx context.Context, m *model.Media) ([]model.MediaSegment, bool) {
	if s == nil || s.probe == nil || s.repo == nil {
		return nil, false
	}
	rows, err := s.repo.MediaSegment.ListByMediaSource(ctx, m.ID, SegmentSourceFFprobe)
	if err != nil {
		s.debug("list ffprobe segments failed", m.ID, err)
		return nil, false
	}
	cached, err := s.repo.MediaProbe.Get(ctx, m.ID)
	if err != nil {
		s.debug("get media probe failed", m.ID, err)
		return nil, false
	}
	if mediaProbeSettled(cached) {
		return rows, false
	}
	// 还没探过、或失败已过冷却期：值得让客户端稍后再来一次。
	return rows, true
}

// mediaProbeSettled 判断这部媒体的探测是否已经「有结论」——成功过，或者失败但还在
// 冷却期内。有结论就不必再探，客户端也不用继续轮询；失败且已过冷却期时返回
// false，让下一次播放重试。
func mediaProbeSettled(row *model.MediaProbe) bool {
	if row == nil {
		return false
	}
	if strings.TrimSpace(row.LastError) == "" {
		return true
	}
	return time.Since(row.ProbedAt) < mediaProbeFailureRetry
}

func (s *MediaSegmentService) debug(message, mediaID string, err error) {
	if s == nil || s.log == nil {
		return
	}
	s.log.Debug(message, zap.String("media_id", mediaID), zap.Error(err))
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

// queryIDs resolves the provider query key. Movies use their own TMDb id.
//
// 剧集需要「剧集级」TMDb id 加季/集。优先取 Series.TMDbID；但有些刮削路径
// 不建 Series 行，而是把剧集级 id 直接写在 Media.TMDbID 上（生产环境动漫库
// 实测如此：同一剧名下各集共用同一个 id，52/52 个剧名都唯一）。这类行原先
// 一律解析不出 id，等于整库查不到任何片段，所以这里补一条兜底。
//
// 兜底必须验证「是不是剧集级 id」：Media.TMDbID 在另一些刮削路径下存的是
// 单集自己的 id，拿它去查会命中别的片子。判据是多集共用（见
// MediaRepository.ExistsSiblingWithTMDbID）——单集 id 不会在兄弟集上重复。
func (s *MediaSegmentService) queryIDs(ctx context.Context, m *model.Media) (tmdbID, season, episode int) {
	if m.SeasonNum > 0 || m.EpisodeNum > 0 {
		return s.episodeQueryIDs(ctx, m)
	}
	if m.TMDbID > 0 {
		return m.TMDbID, 0, 0
	}
	return 0, 0, 0
}

func (s *MediaSegmentService) episodeQueryIDs(ctx context.Context, m *model.Media) (tmdbID, season, episode int) {
	if m.SeasonNum <= 0 || m.EpisodeNum <= 0 {
		return 0, 0, 0
	}
	if seriesTMDbID := s.seriesTMDbID(ctx, m); seriesTMDbID > 0 {
		return seriesTMDbID, m.SeasonNum, m.EpisodeNum
	}
	if !s.mediaTMDbIDLooksLikeSeries(ctx, m) {
		return 0, 0, 0
	}
	return m.TMDbID, m.SeasonNum, m.EpisodeNum
}

// seriesTMDbID returns the series-level TMDb id, or 0 when the row has no
// Series association or that Series was never matched.
func (s *MediaSegmentService) seriesTMDbID(ctx context.Context, m *model.Media) int {
	if strings.TrimSpace(m.SeriesID) == "" {
		return 0
	}
	series, err := s.repo.Series.FindByID(ctx, m.SeriesID)
	if err != nil || series == nil || series.TMDbID <= 0 {
		return 0
	}
	return series.TMDbID
}

// mediaTMDbIDLooksLikeSeries reports whether Media.TMDbID can stand in for the
// series id: only an id shared by other episodes of the same show qualifies.
func (s *MediaSegmentService) mediaTMDbIDLooksLikeSeries(ctx context.Context, m *model.Media) bool {
	if s == nil || s.repo == nil || m == nil || m.TMDbID <= 0 {
		return false
	}
	return s.repo.Media.ExistsSiblingWithTMDbID(ctx, m)
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
