// Package delta computes which metadata components changed between two git
// revisions and writes the package manifests and, on request, the changed
// source files.  It is a port of sfdx-git-delta 7.7.2.
package delta

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ForceCLI/sfdx-git-delta-plugin/gitadapter"
	"github.com/ForceCLI/sfdx-git-delta-plugin/ignore"
	"github.com/ForceCLI/sfdx-git-delta-plugin/registry"
	"github.com/ForceCLI/sfdx-git-delta-plugin/xmldiff"
)

// Config is the validated input of a run.
type Config struct {
	To        string
	From      string
	MergeBase bool
	Output    string
	// Source holds the source directories relative to the repository,
	// "." for the whole repository.
	Source            []string
	Ignore            string
	IgnoreDestructive string
	// APIVersion is 0 until it is resolved.
	APIVersion                     int
	Repo                           string
	IgnoreWhitespace               bool
	GenerateDelta                  bool
	Include                        string
	IncludeDestructive             string
	AdditionalMetadataRegistryPath string
	ChangesManifest                string
}

// Status letters of diff lines.
const (
	Addition     = "A"
	Deletion     = "D"
	Modification = "M"
	Renamed      = "R"
)

// ManifestTarget says which manifest an element belongs to.
type ManifestTarget string

const (
	TargetPackage     ManifestTarget = "package"
	TargetDestructive ManifestTarget = "destructiveChanges"
)

// ChangeKind is what happened to a component.
type ChangeKind string

const (
	KindAdd    ChangeKind = "add"
	KindModify ChangeKind = "modify"
	KindDelete ChangeKind = "delete"
	KindRename ChangeKind = "rename"
)

var changeKindByGitType = map[string]ChangeKind{Addition: KindAdd, Modification: KindModify, Deletion: KindDelete}

// ManifestElement is one component in one manifest.
type ManifestElement struct {
	Target     ManifestTarget
	Type       string
	Member     string
	ChangeKind ChangeKind
}

// CopyKind says how a copy operation finds what to write.
type CopyKind int

const (
	// CopyFile writes one file of a revision.
	CopyFile CopyKind = iota
	// CopyDir writes every file below a directory of a revision.
	CopyDir
	// CopyComponent writes every file inside a component folder or named
	// as a dotted sibling of it.
	CopyComponent
	// CopyTypeDirCompanion writes the files of a bundle type directory
	// that belong to no bundle.
	CopyTypeDirCompanion
	// CopyStreamed writes what Writer produces.
	CopyStreamed
	// CopyAugmented rewrites a path after every other operation: Augment
	// receives what an earlier operation wrote there.
	CopyAugmented
)

// CopyOperation is a file, or a set of files, to write below the output
// directory.
type CopyOperation struct {
	Kind     CopyKind
	Path     string
	Revision string
	Writer   func(out io.Writer) error
	// Augment receives the content written at Path earlier in the run;
	// present is false when nothing was written there.
	Augment func(out io.Writer, written string, present bool) error
}

// HandlerResult is what one handler or collector contributes.
type HandlerResult struct {
	Elements []ManifestElement
	Copies   []CopyOperation
	Warnings []string
}

func mergeResults(results ...HandlerResult) HandlerResult {
	var merged HandlerResult
	for _, result := range results {
		merged.Elements = append(merged.Elements, result.Elements...)
		merged.Copies = append(merged.Copies, result.Copies...)
		merged.Warnings = append(merged.Warnings, result.Warnings...)
	}
	return merged
}

// RunContext is what the steps of a run share.
type RunContext struct {
	Config   *Config
	Metadata *registry.Repository
	Trees    *gitadapter.TreeReader
	Git      *gitadapter.Adapter
	// Unions holds the outcome of the files that were compared together
	// with other files, by path.
	Unions map[string]*xmldiff.Outcome
	// parentDiffs holds the outcome of parent files that several children
	// ask for, by path.
	parentDiffs map[string]*xmldiff.Outcome
	shared      *sharedState
}

// sharedState is computed once for a run and kept when the revisions of the
// context are replaced.
type sharedState struct {
	attributes   xmldiff.Attributes
	packable     map[string]bool
	sharedFolder map[string]string
	ignore       *ignore.Helper
	include      *ignore.Helper
}

