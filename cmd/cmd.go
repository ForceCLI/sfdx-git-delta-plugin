// Package cmd is the command line of the plugin: it parses the flags of
// "sgd source delta", runs the comparison and prints the result in the
// format of the Salesforce CLI.
package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/spf13/pflag"

	"github.com/ForceCLI/force/sfplugin"
	"github.com/ForceCLI/sfdx-git-delta-plugin/delta"
)

// Version is the version of the plugin.  It follows the version of
// sfdx-git-delta whose behavior the plugin reproduces.
var Version = "7.7.2"

// CommandID is the id of the command the plugin provides.
const CommandID = "sgd source delta"

const (
	summary     = "Generate incremental package manifest and source content"
	description = "Use two git commit reference to generate the package corresponding to what has changed in between"
	commandName = "SourceDeltaGenerate"
	// bareChangesManifest stands for --changes-manifest given without a
	// value, which means changes.manifest.json in the output directory.
	bareChangesManifest = "__SGD_CHANGES_MANIFEST_DEFAULT__"
)

// Manifest returns the plugin manifest.
func Manifest() sfplugin.Manifest {
	return sfplugin.Manifest{
		Name:     "sfdx-git-delta",
		Version:  Version,
		Protocol: sfplugin.ProtocolVersion,
		Commands: []sfplugin.Command{{ID: CommandID, Summary: summary, Org: sfplugin.OrgNone}},
		Topics: []sfplugin.Topic{
			{ID: "sgd", Summary: "Commands of sfdx-git-delta"},
			{ID: "sgd source", Summary: "Commands that compare source between git revisions"},
		},
	}
}

// result is the value printed with --json.
type result struct {
	OutputDir string `json:"output-dir"`
	Error     string `json:"error,omitempty"`
}

// deprecatedAliases maps a former flag name to the current one.
var deprecatedAliases = map[string]string{
	"output":              "output-dir",
	"repo":                "repo-dir",
	"source":              "source-dir",
	"ignore":              "ignore-file",
	"ignore-destructive":  "ignore-destructive-file",
	"include":             "include-file",
	"include-destructive": "include-destructive-file",
}

var shortNames = map[string]string{
	"output-dir":               "o",
	"repo-dir":                 "r",
	"source-dir":               "s",
	"ignore-file":              "i",
	"ignore-destructive-file":  "D",
	"include-file":             "n",
	"include-destructive-file": "N",
}

// usageError is a problem with the command line.  The command exits with
// status 2 for a flag that is missing or unknown, and 1 for a flag whose
// value is refused.
type usageError struct {
	message  string
	exitCode int
}

func (e *usageError) Error() string {
	return e.message
}

// expandBareChangesManifest adds the marker value after --changes-manifest
// or -c when no value follows it.
func expandBareChangesManifest(args []string) []string {
	var expanded []string
	for i, arg := range args {
		expanded = append(expanded, arg)
		if arg == "--changes-manifest" || arg == "-c" {
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				expanded = append(expanded, bareChangesManifest)
			}
		}
	}
	return expanded
}

// stripCommandWords removes the words of the command id from the start of
// the arguments, in the space form or the colon form.
func stripCommandWords(args []string) []string {
	words := strings.Fields(CommandID)
	if len(args) > 0 && args[0] == strings.Join(words, ":") {
		return args[1:]
	}
	if len(args) >= len(words) && strings.Join(args[:len(words)], " ") == CommandID {
		return args[len(words):]
	}
	return args
}

type options struct {
	input    delta.Input
	json     bool
	help     bool
	warnings []string
}

