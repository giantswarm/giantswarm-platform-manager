package customerportal

import (
	"strconv"
	"strings"
	"time"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// The portal's GitHub App is the installation's own: one App per
// installation, never one shared between installations of different owners
// (the dex-operator model). Its name derives from the installation, the
// secret tooling creates it from the manifest giantswarm-portal declares and
// writes its credentials into the vault item of the App's name, and the
// commit takes them from there by reference; the installation's record keeps
// the App's name, id and creation date under githubApps.customerPortal.
const (
	// appPurpose is the App's key under githubApps in the record.
	appPurpose = "customerPortal"
	// appManifest is the App manifest the secret tooling creates every
	// installation's portal App from.
	appManifest = "giantswarm-portal"
)

// appVaultFields maps each supplied field of the App to the field the
// secret tooling writes it under in the App's vault item.
var appVaultFields = []struct{ field, vault string }{
	{fieldGitHubAppID, "app-id"},
	{fieldGitHubClientID, "client-id"},
	{fieldGitHubClientSecret, "client-secret"},
	{fieldGitHubPrivateKey, "private-key"},
	{fieldGitHubWebhookSecret, "webhook-secret"},
}

// today is the date a commit learns an App on; a test sets it.
var today = func() string { return time.Now().UTC().Format(time.DateOnly) }

// GitHubAppName is the portal's GitHub App on an installation:
// giantswarm-portal-<installation>, its first generation. Each successor the
// secret tooling creates carries the next generation's suffix, -2, -3, …, and
// its credentials in a vault item of that name beside the old one.
func GitHubAppName(installation string) string { return appManifest + "-" + installation }

// appName is the App the portal runs on: the generation the record names,
// else the first.
func (in *Input) appName() string {
	base := GitHubAppName(in.Installation.Name)
	if name := in.appOnRecord().Name; name == base || generation(base, name) > 1 {
		return name
	}
	return base
}

// successor is the App that replaces name: the next generation.
func (in *Input) successor(name string) string {
	base := GitHubAppName(in.Installation.Name)
	return base + "-" + strconv.Itoa(max(generation(base, name), 1)+1)
}

// generation is name's generation of the App base: 1 for base itself, n for
// base-<n>, 0 for a name of no generation of it.
func generation(base, name string) int {
	if name == base {
		return 1
	}
	n, err := strconv.Atoi(strings.TrimPrefix(name, base+"-"))
	if !strings.HasPrefix(name, base+"-") || err != nil || n < 2 {
		return 0
	}
	return n
}

// appSecrets are the commit's --secret references to every credential of the
// App name in its vault item.
func appSecrets(name string) []string {
	secrets := make([]string, 0, len(appVaultFields))
	for _, f := range appVaultFields {
		secrets = append(secrets, "--secret "+f.field+"=beekeeper:op://<vault>/"+name+"/"+f.vault)
	}
	return secrets
}

// ownsApp says whether the render runs the portal on a GitHub App of the
// installation's own: the GitHub integration is on, and the portal is not
// the hub's, kept by hand with credentials the manager does not write.
func (in *Input) ownsApp() bool { return in.Plugins.GitHub.Enabled && !in.HubPortal }

// appOnRecord is the record's entry of the portal's App; zero without one.
func (in *Input) appOnRecord() render.GitHubApp {
	if in.Installation.GitHubApps.CustomerPortal == nil {
		return render.GitHubApp{}
	}
	return *in.Installation.GitHubApps.CustomerPortal
}

// GitHubApps is the portal's App as the plan names it: its name, what the
// record holds of it, the secret tooling's step that creates it (and the
// one that creates its successor) and the commit's references to its
// credentials in the App's vault item. None where the portal runs on no App
// of the installation's own.
func (in *Input) GitHubApps() []render.AppSpec {
	if !in.ownsApp() {
		return nil
	}
	name := in.appName()
	create := "beekeeper app create " + appManifest + " --installation " + in.Installation.Name + " --base-domain " + in.Installation.BaseDomain
	fields := make([]string, 0, len(appVaultFields))
	for _, f := range appVaultFields {
		fields = append(fields, f.field)
	}
	successor := in.successor(name)
	return []render.AppSpec{{Purpose: appPurpose, Name: name, OnRecord: in.appOnRecord(), IDField: fieldGitHubAppID, Fields: fields,
		Create: create, Secrets: appSecrets(name), Replace: create + " --replace", Successor: successor, SuccessorSecrets: appSecrets(successor)}}
}

// appRecord is the record's entry of the portal's App as this render writes
// it into installations/<name>/config.yaml.patch, every other key of the
// record kept. The id is the one supplied at commit; the creation date the
// one on record while that id stands, else the commit's; a new id over one on
// record is the App's successor, recorded under the next generation's name. Where no id is
// supplied — a dry run's marker, or a commit whose App credentials stand on
// record — the entry on record stands, and without one the entry names the
// App alone: no commit learned its id, and the credentials file on record
// says whether the commit asks for it.
func (in *Input) appRecord(secrets map[string]string) render.File {
	onRecord := in.appOnRecord()
	name := in.appName()
	id := render.AppID(secrets[fieldGitHubAppID])
	if id > 0 && onRecord.ID > 0 && id != onRecord.ID {
		name = in.successor(name)
	}
	entry := render.Map{e("name", name)}
	switch {
	case id == 0 && onRecord.ID == 0:
		// The App's name alone: no id was learned.
	case id == 0:
		entry = append(entry, e("id", onRecord.ID))
		if onRecord.Created != "" {
			entry = append(entry, e("created", onRecord.Created))
		}
	case id == onRecord.ID && onRecord.Created != "":
		entry = append(entry, e("id", id), e("created", onRecord.Created))
	default:
		entry = append(entry, e("id", id), e("created", today()))
	}
	return render.File{Content: render.MustYAML(render.Map{e(render.GitHubAppsKey, render.Map{e(appPurpose, entry)})})}
}
