package delta

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"

	"github.com/ForceCLI/sfdx-git-delta-plugin/gitadapter"
	"github.com/ForceCLI/sfdx-git-delta-plugin/registry"
	"github.com/ForceCLI/sfdx-git-delta-plugin/xmldiff"
)

const metadataNamespace = "http://soap.sforce.com/2006/04/metadata"

func defaultXMLHeader() *xmldiff.Object {
	declaration := xmldiff.NewObject()
	declaration.Set(xmldiff.AttributePrefix+"version", "1.0")
	declaration.Set(xmldiff.AttributePrefix+"encoding", "UTF-8")
	header := xmldiff.NewObject()
	header.Set(xmldiff.XMLHeaderKey, declaration)
	return header
}

func namespaceAttributes() *xmldiff.Object {
	attributes := xmldiff.NewObject()
	attributes.Set(xmldiff.AttributePrefix+"xmlns", metadataNamespace)
	return attributes
}

// isSubDir reports whether dir is below parent, both resolved against the
// working directory.
func isSubDir(parent, dir string) bool {
	absoluteParent, err := filepath.Abs(parent)
	if err != nil {
		return false
	}
	absoluteDir, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(absoluteParent, absoluteDir)
	return err == nil && relative != "." && relative != "" && !strings.HasPrefix(relative, "..") && !filepath.IsAbs(relative)
}

func isSamePath(a, b string) bool {
	absoluteA, errA := filepath.Abs(a)
	absoluteB, errB := filepath.Abs(b)
	return errA == nil && errB == nil && absoluteA == absoluteB
}

// collectFlowTranslations adds the translations that refer to a deployed
// flow.  A translation file holds the translations of every flow of one
// language, and a flow is deployed with its translations, so each
// translation file that mentions a deployed flow is listed, and with
// generateDelta is written with the definitions of those flows added to
// what the run wrote there.
func collectFlowTranslations(ctx *RunContext, changes *ChangeSet) (HandlerResult, error) {
	packagedFlows := changes.ForPackageManifest().Get(registry.FlowXMLName)
	if packagedFlows == nil {
		return HandlerResult{}, nil
	}
	config := ctx.Config
	var pathspecs []string
	for _, source := range config.Source {
		pathspecs = append(pathspecs, source+"/*."+registry.TranslationExtension+registry.MetaFileSuffix)
	}
	outputIsRepo := isSamePath(config.Output, config.Repo)
	var order []string
	translations := map[string][]any{}
	for _, translationPath := range ctx.Git.GrepMatchingPathspecs(registry.FlowDefinitionsKey, pathspecs, config.To) {
		if ctx.shared.ignore.Global.Ignores(translationPath) || (!outputIsRepo && isSubDir(config.Output, translationPath)) {
			continue
		}
		source, err := ctx.Git.StringContent(translationPath, config.To)
		if err != nil {
			source = ""
		}
		xmldiff.ParseFromSide(source, func(subType string, element any) {
			if subType != registry.FlowDefinitionsKey {
				return
			}
			fullName, ok := fullNameOf(element)
			if !ok || fullName == "" || !packagedFlows.Has(fullName) {
				return
			}
			if _, seen := translations[translationPath]; !seen {
				order = append(order, translationPath)
			}
			translations[translationPath] = append(translations[translationPath], element)
		})
	}
	var result HandlerResult
	for _, translationPath := range order {
		result.Elements = append(result.Elements, ManifestElement{
			Target:     TargetPackage,
			Type:       registry.TranslationType,
			Member:     parsePath(withoutMetaSuffixAtEnd(translationPath)).name,
			ChangeKind: KindModify,
		})
		if !config.GenerateDelta {
			continue
		}
		path := translationPath
		flows := translations[translationPath]
		result.Copies = append(result.Copies, CopyOperation{
			Kind: CopyAugmented,
			Path: path,
			Augment: func(out io.Writer, written string, present bool) error {
				if !present && outputIsRepo {
					if content, err := os.ReadFile(filepath.Join(config.Output, filepath.FromSlash(path))); err == nil {
						written = string(content)
					}
				}
				return writeMergedTranslation(out, written, flows)
			},
		})
	}
	return result, nil
}

