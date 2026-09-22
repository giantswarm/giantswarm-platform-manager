package render

import (
	"errors"
	"strings"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
)

// Refusal is a definition's refusal of its inputs as a person reads it: one
// sentence naming the input (what the schema says it is, its key), what is
// wrong with it and what supplies it. The error's text carries the
// definition's sentinel as its prefix, for the logs and errors.Is; Reason is
// the sentence alone, what verify_capability, a dry run and platformctl
// answer.
type Refusal struct {
	// Kind is the definition's sentinel the refusal unwraps to (its ErrInput).
	Kind error
	// Sentence is the refusal as a person reads it.
	Sentence string
}

func (r *Refusal) Error() string { return r.Kind.Error() + ": " + r.Sentence }

func (r *Refusal) Unwrap() error { return r.Kind }

// Reason is what a person reads of a definition's error: a Refusal's
// sentence, any other error's text.
func Reason(err error) string {
	var r *Refusal
	if errors.As(err, &r) {
		return r.Sentence
	}
	return err.Error()
}

// Describe names an input for a sentence: what the capability's schema says
// it is, its key in parentheses — "the portal's hostname (portal.domain)" —
// or the key alone when the schema says nothing short about it.
func Describe(capability, field string) string {
	if what := definitions.InputSummary(capability, field); what != "" {
		return what + " (" + field + ")"
	}
	return field
}

// List joins names for a sentence: "a", "a and b", "a, b and c".
func List(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}
