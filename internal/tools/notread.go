package tools

import (
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/verify"
)

// The causes of a read the watch could not make.
const (
	// CauseAuthRequired: muster answered auth_required — the caller's session
	// is not signed in to the installation's kubernetes server.
	CauseAuthRequired = "auth_required"
	// CauseUnreachable: the manager got no answer from the host (timeout,
	// DNS, connection refused).
	CauseUnreachable = "unreachable"
	// CauseForbidden: the installation's apiserver refused the read as the caller.
	CauseForbidden = "forbidden"
	// CauseNoAnswer: a read through muster did not answer within its bound,
	// or the live verify's read budget was spent before it started.
	CauseNoAnswer = "no_answer"
	// CauseOther: any other refusal, as it was said.
	CauseOther = "other"
)

// NotRead is one cause of reads the watch could not make, where it struck,
// the checks it left unread and what makes them work: the muster server to
// sign in to, the host the manager got no answer from, the apiserver's
// refusal or the error as it was said. A count of unchecked items alone says
// nothing a person can act on.
type NotRead struct {
	Cause string `json:"cause"`
	// Server is the muster server the caller's session is not signed in to
	// (auth_required); SignInURL the sign-in muster offered for it.
	Server    string `json:"server,omitempty"`
	SignInURL string `json:"signInUrl,omitempty"`
	// Host is the host the manager got no answer from (unreachable).
	Host string `json:"host,omitempty"`
	// Message is what was answered, in one line: muster's, the apiserver's,
	// the transport's (the first of the group).
	Message string `json:"message,omitempty"`
	// Fix is what makes the reads work.
	Fix string `json:"fix,omitempty"`
	// Checks is how many checks the cause left unread, in Dimensions.
	Checks     int      `json:"checks"`
	Dimensions []string `json:"dimensions"`
}

// Line is the read in one line, never the sign-in URL (it carries the
// caller's state): what the review's thread and the watch's message carry.
func (n NotRead) Line() string {
	var where string
	switch n.Cause {
	case CauseAuthRequired:
		where = " on muster server " + orInstallationServer(n.Server)
	case CauseUnreachable:
		where = " " + n.Host
	}
	line := fmt.Sprintf("%s%s: %s of %s (%s)", n.Cause, where, plural(n.Checks, "check"), plural(len(n.Dimensions), "dimension"), strings.Join(n.Dimensions, ", "))
	if n.Fix != "" {
		line += " — " + n.Fix
	}
	return line
}

