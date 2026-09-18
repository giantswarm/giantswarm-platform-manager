package installations

import (
	"encoding/json"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/render"
	"github.com/giantswarm/giantswarm-platform-manager/render/agentplatform"
)

// State is a capability's state on an installation. The first three are read
// from the repositories now; the last five come from the Action record and
// the last verify, once those exist — the model carries them so they plug in.
type State string

// The states, in the order an enablement moves through them.
const (
	// StateNotOptedIn: the installation carries no opt-in declaration (or
	// optIn: false); the manager may not act on it.
	StateNotOptedIn State = "not opted in"
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
// definition's name, what it enables, the marker whose presence in the
// installation's configs repository means the capability is enabled there —
// the first file of its fileset — and the render of its definition. Its data
// (the input schema, the features, the probes, the removals, the policy) is
// the embedded definitions/<name>/ directory.
type Capability struct {
	Name string
	// Description is what the capability enables, in one sentence.
	Description string
	// EnabledMarker is the path of the marker for installation name.
	EnabledMarker func(installation string) string
	// Render is the definition's render: the decoded input document and the
	// person-supplied secret values by field in, the fileset out.
	Render func(raw any, secrets map[string]string) (*render.Result, error)
}

// Schema is the definition's input schema, as embedded.
func (c Capability) Schema() (json.RawMessage, error) {
	raw, err := definitions.FS.ReadFile(c.Name + "/schema.json")
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}

// AgentPlatform is the agent-platform capability: enabled once the
// installation's configs repository carries the agent-platform values patch.
const AgentPlatform = "agent-platform"

// Capabilities is the registry of the capability definitions the manager
// knows — the one list get_info, list_installations, the write tools, the
// verify and platformctl template read. A definition registers here and
// under definitions/<name>/, nowhere else.
func Capabilities() []Capability {
	return []Capability{{
		Name:        AgentPlatform,
		Description: "The agent platform on an installation: muster with its MCP servers, kagent and its agents, the Dex clients, the developer portal's section, the hub's federation, klaus-gateway and cluster-manager.",
		EnabledMarker: func(installation string) string {
			return "installations/" + installation + "/apps/agent-platform/configmap-values.yaml.patch"
		},
		Render: agentplatform.Render,
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

// FromAction says whether s is a state the Action record produces — one an
// unfinished or failed action lets stand over the state read from the files.
func (s State) FromAction() bool {
	switch s {
	case StatePendingApproval, StateRollingOut, StateWaitingForCustomer, StateDrifted, StateFailed:
		return true
	}
	return false
}
