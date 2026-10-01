package service

import (
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

// newProbeBackfillScanner builds a scanner over the given repository whose
// ffprobe "binary" is a temp file, so probe availability is deterministic
// regardless of what happens to be installed on the test machine.
func newProbeBackfillScanner(t *testing.T, repos *repository.Container, probeAvailable bool) *ScannerService {
	t.Helper()
	cfg := &config.Config{}
	probePath := filepath.Join(t.TempDir(), "not-a-real-ffprobe")
	if probeAvailable {
		if err := os.WriteFile(probePath, []byte("stub"), 0o755); err != nil {
			t.Fatalf("write stub probe: %v", err)
		}
	}
	// Point the probe and its ffmpeg fallback at paths that only exist when this
	// test wants them to, so Available() cannot pick up a system binary.
	cfg.App.FFprobePath = probePath
	cfg.App.FFmpegPath = probePath
	return NewScannerService(cfg, zap.NewNop(), repos, NewHub(zap.NewNop()), NewFFprobeService(cfg, zap.NewNop()), nil)
}

func newProbeBackfillRepo(t *testing.T) *repository.Container {
	t.Helper()
	return repository.New(newServiceTestDB(t, &model.Library{}, &model.Media{}, &model.Setting{}))
}

func probeBackfillScanner(t *testing.T, probeAvailable bool) *ScannerService {
	t.Helper()
	return newProbeBackfillScanner(t, newProbeBackfillRepo(t), probeAvailable)
}

// localMediaProbeDataMissing decides which unchanged rows are worth re-probing.
func TestLocalMediaProbeDataMissing(t *testing.T) {
	cases := []struct {
		name     string
		existing existingLocalMedia
		want     bool
	}{
		{name: "never probed", existing: existingLocalMedia{}, want: true},
		{name: "duration only", existing: existingLocalMedia{DurationSec: 148}, want: false},
		{name: "resolution only", existing: existingLocalMedia{Width: 1920, Height: 1080}, want: false},
		{name: "video codec only", existing: existingLocalMedia{VideoCodec: "h264"}, want: false},
		{name: "audio codec only", existing: existingLocalMedia{AudioCodec: "aac"}, want: false},
		{name: "fully probed", existing: existingLocalMedia{DurationSec: 148, Width: 1920, Height: 1080, VideoCodec: "h264", AudioCodec: "aac"}, want: false},
		{name: "blank codecs are not data", existing: existingLocalMedia{VideoCodec: "  ", AudioCodec: " "}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := localMediaProbeDataMissing(tc.existing); got != tc.want {
				t.Fatalf("localMediaProbeDataMissing(%+v) = %v, want %v", tc.existing, got, tc.want)
			}
		})
	}
}

// A rescan must re-queue ffprobe for media that still has no technical metadata,
// otherwise a library scanned before ffmpeg was installed stays empty forever.
func TestRescanQueuesProbeBackfillForUnprobedMedia(t *testing.T) {
	sc := probeBackfillScanner(t, true)

	if !sc.queueProbeBackfillIfNeeded("/media/movie.mkv", ".mkv", existingLocalMedia{}) {
		t.Fatal("unprobed media should get a backfill probe queued")
	}
	if sc.queueProbeBackfillIfNeeded("/media/movie.mkv", ".mkv", existingLocalMedia{DurationSec: 148, VideoCodec: "h264"}) {
		t.Fatal("already probed media must not be re-queued")
	}
}

// Backfill is pointless without a usable binary: every probe would fail, so the
// scanner must not flood the probe queue.
func TestRescanSkipsProbeBackfillWhenProbeUnavailable(t *testing.T) {
	sc := probeBackfillScanner(t, false)

	if sc.probe.Available() {
		t.Skip("a system ffprobe/ffmpeg is resolvable in this environment; availability gating cannot be exercised")
	}
	if sc.queueProbeBackfillIfNeeded("/media/movie.mkv", ".mkv", existingLocalMedia{}) {
		t.Fatal("no probe should be queued while ffprobe/ffmpeg is unavailable")
	}
}

// Formats that mediaExtensionSupportsProbe excludes must never enter the backfill
// path, even when the row looks unprobed.
func TestRescanSkipsProbeBackfillForUnprobeableExtensions(t *testing.T) {
	sc := probeBackfillScanner(t, true)

	for _, ext := range []string{".strm", ".iso"} {
		if sc.queueProbeBackfillIfNeeded("/media/item"+ext, ext, existingLocalMedia{}) {
			t.Fatalf("ext %s must not be queued for a probe backfill", ext)
		}
	}
}

// End-to-end guard: after a scan leaves a row without technical metadata, the
// incremental-skip path for an unchanged file must still schedule a backfill
// probe rather than skipping the file forever.
func TestScanLibraryQueuesBackfillOnUnchangedRescan(t *testing.T) {
	repos := newProbeBackfillRepo(t)
	root := t.TempDir()
	lib := model.Library{Name: "Movies", Path: root, Type: "movie", Enabled: true}
	if err := repos.Library.Create(t.Context(), &lib); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "Some Movie (2021).mkv")
	if err := os.WriteFile(file, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	first := newProbeBackfillScanner(t, repos, true)
	res, err := first.ScanLibrary(t.Context(), lib.ID)
	if err != nil {
		t.Fatalf("first scan: %v", err)
	}
	if res.Added != 1 {
		t.Fatalf("first scan added=%d, want 1", res.Added)
	}

	var existing existingLocalMedia
	if err := repos.DB.Model(&model.Media{}).
		Select("duration_sec", "width", "height", "video_codec", "audio_codec", "size_bytes").
		Where("path = ?", file).Scan(&existing).Error; err != nil {
		t.Fatal(err)
	}
	if !localMediaProbeDataMissing(existing) {
		t.Fatalf("fixture precondition failed, media already has probe data: %+v", existing)
	}

	// A fresh scanner instance has no probe in flight for this path, which is what
	// a later rescan (e.g. after the operator installs ffmpeg) looks like.
	second := newProbeBackfillScanner(t, repos, true)
	_, skipUnchanged := second.localMediaScanState(localMediaScanStateInput{
		ctx:           t.Context(),
		path:          file,
		cleanPath:     filepath.Clean(file),
		ext:           ".mkv",
		size:          existing.SizeBytes,
		existingMedia: map[string]existingLocalMedia{filepath.Clean(file): existing},
	})
	if !skipUnchanged {
		t.Fatal("unchanged file should take the incremental-skip path")
	}

	// Control: a different unprobed path on the same instance still queues, so the
	// instance is demonstrably able to schedule probes.
	other := filepath.Join(root, "Another Movie (2022).mkv")
	if !second.queueProbeBackfillIfNeeded(other, ".mkv", existing) {
		t.Fatal("control path should queue a backfill probe")
	}
	// Therefore the incremental-skip path above must already have queued this path:
	// queueing reserves the path, so a second attempt is refused. If the skip path
	// had not queued it, this call would have succeeded.
	if second.queueProbeBackfillIfNeeded(file, ".mkv", existing) {
		t.Fatal("unchanged unprobed media was not queued by the incremental-skip path")
	}
}
