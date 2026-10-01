package service

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/helper"
)

func (s *ScannerService) invalidateMediaCache(ctx context.Context) {
	if s != nil && s.cache != nil {
		s.cache.DeletePrefix(ctx, "media:")
		s.cache.DeletePrefix(ctx, "stats:")
	}
}

func (s *ScannerService) startAutoScrape(ctx context.Context, libraryID string) {
	scrapeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Minute)
	go func() {
		defer cancel()
		// 扫描触发的后台刮削与请求线程无关，panic 只记日志，不能带崩进程。
		helper.Run(s.log, "scanner.autoScrape", func() {
			_, err := s.scraper.EnrichLibraryDetailedWithOptions(scrapeCtx, libraryID, skipEpisodeArtworkOptions(false))
			if err != nil {
				s.log.Warn("scraper enrich failed", zap.Error(err))
			}
		})
	}()
}
