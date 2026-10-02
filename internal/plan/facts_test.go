package plan

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// factsInput is a parsed input whose render needs the record's facts.
type factsInput struct {
	fakeInput
	facts []render.Fact
}

func (in factsInput) Facts() []render.Fact { return in.facts }

// azureFacts are two facts of the record, as a provider's render needs them.
var azureFacts = []render.Fact{
	{Key: "provider.azure.subscriptionId", Source: "the AzureCluster's subscription", Renders: "the snapshot store's identity"},
	{Key: "provider.azure.oidcIssuerUrl", Source: "the API server's service-account issuer", Renders: "the snapshot store's identity"},
}

// The plan reads the facts the render needs from the installation's record
// and names the ones it leaves empty — absent, or empty as the shared default
// carries them —, the record and where each value comes from; a commit is held
// while one is, and the comparison runs all the same. A record that sets them
// plans as before; a record that cannot be read leaves every fact missing.
func TestBuildNamesTheRecordsMissingFacts(t *testing.T) {
	record := render.RecordPath("rowan")
	file := acmeConfigs + ":" + record
	for name, c := range map[string]struct {
		record  string
		err     error
		missing []string
		refusal string
	}{
		"both set":   {record: "provider:\n  kind: capz\n  azure:\n    subscriptionId: sub\n    oidcIssuerUrl: https://issuer\n"},
		"none":       {record: "provider:\n  kind: capz\n", missing: []string{"provider.azure.subscriptionId", "provider.azure.oidcIssuerUrl"}, refusal: "the record " + file + " leaves provider.azure.subscriptionId and provider.azure.oidcIssuerUrl empty; the installation's configuration renders the snapshot store's identity from them, and the chart refuses the install without them. Set them there first: provider.azure.subscriptionId is the AzureCluster's subscription; provider.azure.oidcIssuerUrl is the API server's service-account issuer"},
		"one empty":  {record: "provider:\n  azure:\n    subscriptionId: sub\n    oidcIssuerUrl: \"\"\n", missing: []string{"provider.azure.oidcIssuerUrl"}, refusal: "leaves provider.azure.oidcIssuerUrl empty; the installation's configuration renders the snapshot store's identity from it, and the chart refuses the install without it. Set it there first: provider.azure.oidcIssuerUrl is"},
		"not a leaf": {record: "provider:\n  azure:\n    subscriptionId: {id: sub}\n    oidcIssuerUrl: https://issuer\n", missing: []string{"provider.azure.subscriptionId"}, refusal: "leaves provider.azure.subscriptionId empty"},
		"unreadable": {err: gh.ErrNotFound, missing: []string{"provider.azure.subscriptionId", "provider.azure.oidcIssuerUrl"}, refusal: "the record " + file + ", which has to set provider.azure.subscriptionId and provider.azure.oidcIssuerUrl, could not be read: " + gh.ErrNotFound.Error()},
	} {
		t.Run(name, func(t *testing.T) {
			def := filesDefinition(map[string]string{"installations/rowan/a.yaml": "key: a\n"})
			def.Parse = func(any) (render.Input, error) { return factsInput{facts: azureFacts}, nil }
			read := func(_ context.Context, repository, path string) (string, error) {
				if repository == acmeConfigs && path == record {
					return c.record, c.err
				}
				return "", gh.ErrNotFound
			}
			p := Build(context.Background(), Options{Definition: def, Installation: rowanInstallation(), Inputs: map[string]any{}, Read: read})
			if p.Refused != "" || len(p.Files) == 0 {
				t.Fatalf("the comparison runs: refused %q, %d files", p.Refused, len(p.Files))
			}
			var missing []string
			if p.MissingFacts != nil {
				for _, f := range p.MissingFacts.Missing {
					missing = append(missing, f.Key)
				}
			}
			if strings.Join(missing, ",") != strings.Join(c.missing, ",") {
				t.Errorf("missing %v, want %v", missing, c.missing)
			}
			got := p.FactsRefusal()
			if (c.refusal == "") != (got == "") || !strings.Contains(got, c.refusal) {
				t.Errorf("refusal %q, want %q", got, c.refusal)
			}
		})
	}
}

// A render that needs no fact does not read the record for one.
func TestBuildWithoutFactsReadsNoRecord(t *testing.T) {
	read := func(_ context.Context, _, path string) (string, error) {
		if path == render.RecordPath("rowan") {
			t.Error("the record was read for no fact")
		}
		return "", errors.New("absent")
	}
	p := Build(context.Background(), Options{Definition: filesDefinition(map[string]string{"installations/rowan/a.yaml": "key: a\n"}), Installation: rowanInstallation(), Inputs: map[string]any{}, Read: read})
	if p.MissingFacts != nil || p.FactsRefusal() != "" {
		t.Errorf("facts %+v, refusal %q", p.MissingFacts, p.FactsRefusal())
	}
}
