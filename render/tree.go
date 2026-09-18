package render

import (
	"path"
	"sort"
	"strings"
)

// The entries of a Tree beside the rendered files: IncludesFile lists the
// shared kustomization entries the Result asks for, one per line as
// "<owner>/<repo>:<path> <list> <resource>" where <list> is resources or
// components, sorted; ProbesFile and ActionsFile carry the probes and the
// customer actions as YAML data.
const (
	IncludesFile = "includes.txt"
	ProbesFile   = "probes.yaml"
	ActionsFile  = "actions.yaml"
)

// Tree is the Result as one directory tree: every rendered file under
// <owner>/<repo>/<repository-relative path>, plus IncludesFile, ProbesFile and
// ActionsFile. The golden filesets and `platformctl template` are both this
// tree, so they agree byte for byte.
func (r *Result) Tree() map[string][]byte {
	out := make(map[string][]byte, r.Len()+3)
	for repo, files := range r.Files {
		for p, f := range files {
			out[path.Join(string(repo), p)] = f.Content
		}
	}
	includes := make([]string, 0, len(r.Includes))
	for _, inc := range r.Includes {
		list := "resources"
		if inc.Component {
			list = "components"
		}
		includes = append(includes, string(inc.Repository)+":"+inc.Path+" "+list+" "+inc.Resource)
	}
	sort.Strings(includes)
	out[IncludesFile] = []byte(strings.Join(includes, "\n") + "\n")
	out[ProbesFile] = MustYAML(r.Probes)
	out[ActionsFile] = MustYAML(r.Actions)
	return out
}
