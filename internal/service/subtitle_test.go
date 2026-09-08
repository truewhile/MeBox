package service

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

func TestSubtitleDiscoverNoTracksReturnsEmptySlice(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Library{}, &model.Media{}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	media := model.Media{
		Title: "No Subtitles",
		Path:  filepath.Join(dir, "No Subtitles.mkv"),
	}
	if err := db.Create(&media).Error; err != nil {
		t.Fatal(err)
	}

	svc := NewSubtitleService(&config.Config{}, zap.NewNop(), repository.New(db))
	tracks, err := svc.Discover(t.Context(), media.ID)
	if err != nil {
		t.Fatal(err)
	}
	if tracks == nil {
		t.Fatal("tracks is nil, want empty slice")
	}
	if len(tracks) != 0 {
		t.Fatalf("len(tracks) = %d, want 0", len(tracks))
	}
}

func TestEmbeddedSubtitleProbeClassifiesTextAndBitmapTracks(t *testing.T) {
	var probe embeddedSubtitleProbe
	raw := []byte(`{"streams":[
		{"index":2,"codec_name":"ass","tags":{"language":"chi","title":"中文"},"disposition":{"default":1}},
		{"index":4,"codec_name":"hdmv_pgs_subtitle","tags":{"language":"eng"},"disposition":{"forced":1}}
	]}`)
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatal(err)
	}
	tracks := subtitleTracksFromProbe(probe)
	if len(tracks) != 2 {
		t.Fatalf("len(tracks) = %d, want 2", len(tracks))
	}
	if tracks[0].Delivery != "webvtt" || tracks[0].Path != "embedded:2" {
		t.Fatalf("text track = %#v", tracks[0])
	}
	if tracks[1].Delivery != "burn" || tracks[1].StreamIndex != 4 {
		t.Fatalf("bitmap track = %#v", tracks[1])
	}
}

func TestNormaliseTimecode(t *testing.T) {
	cases := map[string]string{
		"0:00:01":       "00:00:01",
		"0:00:01.51":    "00:00:01.510", // ASS centiseconds -> 3-digit ms
		"0:00:04.123":   "00:00:04.123", // already 3 digits
		"00:00:12.07":   "00:00:12.070", // 2-digit fraction padded
		"00:01:30.1234": "00:01:30.123", // capped at 3 digits
		"00:00:1.5":     "00:00:01.500", // single-digit seconds and fraction
	}
	for in, want := range cases {
		if got := normaliseTimecode(in); got != want {
			t.Errorf("normaliseTimecode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAssToVTTDeduplicatesDialogueAndNormalisesLineBreaks(t *testing.T) {
	body := strings.Join([]string{
		`Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,{\an2}第一行\N第二行`,
		`Dialogue: 1,0:00:01.00,0:00:02.00,Copy,,0,0,0,,{\bord2}第一行\N第二行`,
		`Dialogue: 0,0:00:03.00,0:00:04.00,Default,,0,0,0,,{\i1}`,
	}, "\n")

	got := assToVTT(body)
	if strings.Count(got, "第一行\n第二行") != 1 {
		t.Fatalf("duplicate ASS dialogue was not collapsed:\n%s", got)
	}
	if strings.Contains(got, `\N`) || strings.Contains(got, `\an2`) {
		t.Fatalf("ASS control sequences leaked into WebVTT:\n%s", got)
	}
	if strings.Contains(got, "00:00:03.000 --> 00:00:04.000") {
		t.Fatalf("empty styled dialogue should be omitted:\n%s", got)
	}
}

func TestSubtitleServeRawWritesSourceBytes(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Library{}, &model.Media{}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	videoPath := filepath.Join(dir, "MovieName.mkv")
	subPath := filepath.Join(dir, "MovieName.ass")
	raw := "Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,線合なライン\n"
	if err := os.WriteFile(videoPath, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(subPath, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	media := model.Media{Title: "MovieName", Path: videoPath}
	if err := db.Create(&media).Error; err != nil {
		t.Fatal(err)
	}

	svc := NewSubtitleService(&config.Config{}, zap.NewNop(), repository.New(db))
	var buf bytes.Buffer
	if err := svc.ServeRaw(t.Context(), media.ID, subPath, &buf); err != nil {
		t.Fatal(err)
	}
	// ServeRaw must NOT convert ASS->VTT; it returns the exact source bytes.
	if got := buf.String(); got != raw {
		t.Fatalf("ServeRaw returned %q, want raw %q", got, raw)
	}
}
