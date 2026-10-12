package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"github.com/giantswarm/giantswarm-platform-manager/internal/dexsplit"
	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
)

// dexSplitHelp is the command's long help.
const dexSplitHelp = `Split an installation's Dex clients out of its encrypted dex-app values into the shape the
definitions render, from checkouts of its two repositories. The encrypted
installations/<mc>/apps/dex-app/secret-values.yaml.patch of the configs repository carries the
hand-registered clients with their inline secrets and the authenticator's trustedPeers; a list there
replaces the plaintext list wholesale, so no client the definitions render reaches Dex and the
manager refuses the commit. The split writes:

  management-clusters (merge first): one SOPS Secret per inline client secret, namespace giantswarm,
    key secret, at the path the definitions render (extras/agent-platform/secrets/ for muster, kagent
    and muster-*, extras/mcp-<x>/ for mcp<X>, the portal's dex-client-backstage-secret.enc.yaml,
    extras/dex/ for any other client), listed in its kustomization. A hub's token-exchange client
    (muster-token-exchange-<mc>[-<hub>]) goes under extras/agent-platform/secrets/ whether or not the
    platform's directory is on the installation, where cluster-mcp-servers renders the pair's Dex
    side, with the kustomization chain to it; one an earlier split put under extras/dex/ moves on
    there unchanged
  configs (merge after flux-extras applied the Secrets): the plaintext configmap-values.yaml.patch
    lists every client with clientSecretRef or secretRef and the trustedPeers; the encrypted patch
    drops the lists and the moved secrets and keeps the rest (the authenticator's own secret, the
    login connectors)

Ids, names, redirect URIs, public flags, peers and values never change. Without --write the command
is the dry run: per client the source key path, the destination file and Secret, the plaintext
entry, what the encrypted patch keeps, the token-exchange pairing and the files per repository. A
split installation answers that nothing is to do. dex-app older than 3.2.2 is refused with the pin
to add.

--vault sops (a person): the local sops decrypts with the person's age identity and encrypts under
the nearest .sops.yaml. --vault beekeeper (an agent, beekeeper 0.114.0 or newer): beekeeper secret copy moves each value,
beekeeper secret compare checks equality and beekeeper secret unset drops the moved keys, no secret
reaching the caller; beekeeper secret reveal answers the configuration fields once its classifier
finds no secret in them. --hub and --hub-management-clusters compare the
token-exchange client's value with the hub's <mc>-token-exchange-credentials, which must stay one
value; --write refuses when they differ.`

func newDexSplitCmd() *cobra.Command {
	var o dexsplit.Options
	var vault, output string
	cmd := leaf("dex-split <installation>", "Split an installation's inline Dex clients into referenced Secrets", func(pos []string, stdout, stderr io.Writer) int {
		if len(pos) != 1 {
			return usageError(stderr, "installation dex-split <installation> --configs <checkout> --management-clusters <checkout>")
		}
		if o.Configs == "" || o.ManagementClusters == "" {
			return usageError(stderr, "--configs and --management-clusters name the installation's two checkouts")
		}
		if (o.Hub == "") != (o.HubManagementClusters == "") {
			return usageError(stderr, "--hub and --hub-management-clusters go together")
		}
		if output != outputText && output != outputJSON {
			return usageError(stderr, fmt.Sprintf("-o %q: %s or %s", output, outputText, outputJSON))
		}
		v, err := dexsplit.NewVault(vault, dexsplit.Exec)
		if err != nil {
			return usageError(stderr, err.Error())
		}
		o.Installation, o.Vault, o.ReadBase = pos[0], v, readPublic
		r, err := dexsplit.Run(context.Background(), o)
		if output == outputJSON {
			enc := json.NewEncoder(stdout)
			enc.SetIndent("", "  ")
			if encErr := enc.Encode(r); encErr != nil {
				return fail(stderr, encErr)
			}
		} else if err == nil || (errors.Is(err, dexsplit.ErrRefused) && r.Clients != nil) {
			r.Print(stdout)
		}
		if err != nil {
			return fail(stderr, err)
		}
		return exitOK
	})
	cmd.Long = dexSplitHelp
	f := cmd.Flags()
	f.StringVar(&o.Configs, "configs", "", "checkout of the installation's configs repository")
	f.StringVar(&o.ManagementClusters, "management-clusters", "", "checkout of the installation's management-clusters repository")
	f.StringVar(&o.Hub, "hub", "", "the hub installation whose muster exchanges tokens with this one")
	f.StringVar(&o.HubManagementClusters, "hub-management-clusters", "", "checkout of the hub's management-clusters repository")
	f.StringVar(&vault, "vault", dexsplit.VaultSOPS, "who moves the values: sops (a person) or beekeeper (an agent)")
	f.BoolVar(&o.Write, "write", false, "write the split into the checkouts; without it the dry run")
	f.StringVarP(&output, "output", "o", outputText, "text or json")
	for _, dir := range []string{"configs", "management-clusters", "hub-management-clusters"} {
		_ = cmd.MarkFlagDirname(dir)
	}
	_ = cmd.RegisterFlagCompletionFunc("vault", cobra.FixedCompletions([]string{dexsplit.VaultSOPS, dexsplit.VaultBeekeeper}, cobra.ShellCompDirectiveNoFileComp))
	return cmd
}

// readPublic reads a file of a public GitHub repository at a ref, the fleet
// base's App dex-app for one; no sign-in.
func readPublic(ctx context.Context, repository, path, ref string) (string, error) {
	if ref == "" {
		ref = "HEAD"
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://raw.githubusercontent.com/"+repository+"/"+ref+"/"+path, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return "", fmt.Errorf("%s:%s at %s: %w", repository, path, ref, gh.ErrNotFound)
	case resp.StatusCode != http.StatusOK:
		return "", errors.New(repository + ":" + path + " at " + ref + ": " + resp.Status)
	}
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}
