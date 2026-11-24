// sfdx-git-delta is a plugin for the sf-compatible mode of the force CLI.
// It provides the command "sgd source delta", which compares two git
// revisions of a Salesforce project and writes the package manifests for
// what changed between them.
package main

import (
	"os"

	"github.com/ForceCLI/force/sfplugin"
	"github.com/ForceCLI/sfdx-git-delta-plugin/cmd"
)

func main() {
	sfplugin.Main(cmd.Manifest(), func(args []string) int {
		return cmd.Run(args, os.Stdout, os.Stderr)
	})
}
