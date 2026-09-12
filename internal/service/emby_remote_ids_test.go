package service

import (
	"testing"
)

func TestEmbyRemoteIDEncodeDecode(t *testing.T) {
	encoded := EncodeEmbyRemoteID("acct-1", "item-123")
	want := "embyremote~acct-1~item-123"
	if encoded != want {
		t.Fatalf("encoded = %q, want %q", encoded, want)
	}
	if !IsEmbyRemoteID(encoded) {
		t.Fatalf("IsEmbyRemoteID(%q) = false", encoded)
	}
	acctID, remoteID, ok := DecodeEmbyRemoteID(encoded)
	if !ok || acctID != "acct-1" || remoteID != "item-123" {
		t.Fatalf("decode = (%q, %q, %v)", acctID, remoteID, ok)
	}
}

func TestDecodeEmbyRemoteIDAllowsTildeInRemoteID(t *testing.T) {
	// 远程 ID 本身允许包含 "~"：只切第一刀。
	encoded := EncodeEmbyRemoteID("acct-1", "a~b~c")
	acctID, remoteID, ok := DecodeEmbyRemoteID(encoded)
	if !ok || acctID != "acct-1" || remoteID != "a~b~c" {
		t.Fatalf("decode = (%q, %q, %v)", acctID, remoteID, ok)
	}
}

func TestDecodeEmbyRemoteIDRejectsLocalUUIDs(t *testing.T) {
	if _, _, ok := DecodeEmbyRemoteID("550e8400-e29b-41d4-a716-446655440000"); ok {
		t.Fatal("local UUID must not decode as remote id")
	}
	if _, _, ok := DecodeEmbyRemoteID("embyremote~only-acct"); ok {
		t.Fatal("malformed remote id must not decode")
	}
	if _, _, ok := DecodeEmbyRemoteID("embyremote~~"); ok {
		t.Fatal("empty parts must not decode")
	}
}

func TestRewriteEmbyRemoteIDs(t *testing.T) {
	payload := map[string]any{
		"Id":                   "item-1",
		"ParentId":             "folder-1",
		"SeriesId":             "series-1",
		"SeasonId":             "season-1",
		"PrimaryImageItemId":   "item-1",
		"DisplayPreferencesId": "folder-1",
		"ImageTags": map[string]any{
			"Primary": "item-1",
		},
		"BackdropImageTags": []any{"item-1-bd"},
		"Items": []any{
			map[string]any{"Id": "item-2", "ParentId": "folder-2"},
		},
		"People": []any{
			map[string]any{
				"Id":              "person-1",
				"Name":            "演员甲",
				"Type":            "Actor",
				"PrimaryImageTag": "person-tag-1",
			},
		},
		// MediaSource 的 Id 保持原样（客户端仅作为 MediaSourceId 查询参数）。
		"MediaSources": []any{
			map[string]any{
				"Id":              "ms-9",
				"DirectStreamUrl": "/Videos/item-1/stream",
				"MediaStreams": []any{
					map[string]any{"Type": "Subtitle", "DeliveryUrl": "/Videos/item-1/Subtitles/2/Stream.srt"},
				},
			},
		},
	}
	RewriteEmbyRemoteIDs(payload, "acct-1")

	if got := payload["Id"]; got != "embyremote~acct-1~item-1" {
		t.Fatalf("Id = %v", got)
	}
	if got := payload["ParentId"]; got != "embyremote~acct-1~folder-1" {
		t.Fatalf("ParentId = %v", got)
	}
	if got := payload["SeriesId"]; got != "embyremote~acct-1~series-1" {
		t.Fatalf("SeriesId = %v", got)
	}
	if got := payload["SeasonId"]; got != "embyremote~acct-1~season-1" {
		t.Fatalf("SeasonId = %v", got)
	}
	if got := payload["ImageTags"].(map[string]any)["Primary"]; got != "embyremote~acct-1~item-1" {
		t.Fatalf("ImageTags.Primary = %v", got)
	}
	if got := payload["BackdropImageTags"].([]any)[0]; got != "embyremote~acct-1~item-1-bd" {
		t.Fatalf("BackdropImageTags[0] = %v", got)
	}
	nested := payload["Items"].([]any)[0].(map[string]any)
	if nested["Id"] != "embyremote~acct-1~item-2" {
		t.Fatalf("nested Id = %v", nested["Id"])
	}
	person := payload["People"].([]any)[0].(map[string]any)
	if person["Id"] != "embyremote~acct-1~person-1" {
		t.Fatalf("person Id = %v", person["Id"])
	}
	if person["Name"] != "演员甲" || person["PrimaryImageTag"] != "person-tag-1" {
		t.Fatalf("person display fields changed: %#v", person)
	}

	// MediaSource.Id 与 URL 不被 ID 重写器触碰（URL 由代理模式函数改写）。
	ms := payload["MediaSources"].([]any)[0].(map[string]any)
	if ms["Id"] != "ms-9" {
		t.Fatalf("MediaSource.Id must stay raw, got %v", ms["Id"])
	}
	if ms["DirectStreamUrl"] != "/Videos/item-1/stream" {
		t.Fatalf("DirectStreamUrl must stay raw, got %v", ms["DirectStreamUrl"])
	}
}
