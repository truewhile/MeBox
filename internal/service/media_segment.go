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
//
// 未命中 TTL 刻意短于命中：社区库在持续补充，尤其是热门剧，24h 重试一次比
// 锁死 7 天更能跟上贡献节奏；预热任务也会优先扫最近播放过的 miss。
const (
	segmentFoundTTL   = 30 * 24 * time.Hour
	segmentMissingTTL = 24 * time.Hour

	// SegmentSourceManual / Propagated 与 IntroDBSource 并列，播放时按优先级合并。
	SegmentSourceManual     = "manual"
	SegmentSourcePropagated = "propagated"

	// segmentPrewarmDefaultLimit 是一次预热任务最多处理的媒体数，避免单次跑太久。
	segmentPrewarmDefaultLimit = 200
	// segmentPrewarmInterval 是预热请求之间的间隔，压低对 TheIntroDB 的 429。
	segmentPrewarmInterval = time.Second
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
	// probe 负责异步补齐媒体信息（主要是 STRM 时长）。未注入时只用 TheIntroDB。
	probe *MediaProbeService
	// prewarmGap 是预热两次 IntroDB 请求之间的间隔；测试可设为 0。
	prewarmGap time.Duration
	now        func() time.Time
}

// NewMediaSegmentService is the constructor.
func NewMediaSegmentService(log *zap.Logger, repo *repository.Container) *MediaSegmentService {
	return &MediaSegmentService{
		log:        log,
		repo:       repo,
		prewarmGap: segmentPrewarmInterval,
		now:        time.Now,
	}
}

// SetPrewarmGap overrides the delay between prewarm fetches (tests use 0).
func (s *MediaSegmentService) SetPrewarmGap(gap time.Duration) *MediaSegmentService {
	if s != nil {
		s.prewarmGap = gap
	}
	return s
}

// SetIntroDB wires the provider. Without it the service only reads cached rows.
func (s *MediaSegmentService) SetIntroDB(p *IntroDBService) *MediaSegmentService {
	if s != nil {
		s.introdb = p
	}
	return s
}

// SetProbe wires the async media probe (duration backfill for STRM). Without it
// open-ended credits still work once duration is known from elsewhere.
func (s *MediaSegmentService) SetProbe(p *MediaProbeService) *MediaSegmentService {
	if s != nil {
		s.probe = p
	}
	return s
}

// ListForPlayback returns the segments known for a media item, refreshing from
// the provider when the cache is stale.
//
// 它不做任何阻塞起播的事情——调用方是在播放已经开始之后用一次独立请求进来的，
// 抓取失败也只是少一个「跳过片头」按钮，绝不能让播放报错。
//
// 时间轴数据只来自 TheIntroDB。顺带在返回前起一次异步探测补齐媒体信息（主要是
// STRM 媒体的时长），但探测结果不参与片段判定，详见 MediaProbeService 的说明。
func (s *MediaSegmentService) ListForPlayback(ctx context.Context, m *model.Media) ([]model.MediaSegment, error) {
	if s == nil || s.repo == nil || m == nil || m.ID == "" {
		return nil, nil
	}
	// 用 defer 保证探测一定在本次请求所有数据库读写之后才启动：后台探测自己也要
	// 写库，若在本次写事务还没结束时启动，两个写事务会抢同一把锁（SQLite 下就是
	// SQLITE_BUSY，实测能直接把社区库的落库打失败）。
	defer s.ensureMediaProbe(ctx, m)
	return s.introDBSegments(ctx, m)
}

// ensureMediaProbe 在还没探过（或上次失败已过冷却期）时起一次异步探测。
//
// 它只为「补齐媒体信息」服务：失败只是拿不到时长，不影响播放，也不影响片段。
func (s *MediaSegmentService) ensureMediaProbe(ctx context.Context, m *model.Media) {
	if s == nil || s.probe == nil || m == nil {
		return
	}
	cached, err := s.repo.MediaProbe.Get(ctx, m.ID)
	if err != nil {
		s.debug("get media probe failed", m.ID, err)
		return
	}
	if mediaProbeSettled(cached) {
		return
	}
	s.probe.EnsureAsync(m)
}

// mediaProbeSettled 判断这部媒体的探测是否已经「有结论」——成功过，或者失败但还在
// 冷却期内。有结论就不必再探；失败且已过冷却期时返回 false，让下一次播放重试。
func mediaProbeSettled(row *model.MediaProbe) bool {
	if row == nil {
		return false
	}
	if strings.TrimSpace(row.LastError) == "" {
		return true
	}
	return time.Since(row.ProbedAt) < mediaProbeFailureRetry
}

// introDBSegments 读社区库的片段，缓存过期时按调用方的预算抓一次并落库。
// 返回值是按来源优先级合并后的结果（manual > theintrodb > propagated）。
func (s *MediaSegmentService) introDBSegments(ctx context.Context, m *model.Media) ([]model.MediaSegment, error) {
	ledger, err := s.repo.MediaSegment.GetFetch(ctx, m.ID, IntroDBSource)
	if err != nil {
		// 调用方预算耗尽时仍读本地缓存，绝不能让播放接口报错。
		if ctx.Err() != nil {
			return s.mergeForPlayback(context.WithoutCancel(ctx), m.ID)
		}
		return nil, err
	}
	if ledger == nil || !ledgerFresh(ledger) {
		if _, _, err := s.refresh(ctx, m); err != nil {
			// 社区库不可达或返回异常：沿用已有缓存，不影响播放；也不写负缓存。
			logIntroDBFailure(s.log, 0, err)
		}
	}
	// 合并读库只碰本地，脱离调用方 deadline，避免外网抓取耗尽预算后读缓存也失败。
	return s.mergeForPlayback(context.WithoutCancel(ctx), m.ID)
}

