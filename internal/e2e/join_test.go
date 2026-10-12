package e2e

// A hub join over the invented registry: birch brokered for rowan as its
// organisation's hub and is a target of hazel's now, its agent-platform
// values on record still carrying the broker's target, the identity
// provider, muster's server list — birch's own in-cluster servers and
// rowan's — and the gateway's registration URI, which hazel's record
// registers now. The dry run over the set names every entry the render takes
// off birch's muster and the URI that moves to hazel; birch's commit is
// refused on its own servers, and so is the wave, nothing written; forced,
// the dry run carries the warning with nothing refused, and the wave opens
// birch's stage with the entries gone from its patch and hazel's with the
// URI on it.

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
)

// The gateway's registration URI the join moves, and the former hub's keys
// of birch's agent-platform values patch.
const (
	joinGatewayCallback = "https://gateway.acme.test/oauth/callback"
	birchFormerHubKeys  = `        trustedPublicRegistrationRedirectURIs:
          - ` + joinGatewayCallback + `
        tokenExchangeBroker:
          targets:
            rowan:
              dexTokenEndpoint: https://dex.rowan.acme.test/token
              connectorId: acme-simple-oidc
`
	birchFormerHubMCPs = `agent-platform-mcps:
  identityProviders:
    rowan:
      tokenEndpoint: https://dex.rowan.acme.test/token
  mcpServers:
    - cluster: birch
      group: kubernetes
      url: http://mcp-kubernetes.mcp-kubernetes.svc:8080/mcp
    - cluster: rowan
      group: kubernetes
      url: https://mcp-kubernetes.rowan.acme.test/mcp
`
)

// registerClientOnHub puts the gateway's client registration on the hub's
// record: the ConfigMap under extras/agent-platform/mcpclients, listed by
// its kustomization and the tree's.
func registerClientOnHub(st *stack) {
	tree := "management-clusters/" + hub + "/extras/agent-platform/"
	kustomization := "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n"
	st.ghs.addFiles(hubMCs, map[string]string{
		tree + "kustomization.yaml":            kustomization + "  - https://github.com/giantswarm/management-cluster-bases//extras/agent-platform?ref=main\n  - ./secrets\n  - ./mcpclients\n",
		tree + "mcpclients/kustomization.yaml": kustomization + "  - gateway.yaml\n",
		tree + "mcpclients/gateway.yaml":       "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: gateway\n  namespace: agent-platform\ndata:\n  redirectURIs: |\n    " + joinGatewayCallback + "\n",
	})
}

