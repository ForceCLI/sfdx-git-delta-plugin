package delta

import (
	"sort"
	"strings"

	"github.com/ForceCLI/sfdx-git-delta-plugin/gitadapter"
	"github.com/ForceCLI/sfdx-git-delta-plugin/registry"
	"github.com/ForceCLI/sfdx-git-delta-plugin/xmldiff"
)

// revisionSide is what the move check reads of one revision.
type revisionSide struct {
	project *sfdxProject
	files   *gitadapter.Revision
}

func (s *revisionSide) entryOf(filePath string) string {
	return s.project.packageDirectoryOf(filePath)
}

type revisionView struct {
	from *revisionSide
	to   *revisionSide
}

// probe is the component a diff line belongs to.
type probe struct {
	typeName         string
	member           string
	comparesElements bool
}

// moveGroup is the deletions and additions of one component in a diff that
// deletes and adds it.
type moveGroup struct {
	name      string
	deletions []string
	additions []string
}

func (g *moveGroup) lines() []string {
	return append(append([]string{}, g.deletions...), g.additions...)
}

func pathsOf(lines []string) []string {
	paths := make([]string, len(lines))
	for i, line := range lines {
		paths[i] = registry.PathOfLine(line)
	}
	return paths
}

type candidate struct {
	deletions []string
	additions []string
	roots     []string
}

// moveResolution is the lines to process after the move check.
type moveResolution struct {
	lines     []string
	unions    map[string]*xmldiff.Outcome
	skipCause string
}

// orderedGroups groups values by key and keeps the keys in the order they
// first occur.
type orderedGroups struct {
	order  []string
	groups map[string][]string
}

func groupBy(values []string, keyOf func(string) string) *orderedGroups {
	result := &orderedGroups{groups: map[string][]string{}}
	for _, value := range values {
		key := keyOf(value)
		if _, seen := result.groups[key]; !seen {
			result.order = append(result.order, key)
		}
		result.groups[key] = append(result.groups[key], value)
	}
	return result
}

func entryCount(deletions, additions []string, view *revisionView) int {
	entries := map[string]bool{}
	for _, filePath := range deletions {
		entries[view.from.entryOf(filePath)] = true
	}
	for _, filePath := range additions {
		entries[view.to.entryOf(filePath)] = true
	}
	return len(entries)
}

// resolveMoves removes from the lines the additions of components that only
// moved: every file of the component was deleted at one place and added at
// another inside the same package directory, with the same content.  Such a
// component is neither deployed nor deleted.
//
// For the files whose elements are compared (labels, workflows, ...) it
// compares the files of one container together, so that an element moved
// from one file to another is neither added nor deleted.
//
// When the revisions cannot be read for this check, the lines are returned
// unchanged with the cause.
func resolveMoves(ctx *RunContext, lines []string, cancelled []deferredDeletion) moveResolution {
	unchanged := moveResolution{lines: lines, unions: map[string]*xmldiff.Outcome{}}
	if len(cancelled) == 0 {
		return unchanged
	}
	view, err := loadView(ctx)
	if err != nil {
		unchanged.skipCause = err.Error()
		return unchanged
	}
	resolver := &moveResolver{ctx: ctx, view: view, factory: newHandlerFactory(ctx), probes: map[string]probe{}}
	groups := resolver.groupMoves(lines, cancelled)
	resolver.probeAll(groups)
	dropped, triggers := resolver.judge(groups)
	unions, unionDropped := resolver.diffUnions(triggers, lines)
	for line := range unionDropped {
		dropped[line] = true
	}
	var kept []string
	for _, line := range lines {
		if !dropped[line] {
			kept = append(kept, line)
		}
	}
	return moveResolution{lines: kept, unions: unions}
}

