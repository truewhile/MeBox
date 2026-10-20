// Package model — 阅读（legado 书源兼容）子系统数据模型。
// 字段语义对齐 legado 的 BookSource / Book / BookChapter / ReplaceRule 实体；
// 阅读进度沿用 legado 的做法直接挂在书籍上（durChapter* 字段）。
package model

import (
	"time"
)

// ReaderBookSource 书源：原始 JSON 全量存储 + 常用字段冗余列出便于筛选排序。
//
// 书源按用户独立：同一份书源不同用户可以各存一份（各自启停、排序、修改规则与变量），
// 互不可见。唯一键是 (user_id, source_url)；跨用户的「同一本书」靠
// ReaderBook.Origin（书源 URL）而不是书源行 ID 关联。
type ReaderBookSource struct {
	Base
	// UserID 归属用户。历史数据迁移时会回填；值为空表示「旧版全局书源」，
	// 只在没有任何用户引用时才会出现。
	UserID         string     `gorm:"type:varchar(36);uniqueIndex:uniq_reader_source_user_url;index" json:"user_id"`
	Name           string     `gorm:"type:varchar(255);index" json:"name"`
	GroupName      string     `gorm:"type:varchar(255);index" json:"group"`
	Type           int        `gorm:"default:0" json:"type"` // 0文本 1音频 2图片 3文件 4视频
	SourceURL      string     `gorm:"type:varchar(512);uniqueIndex:uniq_reader_source_user_url;index" json:"source_url"`
	RawJSON        string     `gorm:"type:text" json:"-"`
	Enabled        bool       `gorm:"default:true" json:"enabled"`
	EnabledExplore bool       `gorm:"default:true" json:"enabled_explore"`
	CustomOrder    int        `json:"custom_order"`
	Weight         int        `json:"weight"`
	ConcurrentRate string     `gorm:"type:varchar(64)" json:"concurrent_rate"`
	Header         string     `gorm:"type:text" json:"header"` // 书源级请求头 JSON
	Comment        string     `gorm:"type:text" json:"comment"`
	Variables      string     `gorm:"type:text" json:"variables"` // source 变量 JSON
	LastUpdateTime int64      `json:"last_update_time"`
	LastCheckAt    *time.Time `json:"last_check_at"`
	RespondTime    int64      `json:"respond_time"` // 最近一次调试响应耗时（ms）
	// HasLogin 是否声明了登录能力（loginUrl/loginUi），列表接口按需计算，不落库。
	HasLogin bool `gorm:"-" json:"has_login"`
	// NeedsBrowser 是否依赖 WebView/无头浏览器（webView 选项或 webjs 规则）。
	// 服务端没有浏览器，这类源注定不可用，列表接口按需计算让前端能提前提示。
	NeedsBrowser bool `gorm:"-" json:"needs_browser"`
}

// ReaderSourceState 书源会话状态：对应 legado 中按书源 key 存储的
// sourceVariable / userInfo（登录信息）/ loginHeader 与 CookieStore。
// 与书源分表存放，避免每次导入更新书源时把用户登录态覆盖掉。
type ReaderSourceState struct {
	Base
	SourceURL   string `gorm:"type:varchar(512);uniqueIndex" json:"source_url"`
	Variable    string `gorm:"type:text" json:"variable"`     // source.getVariable/setVariable
	LoginInfo   string `gorm:"type:text" json:"login_info"`   // source.getLoginInfo/putLoginInfo（登录表单 JSON）
	LoginHeader string `gorm:"type:text" json:"login_header"` // source 登录请求头 JSON
	Cookies     string `gorm:"type:text" json:"cookies"`      // JSON: domain → "k=v; k=v"
}

