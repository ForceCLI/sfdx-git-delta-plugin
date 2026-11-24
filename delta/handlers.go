package delta

import (
	"fmt"
	"strings"

	"github.com/ForceCLI/sfdx-git-delta-plugin/registry"
	"github.com/ForceCLI/sfdx-git-delta-plugin/xmldiff"
)

// Handler turns one diff line into manifest elements and copy operations.
type Handler interface {
	Collect() HandlerResult
	// Descriptor returns the type and member name of the component.
	Descriptor() (string, string, error)
	// ComparesElements reports whether the handler compares the elements
	// inside the file instead of treating the file as one component.
	ComparesElements() bool
	element() *Element
}

// hooks are the steps a handler type can replace.  The shared code calls
// them through the handler that was created, so a replacement applies to
// the steps of the types it is built on.
type hooks interface {
	collectAddition() (HandlerResult, error)
	collectDeletion() (HandlerResult, error)
	collectModification() (HandlerResult, error)
	elementName() string
	descriptor() (string, string, error)
	comparesElements() bool
	isProcessable() bool
	delegateFileCopy() bool
	shouldCopyMetaFile(path string) bool
	metaTypeFilePath(path string) string
}

// standardHandler handles a type whose component is one file, with an
// optional -meta.xml file next to it.
type standardHandler struct {
	self       hooks
	changeType string
	el         *Element
	ctx        *RunContext
}

func (h *standardHandler) element() *Element {
	return h.el
}

func (h *standardHandler) Collect() HandlerResult {
	if !h.self.isProcessable() {
		return HandlerResult{}
	}
	var result HandlerResult
	var err error
	switch h.changeType {
	case Addition:
		result, err = h.self.collectAddition()
	case Deletion:
		result, err = h.self.collectDeletion()
	case Modification:
		result, err = h.self.collectModification()
	}
	if err != nil {
		return HandlerResult{Warnings: []string{h.el.BasePath + ": " + err.Error()}}
	}
	return result
}

func (h *standardHandler) Descriptor() (string, string, error) {
	return h.self.descriptor()
}

func (h *standardHandler) ComparesElements() bool {
	return h.self.comparesElements()
}

func (h *standardHandler) collectAddition() (HandlerResult, error) {
	var copies []CopyOperation
	h.collectCopyWithMetaFile(&copies, h.el.BasePath)
	element, err := h.manifestElement(TargetPackage)
	if err != nil {
		return HandlerResult{}, err
	}
	return HandlerResult{Elements: []ManifestElement{element}, Copies: copies}, nil
}

func (h *standardHandler) collectDeletion() (HandlerResult, error) {
	element, err := h.manifestElement(TargetDestructive)
	if err != nil {
		return HandlerResult{}, err
	}
	return HandlerResult{Elements: []ManifestElement{element}}, nil
}

func (h *standardHandler) collectModification() (HandlerResult, error) {
	return h.self.collectAddition()
}

func (h *standardHandler) elementName() string {
	return h.el.ComponentName()
}

func (h *standardHandler) descriptor() (string, string, error) {
	return h.el.Type().XMLName, h.self.elementName(), nil
}

func (h *standardHandler) comparesElements() bool {
	return false
}

func (h *standardHandler) changeKind() ChangeKind {
	return changeKindByGitType[h.changeType]
}

func (h *standardHandler) manifestElement(target ManifestTarget) (ManifestElement, error) {
	typeName, member, err := h.self.descriptor()
	if err != nil {
		return ManifestElement{}, err
	}
	return ManifestElement{Target: target, Type: typeName, Member: member, ChangeKind: h.changeKind()}, nil
}

func (h *standardHandler) collectCopyWithMetaFile(copies *[]CopyOperation, src string) {
	if !h.self.delegateFileCopy() {
		return
	}
	h.collectCopy(copies, src)
	if h.self.shouldCopyMetaFile(src) {
		h.collectCopy(copies, h.self.metaTypeFilePath(src))
	}
}

func (h *standardHandler) shouldCollectCopies() bool {
	return h.ctx.Config.GenerateDelta
}

func (h *standardHandler) collectCopy(copies *[]CopyOperation, path string) {
	if !h.shouldCollectCopies() {
		return
	}
	*copies = append(*copies, CopyOperation{Kind: CopyFile, Path: path, Revision: h.ctx.Config.To})
}

