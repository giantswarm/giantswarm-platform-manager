package dexsplit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/Masterminds/semver/v3"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// tokenExchangePrefix starts the id of the client a hub's muster exchanges
// tokens with: muster-token-exchange-<installation>. Its value is one with
// the hub's <installation>-token-exchange-credentials file.
const tokenExchangePrefix = "muster-token-exchange-" //nolint:gosec // a client id prefix, no credential

// Options are one split.
type Options struct {
	Installation string
	// Configs and ManagementClusters are checkouts of the installation's
	// two repositories.
	Configs, ManagementClusters string
	// Hub and HubManagementClusters name the hub whose muster exchanges
	// tokens with the installation, and a checkout of its
	// management-clusters repository; empty skips the pairing check.
	Hub, HubManagementClusters string
	Vault                      Vault
	// Write writes the split; without it the run is the dry run.
	Write bool
	// ReadBase reads a file of the fleet's base at a ref, for the dex-app
	// version where the installation pins none.
	ReadBase func(ctx context.Context, repository, path, ref string) (string, error)
}

// Report is what a run found and did.
type Report struct {
	Installation string `json:"installation"`
	Vault        string `json:"vault"`
	Write        bool   `json:"write"`
	DexApp       string `json:"dexApp"`
	DexAppSource string `json:"dexAppSource"`
	// Revealed says whether the vault revealed the clients' ids, names,
	// redirect URIs and peers; a keys-only report names them by key path.
	Revealed   bool   `json:"revealed"`
	Unrevealed string `json:"unrevealed,omitempty"`
	// Done says the installation is split already: nothing to move.
	Done      bool     `json:"done"`
	Clients   []Client `json:"clients,omitempty"`
	Peers     []string `json:"peers,omitempty"`
	PeerCount int      `json:"peerCount,omitempty"`
	Drop      []string `json:"drop,omitempty"`
	Keep      []string `json:"keep,omitempty"`
	Pairing   *Pairing `json:"pairing,omitempty"`
	// Files are the files the split writes, or wrote, per repository: the
	// management-clusters change merges first, the configs change after
	// flux-extras applied its Secrets.
	ManagementClustersFiles []string `json:"managementClustersFiles,omitempty"`
	ConfigsFiles            []string `json:"configsFiles,omitempty"`
}

// Client is one client of the split.
type Client struct {
	// Source is the client's key path in the encrypted patch; BuiltIn the
	// key under oidc.staticClients for a built-in client.
	Source  string `json:"source"`
	BuiltIn string `json:"builtIn,omitempty"`
	// Entry is the client's plaintext entry; nil while unrevealed.
	Entry *Entry `json:"entry,omitempty"`
	// Secret is where the inline secret moves; nil for a client without one.
	Secret *Move `json:"secret,omitempty"`
}

// ID is the client's id, or its key path while unrevealed.
func (c Client) ID() string {
	switch {
	case c.BuiltIn != "":
		return c.BuiltIn
	case c.Entry != nil:
		return c.Entry.ID
	}
	return c.Source
}

// Entry is an extra static client's plaintext fields.
type Entry struct {
	ID           string   `json:"id"`
	Name         string   `json:"name,omitempty"`
	Public       *bool    `json:"public,omitempty"`
	RedirectURIs []string `json:"redirectURIs,omitempty"`
	TrustedPeers []string `json:"trustedPeers,omitempty"`
	LogoURL      string   `json:"logoURL,omitempty"`
	// SecretRef is the Secret the entry references.
	SecretRef string `json:"secretRef,omitempty"`
}

// Move is one inline secret moving to its Secret.
type Move struct {
	// From is the secret's key path in the encrypted patch; empty for a
	// Secret an earlier split wrote already, which moves on from MovesFrom.
	From string `json:"from,omitempty"`
	// File is the Secret's file in the management-clusters repository,
	// Kustomization the kustomization.yaml that lists it.
	File          string `json:"file"`
	Secret        string `json:"secret"`
	Kustomization string `json:"kustomization"`
	// MovesFrom is the file an earlier split put the Secret in under
	// extras/dex, before the definitions' path was its destination: the
	// file moves on to File unchanged and is unlisted there.
	MovesFrom string `json:"movesFrom,omitempty"`
	// State is to-write, to-move (MovesFrom moves on), present (the file
	// carries the same value) or differs (refused); written and moved after
	// a write.
	State string `json:"state"`
}

