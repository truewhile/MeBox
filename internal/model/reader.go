// Package model — 阅读（legado 书源兼容）子系统数据模型。
// 字段语义对齐 legado 的 BookSource / Book / BookChapter / ReplaceRule 实体；
// 阅读进度沿用 legado 的做法直接挂在书籍上（durChapter* 字段）。
package model

import (
	"time"
)

// ReaderBookSource 书源：原始 JSON 全量存储 + 常用字段冗余列出便于筛选排序。
type ReaderBookSource struct {
	Base
	Name           string     `gorm:"type:varchar(255);index" json:"name"`
	GroupName      string     `gorm:"type:varchar(255);index" json:"group"`
	Type           int        `gorm:"default:0" json:"type"` // 0文本 1音频 2图片 3文件 4视频
	SourceURL      string     `gorm:"type:varchar(512);index" json:"source_url"`
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
