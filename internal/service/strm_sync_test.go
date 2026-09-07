package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
	"github.com/truewhile/MeBox/internal/service/cloud"
	"github.com/truewhile/MeBox/internal/service/cloud115"
)

// testStrmService 构建带内存库的 StrmService。
// 同步引擎在独立 goroutine 里访问 DB，因此必须用共享缓存的内存库，
// 否则每个连接都会得到一张空表。
func testStrmService(t *testing.T) *StrmService {
	t.Helper()
	dsn := "file:strmtest_" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(4)
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	if err := db.AutoMigrate(&model.StrmAccount{}, &model.StrmSyncPath{}, &model.StrmSyncRecord{},
		&model.StrmDownloadTask{}, &model.StrmUploadTask{}, &model.StrmDirCache{}, &model.Setting{}); err != nil {
		t.Fatal(err)
	}
	repos := repository.New(db)
	ctx := context.Background()
	if err := repos.Setting.Set(ctx, StrmSettingBaseURL, "http://test.local:8096"); err != nil {
		t.Fatal(err)
	}
	return NewStrmService(nil, zap.NewNop(), repos, NewCryptoService("test-secret", zap.NewNop()))
}

func syncPathRecord(t *testing.T, svc *StrmService, provider, remote, local string, enabled bool) *model.StrmSyncPath {
	t.Helper()
	p, err := svc.CreateSyncPath(context.Background(), &model.StrmSyncPath{
		Name:         "test",
		Provider:     provider,
		RemotePath:   remote,
		LocalPath:    local,
		AddPath:      1,
		Enabled:      enabled,
		DownloadMeta: true,
	})
	if err != nil {
		t.Fatalf("create sync path: %v", err)
	}
	return p
}

