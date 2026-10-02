package verify

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// The migrations that create the objects below, as their reasons end.
const (
	m5Secret  = "Added: the kagent Dex client's secret is the Secret dex-client-kagent under extras/agent-platform/secrets · M5"
	m30Client = "Added: Dex gets a client named backstage for the Dev Portal and every customer portal · M30"
	clientM30 = "backstage"
	secretM5  = "dex-client-kagent"
)

// The objects a plan creates that the record lacks are each of a file the
// plan creates, under a migration — the Secret of the kagent client's file,
// by its manifest's kind, namespace and name — and each Dex client the
// rendered dex patch declares that the patch on record has no entry for,
// under the migration that names its leaves. A created file no migration
// names is drift, not planned; a client the record carries, and every object
// once the record carries it, is no planned object.
func TestPlannedObjectsAreWhatTheRecordLacks(t *testing.T) {
	const (
		repo       = "giantswarm/giantswarm-management-clusters"
		secretFile = "management-clusters/x/extras/agent-platform/secrets/dex-client-kagent-secret.yaml"
		strayFile  = "management-clusters/x/extras/agent-platform/secrets/stray-secret.yaml"
		configRepo = "giantswarm/giantswarm-configs"
		muster     = "dex-client-muster"
	)
	secret := "apiVersion: v1\nkind: Secret\nmetadata:\n  name: " + secretM5 + "\n  namespace: giantswarm\nstringData:\n  secret: x\n"
	stray := "apiVersion: v1\nkind: Secret\nmetadata:\n  name: stray\n"
	withClient := "oidc:\n  staticClients:\n    muster:\n      clientSecretRef:\n        name: " + muster + "\n  extraStaticClients:\n  - id: backstage\n    name: Backstage\n"
	without := "oidc:\n  staticClients:\n    muster:\n      clientSecretRef:\n        name: " + muster + "\n"
	clients := []plan.DexClient{{ID: "muster-x", Client: "muster", SecretRef: muster}, {ID: clientM30, Name: "Backstage"}}
	files := []plan.File{
		{Repository: repo, Path: secretFile, Change: plan.ChangeCreate, Content: secret},
		{Repository: repo, Path: strayFile, Change: plan.ChangeCreate, Content: stray},
		{Repository: configRepo, Path: testDexPatch, Change: plan.ChangeUpdate, Content: withClient},
	}
	diffs := map[string]*fileDiff{
		fileKey(repo, secretFile): {key: fileKey(repo, secretFile), path: secretFile, diffs: []Difference{
			{Path: "metadata.name", Rendered: secretM5, absent: true, Planned: m5Secret}}},
		fileKey(repo, strayFile): {key: fileKey(repo, strayFile), path: strayFile, diffs: []Difference{
			{Path: "metadata.name", Rendered: "stray", absent: true}}},
		fileKey(configRepo, testDexPatch): {key: fileKey(configRepo, testDexPatch), path: testDexPatch, diffs: []Difference{
			{Path: "oidc.extraStaticClients[backstage].id", Rendered: clientM30, absent: true, Planned: m30Client},
			{Path: "oidc.extraStaticClients[backstage].name", Rendered: "Backstage", absent: true, Planned: m30Client}}},
	}
	onRecord := func(dex string) func(string) string {
		return func(key string) string {
			if key == fileKey(configRepo, testDexPatch) {
				return dex
			}
			return ""
		}
	}
	got := plannedObjects(files, diffs, clients, onRecord(without))
	want := PlannedObjects{
		{Kind: kindSecret, Namespace: "giantswarm", Name: secretM5, Reason: m5Secret},
		{Kind: PlannedDexClient, Name: clientM30, Reason: m30Client},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("before the migration: %+v, want %+v", got, want)
	}

	// After the merge the record carries both: the file is unchanged and the
	// dex patch has the client's entry.
	files[0].Change = plan.ChangeUnchanged
	if got := plannedObjects(files, diffs, clients, onRecord(withClient)); len(got) != 0 {
		t.Errorf("after the merge: %+v, want none", got)
	}
}

