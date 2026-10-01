// STRM 同步引擎：扫描网盘/本地目录，生成 .strm 文件，按需入队元数据下载/上传，
// 并清理远端已不存在的本地多余文件。参考 QMediaSync 的 STRM 同步流程实现。
package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/helper"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/service/cloud"
	"github.com/truewhile/MeBox/internal/service/cloud115"
)

var errFallbackToWalkRemote = errors.New("fallback to walk remote")

// remoteMetaItem 记录远端存在的单个元数据文件副本信息（大小、文件ID、内容SHA1、修改时间）。
type remoteMetaItem struct {
	ID    string
	Size  int64
	Sha1  string
	MTime int64
}

// strmSyncState 是一次同步执行的上下文。
type strmSyncState struct {
	s        *StrmService
	ctx      context.Context
	p        *model.StrmSyncPath
	acct     *model.StrmAccount
	provider cloud.Provider // local 提供方为 nil
	cfg      *strmPathConfig
	rec      *model.StrmSyncRecord
	syncType string

	mu                sync.Mutex
	processed         int                         // 已处理文件计数（用于定期落库进度）
	lastProgressFlush time.Time                   // 上次进度落库时间
	seenVideo         map[string]bool             // "v:"+strm 去扩展名相对路径 → 远端存在该视频（供 prune）
	seenDir           map[string]bool             // 清洗后的目录相对路径 → 远端存在该目录（供整目录 prune）
	seenMeta          map[string]bool             // "m:"+相对路径 → 远端存在该元数据
	remoteMeta        map[string][]remoteMetaItem // "m:"+相对路径 → 远端元数据副本列表（多副本聚合，支持择优比对与冗余清理）
	seenMetaTarget    map[string]cloud.FileEntry
	seenVideoTarget   map[string]cloud.FileEntry
	// remoteVideos：prefer 模式下同名（去扩展名）视频候选列表，walk 结束后择优写盘
	remoteVideos        map[string][]remoteVideoCandidate
	activeDownloadPaths map[string]bool // 本地已在排队/进行的下载任务路径（内存去重）
	activeUploadPaths   map[string]bool // 本地已在排队/进行的上传任务路径（内存去重）
	// recentDoneUploadSizes：近期已成功上传的 local_path → size，缩短「done 但列表未到」窗口内的重复入队
	recentDoneUploadSizes map[string]int64
	pendingDownloads      []*model.StrmDownloadTask
	pendingUploads        []*model.StrmUploadTask
	dirCache              sync.Map          // dirID (string) -> relativePath (string)
	dirPathToID           map[string]string // relativePath (string) -> dirID（115 上传父目录寻址用，walk 后构建）
	dirCacheDirty         map[string]string // 待批量落库的目录缓存（dirID → 相对路径），避免逐目录单条 upsert

	scanIncomplete atomic.Bool // 远端目录树/文件列表本次扫描不完整 → 禁止增量 prune 误删本地文件
}

// remoteVideoCandidate 记录同名视频的一个远端副本（不同扩展名/体积）。
type remoteVideoCandidate struct {
	entry cloud.FileEntry
	rel   string
	ext   string
}

// StartSync 启动一次同步（异步执行，同一目录同时只允许一个任务）。
// syncType 支持 "incremental"（默认增量）和 "full"（全量同步）。
func (s *StrmService) StartSync(ctx context.Context, pathID string, syncType ...string) error {
	p, err := s.repo.StrmSyncPath.FindByID(ctx, pathID)
	if err != nil || p == nil {
		return errNotFoundOr(err, "同步目录不存在")
	}
	if !p.Enabled {
		return errors.New("同步目录已禁用")
	}
	if p.Provider == model.StrmProviderLocal && strings.TrimSpace(p.RemotePath) == "" {
		return errors.New("本地同步需要填写源目录")
	}
	if p.Provider != model.StrmProviderLocal && strings.TrimSpace(p.AccountID) == "" {
		return errors.New("该同步目录未关联网盘账号")
	}
	s.mu.Lock()
	if _, exists := s.running[pathID]; exists {
		s.mu.Unlock()
		return errors.New("该目录正在同步中")
	}
	// 同步在后台持续执行，不受 HTTP 请求生命周期影响
	runCtx, cancel := context.WithCancel(s.baseCtx)
	s.running[pathID] = cancel
	s.mu.Unlock()

	mode := model.StrmSyncTypeIncremental
	if len(syncType) > 0 && syncType[0] != "" {
		mode = syncType[0]
	} else if p.SyncMode != "" {
		mode = p.SyncMode
	}
	if mode != model.StrmSyncTypeFull {
		mode = model.StrmSyncTypeIncremental
	}

	now := time.Now()
	rec := &model.StrmSyncRecord{
		SyncPathID: pathID,
		SyncType:   mode,
		Status:     model.StrmSyncRecordRunning,
		StartedAt:  &now,
	}
	if err := s.repo.StrmSyncRecord.Create(ctx, rec); err != nil {
		s.clearRunning(pathID, cancel)
		cancel()
		return err
	}
	status := model.StrmSyncRecordRunning
	p.LastSyncAt = &now
	p.LastSyncStatus = status
	p.LastSyncMessage = "同步进行中"
	_ = s.repo.StrmSyncPath.Update(ctx, p)

	helper.Go(s.log, "strm.sync", func() { s.runSync(runCtx, p, rec, cancel) })
	return nil
}

// CancelSync 取消正在进行的同步（若为僵尸运行状态则直接自愈重置）。
func (s *StrmService) CancelSync(ctx context.Context, pathID string) error {
	s.mu.Lock()
	cancel, exists := s.running[pathID]
	s.mu.Unlock()

	// 不在这里预删 running 标记：runSync 退出时的 clearRunning 会按
	// cancel 身份校验后删除，避免旧同步收尾误删新同步的标记。
	if exists && cancel != nil {
		cancel()
	}

	// 无论内存中是否活跃，确保同步目录状态正确重置为已取消
	if p, err := s.repo.StrmSyncPath.FindByID(ctx, pathID); err == nil && p != nil {
		if p.LastSyncStatus == model.StrmSyncRecordRunning {
			p.LastSyncStatus = model.StrmSyncRecordCanceled
			p.LastSyncMessage = "已取消"
			_ = s.repo.StrmSyncPath.Update(ctx, p)
		}
	}
	return nil
}

// IsSyncRunning 报告某同步目录是否正在同步。
func (s *StrmService) IsSyncRunning(pathID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, exists := s.running[pathID]
	return exists
}

// clearRunning 清除同步的运行标记；仅当 map 中登记的 cancel 与本次同步
// 一致时才删除，防止慢收尾的旧同步把随后启动的新同步标记误删掉。
func (s *StrmService) clearRunning(pathID string, cancel context.CancelFunc) {
	s.mu.Lock()
	if cur, ok := s.running[pathID]; ok {
		if cancel == nil || cur == nil || sameCancelFunc(cur, cancel) {
			delete(s.running, pathID)
		}
	}
	s.mu.Unlock()
}

// sameCancelFunc 比较两个 cancel 是否为同一实例（每次 WithCancel 返回
// 独立闭包，函数指针即身份）。约定 running 表只登记 StartSync 的 cancel。
func sameCancelFunc(a, b context.CancelFunc) bool {
	return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
}

// ListRemoteDir 列出网盘账号某目录下的条目（供前端目录选择器使用）。
func (s *StrmService) ListRemoteDir(ctx context.Context, accountID, dir string) ([]cloud.FileEntry, error) {
	acct, err := s.repo.StrmAccount.FindByID(ctx, accountID)
	if err != nil || acct == nil {
		return nil, errNotFoundOr(err, "网盘账号不存在")
	}
	provider, err := s.providerFor(ctx, acct)
	if err != nil {
		return nil, err
	}
	if dir == "" {
		if acct.Provider == model.StrmProvider115 {
			dir = "0"
		} else {
			dir = "/"
		}
	}
	return provider.List(ctx, dir)
}

// ResolveRemoteDirPath 解析远端目录的完整展示路径。115 的目录以 ID 存储，
// 用户无法辨认，这里按 ID 反查 115 返回的祖先链拼出人类可读路径；路径型
// 网盘（CD2/OpenList）与本地目录的 remote_path 本身就是路径，原样返回。
func (s *StrmService) ResolveRemoteDirPath(ctx context.Context, accountID, dir string) (string, error) {
	acct, err := s.repo.StrmAccount.FindByID(ctx, accountID)
	if err != nil || acct == nil {
		return "", errNotFoundOr(err, "网盘账号不存在")
	}
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", nil
	}
	if acct.Provider != model.StrmProvider115 {
		return dir, nil
	}
	if dir == "0" {
		return "", nil
	}
	provider, err := s.providerFor(ctx, acct)
	if err != nil {
		return "", err
	}
	oc, ok := provider.(interface{ OpenClient() *cloud115.OpenClient })
	if !ok {
		return "", fmt.Errorf("115: 客户端初始化失败")
	}
	detail, err := oc.OpenClient().GetFsDetailByCid(ctx, dir)
	if err != nil {
		return "", fmt.Errorf("115: 解析目录路径失败：%w", err)
	}
	return cloud115FullPath(detail), nil
}

// cloud115FullPath 把 115 目录详情的祖先链拼成以 / 开头的完整路径。
func cloud115FullPath(detail *cloud115.RemoteFileDetail) string {
	if detail == nil {
		return ""
	}
	segments := make([]string, 0, len(detail.Paths)+1)
	hasSelf := false
	for _, p := range detail.Paths {
		if p.FileId == "0" || p.FileId == "" {
			continue
		}
		if p.FileId == detail.FileId {
			hasSelf = true
		}
		if name := strings.TrimSpace(p.Name); name != "" {
			segments = append(segments, name)
		}
	}
	if !hasSelf && strings.TrimSpace(detail.FileName) != "" {
		segments = append(segments, strings.TrimSpace(detail.FileName))
	}
	if len(segments) == 0 {
		return ""
	}
	return "/" + strings.Join(segments, "/")
}

