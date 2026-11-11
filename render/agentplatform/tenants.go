package agentplatform

import (
	"fmt"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// Subject is an RBAC subject in the meta chart's shape
// (tenants.platform.users[*] and its siblings).
type Subject struct {
	APIGroup  string `json:"apiGroup,omitempty"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

// PlatformTenant is the person's choice of who the platform tenant binds
// (tenants.*) and who lists muster's MCP servers (musterMcpServerViewers),
// each read back from the configmap patch on record. Each list is nil where
// the person chose none: the key is not written and the chart's default
// stands. An empty list is a choice, written as []: the meta chart drops a
// null and its default comes back, so [] is the only way to bind nobody.
type PlatformTenant struct {
	Users        *[]Subject
	Editors      *[]Subject
	PlatformTeam *[]Subject
	Viewers      *[]Subject
}

// rbacGroup is the API group of a User or a Group subject.
const rbacGroup = "rbac.authorization.k8s.io"

// The kinds of an RBAC subject.
const (
	subjectUser           = "User"
	subjectGroup          = "Group"
	subjectServiceAccount = "ServiceAccount"
)

// platformTenantChart is the first meta chart that takes tenants.*
// (giantswarm/agent-platform#706); an earlier one's values schema refuses the
// key.
const platformTenantChart = "4.132.0-rc.1"

// musterViewersChart is the first meta chart that takes
// musterMcpServerViewers (giantswarm/agent-platform#1331, in its release
// candidate 4.178.0-rc.4); an earlier one's values schema refuses the key.
const musterViewersChart = "4.178.0-rc.4"

// The inputs, by the chart paths they write.
const (
	inputPlatformUsers   = "tenants.platform.users"
	inputPlatformEditors = "tenants.platform.editors"
	inputPlatformTeam    = "tenants.platformTeam"
	inputMusterViewers   = "musterMcpServerViewers"
)

// subjectList is a chosen list in the chart's shape, each subject's keys in
// the chart's order, so a render with the choice unchanged reproduces the
// patch on record.
func subjectList(s *[]Subject) []render.Map {
	list := make([]render.Map, 0, len(*s))
	for _, sub := range *s {
		var m render.Map
		if sub.APIGroup != "" {
			m = append(m, e("apiGroup", sub.APIGroup))
		}
		m = append(m, e("kind", sub.Kind), e("name", sub.Name))
		if sub.Namespace != "" {
			m = append(m, e("namespace", sub.Namespace))
		}
		list = append(list, m)
	}
	return list
}

// platformTenantValues appends the chosen lists to the configmap patch, an
// empty one as []; a list not chosen is not written.
func (in *Input) platformTenantValues(m render.Map) render.Map {
	t := in.PlatformTenant
	var platform render.Map
	if t.Users != nil {
		platform = append(platform, e("users", subjectList(t.Users)))
	}
	if t.Editors != nil {
		platform = append(platform, e("editors", subjectList(t.Editors)))
	}
	var tenants render.Map
	if len(platform) > 0 {
		tenants = append(tenants, e("platform", platform))
	}
	if t.PlatformTeam != nil {
		tenants = append(tenants, e("platformTeam", subjectList(t.PlatformTeam)))
	}
	if len(tenants) > 0 {
		m = append(m, render.Entry{Key: "tenants", Value: tenants, Comment: "The platform tenant's bindings in the kagent namespace: the inputs tenants.*,\nread back so a reconcile keeps them. An empty list binds nobody."})
	}
	if t.Viewers != nil {
		m = append(m, render.Entry{Key: "musterMcpServerViewers", Value: subjectList(t.Viewers), Comment: "muster's MCP-server viewers: the input musterMcpServerViewers, read back so\na reconcile keeps it. An empty list binds nobody."})
	}
	return m
}

// checkPlatformTenant refuses the lists the installation cannot render: off
// the 4 chart line, under a hold of the meta chart before the release that
// takes the key, the platform tenant's own lists where no kagent runs (the
// chart binds them in kagent's namespace only), a subject the API server
// refuses in a RoleBinding, and, with the organizations on, no viewer of
// muster's MCP servers: the meta chart fails the render then
// (agent-platform.muster.organizations).
func (in *Input) checkPlatformTenant() error {
	t := in.PlatformTenant
	for _, c := range []struct {
		input string
		list  *[]Subject
		floor string
	}{
		{inputPlatformUsers, t.Users, platformTenantChart},
		{inputPlatformEditors, t.Editors, platformTenantChart},
		{inputPlatformTeam, t.PlatformTeam, platformTenantChart},
		{inputMusterViewers, t.Viewers, musterViewersChart},
	} {
		if c.list == nil {
			continue
		}
		if in.Installation.ChartLine != lineFour {
			return refuse(fmt.Sprintf("%s is the 4 chart line's, and this installation runs the %s line; the record's chart line decides", describe(c.input), in.Installation.ChartLine))
		}
		if !holdAdmits(in.Versions.Chart, c.floor) {
			return refuse(fmt.Sprintf("%s needs the meta chart %s or later, whose values take the key, and %s holds it at %s; move or lift the hold first", describe(c.input), c.floor, describe("versions.chart"), in.Versions.Chart))
		}
		if (c.input == inputPlatformUsers || c.input == inputPlatformEditors) && !in.kagent() {
			return refuse(fmt.Sprintf("%s is bound in kagent's namespace, and the fleet policy runs no kagent on %s's installations", describe(c.input), in.Installation.Customer))
		}
		for i, s := range *c.list {
			if err := checkSubject(s); err != "" {
				return refuse(fmt.Sprintf("entry %d of %s is a subject the API server refuses in a RoleBinding: %s", i, describe(c.input), err))
			}
		}
	}
	if in.Organizations.Enabled && len(in.musterViewers()) == 0 {
		return refuse(fmt.Sprintf("%s needs muster's MCP-server viewers, and %s; the meta chart refuses the render then, since muster lists its own namespace's MCP servers as the person and nobody is bound to list them", describe("organizations.enabled"), in.emptyViewers()))
	}
	return nil
}

// musterViewers are the viewers of muster's MCP servers as the chart
// resolves them: musterMcpServerViewers where set, else tenants.platform.users
// where set, else the chart's default, every signed-in person.
func (in *Input) musterViewers() []Subject {
	t := in.PlatformTenant
	switch {
	case t.Viewers != nil:
		return *t.Viewers
	case t.Users != nil:
		return *t.Users
	}
	return []Subject{{APIGroup: rbacGroup, Kind: subjectGroup, Name: "system:authenticated"}}
}

// emptyViewers says which empty list leaves muster's MCP servers without a
// viewer.
func (in *Input) emptyViewers() string {
	if in.PlatformTenant.Viewers != nil {
		return describe(inputMusterViewers) + " are an empty list"
	}
	return describe(inputPlatformUsers) + " are an empty list while " + describe(inputMusterViewers) + " are unset, so the users are the viewers; set " + inputMusterViewers + " first"
}

// checkSubject is what the API server refuses of a RoleBinding's subject
// (rbac.authorization.k8s.io/v1, the apiGroup defaulted where empty): a
// User's or a Group's apiGroup other than rbac.authorization.k8s.io, a
// ServiceAccount's apiGroup or a name that is no ServiceAccount's; empty when
// it is accepted.
func checkSubject(s Subject) string {
	switch s.Kind {
	case subjectUser, subjectGroup:
		if s.APIGroup != "" && s.APIGroup != rbacGroup {
			return fmt.Sprintf("a %s's apiGroup is %s, not %q", s.Kind, rbacGroup, s.APIGroup)
		}
	case subjectServiceAccount:
		if s.APIGroup != "" {
			return fmt.Sprintf("a ServiceAccount's apiGroup is empty, not %q", s.APIGroup)
		}
		if errs := validation.IsDNS1123Subdomain(s.Name); len(errs) > 0 {
			return fmt.Sprintf("%q is no ServiceAccount's name: %s", s.Name, errs[0])
		}
	}
	return ""
}
