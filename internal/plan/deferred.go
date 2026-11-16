package plan

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// DeferredPair is a new shared value whose other installation is outside the plan.
type DeferredPair struct {
	Name         string   `json:"name"`
	Installation string   `json:"installation"`
	Files        []string `json:"files"`
	Reason       string   `json:"reason"`
}

func deferPairs(ctx context.Context, opts Options, result *render.Result) (*render.Result, []DeferredPair, error) {
	if opts.PlanInstallations == nil || result.WithoutPairs == nil {
		return result, nil, nil
	}
	candidates := map[string]*DeferredPair{}
	blocked := map[string]bool{}
	for repository, files := range result.Files {
		resolved := ResolveRepository(string(repository), opts.Installation, opts.Hub)
		for path, file := range files {
			_, currentError := opts.Read(ctx, resolved, path)
			for _, generated := range file.Generated {
				if !errors.Is(currentError, gh.ErrNotFound) || slices.Contains(opts.Rotate, generated.Name) {
					blocked[generated.Name] = true
				}
				if generated.Peer == nil || opts.PlanInstallations[generated.Peer.Installation] {
					continue
				}
				peer := peerOf(*generated.Peer, opts.Installations)
				if peer.err != nil {
					blocked[generated.Name] = true
					continue
				}
				_, peerError := opts.Read(ctx, peer.ref.repository, peer.ref.path)
				if !errors.Is(peerError, gh.ErrNotFound) {
					blocked[generated.Name] = true
					continue
				}
				candidate := candidates[generated.Name]
				if candidate == nil {
					candidate = &DeferredPair{Name: generated.Name, Installation: peer.installation,
						Reason: fmt.Sprintf("%s is outside this plan and neither side is on record; reconcile both installations in the same wave", peer.installation)}
					candidates[generated.Name] = candidate
				}
				candidate.Files = append(candidate.Files, resolved+":"+path)
			}
		}
	}
	var deferred []DeferredPair
	for name, candidate := range candidates {
		if !blocked[name] {
			sort.Strings(candidate.Files)
			deferred = append(deferred, *candidate)
		}
	}
	sort.Slice(deferred, func(i, j int) bool { return deferred[i].Name < deferred[j].Name })
	filtered, err := (Installation{DeferredPairs: deferred}).ApplyDeferrals(result)
	return filtered, deferred, err
}

// ApplyDeferrals omits deferred pairs and their consumers from a commit render.
func (p Installation) ApplyDeferrals(result *render.Result) (*render.Result, error) {
	if len(p.DeferredPairs) == 0 {
		return result, nil
	}
	if result.WithoutPairs == nil {
		return nil, errors.New("the definition cannot defer paired values and their consumers")
	}
	names := make([]string, len(p.DeferredPairs))
	for index, deferred := range p.DeferredPairs {
		names[index] = deferred.Name
	}
	filtered, err := result.WithoutPairs(names)
	if err != nil {
		return nil, err
	}
	for _, files := range filtered.Files {
		for _, file := range files {
			for _, generated := range file.Generated {
				if slices.Contains(names, generated.Name) {
					return nil, fmt.Errorf("the definition still renders deferred pair %s", generated.Name)
				}
			}
		}
	}
	return filtered, nil
}
