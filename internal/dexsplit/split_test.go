package dexsplit

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The invented installation puffin, its hub hubble. Every "value" is made
// up; the encrypted fixture carries ENC[…] where sops would.
const (
	inst = "puffin"
	hub  = "hubble"
)

const encryptedPatch = `oidc:
    customer:
        connectors:
            - connectorConfig: ENC[a]
              id: ENC[b]
    staticClients:
        mcpKubernetes:
            clientSecret: ENC[c]
        dexK8SAuthenticator:
            clientSecret: ENC[d]
            trustedPeers:
                - ENC[e]
                - ENC[f]
        muster:
            clientSecret: ENC[g]
    extraStaticClients:
        - id: ENC[h]
          name: ENC[i]
          secret: ENC[j]
          redirectURIs:
            - ENC[k]
        - id: ENC[l]
          name: ENC[m]
          secret: ENC[n]
        - id: ENC[o]
          name: ENC[p]
          public: ENC[q]
          secret: ENC[r]
          redirectURIs:
            - ENC[s]
            - ENC[t]
        - id: ENC[u]
          name: ENC[v]
          secret: ENC[w]
sops:
    mac: ENC[x]
    unencrypted_suffix: _unencrypted
`

// The same document as the person's sops decrypts it.
const decryptedPatch = `oidc:
  customer:
    connectors:
      - connectorConfig: {clientSecret: connector-value}
        id: github
  staticClients:
    mcpKubernetes:
      clientSecret: mcp-kubernetes-value
    dexK8SAuthenticator:
      clientSecret: authenticator-value
      trustedPeers: [kagent, muster-token-exchange-puffin]
    muster:
      clientSecret: muster-value
  extraStaticClients:
    - id: kagent
      name: kagent-ui
      secret: kagent-value
      redirectURIs: [https://kagent.puffin.example/oauth2/callback]
    - id: muster-token-exchange-puffin
      name: hub token exchange
      secret: exchange-value
    - id: backstage
      name: Dev Portal
      public: true
      secret: portal-value
      redirectURIs: [https://portal.example/a, https://portal.example/b]
    - id: Grafana
      name: grafana
      secret: grafana-value
`

// fakeVault keeps each SOPS file's decrypted document by path and writes
// ENC[…] where sops would, so the files on disk carry no fixture value.
type fakeVault struct {
	plain  map[string]*yaml.Node
	reveal bool
	copies []string
	unsets []string
}

func (*fakeVault) Name() string { return "fake" }

func (v *fakeVault) Reveal(_ context.Context, file string, paths []string) (map[string]string, error) {
	if !v.reveal {
		return nil, ErrUnsupported
	}
	out := map[string]string{}
	for _, p := range paths {
		n := lookup(v.plain[file], p)
		if n == nil {
			return nil, errors.New("no " + p)
		}
		out[p] = n.Value
	}
	return out, nil
}

func (v *fakeVault) value(r Ref) string {
	if n := lookup(v.plain[r.File], r.Path); n != nil {
		return n.Value
	}
	return ""
}

func (v *fakeVault) CopySecret(_ context.Context, src Ref, dst, name, namespace, key string) error {
	if _, err := os.Stat(dst); err == nil {
		return errors.New(dst + " exists")
	}
	m, err := secretManifest(name, namespace, key, v.value(src))
	if err != nil {
		return err
	}
	var doc yaml.Node
	_ = yaml.Unmarshal(m, &doc)
	v.plain[dst] = doc.Content[0]
	v.copies = append(v.copies, src.Path+">"+name)
	onDisk := strings.Replace(string(m), v.value(src), "ENC[fake]", 1)
	return os.WriteFile(dst, []byte(onDisk), 0o600)
}

func (v *fakeVault) Unset(_ context.Context, file string, paths []string) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	for _, p := range paths {
		remove(doc.Content[0], p)
		remove(v.plain[file], p)
		v.unsets = append(v.unsets, p)
	}
	out, err := encode(&doc)
	if err != nil {
		return err
	}
	return os.WriteFile(file, out, 0o600)
}

func (v *fakeVault) Equal(_ context.Context, a, b Ref) (bool, error) {
	return v.value(a) != "" && v.value(a) == v.value(b), nil
}

func remove(n *yaml.Node, path string) {
	i := strings.LastIndex(path, ".")
	parent := n
	if i >= 0 {
		parent = lookup(n, path[:i])
	}
	key := path[i+1:]
	for j := 0; parent != nil && j+1 < len(parent.Content); j += 2 {
		if parent.Content[j].Value == key {
			parent.Content = append(parent.Content[:j], parent.Content[j+2:]...)
			return
		}
	}
}

