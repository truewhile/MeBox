package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

const (
	RecognitionWordsEnabledKey    = "recognition_words.enabled"
	RecognitionWordsLocalTextKey  = "recognition_words.local_text"
	RecognitionWordsSharedURLsKey = "recognition_words.shared_urls"
	RecognitionWordsSharedTextKey = "recognition_words.shared_text"
	RecognitionWordsSyncedAtKey   = "recognition_words.synced_at"
)

var DefaultRecognitionWordURLs = []string{
	"https://raw.githubusercontent.com/Putarku/MoviePilot-Help/main/Words/general.txt",
	"https://raw.githubusercontent.com/Putarku/MoviePilot-Help/main/Words/TV.txt",
	"https://raw.githubusercontent.com/Putarku/MoviePilot-Help/main/Words/anime.txt",
}

type RecognitionWordsService struct {
	log    *zap.Logger
	repo   *repository.Container
	client *http.Client
}

type RecognitionWordsConfig struct {
	Enabled    bool     `json:"enabled"`
	LocalText  string   `json:"local_text"`
	SharedURLs []string `json:"shared_urls"`
	SharedText string   `json:"shared_text,omitempty"`
	SyncedAt   string   `json:"synced_at,omitempty"`
	RuleCount  int      `json:"rule_count"`
}

type RecognitionWordsTestResult struct {
	Input   string `json:"input"`
	Output  string `json:"output"`
	Title   string `json:"title"`
	Year    int    `json:"year"`
	Changed bool   `json:"changed"`
}

func NewRecognitionWordsService(log *zap.Logger, repo *repository.Container) *RecognitionWordsService {
	return &RecognitionWordsService{
		log:    log,
		repo:   repo,
		client: recognitionWordHTTPClient(),
	}
}

func (s *RecognitionWordsService) Config(ctx context.Context) RecognitionWordsConfig {
	cfg, rules := recognitionWordsRuntime(ctx, s.repo)
	cfg = cloneRecognitionWordsConfig(cfg)
	cfg.RuleCount = len(rules)
	return cfg
}

func (s *RecognitionWordsService) SaveConfig(ctx context.Context, cfg RecognitionWordsConfig) error {
	if s == nil || s.repo == nil || s.repo.Setting == nil {
		return fmt.Errorf("setting repository unavailable")
	}
	if err := s.repo.Setting.Set(ctx, RecognitionWordsEnabledKey, strconv.FormatBool(cfg.Enabled)); err != nil {
		return err
	}
	if err := s.repo.Setting.Set(ctx, RecognitionWordsLocalTextKey, cfg.LocalText); err != nil {
		return err
	}
	rawURLs, err := json.Marshal(normalizeRecognitionWordURLs(cfg.SharedURLs))
	if err != nil {
		return err
	}
	if err := s.repo.Setting.Set(ctx, RecognitionWordsSharedURLsKey, string(rawURLs)); err != nil {
		return err
	}
	invalidateRecognitionWordsRuntime(s.repo)
	return nil
}

func (s *RecognitionWordsService) SyncShared(ctx context.Context) (RecognitionWordsConfig, error) {
	cfg := s.Config(ctx)
	urls := cfg.SharedURLs
	if len(urls) == 0 {
		urls = DefaultRecognitionWordURLs
	}
	var combined []string
	for _, rawURL := range urls {
		text, err := s.fetchSharedWords(ctx, rawURL)
		if err != nil {
			return cfg, err
		}
		combined = append(combined, "# "+rawURL, text)
	}
	now := time.Now().Format(time.RFC3339)
	if err := s.repo.Setting.Set(ctx, RecognitionWordsSharedTextKey, strings.Join(combined, "\n")); err != nil {
		return cfg, err
	}
	if err := s.repo.Setting.Set(ctx, RecognitionWordsSyncedAtKey, now); err != nil {
		return cfg, err
	}
	invalidateRecognitionWordsRuntime(s.repo)
	return s.Config(ctx), nil
}

func (s *RecognitionWordsService) Test(ctx context.Context, input string) RecognitionWordsTestResult {
	output := ApplyRecognitionWords(ctx, s.repo, input)
	title, year := CleanQuery(output)
	return RecognitionWordsTestResult{
		Input:   input,
		Output:  output,
		Title:   title,
		Year:    year,
		Changed: strings.TrimSpace(input) != strings.TrimSpace(output),
	}
}

func ApplyRecognitionWords(ctx context.Context, repo *repository.Container, raw string) string {
	cfg, rules := recognitionWordsRuntime(ctx, repo)
	if !cfg.Enabled || len(rules) == 0 {
		return raw
	}
	return applyRecognitionWordRules(raw, rules)
}

