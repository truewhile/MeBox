// Package service — filesystem scanner.
//
// ScannerService walks the configured library roots looking for video
// files, then upserts a model.Media row per file. Each upsert also runs
// ffprobe (when available) and queues a metadata lookup for newly added
// rows.
//
// When a filename exposes season + episode numbers we store them on the
// Media row for every library type, so variety shows and other episodic
// collections get the same grouping experience as TV/anime.
package service

import (
	"errors"
	"strings"
	"sync"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/repository"
)

// videoExtensions lists the file extensions treated as media. Matches the
// legacy Python defaults.
var videoExtensions = map[string]struct{}{
	".mkv":  {},
	".mp4":  {},
	".m4v":  {},
	".avi":  {},
	".mov":  {},
	".webm": {},
	".flv":  {},
	".wmv":  {},
	".ts":   {},
	".m2ts": {},
	".mts":  {},
	".vob":  {},
	".rmvb": {},
	".rm":   {},
	".3gp":  {},
	".mpg":  {},
	".mpeg": {},
	".iso":  {},
	".strm": {},
}

func mediaExtensionSupportsProbe(ext string) bool {
	return ext != ".strm" && ext != ".iso"
}

// ScannerService walks libraries on disk and upserts model.Media rows.
type ScannerService struct {
	cfg       *config.Config
	log       *zap.Logger
	repo      *repository.Container
	hub       *Hub
	probe     *FFprobeService
	scraper   *ScraperService
	organizer *OrganizerService
	cache     *RuntimeCacheService

	imageProxy *ImageProxy

	localMediaProbeOnce  sync.Once
	localMediaProbeQueue chan localMediaProbeTask
	localMediaProbeMu    sync.Mutex
	localMediaProbing    map[string]struct{}
	localScanMu          sync.Mutex
	localScans           map[string]struct{}
}

// NewScannerService is the constructor.
func NewScannerService(
	cfg *config.Config,
	log *zap.Logger,
	repo *repository.Container,
	hub *Hub,
	probe *FFprobeService,
	scraper *ScraperService,
) *ScannerService {
	return &ScannerService{
		cfg: cfg, log: log, repo: repo, hub: hub,
		probe:                probe,
		scraper:              scraper,
		localMediaProbeQueue: make(chan localMediaProbeTask, 1024),
		localMediaProbing:    make(map[string]struct{}),
		localScans:           make(map[string]struct{}),
	}
}

func (s *ScannerService) SetOrganizer(organizer *OrganizerService) {
	if s != nil {
		s.organizer = organizer
	}
}

func (s *ScannerService) SetRuntimeCache(cache *RuntimeCacheService) {
	if s != nil {
		s.cache = cache
	}
}

func (s *ScannerService) SetImageProxy(imageProxy *ImageProxy) {
	s.imageProxy = imageProxy
}

// ScanResult summarises a scan run.
type ScanResult struct {
	LibraryID     string   `json:"library_id"`
	Visited       int      `json:"visited"`
	Added         int      `json:"added"`
	Updated       int      `json:"updated"`
	Skipped       int      `json:"skipped"`
	Probed        int      `json:"probed"`
	LocalMetadata int      `json:"local_metadata"`
	Removed       int64    `json:"removed"`
	ErrorCount    int      `json:"error_count,omitempty"`
	Errors        []string `json:"errors,omitempty"`
}

var ErrLocalScanAlreadyRunning = errors.New("local scan already running")

const maxScanErrorDetails = 20

func addScanError(res *ScanResult, path string, err error) {
	if res == nil || err == nil {
		return
	}
	res.ErrorCount++
	if len(res.Errors) >= maxScanErrorDetails {
		return
	}
	path = strings.TrimSpace(path)
	msg := strings.TrimSpace(err.Error())
	if path != "" {
		msg = path + ": " + msg
	}
	res.Errors = append(res.Errors, msg)
}

type localMediaProbeTask struct {
	path string
}

type existingLocalMedia struct {
	LibraryRootID string
	RelativePath  string
	Title         string
	OriginalName  string
	EpisodeTitle  string
	SizeBytes     int64
	DurationSec   int
	Width         int
	Height        int
	VideoCodec    string
	AudioCodec    string
	Container     string
	STRMURL       string
	FileID        string
	PosterURL     string
	BackdropURL   string
	Overview      string
	Year          int
	ReleaseDate   string
	Rating        float32
	TMDbID        int
	BangumiID     int
	DoubanID      string
	TheTVDBID     string
	SeasonNum     int
	EpisodeNum    int
	// EpisodeFraction 是集号的小数部分（S01E11.5 → 0.5）；变化同样要触发刷新，
	// 否则已有行上的「半集」标记不会被写回。
	EpisodeFraction float64
	Genres          string
	Countries       string
	Languages       string
	NSFW            bool
	ScrapeStatus    string
}
