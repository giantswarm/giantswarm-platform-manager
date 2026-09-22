package render_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// TestRefusal: a definition's refusal is one sentence for a person, and the
// definition's sentinel with that sentence for the logs; any other error
// reads as it is.
func TestRefusal(t *testing.T) {
	kind := errors.New("customer-portal: input")
	sentence := "the portal's hostname (portal.domain) is not on record; supply it under Apply changes"
	err := fmt.Errorf("render: %w", &render.Refusal{Kind: kind, Sentence: sentence})
	if !errors.Is(err, kind) {
		t.Errorf("%v is not %v", err, kind)
	}
	if got := render.Reason(err); got != sentence {
		t.Errorf("reason %q, want %q", got, sentence)
	}
	if got, want := err.Error(), "render: customer-portal: input: "+sentence; got != want {
		t.Errorf("error %q, want %q", got, want)
	}
	if plain := errors.New("customer-portal: schema: unreadable"); render.Reason(plain) != plain.Error() {
		t.Errorf("a plain error reads %q", render.Reason(plain))
	}
}

// TestDescribe names an input as the schema says it is with its key, or by
// its key alone.
func TestDescribe(t *testing.T) {
	for _, tc := range []struct{ field, want string }{
		{"portal.domain", "the portal's hostname (portal.domain)"},
		{"plugins.github.appId", "the GitHub App's id (plugins.github.appId)"},
		{"federation.tokenBroker", "federation.tokenBroker"},
		{"portal.nothing", "portal.nothing"},
	} {
		if got := render.Describe("customer-portal", tc.field); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.field, got, tc.want)
		}
	}
}

func TestList(t *testing.T) {
	for _, tc := range []struct {
		names []string
		want  string
	}{
		{nil, ""},
		{[]string{"a"}, "a"},
		{[]string{"a", "b"}, "a and b"},
		{[]string{"a", "b", "c"}, "a, b and c"},
	} {
		if got := render.List(tc.names); got != tc.want {
			t.Errorf("%v: %q, want %q", tc.names, got, tc.want)
		}
	}
}