func (h *standardHandler) collectExactNameCopy(copies *[]CopyOperation, componentPath string) {
	if h.ctx.fileExists(componentPath) {
		h.collectCopy(copies, componentPath)
	}
}

func (h *standardHandler) collectComponentCopy(copies *[]CopyOperation, path string) {
	if !h.shouldCollectCopies() {
		return
	}
	*copies = append(*copies, CopyOperation{Kind: CopyComponent, Path: path, Revision: h.ctx.Config.To})
}

func (h *standardHandler) metaTypeFilePath(path string) string {
	parsed := parsePath(path)
	return joinPath(parsed.dir, parsed.name+"."+h.el.Type().Suffix+registry.MetaFileSuffix)
}

func (h *standardHandler) shouldCopyMetaFile(path string) bool {
	return h.el.Type().MetaFile && !strings.HasSuffix(path, registry.MetaFileSuffix)
}

func (h *standardHandler) isProcessable() bool {
	return h.el.Type().Suffix == h.el.Extension
}

func (h *standardHandler) delegateFileCopy() bool {
	return true
}

func (h *standardHandler) parentFolderIsNotTheType() bool {
	return h.el.ParentFolder != h.el.Type().DirectoryName
}

// inFileHandler handles a file whose child elements are components:
// workflows, sharing rules, labels, profiles, translations.
type inFileHandler struct {
	standardHandler
	// decomposedLabel is set for the CustomLabel type, whose components
	// are either elements of a .labels file or one .label file each.
	customLabel bool
}

func (h *inFileHandler) isDecomposedLabel() bool {
	return h.customLabel && h.el.Extension == registry.LabelDecomposedSuffix
}

func (h *inFileHandler) collectAddition() (HandlerResult, error) {
	if h.isDecomposedLabel() {
		return h.standardHandler.collectAddition()
	}
	return h.collectCompareResult(), nil
}

func (h *inFileHandler) collectDeletion() (HandlerResult, error) {
	treatAsDeletion := h.el.Type().PruneOnly
	if h.customLabel {
		treatAsDeletion = h.isDecomposedLabel()
	}
	if treatAsDeletion {
		return h.standardHandler.collectDeletion()
	}
	return h.self.collectAddition()
}

func (h *inFileHandler) collectModification() (HandlerResult, error) {
	return h.self.collectAddition()
}

func (h *inFileHandler) comparesElements() bool {
	if h.customLabel {
		return !h.isDecomposedLabel()
	}
	return true
}

func (h *inFileHandler) delegateFileCopy() bool {
	return h.customLabel && h.isDecomposedLabel()
}

func (h *inFileHandler) isProcessable() bool {
	if h.customLabel {
		return true
	}
	return h.standardHandler.isProcessable()
}

// qualifiedName is the prefix of the member names: the name of the file
// and a dot, or nothing for labels.
func (h *inFileHandler) qualifiedName() string {
	if h.customLabel {
		return ""
	}
	base := lastSegment(strings.TrimRight(h.el.BasePath, "/"))
	return strings.Split(base, ".")[0] + "."
}

func (h *inFileHandler) malformed() HandlerResult {
	return HandlerResult{Warnings: []string{message("warning.MalformedXML", h.el.BasePath, h.ctx.Config.From, h.ctx.Config.To)}}
}

func (h *inFileHandler) collectCompareResult() HandlerResult {
	outcome, ok := h.ctx.Unions[h.el.BasePath]
	if !ok {
		var err error
		if outcome, err = h.ctx.differ().Run(h.el.BasePath); err != nil {
			return h.malformed()
		}
	}
	var result HandlerResult
	add := func(target ManifestTarget, kind ChangeKind, entries []xmldiff.CompareEntry) {
		for _, entry := range entries {
			if h.ctx.isPackable(entry.Type) {
				result.Elements = append(result.Elements, ManifestElement{Target: target, Type: entry.Type, Member: h.qualifiedName() + entry.Member, ChangeKind: kind})
			}
		}
	}
	// The elements deleted from a modified file are subject to the
	// destructive ignore patterns, as the deletion of the file would be.
	if h.changeType != Modification || h.ctx.shared.ignore.Keep(lineOf(Deletion, h.el.BasePath)) {
		add(TargetDestructive, KindDelete, outcome.Deleted)
	}
	add(TargetPackage, KindAdd, outcome.Added)
	add(TargetPackage, KindModify, outcome.Modified)
	if !h.customLabel && outcome.HasPackageContent {
		container, err := h.standardHandler.collectAddition()
		if err != nil {
			return h.malformed()
		}
		result.Elements = append(result.Elements, container.Elements...)
	}
	if outcome.Writer != nil && h.shouldCollectCopies() {
		result.Copies = append(result.Copies, CopyOperation{Kind: CopyStreamed, Path: h.el.BasePath, Writer: outcome.Writer})
	}
	return result
}

