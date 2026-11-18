package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoginExistingCredentialsRefusedBeforeBrowserOrNetwork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oauth.json")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"", "device", "browser"} {
		var output bytes.Buffer
		err := runLogin(context.Background(), loginOptions{Method: method, AuthPath: path, ProxyURL: "invalid proxy"}, &output)
		if err == nil || !strings.Contains(err.Error(), "destination exists") || output.Len() != 0 {
			t.Fatal("attempted login before refusing existing credentials", err)
		}
		contents, err := os.ReadFile(path)
		if err != nil || string(contents) != "original" {
			t.Fatal("existing credentials changed")
		}
	}
}

func TestLoginFlagsRejectUnusableModes(t *testing.T) {
	for _, tc := range []struct {
		login           bool
		method, browser string
		noBrowser       bool
	}{
		{false, "browser", "", false}, {false, "device", "", false}, {false, "", "chrome", false},
		{true, "unknown", "", false}, {true, "browser", "", true}, {true, "device", "chrome", false},
	} {
		if err := validateLoginFlags(tc.login, tc.method, tc.browser, tc.noBrowser); err == nil {
			t.Fatal("unsupported flags were accepted", tc)
		}
	}
	if err := validateLoginFlags(true, "", "", true); err != nil {
		t.Fatal("server device login rejected", err)
	}
	if err := validateLoginFlags(true, "browser", "", false); err != nil {
		t.Fatal(err)
	}
}
