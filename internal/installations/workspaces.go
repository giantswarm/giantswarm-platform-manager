package installations

import (
	"context"
	"errors"
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
)

// Workspaces is the workspace-manager as an installation's own agent-platform
// values turn it on: workspaces.enabled, the manager's public base URL where
// the values set one (workspace-manager.oauth.baseURL) and the Dex client it
// signs the person's browser in with where they name one
// (workspace-manager.oauth.dex.clientID). The definition renders none of the
// keys; list_installations reports them.
type Workspaces struct {
	Enabled  bool   `json:"enabled"`
	BaseURL  string `json:"baseUrl,omitempty"`
	ClientID string `json:"clientId,omitempty"`
}

// readWorkspaces reads whether inst's agent-platform values on record turn
// workspaces on, with the manager's base URL and client where they name
// them: nil where the patch does not exist, carries no workspaces switch or
// turns it off; an installation without a configs repository has none.
func readWorkspaces(ctx context.Context, read Reader, inst Installation) (*Workspaces, error) {
	if inst.Repositories.Configs == "" {
		return nil, nil
	}
	data, err := read(ctx, inst.Repositories.Configs, AgentPlatformPatchPath(inst.Name))
	if errors.Is(err, gh.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("the workspaces on record: %w", err)
	}
	var patch struct {
		Workspaces struct {
			Enabled bool `yaml:"enabled"`
		} `yaml:"workspaces"`
		WorkspaceManager struct {
			OAuth struct {
				BaseURL string `yaml:"baseURL"`
				Dex     struct {
					ClientID string `yaml:"clientID"`
				} `yaml:"dex"`
			} `yaml:"oauth"`
		} `yaml:"workspace-manager"`
	}
	if err := yaml.Unmarshal([]byte(data), &patch); err != nil {
		return nil, fmt.Errorf("the workspaces on record: %s: %w", AgentPlatformPatchPath(inst.Name), err)
	}
	if !patch.Workspaces.Enabled {
		return nil, nil
	}
	return &Workspaces{Enabled: true, BaseURL: patch.WorkspaceManager.OAuth.BaseURL, ClientID: patch.WorkspaceManager.OAuth.Dex.ClientID}, nil
}
