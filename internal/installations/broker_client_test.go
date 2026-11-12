package installations

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/render"
	"github.com/giantswarm/giantswarm-platform-manager/render/agentplatform"
)

const (
	fleetClient    = "A9gQ-8CaXDUy9G8T1roAuXA_p4bzc8zZBvcBc0x4Zt4"
	hubPatchPath   = "installations/gazelle/apps/agent-platform/configmap-values.yaml.patch"
	managerClient  = "agent-manager"
	fleetSecret    = "muster-broker-clients"       //nolint:gosec // a Secret's name, no credential
	managerSecret  = "agent-manager-github-broker" //nolint:gosec // a Secret's name, no credential
	brokerPatchFmt = "muster:\n  muster:\n    oauth:\n      server:\n        tokenExchangeBroker:\n          brokerClients:\n%s"
)

// brokerPatch is a hub's values patch with the broker clients named, each
// with the Secret its credentials are read from.
func brokerPatch(clients map[string]string) string {
	var b strings.Builder
	for id, secret := range clients {
		b.WriteString("            " + id + ":\n              clientCredentialsSecretRef: {name: " + secret + "}\n")
	}
	return strings.Replace(brokerPatchFmt, "%s", b.String(), 1)
}

// TestFleetBrokerClient reads the fleet's broker client back from a hub's
// patch: beside agent-manager's own exchange client (gazelle's record since
// agentManager.githubTokenExchange.enabled), alone, or none; two clients
// that are no manager's are a record the definition did not render.
func TestFleetBrokerClient(t *testing.T) {
	for _, c := range []struct {
		name    string
		clients map[string]string
		want    string
		refused string
	}{
		{"the fleet's beside agent-manager's exchange client", map[string]string{fleetClient: fleetSecret, managerClient: managerSecret}, fleetClient, ""},
		{"the fleet's alone", map[string]string{fleetClient: fleetSecret}, fleetClient, ""},
		{"agent-manager's alone", map[string]string{managerClient: managerSecret}, "", ""},
		{"none", map[string]string{}, "", ""},
		{"two that are no manager's", map[string]string{fleetClient: fleetSecret, "other": fleetSecret}, "", "2 fleet broker clients (" + fleetClient + ", other)"},
		{"two that are no manager's, beside agent-manager's", map[string]string{fleetClient: fleetSecret, "other": "x", managerClient: managerSecret}, "", "2 fleet broker clients (" + fleetClient + ", other)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			// Map order varies between runs: every run reads the same.
			for range 20 {
				got, err := fleetBrokerClient(hubPatchPath, brokerPatch(c.clients))
				if c.refused != "" {
					require.ErrorContains(t, err, c.refused)
					require.ErrorContains(t, err, hubPatchPath)
					continue
				}
				require.NoError(t, err)
				require.Equal(t, c.want, got)
			}
		})
	}
	got, err := fleetBrokerClient(hubPatchPath, "kagent:\n  enabled: true\n")
	require.NoError(t, err)
	require.Empty(t, got, "a patch without a broker")
}

// TestRenderedBrokerClientReadsBack renders every agent-platform golden
// shape, as is and with agent-manager's credential grant exchange on, and
// reads the fleet's broker client back from the rendered patch: what the
// definition renders, the read-back reads. As is, a brokering shape reads
// back its one broker client; with the exchange on, beside agent-manager's
// own client, it reads back the same one.
func TestRenderedBrokerClientReadsBack(t *testing.T) {
	shapes := os.DirFS(filepath.Join("..", "..", "render", "agentplatform", "testdata"))
	entries, err := fs.ReadDir(shapes, ".")
	require.NoError(t, err)
	var exchanged []string
	for _, entry := range entries {
		raw, err := fs.ReadFile(shapes, entry.Name()+"/input.yaml")
		if err != nil {
			continue // consumption/ and other non-shape directories
		}
		var asIs string
		for _, exchange := range []bool{false, true} {
			var doc struct {
				Input   map[string]any    `yaml:"input"`
				Secrets map[string]string `yaml:"secrets"`
			}
			require.NoError(t, yaml.Unmarshal(raw, &doc))
			if exchange {
				doc.Input["agentManager"] = map[string]any{"githubTokenExchange": map[string]any{"enabled": true}}
				delete(doc.Input, "versions")
			}
			result, err := agentplatform.Render(doc.Input, doc.Secrets, render.ModeCommit)
			if exchange && err != nil {
				continue // a shape that cannot take the exchange (no agent-manager, the 3 line, commit mode)
			}
			require.NoError(t, err, entry.Name())
			var patch string
			for name, content := range result.Tree() {
				if strings.HasSuffix(filepath.ToSlash(name), "/apps/agent-platform/configmap-values.yaml.patch") {
					patch = string(content)
				}
			}
			require.NotEmpty(t, patch, entry.Name())
			got, err := fleetBrokerClient(entry.Name(), patch)
			require.NoError(t, err, "%s (exchange %v)", entry.Name(), exchange)
			if !exchange {
				asIs = got
				if got != "" {
					require.Contains(t, patch, "\n            "+got+":\n              clientCredentialsSecretRef:\n                name: "+fleetSecret+"\n", entry.Name())
				}
				continue
			}
			require.Contains(t, patch, "\n            "+managerClient+":\n", "%s: the exchange renders agent-manager's broker client", entry.Name())
			require.Equal(t, asIs, got, "%s: agent-manager's exchange client changes the fleet's broker client read back", entry.Name())
			if asIs != "" {
				exchanged = append(exchanged, entry.Name())
			}
		}
	}
	// The hub shape brokers for its targets and runs the agent-manager on the
	// 4 line, as gazelle does: it must render with the exchange, so a
	// refusal there fails here instead of being skipped.
	require.Contains(t, exchanged, "hub-private-target", "the hub shape did not render with agent-manager's exchange: the read-back beside it is unproved")
}
