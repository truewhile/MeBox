// Package service — 片头/片尾片段的外部提供方抽象。
//
// 目前有三个社区库可用，它们对「媒体身份」的要求各不相同：
//
//	TheIntroDB  TMDb id（服务端自己映射到 TVDB）
//	IntroDB     只有 IMDb id（用 is_movie 区分电影/剧集）
//	TODb        TMDb id + 平台类型（tmdb_id_tv / tmdb_id_movie），秒级时间轴
//
// 播放链路只关心「一段可跳过的区间」，所以这里把三者收敛成同一个
// SegmentProvider 接口和 SegmentQuery 入参，上层按设置项挑选其一。
package service

import "context"

// SegmentSpan 是一个已解析的可跳过区间，单位为毫秒。
//
// EndMs == 0 是哨兵值，表示「一直到片尾」：只有片尾（credits/preview）可能
// 出现这种区间，由客户端结合媒体总时长补齐。片头/回顾不允许开放区间，
// 拿不到结束点就整段丢弃——否则会得到一个覆盖全片的区间。
type SegmentSpan struct {
	Kind    string
	StartMs int64
	EndMs   int64
}

// IntroDBSpan 保留旧名字，避免既有调用方和测试大范围改名。
type IntroDBSpan = SegmentSpan

// SegmentQuery 是一次片段查询的身份参数。不同提供方按需取用其中一部分。
type SegmentQuery struct {
	TMDbID  int
	IMDbID  string
	Season  int
	Episode int
	// IsMovie 为 true 时 Season/Episode 无意义。
	IsMovie bool
}

// SegmentProvider 是一个外部片段数据源。
type SegmentProvider interface {
	// Source 返回写入 media_segments.source 的标识，也是缓存账本的键。
	Source() string
	// FetchSegments 返回该媒体已知的可跳过区间。查不到不是错误（返回空切片），
	// 只有请求本身失败才返回 error——调用方据此决定是否保留旧缓存。
	FetchSegments(ctx context.Context, q SegmentQuery) ([]SegmentSpan, error)
}
