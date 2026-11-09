package plan

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
)

// SwitchOff is what a plan switches off of what runs on the installation:
// the components its agent-platform values patch enables on record
// (components.<name>.enabled: true) that the render no longer enables. One
// toggle among the drift lines is easy to miss, and a component switched off
// takes down what depends on it — the agent-manager the Dev Portal's Create
// agent. The dry run prints the warning first, apart from the drift lines,
// the action's summary carries it, and a commit is refused while the plan
// switches one off, until the person forces it with the warning known
// (SwitchOffRefusal).
type SwitchOff struct {
	// Components are the components switched off, in the patch's order on
	// record.
	Components []SwitchedOff `json:"components"`
	// Forced: the commit was asked for with the warning known
	// (forceSwitchOff), so it proceeds.
	Forced bool `json:"forced,omitempty"`
}

// SwitchedOff is one running component the plan switches off, and what
// depends on it.
type SwitchedOff struct {
	Component string `json:"component"`
	// DependedOnBy is what stops working with it, as a person reads it.
	DependedOnBy string `json:"dependedOnBy,omitempty"`
}

// String is the component as the warning names it.
func (s SwitchedOff) String() string {
	if s.DependedOnBy == "" {
		return s.Component
	}
	return s.Component + " (" + s.DependedOnBy + " depend on it)"
}

// servedModels depend on every component of the serving slice.
const servedModels = "the models the installation serves"

// componentDependents is what depends on each agent-platform component, as
// the warning names it; a component not listed names none.
var componentDependents = map[string]string{
	"kagent":                   "the installation's agents, their sessions and the Dev Portal's agents",
	"agent-manager":            "the Dev Portal's Create agent and agent editing",
	"klaus-gateway":            "the Slack app's conversations with the agents",
	"cluster-manager":          "the agents' cluster and node pool changes",
	"modelServing":             servedModels,
	"kserve-llmisvc-crd":       servedModels,
	"kserve-llmisvc-resources": servedModels,
	"kserve-runtime-configs":   servedModels,
}

// componentsKey is the components map of the agent-platform values patch.
const componentsKey = "components"

// templateEnabled are the components the shared defaults enable on every
// installation: the removals of kind template of the agent-platform
// configmap patch that name a component's toggle (components.<name>.enabled)
// or the component (components.<name>). A patch on record that repeats one's
// toggle loses it to the template, and the component keeps running.
var templateEnabled = mustTemplateComponents(installations.AgentPlatform, definitions.KindConfigMap)

// mustTemplateComponents reads the components among the definition's
// template keys under the prefix, as mustKeptKeys reads the kept keys.
func mustTemplateComponents(capability, prefix string) map[string]bool {
	keys, err := definitions.TemplateKeys(capability, prefix)
	if err != nil {
		panic(err)
	}
	out := map[string]bool{}
	for _, k := range keys {
		rest, ok := strings.CutPrefix(k, componentsKey+".")
		if !ok {
			continue
		}
		if name, toggle, nested := strings.Cut(rest, "."); !nested || toggle == "enabled" {
			out[name] = true
		}
	}
	return out
}

// switchOff reads the installation's agent-platform values patch on record
// against the render and records the components enabled on record that run
// no more once the render is applied: the render sets the toggle false, or
// leaves it out where the shared defaults do not enable the component — the
// patch carries a component's toggle only where it deviates from the shared
// template, which runs none of the optional ones; one the template enables
// (templateEnabled) keeps running without the toggle. A patch that does not
// decode on either side records nothing: the comparison names that by
// itself.
func (p *Installation) switchOff(current, rendered string, forced bool) {
	cur, ren := patchRoot(current), patchRoot(rendered)
	if cur == nil || ren == nil {
		return
	}
	comps := at(cur, componentsKey)
	if comps == nil || comps.Kind != yaml.MappingNode {
		return
	}
	var off []SwitchedOff
	for i := 0; i+1 < len(comps.Content); i += 2 {
		name := comps.Content[i].Value
		if !enabled(at(comps.Content[i+1], "enabled")) || runs(at(ren, componentsKey, name, "enabled"), templateEnabled[name]) {
			continue
		}
		off = append(off, SwitchedOff{Component: name, DependedOnBy: componentDependents[name]})
	}
	if len(off) > 0 {
		p.SwitchOff = &SwitchOff{Components: off, Forced: forced}
	}
}

// runs says a component runs under the rendered toggle: one set is the
// component's state, one left out is the shared defaults'.
func runs(toggle *yaml.Node, byDefault bool) bool {
	if toggle == nil {
		return byDefault
	}
	return enabled(toggle)
}

// enabled says a toggle reads true.
func enabled(n *yaml.Node) bool {
	return n != nil && n.Kind == yaml.ScalarNode && strings.EqualFold(n.Value, "true")
}

// Names are the components switched off, by name.
func (s *SwitchOff) Names() []string {
	if s == nil {
		return nil
	}
	out := make([]string, len(s.Components))
	for i, c := range s.Components {
		out[i] = c.Component
	}
	return out
}

// Warning is the warning in one line, as the action's summary carries it;
// empty where the plan switches nothing off.
func (s *SwitchOff) Warning() string {
	if s == nil || len(s.Components) == 0 {
		return ""
	}
	parts := make([]string, len(s.Components))
	for i, c := range s.Components {
		parts[i] = c.String()
	}
	forced := ""
	if s.Forced {
		forced = " (forced)"
	}
	return "warning: switches off running " + strings.Join(parts, "; ") + forced
}

// SwitchOffRefusal says why a commit of p is refused for the components it
// switches off: they run on record, and what depends on them stops the
// moment the commit merges. Empty where the plan switches nothing off, or
// the person forced it with the warning known. The comparison runs either
// way; only the commit is held.
func (p Installation) SwitchOffRefusal() string {
	if p.SwitchOff == nil || p.SwitchOff.Forced || len(p.SwitchOff.Components) == 0 {
		return ""
	}
	parts := make([]string, len(p.SwitchOff.Components))
	for i, c := range p.SwitchOff.Components {
		parts[i] = c.String()
	}
	return fmt.Sprintf("this reconcile switches off what runs on %s: %s. Check the dry run's warning — a policy listing the installation with fewer components than it runs is the usual cause — and fix the policy, or force the switch-off (forceSwitchOff; platformctl --force-switch-off), which proceeds with the warning",
		p.Name, strings.Join(parts, "; "))
}