// inFolderHandler handles types kept in folders: documents and email
// templates, and through reportingFolderHandler reports and dashboards.
type inFolderHandler struct {
	standardHandler
	// reporting is set for Report and Dashboard, whose type comes from
	// the file suffix.
	reporting    bool
	resolvedType string
}

func (h *inFolderHandler) collectAddition() (HandlerResult, error) {
	if h.reporting && h.resolvedType == "" {
		return HandlerResult{}, nil
	}
	result, err := h.standardHandler.collectAddition()
	if err != nil {
		return result, err
	}
	folderPath := h.el.TypeDirectoryPath()
	folderName := ""
	if after := h.el.PathAfterType(); len(after) > 0 {
		folderName = after[0]
	}
	folderFileName := folderName + registry.MetaFileSuffix
	if !h.reporting {
		suffix := ""
		if !strings.HasSuffix(folderName, registry.InFolderSuffix) {
			suffix = "." + strings.ToLower(h.el.Type().Suffix)
		}
		folderFileName = folderName + suffix + registry.MetaFileSuffix
	}
	h.collectCopyWithMetaFile(&result.Copies, joinPath(folderPath, folderFileName))
	if h.shouldCollectCopies() {
		parsed := parsePath(h.el.BasePath)
		componentPath := joinPath(parsed.dir, parsed.name)
		h.collectExactNameCopy(&result.Copies, componentPath)
		h.collectComponentCopy(&result.Copies, componentPath)
	}
	return result, nil
}

func (h *inFolderHandler) collectDeletion() (HandlerResult, error) {
	if h.reporting && h.resolvedType == "" {
		return HandlerResult{}, nil
	}
	return h.standardHandler.collectDeletion()
}

func (h *inFolderHandler) elementName() string {
	name := withoutMetaSuffixAtEnd(strings.Join(h.el.PathAfterType(), "/"))
	name = strings.TrimSuffix(name, registry.InFolderSuffix)
	return withoutExtension(name)
}

func (h *inFolderHandler) isProcessable() bool {
	return h.standardHandler.isProcessable() || h.parentFolderIsNotTheType() || strings.HasSuffix(h.el.Extension, registry.InFolderSuffix)
}

func (h *inFolderHandler) descriptor() (string, string, error) {
	if !h.reporting {
		return h.standardHandler.descriptor()
	}
	if h.resolvedType == "" {
		return "", "", fmt.Errorf("ReportingFolderHandler: resolvedType is missing for %s", h.el.FullPath)
	}
	return h.resolvedType, h.self.elementName(), nil
}

// sharedFolderHandler handles types that share one directory and are told
// apart by suffix: wave, discovery and moderation files, and bot versions.
type sharedFolderHandler struct {
	standardHandler
	resolvedType string
	bot          bool
}

func (h *sharedFolderHandler) descriptor() (string, string, error) {
	if h.resolvedType == "" {
		return "", "", fmt.Errorf("SharedFolderHandler: resolvedType is missing for %s", h.el.FullPath)
	}
	return h.resolvedType, h.self.elementName(), nil
}

func (h *sharedFolderHandler) collectAddition() (HandlerResult, error) {
	var result HandlerResult
	if h.resolvedType != "" {
		var err error
		if result, err = h.standardHandler.collectAddition(); err != nil {
			return result, err
		}
	}
	if !h.bot {
		return result, nil
	}
	// A bot version is deployed with its bot.
	botName := lastSegment(h.el.ParentFolder)
	botPath := parsePath(h.el.BasePath).dir + "/" + botName + ".bot"
	h.collectCopyWithMetaFile(&result.Copies, botPath)
	result.Elements = append(result.Elements, ManifestElement{Target: TargetPackage, Type: registry.BotType, Member: botName, ChangeKind: h.changeKind()})
	return result, nil
}

