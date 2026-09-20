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
	"github.com/giantswarm/giantswarm-platform-manager/render/agentplatform"
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
// installations it reaches through the tunnel on its host and the installations
// whose agent platform (kagent, agentgateway) it proxies — its app-config's
// agentPlatform.kagent.installations.
type Portal struct {
	Host            string
	Customer        string
	Domain          string
	ClientID        string
	Broker          string
	Installations   []string
	Tunnelled       []string
	PlatformProxied []string
}

// PortalRef is a portal that signs people in on an installation, as the
// definitions' installation.portals[*] names it.
type PortalRef struct {
	Installation string `json:"installation"`
	Customer     string `json:"customer"`
	Domain       string `json:"domain"`
	ClientID     string `json:"clientId,omitempty"`
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
	p := &Portal{Host: host.Name, Customer: host.Customer, Domain: hostOf(cfg.BaseURL)}
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

// portalAudiences reads the portals' Dex client ids the installation trusts
// today from its own patches, the second place a portal's id is on record:
// the union of the four lists the definition renders the portals' audiences
// into, without the ids it renders itself. Empty when the installation has
// no configs repository, or neither patch exists or names any.
func portalAudiences(ctx context.Context, c *github.Client, rep Report) ([]string, error) {
	if rep.Repositories.Configs == "" {
		return nil, nil
	}
	owner, repo, err := gh.SplitRepo(rep.Repositories.Configs)
	if err != nil {
		return nil, err
	}
	read := func(path string) (string, error) {
		data, err := gh.ReadFile(ctx, c, owner, repo, path)
		if errors.Is(err, gh.ErrNotFound) {
			return "", nil
		}
		return data, err
	}
	patch, err := read(AgentPlatformPatchPath(rep.Name))
	if err != nil {
		return nil, err
	}
	dexPatch, err := read(DexPatchPath(rep.Name))
	if err != nil {
		return nil, err
	}
	ids, err := portalAudiencesOf(patch, dexPatch, agentplatform.OwnAudiences(rep.Record.MusterClientID, rep.Federation.Hubs))
	if err != nil {
		return nil, fmt.Errorf("installations/%s/apps: %w", rep.Name, err)
	}
	return ids, nil
}

// portalAudiencesOf is the union of the four lists on record that carry the
// portals' audiences — the platform patch's
// muster.muster.oauth.server.trustedAudiences, its comma-separated
// kagent.oauth2-proxy.extraArgs.oidc-extra-audience and the edge's
// agent-platform-mcps.agentgateway.jwt.extraProviders[*].audiences, and the
// dex-app patch's oidc.staticClients.dexK8SAuthenticator.trustedPeers —
// without own, the ids the definition renders itself: each once, in the
// order first seen. Either patch may be absent (empty).
func portalAudiencesOf(patch, dexPatch string, own []string) ([]string, error) {
	var p struct {
		Muster struct {
			Muster struct {
				OAuth struct {
					Server struct {
						TrustedAudiences []string `yaml:"trustedAudiences"`
					} `yaml:"server"`
				} `yaml:"oauth"`
			} `yaml:"muster"`
		} `yaml:"muster"`
		Kagent struct {
			OAuth2Proxy struct {
				ExtraArgs struct {
					ExtraAudience any `yaml:"oidc-extra-audience"`
				} `yaml:"extraArgs"`
			} `yaml:"oauth2-proxy"`
		} `yaml:"kagent"`
		MCPs struct {
			Agentgateway struct {
				JWT struct {
					ExtraProviders []struct {
						Audiences []string `yaml:"audiences"`
					} `yaml:"extraProviders"`
				} `yaml:"jwt"`
			} `yaml:"agentgateway"`
		} `yaml:"agent-platform-mcps"`
	}
	var d struct {
		OIDC struct {
			StaticClients struct {
				DexK8SAuthenticator struct {
					TrustedPeers []string `yaml:"trustedPeers"`
				} `yaml:"dexK8SAuthenticator"`
			} `yaml:"staticClients"`
		} `yaml:"oidc"`
	}
	if err := yaml.Unmarshal([]byte(patch), &p); err != nil {
		return nil, fmt.Errorf("agent-platform/configmap-values.yaml.patch: %w", err)
	}
	if err := yaml.Unmarshal([]byte(dexPatch), &d); err != nil {
		return nil, fmt.Errorf("dex-app/configmap-values.yaml.patch: %w", err)
	}
	lists := [][]string{p.Muster.Muster.OAuth.Server.TrustedAudiences, audienceList(p.Kagent.OAuth2Proxy.ExtraArgs.ExtraAudience)}
	for _, provider := range p.MCPs.Agentgateway.JWT.ExtraProviders {
		lists = append(lists, provider.Audiences)
	}
	lists = append(lists, d.OIDC.StaticClients.DexK8SAuthenticator.TrustedPeers)
	var ids []string
	for _, list := range lists {
		for _, id := range list {
			id = strings.TrimSpace(id)
			if id != "" && !slices.Contains(own, id) && !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	return ids, nil
}

// audienceList is oidc-extra-audience as a list: the flag is a StringSlice,
// written as one comma-separated scalar (the chart's extraArgs is a map) or,
// in a hand-written patch, as a list.
func audienceList(v any) []string {
	switch v := v.(type) {
	case string:
		return strings.Split(v, ",")
	case []any:
		ids := make([]string, 0, len(v))
		for _, item := range v {
			ids = append(ids, fmt.Sprint(item))
		}
		return ids
	}
	return nil
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
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	for i, host := range hosts {
		wg.Add(1)
		go func(i int, host Installation) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			found[i], errs[i] = r.readPortal(ctx, c, host)
		}(i, host)
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
// proxies the target's agent platform; a target not among the reports has its
// record read for its base domain. A hub's broker client id is read back from
// its patch. What cannot be read is
// an error of the report: the record is then incomplete and the installation
// is not planned.
func (r *Registry) derive(ctx context.Context, c *github.Client, reports []Report, portals []Portal) {
	byName := map[string]*Report{}
	for i := range reports {
		byName[reports[i].Name] = &reports[i]
	}
	for i := range reports {
		rep := &reports[i]
		if rep.Record == nil {
			continue
		}
		targets := rep.derivePortals(portals)
		rep.Record.Private = tunnelled(portals, rep.Name)
		ids, err := portalAudiences(ctx, c, *rep)
		if err != nil {
			rep.fail(fmt.Sprintf("the portal audiences: %v", err))
		}
		rep.PortalAudiences = append(rep.PortalAudiences, ids...)
		for _, name := range targets {
			target, err := r.target(ctx, c, name, byName[name], tunnelled(portals, name), proxied(portals, rep.Name, name))
			if err != nil {
				rep.fail(fmt.Sprintf("federation target %s: %v", name, err))
				continue
			}
			rep.Federation.Targets = append(rep.Federation.Targets, target)
		}
		if len(targets) > 0 {
			id, err := brokerClientID(ctx, c, *rep)
			if err != nil {
				rep.fail(fmt.Sprintf("the broker client id: %v", err))
			}
			rep.Federation.BrokerClientID = id
		}
	}
}

// derivePortals fills the report's portals and hubs from the portals on record
// and answers the names of the installations its own muster brokers for, in
// the portals' order, each once.
func (r *Report) derivePortals(portals []Portal) []string {
	r.Portals, r.PortalAudiences, r.Federation = []PortalRef{}, []string{}, &Federation{Hubs: []string{}, Targets: []FederatedTarget{}}
	var targets []string
	for _, p := range portals {
		if slices.Contains(p.Installations, r.Name) {
			r.Portals = append(r.Portals, PortalRef{Installation: p.Host, Customer: p.Customer, Domain: p.Domain, ClientID: p.ClientID})
			if p.Broker != "" && p.Broker != r.Name && !slices.Contains(r.Federation.Hubs, p.Broker) {
				r.Federation.Hubs = append(r.Federation.Hubs, p.Broker)
			}
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

// fail records what could not be read; the report is no longer a complete record.
func (r *Report) fail(msg string) {
	r.Errors = append(r.Errors, msg)
	r.Readable = false
}

// target is a federated target's facts: the registry's base domain (the
// record's, read where the target was not inspected), whether it is reached
// through the tunnel and whether the hub's portal proxies its agent platform.
func (r *Registry) target(ctx context.Context, c *github.Client, name string, inspected *Report, private, proxied bool) (FederatedTarget, error) {
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
		if rec, err = readRecord(ctx, c, owner, repo, inst); err != nil {
			return FederatedTarget{}, err
		}
	}
	return FederatedTarget{Installation: name, BaseDomain: rec.BaseDomain, Private: private, PlatformProxied: proxied}, nil
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
