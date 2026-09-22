package plan

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// The repositories of the ordering scenarios: an installation's pair, the
// hub's pair and teleport-fleet.
const (
	oakConfigs    = "acme/configs"
	oakMCs        = "acme/management-clusters"
	hubConfigs    = "example/configs"
	hubMCs        = "example/management-clusters"
	teleportFleet = "giantswarm/teleport-fleet"
)

var (
	oak    = installations.Installation{Name: "oak", Customer: "acme", Repositories: installations.Repositories{Configs: oakConfigs, ManagementClusters: oakMCs}}
	hazel  = installations.Installation{Name: "hazel", Customer: "example", Repositories: installations.Repositories{Configs: hubConfigs, ManagementClusters: hubMCs}}
	byName = map[string]installations.Installation{oak.Name: oak, hazel.Name: hazel}
)

const (
	kagentSecret = "apiVersion: v1\nkind: Secret\nmetadata:\n  name: dex-client-kagent\n  namespace: giantswarm\nstringData:\n  secret: GENERATED(oak-kagent-dex-client-secret)\n"
	dexPatch     = "oidc:\n  staticClients:\n    muster:\n      clientSecretRef:\n        name: dex-client-muster\n  extraStaticClients:\n    - id: kagent\n      secretRef:\n        name: dex-client-kagent\n        key: secret\n"
	remoteApps   = "apiVersion: access.giantswarm.io/v1alpha1\nkind: RemoteApp\nmetadata:\n  name: dex-alder\nspec:\n  appName: dex-alder\n  tokenName: dex-alder-bot-token\n---\napiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nmetadata:\n  name: tunnelport\nspec:\n  values:\n    trustBundle:\n      secretName: spiffe-bundle\n      tokenName: tunnelport-trust-bundle-token-oak\n"
	tunnelport   = "tunnelport:\n  consumers:\n    oak:\n      issuer: https://irsa.oak.acme.test\n  trustBundle:\n    tokens:\n      - name: tunnelport-trust-bundle-token-oak\n        consumer: oak\n  tunnels:\n    - name: dex-alder\n      tokens:\n        - name: dex-alder-bot-token\n          consumer: oak\n        - name: dex-alder-bot-token-hazel\n          consumer: hazel\n"
	musterValues = "muster:\n  oauth:\n    existingSecret: muster-oauth-credentials\n  kagent:\n    apiKeySecretRef:\n      name: kagent-model-key\n"
)

// What a file creates and references is read from its documents: a Secret
// manifest creates the Secret, the tunnelport values create every token they
// list; a secretRef of any spelling, an existingSecret and a secretName name a
// Secret, a tokenName a provision token. Each object once, sorted; a document
// that does not parse contributes nothing.
func TestObjects(t *testing.T) {
	for _, tc := range []struct {
		name, content       string
		creates, references []string
	}{
		{"secret", kagentSecret, []string{"Secret/dex-client-kagent"}, nil},
		{"dex patch", dexPatch, nil, []string{"Secret/dex-client-kagent", "Secret/dex-client-muster"}},
		{"remoteapps and release", remoteApps, nil, []string{"ProvisionToken/dex-alder-bot-token", "ProvisionToken/tunnelport-trust-bundle-token-oak", "Secret/spiffe-bundle"}},
		{"tunnelport values", tunnelport, []string{"ProvisionToken/dex-alder-bot-token", "ProvisionToken/dex-alder-bot-token-hazel", "ProvisionToken/tunnelport-trust-bundle-token-oak"}, nil},
		{"values", musterValues, nil, []string{"Secret/kagent-model-key", "Secret/muster-oauth-credentials"}},
		{"not yaml", "- a\nb: [", nil, nil},
		{"empty", "", nil, nil},
	} {
		creates, references := objects([]byte(tc.content))
		if !slices.Equal(creates, tc.creates) || !slices.Equal(references, tc.references) {
			t.Errorf("%s: creates %v references %v, want %v and %v", tc.name, creates, references, tc.creates, tc.references)
		}
	}
}

