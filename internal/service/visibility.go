package service

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

const AdultLibraryIDsSettingKey = "adult.library_ids"

// AdultContentEnabled reads the global Adult / NSFW switch.
func AdultContentEnabled(ctx context.Context, repo *repository.Container) bool {
	if repo == nil || repo.Setting == nil {
		return true
	}
	value, err := repo.Setting.Get(ctx, "adult.enabled")
	if err != nil {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on", "enabled", "启用", "开启":
		return true
	case "0", "false", "no", "off", "disabled", "禁用", "关闭":
		return false
	default:
		return true
	}
}

// UserHidesAdult reports whether a user's own lock overrides all profiles.
func UserHidesAdult(ctx context.Context, repo *repository.Container, userID string) bool {
	if strings.TrimSpace(userID) == "" || repo == nil || repo.User == nil {
		return false
	}
	user, err := repo.User.FindByID(ctx, userID)
	return err == nil && user != nil && user.HideAdult
}

// UserDefaultMediaVisibility is the visibility policy used by clients that
// cannot pass a web play-profile token, notably Emby/Jellyfin-compatible apps.
func UserDefaultMediaVisibility(ctx context.Context, repo *repository.Container, userID string) MediaVisibility {
	visibility := MediaVisibility{IncludeNSFW: AdultContentEnabled(ctx, repo)}
	if repo == nil {
		return visibility
	}
	if UserHidesAdult(ctx, repo, userID) {
		visibility.IncludeNSFW = false
	}
	visibility.HiddenLibraryIDs = hiddenAdultLibraryIDs(ctx, repo, visibility.IncludeNSFW)
	if userID == "" {
		return visibility
	}

	if repo.User != nil {
		user, err := repo.User.FindByID(ctx, userID)
		if err == nil && user != nil && user.Role != "admin" {
			if userAllowed := user.DecodeAllowedLibraryIDs(); len(userAllowed) > 0 {
				visibility.AllowedLibraryIDs = userAllowed
			}
		}
	}

	if repo.PlayProfile == nil {
		return visibility
	}
	rows, err := repo.PlayProfile.ListByUser(ctx, userID)
	if err != nil {
		return visibility
	}
	for _, row := range rows {
		if !row.IsDefault {
			continue
		}
		visibility.IncludeNSFW = visibility.IncludeNSFW && row.AllowAdult
		profileAllowed := DecodeAllowedLibraryIDs(row.AllowedLibraryIDs)
		if len(profileAllowed) > 0 {
			if len(visibility.AllowedLibraryIDs) > 0 {
				visibility.AllowedLibraryIDs = IntersectStrings(visibility.AllowedLibraryIDs, profileAllowed)
			} else {
				visibility.AllowedLibraryIDs = profileAllowed
			}
		}
		visibility.HiddenLibraryIDs = hiddenAdultLibraryIDs(ctx, repo, visibility.IncludeNSFW)
		break
	}
	return visibility
}

// IntersectStrings 计算两个字符串切片的交集。
func IntersectStrings(a, b []string) []string {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}
	set := make(map[string]struct{}, len(b))
	for _, s := range b {
		set[s] = struct{}{}
	}
	var out []string
	for _, s := range a {
		if _, ok := set[s]; ok {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return []string{"__no_access__"}
	}
	return out
}

// DecodeAllowedLibraryIDs normalises a PlayProfile allowed-library JSON string.
func DecodeAllowedLibraryIDs(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil
	}
	out := ids[:0]
	for _, id := range ids {
		if strings.TrimSpace(id) != "" {
			out = append(out, strings.TrimSpace(id))
		}
	}
	return out
}

// LibraryIDAllowed reports whether libraryID is permitted by the allow-list.
// An empty AllowedLibraryIDs means unrestricted access.
func LibraryIDAllowed(visibility MediaVisibility, libraryID string) bool {
	if len(visibility.AllowedLibraryIDs) == 0 {
		return true
	}
	for _, id := range visibility.AllowedLibraryIDs {
		if id == libraryID {
			return true
		}
	}
	return false
}

// EmbyMountLibraryID is the web/Emby library id for a mounted remote view.
func EmbyMountLibraryID(mount *model.EmbyMount) string {
	if mount == nil {
		return ""
	}
	return EncodeEmbyRemoteID(mount.ID, mount.RemoteViewID)
}

// EmbyMountLibraryAllowed reports whether a mounted Emby library is allowed for
// the given visibility policy.
func EmbyMountLibraryAllowed(visibility MediaVisibility, mount *model.EmbyMount) bool {
	return LibraryIDAllowed(visibility, EmbyMountLibraryID(mount))
}

// LibraryVisibleForUser applies profile library limits and adult-directory
// hiding to a library card/folder.
func LibraryVisibleForUser(ctx context.Context, repo *repository.Container, lib model.Library, visibility MediaVisibility) bool {
	if !LibraryIDAllowed(visibility, lib.ID) {
		return false
	}
	if visibility.IncludeNSFW {
		return true
	}
	hiddenLibraryIDs := visibility.HiddenLibraryIDs
	configuredAdultLibraryIDs := AdultLibraryIDs(ctx, repo)
	hasConfiguredAdultLibraries := len(hiddenLibraryIDs) > 0 || len(configuredAdultLibraryIDs) > 0
	if len(hiddenLibraryIDs) == 0 {
		hiddenLibraryIDs = configuredAdultLibraryIDs
	}
	for _, id := range hiddenLibraryIDs {
		if id == lib.ID {
			return false
		}
	}
	if hasConfiguredAdultLibraries {
		return true
	}
	if LibraryLooksAdult(lib) {
		return false
	}
	if repo != nil && repo.DB != nil {
		var totalCount int64
		_ = repo.DB.WithContext(ctx).Model(&model.Media{}).
			Where("library_id = ?", lib.ID).
			Count(&totalCount).Error
		if totalCount > 0 {
			var nsfwCount int64
			_ = repo.DB.WithContext(ctx).Model(&model.Media{}).
				Where("library_id = ? AND nsfw = ?", lib.ID, true).
				Count(&nsfwCount).Error
			// 仅当整库媒体全部为成人内容（纯成人库）时才隐藏整库；
			// 含有普通内容的混合媒体库保持库本身可见，具体 NSFW 条目在媒体列表内过滤。
			if nsfwCount == totalCount {
				return false
			}
		}
	}
	return true
}

// LibraryLooksAdult catches adult-only roots even before all rows are scraped.
func LibraryLooksAdult(lib model.Library) bool {
	text := strings.ToLower(strings.TrimSpace(lib.Name + " " + lib.Path + " " + lib.Type))
	if text == "" {
		return false
	}
	for _, token := range []string{"成人", "限制级", "nsfw", "adult", "jav", "javdb", "javbus", "9kg", "里番", "番号"} {
		if strings.Contains(text, token) {
			return true
		}
	}
	return false
}

func AdultLibraryIDs(ctx context.Context, repo *repository.Container) []string {
	if repo == nil || repo.Setting == nil {
		return nil
	}
	raw, err := repo.Setting.Get(ctx, AdultLibraryIDsSettingKey)
	if err != nil {
		return nil
	}
	return DecodeAllowedLibraryIDs(raw)
}

func hiddenAdultLibraryIDs(ctx context.Context, repo *repository.Container, includeNSFW bool) []string {
	if includeNSFW {
		return nil
	}
	return AdultLibraryIDs(ctx, repo)
}
