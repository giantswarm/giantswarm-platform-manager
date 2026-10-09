package dexsplit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// ConfigMapPatchPath is the installation's plaintext dex-app patch in its
// configs repository.
func ConfigMapPatchPath(installation string) string {
	return "installations/" + installation + "/apps/dex-app/configmap-values.yaml.patch"
}

// extrasKustomization is the installation's extras kustomization, which
// lists DexDir where the split creates it.
func extrasKustomization(installation string) string {
	return "management-clusters/" + installation + "/extras/" + kustomizationFile
}

// plannedFiles are the files a write changes, per repository; a client
// still unrevealed has no file yet.
func plannedFiles(o Options, r Report) (mc, configs []string) {
	for _, c := range r.Clients {
		m := c.Secret
		if m == nil || m.Kustomization == "" || m.State == StatePresent {
			continue
		}
		mc = appendNew(mc, m.File)
		mc = appendNew(mc, m.Kustomization)
		if filepath.Base(filepath.Dir(m.Kustomization)) == DexDir && !exists(filepath.Join(o.ManagementClusters, m.Kustomization)) {
			mc = appendNew(mc, extrasKustomization(o.Installation))
		}
	}
	return mc, []string{ConfigMapPatchPath(o.Installation), installations.DexSecretPatchPath(o.Installation)}
}

func appendNew(list []string, s string) []string {
	if slices.Contains(list, s) {
		return list
	}
	return append(list, s)
}

// write writes the split: the Secrets and their kustomization entries into
// the management-clusters checkout, then the plaintext patch and the cut
// encrypted patch into the configs checkout. Each step is idempotent, so a
// run that stopped half-way is completed by the next.
func write(ctx context.Context, o Options, encrypted string, r *Report) error {
	for _, c := range r.Clients {
		m := c.Secret
		if m == nil {
			continue
		}
		r.ManagementClustersFiles = appendNew(r.ManagementClustersFiles, m.File)
		if m.State == StateToWrite {
			dst := filepath.Join(o.ManagementClusters, m.File)
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			if err := o.Vault.CopySecret(ctx, Ref{File: encrypted, Path: m.From}, dst, m.Secret, render.DexNamespace, render.DexSecretKey); err != nil {
				return err
			}
			m.State = StateWritten
		}
		changed, err := listResource(o, m)
		if err != nil {
			return err
		}
		r.ManagementClustersFiles = append(r.ManagementClustersFiles, changed...)
	}
	plain := filepath.Join(o.Configs, ConfigMapPatchPath(o.Installation))
	current, err := os.ReadFile(plain)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	merged, err := MergePlaintext(current, r.Clients, r.Peers)
	if err != nil {
		return fmt.Errorf("%s: %w", ConfigMapPatchPath(o.Installation), err)
	}
	if err := os.WriteFile(plain, merged, 0o644); err != nil { //nolint:gosec // a plaintext values file of the repository
		return err
	}
	if err := o.Vault.Unset(ctx, encrypted, r.Drop); err != nil {
		return err
	}
	r.ConfigsFiles = []string{ConfigMapPatchPath(o.Installation), installations.DexSecretPatchPath(o.Installation)}
	return nil
}

// listResource lists m's file in its kustomization, creating the dex
// directory's kustomization and listing it in the extras' where needed. It
// answers the files it changed.
func listResource(o Options, m *Move) ([]string, error) {
	var changed []string
	path := filepath.Join(o.ManagementClusters, m.Kustomization)
	current, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		current = []byte("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\n")
		extras := filepath.Join(o.ManagementClusters, extrasKustomization(o.Installation))
		data, err := os.ReadFile(extras)
		if err != nil {
			return nil, fmt.Errorf("the extras kustomization: %w", err)
		}
		edited, ok, err := plan.ListEntry(data, plan.ListResources, "./"+DexDir+"/")
		if err != nil {
			return nil, fmt.Errorf("%s: %w", extrasKustomization(o.Installation), err)
		}
		if ok {
			if err := os.WriteFile(extras, edited, 0o644); err != nil { //nolint:gosec // a kustomization of the repository
				return nil, err
			}
			changed = append(changed, extrasKustomization(o.Installation))
		}
	} else if err != nil {
		return nil, err
	}
	edited, ok, err := plan.ListEntry(current, plan.ListResources, filepath.Base(m.File))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", m.Kustomization, err)
	}
	if ok {
		if err := os.WriteFile(path, edited, 0o644); err != nil { //nolint:gosec // a kustomization of the repository
			return nil, err
		}
		changed = append(changed, m.Kustomization)
	}
	return changed, nil
}

