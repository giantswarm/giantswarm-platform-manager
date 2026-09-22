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
	// DexSecretLists are the lists the installation's encrypted dex-app
	// secret patch carries that the values merge takes whole over the
	// plaintext patch's — the hand-registered extra static clients, the
	// authenticator's trusted peers (readDexSecretLists); none where the
	// patch carries none or does not exist. DexSecretSource is the file,
	// repository:path. A commit whose Dex patch renders one of those lists
	// is held while the encrypted patch carries it.
	DexSecretLists  []DexSecretList `json:"dexSecretLists,omitempty"`
	DexSecretSource string          `json:"dexSecretSource,omitempty"`
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
	// enabled, and Enabled whether it is there: the capability's fileset is
	// on record, whoever put it there.
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
	Capabilities []CapabilityState `json:"capabilities"`
	// Readable says whether the installation's repositories could be read as
	// the caller; Errors carries what could not.
	Readable bool     `json:"readable"`
	Errors   []string `json:"errors,omitempty"`
	// Portals and Federation are the facts derived from the portals on record
	// (derive.go); nil until a registry inspection filled them.
	Portals    []PortalRef `json:"portals,omitempty"`
	Federation *Federation `json:"federation,omitempty"`
	// Hosted is the portal hosted on the installation with the installations
	// it shows besides its own (derive.go); nil where it hosts none.
	Hosted *HostedPortal `json:"hosted,omitempty"`
}

// Detail is how much of an installation list_installations reads.
type Detail int

const (
	// Full reads everything a plan needs: the record with the cluster App,
	// the markers, the portals and the federation facts.
	Full Detail = iota
	// Summary reads the markers alone: the state per capability for an
	// overview, without the record, the portals or the federation facts.
	Summary
)

// inspect reads inst's record and enabled markers as the person, now, every
// read at once; the client bounds what is in flight.
func (r *Registry) inspect(ctx context.Context, c *github.Client, inst Installation, caps []Capability, detail Detail) Report {
	rep := Report{Installation: inst}
	if !inst.Repositories.Known() {
		rep.Errors = append(rep.Errors, "the catalog names no GitOps repositories (links of type CCR and CMC) for this installation; nothing of it can be read")
		rep.Capabilities = unknownCapabilities(caps, inst.Name)
		return rep
	}
	owner, repo, err := gh.SplitRepo(inst.Repositories.Configs)
	if err != nil {
		rep.Errors = append(rep.Errors, err.Error())
		rep.Capabilities = unknownCapabilities(caps, inst.Name)
		return rep
	}

	var (
		wg      sync.WaitGroup
		record  *Record
		recErr  error
		pcr     bool
		pcrErr  error
		dex     DexAppVersion
		dexErr  error
		lists   []DexSecretList
		listErr error
		markers = make([]markerRead, len(caps))
	)
	if detail == Full {
		wg.Go(func() { record, recErr = r.readRecord(ctx, c, owner, repo, inst) })
		wg.Go(func() { pcr, pcrErr = readPodCertificateRequest(ctx, readAs(c), inst) })
		wg.Go(func() { dex, dexErr = readDexAppVersion(ctx, readAt(c), inst) })
		wg.Go(func() { lists, listErr = readDexSecretLists(ctx, readAs(c), inst) })
	}
	for i, cap := range caps {
		wg.Go(func() { markers[i] = readMarker(ctx, c, inst, cap) })
	}
	wg.Wait()

	// Readable until a read as the person fails: the record, or a marker.
	rep.Readable = true
	if detail == Full {
		switch {
		case recErr != nil:
			rep.Errors = append(rep.Errors, recErr.Error())
			rep.Readable = false
		case pcrErr != nil && !errors.Is(pcrErr, ErrRelease):
			rep.Record = record
			rep.Errors = append(rep.Errors, pcrErr.Error())
			rep.Readable = false
		default:
			record.PodCertificateRequest = pcr
			rep.Record = record
			if pcrErr != nil {
				// The release the cluster App names is a fact of the record, not
				// a condition of reading the installation: unreadable, the fact
				// stays false and the comparison still runs, with the error on
				// the report.
				rep.Errors = append(rep.Errors, pcrErr.Error())
			}
		}
		// The dex-app on record is a fact, not a condition of reading the
		// installation: unreadable, the fact stays empty and the comparison
		// still runs, with the error on the report.
		if record != nil && recErr == nil {
			if dexErr != nil {
				rep.Errors = append(rep.Errors, dexErr.Error())
			} else if dex.Version != "" {
				record.DexAppVersion, record.DexAppSource = dex.Version, dex.Source()
			}
			// So are the lists the encrypted Dex values carry.
			if listErr != nil {
				rep.Errors = append(rep.Errors, listErr.Error())
			} else if len(lists) > 0 {
				record.DexSecretLists, record.DexSecretSource = lists, inst.Repositories.Configs+":"+DexSecretPatchPath(inst.Name)
			}
		}
	}
	for i, cap := range caps {
		cs := CapabilityState{Name: cap.Name, EnabledMarker: cap.EnabledMarker(inst.Name), MarkerRepository: cap.MarkerRepository, State: StateUnknown}
		if rep.Record != nil {
			cs.Inputs = map[string]any{InputsInstallation: rep.Record}
		}
		// A marker only counts where the record could be read: an installation
		// unreadable as the person has no state.
		if rep.Readable {
			if m := markers[i]; m.err != nil {
				rep.Errors = append(rep.Errors, m.err.Error())
				rep.Readable = false
			} else {
				cs.Enabled = m.enabled
				cs.State = stateOf(cs.Enabled)
			}
		}
		rep.Capabilities = append(rep.Capabilities, cs)
	}
	return rep
}

