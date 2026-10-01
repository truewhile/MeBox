package handler

import "testing"

func TestIsEmbyPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: "/emby", want: true},
		{path: "/emby/System/Info/Public", want: true},
		{path: "//emby//Items//1", want: true},
		{path: "/System/Info/Public", want: true},
		{path: "/Search/Hints", want: true},
		{path: "/Playback/BitrateTest", want: true},
		{path: "/api/unknown", want: false},
		{path: "/library/123e4567-e89b-12d3-a456-426614174000", want: true},
		{path: "/random/path", want: false},
	}
	for _, tt := range tests {
		if got := IsEmbyPath(tt.path); got != tt.want {
			t.Errorf("IsEmbyPath(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestNormalizeEmbyPrefixedInternalAPIPath(t *testing.T) {
	got, changed := NormalizeEmbyPath("/emby/api/Stream/123")
	if !changed || got != "/emby/api/stream/123" {
		t.Fatalf("NormalizeEmbyPath() = (%q, %v), want (/emby/api/stream/123, true)", got, changed)
	}
}
