package update

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/creativeprojects/go-selfupdate"
	selfupdatecosign "github.com/giantswarm/selfupdate-cosign"
	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/internal/platformctl/update/updatetest"
)

// The running binary and the release the fake GitHub carries in most tests.
const (
	running = "v0.42.1"
	latestV = "v0.42.2"
	// afterTag is a go build one commit after the latest tag.
	afterTag = "v0.42.3-0.20260923194343-78597178b8ce"
	dev      = "dev"
	// commitBuild is a go build outside a module version: its commit.
	commitBuild = "dev-78597178b8ce"
)

// fixture is a platformctl with src as GitHub, its own cache directory, a
// clock the test moves and the given running version.
type fixture struct {
	u   *Updater
	src *updatetest.Source
	at  time.Time
}

func setup(t *testing.T, current string, src *updatetest.Source) *fixture {
	t.Helper()
	t.Setenv(OptOutEnv, "")
	f := &fixture{src: src, at: time.Date(2026, 9, 23, 21, 0, 0, 0, time.UTC)}
	f.u = &Updater{
		Source:        src,
		Validator:     selfupdatecosign.New(Repository),
		CacheDir:      t.TempDir(),
		Version:       current,
		Executable:    func() (string, error) { return "", errors.New("no executable in this test") },
		Now:           func() time.Time { return f.at },
		RemindTimeout: RemindTimeout,
	}
	return f
}

func releases(rs ...updatetest.Release) *updatetest.Source {
	src := &updatetest.Source{}
	for _, r := range rs {
		src.Releases = append(src.Releases, r)
	}
	return src
}

// installed points self-update at a throwaway file instead of the running
// executable and returns its path and content, so a test can assert that the
// file was replaced — or that it survived a refusal byte for byte.
func (f *fixture) installed(t *testing.T) (string, []byte) {
	t.Helper()
	content := []byte("the platformctl that is installed right now")
	exe := filepath.Join(t.TempDir(), updatetest.BinaryName())
	if err := os.WriteFile(exe, content, 0o700); err != nil { //nolint:gosec // an executable
		t.Fatal(err)
	}
	f.u.Executable = func() (string, error) { return exe, nil }
	return exe, content
}

func assertUnchanged(t *testing.T, exe string, content []byte) {
	t.Helper()
	got, err := os.ReadFile(filepath.Clean(exe))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("the installed binary was replaced: %q", got)
	}
}

func (f *fixture) advance(d time.Duration) { f.at = f.at.Add(d) }

func (f *fixture) cached(t *testing.T) cache {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.u.CacheDir, cacheFile))
	if err != nil {
		t.Fatalf("reading the cache: %v", err)
	}
	var c cache
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatalf("cache is not JSON: %v\n%s", err, data)
	}
	return c
}

func (f *fixture) remind() string {
	var out bytes.Buffer
	f.u.Remind(context.Background(), &out)
	return out.String()
}

func (f *fixture) run(checkOnly bool) (string, error) {
	var out bytes.Buffer
	err := f.u.Run(context.Background(), &out, checkOnly)
	return out.String(), err
}

func TestNewIsTheRealOne(t *testing.T) {
	u := New()
	if _, ok := u.Validator.(*selfupdatecosign.Validator); !ok {
		t.Errorf("validator %T, want the cosign validator", u.Validator)
	}
	if u.Source == nil || u.Executable == nil || u.Now == nil || u.RemindTimeout != 2*time.Second {
		t.Errorf("incomplete: %+v", u)
	}
	if u.CacheDir != "" && filepath.Base(u.CacheDir) != Binary {
		t.Errorf("cache directory %q, want one of its own", u.CacheDir)
	}
}

// TestAssetNameIsWhatCircleCIPublishes: self-update looks for the asset
// .circleci/custom.yml attaches to every release — the binary's name, not the
// repository's, one per platform the build names.
func TestAssetNameIsWhatCircleCIPublishes(t *testing.T) {
	if AssetName() != updatetest.BinaryName() {
		t.Fatalf("AssetName() = %q, the fake GitHub publishes %q", AssetName(), updatetest.BinaryName())
	}
	raw, err := os.ReadFile("../../../.circleci/custom.yml")
	if err != nil {
		t.Fatal(err)
	}
	var ci struct {
		Workflows map[string]struct {
			Jobs []map[string]map[string]any `yaml:"jobs"`
		} `yaml:"workflows"`
	}
	if err := yaml.Unmarshal(raw, &ci); err != nil {
		t.Fatal(err)
	}
	var build map[string]any
	for _, job := range ci.Workflows["build"].Jobs {
		if b, ok := job["architect/go-build"]; ok && b["name"] == "go-build-platformctl" {
			build = b
		}
	}
	if build == nil {
		t.Fatal("no go-build-platformctl job in .circleci/custom.yml")
	}
	path, _ := build["path"].(string)
	if build["binary"] != Binary || path == "" || !strings.HasSuffix(Module, strings.TrimPrefix(path, ".")) {
		t.Errorf("CI builds %v from %v; self-update looks for %s built from %s", build["binary"], build["path"], Binary, Module)
	}
	platforms, _ := build["architectures"].(string)
	for _, p := range []string{"linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64", "windows/amd64", "windows/arm64"} {
		if !strings.Contains(platforms, p) {
			t.Errorf("CI builds no %s binary: %q", p, platforms)
		}
	}
}

