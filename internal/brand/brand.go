// Package brand contains the embedded MeBox application artwork.
package brand

import _ "embed"

// Icon is the Windows ICO used by both the executable resource and the
// notification-area icon.
//
//go:embed logo.ico
var Icon []byte