// The states of a Move and a Pairing.
const (
	StateToWrite   = "to-write"
	StateToMove    = "to-move"
	StateWritten   = "written"
	StateMoved     = "moved"
	StatePresent   = "present"
	StateDiffers   = "differs"
	StateEqual     = "equal"
	StateUnchecked = "unchecked"
)

// source is where the secret's value is read from: the encrypted patch at
// From, or the file an earlier split wrote (earlier) once the patch no
// longer carries it.
func (m Move) source(o Options, encrypted string) Ref {
	if m.From == "" {
		return m.earlier(o)
	}
	return Ref{File: encrypted, Path: m.From}
}

// earlier is the value in the file an earlier split wrote (MovesFrom).
func (m Move) earlier(o Options) Ref {
	return Ref{File: filepath.Join(o.ManagementClusters, m.MovesFrom), Path: "stringData." + render.DexSecretKey}
}

// Pairing is the token-exchange client's value and the hub's copy.
type Pairing struct {
	Client string `json:"client"`
	HubRef string `json:"hubRef"`
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

// ErrRefused is a run the split refuses; the error names why.
var ErrRefused = errors.New("refused")

func refused(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrRefused, fmt.Sprintf(format, a...))
}

// Run plans the split of o.Installation and, with o.Write, writes it into
// the two checkouts. A split installation answers Done and writes nothing.
func Run(ctx context.Context, o Options) (Report, error) {
	r := Report{Installation: o.Installation, Vault: o.Vault.Name(), Write: o.Write}
	if err := dexAppGate(ctx, o, &r); err != nil {
		return r, err
	}
	encrypted := filepath.Join(o.Configs, installations.DexSecretPatchPath(o.Installation))
	data, err := os.ReadFile(filepath.Clean(encrypted))
	if errors.Is(err, os.ErrNotExist) {
		r.Done = true
		return r, nil
	}
	if err != nil {
		return r, err
	}
	shape, err := ReadShape(data)
	if err != nil {
		return r, fmt.Errorf("%s: %w", installations.DexSecretPatchPath(o.Installation), err)
	}
	r.Keep = shape.Keep
	split := shape.Split()
	if split {
		// The patch is split; a token-exchange client an earlier split put
		// under extras/dex still moves on, its id read from its file.
		r.Clients = movedOn(o)
		if len(r.Clients) == 0 {
			r.Done = true
			return r, nil
		}
		r.Revealed = true
	} else {
		r.Drop, r.PeerCount = shape.Drop, shape.Peers
		revealed, err := o.Vault.Reveal(ctx, encrypted, revealPaths(shape))
		switch {
		case errors.Is(err, ErrUnsupported) && !o.Write:
			r.Unrevealed = err.Error()
		case err != nil:
			return r, err
		default:
			r.Revealed = true
		}
		if r.Clients, err = clients(o, shape, revealed); err != nil {
			return r, err
		}
		for i := range shape.Peers {
			if v, ok := revealed[pathPeers+"."+strconv.Itoa(i)]; ok {
				r.Peers = append(r.Peers, v)
			}
		}
	}
	if err := checkMoves(ctx, o, encrypted, &r); err != nil {
		return r, err
	}
	pairing(ctx, o, encrypted, &r)
	if r.Pairing != nil && r.Pairing.State == StateDiffers {
		return r, refused("%s differs from the hub's %s: the two must stay one value; settle it first", r.Pairing.Client, r.Pairing.HubRef)
	}
	if !o.Write {
		r.ManagementClustersFiles, r.ConfigsFiles = plannedFiles(o, r, !split)
		return r, nil
	}
	if err := writeSecrets(ctx, o, encrypted, &r); err != nil {
		return r, err
	}
	if split {
		return r, nil
	}
	return r, writeConfigs(ctx, o, encrypted, &r)
}