func newFlagSet() (*pflag.FlagSet, *options, map[string]*string) {
	flags := pflag.NewFlagSet(CommandID, pflag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.SortFlags = false
	parsed := &options{}
	config := &parsed.input.Config
	flags.StringVarP(&config.From, "from", "f", "", "commit sha from where the diff is done")
	flags.StringVarP(&config.To, "to", "t", "HEAD", "commit sha to where the diff is done")
	flags.BoolVarP(&config.MergeBase, "merge-base", "b", false, "diff from the merge base of --from and --to instead of --from itself (git three-dot semantics)")
	flags.BoolVarP(&config.GenerateDelta, "generate-delta", "d", false, "generate delta files in [--output-dir] folder")
	flags.StringVarP(&config.Output, "output-dir", "o", "./output", "source package specific output")
	flags.StringVarP(&config.Repo, "repo-dir", "r", "./", "git repository location")
	flags.StringArrayVarP(&parsed.input.SourceDirs, "source-dir", "s", []string{"./"}, "source folders focus location relative to --repo-dir")
	flags.StringVarP(&config.Ignore, "ignore-file", "i", "", "file listing paths to explicitly ignore for any diff actions")
	flags.StringVarP(&config.IgnoreDestructive, "ignore-destructive-file", "D", "", "file listing paths to explicitly ignore for any destructive actions")
	flags.StringVarP(&config.Include, "include-file", "n", "", "file listing paths to explicitly include for any diff actions")
	flags.StringVarP(&config.IncludeDestructive, "include-destructive-file", "N", "", "file listing paths to explicitly include for any destructive actions")
	flags.StringVarP(&config.AdditionalMetadataRegistryPath, "additional-metadata-registry", "M", "", "file containing additional metadata definitions to add to the registry")
	flags.StringVarP(&config.ChangesManifest, "changes-manifest", "c", "", "path to a JSON file grouping changed components by kind (add, modify, delete, rename); setting this flag also enables git rename detection")
	flags.BoolVarP(&config.IgnoreWhitespace, "ignore-whitespace", "W", false, "ignore git diff whitespace (space, tab, eol) changes")
	apiVersion := flags.StringP("api-version", "a", "", `salesforce metadata API version, default to sfdx-project.json "sourceApiVersion" at --to or latest version`)
	flags.BoolVar(&parsed.json, "json", false, "Format output as json.")
	flags.BoolVarP(&parsed.help, "help", "h", false, "Show help for the command.")

	aliasValues := map[string]*string{"api-version": apiVersion}
	for alias, current := range deprecatedAliases {
		if current == "source-dir" {
			continue
		}
		aliasValues[alias] = flags.String(alias, "", "")
		flags.MarkHidden(alias)
	}
	flags.StringArray("source", nil, "")
	flags.MarkHidden("source")
	return flags, parsed, aliasValues
}

func usage(flags *pflag.FlagSet) string {
	binName := sfplugin.BinName()
	var out strings.Builder
	out.WriteString(summary + "\n\n")
	out.WriteString("USAGE\n  $ " + binName + " " + CommandID + " -f <value> [flags]\n\n")
	out.WriteString("FLAGS\n" + flags.FlagUsagesWrapped(120) + "\n")
	out.WriteString("DESCRIPTION\n  " + description + "\n\n")
	out.WriteString("  --source-dir can be given more than once.  Each value is a literal path relative to --repo-dir:\n")
	out.WriteString("  wildcards (*, ?, [), git pathspec magic (:(...)), absolute paths, \"..\" and the empty string are refused.\n")
	out.WriteString("  A folder that does not exist gives no output, unless the diff deleted it.\n\n")
	out.WriteString("EXAMPLES\n")
	out.WriteString("  Build incremental manifest from the previous commit\n\n")
	out.WriteString("    $ " + binName + " " + CommandID + " --from \"origin/development\" --output-dir incremental\n\n")
	out.WriteString("  Build incremental manifest and source from the development branch\n\n")
	out.WriteString("    $ " + binName + " " + CommandID + " --from \"origin/development\" --generate-delta --output-dir incremental\n\n")
	out.WriteString("  Build incremental manifest from where the current branch diverged from main\n\n")
	out.WriteString("    $ " + binName + " " + CommandID + " --from \"main\" --merge-base --output-dir incremental\n")
	return out.String()
}

func mustExist(flag, value string, directory bool) error {
	if value == "" {
		return nil
	}
	info, err := os.Stat(value)
	if directory {
		if err != nil || !info.IsDir() {
			return &usageError{message: fmt.Sprintf("No directory found at %s", value), exitCode: 1}
		}
		return nil
	}
	if err != nil || info.IsDir() {
		return &usageError{message: fmt.Sprintf("No file found at %s", value), exitCode: 1}
	}
	return nil
}

func parse(args []string) (*options, *pflag.FlagSet, error) {
	flags, parsed, aliasValues := newFlagSet()
	if err := flags.Parse(expandBareChangesManifest(stripCommandWords(args))); err != nil {
		text := err.Error()
		if name, ok := strings.CutPrefix(text, "unknown flag: "); ok {
			text = "Nonexistent flag: " + name
		} else if name, ok := strings.CutPrefix(text, "unknown shorthand flag: "); ok {
			text = "Nonexistent flag: " + name
		}
		return parsed, flags, &usageError{message: text + "\nSee more help with --help", exitCode: 2}
	}
	if parsed.help {
		return parsed, flags, nil
	}
	if extra := flags.Args(); len(extra) > 0 {
		return parsed, flags, &usageError{message: fmt.Sprintf("Unexpected argument: %s\nSee more help with --help", extra[0]), exitCode: 2}
	}
	config := &parsed.input.Config

	// A former flag name sets the current flag, with a warning.
	deprecation := func(alias string) {
		current := deprecatedAliases[alias]
		parsed.warnings = append(parsed.warnings, fmt.Sprintf("The %q flag has been deprecated. Use %q instead.", "--"+alias, "--"+current+" | -"+shortNames[current]))
	}
	targets := map[string]*string{
		"output":              &config.Output,
		"repo":                &config.Repo,
		"ignore":              &config.Ignore,
		"ignore-destructive":  &config.IgnoreDestructive,
		"include":             &config.Include,
		"include-destructive": &config.IncludeDestructive,
	}
	for _, alias := range []string{"output", "repo", "ignore", "ignore-destructive", "include", "include-destructive"} {
		if flags.Changed(alias) {
			*targets[alias] = *aliasValues[alias]
			deprecation(alias)
		}
	}
	if flags.Changed("source") {
		values, _ := flags.GetStringArray("source")
		if flags.Changed("source-dir") {
			parsed.input.SourceDirs = append(parsed.input.SourceDirs, values...)
		} else {
			parsed.input.SourceDirs = values
		}
		deprecation("source")
	}

	if !flags.Changed("from") {
		return parsed, flags, &usageError{message: "The following error occurred:\n  Missing required flag from\nSee more help with --help", exitCode: 2}
	}
	apiVersion := *aliasValues["api-version"]
	if !flags.Changed("api-version") {
		apiVersion = os.Getenv("SF_ORG_API_VERSION")
	}
	if apiVersion != "" {
		digits := apiVersion
		if dot := strings.Index(digits, "."); dot >= 0 {
			digits = digits[:dot]
		}
		version, err := strconv.Atoi(strings.TrimSpace(digits))
		if err != nil || version <= 0 {
			return parsed, flags, &usageError{message: fmt.Sprintf("Parsing --api-version \n\t%s is not a valid API version. It should end in '.0' like '54.0'.\nSee more help with --help", apiVersion), exitCode: 2}
		}
		config.APIVersion = version
	}
	if config.ChangesManifest == bareChangesManifest {
		config.ChangesManifest = path.Join(strings.ReplaceAll(config.Output, `\`, "/"), "changes.manifest.json")
	}
	for _, check := range []struct {
		flag, value string
		directory   bool
	}{
		{"output-dir", config.Output, true},
		{"repo-dir", config.Repo, true},
		{"ignore-file", config.Ignore, false},
		{"ignore-destructive-file", config.IgnoreDestructive, false},
		{"include-file", config.Include, false},
		{"include-destructive-file", config.IncludeDestructive, false},
		{"additional-metadata-registry", config.AdditionalMetadataRegistryPath, false},
	} {
		// A default value is not checked, as in sf: the output directory
		// is created when it is written to.
		given := flags.Changed(check.flag)
		for alias, current := range deprecatedAliases {
			given = given || (current == check.flag && flags.Changed(alias))
		}
		if !given {
			continue
		}
		if err := mustExist(check.flag, check.value, check.directory); err != nil {
			return parsed, flags, err
		}
	}
	return parsed, flags, nil
}

func hasJSONFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if arg == "--json" || arg == "--json=true" {
			return true
		}
	}
	return false
}

// Run executes the command with the arguments after the program name and
// returns the exit status: 0 when the comparison ran, 1 when it failed, 2
// for a missing or unknown flag.
func Run(args []string, out, errOut io.Writer) int {
	parsed, flags, err := parse(args)
	jsonOutput := parsed.json || hasJSONFlag(args)
	if err != nil {
		exitCode := 1
		var usageProblem *usageError
		if errors.As(err, &usageProblem) {
			exitCode = usageProblem.exitCode
		}
		if jsonOutput {
			sfplugin.WriteError(out, "", err.Error(), exitCode, commandName, parsed.warnings)
		} else {
			fmt.Fprintf(errOut, "Error (%d): %s\n", exitCode, err.Error())
		}
		return exitCode
	}
	if parsed.help {
		fmt.Fprint(out, usage(flags))
		return 0
	}

	output := result{OutputDir: parsed.input.Config.Output}
	warnings := parsed.warnings
	exitCode := 0
	finalMessage := delta.Message("info.CommandSuccess")
	work, err := delta.Run(parsed.input)
	if err != nil {
		exitCode = 1
		output.Error = err.Error()
		finalMessage = delta.Message("info.CommandFailure") + ": " + err.Error()
	} else {
		warnings = append(warnings, work.Warnings...)
	}
	if jsonOutput {
		sfplugin.WriteResult(out, exitCode, output, warnings)
		return exitCode
	}
	for _, warning := range warnings {
		fmt.Fprintf(errOut, "Warning: %s\n", warning)
	}
	fmt.Fprintf(out, "%s... %s\n", delta.Message("info.CommandIsRunning"), finalMessage)
	return exitCode
}
