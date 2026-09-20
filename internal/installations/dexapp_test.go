package installations

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
)

// The fixture: an installation, its management-clusters repository, the
// fleet's base repository, and the dex-app versions before and with the
// referenced Dex client secrets.
const (
	fixtureInstallation = "maple"
	fixtureMCs          = "fleet/umbra-management-clusters"
	fixtureBases        = "fleet/management-cluster-bases"
	fleetDexApp         = "2.2.3"
	pinnedDexApp        = "3.2.2"
)

// The installation's collections kustomization: the fleet's base as a remote
// resource at ref, and, with pin, the installation's own patch on the App
// dex-app in the form the management-clusters repositories carry it.
func collectionsFixture(ref, pin string) string {
	s := "resources:\n  - https://github.com/" + fixtureBases + "//bases/collections/capa/stages/stable?ref=" + ref + "\n"
	if pin != "" {
		s += "patches:\n  # the installation runs ahead of the fleet's pin\n  - target:\n      kind: App\n      name: dex-app\n      namespace: giantswarm\n    patch: |\n      - op: replace\n        path: /spec/version\n        value: " + pin + "\n"
	}
	return s
}

// dexAppManifest is the base's App dex-app at version.
func dexAppManifest(version string) string {
	return "apiVersion: application.giantswarm.io/v1alpha1\nkind: App\nmetadata:\n  name: dex-app\n  namespace: giantswarm\nspec:\n  catalog: control-plane-catalog\n  name: dex-app\n  namespace: giantswarm\n  version: " + version + "\n"
}

// filesAt answers the files on record by repository@ref:path (@ref left out
// for the default branch); a path not there is gh.ErrNotFound, and every
// read is recorded.
func filesAt(m map[string]string, reads *[]string) refReader {
	return func(_ context.Context, repository, path, ref string) (string, error) {
		key := repository
		if ref != "" {
			key += "@" + ref
		}
		key += ":" + path
		*reads = append(*reads, key)
		if content, ok := m[key]; ok {
			return content, nil
		}
		return "", gh.ErrNotFound
	}
}

// The dex-app on record is the installation's own pin where its collections
// kustomization patches the App's version, else the base's App at the ref
// the kustomization names; an installation without the kustomization, or a
// base without the App, has none.
func TestDexAppVersionFromTheRecord(t *testing.T) {
	inst := Installation{Name: fixtureInstallation, Repositories: Repositories{ManagementClusters: fixtureMCs}}
	kustomization := fixtureMCs + ":" + CollectionsKustomizationPath(fixtureInstallation)
	baseAtMain, baseAtPin := fixtureBases+"@main:"+DexAppBasePath, fixtureBases+"@v2.1.0:"+DexAppBasePath
	cases := []struct {
		name  string
		files map[string]string
		want  DexAppVersion
		read  string // the last file read
	}{
		{"the installation's own pin wins over the base", map[string]string{kustomization: collectionsFixture("main", pinnedDexApp), baseAtMain: dexAppManifest(fleetDexApp)},
			DexAppVersion{Version: pinnedDexApp, Repository: fixtureMCs, Path: CollectionsKustomizationPath(fixtureInstallation)}, kustomization},
		{"without a pin, the base at the ref the kustomization names", map[string]string{kustomization: collectionsFixture("v2.1.0", ""), baseAtMain: dexAppManifest(pinnedDexApp), baseAtPin: dexAppManifest(fleetDexApp)},
			DexAppVersion{Version: fleetDexApp, Repository: fixtureBases, Path: DexAppBasePath}, baseAtPin},
		{"a strategic merge patch pins as well", map[string]string{kustomization: "resources:\n  - github.com/" + fixtureBases + "//bases/collections/shared/base?ref=main\npatches:\n  - patch: |\n      apiVersion: application.giantswarm.io/v1alpha1\n      kind: App\n      metadata:\n        name: dex-app\n        namespace: giantswarm\n      spec:\n        version: 3.2.2\n", baseAtMain: dexAppManifest(fleetDexApp)},
			DexAppVersion{Version: pinnedDexApp, Repository: fixtureMCs, Path: CollectionsKustomizationPath(fixtureInstallation)}, kustomization},
		{"a patch on another App pins nothing", map[string]string{kustomization: "resources:\n  - https://github.com/" + fixtureBases + "//bases/collections/shared/base?ref=main\npatches:\n  - target:\n      kind: App\n      name: happa\n    patch: |\n      - op: replace\n        path: /spec/version\n        value: 9.9.9\n", baseAtMain: dexAppManifest(fleetDexApp)},
			DexAppVersion{Version: fleetDexApp, Repository: fixtureBases, Path: DexAppBasePath}, baseAtMain},
		{"no kustomization on record", map[string]string{baseAtMain: dexAppManifest(fleetDexApp)}, DexAppVersion{}, kustomization},
		{"no App in the base at that ref", map[string]string{kustomization: collectionsFixture("main", "")}, DexAppVersion{}, baseAtMain},
	}
	for _, c := range cases {
		var reads []string
		got, err := readDexAppVersion(context.Background(), filesAt(c.files, &reads), inst)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
		if last := reads[len(reads)-1]; last != c.read {
			t.Errorf("%s: read %v, want the last read %s", c.name, reads, c.read)
		}
	}
	if (DexAppVersion{Version: pinnedDexApp, Repository: fixtureMCs, Path: "p"}).Source() != fixtureMCs+":p" {
		t.Error("the source is repository:path")
	}
}

