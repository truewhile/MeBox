// Package service — 片头/片尾片段（intro / recap / credits / preview）。
//
// 播放器只认本地库里的片段数据；外部提供方（当前为 TheIntroDB）在播放时按需
// 补齐并落库，因此同一部片第二次播放时不再产生任何外网请求。
package service

import (
	"context"
	"errors"
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
//
// 顺手丢掉明显不可能的区间：库里的历史数据可能是在 end_ms: 0 被误当成「到片尾」
// 的时期写下的（那会把整集变成片头），这些行如果原样下发，播放器仍然会整集弹
// 跳过按钮。只有片尾类允许开放结束点（EndMs == 0），其余要求 EndMs > StartMs。
func ToSegmentViews(rows []model.MediaSegment) []SegmentView {
	out := make([]SegmentView, 0, len(rows))
	for _, row := range rows {
		openEnded := row.Kind == model.SegmentKindCredits || row.Kind == model.SegmentKindPreview
		if row.StartMs < 0 {
			continue
		}
		if row.EndMs <= row.StartMs && !(openEnded && row.EndMs <= 0) {
			continue
		}
		out = append(out, SegmentView{Kind: row.Kind, StartMs: row.StartMs, EndMs: row.EndMs})
	}
	return out
}

// MediaSegmentService 负责把外部片头片尾数据补齐到本地并供播放器读取。
type MediaSegmentService struct {
	log  *zap.Logger
	repo *repository.Container
	// probe 负责异步补齐媒体信息（主要是 STRM 时长）。未注入时只用外部源。
	probe *MediaProbeService
	// prewarmGap 是预热两次请求之间的间隔；测试可设为 0。
	prewarmGap time.Duration
	now        func() time.Time
	// providers 是已启用的外部片段源，顺序即注册顺序。
	// 由设置项 segment.source 决定实际使用哪一个，见 activeProvider。
	providers []SegmentProvider
	// imdb 用于给只认 IMDb id 的源（IntroDB）解析外部 id。未注入时该源被跳过。
	imdb IMDbIDResolver
}

// IMDbIDResolver 把 TMDb 条目解析成 IMDb id（ttXXXXXXX）。
// 由 TMDbProvider 实现；抽出接口是为了让 service 层不直接依赖配置与 API key。
type IMDbIDResolver interface {
	ResolveIMDbID(ctx context.Context, tmdbID int, isMovie bool) (string, error)
}

// 片段数据源设置项。取值形如 off / theintrodb / introdb / todb，
// 与播放器档案里的「自动跳过」是两件独立的事。
const SegmentSourceSettingKey = "segment.source"

// SegmentSourceOff 表示不查询任何外部源，只消费已落库的数据（手动标记等）。
const SegmentSourceOff = "off"

// SegmentSourceOptions 是设置页可选的源标识。
var SegmentSourceOptions = []string{SegmentSourceOff, IntroDBSource, IntroDBAppSource, TODbSource}

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

// SetIntroDB wires the TheIntroDB provider and registers it as a source.
func (s *MediaSegmentService) SetIntroDB(p *IntroDBService) *MediaSegmentService {
	if s != nil {
		s.registerProvider(p)
	}
	return s
}

// SetIntroDBApp wires the IntroDB (introdb.app) provider.
func (s *MediaSegmentService) SetIntroDBApp(p *IntroDBAppService) *MediaSegmentService {
	if s != nil {
		s.registerProvider(p)
	}
	return s
}

// SetTODb wires the TODb provider.
func (s *MediaSegmentService) SetTODb(p *TODbService) *MediaSegmentService {
	if s != nil {
		s.registerProvider(p)
	}
	return s
}

// SetIMDbResolver wires the TMDb-backed IMDb id resolver used by sources that
// only address media by IMDb id.
func (s *MediaSegmentService) SetIMDbResolver(r IMDbIDResolver) *MediaSegmentService {
	if s != nil {
		s.imdb = r
	}
	return s
}

// registerProvider adds a provider once, keeping registration order as the
// fallback order.
func (s *MediaSegmentService) registerProvider(p SegmentProvider) {
	if s == nil || p == nil {
		return
	}
	for _, existing := range s.providers {
		if existing.Source() == p.Source() {
			return
		}
	}
	s.providers = append(s.providers, p)
}

// providerBySource returns the registered provider with that source tag.
func (s *MediaSegmentService) providerBySource(source string) SegmentProvider {
	for _, p := range s.providers {
		if p.Source() == source {
			return p
		}
	}
	return nil
}

// activeProvider 选出本次查询要用的源。
//
// 设置项没配置时沿用历史行为（TheIntroDB），避免升级后功能凭空消失；
// 显式配置成 off 才真正停用外部查询。
func (s *MediaSegmentService) activeProvider(ctx context.Context) SegmentProvider {
	if s == nil || len(s.providers) == 0 {
		return nil
	}
	source := s.configuredSource(ctx)
	if source == "" {
		return s.providerBySource(IntroDBSource)
	}
	if source == SegmentSourceOff {
		return nil
	}
	if p := s.providerBySource(source); p != nil {
		return p
	}
	// 配了一个不认识的源（多半是降级回滚后的残留设置）：退回默认源，
	// 让功能继续可用，而不是静默失效。
	return s.providerBySource(IntroDBSource)
}

// configuredSource reads the operator's source selection.
func (s *MediaSegmentService) configuredSource(ctx context.Context) string {
	if s == nil || s.repo == nil || s.repo.Setting == nil {
		return ""
	}
	v, err := s.repo.Setting.Get(ctx, SegmentSourceSettingKey)
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(v))
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
	return s.communitySegments(ctx, m)
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

// communitySegments 读社区库的片段，缓存过期时按调用方的预算抓一次并落库。
// 返回值是按来源优先级合并后的结果（manual > 外部源 > propagated）。
func (s *MediaSegmentService) communitySegments(ctx context.Context, m *model.Media) ([]model.MediaSegment, error) {
	provider := s.activeProvider(ctx)
	// 没选任何外部源（off）：只读本地已有数据，绝不发起外网请求。
	if provider == nil {
		return s.mergeForPlayback(context.WithoutCancel(ctx), m.ID)
	}
	ledger, err := s.repo.MediaSegment.GetFetch(ctx, m.ID, provider.Source())
	if err != nil {
		// 调用方预算耗尽时仍读本地缓存，绝不能让播放接口报错。
		if ctx.Err() != nil {
			return s.mergeForPlayback(context.WithoutCancel(ctx), m.ID)
		}
		return nil, err
	}
	if ledger == nil || !ledgerFresh(ledger) {
		if _, _, err := s.refreshWithProvider(ctx, m, provider); err != nil {
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
//
// 顺序体现用户意图的强度：
//
//	manual     用户在当前集显式标记
//	propagated 用户在同季其它集标记后继承来的值（仍然是人给的，优先于社区库）
//	外部社区库  自动抓取，最容易被覆盖
//
// 外部社区库之间刻意同级：用户一次只会启用其中一个，历史数据可能来自另一个，
// 同级时按区间起点排序展示即可，不必让旧源压住新源。
func segmentSourcePriority(source string) int {
	switch source {
	case SegmentSourceManual:
		return 3
	case SegmentSourcePropagated:
		return 2
	case IntroDBSource, IntroDBAppSource, TODbSource:
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

// refreshWithProvider 向指定来源查询并落库，返回 (rows, 是否真的发起过查询, error)。
//
// attempted=false 表示这部媒体缺少可查询的外部 ID（最常见的原因是还没刮削，
// 剧集也还没关联到 Series），或者该源需要的 id 类型拿不到（IntroDB 要 IMDb），
// 此时刻意不写负缓存：等元数据补齐后下次播放就能查到。
func (s *MediaSegmentService) refreshWithProvider(ctx context.Context, m *model.Media, provider SegmentProvider) ([]model.MediaSegment, bool, error) {
	if s == nil || provider == nil || m == nil {
		return nil, false, nil
	}
	query, ok := s.segmentQuery(ctx, m, provider)
	if !ok {
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

	spans, err := provider.FetchSegments(fetchCtx, query)
	if err != nil {
		return nil, true, err
	}
	source := provider.Source()
	rows := make([]model.MediaSegment, 0, len(spans))
	for _, span := range spans {
		rows = append(rows, model.MediaSegment{
			MediaID:  m.ID,
			SeriesID: m.SeriesID,
			Kind:     span.Kind,
			StartMs:  span.StartMs,
			EndMs:    span.EndMs,
			Source:   source,
		})
	}
	if err := s.repo.MediaSegment.ReplaceForMedia(fetchCtx, m.ID, source, rows); err != nil {
		return nil, true, err
	}
	if err := s.repo.MediaSegment.UpsertFetch(fetchCtx, &model.MediaSegmentFetch{
		MediaID:   m.ID,
		Source:    source,
		FetchedAt: time.Now(),
		Found:     len(rows) > 0,
	}); err != nil {
		return nil, true, err
	}
	s.propagateIntroToSeason(fetchCtx, m, rows)
	return rows, true, nil
}

// refresh 保留旧签名（预热任务与测试使用），走当前选中的源。
func (s *MediaSegmentService) refresh(ctx context.Context, m *model.Media) ([]model.MediaSegment, bool, error) {
	return s.refreshWithProvider(ctx, m, s.activeProvider(ctx))
}

// segmentQuery 组装该源需要的身份参数。返回 false 表示这个源缺少必要 ID，
// 本次不必发请求。
func (s *MediaSegmentService) segmentQuery(ctx context.Context, m *model.Media, provider SegmentProvider) (SegmentQuery, bool) {
	tmdbID, season, episode := s.queryIDs(ctx, m)
	isMovie := season <= 0 && episode <= 0
	q := SegmentQuery{TMDbID: tmdbID, Season: season, Episode: episode, IsMovie: isMovie}

	switch provider.Source() {
	case IntroDBAppSource:
		// IntroDB 只认 IMDb id，必须经 TMDb external_ids 解析。
		if s.imdb == nil || tmdbID <= 0 {
			return q, false
		}
		imdbID, err := s.imdb.ResolveIMDbID(ctx, tmdbID, isMovie)
		if err != nil || imdbID == "" {
			// 解析不出 IMDb id 不是错误：这部片可能本来就没有，或 TMDb 不可达。
			// 返回 false 让本次跳过，但不写负缓存，下次还有机会。
			s.debug("resolve imdb id failed", m.ID, err)
			return q, false
		}
		q.IMDbID = imdbID
		return q, true
	default:
		return q, tmdbID > 0
	}
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

// ManualMarkRequest 是「标记片头/片尾」的入参。
//
// PositionMs 是用户点击时所在的播放位置：标记片头时它是片头结束点，
// 标记片尾时它是片尾开始点。
type ManualMarkRequest struct {
	Kind       string
	PositionMs int64
	// Clear 为 true 表示清除该类型的标记，PositionMs 被忽略。
	Clear bool
}

// ErrInvalidManualMark 表示请求本身不合法（类型的取值或位置越界）。
var ErrInvalidManualMark = errors.New("invalid manual skip mark")

// SetManualMark 写入或清除一条手动标记，并按季传播片头/片尾。
//
// 手动标记的优先级最高（见 segmentSourcePriority），所以用户标过一次之后，
// 外部源即使给出不同位置也不会覆盖它。清除时只删 manual 来源的行，外部数据
// 会自然回落到优先队列里。
func (s *MediaSegmentService) SetManualMark(ctx context.Context, m *model.Media, req ManualMarkRequest) error {
	if s == nil || s.repo == nil || m == nil || m.ID == "" {
		return ErrInvalidManualMark
	}
	kind := normalizeManualMarkKind(req.Kind)
	if kind == "" {
		return ErrInvalidManualMark
	}
	if !req.Clear && req.PositionMs < 0 {
		return ErrInvalidManualMark
	}
	rows := []model.MediaSegment{}
	if !req.Clear {
		if req.PositionMs <= 0 {
			// 位置 0 的标记没有意义：片头在 0 秒结束等于没有片头。
			return ErrInvalidManualMark
		}
		rows = append(rows, model.MediaSegment{
			MediaID:  m.ID,
			SeriesID: m.SeriesID,
			Kind:     kind,
			StartMs:  manualMarkStart(kind),
			EndMs:    req.PositionMs,
			Source:   SegmentSourceManual,
		})
	}
	if err := s.repo.MediaSegment.ReplaceForMediaSourceKind(ctx, m.ID, SegmentSourceManual, kind, rows); err != nil {
		return err
	}
	if len(rows) > 0 {
		s.propagateManualMarkToSeason(ctx, m, rows)
	}
	return nil
}

// normalizeManualMarkKind 把外部传入的类型收敛到库内 kind。
// 只接受片头与片尾：回顾/预告没有对应的用户操作入口。
func normalizeManualMarkKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case model.SegmentKindIntro:
		return model.SegmentKindIntro
	case model.SegmentKindCredits:
		return model.SegmentKindCredits
	default:
		return ""
	}
}

// manualMarkStart 给出该类型区间的起点：片头从 0 开始，片尾的开始点就是用户
// 标记的位置（所以这里填 0，真正的起点在 EndMs 里由调用方传入）。
func manualMarkStart(kind string) int64 { return 0 }

// propagateManualMarkToSeason 把手动标记复制到同季还没有「显式手动标记」的兄弟集。
//
// 同一季的 OP/ED 通常一致，标一集即可覆盖整季——这是报告里最实用的行为。
// 关键约束：传播值必须写成 SegmentSourcePropagated 而不是 manual，这样才能区分
// 「用户显式标过的集」与「继承来的值」。否则先标 A 集会让 B 集看起来也是手动标
// 过的，之后用户再标 B 集时反而无法覆盖它（或反过来，B 集的传播值挡住 A 集的
// 新标记），两种顺序都会得出错误结果。
func (s *MediaSegmentService) propagateManualMarkToSeason(ctx context.Context, m *model.Media, rows []model.MediaSegment) {
	if s == nil || s.repo == nil || m == nil || m.SeasonNum <= 0 || len(rows) == 0 {
		return
	}
	siblings, err := s.repo.Media.ListSeasonSiblings(ctx, m)
	if err != nil {
		s.debug("list season siblings for manual mark failed", m.ID, err)
		return
	}
	for i := range siblings {
		sib := &siblings[i]
		// 用户显式标过的集保持原样；继承值可以被新的显式标记覆盖。
		explicit, err := s.repo.MediaSegment.ListByMediaSource(ctx, sib.ID, SegmentSourceManual)
		if err != nil {
			s.debug("list sibling manual segments failed", sib.ID, err)
			continue
		}
		if hasSegmentKind(explicit, rows[0].Kind) {
			continue
		}
		copied := make([]model.MediaSegment, 0, len(rows))
		for _, row := range rows {
			copied = append(copied, model.MediaSegment{
				MediaID:  sib.ID,
				SeriesID: sib.SeriesID,
				Kind:     row.Kind,
				StartMs:  row.StartMs,
				EndMs:    row.EndMs,
				Source:   SegmentSourcePropagated,
			})
		}
		if err := s.repo.MediaSegment.ReplaceForMediaSourceKind(ctx, sib.ID, SegmentSourcePropagated, rows[0].Kind, copied); err != nil {
			s.debug("propagate manual mark failed", sib.ID, err)
		}
	}
}

// Prewarm 批量补齐过期/缺失账本的可查询媒体。返回实际发起过查询的数量。
//
// 只预热当前启用的源：用户把源切成 off 时不该继续打外网，切换源之后旧源的
// 账本也不再需要刷新。
func (s *MediaSegmentService) Prewarm(ctx context.Context, limit int) (int, error) {
	if s == nil || s.repo == nil || len(s.providers) == 0 {
		return 0, nil
	}
	provider := s.activeProvider(ctx)
	if provider == nil {
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
		ctx, provider.Source(), now.Add(-segmentMissingTTL), now.Add(-segmentFoundTTL), limit,
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
		_, did, err := s.refreshWithProvider(ctx, m, provider)
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
