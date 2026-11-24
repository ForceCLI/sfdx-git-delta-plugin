package delta

import (
	"strconv"
	"strings"

	"github.com/ForceCLI/sfdx-git-delta-plugin/gitadapter"
	"github.com/ForceCLI/sfdx-git-delta-plugin/registry"
)

// Work is the result of a run.
type Work struct {
	Config   *Config
	Changes  *ChangeSet
	Warnings []string
}

// Run compares the two revisions and writes the manifests and, with
// GenerateDelta, the changed files below the output directory.
func Run(input Input) (*Work, error) {
	config := input.Config
	requestedFrom, requestedTo := config.From, config.To
	source, rejections := parseSourceDirs(input.SourceDirs)
	config.Source = source

	git, configWarnings, err := validate(&config, rejections)
	if err != nil {
		return nil, err
	}
	metadata, err := registry.Load(config.AdditionalMetadataRegistryPath)
	if err != nil {
		return nil, err
	}
	ctx, err := NewRunContext(&config, metadata, nil, git)
	if err != nil {
		return nil, err
	}
	diff, err := gatherLines(ctx)
	if err != nil {
		return nil, err
	}

	// The handlers list files of the revisions only below the type
	// directories the diff touches.  With include files every source
	// directory is listed.
	scopePaths := config.Source
	if config.Include == "" && config.IncludeDestructive == "" {
		scopePaths = treeIndexScope(diff.lines, metadata)
	}
	indexes := map[string]*gitadapter.TreeIndex{}
	toUnindexed := false
	if len(scopePaths) > 0 {
		if index := git.BuildTreeIndex(config.To, scopePaths); index != nil {
			indexes[config.To] = index
		} else {
			toUnindexed = true
		}
		if index := git.BuildTreeIndex(config.From, scopePaths); index != nil {
			indexes[config.From] = index
		}
	}
	ctx.Trees = gitadapter.NewTreeReader(indexes)

	moves := resolveMoves(ctx, diff.lines, diff.cancelledDeletions)
	ctx.Unions = moves.unions

	handlerResult, err := processLines(ctx, moves.lines)
	if err != nil {
		return nil, err
	}
	handlerView := NewChangeSet(handlerResult.Elements, nil)
	postResult := collectAll(ctx, handlerView)
	renames := resolveRenames(ctx, diff.renamePairs)
	assembled := assembleChanges(handlerResult, postResult, renames)
	changes := assembled.changes

	io := newExecutor(ctx)
	io.execute(assembled.copies)
	processorWarnings := executeRemaining(ctx, changes)

	warnings := append([]string{}, configWarnings...)
	warnings = append(warnings, assembled.warnings...)
	warnings = append(warnings, io.warnings...)
	warnings = append(warnings, processorWarnings...)
	if unmatched := diff.unmatchedSourceScopes(config.Source); len(unmatched) > 0 && changes.IsEmpty() {
		sanitized := make([]string, len(unmatched))
		for i, scope := range unmatched {
			sanitized[i] = sanitizeForMessage(scope)
		}
		warnings = append(warnings, message("warning.SourceDirMatchedNothing", strings.Join(sanitized, ", "), sanitizeForMessage(requestedFrom), sanitizeForMessage(requestedTo)))
	}
	if diff.probeFailed {
		warnings = append(warnings, message("warning.IgnoredMoveCheckSkipped", sanitizeForMessage(requestedTo), strconv.Itoa(diff.probeCandidates)))
	}
	if moves.skipCause != "" {
		warnings = append(warnings, message("warning.PureMoveCheckSkipped", sanitizeForMessage(requestedFrom), sanitizeForMessage(requestedTo), sanitizeForMessage(moves.skipCause)))
	}
	if toUnindexed {
		warnings = append(warnings, message("warning.TreeIndexUnavailable", sanitizeForMessage(requestedTo)))
	}
	undeletable := changes.UndeletableDeletions()
	for _, typeName := range undeletable.Types() {
		members := undeletable.Get(typeName).Members()
		sanitized := make([]string, len(members))
		for i, member := range members {
			sanitized[i] = sanitizeForMessage(member)
		}
		warnings = append(warnings, message("warning.UndeletableComponentsOmitted", typeName, strings.Join(sanitized, ", ")))
	}
	return &Work{Config: &config, Changes: changes, Warnings: warnings}, nil
}
