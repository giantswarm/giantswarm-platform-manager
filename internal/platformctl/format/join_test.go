package format

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// The gateway's registration URI a hub join moves, and the former hub's keys
// of the agent-platform values patch on record: the broker's targets, the
// registration URI, and muster's identity providers and server list with
// the installation's own in-cluster servers and a sibling's.
const (
	joinGatewayCallback = "https://gateway.oakridge.example/oauth/callback"
	formerHubServerKeys = `        trustedPublicRegistrationRedirectURIs:
          - ` + joinGatewayCallback + `
        tokenExchangeBroker:
          brokerClients:
            muster-broker-kestrel:
              clientCredentialsSecretRef:
                name: muster-broker-clients
          clientAudiences:
            muster-broker-kestrel:
              - plover
          targets:
            plover:
              dexTokenEndpoint: https://dex.plover.oakridge.example/token
              connectorId: oakridge-simple-oidc
              scopes: openid profile email groups audience:server:client_id:dex-k8s-authenticator
              clientCredentialsSecretRef:
                name: plover-token-exchange-credentials
`
	formerHubMCPs = `agent-platform-mcps:
  identityProviders:
    plover:
      tokenEndpoint: https://dex.plover.oakridge.example/token
      connectorId: oakridge-simple-oidc
      scopes: openid profile email groups
      credentialsSecret:
        name: plover-token-exchange-credentials
        clientIdKey: client-id
        clientSecretKey: client-secret
  mcpServers:
    - cluster: kestrel
      group: kubernetes
      url: http://mcp-kubernetes.mcp-kubernetes.svc:8080/mcp
      timeout: 30
      auth:
        mode: forward
    - cluster: kestrel
      group: prometheus
      url: http://mcp-prometheus.mcp-prometheus.svc:8080/mcp
      timeout: 30
      auth:
        mode: forward
    - cluster: kestrel
      group: capi
      url: http://mcp-capi.mcp-capi.svc:8080/mcp
      timeout: 30
      auth:
        mode: forward
    - cluster: plover
      group: kubernetes
      url: https://mcp-kubernetes.plover.oakridge.example/mcp
      timeout: 30
      auth:
        mode: exchange
        provider: plover
`
)