func (h *sharedFolderHandler) collectDeletion() (HandlerResult, error) {
	if h.resolvedType == "" {
		return HandlerResult{}, nil
	}
	return h.standardHandler.collectDeletion()
}

func (h *sharedFolderHandler) isProcessable() bool {
	return h.standardHandler.isProcessable() || h.resolvedType != ""
}

func (h *sharedFolderHandler) metaTypeFilePath(path string) string {
	parsed := parsePath(path)
	return joinPath(parsed.dir, parsed.name+parsed.ext+registry.MetaFileSuffix)
}

func (h *sharedFolderHandler) elementName() string {
	if !h.bot {
		return h.standardHandler.elementName()
	}
	fileName := parsePath(h.el.BasePath).name
	if fileName == h.el.ParentFolder {
		return fileName
	}
	return h.el.ParentFolder + "." + fileName
}

// resourceVariant selects the behavior of a resourceHandler.
type resourceVariant int

const (
	resourcePlain resourceVariant = iota
	resourceLwc
	resourceBundle
	resourceObjectTranslation
)

// resourceHandler handles a component that is a directory of files, with
// or without a file of the same name next to it: static resources, Aura
// and Lightning web component bundles, experience bundles, digital
// experiences and object translations.
type resourceHandler struct {
	standardHandler
	variant      resourceVariant
	metadataName string
}

func (h *resourceHandler) pageContentSegments() []string {
	if h.variant != resourceBundle {
		return nil
	}
	if segments := h.el.PathAfterType(); len(segments) > registry.DigitalExperienceContentDepth {
		return segments[:registry.DigitalExperienceContentDepth]
	}
	return nil
}

func (h *resourceHandler) resolveMetadataName() string {
	switch h.variant {
	case resourceObjectTranslation:
		return h.el.TypeDirectoryPath() + "/" + h.self.elementName()
	case resourceBundle:
		if segments := h.pageContentSegments(); segments != nil {
			return joinPath(append([]string{h.el.TypeDirectoryPath()}, segments...)...)
		}
	}
	return h.el.ComponentPath()
}

func (h *resourceHandler) collectAddition() (HandlerResult, error) {
	if h.variant == resourceObjectTranslation {
		return h.collectObjectTranslation()
	}
	h.metadataName = h.resolveMetadataName()
	result, err := h.standardHandler.collectAddition()
	if err != nil {
		return result, err
	}
	if h.shouldCollectCopies() {
		h.collectExactNameCopy(&result.Copies, h.metadataName)
	}
	h.collectComponentCopy(&result.Copies, h.metadataName)
	if h.variant == resourceLwc && h.shouldCollectCopies() {
		result.Copies = append(result.Copies, CopyOperation{Kind: CopyTypeDirCompanion, Path: h.el.TypeDirectoryPath(), Revision: h.ctx.Config.To})
	}
	return result, nil
}

// collectObjectTranslation lists the object translation and writes its
// parent file reduced to the elements that changed.  Each parent file is
// compared once, however many of its files changed.
func (h *resourceHandler) collectObjectTranslation() (HandlerResult, error) {
	result, err := h.standardHandler.collectAddition()
	if err != nil || !h.shouldCollectCopies() {
		return result, err
	}
	path := parsePath(h.el.BasePath).dir + "/" + h.self.elementName() + "." + registry.ObjectTranslationMetaXMLSuffix
	outcome, cached := h.ctx.parentDiffs[path]
	if !cached {
		if outcome, err = h.ctx.differ().Run(path); err != nil {
			return HandlerResult{}, err
		}
		h.ctx.parentDiffs[path] = outcome
	}
	if outcome.Writer != nil {
		result.Copies = append(result.Copies, CopyOperation{Kind: CopyStreamed, Path: path, Writer: outcome.Writer})
	} else {
		h.collectCopy(&result.Copies, path)
	}
	return result, nil
}

// collectDeletion treats the deletion of a file of a component that still
// exists as a modification of the component.
func (h *resourceHandler) collectDeletion() (HandlerResult, error) {
	h.metadataName = h.resolveMetadataName()
	if h.ctx.pathExists(h.metadataName) {
		return h.self.collectModification()
	}
	return h.standardHandler.collectDeletion()
}

