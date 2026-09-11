// Package service — user profile management.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

// ProfileService handles non-credential user mutations.
type ProfileService struct {
	log  *zap.Logger
	repo *repository.Container
}

// NewProfileService is the constructor.
func NewProfileService(log *zap.Logger, repo *repository.Container) *ProfileService {
	return &ProfileService{log: log, repo: repo}
}

// ProfileUpdate is the patch object accepted by UpdateProfile. Empty
// fields are ignored so the same payload can be reused across screens.
type ProfileUpdate struct {
	Username            *string `json:"username,omitempty"`
	Nickname            *string `json:"nickname,omitempty"`
	Email               *string `json:"email,omitempty"`
	AvatarURL           *string `json:"avatar_url,omitempty"`
	HideAdult           *bool   `json:"hide_adult,omitempty"`
	SubtitleChineseMode *string `json:"subtitle_chinese_mode,omitempty"`
	Password            string  `json:"password,omitempty"`
}

// UpdateProfile applies a non-credential patch to the user.
func (p *ProfileService) UpdateProfile(ctx context.Context, userID string, patch ProfileUpdate) (*model.User, error) {
	if userID == "" {
		return nil, errors.New("missing user id")
	}
	current, err := p.repo.User.FindByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, errors.New("user not found")
	}
	updates := map[string]any{}
	if patch.Username != nil {
		v := strings.TrimSpace(*patch.Username)
		if v == "" {
			return nil, errors.New("username required")
		}
		if existing, err := p.repo.User.FindByUsername(ctx, v); err != nil {
			return nil, err
		} else if existing != nil && existing.ID != userID {
			return nil, ErrUsernameTaken
		}
		updates["username"] = v
	}
	if patch.Nickname != nil {
		updates["nickname"] = strings.TrimSpace(*patch.Nickname)
	}
	if patch.Email != nil {
		v := strings.TrimSpace(*patch.Email)
		updates["email"] = v
	}
	if patch.AvatarURL != nil {
		updates["avatar_url"] = strings.TrimSpace(*patch.AvatarURL)
	}
	if patch.HideAdult != nil {
		updates["hide_adult"] = *patch.HideAdult
	}
	if patch.SubtitleChineseMode != nil {
		mode := strings.ToLower(strings.TrimSpace(*patch.SubtitleChineseMode))
		switch mode {
		case "original", "simplified", "traditional":
			updates["subtitle_chinese_mode"] = mode
		default:
			return nil, errors.New("subtitle_chinese_mode must be original, simplified, or traditional")
		}
	}
	if len(updates) > 0 {
		if err := p.repo.DB.Model(&model.User{}).Where("id = ?", userID).
			Updates(updates).Error; err != nil {
			return nil, err
		}
	}
	return p.repo.User.FindByID(ctx, userID)
}

// GetPinnedLibraryIDs returns the user's pinned library IDs, filtered to libraries
// they can still access.
func (p *ProfileService) GetPinnedLibraryIDs(ctx context.Context, userID string) ([]string, error) {
	user, err := p.repo.User.FindByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, errors.New("user not found")
	}
	visibility := UserDefaultMediaVisibility(ctx, p.repo, userID)
	accessible, err := p.accessibleLibraryIDSet(ctx, visibility)
	if err != nil {
		return nil, err
	}
	return filterPinnedLibraryIDs(user.DecodePinnedLibraryIDs(), accessible), nil
}

// SetPinnedLibraryIDs persists the user's pinned library order after filtering to
// accessible, enabled libraries.
func (p *ProfileService) SetPinnedLibraryIDs(ctx context.Context, userID string, ids []string) ([]string, error) {
	if userID == "" {
		return nil, errors.New("missing user id")
	}
	user, err := p.repo.User.FindByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, errors.New("user not found")
	}
	visibility := UserDefaultMediaVisibility(ctx, p.repo, userID)
	accessible, err := p.accessibleLibraryIDSet(ctx, visibility)
	if err != nil {
		return nil, err
	}
	normalized := filterPinnedLibraryIDs(normalizePinnedLibraryIDs(ids), accessible)
	if normalized == nil {
		normalized = []string{}
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return nil, err
	}
	if err := p.repo.User.UpdateFields(ctx, userID, map[string]any{
		"pinned_library_ids": string(raw),
	}); err != nil {
		return nil, err
	}
	return normalized, nil
}

func (p *ProfileService) accessibleLibraryIDSet(ctx context.Context, visibility MediaVisibility) (map[string]struct{}, error) {
	libs, err := p.repo.Library.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]struct{})
	for _, lib := range libs {
		if !lib.Enabled {
			continue
		}
		if !LibraryVisibleForUser(ctx, p.repo, lib, visibility) {
			continue
		}
		out[lib.ID] = struct{}{}
	}
	// Mounted Emby libraries are not rows in the local libraries table; their
	// web IDs are embyremote~{mountID}~{remoteViewID}. Include enabled mounts
	// from the mount table so pinning them does not get stripped (and so a
	// pin-save that includes remotes cannot accidentally wipe local pins).
	if p.repo.EmbyMount != nil {
		mounts, err := p.repo.EmbyMount.List(ctx)
		if err != nil {
			return nil, err
		}
		for _, mount := range mounts {
			if !mount.Enabled || strings.TrimSpace(mount.RemoteViewID) == "" {
				continue
			}
			out[EncodeEmbyRemoteID(mount.ID, mount.RemoteViewID)] = struct{}{}
		}
	}
	return out, nil
}

func normalizePinnedLibraryIDs(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func filterPinnedLibraryIDs(ids []string, accessible map[string]struct{}) []string {
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := accessible[id]; ok {
			out = append(out, id)
		}
	}
	return out
}

// AdminUpdateRole lets administrators promote / demote another user. The
// caller is expected to gate the route with AdminRequired.
func (p *ProfileService) AdminUpdateRole(ctx context.Context, userID, role string) (*model.User, error) {
	role = strings.ToLower(strings.TrimSpace(role))
	if role != "admin" && role != "user" {
		return nil, errors.New("role must be admin or user")
	}
	if firstAdmin, err := p.repo.User.FirstAdmin(ctx); err != nil {
		return nil, err
	} else if firstAdmin != nil && firstAdmin.ID == userID && role != "admin" {
		return nil, errors.New("default admin must keep admin role")
	}
	updates := map[string]any{"role": role}
	if role == "admin" {
		updates["tier"] = "plus"
	}
	if err := p.repo.User.UpdateFields(ctx, userID, updates); err != nil {
		return nil, err
	}
	return p.repo.User.FindByID(ctx, userID)
}
