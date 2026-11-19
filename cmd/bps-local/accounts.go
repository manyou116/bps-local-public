package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sunboyzoey/GPT-BPS/internal/auth"
	"github.com/sunboyzoey/GPT-BPS/internal/gateway"
	"github.com/sunboyzoey/GPT-BPS/internal/proxy"
)

var routeKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{32,256}$`)

type accountRoute struct {
	id, authPath string
	digest       [sha256.Size]byte
}

func loadAccountRoutes(path string) ([]accountRoute, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	const maxBytes = 1 << 20
	raw, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxBytes {
		return nil, errors.New("account configuration exceeds 1 MiB")
	}
	var config struct {
		Accounts []struct {
			Auth   string `json:"auth"`
			APIKey string `json:"api_key"`
		} `json:"accounts"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&config) != nil || decoder.Decode(new(json.RawMessage)) != io.EOF {
		return nil, errors.New("invalid accounts JSON; expected an accounts array with auth and api_key")
	}
	if len(config.Accounts) == 0 {
		return nil, errors.New("accounts configuration must contain at least one account")
	}
	routes := make([]accountRoute, 0, len(config.Accounts))
	paths := make(map[string]bool)
	keys := make(map[[sha256.Size]byte]bool)
	for i, account := range config.Accounts {
		if !routeKeyPattern.MatchString(account.APIKey) {
			return nil, fmt.Errorf("accounts entry %d: api_key must be 32-256 characters, using only letters, digits, underscore and hyphen", i+1)
		}
		digest := sha256.Sum256([]byte(account.APIKey))
		if keys[digest] {
			return nil, fmt.Errorf("accounts entry %d: duplicate api_key", i+1)
		}
		if strings.TrimSpace(account.Auth) == "" {
			return nil, fmt.Errorf("accounts entry %d: auth file is required", i+1)
		}
		authPath := filepath.Clean(account.Auth)
		if !filepath.IsAbs(authPath) {
			authPath = filepath.Join(filepath.Dir(path), authPath)
		}
		if paths[authPath] {
			return nil, fmt.Errorf("accounts entry %d: duplicate auth file path", i+1)
		}
		id := fmt.Sprintf("%d", i+1)
		keys[digest], paths[authPath] = true, true
		routes = append(routes, accountRoute{id: id, authPath: authPath, digest: digest})
	}
	return routes, nil
}

type accountsHandler map[[sha256.Size]byte]http.Handler

func (h accountsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"ok":true}`)
		}
		return
	}
	values := r.Header.Values("Authorization")
	if len(values) == 1 {
		scheme, key, ok := strings.Cut(values[0], " ")
		if ok && strings.EqualFold(scheme, "Bearer") && routeKeyPattern.MatchString(key) {
			if handler := h[sha256.Sum256([]byte(key))]; handler != nil {
				request := r.Clone(r.Context())
				request.Header.Del("Authorization")
				handler.ServeHTTP(w, request)
				return
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("WWW-Authenticate", "Bearer")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = io.WriteString(w, `{"error":{"code":"invalid_api_key","message":"Invalid or missing API key"}}`)
}

func openAccounts(routes []accountRoute, cfg proxy.Config, root string, limits gateway.Limits) (accountsHandler, func(), error) {
	var accounts []*standaloneAccount
	closeAll := func() { closeStandaloneAccounts(accounts) }
	handlers := make(accountsHandler, len(routes))
	for _, route := range routes {
		store, err := auth.Load(route.authPath)
		if err != nil {
			closeAll()
			return nil, nil, fmt.Errorf("account %q: could not load OAuth credentials: %w", route.id, err)
		}
		bridge, err := proxy.New(cfg, store)
		if err == nil {
			var account *standaloneAccount
			account, err = openStandaloneAccount(bridge, store, filepath.Join(root, "account-"+route.id), limits)
			if err == nil {
				accounts = append(accounts, account)
				handlers[route.digest] = account.handler
				continue
			}
		}
		closeAll()
		return nil, nil, fmt.Errorf("account %q: %w", route.id, err)
	}
	return handlers, closeAll, nil
}

func serveAccounts(ctx context.Context, path, dir string, cfg proxy.Config, limits gateway.Limits) error {
	routes, err := loadAccountRoutes(path)
	if err != nil {
		return err
	}
	root, err := standaloneStateDir(dir)
	if err != nil {
		return err
	}
	if strings.TrimSpace(dir) == "" {
		root = filepath.Join(filepath.Dir(root), "accounts-state")
	}
	handler, closeAll, err := openAccounts(routes, cfg, root, limits)
	if err != nil {
		return err
	}
	defer closeAll()
	log.Printf("API key routing enabled: %d accounts", len(routes))
	logStandaloneLimits(limits, cfg.UpstreamRPS)
	log.Printf("persistent task state: %s", root)
	return proxy.ListenAndServeHandler(ctx, cfg.Addr, handler)
}