func TestNewerReadsTheVersionsInternalVersionReports(t *testing.T) {
	for _, tc := range []struct {
		latest, current string
		want            bool
	}{
		{latestV, running, true},
		{latestV, latestV, false},
		{latestV, latestV + "+dirty", false},                     // a tag with local edits is the tag
		{latestV, afterTag, false},                               // a go build after the tag
		{latestV, "v0.42.2-0.20260923180000-ba86c3b1d2e3", true}, // a go build before the tag
		{"v1.0.0", latestV, true},
		{latestV, dev, false},
		{latestV, commitBuild, false},
		{latestV, commitBuild + "-dirty", false},
		{latestV, "unknown", false},
		{"", running, false},
	} {
		if got := newer(tc.latest, tc.current); got != tc.want {
			t.Errorf("newer(%q, %q) = %v, want %v", tc.latest, tc.current, got, tc.want)
		}
	}
}

func TestRemindHintsAtANewerRelease(t *testing.T) {
	f := setup(t, running, releases(
		updatetest.Signed("v0.42.0"),
		updatetest.Signed(latestV),
		updatetest.Release{Tag: "v0.43.0-rc.1", Prerelease: true, Assets: updatetest.Signed("v0.43.0-rc.1").Assets},
		updatetest.Release{Tag: "v0.44.0", Draft: true, Assets: updatetest.Signed("v0.44.0").Assets},
	))
	out := f.remind()
	for _, want := range []string{latestV, running, "platformctl self-update", OptOutEnv} {
		if !strings.Contains(out, want) {
			t.Errorf("hint lacks %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "\n") != 1 {
		t.Errorf("the hint is one line:\n%s", out)
	}
	for _, skipped := range []string{"v0.43.0-rc.1", "v0.44.0"} {
		if strings.Contains(out, skipped) {
			t.Errorf("hint names the pre-release or draft %s:\n%s", skipped, out)
		}
	}
	if c := f.cached(t); c.Latest != latestV || !c.CheckedAt.Equal(f.at) || !c.FailedAt.IsZero() {
		t.Errorf("cache after the hint: %+v", c)
	}
}

func TestRemindStaysQuiet(t *testing.T) {
	for _, tc := range []struct {
		name    string
		current string
		release updatetest.Release
		optOut  string
		asks    int32
	}{
		{name: "this is the latest", current: latestV, release: updatetest.Signed(latestV), asks: 1},
		{name: "a build after the tag", current: afterTag, release: updatetest.Signed(latestV), asks: 1},
		{name: "the tag with local edits", current: latestV + "+dirty", release: updatetest.Signed(latestV), asks: 1},
		{name: "no binary for this platform", current: running, release: updatetest.Release{Tag: latestV, Assets: []selfupdate.SourceAsset{
			updatetest.Asset{ID: 1, Name: "platformctl-plan9-mips"},
			updatetest.Asset{ID: 2, Name: "platformctl-plan9-mips.bundle"},
			updatetest.Asset{ID: 3, Name: "giantswarm-platform-manager-" + runtime.GOOS + "-" + runtime.GOARCH},
		}}, asks: 1},
		{name: "a development build never asks", current: dev, release: updatetest.Signed(latestV), asks: 0},
		{name: "a build of a commit never asks", current: commitBuild, release: updatetest.Signed(latestV), asks: 0},
		{name: "opted out never asks", current: running, release: updatetest.Signed(latestV), optOut: "1", asks: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t, tc.current, releases(tc.release))
			if tc.optOut != "" {
				t.Setenv(OptOutEnv, tc.optOut)
			}
			if out := f.remind(); out != "" {
				t.Errorf("unexpected hint:\n%s", out)
			}
			if got := f.src.Calls(); got != tc.asks {
				t.Errorf("asked GitHub %d times, want %d", got, tc.asks)
			}
		})
	}
}

