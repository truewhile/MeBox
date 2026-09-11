package model

import (
	"encoding/json"
	"strings"
	"time"
)

// User 是本地账户。第一个注册的管理员（或种子管理员）获得 "admin" 角色；
// 其他所有用户默认为 "user"。
type User struct {
	Base
	Username           string     `gorm:"uniqueIndex;size:64;not null" json:"username"`
	PasswordHash       string     `gorm:"size:128;not null" json:"-"`
	Role               string     `gorm:"size:16;not null;default:user" json:"role"`
	Tier               string     `gorm:"size:16;default:free" json:"tier"` // free / plus
	Nickname           string     `gorm:"size:128" json:"nickname,omitempty"`
	Email              string     `gorm:"size:128" json:"email,omitempty"`
	AvatarURL          string     `gorm:"size:255" json:"avatar_url,omitempty"`
	HideAdult          bool       `gorm:"default:true" json:"hide_adult"`
	ForcePasswordReset bool       `gorm:"default:false" json:"force_password_reset"`
	IsActive           bool       `gorm:"default:true" json:"is_active"`
	LastLoginAt        *time.Time `json:"last_login_at,omitempty"`
	// AllowedLibraryIDs 存储管理员为该用户指定的受限可访问媒体库 ID 列表（JSON 字符串）。
	// 为空时代表不限制（全库可访问）。
	AllowedLibraryIDs  string   `gorm:"type:text" json:"-"`
	AllowedLibraryList []string `gorm:"-" json:"allowed_library_ids,omitempty"`
	// PinnedLibraryIDs 存储用户置顶的媒体库 ID 列表（JSON 字符串），顺序即置顶优先级。
	PinnedLibraryIDs  string   `gorm:"type:text" json:"-"`
	PinnedLibraryList []string `gorm:"-" json:"pinned_library_ids,omitempty"`
	// SubtitleChineseMode 是网页播放器外挂字幕的简繁转换偏好：
	// original / simplified / traditional。
	SubtitleChineseMode string `gorm:"size:16;not null;default:original" json:"subtitle_chinese_mode"`
	// DanmakuMergeSources 是网页播放器的弹幕偏好：开启后，同一集的多个弹幕
	// 来源会被合并并按「时间 + 内容」去重后一起展示。按用户存储，避免一个
	// 用户的开关影响其他人。
	DanmakuMergeSources bool `gorm:"default:false" json:"danmaku_merge_sources"`
	// ExpiredAt is the account expiry time. Nil means the account never
	// expires. When set and in the past, the account is treated as expired
	// (login blocked) until an admin or a redemption code renews it.
	ExpiredAt *time.Time `json:"expired_at,omitempty"`
	// ShareWarnings counts anti-account-sharing warnings, mainly device
	// fingerprint mismatches. Once it exceeds the configured threshold a
	// re-offence disables the account until an admin re-enables it.
	ShareWarnings       int        `gorm:"default:0" json:"share_warnings"`
	LastShareWarnAt     *time.Time `json:"last_share_warn_at,omitempty"`
	IsDefaultAdmin      bool       `gorm:"-" json:"is_default_admin,omitempty"`
	IsProtected         bool       `gorm:"-" json:"is_protected,omitempty"`
	RealtimeOnline      bool       `gorm:"-" json:"realtime_online,omitempty"`
	RealtimeDeviceCount int        `gorm:"-" json:"realtime_device_count,omitempty"`
}

// DecodeAllowedLibraryIDs 解析 AllowedLibraryIDs 字段。
func (u *User) DecodeAllowedLibraryIDs() []string {
	if u == nil || strings.TrimSpace(u.AllowedLibraryIDs) == "" {
		return nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(u.AllowedLibraryIDs), &ids); err != nil {
		return nil
	}
	var out []string
	for _, id := range ids {
		trimmed := strings.TrimSpace(id)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// DecodePinnedLibraryIDs 解析 PinnedLibraryIDs 字段。
func (u *User) DecodePinnedLibraryIDs() []string {
	if u == nil || strings.TrimSpace(u.PinnedLibraryIDs) == "" {
		return nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(u.PinnedLibraryIDs), &ids); err != nil {
		return nil
	}
	var out []string
	for _, id := range ids {
		trimmed := strings.TrimSpace(id)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// PopulateComputedFields 填充非 DB 虚拟计算字段（如 AllowedLibraryList）。
func (u *User) PopulateComputedFields() {
	if u == nil {
		return
	}
	u.AllowedLibraryList = u.DecodeAllowedLibraryIDs()
	u.PinnedLibraryList = u.DecodePinnedLibraryIDs()
}
