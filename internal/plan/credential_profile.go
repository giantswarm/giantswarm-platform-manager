package plan

import (
	"errors"
	"strings"

	"gopkg.in/yaml.v3"
)

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
