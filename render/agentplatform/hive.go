package agentplatform

import "github.com/giantswarm/giantswarm-platform-manager/render"

// The portal's Hive section: Plans, Roadmap and the product magazine at
// /hive. The plans backend reads the plan repositories and the magazine as
// the signed-in person through a GitHub MCP server on this installation's
// muster that holds the person's own grant; the roadmap backend runs pro's
// board tools on the same muster with the person's grant at pro. Both
// forward the person's ID token from this installation's Dex, so the portal
// is the installation's own and its muster registers both servers
// (installation.mcpServers): the section carries no credential. The pages
// are disabled in the app until a portal lists them, so on a rendered portal
// the fragment includes the shared list with them; a hand-kept portal lists
// them itself.

// Hive is the person's choice of the section: whether the fragment carries
// it, the plan repositories, the magazine's repository, and the board with
// its default teams.
type Hive struct {
	Enabled bool `json:"enabled"`
	Plans   struct {
		Repositories []string `json:"repositories"`
	} `json:"plans"`
	Magazine struct {
		Repository string `json:"repository"`
	} `json:"magazine"`
	Roadmap struct {
		Board string   `json:"board"`
		Teams []string `json:"teams"`
	} `json:"roadmap"`
}

// hive says whether the portal section carries the Hive: the person's choice;
// checkHive has refused it where the installation cannot serve it.
func (in *Input) hive() bool { return in.Hive.Enabled }

// boardServer is the registered server that serves the roadmap board, the
// first on record; nil without one.
func (in *Input) boardServer() *RegisteredServer {
	for i := range in.Installation.MCPServers {
		if s := &in.Installation.MCPServers[i]; s.Board {
			return s
		}
	}
	return nil
}

// grantServer is the registered server that holds the person's own GitHub
// grant, the first on record (githubGrantServer); nil without one.
func (in *Input) grantServer() *RegisteredServer {
	for i := range in.Installation.MCPServers {
		if s := &in.Installation.MCPServers[i]; s.GitHubGrant {
			return s
		}
	}
	return nil
}

// checkHive refuses the Hive where the installation cannot serve it: its
// portal is not its own — a sibling's portal signs people in at the
// sibling's Dex, whose tokens this muster does not trust —, its muster
// registers no grant server or no board server, or no plan repository is
// named.
func (in *Input) checkHive() error {
	if !in.hive() {
		return nil
	}
	if p := in.hostedPortal(); p == nil || p.Installation != in.Installation.Name {
		return refuse(describe("hive.enabled") + " asks for the Hive pages in the developer portal hosted on this installation, and the record lists no portal hosted on it; the pages read GitHub and the board through this installation's muster with its Dex's ID tokens")
	}
	if in.grantServer() == nil {
		return refuse(describe("hive.enabled") + " reads the plan repositories as the signed-in person through a registered server that holds the person's GitHub grant, and installation.mcpServers lists none with githubGrant; register a GitHub MCPServer at GitHub's authorization server with grantScope subject under management-clusters/" + in.Installation.Name + "/extras/agent-platform/mcpservers/")
	}
	if in.boardServer() == nil {
		return refuse(describe("hive.enabled") + " reads the roadmap board as the signed-in person through pro, and installation.mcpServers lists no server labelled muster.giantswarm.io/type: mcp-pro; register pro's MCPServer under management-clusters/" + in.Installation.Name + "/extras/agent-platform/mcpservers/")
	}
	if len(in.Hive.Plans.Repositories) == 0 {
		return refuse(describe("hive.plans.repositories") + " is empty, and the plans backend serves nothing without a plan repository; name at least one as owner/repo")
	}
	return nil
}

// hiveMuster is a backend's muster block: this installation's muster, the
// server and its tool prefix where it differs from the name.
func (in *Input) hiveMuster(s *RegisteredServer) render.Map {
	m := render.Map{e("installation", in.Installation.Name), e("server", s.Name)}
	if s.ToolPrefix != "" && s.ToolPrefix != s.Name {
		m = append(m, e("toolPrefix", s.ToolPrefix))
	}
	return m
}

// hiveSection is the fragment's plans and roadmap blocks.
func (in *Input) hiveSection() render.Map {
	plans := render.Map{e("repositories", in.Hive.Plans.Repositories)}
	if repo := in.Hive.Magazine.Repository; repo != "" {
		plans = append(plans, e("magazine", render.Map{e("repository", repo)}))
	}
	plans = append(plans, e("muster", in.hiveMuster(in.grantServer())))
	roadmap := render.Map{e("board", in.Hive.Roadmap.Board)}
	if len(in.Hive.Roadmap.Teams) > 0 {
		roadmap = append(roadmap, e("teams", in.Hive.Roadmap.Teams))
	}
	roadmap = append(roadmap, e("muster", in.hiveMuster(in.boardServer())))
	return render.Map{e("plans", plans), e("roadmap", roadmap)}
}
