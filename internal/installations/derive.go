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
)

// The facts a definition derives from the portals' app-configs — which portals
// sign people in on an installation, whose broker exchanges tokens into it,
// and which installations its own muster brokers for — are read here, once per
// inspection, and land on the Report as installation facts (Facts): a
// definition names them under installation in its schema and never asks a
// person for them.

// Portal is a developer portal on record: the installation hosting it and its
// organisation, its hostname, the installations it lists and the installation
// whose muster brokers its cluster tokens (empty when it brokers none).
type Portal struct {
	Host          string
	Customer      string
	Domain        string
	Broker        string
	Installations []string
}

// PortalRef is a portal that signs people in on an installation, as the
// definitions' installation.portals[*] names it.
type PortalRef struct {
	Installation string `json:"installation"`
	Customer     string `json:"customer"`
	Domain       string `json:"domain"`
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
}

// AgentPlatformPatchPath is where the installation's configs repository keeps
// the platform's values patch — the agent-platform capability's marker, and
// where a hub's broker client id is read back from.
func AgentPlatformPatchPath(name string) string {
	return "installations/" + name + "/apps/agent-platform/configmap-values.yaml.patch"
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
	return p, nil
}

// hostOf is the host of a URL, empty for none.
func hostOf(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
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
// record. A target's private flag is its record's; a target not among the
// reports has its record read. A hub's broker client id is read back from its
// patch. What cannot be read is an error of the report: the record is then
// incomplete and the installation is not planned.
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
		for _, name := range targets {
			target, err := r.target(ctx, c, name, byName[name])
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
	r.Portals, r.Federation = []PortalRef{}, &Federation{Hubs: []string{}, Targets: []FederatedTarget{}}
	var targets []string
	for _, p := range portals {
		if slices.Contains(p.Installations, r.Name) {
			r.Portals = append(r.Portals, PortalRef{Installation: p.Host, Customer: p.Customer, Domain: p.Domain})
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

// target is a federated target's facts: the registry's base domain and the
// record's private flag, read where the target was not inspected.
func (r *Registry) target(ctx context.Context, c *github.Client, name string, inspected *Report) (FederatedTarget, error) {
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
	return FederatedTarget{Installation: name, BaseDomain: rec.BaseDomain, Private: rec.Private}, nil
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