func fullNameOf(element any) (string, bool) {
	object, ok := element.(*xmldiff.Object)
	if !ok {
		return "", false
	}
	value, present := object.Get("fullName")
	if !present {
		return "", false
	}
	name, isText := value.(string)
	return name, isText
}

// writeMergedTranslation writes the translation that is already in the
// output with the flow definitions added that it does not have.  Without
// an existing file it writes a translation with those definitions only.
func writeMergedTranslation(out io.Writer, existing string, flows []any) error {
	type child struct {
		key      string
		elements []any
	}
	var children []*child
	indexByKey := map[string]*child{}
	seenFullNames := map[string]bool{}
	seenNameless := false
	note := func(element any) bool {
		name, ok := fullNameOf(element)
		if !ok {
			wasSeen := seenNameless
			seenNameless = true
			return wasSeen
		}
		wasSeen := seenFullNames[name]
		seenFullNames[name] = true
		return wasSeen
	}
	capture := xmldiff.ParseFromSide(existing, func(subType string, element any) {
		bucket := indexByKey[subType]
		if bucket == nil {
			bucket = &child{key: subType}
			indexByKey[subType] = bucket
			children = append(children, bucket)
		}
		bucket.elements = append(bucket.elements, element)
		if subType == registry.FlowDefinitionsKey {
			note(element)
		}
	})
	if capture == nil {
		capture = &xmldiff.RootCapture{XMLHeader: defaultXMLHeader(), RootKey: "Translations", RootAttributes: namespaceAttributes()}
		children = nil
		indexByKey = map[string]*child{}
		seenFullNames = map[string]bool{}
		seenNameless = false
	}
	flowBucket := indexByKey[registry.FlowDefinitionsKey]
	if flowBucket == nil {
		flowBucket = &child{key: registry.FlowDefinitionsKey}
		children = append(children, flowBucket)
	}
	for _, flow := range flows {
		if !note(flow) {
			flowBucket.elements = append(flowBucket.elements, flow)
		}
	}
	entries := make([]xmldiff.Entry, 0, len(children))
	for _, bucket := range children {
		elements := bucket.elements
		if elements == nil {
			elements = []any{}
		}
		entries = append(entries, xmldiff.Entry{Key: bucket.key, Value: elements})
	}
	return xmldiff.WriteDocument(out, capture, entries, xmldiff.WriteOptions{})
}

// collectIncludes treats every file of the later revision that the include
// patterns match as added, and every file the destructive include patterns
// match as deleted.
func collectIncludes(ctx *RunContext) (HandlerResult, error) {
	config := ctx.Config
	if config.Include == "" && config.IncludeDestructive == "" {
		return HandlerResult{}, nil
	}
	var additions, deletions []string
	for _, file := range ctx.Trees.FilesUnder(config.To, config.Source...) {
		if line := lineOf(Addition, file); !ctx.shared.include.Keep(line) {
			additions = append(additions, line)
		}
		if line := lineOf(Deletion, file); !ctx.shared.include.Keep(line) {
			deletions = append(deletions, line)
		}
	}
	var results []HandlerResult
	if len(additions) > 0 {
		result, err := processLines(ctx.withRevisions(gitadapter.EmptyTreeID, config.To), additions)
		if err != nil {
			return HandlerResult{}, err
		}
		results = append(results, result)
	}
	if len(deletions) > 0 {
		result, err := processLines(ctx.withRevisions(config.To, gitadapter.EmptyTreeID), deletions)
		if err != nil {
			return HandlerResult{}, err
		}
		results = append(results, result)
	}
	return mergeResults(results...), nil
}

// collectAll runs the collectors.  A collector that fails contributes a
// warning and nothing else.
func collectAll(ctx *RunContext, handlerView *ChangeSet) HandlerResult {
	var results []HandlerResult
	if result, err := collectFlowTranslations(ctx, handlerView); err != nil {
		results = append(results, HandlerResult{Warnings: []string{"FlowTranslationProcessor: " + err.Error()}})
	} else {
		results = append(results, result)
	}
	if result, err := collectIncludes(ctx); err != nil {
		results = append(results, HandlerResult{Warnings: []string{"IncludeProcessor: " + err.Error()}})
	} else {
		results = append(results, result)
	}
	return mergeResults(results...)
}

// frenchCollator orders names as upstream does, with the collation of the
// French locale: punctuation, then digits, then letters, with a lower case
// letter before the same letter in upper case.
func sortNames(names []string) []string {
	sorted := append([]string{}, names...)
	collate.New(language.French).SortStrings(sorted)
	return sorted
}