func TestRemindAsksGitHubOnceAnHour(t *testing.T) {
	f := setup(t, running, releases(updatetest.Signed(latestV)))
	for i := 0; i < 3; i++ {
		if out := f.remind(); !strings.Contains(out, latestV) {
			t.Fatalf("run %d: no hint:\n%s", i, out)
		}
		f.advance(20 * time.Minute)
	}
	if got := f.src.Calls(); got != 1 {
		t.Errorf("asked GitHub %d times within the hour, want 1", got)
	}
	f.advance(time.Minute) // 61 minutes after the fetch
	f.remind()
	if got := f.src.Calls(); got != 2 {
		t.Errorf("asked GitHub %d times after the hour, want 2", got)
	}
}

func TestRemindGivesUpFastWhenOffline(t *testing.T) {
	f := setup(t, running, &updatetest.Source{Hang: true})
	f.u.RemindTimeout = 100 * time.Millisecond

	start := time.Now()
	out := f.remind()
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("the hint held the command for %s", elapsed)
	}
	if out != "" {
		t.Errorf("hint without an answer:\n%s", out)
	}
	if c := f.cached(t); !c.FailedAt.Equal(f.at) || c.Latest != "" || !c.CheckedAt.IsZero() {
		t.Errorf("cache after the failed attempt: %+v", c)
	}

	// The failure is remembered: the next commands do not wait at all.
	f.advance(5 * time.Minute)
	f.remind()
	if got := f.src.Calls(); got != 1 {
		t.Errorf("asked GitHub %d times within the retry window, want 1", got)
	}
	f.advance(6 * time.Minute) // 11 minutes after the failure
	f.remind()
	if got := f.src.Calls(); got != 2 {
		t.Errorf("asked GitHub %d times after the retry window, want 2", got)
	}
}

func TestRemindKeepsTheLastAnswerWhileOffline(t *testing.T) {
	f := setup(t, running, releases(updatetest.Signed(latestV)))
	if out := f.remind(); !strings.Contains(out, latestV) {
		t.Fatalf("no hint while online:\n%s", out)
	}
	f.advance(2 * time.Hour)
	f.src.Err = errors.New("dial tcp: no route to host")
	if out := f.remind(); !strings.Contains(out, latestV) {
		t.Errorf("the remembered answer was dropped when GitHub failed:\n%s", out)
	}
	if got := f.src.Calls(); got != 2 {
		t.Errorf("asked GitHub %d times, want 2", got)
	}
	if c := f.cached(t); c.Latest != latestV || !c.FailedAt.Equal(f.at) {
		t.Errorf("cache after the failed refresh: %+v", c)
	}
}

