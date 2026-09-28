package agentplatform

import (
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// The portal reads GitHub through this installation's muster only where the
// muster holds the person's GitHub grant and the installation is neither the
// hub nor a broker for targets: then the broker, the Component's gs block and
// its broker Secret render; in every other case none of them does. The
// broker client's secret is one generated name in muster's Secret and the
// Component's, so the commit draws it once for both.
func TestPortalGitHubThroughOwnMuster(t *testing.T) {
	const (
		patchPath = "giantswarm/riverbend-configs/installations/otter/apps/agent-platform/configmap-values.yaml.patch"
		component = "giantswarm/riverbend-management-clusters/management-clusters/otter/extras/backstage/agent-platform/"
		muster    = "giantswarm/riverbend-management-clusters/management-clusters/otter/extras/agent-platform/secrets/muster-broker-clients.yaml"
		// server is the shape's GitHub server's name.
		server = "github"
	)
	for _, tc := range []struct {
		name   string
		change func(inst map[string]any)
		want   bool
	}{
		{"the grant on a portal's installation", func(map[string]any) {}, true},
		{"no grant server", func(inst map[string]any) {
			inst["mcpServers"] = []any{map[string]any{"name": server, "url": "https://api.githubcopilot.com/mcp/", "auth": "oauth"}}
		}, false},
		{"no hosted portal", func(inst map[string]any) { inst["portals"] = inst["portals"].([]any)[1:] }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input, secrets := loadInput(t, shapePortalGitHubGrant)
			tc.change(input["installation"].(map[string]any))
			result, err := Render(input, secrets, render.ModeCommit)
			if err != nil {
				t.Fatal(err)
			}
			tree := result.Tree()
			patch := string(tree[patchPath])
			got := map[string]bool{
				"broker":           strings.Contains(patch, "tokenExchangeBroker"),
				"grant target":     strings.Contains(patch, "grantIssuer: "+githubGrantIssuer),
				"gs block":         strings.Contains(string(tree[component+"app-config.yaml"]), "brokerAudience: github"),
				"component secret": tree[component+portalBrokerFile] != nil,
				"muster secret":    tree[muster] != nil,
			}
			for what, rendered := range got {
				if rendered != tc.want {
					t.Errorf("%s rendered %v, want %v", what, rendered, tc.want)
				}
			}
			if tc.want {
				name := "GENERATED(otter-" + brokerClientSecret
				if !strings.Contains(string(tree[muster]), name+")") || !strings.Contains(string(tree[component+portalBrokerFile]), name+".base64)") {
					t.Errorf("the broker client's secret is not one value in both Secrets:\n%s\n%s", tree[muster], tree[component+portalBrokerFile])
				}
			}
		})
	}
}
