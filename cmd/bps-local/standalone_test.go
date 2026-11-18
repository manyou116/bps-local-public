package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sunboyzoey/GPT-BPS/internal/gateway"
	"github.com/sunboyzoey/GPT-BPS/internal/proxy"
)

type standaloneTestCredentials struct{}

func (standaloneTestCredentials) Credentials() (string, string, error) {
	return "offline-token", "offline-account", nil
}
func (standaloneTestCredentials) Expiry() time.Time { return time.Time{} }

func TestStandaloneLifecycleReleasesStateAfterListenerStops(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state")
	var previousKey string
	for range 2 {
		bridge, err := proxy.New(proxy.Config{Addr: "127.0.0.1:0"}, standaloneTestCredentials{})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := serveStandalone(ctx, bridge, standaloneTestCredentials{}, path, gateway.DefaultLimits(), 0); err != nil {
			t.Fatal(err)
		}
		state, err := gateway.OpenStandaloneState(path)
		if err != nil {
			t.Fatal("state still locked after listener exit", err)
		}
		if previousKey != "" && previousKey != state.Key() {
			t.Error("restart changed task namespace")
		}
		previousKey = state.Key()
		state.Close()
	}
}

func TestStandaloneInvalidLimitsDoNotCreateState(t *testing.T) {
	for _, limits := range []gateway.Limits{
		{PerAccount: 0, Wait: time.Second},
		{PerAccount: 10001, Wait: time.Second},
		{PerAccount: 4, Queue: -1, Wait: time.Second},
		{PerAccount: 4, Wait: 0},
	} {
		path := filepath.Join(t.TempDir(), "state")
		if err := serveStandalone(context.Background(), nil, nil, path, limits, 0); err == nil {
			t.Fatal("invalid limits accepted")
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("invalid invocation wrote task state")
		}
	}
}

func TestStandaloneStateNamespaceDoesNotFollowAuthFile(t *testing.T) {
	before, err := standaloneStateDir("")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("BPS_AUTH_FILE", filepath.Join(t.TempDir(), "other-auth.json"))
	after, err := standaloneStateDir("")
	if err != nil || before != after {
		t.Fatal("changing authorization path rotated task state")
	}
}