func loadView(ctx *RunContext) (*revisionView, error) {
	load := func(revision string) (*revisionSide, error) {
		project, err := loadSfdxProject(ctx, revision)
		if err != nil {
			return nil, err
		}
		files, err := ctx.Git.Files(revision)
		if err != nil {
			return nil, err
		}
		return &revisionSide{project: project, files: files}, nil
	}
	from, err := load(ctx.Config.From)
	if err != nil {
		return nil, err
	}
	to, err := load(ctx.Config.To)
	if err != nil {
		return nil, err
	}
	return &revisionView{from: from, to: to}, nil
}

type moveResolver struct {
	ctx     *RunContext
	view    *revisionView
	factory *handlerFactory
	probes  map[string]probe
}

func (r *moveResolver) keeps(status, filePath string) bool {
	return r.ctx.shared.ignore.Keep(lineOf(status, filePath))
}

func (r *moveResolver) groupMoves(lines []string, cancelled []deferredDeletion) []*moveGroup {
	var groups []*moveGroup
	byName := map[string]*moveGroup{}
	seen := map[string]bool{}
	for _, deletion := range cancelled {
		group := byName[deletion.name]
		if group == nil {
			group = &moveGroup{name: deletion.name}
			byName[deletion.name] = group
			groups = append(groups, group)
		}
		if !seen[deletion.line] {
			seen[deletion.line] = true
			group.deletions = append(group.deletions, deletion.line)
		}
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, Addition) {
			continue
		}
		if group := byName[comparisonName(r.ctx.Metadata, line)]; group != nil {
			group.additions = append(group.additions, line)
		}
	}
	return groups
}

func (r *moveResolver) spansEntries(group *moveGroup) bool {
	return entryCount(pathsOf(group.deletions), pathsOf(group.additions), r.view) > 1
}

func (r *moveResolver) probeLine(line string) {
	handler, err := r.factory.handlerFor(line)
	if err != nil {
		return
	}
	typeName, member, err := handler.Descriptor()
	if err != nil {
		return
	}
	r.probes[line] = probe{typeName: typeName, member: member, comparesElements: handler.ComparesElements()}
}

// probeAll resolves the component of each line of the groups.  For a group
// whose paths are in more than one package directory only the first line
// is resolved, unless that line is of a type whose elements are compared.
func (r *moveResolver) probeAll(groups []*moveGroup) {
	var spanning []*moveGroup
	for _, group := range groups {
		if r.spansEntries(group) {
			spanning = append(spanning, group)
			continue
		}
		for _, line := range group.lines() {
			r.probeLine(line)
		}
	}
	for _, group := range spanning {
		if all := group.lines(); len(all) > 0 {
			r.probeLine(all[0])
		}
	}
	for _, group := range spanning {
		all := group.lines()
		if len(all) == 0 || !r.probes[all[0]].comparesElements {
			continue
		}
		for _, line := range all[1:] {
			r.probeLine(line)
		}
	}
}

// probesOf returns the components of the lines of a group, or false when
// one of them was not resolved.
func (r *moveResolver) probesOf(group *moveGroup) ([]probe, bool) {
	var found []probe
	for _, line := range group.lines() {
		resolved, ok := r.probes[line]
		if !ok {
			return nil, false
		}
		found = append(found, resolved)
	}
	return found, true
}

func (r *moveResolver) listingRoots(deletions, additions []string) ([]string, bool) {
	var roots []string
	for _, filePath := range append(append([]string{}, deletions...), additions...) {
		root, ok := componentListingRoot(filePath, r.ctx.Metadata)
		if !ok {
			return nil, false
		}
		roots = append(roots, root)
	}
	return roots, true
}

func (r *moveResolver) candidateOf(group *moveGroup) (*candidate, bool) {
	known, ok := r.probesOf(group)
	if !ok || len(known) == 0 {
		return nil, false
	}
	for _, resolved := range known {
		if resolved.comparesElements || resolved.typeName != known[0].typeName || resolved.member != known[0].member {
			return nil, false
		}
	}
	deletions := pathsOf(group.deletions)
	additions := pathsOf(group.additions)
	for _, filePath := range deletions {
		if !r.keeps(Addition, filePath) {
			return nil, false
		}
	}
	if entryCount(deletions, additions, r.view) != 1 {
		return nil, false
	}
	roots, ok := r.listingRoots(deletions, additions)
	if !ok {
		return nil, false
	}
	return &candidate{deletions: deletions, additions: additions, roots: roots}, true
}

