package plan

import (
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/render/agentplatform"
)

const providerAppsList = "muster.muster.providerApps"

func keepCredentialProviderApps(rendered, current *yaml.Node, kept *[]Kept) error {
	path := strings.Split(providerAppsList, ".")
	currentApps := at(current, path...)
	if currentApps == nil {
		return nil
	}
	if currentApps.Kind != yaml.SequenceNode {
		return fmt.Errorf("%s on record must be a list", providerAppsList)
	}
	renderedApps := at(rendered, path...)
	managedEnabled := false
	if renderedApps != nil {
		for _, app := range renderedApps.Content {
			if managedManagerProviderApp(app) {
				managedEnabled = true
			}
		}
	}
	seen := make(map[string]bool)
	for _, app := range currentApps.Content {
		name := entry(app, keyName)
		if app.Kind != yaml.MappingNode || name == nil || name.Kind != yaml.ScalarNode || name.Value == "" || seen[name.Value] {
			return fmt.Errorf("%s on record must contain uniquely named provider apps", providerAppsList)
		}
		seen[name.Value] = true
		if managedManagerProviderApp(app) {
			continue
		}
		issuer := entry(app, keyIssuer)
		if managedEnabled && (name.Value == agentplatform.ManagerProviderAppName || (issuer != nil && issuer.Value == agentplatform.ManagerGrantIssuer)) {
			return fmt.Errorf("%s app %q conflicts with the managed agent-manager GitHub App", providerAppsList, name.Value)
		}
		if renderedApps == nil {
			parent := rendered
			for _, key := range path[:len(path)-1] {
				var err error
				parent, err = nodeUnder(parent, key, yaml.MappingNode)
				if err != nil {
					return err
				}
			}
			var err error
			renderedApps, err = nodeUnder(parent, path[len(path)-1], yaml.SequenceNode)
			if err != nil {
				return err
			}
		}
		renderedApps.Content = append(renderedApps.Content, app)
		renderedApps.Style = 0
		*kept = append(*kept, Kept{List: providerAppsList, Entry: name.Value})
	}
	return nil
}

func managedManagerProviderApp(app *yaml.Node) bool {
	name, issuer := entry(app, keyName), entry(app, keyIssuer)
	return name != nil && issuer != nil && name.Value == agentplatform.ManagerProviderAppName && issuer.Value == agentplatform.ManagerGrantIssuer
}

// brokerIssuer is the subject-token issuer the render trusts for the
// manager's grant exchange, and the audiences the exchange needs it to accept.
type brokerIssuer struct {
	issuer    string
	audiences []string
}

// renderedBrokerIssuer is the render's broker issuer while it carries the
// manager's grant exchange; nil without it. Read before the record's issuers
// are merged, which replace the render's entry of the same issuer.
func renderedBrokerIssuer(values *yaml.Node) *brokerIssuer {
	if endpoint := at(values, "agent-manager", "githubTokenExchange", "endpoint"); endpoint == nil || endpoint.Value == "" {
		return nil
	}
	b := &brokerIssuer{}
	issuers := at(values, strings.Split(ListTrustedIssuers, ".")...)
	if issuers == nil || issuers.Kind != yaml.SequenceNode || len(issuers.Content) != 1 {
		return b
	}
	if issuer := entry(issuers.Content[0], keyIssuer); issuer != nil {
		b.issuer = issuer.Value
	}
	if audiences := entry(issuers.Content[0], keyAllowedAudiences); audiences != nil && audiences.Kind == yaml.SequenceNode {
		for _, a := range audiences.Content {
			b.audiences = append(b.audiences, a.Value)
		}
	}
	return b
}

// enforceBrokerIssuer holds the merged trusted issuers to the grant
// exchange's deployment gate: the render's issuer is the only one, its entry
// on record carries an explicit audience allowlist, and that allowlist gains
// every audience the render needs that the record lacks. The record's own
// audiences stay.
func enforceBrokerIssuer(values *yaml.Node, want *brokerIssuer) error {
	if want == nil {
		return nil
	}
	refusal := errors.New("agentManager.githubTokenExchange.enabled requires one canonical Dex subject-token issuer and an explicit input audience allowlist; the issuers preserved from this installation's record do not meet that condition")
	issuers := at(values, strings.Split(ListTrustedIssuers, ".")...)
	if want.issuer == "" || issuers == nil || issuers.Kind != yaml.SequenceNode || len(issuers.Content) != 1 {
		return refusal
	}
	issuer := entry(issuers.Content[0], keyIssuer)
	audiences := entry(issuers.Content[0], keyAllowedAudiences)
	if issuer == nil || issuer.Value != want.issuer || audiences == nil || audiences.Kind != yaml.SequenceNode || len(audiences.Content) == 0 {
		return refusal
	}
	for _, a := range want.audiences {
		if !contains(audiences, a) {
			audiences.Content = append(audiences.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: a})
		}
	}
	return nil
}
