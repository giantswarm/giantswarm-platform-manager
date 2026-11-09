package local

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func env(vars map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := vars[k]; return v, ok }
}

// fakeGH is a gh CLI that prints out for `gh auth token`, or fails.
func fakeGH(t *testing.T, out string, fail bool) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "gh")
	body := "#!/bin/sh\n[ \"$1 $2\" = \"auth token\" ] || exit 2\n"
	if fail {
		body += "exit 1\n"
	} else {
		body += "echo " + out + "\n"
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	// The fake CLI runs as the owner's program: owner-only, executable.
	if err := os.Chmod(p, 0o700); err != nil { //nolint:gosec // an executable fake, owner-only
		t.Fatal(err)
	}
	return p
}

func TestToken(t *testing.T) {
	ctx := context.Background()
	gh := fakeGH(t, "from-gh", false)
	for _, c := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"GITHUB_TOKEN first", map[string]string{"GITHUB_TOKEN": "a", "GH_TOKEN": "b"}, "a"},
		{"GH_TOKEN next", map[string]string{"GITHUB_TOKEN": " ", "GH_TOKEN": "b"}, "b"},
		{"gh last", nil, "from-gh"},
	} {
		got, err := Token(ctx, env(c.env), gh)
		if err != nil || got != c.want {
			t.Errorf("%s: %q, %v; want %q", c.name, got, err, c.want)
		}
	}
	if _, err := Token(ctx, env(nil), fakeGH(t, "", true)); err == nil || !strings.Contains(err.Error(), "gh auth login") {
		t.Errorf("no token anywhere: %v", err)
	}
}

func TestOpenNeedsTheHub(t *testing.T) {
	if _, err := Open(context.Background(), Options{Token: "t"}); err == nil || !strings.Contains(err.Error(), "--hub") {
		t.Errorf("no hub: %v", err)
	}
}