// runSync 执行同步主体；结束时更新记录与目录状态。
func (s *StrmService) runSync(ctx context.Context, p *model.StrmSyncPath, rec *model.StrmSyncRecord, cancel context.CancelFunc) {
	defer s.clearRunning(p.ID, cancel)
	defer cancel()

	cfg, err := s.strmEffectiveConfig(ctx, p)
	if err != nil {
		s.finishSync(p, rec, model.StrmSyncRecordFailed, err.Error())
		return
	}
	st := &strmSyncState{
		s:               s,
		ctx:             ctx,
		p:               p,
		cfg:             cfg,
		rec:             rec,
		syncType:        rec.SyncType,
		seenVideo:       map[string]bool{},
		seenDir:         map[string]bool{"": true},
		seenMeta:        map[string]bool{},
		remoteMeta:      map[string][]remoteMetaItem{},
		seenMetaTarget:  map[string]cloud.FileEntry{},
		seenVideoTarget: map[string]cloud.FileEntry{},
		remoteVideos:    map[string][]remoteVideoCandidate{},
	}
	if p.Provider != model.StrmProviderLocal {
		acct, err := s.repo.StrmAccount.FindByID(ctx, p.AccountID)
		if err != nil || acct == nil {
			s.finishSync(p, rec, model.StrmSyncRecordFailed, "网盘账号不存在或已删除")
			return
		}
		if !acct.Enabled {
			s.finishSync(p, rec, model.StrmSyncRecordFailed, "网盘账号已禁用")
			return
		}
		provider, err := s.providerFor(ctx, acct)
		if err != nil {
			s.finishSync(p, rec, model.StrmSyncRecordFailed, err.Error())
			return
		}
		st.acct = acct
		st.provider = provider
	}

	if err := st.run(); err != nil {
		if errors.Is(err, context.Canceled) {
			s.finishSync(p, rec, model.StrmSyncRecordCanceled, "已取消")
		} else {
			s.finishSync(p, rec, model.StrmSyncRecordFailed, err.Error())
		}
		return
	}
	s.finishSync(p, rec, model.StrmSyncRecordDone, "")
}

// finishSync 落库同步结果。
func (s *StrmService) finishSync(p *model.StrmSyncPath, rec *model.StrmSyncRecord, status, message string) {
	now := time.Now()
	rec.Status = status
	rec.Message = message
	rec.FinishedAt = &now
	if err := s.repo.StrmSyncRecord.Update(context.Background(), rec); err != nil {
		s.log.Warn("update strm sync record failed", zap.Error(err))
	}
	p.LastSyncStatus = status
	p.LastSyncMessage = message
	if status != model.StrmSyncRecordFailed && message == "" {
		syncTypeLabel := "增量"
		if rec.SyncType == model.StrmSyncTypeFull {
			syncTypeLabel = "全量"
		}
		p.LastSyncMessage = fmt.Sprintf("[%s] 完成：新增/更新 %d 个 strm，跳过 %d 个，下载 %d 个元数据，上传 %d 个元数据，清理 %d 个文件",
			syncTypeLabel, rec.NewStrm, rec.Skipped, rec.NewMeta, rec.Uploaded, rec.Pruned)
	}
	if err := s.repo.StrmSyncPath.Update(context.Background(), p); err != nil {
		s.log.Warn("update strm sync path failed", zap.Error(err))
	}
	s.log.Info("strm sync finished",
		zap.String("path_id", p.ID), zap.String("sync_type", rec.SyncType), zap.String("status", status),
		zap.Int64("new_strm", rec.NewStrm), zap.Int64("skipped", rec.Skipped), zap.Int64("new_meta", rec.NewMeta),
		zap.Int64("uploaded", rec.Uploaded), zap.Int64("pruned", rec.Pruned), zap.String("message", message))
}

func (st *strmSyncState) run() error {
	if err := ensureLocalDir(st.p.LocalPath); err != nil {
		return fmt.Errorf("创建输出目录失败：%w", err)
	}
	if st.cfg.DownloadMeta {
		if active, err := st.s.repo.StrmDownload.GetActiveLocalPathMap(st.ctx, st.p.ID); err == nil {
			st.activeDownloadPaths = active
		} else {
			st.activeDownloadPaths = map[string]bool{}
		}
	}
	if st.cfg.UploadMeta {
		if active, err := st.s.repo.StrmUpload.GetActiveLocalPathMap(st.ctx, st.p.ID); err == nil {
			st.activeUploadPaths = active
		} else {
			st.activeUploadPaths = map[string]bool{}
		}
		since := time.Now().Add(-strmRecentUploadSkipWindow)
		if recent, err := st.s.repo.StrmUpload.GetRecentDoneUploadSizeMap(st.ctx, st.p.ID, since); err == nil {
			st.recentDoneUploadSizes = recent
		} else {
			st.recentDoneUploadSizes = map[string]int64{}
			st.s.log.Warn("加载近期已完成上传任务失败，跳过 done 窗口去重",
				zap.String("path_id", st.p.ID), zap.Error(err))
		}
	}

	if st.provider != nil {
		if open115, ok := st.provider.(cloud.OpenAPI115Provider); ok && st.p.Provider == model.StrmProvider115 {
			err := st.walk115Flat(open115.OpenClient())
			if err != nil {
				if errors.Is(err, errFallbackToWalkRemote) {
					st.s.log.Warn("115: 扁平列表文件数超限，自动降级为传统并发递归同步",
						zap.String("path_id", st.p.ID))
					if errWalk := st.walkRemote(); errWalk != nil {
						return errWalk
					}
				} else {
					return err
				}
			}
		} else {
			if err := st.walkRemote(); err != nil {
				return err
			}
		}
	} else {
		if err := st.walkLocalSource(); err != nil {
			return err
		}
	}
	st.flushPreferredVideos()
	st.flushPendingDownloads()
	st.flushProgress()
	if st.cfg.UploadMeta && st.provider != nil {
		// 115 上传需要父目录 cid，先用 dirCache 构建「路径 → cid」反向索引
		if st.p.Provider == model.StrmProvider115 {
			reversed := map[string]string{}
			st.dirCache.Range(func(key, value any) bool {
				path, ok := value.(string)
				if ok && path != "" {
					if id, ok2 := key.(string); ok2 {
						reversed[path] = id
					}
				}
				return true
			})
			st.dirPathToID = reversed
		}
		if err := st.scanLocalMetaForUpload(); err != nil {
			return err
		}
		st.flushPendingUploads()
	}
	if err := st.pruneLocal(); err != nil {
		return err
	}
	st.flushProgress()
	_ = st.ctx.Err()
	return nil
}

// strmScanWorkers 远端目录树并发遍历的 worker 数。115 开放平台有全局
// 令牌桶限流（QPS/QPM/QPH），并发请求自动排队，不会触发风控；并发让
// 多个目录列表请求的网络往返彼此重叠，大幅缩短大目录树同步耗时。
const strmScanWorkers = 8

// strmProcessWorkers 115 平铺拉取后本地文件分类处理（生成 strm/元数据入队）
// 的 worker 数。本地磁盘 I/O 是大库同步的尾部瓶颈，输出目录在网络挂载上尤甚；
// processRemoteFile 的共享状态均由 st.mu 保护，可安全并发。
const strmProcessWorkers = 8

// walkRemote 并发广度优先遍历网盘目录树。
// 多个 worker 并行执行 List（受全局 115 令牌桶限流约束），子目录动态
// 入队；任一目录失败则取消其余 worker 并返回错误（与旧串行版语义一致）。
func (st *strmSyncState) walkRemote() error {
	defer st.flushPendingDownloads()
	root := strings.TrimSpace(st.p.RemotePath)
	if root == "" {
		root = "/"
	}
	type dirTask struct {
		id  string
		rel string
	}

	ctx, cancel := context.WithCancel(st.ctx)
	defer cancel()

	// 工作队列用「互斥锁 + 条件变量 + 动态 slice」实现，而不是有界
	// channel：有界缓冲下所有 worker 可能同时阻塞在发送上、无人接收，
	// closer 又在等 pending 归零，形成永久死锁。push 永不阻塞即可保证
	// 有进度就一定有推进。
	// pending 计数 = 尚未处理完的任务数（在 work 里或正在被 List）。
	var (
		walkMu   sync.Mutex
		walkCond = sync.NewCond(&walkMu)
		work     []dirTask
		pending  int
	)
	push := func(t dirTask) {
		walkMu.Lock()
		work = append(work, t)
		pending++
		walkCond.Signal()
		walkMu.Unlock()
	}
	// ctx 取消时唤醒所有等待中的 worker 让其退出。
	go func() {
		<-ctx.Done()
		walkMu.Lock()
		walkCond.Broadcast()
		walkMu.Unlock()
	}()

	// 根目录入队
	push(dirTask{id: root, rel: ""})

	var (
		wg       sync.WaitGroup
		errMu    sync.Mutex
		firstErr error
	)
	for i := 0; i < strmScanWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// worker 解析远端响应 panic 时取消整个同步，让其余 worker
			// 正常收尾；正常退出不取消。
			if err := helper.Recover(st.s.log, "strm.sync.walkRemote", func() error {
				for {
					walkMu.Lock()
					for len(work) == 0 {
						if ctx.Err() != nil || pending == 0 {
							walkMu.Unlock()
							return nil
						}
						walkCond.Wait()
					}
					task := work[0]
					work = work[1:]
					walkMu.Unlock()

					if ctx.Err() != nil {
						walkMu.Lock()
						pending--
						walkCond.Broadcast()
						walkMu.Unlock()
						return nil
					}
					entries, err := st.provider.List(ctx, task.id)
					if err != nil {
						errMu.Lock()
						if firstErr == nil {
							firstErr = fmt.Errorf("列出远端目录 %s 失败：%w", task.id, err)
						}
						errMu.Unlock()
						walkMu.Lock()
						pending--
						walkCond.Broadcast()
						walkMu.Unlock()
						cancel()
						return nil
					}
					for _, entry := range entries {
						cleanName := cleanEntryName(entry.Name, entry.IsDir)
						rel := cleanName
						if task.rel != "" {
							rel = task.rel + "/" + cleanName
						}
						if entry.IsDir {
							st.markSeenDir(rel)
							st.dirCache.Store(entry.ID, rel)
							st.deferDirCacheSave(entry.ID, rel)
							push(dirTask{id: entry.ID, rel: rel})
						} else {
							st.processRemoteFile(entry, rel)
						}
					}
					walkMu.Lock()
					pending--
					if pending == 0 {
						walkCond.Broadcast()
					}
					walkMu.Unlock()
				}
			}); err != nil {
				cancel()
			}
		}()
	}
	wg.Wait()
	st.flushDirCacheSave()
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}