func (h *resourceHandler) elementName() string {
	after := h.el.PathAfterType()
	if h.variant == resourceBundle {
		name := withoutMetaSuffixAtEnd(strings.Join(sliceOf(after, 0, registry.DigitalExperienceBundleDepth), "/"))
		return strings.TrimSuffix(name, "."+h.el.Type().Suffix)
	}
	if len(after) > 1 {
		return after[0]
	}
	return h.el.ComponentName()
}

func (h *resourceHandler) descriptor() (string, string, error) {
	if segments := h.pageContentSegments(); segments != nil {
		return registry.DigitalExperienceType, segments[0] + "/" + segments[1] + "." + segments[2] + "/" + segments[3], nil
	}
	return h.standardHandler.descriptor()
}

func (h *resourceHandler) isProcessable() bool {
	if h.variant == resourceLwc {
		return h.el.ParentFolder != h.el.Type().DirectoryName
	}
	return true
}

func (h *resourceHandler) metaTypeFilePath(string) string {
	return h.metadataName + "." + h.el.Type().Suffix + registry.MetaFileSuffix
}

func (h *resourceHandler) shouldCopyMetaFile(string) bool {
	switch h.variant {
	case resourceObjectTranslation:
		return false
	case resourceBundle:
		return h.pageContentSegments() == nil
	}
	return true
}

func (h *resourceHandler) delegateFileCopy() bool {
	if h.variant == resourceObjectTranslation {
		return !strings.HasSuffix(h.el.FullPath, registry.ObjectTranslationMetaXMLSuffix)
	}
	return true
}

// decomposedHandler handles a child type stored in its own file below its
// parent: fields, sharing rules, territories.
type decomposedHandler struct {
	standardHandler
	// customField copies the parent object only for a master-detail
	// field.
	customField bool
	// childOnly is set for the children of an object that are listed
	// without a copy of the parent.
	childOnly bool
}

func (h *decomposedHandler) collectAddition() (HandlerResult, error) {
	result, err := h.standardHandler.collectAddition()
	if err != nil || h.childOnly {
		return result, err
	}
	if h.customField {
		if !h.shouldCollectCopies() {
			return result, nil
		}
		if len(h.ctx.Git.GrepUnderPaths(registry.MasterDetailTag, []string{h.el.BasePath}, h.ctx.Config.To)) == 0 {
			return result, nil
		}
	}
	parentType := h.el.ParentType()
	if parentType == nil || parentType.Suffix == "" {
		return result, nil
	}
	parentPath := joinPath(dirname(h.el.TypeDirectoryPath()), h.el.ParentName()+"."+parentType.Suffix+registry.MetaFileSuffix)
	h.collectCopyWithMetaFile(&result.Copies, parentPath)
	return result, nil
}

func (h *decomposedHandler) elementName() string {
	return h.el.ParentName() + "." + h.standardHandler.elementName()
}

// customObjectHandler handles objects and territory models.  An object is
// copied with its master-detail fields, which it cannot be deployed
// without.
type customObjectHandler struct {
	standardHandler
}

func (h *customObjectHandler) collectAddition() (HandlerResult, error) {
	result, err := h.standardHandler.collectAddition()
	if err != nil || !h.shouldCollectCopies() || h.el.Type().XMLName != registry.ObjectType {
		return result, err
	}
	fieldsFolder := joinPath(parsePath(h.el.BasePath).dir, registry.FieldDirectoryName)
	if !h.ctx.pathExists(fieldsFolder) {
		return result, nil
	}
	for _, field := range h.ctx.Git.GrepUnderPaths(registry.MasterDetailTag, []string{fieldsFolder}, h.ctx.Config.To) {
		h.collectCopyWithMetaFile(&result.Copies, field)
	}
	return result, nil
}

// containedDecomposedHandler handles permission sets, which are one file
// or a folder of files that all belong to the same permission set.
type containedDecomposedHandler struct {
	standardHandler
	holderDir  string
	holderBase string
}

func (h *containedDecomposedHandler) isDecomposedFormat() bool {
	parsed := parsePath(h.el.BasePath)
	return !strings.Contains(parsed.base, "."+h.el.Type().Suffix) || lastSegment(parsed.dir) == parsed.name
}

func (h *containedDecomposedHandler) setHolderFolder() {
	var holder parsedPath
	if !h.isDecomposedFormat() {
		holder = parsePath(strings.Replace(strings.Replace(h.el.BasePath, registry.MetaFileSuffix, "", 1), "."+h.el.Type().Suffix, "", 1))
	} else {
		offset := 3
		if h.el.ComponentName() == h.el.ParentFolder {
			offset = 2
		}
		holder = parsePath(strings.Join(sliceOf(h.el.Parts, 0, len(h.el.Parts)-offset+1), "/"))
	}
	h.holderDir, h.holderBase = holder.dir, holder.base
}