// checkouts lays out the two repositories of puffin and the hub's.
func checkouts(t *testing.T, dexApp string) (Options, *fakeVault) {
	t.Helper()
	base := t.TempDir()
	configs, mc, hubMC := filepath.Join(base, "configs"), filepath.Join(base, "mc"), filepath.Join(base, "hub-mc")
	files := map[string]string{
		filepath.Join(configs, "installations/puffin/apps/dex-app/secret-values.yaml.patch"): encryptedPatch,
		filepath.Join(mc, "management-clusters/puffin/collections/kustomization.yaml"): `resources:
  - https://github.com/example/bases//collections/stable?ref=main
patches:
  - target: {kind: App, name: dex-app}
    patch: |
      - op: replace
        path: /spec/version
        value: ` + dexApp + "\n",
		filepath.Join(mc, "management-clusters/puffin/extras/kustomization.yaml"):                                               "resources:\n  - ./agent-platform/\n  - ./mcp-kubernetes/\n",
		filepath.Join(mc, "management-clusters/puffin/extras/agent-platform/secrets/kustomization.yaml"):                        "resources:\n  - muster-oauth-credentials.yaml\n",
		filepath.Join(mc, "management-clusters/puffin/extras/mcp-kubernetes/kustomization.yaml"):                                "resources:\n  - oauth-credentials.enc.yaml\n",
		filepath.Join(mc, "management-clusters/puffin/extras/backstage/backstage/kustomization.yaml"):                           "resources:\n  - app-config.yaml\n",
		filepath.Join(hubMC, "management-clusters/hubble/extras/agent-platform/secrets/puffin-token-exchange-credentials.yaml"): "stringData:\n  client-secret: ENC[y]\n",
	}
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	v := &fakeVault{plain: map[string]*yaml.Node{}, reveal: true}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(decryptedPatch), &doc); err != nil {
		t.Fatal(err)
	}
	v.plain[filepath.Join(configs, "installations/puffin/apps/dex-app/secret-values.yaml.patch")] = doc.Content[0]
	var hubDoc yaml.Node
	_ = yaml.Unmarshal([]byte("stringData:\n  client-secret: exchange-value\n"), &hubDoc)
	v.plain[filepath.Join(hubMC, "management-clusters/hubble/extras/agent-platform/secrets/puffin-token-exchange-credentials.yaml")] = hubDoc.Content[0]
	return Options{
		Installation: inst, Configs: configs, ManagementClusters: mc,
		Hub: hub, HubManagementClusters: hubMC, Vault: v,
		ReadBase: func(context.Context, string, string, string) (string, error) {
			t.Fatal("pinned: no base read")
			return "", nil
		},
	}, v
}

func TestReadShape(t *testing.T) {
	s, err := ReadShape([]byte(encryptedPatch))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(s.BuiltIns, ","), "mcpKubernetes,muster"; got != want {
		t.Errorf("built-ins %s, want %s", got, want)
	}
	if len(s.Extras) != 4 || s.Peers != 2 || s.Extras[2].RedirectURIs != 2 {
		t.Errorf("extras %+v, peers %d", s.Extras, s.Peers)
	}
	wantDrop := []string{"oidc.staticClients.mcpKubernetes", "oidc.staticClients.dexK8SAuthenticator.trustedPeers", "oidc.staticClients.muster", "oidc.extraStaticClients"}
	if strings.Join(s.Drop, ",") != strings.Join(wantDrop, ",") {
		t.Errorf("drop %v, want %v", s.Drop, wantDrop)
	}
	wantKeep := []string{"oidc.customer.connectors (1 entries)", "oidc.staticClients.dexK8SAuthenticator.clientSecret"}
	if strings.Join(s.Keep, ",") != strings.Join(wantKeep, ",") {
		t.Errorf("keep %v, want %v", s.Keep, wantKeep)
	}
}

func TestReadShapeRefusesAnUnknownField(t *testing.T) {
	_, err := ReadShape([]byte("oidc:\n  extraStaticClients:\n    - id: ENC[a]\n      secretEnv: ENC[b]\n"))
	if err == nil || !strings.Contains(err.Error(), "oidc.extraStaticClients.0.secretEnv") {
		t.Fatalf("err %v, want the unknown field named", err)
	}
}

func TestReadShapeSplit(t *testing.T) {
	s, err := ReadShape([]byte("oidc:\n  staticClients:\n    dexK8SAuthenticator:\n      clientSecret: ENC[a]\nsops:\n  mac: ENC[b]\n"))
	if err != nil || !s.Split() || strings.Join(s.Keep, ",") != "oidc.staticClients.dexK8SAuthenticator.clientSecret" {
		t.Fatalf("shape %+v, err %v", s, err)
	}
}

