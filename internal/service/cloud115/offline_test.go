package cloud115

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// newOfflineTestClient 构造一个指向 mock server 的客户端。
// 令牌直接写入内存字段，避免走文件/数据库。
func newOfflineTestClient(accessToken string) *OpenClient {
	return NewOpenClient("test-app", accessToken, "")
}

func TestAddTaskURLsUsesMultipartAndBatches(t *testing.T) {
	mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != OfflinePathAddTaskURLs {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "multipart/form-data") {
			t.Errorf("expected multipart content-type, got %q", ct)
		}
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") || len(got) <= len("Bearer ") {
			t.Errorf("unexpected authorization %q", got)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		if got := r.FormValue("wp_path_id"); got != "12345" {
			t.Errorf("wp_path_id = %q, want 12345", got)
		}
		urls := r.FormValue("urls")
		if want := "magnet:?xt=urn:btih:AAA\ned2k://|file|x|1|H|/"; urls != want {
			t.Errorf("urls = %q, want %q", urls, want)
		}
		_, _ = w.Write([]byte(`{"state":true,"code":0,"data":[{"state":true,"code":0,"info_hash":"ABC","url":"magnet:?xt=urn:btih:AAA"}]}`))
	})

	client := newOfflineTestClient("token-abc")
	res, err := client.AddTaskURLs(context.Background(), []string{
		"  magnet:?xt=urn:btih:AAA  ",
		"ed2k://|file|x|1|H|/",
	}, "12345")
	if err != nil {
		t.Fatalf("AddTaskURLs: %v", err)
	}
	if len(res) != 1 || !res[0].State || res[0].InfoHash != "ABC" {
		t.Fatalf("unexpected result %+v", res)
	}
}

func TestAddTaskURLsRejectsOverLimitWithoutRequest(t *testing.T) {
	called := false
	mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
	})
	urls := make([]string, OfflineMaxURLsPerBatch+1)
	for i := range urls {
		urls[i] = "https://example.com/a"
	}
	if _, err := newOfflineTestClient("t").AddTaskURLs(context.Background(), urls, "1"); err == nil {
		t.Fatal("expected error for over-limit batch")
	}
	if called {
		t.Fatal("must not call 115 when batch exceeds the documented limit")
	}
}

func TestAddTaskURLsSurfacesBusinessError(t *testing.T) {
	mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"state":false,"code":1000012,"message":"云下载配额已用完"}`))
	})
	_, err := newOfflineTestClient("t").AddTaskURLs(context.Background(), []string{"magnet:?xt=urn:btih:AAA"}, "1")
	if err == nil {
		t.Fatal("expected business error")
	}
	apiErr, ok := err.(*OpenAPIError)
	if !ok {
		t.Fatalf("expected *OpenAPIError, got %T", err)
	}
	if apiErr.Code != OfflineCodeQuotaUsedUp {
		t.Fatalf("code = %d, want %d", apiErr.Code, OfflineCodeQuotaUsedUp)
	}
}

func TestParseTorrentDecodesFileList(t *testing.T) {
	mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != OfflinePathTorrent {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		if r.FormValue("torrent_sha1") != "SHA1" || r.FormValue("pick_code") != "PICK" {
			t.Errorf("unexpected form: %v", r.MultipartForm.Value)
		}
		_, _ = w.Write([]byte(`{"state":true,"code":0,"data":{
			"file_size":1024,"torrent_name":"Demo","file_count":2,"info_hash":"HASH",
			"torrent_filelist":[{"size":512,"path":"a.mkv","wanted":1},{"size":512,"path":"b.srt","wanted":0}]}}`))
	})
	info, err := newOfflineTestClient("t").ParseTorrent(context.Background(), "SHA1", "PICK")
	if err != nil {
		t.Fatalf("ParseTorrent: %v", err)
	}
	if info.InfoHash != "HASH" || info.Name != "Demo" || len(info.Files) != 2 {
		t.Fatalf("unexpected info %+v", info)
	}
	if info.Files[0].Path != "a.mkv" || info.Files[0].Size != 512 {
		t.Fatalf("unexpected file %+v", info.Files[0])
	}
}

func TestAddTaskBTRequiresSeedFields(t *testing.T) {
	called := false
	mockAPI(t, func(w http.ResponseWriter, r *http.Request) { called = true })
	client := newOfflineTestClient("t")
	if err := client.AddTaskBT(context.Background(), OfflineBTRequest{InfoHash: "H"}); err == nil {
		t.Fatal("expected validation error")
	}
	if called {
		t.Fatal("must validate locally before calling 115")
	}
}

func TestGetOfflineTaskListParsesCamelCasePercentDone(t *testing.T) {
	mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != OfflinePathTaskList {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.URL.Query().Get("page") != "2" {
			t.Errorf("page = %q, want 2", r.URL.Query().Get("page"))
		}
		_, _ = w.Write([]byte(`{"state":true,"code":0,"data":{"page":2,"page_count":3,"count":30,
			"tasks":[{"info_hash":"H","percentDone":42,"size":100,"name":"n","status":1,"url":"u","wp_path_id":"9"}]}}`))
	})
	page, err := newOfflineTestClient("t").GetOfflineTaskList(context.Background(), 2)
	if err != nil {
		t.Fatalf("GetOfflineTaskList: %v", err)
	}
	if page.Page != 2 || page.PageCount != 3 || len(page.Tasks) != 1 {
		t.Fatalf("unexpected page %+v", page)
	}
	// 官方字段是驼峰 percentDone：写成 snake_case 会永远 0，这里必须锁死。
	if page.Tasks[0].PercentDone != 42 {
		t.Fatalf("percentDone = %d, want 42", page.Tasks[0].PercentDone)
	}
	if page.Tasks[0].Status != OfflineTaskStatusDownloading {
		t.Fatalf("status = %d", page.Tasks[0].Status)
	}
}

func TestGetOfflineQuota(t *testing.T) {
	mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != OfflinePathQuotaInfo {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"state":true,"code":0,"data":{"package":[{"surplus":3,"used":1,"count":4,"name":"p"}],"count":4,"surplus":3,"used":1}}`))
	})
	q, err := newOfflineTestClient("t").GetOfflineQuota(context.Background())
	if err != nil {
		t.Fatalf("GetOfflineQuota: %v", err)
	}
	if q.Surplus != 3 || q.Count != 4 || len(q.Package) != 1 {
		t.Fatalf("unexpected quota %+v", q)
	}
}