// notReadClause is the reads not made in one sentence, for the watch's message.
func notReadClause(reads []NotRead) string {
	lines := make([]string, 0, len(reads))
	for _, n := range reads {
		lines = append(lines, n.Line())
	}
	return "Not read: " + strings.Join(lines, "; ") + "."
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func orInstallationServer(server string) string {
	if server == "" {
		return "of the installation's kubernetes tools"
	}
	return server
}

// signIn is the fix of an auth_required: the server to connect in muster.
func signIn(server string) string {
	if server == "" {
		return "sign in to the installation's kubernetes server in muster, then watch again"
	}
	return fmt.Sprintf("sign in to %s in muster (muster auth login --server %s), then watch again", server, server)
}

// noRoute is the fix of an unreachable host.
const noRoute = "the manager has no answer from the host: it is down, or on addresses the manager does not reach"

// notRead names every read the watch could not make in res, grouped by
// cause and where — the live checks muster, the apiserver or the transport
// left unread and the anonymous probes' requests that got no answer — in
// the order first met. server is the muster server the installation's reads
// go to, for an auth_required that names none.
func notRead(res verify.Result, server string) []NotRead {
	var out []NotRead
	index := map[string]int{}
	add := func(key string, n NotRead, dim string) {
		i, ok := index[key]
		if !ok {
			i, index[key] = len(out), len(out)
			n.Dimensions = []string{}
			out = append(out, n)
		}
		out[i].Checks++
		if !slices.Contains(out[i].Dimensions, dim) {
			out[i].Dimensions = append(out[i].Dimensions, dim)
		}
	}
	for _, f := range res.Features {
		for _, d := range f.Dimensions {
			switch {
			case d.Kind == definitions.KindLive && d.Live != nil:
				for _, c := range d.Live.Checks {
					if c.Mark != verify.NotChecked {
						continue
					}
					key, n := checkNotRead(c, d.Live.AuthRequired, server)
					add(key, n, d.ID)
				}
			case d.Kind == definitions.KindProbe && d.Probe != nil:
				for _, r := range d.Probe.Requests {
					if r.Error == "" {
						continue
					}
					h := hostOf(r.URL)
					add(CauseUnreachable+"|"+h, NotRead{Cause: CauseUnreachable, Host: h, Message: r.Error, Fix: noRoute}, d.ID)
				}
			}
		}
	}
	return out
}

// checkNotRead is the cause of a live check left unread, with its grouping key.
func checkNotRead(c verify.Check, auth *verify.AuthRequired, server string) (string, NotRead) {
	switch {
	case strings.HasPrefix(c.Message, verify.NotConnected):
		n := NotRead{Cause: CauseAuthRequired, Server: server, Message: strings.TrimSpace(strings.TrimPrefix(c.Message, verify.NotConnected+":"))}
		if auth != nil {
			if auth.Server != "" {
				n.Server = auth.Server
			}
			n.SignInURL = auth.URL
		}
		n.Fix = signIn(n.Server)
		return CauseAuthRequired + "|" + n.Server, n
	case c.URL != "" && strings.HasPrefix(c.Message, verify.ReasonUnreachable):
		h := hostOf(c.URL)
		return CauseUnreachable + "|" + h, NotRead{Cause: CauseUnreachable, Host: h, Message: orMessage(c.Detail, c.Message), Fix: noRoute}
	case strings.HasPrefix(c.Message, "forbidden for "):
		return CauseForbidden, NotRead{Cause: CauseForbidden, Message: c.Message, Fix: "the installation's RBAC refuses these reads as you: a role that may read the objects"}
	case strings.HasPrefix(c.Message, "no answer within ") || strings.HasPrefix(c.Message, "not read: the live verify's read budget"):
		return CauseNoAnswer, NotRead{Cause: CauseNoAnswer, Message: c.Message, Fix: "the path to the installation (muster, the tunnel, its kubernetes tool, the apiserver) did not answer: watch again once it does"}
	}
	return CauseOther + "|" + c.Message, NotRead{Cause: CauseOther, Message: c.Message}
}

func orMessage(detail, message string) string {
	if detail != "" {
		return detail
	}
	return message
}

// hostOf is the host of u, never its query (a Dex client's redirect URI); u
// when it parses to none.
func hostOf(u string) string {
	if p, err := url.Parse(u); err == nil && p.Host != "" {
		return p.Host
	}
	return u
}

// privateNote says up front that the installation is private and the
// manager reaches none of the endpoints it probed — the HTTP checks the
// manager sends itself are not read — and which checks run from the caller
// instead: the reads of the installation's objects, through the caller's
// muster session to server. "" for an installation not on record as private
// or one whose endpoints answered.
func privateNote(installation string, inputs map[string]any, reads []NotRead, res verify.Result, server string) string {
	if private, _ := dig(inputs, inputInstallation, inputPrivate).(bool); !private {
		return ""
	}
	var hosts, unread []string
	for _, n := range reads {
		if n.Cause != CauseUnreachable {
			continue
		}
		hosts = append(hosts, n.Host)
		for _, d := range n.Dimensions {
			if !slices.Contains(unread, d) {
				unread = append(unread, d)
			}
		}
	}
	if len(hosts) == 0 {
		return ""
	}
	var caller []string
	for _, f := range res.Features {
		for _, d := range f.Dimensions {
			if d.Kind == definitions.KindLive && d.Live != nil && readsObjects(d.Live) {
				caller = append(caller, d.ID)
			}
		}
	}
	through := "your muster session"
	if server != "" {
		through += " to " + server
	}
	note := fmt.Sprintf("%s is private: the manager does not reach its endpoints (%s), so %s are not read from the manager (%s)", installation, strings.Join(hosts, ", "), plural(len(unread), "endpoint check"), strings.Join(unread, ", "))
	if len(caller) > 0 {
		note += fmt.Sprintf("; the checks of its objects run through %s instead (%s)", through, strings.Join(caller, ", "))
	}
	return note + "."
}

// readsObjects says whether a live dimension reads objects of the
// installation, not only URLs.
func readsObjects(lv *verify.LiveResult) bool {
	for _, c := range lv.Checks {
		if c.URL == "" {
			return true
		}
	}
	return false
}

// inputInstallation is the inputs' section of the installation's facts, inputPrivate its flag of an installation reached through the tunnel.
const (
	inputInstallation = "installation"
	inputPrivate      = "private"
)

// dig reads the leaf of values at path.
func dig(values map[string]any, path ...string) any {
	var cur any = values
	for _, k := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[k]
	}
	return cur
}
