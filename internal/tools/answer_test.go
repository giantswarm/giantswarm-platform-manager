package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/giantswarm/giantswarm-platform-manager/internal/verify"
)

// An answer within the limit is the document; one above it is the tool's
// refusal naming the answer's size, the limit and how to ask for less —
// never an answer the path to the person drops.
func TestAnswerAboveTheLimitIsRefusedNamingSizeAndLimit(t *testing.T) {
	small := Answer(map[string]any{"ok": true})
	if small.IsError || !strings.Contains(text(small), `"ok": true`) {
		t.Fatalf("a small answer: %+v", small)
	}
	doc := map[string]any{"content": strings.Repeat("x", AnswerLimit)}
	b, _ := json.MarshalIndent(doc, "", "  ")
	big := Answer(doc)
	if !big.IsError {
		t.Fatalf("an answer of %d bytes was not refused", len(b))
	}
	for _, want := range []string{fmt.Sprintf("the answer is %d bytes (%s)", len(b), size(len(b))), fmt.Sprintf("above the %d bytes (1.0 MiB)", AnswerLimit), ArgInstallations, ArgInstallation, ArgContent + ": false"} {
		if !strings.Contains(text(big), want) {
			t.Errorf("the refusal %q lacks %q", text(big), want)
		}
	}
	if got := size(553*1024 + 512); got != "553.5 KiB" {
		t.Errorf("size: got %q", got)
	}
}

// One installation's entry carries the comparison's evidence; a set's entry
// carries every dimension's mark and reason and none of it.
func TestASetsEntryCarriesTheMarksWithoutTheEvidence(t *testing.T) {
	res := &verify.Result{Installation: "lab", Summary: map[verify.Mark]int{verify.Drifted: 1}, Features: []verify.Feature{{
		ID: "runtime", Title: "Runtime", Mark: verify.Drifted, Marks: map[verify.Mark]int{verify.Drifted: 1},
		Dimensions: []verify.Dimension{{
			ID: "kagent-providers", Kind: "configmap", Key: "kagent.providers", Mark: verify.Drifted, Reason: "the record differs", Detail: "the transport's error",
			Files:       []string{"acme/configs:installations/lab/apps/agent-platform/configmap-values.yaml.patch"},
			Differences: []verify.Difference{{Path: "kagent.providers.anthropic.config.maxTokens", Rendered: "32000", Current: "8192"}},
			Probe:       &verify.ProbeResult{},
		}},
	}}}
	whole := dryRun(res, true)
	if d := whole.Features[0].Dimensions[0]; len(d.Differences) != 1 || len(d.Files) != 1 || d.Detail == "" || d.Probe == nil {
		t.Fatalf("one installation's entry lost evidence: %+v", d)
	}
	set := dryRun(res, false)
	d := set.Features[0].Dimensions[0]
	if d.ID != "kagent-providers" || d.Kind != "configmap" || d.Key != "kagent.providers" || d.Mark != verify.Drifted || d.Reason != "the record differs" {
		t.Errorf("a set's entry lost a mark or its reason: %+v", d)
	}
	if len(d.Differences) != 0 || len(d.Files) != 0 || d.Detail != "" || d.Probe != nil {
		t.Errorf("a set's entry carries evidence: %+v", d)
	}
	if set.Name != "lab" || set.Summary[verify.Drifted] != 1 || set.Features[0].Mark != verify.Drifted || set.Features[0].Marks[verify.Drifted] != 1 {
		t.Errorf("a set's entry lost the roll-up: %+v", set)
	}
	if got := res.Features[0].Dimensions[0]; len(got.Differences) != 1 {
		t.Errorf("the result itself was stripped: %+v", got)
	}
}

// The files' content is answered as asked, else for one installation's plan
// and not for a set's.
func TestContentArg(t *testing.T) {
	for _, tc := range []struct {
		args  map[string]any
		whole bool
		want  bool
	}{
		{map[string]any{}, true, true},
		{map[string]any{}, false, false},
		{map[string]any{ArgContent: true}, false, true},
		{map[string]any{ArgContent: false}, true, false},
	} {
		if got := contentArg(tc.args, tc.whole); got != tc.want {
			t.Errorf("content %v whole %v: got %v, want %v", tc.args[ArgContent], tc.whole, got, tc.want)
		}
	}
}

func text(res *mcp.CallToolResult) string {
	for _, c := range res.Content {
		if t, ok := mcp.AsTextContent(c); ok {
			return t.Text
		}
	}
	return ""
}
