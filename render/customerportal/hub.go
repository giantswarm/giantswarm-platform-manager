package customerportal

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// semverPath is the OCIRepository patch operation the chart line is written
// in, as the fleet's portal kustomizations patch it over the base's tag.
const semverPath = "/spec/ref/semver"

// hubShape renders the hub's Dev Portal kept by hand (HubPortal): its files
// stand as the record holds them — the hub's own extension list, its pages
// over muster, its secrets — and the definition changes the one key the hub's
// shape takes as an input, the chart line, in the portal's kustomization on
// record. No other file, no include, no Dex patch and no generated value: a
// reconcile of the hub's portal moves its release range and nothing else.
func (in *Input) hubShape(r *render.Result, repo render.Repository, dir string) {
	line := in.Chart.Line
	r.Add(repo, dir+"kustomization.yaml", render.File{Edit: func(current []byte) ([]byte, error) { return withChartLine(current, line) }})
}

// withChartLine is the portal kustomization current with the value of its
// OCIRepository patch's semver operation set to line, every other byte as it
// is: the value's line is rewritten in place, quoted as the record quotes it.
// A kustomization without exactly one such operation is no portal
// kustomization the hub's shape can move, and the error names what it lacks.
func withChartLine(current []byte, line string) ([]byte, error) {
	value, fileLine, err := semverValue(current)
	if err != nil {
		return nil, err
	}
	if value.Value == line {
		return current, nil
	}
	lines := strings.Split(string(current), "\n")
	if fileLine < 1 || fileLine > len(lines) {
		return nil, fmt.Errorf("the OCIRepository patch's %s value is at line %d, outside the file", semverPath, fileLine)
	}
	old := lines[fileLine-1]
	at := strings.LastIndex(old, "value:")
	if at < 0 || strings.Contains(old[at:], "#") {
		return nil, fmt.Errorf("line %d of the kustomization is not the %s operation's plain value line", fileLine, semverPath)
	}
	lines[fileLine-1] = old[:at] + "value: " + quoted(line, value.Style)
	edited := []byte(strings.Join(lines, "\n"))
	if got, _, err := semverValue(edited); err != nil || got.Value != line {
		return nil, fmt.Errorf("the edited kustomization does not read back the chart line %q", line)
	}
	return edited, nil
}

// semverValue is the value node of the semver operation of current's
// OCIRepository patch, and its line in current.
func semverValue(current []byte) (*yaml.Node, int, error) {
	var k struct {
		Patches []struct {
			Patch  yaml.Node `yaml:"patch"`
			Target struct {
				Kind string `yaml:"kind"`
				Name string `yaml:"name"`
			} `yaml:"target"`
		} `yaml:"patches"`
	}
	if err := yaml.Unmarshal(current, &k); err != nil {
		return nil, 0, fmt.Errorf("the portal kustomization does not read: %w", err)
	}
	var found []*yaml.Node
	var at []int
	for _, p := range k.Patches {
		if p.Target.Kind != "OCIRepository" || p.Target.Name != releaseName {
			continue
		}
		if p.Patch.Style&yaml.LiteralStyle == 0 {
			return nil, 0, errors.New("the OCIRepository patch is no literal block; the hub's shape moves the chart line in a block it can keep byte for byte")
		}
		var ops []struct {
			Path  string    `yaml:"path"`
			Value yaml.Node `yaml:"value"`
		}
		if err := yaml.Unmarshal([]byte(p.Patch.Value), &ops); err != nil {
			return nil, 0, fmt.Errorf("the OCIRepository patch does not read as a list of operations: %w", err)
		}
		for _, op := range ops {
			if op.Path == semverPath {
				found = append(found, &op.Value)
				// A literal block's content starts on the line after its indicator.
				at = append(at, p.Patch.Line+op.Value.Line)
			}
		}
	}
	if len(found) != 1 {
		return nil, 0, fmt.Errorf("the portal kustomization carries %d %s operation(s) in its OCIRepository %s patch; the hub's shape moves the chart line in exactly one", len(found), semverPath, releaseName)
	}
	return found[0], at[0], nil
}

// quoted is v in the quoting style of the value it replaces: single quotes
// where the record has them, double quotes otherwise (a range starts with >,
// which YAML does not take plain).
func quoted(v string, style yaml.Style) string {
	if style&yaml.SingleQuotedStyle != 0 {
		return "'" + strings.ReplaceAll(v, "'", "''") + "'"
	}
	var b bytes.Buffer
	b.WriteByte('"')
	b.WriteString(strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v))
	b.WriteByte('"')
	return b.String()
}
