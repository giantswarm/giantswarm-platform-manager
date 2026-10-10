package plan

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
)

// A former hub's agent-platform values patch on record and the render of
// the same installation as a target: the broker's target, the identity
// provider and muster's server list — its own in-cluster servers and a
// sibling's — go, and the gateway's registration URI goes with them.
const (
	birchName       = "birch"
	rowanName       = "rowan"
	gatewayCallback = "https://gateway.acme.test/oauth/callback"
	formerHubPatch  = `global:
  domain: birch.acme.test
muster:
  muster:
    oauth:
      server:
        existingSecret: muster-oauth-credentials
        trustedAudiences:
          - dex-k8s-authenticator
        trustedPublicRegistrationRedirectURIs:
          - ` + gatewayCallback + `
        tokenExchangeBroker:
          brokerClients:
            broker: {}
          targets:
            rowan:
              dexTokenEndpoint: https://dex.rowan.acme.test/token
              connectorId: acme-simple-oidc
agent-platform-mcps:
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
    - group: timescale
      url: http://mcp-timescale.mcp-timescale.svc.cluster.local:8080/mcp
`
	targetPatch = `global:
  domain: birch.acme.test
muster:
  muster:
    oauth:
      server:
        existingSecret: muster-oauth-credentials
        trustedAudiences:
          - dex-k8s-authenticator
`
)

// The join names every entry the render takes off muster — the broker's
// target, the identity provider, each server by URL with the installation's
// own marked, the registration URI — and the commit is refused on the own
// servers, naming them and the counts, until the join is forced.
func TestJoinNamesWhatThePatchTakesOffMuster(t *testing.T) {
	p := &Installation{Name: birchName}
	p.join(formerHubPatch, targetPatch, false)
	j := p.Join
	if j == nil {
		t.Fatal("the patch takes the former hub's entries off muster and the plan records no join")
	}
	if !slices.Equal(j.ExchangeTargets, []string{rowanName}) || !slices.Equal(j.IdentityProviders, []string{rowanName}) {
		t.Errorf("exchange targets %v, identity providers %v", j.ExchangeTargets, j.IdentityProviders)
	}
	servers := []JoinServer{
		{Cluster: birchName, Group: "kubernetes", URL: "http://mcp-kubernetes.mcp-kubernetes.svc:8080/mcp", Own: true},
		{Cluster: rowanName, Group: "kubernetes", URL: "https://mcp-kubernetes.rowan.acme.test/mcp"},
		{Group: "timescale", URL: "http://mcp-timescale.mcp-timescale.svc.cluster.local:8080/mcp", Own: true},
	}
	if !slices.Equal(j.Servers, servers) {
		t.Errorf("servers %+v, want %+v", j.Servers, servers)
	}
	if !slices.Equal(j.RedirectURIs, []JoinURI{{URI: gatewayCallback}}) {
		t.Errorf("redirect URIs %+v", j.RedirectURIs)
	}
	if j.Forced {
		t.Error("forced without being asked for")
	}
	refusal := p.JoinRefusal()
	for _, want := range []string{"birch's muster serves a registry of its own",
		"(http://mcp-kubernetes.mcp-kubernetes.svc:8080/mcp, http://mcp-timescale.mcp-timescale.svc.cluster.local:8080/mcp)",
		"3 server entries, 1 exchange target and 1 identity provider go", "forceJoin; platformctl --force-join"} {
		if !strings.Contains(refusal, want) {
			t.Errorf("the refusal lacks %q:\n%s", want, refusal)
		}
	}
	p = &Installation{Name: birchName}
	p.join(formerHubPatch, targetPatch, true)
	if p.Join == nil || !p.Join.Forced || !slices.Equal(p.Join.Servers, servers) {
		t.Errorf("forced, the join still names every entry: %+v", p.Join)
	}
	if r := p.JoinRefusal(); r != "" {
		t.Errorf("forced, the commit is refused: %s", r)
	}
}

// A patch that takes only a sibling's servers off muster warns and holds no
// commit: the installation serves no registry of its own.
func TestJoinWarnsWithoutARefusalWhereNoServerIsTheInstallationsOwn(t *testing.T) {
	current := strings.Replace(formerHubPatch, "    - cluster: birch\n      group: kubernetes\n      url: http://mcp-kubernetes.mcp-kubernetes.svc:8080/mcp\n", "", 1)
	current = strings.Replace(current, "    - group: timescale\n      url: http://mcp-timescale.mcp-timescale.svc.cluster.local:8080/mcp\n", "", 1)
	p := &Installation{Name: birchName}
	p.join(current, targetPatch, false)
	if p.Join == nil || len(p.Join.Servers) != 1 || p.Join.Servers[0].Own || p.Join.Servers[0].Cluster != rowanName {
		t.Fatalf("join %+v", p.Join)
	}
	if r := p.JoinRefusal(); r != "" {
		t.Errorf("refused over a sibling's server: %s", r)
	}
}

