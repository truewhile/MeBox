package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/truewhile/MeBox/internal/model"
)

func TestWriteMediaNFOUsesMappedDestinationPath(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MEBOX_MEDIA_DIR", root)
	t.Setenv("MEBOX_MEDIA_CONTAINER_DIR", "/media")
	mediaPath := filepath.Join(root, "电影", "测试电影.mkv")
	if err := os.MkdirAll(filepath.Dir(mediaPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mediaPath, []byte("media"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := WriteMediaNFO(&model.Media{
		Title: "测试电影",
		Path:  "/media/电影/测试电影.mkv",
		Year:  2026,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "电影", "测试电影.nfo")
	if got != want {
		t.Fatalf("nfo path = %q, want %q", got, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatal(err)
	}
}

func TestWriteMediaNFOUsesEpisodeTitleForEpisodeDetails(t *testing.T) {
	root := t.TempDir()
	mediaPath := filepath.Join(root, "剧集", "间谍过家家", "Season 02", "间谍过家家 - S02E01.mkv")
	if err := os.MkdirAll(filepath.Dir(mediaPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mediaPath, []byte("media"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := WriteMediaNFO(&model.Media{
		Title:        "间谍过家家",
		OriginalName: "SPY×FAMILY",
		EpisodeTitle: "任务代号: 猫",
		Path:         mediaPath,
		SeasonNum:    2,
		EpisodeNum:   1,
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "<title>任务代号: 猫</title>") || !strings.Contains(text, "<showtitle>间谍过家家</showtitle>") {
		t.Fatalf("episode nfo did not keep episode/show titles separate:\n%s", text)
	}
}

func TestWriteMediaNFOAdultTitleWithCode(t *testing.T) {
	root := t.TempDir()
	mediaPath := filepath.Join(root, "成人", "IPZZ-090-测试成人影片", "IPZZ-090.mp4")
	if err := os.MkdirAll(filepath.Dir(mediaPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mediaPath, []byte("media"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := WriteMediaNFO(&model.Media{
		Title:        "测试成人影片",
		OriginalName: "IPZZ-090",
		Path:         mediaPath,
		NSFW:         true,
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "<title>IPZZ-090-测试成人影片</title>") {
		t.Fatalf("expected title with code at front in adult nfo, got:\n%s", text)
	}
	if !strings.Contains(text, "<originaltitle>IPZZ-090</originaltitle>") {
		t.Fatalf("expected originaltitle with code, got:\n%s", text)
	}
}

func TestWriteMediaNFORoundTripsExternalUniqueIDs(t *testing.T) {
	root := t.TempDir()
	mediaPath := filepath.Join(root, "云下载", "sivr-270", "sivr-270-1.strm")
	if err := os.MkdirAll(filepath.Dir(mediaPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mediaPath, []byte("strm"), 0o644); err != nil {
		t.Fatal(err)
	}

	dst, err := WriteMediaNFO(&model.Media{
		Title:        "SIVR-270-河北彩花",
		OriginalName: "SIVR-270",
		Path:         mediaPath,
		Year:         2023,
		NSFW:         true,
		DoubanID:     "SIVR-270",
		TheTVDBID:    "JavBus",
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	// 成人条目把番号/provider 借用存在 douban/thetvdb 两列；sidecar 必须把它们
	// 带回来，否则「在线刮削 → 写 NFO → 重扫读 NFO」之后同一部片会被按不同
	// 分组键拆成多张卡。
	for _, want := range []string{
		`<uniqueid type="douban">SIVR-270</uniqueid>`,
		`<uniqueid type="thetvdb">JavBus</uniqueid>`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("sidecar missing %s, got:\n%s", want, text)
		}
	}

	meta, err := ReadLocalMetadata(mediaPath, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if meta == nil || meta.DoubanID != "SIVR-270" || meta.TheTVDBID != "JavBus" {
		t.Fatalf("external ids did not round trip: %#v", meta)
	}
}
