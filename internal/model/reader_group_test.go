package model

import (
	"strings"
	"testing"
)

// 书架分组的清洗与编解码回归测试（与 library_tag_test.go 同构）。

func TestNormalizeBookGroupsMergesDuplicatesAndTrims(t *testing.T) {
	groups := []BookGroupSet{
		{Name: " 科幻 ", BookIDs: []string{"a", "b"}},
		{Name: "科幻", BookIDs: []string{"b", "c"}},
		{Name: "   ", BookIDs: []string{"d"}},
		{Name: "在读", BookIDs: []string{"", "e", "e"}},
	}
	got := NormalizeBookGroups(groups)
	if len(got) != 2 {
		t.Fatalf("NormalizeBookGroups len = %d, want 2 (%#v)", len(got), got)
	}
	if got[0].Name != "科幻" || strings.Join(got[0].BookIDs, ",") != "a,b,c" {
		t.Fatalf("首个分组 = %#v，期望 科幻[a,b,c]", got[0])
	}
	if got[1].Name != "在读" || strings.Join(got[1].BookIDs, ",") != "e" {
		t.Fatalf("第二个分组 = %#v，期望 在读[e]", got[1])
	}
}

func TestNormalizeBookGroupsCapsCountAndNameLength(t *testing.T) {
	long := strings.Repeat("长", MaxBookGroupNameLen+10)
	got := NormalizeBookGroups([]BookGroupSet{{Name: long}})
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if runes := []rune(got[0].Name); len(runes) != MaxBookGroupNameLen {
		t.Fatalf("分组名长度 = %d，期望 %d", len(runes), MaxBookGroupNameLen)
	}

	many := make([]BookGroupSet, 0, MaxBookGroups+5)
	for i := 0; i < MaxBookGroups+5; i++ {
		many = append(many, BookGroupSet{Name: string(rune('a' + i))})
	}
	if capped := NormalizeBookGroups(many); len(capped) != MaxBookGroups {
		t.Fatalf("capped len = %d, want %d", len(capped), MaxBookGroups)
	}
}

func TestNormalizeBookGroupsKeepsEmptyGroups(t *testing.T) {
	got := NormalizeBookGroups([]BookGroupSet{{Name: "空组"}})
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	// 空分组的 BookIDs 应该是空数组而不是 nil，前端直接遍历时不会炸
	if got[0].BookIDs == nil || len(got[0].BookIDs) != 0 {
		t.Fatalf("BookIDs = %#v，期望空数组", got[0].BookIDs)
	}
}

func TestEncodeDecodeBookGroupsRoundTrip(t *testing.T) {
	if encoded, err := EncodeBookGroups(nil); err != nil || encoded != "" {
		t.Fatalf("EncodeBookGroups(nil) = %q, %v; want \"\", nil", encoded, err)
	}

	raw, err := EncodeBookGroups([]BookGroupSet{{Name: "科幻", BookIDs: []string{"b-1"}}})
	if err != nil {
		t.Fatalf("EncodeBookGroups: %v", err)
	}
	row := &ReaderBookGroups{Groups: raw}
	decoded := row.DecodeBookGroups()
	if len(decoded) != 1 || decoded[0].Name != "科幻" || decoded[0].BookIDs[0] != "b-1" {
		t.Fatalf("DecodeBookGroups = %#v", decoded)
	}

	row.Groups = "{not json"
	if decoded := row.DecodeBookGroups(); decoded != nil {
		t.Fatalf("损坏数据应返回 nil，实际 %#v", decoded)
	}

	// nil 接收者不应 panic（仓库层查不到记录时会返回 nil）
	var nilRow *ReaderBookGroups
	if decoded := nilRow.DecodeBookGroups(); decoded != nil {
		t.Fatalf("nil 接收者应返回 nil，实际 %#v", decoded)
	}
}
