// Package updatetest is GitHub's releases of this repository as a test
// double for self-update and the newer-release hint: a source that lists
// releases and serves their assets, releases laid out the way CircleCI
// attaches platformctl, and a validator that accepts what it is handed.
package updatetest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/creativeprojects/go-selfupdate"
)

// The asset IDs of a Signed release: the platformctl binary for this
// platform and its bundle; the other assets are decoys.
const (
	BinaryID int64 = 1
	BundleID int64 = 2
)

// Source stands in for GitHub: the releases it lists, how it fails, and what
// each asset's download returns.
type Source struct {
	Releases []selfupdate.SourceRelease
	// Err fails ListReleases.
	Err error
	// Hang makes ListReleases wait for the context: a network that swallows
	// packets.
	Hang bool
	// Assets is what the download of each asset (by ID) returns.
	Assets map[int64][]byte

	calls atomic.Int32
}

var _ selfupdate.Source = (*Source)(nil)

// Calls is how often the releases were listed: the round trips to GitHub.
func (s *Source) Calls() int32 { return s.calls.Load() }

func (s *Source) ListReleases(ctx context.Context, _ selfupdate.Repository) ([]selfupdate.SourceRelease, error) {
	s.calls.Add(1)
	if s.Hang {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if s.Err != nil {
		return nil, s.Err
	}
	return s.Releases, nil
}

func (s *Source) DownloadReleaseAsset(_ context.Context, _ *selfupdate.Release, id int64) (io.ReadCloser, error) {
	data, ok := s.Assets[id]
	if !ok {
		return nil, fmt.Errorf("no asset %d", id)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// Asset is one file attached to a release.
type Asset struct {
	ID   int64
	Name string
}

func (a Asset) GetID() int64                  { return a.ID }
func (a Asset) GetName() string               { return a.Name }
func (a Asset) GetSize() int                  { return 3 }
func (a Asset) GetBrowserDownloadURL() string { return "https://example.test/download/" + a.Name }

// Release is one GitHub release.
type Release struct {
	Tag               string
	Prerelease, Draft bool
	Assets            []selfupdate.SourceAsset
}

func (r Release) GetID() int64                        { return 1 }
func (r Release) GetTagName() string                  { return r.Tag }
func (r Release) GetDraft() bool                      { return r.Draft }
func (r Release) GetPrerelease() bool                 { return r.Prerelease }
func (r Release) GetPublishedAt() time.Time           { return time.Date(2026, 9, 23, 19, 43, 55, 0, time.UTC) }
func (r Release) GetReleaseNotes() string             { return "notes" }
func (r Release) GetName() string                     { return r.Tag }
func (r Release) GetAssets() []selfupdate.SourceAsset { return r.Assets }
func (r Release) GetURL() string {
	return "https://github.com/giantswarm/giantswarm-platform-manager/releases/tag/" + r.Tag
}

// BinaryName is the asset CircleCI attaches for this platform.
func BinaryName() string {
	name := "platformctl-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// decoys are assets a release could carry ahead of the binary and
// self-update must never pick: the server binary for this platform (its name
// ends in <os>-<arch> as well) and the chart.
func decoys(tag string) []selfupdate.SourceAsset {
	suffix := runtime.GOOS + "-" + runtime.GOARCH
	return []selfupdate.SourceAsset{
		Asset{10, "giantswarm-platform-manager-" + suffix},
		Asset{11, "giantswarm-platform-manager-" + strings.TrimPrefix(tag, "v") + ".tgz"},
	}
}

// Signed is a release the way CircleCI publishes platformctl: the binary for
// this platform next to its cosign bundle, the bundle first, behind decoys
// that end in the platform's name too — to prove the binary is the one
// picked.
func Signed(tag string) Release {
	return Release{Tag: tag, Assets: append(decoys(tag),
		Asset{BundleID, BinaryName() + ".bundle"},
		Asset{BinaryID, BinaryName()},
	)}
}

// Unsigned is a release with the binary for this platform but no bundle next
// to it: what a release made outside the pipeline would look like.
func Unsigned(tag string) Release {
	return Release{Tag: tag, Assets: append(decoys(tag), Asset{BinaryID, BinaryName()})}
}

// Validator stands in for the cosign validator on the happy path: it asks
// for the same bundle and accepts whatever it is handed, recording what that
// was. The signature check itself is tested where it lives, in
// github.com/giantswarm/selfupdate-cosign; a test with this validator proves
// that self-update wires it in so that nothing unverified reaches the disk.
type Validator struct {
	Asset, BundleName string
	Binary, Bundle    []byte
}

var _ selfupdate.Validator = (*Validator)(nil)

func (v *Validator) GetValidationAssetName(assetName string) string {
	return assetName + ".bundle"
}

func (v *Validator) Validate(assetName string, release, validation []byte) error {
	v.Asset, v.BundleName, v.Binary, v.Bundle = assetName, v.GetValidationAssetName(assetName), release, validation
	return nil
}
