package plan

import (
	"errors"
	"strings"

	"gopkg.in/yaml.v3"
)

// brokerIssuer is the subject-token issuer the render trusts for the
// manager's grant exchange and the audiences it accepts from it.
type brokerIssuer struct {
	issuer    string
	audiences []string
}

// renderedBrokerIssuer is the render's broker issuer; nil when the render
// carries no manager grant exchange. Read before the record's issuers are
// merged, which replace the render's entry of the same issuer.
func renderedBrokerIssuer(values *yaml.Node) *brokerIssuer {
	if endpoint := at(values, "agent-manager", "githubTokenExchange", "endpoint"); endpoint == nil || endpoint.Value == "" {
		return nil
	}
	b := &brokerIssuer{}
	issuers := at(values, strings.Split(ListTrustedIssuers, ".")...)
	if issuers != nil && issuers.Kind == yaml.SequenceNode && len(issuers.Content) == 1 {
		if issuer := entry(issuers.Content[0], keyIssuer); issuer != nil {
			b.issuer = issuer.Value
		}
		if audiences := entry(issuers.Content[0], keyAllowedAudiences); audiences != nil && audiences.Kind == yaml.SequenceNode {
			for _, a := range audiences.Content {
				b.audiences = append(b.audiences, a.Value)
			}
		}
	}
	return b
}

// enforceBrokerIssuer holds the merged trusted issuers to the render's broker
// issuer: it is the only one, its entry on record carries an explicit
// audience allowlist, and that allowlist gains every audience the render
// needs that the record lacks. The record's own audiences stay, also once the
// exchange is off: the entry is the installation's.
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
			audiences.Content = append(audiences.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: tagStr, Value: a})
		}
	}
	return nil
}
