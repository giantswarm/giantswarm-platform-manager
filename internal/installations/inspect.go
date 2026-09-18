package installations

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/google/go-github/v92/github"
	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
)

// ConfigPatchPath is where the installation's configs repository keeps the
// facts on record about it.
func ConfigPatchPath(name string) string { return "installations/" + name + "/config.yaml.patch" }

// Record are the installation facts on record — the registry's and the
// installation's config.yaml.patch's — in the shape of the definitions'
// installation.* inputs: read, never typed.
type Record struct {
	Name       string `json:"name"`
	BaseDomain string `json:"baseDomain,omitempty"`
	Customer   string `json:"customer,omitempty"`
	Provider   string `json:"provider,omitempty"`
	// Private: managementCluster.private in config.yaml.patch.
	Private bool `json:"private"`
	// ChartLine is the agent-platform meta chart line: "4" when
	// agentPlatform.kagentApiV2 is set in config.yaml.patch, else "3".
	ChartLine string `json:"chartLine"`
	// MusterClientID is services.muster.clientId in config.yaml.patch, when
	// the installation sets one.
	MusterClientID string `json:"musterClientId,omitempty"`
}

// configPatch is the part of config.yaml.patch the record reads.
type configPatch struct {
	Codename          string `yaml:"codename"`
	Base              string `yaml:"base"`
	Customer          string `yaml:"customer"`
	ManagementCluster struct {
		Private bool `yaml:"private"`
	} `yaml:"managementCluster"`
	Provider struct {
		Kind string `yaml:"kind"`
	} `yaml:"provider"`
	AgentPlatform struct {
		KagentAPIV2 bool `yaml:"kagentApiV2"`
	} `yaml:"agentPlatform"`
	Services struct {
		Muster struct {
			ClientID string `yaml:"clientId"`
		} `yaml:"muster"`
	} `yaml:"services"`
}

// CapabilityState is one capability on one installation.
type CapabilityState struct {
	Name  string `json:"name"`
	State State  `json:"state"`
	// Inputs are the inputs on record for the capability: today the
	// installation facts; the person's inputs read back from the checkout
	// follow with the renderer.
	Inputs map[string]any `json:"inputs,omitempty"`
	// EnabledMarker is the file whose presence in MarkerRepository (the
	// installation's configs or management-clusters repository) means
	// enabled, and Enabled whether it is there.
	EnabledMarker    string           `json:"enabledMarker"`
	MarkerRepository MarkerRepository `json:"markerRepository"`
	Enabled          bool             `json:"enabled"`
	// LastAction is the last Action record for this capability on this
	// installation; null until the Action record exists.
	LastAction *ActionRef `json:"lastAction"`
}

// ActionRef names an Action record. Filled once the record exists.
type ActionRef struct {
	Name   string `json:"name"`
	Result string `json:"result,omitempty"`
}

// Report is everything list_installations says about one installation.
type Report struct {
	Installation
	Record       *Record           `json:"record,omitempty"`
	OptIn        *OptIn            `json:"optIn,omitempty"`
	Capabilities []CapabilityState `json:"capabilities"`
	// Readable says whether the installation's repositories could be read as
	// the caller; Errors carries what could not.
	Readable bool     `json:"readable"`
	Errors   []string `json:"errors,omitempty"`
}

// Inspect reads inst's opt-in, record and enabled markers as the person, now.
func Inspect(ctx context.Context, c *github.Client, inst Installation, caps []Capability) Report {
	r := Report{Installation: inst}
	if !inst.Repositories.Known() {
		r.Errors = append(r.Errors, "the catalog names no GitOps repositories (links of type CCR and CMC) for this installation; nothing of it can be read")
		r.Capabilities = unknownCapabilities(caps, inst.Name)
		return r
	}
	optIn := ReadOptIn(ctx, c, inst)
	r.OptIn = &optIn
	if optIn.State == OptInUnreadable {
		r.Errors = append(r.Errors, optIn.Error)
	}

	owner, repo, err := gh.SplitRepo(inst.Repositories.Configs)
	if err != nil {
		r.Errors = append(r.Errors, err.Error())
		r.Capabilities = unknownCapabilities(caps, inst.Name)
		return r
	}
	record, err := readRecord(ctx, c, owner, repo, inst)
	if err != nil {
		r.Errors = append(r.Errors, err.Error())
	} else {
		r.Record = record
	}

	r.Readable = optIn.State != OptInUnreadable && err == nil
	for _, cap := range caps {
		cs := CapabilityState{Name: cap.Name, EnabledMarker: cap.EnabledMarker(inst.Name), MarkerRepository: cap.MarkerRepository, State: StateUnknown}
		if r.Record != nil {
			cs.Inputs = map[string]any{"installation": r.Record}
		}
		if r.Readable {
			markerOwner, markerRepo, err := gh.SplitRepo(cap.Repository(inst.Repositories))
			if err != nil {
				r.Errors = append(r.Errors, err.Error())
				r.Readable = false
				r.Capabilities = append(r.Capabilities, cs)
				continue
			}
			enabled, err := exists(ctx, c, markerOwner, markerRepo, cs.EnabledMarker)
			if err != nil {
				r.Errors = append(r.Errors, err.Error())
				r.Readable = false
			} else {
				cs.Enabled = enabled
				cs.State = stateOf(optIn.State, enabled)
			}
		}
		r.Capabilities = append(r.Capabilities, cs)
	}
	return r
}

