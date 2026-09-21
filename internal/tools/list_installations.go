package tools

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/internal/identity"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
)

// list_installations' arguments.
const (
	ArgInstallations = "installations"
	ArgCustomer      = "customer"
	ArgSummary       = "summary"
)

// ListInstallationsResult is list_installations' answer: every installation
// of the registry (or the ones asked for) with, per capability, the state,
// the inputs on record and the last action.
type ListInstallationsResult struct {
	// Caller is the person the registry and the repositories were read as.
	Caller string `json:"caller"`
	Hub    string `json:"hub"`
	// Summary says the answer stops at the states and the last actions: no
	// record, inputs on record, portals or federation facts were read.
	Summary bool `json:"summary"`
	// Registry names the two sources as read.
	Registry RegistryInfo `json:"registry"`
	// Capabilities are the capabilities every installation is answered for.
	Capabilities  []string               `json:"capabilities"`
	Installations []installations.Report `json:"installations"`
	// Unreadable names the installations whose repositories could not be
	// read as the caller; their states are unknown, their errors say why.
	Unreadable []string `json:"unreadable"`
	// States lists every state the model knows, so a reader can tell which
	// ones this answer can produce from the repositories alone.
	States StatesInfo `json:"states"`
}

// RegistryInfo names the registry sources.
type RegistryInfo struct {
	Catalog installations.Location `json:"catalog"`
	Portal  installations.Location `json:"portal"`
}

// StatesInfo separates the states read from the repositories from the ones
// the Action record and the last verify will add.
type StatesInfo struct {
	FromRepositories []installations.State `json:"fromRepositories"`
	FromActions      []installations.State `json:"fromActions"`
}

func statesInfo() StatesInfo {
	return StatesInfo{
		FromRepositories: []installations.State{installations.StateNotOptedIn, installations.StateEnabledNotOptedIn, installations.StateNotEnabled, installations.StateEnabled, installations.StateUnknown},
		FromActions:      []installations.State{installations.StatePendingApproval, installations.StateRollingOut, installations.StateWaitingForCustomer, installations.StateDrifted, installations.StateFailed},
	}
}

// listInstallationsTool is the tool as registered.
func listInstallationsTool() mcp.Tool {
	return mcp.NewTool(ToolListInstallations,
		mcp.WithDescription("Read-only, as you. Every installation of the registry — the installations catalog and the Dev Portal's app-config, read with your GitHub token now — with, per capability, two facts and their state: enabled (the capability's fileset is on record, its marker in the installation's repository, whoever put it there) and optedIn (the owners' declaration: the manager may write here), read as one word — 'not opted in' (neither), 'enabled, not opted in' (on record, installed by the owners themselves, and the manager may not write to it), 'not enabled' (opted in, nothing on record), 'enabled' (both); 'pending approval', 'rolling out', 'waiting for the customer', 'drifted' and 'failed' once the Action record exists — with the inputs on record (the installation facts from the registry and its config.yaml.patch) and the last action. The opt-in declaration management-clusters/<name>/platform-manager.yaml is read at call time from the installation's management-clusters repository, never cached; an installation without it (or with optIn: false) is not opted in whatever is on record, and the answer names the file and the pull request by its owners that would add it. An installation whose repositories you cannot read is listed as unreadable with the reason. Narrow with installations (names) or customer to read less; summary: true answers the states and the last actions alone, without the record, the inputs on record, the portals and the federation facts — a fraction of the reads, for an overview."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithArray(ArgInstallations, mcp.Description("Installation names to answer for; empty is every installation of the registry."), mcp.Items(stringItems())),
		mcp.WithString(ArgCustomer, mcp.Description("Answer only for this customer's installations, as the catalog names the customer.")),
		mcp.WithBoolean(ArgSummary, mcp.Description("The states and the last actions alone: no record, inputs on record, portals or federation facts. For an overview; the default answer carries them all.")),
	)
}

func (t *Tools) listInstallations(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	start := time.Now()
	token, ok := identity.TokenFromContext(ctx)
	if !ok {
		return result(nil, errors.New(ToolListInstallations+" needs a caller: the request carried no GitHub user token to read the registry and the installations' repositories as; "+identity.SignIn))
	}
	c, reads, err := gh.AsPersonCounted(t.d.GitHubAPIURL, token)
	if err != nil {
		return result(nil, err)
	}
	reg, err := t.registry(ctx, c)
	if err != nil {
		return result(nil, err)
	}
	selected, err := reg.Select(req.GetStringSlice(ArgInstallations, nil), strings.TrimSpace(req.GetString(ArgCustomer, "")))
	if err != nil {
		return result(nil, err)
	}
	caps := installations.Capabilities()
	detail := installations.Full
	if req.GetBool(ArgSummary, false) {
		detail = installations.Summary
	}
	out := ListInstallationsResult{
		Caller:        identity.Caller(ctx),
		Hub:           reg.Hub,
		Summary:       detail == installations.Summary,
		Registry:      RegistryInfo{Catalog: reg.Catalog, Portal: reg.Portal},
		Installations: reg.InspectAll(ctx, c, selected, caps, detail),
		Unreadable:    []string{},
		States:        statesInfo(),
	}
	if err := t.lastActions(ctx, out.Installations); err != nil {
		return result(nil, err)
	}
	for _, cap := range caps {
		out.Capabilities = append(out.Capabilities, cap.Name)
	}
	for _, r := range out.Installations {
		if !r.Readable {
			out.Unreadable = append(out.Unreadable, r.Name)
		}
	}
	t.d.Log.Info("list_installations", identity.LogAttr(ctx), "installations", len(out.Installations), "unreadable", len(out.Unreadable),
		"summary", out.Summary, "reads", reads.Requests(), "duration_ms", time.Since(start).Milliseconds())
	return result(out, nil)
}
