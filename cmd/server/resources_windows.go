//go:build windows

package main

// Regenerate the linked Windows resources after changing the project logo or
// manifest:
//
//	go generate ./cmd/server
//
//go:generate go run github.com/akavel/rsrc@v0.10.2 -arch amd64 -ico ../../internal/brand/logo.ico -manifest winres/mebox.manifest -o rsrc_windows_amd64.syso
//go:generate go run github.com/akavel/rsrc@v0.10.2 -arch arm64 -ico ../../internal/brand/logo.ico -manifest winres/mebox.manifest -o rsrc_windows_arm64.syso
