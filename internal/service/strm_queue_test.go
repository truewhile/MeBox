package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
	"github.com/truewhile/MeBox/internal/service/cloud"
	"github.com/truewhile/MeBox/internal/service/cloud115"
	"go.uber.org/zap"
)

func TestIs115Blocked(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{errors.New("115 接口返回 HTTP 405：<!doctypehtml>...访问被阻断"), true},
		{errors.New("115 接口返回 HTTP 405"), true},
		{errors.New("115 接口触发频控/安全拦截（HTTP 405）：阿里云 WAF 拦截页"), true},
		{errors.New("115 接口错误（770004）：访问频率过高"), true},
		{errors.New("115 接口错误（406）：达到访问上限"), true},
		{errors.New("下载失败：http 403"), false},
		{errors.New("解析下载地址失败：115 接口调用失败"), false},
		{errors.New("database is locked (517)"), false},
		{nil, false},
	}
	for _, c := range cases {
		if got := is115Blocked(c.err); got != c.want {
			t.Errorf("is115Blocked(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestIsHTTPDownloadFailure(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{errors.New("http 403"), true},
		{errors.New("http 404"), true},
		{errors.New("http 410"), true},
		{errors.New("http 500"), true},
		{errors.New("Get \"https://x\": connection refused"), false},
		{nil, false},
	}
	for _, c := range cases {
		if got := isHTTPDownloadFailure(c.err); got != c.want {
			t.Errorf("isHTTPDownloadFailure(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestStrmUploadTasksClearAndRetry(t *testing.T) {
	db := newServiceTestDB(t, &model.StrmUploadTask{})
	repos := repository.New(db)
	svc := NewStrmService(nil, zap.NewNop(), repos, nil)
	ctx := context.Background()

	tasks := []*model.StrmUploadTask{
		{Base: model.Base{ID: "task-pending"}, Status: model.StrmTaskPending, FileName: "1.nfo"},
		{Base: model.Base{ID: "task-running"}, Status: model.StrmTaskRunning, FileName: "2.nfo"},
		{Base: model.Base{ID: "task-done"}, Status: model.StrmTaskDone, FileName: "3.nfo"},
		{Base: model.Base{ID: "task-failed"}, Status: model.StrmTaskFailed, FileName: "4.nfo", Error: "some error", RetryCount: 3},
		{Base: model.Base{ID: "task-canceled"}, Status: model.StrmTaskCanceled, FileName: "5.nfo"},
	}
	for _, task := range tasks {
		if err := db.Create(task).Error; err != nil {
			t.Fatalf("failed to insert task: %v", err)
		}
	}

	// 1. RetryAllFailedUploadTasks
	retried, err := svc.RetryAllFailedUploadTasks(ctx)
	if err != nil {
		t.Fatalf("RetryAllFailedUploadTasks failed: %v", err)
	}
	if retried != 1 {
		t.Fatalf("expected 1 retried task, got %d", retried)
	}
	var failedTask model.StrmUploadTask
	if err := db.First(&failedTask, "id = ?", "task-failed").Error; err != nil {
		t.Fatalf("failed to get task-failed: %v", err)
	}
	if failedTask.Status != model.StrmTaskPending || failedTask.Error != "" || failedTask.RetryCount != 0 {
		t.Fatalf("task-failed was not reset properly: %+v", failedTask)
	}

	// 再次改为 failed 以便测试 ClearFinished
	db.Model(&model.StrmUploadTask{}).Where("id = ?", "task-failed").Updates(map[string]any{"status": model.StrmTaskFailed})

	// 2. ClearFinishedUploadTasks 应删除 done, failed, canceled 三条历史记录
	deleted, err := svc.ClearFinishedUploadTasks(ctx)
	if err != nil {
		t.Fatalf("ClearFinishedUploadTasks failed: %v", err)
	}
	if deleted != 3 {
		t.Fatalf("expected 3 deleted tasks (done, failed, canceled), got %d", deleted)
	}

	// 验证剩余的任务只有 pending 和 running
	var count int64
	db.Model(&model.StrmUploadTask{}).Count(&count)
	if count != 2 {
		t.Fatalf("expected 2 remaining tasks, got %d", count)
	}
}

func TestDownloadSemSeparateByProvider(t *testing.T) {
	svc := NewStrmService(nil, zap.NewNop(), nil, nil)
	svc.initDownloadSems(6)

	if cap(svc.downloadSem115) != strm115DownloadSemCap {
		t.Fatalf("115 sem cap = %d, want %d", cap(svc.downloadSem115), strm115DownloadSemCap)
	}
	if cap(svc.downloadSemDAV) != 6 {
		t.Fatalf("dav sem cap = %d, want 6", cap(svc.downloadSemDAV))
	}

	ctx := context.Background()
	for i := 0; i < strm115DownloadSemCap; i++ {
		if !svc.acquireDownloadSlot(ctx, model.StrmProvider115) {
			t.Fatalf("failed to acquire 115 slot %d", i)
		}
	}
	// 115 槽位占满后，OpenList 仍应能独立获取槽位。
	if !svc.acquireDownloadSlot(ctx, model.StrmProviderOpenList) {
		t.Fatal("openlist slot should remain available while 115 slots are full")
	}
	for i := 0; i < 5; i++ {
		if !svc.acquireDownloadSlot(ctx, model.StrmProviderOpenList) {
			t.Fatalf("failed to acquire extra openlist slot %d", i)
		}
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if svc.acquireDownloadSlot(timeoutCtx, model.StrmProviderOpenList) {
		t.Fatal("expected openlist slots to be exhausted after 6 acquisitions")
	}
}

func TestRequeueDownloadTask(t *testing.T) {
	db := newServiceTestDB(t, &model.StrmDownloadTask{})
	repos := repository.New(db)
	svc := NewStrmService(nil, zap.NewNop(), repos, nil)
	now := time.Now()
	task := &model.StrmDownloadTask{
		Base:      model.Base{ID: "dl-running"},
		Status:    model.StrmTaskRunning,
		Provider:  model.StrmProviderOpenList,
		StartedAt: &now,
	}
	if err := db.Create(task).Error; err != nil {
		t.Fatalf("insert task: %v", err)
	}

	svc.requeueDownloadTask(task)

	var got model.StrmDownloadTask
	if err := db.First(&got, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("load task: %v", err)
	}
	if got.Status != model.StrmTaskPending || got.StartedAt != nil {
		t.Fatalf("task not requeued: %+v", got)
	}
}

// TestProcessUpload115DeletesStaleRemoteMetaFirst 验证 115 覆盖上传语义（以本地为准）：
// 探活未命中时，任务携带网盘旧文件 ID 必须先 /open/ufile/delete 再上传；
// 上传成功后 RemoteRef 回写为新 file_id。
func TestProcessUpload115DeletesStaleRemoteMetaFirst(t *testing.T) {
	svc := testStrmService(t)
	localDir := t.TempDir()
	localFile := filepath.Join(localDir, "movie.nfo")
	if err := os.WriteFile(localFile, []byte("local-nfo-data"), 0o644); err != nil {
		t.Fatal(err)
	}

	acct := &model.StrmAccount{Name: "fake115", Provider: "cloud115", Config: "{}", Enabled: true}
	if err := svc.repo.StrmAccount.Create(context.Background(), acct); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var calls []string
	deleteForm := map[string]string{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/open/ufile/files":
			calls = append(calls, "probe")
			// 探活：目录为空 / 无同内容副本 → 继续删旧上传
			w.Write([]byte(`{"state":true,"data":[]}`))
		case "/open/ufile/delete":
			calls = append(calls, "delete")
			deleteForm["file_ids"] = r.FormValue("file_ids")
			deleteForm["parent_id"] = r.FormValue("parent_id")
			w.Write([]byte(`{"state":true,"data":[]}`))
		case "/open/upload/init":
			calls = append(calls, "upload")
			// 返回秒传成功，跳过 OSS 真实上传
			w.Write([]byte(`{"state":true,"data":{"status":2,"file_id":"new-1","pick_code":"new-pc-1","callback":null}}`))
		default:
			t.Errorf("unexpected 115 api path %s", r.URL.Path)
			w.Write([]byte(`{"state":false,"message":"unexpected path"}`))
		}
	}))
	defer api.Close()
	oldPro := cloud115.ProAPIBase
	cloud115.ProAPIBase = api.URL
	defer func() { cloud115.ProAPIBase = oldPro }()

	task := &model.StrmUploadTask{
		Base:       model.Base{ID: "up-del-1"},
		SyncPathID: "p1",
		AccountID:  acct.ID,
		Provider:   model.StrmProvider115,
		FileName:   "movie.nfo",
		LocalPath:  localFile,
		RemotePath: "777",
		RemoteRef:  "old-file-1",
		Status:     model.StrmTaskRunning,
	}
	svc.processUpload115(context.Background(), task)

	if task.Status != model.StrmTaskDone {
		t.Fatalf("upload task should succeed, status = %s, error = %s", task.Status, task.Error)
	}
	if task.RemoteRef != "new-1" {
		t.Fatalf("successful upload should record new file_id in RemoteRef, got %q", task.RemoteRef)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 3 || calls[0] != "probe" || calls[1] != "delete" || calls[2] != "upload" {
		t.Fatalf("expected probe→delete→upload, got calls = %v", calls)
	}
	if deleteForm["file_ids"] != "old-file-1" {
		t.Fatalf("delete file_ids = %q, want old-file-1", deleteForm["file_ids"])
	}
	if deleteForm["parent_id"] != "777" {
		t.Fatalf("delete parent_id = %q, want 777", deleteForm["parent_id"])
	}
}

// TestProcessUpload115SkipsWhenProbeFindsSameContent 验证上传/重试前探活命中同内容副本时跳过上传，
// 并清理其它脏副本，RemoteRef 回写为已存在的 file_id。
func TestProcessUpload115SkipsWhenProbeFindsSameContent(t *testing.T) {
	svc := testStrmService(t)
	localDir := t.TempDir()
	localFile := filepath.Join(localDir, "movie.nfo")
	content := []byte("already-on-115")
	if err := os.WriteFile(localFile, content, 0o644); err != nil {
		t.Fatal(err)
	}
	sha, err := cloud115.FileSHA1(localFile)
	if err != nil {
		t.Fatal(err)
	}

	acct := &model.StrmAccount{Name: "fake115", Provider: "cloud115", Config: "{}", Enabled: true}
	if err := svc.repo.StrmAccount.Create(context.Background(), acct); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var calls []string
	deleteForm := map[string]string{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/open/ufile/files":
			calls = append(calls, "probe")
			w.Write([]byte(fmt.Sprintf(
				`{"state":true,"data":[
					{"fid":"keep-1","fc":"1","fn":"movie.nfo","fs":%d,"sha1":%q,"fta":"1"},
					{"fid":"dirty-2","fc":"1","fn":"movie.nfo","fs":9,"sha1":"DEADBEEF","fta":"1"}
				]}`, len(content), sha)))
		case "/open/ufile/delete":
			calls = append(calls, "delete")
			deleteForm["file_ids"] = r.FormValue("file_ids")
			w.Write([]byte(`{"state":true,"data":[]}`))
		case "/open/upload/init":
			calls = append(calls, "upload")
			w.Write([]byte(`{"state":true,"data":{"status":2,"file_id":"should-not","pick_code":"x","callback":null}}`))
		default:
			t.Errorf("unexpected 115 api path %s", r.URL.Path)
			w.Write([]byte(`{"state":false,"message":"unexpected path"}`))
		}
	}))
	defer api.Close()
	oldPro := cloud115.ProAPIBase
	cloud115.ProAPIBase = api.URL
	defer func() { cloud115.ProAPIBase = oldPro }()

	task := &model.StrmUploadTask{
		Base:       model.Base{ID: "up-skip-1"},
		SyncPathID: "p1",
		AccountID:  acct.ID,
		Provider:   model.StrmProvider115,
		FileName:   "movie.nfo",
		LocalPath:  localFile,
		RemotePath: "777",
		RemoteRef:  "old-ref",
		Size:       int64(len(content)),
		Status:     model.StrmTaskRunning,
	}
	svc.processUpload115(context.Background(), task)

	if task.Status != model.StrmTaskDone {
		t.Fatalf("probe hit should finish done, status=%s err=%s", task.Status, task.Error)
	}
	if task.RemoteRef != "keep-1" {
		t.Fatalf("RemoteRef should be matched file_id keep-1, got %q", task.RemoteRef)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 || calls[0] != "probe" || calls[1] != "delete" {
		t.Fatalf("expected probe→delete (no upload), got %v", calls)
	}
	// 脏副本 dirty-2 与任务旧 ref 都应被清理，keep-1 不得出现
	ids := strings.Split(deleteForm["file_ids"], ",")
	idSet := map[string]bool{}
	for _, id := range ids {
		idSet[strings.TrimSpace(id)] = true
	}
	if !idSet["dirty-2"] || !idSet["old-ref"] {
		t.Fatalf("delete should include dirty-2 and old-ref, got %q", deleteForm["file_ids"])
	}
	if idSet["keep-1"] {
		t.Fatalf("must not delete matched keep-1, got %q", deleteForm["file_ids"])
	}
}

// TestBatchResolve115Links 验证下载队列的批量换链：同账号多个 115 任务的
// pickcode 合并为一次 downurl 请求（官方接口支持逗号分隔多 pick_code），
// 重复引用去重、非 115 任务不参与、直链携带绑定 UA。
func TestBatchResolve115Links(t *testing.T) {
	svc := testStrmService(t)
	acct := &model.StrmAccount{Name: "fake115", Provider: model.StrmProvider115, Config: "{}", Enabled: true}
	if err := svc.repo.StrmAccount.Create(context.Background(), acct); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var requests []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open/ufile/downurl":
			_ = r.ParseForm()
			mu.Lock()
			requests = append(requests, r.PostFormValue("pick_code"))
			mu.Unlock()
			w.Write([]byte(`{"state":true,"data":{
				"111":{"pick_code":"q-pc-a","url":{"url":"http://cdn/a.mkv"}},
				"222":{"pick_code":"q-pc-b","url":{"url":"http://cdn/b.jpg"}}}}`))
		default:
			t.Errorf("unexpected 115 api path %s", r.URL.Path)
			w.Write([]byte(`{"state":false,"message":"unexpected path"}`))
		}
	}))
	defer api.Close()
	oldPro := cloud115.ProAPIBase
	cloud115.ProAPIBase = api.URL
	defer func() { cloud115.ProAPIBase = oldPro }()
	defer cloud115.ClearDownloadURLCache("q-pc-a")
	defer cloud115.ClearDownloadURLCache("q-pc-b")

	tasks := []*model.StrmDownloadTask{
		{SyncPathID: "p1", AccountID: acct.ID, Provider: model.StrmProvider115, RemoteRef: "q-pc-a"},
		{SyncPathID: "p1", AccountID: acct.ID, Provider: model.StrmProvider115, RemoteRef: "q-pc-a"}, // 重复引用
		{SyncPathID: "p1", AccountID: acct.ID, Provider: model.StrmProvider115, RemoteRef: "q-pc-b"},
		{SyncPathID: "p1", AccountID: acct.ID, Provider: model.StrmProviderOpenList, RemoteRef: "ol-ref"},
		{SyncPathID: "p1", AccountID: acct.ID, Provider: model.StrmProvider115, RemoteRef: "   "}, // 空引用
	}
	resolved := svc.batchResolve115Links(context.Background(), tasks)

	if got := resolved[dlResolveKey(acct.ID, "q-pc-a")]; got == nil || got.URL != "http://cdn/a.mkv" {
		t.Fatalf("missing/bad link for q-pc-a: %+v", resolved)
	}
	if got := resolved[dlResolveKey(acct.ID, "q-pc-b")]; got == nil || got.URL != "http://cdn/b.jpg" {
		t.Fatalf("missing/bad link for q-pc-b: %+v", resolved)
	}
	if got := resolved[dlResolveKey(acct.ID, "q-pc-a")].Headers["User-Agent"]; got != cloud115.DefaultUA {
		t.Fatalf("link UA = %q, want default bound UA", got)
	}
	if _, ok := resolved[dlResolveKey(acct.ID, "ol-ref")]; ok {
		t.Fatal("non-115 task should not be batch resolved")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 1 || requests[0] != "q-pc-a,q-pc-b" {
		t.Fatalf("expected single batched downurl request, got %v", requests)
	}
}