// processRemoteFile 分类处理远端文件：视频生成 STRM，元数据入下载队列。
func (st *strmSyncState) processRemoteFile(entry cloud.FileEntry, rel string) {
	st.markSeenDir(filepath.ToSlash(filepath.Dir(rel)))
	fileName := entry.Name
	if st.isExcluded(fileName) {
		return
	}
	ext := strings.ToLower(filepath.Ext(fileName))
	switch {
	case st.isVideoExt(ext, entry.Size):
		st.handleVideo(entry, rel, ext)
	case st.isMetaExt(ext):
		st.recordRemoteMeta(entry, rel)
		if st.cfg.DownloadMeta {
			st.handleMeta(entry, rel, ext)
		} else {
			st.touchProgress()
		}
	default:
		st.touchProgress()
	}
}

func (st *strmSyncState) isExcluded(fileName string) bool {
	lower := strings.ToLower(fileName)
	for _, keyword := range st.cfg.ExcludeName {
		if keyword != "" && strings.Contains(lower, keyword) {
			return true
		}
	}
	return false
}

func (st *strmSyncState) isVideoExt(ext string, size int64) bool {
	if st.cfg.MinSize > 0 && size < st.cfg.MinSize {
		return false
	}
	for _, e := range st.cfg.VideoExt {
		if "."+e == ext {
			return true
		}
	}
	return false
}

func (st *strmSyncState) isMetaExt(ext string) bool {
	// Legacy MeBox installs may have persisted strm.meta_ext without img.
	// .img is emitted by the artwork writer as a fallback image container, so
	// keep accepting existing sidecars without requiring a settings migration.
	if strings.EqualFold(ext, ".img") {
		return true
	}
	for _, e := range st.cfg.MetaExt {
		if "."+e == ext {
			return true
		}
	}
	return false
}

// cleanDirRel 对 115 扁平化拉取的目录相对路径逐段套用目录级文件名清洗，
// 确保与 walkRemote / joinLocalRel（sanitizeRelativePath）使用同一套清洗规则。
// 若不清洗，目录名中的冒号等非法字符会直达 rel，而 seenVideo/remoteMeta 的 key
// 与磁盘实际路径不一致，导致 pruneLocal 误删已下载的 strm、上传误传或重复下载。
// 空 rel（根目录）原样返回。
func cleanDirRel(rel string) string {
	if rel == "" {
		return ""
	}
	parts := strings.Split(rel, "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		clean := cleanEntryName(part, true)
		if clean != "" && clean != "." && clean != ".." {
			out = append(out, clean)
		}
	}
	return strings.Join(out, "/")
}

// deferDirCacheSave 暂存一条目录缓存写入，由 flushDirCacheSave 统一批量落库。
// 首次全量同步可能有上万个目录，逐目录单条 upsert 会造成明显的 SQLite 写锁
// 竞争；内存 dirCache（sync.Map）始终即时可用，落库仅服务于下次增量预加载。
func (st *strmSyncState) deferDirCacheSave(dirID, relPath string) {
	st.mu.Lock()
	if st.dirCacheDirty == nil {
		st.dirCacheDirty = map[string]string{}
	}
	st.dirCacheDirty[dirID] = relPath
	st.mu.Unlock()
}

// flushDirCacheSave 把暂存的目录缓存一次性批量落库；失败仅记日志（缓存缺失
// 只影响下次增量的目录解析提速，正确性由"重新向 115 获取"兜底）。
func (st *strmSyncState) flushDirCacheSave() {
	st.mu.Lock()
	dirty := st.dirCacheDirty
	st.dirCacheDirty = nil
	st.mu.Unlock()
	if len(dirty) == 0 {
		return
	}
	if err := st.s.repo.StrmDirCache.SetBatch(st.ctx, st.p.ID, dirty); err != nil {
		st.s.log.Warn("batch save strm dir cache failed",
			zap.Error(err), zap.Int("count", len(dirty)), zap.String("path_id", st.p.ID))
	}
}

