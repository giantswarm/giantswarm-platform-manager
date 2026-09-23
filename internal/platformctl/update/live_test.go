package update

import (
	"context"
	"debug/buildinfo"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// liveReleaseEnv names a published release ("v0.42.2") to check against
// GitHub and Sigstore; unset, the test is skipped: it needs the network and
// downloads a binary of about 45 MB.
const liveReleaseEnv = "PLATFORMCTL_LIVE_RELEASE"

// TestLiveReleaseVerifies runs self-update's real path against one published
// release: GitHub's releases, the filter that picks this platform's
// platformctl among the assets, its bundle, the cosign validator for this
// repository against the Sigstore public-good trust root, and the write over
// an executable (a throwaway file here). The installed binary must then report
// the release's tag as its version — the version self-update and the hint
// compare with — or every release would read as newer than itself. And the
// same bundle must refuse the binary with one byte changed.
//
//	PLATFORMCTL_LIVE_RELEASE=v0.42.2 go test -count=1 -run TestLiveReleaseVerifies -v ./internal/platformctl/update/
func TestLiveReleaseVerifies(t *testing.T) {
	want := os.Getenv(liveReleaseEnv)
	if want == "" {
		t.Skipf("set %s to a release tag to verify it against GitHub and Sigstore", liveReleaseEnv)
	}
	u := New()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	up, rel, found, err := u.detect(ctx, u.Validator, want)
	if err != nil {
		t.Fatalf("looking up %s: %v", want, err)
	}
	if !found {
		t.Fatalf("%s carries no %s", want, AssetName())
	}
	if rel.AssetName != AssetName() {
		t.Fatalf("picked %q, want %q", rel.AssetName, AssetName())
	}
	t.Logf("%s: %s (%d bytes), bundle %s", tag(rel), rel.AssetURL, rel.AssetByteSize, rel.ValidationAssetURL)

	exe := filepath.Join(t.TempDir(), AssetName())
	if err := os.WriteFile(exe, []byte("placeholder"), 0o700); err != nil { //nolint:gosec // an executable
		t.Fatal(err)
	}
	if err := up.UpdateTo(ctx, rel, exe); err != nil {
		t.Fatalf("%s did not verify: %v", AssetName(), err)
	}
	t.Logf("%s verified against %s.bundle as a CircleCI build of %s and installed", AssetName(), AssetName(), Repository)

	info, err := buildinfo.ReadFile(exe)
	if err != nil {
		t.Fatalf("reading the installed binary's build info: %v", err)
	}
	if info.Main.Version != want {
		t.Errorf("the %s binary reports version %q, want its tag %q", want, info.Main.Version, want)
	}
	t.Logf("the installed binary reports %s (%s)", info.Main.Version, info.GoVersion)

	// The same bundle refuses the binary with one byte changed.
	binary, err := os.ReadFile(filepath.Clean(exe))
	if err != nil {
		t.Fatal(err)
	}
	binary[len(binary)/2] ^= 0xff
	body, err := u.Source.DownloadReleaseAsset(ctx, rel, rel.ValidationAssetID)
	if err != nil {
		t.Fatalf("downloading the bundle: %v", err)
	}
	defer func() { _ = body.Close() }()
	bundle, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := u.Validator.Validate(AssetName(), binary, bundle); err == nil {
		t.Error("the bundle verified a tampered binary")
	} else {
		t.Logf("a tampered binary is refused: %v", err)
	}
}