// dexAppGate refuses a dex-app that cannot read a referenced client Secret,
// naming the pin to add.
func dexAppGate(ctx context.Context, o Options, r *Report) error {
	path := installations.CollectionsKustomizationPath(o.Installation)
	data, err := os.ReadFile(filepath.Clean(filepath.Join(o.ManagementClusters, path)))
	if err != nil {
		return fmt.Errorf("the dex-app version: %w", err)
	}
	v, err := installations.DexAppVersionIn(ctx, o.ReadBase, o.Installation, filepath.Base(o.ManagementClusters), string(data))
	if err != nil {
		return err
	}
	if v.Version == "" {
		return refused("no dex-app version in %s or its base", path)
	}
	r.DexApp, r.DexAppSource = v.Version, v.Source()
	sv, err := semver.NewVersion(v.Version)
	if err != nil {
		return err
	}
	if !render.DexAppTakes(sv, plan.DexAppReferencedSecrets) {
		return refused("dex-app %s (%s) reads no referenced client Secret: pin %s in %s first "+
			"(a patch on the App dex-app: op replace, path /spec/version)", v.Version, v.Source(),
			render.DexAppNeeds(plan.DexAppReferencedSecrets), path)
	}
	return nil
}

// revealPaths are the non-secret leaves of the shape: every extra client's
// fields but its secret, and the authenticator's peers.
func revealPaths(s Shape) []string {
	var out []string
	for _, e := range s.Extras {
		for _, f := range e.Fields {
			p := e.Path() + "." + f
			switch f {
			case keyRedirectURIs:
				out = append(out, indexed(p, e.RedirectURIs)...)
			case keyTrustedPeers:
				out = append(out, indexed(p, e.Peers)...)
			case keySecretRef:
				out = append(out, p+"."+keyName)
			default:
				out = append(out, p)
			}
		}
	}
	return append(out, indexed(pathPeers, s.Peers)...)
}

func indexed(path string, n int) []string {
	out := make([]string, n)
	for i := range n {
		out[i] = path + "." + strconv.Itoa(i)
	}
	return out
}

// clients are the split's clients with their entries, where revealed, and
// the destinations of their secrets.
func clients(o Options, s Shape, revealed map[string]string) ([]Client, error) {
	var out []Client
	names := map[string]string{}
	for _, key := range s.BuiltIns {
		component := kebab(key)
		c := Client{Source: pathStatic + "." + key, BuiltIn: key}
		c.Secret = destination(o, component, pathStatic+"."+key+"."+keyClientSecret)
		out = append(out, c)
	}
	for _, e := range s.Extras {
		c := Client{Source: e.Path()}
		if revealed != nil {
			entry, err := extraEntry(e, revealed)
			if err != nil {
				return nil, err
			}
			c.Entry = &entry
		}
		if e.Inline {
			if c.Entry == nil {
				c.Secret = &Move{From: e.Path() + "." + keySecret, File: "by the client's id", Secret: render.DexClientSecretName("<id>")}
			} else {
				id := strings.ToLower(c.Entry.ID)
				if !dnsSubdomain.MatchString(render.DexClientSecretName(id)) {
					return nil, refused("%s: the id %q makes no Secret name; move this client by hand", e.Path(), c.Entry.ID)
				}
				c.Secret = destination(o, id, e.Path()+"."+keySecret)
				c.Entry.SecretRef = c.Secret.Secret
			}
		}
		out = append(out, c)
	}
	for _, c := range out {
		if c.Secret == nil || c.Secret.Kustomization == "" {
			continue
		}
		if other, ok := names[c.Secret.Secret]; ok {
			return nil, refused("%s and %s both make the Secret %s; move one by hand", other, c.Source, c.Secret.Secret)
		}
		names[c.Secret.Secret] = c.Source
	}
	return out, nil
}

