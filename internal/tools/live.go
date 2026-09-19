package tools

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/identity"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/live"
	"github.com/giantswarm/giantswarm-platform-manager/internal/verify"
)

// LiveToolPrefix is the MCPServer name muster registers the live surface
// under: the second registration of the same Deployment, forwardToken.
const LiveToolPrefix = ToolPrefix + "-live"

// ToolVerifyInstallation is the live surface's one tool: the definition's
// probes of the running installation, read as the person.
const ToolVerifyInstallation = "verify_installation"

// LiveInfo is the live surface as get_info reports it.
type LiveInfo struct {
	// Configured says whether the live path serves; without it the live
	// dimensions of every verify read not checked.
	Configured bool   `json:"configured"`
	ToolPrefix string `json:"toolPrefix"`
	Tool       string `json:"tool"`
	live.Info
}

func (t *Tools) liveInfo() LiveInfo {
	info := LiveInfo{Configured: t.d.Live != nil, ToolPrefix: LiveToolPrefix, Tool: ToolVerifyInstallation}
	if t.d.Live != nil {
		info.Info = t.d.Live.Info()
	}
	return info
}

// LiveMCPServer is the tool set of the live path: verify_installation, and
// nothing that acts on GitHub — the bearer here is the person's ID token,
// not a GitHub token.
func (t *Tools) LiveMCPServer() *mcpserver.MCPServer {
	s := mcpserver.NewMCPServer(LiveToolPrefix, t.d.Version,
		mcpserver.WithToolCapabilities(false),
		mcpserver.WithInstructions("Giant Swarm's installation manager, the live surface: muster forwards your own sign-in token here, and verify_installation reads an installation with it — through muster's kubernetes tools, as you, with your access on that installation — and compares what runs there with the capability's definition, grouped into features with one mark each. The repository comparison is verify_capability on the App-pinned registration ("+ToolPrefix+"); a portal or platformctl shows the two as one result."),
	)
	s.AddTool(mcp.NewTool(ToolVerifyInstallation,
		mcp.WithDescription("Verify the running installation against a capability's definition, as you: the definition's probes — HelmReleases Ready, workloads Available, the Secrets and MCPServer objects present, conditions, logs, the live values against the render from the inputs on record — read through muster's kubernetes tools with the token muster forwarded, so what you may read decides what is checked: an object you may not read is reported as not checked, forbidden for you, never as a failure of the installation; an installation you are not connected to in muster answers with muster's own sign-in. Grouped into the definition's features with one mark each — as defined, differs by input, drifted — and expanded to its dimensions; the repository dimensions read not checked here (verify_capability). The result is recorded on the newest action of the installation and feeds list_installations: drifted, or waiting for the customer when the only red dimension is the one the customer's action holds up."),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithString(ArgInstallation, mcp.Required(), mcp.Description("The installation to verify, by name: the management cluster muster reads for you.")),
		mcp.WithString(ArgCapability, mcp.Description(capabilityArgDescription), mcp.Enum(installations.CapabilityNames()...)),
	), t.verifyInstallationLive)
	return s
}

func (t *Tools) verifyInstallationLive(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return result(t.verifyLive(ctx, req.GetArguments()))
}

// verifyLive is verify_installation: the newest action's inputs on record
// (the manager's own read, no GitHub), the reads through muster as the
// person, the result recorded on that action.
func (t *Tools) verifyLive(ctx context.Context, args map[string]any) (any, error) {
	token, ok := identity.TokenFromContext(ctx)
	id, _ := identity.FromContext(ctx)
	if !ok || id == nil {
		return nil, errors.New(ToolVerifyInstallation + " needs the person's token: the request carried no forwarded ID token — this tool is reached through muster's " + LiveToolPrefix + " registration, which forwards your own")
	}
	if t.d.Live == nil {
		return nil, errors.New(ToolVerifyInstallation + ": the live path is not configured (muster.liveServer); get_info reports live.configured")
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
	opts := verify.LiveOptions{Definition: def, Installation: name, Inputs: verify.Inputs{Source: InputsNone}, Probes: t.d.Probes, Person: id.String()}
	var record *actions.Action
	for i := range acts {
		if in := acts[i].InputsOnRecord(name); in != nil {
			record = &acts[i]
			opts.Inputs = verify.Inputs{Source: "action " + record.Name, Values: in}
			// The state the result starts from is the action's final word,
			// not the state a previous verify recorded over it.
			opts.State = installations.State(record.InstallationState(name))
			if record.Status.Result != nil {
				opts.State = installations.State(record.Status.Result.State)
			}
			break
		}
	}
	if record != nil {
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

// recordLiveVerify writes the live result onto the action it rendered from:
// one probe entry per live dimension of this installation, and — for an
// action that has reached its final word — the installation's state:
// drifted, waiting for the customer, or the action's result again when the
// installation is back as defined. list_installations reads it from there.
func (t *Tools) recordLiveVerify(ctx context.Context, a actions.Action, installation string, res verify.Result) error {
	now := time.Now()
	status := a.Status
	live := map[string]bool{}
	var probes []actions.Probe
	for _, f := range res.Features {
		for _, d := range f.Dimensions {
			if d.Kind != definitions.KindLive {
				continue
			}
			live[d.ID] = true
			p := actions.Probe{ID: d.ID, Installation: installation, Result: string(d.Mark), At: &now}
			switch {
			case d.Reason != "":
				p.Message = d.Reason
			case len(d.Differences) > 0:
				p.Message = fmt.Sprintf("%d difference(s), the first at %s %s", len(d.Differences), d.Differences[0].Object, d.Differences[0].Path)
			case d.Live != nil && len(d.Live.Checks) > 0:
				p.Message = d.Live.Checks[0].Message
			}
			probes = append(probes, p)
		}
	}
	kept := status.Probes[:0:0]
	for _, p := range status.Probes {
		if p.Installation != installation || !live[p.ID] {
			kept = append(kept, p)
		}
	}
	status.Probes = append(kept, probes...)
	if status.Result != nil {
		state := status.Result.State
		if res.State == installations.StateDrifted || res.State == installations.StateWaitingForCustomer {
			state = string(res.State)
		}
		staged := false
		if status.Rollout != nil {
			for i := range status.Rollout.Installations {
				if status.Rollout.Installations[i].Name == installation {
					status.Rollout.Installations[i].State, staged = state, true
				}
			}
		}
		if !staged {
			status.State = state
		}
	}
	_, err := t.d.Actions.UpdateStatus(ctx, a.Name, status)
	return err
}
