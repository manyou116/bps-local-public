package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/sunboyzoey/GPT-BPS/internal/gateway"
	"github.com/sunboyzoey/GPT-BPS/internal/proxy"
)

type standaloneLifecycleCredentials struct {
	standaloneTestCredentials
	visited chan struct{}
	once    sync.Once
}

func (s *standaloneLifecycleCredentials) Credentials() (string, string, error) {
	s.once.Do(func() { close(s.visited) })
	return s.standaloneTestCredentials.Credentials()
}

// startStandaloneLifecycleServer retries only bind conflicts in the short gap
// between reserving an ephemeral port and handing it to the real CLI listener.
func startStandaloneLifecycleServer(t *testing.T, upstream, stateDir string) (string, context.CancelFunc, <-chan error) {
	t.Helper()
	for attempt := 0; attempt < 4; attempt++ {
		reservation, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := reservation.Addr().String()
		if err := reservation.Close(); err != nil {
			t.Fatal(err)
		}
		store := &standaloneLifecycleCredentials{visited: make(chan struct{})}
		bridge, err := proxy.New(proxy.Config{
			Addr: addr, Upstream: upstream, UpstreamTransport: "ws", WSIdleTimeout: time.Minute,
		}, store)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		result, finished := make(chan error, 1), make(chan struct{})
		go func() {
			defer close(finished)
			result <- serveStandalone(ctx, bridge, store, stateDir, gateway.Limits{
				PerAccount: 1, Queue: 1, Wait: time.Minute,
			}, 0)
		}()
		t.Cleanup(func() {
			cancel()
			select {
			case <-finished:
			case <-time.After(10 * time.Second):
				t.Error("standalone listener did not stop during cleanup")
			}
		})
		client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 200 * time.Millisecond}
		t.Cleanup(client.CloseIdleConnections)
		deadline := time.NewTimer(5 * time.Second)
		tick := time.NewTicker(10 * time.Millisecond)
		retry := false
		for !retry {
			select {
			case err := <-result:
				deadline.Stop()
				tick.Stop()
				cancel()
				if errors.Is(err, syscall.EADDRINUSE) {
					retry = true
					continue
				}
				t.Fatalf("standalone listener exited before readiness: %v", err)
			case <-deadline.C:
				tick.Stop()
				t.Fatal("standalone listener did not become ready")
			case <-tick.C:
				response, err := client.Get("http://" + addr + "/healthz")
				if err != nil {
					continue
				}
				_, _ = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
				if response.StatusCode != http.StatusOK {
					continue
				}
				select {
				case <-store.visited:
					// The fake store proves this was our listener, not another
					// process that happened to obtain the reserved port.
					deadline.Stop()
					tick.Stop()
					return "http://" + addr, cancel, result
				default:
				}
			}
		}
	}
	t.Fatal("standalone listener repeatedly lost its reserved port")
	return "", nil, nil
}

func TestStandaloneLifecycleDrainsActiveWebSocketAndQueuedRequestBeforeUnlock(t *testing.T) {
	upstreamCtx, stopUpstream := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(stopUpstream)
	created, closed := make(chan struct{}, 1), make(chan struct{}, 1)
	peerErrors := make(chan error, 4)
	var attempts atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols: []string{"responses"}, OriginPatterns: []string{"bps.openai.com"},
		})
		if err != nil {
			peerErrors <- err
			return
		}
		defer conn.CloseNow()
		var body map[string]any
		if err := wsjson.Read(upstreamCtx, conn, &body); err != nil {
			peerErrors <- err
			return
		}
		if body["type"] != "response.create" {
			peerErrors <- errors.New("unexpected upstream request type")
			return
		}
		created <- struct{}{}
		// Hold generation open without a terminal event. Service cancellation
		// must close this socket rather than wait for a successful response.
		_, _, err = conn.Read(upstreamCtx)
		if err == nil {
			peerErrors <- errors.New("unexpected extra frame or automatic replay")
		} else if upstreamCtx.Err() != nil {
			peerErrors <- errors.New("mock deadline expired before service closed the websocket")
		}
		closed <- struct{}{}
	}))
	t.Cleanup(upstream.Close)
	stateDir := filepath.Join(t.TempDir(), "state")
	base, stopService, serviceResult := startStandaloneLifecycleServer(t, upstream.URL, stateDir)
	requestCtx, stopRequests := context.WithTimeout(context.Background(), 12*time.Second)
	t.Cleanup(stopRequests)
	client := &http.Client{Transport: &http.Transport{Proxy: nil}}
	t.Cleanup(client.CloseIdleConnections)
	type responseResult struct {
		status int
		body   string
		err    error
	}
	responses := make(chan responseResult, 3)
	send := func(index int) {
		payload := fmt.Sprintf(`{"model":"gpt-6-astra","input":"offline lifecycle %d","stream":true}`, index)
		req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, base+"/v1/responses", strings.NewReader(payload))
		if err != nil {
			responses <- responseResult{err: err}
			return
		}
		req.Header.Set("Content-Type", "application/json")
		response, err := client.Do(req)
		if err != nil {
			responses <- responseResult{err: err}
			return
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		responses <- responseResult{status: response.StatusCode, body: string(body), err: err}
	}
	go send(1)
	select {
	case <-created:
	case err := <-peerErrors:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("first request never reached the mock websocket")
	}
	// With one occupied slot and one waiting position, exactly one of these
	// requests queues and the other reports queue-full. No sleep is needed to
	// guess whether the queued request has entered admission.
	go send(2)
	go send(3)
	select {
	case response := <-responses:
		if response.err != nil || response.status != http.StatusTooManyRequests || !strings.Contains(response.body, "bps_queue_full") {
			t.Fatalf("expected queue-full proof of a waiting request: %+v", response)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiting position was not filled")
	}
	if competing, err := gateway.OpenStandaloneState(stateDir); err == nil {
		competing.Close()
		t.Fatal("live service released its task state lock")
	}
	if attempts.Load() != 1 {
		t.Fatal("queued request unexpectedly reached upstream")
	}
	stopService()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled service did not close its active websocket")
	}
	select {
	case err := <-serviceResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("service did not return after cancelling active and queued work")
	}
	for range 2 {
		select {
		case <-responses:
		case <-time.After(5 * time.Second):
			t.Fatal("an active or queued local request remained after service exit")
		}
	}
	state, err := gateway.OpenStandaloneState(stateDir)
	if err != nil {
		t.Fatal("task state remained locked after service cleanup", err)
	}
	state.Close()
	if attempts.Load() != 1 {
		t.Fatalf("expected one upstream attempt with no replay, got %d", attempts.Load())
	}
	select {
	case err := <-peerErrors:
		t.Fatal(err)
	default:
	}
}
