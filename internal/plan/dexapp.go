package plan

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Masterminds/semver/v3"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// DexAppReferencedSecrets is the first 3.x dex-app that renders every Dex client
// from a referenced Secret — next to a hand-made inline secret as well, which
// an installation enabled by hand still carries. Every client the definitions
// render is a referenced Secret (secretRef on an extra static client,
// clientSecretRef on a built-in one), so a commit onto an older dex-app would
// break Dex. The 2.x line carries it from render.DexAppLine2.
const DexAppReferencedSecrets = "3.2.2"

// DexAppRefusal says why a commit of p is refused for the dex-app on record:
// p's Dex patch declares a client with a referenced Secret and rec's dex-app
// is older than DexAppReferencedSecrets. Empty where nothing refuses — no
// referenced client, no version on record (the report carries why it could
// not be read), or a dex-app that takes them (render.DexAppTakes). The comparison runs either way;
// only the commit is held.
func (p Installation) DexAppRefusal(rec *installations.Record) string {
	if rec == nil || rec.DexAppVersion == "" || !p.referencesDexSecrets() {
		return ""
	}
	v, err := semver.NewVersion(rec.DexAppVersion)
	if err != nil || render.DexAppTakes(v, DexAppReferencedSecrets) {
		return ""
	}
	return fmt.Sprintf("dex-app %s on record (%s): the referenced Dex client secrets need %s; pin it in %s first",
		rec.DexAppVersion, rec.DexAppSource, render.DexAppNeeds(DexAppReferencedSecrets), installations.CollectionsKustomizationPath(p.Name))
}

// referencesDexSecrets says whether the rendered Dex patch declares a client
// whose secret is a referenced Secret.
func (p Installation) referencesDexSecrets() bool {
	return slices.ContainsFunc(p.DexClients, func(c DexClient) bool { return c.SecretRef != "" })
}

// DexSecretRefusal says why a commit of p is refused for the encrypted Dex
// values on record: p's Dex patch renders a list — oidc.extraStaticClients,
// the authenticator's trustedPeers, the customer's connectors — that rec's
// encrypted dex-app secret patch carries as well. app-operator merges the
// config ConfigMap with the config Secret, and helm-controller its
// valuesFrom, by replacing a list, never by appending, so the encrypted list
// would stand and the rendered clients, peers or connectors never reach Dex.
// The engine never edits an encrypted file: the entries are carried over by
// hand first — every client to its own Secret and a plaintext entry, every
// peer and every connector to the plaintext list — and the lists dropped
// from the encrypted values. Empty where nothing refuses — no record, no
// list in common, a plan without a Dex patch. The comparison runs either
// way; only the commit is held.
func (p Installation) DexSecretRefusal(rec *installations.Record) string {
	if rec == nil || len(rec.DexSecretLists) == 0 {
		return ""
	}
	var lists []installations.DexSecretList
	var clients, peers, connectors []string
	for _, l := range rec.DexSecretLists {
		switch l.Path {
		case installations.DexExtraStaticClients:
			if ids := p.extraDexClients(); len(ids) > 0 {
				lists, clients = append(lists, l), ids
			}
		case installations.DexTrustedPeers:
			if ids := p.renderedTrustedPeers(); len(ids) > 0 {
				lists, peers = append(lists, l), ids
			}
		case installations.DexCustomerConnectors:
			if len(p.DexConnectors) > 0 {
				lists, connectors = append(lists, l), p.DexConnectors
			}
		}
	}
	if len(lists) == 0 {
		return ""
	}
	return DexSecretHold(rec.DexSecretSource, lists, clients, peers, connectors)
}

// DexSecretHold is the sentence the commit is held with: the encrypted file,
// the lists it carries with their entries, and the rendered clients, trusted
// peers and connectors those lists would shadow.
func DexSecretHold(source string, lists []installations.DexSecretList, clients, peers, connectors []string) string {
	named := make([]string, len(lists))
	for i, l := range lists {
		named[i] = fmt.Sprintf("%s (%d entries)", l.Path, l.Entries)
	}
	var shadowed []string
	if len(clients) > 0 {
		shadowed = append(shadowed, "clients "+strings.Join(clients, ", "))
	}
	if len(peers) > 0 {
		shadowed = append(shadowed, "trusted peers "+strings.Join(peers, ", "))
	}
	if len(connectors) > 0 {
		shadowed = append(shadowed, "connectors "+strings.Join(connectors, ", "))
	}
	return fmt.Sprintf("the encrypted Dex values on record (%s) carry %s, which the values merge takes whole over the plaintext patch, so the rendered %s would never reach Dex; carry every entry over by hand first — each client to its own Secret and a plaintext entry, each peer and each connector to the plaintext list — then drop the lists from the encrypted values",
		source, strings.Join(named, " and "), strings.Join(shadowed, " and "))
}

// extraDexClients are the ids of the extra static clients the rendered Dex
// patch declares: every client that is not one of the chart's built-in ones.
func (p Installation) extraDexClients() []string {
	var ids []string
	for _, c := range p.DexClients {
		if c.Client == "" && c.ID != "" {
			ids = append(ids, c.ID)
		}
	}
	return ids
}

// renderedTrustedPeers are the authenticator's trusted peers the rendered
// Dex patch declares.
func (p Installation) renderedTrustedPeers() []string {
	for _, c := range p.DexClients {
		if c.Client == keyAuthenticator {
			return c.TrustedPeers
		}
	}
	return nil
}