// indexSide groups the files of a revision that are in a candidate root,
// in a source directory, of a known type and not ignored, by component.
func (r *moveResolver) indexSide(files *gitadapter.Revision, inCandidateRoots func(string) bool) map[string][]string {
	inSource := gitadapter.ScopeMatcher(r.ctx.Config.Source)
	index := map[string][]string{}
	for _, filePath := range files.Paths {
		if !inCandidateRoots(filePath) || !inSource(filePath) {
			continue
		}
		line := lineOf(Addition, filePath)
		if !r.ctx.Metadata.Has(line) || !r.ctx.shared.ignore.Keep(line) {
			continue
		}
		name := comparisonName(r.ctx.Metadata, line)
		index[name] = append(index[name], filePath)
	}
	return index
}

func sameSet(found, expected []string) bool {
	wanted := map[string]bool{}
	for _, filePath := range expected {
		wanted[filePath] = true
	}
	if len(found) != len(wanted) {
		return false
	}
	for _, filePath := range found {
		if !wanted[filePath] {
			return false
		}
	}
	return true
}

// commonDirectory returns the directory segments the paths share.
func commonDirectory(paths []string) string {
	var shared []string
	for index, filePath := range paths {
		segments := strings.Split(dirname(filePath), "/")
		if index == 0 {
			shared = segments
			continue
		}
		for i, segment := range shared {
			if i >= len(segments) || segments[i] != segment {
				shared = shared[:i]
				break
			}
		}
	}
	return strings.Join(shared, "/")
}

// blobIDsByRelativePath maps each path, relative to the directory the
// paths share, to its object id.  The second result is false when a path
// has no object id.
func blobIDsByRelativePath(paths []string, files *gitadapter.Revision) (map[string]string, bool) {
	root := commonDirectory(paths)
	skip := 0
	if root != "" && root != "." {
		skip = len(root) + 1
	}
	complete := true
	result := map[string]string{}
	for _, filePath := range paths {
		relative := filePath
		if skip <= len(filePath) {
			relative = filePath[skip:]
		}
		id, ok := files.ID(filePath)
		if !ok {
			complete = false
			result[relative] = ""
			continue
		}
		result[relative] = id.String()
	}
	return result, complete
}

func (r *moveResolver) sameContent(moved *candidate) bool {
	before, complete := blobIDsByRelativePath(moved.deletions, r.view.from.files)
	if !complete {
		return false
	}
	after, _ := blobIDsByRelativePath(moved.additions, r.view.to.files)
	if len(before) != len(after) {
		return false
	}
	for relative, id := range before {
		if other, ok := after[relative]; !ok || other == "" || other != id {
			return false
		}
	}
	return true
}

// unionTrigger asks for the files of one container in one package
// directory to be compared together.
type unionTrigger struct {
	scope string
	entry string
	roots []string
}

// judge returns the addition lines of the components that only moved, and
// the containers of the other groups whose elements are compared.
func (r *moveResolver) judge(groups []*moveGroup) (map[string]bool, []unionTrigger) {
	candidates := map[*moveGroup]*candidate{}
	var allRoots []string
	for _, group := range groups {
		if found, ok := r.candidateOf(group); ok {
			candidates[group] = found
			allRoots = append(allRoots, found.roots...)
		}
	}
	fromIndex, toIndex := map[string][]string{}, map[string][]string{}
	if len(candidates) > 0 {
		inCandidateRoots := gitadapter.ScopeMatcher(allRoots)
		fromIndex = r.indexSide(r.view.from.files, inCandidateRoots)
		toIndex = r.indexSide(r.view.to.files, inCandidateRoots)
	}
	dropped := map[string]bool{}
	var triggers []unionTrigger
	for _, group := range groups {
		if moved, ok := candidates[group]; ok {
			inRoots := gitadapter.ScopeMatcher(moved.roots)
			listed := func(index map[string][]string) []string {
				var result []string
				for _, filePath := range index[group.name] {
					if inRoots(filePath) {
						result = append(result, filePath)
					}
				}
				return result
			}
			// The move is complete when the listed files of the
			// component are exactly the deleted and the added files.
			if sameSet(listed(fromIndex), moved.deletions) && sameSet(listed(toIndex), moved.additions) && r.sameContent(moved) {
				for _, line := range group.additions {
					dropped[line] = true
				}
				continue
			}
		}
		triggers = append(triggers, r.triggersOf(group)...)
	}
	return dropped, triggers
}

