// Package gh is the GitHub client of the manager: every call runs with the
// person's own user token — the bearer muster put on the request. The server
// holds no token of its own.
package gh

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/go-github/v92/github"
)

// ErrNotFound is a path or repository GitHub answered 404 for as the person:
// the file is absent, or the repository is one the person (or the App) may
// not see — GitHub does not tell the two apart.
var ErrNotFound = errors.New("not found")

// ErrForbidden is a repository GitHub answered 403 for as the person: the
// person may not read it, or the App is not installed on it.
var ErrForbidden = errors.New("forbidden")

// AsPerson returns a client that calls GitHub with the person's user token.
func AsPerson(apiURL, accessToken string) (*github.Client, error) {
	c, _, err := AsPersonCounted(apiURL, accessToken)
	return c, err
}

// Reads is what a client has cost so far: the number of requests it made.
type Reads interface {
	Requests() int64
}

// AsPersonCounted is AsPerson with the client's request count alongside, for
// a tool that reports what a call cost.
func AsPersonCounted(apiURL, accessToken string) (*github.Client, Reads, error) {
	return newClient(apiURL, github.WithAuthToken(accessToken))
}

// User is the person the token belongs to (GET /user): the call that verifies
// a bearer. A refusal keeps GitHub's *github.ErrorResponse in the chain.
func User(ctx context.Context, apiURL, accessToken string) (login string, id int64, email string, err error) {
	c, err := AsPerson(apiURL, accessToken)
	if err != nil {
		return "", 0, "", err
	}
	u, _, err := c.Users.Get(ctx, "")
	if err != nil {
		return "", 0, "", fmt.Errorf("github: GET /user as the person: %w", err)
	}
	return u.GetLogin(), u.GetID(), u.GetEmail(), nil
}

// ReadFile is the content of path on the default branch of owner/repo, read
// as the person. An absent file or an invisible repository is ErrNotFound, a
// refused one ErrForbidden; both keep GitHub's *github.ErrorResponse in the
// chain. A directory at path is an error: the manager reads files.
func ReadFile(ctx context.Context, c *github.Client, owner, repo, path string) (string, error) {
	file, dir, _, err := c.Repositories.GetContents(ctx, owner, repo, path, nil)
	if err != nil {
		return "", fmt.Errorf("github: %s/%s:%s: %w", owner, repo, path, classify(err))
	}
	if file == nil || dir != nil {
		return "", fmt.Errorf("github: %s/%s:%s is a directory, not a file", owner, repo, path)
	}
	content, err := file.GetContent()
	if err != nil {
		return "", fmt.Errorf("github: %s/%s:%s: decode content: %w", owner, repo, path, err)
	}
	return content, nil
}

// classify wraps GitHub's 404 and 403 in the sentinels callers branch on.
func classify(err error) error {
	var er *github.ErrorResponse
	if errors.As(err, &er) && er.Response != nil {
		switch er.Response.StatusCode {
		case http.StatusNotFound:
			return fmt.Errorf("%w: %w", ErrNotFound, err)
		case http.StatusForbidden:
			return fmt.Errorf("%w: %w", ErrForbidden, err)
		}
	}
	return err
}

// SplitRepo splits "owner/repo" into its two parts.
func SplitRepo(full string) (owner, repo string, err error) {
	owner, repo, ok := strings.Cut(strings.TrimSpace(full), "/")
	if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
		return "", "", fmt.Errorf("repository %q is not owner/repo", full)
	}
	return owner, repo, nil
}

// newClient builds a go-github client with a 15 s timeout, against apiURL
// when set (GitHub Enterprise shape; the fake in tests).
// maxInFlight bounds the requests one client has in flight at a time. A tool
// call reads a fleet's worth of files, and everything above the client runs
// concurrently; this is the one bound, well under GitHub's secondary limit
// of 100 concurrent requests.
const maxInFlight = 32

// limiter is the client's transport: at most maxInFlight requests at a time,
// and a count of every request the client made (Requests).
type limiter struct {
	base     http.RoundTripper
	slots    chan struct{}
	requests atomic.Int64
}

func newLimiter(base http.RoundTripper, n int) *limiter {
	return &limiter{base: base, slots: make(chan struct{}, n)}
}

func (l *limiter) RoundTrip(req *http.Request) (*http.Response, error) {
	select {
	case l.slots <- struct{}{}:
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
	defer func() { <-l.slots }()
	l.requests.Add(1)
	return l.base.RoundTrip(req)
}

// Requests is the number of requests the client made.
func (l *limiter) Requests() int64 { return l.requests.Load() }

func newClient(apiURL string, opts ...github.ClientOptionsFunc) (*github.Client, *limiter, error) {
	l := newLimiter(http.DefaultTransport, maxInFlight)
	opts = append(opts,
		github.WithHTTPClient(&http.Client{Transport: l}),
		github.WithTimeout(15*time.Second))
	if apiURL != "" {
		opts = append(opts, github.WithEnterpriseURLs(apiURL, apiURL))
	}
	c, err := github.NewClient(opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("github: client for %q: %w", apiURL, err)
	}
	return c, l, nil
}

// DefaultBranch is the branch owner/repo's pull requests land on, read as the
// person: the base of every branch this manager pushes.
func DefaultBranch(ctx context.Context, c *github.Client, owner, repo string) (string, error) {
	r, _, err := c.Repositories.Get(ctx, owner, repo)
	if err != nil {
		return "", fmt.Errorf("%s/%s: %w", owner, repo, classify(err))
	}
	if r.GetDefaultBranch() == "" {
		return "", fmt.Errorf("%s/%s: GitHub named no default branch", owner, repo)
	}
	return r.GetDefaultBranch(), nil
}

// PullRequestState is a pull request as GitHub has it now: open, merged or
// closed unmerged, with the merge's commit, time and login when merged.
type PullRequestState struct {
	// State is open or closed; Merged says a closed one was merged.
	State       string
	Merged      bool
	MergeCommit string
	MergedAt    *time.Time
	MergedBy    string
	ClosedAt    *time.Time
	HeadSHA     string
}

// The states GitHub answers for a pull request.
const (
	PullRequestOpen   = "open"
	PullRequestClosed = "closed"
)

// PullRequest reads owner/repo#number as the person: what the record of an
// action follows, whoever merged or closed it. A pull request the person
// cannot see is ErrNotFound, a refused one ErrForbidden.
func PullRequest(ctx context.Context, c *github.Client, owner, repo string, number int) (PullRequestState, error) {
	pr, _, err := c.PullRequests.Get(ctx, owner, repo, number)
	if err != nil {
		return PullRequestState{}, fmt.Errorf("github: %s/%s#%d: %w", owner, repo, number, classify(err))
	}
	st := PullRequestState{State: pr.GetState(), Merged: pr.GetMerged(), MergeCommit: pr.GetMergeCommitSHA(), MergedBy: pr.GetMergedBy().GetLogin(), HeadSHA: pr.GetHead().GetSHA()}
	if t := pr.GetMergedAt(); !t.IsZero() {
		at := t.UTC()
		st.MergedAt = &at
	}
	if t := pr.GetClosedAt(); !t.IsZero() {
		at := t.UTC()
		st.ClosedAt = &at
	}
	return st, nil
}

// FileExists says whether path is on the default branch of owner/repo, read
// as the person: false for an absent file, an error for anything the person
// could not read.
func FileExists(ctx context.Context, c *github.Client, owner, repo, path string) (bool, error) {
	_, err := ReadFile(ctx, c, owner, repo, path)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, ErrNotFound):
		return false, nil
	}
	return false, err
}