// A kustomization or a base that cannot be read or parsed, a kustomization
// that names no remote base, and a version that is no semantic version are
// errors of the report, each naming the file.
func TestDexAppVersionErrors(t *testing.T) {
	inst := Installation{Name: fixtureInstallation, Repositories: Repositories{ManagementClusters: fixtureMCs}}
	kustomization := fixtureMCs + ":" + CollectionsKustomizationPath(fixtureInstallation)
	baseAtMain := fixtureBases + "@main:" + DexAppBasePath
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"a kustomization that does not decode", map[string]string{kustomization: "resources: [not\n"}, CollectionsKustomizationPath(fixtureInstallation)},
		{"a kustomization without a remote base", map[string]string{kustomization: "resources:\n  - ./apps/\n"}, "names no remote base"},
		{"a pin that is no semantic version", map[string]string{kustomization: collectionsFixture("main", "latest")}, `"latest" is no semantic version`},
		{"a base without the App", map[string]string{kustomization: collectionsFixture("main", ""), baseAtMain: "kind: ConfigMap\nmetadata:\n  name: dex-app\n"}, "no App dex-app"},
		{"a base App without a version", map[string]string{kustomization: collectionsFixture("main", ""), baseAtMain: "kind: App\nmetadata:\n  name: dex-app\nspec: {}\n"}, "sets no spec.version"},
		{"a base version that is no semantic version", map[string]string{kustomization: collectionsFixture("main", ""), baseAtMain: dexAppManifest("latest")}, DexAppBasePath},
	}
	for _, c := range cases {
		var reads []string
		got, err := readDexAppVersion(context.Background(), filesAt(c.files, &reads), inst)
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.HasPrefix(err.Error(), "the dex-app on record: ") {
			t.Errorf("%s: %+v, %v; want an error naming %q", c.name, got, err, c.want)
		}
	}
	refused := errors.New("github: 403")
	forbidden := func(_ context.Context, _, _, _ string) (string, error) { return "", refused }
	if _, err := readDexAppVersion(context.Background(), forbidden, inst); !errors.Is(err, refused) {
		t.Errorf("a read that fails is the report's error: %v", err)
	}
}

// A remote resource names its repository with or without the scheme, with
// or without a path inside the repository, and its ref after ?ref=; a local
// path or another host names none.
func TestParseRemoteBase(t *testing.T) {
	cases := map[string]remoteBase{
		"https://github.com/fleet/management-cluster-bases//bases/collections/capa/stages/stable?ref=main": {Repository: fixtureBases, Ref: "main"},
		"github.com/fleet/management-cluster-bases//bases/collections/shared/base?ref=v2.1.0":              {Repository: fixtureBases, Ref: "v2.1.0"},
		"https://github.com/fleet/management-cluster-bases.git?ref=main":                                   {Repository: fixtureBases, Ref: "main"},
		"https://github.com/fleet/management-cluster-bases":                                                {Repository: fixtureBases},
		"./apps/":                            {},
		"https://gitlab.example/fleet/bases": {},
		"https://github.com/fleet":           {},
	}
	for resource, want := range cases {
		got, ok := parseRemoteBase(resource)
		if ok != (want.Repository != "") || got != want {
			t.Errorf("%s: %+v %v, want %+v", resource, got, ok, want)
		}
	}
}