var dnsSubdomain = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)

// extraEntry is an extra client's plaintext entry from the revealed leaves.
func extraEntry(e ExtraShape, revealed map[string]string) (Entry, error) {
	get := func(f string) string { return revealed[e.Path()+"."+f] }
	entry := Entry{ID: get(keyID), Name: get(keyName), LogoURL: get(keyLogoURL)}
	for i := range e.RedirectURIs {
		entry.RedirectURIs = append(entry.RedirectURIs, get(keyRedirectURIs+"."+strconv.Itoa(i)))
	}
	for i := range e.Peers {
		entry.TrustedPeers = append(entry.TrustedPeers, get(keyTrustedPeers+"."+strconv.Itoa(i)))
	}
	for _, f := range e.Fields {
		switch f {
		case keyPublic:
			b, err := strconv.ParseBool(get(keyPublic))
			if err != nil {
				return entry, fmt.Errorf("%s.%s: no boolean", e.Path(), keyPublic)
			}
			entry.Public = &b
		case keySecretRef:
			entry.SecretRef = get(keySecretRef + "." + keyName)
		}
	}
	if entry.ID == "" {
		return entry, fmt.Errorf("%s.%s: empty", e.Path(), keyID)
	}
	return entry, nil
}

// destination is where a client's secret goes: the file the definitions
// render for it, in its component's directory where that directory's
// kustomization exists, else in the extras' dex directory the split keeps
// for the installation's own clients. A hub's token-exchange client goes
// under the platform's secrets whether or not the platform's directory is
// on the installation: cluster-mcp-servers renders the pair's Dex side
// there, the file the agent-platform definition renders for the same
// pairing, and the split writes the kustomization chain to it. Its Secret
// an earlier split put under extras/dex moves on from there.
func destination(o Options, client, from string) *Move {
	extras := "management-clusters/" + o.Installation + "/extras/"
	secret := render.DexClientSecretName(client)
	file := render.DexClientSecretFile(client)
	var dir string
	switch {
	case strings.HasPrefix(client, tokenExchangePrefix):
		m := &Move{From: from, File: extras + PlatformSecretsDir + "/" + file, Secret: secret, Kustomization: extras + PlatformSecretsDir + "/" + kustomizationFile, State: StateToWrite}
		if earlier := extras + DexDir + "/" + file; exists(filepath.Join(o.ManagementClusters, earlier)) {
			m.MovesFrom, m.State = earlier, StateToMove
		}
		return m
	case client == "muster" || client == "kagent" || strings.HasPrefix(client, "muster-"):
		dir = extras + PlatformSecretsDir
	case strings.HasPrefix(client, "mcp-"):
		dir = extras + client
	case client == render.PortalDexClientID:
		dir, file = extras+"backstage/"+render.PortalDir, render.PortalDexClientSecretFile
	}
	if dir == "" || !exists(filepath.Join(o.ManagementClusters, dir, kustomizationFile)) {
		dir, file = extras+DexDir, render.DexClientSecretFile(client)
	}
	return &Move{From: from, File: dir + "/" + file, Secret: secret, Kustomization: dir + "/" + kustomizationFile, State: StateToWrite}
}

// movedOn are the token-exchange clients whose Secrets an earlier split put
// under extras/dex, each moving on to the platform's secrets: a split patch
// reveals nothing, so the client's id is read from its file's name.
func movedOn(o Options) []Client {
	dir := filepath.Join(o.ManagementClusters, "management-clusters", o.Installation, "extras", DexDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Client
	for _, e := range entries {
		client, ok := clientOfFile(e.Name())
		if !ok || !strings.HasPrefix(client, tokenExchangePrefix) {
			continue
		}
		m := destination(o, client, "")
		out = append(out, Client{Source: m.MovesFrom, Entry: &Entry{ID: client, SecretRef: m.Secret}, Secret: m})
	}
	return out
}

// clientOfFile is the client whose Secret a file of the split's naming is
// (render.DexClientSecretFile), or false.
func clientOfFile(name string) (string, bool) {
	prefix, suffix := render.DexClientSecretName(""), strings.TrimPrefix(render.DexClientSecretFile(""), render.DexClientSecretName(""))
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) || len(name) <= len(prefix)+len(suffix) {
		return "", false
	}
	return name[len(prefix) : len(name)-len(suffix)], true
}

