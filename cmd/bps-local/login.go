package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/sunboyzoey/GPT-BPS/internal/auth"
)

type loginOptions struct {
	Method      string
	BrowserPath string
	NoBrowser   bool
	AuthPath    string
	ProxyURL    string
}

func validateLoginFlags(login bool, method, browserPath string, noBrowser bool) error {
	if method != "" && method != "device" && method != "browser" {
		return errors.New("--login-method must be device or browser")
	}
	if !login && (method != "" || browserPath != "") {
		return errors.New("--login-method and --browser-path require --login")
	}
	if browserPath != "" && method != "browser" {
		return errors.New("--browser-path requires --login-method browser")
	}
	if method == "browser" && noBrowser {
		return errors.New("browser login requires a local graphical browser; use --login --no-browser for device login on a server")
	}
	return nil
}

func runLogin(ctx context.Context, options loginOptions, out io.Writer) error {
	if err := validateLoginFlags(true, options.Method, options.BrowserPath, options.NoBrowser); err != nil {
		return err
	}
	// Refuse existing files before opening a browser or sending OAuth requests.
	// SaveDeviceCredentials also protects against a concurrent writer.
	if _, err := os.Lstat(options.AuthPath); err == nil {
		return errors.New("OAuth destination exists; choose a new --auth path (no credentials overwritten)")
	} else if !os.IsNotExist(err) {
		return errors.New("cannot inspect OAuth destination; check --auth path and permissions")
	}
	var creds *auth.DeviceCredentials
	var err error
	if options.Method == "browser" {
		client, e := auth.NewBrowserClient(options.ProxyURL)
		if e != nil {
			return e
		}
		fmt.Fprintln(out, "Opening an isolated browser for BPS authorization. Complete login and consent yourself; waiting up to 15 minutes…")
		creds, err = client.LoginBrowser(ctx, func(ctx context.Context, address string) (string, error) {
			return auth.CaptureBrowserAuthorization(ctx, address, options.ProxyURL, options.BrowserPath)
		})
	} else {
		client, e := auth.NewDeviceClient(options.ProxyURL)
		if e != nil {
			return e
		}
		creds, err = client.Login(ctx, func(p auth.DevicePrompt) error {
			fmt.Fprintf(out, "Open: %s\nDevice code: %s\nComplete authorization yourself in the browser. Waiting up to 15 minutes…\n", p.URL, p.UserCode)
			if !options.NoBrowser {
				if err := openBrowser(p.URL); err != nil {
					fmt.Fprintln(out, "Browser could not be opened; open the URL above manually.")
				}
			}
			return nil
		})
	}
	if err != nil {
		return err
	}
	if err := auth.SaveDeviceCredentials(options.AuthPath, creds); err != nil {
		return err
	}
	fmt.Fprintf(out, "BPS OAuth credentials saved: %s\nBPS generation not yet verified. Automatic refresh remains disabled.\n", options.AuthPath)
	return nil
}
