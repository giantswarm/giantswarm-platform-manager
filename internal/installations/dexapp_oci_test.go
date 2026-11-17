package installations

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// listedDexApp, latestDexApp and rcDexApp are dex-app releases the tag lists carry; latestDexApp is the highest stable one.
const (
	oldestDexApp = "3.4.9"
	listedDexApp = "3.4.24"
	latestDexApp = "3.4.25"
	rcDexApp     = "3.5.0-rc.1"
)

// ociDexApp is the base's dex-app as a Flux OCIRepository with ref and the
// HelmRelease that installs it.
func ociDexApp(ref string) string {
	return "---\napiVersion: source.toolkit.fluxcd.io/v1\nkind: OCIRepository\nmetadata:\n  name: dex-app\n  namespace: giantswarm\nspec:\n  interval: 10m\n  ref:\n    " + ref +
		"\n  url: oci://gsoci.azurecr.io/charts/giantswarm/dex-app\n---\napiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nmetadata:\n  name: dex-app\n  namespace: giantswarm\nspec:\n  chartRef:\n    kind: OCIRepository\n    name: dex-app\n"
}

// withCollectionBases adds the fleet base's stage and shared kustomizations an
// OCIRepository base is read through.
func withCollectionBases(files map[string]string) map[string]string {
	files[fixtureBases+"@main:bases/collections/capa/stages/stable/kustomization.yaml"] = "resources: [../../../shared/base/dex-app.yaml]\n"
	files[fixtureBases+"@main:bases/collections/shared/base/kustomization.yaml"] = "resources: [dex-app.yaml]\n"
	return files
}

// registryTagsOf answers the chart's tags as the registry lists them, and
// records the URLs listed.
func registryTagsOf(listed *[]string, tags ...string) TagLister {
	return func(_ context.Context, ociURL string) ([]string, error) {
		*listed = append(*listed, ociURL)
		return tags, nil
	}
}

// The base's OCIRepository gives the version: an exact tag or semver is it;
// a range resolves to the highest stable tag that satisfies it, as Flux
// picks; an installation's patch on the OCIRepository pins the same way.
func TestDexAppVersionFromAnOCIRepository(t *testing.T) {
	inst := Installation{Name: fixtureInstallation, Repositories: Repositories{ManagementClusters: fixtureMCs}}
	kustomization := fixtureMCs + ":" + CollectionsKustomizationPath(fixtureInstallation)
	baseAtMain := fixtureBases + "@main:" + DexAppBasePath
	tags := []string{oldestDexApp, listedDexApp, "3.4.25-r5aa25357t20261116141604h1932a47", latestDexApp, rcDexApp, "not-a-version", "2.2.3"}
	semverPin := "resources:\n  - https://github.com/" + fixtureBases + "//bases/collections/shared/base?ref=main\npatches:\n  - target:\n      kind: OCIRepository\n      name: dex-app\n    patch: |\n      - op: replace\n        path: /spec/ref/semver\n        value: \">=3.4.0 <3.4.25\"\n"
	cases := []struct {
		name   string
		files  map[string]string
		want   DexAppVersion
		listed []string
	}{
		{"an exact tag", map[string]string{kustomization: collectionsFixture("main", ""), baseAtMain: ociDexApp("tag: 3.4.24")},
			DexAppVersion{Version: listedDexApp, Repository: fixtureBases, Path: DexAppBasePath}, nil},
		{"an exact semver", map[string]string{kustomization: collectionsFixture("main", ""), baseAtMain: ociDexApp("semver: 3.4.24")},
			DexAppVersion{Version: listedDexApp, Repository: fixtureBases, Path: DexAppBasePath}, nil},
		{"every stable release (x.x.x)", map[string]string{kustomization: collectionsFixture("main", ""), baseAtMain: ociDexApp("semver: x.x.x")},
			DexAppVersion{Version: latestDexApp, Range: "x.x.x", Repository: fixtureBases, Path: DexAppBasePath}, []string{dexAppChart}},
		{"a bounded range", map[string]string{kustomization: collectionsFixture("main", ""), baseAtMain: ociDexApp("semver: '>=3.4.0 <3.5.0'")},
			DexAppVersion{Version: latestDexApp, Range: ">=3.4.0 <3.5.0", Repository: fixtureBases, Path: DexAppBasePath}, []string{dexAppChart}},
		{"a range that asks for prereleases", map[string]string{kustomization: collectionsFixture("main", ""), baseAtMain: ociDexApp("semver: '>=3.5.0-0'")},
			DexAppVersion{Version: rcDexApp, Range: ">=3.5.0-0", Repository: fixtureBases, Path: DexAppBasePath}, []string{dexAppChart}},
		{"an installation's range pin on the OCIRepository", map[string]string{kustomization: semverPin, baseAtMain: ociDexApp("semver: x.x.x")},
			DexAppVersion{Version: listedDexApp, Range: ">=3.4.0 <3.4.25", Repository: fixtureMCs, Path: CollectionsKustomizationPath(fixtureInstallation)}, []string{dexAppChart}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var reads, listed []string
			read := filesAt(withCollectionBases(c.files), &reads)
			col, err := readCollections(t.Context(), read, inst)
			require.NoError(t, err)
			got, err := readDexAppVersion(t.Context(), read, registryTagsOf(&listed, tags...), inst, col)
			require.NoError(t, err)
			require.Equal(t, c.want, got)
			require.Equal(t, c.listed, listed)
		})
	}
}

