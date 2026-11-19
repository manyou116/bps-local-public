package main

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/sunboyzoey/GPT-BPS/internal/gateway"
	"github.com/sunboyzoey/GPT-BPS/internal/proxy"
)

func standaloneStateDir(path string) (string, error) {
	if path = strings.TrimSpace(path); path != "" {
		return filepath.Abs(path)
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, ".bps-local", "local-state"), nil
}

func serveStandalone(ctx context.Context, bridge *proxy.Server, store proxy.CredentialSource, dir string, limits gateway.Limits, rps int) error {
	path, err := standaloneStateDir(dir)
	if err != nil {
		return err
	}
	account, err := openStandaloneAccount(bridge, store, path, limits)
	if err != nil {
		return err
	}
	defer closeStandaloneAccounts([]*standaloneAccount{account})
	logStandaloneLimits(limits, rps)
	log.Printf("persistent task state: %s", path)
	return bridge.ListenAndServeHandler(ctx, account.handler)
}

type standaloneAccount struct {
	state   *gateway.StandaloneState
	handler *gateway.StandaloneHandler
}

func openStandaloneAccount(bridge *proxy.Server, store proxy.CredentialSource, path string, limits gateway.Limits) (*standaloneAccount, error) {
	if limits.PerAccount < 1 || limits.PerAccount > 10000 || limits.Global != 0 || limits.Queue < 0 || limits.Wait <= 0 {
		return nil, errors.New("invalid standalone limits: max-concurrent must be 1-10000, max-pending >= 0 and queue-timeout > 0")
	}
	state, err := gateway.OpenStandaloneState(path)
	if err != nil {
		return nil, err
	}
	handler, err := gateway.NewStandaloneHandler(bridge, store, state.Key(), limits)
	if err != nil {
		state.Close()
		return nil, err
	}
	return &standaloneAccount{state: state, handler: handler}, nil
}

func closeStandaloneAccounts(accounts []*standaloneAccount) {
	for _, account := range accounts {
		account.handler.Close()
	}
	for _, account := range accounts {
		account.handler.Wait()
		account.state.Close()
	}
}

func logStandaloneLimits(limits gateway.Limits, rps int) {
	log.Printf("task pool: account_slots_v1; per-account concurrency: %d; queue: %d; wait budget: %s", limits.PerAccount, limits.Queue, limits.Wait)
	if rps == 0 {
		log.Print("upstream rolling start limit: disabled (concurrency limit remains active)")
	} else {
		log.Printf("upstream rolling start limit: %d/s for WS dials and response.create", rps)
	}
}
