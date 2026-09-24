// Package update keeps platformctl current. `platformctl self-update`
// installs the newest GitHub release of this repository over the running
// executable (creativeprojects/go-selfupdate against the releases; a
// development build is refused), and only after the binary's cosign Sigstore
// bundle verifies: the architect orb signs every binary it attaches to a
// release in CircleCI, keyless, and the shared validator
// (github.com/giantswarm/selfupdate-cosign) checks the download against that
// signature before anything is written. A release without a bundle, or a
// download that does not match its bundle, is refused and the installed binary
// stays as it is. The verified binary replaces the executable with a single
// rename (selfupdatecosign.Install): a platformctl started meanwhile runs the
// old binary or the new one, several updates may run at once, and no other
// file is touched.
//
// Remind is the one-line hint ahead of every other command while a newer
// release exists. It never blocks — an outdated platformctl runs the command
// the same — and gives up after two seconds when GitHub cannot be reached.
// GitHub's answer is remembered under the user's cache directory for an hour
// (a failed attempt for ten minutes), so the round trip is rare. The hint
// installs nothing, so it asks for no bundle.
//
// The package is the shape of muster's and agentlab's internal/update, so the
// CLIs behave alike.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/creativeprojects/go-selfupdate"
	selfupdatecosign "github.com/giantswarm/selfupdate-cosign"

	"github.com/giantswarm/giantswarm-platform-manager/internal/version"
)

const (
	// Repository is the GitHub repository whose releases carry the binaries.
	Repository = "giantswarm/giantswarm-platform-manager"
	// Binary is the command's name and the prefix of its release assets.
	Binary = "platformctl"
	// Module is the package `go install Module@latest` builds the newest
	// release from.
	Module = "github.com/giantswarm/giantswarm-platform-manager/cmd/platformctl"
	// OptOutEnv silences the newer-release hint when set to any value.
	// self-update ignores it.
	OptOutEnv = "PLATFORMCTL_NO_UPDATE_CHECK"
)

const (
	// cacheTTL is how long GitHub's answer stands before the hint asks
	// again; retryAfterFailure how long a failed attempt keeps it from
	// trying (offline for the afternoon: one short wait every ten minutes,
	// not one per command).
	cacheTTL          = time.Hour
	retryAfterFailure = 10 * time.Minute

	// lookupTimeout bounds finding the latest release for self-update; the
	// download itself runs until done or interrupted.
	lookupTimeout = 15 * time.Second

	// RemindTimeout caps the GitHub round trip behind the hint: no route, a
	// resolver that never answers, a captive portal that swallows the
	// request — the command starts after at most this long, without the hint.
	RemindTimeout = 2 * time.Second

	cacheFile = "latest-release.json"
)

// ErrOutdated is what Run answers with checkOnly when a newer release exists;
// platformctl turns it into exit code 125, devctl's convention for `version
// check`.
var ErrOutdated = errors.New("a newer platformctl release is available")