// A range no tag satisfies, a listing that fails, and a base with neither an
// App nor an OCIRepository are errors of the report, naming the file.
func TestDexAppVersionFromAnOCIRepositoryErrors(t *testing.T) {
	inst := Installation{Name: fixtureInstallation, Repositories: Repositories{ManagementClusters: fixtureMCs}}
	kustomization := fixtureMCs + ":" + CollectionsKustomizationPath(fixtureInstallation)
	baseAtMain := fixtureBases + "@main:" + DexAppBasePath
	unreachable := errors.New("registry unreachable")
	cases := []struct {
		name string
		base string
		tags TagLister
		want string
	}{
		{"no tag satisfies the range", ociDexApp("semver: '>=9.0.0'"), registryTagsOf(new([]string), "3.4.25"), `no tag of oci://gsoci.azurecr.io/charts/giantswarm/dex-app satisfies ">=9.0.0"`},
		{"the listing fails", ociDexApp("semver: x.x.x"), func(context.Context, string) ([]string, error) { return nil, unreachable }, "registry unreachable"},
		{"neither an App nor an OCIRepository", "---\nkind: HelmRelease\nmetadata:\n  name: dex-app\n---\nkind: ConfigMap\nmetadata:\n  name: dex-app\n", registryTagsOf(new([]string)), "no App dex-app or OCIRepository dex-app"},
		{"an OCIRepository without a ref", "kind: OCIRepository\nmetadata:\n  name: dex-app\nspec:\n  url: oci://gsoci.azurecr.io/charts/giantswarm/dex-app\n", registryTagsOf(new([]string)), "the OCIRepository dex-app sets no spec.ref.tag or spec.ref.semver"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var reads []string
			read := filesAt(withCollectionBases(map[string]string{kustomization: collectionsFixture("main", ""), baseAtMain: c.base}), &reads)
			col, err := readCollections(t.Context(), read, inst)
			require.NoError(t, err)
			_, err = readDexAppVersion(t.Context(), read, c.tags, inst, col)
			require.ErrorContains(t, err, c.want)
			require.True(t, strings.HasPrefix(err.Error(), "the dex-app on record: "), err.Error())
		})
	}
}

// The registry lister answers a Bearer challenge with the realm's anonymous
// token and follows the Link header across pages to the last.
func TestRegistryTags(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/oauth2/token":
			require.Equal(t, "fleet.example", r.URL.Query().Get("service"))
			require.Equal(t, "repository:charts/giantswarm/dex-app:metadata_read", r.URL.Query().Get("scope"))
			_, _ = w.Write([]byte(`{"access_token":"anon"}`))
		case r.Header.Get("Authorization") != "Bearer anon":
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+srv.URL+`/oauth2/token",service="fleet.example",scope="repository:charts/giantswarm/dex-app:metadata_read"`)
			w.WriteHeader(http.StatusUnauthorized)
		case r.URL.Query().Get("last") == "":
			w.Header().Set("Link", `</v2/charts/giantswarm/dex-app/tags/list?last=3.4.24&n=100>; rel="next"`)
			_, _ = w.Write([]byte(`{"tags":["3.4.9","3.4.24"]}`))
		default:
			require.Equal(t, listedDexApp, r.URL.Query().Get("last"))
			_, _ = w.Write([]byte(`{"tags":["3.4.25"]}`))
		}
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "https://")
	tags, err := registryTags(srv.Client(), "https")(t.Context(), "oci://"+host+"/charts/giantswarm/dex-app")
	require.NoError(t, err)
	require.Equal(t, []string{oldestDexApp, listedDexApp, latestDexApp}, tags)

	_, err = registryTags(srv.Client(), "https")(t.Context(), "https://"+host+"/charts/giantswarm/dex-app")
	require.ErrorContains(t, err, "is no oci://<registry>/<repository> URL")
}

