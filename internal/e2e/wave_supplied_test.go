package e2e

import (
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/getsops/sops/v3/decrypt"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
)

// A wave whose two targets each ask for a value at commit: the hub and rowan
// both host a portal, and the chat's key is the Component's on each, its
// Secret not on record. The wave renders and commits in one action with both
// values supplied by <installation>/<field>; a value missing or one no target
// asks for refuses the wave whole, by field, with nothing recorded. Each
// installation's credentials file lands encrypted on its stage's branch
// holding its own value (the chart's base64 leaf), and no value appears in the answer, the action, a
// pull request or a plaintext file.
func TestWaveCommitsSuppliedValuesPerInstallation(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	id := sopsFor(t, st.ghs, acmeConfigs, acmeMCs, hubConfigs, hubMCs)
	// rowan's platform values and its own portal on record, put there by hand.
	st.ghs.addFile(acmeConfigs, installations.Capabilities()[0].EnabledMarker(rowan), "muster: {}\n")
	st.ghs.addFile(acmeMCs, installations.PortalConfigPath(rowan), portalConfig(rowan))
	st.ghs.addFile(acmeMCs, "management-clusters/"+rowan+"/extras/backstage/backstage/kustomization.yaml", hubPortalKustomization)
	seedRemote(t, st)
	c := st.mcpClient(t, aliceToken)

	const chat, field = "aiChat", "aiChat.anthropic.apiKey" // #nosec G101 -- field names, not values
	values := map[string]string{hub: "sk-fixture-hub-chat-7d1e", rowan: "sk-fixture-rowan-chat-2b8a"}
	file := func(name string) string {
		return "management-clusters/" + name + "/extras/backstage/agent-platform/ai-chat-credentials.enc.yaml"
	}
	args := func(secrets map[string]any) map[string]any {
		a := map[string]any{tools.ArgInstallations: []string{hub, rowan}, tools.ArgInputs: map[string]any{chat: map[string]any{enabledKey: true}}, tools.ArgReason: commitReason}
		if secrets != nil {
			a[tools.ArgSecrets] = secrets
		}
		return a
	}
	noValue := func(where, text string) {
		t.Helper()
		for _, v := range values {
			if strings.Contains(text, v) {
				t.Fatalf("%s carries a supplied value", where)
			}
		}
	}

	dry, text, isErr := dryRun(t, c, tools.ToolReconcileCapability, args(nil))
	if isErr {
		t.Fatal(text)
	}
	for _, name := range []string{hub, rowan} {
		if p := findPlan(t, dry, name); !slices.Equal(p.SuppliedSecrets, []string{field}) || p.Refused != "" || p.CommitRefused != "" {
			t.Fatalf("%s: supplied %v, refused %q, commitRefused %q", name, p.SuppliedSecrets, p.Refused, p.CommitRefused)
		}
	}

	for _, c2 := range []struct {
		secrets map[string]any
		want    string
	}{
		{nil, "misses the value(s) of " + hub + ": " + field + "; " + rowan + ": " + field},
		{map[string]any{hub + "/" + field: values[hub]}, "misses the value(s) of " + rowan + ": " + field},
		{map[string]any{field: values[hub], birch + "/" + field: values[rowan]}, "names " + birch + "/" + field + ", which the plan does not ask for"},
	} {
		_, text, isErr := commitCall(t, c, tools.ToolReconcileCapability, args(c2.secrets))
		if !isErr || !strings.Contains(text, c2.want) || !strings.Contains(text, "nothing is committed") {
			t.Fatalf("%v: %v %s", c2.secrets, isErr, text)
		}
		noValue("the refusal", text)
	}
	if got := listActionsOf(t, c, hub); len(got) != 0 {
		t.Fatalf("a refused wave recorded %d action(s)", len(got))
	}
	if prs := st.remote.PullRequests(); len(prs) != 0 {
		t.Fatalf("a refused wave opened %d pull request(s)", len(prs))
	}

	_, text, isErr = commitCall(t, c, tools.ToolReconcileCapability, args(map[string]any{hub + "/" + field: values[hub], rowan + "/" + field: values[rowan]}))
	var w tools.WaveResult
	if isErr || json.Unmarshal([]byte(text), &w) != nil || w.Action == nil || strings.Join(w.Order, ",") != hub+","+rowan {
		t.Fatalf("wave commit: %v %s", isErr, text)
	}
	noValue("the wave's answer", text)
	assertNoLeak(t, "the wave's answer", text)
	if a, err := json.Marshal(listActionsOf(t, c, hub)); err != nil {
		t.Fatal(err)
	} else {
		noValue("the action", string(a))
	}

	t.Setenv("SOPS_AGE_KEY", id.String())
	for _, name := range []string{hub, rowan} {
		i := slices.IndexFunc(w.PullRequests, func(pr actions.PullRequest) bool {
			_, ok := st.remote.Files(repoOf(t, pr.Repository), pr.Head)[file(name)]
			return pr.Installation == name && ok
		})
		if i < 0 {
			t.Fatalf("no pull request of %s carries %s: %+v", name, file(name), w.PullRequests)
		}
		pr := w.PullRequests[i]
		content := st.remote.Files(repoOf(t, pr.Repository), pr.Head)[file(name)]
		if !strings.Contains(string(content), "ENC[") {
			t.Fatalf("%s: %s is not encrypted:\n%s", name, file(name), content)
		}
		noValue(name+"'s encrypted "+file(name), string(content))
		assertNoLeak(t, file(name), string(content))
		clear, err := decrypt.Data(content, "yaml")
		if err != nil {
			t.Fatalf("%s: decrypting %s: %v", name, file(name), err)
		}
		// The chart value is a base64 leaf the chart decodes.
		for other, v := range values {
			if got := strings.Contains(string(clear), base64.StdEncoding.EncodeToString([]byte(v))); got != (other == name) {
				t.Errorf("%s: %s holds %s's value: %v", name, file(name), other, got)
			}
		}
	}
	for _, pr := range st.remote.PullRequests() {
		noValue("pull request "+pr.Title, pr.Title+pr.Body)
		for path, content := range st.remote.Files(pr.Repository, pr.Head) {
			if !strings.Contains(path, ".enc.") {
				noValue(path, string(content))
			}
		}
	}
}
