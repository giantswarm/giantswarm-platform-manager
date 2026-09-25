package approvals

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

const standup = "CSTANDUP"

// A notice goes to POST /notices with the team and the standup channel of
// the configuration, the bearer from the token file; without a standup
// channel nothing is sent.
func TestNoticeGoesToTheStandupChannel(t *testing.T) {
	var got map[string]any
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/notices" || r.Header.Get("Authorization") != "Bearer sa-token" {
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusBadRequest)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"channel":"` + standup + `","ts":"1.2"}`))
	}))
	defer srv.Close()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("sa-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{GatewayURL: srv.URL, Team: "team-a", Channel: "CREVIEW", StandupChannel: standup, TokenFile: tokenFile}
	receipt, err := New(cfg, nil).Notice(context.Background(), Notice{Text: "*alice* reconciled *x* on *lab*.", PullRequests: []string{"https://example.test/pr/1"}})
	if err != nil || receipt.Channel != standup || receipt.TS != "1.2" {
		t.Fatalf("notice: %+v %v", receipt, err)
	}
	if got["team"] != "team-a" || got["channel"] != standup || got["text"] != "*alice* reconciled *x* on *lab*." || len(got["pullRequests"].([]any)) != 1 {
		t.Fatalf("the gateway received %v", got)
	}
	cfg.StandupChannel = ""
	if _, err := New(cfg, nil).Notice(context.Background(), Notice{Text: "x"}); err == nil || calls != 1 {
		t.Fatalf("a notice without a standup channel: %v, %d call(s)", err, calls)
	}
}
