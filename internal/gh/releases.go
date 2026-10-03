package gh

import (
	"context"
	"fmt"

	"github.com/google/go-github/v92/github"
)

// Release is a GitHub release of a repository: its tag, and whether it is a
// draft or marked as a pre-release.
type Release struct {
	Tag        string
	Draft      bool
	Prerelease bool
}

// releasesPerPage is the page size of a releases read, GitHub's largest.
const releasesPerPage = 100

// ReleasesPage reads page (from 1) of owner/repo's releases as the person,
// newest first, and whether a further page exists.
func ReleasesPage(ctx context.Context, c *Client, owner, repo string, page int) ([]Release, bool, error) {
	list, resp, err := c.Repositories.ListReleases(ctx, owner, repo, &github.ListOptions{Page: page, PerPage: releasesPerPage})
	if err != nil {
		return nil, false, fmt.Errorf("the releases of %s/%s: %w", owner, repo, classify(err))
	}
	out := make([]Release, 0, len(list))
	for _, r := range list {
		out = append(out, Release{Tag: r.GetTagName(), Draft: r.GetDraft(), Prerelease: r.GetPrerelease()})
	}
	return out, resp != nil && resp.NextPage != 0, nil
}
