package service

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

func TestParseCloudDownloadLinks(t *testing.T) {
	links, skipped := parseCloudDownloadLinks([]string{
		"  magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&dn=x  ",
		"ed2k://|file|a.mkv|100|0123456789ABCDEF0123456789ABCDEF|/",
		"https://example.com/a.mkv",
		"ftp://example.com/b.mkv",
		"magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567", // 与第一条重复
		"not-a-link",
		"",
		"magnet:?xt=urn:btih:tooshort", // 非法哈希
	})
	if len(links) != 4 {
		t.Fatalf("links = %d, want 4: %+v", len(links), links)
	}
	if links[0].SourceType != model.CloudDownloadSourceMagnet {
		t.Errorf("first source type = %q", links[0].SourceType)
	}
	if links[0].Key != "0123456789ABCDEF0123456789ABCDEF01234567" {
		t.Errorf("magnet key = %q, want uppercase info hash", links[0].Key)
	}
	if links[1].SourceType != model.CloudDownloadSourceED2K {
		t.Errorf("second source type = %q", links[1].SourceType)
	}
	if links[2].SourceType != model.CloudDownloadSourceHTTP {
		t.Errorf("third source type = %q", links[2].SourceType)
	}
	if links[3].SourceType != model.CloudDownloadSourceFTP {
		t.Errorf("fourth source type = %q", links[3].SourceType)
	}
	// 空行不算错误，只有重复链接/不支持的类型/非法哈希会被记为跳过。
	if len(skipped) != 3 {
		t.Fatalf("skipped = %d, want 3: %+v", len(skipped), skipped)
	}
}

func TestParseCloudDownloadLinksAcceptsMultilinePaste(t *testing.T) {
	links, _ := parseCloudDownloadLinks([]string{
		"magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567\nhttps://example.com/a.mkv\n",
	})
	if len(links) != 2 {
		t.Fatalf("links = %d, want 2", len(links))
	}
}

func TestMagnetInfoHashSupportsBase32(t *testing.T) {
	got := magnetInfoHash("magnet:?xt=urn:btih:ABCDEFGHIJKLMNOPQRSTUVWXYZ234567")
	if got != "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567" {
		t.Fatalf("base32 hash = %q", got)
	}
	if got := magnetInfoHash("magnet:?dn=x"); got != "" {
		t.Fatalf("missing btih should yield empty, got %q", got)
	}
}

func TestOfflineErrorMessage(t *testing.T) {
	cases := []struct {
		code int
		want string
	}{
		{1000012, "115 云下载配额已用尽，请在 115 端获取更多配额后重试"},
		{91006, "115 存储空间不足"},
		{20018, "115 找不到该种子文件（可能已被清理），请重新上传 .torrent"},
		{1000011, "单批链接超过 115 条上限"},
		{990002, "115 拒绝了提交参数：bad"},
		{980004, "115 操作失败，请稍后重试"},
	}
	for _, c := range cases {
		if got := offlineErrorMessage(c.code, "bad"); got != c.want {
			t.Errorf("code %d => %q, want %q", c.code, got, c.want)
		}
	}
	if got := offlineErrorMessage(1234, "原始信息"); got != "原始信息" {
		t.Errorf("unknown code should keep 115 message, got %q", got)
	}
}

func TestCloudDownloadDebounce(t *testing.T) {
	db := newServiceTestDB(t)
	repos := repository.New(db)
	svc := NewCloudDownloadService(zap.NewNop(), repos, nil, nil, nil)

	if !svc.debounceElapsed("p1") {
		t.Fatal("first trigger must be allowed")
	}
	svc.noteTrigger("p1")
	if svc.debounceElapsed("p1") {
		t.Fatal("second trigger inside window must be suppressed")
	}
	// 另一个同步目录互不影响。
	if !svc.debounceElapsed("p2") {
		t.Fatal("debounce must be per sync path")
	}
	// 窗口置 0 表示不合并。
	if err := repos.Setting.Set(context.Background(), CloudDownloadSettingSyncDebounce, "0"); err != nil {
		t.Fatal(err)
	}
	if !svc.debounceElapsed("p1") {
		t.Fatal("zero window should disable debounce")
	}
}

