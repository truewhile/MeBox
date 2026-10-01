package service

import (
	"strings"
	"testing"
)

// TestCleanQueryPreservesOriginalTitleCase is the regression guard for English
// titles being lowercased on ingest: "The.Matrix.1999.1080p.BluRay.x264-AMIABLE"
// used to be stored as "the matrix", which also made metadata providers miss it.
func TestCleanQueryPreservesOriginalTitleCase(t *testing.T) {
	cases := []struct {
		in        string
		wantTitle string
		wantYear  int
	}{
		{"The.Matrix.1999.1080p.BluRay.x264-AMIABLE.mkv", "The Matrix", 1999},
		{"The.Shawshank.Redemption.1994.1080p.BluRay.mkv", "The Shawshank Redemption", 1994},
		{"Interstellar.2014.2160p.WEB-DL.mkv", "Interstellar", 2014},
		{"Breaking.Bad.S01E01.1080p.WEB-DL.mkv", "Breaking Bad", 0},
		{"Fast.and.Furious.2001.mkv", "Fast and Furious", 2001},
		{"spider-man.2002.1080p.mkv", "spider man", 2002},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			gotTitle, gotYear := CleanQuery(tc.in)
			if gotTitle != tc.wantTitle || gotYear != tc.wantYear {
				t.Errorf("CleanQuery(%q) = (%q, %d), want (%q, %d)",
					tc.in, gotTitle, gotYear, tc.wantTitle, tc.wantYear)
			}
		})
	}
}

// The release-tag rules run on lowercased tokens, so restoring the display case
// must not change which tokens survive. Anything that starts matching or
// dropping tokens only because of casing (e.g. patEP's boundary class seeing an
// uppercase letter) would show up here.
func TestCleanQueryCaseRestoreDoesNotChangeTokenSelection(t *testing.T) {
	// Both spellings must select exactly the same title tokens.
	pairs := [][2]string{
		{"The.Matrix.1999.1080p.BluRay.mkv", "the.matrix.1999.1080p.bluray.mkv"},
		{"SE7EN.1995.1080p.BluRay.mkv", "se7en.1995.1080p.bluray.mkv"},
		{"WEB.Therapy.S01E01.1080p.mkv", "web.therapy.s01e01.1080p.mkv"},
		{"For.All.Mankind.S05E06.4K.mkv", "for.all.mankind.s05e06.4k.mkv"},
	}
	for _, pair := range pairs {
		upper, upperYear := CleanQuery(pair[0])
		lower, lowerYear := CleanQuery(pair[1])
		if !equalFoldASCII(upper, lower) || upperYear != lowerYear {
			t.Errorf("case changed token selection: %q -> (%q, %d) vs %q -> (%q, %d)",
				pair[0], upper, upperYear, pair[1], lower, lowerYear)
		}
	}
}

func equalFoldASCII(a, b string) bool {
	return strings.EqualFold(a, b)
}

// A filename with no case information (all lowercase, as many release names are)
// must stay as-is rather than being title-cased: MeBox cannot invent casing.
func TestCleanQueryKeepsLowercaseInputLowercase(t *testing.T) {
	title, _ := CleanQuery("the.matrix.1999.1080p.mkv")
	if title != "the matrix" {
		t.Fatalf("CleanQuery = %q, want %q", title, "the matrix")
	}
}
