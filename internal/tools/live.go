package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/identity"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/live"
	"github.com/giantswarm/giantswarm-platform-manager/internal/verify"
)

// ToolVerifyInstallation is the live verify: the definition's probes of the
// running installation, read as the person.
const ToolVerifyInstallation = "verify_installation"

// LiveInfo is the live tools as get_info reports them.
type LiveInfo struct {
	// Configured says whether the live tools are served; without them the
	// live dimensions of every verify read not checked.
	Configured bool `json:"configured"`
	// Tool is the verify; Tools every live tool.
	Tool  string   `json:"tool"`
	Tools []string `json:"tools"`
	// Identity is how the person's identity reaches the live tools, and
	// whether this call carried it.
	Identity IdentityInfo `json:"identity"`
	live.Info
}

// IdentityInfo is the identity forwarding: the header muster sets on every
// call of a registration with auth.forwardIdentity, and what this call's
// header said.
type IdentityInfo struct {
	Header string `json:"header"`
	Muster string `json:"muster"`
	// Forwarded says whether this call carried the header; Person is who it
	// names once verified, Refused why it was not accepted.
	Forwarded bool               `json:"forwarded"`
	Person    *identity.Identity `json:"person,omitempty"`
	Refused   string             `json:"refused,omitempty"`
}

func (t *Tools) liveInfo(ctx context.Context) LiveInfo {
	info := LiveInfo{Configured: t.d.Live != nil, Tool: ToolVerifyInstallation, Tools: []string{ToolVerifyInstallation, ToolWatchAction},
		Identity: IdentityInfo{Header: identity.ForwardedIdentityHeader, Muster: identity.ForwardIdentity}}
	if t.d.Live == nil {
		return info
	}
	info.Info = t.d.Live.Info()
	if token, ok := identity.ForwardedTokenFromContext(ctx); ok {
		info.Identity.Forwarded = true
		if who, err := t.d.Live.Verify(ctx, token); err != nil {
			info.Identity.Refused = err.Error()
		} else {
			info.Identity.Person = who
		}
	}
	return info
}

// forwarded is the person a live tool reads as: the ID token muster
// forwarded next to the bearer, verified — issuer, audiences, key set — and
// who it names. The GitHub bearer is not it: the reads through muster and
// the installations run with the platform identity. A call without the
// header, or with one the check refuses, is refused by name.
func (t *Tools) forwarded(ctx context.Context, tool string) (string, *identity.Identity, error) {
	if t.d.Live == nil {
		return "", nil, fmt.Errorf("%s: the live tools are not configured (live.enabled); get_info reports live.configured", tool)
	}
	how := fmt.Sprintf("muster sets it on every call of a registration with %s (the MCPServer's spec.%s: true)", identity.ForwardIdentity, identity.ForwardIdentity)
	token, ok := identity.ForwardedTokenFromContext(ctx)
	if !ok {
		return "", nil, fmt.Errorf("%s reads as you and needs your platform identity: the call carried no %s header — %s", tool, identity.ForwardedIdentityHeader, how)
	}
	who, err := t.d.Live.Verify(ctx, token)
	if err != nil {
		t.d.Log.Warn("forwarded identity refused", identity.LogAttr(ctx), "tool", tool, "error", err)
		return "", nil, fmt.Errorf("%s reads as you and needs your platform identity: the %s header was not accepted: %v — %s", tool, identity.ForwardedIdentityHeader, err, how)
	}
	return token, who, nil
}

// registerLiveTools adds verify_installation and watch_action, the tools
// that read an installation as the person, with live.enabled.
func (t *Tools) registerLiveTools(s *mcpserver.MCPServer) {
	if t.d.Live == nil {
		return
	}
	t.registerWatchTool(s)
	s.AddTool(mcp.NewTool(ToolVerifyInstallation,
		mcp.WithDescription("Verify the running installation against a capability's definition, as you: the definition's probes — HelmReleases Ready, workloads Available, the Secrets and MCPServer objects present, conditions, logs, the live values against the render from the inputs on record — read through muster's kubernetes tools with the platform ID token muster forwards for you (auth.forwardIdentity), so what you may read decides what is checked: an object you may not read is reported as not checked, forbidden for you, never as a failure of the installation; an installation you are not connected to in muster answers with muster's own sign-in. Grouped into the definition's features with one mark each — as defined, differs by input, drifted — and expanded to its dimensions; the repository dimensions read not checked here (verify_capability). The result is recorded on the newest action of the installation and feeds list_installations: drifted, or waiting for the customer when the only red dimension is the one the customer's action holds up."),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithString(ArgInstallation, mcp.Required(), mcp.Description("The installation to verify, by name: the management cluster muster reads for you.")),
		mcp.WithString(ArgCapability, mcp.Description(capabilityArgDescription), mcp.Enum(installations.CapabilityNames()...)),
		mcp.WithObject(ArgInputs, mcp.Description("The inputs object of "+ToolVerifyCapability+"'s answer (source, values, readBack), so both halves render from the same inputs; left out, the newest action's inputs on record; without either the live dimensions read not checked.")),
	), t.verifyInstallationLive)
}

