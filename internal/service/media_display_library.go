package service

import (
	"context"
	"sort"
	"strings"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

func (s *MediaService) attachLibraryMetadata(ctx context.Context, items []model.Media) {
	if s == nil || s.repo == nil || s.repo.Library == nil || len(items) == 0 {
		return
	}
	libs, err := s.displayLibraries(ctx)
	if err != nil {
		return
	}
	byID := make(map[string]model.Library, len(libs))
	for _, lib := range libs {
		byID[lib.ID] = lib
	}
	resolver := newMediaDisplayLibraryResolver(ctx, s.repo, libs)
	for i := range items {
		var own model.Library
		var hasOwn bool
		if lib, ok := byID[items[i].LibraryID]; ok {
			own = lib
			hasOwn = true
			items[i].LibraryName = lib.Name
			items[i].LibraryPath = lib.Path
		}
		if lib, ok := resolver.DisplayLibraryForMedia(items[i]); ok {
			items[i].DisplayLibraryID = lib.ID
			items[i].DisplayLibraryName = lib.Name
			items[i].DisplayLibraryPath = lib.Path
			if hasOwn && CloudLibraryAutoCategory(own) {
				items[i].LibraryName = lib.Name
				items[i].LibraryPath = lib.Path
			}
		}
	}
}

func (s *MediaService) displayLibraries(ctx context.Context) ([]model.Library, error) {
	const cacheKey = "media:obj:library-metadata"
	if s.cache != nil {
		if cachedObj, ok := s.cache.GetObject(cacheKey); ok {
			if cached, ok := cachedObj.([]model.Library); ok {
				return cached, nil
			}
		}
	}
	libs, err := s.repo.Library.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range libs {
		libs[i] = normalizeLocalLibraryPathForDisplay(libs[i])
	}
	if s.cache != nil {
		s.cache.SetObject(cacheKey, libs, s.mediaObjectTTL())
	}
	return libs, nil
}

type mediaDisplayLibraryResolver struct {
	byID              map[string]model.Library
	displayByID       map[string]model.Library
	displayByMergeKey map[string]model.Library
	displayLibraries  []model.Library
	localDisplays     []mediaLocalDisplayLibrary
}

type mediaLocalDisplayLibrary struct {
	library model.Library
	path    string
}

func newMediaDisplayLibraryResolver(ctx context.Context, repo *repository.Container, libs []model.Library) mediaDisplayLibraryResolver {
	displayLibraries := FilterDisplayCloudLibraries(ctx, repo, append([]model.Library(nil), libs...))
	resolver := mediaDisplayLibraryResolver{
		byID:              make(map[string]model.Library, len(libs)),
		displayByID:       make(map[string]model.Library, len(displayLibraries)),
		displayByMergeKey: make(map[string]model.Library, len(displayLibraries)),
		displayLibraries:  displayLibraries,
		localDisplays:     make([]mediaLocalDisplayLibrary, 0, len(displayLibraries)),
	}
	for _, lib := range libs {
		normalized := normalizeLocalLibraryPathForDisplay(lib)
		resolver.byID[normalized.ID] = normalized
	}
	for _, lib := range displayLibraries {
		resolver.displayByID[lib.ID] = lib
		if key, ok := CloudLibraryMergeKey(lib); ok {
			if _, exists := resolver.displayByMergeKey[key]; !exists {
				resolver.displayByMergeKey[key] = lib
			}
		}
		if _, ok := ParseCloudLibraryMount(lib.Path); ok || !lib.Enabled {
			continue
		}
		displayPath := cleanPathForVolumeMapping(resolveMappedDestinationPath(lib.Path))
		if displayPath == "" || displayPath == "." {
			continue
		}
		resolver.localDisplays = append(resolver.localDisplays, mediaLocalDisplayLibrary{
			library: lib,
			path:    displayPath,
		})
	}
	// 最长路径优先：一次命中就是原逻辑中最具体的媒体库，避免每条媒体都
	// 重新规范化全部库路径并扫描整个库列表。
	sort.SliceStable(resolver.localDisplays, func(i, j int) bool {
		return len(resolver.localDisplays[i].path) > len(resolver.localDisplays[j].path)
	})
	return resolver
}

func (r mediaDisplayLibraryResolver) DisplayLibraryForMedia(media model.Media) (model.Library, bool) {
	// Issue #61: an auto-category assignment is authoritative. The media was explicitly
	// categorized into this library even though its physical (cloud) path may still live
	// under the source scan directory (e.g. cloud://cloud115/云下载/...). Resolving by path
	// here would wrongly redirect the media back to the source cloud library, so resolve it
	// from the owning library instead.
	if own, ok := r.byID[media.LibraryID]; ok && CloudLibraryAutoCategory(own) {
		return r.autoCategoryDisplayLibrary(own), true
	}
	if lib, ok := r.bestPathDisplayLibrary(media); ok {
		return lib, true
	}
	if lib, ok := r.displayByID[media.LibraryID]; ok {
		return lib, true
	}
	if own, hasOwn := r.byID[media.LibraryID]; hasOwn {
		if key, ok := CloudLibraryMergeKey(own); ok {
			if lib, exists := r.displayByMergeKey[key]; exists {
				return lib, true
			}
		}
		return own, true
	}
	return model.Library{}, false
}

// autoCategoryDisplayLibrary resolves the visible library that should represent an
// auto-category library: the library itself when it is displayed standalone, otherwise
// the sibling it was merged into, otherwise the root cloud library it was split from.
func (r mediaDisplayLibraryResolver) autoCategoryDisplayLibrary(own model.Library) model.Library {
	if lib, ok := r.displayByID[own.ID]; ok {
		return lib
	}
	if key, ok := CloudLibraryMergeKey(own); ok {
		if lib, exists := r.displayByMergeKey[key]; exists {
			return lib
		}
	}
	if lib, ok := r.rootCloudDisplayLibraryForAutoCategory(own); ok {
		return lib
	}
	return own
}

func (r mediaDisplayLibraryResolver) rootCloudDisplayLibraryForAutoCategory(auto model.Library) (model.Library, bool) {
	info, ok := ParseCloudLibraryMount(auto.Path)
	if !ok {
		return model.Library{}, false
	}
	for _, lib := range r.displayLibraries {
		if !lib.Enabled {
			continue
		}
		candidate, ok := ParseCloudLibraryMount(lib.Path)
		if ok && candidate.Provider == info.Provider && cloudRootMountNeedsAutoCategory(candidate) {
			return lib, true
		}
	}
	return model.Library{}, false
}

func (r mediaDisplayLibraryResolver) bestPathDisplayLibrary(media model.Media) (model.Library, bool) {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(media.Path)), "cloud://") {
		mediaInfo, ok := ParseCloudLibraryMount(media.Path)
		if !ok {
			return model.Library{}, false
		}
		var best model.Library
		bestDepth := 0
		for _, lib := range r.displayLibraries {
			info, ok := ParseCloudLibraryMount(lib.Path)
			if !ok || info.Provider != mediaInfo.Provider || !lib.Enabled {
				continue
			}
			dir := strings.Trim(firstNonEmpty(info.DisplayDir, info.ScanDir), "/")
			if dir == "" {
				continue
			}
			mediaDir := strings.Trim(firstNonEmpty(mediaInfo.DisplayDir, mediaInfo.ScanDir), "/")
			if mediaDir != dir && !cloudMountAncestor(dir, mediaDir) {
				continue
			}
			depth := len(strings.Split(dir, "/"))
			if depth > bestDepth {
				best = lib
				bestDepth = depth
			}
		}
		if bestDepth > 0 {
			return best, true
		}
		return model.Library{}, false
	}

	mediaPath := cleanPathForVolumeMapping(media.Path)
	if isRelativeVolumeMarkerPath(media.Path) {
		mediaPath = cleanPathForVolumeMapping(resolveMappedDestinationPath(media.Path))
	}
	for _, display := range r.localDisplays {
		if mediaPath == display.path || strings.HasPrefix(mediaPath, strings.TrimRight(display.path, "/")+"/") {
			return display.library, true
		}
	}
	return model.Library{}, false
}

func normalizeLocalLibraryPathForDisplay(lib model.Library) model.Library {
	if _, ok := ParseCloudLibraryMount(lib.Path); ok {
		return lib
	}
	lib.Path = resolveMappedDestinationPath(lib.Path)
	for i := range lib.Roots {
		if _, ok := ParseCloudLibraryMount(lib.Roots[i].Path); ok {
			continue
		}
		lib.Roots[i].Path = resolveMappedDestinationPath(lib.Roots[i].Path)
	}
	return lib
}
