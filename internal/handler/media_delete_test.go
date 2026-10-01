package handler

import "testing"

func TestParseDeleteFilesQuery(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"", false},
		{"0", false},
		{"false", false},
		{"1", true},
		{"true", true},
		{"TRUE", true},
		{"yes", true},
	}
	for _, tc := range cases {
		if got := parseDeleteFilesQuery(tc.raw); got != tc.want {
			t.Fatalf("parseDeleteFilesQuery(%q)=%v, want %v", tc.raw, got, tc.want)
		}
	}
}
