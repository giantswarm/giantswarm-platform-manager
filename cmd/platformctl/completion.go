package main

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
)

// Shell completion is cobra's: `platformctl completion bash|zsh|fish|powershell`
// prints the script, the script asks the hidden `platformctl __complete` for
// the words. Subcommands and flags complete by themselves; what follows are
// the positional arguments and flag values, all from what this binary knows —
// the registry's capabilities — never from a call: an installation's or an
// action's name would need the manager and a sign-in, so they complete to
// nothing.

// capabilities completes a capability's name, with its description, from the
// registry this binary was built with.
func capabilities(toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	var out []cobra.Completion
	for _, c := range installations.Capabilities() {
		if strings.HasPrefix(c.Name, toComplete) {
			out = append(out, cobra.CompletionWithDesc(c.Name, c.Description))
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// capabilityAt completes a capability's name where at says the positional
// arguments so far (pos) are followed by one, and nothing elsewhere.
func capabilityAt(at func(cmd *cobra.Command, pos []string) bool) cobra.CompletionFunc {
	return func(cmd *cobra.Command, pos []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		if at(cmd, pos) {
			return capabilities(toComplete)
		}
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
}

// dirs completes a directory.
func dirs(*cobra.Command, []string, string) ([]cobra.Completion, cobra.ShellCompDirective) {
	return nil, cobra.ShellCompDirectiveFilterDirs
}

// completeFlag registers fn as the completion of cmd's flag name. The flag
// is registered right before and registered once, so the registration
// cannot fail.
func completeFlag(cmd *cobra.Command, name string, fn cobra.CompletionFunc) {
	_ = cmd.RegisterFlagCompletionFunc(name, fn)
}