// walk115Flat 使用 115 开放平台扁平化分页批量拉取机制与目录拓扑缓存（参考 QMediaSync）。
// 极大地降低 API 请求次数并支持毫秒级/秒级增量同步。
func (st *strmSyncState) walk115Flat(open115 *cloud115.OpenClient) error {
	defer st.flushPendingDownloads()
	ctx, cancel := context.WithCancel(st.ctx)
	defer cancel()
	rootCID := strings.TrimSpace(st.p.RemotePath)
	if rootCID == "" {
		rootCID = "0"
	}

	// 1. 目录拓扑缓存处理
	st.dirCache.Store(rootCID, "")
	if st.syncType == model.StrmSyncTypeFull {
		// 全量同步：清空本路径的历史目录缓存
		if err := st.s.repo.StrmDirCache.DeleteBySyncPathID(ctx, st.p.ID); err != nil {
			st.s.log.Warn("delete strm dir cache failed", zap.Error(err))
		}
	} else {
		// 增量同步：预加载历史目录缓存（过滤历史一对多塌陷冲突的脏数据以自愈刷新）
		cached, err := st.s.repo.StrmDirCache.ListBySyncPathID(ctx, st.p.ID)
		if err == nil {
			pathCounts := make(map[string]int, len(cached))
			for _, item := range cached {
				pathCounts[item.Path]++
			}
			for _, item := range cached {
				// 若同一个 path 对应了多个不同 dir_id，说明包含历史层级塌陷的脏数据，不预加载，让后续步骤重新向 115 获取精确路径
				if pathCounts[item.Path] > 1 {
					continue
				}
				st.dirCache.Store(item.DirID, cleanDirRel(item.Path))
			}
		}
	}

	// 2. 自适应分治拉取文件列表（单目录超 9500 时自动对子目录并发分治扁平化）
	allFiles, err := st.fetch115FilesAdaptive(ctx, open115, rootCID)
	if err != nil {
		return err
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}

	// 4. 收集所有未在缓存中的父目录 ID (file.Pid)
	missingPids := make(map[string]struct{})
	for _, f := range allFiles {
		pid := f.Pid
		if pid == "" || pid == rootCID {
			continue
		}
		if _, ok := st.dirCache.Load(pid); !ok {
			missingPids[pid] = struct{}{}
		}
	}

	// 并发补全未知目录详情与祖先链
	if len(missingPids) > 0 {
		pidList := make([]string, 0, len(missingPids))
		for pid := range missingPids {
			pidList = append(pidList, pid)
		}

		pidCh := make(chan string, len(pidList))
		for _, pid := range pidList {
			pidCh <- pid
		}
		close(pidCh)

		var (
			pwg        sync.WaitGroup
			dirWorkers = 8
			doneDirs   atomic.Int64
			totalDirs  = len(pidList)
			errMu      sync.Mutex
			firstErr   error
		)
		if len(pidList) < dirWorkers {
			dirWorkers = len(pidList)
		}

		st.updateSyncMessage(fmt.Sprintf("正在解析目录树 (0/%d)...", totalDirs))

		for i := 0; i < dirWorkers; i++ {
			pwg.Add(1)
			go func() {
				defer pwg.Done()
				// 解析目录详情 panic 时中止整个同步（避免带着损坏的相对路径
				// 继续执行）；正常退出不取消。
				if err := helper.Recover(st.s.log, "strm.sync.walk115.dirTree", func() error {
					for pid := range pidCh {
						if ctx.Err() != nil {
							return nil
						}
						if _, loaded := st.dirCache.Load(pid); loaded {
							if n := doneDirs.Add(1); n%20 == 0 || n == int64(totalDirs) {
								st.updateSyncMessage(fmt.Sprintf("正在解析目录树 (%d/%d)...", n, totalDirs))
							}
							continue
						}
						detail, err := open115.GetFsDetailByCid(ctx, pid)
						if err != nil {
							// 目录详情解析失败会导致下游文件 rel 无法还原真实父路径，
							// seen key 与磁盘路径对不上：增量 prune 会误删本地文件、上传会
							// 误传本地未变文件、下载会重复下载。这里不是降级容错，而是
							// 直接中止整个同步——宁可本次同步失败，也不带着损坏的相对路径
							// 继续执行造成大规模误删/误传/重下（参考用户反馈"云盘没动却重下重传"）。
							errMu.Lock()
							if firstErr == nil {
								firstErr = fmt.Errorf("115: 解析目录树失败（file_id=%s）：%w", pid, err)
							}
							errMu.Unlock()
							st.scanIncomplete.Store(true)
							cancel()
							return nil
						} else if detail != nil {
							// 解析相对路径
							relPath := cleanDirRel(detail.RelativePath(rootCID))
							st.dirCache.Store(pid, relPath)
							st.deferDirCacheSave(pid, relPath)

							// 顺便解析并缓存 detail.Paths 中包含的中间各层级目录
							for _, ancestor := range detail.Paths {
								if ancestor.FileId == "0" || ancestor.FileId == rootCID {
									continue
								}
								if _, loaded := st.dirCache.Load(ancestor.FileId); !loaded {
									subDetail := &cloud115.RemoteFileDetail{
										FileId:   ancestor.FileId,
										FileName: ancestor.Name,
										Paths:    nil,
									}
									for _, p := range detail.Paths {
										subDetail.Paths = append(subDetail.Paths, p)
										if p.FileId == ancestor.FileId {
											break
										}
									}
									ancestorRel := cleanDirRel(subDetail.RelativePath(rootCID))
									st.dirCache.Store(ancestor.FileId, ancestorRel)
									st.deferDirCacheSave(ancestor.FileId, ancestorRel)
								}
							}
						}
						if n := doneDirs.Add(1); n%10 == 0 || n == int64(totalDirs) {
							st.updateSyncMessage(fmt.Sprintf("正在解析目录树 (%d/%d)...", n, totalDirs))
						}
					}
					return nil
				}); err != nil {
					cancel()
				}
			}()
		}
		pwg.Wait()
		// 目录解析阶段结束即批量落库已解析的缓存：失败路径也保留部分成果，
		// 下次同步可少解析一批目录。
		st.flushDirCacheSave()
		if firstErr != nil {
			// 目录树解析失败会导致 rel 塌缩，若继续处理会让大量本地文件
			// 被错误判定为"云端不存在"而重复下载/上传，并可能误删本地文件。
			// 中止本次同步，避免在损坏的相对路径上执行任何写操作。
			return firstErr
		}
	}

	st.updateSyncMessage(fmt.Sprintf("正在生成 STRM 与同步文件 (共 %d 个)...", len(allFiles)))

	// 5. 分类处理所有文件。本地磁盘 I/O（Stat/读内容比对/写盘）远慢于列表
	// 拉取，串行消化是大库同步的尾部瓶颈（输出目录在网络挂载上尤甚）；
	// processRemoteFile 的共享状态均由 st.mu 保护（walkRemote 已并发调用），
	// 这里用有界 worker 池并行处理。rel 构建依赖 dirCache 且需在父目录缺失
	// 时整体中止，保留在生产者侧串行完成。
	type strmFileTask struct {
		file cloud115.RemoteFile
		rel  string
	}
	fileCh := make(chan strmFileTask)
	var (
		procWg    sync.WaitGroup
		procErrMu sync.Mutex
		procErr   error
	)
	for i := 0; i < strmProcessWorkers; i++ {
		procWg.Add(1)
		go func() {
			defer procWg.Done()
			if err := helper.Recover(st.s.log, "strm.sync.walk115.process", func() error {
				for t := range fileCh {
					// 中止（ctx 取消）后排空队列即可，不再产生任何写操作
					if ctx.Err() != nil {
						continue
					}
					entry := cloud.FileEntry{
						ID:       t.file.FileId,
						Name:     t.file.FileName,
						IsDir:    false,
						Size:     t.file.FileSize,
						MTime:    t.file.Utime,
						PickCode: t.file.PickCode,
						Sha1:     t.file.Sha1,
					}
					st.processRemoteFile(entry, t.rel)
				}
				return nil
			}); err != nil {
				procErrMu.Lock()
				if procErr == nil {
					procErr = err
				}
				procErrMu.Unlock()
				cancel()
			}
		}()
	}
feed:
	for _, f := range allFiles {
		if ctx.Err() != nil {
			break
		}
		cleanName := cleanEntryName(f.FileName, false)
		var rel string
		if f.Pid == "" || f.Pid == rootCID {
			rel = cleanName
		} else {
			if parentVal, ok := st.dirCache.Load(f.Pid); ok && parentVal.(string) != "" {
				rel = cleanDirRel(parentVal.(string)) + "/" + cleanName
			} else {
				// 父目录不在目录缓存，无法还原真实相对路径。若继续用塌缩后的
				// 根路径处理，该文件会被错误判定，导致重复下载/上传或误删本地文件。
				// 目录树不完整时宁可中止本次同步，也不带着损坏的 rel 继续执行。
				procErrMu.Lock()
				if procErr == nil {
					procErr = fmt.Errorf("115: 文件 %s 的父目录未解析成功，目录树不完整，中止同步以防误删/误传", cleanName)
				}
				procErrMu.Unlock()
				cancel()
				break
			}
		}
		select {
		case fileCh <- strmFileTask{file: f, rel: rel}:
		case <-ctx.Done():
			break feed
		}
	}
	close(fileCh)
	procWg.Wait()
	if procErr != nil {
		return procErr
	}
	return ctx.Err()
}

const (
	flat115PageSize     = 1150
	flat115Threshold    = 9500
	max115AdaptiveDepth = 10
)

type adaptive115Task struct {
	cid   string
	rel   string
	depth int
}

// fetch115FlatSubtree 扁平拉取单个文件数在安全阈值内的子树全部文件。
func fetch115FlatSubtree(ctx context.Context, open115 *cloud115.OpenClient, cid string, firstBatch []cloud115.RemoteFile, totalCount int64, log *zap.Logger) ([]cloud115.RemoteFile, error) {
	allFiles := make([]cloud115.RemoteFile, 0, totalCount)
	allFiles = append(allFiles, firstBatch...)
	if totalCount <= int64(len(firstBatch)) {
		return allFiles, nil
	}

	totalPages := int((totalCount + flat115PageSize - 1) / flat115PageSize)
	type pageTask struct {
		offset int
	}
	pageTasks := make([]pageTask, 0, totalPages-1)
	for page := 1; page < totalPages; page++ {
		pageTasks = append(pageTasks, pageTask{offset: page * flat115PageSize})
	}

	var (
		filesMu  sync.Mutex
		wg       sync.WaitGroup
		taskCh   = make(chan pageTask, len(pageTasks))
		errMu    sync.Mutex
		fetchErr error
	)
	for _, t := range pageTasks {
		taskCh <- t
	}
	close(taskCh)

	workers := 8
	if len(pageTasks) < workers {
		workers = len(pageTasks)
	}

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := helper.Recover(log, "strm.sync.walk115.page", func() error {
				for t := range taskCh {
					if ctx.Err() != nil {
						return nil
					}
					files, _, err := open115.GetFsListFlat(ctx, cid, t.offset, flat115PageSize)
					if err != nil {
						errMu.Lock()
						if fetchErr == nil {
							fetchErr = err
						}
						errMu.Unlock()
						return nil
					}
					filesMu.Lock()
					allFiles = append(allFiles, files...)
					filesMu.Unlock()
				}
				return nil
			}); err != nil {
				errMu.Lock()
				if fetchErr == nil {
					fetchErr = err
				}
				errMu.Unlock()
			}
		}()
	}
	wg.Wait()
	if fetchErr != nil {
		return nil, fmt.Errorf("115: 分页拉取失败：%w", fetchErr)
	}
	return allFiles, nil
}