func (t *Tools) verifyInstallationLive(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return result(t.verifyLive(ctx, req.GetArguments()))
}

// verifyLive is verify_installation: the inputs the caller hands over
// (verify_capability's, so both halves render the same), else the newest
// action's on record (the manager's own read, no GitHub); the reads through
// muster as the person; the result recorded on that action.
func (t *Tools) verifyLive(ctx context.Context, args map[string]any) (any, error) {
	token, id, err := t.forwarded(ctx, ToolVerifyInstallation)
	if err != nil {
		return nil, err
	}
	def, err := capabilityArg(args)
	if err != nil {
		return nil, err
	}
	name, _ := args[ArgInstallation].(string)
	if name == "" {
		return nil, fmt.Errorf("%s needs %s", ToolVerifyInstallation, ArgInstallation)
	}
	if t.d.Actions == nil {
		return nil, fmt.Errorf("%s: the inputs on record are the Action records, and the manager runs without them (no in-cluster ServiceAccount); get_info reports actions.configured", ToolVerifyInstallation)
	}
	acts, err := t.d.Actions.List(ctx, actions.Filter{Installation: name, Capability: def.Name})
	if err != nil {
		return nil, err
	}
	opts := verify.LiveOptions{Definition: def, Installation: name, Inputs: verify.Inputs{Source: verify.SourceNone}, Probes: t.d.Probes, Person: id.String()}
	given, err := givenInputs(args)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ToolVerifyInstallation, err)
	}
	if given != nil {
		opts.Inputs = *given
	}
	var record *actions.Action
	for i := range acts {
		if in := acts[i].InputsOnRecord(name); in != nil {
			record = &acts[i]
			if given == nil {
				opts.Inputs = verify.Inputs{Source: "action " + record.Name, Values: in, Typed: record.Spec.Inputs}
			}
			// The state the result starts from is the action's final word —
			// not the state a previous verify recorded over it, and not the
			// customer's wait, which a clean read ends.
			opts.State = settledState(record.InstallationState(name))
			if record.Status.Result != nil {
				opts.State = settledState(record.Status.Result.State)
			}
			break
		}
	}
	if opts.Inputs.Values != nil {
		if opts.Cluster, err = t.d.Live.Cluster(ctx, token, id, name); err != nil {
			return nil, err
		}
	}
	out := verify.CompareLive(ctx, opts)
	out.Caller = id.String()
	if record != nil {
		if err := t.recordLiveVerify(ctx, *record, name, out); err != nil {
			t.d.Log.Warn("live verify not recorded", identity.LogAttr(ctx), "action", record.Name, "error", err)
		}
	}
	t.d.Log.Info(ToolVerifyInstallation, identity.LogAttr(ctx), "installation", name, "inputs", opts.Inputs.Source, "state", out.State, "summary", out.Summary)
	return &out, nil
}

// givenInputs are the inputs the caller handed over: verify_capability's
// inputs object (source, values, readBack), decoded; nil when none.
func givenInputs(args map[string]any) (*verify.Inputs, error) {
	raw, ok := args[ArgInputs].(map[string]any)
	if !ok {
		return nil, nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var in verify.Inputs
	if err := json.Unmarshal(b, &in); err != nil {
		return nil, fmt.Errorf("%s is not the inputs object of a %s answer: %w", ArgInputs, ToolVerifyCapability, err)
	}
	if in.Values == nil {
		return nil, fmt.Errorf("%s carries no values: pass the inputs object of a %s answer", ArgInputs, ToolVerifyCapability)
	}
	return &in, nil
}

// recordLiveVerify writes the live result onto the action it rendered from:
// one probe entry per live dimension of this installation, and — for a stage
// that has reached a state — the installation's state: drifted, waiting for
// the customer, or enabled again when the installation is back as defined
// (the customer's action done flips a stage waiting for the customer to
// enabled, and the action with it). list_installations reads it from there.
// A stage rolling out is the watch's to carry; a failed one is over.
func (t *Tools) recordLiveVerify(ctx context.Context, a actions.Action, installation string, res verify.Result) error {
	status := a.Status
	status.Probes = mergeProbes(status.Probes, installation, probesOf(installation, res))
	status.Rollout = stagesOf(&a)
	if i := stageIndex(status.Rollout, installation); i >= 0 && status.Result != nil && verifyMoves(status.Rollout.Installations[i].State) {
		st := &status.Rollout.Installations[i]
		prev := st.State
		st.State, st.Message, _ = decideStage(prev, res)
		if st.State != prev {
			st.ReportedAt = nil
		}
		applyStage(&status, i, prev)
	}
	_, err := t.d.Actions.UpdateStatus(ctx, a.Name, status)
	return err
}
