package service

import (
	"context"
	"strings"
	"time"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

// ImageURL returns artwork for a media/series/season item id.
func (e *EmbyService) ImageURL(ctx context.Context, id, imageType string) (string, error) {
	// 远程 Emby 条目：直接返回远程图片绝对地址，由 ImageProxy 拉取透传。
	if e.remote != nil && IsEmbyRemoteID(id) {
		mountID, remoteID, _ := DecodeEmbyRemoteID(id)
		mount, acct, _ := e.remote.ResolveMount(ctx, mountID)
		if mount == nil || acct == nil {
			return "", nil
		}
		return e.remote.RemoteImageURL(ctx, acct, remoteID, imageType)
	}
	pick := func(primary, backdrop string) string {
		switch strings.ToLower(imageType) {
		case "backdrop", "art":
			if backdrop != "" {
				return backdrop
			}
		}
		if primary != "" {
			return primary
		}
		return backdrop
	}
	if strings.HasPrefix(id, embyVirtualSeasonPrefix) {
		if raw, ok := e.cachedArtworkURL(id, imageType); ok {
			return raw, nil
		}
		return "", nil
	}
	if strings.HasPrefix(id, embyVirtualSeriesPrefix) {
		if raw, ok := e.cachedArtworkURL(id, imageType); ok {
			return raw, nil
		}
		return "", nil
	}
	m, err := e.repo.Media.FindByID(ctx, id)
	if err == nil && m != nil {
		if e.mediaShouldBeEpisode(ctx, m) {
			switch strings.ToLower(imageType) {
			case "backdrop", "art":
				return "", nil
			}
		}
		return pick(e.mediaPrimaryArtwork(ctx, m), e.mediaBackdropArtwork(ctx, m)), nil
	}
	if err != nil {
		return "", err
	}
	if series, ok, err := e.findSeriesGroup(ctx, id, ""); err != nil {
		return "", err
	} else if ok {
		return pick(series.PosterURL, series.BackdropURL), nil
	}
	// id 既不是媒体也不是剧集组时,还可能是一个媒体库(Emby 客户端通过
	// /Items/{libraryID}/Images/Primary 请求库封面)。复用网页的封面来源:
	// cover_url 优先,否则挑库内评分最高的一张成员海报。
	if raw := e.libraryCoverArtwork(ctx, id); raw != "" {
		return raw, nil
	}
	return "", nil
}

// imageInfoTypes 是 GET /Items/{Id}/Images 会报告的图片类型。只列 MeBox
// 真正存储的两类：ImageURL 对 Thumb / Logo / Banner 等其余类型会回退到
// 主图，若一并列出会让客户端以为存在这些图并去请求，实际拿到的却是主图。
var imageInfoTypes = []string{"Primary", "Backdrop"}

// ImageInfos 返回条目的图片清单，对应 Emby 的 GET /Items/{Id}/Images。
// 客户端用它在详情页决定要加载哪些图；缺失该接口会落到 404，部分客户端
// 因此把条目当成"无图"而放弃渲染海报。
func (e *EmbyService) ImageInfos(ctx context.Context, id string) []map[string]any {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	out := make([]map[string]any, 0, 2)
	seen := map[string]bool{}
	for _, imageType := range imageInfoTypes {
		raw, err := e.ImageURL(ctx, id, imageType)
		if err != nil {
			continue
		}
		raw = strings.TrimSpace(raw)
		// 非 Backdrop 类型在缺图时会回退到主图，去重避免同一张图重复出现。
		if raw == "" || seen[raw] {
			continue
		}
		seen[raw] = true
		out = append(out, map[string]any{
			"ImageType":  imageType,
			"ImageIndex": 0,
			"ImageTag":   id,
		})
	}
	return out
}

// UserAvatarURL 返回用户头像的来源地址；用户未设置头像时返回空串。
func (e *EmbyService) UserAvatarURL(ctx context.Context, userID string) string {
	userID = strings.TrimSpace(userID)
	if userID == "" || e == nil || e.repo == nil || e.repo.User == nil {
		return ""
	}
	user, err := e.repo.User.FindByID(ctx, userID)
	if err != nil || user == nil {
		return ""
	}
	return strings.TrimSpace(user.AvatarURL)
}

// cachedLibraryCover returns a previously resolved library cover URL within TTL.
func (e *EmbyService) cachedLibraryCover(id string) (string, bool) {
	if e == nil || strings.TrimSpace(id) == "" {
		return "", false
	}
	now := time.Now()
	e.libraryCoverMu.Lock()
	defer e.libraryCoverMu.Unlock()
	entry, ok := e.libraryCoverCache[id]
	if !ok || now.After(entry.expiresAt) {
		if ok {
			delete(e.libraryCoverCache, id)
		}
		return "", false
	}
	return entry.primary, true
}

func (e *EmbyService) rememberLibraryCover(id, cover string) {
	if e == nil || strings.TrimSpace(id) == "" || strings.TrimSpace(cover) == "" {
		return
	}
	e.libraryCoverMu.Lock()
	defer e.libraryCoverMu.Unlock()
	if e.libraryCoverCache == nil {
		e.libraryCoverCache = make(map[string]embyArtworkCacheEntry)
	}
	if len(e.libraryCoverCache) > 4000 {
		e.libraryCoverCache = make(map[string]embyArtworkCacheEntry)
	}
	e.libraryCoverCache[id] = embyArtworkCacheEntry{
		primary:   cover,
		expiresAt: time.Now().Add(embyVirtualCacheTTL),
	}
}

// libraryCoverArtwork 解析媒体库封面,与网页媒体库封面逻辑一致:
//  1. 用户手动设置的 cover_url 优先;
//  2. 否则取库内最近的一批成员,按 seriesArtworkScore(同网页 artworkScore)
//     挑出评分最高的那张海报作为封面。
//
// Emby 主图只能显示单张,因此返回的正是网页拼图里最靠前的那张。
func (e *EmbyService) libraryCoverArtwork(ctx context.Context, id string) string {
	if e == nil {
		return ""
	}
	if cached, ok := e.cachedLibraryCover(id); ok {
		return cached
	}
	lib, err := e.repo.Library.FindByID(ctx, id)
	if err != nil || lib == nil {
		return ""
	}
	if strings.TrimSpace(lib.CoverURL) != "" {
		cover := lib.CoverURL
		e.rememberLibraryCover(id, cover)
		return cover
	}
	var cover string
	if rows, _, err := e.repo.Media.ListByLibraryFiltered(ctx, id, 0, 12, repository.MediaQueryFilter{IncludeNSFW: true}); err == nil {
		var bestScore = -1
		for i := range rows {
			score := seriesArtworkScore(rows[i])
			if score <= bestScore {
				continue
			}
			bestScore = score
			cover = rows[i].PosterURL
			if cover == "" {
				cover = rows[i].BackdropURL
			}
		}
	}
	if cover != "" {
		e.rememberLibraryCover(id, cover)
	}
	return cover
}

// LibraryHasCover reports whether a library has an Emby-servable cover, so the
// Views / virtual-folders payloads can advertise ImageTags.Primary to clients.
func (e *EmbyService) LibraryHasCover(ctx context.Context, id string) bool {
	return e.libraryCoverArtwork(ctx, id) != ""
}

func (e *EmbyService) mediaPrimaryArtwork(ctx context.Context, m *model.Media) string {
	if m == nil {
		return ""
	}
	if e.mediaShouldBeEpisode(ctx, m) && strings.TrimSpace(m.BackdropURL) != "" {
		return m.BackdropURL
	}
	return m.PosterURL
}

func (e *EmbyService) mediaBackdropArtwork(ctx context.Context, m *model.Media) string {
	if m == nil {
		return ""
	}
	if e.mediaShouldBeEpisode(ctx, m) {
		return ""
	}
	return m.BackdropURL
}
