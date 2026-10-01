package middleware

import (
	"github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"
)

// gzipExcludedExtensions 已经是压缩格式（图片/字体/媒体/归档）的响应体，
// 再 gzip 只浪费 CPU 不省流量。
var gzipExcludedExtensions = []string{
	".png", ".gif", ".jpeg", ".jpg", ".webp", ".avif", ".ico", ".svg",
	".woff", ".woff2", ".ttf", ".otf",
	".mp4", ".mkv", ".webm", ".ts", ".m4s", ".m3u8",
	".mp3", ".flac", ".aac", ".ogg",
	".zip", ".gz", ".xz", ".7z", ".rar",
}

// apiGzipExcludedPrefixes 大文件流式传输（Range 语义）与 WS/SSE 长连接
// 不参与 gzip：压缩会破坏 Range / 逐块推送语义。
var apiGzipExcludedPrefixes = []string{
	"/api/stream/",
	"/api/hls/",
	"/api/img",
	"/api/subtitles/",
	"/api/strm/play/",
	"/api/ws",
	"/api/events",
}

// GzipAPI 压缩 /api 下的 JSON 响应（媒体列表动辄数 MB，压缩率 85%+）。
// gin-contrib/gzip 的路径排除是前缀匹配，且会自动校验 Accept-Encoding
// 与 Connection: Upgrade（WebSocket 安全）。
func GzipAPI() gin.HandlerFunc {
	return gzip.Gzip(
		gzip.DefaultCompression,
		gzip.WithExcludedExtensions(gzipExcludedExtensions),
		gzip.WithExcludedPaths(apiGzipExcludedPrefixes),
	)
}

// GzipStatic 压缩 SPA 静态资源（JS/CSS/HTML 是构建产物的大头）。
func GzipStatic() gin.HandlerFunc {
	return gzip.Gzip(
		gzip.DefaultCompression,
		gzip.WithExcludedExtensions(gzipExcludedExtensions),
	)
}
