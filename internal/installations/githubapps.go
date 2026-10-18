package installations

import (
	"context"
	"sort"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// AppHolder is an installation whose record keeps a GitHub App: the
// installation, its owning organisation, the App's purpose and its entry.
type AppHolder struct {
	Installation string           `json:"installation"`
	Customer     string           `json:"customer"`
	Purpose      string           `json:"purpose"`
	App          render.GitHubApp `json:"app"`
}

// AppHolders reads the credential inventory of GitHub Apps of every
// installation of insts as the person — githubApps of each config.yaml.patch,
// every read at once — and answers the holders by App id. An installation
// whose record cannot be read as the person is named in unread: what it
// holds is not known.
func (r *Registry) AppHolders(ctx context.Context, c *gh.Client, insts []Installation) (byID map[int][]AppHolder, unread []string) {
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	byID = map[int][]AppHolder{}
	for _, inst := range insts {
		if !inst.Repositories.Known() {
			continue
		}
		wg.Go(func() {
			apps, err := readGitHubApps(ctx, c, inst)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				unread = append(unread, inst.Name)
				return
			}
			for purpose, app := range apps {
				if app.ID > 0 {
					byID[app.ID] = append(byID[app.ID], AppHolder{Installation: inst.Name, Customer: inst.Customer, Purpose: purpose, App: app})
				}
			}
		})
	}
	wg.Wait()
	for id := range byID {
		sort.Slice(byID[id], func(i, j int) bool { return byID[id][i].Installation < byID[id][j].Installation })
	}
	sort.Strings(unread)
	return byID, unread
}

// readGitHubApps is githubApps of the installation's config.yaml.patch.
func readGitHubApps(ctx context.Context, c *gh.Client, inst Installation) (map[string]render.GitHubApp, error) {
	owner, repo, err := gh.SplitRepo(inst.Repositories.Configs)
	if err != nil {
		return nil, err
	}
	data, err := gh.ReadFile(ctx, c, owner, repo, ConfigPatchPath(inst.Name))
	if err != nil {
		return nil, err
	}
	var p configPatch
	if err := yaml.Unmarshal([]byte(data), &p); err != nil {
		return nil, err
	}
	return p.GitHubApps, nil
}