func (h *containedDecomposedHandler) holderPath() string {
	return joinPath(h.holderDir, h.holderBase)
}

func (h *containedDecomposedHandler) collectAddition() (HandlerResult, error) {
	result, err := h.standardHandler.collectAddition()
	if err != nil || !h.isDecomposedFormat() {
		return result, err
	}
	if h.shouldCollectCopies() {
		result.Copies = append(result.Copies, CopyOperation{Kind: CopyDir, Path: h.holderPath(), Revision: h.ctx.Config.To})
	}
	return result, nil
}

func (h *containedDecomposedHandler) collectDeletion() (HandlerResult, error) {
	if h.isDecomposedFormat() && len(h.ctx.readDirs(h.holderPath())) > 0 {
		return h.self.collectModification()
	}
	return h.standardHandler.collectDeletion()
}

func (h *containedDecomposedHandler) elementName() string {
	return h.holderBase
}

func (h *containedDecomposedHandler) isProcessable() bool {
	return true
}

// externalServiceHandler handles external service registrations, which
// have a definition file and can have a schema file next to it.
type externalServiceHandler struct {
	standardHandler
}

func (h *externalServiceHandler) isSchemaFile() bool {
	return h.el.Extension == "yaml"
}

func (h *externalServiceHandler) componentFile(fileSuffix string) string {
	return joinPath(parsePath(h.el.BasePath).dir, h.el.ComponentName()+"."+fileSuffix)
}

func (h *externalServiceHandler) definitionPath() string {
	return h.componentFile(h.el.Type().Suffix + registry.MetaFileSuffix)
}

func (h *externalServiceHandler) collectAddition() (HandlerResult, error) {
	result, err := h.standardHandler.collectAddition()
	if err != nil {
		return result, err
	}
	sibling := h.componentFile("yaml")
	if h.isSchemaFile() {
		sibling = h.definitionPath()
	}
	if h.shouldCollectCopies() && h.ctx.pathExists(sibling) {
		h.collectCopy(&result.Copies, sibling)
	}
	return result, nil
}

// collectDeletion deploys the definition again when its schema file was
// deleted and the definition still exists.
func (h *externalServiceHandler) collectDeletion() (HandlerResult, error) {
	if !h.isSchemaFile() {
		return h.standardHandler.collectDeletion()
	}
	definition := h.definitionPath()
	if !h.ctx.pathExists(definition) {
		return HandlerResult{}, nil
	}
	var copies []CopyOperation
	h.collectCopy(&copies, definition)
	element, err := h.manifestElement(TargetPackage)
	if err != nil {
		return HandlerResult{}, err
	}
	return HandlerResult{Elements: []ManifestElement{element}, Copies: copies}, nil
}

func (h *externalServiceHandler) isProcessable() bool {
	return h.standardHandler.isProcessable() || h.isSchemaFile()
}

// flowHandler warns when a flow is deleted.
type flowHandler struct {
	standardHandler
}

func (h *flowHandler) collectDeletion() (HandlerResult, error) {
	result, err := h.standardHandler.collectDeletion()
	if err != nil {
		return result, err
	}
	result.Warnings = append(result.Warnings, message("warning.FlowDeleted", h.self.elementName()))
	return result, nil
}

// handlerFactory creates the handler for a diff line.
type handlerFactory struct {
	ctx                  *RunContext
	resolver             *boundaryResolver
	inFileParentXMLNames map[string]bool
}

func newHandlerFactory(ctx *RunContext) *handlerFactory {
	factory := &handlerFactory{ctx: ctx, resolver: newBoundaryResolver(ctx), inFileParentXMLNames: map[string]bool{}}
	for _, definition := range ctx.Metadata.Values() {
		if definition.XMLTag == "" || definition.Key == "" || definition.ParentXMLName == "" {
			continue
		}
		if parent := ctx.Metadata.GetByXMLName(definition.ParentXMLName); parent != nil && parent.Adapter == "" {
			factory.inFileParentXMLNames[definition.ParentXMLName] = true
		}
	}
	return factory
}

