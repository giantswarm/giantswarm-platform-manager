package plan

import (
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	providerAppsList         = "muster.muster.providerApps"
	managerProviderAppName   = "agent-manager"
	managerProviderAppIssuer = "https://github.com/apps/giantswarm-agent-manager"
)

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
		if managedEnabled && (name.Value == managerProviderAppName || (issuer != nil && issuer.Value == managerProviderAppIssuer)) {
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
	return name != nil && issuer != nil && name.Value == managerProviderAppName && issuer.Value == managerProviderAppIssuer
}

func credentialBrokerIssuer(values *yaml.Node) string {
	endpoint := at(values, "agent-manager", "githubTokenExchange", "endpoint")
	if endpoint == nil || endpoint.Value == "" {
		return ""
	}
	issuers := at(values, strings.Split(ListTrustedIssuers, ".")...)
	if issuers == nil || len(issuers.Content) != 1 {
		return ""
	}
	issuer := entry(issuers.Content[0], keyIssuer)
	if issuer == nil {
		return ""
	}
	return issuer.Value
}

func validateCredentialBrokerIssuer(values *yaml.Node, expected string) error {
	endpoint := at(values, "agent-manager", "githubTokenExchange", "endpoint")
	if endpoint == nil || endpoint.Value == "" {
		return nil
	}
	issuers := at(values, strings.Split(ListTrustedIssuers, ".")...)
	if expected != "" && issuers != nil && issuers.Kind == yaml.SequenceNode && len(issuers.Content) == 1 {
		issuer := entry(issuers.Content[0], keyIssuer)
		audiences := entry(issuers.Content[0], "allowedAudiences")
		if issuer != nil && issuer.Value == expected && audiences != nil && audiences.Kind == yaml.SequenceNode && len(audiences.Content) > 0 {
			return nil
		}
	}
	return errors.New("agentManager.githubTokenExchange.enabled requires one canonical Dex subject-token issuer and an explicit input audience allowlist; the issuers preserved from this installation's record do not meet that condition")
}
