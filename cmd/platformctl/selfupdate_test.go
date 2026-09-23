package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creativeprojects/go-selfupdate"
	selfupdatecosign "github.com/giantswarm/selfupdate-cosign"

	"github.com/giantswarm/giantswarm-platform-manager/internal/platformctl/update"
	"github.com/giantswarm/giantswarm-platform-manager/internal/platformctl/update/updatetest"
)

// The update itself — the look-up, the cache, the refusals — is tested in
// internal/platformctl/update. These tests run the command line: self-update
// against a fake GitHub, its exit codes, and where the hint appears.

const (
	selfUpdateCmd = "self-update"
	checkFlag     = "--check"
	runningV      = "v0.42.1"
	releasedV     = "v0.42.2"
	installedBin  = "the platformctl that is installed right now"
	newBin        = "the new platformctl"
)

// fakeGitHub is a GitHub whose latest release is releasedV, signed, with
// bundle as its bundle's content.
func fakeGitHub(bundle string) *updatetest.Source {
	return &updatetest.Source{
		Releases: []selfupdate.SourceRelease{updatetest.Signed(releasedV)},
		Assets:   map[int64][]byte{updatetest.BinaryID: []byte(newBin), updatetest.BundleID: []byte(bundle)},
	}
}

// updater is platformctl at version current against src, its executable a
// throwaway file whose path it answers.
func updater(t *testing.T, current string, src *updatetest.Source) (*update.Updater, string) {
	t.Helper()
	t.Setenv(update.OptOutEnv, "")
	exe := filepath.Join(t.TempDir(), updatetest.BinaryName())
	if err := os.WriteFile(exe, []byte(installedBin), 0o700); err != nil { //nolint:gosec // an executable
		t.Fatal(err)
	}
	return &update.Updater{
		Source:        src,
		Validator:     &updatetest.Validator{},
		CacheDir:      t.TempDir(),
		Version:       current,
		Executable:    func() (string, error) { return exe, nil },
		Now:           time.Now,
		RemindTimeout: update.RemindTimeout,
	}, exe
}

