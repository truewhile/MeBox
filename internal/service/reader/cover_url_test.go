package reader

import "testing"

// 聚合类书源（如「光遇聚合」）在没有封面时会把搜索参数信封当封面返回，
// normalizeCoverURL 必须拦住这类非图片地址，避免前端 <img> 显示破图。
func TestNormalizeCoverURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"blank", "   ", ""},
		{"https", "https://img.example.com/a.jpg", "https://img.example.com/a.jpg"},
		{"http", "http://img.example.com/a.jpg", "http://img.example.com/a.jpg"},
		{"uppercase scheme", "HTTPS://img.example.com/a.jpg", "HTTPS://img.example.com/a.jpg"},
		{"trimmed", "  https://img.example.com/a.jpg  ", "https://img.example.com/a.jpg"},
		{"inline image data uri", "data:image/png;base64,iVBORw0KGgo=", "data:image/png;base64,iVBORw0KGgo="},
		{"local asset url", "/api/reader/local/asset?b=1&p=2&s=3", "/api/reader/local/asset?b=1&p=2&s=3"},
		{
			"search params envelope",
			"data:;base64,eyJrZXkiOiLlhajnkIPpq5jmraYiLCJ0YWIiOiLlsI/or7QiLCJzb3VyY2VzS2V5Ijoi5YWo6YOoIiwicGFnZSI6MSwiZGlzYWJsZWRfc291cmNlcyI6IjAifQ==",
			"",
		},
		{"text data uri", "data:text/plain;base64,aGk=", ""},
		{"source name", "光遇聚合", ""},
		{"javascript url", "javascript:alert(1)", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeCoverURL(tc.in); got != tc.want {
				t.Fatalf("normalizeCoverURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