// ReaderBook 书架条目（含阅读进度，对应 legado Book）。
type ReaderBook struct {
	Base
	UserID             string `gorm:"type:varchar(36);index" json:"user_id"`
	Origin             string `gorm:"type:varchar(512)" json:"origin"` // 书源 URL
	OriginName         string `gorm:"type:varchar(255)" json:"origin_name"`
	BookURL            string `gorm:"type:varchar(512);index" json:"book_url"`
	TocURL             string `gorm:"type:varchar(512)" json:"toc_url"`
	Name               string `gorm:"type:varchar(255)" json:"name"`
	Author             string `gorm:"type:varchar(255)" json:"author"`
	Kind               string `gorm:"type:varchar(255)" json:"kind"`
	CoverURL           string `gorm:"type:varchar(512)" json:"cover_url"`
	Intro              string `gorm:"type:text" json:"intro"`
	Charset            string `gorm:"type:varchar(32)" json:"charset"`
	Type               int    `gorm:"default:0" json:"type"` // 0文本 1音频 2图片
	LatestChapterTitle string `gorm:"type:varchar(512)" json:"latest_chapter_title"`
	// LatestChapterTime 最后一次「目录末尾章节发生变化」的时间（毫秒）。
	// 对应 legado Book.latestChapterTime：书架「最近更新」排序与「更新时间」展示都读它。
	// 抓目录时发现末章标题与已存值不同才刷新；本地导入书籍不参与。
	LatestChapterTime int64  `json:"latest_chapter_time"`
	TotalChapterNum   int    `json:"total_chapter_num"`
	DurChapterIndex   int    `json:"dur_chapter_index"`
	DurChapterPos     int    `json:"dur_chapter_pos"`
	DurChapterTitle   string `gorm:"type:varchar(512)" json:"dur_chapter_title"`
	DurChapterTime    int64  `json:"dur_chapter_time"`
	Order             int    `json:"order"`
	Variable          string `gorm:"type:text" json:"variable"`
	// LocalPath 本地导入书籍的位置：默认是 data/reader/local 下的文件名
	// （如 "<id>.txt"）；LocalExternal 为真时是服务器上的绝对路径。
	// 为空表示来自网络书源。不对外暴露路径，前端用 is_local 判断。
	LocalPath string `gorm:"type:varchar(255)" json:"-"`
	// LocalExternal 为真表示原地引用服务器上已有的文件/目录（管理员在导入时选定），
	// 移出书架只解除引用，不删除源文件；为假表示 data/reader/local 下的托管副本。
	LocalExternal bool `json:"local_external"`
	// IsLocal 是否本地导入书籍，列表接口按需计算，不落库。
	IsLocal bool `gorm:"-" json:"is_local"`
	// 听书（音频源）跳过片头/片尾秒数，对应 legado Book.getOpenCredits/getCloseCredits。
	// 0 表示不跳过。仅对音频和视频源生效。
	OpenCredits  int `gorm:"default:0" json:"open_credits"`
	CloseCredits int `gorm:"default:0" json:"close_credits"`
}

// ReaderChapter 章节缓存（对应 legado BookChapter）。
type ReaderChapter struct {
	Base
	BookID   string `gorm:"type:varchar(36);uniqueIndex:idx_reader_book_chapter" json:"book_id"`
	Index    int    `gorm:"uniqueIndex:idx_reader_book_chapter" json:"index"`
	URL      string `gorm:"type:varchar(512)" json:"url"`
	Title    string `gorm:"type:varchar(512)" json:"title"`
	IsVolume bool   `json:"is_volume"`
	Tag      string `gorm:"type:varchar(255)" json:"tag"`
}

