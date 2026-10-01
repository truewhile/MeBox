package model

import "time"

// MediaProbe 是一次 ffprobe 全量探测（容器 / 轨道 / 内嵌章节）的结果缓存。
//
// 它存在的理由：探测一次要 3～4 秒——远端直链更慢，因为要跨洋跑三次 HTTP
// 事务。播放链路绝不能等它，所以第一次播放只起后台任务，结果落库后由后续请求
// 与「跳过片头」的章节数据直接读库。
//
// Payload 刻意只保存裁剪后的字段：ffprobe 原始输出里的 format.filename 是解析
// 后的播放直链（带网盘签名与 pickcode），原样落库等于把可直接下载的链接留在
// 数据库里，所以只保留与技术信息有关的字段。
type MediaProbe struct {
	Base
	MediaID string `gorm:"uniqueIndex;size:128;not null" json:"media_id"`
	// Signature 是「探的是哪个文件」的指纹（哈希）：本地文件取路径 + 大小 +
	// 修改时间，STRM / 云盘取固化的播放目标。文件换了就说明缓存不再对应当前
	// 内容，需要重探。
	Signature string `gorm:"size:64" json:"signature,omitempty"`
	// Source 记录输入形态：local | strm。
	Source string `gorm:"size:16" json:"source,omitempty"`

	Container       string `gorm:"size:64" json:"container,omitempty"`
	DurationSec     int    `json:"duration_sec"`
	BitRate         int64  `json:"bit_rate,omitempty"`
	Width           int    `json:"width,omitempty"`
	Height          int    `json:"height,omitempty"`
	VideoCodec      string `gorm:"size:64" json:"video_codec,omitempty"`
	AudioCodec      string `gorm:"size:64" json:"audio_codec,omitempty"`
	VideoStreams    int    `json:"video_streams"`
	AudioStreams    int    `json:"audio_streams"`
	SubtitleStreams int    `json:"subtitle_streams"`
	ChapterCount    int    `json:"chapter_count"`

	// Payload 是供详情页展示的裁剪后 JSON（容器 + 每路轨道 + 章节）。
	Payload string `gorm:"type:text" json:"payload,omitempty"`
	// ProbedAt 是最近一次探测的时刻。LastError 非空表示这次探测失败；失败只更新
	// 这两个字段，不会覆盖此前成功的 Payload 与已经落库的章节片段。
	ProbedAt  time.Time `json:"probed_at"`
	LastError string    `gorm:"size:512" json:"last_error,omitempty"`
}
