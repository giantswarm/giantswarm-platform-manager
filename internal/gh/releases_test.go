package gh

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A page of releases is read with GitHub's largest page size, and the Link
// header says whether a further page exists.
func TestReleasesPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/repos/giantswarm/agent-platform/releases" || r.URL.Query().Get("per_page") != "100" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "1" {
			w.Header().Set("Link", `<`+"http://"+r.Host+r.URL.Path+`?page=2&per_page=100>; rel="next"`)
			_, _ = w.Write([]byte(`[{"tag_name":"v4.2.0-rc.1","prerelease":true},{"tag_name":"v4.2.0","draft":true}]`))
			return
		}
		_, _ = w.Write([]byte(`[{"tag_name":"v4.1.0"}]`))
	}))
	t.Cleanup(srv.Close)
	c, err := AsPerson(NewFiles(time.Minute), srv.URL+"/", "token")
	if err != nil {
		t.Fatal(err)
	}

	got, more, err := ReleasesPage(t.Context(), c, "giantswarm", "agent-platform", 1)
	if err != nil || !more || len(got) != 2 || got[0] != (Release{Tag: "v4.2.0-rc.1", Prerelease: true}) || got[1] != (Release{Tag: "v4.2.0", Draft: true}) {
		t.Fatalf("page 1: %+v %v %v", got, more, err)
	}
	got, more, err = ReleasesPage(t.Context(), c, "giantswarm", "agent-platform", 2)
	if err != nil || more || len(got) != 1 || got[0] != (Release{Tag: "v4.1.0"}) {
		t.Fatalf("page 2: %+v %v %v", got, more, err)
	}
	if _, _, err := ReleasesPage(t.Context(), c, "giantswarm", "missing", 1); err == nil {
		t.Fatal("a repository GitHub does not answer: no error")
	}
}
