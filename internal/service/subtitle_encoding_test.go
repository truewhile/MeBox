package service

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"golang.org/x/text/encoding/simplifiedchinese"
	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

const assFixture = "[Script Info]\n" +
	"Title:Railgun 01 BD\n" +
	"ScriptType:v4.00+\n" +
	"\n" +
	"[Events]\n" +
	"Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n" +
	"Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,放て！心に刻んだ夢を\n"

const srtFixture = "1\n" +
	"00:00:01,000 --> 00:00:02,000\n" +
	"只有我的超电磁炮\n" +
	"\n"

func encodeUTF16(s string, bigEndian bool, withBOM bool) []byte {
	units := utf16.Encode([]rune(s))
	out := make([]byte, 0, len(units)*2+2)
	if withBOM {
		if bigEndian {
			out = append(out, 0xFE, 0xFF)
		} else {
			out = append(out, 0xFF, 0xFE)
		}
	}
	buf := make([]byte, 2)
	for _, unit := range units {
		if bigEndian {
			binary.BigEndian.PutUint16(buf, unit)
		} else {
			binary.LittleEndian.PutUint16(buf, unit)
		}
		out = append(out, buf...)
	}
	return out
}

func encodeGBK(t *testing.T, s string) []byte {
	t.Helper()
	encoded, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(s))
	if err != nil {
		t.Fatalf("encode GBK: %v", err)
	}
	return encoded
}

// 这是本次线上问题的核心场景：字幕组给的 ASS 是带 BOM 的 UTF-16LE，
// 之前原样下发，浏览器按 UTF-8 解开后整篇是替换字符，一个字都渲染不出来。
func TestDecodeSubtitleTextNormalisesLegacyEncodings(t *testing.T) {
	cases := []struct {
		name string
		raw  []byte
	}{
		{"utf8", []byte(assFixture)},
		{"utf8-bom", append([]byte{0xEF, 0xBB, 0xBF}, []byte(assFixture)...)},
		{"utf16le-bom", encodeUTF16(assFixture, false, true)},
		{"utf16be-bom", encodeUTF16(assFixture, true, true)},
		{"utf16le-no-bom", encodeUTF16(assFixture, false, false)},
		{"gb18030", encodeGBK(t, assFixture)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decodeSubtitleText(tc.raw)
			if !strings.Contains(got, "[Script Info]") {
				t.Fatalf("decoded text lost ASS header: %q", got)
			}
			if !strings.Contains(got, "Dialogue:") {
				t.Fatalf("decoded text lost Dialogue line: %q", got)
			}
			if !strings.Contains(got, "放て！心に刻んだ夢を") {
				t.Fatalf("decoded text lost non-ASCII content: %q", got)
			}
			if strings.ContainsRune(got, 0) || strings.ContainsRune(got, '\uFFFD') {
				t.Fatalf("decoded text still contains NUL or replacement runes: %q", got)
			}
		})
	}
}

func TestDecodeSubtitleTextKeepsUnknownBytes(t *testing.T) {
	raw := []byte("not a subtitle at all: \x81\x82\x83\x84")
	if got := decodeSubtitleText(raw); got != string(raw) {
		t.Fatalf("decodeSubtitleText mangled non-subtitle bytes: %q", got)
	}
}

// 接口层回归：外挂 UTF-16 字幕经 /subtitles/:id/ass（libass 路径）与
// /subtitles/:id（WebVTT 降级路径）下发时都必须是可解析的 UTF-8。
func TestSubtitleHandlersNormaliseUTF16Sidecar(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:subtitle-encoding?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Library{}, &model.Media{}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	videoPath := filepath.Join(dir, "Railgun S01E01.mkv")
	assPath := filepath.Join(dir, "Railgun S01E01.ass")
	srtPath := filepath.Join(dir, "Railgun S01E01.zh.srt")
	if err := os.WriteFile(videoPath, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(assPath, encodeUTF16(assFixture, false, true), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(srtPath, encodeGBK(t, srtFixture), 0o644); err != nil {
		t.Fatal(err)
	}

	media := model.Media{Title: "Railgun", Path: videoPath}
	if err := db.Create(&media).Error; err != nil {
		t.Fatal(err)
	}
	svc := NewSubtitleService(&config.Config{}, zap.NewNop(), repository.New(db))

	var buf bytes.Buffer
	if err := svc.ServeASS(t.Context(), media.ID, assPath, &buf); err != nil {
		t.Fatalf("ServeASS: %v", err)
	}
	ass := buf.String()
	if !bytes.HasPrefix(buf.Bytes(), []byte("[Script Info]")) {
		t.Fatalf("ServeASS output is not UTF-8 ASS: %q", ass)
	}
	if !strings.Contains(ass, "放て！心に刻んだ夢を") {
		t.Fatalf("ServeASS output lost dialogue text: %q", ass)
	}

	buf.Reset()
	if err := svc.Serve(t.Context(), media.ID, assPath, &buf); err != nil {
		t.Fatalf("Serve(ass): %v", err)
	}
	vtt := buf.String()
	if !strings.HasPrefix(vtt, "WEBVTT") || !strings.Contains(vtt, "-->") {
		t.Fatalf("ASS->WebVTT fallback produced no cues: %q", vtt)
	}
	if !strings.Contains(vtt, "放て！心に刻んだ夢を") {
		t.Fatalf("ASS->WebVTT fallback lost dialogue text: %q", vtt)
	}

	buf.Reset()
	if err := svc.Serve(t.Context(), media.ID, srtPath, &buf); err != nil {
		t.Fatalf("Serve(srt): %v", err)
	}
	vtt = buf.String()
	if !strings.Contains(vtt, "只有我的超电磁炮") {
		t.Fatalf("GBK SRT was not transcoded: %q", vtt)
	}
}
