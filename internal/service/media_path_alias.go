package service

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// strmPathAliases returns sibling STRM names that can represent the same media
// item when keep_ext is toggled on or off.
//
// Both directions are supported:
//
//	foo.strm      <-> foo.mkv.strm
//	foo.strm      <-> foo.mp4.strm
//
// The caller must still verify that the aliases are no longer present on disk.
// keep_ext intentionally allows foo.mkv.strm and foo.mp4.strm to coexist as
// two real versions, so an existing sibling must never be absorbed.
func strmPathAliases(path string) []string {
	clean := filepath.Clean(strings.TrimSpace(path))
	if clean == "" || clean == "." || !strings.EqualFold(filepath.Ext(clean), ".strm") {
		return nil
	}
	base := mediaSidecarBase(clean)
	if base == "" {
		return nil
	}
	name := filepath.Base(clean)
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	lastExt := strings.ToLower(filepath.Ext(stem))
	_, hasVideoExt := videoExtensions[lastExt]
	hasVideoExt = hasVideoExt && !strings.EqualFold(lastExt, ".strm")

	dir := filepath.Dir(clean)
	out := make([]string, 0, len(videoExtensions)+1)
	seen := make(map[string]struct{}, len(videoExtensions)+1)
	add := func(candidate string) {
		candidate = filepath.Clean(candidate)
		if candidate == "" || candidate == "." || samePath(candidate, clean) {
			return
		}
		key := strings.ToLower(candidate)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, candidate)
	}
	if hasVideoExt {
		// Current keep_ext shape: the only legacy shape is the stripped name.
		add(filepath.Join(dir, base+".strm"))
		return out
	}
	// Stripped shape: an old keep_ext file may use any configured video ext.
	exts := make([]string, 0, len(videoExtensions))
	for ext := range videoExtensions {
		if strings.EqualFold(ext, ".strm") {
			continue
		}
		exts = append(exts, strings.ToLower(ext))
	}
	sort.Strings(exts)
	for _, ext := range exts {
		add(filepath.Join(dir, base+ext+".strm"))
		if upper := strings.ToUpper(ext); upper != ext {
			add(filepath.Join(dir, base+upper+".strm"))
		}
	}
	return out
}

// missingSTRMPathAliases keeps only aliases which no longer exist on disk.
// This is the key protection for keep_ext=true multi-version libraries: an
// existing foo.mp4.strm is a real second version, not a rename tombstone.
func missingSTRMPathAliases(path string) []string {
	aliases := strmPathAliases(path)
	if len(aliases) == 0 {
		return nil
	}
	out := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		if _, err := os.Lstat(alias); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			continue
		}
		out = append(out, alias)
	}
	return out
}

func liveSTRMPathAlias(path string) bool {
	for _, alias := range strmPathAliases(path) {
		if info, err := os.Stat(alias); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}
