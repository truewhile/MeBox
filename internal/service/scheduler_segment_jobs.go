package service

import (
	"context"
	"time"

	"go.uber.org/zap"
)

const (
	segmentPrewarmJobInterval = 6 * time.Hour
	// SegmentPrewarmEnabledKey 控制是否后台预热 TheIntroDB 片段；默认关闭。
	SegmentPrewarmEnabledKey = "segment.prewarm_enabled"
)

// SetSegments wires the IntroDB prewarm job. Without it the job is not registered.
func (s *SchedulerService) SetSegments(segments *MediaSegmentService) {
	if s != nil {
		s.segments = segments
	}
}

func (s *SchedulerService) jobSegmentPrewarm(ctx context.Context) error {
	if s == nil || s.segments == nil {
		return nil
	}
	manual, _ := ctx.Value(schedulerManualRunKey{}).(bool)
	if !manual && !s.segmentPrewarmEnabled(ctx) {
		return nil
	}
	n, err := s.segments.Prewarm(ctx, segmentPrewarmDefaultLimit)
	if s.log != nil {
		s.log.Info("segment prewarm finished",
			zap.Int("attempted", n),
			zap.Error(err),
		)
	}
	return err
}

// segmentPrewarmEnabled reports whether the operator opted into background
// IntroDB prewarm. Defaults to false so playback-on-demand remains the only path.
func (s *SchedulerService) segmentPrewarmEnabled(ctx context.Context) bool {
	if s.repo == nil || s.repo.Setting == nil {
		return false
	}
	v, err := s.repo.Setting.Get(ctx, SegmentPrewarmEnabledKey)
	if err != nil {
		return false
	}
	return parseBoolSetting(v, false)
}