func TestHubJoinWarnsAndRefusesUntilForced(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	sopsFor(t, st.ghs, acmeConfigs, acmeMCs, hubConfigs, hubMCs)
	// The hub brokers into birch, which runs the platform; its broker client
	// is on record in its patch.
	portal := strings.Replace(portalConfig(hub, alder, birch), "        gs:\n", "        gs:\n          clusterTokenBroker:\n            tokenUrl: https://muster."+hub+".example.test/token\n", 1)
	st.ghs.addFile(hubMCs, installations.PortalConfigPath(hub), portal)
	marker := installations.Capabilities()[0].EnabledMarker(hub)
	patch, _ := st.ghs.file(hubConfigs, marker)
	st.ghs.addFile(hubConfigs, marker, strings.Replace(patch, "      server:\n", "      server:\n        tokenExchangeBroker:\n          brokerClients:\n            broker: {}\n", 1))
	st.ghs.addRepo(teleportFleet, map[string]string{tunnelportValuesPath: tunnelportValuesBare})
	registerClientOnHub(st)
	// birch's patch on record: the former hub's keys over what it carries.
	birchMarker := installations.Capabilities()[0].EnabledMarker(birch)
	patch, _ = st.ghs.file(acmeConfigs, birchMarker)
	st.ghs.addFile(acmeConfigs, birchMarker, strings.Replace(patch, "      server:\n", "      server:\n"+birchFormerHubKeys, 1)+birchFormerHubMCPs)
	aliceC := st.mcpClient(t, aliceToken)
	set := []string{hub, birch}

	// The dry run: birch's join names every entry and the URI's move, the
	// commit is refused on birch's own server; hazel's join names the URI
	// moved from birch and refuses nothing.
	dry, text, isErr := dryRun(t, aliceC, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallations: set, tools.ArgInputs: minimalInputs(nil), tools.ArgContent: true})
	if isErr {
		t.Fatal(text)
	}
	b := findPlan(t, dry, birch)
	if b.Join == nil {
		t.Fatalf("birch's plan records no join: %s", text)
	}
	servers := []plan.JoinServer{{Cluster: birch, Group: kubernetesFamily, URL: "http://mcp-kubernetes.mcp-kubernetes.svc:8080/mcp", Own: true}, {Cluster: rowan, Group: kubernetesFamily, URL: "https://mcp-kubernetes.rowan.acme.test/mcp"}}
	if !slices.Equal(b.Join.ExchangeTargets, []string{rowan}) || !slices.Equal(b.Join.IdentityProviders, []string{rowan}) || !slices.Equal(b.Join.Servers, servers) ||
		!slices.Equal(b.Join.RedirectURIs, []plan.JoinURI{{URI: joinGatewayCallback, Peer: hub}}) || b.Join.Forced {
		t.Errorf("birch's join: %+v", b.Join)
	}
	for _, want := range []string{birch + "'s muster serves a registry of its own", "(http://mcp-kubernetes.mcp-kubernetes.svc:8080/mcp)", "2 server entries, 1 exchange target and 1 identity provider go", "forceJoin"} {
		if !strings.Contains(b.CommitRefused, want) {
			t.Errorf("birch's commitRefused lacks %q: %q", want, b.CommitRefused)
		}
	}
	h := findPlan(t, dry, hub)
	if h.Join == nil || !slices.Equal(h.Join.RedirectURIs, []plan.JoinURI{{URI: joinGatewayCallback, Added: true, Peer: birch}}) || len(h.Join.Servers) > 0 || len(h.Join.ExchangeTargets) > 0 {
		t.Errorf("hazel's join: %+v", h.Join)
	}
	if h.CommitRefused != "" {
		t.Errorf("hazel's commit is refused: %s", h.CommitRefused)
	}
	for _, f := range b.Files {
		if strings.HasSuffix(f.Path, "/apps/agent-platform/configmap-values.yaml.patch") && (strings.Contains(f.Content, "tokenExchangeBroker") || strings.Contains(f.Content, "mcpServers")) {
			t.Errorf("birch's patch as the plan writes it keeps the former hub's keys:\n%s", f.Content)
		}
	}

	// The wave: refused whole on birch, nothing recorded.
	text, isErr = call(t, aliceC, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallations: set, tools.ArgInputs: minimalInputs(nil), tools.ArgReason: commitReason, tools.ArgMode: string(tools.ModeCommit)})
	if !isErr || !strings.Contains(text, birch+": "+birch+"'s muster serves a registry of its own") || !strings.Contains(text, "nothing is committed") {
		t.Fatalf("the wave over a join: %v %s", isErr, text)
	}
	if prs := st.remote.PullRequests(); len(prs) > 0 {
		t.Fatalf("a refused wave opened pull requests: %+v", prs)
	}

	// Forced: the warning stands, nothing is refused, and the wave opens
	// both stages — birch's patch without the former hub's keys, hazel's
	// with the URI.
	dry, text, isErr = dryRun(t, aliceC, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallations: set, tools.ArgInputs: minimalInputs(nil), tools.ArgContent: true, tools.ArgForceJoin: true})
	if isErr {
		t.Fatal(text)
	}
	b = findPlan(t, dry, birch)
	if b.Join == nil || !b.Join.Forced || !slices.Equal(b.Join.Servers, servers) || b.CommitRefused != "" {
		t.Fatalf("birch's forced plan: join %+v, commitRefused %q", b.Join, b.CommitRefused)
	}
	for _, p := range dry.Installations {
		for _, f := range p.Files {
			if isSecretFile(f.Path) && strings.Contains(f.Content, "SUPPLIED(") {
				st.ghs.addFile(f.Repository, f.Path, "sops: on record\n")
			}
		}
	}
	seedRemote(t, st)
	text, isErr = call(t, aliceC, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallations: set, tools.ArgInputs: minimalInputs(nil), tools.ArgReason: commitReason, tools.ArgMode: string(tools.ModeCommit), tools.ArgForceJoin: true})
	var w tools.WaveResult
	if isErr || json.Unmarshal([]byte(text), &w) != nil || w.Action == nil || strings.Join(w.Action.Spec.Installations, ",") != hub+","+birch {
		t.Fatalf("the forced wave: %v %s", isErr, text)
	}
	assertNoLeak(t, "the wave's answer", text)
	patches := map[string]string{}
	for _, inst := range []struct{ name, repo string }{{birch, acmeConfigs}, {hub, hubConfigs}} {
		i := slices.IndexFunc(w.PullRequests, func(pr actions.PullRequest) bool { return pr.Installation == inst.name && pr.Repository == inst.repo })
		if i < 0 {
			t.Fatalf("no pull request of %s on %s among %+v", inst.name, inst.repo, w.PullRequests)
		}
		content, ok := st.remote.Files(repoOf(t, inst.repo), w.PullRequests[i].Head)[installations.Capabilities()[0].EnabledMarker(inst.name)]
		if !ok {
			t.Fatalf("%s's patch is not on %s", inst.name, w.PullRequests[i].Head)
		}
		patches[inst.name] = string(content)
	}
	if p := patches[birch]; strings.Contains(p, "tokenExchangeBroker") || strings.Contains(p, "identityProviders") || strings.Contains(p, "mcpServers") || strings.Contains(p, joinGatewayCallback) {
		t.Errorf("birch's patch keeps the former hub's keys:\n%s", p)
	}
	if p := patches[hub]; !strings.Contains(p, "trustedPublicRegistrationRedirectURIs:\n          - "+joinGatewayCallback) {
		t.Errorf("hazel's patch lacks the moved URI:\n%s", p)
	}
}
