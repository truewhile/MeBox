package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/truewhile/MeBox/internal/model"
)

func TestResolveMediaPeopleUsesSharedStemBesideKeepExtStrm(t *testing.T) {
	svc := newTestEmbyService(t)
	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "竞女01.mkv.strm")
	nfoPath := filepath.Join(dir, "竞女01.nfo")
	if err := os.WriteFile(mediaPath, []byte("http://example/play"), 0o644); err != nil {
		t.Fatal(err)
	}
	nfo := `<movie>
<title>竞女01</title>
<director>导演甲</director>
<actor><name>演员乙</name><role>主角</role></actor>
</movie>`
	if err := os.WriteFile(nfoPath, []byte(nfo), 0o644); err != nil {
		t.Fatal(err)
	}

	people := svc.resolveMediaPeople(t.Context(), &model.Media{Path: mediaPath, Title: "竞女01"})
	if len(people) != 2 {
		t.Fatalf("people=%d want 2: %#v", len(people), people)
	}
	got := map[string]string{}
	for _, p := range people {
		got[p["Name"].(string)] = p["Type"].(string)
	}
	if got["导演甲"] != "Director" || got["演员乙"] != "Actor" {
		t.Fatalf("unexpected people: %#v", people)
	}
}

func TestResolveMediaPeopleStillReadsLegacyKeepExtNFO(t *testing.T) {
	svc := newTestEmbyService(t)
	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "竞女01.mkv.strm")
	legacyNFO := filepath.Join(dir, "竞女01.mkv.nfo")
	if err := os.WriteFile(mediaPath, []byte("http://example/play"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyNFO, []byte(`<movie><director>旧导演</director></movie>`), 0o644); err != nil {
		t.Fatal(err)
	}

	people := svc.resolveMediaPeople(t.Context(), &model.Media{Path: mediaPath})
	if len(people) != 1 || people[0]["Name"] != "旧导演" {
		t.Fatalf("expected legacy keep_ext nfo people, got %#v", people)
	}
}