// ReaderContentCache 正文持久缓存的索引行（内容本体在磁盘上，见 reader_content_cache.go）。
//
// 与 legado BookHelp 的章节正文缓存对应：缓存的是「书源侧产物」（书源 replaceRegex
// 之后、用户替换规则与代理改写之前），因此可以跨用户共享；用户维度的处理在读出后
// 逐请求应用，规则改动即时生效。
//
// 章节身份不落库为外键，而是 BookKey（书源 + 书本地址）与 ChapterKey（绝对化章节
// 地址或标题）的哈希：目录刷新（ReplaceChapters 物理重建、行 ID 会变）与书源更新
// 之后仍然能按同一身份命中或迁移。
type ReaderContentCache struct {
	Base
	// OriginHash 书源地址哈希（磁盘目录的第一层，清理时定位文件用）。
	OriginHash string `gorm:"type:varchar(64)" json:"origin_hash"`
	// BookKey 书源身份哈希（sha256(origin + "\0" + bookURL) 前 16 字节 hex）。
	BookKey string `gorm:"type:varchar(64);index:idx_reader_content_book" json:"book_key"`
	// ChapterKey 章节身份哈希（绝对化 URL 优先，退化为 title）。
	ChapterKey string `gorm:"type:varchar(64);index:idx_reader_content_chapter" json:"chapter_key"`
	// ChapterIdentity 章节身份原文（便于诊断与 remap 时的标题兜底匹配）。
	ChapterIdentity string `gorm:"type:varchar(512)" json:"chapter_identity"`
	// ChapterIndex 保存时的章节序号（remap 时更新）。
	ChapterIndex int `json:"chapter_index"`
	// ContentType text / audio / image。
	ContentType string `gorm:"type:varchar(16)" json:"content_type"`
	// SourceHash 书源内容指纹（RawJSON 哈希）：书源更新后自然失效。
	SourceHash string `gorm:"type:varchar(64)" json:"source_hash"`
	// FormatVersion 缓存载荷格式版本：解析管线语义变化时递增，旧条目自然失效。
	FormatVersion int `json:"format_version"`
	// SizeBytes 载荷字节数（容量统计用）。
	SizeBytes int64 `json:"size_bytes"`
	// AssetCount 音频轨/图片张数（清单类内容的完整性统计）。
	AssetCount int `json:"asset_count"`
	// ExpiresAt 过期时间（unix 秒）；0 表示不过期。
	ExpiresAt int64 `json:"expires_at"`
	// LastAccessAt 最近命中时间（unix 秒），LRU 淘汰依据。
	LastAccessAt int64 `json:"last_access_at"`
	// Hits 命中次数（诊断用）。
	Hits int `json:"hits"`
}

// ReaderReplaceRule 替换净化规则（对应 legado ReplaceRule）。
type ReaderReplaceRule struct {
	Base
	UserID             string `gorm:"type:varchar(36);index" json:"user_id"`
	Name               string `gorm:"type:varchar(255)" json:"name"`
	GroupName          string `gorm:"type:varchar(255);index" json:"group"`
	Pattern            string `gorm:"type:text" json:"pattern"`
	Replacement        string `gorm:"type:text" json:"replacement"`
	Scope              string `gorm:"type:varchar(255)" json:"scope"`
	ScopeTitle         bool   `json:"scope_title"`
	ScopeContent       bool   `gorm:"default:true" json:"scope_content"`
	ExcludeScope       string `gorm:"type:varchar(255)" json:"exclude_scope"`
	IsEnabled          bool   `gorm:"default:true" json:"is_enabled"`
	IsRegex            bool   `gorm:"default:true" json:"is_regex"`
	TimeoutMillisecond int64  `gorm:"default:3000" json:"timeout_millisecond"`
	Order              int    `json:"order"`
}

// ReaderProfile 阅读器偏好（每个用户一条）。
//
// 主题 / 排版 / 听书 / 书架展示这些设置原先只存在浏览器 localStorage，属于设备级：
// 换设备或换浏览器就丢。这里按用户落库作为权威来源，前端 localStorage 退化成首屏缓存。
// 首页的「影视 / 阅读」模式（homeMode）是设备级偏好，故意不在这里同步。
type ReaderProfile struct {
	Base
	UserID string `gorm:"type:varchar(36);uniqueIndex" json:"user_id"`

	ThemeID          string  `gorm:"type:varchar(32)" json:"theme_id"`
	Night            bool    `json:"night"`
	PageMode         string  `gorm:"type:varchar(16)" json:"page_mode"` // page 翻页 / scroll 滚动
	FontSize         int     `json:"font_size"`
	LineHeight       float64 `json:"line_height"`
	ParagraphSpacing int     `json:"paragraph_spacing"`

	// 听书偏好（对应 legado AudioPlay.playSpeed 与 AppConfig.ttsTimer）
	AudioSpeed        float64 `json:"audio_speed"`
	AudioTimerMinutes int     `json:"audio_timer_minutes"`

	// 书架展示偏好（对应 legado 的书架设置）
	ShelfLayout         string `gorm:"type:varchar(16)" json:"shelf_layout"` // grid / list / compact
	ShelfGridColumns    int    `json:"shelf_grid_columns"`                   // 0 表示自适应
	ShelfSort           string `gorm:"type:varchar(16)" json:"shelf_sort"`
	ShelfShowUnread     bool   `json:"shelf_show_unread"`
	ShelfShowUpdateTime bool   `json:"shelf_show_update_time"`
}
