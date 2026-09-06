package service

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/truewhile/MeBox/internal/model"
)

// SystemInfo returns the full Emby identity payload.
func (e *EmbyService) SystemInfo() map[string]any {
	port := 8096
	if e != nil && e.cfg != nil {
		port = e.cfg.App.Port
	}
	return map[string]any{
		"Id":                     embyServerID,
		"ServerId":               embyServerID,
		"ServerName":             "MeBox",
		"Version":                embyCompatVersion,
		"ServerVersion":          embyCompatVersion,
		"ProductName":            "Emby Server",
		"OperatingSystem":        "Windows",
		"Architecture":           "X64",
		"LocalAddress":           "",
		"WanAddress":             "",
		"HasPendingRestart":      false,
		"IsShuttingDown":         false,
		"SupportsLibraryMonitor": true,
		"SupportsHttps":          false,
		"SupportsAutoDiscovery":  true,
		"HttpServerPortNumber":   port,
		"HttpsPortNumber":        0,
		"PublishedServerUrl":     "",
		"WebSocketPortNumber":    port,
		"CompletedInstallations": []any{},
		"CanSelfRestart":         false,
		"CanLaunchWebBrowser":    false,
		"CanRestart":             false,
	}
}

// SystemInfoPublic 是不需要认证的精简版（Emby Web 客户端登陆前会拉）。
func (e *EmbyService) SystemInfoPublic() map[string]any {
	port := 8096
	if e != nil && e.cfg != nil {
		port = e.cfg.App.Port
	}
	return map[string]any{
		"Id":                     embyServerID,
		"ServerId":               embyServerID,
		"ServerName":             "MeBox",
		"Version":                embyCompatVersion,
		"ServerVersion":          embyCompatVersion,
		"ProductName":            "Emby Server",
		"OperatingSystem":        "Windows",
		"LocalAddress":           "",
		"WanAddress":             "",
		"HttpServerPortNumber":   port,
		"HttpsPortNumber":        0,
		"SupportsHttps":          false,
		"SupportsAutoDiscovery":  true,
		"StartupWizardCompleted": true,
	}
}

// ListUsers returns Emby-shaped users.
func (e *EmbyService) ListUsers(ctx context.Context) ([]map[string]any, error) {
	users, err := e.repo.User.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(users))
	for _, u := range users {
		out = append(out, e.userPayload(&u))
	}
	return out, nil
}

// FindUser 用 ID 查用户，用于 /Users/Me 与 /Users/{id}。
func (e *EmbyService) FindUser(ctx context.Context, id string) (map[string]any, error) {
	u, err := e.repo.User.FindByID(ctx, id)
	if err != nil || u == nil {
		return nil, err
	}
	return e.userPayload(u), nil
}

func (e *EmbyService) userPayload(u *model.User) map[string]any {
	canDownload := u.Role == "admin"
	return map[string]any{
		"Id":                        u.ID,
		"Name":                      u.Username,
		"ServerId":                  embyServerID,
		"ServerName":                "MeBox",
		"HasPassword":               true,
		"HasConfiguredPassword":     true,
		"HasConfiguredEasyPassword": false,
		"EnableAutoLogin":           false,
		"LastLoginDate":             u.LastLoginAt,
		"LastActivityDate":          u.UpdatedAt,
		"Configuration": map[string]any{
			"PlayDefaultAudioTrack":      true,
			"DisplayCollectionsView":     true,
			"DisplayMissingEpisodes":     false,
			"SubtitleMode":               "Default",
			"EnableNextEpisodeAutoPlay":  true,
			"AudioLanguagePreference":    "",
			"SubtitleLanguagePreference": "",
		},
		"Policy": map[string]any{
			"IsAdministrator":                u.Role == "admin",
			"IsHidden":                       false,
			"IsDisabled":                     !u.IsActive,
			"EnableUserPreferenceAccess":     true,
			"EnableRemoteAccess":             true,
			"EnableMediaPlayback":            true,
			"EnableAudioPlaybackTranscoding": true,
			"EnableVideoPlaybackTranscoding": true,
			"EnablePlaybackRemuxing":         true,
			"EnableLiveTvAccess":             false,
			"EnableContentDownloading":       canDownload,
			"EnableSyncTranscoding":          canDownload,
			"EnableMediaConversion":          canDownload,
			"EnableAllChannels":              true,
			"EnableAllFolders":               true,
			"EnableAllDevices":               true,
			"AuthenticationProviderId":       embyLocalAuthenticationProviderID,
			"PasswordResetProviderId":        embyLocalPasswordResetProviderID,
		},
	}
}