// DexDir is the extras directory the split keeps the installation's own
// clients' Secrets in: listed by the extras' kustomization, applied by
// flux-extras, owned by no definition.
const DexDir = "dex"

// PlatformSecretsDir is the extras directory the definitions render the
// platform's Secrets in: muster's and kagent's Dex clients, and the hubs'
// token-exchange clients whichever definition renders the Dex side.
const PlatformSecretsDir = "agent-platform/secrets"

const kustomizationFile = "kustomization.yaml"

// kebab is a built-in client key as its component's name: mcpKubernetes is
// mcp-kubernetes.
func kebab(key string) string {
	var b strings.Builder
	for i, r := range key {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('-')
			}
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// checkMoves marks each move whose Secret file exists: present when it
// carries the inline value, refused when it carries another. A Secret that
// moves on from an earlier split's file is checked against the inline value
// while the patch still carries it, and refused where its destination exists
// as well: two files of one client are a person's to settle.
func checkMoves(ctx context.Context, o Options, encrypted string, r *Report) error {
	for _, c := range r.Clients {
		m := c.Secret
		if m == nil || m.Kustomization == "" {
			continue
		}
		dst := filepath.Join(o.ManagementClusters, m.File)
		if m.MovesFrom != "" {
			if exists(dst) {
				m.State = StateDiffers
				return refused("%s and %s both exist for %s; keep one by hand", m.MovesFrom, m.File, c.ID())
			}
			if m.From == "" {
				continue
			}
			equal, err := o.Vault.Equal(ctx, Ref{File: encrypted, Path: m.From}, m.earlier(o))
			if err != nil {
				return err
			}
			if !equal {
				m.State = StateDiffers
				return refused("%s exists with another value than %s: Dex would change the client's secret; settle it by hand", m.MovesFrom, m.From)
			}
			continue
		}
		if !exists(dst) {
			continue
		}
		equal, err := o.Vault.Equal(ctx, Ref{File: encrypted, Path: m.From}, Ref{File: dst, Path: "stringData." + render.DexSecretKey})
		if err != nil {
			return err
		}
		if !equal {
			m.State = StateDiffers
			return refused("%s exists with another value than %s: Dex would change the client's secret; settle it by hand", m.File, m.From)
		}
		m.State = StatePresent
	}
	return nil
}

// pairing checks the token-exchange client's value against the hub's copy.
func pairing(ctx context.Context, o Options, encrypted string, r *Report) {
	for _, c := range r.Clients {
		if c.Entry == nil || !strings.HasPrefix(c.Entry.ID, tokenExchangePrefix) || c.Secret == nil {
			continue
		}
		target := strings.TrimPrefix(c.Entry.ID, tokenExchangePrefix)
		p := &Pairing{Client: c.Entry.ID, State: StateUnchecked}
		r.Pairing = p
		if o.Hub == "" || o.HubManagementClusters == "" {
			p.HubRef = "<hub>:management-clusters/<hub>/extras/agent-platform/secrets/" + target + "-token-exchange-credentials.yaml#stringData.client-secret"
			p.Reason = "no --hub and --hub-management-clusters"
			return
		}
		file := "management-clusters/" + o.Hub + "/extras/agent-platform/secrets/" + target + "-token-exchange-credentials.yaml"
		p.HubRef = file + "#stringData.client-secret"
		equal, err := o.Vault.Equal(ctx, c.Secret.source(o, encrypted), Ref{File: filepath.Join(o.HubManagementClusters, file), Path: "stringData.client-secret"})
		switch {
		case err != nil:
			p.Reason = err.Error()
		case equal:
			p.State = StateEqual
		default:
			p.State = StateDiffers
		}
		return
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