// AssetName is the release asset of this platform's binary, as CircleCI
// attaches it: platformctl-<os>-<arch>, with .exe on Windows. Its Sigstore
// bundle is the same name with ".bundle".
func AssetName() string {
	name := Binary + "-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// Updater is self-update and the hint over a release source. New gives the
// real one; a test replaces the fields it needs.
type Updater struct {
	// Source lists the releases and downloads their assets: GitHub.
	Source selfupdate.Source
	// Validator checks a download against its bundle: the cosign validator
	// for releases of Repository, which pins the CircleCI issuer, a CircleCI
	// pipeline as the subject and this repository as the source, against the
	// Sigstore public-good trust root.
	Validator selfupdate.Validator
	// CacheDir holds the hint's memory of GitHub's answer; "" remembers
	// nothing.
	CacheDir string
	// Version is the running binary's (internal/version).
	Version string
	// Executable is the file self-update replaces.
	Executable func() (string, error)
	// Now is the clock the cache is kept by.
	Now func() time.Time
	// RemindTimeout caps the hint's round trip.
	RemindTimeout time.Duration
}

// New is the Updater of the running binary against GitHub.
func New() *Updater {
	u := &Updater{
		Validator:     selfupdatecosign.New(Repository),
		Version:       version.String(),
		Executable:    selfupdate.ExecutablePath,
		Now:           time.Now,
		RemindTimeout: RemindTimeout,
	}
	// The error is for GitHub Enterprise URLs only; without a source
	// NewUpdater builds the same public-GitHub one itself. A GITHUB_TOKEN in
	// the environment lifts the anonymous rate limit, nothing else changes.
	if src, err := selfupdate.NewGitHubSource(selfupdate.GitHubConfig{}); err == nil {
		u.Source = src
	}
	if dir, err := os.UserCacheDir(); err == nil {
		u.CacheDir = filepath.Join(dir, Binary)
	}
	return u
}

// Remind writes one line on w when a release newer than the running binary
// exists, and nothing otherwise — never an error, never a wait worth
// noticing: platformctl works the same on an old binary. Quiet for a build
// that is no release (dev), under OptOutEnv, and whenever GitHub cannot say
// within RemindTimeout (offline, rate-limited, no binary for this platform).
func (u *Updater) Remind(ctx context.Context, w io.Writer) {
	if os.Getenv(OptOutEnv) != "" || !isRelease(u.Version) {
		return
	}
	latest := u.rememberedLatest(ctx)
	if !newer(latest, u.Version) {
		return
	}
	_, _ = fmt.Fprintf(w, "platformctl %s is out (this is %s): `platformctl self-update` installs it; %s=1 silences this hint.\n", latest, u.Version, OptOutEnv)
}

// Run is `platformctl self-update`. It looks up the newest release and,
// unless checkOnly, downloads this platform's binary and its Sigstore bundle,
// verifies the one against the other, and only then writes it over the
// running executable. Progress goes to w. A development build is refused: dev
// compares with nothing and was not installed from a release to begin with.
// A release without a bundle for this platform's binary is refused before
// anything is downloaded, a download that does not verify before anything is
// written; both leave the executable as it is. With checkOnly the versions are
// reported and the result is ErrOutdated when a newer release exists, nil when
// nothing newer is out, and the same refusal when the newest release has no
// bundle, since self-update would install nothing from it.
func (u *Updater) Run(ctx context.Context, w io.Writer, checkOnly bool) error {
	if !isRelease(u.Version) {
		return fmt.Errorf("cannot self-update a development build (version %s): install a release with `go install %s@latest` or from https://github.com/%s/releases", u.Version, Module, Repository)
	}
	_, _ = fmt.Fprintf(w, "Current version: %s\n", u.Version)
	_, _ = fmt.Fprintf(w, "Looking up the latest release of %s...\n", Repository)
	lookup, cancel := context.WithTimeout(ctx, lookupTimeout)
	up, rel, found, err := u.detect(lookup, u.Validator, "")
	cancel()
	if errors.Is(err, selfupdate.ErrValidationAssetNotFound) {
		// The newest release carries this platform's binary but no bundle
		// next to it; nothing has been downloaded.
		return fmt.Errorf("the latest release of %s has no signature bundle for %s, so it cannot be verified; refusing to install it: %w", Repository, AssetName(), err)
	}
	if err != nil {
		return fmt.Errorf("looking up the latest release of %s: %w (is this machine online?)", Repository, err)
	}
	if !found {
		return fmt.Errorf("no release of %s carries %s", Repository, AssetName())
	}
	latest := tag(rel)
	// The next hint agrees with what the command just learned, without a
	// round trip of its own.
	u.store(cache{CheckedAt: u.Now(), Latest: latest})
	if !newer(latest, u.Version) {
		_, _ = fmt.Fprintf(w, "Nothing newer than %s on GitHub (latest release %s).\n", u.Version, latest)
		return nil
	}
	_, _ = fmt.Fprintf(w, "Newer release: %s (published %s)\n  %s\n", latest, rel.PublishedAt.Format(time.RFC3339), rel.URL)
	if checkOnly {
		return ErrOutdated
	}
	exe, err := u.Executable()
	if err != nil {
		return fmt.Errorf("locating the running executable: %w", err)
	}
	_, _ = fmt.Fprintf(w, "Updating %s to %s...\n", exe, latest)
	// Downloads the binary and its bundle, verifies, then renames it over the
	// file; a failed verification leaves it untouched.
	if err := selfupdatecosign.Install(ctx, up, rel, exe); err != nil {
		return fmt.Errorf("updating %s failed, it is unchanged: %w", exe, err)
	}
	_, _ = fmt.Fprintf(w, "Verified the signature and updated to %s.\n", latest)
	return nil
}

// assetFilter selects AssetName and nothing else. go-selfupdate would pick
// any asset ending in <os>-<arch>, and this repository's name is not the
// binary's, so the name is pinned whole: never the .bundle, a chart or another
// binary a release might carry.
func assetFilter() string {
	return "^" + regexp.QuoteMeta(AssetName()) + "$"
}

// detect asks the source for the release tagged want ("v0.42.2"), the
// newest when want is "", with this platform's binary (found is false when
// none has one). The updater comes back with the
// release because the download has to go through the same source. With a
// validator the release must also carry the validator's bundle for that
// binary — else the error wraps selfupdate.ErrValidationAssetNotFound — and
// UpdateTo checks the download against it; nil asks for no bundle, for a
// caller that installs nothing.
func (u *Updater) detect(ctx context.Context, validator selfupdate.Validator, want string) (*selfupdate.Updater, *selfupdate.Release, bool, error) {
	up, err := selfupdate.NewUpdater(selfupdate.Config{Source: u.Source, Validator: validator, Filters: []string{assetFilter()}})
	if err != nil {
		return nil, nil, false, err
	}
	rel, found, err := up.DetectVersion(ctx, selfupdate.ParseSlug(Repository), want)
	return up, rel, found, err
}

// tag is the version in the form the tags and internal/version use
// ("v0.42.2"); go-selfupdate reports it without the v.
func tag(rel *selfupdate.Release) string {
	return "v" + rel.Version()
}

// isRelease says whether v is a version a release can be compared with: a
// tag ("v0.42.2"), the tag with local edits ("v0.42.2+dirty") or a Go
// pseudo-version between tags — not dev, dev-<commit> or unknown.
func isRelease(v string) bool {
	_, err := semver.NewVersion(v)
	return err == nil
}

// newer reports whether latest is a higher version than current. Current is
// what internal/version reports: a tag ("v0.42.2"), a tag with local edits
// ("v0.42.2+dirty", which is the tag), a Go pseudo-version between tags
// ("v0.42.3-0.20260923194343-78597178b8ce": after v0.42.2, before v0.42.3) —
// or dev, which, like anything semver cannot read, is never outdated.
func newer(latest, current string) bool {
	l, err := semver.NewVersion(latest)
	if err != nil {
		return false
	}
	c, err := semver.NewVersion(current)
	if err != nil {
		return false
	}
	return l.GreaterThan(c)
}

// rememberedLatest is the newest release version: from the cache while it is
// current, else from the source within RemindTimeout — "" when neither
// knows. The hint installs nothing, so it asks for no bundle: a release
// self-update would refuse is still a newer release worth knowing about.
func (u *Updater) rememberedLatest(ctx context.Context) string {
	c := u.load()
	at := u.Now()
	if c.current(at) {
		return c.Latest
	}
	ctx, cancel := context.WithTimeout(ctx, u.RemindTimeout)
	defer cancel()
	_, rel, found, err := u.detect(ctx, nil, "")
	switch {
	case err != nil:
		// Keep the previous answer, if any: it is still the best this
		// machine knows, and there is no point asking again right away.
		c.FailedAt = at
	case !found:
		c = cache{CheckedAt: at}
	default:
		c = cache{CheckedAt: at, Latest: tag(rel)}
	}
	u.store(c)
	return c.Latest
}

// cache is what the hint remembers between commands, as JSON under the
// user's cache directory: GitHub's last answer and when it came, or when the
// last attempt got none.
type cache struct {
	// CheckedAt is when Latest was fetched; zero when it never was.
	CheckedAt time.Time `json:"checkedAt"`
	// Latest is the newest release GitHub reported ("v0.42.2"); "" when no
	// release carries this platform's binary.
	Latest string `json:"latest"`
	// FailedAt is the last attempt that got no answer; zero when the last
	// attempt succeeded.
	FailedAt time.Time `json:"failedAt,omitempty"`
}

// current says whether the cache still answers on its own: fetched within
// cacheTTL, or failed within retryAfterFailure — then Latest (the previous
// answer, or nothing) is the best this machine knows and GitHub is left
// alone.
func (c cache) current(at time.Time) bool {
	return (!c.CheckedAt.IsZero() && at.Sub(c.CheckedAt) < cacheTTL) ||
		(!c.FailedAt.IsZero() && at.Sub(c.FailedAt) < retryAfterFailure)
}

// cachePath is the cache file, "" without a cache directory.
func (u *Updater) cachePath() string {
	if u.CacheDir == "" {
		return ""
	}
	return filepath.Join(u.CacheDir, cacheFile)
}

// load reads the cache; a missing or unreadable file is an empty cache.
func (u *Updater) load() cache {
	path := u.cachePath()
	if path == "" {
		return cache{}
	}
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return cache{}
	}
	var c cache
	if err := json.Unmarshal(data, &c); err != nil {
		return cache{}
	}
	return c
}

// store writes the cache atomically — a sibling temp file, then a rename —
// so two platformctl commands running at once never leave a torn file
// behind. Failing to write is failing to remember, nothing worse.
func (u *Updater) store(c cache) {
	path := u.cachePath()
	if path == "" {
		return
	}
	data, err := json.Marshal(c)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), cacheFile+".*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), path) != nil {
		_ = os.Remove(tmp.Name())
	}
}