func TestDryRun(t *testing.T) {
	o, v := checkouts(t, "3.2.5")
	r, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.copies) != 0 || len(v.unsets) != 0 {
		t.Fatalf("the dry run wrote: %v %v", v.copies, v.unsets)
	}
	want := map[string]string{
		"mcpKubernetes":                "management-clusters/puffin/extras/mcp-kubernetes/dex-client-mcp-kubernetes-secret.yaml",
		"muster":                       "management-clusters/puffin/extras/agent-platform/secrets/dex-client-muster-secret.yaml",
		"kagent":                       "management-clusters/puffin/extras/agent-platform/secrets/dex-client-kagent-secret.yaml",
		"muster-token-exchange-puffin": "management-clusters/puffin/extras/agent-platform/secrets/dex-client-muster-token-exchange-puffin-secret.yaml",
		"backstage":                    "management-clusters/puffin/extras/backstage/backstage/dex-client-backstage-secret.enc.yaml",
		"Grafana":                      "management-clusters/puffin/extras/dex/dex-client-grafana-secret.yaml",
	}
	for _, c := range r.Clients {
		if c.Secret == nil || c.Secret.File != want[c.ID()] {
			t.Errorf("%s: %+v, want %s", c.ID(), c.Secret, want[c.ID()])
		}
	}
	if r.Pairing == nil || r.Pairing.State != StateEqual {
		t.Errorf("pairing %+v, want equal", r.Pairing)
	}
	var out bytes.Buffer
	r.Print(&out)
	for _, value := range []string{"connector-value", "mcp-kubernetes-value", "authenticator-value", "muster-value", "kagent-value", "exchange-value", "portal-value", "grafana-value", "ENC["} {
		if strings.Contains(out.String(), value) {
			t.Errorf("the report carries %q:\n%s", value, out.String())
		}
	}
	for _, s := range []string{"oidc.extraStaticClients.3.secret", "dex-client-grafana", "extras/kustomization.yaml", "kagent, muster-token-exchange-puffin"} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("the report lacks %q:\n%s", s, out.String())
		}
	}
}

func TestDryRunKeysOnly(t *testing.T) {
	o, v := checkouts(t, "3.2.5")
	v.reveal = false
	r, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if r.Revealed || len(r.Clients) != 6 || r.Clients[2].ID() != "oidc.extraStaticClients.0" {
		t.Fatalf("report %+v", r)
	}
	o.Write = true
	if _, err := Run(context.Background(), o); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("a keys-only write: %v, want unsupported", err)
	}
}

func TestWriteThenNoOp(t *testing.T) {
	o, v := checkouts(t, "3.2.5")
	o.Write = true
	r, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.copies) != 6 {
		t.Errorf("copies %v, want 6", v.copies)
	}
	plain, err := os.ReadFile(filepath.Join(o.Configs, ConfigMapPatchPath(inst)))
	if err != nil {
		t.Fatal(err)
	}
	want := `oidc:
  staticClients:
    mcpKubernetes:
      clientSecretRef:
        name: dex-client-mcp-kubernetes
        key: secret
    muster:
      clientSecretRef:
        name: dex-client-muster
        key: secret
    dexK8SAuthenticator:
      trustedPeers:
        - kagent
        - muster-token-exchange-puffin
  extraStaticClients:
    - id: kagent
      name: kagent-ui
      redirectURIs:
        - https://kagent.puffin.example/oauth2/callback
      secretRef:
        name: dex-client-kagent
        key: secret
    - id: muster-token-exchange-puffin
      name: hub token exchange
      secretRef:
        name: dex-client-muster-token-exchange-puffin
        key: secret
    - id: backstage
      name: Dev Portal
      public: true
      redirectURIs:
        - https://portal.example/a
        - https://portal.example/b
      secretRef:
        name: dex-client-backstage
        key: secret
    - id: Grafana
      name: grafana
      secretRef:
        name: dex-client-grafana
        key: secret
`
	if string(plain) != want {
		t.Errorf("plaintext patch:\n%s\nwant:\n%s", plain, want)
	}
	encrypted, _ := os.ReadFile(filepath.Join(o.Configs, "installations/puffin/apps/dex-app/secret-values.yaml.patch"))
	for _, gone := range []string{"extraStaticClients", "trustedPeers", "muster:", "mcpKubernetes"} {
		if strings.Contains(string(encrypted), gone) {
			t.Errorf("the encrypted patch keeps %s:\n%s", gone, encrypted)
		}
	}
	for _, kept := range []string{"connectors", "dexK8SAuthenticator", "sops:"} {
		if !strings.Contains(string(encrypted), kept) {
			t.Errorf("the encrypted patch lost %s:\n%s", kept, encrypted)
		}
	}
	extras, _ := os.ReadFile(filepath.Join(o.ManagementClusters, "management-clusters/puffin/extras/kustomization.yaml"))
	dex, _ := os.ReadFile(filepath.Join(o.ManagementClusters, "management-clusters/puffin/extras/dex/kustomization.yaml"))
	secrets, _ := os.ReadFile(filepath.Join(o.ManagementClusters, "management-clusters/puffin/extras/agent-platform/secrets/kustomization.yaml"))
	if !strings.Contains(string(extras), "./dex/") || !strings.Contains(string(dex), "dex-client-grafana-secret.yaml") ||
		!strings.Contains(string(secrets), "dex-client-muster-token-exchange-puffin-secret.yaml") {
		t.Errorf("kustomizations:\n%s\n%s\n%s", extras, dex, secrets)
	}
	if len(r.ManagementClustersFiles) == 0 || len(r.ConfigsFiles) != 2 {
		t.Errorf("files %v %v", r.ManagementClustersFiles, r.ConfigsFiles)
	}

	again, err := Run(context.Background(), o)
	if err != nil || !again.Done {
		t.Fatalf("second run: %+v, %v", again, err)
	}
	if len(v.copies) != 6 {
		t.Errorf("the second run copied again: %v", v.copies)
	}
}

