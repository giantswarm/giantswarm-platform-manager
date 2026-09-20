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
	// Private: reached through Teleport — a portal on record reaches the
	// installation's Kubernetes API through the tunnel on its host
	// (kubernetes-<name>.agent-platform.svc.cluster.local); set by derive.
	Private bool `json:"private"`
	// ChartLine is the agent-platform meta chart line: "4" when
	// agentPlatform.kagentApiV2 is set in config.yaml.patch, else "3".
	ChartLine string `json:"chartLine"`
	// MusterClientID is services.muster.clientId in config.yaml.patch, when
	// the installation sets one.
	MusterClientID string `json:"musterClientId,omitempty"`
	// PodCertificateRequest says the cluster serves certificates.k8s.io/v1beta1
	// PodCertificateRequest — what kagent's Agent Substrate needs on the 4
	// chart line: the cluster App on record (cluster-app-manifests.yaml in the
	// management-clusters repository) enables the feature gates, or its chart
	// does by default; read by readPodCertificateRequest.
	PodCertificateRequest bool `json:"podCertificateRequest"`
	// DexAppVersion is the dex-app the installation runs — spec.version of
	// the App dex-app: the installation's own pin in its collections
	// kustomization, else the fleet's shared base at the ref the
	// kustomization names (readDexAppVersion); empty when neither says.
	// DexAppSource is the file it was read from, repository:path.
	DexAppVersion string `json:"dexAppVersion,omitempty"`
	DexAppSource  string `json:"dexAppSource,omitempty"`
}

// configPatch is the part of config.yaml.patch the record reads.
type configPatch struct {
	Codename string `yaml:"codename"`
	Base     string `yaml:"base"`
	Customer string `yaml:"customer"`
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
	// Portals and Federation are the facts derived from the portals on record
	// (derive.go); nil until a registry inspection filled them.
	Portals []PortalRef `json:"portals,omitempty"`
	// PortalAudiences are the portals' Dex client ids the installation trusts
	// today, read from its own platform patch (derive.go).
	PortalAudiences []string    `json:"portalAudiences,omitempty"`
	Federation      *Federation `json:"federation,omitempty"`
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
		if record.PodCertificateRequest, err = readPodCertificateRequest(ctx, c, inst); err != nil {
			r.Errors = append(r.Errors, err.Error())
		}
		// The dex-app on record is a fact, not a condition of reading the
		// installation: unreadable, the fact stays empty and the comparison
		// still runs, with the error on the report.
		if v, err := readDexAppVersion(ctx, readAt(c), inst); err != nil {
			r.Errors = append(r.Errors, err.Error())
		} else if v.Version != "" {
			record.DexAppVersion, record.DexAppSource = v.Version, v.Source()
		}
	}

	r.Readable = optIn.State != OptInUnreadable && err == nil
	for _, cap := range caps {
		cs := CapabilityState{Name: cap.Name, EnabledMarker: cap.EnabledMarker(inst.Name), MarkerRepository: cap.MarkerRepository, State: StateUnknown}
		if r.Record != nil {
			cs.Inputs = map[string]any{InputsInstallation: r.Record}
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
		ChartLine: "3", MusterClientID: p.Services.Muster.ClientID}
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
// returns the reports in the registry's order, each with the facts the
// portals on record derive for it. A portal that cannot be read as the person
// is an error of every report: no record is complete without it.
func (r *Registry) InspectAll(ctx context.Context, c *github.Client, insts []Installation, caps []Capability) []Report {
	reports := inspectAll(ctx, c, insts, caps)
	portals, failed := r.Portals(ctx, c, insts)
	for i := range reports {
		for _, key := range []string{"", reports[i].Customer} {
			if err := failed[key]; err != nil {
				reports[i].fail(fmt.Sprintf("the portals on record: %v", err))
			}
		}
	}
	r.derive(ctx, c, reports, portals)
	return reports
}

func inspectAll(ctx context.Context, c *github.Client, insts []Installation, caps []Capability) []Report {
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
	in := map[string]any{"name": r.Name, "baseDomain": r.BaseDomain, "customer": r.Customer, "provider": r.Provider,
		"private": r.Private, "chartLine": r.ChartLine, "musterClientId": r.MusterClientID, "podCertificateRequest": r.PodCertificateRequest}
	if r.DexAppVersion != "" {
		// Optional in the schema: absent where the record says nothing.
		in["dexAppVersion"] = r.DexAppVersion
	}
	return in
}

// Facts are every installation fact on record, as a definition's inputs name
// them under installation: the record's (config.yaml.patch), the registry's
// region, pipeline and whether this is the hub, and per capability whether it
// is enabled here (agentPlatform, customerPortal). A definition takes the ones
// its schema names (Capability.Facts).
func (r Report) Facts() map[string]any {
	facts := map[string]any{}
	if r.Record != nil {
		facts = r.Record.Input()
	}
	facts["region"], facts["pipeline"], facts["hub"] = r.Region, r.Pipeline, r.Hub
	if r.Portals != nil {
		facts["portals"] = r.Portals
	}
	if r.PortalAudiences != nil {
		facts["portalAudiences"] = r.PortalAudiences
	}
	if r.Federation != nil {
		facts["federation"] = r.Federation
	}
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