const labelsUnionScope = "CustomLabels"

func isLabelsContainer(filePath string, metadata *registry.Repository) bool {
	definition := metadata.Get(filePath)
	if definition == nil || definition.XMLName != "CustomLabel" {
		return false
	}
	return registry.Ext(strings.Replace(registry.Base(filePath), registry.MetaFileSuffix, "", 1)) == ".labels"
}

func isLabelsFile(filePath string) bool {
	return strings.HasSuffix(filePath, ".labels"+registry.MetaFileSuffix)
}

func unionScopeOf(filePath string, metadata *registry.Repository) string {
	if isLabelsContainer(filePath, metadata) {
		return labelsUnionScope
	}
	return comparisonName(metadata, lineOf(Addition, filePath))
}

func (r *moveResolver) triggersOf(group *moveGroup) []unionTrigger {
	known, ok := r.probesOf(group)
	if !ok {
		return nil
	}
	compares := false
	for _, resolved := range known {
		compares = compares || resolved.comparesElements
	}
	deletions := pathsOf(group.deletions)
	if !compares || len(deletions) == 0 {
		return nil
	}
	scope := unionScopeOf(deletions[0], r.ctx.Metadata)
	added := groupBy(pathsOf(group.additions), r.view.to.entryOf)
	var visible []string
	for _, filePath := range deletions {
		if r.keeps(Addition, filePath) {
			visible = append(visible, filePath)
		}
	}
	var triggers []unionTrigger
	byEntry := groupBy(visible, r.view.from.entryOf)
	for _, entry := range byEntry.order {
		arrived, ok := added.groups[entry]
		if !ok {
			continue
		}
		if roots, ok := r.listingRoots(byEntry.groups[entry], arrived); ok {
			triggers = append(triggers, unionTrigger{scope: scope, entry: entry, roots: roots})
		}
	}
	return triggers
}

type unionTarget struct {
	scope        string
	entry        string
	listingRoots []string
}

func toUnionTargets(triggers []unionTrigger) []unionTarget {
	type entries struct {
		order []string
		roots map[string]map[string]bool
	}
	var scopeOrder []string
	merged := map[string]*entries{}
	for _, trigger := range triggers {
		scoped := merged[trigger.scope]
		if scoped == nil {
			scoped = &entries{roots: map[string]map[string]bool{}}
			merged[trigger.scope] = scoped
			scopeOrder = append(scopeOrder, trigger.scope)
		}
		if scoped.roots[trigger.entry] == nil {
			scoped.roots[trigger.entry] = map[string]bool{}
			scoped.order = append(scoped.order, trigger.entry)
		}
		for _, root := range trigger.roots {
			scoped.roots[trigger.entry][root] = true
		}
	}
	var targets []unionTarget
	for _, scope := range scopeOrder {
		scoped := merged[scope]
		for _, entry := range scoped.order {
			var roots []string
			for root := range scoped.roots[entry] {
				roots = append(roots, root)
			}
			sort.Strings(roots)
			targets = append(targets, unionTarget{scope: scope, entry: entry, listingRoots: roots})
		}
	}
	return targets
}

