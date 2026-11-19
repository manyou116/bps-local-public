package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sunboyzoey/GPT-BPS/internal/gateway"
	"github.com/sunboyzoey/GPT-BPS/internal/proxy"
)

var fixtureKeyA = strings.Repeat("a", 64)
var fixtureKeyB = strings.Repeat("b", 64)

func writeAccountsFixture(t *testing.T, dir, name, value string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func routeFixture(id, auth, key string) string {
	raw, _ := json.Marshal(map[string]string{"auth": auth, "api_key": key})
	return string(raw)
}

func TestLoadAccountRoutesRejectsAmbiguousConfig(t *testing.T) {
	a := routeFixture("a", "auth-a.json", fixtureKeyA)
	cases := map[string]string{
		"duplicate key":  a + "," + routeFixture("b", "auth-b.json", fixtureKeyA),
		"duplicate auth": a + "," + routeFixture("b", "./auth-a.json", fixtureKeyB),
		"short key":      routeFixture("a", "auth-a.json", "replace-me"),
		"missing auth":   routeFixture("a", "", fixtureKeyA),
		"empty":          "",
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeAccountsFixture(t, t.TempDir(), "accounts.json", `{"accounts":[`+entries+`]}`)
			if _, err := loadAccountRoutes(path); err == nil || strings.Contains(err.Error(), fixtureKeyA) {
				t.Fatal("expected an error without API key material")
			}
		})
	}
}

func TestAccountsRoutesUseOnlySelectedOAuth(t *testing.T) {
	dir := t.TempDir()
	path := writeAccountsFixture(t, dir, "accounts.json", `{"accounts":[`+
		routeFixture("a", "auth-a.json", fixtureKeyA)+","+routeFixture("b", "auth-b.json", fixtureKeyB)+`]}`)
	writeAccountsFixture(t, dir, "auth-a.json", `{"account_id":"acct-a","access_token":"mock-token-a"}`)
	writeAccountsFixture(t, dir, "auth-b.json", fmt.Sprintf(`{"account_id":"acct-b","access_token":"mock-token-b","expires_at":%d}`, time.Now().Add(time.Minute).Unix()))
	routes, err := loadAccountRoutes(path)
	if err != nil {
		t.Fatal(err)
	}
	upstreamRequests := make(chan [2]string, 8)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamRequests <- [2]string{r.Header.Get("Authorization"), r.Header.Get("Chatgpt-Account-Id")}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n")
	}))
	defer upstream.Close()
	state := filepath.Join(dir, "state")
	h, closeAccounts, err := openAccounts(routes, proxy.Config{Upstream: upstream.URL, UpstreamTransport: "sse"}, state,
		gateway.Limits{PerAccount: 1, Wait: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer closeAccounts()
	for _, id := range []string{"1", "2"} {
		if _, err := os.Stat(filepath.Join(state, "account-"+id, "task.key")); err != nil {
			t.Fatal(err)
		}
	}
	post := func(path string, auth ...string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"gpt-6-astra","input":"offline routing test","stream":true}`))
		for _, header := range auth {
			r.Header.Add("Authorization", header)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, headers := range [][]string{nil, {"Bearer " + strings.Repeat("z", 64)}, {"Bearer " + fixtureKeyA, "Bearer " + fixtureKeyB}} {
		if w := post("/v1/responses", headers...); w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "invalid_api_key") {
			t.Fatalf("unexpected unauthorized response: %d", w.Code)
		}
	}
	for _, tc := range []struct{ path, key, suffix string }{{"/v1/responses", fixtureKeyA, "a"}, {"/responses", fixtureKeyB, "b"}} {
		if w := post(tc.path, "Bearer "+tc.key); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "response.completed") {
			t.Fatalf("unexpected proxied response: %d %s", w.Code, w.Body.String())
		}
		select {
		case got := <-upstreamRequests:
			if got != [2]string{"Bearer mock-token-" + tc.suffix, "acct-" + tc.suffix} {
				t.Fatal("request used the wrong upstream OAuth credentials")
			}
		default:
			t.Fatal("expected a mock upstream request")
		}
	}
	writeAccountsFixture(t, dir, "auth-b.json", `{"account_id":"acct-b","access_token":"mock-token-b","expires_at":1}`)
	if w := post("/v1/responses", "Bearer "+fixtureKeyB); w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "basispoints_auth_unavailable") {
		t.Fatalf("failed account did not return its own credential error: %d", w.Code)
	}
	if len(upstreamRequests) != 0 {
		t.Fatal("unexpected extra upstream request")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != http.StatusOK || w.Body.String() != `{"ok":true}` {
		t.Fatal("health should report liveness without account details")
	}
}
