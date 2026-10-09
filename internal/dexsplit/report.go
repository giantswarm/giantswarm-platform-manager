package dexsplit

import (
	"fmt"
	"io"
	"strings"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
)

// Print writes the report for a person: per client where its secret comes
// from and goes, and its plaintext entry; the peers; what the encrypted
// patch drops and keeps; the token-exchange pairing; the files per
// repository in the order they merge. No value appears in it.
func (r Report) Print(w io.Writer) {
	mode := "dry run"
	if r.Write {
		mode = "written"
	}
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }
	p("dex-split %s (%s, vault %s)\n", r.Installation, mode, r.Vault)
	if r.DexApp != "" {
		p("dex-app %s (%s): reads referenced client Secrets\n", r.DexApp, r.DexAppSource)
	}
	if r.Done {
		p("\nNothing to do: %s carries no client list and no inline client secret.\n", installations.DexSecretPatchPath(r.Installation))
		if len(r.Keep) > 0 {
			p("It keeps: %s\n", strings.Join(r.Keep, ", "))
		}
		return
	}
	if !r.Revealed {
		p("Keys only: %s. Clients are named by key path; ids, names, redirect URIs and peers show with --vault sops.\n", r.Unrevealed)
	}
	p("\nClients (%d):\n", len(r.Clients))
	for _, c := range r.Clients {
		kind := "extra static client"
		if c.BuiltIn != "" {
			kind = "built-in client"
		}
		p("  %s (%s, %s)\n", c.ID(), kind, c.Source)
		if m := c.Secret; m != nil {
			p("    secret from  %s\n", m.From)
			p("    to           %s (Secret giantswarm/%s, key secret)", m.File, m.Secret)
			if m.State != "" {
				p(" [%s]", m.State)
			}
			p("\n")
			if m.Kustomization != "" {
				p("    listed in    %s\n", m.Kustomization)
			}
		} else {
			p("    no inline secret\n")
		}
		p("    plaintext    %s\n", c.plaintext())
	}
	if r.PeerCount > 0 {
		peers := fmt.Sprintf("%d, unrevealed", r.PeerCount)
		if len(r.Peers) > 0 {
			peers = strings.Join(r.Peers, ", ")
		}
		p("\nTrusted peers to %s (plaintext): %s\n", pathPeers, peers)
	}
	p("\nThe encrypted patch drops: %s\n", strings.Join(r.Drop, ", "))
	p("It keeps: %s\n", strings.Join(r.Keep, ", "))
	switch pr := r.Pairing; {
	case pr != nil:
		p("\nToken exchange: %s pairs with the hub's %s: %s", pr.Client, pr.HubRef, pr.State)
		if pr.Reason != "" {
			p(" (%s)", pr.Reason)
		}
		p("\n")
	case !r.Revealed:
		p("\nToken exchange: a %s<installation> client pairs with the hub's <installation>-token-exchange-credentials (ids unrevealed)\n", tokenExchangePrefix)
	}
	verb := "Writes"
	if r.Write {
		verb = "Wrote"
	}
	if len(r.ManagementClustersFiles) > 0 {
		p("\n%s, management-clusters (merge first, then wait for flux-extras to apply the Secrets):\n", verb)
		for _, f := range r.ManagementClustersFiles {
			p("  %s\n", f)
		}
	}
	if len(r.ConfigsFiles) > 0 {
		p("%s, configs (merge second; Dex rolls on the values):\n", verb)
		for _, f := range r.ConfigsFiles {
			p("  %s\n", f)
		}
	}
}

// plaintext is the client's plaintext entry in one line.
func (c Client) plaintext() string {
	switch {
	case c.BuiltIn != "" && c.Secret != nil:
		return fmt.Sprintf("%s.%s.clientSecretRef {name: %s, key: secret}", pathStatic, c.BuiltIn, c.Secret.Secret)
	case c.Entry == nil:
		return pathExtraClients + " entry: the client's fields, a secretRef to its Secret"
	}
	e := c.Entry
	parts := []string{"id: " + e.ID}
	if e.Name != "" {
		parts = append(parts, "name: "+e.Name)
	}
	if e.Public != nil {
		parts = append(parts, fmt.Sprintf("public: %t", *e.Public))
	}
	if len(e.RedirectURIs) > 0 {
		parts = append(parts, "redirectURIs: ["+strings.Join(e.RedirectURIs, ", ")+"]")
	}
	if len(e.TrustedPeers) > 0 {
		parts = append(parts, "trustedPeers: ["+strings.Join(e.TrustedPeers, ", ")+"]")
	}
	if e.LogoURL != "" {
		parts = append(parts, "logoURL: "+e.LogoURL)
	}
	if e.SecretRef != "" {
		parts = append(parts, "secretRef: {name: "+e.SecretRef+", key: secret}")
	}
	return pathExtraClients + " {" + strings.Join(parts, ", ") + "}"
}