// A file on record that carries the object already introduces nothing: the
// Secret exists, the token is listed; a file with no record introduces every
// object it creates, and a new token among the edited tunnelport values is
// the one introduced.
func TestIntroduced(t *testing.T) {
	if got := introduced(kagentSecret, "", false); !slices.Equal(got, []string{"Secret/dex-client-kagent"}) {
		t.Errorf("no record: %v", got)
	}
	encrypted := strings.Replace(kagentSecret, "GENERATED(oak-kagent-dex-client-secret)", "ENC[AES256_GCM,data:x,type:str]", 1)
	if got := introduced(kagentSecret, encrypted, true); len(got) != 0 {
		t.Errorf("the Secret on record: %v", got)
	}
	without := strings.Replace(tunnelport, "        - name: dex-alder-bot-token-hazel\n          consumer: hazel\n", "", 1)
	if got := introduced(tunnelport, without, true); !slices.Equal(got, []string{"ProvisionToken/dex-alder-bot-token-hazel"}) {
		t.Errorf("one token gained: %v", got)
	}
}

// file is a file to create in the ordering scenarios, with what it creates.
func file(repo, path string, creates ...string) File {
	return File{Repository: repo, Path: path, Change: ChangeCreate, Creates: creates}
}

func repos(prs []PullRequest) string {
	var out []string
	for _, pr := range prs {
		out = append(out, pr.Repository)
	}
	return strings.Join(out, " ")
}

// The pull requests merge in the order the files' references imply: the one
// that creates the Secret a Dex client names before the one that names it,
// teleport-fleet's tokens before the RemoteApps and the release that name
// them; every pull request is numbered in that order and says which ones it
// follows and why. Among the pull requests no reference orders, the
// repositories' kind: configs, management-clusters, the hub's pair,
// teleport-fleet.
func TestPullRequestsFollowTheReferences(t *testing.T) {
	secret, patch := file(oakMCs, "extras/agent-platform/secrets/dex-client-kagent-secret.yaml", "Secret/dex-client-kagent"), File{Repository: oakConfigs, Path: "installations/oak/apps/dex-app/configmap-values.yaml.patch", Change: ChangeUpdate, References: []string{"Secret/dex-client-kagent", "Secret/dex-client-muster"}}
	tokens := File{Repository: teleportFleet, Path: tunnelportValuesFile, Change: ChangeUpdate, Creates: []string{"ProvisionToken/dex-alder-bot-token", "ProvisionToken/tunnelport-trust-bundle-token-oak"}}
	apps := file(oakMCs, "extras/agent-platform/tunnelport/remoteapps.yaml")
	apps.References = []string{"ProvisionToken/dex-alder-bot-token", "ProvisionToken/tunnelport-trust-bundle-token-oak", "Secret/spiffe-bundle"}
	for _, tc := range []struct {
		name  string
		files []File
		order string
		after map[string]string // repository → AfterClause
	}{
		{"kind order alone", []File{file(oakConfigs, "a"), file(oakMCs, "b"), file(hubConfigs, "c"), file(hubMCs, "d"), file(teleportFleet, "e")},
			strings.Join([]string{oakConfigs, oakMCs, hubConfigs, hubMCs, teleportFleet}, " "), nil},
		{"a referenced Dex client", []File{patch, secret}, oakMCs + " " + oakConfigs,
			map[string]string{oakConfigs: "after " + oakMCs + " (Secret/dex-client-kagent)"}},
		{"a Secret on record is no dependency", []File{patch, File{Repository: oakMCs, Path: "s", Change: ChangeUpdate}}, oakConfigs + " " + oakMCs, nil},
		{"a tunnel token", []File{file(oakConfigs, "values"), apps, tokens}, strings.Join([]string{oakConfigs, teleportFleet, oakMCs}, " "),
			map[string]string{oakMCs: "after " + teleportFleet + " (ProvisionToken/dex-alder-bot-token, ProvisionToken/tunnelport-trust-bundle-token-oak)"}},
		{"a fresh enable with a private target", []File{patch, secret, apps, tokens}, strings.Join([]string{teleportFleet, oakMCs, oakConfigs}, " "),
			map[string]string{oakConfigs: "after " + oakMCs + " (Secret/dex-client-kagent)", oakMCs: "after " + teleportFleet + " (ProvisionToken/dex-alder-bot-token, ProvisionToken/tunnelport-trust-bundle-token-oak)"}},
		{"the same pull request creates what it names", []File{file(oakMCs, "s", "Secret/x"), File{Repository: oakMCs, Path: "v", Change: ChangeCreate, References: []string{"Secret/x"}}, file(oakConfigs, "c")}, oakConfigs + " " + oakMCs, nil},
		{"a cycle falls back to the kind order", []File{File{Repository: oakConfigs, Path: "a", Change: ChangeCreate, Creates: []string{"Secret/a"}, References: []string{"Secret/b"}}, File{Repository: oakMCs, Path: "b", Change: ChangeCreate, Creates: []string{"Secret/b"}, References: []string{"Secret/a"}}, File{Repository: hubMCs, Path: "h", Change: ChangeCreate, References: []string{"Secret/a"}}},
			strings.Join([]string{oakConfigs, oakMCs, hubMCs}, " "), map[string]string{oakConfigs: "after " + oakMCs + " (Secret/b)", oakMCs: "after " + oakConfigs + " (Secret/a)", hubMCs: "after " + oakConfigs + " (Secret/a)"}},
	} {
		prs := PullRequests([]Installation{{Name: oak.Name, Files: tc.files}}, byName, hazel)
		if got := repos(prs); got != tc.order {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.order)
		}
		for i, pr := range prs {
			if pr.Order != i+1 {
				t.Errorf("%s: %s numbered %d at %d", tc.name, pr.Repository, pr.Order, i+1)
			}
			if got := pr.AfterClause(); got != tc.after[pr.Repository] {
				t.Errorf("%s: %s %q, want %q", tc.name, pr.Repository, got, tc.after[pr.Repository])
			}
		}
	}
}