// A render that takes nothing off muster records no join; one that puts a
// registration URI on that no plan of a set takes off tells nothing.
func TestJoinRecordsNothingForAPatchThatTakesNothingOff(t *testing.T) {
	p := &Installation{Name: birchName}
	p.join(targetPatch, targetPatch, false)
	if p.Join != nil {
		t.Errorf("an unchanged patch records a join: %+v", p.Join)
	}
	p = &Installation{Name: birchName}
	p.join(targetPatch, formerHubPatch, false)
	if p.Join.Pruned() != nil {
		t.Errorf("a patch that puts the entries on records a join: %+v", p.Join)
	}
	p = &Installation{Name: birchName}
	p.join("not: [a mapping", targetPatch, false)
	if p.Join != nil {
		t.Errorf("a patch that does not decode records a join: %+v", p.Join)
	}
}

// Across a set, a registration URI one plan's patch takes off and another's
// puts on moves between the two, both sides naming the other; a URI put on
// that no plan takes off is no join's, and a plan that held nothing else
// loses its join.
func TestMoveRedirectURIsPairsTheSet(t *testing.T) {
	target := &Installation{Name: birchName}
	target.join(formerHubPatch, targetPatch, false)
	hubRendered := strings.Replace(targetPatch, "          - dex-k8s-authenticator\n", "          - dex-k8s-authenticator\n        trustedPublicRegistrationRedirectURIs:\n          - "+gatewayCallback+"\n", 1)
	hub := &Installation{Name: hazel.Name}
	hub.join(targetPatch, hubRendered, false)
	if hub.Join == nil || hub.Join.Pruned() != nil {
		t.Fatalf("the hub's plan before the pairing: %+v", hub.Join)
	}
	other := &Installation{Name: "maple"}
	other.join(targetPatch, strings.Replace(hubRendered, "gateway.acme.test", "other.acme.test", 1), false)
	MoveRedirectURIs([]*Installation{target, hub, other})
	if !slices.Equal(target.Join.RedirectURIs, []JoinURI{{URI: gatewayCallback, Peer: hazel.Name}}) {
		t.Errorf("the target's URI: %+v", target.Join.RedirectURIs)
	}
	if hub.Join == nil || !slices.Equal(hub.Join.RedirectURIs, []JoinURI{{URI: gatewayCallback, Added: true, Peer: birchName}}) {
		t.Errorf("the hub's URI: %+v", hub.Join)
	}
	if hub.JoinRefusal() != "" {
		t.Errorf("the hub's commit is refused: %s", hub.JoinRefusal())
	}
	if other.Join != nil {
		t.Errorf("a URI put on that no plan takes off is a join: %+v", other.Join)
	}
}

// The plan records the join of its agent-platform values patch on an
// update, forced as asked for.
func TestBuildRecordsTheJoinOfThePlatformPatch(t *testing.T) {
	const patch = "installations/rowan/apps/agent-platform/configmap-values.yaml.patch"
	read := func(_ context.Context, _, path string) (string, error) {
		switch path {
		case rowanKustomization:
			return otherResources, nil
		case patch:
			return formerHubPatch, nil
		}
		return "", gh.ErrNotFound
	}
	for _, forced := range []bool{false, true} {
		p := Build(context.Background(), Options{Definition: filesDefinition(map[string]string{patch: targetPatch}), Installation: rowanInstallation(), Inputs: map[string]any{}, Read: read, ForceJoin: forced})
		if p.Refused != "" {
			t.Fatalf("refused: %s", p.Refused)
		}
		if p.Join == nil || p.Join.Forced != forced || !slices.Equal(p.Join.ExchangeTargets, []string{rowanName}) || len(p.Join.Servers) != 3 {
			t.Fatalf("forced %v: join %+v", forced, p.Join)
		}
		if refused := p.JoinRefusal() != ""; refused == forced {
			t.Errorf("forced %v: refusal %q", forced, p.JoinRefusal())
		}
	}
}
