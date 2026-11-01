package main

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
)

// version is a release build's (build.sh: -X main.version=…).
var version string

// Version is the release's version, or the module's as `go install` stamps it ("devel" from a checkout).
func Version() string {
	if version != "" {
		return "webpc-net " + version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return "webpc-net " + bi.Main.Version
	}
	return "webpc-net devel"
}

// tokenPath is where the token is kept, so a page's stored connection survives a restart of the helper.
func tokenPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("no config directory for the token: %w", err)
	}
	return filepath.Join(dir, "webpc-net", "token"), nil
}

// loadToken reads the kept token, or makes one (always, with fresh) and keeps it. WEBPC_NET_TOKEN overrides both (tests).
func loadToken(fresh bool) (string, error) {
	if t := os.Getenv("WEBPC_NET_TOKEN"); t != "" {
		return t, nil
	}
	p, err := tokenPath()
	if err != nil {
		return "", err
	}
	if !fresh {
		b, err := os.ReadFile(p)
		if t := strings.TrimSpace(string(b)); err == nil && validToken(t) {
			return t, nil
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
	}
	t := newToken()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(p, []byte(t+"\n"), 0o600); err != nil {
		return "", err
	}
	return t, nil
}

// newToken is 256 random bits, URL-safe.
func newToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // never fails (crypto/rand)
	return base64.RawURLEncoding.EncodeToString(b)
}

func validToken(t string) bool {
	b, err := base64.RawURLEncoding.DecodeString(t)
	return err == nil && len(b) == 32
}
