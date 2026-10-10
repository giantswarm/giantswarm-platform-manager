package e2e

// platformctl installation dex-split on an installation without the agent
// platform, then the cluster-mcp-servers dry run over the record the split
// leaves: the hub's token-exchange client goes under the platform's secrets
// with the chain the definition renders, so the pair's Dex side is on record
// where the plan looks for it, kept, with nothing refused for the pair.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/internal/dexsplit"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
	"github.com/giantswarm/giantswarm-platform-manager/render"
	"github.com/giantswarm/giantswarm-platform-manager/render/clustermcpservers"
)

// enc is a value as SOPS leaves it on record: ciphertext, no fixture value.
func enc(s string) string { return "ENC[AES256_GCM,data:" + s + ",iv:fixture,tag:fixture,type:str]" }

// sopsBlockOf closes a fixture file the way SOPS does.
const sopsBlockOf = "sops:\n    age:\n        - recipient: age1fixture\n    lastmodified: \"2026-09-22T05:16:28Z\"\n    mac: " + "ENC[AES256_GCM,data:mac,iv:fixture,tag:fixture,type:str]" + "\n    unencrypted_suffix: _unencrypted\n    version: 3.11.0\n"

// splitVault is the split's vault over fixture plaintexts by reference: it
// reveals the configuration fields, compares values, writes a Secret the way
// SOPS leaves one on record (ciphertext under stringData, the sops block)
// and unsets keys of the encrypted patch on its YAML nodes. No value is a
// real one, and none reaches a file.
type splitVault struct {
	values map[string]string
}

func (*splitVault) Name() string { return "fixture" }

func (v *splitVault) Reveal(_ context.Context, file string, paths []string) (map[string]string, error) {
	out := map[string]string{}
	for _, p := range paths {
		val, ok := v.values[dexsplit.Ref{File: file, Path: p}.String()]
		if !ok {
			return nil, errors.New("no fixture value at " + file + "#" + p)
		}
		out[p] = val
	}
	return out, nil
}

func (v *splitVault) CopySecret(_ context.Context, src dexsplit.Ref, dst, name, namespace, key string) error {
	val, ok := v.values[src.String()]
	if !ok {
		return errors.New("no fixture value at " + src.String())
	}
	v.values[dexsplit.Ref{File: dst, Path: "stringData." + key}.String()] = val
	content := "apiVersion: v1\nkind: Secret\nmetadata:\n    name: " + name + "\n    namespace: " + namespace + "\ntype: Opaque\nstringData:\n    " + key + ": " + enc(val) + "\n" + sopsBlockOf
	return os.WriteFile(dst, []byte(content), 0o600)
}

func (v *splitVault) Unset(_ context.Context, file string, paths []string) error {
	data, err := os.ReadFile(filepath.Clean(file))
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	for _, p := range paths {
		unsetPath(doc.Content[0], strings.Split(p, "."))
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return err
	}
	return os.WriteFile(file, out, 0o600)
}

// unsetPath removes the key at path under the mapping n, if there.
func unsetPath(n *yaml.Node, path []string) {
	for i := 0; n != nil && n.Kind == yaml.MappingNode && i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value != path[0] {
			continue
		}
		if len(path) == 1 {
			n.Content = append(n.Content[:i], n.Content[i+2:]...)
			return
		}
		unsetPath(n.Content[i+1], path[1:])
		return
	}
}

func (v *splitVault) Equal(_ context.Context, a, b dexsplit.Ref) (bool, error) {
	va, ok := v.values[a.String()]
	if !ok {
		return false, errors.New("no fixture value at " + a.String())
	}
	vb, ok := v.values[b.String()]
	if !ok {
		return false, errors.New("no fixture value at " + b.String())
	}
	return va == vb, nil
}