// TestProcessDownloadTaskUsesPreResolvedLink 验证任务执行时优先使用批量换链
// 预取的直链：downurl 接口保持失败，若任务仍走逐个换链则必然失败。
func TestProcessDownloadTaskUsesPreResolvedLink(t *testing.T) {
	svc := testStrmService(t)
	localDir := t.TempDir()
	acct := &model.StrmAccount{Name: "fake115", Provider: model.StrmProvider115, Config: "{}", Enabled: true}
	if err := svc.repo.StrmAccount.Create(context.Background(), acct); err != nil {
		t.Fatal(err)
	}

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer api.Close()
	oldPro := cloud115.ProAPIBase
	cloud115.ProAPIBase = api.URL
	defer func() { cloud115.ProAPIBase = oldPro }()

	contentSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != cloud115.DefaultUA {
			t.Errorf("download UA = %q, want bound default UA", r.Header.Get("User-Agent"))
		}
		_, _ = w.Write([]byte("nfo-content"))
	}))
	defer contentSrv.Close()

	task := &model.StrmDownloadTask{
		SyncPathID: "p1",
		AccountID:  acct.ID,
		Provider:   model.StrmProvider115,
		FileName:   "movie.nfo",
		RemoteRef:  "q-pc-pre",
		LocalPath:  filepath.Join(localDir, "movie.nfo"),
		Status:     model.StrmTaskRunning,
	}
	if err := svc.repo.StrmDownload.Create(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	resolved := map[string]*cloud.DirectLink{
		dlResolveKey(acct.ID, "q-pc-pre"): {URL: contentSrv.URL, Headers: map[string]string{"User-Agent": cloud115.DefaultUA}},
	}
	svc.processDownloadTask(context.Background(), task, resolved)

	if task.Status != model.StrmTaskDone {
		t.Fatalf("task should be done via pre-resolved link, status = %s, error = %s", task.Status, task.Error)
	}
	data, err := os.ReadFile(task.LocalPath)
	if err != nil || string(data) != "nfo-content" {
		t.Fatalf("downloaded file mismatch: err = %v, data = %q", err, string(data))
	}
}
