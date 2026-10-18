package render

import (
	"strconv"
	"time"
)

// GitHubAppsKey is the installation's record's key (RecordFile) its GitHub
// Apps are kept under, one entry per purpose: the credential inventory of the
// Apps, next to the Dex clients the dex patch keeps.
const GitHubAppsKey = "githubApps"

// GitHubAppRotation is how long an installation's GitHub App runs on its
// credentials: from the date its commit learned it, the App is due a
// successor once this much time has passed.
const GitHubAppRotation = 6 // months

// GitHubApp is one GitHub App an installation runs on, as its record keeps
// it under GitHubAppsKey: the App's name, its id and the date the commit
// that supplied its credentials learned it. No credential: the id is
// configuration, and the values live in the encrypted files alone.
type GitHubApp struct {
	Name    string `json:"name" yaml:"name"`
	ID      int    `json:"id,omitempty" yaml:"id,omitempty"`
	Created string `json:"created,omitempty" yaml:"created,omitempty"`
}

// RotationDue is the date the App is due a successor — GitHubAppRotation
// months after it was created — and whether that date has come by now. An
// App without a readable creation date is never due: there is no age to
// read.
func (a GitHubApp) RotationDue(now time.Time) (string, bool) {
	created, err := time.Parse(time.DateOnly, a.Created)
	if err != nil {
		return "", false
	}
	due := created.AddDate(0, GitHubAppRotation, 0)
	return due.Format(time.DateOnly), !now.Before(due)
}

// AppID is a supplied GitHub App id as a number, 0 where it is none (a dry
// run's marker, an empty value).
func AppID(value string) int {
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 {
		return 0
	}
	return n
}

// AppSpec is a GitHub App a definition's render needs one of per
// installation, as the plan names it: the App's name and the purpose its
// record entry is kept under, what is on record of it, how the secret
// tooling creates it, and the supplied fields its credentials fill with the
// references a commit takes them by. A definition whose Input has AppSpecs
// declares them through GitHubAppsInput.
type AppSpec struct {
	// Purpose is the App's key under GitHubAppsKey in the record.
	Purpose string `json:"purpose"`
	// Name is the App's name on GitHub, the installation's own.
	Name string `json:"name"`
	// OnRecord is the record's entry; zero where the record holds none.
	OnRecord GitHubApp `json:"onRecord"`
	// IDField is the supplied field that carries the App's id; Fields are
	// every supplied field the App's credentials fill, IDField among them.
	IDField string   `json:"idField"`
	Fields  []string `json:"fields"`
	// Create is the secret tooling's step that creates the App and writes
	// its credentials into the vault item Name; Secrets are the commit's
	// --secret arguments that take every credential of the App from that
	// item, by field.
	Create  string   `json:"create"`
	Secrets []string `json:"secrets"`
	// Replace is the step that creates the App's successor, Successor, the
	// next generation of its name, with its credentials in a vault item of
	// that name beside the old one; SuccessorSecrets take them from there.
	Replace          string   `json:"replace"`
	Successor        string   `json:"successor"`
	SuccessorSecrets []string `json:"successorSecrets"`
}

// GitHubAppsInput is an Input whose render runs on GitHub Apps of the
// installation's own.
type GitHubAppsInput interface {
	GitHubApps() []AppSpec
}