// checkoutOf lays a repository of the fake GitHub out as a checkout.
func checkoutOf(t *testing.T, g *fakeGitHub, repo string) string {
	t.Helper()
	dir := t.TempDir()
	for p, content := range g.repos()[repo] {
		path := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// putOnRecordFrom puts the files a split wrote into a checkout on the fake
// GitHub, the way a merge would: each as the checkout has it.
func putOnRecordFrom(t *testing.T, g *fakeGitHub, repo, checkout string, files []string) {
	t.Helper()
	for _, f := range files {
		content, err := os.ReadFile(filepath.Clean(filepath.Join(checkout, f)))
		if errors.Is(err, os.ErrNotExist) {
			continue // a file the split moved away
		}
		if err != nil {
			t.Fatal(err)
		}
		g.addFile(repo, f, string(content))
	}
}

// alder runs mcp-kubernetes without the agent platform and the registry's
// hub brokers into it; its encrypted dex-app patch carries the hub's
// token-exchange client inline, as a hand registration left it, with the
// authenticator trusting it. The split moves the client's Secret under
// extras/agent-platform/secrets, which alder does not carry yet, and writes
// the chain there; the record then holds what cluster-mcp-servers renders
// for the pairing, and its dry run reads the pair's value as kept — the one
// value the hub's credentials file holds the other side of — with nothing
// refused for it and nothing to carry.
func TestClusterMCPServersReadsASplitTokenExchangeClientAsKept(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	portal := strings.Replace(portalConfig(hub, alder, birch), "        gs:\n", "        gs:\n          clusterTokenBroker:\n            tokenUrl: https://muster."+hub+".example.test/token\n", 1)
	st.ghs.addFile(hubMCs, installations.PortalConfigPath(hub), portal)
	st.ghs.addFile(acmeMCs, installations.ClusterMCPServersMarker(alder), "resources:\n  - https://github.com/giantswarm/management-cluster-bases/extras/mcp-kubernetes?ref=main\n")
	st.ghs.addFile(acmeMCs, installations.CollectionsKustomizationPath(alder), collectionsKustomization(clustermcpservers.DexAppRotation))
	st.ghs.addFile(acmeMCs, extrasKustomizationPath(alder), "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - ./mcp-kubernetes/\n")
	client := render.TokenExchangeClient(alder, hub, true)
	name := render.ExchangeSecretName(client)
	hubPath := render.PlatformSecretsPath(hub, render.ExchangeCredentialsSecret(alder)+".yaml")
	st.ghs.addFile(hubMCs, hubPath, "apiVersion: v1\nkind: Secret\nmetadata:\n    name: "+render.ExchangeCredentialsSecret(alder)+"\n    namespace: giantswarm\ntype: Opaque\nstringData:\n    client-id: "+client+"\n    client-secret: "+enc("pair")+"\n"+sopsBlockOf)
	encryptedPath := installations.DexSecretPatchPath(alder)
	st.ghs.addFile(acmeConfigs, encryptedPath, "oidc:\n    staticClients:\n        dexK8SAuthenticator:\n            clientSecret: "+enc("authenticator")+"\n            trustedPeers:\n                - "+enc("peer")+"\n    extraStaticClients:\n        - id: "+enc("id")+"\n          name: "+enc("name")+"\n          secret: "+enc("secret")+"\n"+sopsBlockOf)

	// The split over checkouts of the two repositories and the hub's, as
	// the fake GitHub has them, with its files put on record afterwards.
	configs, mcs, hubMC := checkoutOf(t, st.ghs, acmeConfigs), checkoutOf(t, st.ghs, acmeMCs), checkoutOf(t, st.ghs, hubMCs)
	encrypted := filepath.Join(configs, encryptedPath)
	v := &splitVault{values: map[string]string{
		dexsplit.Ref{File: encrypted, Path: "oidc.extraStaticClients.0.id"}.String():                          client,
		dexsplit.Ref{File: encrypted, Path: "oidc.extraStaticClients.0.name"}.String():                        hub + " token exchange",
		dexsplit.Ref{File: encrypted, Path: "oidc.extraStaticClients.0.secret"}.String():                      "pair-fixture-value",
		dexsplit.Ref{File: encrypted, Path: "oidc.staticClients.dexK8SAuthenticator.trustedPeers.0"}.String(): client,
		dexsplit.Ref{File: filepath.Join(hubMC, hubPath), Path: "stringData.client-secret"}.String():          "pair-fixture-value",
	}}
	r, err := dexsplit.Run(context.Background(), dexsplit.Options{Installation: alder, Configs: configs, ManagementClusters: mcs, Hub: hub, HubManagementClusters: hubMC, Vault: v, Write: true,
		ReadBase: func(context.Context, string, string, string) (string, error) {
			return "", errors.New("pinned: no base read")
		}})
	if err != nil {
		t.Fatal(err)
	}
	secret := "management-clusters/" + alder + "/extras/agent-platform/secrets/" + render.DexClientSecretFile(client)
	for _, f := range []string{secret, "management-clusters/" + alder + "/extras/agent-platform/secrets/kustomization.yaml", "management-clusters/" + alder + "/extras/agent-platform/kustomization.yaml", extrasKustomizationPath(alder)} {
		if !slices.Contains(r.ManagementClustersFiles, f) {
			t.Fatalf("the split did not write %s: %v", f, r.ManagementClustersFiles)
		}
	}
	if r.Pairing == nil || r.Pairing.State != dexsplit.StateEqual {
		t.Fatalf("pairing %+v", r.Pairing)
	}
	putOnRecordFrom(t, st.ghs, acmeMCs, mcs, r.ManagementClustersFiles)
	putOnRecordFrom(t, st.ghs, acmeConfigs, configs, r.ConfigsFiles)
	if patch, _ := st.ghs.file(acmeConfigs, encryptedPath); strings.Contains(patch, "extraStaticClients") || strings.Contains(patch, "trustedPeers") {
		t.Fatalf("the encrypted patch on record still carries a list:\n%s", patch)
	}

	out, text, isErr := dryRun(t, st.mcpClient(t, aliceToken), tools.ToolReconcileCapability, map[string]any{tools.ArgInstallation: alder, tools.ArgCapability: clustermcpservers.Capability})
	if isErr {
		t.Fatal(text)
	}
	if strings.Contains(text, "pair-fixture-value") || strings.Contains(text, "ENC[") {
		t.Fatalf("the dry run carries a value or ciphertext:\n%s", text)
	}
	p := findPlan(t, out, alder)
	if p.Refused != "" {
		t.Fatalf("refused: %s", p.Refused)
	}
	i := slices.IndexFunc(p.GeneratedSecrets, func(g plan.GeneratedSecret) bool { return g.Name == name })
	if i < 0 {
		t.Fatalf("no generated %s in %+v", name, p.GeneratedSecrets)
	}
	if g := p.GeneratedSecrets[i]; !g.Kept || g.Refusal != "" || g.Rotates || g.Supplied || len(g.Carries) != 0 || !slices.Equal(g.FrozenIn, []string{acmeMCs + ":" + secret}) {
		t.Fatalf("%s: %+v, want kept in the split's file alone", name, g)
	}
	if strings.Contains(p.CommitRefused, name) {
		t.Fatalf("commitRefused names the pair: %s", p.CommitRefused)
	}
	j := slices.IndexFunc(p.Files, func(f plan.File) bool { return f.Path == secret })
	if j < 0 || p.Files[j].Change != plan.ChangeUnchanged {
		t.Fatalf("the split's Secret on record: %+v", p.Files)
	}
}