// MergePlaintext answers the plaintext dex-app patch current with every
// client of the split and the peers: a built-in client's clientSecretRef,
// an extra client's entry (replacing an entry of the same id, which the
// encrypted list has shadowed so far) and each peer not listed yet. Every
// other key and comment of current stays.
func MergePlaintext(current []byte, clients []Client, peers []string) ([]byte, error) {
	var doc yaml.Node
	if len(current) > 0 {
		if err := yaml.Unmarshal(current, &doc); err != nil {
			return nil, fmt.Errorf("decode: %w", err)
		}
	}
	if len(doc.Content) == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("the document is no mapping")
	}
	oidc := ensure(root, keyOIDC, yaml.MappingNode)
	for _, c := range clients {
		switch {
		case c.BuiltIn != "" && c.Secret != nil:
			client := ensure(ensure(oidc, keyStaticClients, yaml.MappingNode), c.BuiltIn, yaml.MappingNode)
			set(client, keyClientRef, secretRefNode(c.Secret.Secret))
		case c.Entry != nil:
			list := ensure(oidc, keyExtraClients, yaml.SequenceNode)
			entry := entryNode(*c.Entry)
			i := slices.IndexFunc(list.Content, func(n *yaml.Node) bool {
				id := child(n, keyID)
				return id != nil && id.Value == c.Entry.ID
			})
			if i >= 0 {
				list.Content[i] = entry
			} else {
				list.Content = append(list.Content, entry)
			}
		case c.BuiltIn == "":
			return nil, fmt.Errorf("%s: unrevealed", c.Source)
		}
	}
	if len(peers) > 0 {
		list := ensure(ensure(ensure(oidc, keyStaticClients, yaml.MappingNode), keyAuthenticator, yaml.MappingNode), keyTrustedPeers, yaml.SequenceNode)
		for _, p := range peers {
			if !slices.ContainsFunc(list.Content, func(n *yaml.Node) bool { return n.Value == p }) {
				list.Content = append(list.Content, str(p))
			}
		}
	}
	return encode(&doc)
}

// entryNode is an extra client's entry in the order the definitions render
// one.
func entryNode(e Entry) *yaml.Node {
	n := &yaml.Node{Kind: yaml.MappingNode}
	set(n, keyID, str(e.ID))
	if e.Name != "" {
		set(n, keyName, str(e.Name))
	}
	if e.Public != nil {
		set(n, keyPublic, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: fmt.Sprint(*e.Public)})
	}
	if len(e.RedirectURIs) > 0 {
		set(n, keyRedirectURIs, strs(e.RedirectURIs))
	}
	if len(e.TrustedPeers) > 0 {
		set(n, keyTrustedPeers, strs(e.TrustedPeers))
	}
	if e.LogoURL != "" {
		set(n, keyLogoURL, str(e.LogoURL))
	}
	if e.SecretRef != "" {
		set(n, keySecretRef, secretRefNode(e.SecretRef))
	}
	return n
}

func secretRefNode(name string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.MappingNode}
	set(n, keyName, str(name))
	set(n, "key", str(render.DexSecretKey))
	return n
}

func str(s string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s} }

func strs(ss []string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.SequenceNode}
	for _, s := range ss {
		n.Content = append(n.Content, str(s))
	}
	return n
}

// ensure is the node under key of the mapping m, created as kind where
// missing or null.
func ensure(m *yaml.Node, key string, kind yaml.Kind) *yaml.Node {
	if n := child(m, key); n != nil && n.Kind == kind {
		return n
	}
	n := &yaml.Node{Kind: kind}
	set(m, key, n)
	return n
}

// set puts value under key of the mapping m, replacing a value there.
func set(m *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = value
			return
		}
	}
	m.Content = append(m.Content, str(key), value)
}

// encode encodes with a two-space indent, the repositories' style.
func encode(doc *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