func with(u *update.Updater, args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := execute(u, args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func installed(t *testing.T, exe string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Clean(exe))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSelfUpdateInstallsTheVerifiedRelease(t *testing.T) {
	u, exe := updater(t, runningV, fakeGitHub("its bundle"))
	v := u.Validator.(*updatetest.Validator)
	code, out, errs := with(u, selfUpdateCmd)
	if code != exitOK {
		t.Fatalf("exit %d\n%s%s", code, out, errs)
	}
	if !strings.Contains(out, "Verified the signature and updated to "+releasedV) {
		t.Errorf("stdout:\n%s", out)
	}
	if got := installed(t, exe); got != newBin {
		t.Errorf("the executable holds %q", got)
	}
	if v.Asset != updatetest.BinaryName() || string(v.Bundle) != "its bundle" {
		t.Errorf("verified %q against %q", v.Asset, v.Bundle)
	}
}

// The real cosign validator refuses a bundle that does not verify, and the
// binary stays as it is.
func TestSelfUpdateRefusesABadSignature(t *testing.T) {
	u, exe := updater(t, runningV, fakeGitHub("{}"))
	u.Validator = selfupdatecosign.New(update.Repository)
	code, out, errs := with(u, selfUpdateCmd)
	if code != exitError {
		t.Fatalf("exit %d, want %d\n%s%s", code, exitError, out, errs)
	}
	for _, want := range []string{"platformctl: updating ", "it is unchanged", "is not a Sigstore bundle"} {
		if !strings.Contains(errs, want) {
			t.Errorf("stderr lacks %q:\n%s", want, errs)
		}
	}
	if got := installed(t, exe); got != installedBin {
		t.Errorf("the executable was replaced: %q", got)
	}
}

func TestSelfUpdateCheckExits125WhenANewerReleaseIsOut(t *testing.T) {
	u, exe := updater(t, runningV, fakeGitHub("its bundle"))
	code, out, errs := with(u, selfUpdateCmd, checkFlag)
	if code != exitOutdated || exitOutdated != 125 {
		t.Fatalf("exit %d, want 125\n%s%s", code, out, errs)
	}
	if !strings.Contains(out, "Current version: "+runningV) || !strings.Contains(out, "Newer release: "+releasedV) {
		t.Errorf("stdout:\n%s", out)
	}
	if errs != "" {
		t.Errorf("the exit code is the answer, stderr says nothing: %q", errs)
	}
	if got := installed(t, exe); got != installedBin {
		t.Errorf("--check installed %q", got)
	}

	u.Version = releasedV
	if code, out, errs := with(u, selfUpdateCmd, checkFlag); code != exitOK || !strings.Contains(out, "Nothing newer than "+releasedV) {
		t.Errorf("on the latest release: exit %d\n%s%s", code, out, errs)
	}
}

func TestSelfUpdateRefusesADevelopmentBuild(t *testing.T) {
	src := fakeGitHub("its bundle")
	u, exe := updater(t, "dev", src)
	code, out, errs := with(u, selfUpdateCmd)
	if code != exitError {
		t.Fatalf("exit %d, want %d\n%s%s", code, exitError, out, errs)
	}
	for _, want := range []string{"development build", "go install github.com/giantswarm/giantswarm-platform-manager/cmd/platformctl@latest", "releases"} {
		if !strings.Contains(errs, want) {
			t.Errorf("stderr lacks %q:\n%s", want, errs)
		}
	}
	if src.Calls() != 0 || installed(t, exe) != installedBin {
		t.Errorf("a dev build asked GitHub %d times or was replaced", src.Calls())
	}
}

func TestSelfUpdateTakesNoArguments(t *testing.T) {
	u, _ := updater(t, runningV, fakeGitHub("its bundle"))
	if code, _, errs := with(u, selfUpdateCmd, "now"); code != exitUsage || !strings.Contains(errs, "self-update [--check] takes no arguments") {
		t.Errorf("exit %d\n%s", code, errs)
	}
}

// The hint is one line on stderr ahead of a command that does work; the
// command's own output and exit code are the same. The commands about
// versions, cobra's plumbing and the usage stay quiet, and so does every
// command under the opt-out.
func TestTheHintPrecedesTheCommandsThatDoWork(t *testing.T) {
	hint := "platformctl " + releasedV + " is out (this is " + runningV + ")"
	tmpl := []string{templateCmd, agentPlatform, inputsFlag, publicInput}

	u, _ := updater(t, runningV, fakeGitHub("its bundle"))
	code, out, errs := with(u, tmpl...)
	if code != exitOK || !strings.HasPrefix(errs, hint) || strings.Count(errs, "\n") != 1 {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	if !strings.Contains(out, "--- includes.txt\n") || strings.Contains(out, hint) {
		t.Errorf("stdout is the command's alone:\n%s", out)
	}
	// A usage error of a command still names the release.
	if code, _, errs := with(u, installationCmd, verifyCmd, hazel); code != exitUsage || !strings.HasPrefix(errs, hint) {
		t.Errorf("exit %d, stderr %q", code, errs)
	}

	for _, args := range [][]string{
		{versionCmd},
		{selfUpdateCmd, checkFlag},
		{helpCmd},
		{helpFlag},
		{completionCmd, zsh},
		{"__complete", installationCmd, ""},
		{},
	} {
		if _, _, errs := with(u, args...); strings.Contains(errs, hint) {
			t.Errorf("%v prints the hint: %q", args, errs)
		}
	}

	t.Setenv(update.OptOutEnv, "1")
	if _, _, errs := with(u, tmpl...); errs != "" {
		t.Errorf("the opt-out still prints %q", errs)
	}
}

// A GitHub that fails, or answers nothing, holds no command up beyond the
// hint's bound, and the command runs as it would have.
func TestTheHintNeverFailsACommand(t *testing.T) {
	for name, src := range map[string]*updatetest.Source{
		"refused":   {Err: errors.New("dial tcp: no route to host")},
		"no answer": {Hang: true},
	} {
		t.Run(name, func(t *testing.T) {
			u, _ := updater(t, runningV, src)
			u.RemindTimeout = 100 * time.Millisecond
			start := time.Now()
			code, out, errs := with(u, templateCmd, agentPlatform, inputsFlag, publicInput)
			if elapsed := time.Since(start); elapsed > 2*time.Second {
				t.Errorf("the hint held the command for %s", elapsed)
			}
			if code != exitOK || errs != "" || !strings.Contains(out, "--- includes.txt\n") {
				t.Errorf("exit %d, stderr %q", code, errs)
			}
		})
	}
}
