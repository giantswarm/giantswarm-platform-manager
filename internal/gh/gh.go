// Package gh is the GitHub client of the manager: every call runs with the
// person's own user token — the bearer muster put on the request. The server
// holds no token of its own. Repository files are read through Files: one
// tree per repository, validated as the person with a conditional request,
// the blobs by SHA from a cache shared across calls and persons.
package gh

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
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

// ErrRateLimited is GitHub refusing a request because the person's API quota
// is spent for the hour, or their requests too dense for a moment; the
// message names when it resets. The quota is the person's, shared with every
// other client of their account.
var ErrRateLimited = errors.New("rate limited")

// Client is the person's GitHub client: go-github with the person's token,
// the count of the requests it made, and the repository reads through the
// shared Files (ReadFile, ReadFileAt, FileExists). One Client is one call:
// a tree it validated once is the call's picture of that repository.
type Client struct {
	*github.Client
	files  *Files
	person string
	reads  *limiter

	mu    sync.Mutex
	trees map[treeKey]*flight
}

// AsPerson returns a client that calls GitHub with the person's user token
// and reads repository files through files.
func AsPerson(files *Files, apiURL, accessToken string) (*Client, error) {
	if files == nil {
		return nil, errors.New("github: a client needs the files cache")
	}
	c, l, err := newClient(apiURL, github.WithAuthToken(accessToken))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(accessToken))
	return &Client{Client: c, files: files, person: hex.EncodeToString(sum[:8]), reads: l, trees: map[treeKey]*flight{}}, nil
}

// Requests is the number of requests the client made, the conditional ones
// GitHub answered 304 included; Charged is the number the person's quota
// paid for: the requests less those 304s.
func (c *Client) Requests() int64 { return c.reads.Requests() }

// Charged is the number of the client's requests that cost the person's
// quota: every request but the conditional ones GitHub answered 304.
func (c *Client) Charged() int64 { return c.reads.Requests() - c.reads.NotModified() }

// User is the person the token belongs to (GET /user): the call that verifies
// a bearer. A refusal keeps GitHub's *github.ErrorResponse in the chain.
func User(ctx context.Context, apiURL, accessToken string) (login string, id int64, email string, err error) {
	c, _, err := newClient(apiURL, github.WithAuthToken(accessToken))
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
// as the person: from the repository's tree at HEAD, validated as the person
// once per call, and the blob by SHA. An absent file or an invisible
// repository is ErrNotFound, a refused one ErrForbidden, a spent quota
// ErrRateLimited; the first two keep GitHub's *github.ErrorResponse in the
// chain. A directory at path is an error: the manager reads files.
func ReadFile(ctx context.Context, c *Client, owner, repo, path string) (string, error) {
	return ReadFileAt(ctx, c, owner, repo, path, "")
}

// ReadFileAt is ReadFile at ref — a branch, a tag or a commit; the default
// branch when ref is empty.
func ReadFileAt(ctx context.Context, c *Client, owner, repo, path, ref string) (string, error) {
	return c.files.read(ctx, c, owner, repo, path, ref)
}

// FileExists says whether path is a file on the default branch of owner/repo,
// read as the person: false for an absent file, an error for a repository
// the person could not read. It costs no request beyond the tree's.
func FileExists(ctx context.Context, c *Client, owner, repo, path string) (bool, error) {
	return c.files.exists(ctx, c, owner, repo, path)
}

// classify wraps GitHub's refusals in the sentinels callers branch on: a
// spent or dense quota (ErrRateLimited, naming the reset), 404 and 403.
func classify(err error) error {
	var rl *github.RateLimitError
	if errors.As(err, &rl) {
		reset := rl.Rate.Reset.Time
		return fmt.Errorf("%w: GitHub's API rate limit for your account is spent until %s (in %d min); the manager reads GitHub with your token, and the %d requests per hour are shared with every other client of your account: %w",
			ErrRateLimited, reset.UTC().Format("15:04 UTC"), int(math.Ceil(math.Max(0, time.Until(reset).Minutes()))), rl.Rate.Limit, err)
	}
	var al *github.AbuseRateLimitError
	if errors.As(err, &al) {
		wait := "a moment"
		if al.RetryAfter != nil {
			wait = al.RetryAfter.String()
		}
		return fmt.Errorf("%w: GitHub's secondary rate limit refused the request as too dense; retry in %s: %w", ErrRateLimited, wait, err)
	}
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
// a count of every request the client made (Requests) and of the ones GitHub
// answered 304 Not Modified (NotModified), which cost the quota nothing.
type limiter struct {
	base                  http.RoundTripper
	slots                 chan struct{}
	requests, notModified atomic.Int64
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
	resp, err := l.base.RoundTrip(req)
	if err == nil && resp.StatusCode == http.StatusNotModified {
		l.notModified.Add(1)
	}
	return resp, err
}

// Requests is the number of requests the client made.
func (l *limiter) Requests() int64 { return l.requests.Load() }

// NotModified is the number of them GitHub answered 304.
func (l *limiter) NotModified() int64 { return l.notModified.Load() }

func newClient(apiURL string, opts ...github.ClientOptionsFunc) (*github.Client, *limiter, error) {
	// A call reads in bursts — the trees of an installation's repositories,
	// then the blobs a cold cache lacks — of up to maxInFlight requests to
	// one host; the default transport keeps two idle connections, so each
	// burst opened the rest anew. Keeping as many idle as may be in flight
	// lets the next burst reuse them.
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.MaxIdleConnsPerHost = maxInFlight
	l := newLimiter(base, maxInFlight)
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
func DefaultBranch(ctx context.Context, c *Client, owner, repo string) (string, error) {
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
func PullRequest(ctx context.Context, c *Client, owner, repo string, number int) (PullRequestState, error) {
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