// Views 返回 Emby 中"虚拟根目录"——每个 library 一个条目，外加所有启用的
// 远程 Emby 挂载的媒体库（联邦聚合）。顺序遵循用户置顶偏好：置顶库靠前，
// 未置顶保持原有 sort_order / 远程挂载顺序。
func (e *EmbyService) Views(ctx context.Context, userID string) (map[string]any, error) {
	libs, err := e.repo.Library.List(ctx)
	if err != nil {
		return nil, err
	}
	libs = FilterDisplayCloudLibraries(ctx, e.repo, libs)
	visibility := e.mediaVisibility(ctx, userID)
	items := make([]map[string]any, 0, len(libs)+4)
	for _, l := range libs {
		if !e.libraryVisibleFromCachedVisibility(l, visibility) {
			continue
		}
		items = append(items, e.libraryAsView(ctx, &l))
	}
	for _, remote := range e.remoteViews(ctx) {
		id, _ := remote["Id"].(string)
		if !LibraryIDAllowed(visibility, id) {
			continue
		}
		items = append(items, remote)
	}
	items = sortViewItemsByPinnedIDs(items, e.pinnedLibraryIDsForUser(ctx, userID))
	return map[string]any{"Items": items, "TotalRecordCount": len(items), "StartIndex": 0}, nil
}

func (e *EmbyService) pinnedLibraryIDsForUser(ctx context.Context, userID string) []string {
	if e == nil || e.repo == nil || e.repo.User == nil || strings.TrimSpace(userID) == "" {
		return nil
	}
	user, err := e.repo.User.FindByID(ctx, userID)
	if err != nil || user == nil {
		return nil
	}
	return user.DecodePinnedLibraryIDs()
}

func sortViewItemsByPinnedIDs(items []map[string]any, pinnedIDs []string) []map[string]any {
	if len(items) < 2 || len(pinnedIDs) == 0 {
		return items
	}
	rank := make(map[string]int, len(pinnedIDs))
	for i, id := range pinnedIDs {
		if id == "" {
			continue
		}
		if _, exists := rank[id]; !exists {
			rank[id] = i
		}
	}
	if len(rank) == 0 {
		return items
	}
	sorted := append([]map[string]any(nil), items...)
	sort.SliceStable(sorted, func(i, j int) bool {
		iID, _ := sorted[i]["Id"].(string)
		jID, _ := sorted[j]["Id"].(string)
		iRank, iPinned := rank[iID]
		jRank, jPinned := rank[jID]
		if iPinned != jPinned {
			return iPinned
		}
		if iPinned && jPinned {
			return iRank < jRank
		}
		return false
	})
	return sorted
}

// remoteViews 返回全部启用挂载的远程媒体库视图（只有显式挂载的库才出现）。
func (e *EmbyService) remoteViews(ctx context.Context) []map[string]any {
	if e == nil || e.remote == nil {
		return nil
	}
	views, err := e.remote.RemoteLibraries(ctx)
	if err != nil || len(views) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(views))
	for _, v := range views {
		out = append(out, remoteLibraryViewPayload(v))
	}
	return out
}