func TestDexAppOCIRepository(t *testing.T) {
	const baseVersion, pinnedVersion, digest = "3.4.24", "3.4.0", "selects a digest"
	const missingTag = "sets no spec.ref.tag"
	const header = "kind: OCIRepository\nmetadata:\n  name: dex-app\nspec:\n  ref:\n"
	const release = "---\nkind: HelmRelease\nmetadata:\n  name: dex-app\nspec:\n  chartRef:\n    kind: OCIRepository\n    name: dex-app\n"
	const target = "patches:\n  - target:\n      kind: OCIRepository\n      name: dex-app\n    patch: |\n"
	cases := []struct {
		name, manifest, patches, want, wantError string
	}{
		{name: "base tag with a release", manifest: header + "    tag: 3.4.24\n" + release, want: baseVersion},
		{name: "release before source", manifest: release + "---\n" + header + "    tag: 2.4.0\n", want: "2.4.0"},
		{name: "OCI pin ignores legacy App pin", manifest: header + "    tag: 2.2.3\n", patches: strings.TrimPrefix(collectionsFixture("main", "3.2.5"), collectionsFixture("main", "")) + "  - target:\n      kind: OCIRepository\n      name: dex-app\n    patch: |\n      - op: replace\n        path: /spec/ref/tag\n        value: 3.4.0\n", want: pinnedVersion},
		{name: "legacy pin alone does not pin an OCI source", manifest: header + "    tag: 3.4.24\n", patches: strings.TrimPrefix(collectionsFixture("main", "3.2.5"), collectionsFixture("main", "")), want: baseVersion},
		{name: "semver wins over tag", manifest: header + "    tag: 3.4.24\n    semver: '>=2.0.0'\n", want: latestDexApp},
		{name: "digest wins over semver and tag", manifest: header + "    tag: 3.4.24\n    semver: '>=2.0.0'\n    digest: sha256:fixture\n", wantError: digest},
		{name: "patch adds semver", manifest: header + "    tag: 3.4.24\n", patches: target + "      - op: add\n        path: /spec/ref/semver\n        value: '>=3.0.0'\n", want: latestDexApp},
		{name: "patch clears semver", manifest: header + "    semver: '>=0.0.0-0'\n", patches: target + "      - op: add\n        path: /spec/ref/tag\n        value: 3.4.0\n      - op: remove\n        path: /spec/ref/semver\n", want: pinnedVersion},
		{name: "patch replaces reference", manifest: header + "    semver: '*'\n", patches: target + "      - op: replace\n        path: /spec/ref\n        value: {tag: 3.4.0}\n", want: pinnedVersion},
		{name: "patch removes reference", manifest: header + "    tag: 3.4.24\n", patches: target + "      - op: remove\n        path: /spec/ref\n", wantError: missingTag},
		{name: "patch replaces spec", manifest: header + "    semver: '*'\n", patches: target + "      - op: replace\n        path: /spec\n        value: {ref: {tag: 3.4.0}}\n", want: pinnedVersion},
		{name: "strategic patch clears reference", manifest: header + "    tag: 3.4.24\n", patches: target + "      spec:\n        ref: null\n", wantError: missingTag},
		{name: "strategic patch clears spec", manifest: header + "    tag: 3.4.24\n", patches: target + "      spec: null\n", wantError: missingTag},
		{name: "strategic pin", manifest: header + "    tag: 2.2.3\n", patches: "patches:\n  - patch: |\n      kind: OCIRepository\n      metadata:\n        name: dex-app\n      spec:\n        ref:\n          tag: 3.4.0\n", want: pinnedVersion},
		{name: "targeted strategic pin clears semver", manifest: header + "    semver: '*'\n", patches: target + "      kind: OCIRepository\n      metadata:\n        name: patched-by-target-selector\n      spec:\n        ref:\n          tag: 3.4.0\n          semver: null\n", want: pinnedVersion},
		{name: "unrelated patch", manifest: header + "    tag: 3.4.24\n", patches: strings.ReplaceAll(target, "name: dex-app", "name: other") + "      - op: replace\n        path: /spec/ref/tag\n        value: 1.0.0\n", want: baseVersion},
		{name: "missing tag", manifest: header + "    tag: ''\n", wantError: missingTag},
		{name: "non-semantic tag", manifest: header + "    tag: latest\n", wantError: "is no semantic version"},
		{name: "malformed patch", manifest: header + "    tag: 3.4.24\n", patches: target + "      [invalid\n", wantError: "yaml:"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			files := map[string]string{
				fixtureBases + "@main:" + DexAppBasePath:                                       test.manifest,
				fixtureBases + "@main:bases/collections/capa/stages/stable/kustomization.yaml": "resources: [../../../shared/base/dex-app.yaml]\n",
			}
			files[fixtureMCs+":"+CollectionsKustomizationPath(fixtureInstallation)] = collectionsFixture("main", "") + test.patches
			var reads, listed []string
			read := filesAt(files, &reads)
			inst := Installation{Name: fixtureInstallation, Repositories: Repositories{ManagementClusters: fixtureMCs}}
			col, err := readCollections(t.Context(), read, inst)
			require.NoError(t, err)
			got, err := readDexAppVersion(t.Context(), read, registryTagsOf(&listed, oldestDexApp, listedDexApp, latestDexApp), inst, col)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("got %+v, %v; want %q", got, err, test.wantError)
				}
				return
			}
			if err != nil || got.Version != test.want {
				t.Fatalf("got %+v, %v; want %s", got, err, test.want)
			}
		})
	}
}
