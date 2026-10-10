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

// extrasKustomization is the installation's extras kustomization, the top of
// every chain the split creates a kustomization in.
func extrasKustomization(installation string) string {
	return "management-clusters/" + installation + "/extras/" + kustomizationFile
}

// plannedFiles are the files a write changes, per repository; a client
// still unrevealed has no file yet, and a split patch (configs false) takes
// no configs change.
func plannedFiles(o Options, r Report, configs bool) (mc, cfg []string) {
	for _, c := range r.Clients {
		m := c.Secret
		if m == nil || m.Kustomization == "" || m.State == StatePresent {
			continue
		}
		mc = appendNew(mc, m.File)
		for _, k := range chain(o, m) {
			mc = appendNew(mc, k.file)
		}
		if m.MovesFrom != "" {
			mc = appendNew(mc, m.MovesFrom)
			mc = appendNew(mc, filepath.ToSlash(filepath.Join(filepath.Dir(m.MovesFrom), kustomizationFile)))
		}
	}
	if configs {
		cfg = []string{ConfigMapPatchPath(o.Installation), installations.DexSecretPatchPath(o.Installation)}
	}
	return mc, cfg
}

func appendNew(list []string, s string) []string {
	if slices.Contains(list, s) {
		return list
	}
	return append(list, s)
}

// writeSecrets writes the split's management-clusters side: each Secret
// copied from the encrypted patch, or moved on from an earlier split's
// file, and listed in its kustomization with the chain above it. Each step
// is idempotent, so a run that stopped half-way is completed by the next.
func writeSecrets(ctx context.Context, o Options, encrypted string, r *Report) error {
	for _, c := range r.Clients {
		m := c.Secret
		if m == nil {
			continue
		}
		r.ManagementClustersFiles = appendNew(r.ManagementClustersFiles, m.File)
		dst := filepath.Join(o.ManagementClusters, m.File)
		switch m.State {
		case StateToWrite:
			if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
				return err
			}
			if err := o.Vault.CopySecret(ctx, Ref{File: encrypted, Path: m.From}, dst, m.Secret, render.DexNamespace, render.DexSecretKey); err != nil {
				return err
			}
			m.State = StateWritten
		case StateToMove:
			changed, err := moveOn(o, m)
			if err != nil {
				return err
			}
			r.ManagementClustersFiles = append(r.ManagementClustersFiles, changed...)
			m.State = StateMoved
		}
		changed, err := listResource(o, m)
		if err != nil {
			return err
		}
		r.ManagementClustersFiles = append(r.ManagementClustersFiles, changed...)
	}
	return nil
}

// moveOn moves m's Secret from the earlier split's file to its destination,
// unchanged, and unlists it there; it answers the files it changed.
func moveOn(o Options, m *Move) ([]string, error) {
	dst := filepath.Join(o.ManagementClusters, m.File)
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return nil, err
	}
	if err := os.Rename(filepath.Join(o.ManagementClusters, m.MovesFrom), dst); err != nil {
		return nil, err
	}
	changed := []string{m.MovesFrom}
	listed := filepath.ToSlash(filepath.Join(filepath.Dir(m.MovesFrom), kustomizationFile))
	path := filepath.Join(o.ManagementClusters, listed)
	current, err := os.ReadFile(filepath.Clean(path))
	if errors.Is(err, os.ErrNotExist) {
		return changed, nil
	}
	if err != nil {
		return nil, err
	}
	edited, err := plan.UnlistEntry(current, plan.ListResources, filepath.Base(m.MovesFrom))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", listed, err)
	}
	if !bytes.Equal(edited, current) {
		if err := os.WriteFile(path, edited, 0o644); err != nil { //nolint:gosec // a kustomization of the repository
			return nil, err
		}
		changed = append(changed, listed)
	}
	return changed, nil
}

// writeConfigs writes the split's configs side: the plaintext patch with
// every client and the peers, and the encrypted patch without the moved
// keys.
func writeConfigs(ctx context.Context, o Options, encrypted string, r *Report) error {
	plain := filepath.Join(o.Configs, ConfigMapPatchPath(o.Installation))
	current, err := os.ReadFile(filepath.Clean(plain))
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

// listing is one kustomization of a chain and the entry it lists.
type listing struct {
	file, entry string
}

// chain are the kustomizations m's file is listed through, innermost first:
// m's own kustomization listing the file and, while a kustomization does not
// exist on the installation, its parent's listing the directory, up to the
// extras kustomization, which every installation carries. The entries are
// the definitions': a directory as ./<name>/ in the extras kustomization
// (its includes), as ./<name> below it (the platform's secrets directory).
func chain(o Options, m *Move) []listing {
	out := []listing{{file: m.Kustomization, entry: filepath.Base(m.File)}}
	extras := extrasKustomization(o.Installation)
	for k := m.Kustomization; k != extras && !exists(filepath.Join(o.ManagementClusters, k)); {
		dir := filepath.Dir(k)
		parent := filepath.ToSlash(filepath.Join(filepath.Dir(dir), kustomizationFile))
		entry := "./" + filepath.Base(dir)
		if parent == extras {
			entry += "/"
		}
		out = append(out, listing{file: parent, entry: entry})
		k = parent
	}
	return out
}

// listResource lists m's file in its kustomization and, where that or a
// kustomization above it does not exist yet, creates it and lists it in its
// parent's, up to the extras kustomization (chain). It answers the files it
// changed.
func listResource(o Options, m *Move) ([]string, error) {
	var changed []string
	for _, l := range chain(o, m) {
		path := filepath.Join(o.ManagementClusters, l.file)
		current, err := os.ReadFile(filepath.Clean(path))
		switch {
		case errors.Is(err, os.ErrNotExist) && l.file == extrasKustomization(o.Installation):
			return nil, fmt.Errorf("the extras kustomization: %w", err)
		case errors.Is(err, os.ErrNotExist):
			current = []byte("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\n")
		case err != nil:
			return nil, err
		}
		edited, ok, err := plan.ListEntry(current, plan.ListResources, l.entry)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", l.file, err)
		}
		if !ok {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, edited, 0o644); err != nil { //nolint:gosec // a kustomization of the repository
			return nil, err
		}
		changed = append(changed, l.file)
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
