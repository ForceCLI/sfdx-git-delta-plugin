package delta

import "fmt"

// messages are the texts of upstream's messages/delta.md, with %s for each
// value.
var messages = map[string]string{
	"error.ParameterIsNotGitSHA":              "--%s is not a valid sha pointer: '%s' (If in CI/CD context, check the fetch depth is properly set)",
	"error.ParameterIsNotCommit":              "--%s must resolve to a commit: '%s' resolves to a %s. Use a commit sha, a branch, or a tag that points to a commit",
	"error.MergeBaseNotFound":                 "no merge base found between --from '%s' and --to '%s' (If in CI/CD context, check the fetch depth is properly set)",
	"error.PathIsNotGit":                      "'%s' is not a git repository",
	"error.ChangesManifestNotAFile":           "--changes-manifest must point to a file path, '%s' already exists and is not a regular file",
	"error.ChangesManifestStatFailed":         "--changes-manifest: cannot inspect '%s': %s",
	"error.SourceDirIsEmpty":                  "--source-dir does not accept an empty value; use '.' to scope the whole repository (received: '%s')",
	"error.SourceDirIsAbsolute":               "--source-dir must be a path relative to --repo-dir, not an absolute path (received: '%s')",
	"error.SourceDirEscapesRepository":        "--source-dir does not accept '..' path segments; use a literal repository-relative path instead (received: '%s')",
	"error.SourceDirContainsWildcard":         "--source-dir does not accept wildcards (*, ?, [); repeat --source-dir once per folder, or use --include-file/--ignore-file to filter by pattern (received: '%s')",
	"error.SourceDirUsesPathspecMagic":        "--source-dir does not accept git pathspec magic (e.g. ':(exclude)', ':!'); use a literal repository-relative path instead (received: '%s')",
	"warning.ApiVersionDefaulted":             "No API version found (no --api-version flag, no sourceApiVersion in sfdx-project.json at --to), using '%s'",
	"error.ApiVersionRetrievalFailed":         "Unable to resolve the Salesforce API version. Provide one with --api-version, or set \"sourceApiVersion\" in sfdx-project.json at --to. Caused by: %s",
	"error.ApiVersionLookupUnusable":          "The appexchange org returned '%s', which is not a usable Salesforce API version",
	"error.SfdxProjectUnusable":               "Unable to resolve the Salesforce API version at '%s'. Provide one with --api-version. Caused by: %s",
	"warning.MalformedXML":                    "could not process '%s', please ensure it is properly formatted xml in both '%s' and '%s' revision",
	"warning.FlowDeleted":                     "Attempt to delete the flow '%s' via destructiveChanges.xml may not work as expected (see https://github.com/scolladon/sfdx-git-delta#handle-flow-deletion)",
	"warning.DigitalExperienceBundleDeletion": "Deleting the DigitalExperienceBundle '%s' via destructiveChanges.xml requires the related Experience site to be deactivated first for the deployment to succeed",
	"warning.SourceDirMatchedNothing":         "No changes found under '%s' between '%s' and '%s'",
	"info.CommandIsRunning":                   "Generating incremental package",
	"info.CommandSuccess":                     "Success",
	"info.CommandFailure":                     "Failure",
	"warning.IgnoredMoveCheckSkipped":         "Could not read the file list at '%s', so %s component(s) moved into an ignored directory could not be confirmed as moves and are reported as deletions. Check that --to resolves to a readable commit.",
	"warning.PureMoveCheckSkipped":            "Could not read the package directories or the file list at '%s' and '%s', so moved components are deployed as if they had changed. Caused by: %s",
	"warning.TreeIndexUnavailable":            "Could not build the file tree index for '%s', so components that still exist there may be listed in destructiveChanges.xml as deleted. Check that the commit's tree objects are present locally (a partial clone created with --filter may not have them).",
	"warning.UndeletableComponentsOmitted":    "The Metadata API cannot delete '%s' components, so destructiveChanges.xml omits '%s'. Remove them from the target org manually in Setup.",
	"warning.OutputWriteFailed":               "Unable to write '%s' in the output directory. Caused by: %s",
}

// Message returns the text for a key with the values put in.
func Message(key string, values ...string) string {
	return message(key, values...)
}

func message(key string, values ...string) string {
	args := make([]any, len(values))
	for i, value := range values {
		args[i] = value
	}
	return fmt.Sprintf(messages[key], args...)
}