// stateOf is the state readable from the repositories alone.
func stateOf(optIn OptInState, enabled bool) State {
	switch {
	case optIn != OptedIn:
		return StateNotOptedIn
	case enabled:
		return StateEnabled
	default:
		return StateNotEnabled
	}
}

func unknownCapabilities(caps []Capability, name string) []CapabilityState {
	out := make([]CapabilityState, 0, len(caps))
	for _, cap := range caps {
		out = append(out, CapabilityState{Name: cap.Name, State: StateUnknown, EnabledMarker: cap.EnabledMarker(name), MarkerRepository: cap.MarkerRepository})
	}
	return out
}

// sharedConfigsRepository holds the fleet's shared configuration; its default
// config is what every installation's config.yaml.patch overlays.
const sharedConfigsRepository, sharedDefaultConfig = "shared-configs", "default/config.yaml"

// readRecord reads the installation's config.yaml.patch into the record, the
// platform's client id from the shared default where the patch has none.
func readRecord(ctx context.Context, c *github.Client, owner, repo string, inst Installation) (*Record, error) {
	data, err := gh.ReadFile(ctx, c, owner, repo, ConfigPatchPath(inst.Name))
	if err != nil {
		return nil, fmt.Errorf("the facts on record: %w", err)
	}
	var p configPatch
	if err := yaml.Unmarshal([]byte(data), &p); err != nil {
		return nil, fmt.Errorf("the facts on record: %s in %s: %w", ConfigPatchPath(inst.Name), inst.Repositories.Configs, err)
	}
	rec := &Record{Name: inst.Name, BaseDomain: inst.BaseDomain, Customer: inst.Customer, Provider: inst.Provider,
		Private: p.ManagementCluster.Private, ChartLine: "3", MusterClientID: p.Services.Muster.ClientID}
	if rec.MusterClientID == "" {
		// konfigure overlays the patch on the shared default: an installation
		// without its own client id runs on the fleet's.
		shared, err := gh.ReadFile(ctx, c, owner, sharedConfigsRepository, sharedDefaultConfig)
		if err != nil {
			return nil, fmt.Errorf("the facts on record: %s in %s/%s: %w", sharedDefaultConfig, owner, sharedConfigsRepository, err)
		}
		var d configPatch
		if err := yaml.Unmarshal([]byte(shared), &d); err != nil {
			return nil, fmt.Errorf("the facts on record: %s in %s/%s: %w", sharedDefaultConfig, owner, sharedConfigsRepository, err)
		}
		rec.MusterClientID = d.Services.Muster.ClientID
	}
	if p.AgentPlatform.KagentAPIV2 {
		rec.ChartLine = "4"
	}
	if rec.BaseDomain == "" && p.Codename != "" && p.Base != "" {
		rec.BaseDomain = p.Codename + "." + p.Base
	}
	if rec.Customer == "" {
		rec.Customer = p.Customer
	}
	if rec.Provider == "" {
		rec.Provider = p.Provider.Kind
	}
	return rec, nil
}

// exists says whether path is a file in owner/repo as the person.
func exists(ctx context.Context, c *github.Client, owner, repo, path string) (bool, error) {
	_, err := gh.ReadFile(ctx, c, owner, repo, path)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, gh.ErrNotFound):
		return false, nil
	default:
		return false, err
	}
}

// maxParallel bounds the reads in flight across installations.
const maxParallel = 8

// InspectAll inspects every installation, at most maxParallel at a time, and
// returns the reports in the registry's order.
func InspectAll(ctx context.Context, c *github.Client, insts []Installation, caps []Capability) []Report {
	reports := make([]Report, len(insts))
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	for i, inst := range insts {
		wg.Add(1)
		go func(i int, inst Installation) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			reports[i] = Inspect(ctx, c, inst, caps)
		}(i, inst)
	}
	wg.Wait()
	return reports
}

// Input is the record in the shape of the definitions' installation input:
// every key present, so the schema sees the facts on record and the person
// types only what is not there.
func (r *Record) Input() map[string]any {
	return map[string]any{"name": r.Name, "baseDomain": r.BaseDomain, "customer": r.Customer, "provider": r.Provider,
		"private": r.Private, "chartLine": r.ChartLine, "musterClientId": r.MusterClientID}
}

// Facts are every installation fact on record, as a definition's inputs name
// them under installation: the record's (config.yaml.patch), the registry's
// region and pipeline, and per capability whether it is enabled here
// (agentPlatform, customerPortal). A definition takes the ones its schema
// names (Capability.Facts).
func (r Report) Facts() map[string]any {
	facts := map[string]any{}
	if r.Record != nil {
		facts = r.Record.Input()
	}
	facts["region"], facts["pipeline"] = r.Region, r.Pipeline
	for _, cs := range r.Capabilities {
		facts[factKey(cs.Name)] = cs.Enabled
	}
	return facts
}

// factKey is the fact a capability's enabled state is named by: the
// definition's name in lowerCamelCase (agent-platform: agentPlatform).
func factKey(capability string) string {
	parts := strings.Split(capability, "-")
	for i := 1; i < len(parts); i++ {
		if parts[i] != "" {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}