// list115DirDirect 列出指定目录下的直接子项（单层 cur=1&show_dir=1）。
func list115DirDirect(ctx context.Context, open115 *cloud115.OpenClient, cid string) ([]cloud115.RemoteFile, error) {
	var out []cloud115.RemoteFile
	for offset := 0; ; offset += flat115PageSize {
		files, _, err := open115.GetFsList(ctx, cid, offset, flat115PageSize)
		if err != nil {
			return nil, err
		}
		out = append(out, files...)
		if len(files) < flat115PageSize {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
	}
	return out, nil
}

// fetch115FilesAdaptive 采用自适应分治策略抓取 115 目录树下的全部文件：
// 115 开放平台扁平搜索对 offset+limit 有 10000 的最大深度限制。
//   - 若子树文件总数 < 9500，直接使用全速扁平分页批量拉取；
//   - 若子树文件总数 >= 9500（大库或超大分类目录），自动分治：仅单层列出该目录的直属子项（cur=1），
//     直属纯文件直接收集，直属子目录则派发为独立的子树任务继续递归探测与拉取；
//   - 若超大单目录下无子目录或层级过深（>10层），安全回退到 errFallbackToWalkRemote。
func (st *strmSyncState) fetch115FilesAdaptive(ctx context.Context, open115 *cloud115.OpenClient, rootCID string) ([]cloud115.RemoteFile, error) {
	var (
		allFiles []cloud115.RemoteFile
		filesMu  sync.Mutex

		walkMu   sync.Mutex
		walkCond = sync.NewCond(&walkMu)
		work     []adaptive115Task
		pending  int

		errMu    sync.Mutex
		firstErr error
	)

	push := func(t adaptive115Task) {
		walkMu.Lock()
		work = append(work, t)
		pending++
		walkCond.Signal()
		walkMu.Unlock()
	}

	go func() {
		<-ctx.Done()
		walkMu.Lock()
		walkCond.Broadcast()
		walkMu.Unlock()
	}()

	push(adaptive115Task{cid: rootCID, rel: "", depth: 0})

	workers := 8
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := helper.Recover(st.s.log, "strm.sync.walk115.adaptive", func() error {
				for {
					walkMu.Lock()
					for len(work) == 0 {
						if ctx.Err() != nil || pending == 0 {
							walkMu.Unlock()
							return nil
						}
						walkCond.Wait()
					}
					task := work[0]
					work = work[1:]
					walkMu.Unlock()

					if ctx.Err() != nil {
						walkMu.Lock()
						pending--
						walkCond.Broadcast()
						walkMu.Unlock()
						return nil
					}

					firstBatch, totalCount, err := open115.GetFsListFlat(ctx, task.cid, 0, flat115PageSize)
					if err != nil {
						errMu.Lock()
						if firstErr == nil {
							firstErr = fmt.Errorf("115: 获取文件列表失败（cid=%s）：%w", task.cid, err)
						}
						errMu.Unlock()
						walkMu.Lock()
						pending--
						walkCond.Broadcast()
						walkMu.Unlock()
						return nil
					}

					if totalCount < flat115Threshold {
						// 安全深度内：直接扁平拉取该子树全部文件
						files, err := fetch115FlatSubtree(ctx, open115, task.cid, firstBatch, totalCount, st.s.log)
						if err != nil {
							errMu.Lock()
							if firstErr == nil {
								firstErr = err
							}
							errMu.Unlock()
							walkMu.Lock()
							pending--
							walkCond.Broadcast()
							walkMu.Unlock()
							return nil
						}
						filesMu.Lock()
						allFiles = append(allFiles, files...)
						currentCount := len(allFiles)
						filesMu.Unlock()
						st.updateSyncMessage(fmt.Sprintf("正在拉取远端文件列表 (已获取 %d 个文件)...", currentCount))
					} else {
						// 子树过大（>=9500）：分治展开该目录直接子项
						if task.depth >= max115AdaptiveDepth {
							// 深度超限兜底：单目录嵌套超 10 层仍超 9500，回退为传统递归
							errMu.Lock()
							if firstErr == nil {
								firstErr = errFallbackToWalkRemote
							}
							errMu.Unlock()
							walkMu.Lock()
							pending--
							walkCond.Broadcast()
							walkMu.Unlock()
							return nil
						}
						st.s.log.Info("115: 目录文件数超限，自动分治展开子目录并发扁平拉取",
							zap.String("cid", task.cid),
							zap.String("rel", task.rel),
							zap.Int64("total_count", totalCount),
							zap.Int("depth", task.depth))

						directEntries, err := list115DirDirect(ctx, open115, task.cid)
						if err != nil {
							errMu.Lock()
							if firstErr == nil {
								firstErr = fmt.Errorf("115: 列出单层目录失败（cid=%s）：%w", task.cid, err)
							}
							errMu.Unlock()
							walkMu.Lock()
							pending--
							walkCond.Broadcast()
							walkMu.Unlock()
							return nil
						}

						for _, f := range directEntries {
							if f.Category == cloud115.TypeDir {
								cleanName := cleanEntryName(f.FileName, true)
								subRel := cleanName
								if task.rel != "" {
									subRel = task.rel + "/" + cleanName
								}
								st.dirCache.Store(f.FileId, subRel)
								st.deferDirCacheSave(f.FileId, subRel)
								push(adaptive115Task{cid: f.FileId, rel: subRel, depth: task.depth + 1})
							} else {
								filesMu.Lock()
								allFiles = append(allFiles, f)
								filesMu.Unlock()
							}
						}
					}

					walkMu.Lock()
					pending--
					if pending == 0 {
						walkCond.Broadcast()
					}
					walkMu.Unlock()
				}
			}); err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				errMu.Unlock()
			}
		}()
	}

	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return allFiles, nil
}

// handleVideo 收集/生成 .strm 文件。
// KeepExt=true：每个视频写 name.ext.strm，保留全部版本。
// KeepExt=false（默认）：同名候选先入队，walk 结束后按体积→mtime→扩展名优先级择优写一条 name.strm。
func (st *strmSyncState) handleVideo(entry cloud.FileEntry, rel, ext string) {
	relSansExt := rel[:len(rel)-len(ext)]
	if st.cfg.KeepExt {
		st.writeVideoStrm(entry, rel, ext, rel+".strm", true)
		return
	}
	st.mu.Lock()
	if st.remoteVideos == nil {
		st.remoteVideos = map[string][]remoteVideoCandidate{}
	}
	st.remoteVideos[relSansExt] = append(st.remoteVideos[relSansExt], remoteVideoCandidate{
		entry: entry,
		rel:   rel,
		ext:   ext,
	})
	st.mu.Unlock()
	st.touchProgress()
}

// flushPreferredVideos 在 prefer 模式下对同名多版本择优写盘，并记录冲突日志。
func (st *strmSyncState) flushPreferredVideos() {
	if st.cfg.KeepExt {
		return
	}
	st.mu.Lock()
	pending := st.remoteVideos
	st.remoteVideos = map[string][]remoteVideoCandidate{}
	st.mu.Unlock()
	for base, cands := range pending {
		if len(cands) == 0 {
			continue
		}
		winnerIdx := 0
		for i := 1; i < len(cands); i++ {
			if betterStrmVideoCandidate(cands[i], cands[winnerIdx], st.cfg.VideoExt) {
				winnerIdx = i
			}
		}
		winner := cands[winnerIdx]
		if len(cands) > 1 {
			skippedRels := make([]string, 0, len(cands)-1)
			skippedRefs := make([]string, 0, len(cands)-1)
			for i, c := range cands {
				if i == winnerIdx {
					continue
				}
				skippedRels = append(skippedRels, c.rel)
				ref := strings.TrimSpace(c.entry.PickCode)
				if ref == "" {
					ref = strings.TrimSpace(c.entry.ID)
				}
				if ref != "" {
					skippedRefs = append(skippedRefs, ref)
				}
			}
			st.s.log.Info("strm multi-version prefer",
				zap.String("base", base),
				zap.String("winner", winner.rel),
				zap.String("winner_ref", firstNonEmpty(winner.entry.PickCode, winner.entry.ID)),
				zap.Int64("winner_size", winner.entry.Size),
				zap.Strings("skipped", skippedRels),
				zap.Strings("skipped_refs", skippedRefs),
			)
			st.mu.Lock()
			st.rec.Skipped += int64(len(cands) - 1)
			st.mu.Unlock()
		}
		// 候选已在 handleVideo 计入 Total，此处不再重复 touchProgress
		st.writeVideoStrm(winner.entry, winner.rel, winner.ext, base+".strm", false)
	}
}

// betterStrmVideoCandidate 决定 prefer 模式下的赢家：体积更大 → mtime 更新 → video_ext 列表更靠前。
func betterStrmVideoCandidate(candidate, current remoteVideoCandidate, videoExtOrder []string) bool {
	if candidate.entry.Size != current.entry.Size {
		return candidate.entry.Size > current.entry.Size
	}
	if candidate.entry.MTime != current.entry.MTime {
		return candidate.entry.MTime > current.entry.MTime
	}
	return strmExtPriority(candidate.ext, videoExtOrder) < strmExtPriority(current.ext, videoExtOrder)
}

func strmExtPriority(ext string, order []string) int {
	ext = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(ext)), ".")
	for i, item := range order {
		if strings.TrimPrefix(strings.ToLower(strings.TrimSpace(item)), ".") == ext {
			return i
		}
	}
	return len(order) + 1
}

// writeVideoStrm 把单个视频写成目标 .strm（targetRel 相对本地输出根）。
// countProgress 控制是否计入 Total（prefer 模式下候选已在收集阶段计数）。
func (st *strmSyncState) writeVideoStrm(entry cloud.FileEntry, rel, ext, targetRel string, countProgress bool) {
	seenKey := "v:" + strings.TrimSuffix(targetRel, ".strm")
	st.mu.Lock()
	if st.seenVideo[seenKey] {
		st.mu.Unlock()
		if countProgress {
			st.touchProgress()
		}
		return
	}
	st.seenVideo[seenKey] = true
	st.mu.Unlock()

	target, err := joinLocalRel(st.p.LocalPath, targetRel)
	if err != nil {
		st.s.log.Warn("strm target path out of root", zap.String("rel", targetRel), zap.Error(err))
		if countProgress {
			st.touchProgress()
		}
		return
	}

	st.mu.Lock()
	if st.seenVideoTarget == nil {
		st.seenVideoTarget = map[string]cloud.FileEntry{}
	}
	if _, exists := st.seenVideoTarget[target]; exists {
		st.mu.Unlock()
		if countProgress {
			st.touchProgress()
		}
		return
	}
	st.seenVideoTarget[target] = entry
	st.mu.Unlock()

	done := func() {
		if countProgress {
			st.touchProgress()
		}
	}

	// 增量同步模式快速检查：本地 strm 文件存在、非空且修改时间与远端 mtime 一致，直接跳过无需读磁盘
	if st.syncType == model.StrmSyncTypeIncremental && entry.MTime > 0 {
		if info, err := os.Stat(target); err == nil && info.Size() > 0 && info.ModTime().Unix() == entry.MTime {
			st.mu.Lock()
			st.rec.Skipped++
			st.mu.Unlock()
			done()
			return
		}
	}

	content, err := st.strmContent(entry, rel, ext)
	if err != nil {
		st.s.log.Warn("build strm content failed", zap.String("file", rel), zap.Error(err))
		done()
		return
	}
	existing := ""
	if data, err := os.ReadFile(target); err == nil {
		existing = string(data)
	}
	if existing == content {
		if entry.MTime > 0 {
			mTime := time.Unix(entry.MTime, 0)
			_ = os.Chtimes(target, mTime, mTime)
		}
		st.mu.Lock()
		st.rec.Skipped++
		st.mu.Unlock()
		done()
		return
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		st.s.log.Warn("mkdir strm dir failed", zap.String("dir", filepath.Dir(target)), zap.Error(err))
		done()
		return
	}
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		st.s.log.Warn("write strm tmp failed", zap.String("file", target), zap.Error(err))
		done()
		return
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		st.s.log.Warn("rename strm failed", zap.String("file", target), zap.Error(err))
		done()
		return
	}
	if entry.MTime > 0 {
		mTime := time.Unix(entry.MTime, 0)
		_ = os.Chtimes(target, mTime, mTime)
	}
	st.mu.Lock()
	st.rec.NewStrm++
	st.mu.Unlock()
	done()
}

