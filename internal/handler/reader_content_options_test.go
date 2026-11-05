package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/truewhile/MeBox/internal/service/reader"
)

func TestReaderContentOptions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		query string
		want  reader.ContentOptions
	}{
		{"", reader.ContentOptions{}},
		{"?media=direct", reader.ContentOptions{MediaDirect: true}},
		{"?media=proxy", reader.ContentOptions{ForceProxy: true}},
		{"?audio=native", reader.ContentOptions{NativeAudio: true}},
		{"?audio=native&media=direct", reader.ContentOptions{NativeAudio: true, MediaDirect: true}},
		{"?audio=native&media=proxy", reader.ContentOptions{NativeAudio: true, ForceProxy: true}},
		{"?audio=%20NaTiVe%20&media=%20DiReCt%20", reader.ContentOptions{NativeAudio: true, MediaDirect: true}},
		{"?audio=native&media=unknown", reader.ContentOptions{NativeAudio: true}},
		{"?audio=browser&media=proxy", reader.ContentOptions{ForceProxy: true}},
		{"?audio=unknown&media=direct", reader.ContentOptions{MediaDirect: true}},
	} {
		t.Run(tc.query, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/api/reader/books/book/content"+tc.query, nil)
			if got := readerContentOptions(c); got != tc.want {
				t.Fatalf("options = %+v, want %+v", got, tc.want)
			}
		})
	}
}
