package installations

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

// Capability is a platform capability the manager knows about, with the
// marker whose presence in the installation's configs repository means the
// capability is enabled there — the first file of its fileset.
type Capability struct {
	Name string
	// EnabledMarker is the path of the marker for installation name.
	EnabledMarker func(installation string) string
}

// AgentPlatform is the agent-platform capability: enabled once the
// installation's configs repository carries the agent-platform values patch.
const AgentPlatform = "agent-platform"

// Capabilities are the capabilities list_installations answers for.
func Capabilities() []Capability {
	return []Capability{{
		Name: AgentPlatform,
		EnabledMarker: func(installation string) string {
			return "installations/" + installation + "/apps/agent-platform/configmap-values.yaml.patch"
		},
	}}
}
