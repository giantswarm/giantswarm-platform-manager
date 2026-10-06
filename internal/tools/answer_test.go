package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/internal/verify"
)

// An answer within the limit is the document; one above it is the tool's
// refusal naming the answer's size, the limit and how to ask for less —
// never an answer the path to the person drops.
func TestAnswerAboveTheLimitIsRefusedNamingSizeAndLimit(t *testing.T) {
	small := Answer(map[string]any{"ok": true})
	if small.IsError || !strings.Contains(text(small), `"ok":true`) {
		t.Fatalf("a small answer: %+v", small)
	}
	doc := map[string]any{"content": strings.Repeat("x", AnswerLimit)}
	b, _ := json.Marshal(doc)
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

// One installation's entry carries the comparison's evidence and the plan
// whole; a set's entry carries what differs — the differing dimensions with
// their mark and reason and none of the evidence, the files that change or
// carry a finding without their relations, the generated values a commit
// writes, rotates or refuses — and counts the rest.
func TestASetsEntryCarriesWhatDiffers(t *testing.T) {
	marks := map[verify.Mark]int{verify.Drifted: 1, verify.AsDefined: 1, verify.NotChecked: 1}
	res := &verify.Result{Installation: "lab", Summary: marks, Features: []verify.Feature{{
		ID: "runtime", Title: "Runtime", Mark: verify.Drifted, Marks: marks,
		Dimensions: []verify.Dimension{{
			ID: "kagent-providers", Kind: "configmap", Key: "kagent.providers", Mark: verify.Drifted, Reason: "the record differs", Detail: "the transport's error",
			Files:       []string{"acme/configs:installations/lab/apps/agent-platform/configmap-values.yaml.patch"},
			Differences: []verify.Difference{{Path: "kagent.providers.anthropic.config.maxTokens", Rendered: "32000", Current: "8192"}},
			Probe:       &verify.ProbeResult{},
		}, {ID: "kagent-image", Kind: "configmap", Key: "kagent.image", Mark: verify.AsDefined},
			{ID: "live-kagent", Kind: "live", Key: "kagent answers", Mark: verify.NotChecked, Reason: "needs your session on the installation"}},
	}}}
	whole := dryRun(res)
	whole.Files = []plan.File{
		{Repository: "acme/configs", Path: "update.yaml", Change: plan.ChangeUpdate, Generated: []string{"lab-cookie"}, Kept: []plan.Kept{{List: "a", Entry: "b"}}, Creates: []string{"Secret/a"}, References: []string{"Secret/b"}},
		{Repository: "acme/configs", Path: "unchanged.yaml", Change: plan.ChangeUnchanged, References: []string{"Secret/b"}},
		{Repository: "acme/configs", Path: "unseen.yaml", Change: plan.ChangeUnchanged, Unseen: []plan.Unseen{{}}},
		{Repository: "acme/configs", Path: "unreadable.yaml", Change: plan.ChangeUnchanged, Error: "forbidden"},
	}
	whole.GeneratedSecrets = []plan.GeneratedSecret{{Name: "lab-new"}, {Name: "lab-kept", Kept: true}, {Name: "lab-rotated", Kept: true, Rotates: true}, {Name: "lab-refused", Kept: true, Refusal: "frozen"}}
	if d := whole.Features[0].Dimensions[0]; len(whole.Features[0].Dimensions) != 3 || len(d.Differences) != 1 || len(d.Files) != 1 || d.Detail == "" || d.Probe == nil {
		t.Fatalf("one installation's entry lost evidence: %+v", whole.Features[0])
	}

	set := rolledUp(whole)
	if len(set.Features[0].Dimensions) != 1 {
		t.Fatalf("a set's entry lists dimensions that do not differ: %+v", set.Features[0].Dimensions)
	}
	d := set.Features[0].Dimensions[0]
	if d.ID != "kagent-providers" || d.Kind != "configmap" || d.Key != "kagent.providers" || d.Mark != verify.Drifted || d.Reason != "the record differs" {
		t.Errorf("a set's entry lost a mark or its reason: %+v", d)
	}
	if len(d.Differences) != 0 || len(d.Files) != 0 || d.Detail != "" || d.Probe != nil {
		t.Errorf("a set's entry carries evidence: %+v", d)
	}
	if set.Name != "lab" || set.Summary[verify.AsDefined] != 1 || set.Features[0].Mark != verify.Drifted || set.Features[0].Marks[verify.NotChecked] != 1 {
		t.Errorf("a set's entry lost the counts: %+v", set)
	}
	var paths []string
	for _, f := range set.Files {
		paths = append(paths, f.Path)
		if f.Generated != nil || f.Kept != nil || f.Creates != nil || f.References != nil {
			t.Errorf("a set's file carries its relations: %+v", f)
		}
	}
	if got := strings.Join(paths, " "); got != "update.yaml unseen.yaml unreadable.yaml" {
		t.Errorf("a set's files: got %s", got)
	}
	var names []string
	for _, g := range set.GeneratedSecrets {
		names = append(names, g.Name)
	}
	if got := strings.Join(names, " "); got != "lab-new lab-rotated lab-refused" {
		t.Errorf("a set's generated values: got %s", got)
	}
	if len(whole.Features[0].Dimensions) != 3 || len(whole.Files) != 4 || whole.Files[0].References == nil || len(whole.GeneratedSecrets) != 4 {
		t.Errorf("the whole entry was stripped: %+v", whole)
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
