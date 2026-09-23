package main

import (
	"context"
	"errors"
	"io"

	"github.com/spf13/cobra"

	"github.com/giantswarm/giantswarm-platform-manager/internal/platformctl/update"
	"github.com/giantswarm/giantswarm-platform-manager/internal/version"
)

func newVersionCmd() *cobra.Command {
	return leaf("version", "Print this binary's version", func(_ []string, stdout, _ io.Writer) int {
		say(stdout, "%s\n", version.String())
		return exitOK
	})
}

// newSelfUpdateCmd replaces the running binary with the latest release once
// its signature verifies — the command muster, agentlab and mcp-kubernetes
// ship as well — or, with --check, only says whether one exists. The work
// lives in internal/platformctl/update.
func newSelfUpdateCmd(u *update.Updater) *cobra.Command {
	var check bool
	cmd := leaf("self-update [--check]", "Install the latest release over this binary once its signature verifies", func(pos []string, stdout, stderr io.Writer) int {
		if len(pos) != 0 {
			return usageError(stderr, "self-update [--check] takes no arguments")
		}
		err := u.Run(context.Background(), stdout, check)
		switch {
		case errors.Is(err, update.ErrOutdated):
			// --check has reported both versions; the exit code is the
			// answer.
			return exitOutdated
		case err != nil:
			return fail(stderr, err)
		}
		return exitOK
	})
	cmd.Long = `Looks up the latest release of ` + update.Repository + ` on GitHub and,
when it is newer than this binary, installs its ` + update.Binary + `-<os>-<arch> over the
running executable. --check only reports both versions (exit code 125 when a
newer release exists).

Release binaries are signed in CircleCI (cosign, keyless) and published next to
their Sigstore bundle. The downloaded binary is installed only after that
bundle verifies for a CircleCI build of ` + update.Repository + `; a release
without a bundle, or a download that does not match its signature, is refused
and the installed binary stays as it is.

A binary without a release version (` + "`platformctl version`" + ` says dev) is refused:
install it from a release or with ` + "`go install " + update.Module + "@latest`" + `.
Every other command prints a one-line hint on stderr while a newer release is
out; ` + update.OptOutEnv + `=1 silences it.`
	cmd.Flags().BoolVar(&check, "check", false, "report the running and the latest release without installing anything; exit code 125 when a newer one exists")
	return cmd
}
