package delta

import (
	"strings"

	"github.com/ForceCLI/sfdx-git-delta-plugin/gitadapter"
	"github.com/ForceCLI/sfdx-git-delta-plugin/registry"
)

type renamePathPair struct {
	fromPath string
	toPath   string
}

type deferredDeletion struct {
	line string
	name string
}

// diffLines is the result of reading the diff between the two revisions.
type diffLines struct {
	// lines are the additions and modifications in diff order, followed
	// by the deletions that were not cancelled.
	lines []string
	// renamePairs are the renames git detected, each also present in
	// lines as a deletion and an addition.
	renamePairs []renamePathPair
	// cancelledDeletions are the deletions of components that the same
	// diff adds under another path.
	cancelledDeletions []deferredDeletion
	changesSeen        int
	linesYielded       int
	// probeFailed is true when the files of the later revision could not
	// be listed to check the components that moved under an ignored path.
	probeFailed     bool
	probeCandidates int
}

func comparisonName(metadata *registry.Repository, line string) string {
	return strings.ToLower(metadata.FullyQualifiedName(line))
}

// gatherLines reads the diff, drops the paths of unknown types and the
// ignored paths, and cancels the deletion of a component that is added by
// the same diff: a component that moved, or whose file was renamed.
//
// An ignored addition does not cancel a deletion by itself.  It does when
// the later revision has no file of the component that is not ignored: the
// component then moved to an ignored path and is not deleted.
func gatherLines(ctx *RunContext) (*diffLines, error) {
	config := ctx.Config
	raw, err := ctx.Git.DiffLines(gitadapter.DiffSpec{
		From:             config.From,
		To:               config.To,
		DetectRenames:    config.ChangesManifest != "",
		IgnoreWhitespace: config.IgnoreWhitespace,
	}, config.Source)
	if err != nil {
		return nil, err
	}
	result := &diffLines{changesSeen: raw.ChangesSeen, linesYielded: len(raw.Lines)}
	ignoreHelper := ctx.shared.ignore
	additionNames := map[string]bool{}
	heldAdditionNames := map[string]bool{}
	var deferred []deferredDeletion

	route := func(line string) {
		if line == "" || !ctx.Metadata.Has(line) {
			return
		}
		if strings.HasPrefix(line, Addition) {
			name := comparisonName(ctx.Metadata, line)
			if !ignoreHelper.Keep(line) {
				heldAdditionNames[name] = true
				return
			}
			additionNames[name] = true
			result.lines = append(result.lines, line)
			return
		}
		if !ignoreHelper.Keep(line) {
			return
		}
		if !strings.HasPrefix(line, Deletion) {
			result.lines = append(result.lines, line)
			return
		}
		deferred = append(deferred, deferredDeletion{line: line, name: comparisonName(ctx.Metadata, line)})
	}
	for _, rawLine := range raw.Lines {
		if parts := strings.Split(rawLine, "\t"); strings.HasPrefix(rawLine, Renamed) && len(parts) >= 3 {
			result.renamePairs = append(result.renamePairs, renamePathPair{fromPath: parts[1], toPath: parts[2]})
			route(lineOf(Deletion, parts[1]))
			route(lineOf(Addition, parts[2]))
			continue
		}
		route(rawLine)
	}

	candidates := map[string]bool{}
	for _, deletion := range deferred {
		if heldAdditionNames[deletion.name] && !additionNames[deletion.name] {
			candidates[deletion.name] = true
		}
	}
	vouching := map[string]bool{}
	if len(candidates) > 0 {
		index := ctx.Git.BuildTreeIndex(config.To, config.Source)
		if index == nil {
			result.probeFailed = true
			result.probeCandidates = len(candidates)
		} else {
			visible := map[string]bool{}
			for _, filePath := range index.FilesUnder(config.Source...) {
				line := lineOf(Addition, filePath)
				if !ctx.Metadata.Has(line) {
					continue
				}
				if name := comparisonName(ctx.Metadata, line); candidates[name] && ignoreHelper.Keep(line) {
					visible[name] = true
				}
			}
			for name := range candidates {
				if !visible[name] {
					vouching[name] = true
				}
			}
		}
	}
	for _, deletion := range deferred {
		if additionNames[deletion.name] {
			result.cancelledDeletions = append(result.cancelledDeletions, deletion)
		} else if !vouching[deletion.name] {
			result.lines = append(result.lines, deletion.line)
		}
	}
	return result, nil
}