func writeFile(t *testing.T, path string, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func waitSyncDone(t *testing.T, svc *StrmService, pathID string, timeout time.Duration) *model.StrmSyncRecord {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		records, err := svc.repo.StrmSyncRecord.List(context.Background(), pathID, 5)
		if err != nil {
			t.Fatal(err)
		}
		if len(records) > 0 && records[0].Status != model.StrmSyncRecordRunning &&
			records[0].Status != model.StrmSyncRecordPending {
			return &records[0]
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("sync did not finish in time")
	return nil
}

// TestLocalStrmSync 本地目录：视频 → .strm 生成、内容指向播放端点、
// 二次同步跳过、远端删除后清理。
func TestLocalStrmSync(t *testing.T) {
	svc := testStrmService(t)
	src := t.TempDir()
	out := t.TempDir()

	writeFile(t, filepath.Join(src, "电影", "阿凡达.mkv"), "fake-video-data")
	writeFile(t, filepath.Join(src, "电影", "阿凡达.nfo"), "<xml/>")

	p := syncPathRecord(t, svc, model.StrmProviderLocal, src, out, true)
	if err := svc.StartSync(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	record := waitSyncDone(t, svc, p.ID, 10*time.Second)
	if record.Status != model.StrmSyncRecordDone {
		t.Fatalf("sync status = %s, message = %s", record.Status, record.Message)
	}
	if record.NewStrm != 1 {
		t.Fatalf("expected 1 new strm, got %d", record.NewStrm)
	}
	strmPath := filepath.Join(out, "电影", "阿凡达.strm")
	strmContent, err := os.ReadFile(strmPath)
	if err != nil {
		t.Fatalf("strm file not created: %v", err)
	}
	content := string(strmContent)
	if !strings.Contains(content, "/api/strm/play/local/video.mkv?") || !strings.Contains(content, "path=") {
		t.Fatalf("unexpected strm content: %s", content)
	}
	playURL, err := url.Parse(content)
	if err != nil {
		t.Fatalf("parse strm url: %v", err)
	}
	gotPath, err := url.QueryUnescape(playURL.Query().Get("path"))
	if err != nil {
		t.Fatalf("unescape path: %v", err)
	}
	wantPath := filepath.Join(src, "电影", "阿凡达.mkv")
	if gotPath != wantPath {
		t.Fatalf("strm path param = %q, want %q (content: %s)", gotPath, wantPath, content)
	}

	// 二次同步：内容未变应跳过
	if err := svc.StartSync(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	record = waitSyncDone(t, svc, p.ID, 10*time.Second)
	if record.Skipped != 1 {
		t.Fatalf("expected 1 skipped on second sync, got %d", record.Skipped)
	}

	// 删除远端视频后同步应清理本地 strm
	if err := os.Remove(filepath.Join(src, "电影", "阿凡达.mkv")); err != nil {
		t.Fatal(err)
	}
	if err := svc.StartSync(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	record = waitSyncDone(t, svc, p.ID, 10*time.Second)
	if record.Pruned != 1 {
		t.Fatalf("expected 1 pruned, got %d", record.Pruned)
	}
	if _, err := os.Stat(strmPath); !os.IsNotExist(err) {
		t.Fatalf("strm file should have been pruned, got err=%v", err)
	}
}

// TestStrmFullAndIncrementalSync 测试增量同步与全量同步模式切换及记录
func TestStrmFullAndIncrementalSync(t *testing.T) {
	svc := testStrmService(t)
	src := t.TempDir()
	out := t.TempDir()

	writeFile(t, filepath.Join(src, "电影", "星际穿越.mkv"), "fake-video-data")

	p := syncPathRecord(t, svc, model.StrmProviderLocal, src, out, true)

	// 1. 默认触发增量同步
	if err := svc.StartSync(context.Background(), p.ID, model.StrmSyncTypeIncremental); err != nil {
		t.Fatal(err)
	}
	record := waitSyncDone(t, svc, p.ID, 10*time.Second)
	if record.Status != model.StrmSyncRecordDone {
		t.Fatalf("sync status = %s, message = %s", record.Status, record.Message)
	}
	if record.SyncType != model.StrmSyncTypeIncremental {
		t.Fatalf("expected sync_type = incremental, got %s", record.SyncType)
	}
	if record.NewStrm != 1 {
		t.Fatalf("expected 1 new strm, got %d", record.NewStrm)
	}

	// 2. 再次执行增量同步，应当跳过
	if err := svc.StartSync(context.Background(), p.ID, model.StrmSyncTypeIncremental); err != nil {
		t.Fatal(err)
	}
	record = waitSyncDone(t, svc, p.ID, 10*time.Second)
	if record.SyncType != model.StrmSyncTypeIncremental {
		t.Fatalf("expected sync_type = incremental, got %s", record.SyncType)
	}
	if record.Skipped != 1 {
		t.Fatalf("expected 1 skipped, got %d", record.Skipped)
	}

	// 3. 执行全量同步
	if err := svc.StartSync(context.Background(), p.ID, model.StrmSyncTypeFull); err != nil {
		t.Fatal(err)
	}
	record = waitSyncDone(t, svc, p.ID, 10*time.Second)
	if record.SyncType != model.StrmSyncTypeFull {
		t.Fatalf("expected sync_type = full, got %s", record.SyncType)
	}
	if record.Status != model.StrmSyncRecordDone {
		t.Fatalf("full sync failed: status = %s, message = %s", record.Status, record.Message)
	}
}

// TestStrmCronMatches cron 表达式匹配。
func TestStrmCronMatches(t *testing.T) {
	cases := []struct {
		expr string
		now  time.Time
		want bool
	}{
		{"* * * * *", time.Date(2026, 8, 24, 10, 30, 0, 0, time.Local), true},
		{"30 10 * * *", time.Date(2026, 8, 24, 10, 30, 0, 0, time.Local), true},
		{"30 10 * * *", time.Date(2026, 8, 24, 10, 31, 0, 0, time.Local), false},
		{"*/15 * * * *", time.Date(2026, 8, 24, 10, 30, 0, 0, time.Local), true},
		{"*/15 * * * *", time.Date(2026, 8, 24, 10, 34, 0, 0, time.Local), false},
		{"0 */6 * * *", time.Date(2026, 8, 24, 18, 0, 0, 0, time.Local), true},
		{"0 */6 * * *", time.Date(2026, 8, 24, 19, 0, 0, 0, time.Local), false},
		{"0 2 * * 1-5", time.Date(2026, 8, 24, 2, 0, 0, 0, time.Local), true},  // 周一
		{"0 2 * * 1-5", time.Date(2026, 8, 23, 2, 0, 0, 0, time.Local), false}, // 周日
		{"bad", time.Now(), false},
	}
	for _, tc := range cases {
		if got := cronMatches(tc.expr, tc.now); got != tc.want {
			t.Errorf("cronMatches(%q) = %v, want %v", tc.expr, got, tc.want)
		}
	}
}

// TestStrmDefaultBaseURL 无任何地址配置时自动使用本机监听地址。
func TestStrmDefaultBaseURL(t *testing.T) {
	if got := strmDefaultBaseURL(nil); got != "http://127.0.0.1:8080" {
		t.Fatalf("default base url = %q", got)
	}
	if got := strmDefaultBaseURL(&config.Config{App: config.AppConfig{Port: 9000}}); got != "http://127.0.0.1:9000" {
		t.Fatalf("default base url with port = %q", got)
	}
	// strmEffectiveConfig 无配置不报错且默认兜底
	svc := testStrmService(t)
	p := &model.StrmSyncPath{Provider: model.StrmProviderLocal, AddPath: 1, DownloadMeta: true}
	cfg, err := svc.strmEffectiveConfig(context.Background(), p)
	if err != nil {
		t.Fatalf("effective config should not fail without base url: %v", err)
	}
	if cfg.BaseURL == "" {
		t.Fatal("effective config base url should fall back to default")
	}
}

func TestSanitizePathWithSpecialChars(t *testing.T) {
	cases := []struct {
		input    string
		isDir    bool
		wantName string
	}{
		{"数码宝贝:拯救者", true, "数码宝贝 拯救者"},
		{"数码宝贝:拯救者.mp4", false, "数码宝贝 拯救者.mp4"},
		{"Season 01.", true, "Season 01"},
		{"file:name?test*<foo>|bar\".mkv", false, "file nametestfoobar.mkv"},
		{"poster.jpg", false, "poster.jpg"},
		{"  trailing space  ", true, "trailing space"},
		{"CON", true, "_CON"},
		{"aux.mp4", false, "_aux.mp4"},
		{"NUL.nfo", false, "_NUL.nfo"},
		{"COM1", false, "_COM1"},
		{"test\x00\x1f\x7fcontrol.mkv", false, "testcontrol.mkv"},
	}
	for _, tc := range cases {
		got := cleanEntryName(tc.input, tc.isDir)
		if got != tc.wantName {
			t.Errorf("cleanEntryName(%q, %v) = %q, want %q", tc.input, tc.isDir, got, tc.wantName)
		}
	}

	// Test sanitizeRelativePath
	rel := "动漫/数码宝贝:拯救者/数码宝贝:拯救者.mp4"
	wantRel := filepath.Join("动漫", "数码宝贝 拯救者", "数码宝贝 拯救者.mp4")
	if got := sanitizeRelativePath(rel); got != wantRel {
		t.Errorf("sanitizeRelativePath(%q) = %q, want %q", rel, got, wantRel)
	}

	// Test sanitizeLocalPath - Windows Drive
	// sanitizeLocalPath 的盘符处理是平台相关的：Windows 保留 `D:\` 前缀，
	// Linux 把反斜杠统一当作分隔符（结果等价于 D:/test/...）。
	localWin := `D:\test\动漫\数码宝贝:拯救者\poster.jpg`
	var wantLocalWin string
	if runtime.GOOS == "windows" {
		wantLocalWin = filepath.Join(`D:\`, "test", "动漫", "数码宝贝 拯救者", "poster.jpg")
	} else {
		wantLocalWin = filepath.Join("D", "test", "动漫", "数码宝贝 拯救者", "poster.jpg")
	}
	if got := sanitizeLocalPath(localWin); got != wantLocalWin {
		t.Errorf("sanitizeLocalPath(%q) = %q, want %q", localWin, got, wantLocalWin)
	}

	// Test sanitizeLocalPath - Linux root
	localLinux := `/media/data/动漫/数码宝贝:拯救者/ep01:拯救者.mkv`
	cleanedLinux := sanitizeLocalPath(localLinux)
	if strings.Contains(cleanedLinux, ":") && !strings.HasPrefix(cleanedLinux, "D:") && !strings.HasPrefix(cleanedLinux, "C:") {
		t.Errorf("sanitizeLocalPath should not contain colons in path components: %q", cleanedLinux)
	}

	// Test joinLocalRel
	root := t.TempDir()
	joined, err := joinLocalRel(root, "动漫/数码宝贝:拯救者/poster.jpg")
	if err != nil {
		t.Fatalf("joinLocalRel failed: %v", err)
	}
	wantJoined := filepath.Join(root, "动漫", "数码宝贝 拯救者", "poster.jpg")
	if joined != wantJoined {
		t.Errorf("joinLocalRel = %q, want %q", joined, wantJoined)
	}

	// Test joinLocalRel prevents path traversal
	if _, err := joinLocalRel(root, "../../"); err == nil {
		t.Error("joinLocalRel should error on empty/traversal-only path")
	}
	traversalCleaned, err := joinLocalRel(root, "../../etc/passwd")
	if err != nil {
		t.Fatalf("joinLocalRel failed on traversal path: %v", err)
	}
	if !strings.HasPrefix(traversalCleaned, root) {
		t.Errorf("joinLocalRel allowed path escape: %s", traversalCleaned)
	}

	// Verify mkdir succeeds on this joined path
	if err := os.MkdirAll(filepath.Dir(joined), 0o755); err != nil {
		t.Fatalf("MkdirAll on joined path failed: %v", err)
	}
	if info, err := os.Stat(filepath.Dir(joined)); err != nil || !info.IsDir() {
		t.Fatalf("Dir not created: %v", err)
	}
}

// TestScanLocalMetaForUpload 验证元数据上传比对逻辑：网盘同名同大小（同一文件）跳过，
// 网盘不存在或同名不同大小（内容不同的元数据文件）以本地为准入队上传。
func TestScanLocalMetaForUpload(t *testing.T) {
	svc := testStrmService(t)
	localDir := t.TempDir()

	// 本地有 3 个元数据：
	// poster.jpg  — 网盘已有同名同大小 → 同一文件，跳过
	// fanart.jpg  — 网盘没有 → 上传
	// tvshow.nfo  — 网盘已有同名但大小不同（内容不同的元数据文件）→ 以本地为准覆盖上传
	writeFile(t, filepath.Join(localDir, "动漫", "poster.jpg"), "poster-data")
	writeFile(t, filepath.Join(localDir, "动漫", "fanart.jpg"), "fanart-data")
	writeFile(t, filepath.Join(localDir, "动漫", "tvshow.nfo"), "local-nfo-data")

	p := &model.StrmSyncPath{
		Base:       model.Base{ID: "test-path-upload"},
		AccountID:  "acct-1",
		Provider:   model.StrmProviderCloudDrive,
		RemotePath: "/Media",
		LocalPath:  localDir,
		UploadMeta: true,
	}

	st := &strmSyncState{
		s:          svc,
		ctx:        context.Background(),
		p:          p,
		cfg:        &strmPathConfig{UploadMeta: true, MetaExt: []string{"jpg", "nfo"}},
		rec:        &model.StrmSyncRecord{},
		seenMeta:   map[string]bool{},
		remoteMeta: map[string][]remoteMetaItem{},
	}

	// 模拟远端已存在 poster.jpg（与本地同一文件）和 tvshow.nfo（与本地不同）
	st.remoteMeta["m:动漫/poster.jpg"] = []remoteMetaItem{{ID: "f1", Size: int64(len("poster-data"))}}
	st.remoteMeta["m:动漫/tvshow.nfo"] = []remoteMetaItem{{ID: "f2", Size: 999}}

	if err := st.scanLocalMetaForUpload(); err != nil {
		t.Fatalf("scanLocalMetaForUpload failed: %v", err)
	}

	// fanart.jpg（网盘缺失）与 tvshow.nfo（网盘版本不同）入队，poster.jpg 跳过
	tasks, _, err := svc.repo.StrmUpload.List(context.Background(), "", 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected 2 upload tasks (fanart.jpg, tvshow.nfo), got %d", len(tasks))
	}
	got := map[string]model.StrmUploadTask{}
	for _, task := range tasks {
		got[task.FileName] = task
	}
	if _, ok := got["fanart.jpg"]; !ok {
		t.Errorf("expected upload task for fanart.jpg, got %v", taskNames(tasks))
	}
	if _, ok := got["tvshow.nfo"]; !ok {
		t.Errorf("expected upload task for tvshow.nfo (local wins), got %v", taskNames(tasks))
	}
	if _, ok := got["poster.jpg"]; ok {
		t.Errorf("poster.jpg (same size on remote) should be skipped")
	}

	// 再次扫描：已入队的文件自动去重，不重复入队
	if err := st.scanLocalMetaForUpload(); err != nil {
		t.Fatalf("second scanLocalMetaForUpload failed: %v", err)
	}
	tasks, _, err = svc.repo.StrmUpload.List(context.Background(), "", 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected still 2 upload tasks after dedup, got %d", len(tasks))
	}
}

// TestScanLocalMetaForUpload115CarriesRemoteRef 验证 115 覆盖上传时任务携带
// 网盘旧文件 ID（供上传前删除旧文件，避免同名重复），网盘无同名文件时不携带。
func TestScanLocalMetaForUpload115CarriesRemoteRef(t *testing.T) {
	svc := testStrmService(t)
	localDir := t.TempDir()

	// movie.nfo 网盘已有同名但大小不同（需要删除旧文件后覆盖上传）
	writeFile(t, filepath.Join(localDir, "movie.nfo"), "local-nfo-data")
	// fresh.nfo 网盘没有（普通上传，不携带 ref）
	writeFile(t, filepath.Join(localDir, "fresh.nfo"), "fresh-nfo")

	p := &model.StrmSyncPath{
		Base:       model.Base{ID: "test-path-upload-115"},
		AccountID:  "acct-1",
		Provider:   model.StrmProvider115,
		RemotePath: "100",
		LocalPath:  localDir,
		UploadMeta: true,
	}

	st := &strmSyncState{
		s:          svc,
		ctx:        context.Background(),
		p:          p,
		cfg:        &strmPathConfig{UploadMeta: true, MetaExt: []string{"jpg", "nfo"}},
		rec:        &model.StrmSyncRecord{},
		seenMeta:   map[string]bool{},
		remoteMeta: map[string][]remoteMetaItem{},
	}
	st.remoteMeta["m:movie.nfo"] = []remoteMetaItem{{ID: "file-42", Size: 1}}

	if err := st.scanLocalMetaForUpload(); err != nil {
		t.Fatalf("scanLocalMetaForUpload failed: %v", err)
	}

	tasks, _, err := svc.repo.StrmUpload.List(context.Background(), "", 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected 2 upload tasks, got %d", len(tasks))
	}
	for _, task := range tasks {
		switch task.FileName {
		case "movie.nfo":
			if task.RemoteRef != "file-42" {
				t.Errorf("movie.nfo upload task should carry remote ref %q, got %q", "file-42", task.RemoteRef)
			}
			if task.RemotePath != "100" {
				t.Errorf("movie.nfo upload task remote path = %q, want parent cid %q", task.RemotePath, "100")
			}
		case "fresh.nfo":
			if task.RemoteRef != "" {
				t.Errorf("fresh.nfo (not on remote) should not carry remote ref, got %q", task.RemoteRef)
			}
		default:
			t.Errorf("unexpected upload task %s", task.FileName)
		}
	}
}

func taskNames(tasks []model.StrmUploadTask) []string {
	names := make([]string, 0, len(tasks))
	for _, task := range tasks {
		names = append(names, task.FileName)
	}
	return names
}

// TestPruneLocalKeepsLocalMeta 验证清理规则：远端已删除的视频 .strm 仍会被清理，
// 但本地元数据一律保留（即使开启"下载元数据"且未开启"上传元数据"、网盘端没有
// 该元数据，也不再删除本地刮削好的 nfo/图片/字幕）。
func TestPruneLocalKeepsLocalMeta(t *testing.T) {
	svc := testStrmService(t)
	localDir := t.TempDir()

	// 阿凡达.strm（对应视频已被网盘删除 → 应清理）+ 阿凡达.nfo（网盘没有 → 保留）
	writeFile(t, filepath.Join(localDir, "电影", "阿凡达.strm"), "http://test.local:8096/x")
	writeFile(t, filepath.Join(localDir, "电影", "阿凡达.nfo"), "<local scraped meta/>")
	writeFile(t, filepath.Join(localDir, "电影", "poster.jpg"), "local-poster")

	p := &model.StrmSyncPath{
		Base:         model.Base{ID: "prune-meta-path"},
		Provider:     model.StrmProvider115,
		RemotePath:   "0",
		LocalPath:    localDir,
		DownloadMeta: true,
		UploadMeta:   false,
	}

	st := &strmSyncState{
		s:          svc,
		ctx:        context.Background(),
		p:          p,
		cfg:        &strmPathConfig{DownloadMeta: true, UploadMeta: false, MetaExt: []string{"nfo", "jpg"}},
		rec:        &model.StrmSyncRecord{},
		syncType:   model.StrmSyncTypeFull,
		seenVideo:  map[string]bool{},
		seenMeta:   map[string]bool{},
		remoteMeta: map[string][]remoteMetaItem{},
	}
	// 本次远端扫描既没有看到视频，也没有看到任何元数据
	if err := st.pruneLocal(); err != nil {
		t.Fatalf("pruneLocal failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(localDir, "电影", "阿凡达.strm")); !os.IsNotExist(err) {
		t.Fatalf("orphan .strm should be pruned, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(localDir, "电影", "阿凡达.nfo")); err != nil {
		t.Fatalf("local meta must be kept even when missing on remote: %v", err)
	}
	if _, err := os.Stat(filepath.Join(localDir, "电影", "poster.jpg")); err != nil {
		t.Fatalf("local poster must be kept even when missing on remote: %v", err)
	}
	if st.rec.Pruned != 1 {
		t.Fatalf("expected 1 pruned (strm only), got %d", st.rec.Pruned)
	}
}

// TestHandleMetaKeepsLocalWhenUploadEnabled 验证下载侧规则：开启"上传元数据"时
// 以本地为准——本地已存在的元数据（即使与网盘大小不同）不再入下载队列被网盘版本
// 覆盖；本地不存在的元数据仍正常入队下载。
func TestHandleMetaKeepsLocalWhenUploadEnabled(t *testing.T) {
	svc := testStrmService(t)
	localDir := t.TempDir()

	// 本地已有 test.nfo（大小 50，与网盘版本大小 100 不同）
	writeFile(t, filepath.Join(localDir, "test.nfo"), strings.Repeat("L", 50))

	p := &model.StrmSyncPath{
		Base:         model.Base{ID: "meta-local-wins-path"},
		Provider:     model.StrmProvider115,
		RemotePath:   "0",
		LocalPath:    localDir,
		DownloadMeta: true,
		UploadMeta:   true,
	}

	st := &strmSyncState{
		s:              svc,
		ctx:            context.Background(),
		p:              p,
		cfg:            &strmPathConfig{DownloadMeta: true, UploadMeta: true, MetaExt: []string{"nfo"}},
		rec:            &model.StrmSyncRecord{},
		seenMeta:       map[string]bool{},
		remoteMeta:     map[string][]remoteMetaItem{},
		seenMetaTarget: map[string]cloud.FileEntry{},
	}

	// 网盘版本与本地不同：本地存在 → 不入下载队列（以本地为准）
	entry := cloud.FileEntry{ID: "r1", Name: "test.nfo", Size: 100, PickCode: "pc1"}
	st.handleMeta(entry, "test.nfo", ".nfo")
	// 网盘独有：本地不存在 → 正常入下载队列
	missing := cloud.FileEntry{ID: "r2", Name: "absent.nfo", Size: 200, PickCode: "pc2"}
	st.handleMeta(missing, "absent.nfo", ".nfo")
	st.flushPendingDownloads()

	tasks, _, err := svc.repo.StrmDownload.List(context.Background(), "", 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected only 1 download task (absent.nfo), got %d", len(tasks))
	}
	if tasks[0].FileName != "absent.nfo" {
		t.Fatalf("expected download task for absent.nfo, got %s", tasks[0].FileName)
	}
	if st.rec.NewMeta != 1 {
		t.Fatalf("expected NewMeta = 1, got %d", st.rec.NewMeta)
	}
}

// TestScanLocalMetaForUploadSha1Identity 验证 115 远端 SHA1 可用时按内容精确比对：
// 同名同大小同内容（大小写不敏感）跳过；同名同大小不同内容以本地为准覆盖上传，
// 且任务携带网盘旧文件 ID。纯大小比对无法识别"同大小不同内容"（如 nfo 改一个字符）。
func TestScanLocalMetaForUploadSha1Identity(t *testing.T) {
	svc := testStrmService(t)
	localDir := t.TempDir()

	// same.nfo / diff.nfo 本地与网盘大小均相同：
	// same.nfo 网盘内容与本地一致（SHA1 相同）→ 同一文件，跳过；
	// diff.nfo 网盘上是另一个同大小文件（SHA1 不同）→ 以本地为准覆盖上传。
	writeFile(t, filepath.Join(localDir, "same.nfo"), "same-content")
	writeFile(t, filepath.Join(localDir, "diff.nfo"), "diff-content")
	otherFile := filepath.Join(localDir, "other.tmp")
	writeFile(t, otherFile, "other-content!")

	sameSha, err := cloud115.FileSHA1(filepath.Join(localDir, "same.nfo"))
	if err != nil {
		t.Fatal(err)
	}
	otherSha, err := cloud115.FileSHA1(otherFile)
	if err != nil {
		t.Fatal(err)
	}

	p := &model.StrmSyncPath{
		Base:       model.Base{ID: "test-path-upload-sha1"},
		AccountID:  "acct-1",
		Provider:   model.StrmProvider115,
		RemotePath: "100",
		LocalPath:  localDir,
		UploadMeta: true,
	}
	st := &strmSyncState{
		s:          svc,
		ctx:        context.Background(),
		p:          p,
		cfg:        &strmPathConfig{UploadMeta: true, MetaExt: []string{"nfo"}},
		rec:        &model.StrmSyncRecord{},
		seenMeta:   map[string]bool{},
		remoteMeta: map[string][]remoteMetaItem{},
	}
	// 115 返回大写 SHA1，本地计算为小写：同时验证大小写不敏感比对
	st.remoteMeta["m:same.nfo"] = []remoteMetaItem{
		{ID: "same-1", Size: int64(len("same-content")), Sha1: strings.ToUpper(sameSha)},
	}
	st.remoteMeta["m:diff.nfo"] = []remoteMetaItem{
		{ID: "old-diff-1", Size: int64(len("diff-content")), Sha1: strings.ToUpper(otherSha)},
	}

	if err := st.scanLocalMetaForUpload(); err != nil {
		t.Fatalf("scanLocalMetaForUpload failed: %v", err)
	}

	tasks, _, err := svc.repo.StrmUpload.List(context.Background(), "", 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 upload task (diff.nfo), got %d: %v", len(tasks), taskNames(tasks))
	}
	if tasks[0].FileName != "diff.nfo" {
		t.Fatalf("expected upload task for diff.nfo, got %s", tasks[0].FileName)
	}
	if tasks[0].RemoteRef != "old-diff-1" {
		t.Fatalf("diff.nfo task should carry remote ref %q, got %q", "old-diff-1", tasks[0].RemoteRef)
	}
}

// TestHandleMetaSha1Identity 验证下载侧（未开启上传时镜像网盘元数据）：
// 同名同大小同 SHA1 跳过；网盘更新了同大小元数据（SHA1 不同）仍会下载；
// 网盘未返回哈希时退回大小比对。
func TestHandleMetaSha1Identity(t *testing.T) {
	svc := testStrmService(t)
	localDir := t.TempDir()

	writeFile(t, filepath.Join(localDir, "same.nfo"), "identical-data")   // 与网盘内容一致
	writeFile(t, filepath.Join(localDir, "stale.nfo"), "stale-content!!") // 网盘已更新为同大小新内容
	writeFile(t, filepath.Join(localDir, "nohash.nfo"), "nohash-content") // 网盘未返回 SHA1
	otherFile := filepath.Join(localDir, "other.tmp")
	writeFile(t, otherFile, "totally-newdata")

	sameSha, err := cloud115.FileSHA1(filepath.Join(localDir, "same.nfo"))
	if err != nil {
		t.Fatal(err)
	}
	otherSha, err := cloud115.FileSHA1(otherFile)
	if err != nil {
		t.Fatal(err)
	}

	p := &model.StrmSyncPath{
		Base:         model.Base{ID: "meta-sha1-path"},
		Provider:     model.StrmProvider115,
		RemotePath:   "0",
		LocalPath:    localDir,
		DownloadMeta: true,
		UploadMeta:   false,
	}
	st := &strmSyncState{
		s:              svc,
		ctx:            context.Background(),
		p:              p,
		cfg:            &strmPathConfig{DownloadMeta: true, UploadMeta: false, MetaExt: []string{"nfo"}},
		rec:            &model.StrmSyncRecord{},
		seenMeta:       map[string]bool{},
		remoteMeta:     map[string][]remoteMetaItem{},
		seenMetaTarget: map[string]cloud.FileEntry{},
	}

	st.handleMeta(cloud.FileEntry{ID: "r1", Name: "same.nfo", Size: int64(len("identical-data")), Sha1: strings.ToUpper(sameSha), PickCode: "pc1"}, "same.nfo", ".nfo")
	st.handleMeta(cloud.FileEntry{ID: "r2", Name: "stale.nfo", Size: int64(len("stale-content!!")), Sha1: strings.ToUpper(otherSha), PickCode: "pc2"}, "stale.nfo", ".nfo")
	st.handleMeta(cloud.FileEntry{ID: "r3", Name: "nohash.nfo", Size: int64(len("nohash-content")), PickCode: "pc3"}, "nohash.nfo", ".nfo")
	st.flushPendingDownloads()

	tasks, _, err := svc.repo.StrmDownload.List(context.Background(), "", 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected only 1 download task (stale.nfo), got %d", len(tasks))
	}
	if tasks[0].FileName != "stale.nfo" {
		t.Fatalf("expected download task for stale.nfo, got %s", tasks[0].FileName)
	}
}

// fakeRemoteProvider 是 walkRemote 并发遍历的假提供方：返回一棵固定目录树，
// 并记录每个目录被 List 的次数，用于验证并发遍历无漏目录、无重复目录。
type fakeRemoteProvider struct {
	listed map[string]int
}

func (f *fakeRemoteProvider) Type() string               { return "fake" }
func (f *fakeRemoteProvider) Ping(context.Context) error { return nil }
func (f *fakeRemoteProvider) Resolve(context.Context, string) (*cloud.DirectLink, error) {
	return &cloud.DirectLink{URL: "http://cdn/x.mkv"}, nil
}

func (f *fakeRemoteProvider) List(_ context.Context, dirID string) ([]cloud.FileEntry, error) {
	if f.listed == nil {
		f.listed = map[string]int{}
	}
	f.listed[dirID]++
	switch dirID {
	case "root":
		return []cloud.FileEntry{
			{ID: "a", Name: "动漫", IsDir: true},
			{ID: "b", Name: "电影", IsDir: true},
			{ID: "f1", Name: "孤儿视频.mkv", Size: 100},
		}, nil
	case "a":
		return []cloud.FileEntry{
			{ID: "a1", Name: "番剧", IsDir: true},
			{ID: "fa1", Name: "第01集.mkv", Size: 200},
		}, nil
	case "a1":
		return []cloud.FileEntry{
			{ID: "fa11", Name: "第01集.mkv", Size: 300},
			{ID: "fa12", Name: "第02集.mkv", Size: 300},
		}, nil
	case "b":
		return []cloud.FileEntry{
			{ID: "fb1", Name: "电影A.mkv", Size: 400},
		}, nil
	default:
		return nil, nil
	}
}

// TestWalkRemoteConcurrent 验证并发目录遍历：所有目录均被列出、所有文件
// 均被处理（strm 生成 / 元数据入队），且不重复。
func TestWalkRemoteConcurrent(t *testing.T) {
	svc := testStrmService(t)
	localDir := t.TempDir()

	acct := &model.StrmAccount{
		Name:     "fake",
		Provider: "cloud115",
		Config:   "{}",
		Enabled:  true,
	}
	if err := svc.repo.StrmAccount.Create(context.Background(), acct); err != nil {
		t.Fatal(err)
	}
	p := &model.StrmSyncPath{
		Base:       model.Base{ID: "walk-path"},
		AccountID:  acct.ID,
		Provider:   model.StrmProvider115,
		RemotePath: "root",
		LocalPath:  localDir,
	}

	provider := &fakeRemoteProvider{listed: map[string]int{}}
	st := &strmSyncState{
		s:          svc,
		ctx:        context.Background(),
		p:          p,
		provider:   provider,
		cfg:        &strmPathConfig{VideoExt: []string{"mkv"}, MetaExt: []string{"nfo"}, AddPath: 1, DownloadMeta: false},
		rec:        &model.StrmSyncRecord{},
		seenVideo:  map[string]bool{},
		seenMeta:   map[string]bool{},
		remoteMeta: map[string][]remoteMetaItem{},
	}
	if err := st.walkRemote(); err != nil {
		t.Fatalf("walkRemote failed: %v", err)
	}

	for _, dir := range []string{"root", "a", "a1", "b"} {
		if provider.listed[dir] != 1 {
			t.Errorf("目录 %s 被列出 %d 次，期望 1 次", dir, provider.listed[dir])
		}
	}

	// 5 个视频文件应生成 5 个 .strm：孤儿视频.mkv / a目录第01集 /
	// 番剧第01集+第02集 / 电影A（递归统计，含子目录）
	strmCount := 0
	walkErr := filepath.WalkDir(localDir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(d.Name(), ".strm") {
			strmCount++
		}
		return nil
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}
	if strmCount != 5 {
		t.Errorf("生成的 .strm 数量 = %d，期望 5", strmCount)
	}
}

// TestStrmBatchEnqueueAndConcurrentClaim 测试大规模批量入库及多协程并发认领无死锁
func TestStrmBatchEnqueueAndConcurrentClaim(t *testing.T) {
	svc := testStrmService(t)
	ctx := context.Background()

	// 1. 批量插入 200 个下载任务
	tasks := make([]*model.StrmDownloadTask, 0, 200)
	for i := 0; i < 200; i++ {
		tasks = append(tasks, &model.StrmDownloadTask{
			SyncPathID: "test-sync-path",
			AccountID:  "test-acct",
			Provider:   model.StrmProvider115,
			FileName:   filepath.Base(string(rune('a'+i%26))) + ".nfo",
			LocalPath:  filepath.Join(t.TempDir(), string(rune('a'+i%26)), "test.nfo"),
			Status:     model.StrmTaskPending,
		})
	}
	if err := svc.repo.StrmDownload.CreateInBatches(ctx, tasks, 50); err != nil {
		t.Fatalf("CreateInBatches failed: %v", err)
	}

	// 2. 验证 ActiveLocalPathMap
	activeMap, err := svc.repo.StrmDownload.GetActiveLocalPathMap(ctx, "test-sync-path")
	if err != nil {
		t.Fatalf("GetActiveLocalPathMap failed: %v", err)
	}
	if len(activeMap) == 0 {
		t.Fatal("expected active local path map to have entries")
	}

	// 3. 模拟 6 个 worker 并发 ClaimPendingDownload
	claimedCount := 0
	var claimMu sync.Mutex
	var wg sync.WaitGroup
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				batch, err := svc.repo.StrmDownload.ClaimPendingDownload(ctx, 10)
				if err != nil {
					t.Errorf("concurrent ClaimPendingDownload failed: %v", err)
					return
				}
				if len(batch) == 0 {
					return
				}
				claimMu.Lock()
				claimedCount += len(batch)
				claimMu.Unlock()
			}
		}()
	}
	wg.Wait()

	if claimedCount != 200 {
		t.Fatalf("expected all 200 tasks claimed, got %d", claimedCount)
	}
}

// TestStrmDuplicateFileConflictResolution 测试远端存在多个同名不同大小文件时，本地确定性仲裁，避免增量死循环
func TestStrmDuplicateFileConflictResolution(t *testing.T) {
	svc := testStrmService(t)
	localDir := t.TempDir()

	p := &model.StrmSyncPath{
		Base:         model.Base{ID: "dup-test-path"},
		Provider:     model.StrmProvider115,
		RemotePath:   "root",
		LocalPath:    localDir,
		DownloadMeta: true,
	}

	st := &strmSyncState{
		s:               svc,
		ctx:             context.Background(),
		p:               p,
		cfg:             &strmPathConfig{DownloadMeta: true, MetaExt: []string{"nfo"}},
		rec:             &model.StrmSyncRecord{},
		seenMeta:        map[string]bool{},
		remoteMeta:      map[string][]remoteMetaItem{},
		seenMetaTarget:  map[string]cloud.FileEntry{},
		seenVideoTarget: map[string]cloud.FileEntry{},
	}

	// 模拟远端同目录下存在两个同名不同大小的 nfo 文件 (115 历史重复上传)
	// entry1: 较早文件 (MTime: 1000, Size: 100)
	entry1 := cloud.FileEntry{ID: "f1", Name: "test.nfo", Size: 100, MTime: 1000, PickCode: "p1"}
	// entry2: 较新文件 (MTime: 2000, Size: 200)
	entry2 := cloud.FileEntry{ID: "f2", Name: "test.nfo", Size: 200, MTime: 2000, PickCode: "p2"}

	// 第一次全量处理：两者都在列表中
	st.handleMeta(entry1, "test.nfo", ".nfo")
	st.handleMeta(entry2, "test.nfo", ".nfo")
	st.flushPendingDownloads()

	// 验证仲裁结果：最终只产生 1 个下载任务，且使用的是首个匹配项 (Size 100/p1)
	tasks, _, err := svc.repo.StrmDownload.List(context.Background(), "", 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 download task after conflict resolution, got %d", len(tasks))
	}
	if tasks[0].Size != 100 || tasks[0].RemoteRef != "p1" {
		t.Fatalf("expected task with size 100/p1, got size=%d ref=%s", tasks[0].Size, tasks[0].RemoteRef)
	}

	// 模拟该任务下载落盘完成
	writeFile(t, filepath.Join(localDir, "test.nfo"), strings.Repeat("x", 100))

	// 第二次增量同步：两者再次依次扫描
	st2 := &strmSyncState{
		s:               svc,
		ctx:             context.Background(),
		p:               p,
		cfg:             &strmPathConfig{DownloadMeta: true, MetaExt: []string{"nfo"}},
		rec:             &model.StrmSyncRecord{},
		seenMeta:        map[string]bool{},
		remoteMeta:      map[string][]remoteMetaItem{},
		seenMetaTarget:  map[string]cloud.FileEntry{},
		seenVideoTarget: map[string]cloud.FileEntry{},
	}
	st2.handleMeta(entry1, "test.nfo", ".nfo")
	st2.handleMeta(entry2, "test.nfo", ".nfo")
	st2.flushPendingDownloads()

	// 验证：不会新增任何下载任务，NewMeta 为 0，增量跳过
	if st2.rec.NewMeta != 0 {
		t.Fatalf("expected 0 new meta on incremental sync, got %d", st2.rec.NewMeta)
	}

}

// TestWalk115FlatAbortsOnDirResolveFailure 回归测试：115 开放平台 token 失效/目录详情
// 解析失败时，同步必须中止而不是带着塌缩的 rel 继续处理，否则会导致本地大量元数据
// 被误判为"云端不存在"而重复下载/上传，甚至误删本地文件（用户反馈"云盘没动却重下重传"）。
func TestWalk115FlatAbortsOnDirResolveFailure(t *testing.T) {
	svc := testStrmService(t)
	localDir := t.TempDir()

	acct := &model.StrmAccount{
		Name:     "fake115",
		Provider: "cloud115",
		Config:   "{}",
		Enabled:  true,
	}
	if err := svc.repo.StrmAccount.Create(context.Background(), acct); err != nil {
		t.Fatal(err)
	}
	p := &model.StrmSyncPath{
		Base:       model.Base{ID: "abort-path"},
		AccountID:  acct.ID,
		Provider:   model.StrmProvider115,
		RemotePath: "0",
		LocalPath:  localDir,
	}

	// 115 mock：文件列表返回一个视频（父目录 999 不在缓存，需要 get_info），
	// get_info 恒返回 access_token 格式错误（40140123）→ 目录树解析失败。
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open/ufile/files":
			w.Write([]byte(`{"state":true,"count":1,"data":[{"fid":"100","pid":"999","fc":"1","fn":"movie.mkv","pc":"pc1","upt":1700000000,"fs":1024}]}`))
		case "/open/folder/get_info":
			w.Write([]byte(`{"state":false,"code":40140123,"message":"access_token 格式错误"}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer api.Close()

	oldPro := cloud115.ProAPIBase
	cloud115.ProAPIBase = api.URL
	defer func() { cloud115.ProAPIBase = oldPro }()

	oc := cloud115.NewOpenClient("app", "at", "rt")
	st := &strmSyncState{
		s:          svc,
		ctx:        context.Background(),
		p:          p,
		provider:   cloud.NewOpenAPI115("app", "at", "rt"),
		cfg:        &strmPathConfig{VideoExt: []string{"mkv"}, MetaExt: []string{"nfo"}, AddPath: 1, DownloadMeta: false},
		rec:        &model.StrmSyncRecord{},
		syncType:   model.StrmSyncTypeFull,
		dirCache:   sync.Map{},
		seenVideo:  map[string]bool{},
		seenMeta:   map[string]bool{},
		remoteMeta: map[string][]remoteMetaItem{},
	}
	err := st.walk115Flat(oc)
	if err == nil {
		t.Fatal("expected walk115Flat to abort on dir-resolve failure, got nil error")
	}

	// 中止后不允许产生任何部分写入（本地不允许生成 .strm 文件）。
	var strmCount int
	_ = filepath.WalkDir(localDir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(d.Name(), ".strm") {
			strmCount++
		}
		return nil
	})
	if strmCount != 0 {
		t.Fatalf("expected no .strm written after abort, got %d", strmCount)
	}
}

// TestWalk115FlatConcurrentProcessing 验证 115 平铺拉取后文件分类处理走并发
// worker 池：同一父目录下多个视频的 strm 全部生成，且解析出的目录拓扑缓存
// 通过 SetBatch 批量落库，供下次增量同步预加载（省掉重复的 get_info 调用）。
func TestWalk115FlatConcurrentProcessing(t *testing.T) {
	svc := testStrmService(t)
	localDir := t.TempDir()
	acct := &model.StrmAccount{Name: "fake115", Provider: "cloud115", Config: "{}", Enabled: true}
	if err := svc.repo.StrmAccount.Create(context.Background(), acct); err != nil {
		t.Fatal(err)
	}
	p := &model.StrmSyncPath{
		Base:       model.Base{ID: "flat-path"},
		AccountID:  acct.ID,
		Provider:   model.StrmProvider115,
		RemotePath: "0",
		LocalPath:  localDir,
	}

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open/ufile/files":
			w.Write([]byte(`{"state":true,"count":3,"data":[
				{"fid":"101","pid":"999","fc":"1","fn":"m1.mkv","pc":"pc1","upt":1700000001,"fs":1024},
				{"fid":"102","pid":"999","fc":"1","fn":"m2.mkv","pc":"pc2","upt":1700000002,"fs":2048},
				{"fid":"103","pid":"999","fc":"1","fn":"m3.mkv","pc":"pc3","upt":1700000003,"fs":4096}]}`))
		case "/open/folder/get_info":
			w.Write([]byte(`{"state":true,"data":{"file_id":"999","file_name":"Movies","file_category":"0",
				"paths":[{"file_id":"0","file_name":"根目录"},{"file_id":"999","file_name":"Movies"}]}}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer api.Close()
	oldPro := cloud115.ProAPIBase
	cloud115.ProAPIBase = api.URL
	defer func() { cloud115.ProAPIBase = oldPro }()

	oc := cloud115.NewOpenClient("app", "at", "rt")
	st := &strmSyncState{
		s:               svc,
		ctx:             context.Background(),
		p:               p,
		provider:        cloud.NewOpenAPI115("app", "at", "rt"),
		cfg:             &strmPathConfig{VideoExt: []string{"mkv"}, MetaExt: []string{"nfo"}, AddPath: 1},
		rec:             &model.StrmSyncRecord{},
		syncType:        model.StrmSyncTypeFull,
		dirCache:        sync.Map{},
		seenVideo:       map[string]bool{},
		seenMeta:        map[string]bool{},
		remoteMeta:      map[string][]remoteMetaItem{},
		seenMetaTarget:  map[string]cloud.FileEntry{},
		seenVideoTarget: map[string]cloud.FileEntry{},
	}
	if err := st.walk115Flat(oc); err != nil {
		t.Fatalf("walk115Flat: %v", err)
	}

	// 3 个视频的 strm 全部生成
	if st.rec.NewStrm != 3 {
		t.Fatalf("expected 3 strm created, got %d", st.rec.NewStrm)
	}
	for _, name := range []string{"m1.strm", "m2.strm", "m3.strm"} {
		if _, err := os.Stat(filepath.Join(localDir, "Movies", name)); err != nil {
			t.Fatalf("strm %s missing: %v", name, err)
		}
	}

	// 目录拓扑缓存已批量落库
	rows, err := svc.repo.StrmDirCache.ListBySyncPathID(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].DirID != "999" || rows[0].Path != "Movies" {
		t.Fatalf("dir cache rows = %#v, want one row for dir 999 -> Movies", rows)
	}

	// 增量同步复用目录缓存：不再发起 get_info 调用
	var infoCalls int
	api.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open/ufile/files":
			w.Write([]byte(`{"state":true,"count":3,"data":[
				{"fid":"101","pid":"999","fc":"1","fn":"m1.mkv","pc":"pc1","upt":1700000001,"fs":1024},
				{"fid":"102","pid":"999","fc":"1","fn":"m2.mkv","pc":"pc2","upt":1700000002,"fs":2048},
				{"fid":"103","pid":"999","fc":"1","fn":"m3.mkv","pc":"pc3","upt":1700000003,"fs":4096}]}`))
		case "/open/folder/get_info":
			infoCalls++
			w.Write([]byte(`{"state":true,"data":{"file_id":"999","file_name":"Movies","file_category":"0","paths":[]}}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})

	st2 := &strmSyncState{
		s:               svc,
		ctx:             context.Background(),
		p:               p,
		provider:        cloud.NewOpenAPI115("app", "at", "rt"),
		cfg:             st.cfg,
		rec:             &model.StrmSyncRecord{},
		syncType:        model.StrmSyncTypeIncremental,
		dirCache:        sync.Map{},
		seenVideo:       map[string]bool{},
		seenMeta:        map[string]bool{},
		remoteMeta:      map[string][]remoteMetaItem{},
		seenMetaTarget:  map[string]cloud.FileEntry{},
		seenVideoTarget: map[string]cloud.FileEntry{},
	}
	if err := st2.walk115Flat(oc); err != nil {
		t.Fatalf("incremental walk115Flat: %v", err)
	}
	if infoCalls != 0 {
		t.Fatalf("incremental sync should reuse dir cache, got %d get_info calls", infoCalls)
	}
	if st2.rec.NewStrm != 0 || st2.rec.Skipped != 3 {
		t.Fatalf("incremental sync should skip all, new = %d skipped = %d", st2.rec.NewStrm, st2.rec.Skipped)
	}
}

func TestCloud115FullPath(t *testing.T) {
	paths := func(ids, names []string) []struct {
		FileId string `json:"file_id"`
		Name   string `json:"file_name"`
	} {
		out := make([]struct {
			FileId string `json:"file_id"`
			Name   string `json:"file_name"`
		}, 0, len(ids))
		for i := range ids {
			out = append(out, struct {
				FileId string `json:"file_id"`
				Name   string `json:"file_name"`
			}{FileId: ids[i], Name: names[i]})
		}
		return out
	}

	// 祖先链已包含当前目录自身（115 常规返回）
	d := &cloud115.RemoteFileDetail{
		FileId:   "330",
		FileName: "剧集",
		Paths:    paths([]string{"0", "100", "330"}, []string{"", "电影", "剧集"}),
	}
	if got := cloud115FullPath(d); got != "/电影/剧集" {
		t.Fatalf("full path = %q, want /电影/剧集", got)
	}

	// 祖先链不含当前目录自身，用 FileName 兜底
	d = &cloud115.RemoteFileDetail{
		FileId:   "330",
		FileName: "剧集",
		Paths:    paths([]string{"0", "100"}, []string{"", "电影"}),
	}
	if got := cloud115FullPath(d); got != "/电影/剧集" {
		t.Fatalf("full path = %q, want /电影/剧集", got)
	}

	// 根目录：无有效祖先段
	d = &cloud115.RemoteFileDetail{FileId: "0"}
	if got := cloud115FullPath(d); got != "" {
		t.Fatalf("root full path = %q, want empty", got)
	}
}

// TestScanLocalMetaForUploadMultipleCopiesBestEffortMatch 验证：
// 当远端同一路径存在多个副本（1个与本地一致的副本 + 1个脏副本）时，
// 能够择优识别出匹配的副本，跳过上传（uploaded = 0），避免盲盒覆盖导致的重复上传。
func TestScanLocalMetaForUploadMultipleCopiesBestEffortMatch(t *testing.T) {
	svc := testStrmService(t)
	localDir := t.TempDir()

	nfoPath := filepath.Join(localDir, "test.nfo")
	writeFile(t, nfoPath, "correct-content")
	correctSha, err := cloud115.FileSHA1(nfoPath)
	if err != nil {
		t.Fatal(err)
	}

	p := &model.StrmSyncPath{
		Base:       model.Base{ID: "multi-copy-match-path"},
		Provider:   model.StrmProvider115,
		RemotePath: "root-cid",
		LocalPath:  localDir,
		UploadMeta: true,
	}

	st := &strmSyncState{
		s:          svc,
		ctx:        context.Background(),
		p:          p,
		cfg:        &strmPathConfig{UploadMeta: true, MetaExt: []string{"nfo"}},
		rec:        &model.StrmSyncRecord{},
		seenMeta:   map[string]bool{},
		remoteMeta: map[string][]remoteMetaItem{},
	}

	// 模拟远端存在两个同名副本：一个脏副本（较早），一个正确副本（较晚）
	st.recordRemoteMeta(cloud.FileEntry{
		ID:    "stale-id",
		Name:  "test.nfo",
		Size:  int64(len("stale-dirty-content")),
		Sha1:  "STALE_SHA1",
		MTime: 1000,
	}, "test.nfo")

	st.recordRemoteMeta(cloud.FileEntry{
		ID:    "correct-id",
		Name:  "test.nfo",
		Size:  int64(len("correct-content")),
		Sha1:  correctSha,
		MTime: 2000,
	}, "test.nfo")

	if err := st.scanLocalMetaForUpload(); err != nil {
		t.Fatalf("scanLocalMetaForUpload failed: %v", err)
	}

	// 择优匹配：命中 correct-id，不产生上传任务
	tasks, _, err := svc.repo.StrmUpload.List(context.Background(), "", 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Fatalf("expected 0 upload tasks when a matching copy exists, got %d: %v", len(tasks), taskNames(tasks))
	}
}

// TestScanLocalMetaForUploadMultipleCopiesAllStale 验证：
// 当远端同一路径存在多个副本，且所有副本均与本地内容不一致时，
// 上传任务应携带所有旧副本的 ID（逗号分隔），以便上传前批量清理所有旧副本。
func TestScanLocalMetaForUploadMultipleCopiesAllStale(t *testing.T) {
	svc := testStrmService(t)
	localDir := t.TempDir()

	nfoPath := filepath.Join(localDir, "test.nfo")
	writeFile(t, nfoPath, "brand-new-content")

	p := &model.StrmSyncPath{
		Base:       model.Base{ID: "multi-copy-stale-path"},
		Provider:   model.StrmProvider115,
		RemotePath: "root-cid",
		LocalPath:  localDir,
		UploadMeta: true,
	}

	st := &strmSyncState{
		s:          svc,
		ctx:        context.Background(),
		p:          p,
		cfg:        &strmPathConfig{UploadMeta: true, MetaExt: []string{"nfo"}},
		rec:        &model.StrmSyncRecord{},
		seenMeta:   map[string]bool{},
		remoteMeta: map[string][]remoteMetaItem{},
	}

	// 模拟远端存在两个不同大小和哈希的旧副本
	st.recordRemoteMeta(cloud.FileEntry{
		ID:    "old-1",
		Name:  "test.nfo",
		Size:  10,
		Sha1:  "OLD_SHA1",
		MTime: 1000,
	}, "test.nfo")

	st.recordRemoteMeta(cloud.FileEntry{
		ID:    "old-2",
		Name:  "test.nfo",
		Size:  20,
		Sha1:  "OLD_SHA2",
		MTime: 2000,
	}, "test.nfo")

	if err := st.scanLocalMetaForUpload(); err != nil {
		t.Fatalf("scanLocalMetaForUpload failed: %v", err)
	}

	tasks, _, err := svc.repo.StrmUpload.List(context.Background(), "", 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 upload task, got %d", len(tasks))
	}
	if tasks[0].RemoteRef != "old-1,old-2" {
		t.Fatalf("expected RemoteRef to be 'old-1,old-2', got %q", tasks[0].RemoteRef)
	}
}

// TestRecordRemoteMetaDeduplication 验证当 download_meta 与 upload_meta 同时开启时，
// 同一远端元数据被多次入账不会在 remoteMeta 中生成重复副本，杜绝误判自杀式删除。
func TestRecordRemoteMetaDeduplication(t *testing.T) {
	st := &strmSyncState{
		seenMeta:   map[string]bool{},
		remoteMeta: map[string][]remoteMetaItem{},
	}
	entry := cloud.FileEntry{
		ID:    "unique-fid-1",
		Name:  "test.nfo",
		Size:  100,
		Sha1:  "AAAABBBBCCCC",
		MTime: 12345,
	}
	// 连续记录两次同一文件
	st.recordRemoteMeta(entry, "dir/test.nfo")
	st.recordRemoteMeta(entry, "dir/test.nfo")

	items := st.remoteMeta["m:dir/test.nfo"]
	if len(items) != 1 {
		t.Fatalf("expected 1 item after duplicate record, got %d", len(items))
	}
}

// TestWalk115AdaptiveHierarchicalFlatScan 验证自适应分治扁平化扫描：
// 当根目录探测总数 >= 9500 时，系统自动分治展开单层直接子项，对各子目录分别执行扁平拉取，
// 正确合并根目录直属文件与各子目录深层文件，突破 115 开放平台 10000 深度限制。
func TestWalk115AdaptiveHierarchicalFlatScan(t *testing.T) {
	svc := testStrmService(t)
	localDir := t.TempDir()
	acct := &model.StrmAccount{Name: "fake115-adaptive", Provider: "cloud115", Config: "{}", Enabled: true}
	if err := svc.repo.StrmAccount.Create(context.Background(), acct); err != nil {
		t.Fatal(err)
	}
	p := &model.StrmSyncPath{
		Base:       model.Base{ID: "adaptive-path"},
		AccountID:  acct.ID,
		Provider:   model.StrmProvider115,
		RemotePath: "0",
		LocalPath:  localDir,
	}

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		cid := q.Get("cid")
		cur := q.Get("cur")

		switch r.URL.Path {
		case "/open/ufile/files":
			if cid == "0" && cur == "0" {
				// 根目录扁平探测：模拟总文件数 12000 超限 (>= 9500)
				w.Write([]byte(`{"state":true,"count":12000,"data":[]}`))
				return
			}
			if cid == "0" && cur == "1" {
				// 根目录单层列举：返回 1 个直属视频和 2 个子目录
				w.Write([]byte(`{"state":true,"count":3,"data":[
					{"fid":"100","pid":"0","fc":"1","fn":"root.mkv","pc":"pcr","upt":1700000000,"fs":1024},
					{"fid":"1001","pid":"0","fc":"0","fn":"Heyzo","upt":1700000001,"fs":0},
					{"fid":"1002","pid":"0","fc":"0","fn":"S1","upt":1700000002,"fs":0}]}`))
				return
			}
			if cid == "1001" {
				// 子目录 Heyzo 扁平拉取：文件数安全 (< 9500)
				w.Write([]byte(`{"state":true,"count":2,"data":[
					{"fid":"201","pid":"1001","fc":"1","fn":"h1.mkv","pc":"pc201","upt":1700000001,"fs":1024},
					{"fid":"202","pid":"1001","fc":"1","fn":"h2.mkv","pc":"pc202","upt":1700000002,"fs":2048}]}`))
				return
			}
			if cid == "1002" {
				// 子目录 S1 扁平拉取：文件数安全 (< 9500)
				w.Write([]byte(`{"state":true,"count":1,"data":[
					{"fid":"301","pid":"1002","fc":"1","fn":"s1.mkv","pc":"pc301","upt":1700000003,"fs":4096}]}`))
				return
			}
			t.Errorf("unexpected files query: %s", r.URL.RawQuery)
		case "/open/folder/get_info":
			fileID := q.Get("file_id")
			switch fileID {
			case "1001":
				w.Write([]byte(`{"state":true,"data":{"file_id":"1001","file_name":"Heyzo","file_category":"0",
					"paths":[{"file_id":"0","file_name":"根目录"},{"file_id":"1001","file_name":"Heyzo"}]}}`))
			case "1002":
				w.Write([]byte(`{"state":true,"data":{"file_id":"1002","file_name":"S1","file_category":"0",
					"paths":[{"file_id":"0","file_name":"根目录"},{"file_id":"1002","file_name":"S1"}]}}`))
			default:
				t.Errorf("unexpected get_info file_id: %s", fileID)
			}
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer api.Close()
	oldPro := cloud115.ProAPIBase
	cloud115.ProAPIBase = api.URL
	defer func() { cloud115.ProAPIBase = oldPro }()

	oc := cloud115.NewOpenClient("app", "at", "rt")
	st := &strmSyncState{
		s:               svc,
		ctx:             context.Background(),
		p:               p,
		provider:        cloud.NewOpenAPI115("app", "at", "rt"),
		cfg:             &strmPathConfig{VideoExt: []string{"mkv"}, MetaExt: []string{"nfo"}, AddPath: 1},
		rec:             &model.StrmSyncRecord{},
		syncType:        model.StrmSyncTypeFull,
		dirCache:        sync.Map{},
		seenVideo:       map[string]bool{},
		seenMeta:        map[string]bool{},
		remoteMeta:      map[string][]remoteMetaItem{},
		seenMetaTarget:  map[string]cloud.FileEntry{},
		seenVideoTarget: map[string]cloud.FileEntry{},
	}
	if err := st.walk115Flat(oc); err != nil {
		t.Fatalf("walk115Flat adaptive failed: %v", err)
	}

	// 1个根目录视频 + 2个Heyzo视频 + 1个S1视频 = 共4个视频成功生成 .strm
	if st.rec.NewStrm != 4 {
		t.Fatalf("expected 4 strm created, got %d", st.rec.NewStrm)
	}
	expectedFiles := []string{
		filepath.Join(localDir, "root.strm"),
		filepath.Join(localDir, "Heyzo", "h1.strm"),
		filepath.Join(localDir, "Heyzo", "h2.strm"),
		filepath.Join(localDir, "S1", "s1.strm"),
	}
	for _, f := range expectedFiles {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("expected strm %s to exist, err: %v", f, err)
		}
	}
}

// TestScanLocalMetaForUploadSkipsRecentDoneSameSize 验证近期已成功上传且大小未变时，
// 即使远端列表尚未反映，也不再入队，缩短「done 但列表滞后」窗口。
func TestScanLocalMetaForUploadSkipsRecentDoneSameSize(t *testing.T) {
	svc := testStrmService(t)
	localDir := t.TempDir()
	nfoPath := filepath.Join(localDir, "movie.nfo")
	content := []byte("uploaded-recently")
	writeFile(t, nfoPath, string(content))

	p := &model.StrmSyncPath{
		Base:       model.Base{ID: "recent-done-skip"},
		Provider:   model.StrmProvider115,
		RemotePath: "root-cid",
		LocalPath:  localDir,
		UploadMeta: true,
	}
	now := time.Now()
	doneTask := &model.StrmUploadTask{
		SyncPathID: p.ID,
		Provider:   model.StrmProvider115,
		FileName:   "movie.nfo",
		LocalPath:  nfoPath,
		RemotePath: "parent-cid",
		RemoteRef:  "new-file-id",
		Size:       int64(len(content)),
		Status:     model.StrmTaskDone,
		FinishedAt: &now,
	}
	if err := svc.repo.StrmUpload.Create(context.Background(), doneTask); err != nil {
		t.Fatal(err)
	}

	st := &strmSyncState{
		s:                     svc,
		ctx:                   context.Background(),
		p:                     p,
		cfg:                   &strmPathConfig{UploadMeta: true, MetaExt: []string{"nfo"}},
		rec:                   &model.StrmSyncRecord{},
		seenMeta:              map[string]bool{},
		remoteMeta:            map[string][]remoteMetaItem{}, // 远端列表空 = 滞后
		dirPathToID:           map[string]string{"": "parent-cid"},
		activeUploadPaths:     map[string]bool{},
		recentDoneUploadSizes: nil, // 让 scan 自行从 DB 加载
	}
	if err := st.scanLocalMetaForUpload(); err != nil {
		t.Fatalf("scanLocalMetaForUpload failed: %v", err)
	}
	if st.rec.Uploaded != 0 {
		t.Fatalf("recent done same-size should skip enqueue, uploaded=%d", st.rec.Uploaded)
	}
	if len(st.pendingUploads) != 0 {
		t.Fatalf("expected no pending uploads, got %d", len(st.pendingUploads))
	}
}

// TestScanLocalMetaForUploadRequeuesWhenRecentDoneSizeChanged 本地大小变化后不应被近期 done 窗口挡住。
func TestScanLocalMetaForUploadRequeuesWhenRecentDoneSizeChanged(t *testing.T) {
	svc := testStrmService(t)
	localDir := t.TempDir()
	nfoPath := filepath.Join(localDir, "movie.nfo")
	writeFile(t, nfoPath, "new-longer-content-xxx")

	p := &model.StrmSyncPath{
		Base:       model.Base{ID: "recent-done-resize"},
		Provider:   model.StrmProvider115,
		RemotePath: "root-cid",
		LocalPath:  localDir,
		UploadMeta: true,
	}
	now := time.Now()
	doneTask := &model.StrmUploadTask{
		SyncPathID: p.ID,
		Provider:   model.StrmProvider115,
		FileName:   "movie.nfo",
		LocalPath:  nfoPath,
		RemotePath: "parent-cid",
		Size:       3, // 旧大小
		Status:     model.StrmTaskDone,
		FinishedAt: &now,
	}
	if err := svc.repo.StrmUpload.Create(context.Background(), doneTask); err != nil {
		t.Fatal(err)
	}

	st := &strmSyncState{
		s:                 svc,
		ctx:               context.Background(),
		p:                 p,
		cfg:               &strmPathConfig{UploadMeta: true, MetaExt: []string{"nfo"}},
		rec:               &model.StrmSyncRecord{},
		seenMeta:          map[string]bool{},
		remoteMeta:        map[string][]remoteMetaItem{},
		dirPathToID:       map[string]string{"": "parent-cid"},
		activeUploadPaths: map[string]bool{},
	}
	if err := st.scanLocalMetaForUpload(); err != nil {
		t.Fatalf("scanLocalMetaForUpload failed: %v", err)
	}
	if st.rec.Uploaded != 1 {
		t.Fatalf("size changed should re-enqueue, uploaded=%d", st.rec.Uploaded)
	}
	tasks, _, err := svc.repo.StrmUpload.List(context.Background(), model.StrmTaskPending, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, task := range tasks {
		if task.LocalPath == nfoPath && task.Status == model.StrmTaskPending {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected a pending upload task for resized local file")
	}
}

func TestHandleVideoPreferPicksLargestAndSkipsOthers(t *testing.T) {
	svc := testStrmService(t)
	local := t.TempDir()
	p := syncPathRecord(t, svc, model.StrmProviderLocal, t.TempDir(), local, true)
	p.KeepExt = false
	st := &strmSyncState{
		s:               svc,
		ctx:             context.Background(),
		p:               p,
		cfg:             &strmPathConfig{BaseURL: "http://test.local:8096", VideoExt: csvSplit(StrmDefaultVideoExt), AddPath: 1, KeepExt: false},
		rec:             &model.StrmSyncRecord{},
		syncType:        model.StrmSyncTypeFull,
		seenVideo:       map[string]bool{},
		seenVideoTarget: map[string]cloud.FileEntry{},
		remoteVideos:    map[string][]remoteVideoCandidate{},
	}
	st.handleVideo(cloud.FileEntry{ID: "1", Name: "竞女01.mp4", Size: 100, PickCode: "pc-mp4", MTime: 1000}, "竞女01.mp4", ".mp4")
	st.handleVideo(cloud.FileEntry{ID: "2", Name: "竞女01.mkv", Size: 500, PickCode: "pc-mkv", MTime: 900}, "竞女01.mkv", ".mkv")
	st.flushPreferredVideos()

	preferPath := filepath.Join(local, "竞女01.strm")
	if _, err := os.Stat(preferPath); err != nil {
		t.Fatalf("expected prefer strm at %s: %v", preferPath, err)
	}
	data, err := os.ReadFile(preferPath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "video.mkv") {
		t.Fatalf("prefer should pick larger mkv, content=%s", content)
	}
	if !strings.Contains(content, "%E7%AB%9E%E5%A5%B301.mkv") && !strings.Contains(content, "竞女01.mkv") {
		t.Fatalf("prefer strm should point at winner path, content=%s", content)
	}
	if _, err := os.Stat(filepath.Join(local, "竞女01.mkv.strm")); !os.IsNotExist(err) {
		t.Fatal("prefer mode must not write keep_ext names")
	}
	if st.rec.NewStrm != 1 {
		t.Fatalf("NewStrm=%d want 1", st.rec.NewStrm)
	}
	if st.rec.Skipped < 1 {
		t.Fatalf("Skipped=%d want >=1 for loser", st.rec.Skipped)
	}
}

func TestHandleVideoKeepExtWritesAllVersions(t *testing.T) {
	svc := testStrmService(t)
	local := t.TempDir()
	p := syncPathRecord(t, svc, model.StrmProviderLocal, t.TempDir(), local, true)
	p.KeepExt = true
	st := &strmSyncState{
		s:               svc,
		ctx:             context.Background(),
		p:               p,
		cfg:             &strmPathConfig{BaseURL: "http://test.local:8096", VideoExt: csvSplit(StrmDefaultVideoExt), AddPath: 1, KeepExt: true},
		rec:             &model.StrmSyncRecord{},
		syncType:        model.StrmSyncTypeFull,
		seenVideo:       map[string]bool{},
		seenVideoTarget: map[string]cloud.FileEntry{},
		remoteVideos:    map[string][]remoteVideoCandidate{},
	}
	st.handleVideo(cloud.FileEntry{ID: "1", Name: "竞女01.mp4", Size: 100, PickCode: "pc-mp4"}, "竞女01.mp4", ".mp4")
	st.handleVideo(cloud.FileEntry{ID: "2", Name: "竞女01.mkv", Size: 500, PickCode: "pc-mkv"}, "竞女01.mkv", ".mkv")
	st.flushPreferredVideos()

	mkvPath := filepath.Join(local, "竞女01.mkv.strm")
	mp4Path := filepath.Join(local, "竞女01.mp4.strm")
	for _, path := range []string{mkvPath, mp4Path} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected keep_ext strm %s: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(local, "竞女01.strm")); !os.IsNotExist(err) {
		t.Fatal("keep_ext mode must not write stripped name.strm")
	}
	if st.rec.NewStrm != 2 {
		t.Fatalf("NewStrm=%d want 2", st.rec.NewStrm)
	}
}