// fixtureInput is a render fixture's input document, the capability on
// record (the plan of an installation enabled before).
func fixtureInput(t *testing.T, fixture string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "render", "agentplatform", "testdata", fixture, "input.yaml")) //nolint:gosec // a render fixture of this repository
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Input map[string]any `yaml:"input"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc.Input["installation"].(map[string]any)["agentPlatform"] = true
	return doc.Input
}

// onRecord puts every file a render of input makes on record as rendered,
// resolved to the installation's repositories, and the kustomizations its
// includes land in listing them; it answers the key of the installation's
// agent-platform values patch.
func onRecord(t *testing.T, def installations.Capability, input map[string]any, inst, hub installations.Installation, files map[string]string) string {
	t.Helper()
	in, err := def.Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	res, err := def.Render(input, in.SuppliedMarkers(), render.ModeCompare)
	if err != nil {
		t.Fatal(err)
	}
	var patch string
	for repo, fs := range res.Files {
		for p, f := range fs {
			key := plan.ResolveRepository(string(repo), inst, hub) + ":" + p
			files[key] = string(f.Content)
			if strings.HasSuffix(p, "/apps/agent-platform/configmap-values.yaml.patch") {
				patch = key
			}
		}
	}
	lists := map[string]map[string][]string{} // a kustomization → its lists → the entries
	for _, inc := range res.Includes {
		key := plan.ResolveRepository(string(inc.Repository), inst, hub) + ":" + inc.Path
		if lists[key] == nil {
			lists[key] = map[string][]string{}
		}
		list := plan.ListResources
		if inc.Component {
			list = plan.ListComponents
		}
		lists[key][list] = append(lists[key][list], inc.Resource)
	}
	for key, byList := range lists {
		text := "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\n"
		for _, list := range []string{plan.ListResources, plan.ListComponents} {
			if len(byList[list]) == 0 {
				continue
			}
			text += list + ":\n"
			for _, entry := range byList[list] {
				text += "  - " + entry + "\n"
			}
		}
		files[key] = text
	}
	return patch
}

// rolledUp is a set's entry as the manager answers it: the files that change
// or carry a finding, the generated values a commit writes, rotates or
// refuses.
func rolledUp(p plan.Installation) plan.Installation {
	files := []plan.File{}
	for _, f := range p.Files {
		if f.Change != plan.ChangeUnchanged || f.Error != "" || len(f.Unseen) > 0 || len(f.Dropped) > 0 || len(f.Replaced) > 0 {
			f.Generated, f.Kept, f.Creates, f.References = nil, nil, nil, nil
			files = append(files, f)
		}
	}
	p.Files = files
	generated := []plan.GeneratedSecret{}
	for _, g := range p.GeneratedSecrets {
		if !g.Kept || g.Rotates || g.Refusal != "" {
			generated = append(generated, g)
		}
	}
	p.GeneratedSecrets = generated
	return p
}

// The dry run of a hub join as platformctl prints it, held to a golden: a
// set of two. kestrel (the public-customer shape) was its organisation's
// hub and is a target of the fleet hub now, so its agent-platform values on
// record still carry the broker's target, the identity provider, muster's
// server list — its own in-cluster servers and a sibling's — and the
// gateway's registration URI, all of which the render takes off; heron (the
// multi-cluster-aggregator shape) is the organisation's hub, whose record
// registers the gateway's client now, so the render puts the URI on its
// patch. Every other file is on record as rendered. The set pairs the URI,
// each plan's warning names every entry apart from its file rows, and
// kestrel's commit is refused on its own servers.
func TestHubJoinDryRunGolden(t *testing.T) {
	def, ok := installations.FindCapability(installations.AgentPlatform)
	if !ok {
		t.Fatal("no agent-platform definition")
	}
	const customer = "oakridge"
	repos := installations.Repositories{Configs: "giantswarm/" + customer + "-configs", ManagementClusters: "giantswarm/" + customer + "-management-clusters"}
	hub := installations.Installation{Name: "gopher", Customer: "giantswarm", Hub: true, Repositories: installations.Repositories{Configs: "giantswarm/giantswarm-configs", ManagementClusters: "giantswarm/giantswarm-management-clusters"}}
	kestrel := installations.Installation{Name: "kestrel", Customer: customer, Repositories: repos}
	heron := installations.Installation{Name: "heron", Customer: customer, Repositories: repos}
	byName := map[string]installations.Installation{hub.Name: hub, kestrel.Name: kestrel, heron.Name: heron, "plover": {Name: "plover", Customer: customer, Repositories: repos}}
	files := map[string]string{}
	kestrelInput := fixtureInput(t, "public-customer")
	patch := onRecord(t, def, kestrelInput, kestrel, hub, files)
	files[patch] = strings.Replace(files[patch], "      server:\n", "      server:\n"+formerHubServerKeys, 1) + formerHubMCPs
	heronInput := fixtureInput(t, "multi-cluster-aggregator")
	// The portal is kestrel's plan's: the hub's render of its section reads
	// the portal's choices back from kestrel's files in the manager, which
	// this test's typed inputs do not.
	delete(heronInput["installation"].(map[string]any), "portals")
	onRecord(t, def, heronInput, heron, hub, files)
	heronInput["installation"].(map[string]any)["mcpClients"] = []any{map[string]any{"name": "gateway", "redirectURIs": []any{joinGatewayCallback}}}
	read := func(_ context.Context, repository, p string) (string, error) {
		if c, ok := files[repository+":"+p]; ok {
			return c, nil
		}
		return "", fmt.Errorf("%s: %w", p, gh.ErrNotFound)
	}
	build := func(inst installations.Installation, input map[string]any) plan.Installation {
		p := plan.Build(context.Background(), plan.Options{Definition: def, Installation: inst, Hub: hub, Inputs: input, Read: read, Installations: byName})
		if p.Refused != "" {
			t.Fatalf("%s: %s", inst.Name, p.Refused)
		}
		p.State = installations.StateEnabled
		return p
	}
	ph, pk := build(heron, heronInput), build(kestrel, kestrelInput)
	plan.MoveRedirectURIs([]*plan.Installation{&ph, &pk})
	for _, p := range []*plan.Installation{&ph, &pk} {
		p.CommitRefused = p.JoinRefusal()
	}
	plans := []plan.Installation{ph, pk}
	out := tools.CapabilityResult{Caller: "jane", Tool: tools.ToolReconcileCapability, Capability: def.Name, Hub: hub.Name, DryRun: true,
		Order: []string{heron.Name, kestrel.Name}, Installations: []tools.DryRun{{Installation: rolledUp(ph)}, {Installation: rolledUp(pk)}},
		PullRequests: plan.PullRequests(plans, byName, hub), Commit: "the wave"}

	var buf bytes.Buffer
	if err := Plan(&buf, out, false); err != nil {
		t.Fatal(err)
	}
	const golden = "testdata/hub-join-dry-run.golden"
	if *update {
		if err := os.WriteFile(golden, buf.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run with -update to write it)", err)
	}
	if buf.String() != string(want) {
		t.Fatalf("the dry run differs from %s (run with -update to accept):\n%s", golden, buf.String())
	}
}