// remoteLibraryViewPayload 把挂载库展示信息标准化为 Emby View payload
// （ID 用挂载伪装，名称=挂载显示名）。
func remoteLibraryViewPayload(v RemoteLibraryView) map[string]any {
	encoded := EncodeEmbyRemoteID(v.MountID, v.RemoteID)
	collectionType := v.CollectionType
	if !isSupportedEmbyCollectionType(collectionType) {
		collectionType = "mixed"
	}
	name := strings.TrimSpace(v.Library.Name)
	imageTags := map[string]string{}
	if strings.TrimSpace(v.Library.CoverURL) != "" {
		imageTags["Primary"] = encoded
	}
	return map[string]any{
		"Id":                       encoded,
		"Name":                     name,
		"CollectionType":           collectionType,
		"ServerId":                 embyServerID,
		"Type":                     "CollectionFolder",
		"IsFolder":                 true,
		"Path":                     "",
		"SortName":                 strings.ToLower(name),
		"DateCreated":              time.Now().UTC().Format(time.RFC3339),
		"CanDelete":                false,
		"CanDownload":              false,
		"DisplayPreferencesId":     encoded,
		"PrimaryImageItemId":       encoded,
		"PrimaryImageAspectRatio":  1.7777777777777777,
		"RecursiveItemCount":       0,
		"ChildCount":               0,
		"SpecialFeatureCount":      0,
		"EnableMediaSourceDisplay": true,
		"PlayAccess":               "Full",
		"ExternalUrls":             []any{},
		"ProviderIds":              map[string]string{},
		"Genres":                   []string{},
		"Tags":                     []string{},
		"ImageTags":                imageTags,
		"BackdropImageTags":        []string{},
		"UserData": map[string]any{
			"PlaybackPositionTicks": 0,
			"PlayCount":             0,
			"IsFavorite":            false,
			"Played":                false,
			"UnplayedItemCount":     0,
		},
	}
}

func isSupportedEmbyCollectionType(t string) bool {
	switch t {
	case "movies", "tvshows", "music", "mixed", "homevideos", "boxsets":
		return true
	}
	return false
}

func (e *EmbyService) libraryAsView(ctx context.Context, l *model.Library) map[string]any {
	collectionType := "movies"
	switch l.Type {
	case "tv":
		collectionType = "tvshows"
	case "anime":
		collectionType = "tvshows" // Emby 没有专门的 anime CollectionType
	case "variety":
		collectionType = "tvshows"
	case "music":
		collectionType = "music"
	}
	// 库封面:与剧集/电影一样,只有在能解析出封面时才广告 ImageTags.Primary,
	// 否则客户端会认为该库无主图而不去请求 /Images/Primary。
	imageTags := map[string]string{}
	if e.LibraryHasCover(ctx, l.ID) {
		imageTags["Primary"] = l.ID
	}
	return map[string]any{
		"Id":                       l.ID,
		"Name":                     l.Name,
		"CollectionType":           collectionType,
		"ServerId":                 embyServerID,
		"Type":                     "CollectionFolder",
		"IsFolder":                 true,
		"Path":                     l.Path,
		"SortName":                 strings.ToLower(l.Name),
		"DateCreated":              l.CreatedAt.UTC().Format(time.RFC3339),
		"CanDelete":                false,
		"CanDownload":              false,
		"DisplayPreferencesId":     l.ID,
		"PrimaryImageItemId":       l.ID,
		"PrimaryImageAspectRatio":  1.7777777777777777,
		"RecursiveItemCount":       0,
		"ChildCount":               0,
		"SpecialFeatureCount":      0,
		"EnableMediaSourceDisplay": true,
		"PlayAccess":               "Full",
		"ExternalUrls":             []any{},
		"ProviderIds":              map[string]string{},
		"Genres":                   []string{},
		"Tags":                     []string{},
		"ImageTags":                imageTags,
		"BackdropImageTags":        []string{},
		"UserData": map[string]any{
			"PlaybackPositionTicks": 0,
			"PlayCount":             0,
			"IsFavorite":            false,
			"Played":                false,
			"UnplayedItemCount":     0,
		},
	}
}
