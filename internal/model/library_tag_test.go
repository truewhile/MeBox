package model

import "testing"

func TestNormalizeLibraryTagsMergesDuplicatesAndTrims(t *testing.T) {
	tags := []LibraryTagSet{
		{Name: "  动画  ", LibraryIDs: []string{" a ", "", "b", "a"}},
		{Name: "动画", LibraryIDs: []string{"c", "b"}},
		{Name: "   ", LibraryIDs: []string{"x"}},
		{Name: "电影", LibraryIDs: nil},
	}
	got := NormalizeLibraryTags(tags)
	if len(got) != 2 {
		t.Fatalf("NormalizeLibraryTags len = %d, want 2 (%#v)", len(got), got)
	}
	if got[0].Name != "动画" {
		t.Fatalf("first tag name = %q, want 动画", got[0].Name)
	}
	want := []string{"a", "b", "c"}
	if len(got[0].LibraryIDs) != len(want) {
		t.Fatalf("first tag ids = %v, want %v", got[0].LibraryIDs, want)
	}
	for i := range want {
		if got[0].LibraryIDs[i] != want[i] {
			t.Fatalf("first tag ids = %v, want %v", got[0].LibraryIDs, want)
		}
	}
	if got[1].Name != "电影" || got[1].LibraryIDs == nil {
		t.Fatalf("empty tag should survive with an empty non-nil id list: %#v", got[1])
	}
}

func TestNormalizeLibraryTagsCapsCountAndNameLength(t *testing.T) {
	long := make([]rune, MaxLibraryTagNameLen+10)
	for i := range long {
		long[i] = 'x'
	}
	got := NormalizeLibraryTags([]LibraryTagSet{{Name: string(long)}})
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if runes := []rune(got[0].Name); len(runes) != MaxLibraryTagNameLen {
		t.Fatalf("name length = %d, want %d", len(runes), MaxLibraryTagNameLen)
	}

	many := make([]LibraryTagSet, 0, MaxLibraryTags+5)
	for i := 0; i < MaxLibraryTags+5; i++ {
		many = append(many, LibraryTagSet{Name: string(rune('a' + i%26)) + "-" + string(rune('a'+i/26))})
	}
	if capped := NormalizeLibraryTags(many); len(capped) > MaxLibraryTags {
		t.Fatalf("capped len = %d, want <= %d", len(capped), MaxLibraryTags)
	}
}

func TestEncodeDecodeLibraryTagsRoundTrip(t *testing.T) {
	user := &User{}
	if encoded, err := EncodeLibraryTags(nil); err != nil || encoded != "" {
		t.Fatalf("EncodeLibraryTags(nil) = %q, %v; want \"\", nil", encoded, err)
	}
	raw, err := EncodeLibraryTags([]LibraryTagSet{{Name: "动画", LibraryIDs: []string{"lib-1"}}})
	if err != nil {
		t.Fatalf("EncodeLibraryTags: %v", err)
	}
	user.LibraryTags = raw
	decoded := user.DecodeLibraryTags()
	if len(decoded) != 1 || decoded[0].Name != "动画" || len(decoded[0].LibraryIDs) != 1 || decoded[0].LibraryIDs[0] != "lib-1" {
		t.Fatalf("DecodeLibraryTags = %#v", decoded)
	}

	user.LibraryTags = "{not json"
	if decoded := user.DecodeLibraryTags(); decoded != nil {
		t.Fatalf("corrupt payload should decode to nil, got %#v", decoded)
	}

	user.PopulateComputedFields()
	if user.LibraryTagList != nil {
		t.Fatalf("PopulateComputedFields should mirror DecodeLibraryTags, got %#v", user.LibraryTagList)
	}
}
