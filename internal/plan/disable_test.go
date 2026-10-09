package plan

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// strip takes the render's part out of a file with several owners and
// nothing else: a scalar with its key, a mapping once emptied, a list's
// entries by id, name or value, a kustomization's listed entries alone —
// and leaves what a capability that stays renders at the same place.
func TestStrip(t *testing.T) {
	cases := []struct {
		name, rendered, current, want string
		keeps                         []string
	}{
		{name: "a scalar goes with its key, the rest stays in order with its comments",
			rendered: "agentPlatform:\n  kagentApiV2: true\n",
			current:  "# the record\nbaseDomain: a.example # keep\nagentPlatform:\n  kagentApiV2: true\nprovider: capa\n",
			want:     "# the record\nbaseDomain: a.example # keep\nprovider: capa\n"},
		{name: "a mapping keeps what it holds besides",
			rendered: "oidc:\n  staticClients:\n    muster:\n      clientSecretRef: {name: dex-client-muster, key: secret}\n",
			current:  "oidc:\n  staticClients:\n    muster:\n      clientSecretRef: {name: dex-client-muster, key: secret}\n      redirectURIs: ['https://x']\n",
			want:     "oidc:\n  staticClients:\n    muster:\n      redirectURIs: ['https://x']\n"},
		{name: "list entries go by id, scalars by value, an emptied list with its key",
			rendered: "oidc:\n  extraStaticClients:\n    - id: kagent\n      name: kagent-ui\n  staticClients:\n    dexK8SAuthenticator:\n      trustedPeers: [kagent]\n",
			current:  "oidc:\n  extraStaticClients:\n    - id: kagent\n      name: renamed by hand\n    - id: own\n      name: own\n  staticClients:\n    dexK8SAuthenticator:\n      trustedPeers:\n        - kagent\n",
			want:     "oidc:\n  extraStaticClients:\n    - id: own\n      name: own\n"},
		{name: "what a capability that stays renders stays",
			rendered: "oidc:\n  staticClients:\n    muster:\n      clientSecretRef: {name: dex-client-muster}\n    mcpKubernetes:\n      clientSecretRef: {name: dex-client-mcp-kubernetes}\n    dexK8SAuthenticator:\n      trustedPeers: [a, b]\n",
			keeps:    []string{"oidc:\n  staticClients:\n    mcpKubernetes:\n      clientSecretRef: {name: dex-client-mcp-kubernetes}\n    dexK8SAuthenticator:\n      trustedPeers: [b]\n"},
			current:  "oidc:\n  staticClients:\n    muster:\n      clientSecretRef: {name: dex-client-muster}\n    mcpKubernetes:\n      clientSecretRef: {name: dex-client-mcp-kubernetes}\n    dexK8SAuthenticator:\n      trustedPeers: [a, b]\n",
			want:     "oidc:\n  staticClients:\n    mcpKubernetes:\n      clientSecretRef: {name: dex-client-mcp-kubernetes}\n    dexK8SAuthenticator:\n      trustedPeers: [b]\n"},
		{name: "a kustomization loses its listed entries and nothing else",
			rendered: "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - ./secrets\npatches:\n  - patch: x\n",
			current:  "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - ./secrets\n  - ./mcpclients\npatches:\n  - patch: x\n",
			want:     "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - ./mcpclients\npatches:\n  - patch: x\n"},
		{name: "the render's header comment goes with its part",
			rendered: "# Rendered by the definition.\noidc:\n  staticClients:\n    muster: {}\n",
			current:  "ingress: {}\n# Rendered by the definition.\noidc:\n  staticClients:\n    muster: {}\n    own: {}\n",
			want:     "ingress: {}\noidc:\n  staticClients:\n    own: {}\n"},
		{name: "nothing of the render on record leaves the file byte for byte",
			rendered: "a: 1\n",
			current:  "b:   2 # odd spacing\n",
			want:     "b:   2 # odd spacing\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			keeps := make([][]byte, 0, len(tc.keeps))
			for _, k := range tc.keeps {
				keeps = append(keeps, []byte(k))
			}
			got, err := strip([]byte(tc.rendered), []byte(tc.current), keeps...)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("got:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}

// UnlistEntry is ListEntry's mirror: the entry goes, a list left empty
// goes with its key, an entry not listed changes nothing.
func TestUnlistEntry(t *testing.T) {
	const extras = "# The installation's extras.\nkind: Kustomization\nresources:\n  - ./monitoring/ # keep\n  - ./agent-platform/\ncomponents:\n  - ./agent-platform/\n"
	cases := []struct{ name, current, list, entry, want string }{
		{name: "the entry goes", current: extras, list: ListResources, entry: "./agent-platform",
			want: "# The installation's extras.\nkind: Kustomization\nresources:\n  - ./monitoring/ # keep\ncomponents:\n  - ./agent-platform/\n"},
		{name: "an emptied list goes", current: extras, list: ListComponents, entry: dDir,
			want: "# The installation's extras.\nkind: Kustomization\nresources:\n  - ./monitoring/ # keep\n  - ./agent-platform/\n"},
		{name: "not listed", current: extras, list: ListResources, entry: "./zot/", want: extras},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := UnlistEntry([]byte(tc.current), tc.list, tc.entry)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("got:\n%s", got)
			}
		})
	}
}

// Invented repositories and installations of the disable's fixtures.
const (
	dConfigs = "giantswarm/oak-configs"
	dMC      = "giantswarm/oak-management-clusters"
	dInst    = "kestrel"
	dHubName = "gopher"
	dStays   = "stays"
	dDir     = "./agent-platform/"
	kagentNS = "kagent"
)

var (
	dInstallation = installations.Installation{Name: dInst, Customer: "oak", Repositories: installations.Repositories{Configs: dConfigs, ManagementClusters: dMC}}
	dHub          = installations.Installation{Name: dHubName, Customer: "giantswarm", Hub: true, Repositories: installations.Repositories{Configs: "giantswarm/giantswarm-configs", ManagementClusters: "giantswarm/giantswarm-management-clusters"}}
	dPlatform, _  = installations.FindCapability(installations.AgentPlatform)
)

// repoReader reads a fixed repository state; a path under unreadable fails.
func repoReader(files map[string]string, unreadable string) (Reader, func(ctx context.Context, repository, dir string) ([]string, error)) {
	read := func(_ context.Context, repository, p string) (string, error) {
		if unreadable != "" && strings.HasPrefix(p, unreadable) {
			return "", fmt.Errorf("%s: refused", p)
		}
		content, ok := files[repository+":"+p]
		if !ok {
			return "", fmt.Errorf("%s: %w", p, gh.ErrNotFound)
		}
		return content, nil
	}
	list := func(_ context.Context, repository, dir string) ([]string, error) {
		var out []string
		for k := range files {
			if p, ok := strings.CutPrefix(k, repository+":"); ok && strings.HasPrefix(p, dir+"/") {
				out = append(out, p)
			}
		}
		sort.Strings(out)
		return out, nil
	}
	return read, list
}

func mc(p string) string { return "management-clusters/" + dInst + "/" + p }

// disableFixture is an installation with the agent platform on record: the
// definition's directory with a file another owner put there, a pairing
// with the hub, the shared Dex patch with a client of the installation's
// own, the hub's portal component in the hub's tree, and an MCP server a
// capability that stays renders too.
func disableFixture() (*render.Result, map[string]string) {
	marker := dPlatform.EnabledMarker(dInst)
	res := &render.Result{
		Files: render.Fileset{
			"giantswarm/oak-configs": {
				marker: {Content: []byte("muster:\n  enabled: true\n")},
				"installations/" + dInst + "/apps/dex-app/configmap-values.yaml.patch": {Content: []byte("oidc:\n  staticClients:\n    muster:\n      clientSecretRef: {name: dex-client-muster}\n    mcpKubernetes:\n      clientSecretRef: {name: dex-client-mcp-kubernetes}\n")},
			},
			dMC: {
				mc("extras/agent-platform/kustomization.yaml"):             {Content: []byte("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - https://github.com/giantswarm/bases//extras/agent-platform?ref=main\n  - ./secrets\n")},
				mc("extras/agent-platform/secrets/dex-client-muster.yaml"): {Content: []byte("apiVersion: v1\nkind: Secret\nmetadata:\n  name: dex-client-muster\n  namespace: giantswarm\n")},
				mc("extras/agent-platform/secrets/exchange.yaml"): {Content: []byte("apiVersion: v1\nkind: Secret\nmetadata:\n  name: dex-client-exchange\n  namespace: giantswarm\n"),
					Generated: []render.Generated{{Name: "exchange", Peer: &render.Peer{Installation: dHubName, Path: "management-clusters/gopher/extras/agent-platform/secrets/kestrel.yaml"}}}},
				mc("extras/mcp-kubernetes/kustomization.yaml"):                               {Content: []byte("kind: Kustomization\nresources:\n  - oauth.enc.yaml\n")},
				"management-clusters/gopher/extras/backstage/agent-platform/app-config.yaml": {Content: []byte("agentPlatform: {}\n")},
			},
		},
		Includes: []render.Include{
			{Repository: dMC, Path: mc("extras/kustomization.yaml"), Resource: dDir},
			{Repository: dMC, Path: mc("extras/kustomization.yaml"), Resource: "./mcp-kubernetes/"},
			{Repository: dMC, Path: "management-clusters/gopher/extras/backstage/kustomization.yaml", Resource: dDir, Component: true},
		},
	}
	files := map[string]string{
		dConfigs + ":" + marker: "muster:\n  enabled: true\nkagent: {}\n",
		dConfigs + ":installations/" + dInst + "/apps/dex-app/configmap-values.yaml.patch":  "ingress: {}\noidc:\n  staticClients:\n    muster:\n      clientSecretRef: {name: dex-client-muster}\n    mcpKubernetes:\n      clientSecretRef: {name: dex-client-mcp-kubernetes}\n",
		dConfigs + ":installations/" + dInst + "/config.yaml.patch":                         "baseDomain: kestrel.example\n",
		dMC + ":" + mc("extras/kustomization.yaml"):                                         "kind: Kustomization\nresources:\n  - ./agent-platform/\n  - ./mcp-kubernetes/\n  - ./zot/\n",
		dMC + ":" + mc("extras/agent-platform/kustomization.yaml"):                          string(res.Files[dMC][mc("extras/agent-platform/kustomization.yaml")].Content),
		dMC + ":" + mc("extras/agent-platform/secrets/dex-client-muster.yaml"):              "apiVersion: v1\nkind: Secret\nmetadata:\n  name: dex-client-muster\n  namespace: giantswarm\nsops: {}\n",
		dMC + ":" + mc("extras/agent-platform/secrets/exchange.yaml"):                       "apiVersion: v1\nkind: Secret\nmetadata:\n  name: dex-client-exchange\n  namespace: giantswarm\nsops: {}\n",
		dMC + ":" + mc("extras/agent-platform/mcpclients/gateway.yaml"):                     "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: gateway\n  namespace: agent-platform\n",
		dMC + ":" + mc("extras/mcp-kubernetes/kustomization.yaml"):                          "kind: Kustomization\nresources:\n  - oauth.enc.yaml\n",
		dMC + ":management-clusters/gopher/extras/backstage/agent-platform/app-config.yaml": "agentPlatform: {}\n",
		dMC + ":management-clusters/gopher/extras/backstage/kustomization.yaml":             "kind: Kustomization\ncomponents:\n  - ./agent-platform/\n",
	}
	return res, files
}

// remainingServers is a capability that stays on record: it renders the
// MCP server's directory, its include and its Dex client.
func remainingServers() []Remaining {
	return []Remaining{{Capability: "cluster-mcp-servers", Result: &render.Result{
		Files: render.Fileset{
			dMC:                      {mc("extras/mcp-kubernetes/kustomization.yaml"): {Content: []byte("kind: Kustomization\nresources:\n  - oauth.enc.yaml\n  - revision.enc.yaml\n")}},
			"giantswarm/oak-configs": {"installations/" + dInst + "/apps/dex-app/configmap-values.yaml.patch": {Content: []byte("oidc:\n  staticClients:\n    mcpKubernetes:\n      clientSecretRef: {name: dex-client-mcp-kubernetes}\n")}},
		},
		Includes: []render.Include{{Repository: dMC, Path: mc("extras/kustomization.yaml"), Resource: "./mcp-kubernetes/"}},
	}}}
}

func disableOf(t *testing.T, res *render.Result, files map[string]string, unreadable string, remaining []Remaining) Disablement {
	t.Helper()
	read, list := repoReader(files, unreadable)
	return Disable(context.Background(), DisableOptions{Definition: dPlatform, Installation: dInstallation, Hub: dHub, Result: res, Remaining: remaining, Read: read, List: list})
}

func changeOf(d Disablement, repository, p string) Change {
	for _, r := range d.Files {
		if r.Repository == repository && r.Path == p {
			return r.Change
		}
	}
	for _, s := range d.Stays {
		if s.Repository == repository && s.Path == p {
			return dStays
		}
	}
	return ""
}

// The rules of the disable, one by one, over one installation.
func TestDisable(t *testing.T) {
	res, files := disableFixture()
	d := disableOf(t, res, files, "", remainingServers())
	dex := "installations/" + dInst + "/apps/dex-app/configmap-values.yaml.patch"

	t.Run("the marker goes whole, whatever else it carries", func(t *testing.T) {
		if got := changeOf(d, dConfigs, dPlatform.EnabledMarker(dInst)); got != ChangeDelete {
			t.Fatalf("marker: %q", got)
		}
	})
	t.Run("the definition's directory leaves whole, another owner's file with it", func(t *testing.T) {
		for _, p := range []string{"kustomization.yaml", "secrets/dex-client-muster.yaml", "secrets/exchange.yaml", "mcpclients/gateway.yaml"} {
			if got := changeOf(d, dMC, mc("extras/agent-platform/"+p)); got != ChangeDelete {
				t.Errorf("%s: %q", p, got)
			}
		}
		if !slices.ContainsFunc(d.Files, func(r Removal) bool { return r.Unrendered && strings.HasSuffix(r.Path, "mcpclients/gateway.yaml") }) {
			t.Error("the file another owner put there is not named unrendered")
		}
		if !slices.Equal(d.Directories, []string{dMC + ":" + mc("extras/agent-platform") + "/"}) {
			t.Errorf("directories: %v", d.Directories)
		}
	})
	t.Run("the include is unlisted, an include that stays stays listed", func(t *testing.T) {
		for _, r := range d.Files {
			if r.Path == mc("extras/kustomization.yaml") {
				if r.Change != ChangeUpdate || r.Content != "kind: Kustomization\nresources:\n  - ./mcp-kubernetes/\n  - ./zot/\n" {
					t.Fatalf("%s %q", r.Change, r.Content)
				}
				return
			}
		}
		t.Fatal("extras/kustomization.yaml not edited")
	})
	t.Run("the shared Dex patch loses the definition's part and keeps the rest", func(t *testing.T) {
		for _, r := range d.Files {
			if r.Path == dex {
				want := "ingress: {}\noidc:\n  staticClients:\n    mcpKubernetes:\n      clientSecretRef: {name: dex-client-mcp-kubernetes}\n"
				if r.Change != ChangeUpdate || r.Content != want {
					t.Fatalf("%s %q", r.Change, r.Content)
				}
				return
			}
		}
		t.Fatal("dex patch not edited")
	})
	t.Run("a file a capability that stays renders stays, untouched", func(t *testing.T) {
		if got := changeOf(d, dMC, mc("extras/mcp-kubernetes/kustomization.yaml")); got != dStays && got != "" {
			t.Fatalf("mcp-kubernetes: %q", got)
		}
		if slices.ContainsFunc(d.Files, func(r Removal) bool { return strings.Contains(r.Path, "mcp-kubernetes") }) {
			t.Fatal("the disable writes a file of the capability that stays")
		}
	})
	t.Run("another installation's files stay, named", func(t *testing.T) {
		for _, p := range []string{"management-clusters/gopher/extras/backstage/agent-platform/app-config.yaml", "management-clusters/gopher/extras/backstage/kustomization.yaml"} {
			if got := changeOf(d, dMC, p); got != dStays {
				t.Errorf("%s: %q", p, got)
			}
		}
		if !slices.Equal(d.Others, []string{dHubName}) {
			t.Errorf("others: %v", d.Others)
		}
	})
	t.Run("the record is never written", func(t *testing.T) {
		if got := changeOf(d, dConfigs, "installations/"+dInst+"/config.yaml.patch"); got != "" {
			t.Fatalf("record: %q", got)
		}
	})
	t.Run("a paired value is named with its other side", func(t *testing.T) {
		if !slices.Equal(d.Pairings(), []string{"gopher:management-clusters/gopher/extras/agent-platform/secrets/kestrel.yaml"}) {
			t.Fatalf("pairings: %v", d.Pairings())
		}
	})
	t.Run("the checklist holds what the deleted files declare and the remote bases", func(t *testing.T) {
		got := make([]string, 0, len(d.Checklist))
		for _, o := range Checklist(d.Checklist) {
			got = append(got, o.String())
		}
		want := []string{"ConfigMap agent-platform/gateway", "Secret giantswarm/dex-client-muster", "Secret giantswarm/dex-client-exchange"}
		if !slices.Equal(got, want) {
			t.Errorf("checklist %v", got)
		}
		if !slices.Equal(d.Bases, []string{"https://github.com/giantswarm/bases//extras/agent-platform?ref=main"}) {
			t.Errorf("bases %v", d.Bases)
		}
	})
	t.Run("one pull request per repository", func(t *testing.T) {
		var repos []string
		for _, pr := range d.PullRequests {
			repos = append(repos, pr.Repository)
		}
		sort.Strings(repos)
		if !slices.Equal(repos, []string{dConfigs, dMC}) {
			t.Fatalf("pull requests %v", repos)
		}
	})
}

// A file the disable cannot read is named unknown: nothing goes blind.
func TestDisableUnreadable(t *testing.T) {
	res, files := disableFixture()
	d := disableOf(t, res, files, mc("extras/kustomization.yaml"), nil)
	if u := d.Unknown(); len(u) != 1 || u[0].Path != mc("extras/kustomization.yaml") {
		t.Fatalf("unknown %v", u)
	}
}

// A shared file left with nothing of anyone's goes; the record never does.
func TestDisableEmptiedSharedFile(t *testing.T) {
	res, files := disableFixture()
	dex := "installations/" + dInst + "/apps/dex-app/configmap-values.yaml.patch"
	files[dConfigs+":"+dex] = string(res.Files["giantswarm/oak-configs"][dex].Content)
	d := disableOf(t, res, files, "", nil)
	if got := changeOf(d, dConfigs, dex); got != ChangeDelete {
		t.Fatalf("dex patch: %q", got)
	}
}

// The checklist deletes the HelmReleases first, then their sources and the
// rest, the Namespaces — what is inside one goes with it —, the Secrets
// and ConfigMaps outside them last.
func TestChecklist(t *testing.T) {
	in := []Object{
		{Kind: "Secret", Namespace: "giantswarm", Name: "dex-client-muster"},
		{Kind: "Namespace", Name: kagentNS},
		{Kind: "Secret", Namespace: kagentNS, Name: "inside"},
		{Kind: "Konfiguration", Namespace: fluxNS, Name: "agent-platform-konfiguration"},
		{Kind: "HelmRelease", Namespace: fluxNS, Name: installations.AgentPlatform},
		{Kind: "OCIRepository", Namespace: fluxNS, Name: installations.AgentPlatform},
		{Kind: "HelmRelease", Namespace: fluxNS, Name: installations.AgentPlatform},
	}
	var got []string
	for _, o := range Checklist(in) {
		got = append(got, o.String())
	}
	want := []string{"HelmRelease flux-giantswarm/agent-platform", "Konfiguration flux-giantswarm/agent-platform-konfiguration", "OCIRepository flux-giantswarm/agent-platform", "Namespace kagent", "Secret giantswarm/dex-client-muster"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestRemoteBase(t *testing.T) {
	repo, dir, ref, ok := RemoteBase("https://github.com/giantswarm/management-cluster-bases//extras/agent-platform?ref=main")
	if !ok || repo != "giantswarm/management-cluster-bases" || dir != "extras/agent-platform" || ref != "main" {
		t.Fatalf("%q %q %q %v", repo, dir, ref, ok)
	}
	for _, url := range []string{"./secrets", "https://example.com/x//y", "https://github.com/giantswarm/x"} {
		if _, _, _, ok := RemoteBase(url); ok {
			t.Errorf("%s parsed", url)
		}
	}
}
