package cloud115

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

func TestFindNamedContentInParentMatchAndSameName(t *testing.T) {
	sha := "AABBCCDDEEFF00112233445566778899AABBCCDD"
	mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/open/ufile/files" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Write([]byte(fmt.Sprintf(`{"state":true,"data":[
			{"fid":"dir-1","fc":"0","fn":"sub","fs":0},
			{"fid":"keep","fc":"1","fn":"a.nfo","fs":10,"sha1":%q,"fta":"1"},
			{"fid":"dirty","fc":"1","fn":"a.nfo","fs":10,"sha1":"OTHER","fta":"1"},
			{"fid":"other","fc":"1","fn":"b.nfo","fs":10,"sha1":%q,"fta":"1"},
			{"fid":"incomplete","fc":"1","fn":"a.nfo","fs":10,"sha1":%q,"fta":"0"}
		]}`, sha, sha, sha)))
	})
	c := NewOpenClient("100195125", "at1", "rt1")
	matched, sameName, err := c.FindNamedContentInParent(context.Background(), "parent", "a.nfo", sha, 10)
	if err != nil {
		t.Fatal(err)
	}
	if matched == nil || matched.FileId != "keep" {
		t.Fatalf("matched = %+v, want keep", matched)
	}
	if len(sameName) != 2 { // keep + dirty；incomplete 被 fta 过滤
		t.Fatalf("sameName len=%d, want 2 (incomplete excluded)", len(sameName))
	}
}

func TestFindNamedContentInParentNoMatch(t *testing.T) {
	mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"state":true,"data":[
			{"fid":"x","fc":"1","fn":"a.nfo","fs":9,"sha1":"OTHER","fta":"1"}
		]}`))
	})
	c := NewOpenClient("100195125", "at1", "rt1")
	matched, sameName, err := c.FindNamedContentInParent(context.Background(), "parent", "a.nfo", "WANT", 10)
	if err != nil {
		t.Fatal(err)
	}
	if matched != nil {
		t.Fatalf("expected no match, got %+v", matched)
	}
	if len(sameName) != 1 {
		t.Fatalf("sameName should still list name hits, got %d", len(sameName))
	}
}