// strmContent 构建 strm 文件内容（一行指向本服务播放端点的 URL）。
func (st *strmSyncState) strmContent(entry cloud.FileEntry, rel, ext string) (string, error) {
	q := url.Values{}
	switch st.p.Provider {
	case model.StrmProvider115:
		q.Set("acct", st.p.AccountID)
		q.Set("pickcode", entry.PickCode)
	case model.StrmProviderCloudDrive, model.StrmProviderOpenList:
		q.Set("acct", st.p.AccountID)
		q.Set("ref", entry.ID)
	case model.StrmProviderLocal:
		src, err := joinLocalRel(st.p.RemotePath, rel)
		if err != nil {
			return "", err
		}
		q.Set("path", src)
	default:
		return "", fmt.Errorf("不支持的提供方：%s", st.p.Provider)
	}
	// 本地源用 path 参数携带真实文件路径；网盘源用 path 参数展示目录结构
	if st.p.Provider != model.StrmProviderLocal {
		if pathParam := st.strmPathParam(rel); pathParam != "" {
			q.Set("path", pathParam)
		}
	}
	suffix := ""
	if encoded := q.Encode(); encoded != "" {
		suffix = "?" + encoded
	}
	return st.cfg.BaseURL + "/api/strm/play/" + st.p.Provider + "/video" + ext + suffix, nil
}

// strmPathParam 按 add_path 模式生成 path 查询参数（1=完整相对路径 2=仅文件名 3=不带）。
func (st *strmSyncState) strmPathParam(rel string) string {
	switch st.cfg.AddPath {
	case 1:
		return rel
	case 2:
		_, name := filepath.Split(rel)
		return name
	default:
		return ""
	}
}

// usableSha1 归一化远端内容哈希：115 对目录/未完成文件可能返回空串或占位符 "-"，均视为不可用。
func usableSha1(sha string) string {
	sha = strings.TrimSpace(sha)
	if sha == "-" {
		return ""
	}
	return sha
}

// localSha1Matches 计算本地文件 SHA1 并与远端哈希做大小写不敏感比对
// （115 列表返回大写 hex，本地计算为小写）。读取/哈希失败按"视为同一文件"
// 处理，避免瞬时读文件错误触发大规模重复上传/下载。
func (st *strmSyncState) localSha1Matches(path, remoteSha1 string) bool {
	local, err := cloud115.FileSHA1(path)
	if err != nil {
		st.s.log.Warn("strm 计算本地元数据 SHA1 失败，按同一文件处理",
			zap.String("path", path), zap.Error(err))
		return true
	}
	return strings.EqualFold(local, remoteSha1)
}

// recordRemoteMeta 记录远端存在的元数据索引、文件大小、文件引用及内容 SHA1（多副本聚合追加）。
// 对同一文件 ID 严格去重，避免当 download_meta 与 upload_meta 同时开启时因重复记录引发误判与自杀式删除。
func (st *strmSyncState) recordRemoteMeta(entry cloud.FileEntry, rel string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.remoteMeta == nil {
		st.remoteMeta = map[string][]remoteMetaItem{}
	}
	key := "m:" + rel
	st.seenMeta[key] = true
	if entry.ID != "" {
		for _, existing := range st.remoteMeta[key] {
			if existing.ID == entry.ID {
				return
			}
		}
	}
	st.remoteMeta[key] = append(st.remoteMeta[key], remoteMetaItem{
		ID:    entry.ID,
		Size:  entry.Size,
		Sha1:  usableSha1(entry.Sha1),
		MTime: entry.MTime,
	})
}

func (st *strmSyncState) flushPendingDownloads() {
	st.mu.Lock()
	if len(st.pendingDownloads) == 0 {
		st.mu.Unlock()
		return
	}
	batch := st.pendingDownloads
	st.pendingDownloads = nil
	st.mu.Unlock()

	if err := st.s.repo.StrmDownload.CreateInBatches(st.ctx, batch, 100); err != nil {
		st.s.log.Warn("batch enqueue strm download tasks failed", zap.Error(err))
	}
}

func (st *strmSyncState) flushPendingUploads() {
	st.mu.Lock()
	if len(st.pendingUploads) == 0 {
		st.mu.Unlock()
		return
	}
	batch := st.pendingUploads
	st.pendingUploads = nil
	st.mu.Unlock()

	if err := st.s.repo.StrmUpload.CreateInBatches(st.ctx, batch, 100); err != nil {
		st.s.log.Warn("batch enqueue strm upload tasks failed", zap.Error(err))
	}
}

// handleMeta 元数据入下载队列。本地已存在时：开启上传元数据则一律跳过（以本地
// 为准）；否则大小与 SHA1（115 提供）均一致视为同一文件跳过，内容不同则下载覆盖。
func (st *strmSyncState) handleMeta(entry cloud.FileEntry, rel, ext string) {
	st.recordRemoteMeta(entry, rel)

	target, err := joinLocalRel(st.p.LocalPath, rel)
	if err != nil {
		return
	}

	st.mu.Lock()
	if st.seenMetaTarget == nil {
		st.seenMetaTarget = map[string]cloud.FileEntry{}
	}
	if _, exists := st.seenMetaTarget[target]; exists {
		// 该本地目标路径在当前批次中已被处理（存在同名/重名冲突），直接忽略重复项，避免多份不同大小的文件在本地交替覆盖导致增量死循环
		st.mu.Unlock()
		st.touchProgress()
		return
	}
	st.seenMetaTarget[target] = entry
	st.mu.Unlock()

	if info, err := os.Stat(target); err == nil {
		// 开启上传元数据时以本地为准：本地已存在的元数据不再用远端版本覆盖，
		// 与网盘版本的差异交给上传队列把本地文件推回网盘，避免下载/上传
		// 两个队列互相覆盖形成回环。
		if st.cfg.UploadMeta {
			st.touchProgress()
			return
		}
		// 同名同大小：115 提供远端 SHA1 时做内容级比对，网盘更新了同大小
		// 元数据也能被下载到本地；无哈希（其他网盘/列表未返回）或哈希一致
		// 视为同一文件跳过。
		remoteSha := usableSha1(entry.Sha1)
		if info.Size() == entry.Size && (remoteSha == "" || st.localSha1Matches(target, remoteSha)) {
			st.touchProgress()
			return
		}
	}
	st.mu.Lock()
	if st.activeDownloadPaths == nil {
		if active, err := st.s.repo.StrmDownload.GetActiveLocalPathMap(st.ctx, st.p.ID); err == nil {
			st.activeDownloadPaths = active
		} else {
			st.activeDownloadPaths = map[string]bool{}
		}
	}
	if st.activeDownloadPaths[target] {
		st.mu.Unlock()
		st.touchProgress()
		return
	}
	st.activeDownloadPaths[target] = true
	st.mu.Unlock()

	task := &model.StrmDownloadTask{
		SyncPathID: st.p.ID,
		AccountID:  st.p.AccountID,
		Provider:   st.p.Provider,
		FileName:   entry.Name,
		RemoteRef:  entry.PickCode,
		RemoteDir:  st.p.RemotePath,
		LocalPath:  target,
		Size:       entry.Size,
		Status:     model.StrmTaskPending,
	}
	if task.RemoteRef == "" {
		task.RemoteRef = entry.ID
	}
	// 115 用 pickcode 定位；DAV/OpenList 用路径定位
	if st.p.Provider != model.StrmProvider115 {
		task.RemoteRef = entry.ID
	}

	st.mu.Lock()
	st.pendingDownloads = append(st.pendingDownloads, task)
	shouldFlush := len(st.pendingDownloads) >= 100
	st.rec.NewMeta++
	st.mu.Unlock()

	if shouldFlush {
		st.flushPendingDownloads()
	}
	st.touchProgress()
}

// walkLocalSource 本地源：视频生成 STRM，元数据就地存在。
func (st *strmSyncState) walkLocalSource() error {
	srcRoot := filepath.Clean(st.p.RemotePath)
	info, err := os.Stat(srcRoot)
	if err != nil {
		return fmt.Errorf("本地源目录不可访问：%w", err)
	}
	if !info.IsDir() {
		return errors.New("本地源目录不是目录")
	}
	return filepath.WalkDir(srcRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if path == srcRoot {
			return nil
		}
		select {
		case <-st.ctx.Done():
			return st.ctx.Err()
		default:
		}
		if d.IsDir() {
			rel, relErr := filepath.Rel(srcRoot, path)
			if relErr == nil {
				st.markSeenDir(filepath.ToSlash(rel))
			}
			return nil
		}
		rel, err := filepath.Rel(srcRoot, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if st.isExcluded(rel) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(rel))
		if !st.isVideoExt(ext, info.Size()) {
			st.touchProgress()
			return nil
		}
		entry := cloud.FileEntry{Name: filepath.Base(rel), Size: info.Size(), MTime: info.ModTime().Unix()}
		st.handleVideo(entry, rel, ext)
		return nil
	})
}