func readMatcher(path string) (*ignore.Matcher, error) {
	if path == "" {
		return ignore.Parse(""), nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ignore.Parse(string(content)), nil
}

// NewRunContext builds the context of a run.
func NewRunContext(config *Config, metadata *registry.Repository, trees *gitadapter.TreeReader, git *gitadapter.Adapter) (*RunContext, error) {
	shared := &sharedState{
		attributes:   xmldiff.AttributesOf(metadata),
		packable:     map[string]bool{},
		sharedFolder: map[string]string{},
	}
	// The value for an XML name comes from the last definition in registry
	// order that has a tag.
	for _, definition := range metadata.Values() {
		if definition.XMLTag == "" || definition.XMLName == "" {
			continue
		}
		excluded := definition.Excluded || definition.XMLName == "Translations" || definition.XMLName == "StandardValueSetTranslation" || definition.XMLName == "GlobalValueSetTranslation" ||
			definition.ParentXMLName == "Translations" || definition.ParentXMLName == "StandardValueSetTranslation" || definition.ParentXMLName == "GlobalValueSetTranslation"
		shared.packable[definition.XMLName] = !excluded
	}
	for _, definition := range metadata.Values() {
		for _, content := range definition.Content {
			shared.sharedFolder[content.Suffix] = content.XMLName
		}
	}
	global, err := readMatcher(config.Ignore)
	if err != nil {
		return nil, err
	}
	destructivePath := config.IgnoreDestructive
	if destructivePath == "" {
		destructivePath = config.Ignore
	}
	destructive, err := readMatcher(destructivePath)
	if err != nil {
		return nil, err
	}
	shared.ignore = &ignore.Helper{Global: global, Destructive: destructive}
	include, err := readMatcher(config.Include)
	if err != nil {
		return nil, err
	}
	includeDestructive, err := readMatcher(config.IncludeDestructive)
	if err != nil {
		return nil, err
	}
	shared.include = &ignore.Helper{Global: include, Destructive: includeDestructive}
	return &RunContext{
		Config:      config,
		Metadata:    metadata,
		Trees:       trees,
		Git:         git,
		Unions:      map[string]*xmldiff.Outcome{},
		parentDiffs: map[string]*xmldiff.Outcome{},
		shared:      shared,
	}, nil
}

// withRevisions returns a context for a pass that compares other
// revisions.  The outcomes computed for the original revisions are not
// carried over.
func (c *RunContext) withRevisions(from, to string) *RunContext {
	config := *c.Config
	config.From = from
	config.To = to
	derived := *c
	derived.Config = &config
	derived.Unions = map[string]*xmldiff.Outcome{}
	derived.parentDiffs = map[string]*xmldiff.Outcome{}
	return &derived
}

func (c *RunContext) isPackable(xmlName string) bool {
	packable, known := c.shared.packable[xmlName]
	return !known || packable
}

func (c *RunContext) differ() *xmldiff.Differ {
	return &xmldiff.Differ{
		From:          c.Config.From,
		To:            c.Config.To,
		GenerateDelta: c.Config.GenerateDelta,
		Attributes:    c.shared.attributes,
		Reader:        c.Git,
	}
}

func (c *RunContext) fileExists(path string) bool {
	return c.Trees.FileExists(c.Config.To, path)
}

func (c *RunContext) pathExists(path string) bool {
	return c.Trees.PathExists(c.Config.To, path)
}

func (c *RunContext) readDirs(paths ...string) []string {
	return c.Trees.FilesUnder(c.Config.To, paths...)
}

func lineOf(status, path string) string {
	return status + "\t" + path
}

// sanitizeForMessage prepares a value typed by the user for a message: it
// is cut to 200 characters, backslashes are doubled, and control and format
// characters are written as \u{...}.
func sanitizeForMessage(value string) string {
	runes := []rune(value)
	truncated := len(runes) > 200
	if truncated {
		runes = runes[:200]
	}
	var out strings.Builder
	for _, r := range runes {
		switch {
		case r == '\\':
			out.WriteString(`\\`)
		case isControlOrFormat(r):
			fmt.Fprintf(&out, `\u{%x}`, r)
		default:
			out.WriteRune(r)
		}
	}
	if truncated {
		out.WriteString("…")
	}
	return out.String()
}
