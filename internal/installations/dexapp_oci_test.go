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

// ociDexApp is the base's dex-app as a Flux OCIRepository with ref and the
// HelmRelease that installs it.
func ociDexApp(ref string) string {
	return "---\napiVersion: source.toolkit.fluxcd.io/v1\nkind: OCIRepository\nmetadata:\n  name: dex-app\n  namespace: giantswarm\nspec:\n  interval: 10m\n  ref:\n    " + ref +
		"\n  url: oci://gsoci.azurecr.io/charts/giantswarm/dex-app\n---\napiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nmetadata:\n  name: dex-app\n  namespace: giantswarm\nspec:\n  chartRef:\n    kind: OCIRepository\n    name: dex-app\n"
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
	tags := []string{"3.4.9", "3.4.24", "3.4.25-r5aa25357t20261116141604h1932a47", "3.4.25", "3.5.0-rc.1", "not-a-version", "2.2.3"}
	semverPin := "resources:\n  - https://github.com/" + fixtureBases + "//bases/collections/shared/base?ref=main\npatches:\n  - target:\n      kind: OCIRepository\n      name: dex-app\n    patch: |\n      - op: replace\n        path: /spec/ref/semver\n        value: \">=3.4.0 <3.4.25\"\n"
	cases := []struct {
		name   string
		files  map[string]string
		want   DexAppVersion
		listed []string
	}{
		{"an exact tag", map[string]string{kustomization: collectionsFixture("main", ""), baseAtMain: ociDexApp("tag: 3.4.24")},
			DexAppVersion{Version: "3.4.24", Repository: fixtureBases, Path: DexAppBasePath}, nil},
		{"an exact semver", map[string]string{kustomization: collectionsFixture("main", ""), baseAtMain: ociDexApp("semver: 3.4.24")},
			DexAppVersion{Version: "3.4.24", Repository: fixtureBases, Path: DexAppBasePath}, nil},
		{"every stable release (x.x.x)", map[string]string{kustomization: collectionsFixture("main", ""), baseAtMain: ociDexApp("semver: x.x.x")},
			DexAppVersion{Version: "3.4.25", Range: "x.x.x", Repository: fixtureBases, Path: DexAppBasePath}, []string{"oci://gsoci.azurecr.io/charts/giantswarm/dex-app"}},
		{"a bounded range", map[string]string{kustomization: collectionsFixture("main", ""), baseAtMain: ociDexApp("semver: '>=3.4.0 <3.5.0'")},
			DexAppVersion{Version: "3.4.25", Range: ">=3.4.0 <3.5.0", Repository: fixtureBases, Path: DexAppBasePath}, []string{"oci://gsoci.azurecr.io/charts/giantswarm/dex-app"}},
		{"a range that asks for prereleases", map[string]string{kustomization: collectionsFixture("main", ""), baseAtMain: ociDexApp("semver: '>=3.5.0-0'")},
			DexAppVersion{Version: "3.5.0-rc.1", Range: ">=3.5.0-0", Repository: fixtureBases, Path: DexAppBasePath}, []string{"oci://gsoci.azurecr.io/charts/giantswarm/dex-app"}},
		{"an installation's range pin on the OCIRepository", map[string]string{kustomization: semverPin, baseAtMain: ociDexApp("semver: x.x.x")},
			DexAppVersion{Version: "3.4.24", Range: ">=3.4.0 <3.4.25", Repository: fixtureMCs, Path: CollectionsKustomizationPath(fixtureInstallation)}, []string{dexAppChart}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var reads, listed []string
			read := filesAt(c.files, &reads)
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
		{"neither an App nor an OCIRepository", "---\nkind: HelmRelease\nmetadata:\n  name: dex-app\n---\nkind: ConfigMap\nmetadata:\n  name: dex-app\n", registryTagsOf(new([]string)), "no App or OCIRepository dex-app"},
		{"an OCIRepository without a ref", "kind: OCIRepository\nmetadata:\n  name: dex-app\nspec:\n  url: oci://gsoci.azurecr.io/charts/giantswarm/dex-app\n", registryTagsOf(new([]string)), "the OCIRepository dex-app sets no version"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var reads []string
			read := filesAt(map[string]string{kustomization: collectionsFixture("main", ""), baseAtMain: c.base}, &reads)
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
			require.Equal(t, "3.4.24", r.URL.Query().Get("last"))
			_, _ = w.Write([]byte(`{"tags":["3.4.25"]}`))
		}
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "https://")
	tags, err := registryTags(srv.Client(), "https")(t.Context(), "oci://"+host+"/charts/giantswarm/dex-app")
	require.NoError(t, err)
	require.Equal(t, []string{"3.4.9", "3.4.24", "3.4.25"}, tags)

	_, err = registryTags(srv.Client(), "https")(t.Context(), "https://"+host+"/charts/giantswarm/dex-app")
	require.ErrorContains(t, err, "is no oci://<registry>/<repository> URL")
}
