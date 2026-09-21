package installations

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/google/go-github/v92/github"
	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// The facts a definition derives from the portals' app-configs — which portals
// sign people in on an installation, whose broker exchanges tokens into it,
// and which installations its own muster brokers for — are read here, once per
// inspection, and land on the Report as installation facts (Facts): a
// definition names them under installation in its schema and never asks a
// person for them.

// Portal is a developer portal on record: the installation hosting it and its
// organisation, its hostname, the id of the Dex client it signs in through
// (empty when the host's dex-app configmap patch carries no client with the
// portal's redirect URI), the installations it lists, the installation whose
// muster brokers its cluster tokens (empty when it brokers none), the
// installations it reaches through the tunnel on its host, the installations
// whose agent platform (kagent, agentgateway) it proxies — its app-config's
// agentPlatform.kagent.installations — and whether it is hand-kept: its
// app-config carries a literal app.extensions list of its own where a portal
// the customer-portal definition renders includes the shared list.
type Portal struct {
	Host            string
	Customer        string
	Domain          string
	ClientID        string
	Broker          string
	Installations   []string
	Tunnelled       []string
	PlatformProxied []string
	HandKept        bool
}

// PortalRef is a portal that signs people in on an installation, as the
// definitions' installation.portals[*] names it. HandKept says the portal's
// app-config on record carries its own extension list: the agent-platform
// Component then sets none of the portal's lists.
type PortalRef struct {
	Installation string `json:"installation"`
	Customer     string `json:"customer"`
	Domain       string `json:"domain"`
	ClientID     string `json:"clientId,omitempty"`
	HandKept     bool   `json:"handKept,omitempty"`
}

// Federation is an installation's place in the fleet's token exchange, as the
// definitions' installation.federation names it.
type Federation struct {
	Hubs           []string          `json:"hubs"`
	Targets        []FederatedTarget `json:"targets"`
	BrokerClientID string            `json:"brokerClientId,omitempty"`
}

// FederatedTarget is an installation a hub brokers for.
type FederatedTarget struct {
	Installation string `json:"installation"`
	BaseDomain   string `json:"baseDomain"`
	Private      bool   `json:"private"`
	// PlatformProxied says the hub's portal proxies the target's agent
	// platform (its app-config's agentPlatform.kagent.installations lists
	// the target): a private target with it is also tunnelled to its kagent
	// and its agentgateway.
	PlatformProxied bool `json:"platformProxied"`
	// Hubs are the installations of the hub's organisation whose muster
	// brokers into the target, the hub among them, in the order the
	// connectors the target's Dex registers for them are named: the
	// registry's hub first where it is one, then by name. The target's Dex
	// registers one connector per hub, and only one of an organisation's can
	// carry the organisation's plain name — the first's; every further hub's
	// carries the hub's name (organisationHubs).
	Hubs []string `json:"hubs"`
}

// AgentPlatformPatchPath is where the installation's configs repository keeps
// the platform's values patch — the agent-platform capability's marker, and
// where a hub's broker client id is read back from.
func AgentPlatformPatchPath(name string) string {
	return "installations/" + name + "/apps/agent-platform/configmap-values.yaml.patch"
}

// DexPatchPath is where the installation's configs repository keeps the
// dex-app configmap patch: the clients in plaintext, every secret a
// reference — where a portal's client id is read from.
func DexPatchPath(name string) string {
	return "installations/" + name + "/apps/dex-app/configmap-values.yaml.patch"
}

// portalHosts are the installations whose portal may list one of insts: the
// hub's, and every installation of the same organisations.
func (r *Registry) portalHosts(insts []Installation) []Installation {
	customers := map[string]bool{}
	for _, inst := range insts {
		customers[inst.Customer] = true
	}
	var hosts []Installation
	for _, inst := range r.Installations {
		if (inst.Hub || customers[inst.Customer]) && inst.Repositories.ManagementClusters != "" {
			hosts = append(hosts, inst)
		}
	}
	return hosts
}