// handlerFor returns the handler for a diff line whose path belongs to a
// known type.
func (f *handlerFactory) handlerFor(line string) (Handler, error) {
	changeType := line[:1]
	path := registry.PathOfLine(line)
	definition := f.ctx.Metadata.Get(path)
	if definition == nil {
		return nil, fmt.Errorf("Unknown metadata type for path: %s", path)
	}
	revision := f.ctx.Config.To
	if changeType == Deletion {
		revision = f.ctx.Config.From
	}
	element := f.resolver.createElement(path, definition, revision)
	base := standardHandler{changeType: changeType, el: element, ctx: f.ctx}
	sharedType := f.ctx.shared.sharedFolder[element.Extension]

	switch definition.XMLName {
	case "AuraDefinitionBundle", "GenAiFunction", "LightningComponentBundle", "UIBundle":
		return bind(&resourceHandler{standardHandler: base, variant: resourceLwc}), nil
	case "CustomField":
		return bind(&decomposedHandler{standardHandler: base, customField: true}), nil
	case "CustomFieldTranslation", "CustomObjectTranslation":
		return bind(&resourceHandler{standardHandler: base, variant: resourceObjectTranslation}), nil
	case "CustomLabel":
		return bind(&inFileHandler{standardHandler: base, customLabel: true}), nil
	case "CustomObject", "Territory2Model":
		return bind(&customObjectHandler{standardHandler: base}), nil
	case "Dashboard", "Report":
		return bind(&inFolderHandler{standardHandler: base, reporting: true, resolvedType: sharedType}), nil
	case "ExternalServiceRegistration":
		return bind(&externalServiceHandler{standardHandler: base}), nil
	case "Flow":
		return bind(&flowHandler{standardHandler: base}), nil
	case "GlobalValueSetTranslation", "StandardValueSetTranslation":
		return bind(&inFileHandler{standardHandler: base}), nil
	case "PermissionSet":
		handler := &containedDecomposedHandler{standardHandler: base}
		handler.setHolderFolder()
		return bind(handler), nil
	case "Territory2", "Territory2Rule":
		return bind(&decomposedHandler{standardHandler: base}), nil
	case "VirtualBot":
		return bind(&sharedFolderHandler{standardHandler: base, resolvedType: sharedType, bot: true}), nil
	case "VirtualDiscovery", "VirtualModeration", "VirtualWave":
		return bind(&sharedFolderHandler{standardHandler: base, resolvedType: sharedType}), nil
	}
	if definition.InFolder {
		return bind(&inFolderHandler{standardHandler: base}), nil
	}
	switch definition.Adapter {
	case "bundle", "mixedContent":
		return bind(&resourceHandler{standardHandler: base}), nil
	case registry.DigitalExperienceAdapter:
		return bind(&resourceHandler{standardHandler: base, variant: resourceBundle}), nil
	}
	if definition.ParentXMLName != "" {
		parent := f.ctx.Metadata.GetByXMLName(definition.ParentXMLName)
		if definition.XMLTag != "" && definition.Key != "" && (parent == nil || parent.Adapter == "") {
			return bind(&decomposedHandler{standardHandler: base}), nil
		}
		if definition.XMLTag == "" && parent != nil && parent.Decomposition == "folderPerType" {
			return bind(&decomposedHandler{standardHandler: base, childOnly: true}), nil
		}
	}
	if f.inFileParentXMLNames[definition.XMLName] {
		return bind(&inFileHandler{standardHandler: base}), nil
	}
	return bind(&standardHandler{changeType: changeType, el: element, ctx: f.ctx}), nil
}

// selfSetter is implemented by every handler through standardHandler.
type selfSetter interface {
	Handler
	hooks
	setSelf(self hooks)
}

func (h *standardHandler) setSelf(self hooks) {
	h.self = self
}

// bind makes the shared code of a handler call the steps of the handler
// that was created.
func bind(handler selfSetter) Handler {
	handler.setSelf(handler)
	return handler
}

// processLines runs the handler of each line and collects the results in
// line order.
func processLines(ctx *RunContext, lines []string) (HandlerResult, error) {
	factory := newHandlerFactory(ctx)
	var results []HandlerResult
	for _, line := range lines {
		handler, err := factory.handlerFor(line)
		if err != nil {
			return HandlerResult{}, err
		}
		results = append(results, handler.Collect())
	}
	return mergeResults(results...), nil
}
