package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// AuthSession 是可撤销的设备认证会话。
//
// 会话与刷新令牌的关系是 1:N：一次登录创建一个会话，之后每次轮换刷新
// 令牌都继续归属同一会话。访问令牌通过 JWT 的 sid 字段引用会话，因此
// 服务端可以在 JWT 尚未过期时立即撤销某台设备的访问能力。
//
// 旧客户端没有会话关联（sid 为空），继续走既有兼容路径；本模型不强制
// 把历史刷新令牌绑定到任何设备。
type AuthSession struct {
	ID            string     `gorm:"primaryKey;size:36" json:"id"`
	UserID        string     `gorm:"index;size:36;not null" json:"user_id"`
	DeviceID      string     `gorm:"index:idx_auth_session_device,priority:1;size:128;not null" json:"device_id"`
	DeviceName    string     `gorm:"size:128" json:"device_name,omitempty"`
	ClientName    string     `gorm:"size:64" json:"client_name,omitempty"`
	ClientVersion string     `gorm:"size:64" json:"client_version,omitempty"`
	Revoked       bool       `gorm:"not null;default:false" json:"revoked"`
	RevokedAt     *time.Time `json:"revoked_at,omitempty"`
	LastUsedAt    time.Time  `json:"last_used_at"`
	ExpiresAt     time.Time  `gorm:"index" json:"expires_at"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// BeforeCreate 生成 UUID 主键。
func (s *AuthSession) BeforeCreate(_ *gorm.DB) error {
	if s.ID == "" {
		s.ID = uuid.NewString()
	}
	return nil
}

// IsExpired 判断会话是否超过最长有效期。
func (s *AuthSession) IsExpired() bool {
	return s != nil && !s.ExpiresAt.IsZero() && time.Now().After(s.ExpiresAt)
}

// IsValid 判断会话当前是否可继续使用。
func (s *AuthSession) IsValid() bool {
	return s != nil && !s.Revoked && !s.IsExpired()
}
