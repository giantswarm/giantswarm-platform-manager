package plan

import (
	"errors"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/render/agentplatform"
)

// brokerIssuer is the subject-token issuer the render trusts, the audiences
// it accepts from it, and whether the render carries the manager's grant
// exchange.
type brokerIssuer struct {
	issuer    string
	audiences []string
	exchange  bool
}

// renderedBrokerIssuer is the render's broker issuer; nil when the render
// carries neither the manager's grant exchange nor a single trusted issuer.
// Read before the record's issuers are merged, which replace the render's
// entry of the same issuer.
func renderedBrokerIssuer(values *yaml.Node) *brokerIssuer {
	b := &brokerIssuer{}
	if endpoint := at(values, "agent-manager", "githubTokenExchange", "endpoint"); endpoint != nil && endpoint.Value != "" {
		b.exchange = true
	}
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
	if !b.exchange && b.issuer == "" {
		return nil
	}
	return b
}

// enforceBrokerIssuer holds the merged trusted issuers to the render's broker
// issuer. With the grant exchange: the render's issuer is the only one, its
// entry on record carries an explicit audience allowlist, and that allowlist
// gains every audience the render needs that the record lacks; the record's
// own audiences stay. Without it: the record's entry for the render's issuer
// loses the exchange's subject audience the render no longer accepts.
func enforceBrokerIssuer(values *yaml.Node, want *brokerIssuer) error {
	if want == nil {
		return nil
	}
	issuers := at(values, strings.Split(ListTrustedIssuers, ".")...)
	if !want.exchange {
		withdrawExchangeAudience(issuers, want)
		return nil
	}
	refusal := errors.New("agentManager.githubTokenExchange.enabled requires one canonical Dex subject-token issuer and an explicit input audience allowlist; the issuers preserved from this installation's record do not meet that condition")
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

// withdrawExchangeAudience removes the grant exchange's subject audience from
// the record's entry for the render's issuer when the render does not accept
// it. An allowlist left empty takes the render's audiences: muster accepts
// any audience from an issuer with an empty allowlist.
func withdrawExchangeAudience(issuers *yaml.Node, want *brokerIssuer) {
	if issuers == nil || issuers.Kind != yaml.SequenceNode || slices.Contains(want.audiences, agentplatform.ManagerExchangeSubjectAudience) {
		return
	}
	for _, e := range issuers.Content {
		issuer := entry(e, keyIssuer)
		audiences := entry(e, keyAllowedAudiences)
		if issuer == nil || issuer.Value != want.issuer || audiences == nil || audiences.Kind != yaml.SequenceNode {
			continue
		}
		audiences.Content = slices.DeleteFunc(audiences.Content, func(a *yaml.Node) bool {
			return a.Value == agentplatform.ManagerExchangeSubjectAudience
		})
		if len(audiences.Content) == 0 {
			for _, a := range want.audiences {
				audiences.Content = append(audiences.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: a})
			}
		}
	}
}
