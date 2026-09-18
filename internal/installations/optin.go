package installations

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/go-github/v92/github"
	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
)

// OptInFile is the opt-in declaration's file name in
// management-clusters/<name>/ of the installation's management-clusters
// repository.
const OptInFile = "platform-manager.yaml"

// OptInPath is the declaration's path for installation name.
func OptInPath(name string) string { return "management-clusters/" + name + "/" + OptInFile }

// OptInState is what the declaration says.
type OptInState string

const (
	// OptedIn: the file is present with optIn: true.
	OptedIn OptInState = "opted in"
	// NotOptedIn: the file is absent, or present with optIn: false.
	NotOptedIn OptInState = "not opted in"
	// OptInUnreadable: the repository could not be read as the caller.
	OptInUnreadable OptInState = "unreadable"
)

// OptIn is the installation's opt-in declaration as read now: the owners'
// standing consent that the manager may open pull requests for the
// installation's capabilities (decision D13). Never cached.
type OptIn struct {
	State OptInState `json:"state"`
	// Repository and Path locate the declaration.
	Repository string `json:"repository"`
	Path       string `json:"path"`
	// Present says whether the file exists; Value is its optIn when present.
	Present bool  `json:"present"`
	Value   *bool `json:"optIn,omitempty"`
	// HowToOptIn says, for an installation not opted in, what adds it: a
	// pull request by the owners, never one of the manager's.
	HowToOptIn string `json:"howToOptIn,omitempty"`
	// Error is why the declaration could not be read.
	Error string `json:"error,omitempty"`
}

// optInDeclaration is the file's schema (the README documents it): one key.
type optInDeclaration struct {
	OptIn *bool `yaml:"optIn"`
}

// ReadOptIn reads the declaration of inst as the person, now.
func ReadOptIn(ctx context.Context, c *github.Client, inst Installation) OptIn {
	o := OptIn{Repository: inst.Repositories.ManagementClusters, Path: OptInPath(inst.Name)}
	owner, repo, err := gh.SplitRepo(o.Repository)
	if err != nil {
		o.State, o.Error = OptInUnreadable, err.Error()
		return o
	}
	data, err := gh.ReadFile(ctx, c, owner, repo, o.Path)
	switch {
	case errors.Is(err, gh.ErrNotFound):
		o.State, o.HowToOptIn = NotOptedIn, howToOptIn(o)
		return o
	case err != nil:
		o.State, o.Error = OptInUnreadable, err.Error()
		return o
	}
	o.Present = true
	var d optInDeclaration
	if err := yaml.Unmarshal([]byte(data), &d); err != nil {
		o.State, o.Error = OptInUnreadable, fmt.Sprintf("%s in %s is not the declaration the README describes: %v", o.Path, o.Repository, err)
		return o
	}
	if d.OptIn == nil {
		o.State, o.Error = OptInUnreadable, fmt.Sprintf("%s in %s has no optIn key; the README describes the declaration", o.Path, o.Repository)
		return o
	}
	o.Value = d.OptIn
	if *d.OptIn {
		o.State = OptedIn
		return o
	}
	o.State, o.HowToOptIn = NotOptedIn, howToOptIn(o)
	return o
}

func howToOptIn(o OptIn) string {
	return fmt.Sprintf("a pull request by the installation's owners adding %s with optIn: true to %s — the owners' decision, never a pull request of this manager", o.Path, o.Repository)
}
