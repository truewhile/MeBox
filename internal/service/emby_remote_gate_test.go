package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
)

// 远程 Emby 的并发闸门必须真的把在途请求数压在上限之内：第三方客户端首页会为
// 每个远程媒体库各请求一次 /Items/Latest，几十个库就是几十路并发。
func TestRemoteGateLimitsConcurrentRequests(t *testing.T) {
	const gate = 3
	const requests = 12

	var mu sync.Mutex
	inflight, peak := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		inflight++
		if inflight > peak {
			peak = inflight
		}
		mu.Unlock()

		time.Sleep(20 * time.Millisecond)

		mu.Lock()
		inflight--
		mu.Unlock()
		_, _ = w.Write([]byte(`{"Items":[]}`))
	}))
	defer srv.Close()

	svc := &EmbyRemoteService{http: srv.Client(), remoteGate: make(chan struct{}, gate)}

	var wg sync.WaitGroup
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/Items/Latest", nil)
			if err != nil {
				return
			}
			if _, _, err := svc.fetchRemoteBody(context.Background(), req, "/Items/Latest"); err != nil {
				t.Errorf("fetchRemoteBody: %v", err)
			}
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if peak == 0 {
		t.Fatal("test server never saw a request")
	}
	if peak > gate {
		t.Fatalf("peak concurrency = %d, want <= %d", peak, gate)
	}
}

// 闸门排队时要响应请求取消，不能把整个 HTTP 请求挂死。
func TestRemoteGateHonoursContextCancellation(t *testing.T) {
	svc := &EmbyRemoteService{remoteGate: make(chan struct{}, 1)}
	svc.remoteGate <- struct{}{} // 占满名额

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.enterRemoteGate(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}

	// 释放名额后必须能正常拿到。
	<-svc.remoteGate
	release, err := svc.enterRemoteGate(context.Background())
	if err != nil {
		t.Fatalf("enterRemoteGate after release: %v", err)
	}
	release()
	if len(svc.remoteGate) != 0 {
		t.Fatalf("gate leaked a permit: len = %d", len(svc.remoteGate))
	}
}

// 未配置闸门（测试里直接构造结构体）时不应限流，保持旧行为。
func TestRemoteGateAbsentIsUnlimited(t *testing.T) {
	svc := &EmbyRemoteService{}
	release, err := svc.enterRemoteGate(context.Background())
	if err != nil {
		t.Fatalf("enterRemoteGate: %v", err)
	}
	release()
}

// 生产路径构造出来的服务必须带闸门，否则上面的限制形同虚设。
func TestNewEmbyRemoteServiceInitialisesGate(t *testing.T) {
	svc := NewEmbyRemoteService(&config.Config{}, zap.NewNop(), nil, nil)
	if svc.remoteGate == nil {
		t.Fatal("remoteGate must be initialised by the constructor")
	}
	if cap(svc.remoteGate) != embyRemoteConcurrencyLimit {
		t.Fatalf("gate capacity = %d, want %d", cap(svc.remoteGate), embyRemoteConcurrencyLimit)
	}
}
