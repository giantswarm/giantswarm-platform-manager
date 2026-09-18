// Package approvals is the manager's client of klaus-gateway's Team review:
// one review per action, posted to the capability-owning team's channel
// (and noticed to a second channel), and the results posted into the
// review's thread as the action moves. The client authenticates with the
// pod's projected ServiceAccount token for the gateway's audience, read from
// its file on every call so the kubelet's rotation is followed; it holds no
// other credential and never sees a person's token.
package approvals

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Config is where the reviews go: chart values, never constants.
type Config struct {
	// GatewayURL is klaus-gateway's base URL as reached from the pod.
	GatewayURL string
	// Team is the team whose members decide, as the gateway names it.
	Team string
	// Channel is the capability-owning team's channel the review lands in.
	Channel string
	// NoticeChannel gets the same text without buttons when a customer
	// installation is a target (the Account Engineers' channel). Empty: no notice.
	NoticeChannel string
	// TokenFile holds the projected ServiceAccount token (audience: the
	// gateway's) the requests carry as the bearer.
	TokenFile string
}

// Configured says whether reviews can be posted at all.
func (c Config) Configured() bool { return c.GatewayURL != "" }

// Validate refuses a configuration with a gateway but no team, channel or token file.
func (c Config) Validate() error {
	if !c.Configured() {
		return nil
	}
	var missing []string
	for name, v := range map[string]string{"team": c.Team, "channel": c.Channel, "tokenFile": c.TokenFile} {
		if v == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("approvals: a gateway URL is set and %s is not", strings.Join(sorted(missing), ", "))
	}
	if c.NoticeChannel != "" && c.NoticeChannel == c.Channel {
		return errors.New("approvals: the notice channel must differ from the review channel")
	}
	return nil
}

// Tool is a button's call: the manager's tool and its arguments, called by
// the gateway through muster as the clicking member.
type Tool struct {
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// Review is one approval request as POST /reviews takes it.
type Review struct {
	Team          string   `json:"team"`
	Channel       string   `json:"channel"`
	Text          string   `json:"text"`
	Actor         string   `json:"actor,omitempty"`
	PullRequests  []string `json:"pullRequests,omitempty"`
	Link          string   `json:"link,omitempty"`
	Approve       Tool     `json:"approve"`
	Deny          *Tool    `json:"deny,omitempty"`
	NoticeChannel string   `json:"noticeChannel,omitempty"`
}

// Receipt is the gateway's answer to a posted review.
type Receipt struct {
	ID       string `json:"id"`
	Channel  string `json:"channel"`
	TS       string `json:"ts"`
	NoticeTS string `json:"notice_ts,omitempty"`
}

// ErrGone: the gateway holds no record of the review (an unknown id, or one
// past its retention) — the caller re-posts.
var ErrGone = errors.New("the gateway holds no record of the review")

// Client posts reviews and results.
type Client struct {
	cfg Config
	hc  *http.Client
}

// New builds the client; hc nil is a client with a timeout.
func New(cfg Config, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{cfg: cfg, hc: hc}
}

// Config is the client's configuration.
func (c *Client) Config() Config { return c.cfg }

// Post posts one review; the review's team, channel and notice channel are
// the configuration's unless r names them.
func (c *Client) Post(ctx context.Context, r Review) (Receipt, error) {
	if r.Team == "" {
		r.Team = c.cfg.Team
	}
	if r.Channel == "" {
		r.Channel = c.cfg.Channel
	}
	var out Receipt
	if err := c.do(ctx, "/reviews", r, &out); err != nil {
		return Receipt{}, err
	}
	if out.ID == "" {
		return Receipt{}, errors.New("approvals: the gateway answered a review without an id")
	}
	return out, nil
}

// Result posts text (mrkdwn) into the review's thread; ErrGone when the
// gateway no longer holds the review.
func (c *Client) Result(ctx context.Context, reviewID, text, link string) error {
	body := map[string]string{"text": text}
	if link != "" {
		body["link"] = link
	}
	return c.do(ctx, "/reviews/"+reviewID+"/results", body, nil)
}

func (c *Client) do(ctx context.Context, path string, body, out any) error {
	token, err := os.ReadFile(c.cfg.TokenFile)
	if err != nil {
		return fmt.Errorf("approvals: read the gateway token: %w", err)
	}
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("approvals: encode: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.cfg.GatewayURL, "/")+path, bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("approvals: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("approvals: %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%w (%s)", ErrGone, path)
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return fmt.Errorf("approvals: the gateway answered %s with HTTP %d: %s", path, resp.StatusCode, strings.TrimSpace(string(answer)))
	}
	if out != nil {
		if err := json.Unmarshal(answer, out); err != nil {
			return fmt.Errorf("approvals: decode the gateway's answer to %s: %w", path, err)
		}
	}
	return nil
}

func sorted(s []string) []string {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
	return s
}
