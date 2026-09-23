package model

import "time"

// 片段类型与 TheIntroDB 的返回字段一一对应。客户端按 kind 决定按钮文案
// （片头 / 回顾 / 片尾 / 预告），不依赖具体来源。
const (
	SegmentKindIntro   = "intro"
	SegmentKindRecap   = "recap"
	SegmentKindCredits = "credits"
	SegmentKindPreview = "preview"
)

// MediaSegment 是媒体源时间轴上一个可被跳过的区间（片头 / 回顾 / 片尾 / 预告）。
// 提供方（当前为 TheIntroDB）填充，播放器消费后向用户提供「跳过片头」。
type MediaSegment struct {
	Base
	MediaID  string `gorm:"index;size:128;not null;uniqueIndex:uniq_media_segment" json:"media_id"`
	SeriesID string `gorm:"index;size:128" json:"series_id,omitempty"`
	Kind     string `gorm:"size:16;not null;uniqueIndex:uniq_media_segment" json:"kind"`
	// StartMs/EndMs 是媒体源时间轴上的毫秒绝对值。EndMs 为 0 表示区间一直延续到
	// 片尾（TheIntroDB 对末段返回 end_ms: null），由客户端结合媒体总时长补齐。
	StartMs int64 `gorm:"not null;default:0;uniqueIndex:uniq_media_segment" json:"start_ms"`
	EndMs   int64 `gorm:"not null;default:0" json:"end_ms"`
	// Source 记录数据来源，让同一媒体上多来源共存、以及将来的人工覆盖成为可能。
	// 它必须参与唯一索引：否则「外部数据」与「人工修正」给出同一区间时会撞索引。
	Source string `gorm:"size:32;not null;default:'';uniqueIndex:uniq_media_segment" json:"source,omitempty"`
}

// MediaSegmentFetch 记录「某媒体的片段是否已向某来源查询过」。
// 单独建表是为了能缓存「查不到」这个结果：没有负缓存的话，每次播放一部社区库里
// 还没有数据的影片都会重新打一次外网。
type MediaSegmentFetch struct {
	Base
	MediaID   string    `gorm:"index;size:128;not null;uniqueIndex:uniq_media_segment_fetch" json:"media_id"`
	Source    string    `gorm:"size:32;not null;uniqueIndex:uniq_media_segment_fetch" json:"source"`
	FetchedAt time.Time `json:"fetched_at"`
	Found     bool      `json:"found"`
}
