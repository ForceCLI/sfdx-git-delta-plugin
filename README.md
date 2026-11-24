# sfdx-git-delta-plugin

A Go port of [sfdx-git-delta](https://github.com/scolladon/sfdx-git-delta) 7.7.2, built as a plugin for the sf-compatible mode of the [force CLI](https://github.com/ForceCLI/force). It provides the command `sf sgd source delta`, which compares two git revisions of a Salesforce project and writes:

- `package/package.xml`: the components that were added or changed.
- `destructiveChanges/destructiveChanges.xml` and an empty `destructiveChanges/package.xml`: the components that were deleted.
- With `--generate-delta`, the changed source files, so that the output directory can be deployed.
- With `--changes-manifest`, a JSON file that groups the changed components into `add`, `modify`, `delete` and `rename`.

The plugin is one executable. It reads the git repository in-process with go-git and needs neither Node.js nor the `git` program. It makes no requests to a Salesforce org.

## Install

```
sf plugins install sfdx-git-delta
```

`sf` here is the force CLI running in sf-compatible mode (the `force` binary under the name `sf`, or `force --sf`). The name resolves to `ghcr.io/forcecli/sf-plugins/sfdx-git-delta:latest`.

To use a local build:

```
go build -o sfdx-git-delta .
sf plugins link ./sfdx-git-delta
```

The executable can also be run directly: `./sfdx-git-delta sgd source delta --from HEAD~1`.

## Usage

```
sf sgd source delta --from origin/main --output-dir incremental
sf sgd source delta --from origin/main --generate-delta --output-dir incremental
sf sgd source delta --from main --merge-base --output-dir incremental
```

| Flag | Short | Default | Meaning |
|---|---|---|---|
| `--from` | `-f` | required | Revision the comparison starts from. |
| `--to` | `-t` | `HEAD` | Revision the comparison ends at. |
| `--merge-base` | `-b` | off | Compare from the merge base of `--from` and `--to`. |
| `--generate-delta` | `-d` | off | Also write the changed source files to the output directory. |
| `--output-dir` | `-o` | `./output` | Where the manifests and files are written. A directory given with the flag must exist. |
| `--repo-dir` | `-r` | `./` | The git repository. |
| `--source-dir` | `-s` | `./` | Source folder relative to `--repo-dir`. Repeat the flag for several folders. Wildcards, pathspec magic, absolute paths, `..` and the empty string are refused. |
| `--ignore-file` | `-i` | | File of gitignore patterns for paths to leave out. |
| `--ignore-destructive-file` | `-D` | the ignore file | File of patterns for deletions to leave out. |
| `--include-file` | `-n` | | File of patterns for paths to list as added whether or not they changed. |
| `--include-destructive-file` | `-N` | | File of patterns for paths to list as deleted. |
| `--additional-metadata-registry` | `-M` | | JSON file with definitions of metadata types the built-in registry lacks. |
| `--changes-manifest` | `-c` | | Path of the JSON changes file. Without a value: `changes.manifest.json` in the output directory. Turns on rename detection. |
| `--ignore-whitespace` | `-W` | off | Leave out files whose only changes are white space or blank lines. |
| `--api-version` | `-a` | | API version for the manifests. Also read from `SF_ORG_API_VERSION`. |
| `--json` | | off | Print `{"status", "result": {"output-dir", "error"}, "warnings"}`. |

The former flag names `--output`, `--repo`, `--source`, `--ignore`, `--ignore-destructive`, `--include` and `--include-destructive` are accepted with a deprecation warning.

The API version is taken from `--api-version`, then `SF_ORG_API_VERSION`, then `sourceApiVersion` in `sfdx-project.json` at `--to`. When none of them gives a version, the plugin reads the list of API versions from `https://appexchange.salesforce.com/services/data` without authentication, uses the highest one and prints a warning, as upstream does. This is the only network request the plugin makes; give an API version to avoid it.

Exit status: 0 when the comparison ran, including when a warning was printed; 1 when it failed or a path given to a flag does not exist; 2 for a missing or unknown flag.

## Parity With Upstream

The port follows upstream's `DESIGN.md` and source module by module. Three sets of tests compare its output with upstream's:

- `xmldiff/testdata/byteEquality`: upstream's 17 fixtures for the element-by-element XML comparison (two revisions of a file, the expected manifest entries and the exact bytes of the pruned file). All pass.
- `delta/e2e_test.go`: upstream's end-to-end fixture. It runs the comparison between the branches `origin/e2e/base` and `origin/e2e/head` of an sfdx-git-delta checkout and requires the 119 files under `expected/` to be byte-identical. It passes. The test needs a checkout at `~/git/sfdx-git-delta`, or at the path in `SGD_UPSTREAM_REPO`, and is skipped without one.
- `cmd/cmd_test.go`: the exit statuses and messages of upstream's command-line tests.

Known differences:

| Area | Upstream | This port |
|---|---|---|
| Ignore and include files | npm package `ignore` 7 | Its own gitignore matcher with the same rules: case-insensitive, last match wins, a path below an ignored directory stays ignored. Unusual patterns can match differently. |
| Git access | `@scolladon/tsgit` | go-git. Rename detection (with `--changes-manifest`) is go-git's and can pair files differently when content also changed. Renames never change the two XML manifests. |
| `--ignore-whitespace` | tsgit's diff options | A file is unchanged when its lines are equal after all white space is removed and empty lines are dropped. |
| Console output | Spinner, sponsorship note | One line: `Generating incremental package... Success` or `... Failure: <message>`. Warnings on stderr. |
| `--flags-dir` | Supported by sf | Not supported. |
| Additional registry errors | zod messages | Same checks, different wording. |
| Large files | Streamed above 1 MB | Read into memory. |
| Concurrency | Up to 6 handlers at a time | One at a time, in diff order. |
| A revision written as `<rev>:<path>` | Resolved by tsgit | Recognized only to report that it is a tree or a file, not a commit. |

Not yet done: the handler table of upstream's `__tests__/integration/services.test.ts` has not been transcribed into Go tests. `PORT_CHECKLIST.md` tracks this.

## Development

```
go test ./...
go vet ./...
```

`go.mod` requires `github.com/ForceCLI/force` for the `sfplugin` package, which defines the plugin protocol. Until a force release contains `sfplugin`, the plugin builds only against a checkout of force: create a `go.work` (not committed) with `go work init . ../force`. After such a release, raise the version in `go.mod`.

| Package | Content |
|---|---|
| `registry` | Metadata type definitions: the registry of `@salesforce/source-deploy-retrieve` 13.13.0 (`registry/data/metadataRegistry.json`, Apache-2.0) with upstream's internal registry applied over it, and the mapping from a path to a type and a component name. |
| `gitadapter` | Revisions, trees, file contents, Git LFS objects, content search and diff lines, on go-git. |
| `ignore` | gitignore pattern matching. |
| `xmldiff` | A port of the txml parser, the XML reader and writer, and the two-pass comparison of the child elements of two revisions. |
| `delta` | The pipeline: configuration checks, diff gathering, move and rename resolution, the handlers for each kind of metadata type, the change set, the copy executor and the manifest writers. |
| `cmd` | Flags, messages, `--json` output and exit statuses. |

`registry/data/SOURCE` names where the two registry files come from.

## Publishing

`.github/workflows/publish.yml` builds the executables for Linux, macOS and Windows on amd64 and arm64 when a tag `v*` is pushed, and publishes them to `ghcr.io/forcecli/sf-plugins/sfdx-git-delta` under the tag name and under `latest` with `force --sf plugins publish`. It needs a force release that has the `plugins publish` command.

## License

MIT, as upstream; see `LICENSE` and `LICENSE-sfdx-git-delta.md`. The metadata registry file is licensed under Apache-2.0; see `registry/data/LICENSE-source-deploy-retrieve.txt`.