func CleanQueryWithRecognition(ctx context.Context, repo *repository.Container, raw string) (string, int) {
	return CleanQuery(ApplyRecognitionWords(ctx, repo, raw))
}

func recognitionWordsConfig(ctx context.Context, repo *repository.Container) RecognitionWordsConfig {
	cfg := RecognitionWordsConfig{Enabled: true, SharedURLs: DefaultRecognitionWordURLs}
	if repo == nil || repo.DB == nil || repo.Setting == nil || !repo.DB.Migrator().HasTable(&model.Setting{}) {
		return cfg
	}
	if value, err := repo.Setting.Get(ctx, RecognitionWordsEnabledKey); err == nil && strings.TrimSpace(value) != "" {
		cfg.Enabled = parseBoolSetting(value, true)
	}
	if value, err := repo.Setting.Get(ctx, RecognitionWordsLocalTextKey); err == nil {
		cfg.LocalText = value
	}
	if value, err := repo.Setting.Get(ctx, RecognitionWordsSharedURLsKey); err == nil && strings.TrimSpace(value) != "" {
		cfg.SharedURLs = parseRecognitionWordURLs(value)
	}
	if value, err := repo.Setting.Get(ctx, RecognitionWordsSharedTextKey); err == nil {
		cfg.SharedText = value
	}
	if value, err := repo.Setting.Get(ctx, RecognitionWordsSyncedAtKey); err == nil {
		cfg.SyncedAt = value
	}
	return cfg
}

func recognitionWordsCombinedText(cfg RecognitionWordsConfig) string {
	return strings.TrimSpace(cfg.LocalText + "\n" + cfg.SharedText)
}

func parseRecognitionWordURLs(raw string) []string {
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err == nil {
		return normalizeRecognitionWordURLs(values)
	}
	return normalizeRecognitionWordURLs(strings.FieldsFunc(raw, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ',' || r == ';'
	}))
}

func normalizeRecognitionWordURLs(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

type recognitionWordRule struct {
	raw         string
	block       string
	blockRE     *regexp.Regexp
	replaceFrom string
	replaceTo   string
	replaceRE   *regexp.Regexp
	offsetLeft  string
	offsetRight string
	offsetExpr  string
	offsetRE    *regexp.Regexp
}

// recognitionWordsRuntime caches the parsed rule set (with pre-compiled
// regexes) per repository. Reading five settings rows and parsing/recompiling
// the whole shared word list used to happen on every query candidate, which is
// the scraper's hottest path. Writes through SaveConfig/SyncShared invalidate
// the entry immediately; the TTL only bounds staleness for out-of-process edits.
type recognitionWordsRuntimeEntry struct {
	repo      *repository.Container
	cfg       RecognitionWordsConfig
	rules     []recognitionWordRule
	expiresAt time.Time
}

const recognitionWordsRuntimeTTL = 30 * time.Second

var recognitionWordsRuntimeCache struct {
	sync.RWMutex
	entry recognitionWordsRuntimeEntry
	valid bool
}

func recognitionWordsRuntime(ctx context.Context, repo *repository.Container) (RecognitionWordsConfig, []recognitionWordRule) {
	now := time.Now()
	recognitionWordsRuntimeCache.RLock()
	cached := recognitionWordsRuntimeCache.entry
	hit := recognitionWordsRuntimeCache.valid && cached.repo == repo && now.Before(cached.expiresAt)
	recognitionWordsRuntimeCache.RUnlock()
	if hit {
		return cached.cfg, cached.rules
	}

	cfg := recognitionWordsConfig(ctx, repo)
	rules := parseRecognitionWordRules(recognitionWordsCombinedText(cfg))
	recognitionWordsRuntimeCache.Lock()
	recognitionWordsRuntimeCache.entry = recognitionWordsRuntimeEntry{
		repo:      repo,
		cfg:       cfg,
		rules:     rules,
		expiresAt: now.Add(recognitionWordsRuntimeTTL),
	}
	recognitionWordsRuntimeCache.valid = true
	recognitionWordsRuntimeCache.Unlock()
	return cfg, rules
}

func invalidateRecognitionWordsRuntime(repo *repository.Container) {
	recognitionWordsRuntimeCache.Lock()
	if recognitionWordsRuntimeCache.entry.repo == repo {
		recognitionWordsRuntimeCache.valid = false
	}
	recognitionWordsRuntimeCache.Unlock()
}

func cloneRecognitionWordsConfig(cfg RecognitionWordsConfig) RecognitionWordsConfig {
	cfg.SharedURLs = append([]string(nil), cfg.SharedURLs...)
	return cfg
}