// The plan reads what every file creates and references against the record:
// the Secret file to create introduces its Secret, the dex patch on record
// names it, and the pull requests of the two repositories come out
// management-clusters first — where the Secret is on record already, the
// kind order stands.
func TestBuildOrdersTheReferencedClient(t *testing.T) {
	def := installations.Capability{
		Name:  "dex",
		Parse: func(any) (render.Input, error) { return fakeInput{}, nil },
		Render: func(any, map[string]string, render.Mode) (*render.Result, error) {
			return &render.Result{Files: render.Fileset{
				"giantswarm/acme-configs":             {"installations/oak/apps/dex-app/configmap-values.yaml.patch": render.File{Content: []byte(dexPatch)}},
				"giantswarm/acme-management-clusters": {"management-clusters/oak/extras/agent-platform/secrets/dex-client-kagent-secret.yaml": render.File{Content: []byte(kagentSecret)}},
			}}, nil
		},
	}
	onRecord := map[string]string{oakConfigs + ":installations/oak/apps/dex-app/configmap-values.yaml.patch": "oidc:\n  staticClients:\n    muster:\n      clientSecretRef:\n        name: dex-client-muster\n"}
	read := func(_ context.Context, repository, path string) (string, error) {
		if c, ok := onRecord[repository+":"+path]; ok {
			return c, nil
		}
		return "", gh.ErrNotFound
	}
	p := Build(context.Background(), Options{Definition: def, Installation: oak, Hub: hazel, Inputs: map[string]any{}, Read: read})
	if p.Refused != "" {
		t.Fatal(p.Refused)
	}
	for _, f := range p.Files {
		switch f.Repository {
		case oakMCs:
			if !slices.Equal(f.Creates, []string{"Secret/dex-client-kagent"}) || len(f.References) != 0 {
				t.Errorf("the Secret file: creates %v references %v", f.Creates, f.References)
			}
		case oakConfigs:
			if len(f.Creates) != 0 || !slices.Equal(f.References, []string{"Secret/dex-client-kagent", "Secret/dex-client-muster"}) {
				t.Errorf("the dex patch: creates %v references %v", f.Creates, f.References)
			}
		}
	}
	prs := PullRequests([]Installation{p}, byName, hazel)
	if repos(prs) != oakMCs+" "+oakConfigs || prs[1].AfterClause() != "after "+oakMCs+" (Secret/dex-client-kagent)" {
		t.Fatalf("pull requests: %s, %+v", repos(prs), prs)
	}
	// The Secret on record, encrypted, in another namespace: its file is an
	// update that creates nothing.
	onRecord[oakMCs+":management-clusters/oak/extras/agent-platform/secrets/dex-client-kagent-secret.yaml"] = strings.NewReplacer("GENERATED(oak-kagent-dex-client-secret)", "ENC[AES256_GCM,data:x,type:str]", "namespace: giantswarm", "namespace: old").Replace(kagentSecret)
	p = Build(context.Background(), Options{Definition: def, Installation: oak, Hub: hazel, Inputs: map[string]any{}, Read: read})
	if prs := PullRequests([]Installation{p}, byName, hazel); repos(prs) != oakConfigs+" "+oakMCs || len(prs[1].After) != 0 {
		t.Fatalf("the Secret on record: %s, %+v (files %+v)", repos(prs), prs, p.Files)
	}
}
