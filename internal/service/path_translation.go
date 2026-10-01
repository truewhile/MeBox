package service

import (
	"os"
	"path/filepath"
	"strings"
)

// translateClientPath 将下载客户端报告的路径转换为容器内可访问的路径。
// 常见场景：下载器在另一个容器，报告的路径是其容器内路径，需要映射到当前容器。
func translateClientPath(clientPath string, mappings map[string]string) string {
	if clientPath == "" {
		return ""
	}
	clean := filepath.Clean(clientPath)
	// 尝试直接访问
	if _, err := os.Stat(clean); err == nil {
		return clean
	}
	normalizedClean := cleanPathForVolumeMapping(clientPath)
	if normalizedClean != "" && normalizedClean != "." {
		direct := filepath.Clean(filepath.FromSlash(normalizedClean))
		if _, err := os.Stat(direct); err == nil {
			return direct
		}
	}
	// 尝试路径映射
	cleanForMatch := strings.TrimRight(normalizedClean, "/")
	for clientPrefix, localPrefix := range mappings {
		prefix := strings.TrimRight(cleanPathForVolumeMapping(clientPrefix), "/")
		if prefix == "" || prefix == "." {
			continue
		}
		if cleanForMatch == prefix || strings.HasPrefix(cleanForMatch, prefix+"/") {
			rel := strings.TrimPrefix(cleanForMatch, prefix)
			rel = strings.TrimPrefix(rel, "/")
			translated := filepath.Join(localPrefix, filepath.FromSlash(rel))
			if _, err := os.Stat(translated); err == nil {
				return translated
			}
		}
	}
	return ""
}