// markerRead is whether a capability's marker is in the installation's
// repository, or why that could not be read.
type markerRead struct {
	enabled bool
	err     error
}

func readMarker(ctx context.Context, c *github.Client, inst Installation, cap Capability) markerRead {
	owner, repo, err := gh.SplitRepo(cap.Repository(inst.Repositories))
	if err != nil {
		return markerRead{err: err}
	}
	enabled, err := exists(ctx, c, owner, repo, cap.EnabledMarker(inst.Name))
	return markerRead{enabled: enabled, err: err}
}

// stateOf is the state readable from the repositories alone: whether the
// capability's fileset is on record, whoever put it there.
func stateOf(enabled bool) State {
	if enabled {
		return StateEnabled
	}
	return StateNotEnabled
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
func (r *Registry) readRecord(ctx context.Context, c *github.Client, owner, repo string, inst Installation) (*Record, error) {
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
		if rec.MusterClientID, err = r.shared.clientID(ctx, c, owner); err != nil {
			return nil, err
		}
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

// sharedDefaults reads an owner's shared default config once per call: every
// installation of the owner without its own client id asks for the same file.
type sharedDefaults struct {
	mu      sync.Mutex
	byOwner map[string]*sharedDefault
}

type sharedDefault struct {
	once     sync.Once
	clientID string
	err      error
}

// clientID is services.muster.clientId of owner's shared default config.
func (s *sharedDefaults) clientID(ctx context.Context, c *github.Client, owner string) (string, error) {
	s.mu.Lock()
	if s.byOwner == nil {
		s.byOwner = map[string]*sharedDefault{}
	}
	d := s.byOwner[owner]
	if d == nil {
		d = &sharedDefault{}
		s.byOwner[owner] = d
	}
	s.mu.Unlock()
	d.once.Do(func() {
		shared, err := gh.ReadFile(ctx, c, owner, sharedConfigsRepository, sharedDefaultConfig)
		if err != nil {
			d.err = fmt.Errorf("the facts on record: %s in %s/%s: %w", sharedDefaultConfig, owner, sharedConfigsRepository, err)
			return
		}
		var p configPatch
		if err := yaml.Unmarshal([]byte(shared), &p); err != nil {
			d.err = fmt.Errorf("the facts on record: %s in %s/%s: %w", sharedDefaultConfig, owner, sharedConfigsRepository, err)
			return
		}
		d.clientID = p.Services.Muster.ClientID
	})
	return d.clientID, d.err
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

// InspectAll inspects every installation at once — the client bounds the
// reads in flight — and returns the reports in the registry's order. With
// Full, each carries the facts the portals on record derive for it; a portal
// that cannot be read as the person is an error of every report, since no
// record is complete without it. With Summary the reports stop at the states.
func (r *Registry) InspectAll(ctx context.Context, c *github.Client, insts []Installation, caps []Capability, detail Detail) []Report {
	reports := make([]Report, len(insts))
	var wg sync.WaitGroup
	for i, inst := range insts {
		wg.Go(func() { reports[i] = r.inspect(ctx, c, inst, caps, detail) })
	}
	wg.Wait()
	if detail == Summary {
		return reports
	}
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
