package service

import "testing"

// CleanQuery must not delete words that are also ordinary English title words
// just because a release group reused them as a tag. Regression guard: "max"
// and "web" used to be unconditional noise/boundary tokens, which turned
// "Mad Max" into "mad" and "Web Therapy" into an empty query.
func TestCleanQueryKeepsCommonEnglishTitleWords(t *testing.T) {
	cases := []struct {
		in        string
		wantTitle string
		wantYear  int
	}{
		{"Mad.Max.1979.1080p.BluRay.mkv", "mad max", 1979},
		{"Max.Payne.2008.1080p.WEB-DL.mkv", "max payne", 2008},
		{"Web.Therapy.S01E01.1080p.WEB-DL.mkv", "web therapy", 0},
		{"The.Web.2019.1080p.mkv", "the web", 2019},
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

// A title consisting only of an ambiguous tag plus a release tail must not be
// truncated to nothing; the tag stays so the query still has a chance.
func TestCleanQueryDoesNotTruncateAmbiguousTagToNothing(t *testing.T) {
	for _, in := range []string{"Web.1080p.WEB-DL.mkv", "BD.720p.mkv"} {
		title, _ := CleanQuery(in)
		if title == "" {
			t.Errorf("CleanQuery(%q) returned an empty title", in)
		}
	}
}

// A year-prefixed filename must keep its title: the year arms weak-tag
// truncation but must not discard the real title words that follow it.
func TestCleanQueryKeepsTitleAfterYearPrefix(t *testing.T) {
	cases := map[string]string{
		"2019.Avatar.1080p.BluRay.mkv":     "avatar",
		"2024.Dune.Part.Two.2160p.WEB.mkv": "dune part two",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			got, year := CleanQuery(in)
			if got != want {
				t.Errorf("CleanQuery(%q) = (%q, %d), want title %q", in, got, year, want)
			}
		})
	}
}

// Release-tag truncation must still fire once a real release signal (an
// extracted year, resolution or codec) has been seen, so tags after it are
// dropped as before.
func TestCleanQueryStillDropsTagsAfterReleaseSignal(t *testing.T) {
	cases := map[string]string{
		"复仇者联盟4.2019.BD.1080p.mkv":               "复仇者联盟4",
		"The.Matrix.1999.1080p.WEB-DL.H265.mp4":  "the matrix",
		"Oppenheimer.2023.2160p.UHD.BluRay.mkv":  "oppenheimer",
		"Interstellar.2014.4k.hdr.dts.atmos.mkv": "interstellar",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			got, _ := CleanQuery(in)
			if got != want {
				t.Errorf("CleanQuery(%q) = %q, want %q", in, got, want)
			}
		})
	}
}