// scanLocalMetaForUpload 扫描本地元数据，与远端比对后入上传队列。
// 以本地为准：网盘端不存在、同名不同大小、或同名同大小但 SHA1 不同（115 提供
// 远端哈希时做内容级比对）均入队覆盖上传；同名同大小同内容视为同一文件跳过。
// 另：近期已成功上传且大小未变的路径跳过入队，缩短「任务 done 但 115 列表滞后」窗口。
func (st *strmSyncState) scanLocalMetaForUpload() error {
	defer st.flushPendingUploads()
	if st.activeUploadPaths == nil {
		if active, err := st.s.repo.StrmUpload.GetActiveLocalPathMap(st.ctx, st.p.ID); err == nil {
			st.activeUploadPaths = active
		} else {
			st.activeUploadPaths = map[string]bool{}
		}
	}
	if st.recentDoneUploadSizes == nil {
		since := time.Now().Add(-strmRecentUploadSkipWindow)
		if recent, err := st.s.repo.StrmUpload.GetRecentDoneUploadSizeMap(st.ctx, st.p.ID, since); err == nil {
			st.recentDoneUploadSizes = recent
		} else {
			st.recentDoneUploadSizes = map[string]int64{}
		}
	}
	var pendingDeletes map[string][]string
	if st.p.Provider == model.StrmProvider115 {
		pendingDeletes = map[string][]string{}
	}
	localRoot := filepath.Clean(st.p.LocalPath)
	err := filepath.WalkDir(localRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if path == localRoot {
			return nil
		}
		select {
		case <-st.ctx.Done():
			return st.ctx.Err()
		default:
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(localRoot, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		ext := strings.ToLower(filepath.Ext(rel))
		if !st.isMetaExt(ext) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		st.mu.Lock()
		entries, exists := st.remoteMeta["m:"+rel]
		st.mu.Unlock()

		if exists && len(entries) > 0 {
			// 择优比对：只要远端存在任一副本的大小匹配且 SHA1 匹配（或无 SHA1），即判定远端已有最新副本，无需上传
			matchedIdx := -1
			for i, it := range entries {
				if it.Size == info.Size() {
					if it.Sha1 == "" || st.localSha1Matches(path, it.Sha1) {
						matchedIdx = i
						break
					}
				}
			}
			if matchedIdx >= 0 {
				// 远端已存在完全一致的副本，跳过上传！
				// 若远端还存在其他同名脏副本（副本总数 > 1），在 115 下收集待删除 ID，稍后按目录批量删除。
				// 严禁将已命中的最新副本 ID (matchedID) 放入待删列表，杜绝误杀唯一有效副本。
				if len(entries) > 1 && st.p.Provider == model.StrmProvider115 {
					parentCID := st.uploadRemoteTarget(rel)
					if parentCID != "" {
						matchedID := entries[matchedIdx].ID
						for i, it := range entries {
							if i != matchedIdx && it.ID != "" && it.ID != matchedID {
								pendingDeletes[parentCID] = append(pendingDeletes[parentCID], it.ID)
							}
						}
					}
				}
				return nil
			}
		}

		// 近期已成功上传且大小未变：115 列表可能尚未反映，避免重复入队
		if doneSize, ok := st.recentDoneUploadSizes[path]; ok && doneSize == info.Size() {
			return nil
		}

		remoteTarget := st.uploadRemoteTarget(rel)
		if st.p.Provider == model.StrmProvider115 && remoteTarget == "" {
			// 115 远端不存在对应父目录（如孤儿子目录），禁止降级到根目录上传以防错位死循环
			return nil
		}

		// 入队上传（以本地为准）：网盘端不存在；或所有远端副本均内容不同。
		// 115 的上传接口不保证同名覆盖，任务携带远端所有旧副本 ID（RemoteRef，以逗号连接），
		// 由上传端一次性批量删除所有旧文件后再上传，彻底根除同名文件堆积。
		st.mu.Lock()
		if st.activeUploadPaths != nil && st.activeUploadPaths[path] {
			st.mu.Unlock()
			return nil
		}
		if st.activeUploadPaths != nil {
			st.activeUploadPaths[path] = true
		}
		st.mu.Unlock()

		task := &model.StrmUploadTask{
			SyncPathID: st.p.ID,
			AccountID:  st.p.AccountID,
			Provider:   st.p.Provider,
			FileName:   filepath.Base(rel),
			LocalPath:  path,
			RemotePath: remoteTarget,
			Size:       info.Size(),
			Status:     model.StrmTaskPending,
		}
		if exists && len(entries) > 0 && st.p.Provider == model.StrmProvider115 {
			var oldIDs []string
			for _, it := range entries {
				if it.ID != "" {
					oldIDs = append(oldIDs, it.ID)
				}
			}
			task.RemoteRef = strings.Join(oldIDs, ",")
		}
		st.mu.Lock()
		st.pendingUploads = append(st.pendingUploads, task)
		shouldFlush := len(st.pendingUploads) >= 100
		st.rec.Uploaded++
		st.mu.Unlock()

		if shouldFlush {
			st.flushPendingUploads()
		}
		return nil
	})
	if err != nil {
		return err
	}

	// 批量清理 115 冗余旧元数据副本（按父目录聚合，一次请求批量删除该目录下全部冗余文件，彻底避免并发触发限流器超时）
	if len(pendingDeletes) > 0 {
		st.cleanupBatchRedundantFiles(pendingDeletes)
	}
	return nil
}

// cleanupBatchRedundantFiles 异步按父目录批量清理 115 远端冗余旧元数据副本。
func (st *strmSyncState) cleanupBatchRedundantFiles(deletesByParent map[string][]string) {
	if len(deletesByParent) == 0 || st.provider == nil {
		return
	}
	open115, ok := st.provider.(cloud.OpenAPI115Provider)
	if !ok {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		totalPruned := 0
		for parentCID, fileIDs := range deletesByParent {
			if ctx.Err() != nil {
				break
			}
			if len(fileIDs) == 0 {
				continue
			}
			if err := open115.OpenClient().DeleteFiles(ctx, parentCID, fileIDs...); err != nil {
				st.s.log.Warn("批量清理 115 冗余旧元数据副本失败",
					zap.String("parent_cid", parentCID),
					zap.Int("count", len(fileIDs)),
					zap.Error(err))
			} else {
				totalPruned += len(fileIDs)
			}
			time.Sleep(100 * time.Millisecond)
		}
		if totalPruned > 0 {
			st.s.log.Info("已完成批量清理 115 冗余旧元数据副本", zap.Int("total_pruned", totalPruned))
		}
	}()
}

// remoteUploadPath 远端元数据目标路径 = 同步目录远端根 + 相对路径。
func (st *strmSyncState) remoteUploadPath(rel string) string {
	root := strings.TrimRight(normalizeRemotePath(st.p.RemotePath), "/")
	if root == "/" || root == "" {
		return "/" + rel
	}
	return root + "/" + rel
}

// uploadRemoteTarget 返回上传任务的目标远端描述。
//   - 115：返回父目录 cid（供 PutFileNamed 定位），基于 dirPathToID 把父目录相对路径映射到 cid。
//   - 网盘桥接（clouddrive2/openlist）：返回完整远端路径。
func (st *strmSyncState) uploadRemoteTarget(rel string) string {
	if st.p.Provider == model.StrmProvider115 {
		dir := rel
		if idx := strings.LastIndexByte(dir, '/'); idx >= 0 {
			dir = dir[:idx]
		} else {
			dir = ""
		}
		if dir == "" {
			// 文件在同步根目录下，父目录即 115 同步根目录 ID
			return st.p.RemotePath
		}
		if cid, ok := st.dirPathToID[dir]; ok && cid != "" {
			return cid
		}
		// 子目录在 115 远端不存在（本地孤儿子目录或远端已删除该分类文件夹），
		// 返回空串，禁止降级回退到根目录上传以防污染根目录与错位死循环。
		return ""
	}
	return st.remoteUploadPath(rel)
}

// taskExists 检查是否已有同目录、同目标的进行中/已完成任务（避免重复入队）。
func (st *strmSyncState) taskExists(kind, syncPathID, localPath string) bool {
	ctx := st.ctx
	var count int64
	switch kind {
	case "download":
		count = st.s.repo.StrmDownload.CountActive(ctx, syncPathID, localPath)
	default:
		count = st.s.repo.StrmUpload.CountActive(ctx, syncPathID, localPath)
	}
	return count > 0
}

// markSeenDir 记录远端存在的目录及其全部祖先目录。
func (st *strmSyncState) markSeenDir(rel string) {
	rel = strings.Trim(filepath.ToSlash(rel), "/")
	if rel == "." {
		rel = ""
	}
	st.mu.Lock()
	if st.seenDir == nil {
		st.seenDir = map[string]bool{"": true}
	}
	for {
		st.seenDir[rel] = true
		if rel == "" {
			break
		}
		if idx := strings.LastIndexByte(rel, '/'); idx >= 0 {
			rel = rel[:idx]
		} else {
			rel = ""
		}
	}
	st.mu.Unlock()
}

// refreshUnseen115Dirs 补查本地存在、但 115 扁平文件列表未覆盖的目录。
// 扁平接口不返回空目录；逐层补查这些候选目录可以避免把远端仍存在的空目录误删。
func (st *strmSyncState) refreshUnseen115Dirs(localRoot string, dirs []string) error {
	if st.p.Provider != model.StrmProvider115 || st.provider == nil {
		return nil
	}
	dirIDs := map[string]string{"": strings.TrimSpace(st.p.RemotePath)}
	if dirIDs[""] == "" {
		dirIDs[""] = "0"
	}
	st.dirCache.Range(func(key, value any) bool {
		id, idOK := key.(string)
		rel, relOK := value.(string)
		if idOK && relOK && id != "" {
			dirIDs[cleanDirRel(rel)] = id
		}
		return true
	})
	liveIDs := map[string]string{"": dirIDs[""]}
	listed := map[string]bool{}

	sort.Strings(dirs)
	for _, dir := range dirs {
		rel, err := filepath.Rel(localRoot, dir)
		if err != nil {
			continue
		}
		rel = cleanDirRel(filepath.ToSlash(rel))
		st.mu.Lock()
		seen := st.seenDir[rel]
		st.mu.Unlock()
		if seen {
			if id := dirIDs[rel]; id != "" {
				liveIDs[rel] = id
			}
			continue
		}

		parentRel := ""
		if idx := strings.LastIndexByte(rel, '/'); idx >= 0 {
			parentRel = rel[:idx]
		}
		parentID := liveIDs[parentRel]
		if parentID == "" {
			continue // 父目录已确认不存在，子目录也必然是本地孤儿
		}
		if listed[parentRel] {
			continue
		}
		entries, err := st.provider.List(st.ctx, parentID)
		if err != nil {
			return fmt.Errorf("核对 115 远端目录 %s 失败：%w", parentRel, err)
		}
		listed[parentRel] = true
		for _, entry := range entries {
			if !entry.IsDir {
				continue
			}
			childRel := cleanEntryName(entry.Name, true)
			if parentRel != "" {
				childRel = parentRel + "/" + childRel
			}
			st.markSeenDir(childRel)
			liveIDs[childRel] = entry.ID
			dirIDs[childRel] = entry.ID
		}
	}
	return nil
}

// pruneLocal 清理本地远端已不存在的内容：
//   - 整个目录在远端不存在时，递归删除该本地目录（包括元数据）；
//   - 目录仍存在但视频已删除时，仅删除对应的 .strm，保留本地元数据；
//   - DeleteDir 开启时，最后再清理其余空目录。
func (st *strmSyncState) pruneLocal() error {
	// 增量同步保护：本次远端扫描不完整（目录详情解析失败 / 文件父路径降级）时，
	// seenVideo 覆盖不全，按"远端不存在"清理会误删刚下载或已存在的本地 .strm，
	// 进而触发"下次增量重新下载"的循环。此时跳过清理，仅做进度落库。
	if st.syncType == model.StrmSyncTypeIncremental && st.scanIncomplete.Load() {
		st.s.log.Warn("strm 增量同步跳过清理：本次远端扫描不完整，prune 已禁用",
			zap.String("path_id", st.p.ID))
		return nil
	}
	localRoot := filepath.Clean(st.p.LocalPath)
	var dirs []string
	var strmFiles []string
	err := filepath.WalkDir(localRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if path == localRoot {
			return nil
		}
		select {
		case <-st.ctx.Done():
			return st.ctx.Err()
		default:
		}
		if d.IsDir() {
			dirs = append(dirs, path)
			return nil
		}
		rel, err := filepath.Rel(localRoot, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		ext := strings.ToLower(filepath.Ext(rel))
		if ext == ".strm" {
			strmFiles = append(strmFiles, path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := st.refreshUnseen115Dirs(localRoot, dirs); err != nil {
		return err
	}

	// 先从浅到深找出最上层孤儿目录；父目录已判定为孤儿时无需重复处理子目录。
	sort.Strings(dirs)
	orphanRoots := make([]string, 0)
	for _, dir := range dirs {
		rel, relErr := filepath.Rel(localRoot, dir)
		if relErr != nil {
			continue
		}
		rel = filepath.ToSlash(rel)
		st.mu.Lock()
		existsRemotely := st.seenDir[rel]
		st.mu.Unlock()
		if existsRemotely {
			continue
		}
		underOrphan := false
		for _, root := range orphanRoots {
			childRel, childErr := filepath.Rel(root, dir)
			if childErr == nil && childRel != ".." && !strings.HasPrefix(childRel, ".."+string(filepath.Separator)) {
				underOrphan = true
				break
			}
		}
		if !underOrphan {
			orphanRoots = append(orphanRoots, dir)
		}
	}
	for _, dir := range orphanRoots {
		var fileCount int64
		_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, walkErr error) error {
			if walkErr == nil && !d.IsDir() {
				fileCount++
			}
			return nil
		})
		if err := os.RemoveAll(dir); err == nil {
			st.mu.Lock()
			st.rec.Pruned += fileCount
			st.mu.Unlock()
		}
	}

	// 对仍存在于远端的目录，按原规则清理失去远端视频来源的单个 .strm。
	for _, path := range strmFiles {
		if _, err := os.Stat(path); err != nil {
			continue // 已随孤儿目录递归删除
		}
		rel, relErr := filepath.Rel(localRoot, path)
		if relErr != nil {
			continue
		}
		rel = filepath.ToSlash(rel)
		relSansExt := strings.TrimSuffix(rel, filepath.Ext(rel))
		st.mu.Lock()
		remove := !st.seenVideo["v:"+relSansExt]
		st.mu.Unlock()
		if remove {
			if err := os.Remove(path); err == nil {
				st.mu.Lock()
				st.rec.Pruned++
				st.mu.Unlock()
			}
		}
	}

	if st.cfg.DeleteDir {
		sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
		for _, dir := range dirs {
			entries, err := os.ReadDir(dir)
			if err == nil && len(entries) == 0 {
				_ = os.Remove(dir)
			}
		}
	}
	return nil
}

// touchProgress 进度计数并限流防抖落库（避免高频写 SQLite 导致锁竞争）。
func (st *strmSyncState) touchProgress() {
	st.mu.Lock()
	st.rec.Total++
	st.processed++
	now := time.Now()
	flush := st.processed%100 == 0 || (st.processed%20 == 0 && now.Sub(st.lastProgressFlush) >= 2*time.Second)
	if flush {
		st.lastProgressFlush = now
	}
	st.mu.Unlock()
	if flush {
		st.flushProgress()
	}
}

func (st *strmSyncState) flushProgress() {
	st.mu.Lock()
	rec := *st.rec
	st.mu.Unlock()
	if err := st.s.repo.StrmSyncRecord.Update(st.ctx, &rec); err != nil {
		st.s.log.Warn("update strm sync progress failed", zap.Error(err))
	}
}

// updateSyncMessage 实时更新同步阶段提示信息，让前端界面清晰了解当前进度。
func (st *strmSyncState) updateSyncMessage(msg string) {
	st.mu.Lock()
	st.rec.Message = msg
	st.p.LastSyncMessage = msg
	rec := *st.rec
	p := *st.p
	st.mu.Unlock()
	_ = st.s.repo.StrmSyncRecord.Update(st.ctx, &rec)
	_ = st.s.repo.StrmSyncPath.Update(st.ctx, &p)
}

// ─── 定时同步巡检 ──────────────────────────────────────────────────────────────

func (s *StrmService) cronLoop(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	// 记录上次检查到的分钟：一轮循环若被慢操作拖过 60s（远端 List 慢、
	// 串行 StartSync、DB 忙），ticker 会丢掉中间的 tick，命中排程的分钟
	// 若只按"当前分钟相等"判定就会被静默跳过。逐分钟回放补触。
	last := time.Now().Truncate(time.Minute)
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case now := <-ticker.C:
			now = now.Truncate(time.Minute)
			paths, err := s.repo.StrmSyncPath.List(ctx)
			if err != nil {
				last = now
				continue
			}
			due := make([]time.Time, 0, 2)
			for m := last.Add(time.Minute); !m.After(now); m = m.Add(time.Minute) {
				due = append(due, m)
			}
			last = now
			if len(due) == 0 {
				continue
			}
			for i := range paths {
				p := &paths[i]
				if !p.Enabled || !p.EnableCron || strings.TrimSpace(p.Cron) == "" {
					continue
				}
				matched := false
				for _, m := range due {
					if cronMatches(p.Cron, m) {
						matched = true
						break
					}
				}
				if !matched {
					continue
				}
				s.mu.Lock()
				_, running := s.running[p.ID]
				s.mu.Unlock()
				if running {
					continue
				}
				s.log.Info("strm cron triggered sync", zap.String("path_id", p.ID), zap.String("cron", p.Cron))
				if err := s.StartSync(ctx, p.ID); err != nil {
					s.log.Warn("strm cron sync start failed", zap.String("path_id", p.ID), zap.Error(err))
				}
			}
		}
	}
}

// cronMatches 匹配 5 段 cron 表达式（分 时 日 月 周）。支持 * ? /n a-b a,b 组合。
func cronMatches(expr string, t time.Time) bool {
	fields := strings.Fields(strings.TrimSpace(expr))
	if len(fields) != 5 {
		return false
	}
	now := []int{t.Minute(), t.Hour(), t.Day(), int(t.Month()), int(t.Weekday())}
	if now[4] == 0 {
		now[4] = 7 // 周日常量统一为 7
	}
	for i, field := range fields {
		if !cronFieldMatches(field, now[i]) {
			return false
		}
	}
	return true
}

// cronFieldMatches 匹配单个 cron 字段。
func cronFieldMatches(field string, value int) bool {
	if field == "*" || field == "?" {
		return true
	}
	for _, item := range strings.Split(field, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if strings.HasPrefix(item, "*/") {
			step, err := strconv.Atoi(strings.TrimPrefix(item, "*/"))
			if err != nil || step <= 0 {
				continue
			}
			if value%step == 0 {
				return true
			}
			continue
		}
		if strings.Contains(item, "-") {
			bounds := strings.SplitN(item, "-", 2)
			lo, errLo := strconv.Atoi(strings.TrimSpace(bounds[0]))
			hi, errHi := strconv.Atoi(strings.TrimSpace(bounds[1]))
			if errLo == nil && errHi == nil && lo <= hi && value >= lo && value <= hi {
				return true
			}
			continue
		}
		n, err := strconv.Atoi(item)
		if err == nil && n == value {
			return true
		}
	}
	return false
}
