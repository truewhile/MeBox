// Package service — 服务器身份与移动端能力声明。
package service

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/truewhile/MeBox/internal/repository"
)

// 服务器身份与移动端契约常量。
const (
	// ServerIDSettingKey 是持久化实例身份的设置键。普通重启、升级和同实例
	// 数据恢复都沿用同一个值；把备份克隆成独立服务时需要显式重置。
	ServerIDSettingKey = "mobile.server_id"

	// MobileAPIVersion 是 /api/v1 契约版本。
	MobileAPIVersion = "v1"

	// MinSupportedAppVersionCode 是首个移动端契约要求的最低 App versionCode。
	MinSupportedAppVersionCode = 1

	// MobileServerName 是 server-info 暴露的实例名称。
	MobileServerName = "MeBox"
)

// ServerCapabilities 是 server-info 的能力开关。
//
// 每项都由「接口已实现且当前可运行」共同决定；仅注册了 handler 但功能
// 不可用时必须返回 false，避免 App 依据虚假能力进入不可用流程。
type ServerCapabilities struct {
	DeviceSessions     bool `json:"device_sessions"`
	PlaybackSessions   bool `json:"playback_sessions"`
	ReaderProgressV1   bool `json:"reader_progress_v1"`
	ReaderContentBatch bool `json:"reader_content_batch"`
	LocalTranscode     bool `json:"local_transcode"`
}

// ServerInfo 是 GET /api/v1/server-info 的响应。
type ServerInfo struct {
	Name                       string             `json:"name"`
	ServerID                   string             `json:"server_id"`
	ServerVersion              string             `json:"server_version"`
	APIVersion                 string             `json:"api_version"`
	MinSupportedAppVersionCode int                `json:"min_supported_app_version_code"`
	Capabilities               ServerCapabilities `json:"capabilities"`
}

// ServerIdentityService 负责生成并持久化实例身份。
type ServerIdentityService struct {
	repo    *repository.Container
	version string

	mu     sync.Mutex
	cached string
}

// NewServerIdentityService 创建实例身份服务。
func NewServerIdentityService(repo *repository.Container, version string) *ServerIdentityService {
	return &ServerIdentityService{repo: repo, version: strings.TrimSpace(version)}
}

// ServerID 返回持久化的实例 ID，首次调用时生成并写入设置表。
func (s *ServerIdentityService) ServerID(ctx context.Context) (string, error) {
	if s == nil || s.repo == nil || s.repo.Setting == nil {
		return "", errors.New("server identity unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached != "" {
		return s.cached, nil
	}
	stored, err := s.repo.Setting.Get(ctx, ServerIDSettingKey)
	if err != nil {
		return "", err
	}
	stored = strings.TrimSpace(stored)
	if stored != "" {
		s.cached = stored
		return stored, nil
	}
	generated := uuid.NewString()
	if err := s.repo.Setting.Set(ctx, ServerIDSettingKey, generated); err != nil {
		return "", err
	}
	s.cached = generated
	return generated, nil
}

// Info 组装 server-info 响应。capabilities 由调用方按真实可用服务推导。
func (s *ServerIdentityService) Info(ctx context.Context, capabilities ServerCapabilities) (ServerInfo, error) {
	serverID, err := s.ServerID(ctx)
	if err != nil {
		return ServerInfo{}, err
	}
	version := strings.TrimSpace(s.version)
	if version == "" {
		version = "dev"
	}
	return ServerInfo{
		Name:                       MobileServerName,
		ServerID:                   serverID,
		ServerVersion:              version,
		APIVersion:                 MobileAPIVersion,
		MinSupportedAppVersionCode: MinSupportedAppVersionCode,
		Capabilities:               capabilities,
	}, nil
}

// CurrentServerCapabilities 依据服务容器推导移动端能力。
//
// M1 只启用了设备会话；播放会话、阅读 v1 进度与批量正文属于后续阶段，
// 在对应任务真正实现并通过契约测试前保持 false。
func CurrentServerCapabilities(c *Container) ServerCapabilities {
	return ServerCapabilities{
		DeviceSessions:     c != nil && c.AuthSessions != nil,
		PlaybackSessions:   false,
		ReaderProgressV1:   false,
		ReaderContentBatch: false,
		LocalTranscode:     false,
	}
}