// mergeForPlayback 合并同一媒体上多来源片段。同 kind 只保留优先级最高的来源。
func (s *MediaSegmentService) mergeForPlayback(ctx context.Context, mediaID string) ([]model.MediaSegment, error) {
	rows, err := s.repo.MediaSegment.ListByMedia(ctx, mediaID)
	if err != nil {
		return nil, err
	}
	return preferSegmentsBySource(rows), nil
}

// segmentSourcePriority 数值越大越优先。未知来源视为最低，避免挡住已知源。
func segmentSourcePriority(source string) int {
	switch source {
	case SegmentSourceManual:
		return 3
	case IntroDBSource:
		return 2
	case SegmentSourcePropagated:
		return 1
	default:
		return 0
	}
}

// preferSegmentsBySource 按 kind 选取最高优先级来源的全部区间。
func preferSegmentsBySource(rows []model.MediaSegment) []model.MediaSegment {
	bestPri := make(map[string]int, 4)
	byKind := make(map[string][]model.MediaSegment, 4)
	for _, row := range rows {
		pri := segmentSourcePriority(row.Source)
		cur, seen := bestPri[row.Kind]
		if !seen || pri > cur {
			bestPri[row.Kind] = pri
			byKind[row.Kind] = []model.MediaSegment{row}
			continue
		}
		if pri == cur {
			byKind[row.Kind] = append(byKind[row.Kind], row)
		}
	}
	out := make([]model.MediaSegment, 0, len(rows))
	for _, kind := range []string{
		model.SegmentKindIntro, model.SegmentKindRecap,
		model.SegmentKindCredits, model.SegmentKindPreview,
	} {
		out = append(out, byKind[kind]...)
	}
	return out
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
	s.propagateIntroToSeason(fetchCtx, m, rows)
	return rows, true, nil
}

// propagateIntroToSeason 把本集 IntroDB 命中的 intro 复制到同季还没有
// theintrodb intro 的兄弟集。片尾/预告不传播：各集时长与片尾位置经常不同。
func (s *MediaSegmentService) propagateIntroToSeason(ctx context.Context, m *model.Media, rows []model.MediaSegment) {
	if s == nil || s.repo == nil || m == nil || m.SeasonNum <= 0 {
		return
	}
	intros := introSpansFrom(rows)
	if len(intros) == 0 {
		return
	}
	siblings, err := s.repo.Media.ListSeasonSiblings(ctx, m)
	if err != nil {
		s.debug("list season siblings failed", m.ID, err)
		return
	}
	for i := range siblings {
		sib := &siblings[i]
		existing, err := s.repo.MediaSegment.ListByMediaSource(ctx, sib.ID, IntroDBSource)
		if err != nil {
			s.debug("list sibling introdb segments failed", sib.ID, err)
			continue
		}
		if hasSegmentKind(existing, model.SegmentKindIntro) {
			continue
		}
		copied := make([]model.MediaSegment, 0, len(intros))
		for _, intro := range intros {
			copied = append(copied, model.MediaSegment{
				MediaID:  sib.ID,
				SeriesID: sib.SeriesID,
				Kind:     model.SegmentKindIntro,
				StartMs:  intro.StartMs,
				EndMs:    intro.EndMs,
				Source:   SegmentSourcePropagated,
			})
		}
		if err := s.repo.MediaSegment.ReplaceForMedia(ctx, sib.ID, SegmentSourcePropagated, copied); err != nil {
			s.debug("propagate intro failed", sib.ID, err)
		}
	}
}

func introSpansFrom(rows []model.MediaSegment) []model.MediaSegment {
	out := make([]model.MediaSegment, 0, 1)
	for _, row := range rows {
		if row.Kind == model.SegmentKindIntro {
			out = append(out, row)
		}
	}
	return out
}

func hasSegmentKind(rows []model.MediaSegment, kind string) bool {
	for _, row := range rows {
		if row.Kind == kind {
			return true
		}
	}
	return false
}

// Prewarm 批量向 TheIntroDB 补齐过期/缺失账本的可查询媒体。返回实际发起过查询的数量。
func (s *MediaSegmentService) Prewarm(ctx context.Context, limit int) (int, error) {
	if s == nil || s.repo == nil || s.introdb == nil {
		return 0, nil
	}
	if limit <= 0 {
		limit = segmentPrewarmDefaultLimit
	}
	now := time.Now()
	if s.now != nil {
		now = s.now()
	}
	candidates, err := s.repo.MediaSegment.ListPrewarmCandidates(
		ctx, IntroDBSource, now.Add(-segmentMissingTTL), now.Add(-segmentFoundTTL), limit,
	)
	if err != nil {
		return 0, err
	}
	attempted := 0
	for i := range candidates {
		if err := ctx.Err(); err != nil {
			return attempted, err
		}
		m := &candidates[i]
		_, did, err := s.refresh(ctx, m)
		if err != nil {
			logIntroDBFailure(s.log, m.TMDbID, err)
		}
		if did {
			attempted++
		}
		if i+1 < len(candidates) && s.prewarmGap > 0 {
			if !waitForIntroDBRetry(ctx, s.prewarmGap) {
				return attempted, ctx.Err()
			}
		}
	}
	return attempted, nil
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
