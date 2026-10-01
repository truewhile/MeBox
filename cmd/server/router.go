package main

import (
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/handler"
	"github.com/truewhile/MeBox/internal/middleware"
	"github.com/truewhile/MeBox/internal/service"

	"github.com/truewhile/MeBox/web"
)

func buildRouter(cfg *config.Config, logger *zap.Logger, embyCompatLogger *zap.Logger, svc *service.Container) *gin.Engine {
	if !cfg.App.Debug {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(middleware.RequestLogger(logger))
	r.Use(middleware.EmbyCompatLogger(embyCompatLogger, func(path string) bool {
		return !isFrontendLibraryRoute(path) && handler.IsEmbyPath(path)
	}))
	if !cfg.App.Debug && len(cfg.App.CORSOrigins) == 0 {
		logger.Warn("CORS: no origins configured in production — CORS headers will be omitted (same-origin enforced). Set app.cors_origins for cross-origin access.")
	}
	r.Use(middleware.CORS(cfg.App.CORSOrigins, cfg.App.Debug))

	handler.Register(r, cfg, logger, svc)

	// Prefer a directory on disk when configured explicitly (e.g. the Docker image
	// mounts web/dist from the build stage, or an operator overrides app.web_dir
	// with a custom skin). Otherwise fall back to the SPA embedded into the binary,
	// which is what makes the cross-platform single-file artifacts work.
	uiFS := webui.DistFS()
	if dir := cfg.App.WebDir; dir != "" {
		disk := os.DirFS(dir)
		if index, err := fs.Stat(disk, "index.html"); err == nil && !index.IsDir() {
			uiFS = disk
		}
	}
	serveSPA(r, uiFS)
	return r
}

// serveSPA serves the React build artifacts and falls back to index.html for
// non-API, non-asset paths so client-side routing keeps working. The UI tree
// comes from root, which is either the compiled-in SPA or an on-disk web dir.
func serveSPA(r *gin.Engine, root fs.FS) {
	assets := r.Group("/assets")
	assets.Use(middleware.GzipStatic())
	assets.Use(func(c *gin.Context) {
		c.Header("Cache-Control", "public, max-age=31536000, immutable")
		c.Next()
	})
	assets.GET("/*filepath", serveFSDir(root, "assets"))
	fonts := r.Group("/fonts")
	fonts.Use(middleware.GzipStatic())
	fonts.Use(func(c *gin.Context) {
		c.Header("Cache-Control", "public, max-age=86400")
		c.Next()
	})
	fonts.GET("/*filepath", serveFSDir(root, "fonts"))
	brand := r.Group("/brand")
	brand.Use(func(c *gin.Context) {
		setNoCacheHeaders(c)
		c.Next()
	})
	brand.GET("/*filepath", serveFSDir(root, "brand"))
	for _, rootFile := range []string{"/favicon.ico", "/favicon.svg", "/artwork-cache-sw.js"} {
		name := strings.TrimPrefix(rootFile, "/")
		r.GET(rootFile, serveFSFile(root, name))
		r.HEAD(rootFile, serveFSFile(root, name))
	}
	r.NoRoute(middleware.GzipStatic(), func(c *gin.Context) {
		if handler.TryHandleEmbyNormalizedRoute(c, r) {
			return
		}
		path := c.Request.URL.Path
		if shouldBypassSPAFallback(path) {
			c.Status(http.StatusNotFound)
			return
		}
		setNoCacheHeaders(c)
		data, err := fs.ReadFile(root, "index.html")
		if err != nil {
			c.String(http.StatusNotFound, "MeBox web UI not found")
			return
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", data)
	})
}

// serveFSDir serves a static subdirectory of root. A missing asset returns 404.
func serveFSDir(root fs.FS, dir string) gin.HandlerFunc {
	sub, err := fs.Sub(root, dir)
	if err != nil {
		return func(c *gin.Context) { c.Status(http.StatusNotFound) }
	}
	handler := http.StripPrefix("/"+dir, http.FileServerFS(sub))
	return func(c *gin.Context) {
		handler.ServeHTTP(c.Writer, c.Request)
	}
}

// serveFSFile serves a single root-level file (favicon / service worker) with
// no-cache headers. It reads from root, which may be the embedded SPA or disk.
func serveFSFile(root fs.FS, name string) gin.HandlerFunc {
	return func(c *gin.Context) {
		setNoCacheHeaders(c)
		data, err := fs.ReadFile(root, name)
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		c.Data(http.StatusOK, mimeTypeByName(name), data)
	}
}

// mimeTypeByName returns an HTTP content type guessed from a file extension.
func mimeTypeByName(name string) string {
	switch mime.TypeByExtension(filepath.Ext(name)) {
	case "":
		return "application/octet-stream"
	default:
		return mime.TypeByExtension(filepath.Ext(name))
	}
}

func setNoCacheHeaders(c *gin.Context) {
	c.Header("Cache-Control", "no-cache, no-store, must-revalidate")
	c.Header("Pragma", "no-cache")
	c.Header("Expires", "0")
}

func shouldBypassSPAFallback(path string) bool {
	if isFrontendLibraryRoute(path) {
		return false
	}
	lower := strings.ToLower(path)
	for _, exact := range []string{
		"/emby",
	} {
		if lower == exact {
			return true
		}
	}
	for _, prefix := range []string{
		"/api/",
		"/emby/",
		"/system/",
		"/users/",
		"/items/",
		"/shows/",
		"/library/",
		"/videos/",
		"/sessions/",
		"/displaypreferences/",
		"/branding/",
		"/localization/",
		"/startup/",
		"/quickconnect/",
		"/socket",
		"/embywebsocket",
	} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func isFrontendLibraryRoute(path string) bool {
	const prefix = "/library/"
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	id := strings.TrimPrefix(path, prefix)
	if strings.Contains(id, "/") {
		return false
	}
	// 远程 Emby 挂载库的伪装 ID（embyremote~account~remote）也是前端库路由，
	// 需要交给 SPA 而非当作 Emby API 路径 404。
	if strings.HasPrefix(id, "embyremote~") {
		return true
	}
	if len(id) != 36 {
		return false
	}
	for i, ch := range id {
		switch i {
		case 8, 13, 18, 23:
			if ch != '-' {
				return false
			}
		default:
			if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')) {
				return false
			}
		}
	}
	return true
}
