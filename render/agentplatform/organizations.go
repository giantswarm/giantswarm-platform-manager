package agentplatform

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// Organizations is the person's choice of the Agent Platform organizations
// (organizations.*): the switch, the installation's other Kubernetes API
// hostnames, commit mode and the Dev Portal's Organizations pages.
type Organizations struct {
	Enabled      bool     `json:"enabled"`
	APIHostnames []string `json:"apiHostnames"`
	Commit       struct {
		Enabled bool `json:"enabled"`
	} `json:"commit"`
	Portal struct {
		Enabled bool `json:"enabled"`
	} `json:"portal"`
}

// OrganizationsPolicy is policy.yaml's organizations block: where an
// installation's organizations live in git in commit mode, the same on
// every installation.
type OrganizationsPolicy struct {
	Branch                    string `yaml:"branch"`
	InstallationKustomization string `yaml:"installationKustomization"`
	Flux                      struct {
		Namespace string `yaml:"namespace"`
		Source    struct {
			Kind string `yaml:"kind"`
			Name string `yaml:"name"`
		} `yaml:"source"`
		DecryptionSecret string `yaml:"decryptionSecret"`
	} `yaml:"flux"`
}

// organizationsChart is the first meta chart whose organizations the
// definition turns on: the switch and the operator (4.175.0), muster's
// servers by label and commit mode from agentPlatformOrganizations.flux*
// (4.176.0), the snapshot store for the cleanup of a deleted organization
// (4.176.6).
const organizationsChart = "4.176.6"

// organizationsDir is the directory under an installation's extras that
// holds its organizations in git, in commit mode: the objects the
// installation's Kustomization applies and each organization's content,
// neither containing the other.
const organizationsDir = "agent-platform-organizations"

// organizationsPath is the repository path of the organizations' part
// (objects or content) of this installation.
func (in *Input) organizationsPath(part string) string {
	return "management-clusters/" + in.Installation.Name + "/extras/" + organizationsDir + "/" + part
}

// organizationsValues appends the organizations' values to the configmap
// patch: the switch with the API hostnames and, in commit mode, the Flux
// values the operator's Kustomizations follow and the operator's own
// installation Kustomization. agent-manager's commit settings are its
// section's (organizationsAgentManager).
func (in *Input) organizationsValues(m render.Map) render.Map {
	o := in.Organizations
	if !o.Enabled {
		return m
	}
	orgs := render.Map{e("enabled", true)}
	if len(o.APIHostnames) > 0 {
		orgs = append(orgs, e("apiHostnames", o.APIHostnames))
	}
	if !o.Commit.Enabled {
		return append(m, e("agentPlatformOrganizations", orgs))
	}
	p := in.organizationsPolicy
	orgs = append(orgs, e("fluxNamespace", p.Flux.Namespace),
		e("fluxSource", render.Map{e("kind", p.Flux.Source.Kind), e("name", p.Flux.Source.Name)}),
		e("fluxPathPrefix", "./"+in.organizationsPath("content")+"/"),
		e("fluxDecryptionSecret", p.Flux.DecryptionSecret))
	return append(m, e("agentPlatformOrganizations", orgs),
		e("agent-platform-operator", render.Map{e("commitMode", render.Map{e("installationKustomization",
			render.Map{e("name", p.InstallationKustomization), e("namespace", p.Flux.Namespace)})})}))
}

// organizationsAgentManager is agent-manager's tenants section in commit
// mode: the installation's management-clusters repository, the policy's
// branch and the installation's objects and content paths; nothing
// otherwise, since the meta chart derives tenants.enabled from the switch.
func (in *Input) organizationsAgentManager() render.Map {
	if !in.Organizations.Enabled || !in.Organizations.Commit.Enabled {
		return nil
	}
	return render.Map{e("tenants", render.Map{e("commit", render.Map{
		e("repository", in.managementClustersRepository()),
		e("branch", in.organizationsPolicy.Branch),
		e("objectsPath", in.organizationsPath("objects")),
		e("contentPrefix", in.organizationsPath("content")),
	})})}
}

