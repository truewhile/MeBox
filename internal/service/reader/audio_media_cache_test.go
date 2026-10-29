package reader

import "testing"

func TestIsAudioMediaURL(t *testing.T) {
	cases := []struct {
		name     string
		url      string
		bookType int
		want     bool
	}{
		{"m4a 直链", "https://cdn.example.com/a.m4a?uid=1&vkey=x", 0, true},
		{"mp3 直链", "https://cdn.example.com/a.MP3", 0, true},
		{"音频书里的插图", "https://cdn.example.com/img/cover.jpg", 1, false},
		{"HLS 播放列表", "https://cdn.example.com/index.m3u8?token=1", 1, false},
		{"无后缀签名直链（音频书）", "https://cdn.example.com/stream?token=1", 1, true},
		{"无后缀签名直链（文本/图片书）", "https://cdn.example.com/stream?token=1", 0, false},
		{"坏地址但书籍是音频", "://bad", 1, true},
	}
	for _, c := range cases {
		if got := IsAudioMediaURL(c.url, c.bookType); got != c.want {
			t.Errorf("%s: IsAudioMediaURL(%q, %d) = %v，期望 %v", c.name, c.url, c.bookType, got, c.want)
		}
	}
}

// TestAudioMediaCacheKeyIgnoresQuery 换票会改写 uid/vkey/ct，缓存键必须只看
// scheme + host + path，否则每次换票都会重新下载一遍。
func TestAudioMediaCacheKeyIgnoresQuery(t *testing.T) {
	a := audioMediaCacheKey("book-1", "https://cdn.example.com/long/a.m4a?uid=1&vkey=aaa")
	b := audioMediaCacheKey("book-1", "https://cdn.example.com/long/a.m4a?uid=2&vkey=bbb&ct=3")
	if a == "" || a != b {
		t.Fatalf("同一对象换票后缓存键应一致: %q vs %q", a, b)
	}
	if c := audioMediaCacheKey("book-2", "https://cdn.example.com/long/a.m4a"); c == a {
		t.Fatalf("不同书籍的缓存键不应相同")
	}
	if d := audioMediaCacheKey("book-1", "https://cdn.example.com/long/b.m4a"); d == a {
		t.Fatalf("不同对象的缓存键不应相同")
	}
}
