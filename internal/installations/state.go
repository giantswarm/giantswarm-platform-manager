package installations

import (
	"encoding/json"
	"fmt"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/render"
	"github.com/giantswarm/giantswarm-platform-manager/render/agentplatform"
	"github.com/giantswarm/giantswarm-platform-manager/render/customerportal"
)

// State is a capability's state on an installation. The first four are read
// from the repositories now (stateOf): the fileset on record and the owners'
// opt-in, two facts in one word; the last five come from the Action record
// and the last verify, once those exist — the model carries them so they plug in.
type State string

// The states, in the order an enablement moves through them.
const (
	// StateNotOptedIn: the installation carries no opt-in declaration (or
	// optIn: false) and the capability's fileset is not on record; the
	// manager may not act on it.
	StateNotOptedIn State = "not opted in"
	// StateEnabledNotOptedIn: the capability's fileset is on record — the
	// installation's owners enabled it themselves — and the installation
	// carries no opt-in declaration (or optIn: false): installed, and the
	// manager may not write to it until the owners opt in.
	StateEnabledNotOptedIn State = "enabled, not opted in"
	// StateNotEnabled: opted in, and the capability's fileset is absent.
	StateNotEnabled State = "not enabled"
	// StatePendingApproval: an enablement asked for approval and waits.
	StatePendingApproval State = "pending approval"
	// StateRollingOut: the pull requests are merged and the rollout runs.
	StateRollingOut State = "rolling out"
	// StateWaitingForCustomer: the rollout needs an action of the customer.
	StateWaitingForCustomer State = "waiting for the customer"
	// StateEnabled: the capability's fileset is on record.
	StateEnabled State = "enabled"
	// StateDrifted: the last verify found the installation off its definition.
	StateDrifted State = "drifted"
	// StateFailed: the last action or verify failed.
	StateFailed State = "failed"
	// StateUnknown: the installation's repositories could not be read as the
	// caller; none of the states above can be claimed.
	StateUnknown State = "unknown"
)

// Capability is a platform capability the manager knows about: the
// definition's name, what it enables, the marker whose presence in one of the
// installation's repositories means the capability is enabled there — the
// first file of its fileset — and the render of its definition. Its data
// (the input schema, the features, the probes, the removals, the policy) is
// the embedded definitions/<name>/ directory.
type Capability struct {
	Name string
	// Description is what the capability enables, in one sentence.
	Description string
	// MarkerRepository is the repository the marker is read from.
	MarkerRepository MarkerRepository
	// EnabledMarker is the path of the marker for installation name.
	EnabledMarker func(installation string) string
	// Parse is the definition's read of the decoded input document: the
	// typed input, or the definition's refusal naming the location.
	Parse func(raw any) (render.Input, error)
	// Render is the definition's render: the decoded input document, the
	// person-supplied secret values by field and the mode in, the fileset
	// out. A commit refuses a required person input the document lacks; a
	// comparison renders its Missing marker.
	Render func(raw any, secrets map[string]string, mode render.Mode) (*render.Result, error)
	// Prunes says whether the Flux Kustomization that applies the
	// definition's tree deletes what leaves the record. The fleet's
	// Kustomization over the extras tree does not (prune: false): a revert
	// of the fileset leaves the objects the tree created on the
	// installation, and a removed action names them for a person to delete.
	Prunes bool
}

// Schema is the definition's input schema, as embedded.
func (c Capability) Schema() (json.RawMessage, error) {
	raw, err := definitions.FS.ReadFile(c.Name + "/schema.json")
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}

// Facts picks from all — every fact on record (Report.Facts) — the ones the
// definition's schema names under installation. The schema is the contract:
// a fact it does not name is not an input of this definition, and one it
// requires and the record lacks is the definition's refusal to name.
func (c Capability) Facts(all map[string]any) (map[string]any, error) {
	raw, err := c.Schema()
	if err != nil {
		return nil, err
	}
	var schema struct {
		Properties struct {
			Installation struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"installation"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, fmt.Errorf("%s: schema: %w", c.Name, err)
	}
	out := make(map[string]any, len(schema.Properties.Installation.Properties))
	for k := range schema.Properties.Installation.Properties {
		if v, ok := all[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}

// MarkerRepository names one of an installation's two GitOps repositories.
type MarkerRepository string

const (
	// ConfigsRepository is the <customer>-configs repository.
	ConfigsRepository MarkerRepository = "configs"
	// ManagementClustersRepository is the <customer>-management-clusters repository.
	ManagementClustersRepository MarkerRepository = "management-clusters"
)

// The capabilities.
const (
	// AgentPlatform is the agent-platform capability: enabled once the
	// installation's configs repository carries the agent-platform values patch.
	AgentPlatform = "agent-platform"
	// CustomerPortal is the customer-portal capability: enabled once the
	// installation's management-clusters repository carries the portal's
	// app-config.
	CustomerPortal = "customer-portal"
)

// Capabilities is the registry of the capability definitions the manager
// knows — the one list get_info, list_installations, the write tools, the
// verify and platformctl template read. A definition registers here and
// under definitions/<name>/, nowhere else.
func Capabilities() []Capability {
	return []Capability{{
		Name:             AgentPlatform,
		Description:      "The agent platform: muster with its MCP servers, kagent, the Dex clients, the portal's section, the hub's federation, the chat gateway and cluster-manager.",
		MarkerRepository: ConfigsRepository,
		EnabledMarker: func(installation string) string {
			return "installations/" + installation + "/apps/agent-platform/configmap-values.yaml.patch"
		},
		Parse:  func(raw any) (render.Input, error) { return agentplatform.Parse(raw) },
		Render: agentplatform.Render,
		// The extras tree, under the fleet's non-pruning Kustomization.
		Prunes: false,
	}, {
		Name:             CustomerPortal,
		Description:      "The developer portal: its Backstage tree, its Dex client, the plugin keys and the session secret.",
		MarkerRepository: ManagementClustersRepository,
		EnabledMarker:    PortalConfigPath,
		Parse:            func(raw any) (render.Input, error) { return customerportal.Parse(raw) },
		Render:           customerportal.Render,
		// The extras/backstage tree, under the same Kustomization.
		Prunes: false,
	}}
}

// CapabilityNames are the registry's names, in registry order.
func CapabilityNames() []string {
	caps := Capabilities()
	names := make([]string, 0, len(caps))
	for _, c := range caps {
		names = append(names, c.Name)
	}
	return names
}

// FindCapability answers the registered capability of that name.
func FindCapability(name string) (Capability, bool) {
	for _, c := range Capabilities() {
		if c.Name == name {
			return c, true
		}
	}
	return Capability{}, false
}

// Repository is the installation's repository the capability's marker is read from.
func (c Capability) Repository(repos Repositories) string {
	if c.MarkerRepository == ManagementClustersRepository {
		return repos.ManagementClusters
	}
	return repos.Configs
}

// OnRecord says whether s is a repository state with the capability's fileset
// on record: enabled, with the owners' opt-in or without it.
func (s State) OnRecord() bool { return s == StateEnabled || s == StateEnabledNotOptedIn }

// FromAction says whether s is a state the Action record produces — one an
// unfinished or failed action lets stand over the state read from the files.
func (s State) FromAction() bool {
	switch s {
	case StatePendingApproval, StateRollingOut, StateWaitingForCustomer, StateDrifted, StateFailed:
		return true
	}
	return false
}