// The live check of a Secret a migration creates reads planned while the
// Secret is missing, naming the migration; handed no planned object — the
// record carries the Secret, the watch's case after a merge — a missing one
// is drifted, as is one of another name or namespace.
func TestMissingPlannedSecretIsPlanned(t *testing.T) {
	probe := render.DexSecretLoadedProbe("live-dex-client-secrets-loaded", featureIdentity, render.DexNamespace, secretM5, "kagent")
	planned := PlannedObjects{{Kind: kindSecret, Namespace: render.DexNamespace, Name: secretM5, Reason: m5Secret}}
	for _, tc := range []struct {
		name    string
		planned PlannedObjects
		mark    Mark
	}{
		{"before the migration", planned, Planned},
		{"after the merge", nil, Drifted},
		{"another Secret is planned", PlannedObjects{{Kind: kindSecret, Namespace: render.DexNamespace, Name: "dex-client-other", Reason: m5Secret}}, Drifted},
		{"in another namespace", PlannedObjects{{Kind: kindSecret, Namespace: "elsewhere", Name: secretM5, Reason: m5Secret}}, Drifted},
		{"a manifest without a namespace", PlannedObjects{{Kind: kindSecret, Name: secretM5, Reason: m5Secret}}, Planned},
	} {
		x := &executor{opts: LiveOptions{Cluster: &recordingCluster{}, Inputs: Inputs{Planned: tc.planned}}}
		c, _, _ := x.run(context.Background(), probe)
		if c.Mark != tc.mark {
			t.Errorf("%s: %+v, want %q", tc.name, c, tc.mark)
		}
		if named := strings.Contains(c.Message, "· M5"); named != (tc.mark == Planned) || !strings.HasPrefix(c.Message, "does not exist") {
			t.Errorf("%s: message %q", tc.name, c.Message)
		}
	}
}

// A Dex client a migration creates that Dex does not know yet reads planned
// in both Dex auth probes, the anonymous one (verify_capability's) and the
// live one, the answer naming the migration; a client the record carries
// that Dex does not know is drifted, and so is the planned client once the
// record carries it. A drifted client beside a planned one keeps the
// anonymous probe drifted.
func TestUnknownPlannedDexClientIsPlanned(t *testing.T) {
	const callback = "https://portal.example.test/api/auth/oidc/handler/frame"
	host, client := tlsServing(t, fakeDex{connectors: []string{connectorGitHub}, clients: map[string][]string{clientMuster: {callback}}})
	planned := PlannedObjects{{Kind: PlannedDexClient, Name: clientM30, Reason: m30Client}}
	anonymous := definitions.Probe{ID: "dex-auth-request", Feature: featureIdentity,
		URL:          "https://" + host + "/auth?client_id={{.ClientID}}&redirect_uri={{.RedirectURI}}&response_type=code&scope=openid",
		PerDexClient: true, DexConnectorStep: true, Expect: []int{200, 302}}
	for _, tc := range []struct {
		name    string
		planned PlannedObjects
		clients []string
		mark    Mark
	}{
		{"before the migration", planned, []string{clientM30}, Planned},
		{"after the merge", nil, []string{clientM30}, Drifted},
		{"a known client beside it", planned, []string{clientMuster, clientM30}, Planned},
		{"an unknown client beside it", planned, []string{"bogus", clientM30}, Drifted},
	} {
		var cls []plan.DexClient
		for _, id := range tc.clients {
			cls = append(cls, plan.DexClient{ID: id, RedirectURIs: []string{callback}})
		}
		pr := newProber(client)
		pr.planned = tc.planned
		d := pr.probe(context.Background(), base, cls, true, anonymous)
		last := d.Probe.Requests[len(d.Probe.Requests)-1]
		if d.Mark != tc.mark || last.Status != http.StatusNotFound || (last.Planned == m30Client) != (tc.planned != nil) {
			t.Errorf("%s: anonymous: mark %q, requests %+v, want %q", tc.name, d.Mark, d.Probe.Requests, tc.mark)
		}

		x := &executor{lv: &liveRender{probes: []render.Probe{render.DexAuthProbe("live-dex-auth-per-client", featureIdentity, host, clientM30, callback)}}, pr: newProber(client)}
		x.pr.planned = tc.planned
		x.sendHTTP(context.Background())
		c := x.dimension(context.Background(), definitions.Dimension{ID: "live-dex-auth-per-client", Kind: definitions.KindLive}).Live.Checks[0]
		want := Drifted
		if tc.planned != nil {
			want = Planned
		}
		if c.Mark != want || !strings.Contains(c.Message, "Dex does not know client "+clientM30) || strings.HasSuffix(c.Message, "· M30") != (want == Planned) {
			t.Errorf("%s: live: %+v, want %q", tc.name, c, want)
		}
	}
}