// managementClustersRepository is the installation's management-clusters
// repository, owner/name, the one the render writes the extras into.
func (in *Input) managementClustersRepository() string {
	return "giantswarm/" + in.Installation.Customer + "-management-clusters"
}

// portalOrganizations says whether the portal section turns on the Dev
// Portal's Organizations pages: the person's choice, or the organizations
// on this installation.
func (in *Input) portalOrganizations() bool {
	return in.hostedPortal() != nil && (in.Organizations.Portal.Enabled || in.Organizations.Enabled)
}

// checkOrganizations refuses the organizations' choices the installation
// cannot serve: the switch off the 4 chart line, where no kagent runs or
// with a hold of the meta chart below organizationsChart; its hostnames or
// commit mode without it; commit mode where no agent-manager runs; the
// portal's pages where no portal carries a section for the installation.
func (in *Input) checkOrganizations() error {
	o := in.Organizations
	if o.Enabled && in.Installation.ChartLine != lineFour {
		return refuse(fmt.Sprintf("%s is the 4 chart line's, and this installation runs the %s line; the record's chart line decides", describe("organizations.enabled"), in.Installation.ChartLine))
	}
	if o.Enabled && !in.kagent() {
		return refuse(fmt.Sprintf("%s keeps the organizations' agents in kagent, and the fleet policy runs no kagent on %s's installations", describe("organizations.enabled"), in.Installation.Customer))
	}
	if o.Enabled && !holdAdmits(in.Versions.Chart, organizationsChart) {
		return refuse(fmt.Sprintf("%s needs the meta chart %s or later, and %s holds it at %s; move or lift the hold first", describe("organizations.enabled"), organizationsChart, describe("versions.chart"), in.Versions.Chart))
	}
	if len(o.APIHostnames) > 0 && !o.Enabled {
		return refuse(describe("organizations.apiHostnames") + " names the API hostnames an organization's egress may not reach, and " + describe("organizations.enabled") + " is off")
	}
	if o.Commit.Enabled && !o.Enabled {
		return refuse(describe("organizations.commit.enabled") + " commits the organizations to git, and " + describe("organizations.enabled") + " is off")
	}
	if o.Commit.Enabled && !in.agentManager() {
		return refuse(fmt.Sprintf("%s has agent-manager open the organizations' pull requests, and the fleet policy runs no agent-manager on %s's installations", describe("organizations.commit.enabled"), in.Installation.Customer))
	}
	if o.Portal.Enabled && in.hostedPortal() == nil {
		return refuse(describe("organizations.portal.enabled") + " turns on the Organizations pages in the developer portal's agent-platform section, and no portal carries one for this installation: the record lists no portal hosted on it, nor its organisation's on a sibling that is not hand-kept")
	}
	return nil
}

// kubernetesName is a Kubernetes object name (RFC 1123 label): a namespace,
// or an agent's name.
var kubernetesName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

// organizationNamespacePrefix starts the name of every organization
// namespace agent-platform-operator creates: agents-<organization>-<suffix>.
const organizationNamespacePrefix = "agents-"

// checkDefaultAgent refuses a chat gateway's default agent the gateway
// cannot call: a name in kagent, or namespace/name in an organization
// namespace.
func checkDefaultAgent(field, agent string) error {
	if agent == "" {
		return nil
	}
	namespace, name, qualified := strings.Cut(agent, "/")
	if !qualified {
		namespace, name = componentKagent, agent
	}
	if !kubernetesName.MatchString(namespace) || !kubernetesName.MatchString(name) ||
		(namespace != componentKagent && !strings.HasPrefix(namespace, organizationNamespacePrefix)) {
		return fmt.Errorf("%w: %s: %q is neither an agent's name in kagent nor <organization namespace>/<name>, an organization namespace being %s<organization>-<suffix>", ErrPolicy, field, agent, organizationNamespacePrefix)
	}
	return nil
}