func TestRemindSurvivesAGarbledCache(t *testing.T) {
	f := setup(t, running, releases(updatetest.Signed(latestV)))
	if err := os.WriteFile(filepath.Join(f.u.CacheDir, cacheFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out := f.remind(); !strings.Contains(out, latestV) {
		t.Errorf("no hint over a garbled cache:\n%s", out)
	}
	if c := f.cached(t); c.Latest != latestV {
		t.Errorf("cache was not rewritten: %+v", c)
	}
}

func TestRemindWorksWithoutACacheDirectory(t *testing.T) {
	f := setup(t, running, releases(updatetest.Signed(latestV)))
	f.u.CacheDir = ""
	for i := 0; i < 2; i++ {
		if out := f.remind(); !strings.Contains(out, latestV) {
			t.Errorf("run %d: no hint:\n%s", i, out)
		}
	}
	if got := f.src.Calls(); got != 2 {
		t.Errorf("asked GitHub %d times without a cache, want 2", got)
	}
}

func TestRemindHintsAtAReleaseWithoutASignatureBundle(t *testing.T) {
	// The hint installs nothing, so a release self-update would refuse is
	// still news worth a line.
	f := setup(t, running, releases(updatetest.Unsigned(latestV)))
	if out := f.remind(); !strings.Contains(out, latestV) || !strings.Contains(out, "platformctl self-update") {
		t.Errorf("no hint for a release without a bundle:\n%s", out)
	}
	if c := f.cached(t); c.Latest != latestV || !c.FailedAt.IsZero() {
		t.Errorf("cache after the hint: %+v", c)
	}
}

func TestRunCheckReportsANewerRelease(t *testing.T) {
	f := setup(t, running, releases(updatetest.Signed(latestV)))
	out, err := f.run(true)
	if !errors.Is(err, ErrOutdated) {
		t.Fatalf("Run(--check) = %v, want ErrOutdated", err)
	}
	for _, want := range []string{running, latestV, "releases/tag/" + latestV} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
	if c := f.cached(t); c.Latest != latestV {
		t.Errorf("the check did not refresh the cache: %+v", c)
	}
	// The hint agrees without asking again.
	f.remind()
	if got := f.src.Calls(); got != 1 {
		t.Errorf("asked GitHub %d times, want 1", got)
	}
}

func TestRunCheckSaysWhenNothingIsNewer(t *testing.T) {
	f := setup(t, latestV, releases(updatetest.Signed(latestV)))
	out, err := f.run(true)
	if err != nil {
		t.Fatalf("Run(--check) = %v", err)
	}
	if !strings.Contains(out, "Nothing newer than "+latestV) {
		t.Errorf("report:\n%s", out)
	}
}

// A binary that is the latest release is left alone: the version a release
// binary reports is its tag, so self-update never re-installs it.
func TestRunLeavesTheLatestReleaseAlone(t *testing.T) {
	src := releases(updatetest.Signed(latestV))
	src.Assets = map[int64][]byte{updatetest.BinaryID: []byte("the same platformctl again"), updatetest.BundleID: []byte("{}")}
	f := setup(t, latestV, src)
	exe, content := f.installed(t)
	out, err := f.run(false)
	if err != nil {
		t.Fatalf("an up-to-date binary must not fail: %v", err)
	}
	if !strings.Contains(out, "Nothing newer than "+latestV) {
		t.Errorf("expected the binary to be reported as current, got:\n%s", out)
	}
	assertUnchanged(t, exe, content)
}

// A build without a version and a build that names only its commit are no
// releases: neither compares with one, neither was installed from a release,
// and the refusal says how to get one.
func TestRunRefusesADevelopmentBuild(t *testing.T) {
	for _, current := range []string{dev, commitBuild, commitBuild + "-dirty", "unknown", ""} {
		t.Run(current, func(t *testing.T) {
			f := setup(t, current, releases(updatetest.Signed(latestV)))
			_, err := f.run(false)
			if err == nil || !strings.Contains(err.Error(), "development build") || !strings.Contains(err.Error(), "go install "+Module+"@latest") || !strings.Contains(err.Error(), "/releases") {
				t.Errorf("Run() = %v, want a refusal that says how to install a release", err)
			}
			if got := f.src.Calls(); got != 0 {
				t.Errorf("asked GitHub %d times for a dev build", got)
			}
		})
	}
}

func TestRunReportsAnUnreachableGitHub(t *testing.T) {
	f := setup(t, running, &updatetest.Source{Err: errors.New("dial tcp: no route to host")})
	_, err := f.run(false)
	if err == nil || !strings.Contains(err.Error(), "no route to host") || !strings.Contains(err.Error(), "online") {
		t.Errorf("Run() = %v, want the network error and the hint", err)
	}
}

func TestRunReplacesTheExecutableOnceTheBundleVerifies(t *testing.T) {
	src := releases(updatetest.Signed(latestV))
	src.Assets = map[int64][]byte{updatetest.BinaryID: []byte("new"), updatetest.BundleID: []byte("its bundle")}
	f := setup(t, running, src)
	exe, _ := f.installed(t)
	v := &updatetest.Validator{}
	f.u.Validator = v

	out, err := f.run(false)
	if err != nil {
		t.Fatalf("Run() = %v\n%s", err, out)
	}
	got, err := os.ReadFile(filepath.Clean(exe))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Errorf("executable holds %q after the update", got)
	}
	if runtime.GOOS != "windows" {
		// Replaced in place: the binary keeps its mode, and neither a copy of
		// the old one nor a staging file is left beside it.
		if info, err := os.Stat(exe); err != nil || info.Mode().Perm() != 0o700 {
			t.Errorf("executable's mode after the update: %v %v, want the replaced binary's -rwx------", info.Mode(), err)
		}
		if entries, err := os.ReadDir(filepath.Dir(exe)); err != nil || len(entries) != 1 {
			t.Errorf("beside the executable after the update: %v %v", entries, err)
		}
	}
	if !strings.Contains(out, "Verified the signature and updated to "+latestV) {
		t.Errorf("output:\n%s", out)
	}
	// The validator saw the platformctl binary — not the server binary or
	// the chart that end in the platform too — and its bundle.
	if v.Asset != updatetest.BinaryName() || v.BundleName != updatetest.BinaryName()+".bundle" {
		t.Errorf("validated %q against %q", v.Asset, v.BundleName)
	}
	if string(v.Binary) != "new" || string(v.Bundle) != "its bundle" {
		t.Errorf("validated %q against bundle %q", v.Binary, v.Bundle)
	}

	// The new binary reports the release; the hint has nothing to say and
	// no reason to ask.
	f.u.Version = latestV
	if hint := f.remind(); hint != "" {
		t.Errorf("hint after the update:\n%s", hint)
	}
	if got := f.src.Calls(); got != 1 {
		t.Errorf("asked GitHub %d times, want 1", got)
	}
}

// The signature check itself is tested in github.com/giantswarm/selfupdate-cosign.
// What follows proves that self-update refuses what the real validator cannot
// vouch for, and leaves the installed binary untouched when it does.

func TestRunRefusesAReleaseWithoutASignatureBundle(t *testing.T) {
	src := releases(updatetest.Unsigned(latestV))
	src.Assets = map[int64][]byte{updatetest.BinaryID: []byte("a newer platformctl, unsigned")}
	f := setup(t, running, src)
	exe, content := f.installed(t)

	_, err := f.run(false)
	if err == nil {
		t.Fatal("a release without a bundle must be refused")
	}
	if !strings.Contains(err.Error(), "no signature bundle") || !strings.Contains(err.Error(), updatetest.BinaryName()+".bundle") {
		t.Errorf("the error should say what is missing, got: %v", err)
	}
	if strings.Contains(err.Error(), "online?") {
		t.Errorf("a missing bundle is not a network problem: %v", err)
	}
	assertUnchanged(t, exe, content)
	// Refused before the release was reported: go-selfupdate returns no
	// release with the error, so there is no tag to remember.
	if _, err := os.Stat(filepath.Join(f.u.CacheDir, cacheFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the refusal wrote a cache: %v", err)
	}
}

func TestRunCheckRefusesAReleaseWithoutASignatureBundle(t *testing.T) {
	f := setup(t, running, releases(updatetest.Unsigned(latestV)))
	_, err := f.run(true)
	if err == nil || errors.Is(err, ErrOutdated) {
		t.Fatalf("Run(--check) = %v, want the refusal: self-update would install nothing from this release", err)
	}
	if !strings.Contains(err.Error(), "no signature bundle") {
		t.Errorf("the error should say what is missing, got: %v", err)
	}
}

func TestRunRefusesADownloadThatDoesNotVerify(t *testing.T) {
	src := releases(updatetest.Signed(latestV))
	src.Assets = map[int64][]byte{
		updatetest.BinaryID: []byte("a newer platformctl"),
		updatetest.BundleID: []byte("{}"), // not a Sigstore bundle
	}
	f := setup(t, running, src)
	exe, content := f.installed(t)

	out, err := f.run(false)
	if err == nil {
		t.Fatal("a download whose bundle does not verify must be refused")
	}
	if !strings.Contains(err.Error(), "is unchanged") || !strings.Contains(err.Error(), "is not a Sigstore bundle") {
		t.Errorf("the error should say the binary was refused and why, got: %v", err)
	}
	if !strings.Contains(out, "Newer release: "+latestV) || strings.Contains(out, "Verified the signature") {
		t.Errorf("the newer release is announced, no success reported:\n%s", out)
	}
	assertUnchanged(t, exe, content)
	if c := f.cached(t); c.Latest != latestV {
		t.Errorf("cache after the refusal: %+v", c)
	}
}

// A signature that does not verify is refused like a bundle that is none: a
// validator refusing the download leaves the binary in place.
func TestRunRefusesABadSignature(t *testing.T) {
	src := releases(updatetest.Signed(latestV))
	src.Assets = map[int64][]byte{updatetest.BinaryID: []byte("a tampered platformctl"), updatetest.BundleID: []byte("a bundle for another binary")}
	f := setup(t, running, src)
	f.u.Validator = refusing{}
	exe, content := f.installed(t)

	_, err := f.run(false)
	if err == nil || !strings.Contains(err.Error(), "does not verify") || !strings.Contains(err.Error(), "is unchanged") {
		t.Fatalf("Run() = %v, want the validator's refusal", err)
	}
	assertUnchanged(t, exe, content)
}

// refusing is a validator whose check fails, the way the cosign validator
// answers a binary its bundle does not cover.
type refusing struct{}

func (refusing) GetValidationAssetName(assetName string) string { return assetName + ".bundle" }
func (refusing) Validate(assetName string, _, _ []byte) error {
	return errors.New(assetName + " does not verify against " + assetName + ".bundle")
}