// WritePackage writes a package manifest: the types and their members in
// collation order, and the API version.
func WritePackage(out io.Writer, manifest *Manifest, apiVersion int) error {
	var children []xmldiff.Entry
	for _, typeName := range sortNames(manifest.Types()) {
		members := make([]any, 0, manifest.Get(typeName).Len())
		for _, member := range sortNames(manifest.Get(typeName).Members()) {
			members = append(members, member)
		}
		block := xmldiff.NewObject()
		block.Set("members", members)
		block.Set("name", typeName)
		children = append(children, xmldiff.Entry{Key: "types", Value: block})
	}
	children = append(children, xmldiff.Entry{Key: "version", Value: fmt.Sprintf("%d.0", apiVersion)})
	capture := &xmldiff.RootCapture{XMLHeader: defaultXMLHeader(), RootKey: "Package", RootAttributes: namespaceAttributes()}
	return xmldiff.WriteDocument(out, capture, children, xmldiff.WriteOptions{NoTrailingNewline: true, Escape: true})
}

func outputWriteError(path string, err error) error {
	return fmt.Errorf("%s", message("warning.OutputWriteFailed", sanitizeForMessage(path), sanitizeForMessage(err.Error())))
}

func writeOutputFile(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return outputWriteError(path, err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return outputWriteError(path, err)
	}
	return nil
}

// generatePackages writes destructiveChanges/destructiveChanges.xml,
// package/package.xml and the empty destructiveChanges/package.xml that a
// destructive deployment needs.
func generatePackages(ctx *RunContext, changes *ChangeSet) error {
	files := []struct {
		folder, name string
		manifest     *Manifest
	}{
		{"destructiveChanges", "destructiveChanges.xml", changes.ForDestructiveManifest()},
		{"package", "package.xml", changes.ForPackageManifest()},
		{"destructiveChanges", "package.xml", newManifest()},
	}
	var firstErr error
	for _, file := range files {
		var buffer bytes.Buffer
		if err := WritePackage(&buffer, file.manifest, ctx.Config.APIVersion); err != nil {
			return err
		}
		if err := writeOutputFile(filepath.Join(ctx.Config.Output, file.folder, file.name), buffer.Bytes()); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

type renameEntry struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type changesManifest struct {
	Add    map[string][]string      `json:"add"`
	Modify map[string][]string      `json:"modify"`
	Delete map[string][]string      `json:"delete"`
	Rename map[string][]renameEntry `json:"rename"`
}

func kindBucket(manifest *Manifest) map[string][]string {
	bucket := map[string][]string{}
	for _, typeName := range manifest.Types() {
		members := append([]string{}, manifest.Get(typeName).Members()...)
		sort.Strings(members)
		bucket[typeName] = members
	}
	return bucket
}

// writeChangesManifest writes the changes by kind as JSON, with the types
// and members sorted.
func writeChangesManifest(ctx *RunContext, changes *ChangeSet) error {
	target := ctx.Config.ChangesManifest
	if target == "" {
		return nil
	}
	view := changes.ByChangeKind()
	payload := changesManifest{
		Add:    kindBucket(view.Add),
		Modify: kindBucket(view.Modify),
		Delete: kindBucket(view.Delete),
		Rename: map[string][]renameEntry{},
	}
	for _, typeName := range view.RenameTypes {
		entries := []renameEntry{}
		for _, pair := range view.Renames[typeName] {
			entries = append(entries, renameEntry{From: pair.From, To: pair.To})
		}
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].To < entries[j].To })
		payload.Rename[typeName] = entries
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(payload); err != nil {
		return err
	}
	return writeOutputFile(target, bytes.TrimSuffix(buffer.Bytes(), []byte("\n")))
}

// executeRemaining writes the manifests.  A failure is returned as a
// warning.
func executeRemaining(ctx *RunContext, changes *ChangeSet) []string {
	var warnings []string
	if err := generatePackages(ctx, changes); err != nil {
		warnings = append(warnings, "PackageGenerator: "+err.Error())
	}
	if err := writeChangesManifest(ctx, changes); err != nil {
		warnings = append(warnings, "ChangesManifestProcessor: "+err.Error())
	}
	return warnings
}