// readPortal reads host's portal app-config as the person: the portal, or nil
// when host has none.
func (r *Registry) readPortal(ctx context.Context, c *github.Client, host Installation) (*Portal, error) {
	owner, repo, err := gh.SplitRepo(host.Repositories.ManagementClusters)
	if err != nil {
		return nil, err
	}
	data, err := gh.ReadFile(ctx, c, owner, repo, PortalConfigPath(host.Name))
	if errors.Is(err, gh.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cfg, err := parsePortalConfig(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", PortalConfigPath(host.Name), err)
	}
	p := &Portal{Host: host.Name, Customer: host.Customer, Domain: hostOf(cfg.BaseURL), HandKept: cfg.HandKept}
	for name := range cfg.Installations {
		p.Installations = append(p.Installations, name)
	}
	sort.Strings(p.Installations)
	for name := range cfg.Tunnelled {
		p.Tunnelled = append(p.Tunnelled, name)
	}
	sort.Strings(p.Tunnelled)
	for name := range cfg.PlatformProxied {
		p.PlatformProxied = append(p.PlatformProxied, name)
	}
	sort.Strings(p.PlatformProxied)
	if broker := hostOf(cfg.BrokerTokenURL); broker != "" {
		for _, inst := range r.Installations {
			if inst.BaseDomain != "" && broker == "muster."+inst.BaseDomain {
				p.Broker = inst.Name
			}
		}
		if p.Broker == "" {
			return nil, fmt.Errorf("%s: the cluster-token broker %s is no installation's muster", PortalConfigPath(host.Name), broker)
		}
	}
	if p.ClientID, err = portalClientID(ctx, c, host, p.Domain); err != nil {
		return nil, err
	}
	return p, nil
}

// portalClientID reads the id of the Dex client the portal on domain signs in
// through: the entry of host's dex-app configmap patch whose redirect URIs
// carry the portal's. The app-config names the id only as a reference the
// chart resolves from the portal's encrypted user-secrets, so the patch is the
// id's one plaintext place on record. Empty when host has no configs
// repository or no patch, or the patch carries no such client.
func portalClientID(ctx context.Context, c *github.Client, host Installation, domain string) (string, error) {
	if host.Repositories.Configs == "" {
		return "", nil
	}
	owner, repo, err := gh.SplitRepo(host.Repositories.Configs)
	if err != nil {
		return "", err
	}
	data, err := gh.ReadFile(ctx, c, owner, repo, DexPatchPath(host.Name))
	if errors.Is(err, gh.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	id, err := dexClientByRedirectURI(data, render.PortalRedirectURI(domain, host.Name))
	if err != nil {
		return "", fmt.Errorf("%s: %w", DexPatchPath(host.Name), err)
	}
	return id, nil
}

// dexClientByRedirectURI is the id of the extra static client of a dex-app
// configmap patch that redirects to uri, empty for none.
func dexClientByRedirectURI(data, uri string) (string, error) {
	var patch struct {
		OIDC struct {
			ExtraStaticClients []struct {
				ID           string   `yaml:"id"`
				RedirectURIs []string `yaml:"redirectURIs"`
			} `yaml:"extraStaticClients"`
		} `yaml:"oidc"`
	}
	if err := yaml.Unmarshal([]byte(data), &patch); err != nil {
		return "", err
	}
	for _, client := range patch.OIDC.ExtraStaticClients {
		if slices.Contains(client.RedirectURIs, uri) {
			return client.ID, nil
		}
	}
	return "", nil
}

// hostOf is the hostname of a URL (no port), empty for none.
func hostOf(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// tunnelled says whether an installation is reached through Teleport: a
// portal on record reaches its Kubernetes API through the tunnel on the
// portal's host. That is the record's private flag — its own MCP servers
// reach Dex on private addresses, and a hub brokers for it through the tunnel.
func tunnelled(portals []Portal, name string) bool {
	return slices.ContainsFunc(portals, func(p Portal) bool { return slices.Contains(p.Tunnelled, name) })
}

// Portals reads every portal that may list one of insts, in parallel. A host
// whose portal cannot be read as the person is returned by its organisation
// in failed: a customer's portal lists that customer's installations only, so
// their records are the incomplete ones; the hub's portal fails every record
// (key "").
func (r *Registry) Portals(ctx context.Context, c *github.Client, insts []Installation) (portals []Portal, failed map[string]error) {
	hosts := r.portalHosts(insts)
	found := make([]*Portal, len(hosts))
	errs := make([]error, len(hosts))
	var wg sync.WaitGroup
	for i, host := range hosts {
		wg.Go(func() { found[i], errs[i] = r.readPortal(ctx, c, host) })
	}
	wg.Wait()
	failed = map[string]error{}
	for i, host := range hosts {
		switch {
		case errs[i] != nil && host.Hub:
			failed[""] = errs[i]
		case errs[i] != nil:
			failed[host.Customer] = errs[i]
		case found[i] != nil:
			portals = append(portals, *found[i])
		}
	}
	return portals, failed
}

// derive fills the report's portal and federation facts from the portals on
// record, and the record's private flag: whether a portal reaches the
// installation through the tunnel. A target's private flag is the same fact,
// its platformProxied flag whether a portal this installation brokers for
// proxies the target's agent platform, its hubs the installations of this
// organisation that broker into it; a target not among the reports has its
// record read for its base domain. A hub's broker client id is read back from
// its patch. What cannot be read is
// an error of the report: the record is then incomplete and the installation
// is not planned.
func (r *Registry) derive(ctx context.Context, c *github.Client, reports []Report, portals []Portal) {
	byName := map[string]*Report{}
	for i := range reports {
		byName[reports[i].Name] = &reports[i]
	}
	// Every read at once — a hub brokers for a fleet's worth of targets —
	// and the reports written only once every read is in, in order.
	type derived struct {
		targets []string
		found   []FederatedTarget
		errs    []error
		broker  string
		brkErr  error
	}
	results := make([]derived, len(reports))
	var wg sync.WaitGroup
	for i := range reports {
		rep := &reports[i]
		if rep.Record == nil {
			continue
		}
		targets := rep.derivePortals(portals)
		rep.Record.Private = tunnelled(portals, rep.Name)
		res := &results[i]
		res.targets, res.found, res.errs = targets, make([]FederatedTarget, len(targets)), make([]error, len(targets))
		for j, name := range targets {
			hubs := r.organisationHubs(hubsOf(portals, name), rep.Customer)
			wg.Go(func() {
				res.found[j], res.errs[j] = r.target(ctx, c, name, byName[name], tunnelled(portals, name), proxied(portals, rep.Name, name), hubs)
			})
		}
		if len(targets) > 0 {
			wg.Go(func() { res.broker, res.brkErr = brokerClientID(ctx, c, *rep) })
		}
	}
	wg.Wait()
	for i := range reports {
		rep, res := &reports[i], results[i]
		if rep.Record == nil {
			continue
		}
		for j, name := range res.targets {
			if res.errs[j] != nil {
				rep.fail(fmt.Sprintf("federation target %s: %v", name, res.errs[j]))
				continue
			}
			rep.Federation.Targets = append(rep.Federation.Targets, res.found[j])
		}
		if len(res.targets) > 0 {
			if res.brkErr != nil {
				rep.fail(fmt.Sprintf("the broker client id: %v", res.brkErr))
			}
			rep.Federation.BrokerClientID = res.broker
		}
	}
}

// derivePortals fills the report's portals and hubs from the portals on record
// and answers the names of the installations its own muster brokers for, in
// the portals' order, each once.
func (r *Report) derivePortals(portals []Portal) []string {
	r.Portals, r.Federation = []PortalRef{}, &Federation{Hubs: hubsOf(portals, r.Name), Targets: []FederatedTarget{}}
	var targets []string
	for _, p := range portals {
		if slices.Contains(p.Installations, r.Name) {
			r.Portals = append(r.Portals, PortalRef{Installation: p.Host, Customer: p.Customer, Domain: p.Domain, ClientID: p.ClientID, HandKept: p.HandKept})
		}
		if p.Broker != r.Name {
			continue
		}
		for _, name := range p.Installations {
			if name != r.Name && !slices.Contains(targets, name) {
				targets = append(targets, name)
			}
		}
	}
	return targets
}

// hubsOf are the installations whose muster brokers into name: the
// cluster-token broker of every portal that lists name, other than name
// itself, in the portals' order, each once. Never nil: a definition reads it
// as a list.
func hubsOf(portals []Portal, name string) []string {
	hubs := []string{}
	for _, p := range portals {
		if p.Broker != "" && p.Broker != name && slices.Contains(p.Installations, name) && !slices.Contains(hubs, p.Broker) {
			hubs = append(hubs, p.Broker)
		}
	}
	return hubs
}

// organisationHubs are the hubs of customer's among hubs, in the order the
// connectors a target's Dex registers for them are named: the registry's hub
// first where it is one, then by name. A target's Dex registers one
// connector per hub, and of an organisation's hubs only the first carries the
// organisation's plain name; the order is the registry's, not the portals',
// so that it does not change with the catalog. A broker is always a registry
// installation (readPortal resolves it to one).
func (r *Registry) organisationHubs(hubs []string, customer string) []string {
	own := []string{}
	for _, name := range hubs {
		if inst, ok := r.Find(name); ok && inst.Customer == customer {
			own = append(own, name)
		}
	}
	slices.SortFunc(own, func(a, b string) int {
		ia, _ := r.Find(a)
		ib, _ := r.Find(b)
		if ia.Hub != ib.Hub {
			if ia.Hub {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	})
	return own
}

// fail records what could not be read; the report is no longer a complete record.
func (r *Report) fail(msg string) {
	r.Errors = append(r.Errors, msg)
	r.Readable = false
}

// target is a federated target's facts: the registry's base domain (the
// record's, read where the target was not inspected), whether it is reached
// through the tunnel, whether the hub's portal proxies its agent platform and
// the hubs of the hub's organisation that broker into it (organisationHubs).
func (r *Registry) target(ctx context.Context, c *github.Client, name string, inspected *Report, private, proxied bool, hubs []string) (FederatedTarget, error) {
	inst, ok := r.Find(name)
	if !ok {
		return FederatedTarget{}, errors.New("not in the registry")
	}
	if inst.Repositories.Configs == "" {
		return FederatedTarget{}, errors.New("no configs repository on record")
	}
	rec := (*Record)(nil)
	if inspected != nil {
		rec = inspected.Record
	}
	if rec == nil {
		owner, repo, err := gh.SplitRepo(inst.Repositories.Configs)
		if err != nil {
			return FederatedTarget{}, err
		}
		if rec, err = r.readRecord(ctx, c, owner, repo, inst); err != nil {
			return FederatedTarget{}, err
		}
	}
	return FederatedTarget{Installation: name, BaseDomain: rec.BaseDomain, Private: private, PlatformProxied: proxied, Hubs: hubs}, nil
}

// proxied says whether a portal that hub brokers for proxies name's agent
// platform: its app-config's agentPlatform.kagent.installations lists name,
// so the portal reaches name's kagent and agentgateway through the tunnel on
// hub. A private target with it is tunnelled to both; one the portal reaches
// through the kubernetes tunnel alone is not.
func proxied(portals []Portal, hub, name string) bool {
	return slices.ContainsFunc(portals, func(p Portal) bool { return p.Broker == hub && slices.Contains(p.PlatformProxied, name) })
}

// brokerClientID reads a hub's broker client id back from its patch: the one
// client under muster.muster.oauth.server.tokenExchangeBroker.brokerClients.
// Empty when the hub carries no patch or no broker yet.
func brokerClientID(ctx context.Context, c *github.Client, hub Report) (string, error) {
	owner, repo, err := gh.SplitRepo(hub.Repositories.Configs)
	if err != nil {
		return "", err
	}
	data, err := gh.ReadFile(ctx, c, owner, repo, AgentPlatformPatchPath(hub.Name))
	if errors.Is(err, gh.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var patch struct {
		Muster struct {
			Muster struct {
				OAuth struct {
					Server struct {
						TokenExchangeBroker struct {
							BrokerClients map[string]any `yaml:"brokerClients"`
						} `yaml:"tokenExchangeBroker"`
					} `yaml:"server"`
				} `yaml:"oauth"`
			} `yaml:"muster"`
		} `yaml:"muster"`
	}
	if err := yaml.Unmarshal([]byte(data), &patch); err != nil {
		return "", fmt.Errorf("%s: %w", AgentPlatformPatchPath(hub.Name), err)
	}
	ids := make([]string, 0, 1)
	for id := range patch.Muster.Muster.OAuth.Server.TokenExchangeBroker.BrokerClients {
		ids = append(ids, id)
	}
	if len(ids) > 1 {
		sort.Strings(ids)
		return "", fmt.Errorf("%s: %d broker clients (%s); the definition renders one", AgentPlatformPatchPath(hub.Name), len(ids), strings.Join(ids, ", "))
	}
	if len(ids) == 0 {
		return "", nil
	}
	return ids[0], nil
}
