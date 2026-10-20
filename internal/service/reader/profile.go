package reader

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/truewhile/MeBox/internal/model"
)

// 阅读器偏好（每个用户一条，见 model.ReaderProfile）。
//
// 这些值原本只存在浏览器 localStorage，属于设备级；现在按用户落库，让主题、
// 排版、听书、书架展示设置能跨设备一致。服务端只做「存储 + 收敛」，
// 具体语义（哪些主题、怎么排版）仍由前端决定，所以这里只校验取值范围与枚举，
// 不解释它们。

// 取值范围与前端 readerSettings store 里的 clamp 完全一致，两边改动需同步。
const (
	readerFontSizeMin, readerFontSizeMax       = 14, 32
	readerLineHeightMin, readerLineHeightMax   = 1.4, 2.6
	readerParaSpacingMin, readerParaSpacingMax = 0, 32
	readerAudioSpeedMin, readerAudioSpeedMax   = 0.5, 3
	readerAudioTimerMin, readerAudioTimerMax   = 0, 180
	readerGridColumnsMin, readerGridColumnsMax = 0, 6

	// readerThemeIDDefault 主题标识的自愈值：themeId 不枚举校验（前端可以自由
	// 新增主题），只限制长度，空值时回落到内置的「预设1」。
	readerThemeIDDefault = "preset1"
	readerThemeIDMaxLen  = 32
)

// 前端定义的枚举取值；未知值一律回落成默认项，避免脏值把阅读器置于不可用状态。
var (
	readerPageModes   = []string{"page", "scroll"}
	readerShelfLayout = []string{"grid", "list", "compact"}
	readerShelfSorts  = []string{"recent", "update", "mixed", "name", "author", "manual"}
)

// ReaderSettings 阅读器偏好的读写载荷（与前端 ReaderSettingsProfile 字段一一对应）。
type ReaderSettings struct {
	ThemeID          string  `json:"theme_id"`
	Night            bool    `json:"night"`
	PageMode         string  `json:"page_mode"`
	FontSize         int     `json:"font_size"`
	LineHeight       float64 `json:"line_height"`
	ParagraphSpacing int     `json:"paragraph_spacing"`

	AudioSpeed        float64 `json:"audio_speed"`
	AudioTimerMinutes int     `json:"audio_timer_minutes"`

	ShelfLayout         string `json:"shelf_layout"`
	ShelfGridColumns    int    `json:"shelf_grid_columns"`
	ShelfSort           string `json:"shelf_sort"`
	ShelfShowUnread     bool   `json:"shelf_show_unread"`
	ShelfShowUpdateTime bool   `json:"shelf_show_update_time"`
}

// GetReaderSettings 读用户阅读器偏好。
// 该用户还没保存过时返回 (nil, nil)：由前端用它本地的值播种，避免这里再维护一份默认值。
func (s *ReaderService) GetReaderSettings(ctx context.Context, userID string) (*ReaderSettings, error) {
	if userID == "" {
		return nil, nil
	}
	row, err := s.repo.GetReaderProfile(ctx, userID)
	if err != nil || row == nil {
		return nil, err
	}
	out := settingsFromProfile(*row)
	return &out, nil
}

// SaveReaderSettings 覆盖保存用户阅读器偏好，落库前先做范围收敛。
func (s *ReaderService) SaveReaderSettings(ctx context.Context, userID string, in ReaderSettings) (*ReaderSettings, error) {
	if userID == "" {
		return nil, fmt.Errorf("缺少用户信息")
	}
	sanitized := sanitizeReaderSettings(in)
	row := &model.ReaderProfile{UserID: userID}
	applySettingsToProfile(row, sanitized)
	if err := s.repo.SaveReaderProfile(ctx, row); err != nil {
		return nil, err
	}
	out := settingsFromProfile(*row)
	return &out, nil
}

// sanitizeReaderSettings 把入参收敛到合法范围：数值夹到区间、枚举未知即回落默认、
// 主题标识去空白并限长。
func sanitizeReaderSettings(in ReaderSettings) ReaderSettings {
	return ReaderSettings{
		ThemeID:          sanitizeThemeID(in.ThemeID),
		Night:            in.Night,
		PageMode:         oneOf(in.PageMode, readerPageModes, readerPageModes[0]),
		FontSize:         clampInt(in.FontSize, readerFontSizeMin, readerFontSizeMax),
		LineHeight:       roundTo1(clampFloat(in.LineHeight, readerLineHeightMin, readerLineHeightMax)),
		ParagraphSpacing: clampInt(in.ParagraphSpacing, readerParaSpacingMin, readerParaSpacingMax),

		AudioSpeed:        roundTo1(clampFloat(in.AudioSpeed, readerAudioSpeedMin, readerAudioSpeedMax)),
		AudioTimerMinutes: clampInt(in.AudioTimerMinutes, readerAudioTimerMin, readerAudioTimerMax),

		ShelfLayout:         oneOf(in.ShelfLayout, readerShelfLayout, readerShelfLayout[0]),
		ShelfGridColumns:    clampInt(in.ShelfGridColumns, readerGridColumnsMin, readerGridColumnsMax),
		ShelfSort:           oneOf(in.ShelfSort, readerShelfSorts, readerShelfSorts[0]),
		ShelfShowUnread:     in.ShelfShowUnread,
		ShelfShowUpdateTime: in.ShelfShowUpdateTime,
	}
}

func sanitizeThemeID(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || len(v) > readerThemeIDMaxLen {
		return readerThemeIDDefault
	}
	return v
}

func settingsFromProfile(p model.ReaderProfile) ReaderSettings {
	return ReaderSettings{
		ThemeID:          p.ThemeID,
		Night:            p.Night,
		PageMode:         p.PageMode,
		FontSize:         p.FontSize,
		LineHeight:       p.LineHeight,
		ParagraphSpacing: p.ParagraphSpacing,

		AudioSpeed:        p.AudioSpeed,
		AudioTimerMinutes: p.AudioTimerMinutes,

		ShelfLayout:         p.ShelfLayout,
		ShelfGridColumns:    p.ShelfGridColumns,
		ShelfSort:           p.ShelfSort,
		ShelfShowUnread:     p.ShelfShowUnread,
		ShelfShowUpdateTime: p.ShelfShowUpdateTime,
	}
}

func applySettingsToProfile(p *model.ReaderProfile, in ReaderSettings) {
	p.ThemeID = in.ThemeID
	p.Night = in.Night
	p.PageMode = in.PageMode
	p.FontSize = in.FontSize
	p.LineHeight = in.LineHeight
	p.ParagraphSpacing = in.ParagraphSpacing

	p.AudioSpeed = in.AudioSpeed
	p.AudioTimerMinutes = in.AudioTimerMinutes

	p.ShelfLayout = in.ShelfLayout
	p.ShelfGridColumns = in.ShelfGridColumns
	p.ShelfSort = in.ShelfSort
	p.ShelfShowUnread = in.ShelfShowUnread
	p.ShelfShowUpdateTime = in.ShelfShowUpdateTime
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampFloat(v, lo, hi float64) float64 {
	// JSON 不会给出 NaN，这里是防御性的（例如将来接了别的调用方）
	if math.IsNaN(v) || v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func roundTo1(v float64) float64 {
	return math.Round(v*10) / 10
}

// oneOf 返回 v（当它属于 allowed 时），否则返回 fallback。
func oneOf(v string, allowed []string, fallback string) string {
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	return fallback
}
