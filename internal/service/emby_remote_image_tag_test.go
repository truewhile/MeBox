package service

import (
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

// newImageTagTestService 构造只用于图片标签映射测试的远程 Emby 服务。
func newImageTagTestService(t *testing.T) (*EmbyRemoteService, *model.EmbyMount, *model.StrmAccount) {
	t.Helper()
	db := newServiceTestDB(t, &model.StrmAccount{}, &model.EmbyMount{})
	repos := repository.New(db)
	svc := NewEmbyRemoteService(&config.Config{}, zap.NewNop(), repos, NewCryptoService("", zap.NewNop()))
	acct := &model.StrmAccount{
		Base:     model.Base{ID: "acct-tag"},
		Name:     "tag-emby",
		Provider: model.StrmProviderEmbyRemote,
		Enabled:  true,
		Config:   `{"url":"http://emby.test:8096","token":"fake-token"}`,
	}
	mount := &model.EmbyMount{
		Base:         model.Base{ID: "mount-tag"},
		AccountID:    acct.ID,
		RemoteViewID: "view-tag",
		Enabled:      true,
	}
	if err := repos.StrmAccount.Create(t.Context(), acct); err != nil {
		t.Fatalf("create account: %v", err)
	}
	if err := repos.EmbyMount.Create(t.Context(), mount); err != nil {
		t.Fatalf("create mount: %v", err)
	}
	return svc, mount, acct
}

// testRemoteImageConfig 是图片 URL 构造所需的最小远程配置。
func testRemoteImageConfig() *EmbyRemoteConfig {
	return &EmbyRemoteConfig{BaseURL: "http://emby.test:8096", Token: "fake-token"}
}

// TestMapRemoteItemToMediaCarriesImageTags 下发的图片 URL 必须带上远端图片
// 标签，否则远端换图后磁盘/客户端缓存永远不会失效。
func TestMapRemoteItemToMediaCarriesImageTags(t *testing.T) {
	svc, mount, acct := newImageTagTestService(t)

	media := svc.MapRemoteItemToMedia(t.Context(), mount, acct, testRemoteImageConfig(), map[string]any{
		"Id":   "movie-1",
		"Name": "测试影片",
		"Type": "Movie",
		"ImageTags": map[string]any{
			"Primary": "primary-tag-1",
		},
		"BackdropImageTags": []any{"backdrop-tag-1"},
	})

	if !strings.Contains(media.PosterURL, "tag=primary-tag-1") {
		t.Fatalf("PosterURL missing remote image tag: %q", media.PosterURL)
	}
	if !strings.Contains(media.BackdropURL, "tag=backdrop-tag-1") {
		t.Fatalf("BackdropURL missing remote image tag: %q", media.BackdropURL)
	}
}

// TestMapRemoteItemToMediaCarriesImageTagsAfterRewrite 载荷先经过 ID 伪装
// （tag 变成 embyremote~...~tag）时仍要还原出原始 tag。
func TestMapRemoteItemToMediaCarriesImageTagsAfterRewrite(t *testing.T) {
	svc, mount, acct := newImageTagTestService(t)

	item := map[string]any{
		"Id":   "movie-2",
		"Name": "伪装过的影片",
		"Type": "Movie",
		"ImageTags": map[string]any{
			"Primary": "primary-tag-2",
		},
	}
	RewriteEmbyRemoteIDs(item, mount.ID)

	media := svc.MapRemoteItemToMedia(t.Context(), mount, acct, testRemoteImageConfig(), item)
	if !strings.Contains(media.PosterURL, "tag=primary-tag-2") {
		t.Fatalf("PosterURL missing decoded remote image tag: %q", media.PosterURL)
	}
	if strings.Contains(media.PosterURL, EmbyRemoteIDPrefix) {
		t.Fatalf("PosterURL leaked disguised tag: %q", media.PosterURL)
	}
}

// TestRemoteImageURLCacheKeyFollowsImageTag 远端换图（tag 变化）后，图片 URL
// 必须随之变化，缓存键才会失效。
func TestRemoteImageURLCacheKeyFollowsImageTag(t *testing.T) {
	svc, mount, acct := newImageTagTestService(t)

	first := svc.MapRemoteItemToMedia(t.Context(), mount, acct, testRemoteImageConfig(), map[string]any{
		"Id":        "movie-3",
		"Name":      "换图影片",
		"Type":      "Movie",
		"ImageTags": map[string]any{"Primary": "old-tag"},
	})
	second := svc.MapRemoteItemToMedia(t.Context(), mount, acct, testRemoteImageConfig(), map[string]any{
		"Id":        "movie-3",
		"Name":      "换图影片",
		"Type":      "Movie",
		"ImageTags": map[string]any{"Primary": "new-tag"},
	})

	if first.PosterURL == second.PosterURL {
		t.Fatalf("image URL did not change when the remote tag changed: %q", first.PosterURL)
	}
	if imageCacheKeyURL(first.PosterURL) == imageCacheKeyURL(second.PosterURL) {
		t.Fatal("image cache key did not change when the remote tag changed")
	}
}

// TestMapRemoteItemToMediaWithoutImageTagsKeepsURLTagFree 没有标签时保持原样
// （不追加空 tag 参数）。
func TestMapRemoteItemToMediaWithoutImageTagsKeepsURLTagFree(t *testing.T) {
	svc, mount, acct := newImageTagTestService(t)

	media := svc.MapRemoteItemToMedia(t.Context(), mount, acct, testRemoteImageConfig(), map[string]any{
		"Id":        "movie-4",
		"Name":      "无标签影片",
		"Type":      "Movie",
		"ImageTags": map[string]any{"Primary": "primary-tag-4"},
	})
	if !strings.Contains(media.PosterURL, "tag=primary-tag-4") {
		t.Fatalf("PosterURL missing tag: %q", media.PosterURL)
	}
	if strings.Contains(media.BackdropURL, "tag=") {
		t.Fatalf("BackdropURL should not carry a tag when absent: %q", media.BackdropURL)
	}

	unknown := svc.MapRemoteItemToMedia(t.Context(), mount, acct, testRemoteImageConfig(), map[string]any{
		"Id":   "movie-5",
		"Name": "未知标签影片",
		"Type": "Movie",
		"ImageTags": map[string]any{
			"Primary": "primary-tag-5",
		},
	})
	if strings.Contains(unknown.BackdropURL, "tag=") {
		t.Fatalf("unknown image tag should degrade to no tag: %q", unknown.BackdropURL)
	}
}

// TestRemoteImageURLIncludesRememberedTag 兼容层（/emby/Items/{id}/Images/...）
// 复用同一份标签映射。
func TestRemoteImageURLIncludesRememberedTag(t *testing.T) {
	svc, mount, acct := newImageTagTestService(t)

	svc.rememberRemoteImageTags(acct.ID, map[string]any{
		"Id":        "movie-6",
		"ImageTags": map[string]any{"Primary": "compat-tag"},
	})

	raw, err := svc.RemoteImageURL(t.Context(), acct, "movie-6", "Primary")
	if err != nil {
		t.Fatalf("RemoteImageURL failed: %v", err)
	}
	if !strings.Contains(raw, "tag=compat-tag") {
		t.Fatalf("remote image URL missing tag: %q", raw)
	}

	encoded := EncodeEmbyRemoteID(mount.ID, "movie-6")
	if got := svc.RemoteImageTagOfEncodedID(t.Context(), encoded, "Primary"); got != "compat-tag" {
		t.Fatalf("RemoteImageTagOfEncodedID = %q, want compat-tag", got)
	}
}
