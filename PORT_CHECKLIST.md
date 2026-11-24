# sfdx-git-delta Go Port Checklist

Upstream: `~/git/sfdx-git-delta` at tag v7.7.2 (`76de674`). `DESIGN.md` there is the specification.

## Notes For The Port

- `registry/data/metadataRegistry.json` is copied from `@salesforce/source-deploy-retrieve` 13.13.0 (`lib/src/registry/metadataRegistry.json`, Apache-2.0).
- `registry/data/internalRegistry.json` is generated from upstream `src/metadata/internalRegistry.ts`; an explicit `undefined` there is written as `null` because it overrides the SDR value in the merge.
- Upstream iterates JS objects and Maps in insertion order. The Go code keeps ordered slices wherever the order decides a result.
- `go.mod` requires `github.com/ForceCLI/force` for the `sfplugin` package; A local, uncommitted `go.work` (`go work init . ../force`) is needed until a force release contains it. Add dependencies with `go get`, not `go mod tidy`, until then.

## Packages

The pipeline packages of the plan (`handlers`, `moves`, `changeset`, `output`, `postprocess`, `config`) are one package, `delta`, because they refer to each other in both directions upstream.

- [x] `registry`: SDR conversion, internal registry merge, additional registry, type lookup, fully qualified names
- [x] `gitadapter`: go-git revision resolution, merge base, tree index, blob reads, LFS, grep, diff lines
- [x] `ignore`: gitignore matching (own implementation; rules of the npm `ignore` package, not its code)
- [x] `xmldiff`: txml-compatible reader, writer, streaming diff
- [x] `delta`: 18 handlers and the factory
- [x] `delta`: cancellation, pure moves, unions, renames
- [x] `delta`: change model, package.xml, destructiveChanges.xml, changes manifest, copy executor
- [x] `delta`: flow translations, include files
- [x] `delta`: validation, sfdx-project.json, API version, source dirs
- [x] `cmd`: flags, messages, `--json`, exit codes, plugin manifest

## Parity Tests

- [x] byteEquality fixtures (17 cases, with and without generateDelta)
- [ ] handler table from `__tests__/integration/services.test.ts` (not transcribed)
- [x] e2e branches `origin/e2e/base` → `origin/e2e/head` (119 expected files, byte-identical)
- [x] CLI cases from `__tests__/nut/delta.nut.ts`

## Release

- [x] GitHub Actions publish workflow (written, not run)
- [x] README
- [X] Raise the force version in `go.mod` once a force release contains `sfplugin` and `plugins publish`
- [x] Module path `github.com/ForceCLI/sfdx-git-delta-plugin` confirmed
