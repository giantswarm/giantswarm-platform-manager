package definitions

import (
	"encoding/json"
	"strings"
	"unicode"
)

// summaryLength bounds the clause of a description a refusal quotes in
// parentheses.
const summaryLength = 60

// schemaNode is a definition's schema as InputSummary reads it: the
// properties and what each one says it is.
type schemaNode struct {
	Properties  map[string]schemaNode `json:"properties"`
	Title       string                `json:"title"`
	Description string                `json:"description"`
}

// InputSummary is what a definition's schema says the input at the dotted
// field is, for a person reading a refusal: the field's title, or the first
// clause of its description (up to a colon, comma, semicolon, full stop or
// dash)
// when it is short enough to read in parentheses, its leading capital
// lowered. Empty when the schema does not know the field or says nothing
// short about it.
func InputSummary(capability, field string) string {
	raw, err := FS.ReadFile(capability + "/schema.json")
	if err != nil {
		return ""
	}
	var n schemaNode
	if err := json.Unmarshal(raw, &n); err != nil {
		return ""
	}
	for _, k := range strings.Split(field, ".") {
		child, ok := n.Properties[k]
		if !ok {
			return ""
		}
		n = child
	}
	if n.Title != "" {
		return n.Title
	}
	return clause(n.Description)
}

// clause is the first clause of a description when it is short, its leading
// capital lowered; empty otherwise. A clause ends at a dash, or at a colon,
// comma, semicolon or full stop followed by a space or the end: the dots of
// a key or a host (agentPlatform.kagentApiV2, certificates.k8s.io) are none.
func clause(description string) string {
	if i := clauseEnd(description); i >= 0 {
		description = description[:i]
	}
	r := []rune(strings.TrimSpace(description))
	if len(r) == 0 || len(r) > summaryLength {
		return ""
	}
	if len(r) > 1 && unicode.IsUpper(r[0]) && unicode.IsLower(r[1]) {
		r[0] = unicode.ToLower(r[0])
	}
	return string(r)
}

// clauseEnd is the index where the first clause of description ends, -1
// when it runs to the end.
func clauseEnd(description string) int {
	for i, c := range description {
		switch c {
		case '—':
			return i
		case ':', ',', ';', '.':
			if rest := description[i+1:]; rest == "" || rest[0] == ' ' {
				return i
			}
		}
	}
	return -1
}