func TestWriteResumesAfterTheSecrets(t *testing.T) {
	o, v := checkouts(t, "3.2.5")
	o.Write = true
	if _, err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	// A run stopped before the configs: the encrypted patch as before.
	enc := filepath.Join(o.Configs, "installations/puffin/apps/dex-app/secret-values.yaml.patch")
	_ = os.WriteFile(enc, []byte(encryptedPatch), 0o600)
	var doc yaml.Node
	_ = yaml.Unmarshal([]byte(decryptedPatch), &doc)
	v.plain[enc] = doc.Content[0]
	r, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range r.Clients {
		if c.Secret.State != StatePresent {
			t.Errorf("%s: %s, want present", c.ID(), c.Secret.State)
		}
	}
	if len(v.copies) != 6 {
		t.Errorf("copies %d, want the first run's 6", len(v.copies))
	}
}

func TestWriteRefusesAnotherValue(t *testing.T) {
	o, v := checkouts(t, "3.2.5")
	dst := filepath.Join(o.ManagementClusters, "management-clusters/puffin/extras/agent-platform/secrets/dex-client-kagent-secret.yaml")
	_ = os.WriteFile(dst, []byte("stringData:\n  secret: ENC[z]\n"), 0o600)
	var doc yaml.Node
	_ = yaml.Unmarshal([]byte("stringData:\n  secret: another-value\n"), &doc)
	v.plain[dst] = doc.Content[0]
	o.Write = true
	if _, err := Run(context.Background(), o); !errors.Is(err, ErrRefused) || len(v.copies) != 0 {
		t.Fatalf("err %v, copies %v: want refused before any copy", err, v.copies)
	}
}

func TestWriteRefusesADifferentHubValue(t *testing.T) {
	o, v := checkouts(t, "3.2.5")
	hubFile := filepath.Join(o.HubManagementClusters, "management-clusters/hubble/extras/agent-platform/secrets/puffin-token-exchange-credentials.yaml")
	var doc yaml.Node
	_ = yaml.Unmarshal([]byte("stringData:\n  client-secret: drifted-value\n"), &doc)
	v.plain[hubFile] = doc.Content[0]
	if _, err := Run(context.Background(), o); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "one value") {
		t.Fatalf("err %v, want the pairing refused", err)
	}
}

func TestOldDexAppRefused(t *testing.T) {
	o, _ := checkouts(t, "2.2.3")
	_, err := Run(context.Background(), o)
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "pin dex-app 3.2.2 or later") ||
		!strings.Contains(err.Error(), "management-clusters/puffin/collections/kustomization.yaml") {
		t.Fatalf("err %v, want the pin named", err)
	}
}

func TestMergePlaintextKeepsOtherKeys(t *testing.T) {
	current := "# kept comment\ningress:\n  enabled: true\noidc:\n  extraStaticClients:\n    - id: kagent\n      name: stale\n    - id: other\n      name: other\n"
	public := true
	out, err := MergePlaintext([]byte(current), []Client{{Source: "x", Entry: &Entry{ID: "kagent", Name: "kagent-ui", Public: &public, SecretRef: "dex-client-kagent"}}}, []string{"kagent"})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"# kept comment", "enabled: true", "id: other", "name: kagent-ui", "public: true", "- kagent"} {
		if !strings.Contains(string(out), s) {
			t.Errorf("merged lacks %q:\n%s", s, out)
		}
	}
	if strings.Contains(string(out), "stale") {
		t.Errorf("the shadowed entry stayed:\n%s", out)
	}
}

func TestKebab(t *testing.T) {
	for in, want := range map[string]string{"muster": "muster", "mcpKubernetes": "mcp-kubernetes", "mcpCapi": "mcp-capi"} {
		if got := kebab(in); got != want {
			t.Errorf("kebab(%s) = %s, want %s", in, got, want)
		}
	}
}