// listSide groups the files of a revision that can take part in a union by
// container and by package directory.
func (r *moveResolver) listSide(side *revisionSide, targets []unionTarget) map[string]*orderedGroups {
	inSource := gitadapter.ScopeMatcher(r.ctx.Config.Source)
	var allRoots []string
	for _, target := range targets {
		allRoots = append(allRoots, target.listingRoots...)
	}
	inAnyRoots := gitadapter.ScopeMatcher(allRoots)
	var listed []string
	for _, filePath := range side.files.Paths {
		if inSource(filePath) && (isLabelsFile(filePath) || inAnyRoots(filePath)) && r.ctx.Metadata.Has(filePath) {
			listed = append(listed, filePath)
		}
	}
	byScope := groupBy(listed, func(filePath string) string { return unionScopeOf(filePath, r.ctx.Metadata) })
	result := map[string]*orderedGroups{}
	for _, scope := range byScope.order {
		result[scope] = groupBy(byScope.groups[scope], side.entryOf)
	}
	return result
}

// diffUnions compares the files of each target together and returns the
// outcome of each file that the diff adds or modifies, and the deletion
// lines the union accounts for.
func (r *moveResolver) diffUnions(triggers []unionTrigger, lines []string) (map[string]*xmldiff.Outcome, map[string]bool) {
	outcomes := map[string]*xmldiff.Outcome{}
	dropped := map[string]bool{}
	targets := toUnionTargets(triggers)
	if len(targets) == 0 {
		return outcomes, dropped
	}
	fromListing := r.listSide(r.view.from, targets)
	toListing := r.listSide(r.view.to, targets)
	emitted := map[string]bool{}
	type deletionLine struct {
		line     string
		position int
	}
	deletions := map[string][]deletionLine{}
	for position, line := range lines {
		switch {
		case strings.HasPrefix(line, Addition), strings.HasPrefix(line, Modification):
			emitted[registry.PathOfLine(line)] = true
		case strings.HasPrefix(line, Deletion):
			filePath := registry.PathOfLine(line)
			deletions[filePath] = append(deletions[filePath], deletionLine{line: line, position: position})
		}
	}
	for _, target := range targets {
		inScope := isLabelsFile
		if target.scope != labelsUnionScope {
			inScope = gitadapter.ScopeMatcher(target.listingRoots)
		}
		candidatesOf := func(listing map[string]*orderedGroups) []string {
			var result []string
			if scoped := listing[target.scope]; scoped != nil {
				for _, filePath := range scoped.groups[target.entry] {
					if inScope(filePath) {
						result = append(result, filePath)
					}
				}
			}
			sort.Strings(result)
			return result
		}
		input := xmldiff.UnionInput{}
		var droppedLines []deletionLine
		for _, filePath := range candidatesOf(fromListing) {
			// A file that the later revision has in another package
			// directory is not part of this container any more.
			_, inTo := r.view.to.files.ID(filePath)
			if inTo && r.view.to.entryOf(filePath) != target.entry {
				continue
			}
			keptAsAddition := r.keeps(Addition, filePath)
			keptAsDeletion := r.keeps(Deletion, filePath)
			if keptAsAddition && keptAsDeletion {
				input.DeletionBasePaths = append(input.DeletionBasePaths, filePath)
				droppedLines = append(droppedLines, deletions[filePath]...)
			}
			if keptAsAddition && (inTo || keptAsDeletion) {
				input.AdditionBasePaths = append(input.AdditionBasePaths, filePath)
			}
		}
		input.ShieldPaths = candidatesOf(toListing)
		for _, filePath := range input.ShieldPaths {
			if emitted[filePath] {
				input.MemberPaths = append(input.MemberPaths, filePath)
			}
		}
		result, err := r.ctx.differ().RunUnion(input)
		if err != nil {
			// The files of this container are compared one by one.
			continue
		}
		for filePath, outcome := range result {
			outcomes[filePath] = outcome
		}
		for _, deletion := range droppedLines {
			dropped[deletion.line] = true
		}
	}
	return outcomes, dropped
}
