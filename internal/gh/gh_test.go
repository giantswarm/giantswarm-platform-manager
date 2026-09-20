package gh

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A client's requests run at most maxInFlight at a time, however many are
// asked for at once, and every one of them is counted.
func TestClientBoundsRequestsInFlight(t *testing.T) {
	var inFlight, peak atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		content := base64.StdEncoding.EncodeToString([]byte("optIn: true\n"))
		_ = json.NewEncoder(w).Encode(map[string]any{"type": "file", "encoding": "base64", "content": content, "path": "x"})
	}))
	defer srv.Close()
	c, reads, err := AsPersonCounted(srv.URL, "token")
	if err != nil {
		t.Fatal(err)
	}
	const want = 100
	var wg sync.WaitGroup
	for range want {
		wg.Go(func() {
			if _, err := ReadFile(context.Background(), c, "o", "r", "x"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if p := peak.Load(); p > maxInFlight || p < 2 {
		t.Fatalf("peak in flight %d, bound %d", p, maxInFlight)
	}
	if n := reads.Requests(); n != want {
		t.Fatalf("%d requests counted for %d reads", n, want)
	}
}