// unmatchedSourceScopes returns the source directories when the diff had
// changes and none of them was in a source directory.
func (d *diffLines) unmatchedSourceScopes(source []string) []string {
	var unmatchable []string
	for _, scope := range source {
		if gitadapter.RootPaths[scope] {
			return nil
		}
		unmatchable = append(unmatchable, scope)
	}
	if len(unmatchable) > 0 && d.changesSeen > 0 && d.linesYielded == 0 {
		return unmatchable
	}
	return nil
}

// Types whose handlers list the files of the later revision.
var treeIndexXMLNames = map[string]bool{
	registry.ObjectType:            true,
	registry.DashboardType:         true,
	registry.ReportType:            true,
	"AuraDefinitionBundle":         true,
	"LightningComponentBundle":     true,
	"UIBundle":                     true,
	"GenAiFunction":                true,
	registry.PermissionSetType:     true,
	"Territory2Model":              true,
	"ExternalServiceRegistration":  true,
	registry.ObjectTranslationType: true,
}

// indexedTypeOf returns the type of a path, or its parent type for a child
// type.
func indexedTypeOf(filePath string, metadata *registry.Repository) *registry.Metadata {
	definition := metadata.Get(filePath)
	if definition == nil || definition.ParentXMLName == "" {
		return definition
	}
	return metadata.GetByXMLName(definition.ParentXMLName)
}

// scopeForType returns the path up to the first segment that is the
// directory of the type.
func scopeForType(filePath string, definition *registry.Metadata) (string, bool) {
	parts := strings.Split(filePath, "/")
	for index, part := range parts {
		if part == definition.DirectoryName {
			return strings.Join(parts[:index+1], "/"), true
		}
	}
	return "", false
}

func componentListingRoot(filePath string, metadata *registry.Repository) (string, bool) {
	definition := indexedTypeOf(filePath, metadata)
	if definition == nil {
		return "", false
	}
	return scopeForType(filePath, definition)
}

// treeIndexScope returns the type directories of the lines whose handlers
// list files, in the order they first occur.
func treeIndexScope(lines []string, metadata *registry.Repository) []string {
	seen := map[string]bool{}
	var scope []string
	for _, line := range lines {
		filePath := registry.PathOfLine(line)
		definition := indexedTypeOf(filePath, metadata)
		if definition == nil {
			continue
		}
		if !treeIndexXMLNames[definition.XMLName] && !definition.InFolder && !registry.ContentContainerAdapters[definition.Adapter] {
			continue
		}
		if root, ok := scopeForType(filePath, definition); ok && !seen[root] {
			seen[root] = true
			scope = append(scope, root)
		}
	}
	return scope
}

// resolveRenames turns the renamed paths into renamed components.  A pair
// is dropped when either side is ignored, when a side is of an unknown
// type, when the sides are of different types, or when both sides are the
// same component.
func resolveRenames(ctx *RunContext, pairs []renamePathPair) []RenameTriple {
	factory := newHandlerFactory(ctx)
	var triples []RenameTriple
	for _, pair := range pairs {
		fromLine := lineOf(Deletion, pair.fromPath)
		toLine := lineOf(Addition, pair.toPath)
		if !ctx.shared.ignore.Keep(fromLine) || !ctx.shared.ignore.Keep(toLine) {
			continue
		}
		fromHandler, err := factory.handlerFor(fromLine)
		if err != nil {
			continue
		}
		toHandler, err := factory.handlerFor(toLine)
		if err != nil {
			continue
		}
		fromType, fromMember, err := fromHandler.Descriptor()
		if err != nil {
			continue
		}
		toType, toMember, err := toHandler.Descriptor()
		if err != nil || fromType != toType || fromMember == toMember {
			continue
		}
		triples = append(triples, RenameTriple{Type: fromType, From: fromMember, To: toMember})
	}
	return triples
}
