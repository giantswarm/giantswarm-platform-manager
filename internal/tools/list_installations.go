package tools

import (
	"context"
	"errors"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/internal/identity"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
)

// list_installations' arguments.
const (
	ArgInstallations = "installations"
	ArgCustomer      = "customer"
)

// ListInstallationsResult is list_installations' answer: every installation
// of the registry (or the ones asked for) with, per capability, the state,
// the inputs on record and the last action.
type ListInstallationsResult struct {
	// Caller is the person the registry and the repositories were read as.
	Caller string `json:"caller"`
	Hub    string `json:"hub"`
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
		FromRepositories: []installations.State{installations.StateNotOptedIn, installations.StateNotEnabled, installations.StateEnabled, installations.StateUnknown},
		FromActions:      []installations.State{installations.StatePendingApproval, installations.StateRollingOut, installations.StateWaitingForCustomer, installations.StateDrifted, installations.StateFailed},
	}
}

// listInstallationsTool is the tool as registered.
func listInstallationsTool() mcp.Tool {
	return mcp.NewTool(ToolListInstallations,
		mcp.WithDescription("Read-only, as you. Every installation of the registry — the installations catalog and the Dev Portal's app-config, read with your GitHub token now — with, per capability, its state (not opted in, not enabled, enabled; pending approval, rolling out, waiting for the customer, drifted and failed once the Action record exists), the inputs on record (the installation facts from the registry and its config.yaml.patch) and the last action. The opt-in declaration management-clusters/<name>/platform-manager.yaml is read at call time from the installation's management-clusters repository, never cached; an installation without it (or with optIn: false) is not opted in, and the answer names the file and the pull request by its owners that would add it. An installation whose repositories you cannot read is listed as unreadable with the reason. Narrow with installations (names) or customer to read less."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithArray(ArgInstallations, mcp.Description("Installation names to answer for; empty is every installation of the registry."), mcp.Items(stringItems())),
		mcp.WithString(ArgCustomer, mcp.Description("Answer only for this customer's installations, as the catalog names the customer.")),
	)
}

func (t *Tools) listInstallations(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	token, ok := identity.TokenFromContext(ctx)
	if !ok {
		return result(nil, errors.New(ToolListInstallations+" needs a caller: the request carried no GitHub user token to read the registry and the installations' repositories as; "+identity.SignIn))
	}
	c, err := gh.AsPerson(t.d.GitHubAPIURL, token)
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
	out := ListInstallationsResult{
		Caller:        identity.Caller(ctx),
		Hub:           reg.Hub,
		Registry:      RegistryInfo{Catalog: reg.Catalog, Portal: reg.Portal},
		Installations: installations.InspectAll(ctx, c, selected, caps),
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
	t.d.Log.Info("list_installations", identity.LogAttr(ctx), "installations", len(out.Installations), "unreadable", len(out.Unreadable))
	return result(out, nil)
}
