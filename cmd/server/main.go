// Package main is the MeBox HTTP server entry point.
//
// MeBox is a Go rewrite of the legacy Python implementation,
// adopting the same tech stack as cropflre/nowen-video:
//
//	Backend:  Go 1.25 + Gin + GORM + PostgreSQL/SQLite + Viper + Zap + JWT
//	Frontend: React 18 + Vite + Tailwind + Zustand + HLS.js
//
// The binary embeds the SPA build artifacts at /app/web/dist and serves them
// alongside the JSON REST API at /api/* and the WebSocket hub at /api/ws.
package main

import (
	"os"
	"strings"
)

// version is overwritten at build time via -ldflags="-X main.version=...".
var version = "dev"

func effectiveVersion(buildVersion string) string {
	buildVersion = strings.TrimSpace(buildVersion)
	if buildVersion != "" && buildVersion != "dev" {
		return buildVersion
	}
	if envVersion := strings.TrimSpace(os.Getenv("MEBOX_VERSION")); envVersion != "" {
		return envVersion
	}
	if buildVersion == "" {
		return "dev"
	}
	return buildVersion
}

func main() {
	runProgram()
}
