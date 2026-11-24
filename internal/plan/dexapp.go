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

// DexAppConnectorMaps is the first dex-app that reads oidc.<owner>.connectors
// and oidc.extraStaticClients as maps keyed by id, so the values sources of
// one installation merge their entries instead of one list replacing another.
// The 2.x line does not carry it. The definitions render them so, and a
// connector's clientSecretRef.
const DexAppConnectorMaps = "3.5.0"

// DexAppRefusal says why a commit of p is refused for the dex-app on record:
// p's Dex patch declares a client with a referenced Secret and rec's dex-app
// is older than DexAppReferencedSecrets, or an extra static client or a
// hub's connector and rec's dex-app is older than DexAppConnectorMaps. Empty where nothing refuses — no
// such client, no source on record, or a dex-app that takes them
// (render.DexAppTakes). An unreadable source or unresolved selector holds a
// version-dependent commit. The comparison still runs.
func (p Installation) DexAppRefusal(rec *installations.Record) string {
	if rec == nil {
		return ""
	}
	if p.referencesDexSecrets() && rec.DexAppVersionError != "" {
		return "the referenced Dex client secrets need a proven chart version: " + rec.DexAppVersionError
	}
	if p.rendersDexMaps() && rec.DexAppVersionError != "" {
		return "the Dex clients and connectors keyed by id need a proven chart version: " + rec.DexAppVersionError
	}
	if rec.DexAppVersion == "" {
		return ""
	}
	v, err := semver.NewVersion(rec.DexAppVersion)
	if err != nil {
		return ""
	}
	if p.referencesDexSecrets() && !render.DexAppTakes(v, DexAppReferencedSecrets) {
		return fmt.Sprintf("dex-app %s on record (%s): the referenced Dex client secrets need %s; pin it in %s first",
			rec.DexAppVersion, rec.DexAppSource, render.DexAppNeeds(DexAppReferencedSecrets), installations.CollectionsKustomizationPath(p.Name))
	}
	if p.rendersDexMaps() && !render.DexAppTakes(v, DexAppConnectorMaps) {
		return fmt.Sprintf("dex-app %s on record (%s): the Dex clients and connectors keyed by id need %s; pin it in %s first",
			rec.DexAppVersion, rec.DexAppSource, render.DexAppNeeds(DexAppConnectorMaps), installations.CollectionsKustomizationPath(p.Name))
	}
	return ""
}

// rendersDexMaps says whether p's Dex patch declares an extra static client
// or a hub's connector, which the definitions render as maps keyed by id.
func (p Installation) rendersDexMaps() bool {
	return len(p.extraDexClients()) > 0 || len(p.DexConnectors) > 0 || len(p.DexStaffConnectors) > 0
}

// referencesDexSecrets says whether the rendered Dex patch declares a client
// whose secret is a referenced Secret.
func (p Installation) referencesDexSecrets() bool {
	return slices.ContainsFunc(p.DexClients, func(c DexClient) bool { return c.SecretRef != "" })
}

// DexSecretRefusal says why a commit of p is refused for the encrypted Dex
// values on record: p's Dex patch renders a key — oidc.extraStaticClients,
// the authenticator's trustedPeers, the connectors of either owner, the
// tunnel bundle's dex.volumes and dex.volumeMounts — that
// rec's encrypted dex-app secret patch carries as a list. app-operator merges the
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
	var clients, peers, connectors, volumes []string
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
				lists, connectors = append(lists, l), append(connectors, p.DexConnectors...)
			}
		case installations.DexStaffConnectors:
			if len(p.DexStaffConnectors) > 0 {
				lists, connectors = append(lists, l), append(connectors, p.DexStaffConnectors...)
			}
		case installations.DexVolumes, installations.DexVolumeMounts:
			if len(p.DexVolumes) > 0 {
				lists, volumes = append(lists, l), p.DexVolumes
			}
		}
	}
	if len(lists) == 0 {
		return ""
	}
	return DexSecretHold(rec.DexSecretSource, lists, clients, peers, connectors, volumes)
}

// DexSecretHold is the sentence the commit is held with: the encrypted file,
// the lists it carries with their entries, and the rendered clients, trusted
// peers, connectors and Dex volumes those lists would shadow.
func DexSecretHold(source string, lists []installations.DexSecretList, clients, peers, connectors, volumes []string) string {
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
	if len(volumes) > 0 {
		shadowed = append(shadowed, "Dex volumes "+strings.Join(volumes, ", "))
	}
	return fmt.Sprintf("the encrypted Dex values on record (%s) carry %s, which the values merge takes whole over the plaintext patch, so the rendered %s would never reach Dex; carry every entry over by hand first — each client to its own Secret and a plaintext entry, each peer, each connector, each volume and each mount to the plaintext list — then drop the lists from the encrypted values",
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