func TestMatchLibraryRoot(t *testing.T) {
	db := newServiceTestDB(t)
	repos := repository.New(db)
	ctx := context.Background()

	lib := &model.Library{Name: "电影", Path: "/media/movies", Type: "movie", Enabled: true}
	root := &model.LibraryRoot{LibraryID: "tmp", Path: "/media/movies", Enabled: true}
	if err := repos.Library.CreateWithRoots(ctx, lib, []model.LibraryRoot{*root}); err != nil {
		t.Fatal(err)
	}

	svc := NewCloudDownloadService(zap.NewNop(), repos, nil, nil, nil)
	if got := svc.matchLibraryRoot(ctx, "/media/movies"); got == nil || got.LibraryID != lib.ID {
		t.Fatalf("exact path should match library, got %+v", got)
	}
	if got := svc.matchLibraryRoot(ctx, "/media/movies/2024"); got == nil || got.LibraryID != lib.ID {
		t.Fatalf("sub directory should match library, got %+v", got)
	}
	if got := svc.matchLibraryRoot(ctx, "/media/tv"); got != nil {
		t.Fatalf("unrelated path must not match, got %+v", got)
	}
}

func TestIsCloudDownloadTerminal(t *testing.T) {
	terminal := []string{model.CloudDownloadSucceeded, model.CloudDownloadFailed, model.CloudDownloadCanceled}
	for _, s := range terminal {
		if !isCloudDownloadTerminal(s) {
			t.Errorf("%s should be terminal", s)
		}
	}
	for _, s := range []string{model.CloudDownloadQueued, model.CloudDownloadSubmitted, model.CloudDownloadDownloading} {
		if isCloudDownloadTerminal(s) {
			t.Errorf("%s should not be terminal", s)
		}
	}
}

func TestResolveTargetRejectsInvalidSyncPath(t *testing.T) {
	db := newServiceTestDB(t)
	repos := repository.New(db)
	ctx := context.Background()
	svc := NewCloudDownloadService(zap.NewNop(), repos, nil, nil, nil)

	if _, err := svc.resolveTarget(ctx, ""); err == nil {
		t.Fatal("empty sync path should fail")
	}
	if _, err := svc.resolveTarget(ctx, "missing"); err == nil {
		t.Fatal("unknown sync path should fail")
	}

	// 远端目录为 0（网盘根目录）：离线文件永远扫不到，必须拒绝。
	bad := &model.StrmSyncPath{
		Name: "根目录", AccountID: "acc", Provider: model.StrmProvider115,
		RemotePath: "0", LocalPath: "/media/root", Enabled: true,
	}
	if err := repos.StrmSyncPath.Create(ctx, bad); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.resolveTarget(ctx, bad.ID); err == nil {
		t.Fatal("remote root path should be rejected")
	}

	// 非 115 同步目录不支持云下载。
	cd2 := &model.StrmSyncPath{
		Name: "CD2", AccountID: "acc", Provider: model.StrmProviderCloudDrive,
		RemotePath: "/movies", LocalPath: "/media/cd2", Enabled: true,
	}
	if err := repos.StrmSyncPath.Create(ctx, cd2); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.resolveTarget(ctx, cd2.ID); err == nil {
		t.Fatal("non-115 provider should be rejected")
	}
}

func TestCloudDownloadSettingsDefaults(t *testing.T) {
	db := newServiceTestDB(t)
	svc := NewCloudDownloadService(zap.NewNop(), repository.New(db), nil, nil, nil)

	if got := svc.pollInterval(); got != 60*time.Second {
		t.Errorf("default poll interval = %v", got)
	}
	if got := svc.debounceWindow(); got != 30*time.Second {
		t.Errorf("default debounce = %v", got)
	}
	if !svc.autoScrape() {
		t.Error("auto scrape should default to true")
	}
	if got := svc.taskTimeout(); got != 72*time.Hour {
		t.Errorf("default timeout = %v", got)
	}
	if got := svc.torrentMaxBytes(); got != 20<<20 {
		t.Errorf("default torrent max = %d", got)
	}
	if got := svc.maxPollPages(); got != 10 {
		t.Errorf("default max pages = %d", got)
	}

	// 越界值必须被夹紧，避免把轮询间隔配成 1 秒打爆 115 限流。
	if err := repository.New(db).Setting.Set(context.Background(), CloudDownloadSettingPollInterval, "1"); err != nil {
		t.Fatal(err)
	}
	if got := svc.pollInterval(); got != 30*time.Second {
		t.Errorf("clamped poll interval = %v, want 30s", got)
	}
}
