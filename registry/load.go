package registry

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// sdrRegistryJSON is the metadata registry of the Salesforce
// source-deploy-retrieve library.  data/SOURCE names the version.
//
//go:embed data/metadataRegistry.json
var sdrRegistryJSON []byte

// internalRegistryJSON holds the type definitions that the library's
// registry lacks or that this program needs in another shape.
//
//go:embed data/internalRegistry.json
var internalRegistryJSON []byte

// An entry is a type definition as a set of fields.  Definitions are merged
// field by field, and a field that is present with the value null replaces
// a value from the other definition, so the fields are kept as a map until
// the merge is done.
type entry map[string]any

func (e entry) xmlName() string {
	name, _ := e["xmlName"].(string)
	return name
}

type member struct {
	key   string
	value json.RawMessage
}

// orderedObject returns the members of a JSON object in the order they are
// written.  The registry is read in that order because the first type that
// declares a suffix keeps it and the last type that declares a directory
// keeps it.
func orderedObject(data []byte) ([]member, error) {
	if len(bytes.TrimSpace(data)) == 0 || string(bytes.TrimSpace(data)) == "null" {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return nil, fmt.Errorf("expected a JSON object")
	}
	var members []member
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, fmt.Errorf("expected an object key")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		members = append(members, member{key: key, value: value})
	}
	return members, nil
}

type sdrStrategies struct {
	Adapter       string `json:"adapter"`
	Decomposition string `json:"decomposition"`
}

type sdrChildren struct {
	Types       json.RawMessage `json:"types"`
	Directories json.RawMessage `json:"directories"`
}

type sdrType struct {
	ID              string         `json:"id"`
	Name            string         `json:"name"`
	Suffix          *string        `json:"suffix"`
	DirectoryName   *string        `json:"directoryName"`
	InFolder        bool           `json:"inFolder"`
	FolderType      string         `json:"folderType"`
	AliasFor        string         `json:"aliasFor"`
	Strategies      *sdrStrategies `json:"strategies"`
	Children        *sdrChildren   `json:"children"`
	XMLElementName  string         `json:"xmlElementName"`
	UniqueIDElement string         `json:"uniqueIdElement"`
}

func (t sdrType) suffix() string {
	if t.Suffix == nil {
		return ""
	}
	return *t.Suffix
}

func (t sdrType) directoryName() string {
	if t.DirectoryName == nil {
		return ""
	}
	return *t.DirectoryName
}

func (t sdrType) adapter() string {
	if t.Strategies == nil {
		return ""
	}
	return t.Strategies.Adapter
}

// Adapters of types whose components have a -meta.xml file next to their
// content.
var contentFileAdapters = map[string]bool{
	"matchingContentFile": true,
	"mixedContent":        true,
	"bundle":              true,
	"decomposed":          true,
	"digitalExperience":   true,
}

// Folder-organized types whose folder type shares their directory.
var typesWithContentArray = map[string]bool{"dashboard": true, "report": true, "emailtemplate": true}

// Parent types whose children are never listed in a package manifest.
var excludedParentTypes = map[string]bool{"Translations": true, "CustomObjectTranslation": true}

func parseSDRTypes(data []byte) ([]sdrType, error) {
	members, err := orderedObject(data)
	if err != nil {
		return nil, err
	}
	types := make([]sdrType, 0, len(members))
	for _, m := range members {
		var parsed sdrType
		if err := json.Unmarshal(m.value, &parsed); err != nil {
			return nil, fmt.Errorf("type %s: %w", m.key, err)
		}
		types = append(types, parsed)
	}
	return types, nil
}

// sdrEntries converts the library's registry to type definitions: one for
// each type that is not a folder type, followed by one for each of its
// child types.
func sdrEntries(registryJSON []byte) ([]entry, error) {
	var document struct {
		Types json.RawMessage `json:"types"`
	}
	if err := json.Unmarshal(registryJSON, &document); err != nil {
		return nil, err
	}
	types, err := parseSDRTypes(document.Types)
	if err != nil {
		return nil, err
	}
	byID := map[string]sdrType{}
	baseFolderTypeIDs := map[string]bool{}
	for _, t := range types {
		byID[t.ID] = t
		if t.InFolder && t.FolderType != "" {
			baseFolderTypeIDs[t.FolderType] = true
		}
	}
	folderTypeIDs := map[string]bool{}
	for id := range baseFolderTypeIDs {
		folderTypeIDs[id] = true
	}
	for _, t := range types {
		if t.AliasFor != "" && baseFolderTypeIDs[t.AliasFor] {
			folderTypeIDs[t.ID] = true
		}
	}

	var result []entry
	for _, t := range types {
		// A folder type is reached through the content of its parent.
		if folderTypeIDs[t.ID] {
			continue
		}
		var children []sdrType
		if t.Children != nil {
			if children, err = parseSDRTypes(t.Children.Types); err != nil {
				return nil, fmt.Errorf("children of %s: %w", t.ID, err)
			}
		}
		result = append(result, convertSDRType(t, children, byID))
		if t.Children == nil {
			continue
		}
		directories, err := orderedObject(t.Children.Directories)
		if err != nil {
			return nil, fmt.Errorf("child directories of %s: %w", t.ID, err)
		}
		directoryNames := map[string]bool{}
		for _, directory := range directories {
			directoryNames[directory.key] = true
		}
		for _, child := range children {
			// The directory of a child is the key of the parent's
			// directories that equals the child's XML element name, or
			// the directory the child declares.
			childDirectory := child.directoryName()
			if child.XMLElementName != "" && directoryNames[child.XMLElementName] {
				childDirectory = child.XMLElementName
			}
			// A child with the suffix of its parent would take the place
			// of the parent in the suffix lookup, unless it has a
			// directory of its own.
			hasDifferentDirectory := childDirectory != "" && childDirectory != t.directoryName()
			sameSuffix := (child.Suffix == nil && t.Suffix == nil) || (child.Suffix != nil && t.Suffix != nil && *child.Suffix == *t.Suffix)
			skipSuffix := sameSuffix && !hasDifferentDirectory
			result = append(result, convertSDRChild(child, t.Name, skipSuffix, childDirectory, t.directoryName()))
		}
	}
	return result, nil
}

func convertSDRType(t sdrType, children []sdrType, byID map[string]sdrType) entry {
	result := entry{
		"directoryName": t.directoryName(),
		"inFolder":      t.InFolder,
		"metaFile":      t.adapter() != "" && contentFileAdapters[t.adapter()],
		"xmlName":       t.Name,
	}
	if t.suffix() != "" {
		result["suffix"] = t.suffix()
	}
	if typesWithContentArray[t.ID] && t.FolderType != "" {
		if folderType, ok := byID[t.FolderType]; ok {
			result["content"] = []any{
				map[string]any{"suffix": t.suffix(), "xmlName": t.Name},
				map[string]any{"suffix": folderType.suffix(), "xmlName": folderType.Name},
			}
		}
	}
	if t.Children != nil {
		names := make([]any, 0, len(children))
		for _, child := range children {
			names = append(names, child.Name)
		}
		result["childXmlNames"] = names
	}
	// The library calls its plain single-file strategy "default".  It
	// describes no layout and is not recorded.
	if adapter := t.adapter(); adapter != "" && adapter != "default" {
		result["adapter"] = adapter
	}
	if t.Strategies != nil && t.Strategies.Decomposition != "" {
		result["decomposition"] = t.Strategies.Decomposition
	}
	return result
}

func convertSDRChild(child sdrType, parentXMLName string, skipSuffix bool, childDirectory, parentDirectory string) entry {
	// A child in the directory of its parent has no directory of its own,
	// so that the directory lookup returns the parent.
	directory := childDirectory
	if childDirectory == parentDirectory {
		directory = ""
	}
	result := entry{
		"directoryName": directory,
		"inFolder":      false,
		"metaFile":      false,
		"xmlName":       child.Name,
		"parentXmlName": parentXMLName,
	}
	if !skipSuffix && child.suffix() != "" {
		result["suffix"] = child.suffix()
	}
	if child.XMLElementName != "" {
		result["xmlTag"] = child.XMLElementName
	}
	if child.UniqueIDElement != "" {
		result["key"] = child.UniqueIDElement
	}
	if excludedParentTypes[parentXMLName] {
		result["excluded"] = true
	}
	return result
}

// mergeEntries returns the additional definitions followed by the base
// definitions that share no XML name with them.  A base definition with the
// XML name of an additional one replaces the fields it has, at the position
// of the additional definition.  A later definition with an XML name seen
// before takes the position of the earlier one.
func mergeEntries(base, additional []entry) []entry {
	var order []string
	merged := map[string]entry{}
	for _, item := range additional {
		name := item.xmlName()
		if name == "" {
			continue
		}
		if _, seen := merged[name]; !seen {
			order = append(order, name)
		}
		merged[name] = item
	}
	for _, item := range base {
		name := item.xmlName()
		if name == "" {
			continue
		}
		existing, seen := merged[name]
		if !seen {
			order = append(order, name)
			merged[name] = item
			continue
		}
		combined := entry{}
		for key, value := range existing {
			combined[key] = value
		}
		for key, value := range item {
			combined[key] = value
		}
		merged[name] = combined
	}
	result := make([]entry, 0, len(order))
	for _, name := range order {
		result = append(result, merged[name])
	}
	return result
}

func parseEntries(data []byte) ([]entry, error) {
	var entries []entry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func toMetadata(entries []entry) ([]Metadata, error) {
	data, err := json.Marshal(entries)
	if err != nil {
		return nil, err
	}
	var result []Metadata
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// Error is a problem with the additional metadata registry file.
type Error struct {
	Message string
}

func (e *Error) Error() string {
	return e.Message
}

var additionalRegistryFields = map[string]string{
	"suffix":        "string",
	"xmlName":       "string",
	"content":       "array",
	"parentXmlName": "string",
	"xmlTag":        "string",
	"key":           "string",
	"excluded":      "boolean",
	"pruneOnly":     "boolean",
	"directoryName": "string",
	"inFolder":      "boolean",
	"metaFile":      "boolean",
	"childXmlNames": "array",
	"adapter":       "string",
	"decomposition": "string",
}

func jsonTypeOf(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case []any:
		return "array"
	default:
		return "object"
	}
}

// validateAdditional checks the entries of an additional registry file:
// each is an object with directoryName, inFolder, metaFile and a non-empty
// xmlName, the other fields have their types, and no other field is
// present.
func validateAdditional(parsed any) []string {
	items, ok := parsed.([]any)
	if !ok {
		return []string{fmt.Sprintf("  - : Invalid input: expected array, received %s", jsonTypeOf(parsed))}
	}
	var issues []string
	for index, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			issues = append(issues, fmt.Sprintf("  - %d: Invalid input: expected object, received %s", index, jsonTypeOf(item)))
			continue
		}
		for _, field := range []string{"suffix", "xmlName", "content", "parentXmlName", "xmlTag", "key", "excluded", "pruneOnly", "directoryName", "inFolder", "metaFile", "childXmlNames", "adapter", "decomposition"} {
			value, present := object[field]
			required := field == "directoryName" || field == "inFolder" || field == "metaFile" || field == "xmlName"
			if !present {
				if required {
					issues = append(issues, fmt.Sprintf("  - %d.%s: Invalid input: expected %s, received undefined", index, field, additionalRegistryFields[field]))
				}
				continue
			}
			if got := jsonTypeOf(value); got != additionalRegistryFields[field] {
				issues = append(issues, fmt.Sprintf("  - %d.%s: Invalid input: expected %s, received %s", index, field, additionalRegistryFields[field], got))
				continue
			}
			if field == "xmlName" && value.(string) == "" {
				issues = append(issues, fmt.Sprintf("  - %d.%s: xmlName is required and cannot be empty", index, field))
			}
		}
		var unknown []string
		for key := range object {
			if _, known := additionalRegistryFields[key]; !known {
				unknown = append(unknown, `"`+key+`"`)
			}
		}
		if len(unknown) > 0 {
			sortStrings(unknown)
			noun := "key"
			if len(unknown) > 1 {
				noun = "keys"
			}
			issues = append(issues, fmt.Sprintf("  - %d: Unrecognized %s: %s", index, noun, strings.Join(unknown, ", ")))
		}
	}
	return issues
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

// Definitions returns the type definitions: the library's registry with the
// internal definitions applied over it, and, when additionalPath is not
// empty, the definitions of that file for types the others do not define.
func Definitions(additionalPath string) ([]Metadata, error) {
	internal, err := parseEntries(internalRegistryJSON)
	if err != nil {
		return nil, fmt.Errorf("internal registry: %w", err)
	}
	sdr, err := sdrEntries(sdrRegistryJSON)
	if err != nil {
		return nil, fmt.Errorf("metadata registry: %w", err)
	}
	final := mergeEntries(internal, sdr)
	// CustomObjectTranslation has two definitions with different suffixes:
	// the fieldTranslation one of the internal registry for the files of
	// its children, and this one for the file of the parent.
	final = append(final, entry{
		"directoryName": "objectTranslations",
		"inFolder":      false,
		"metaFile":      false,
		"suffix":        "objectTranslation",
		"xmlName":       "CustomObjectTranslation",
		"pruneOnly":     true,
	})
	if additionalPath != "" {
		content, err := os.ReadFile(additionalPath)
		if err != nil {
			return nil, err
		}
		var parsed any
		if err := json.Unmarshal(content, &parsed); err != nil {
			return nil, &Error{Message: fmt.Sprintf("Unable to parse the additional metadata registry file '%s'. Caused by: %v", additionalPath, err)}
		}
		if issues := validateAdditional(parsed); len(issues) > 0 {
			return nil, &Error{Message: fmt.Sprintf("Invalid additional metadata registry file '%s':\n%s", additionalPath, strings.Join(issues, "\n"))}
		}
		additional, err := parseEntries(content)
		if err != nil {
			return nil, &Error{Message: fmt.Sprintf("Unable to parse the additional metadata registry file '%s'. Caused by: %v", additionalPath, err)}
		}
		final = mergeEntries(final, additional)
	}
	return toMetadata(final)
}

// Load returns the repository of the type definitions.
func Load(additionalPath string) (*Repository, error) {
	definitions, err := Definitions(additionalPath)
	if err != nil {
		return nil, err
	}
	return NewRepository(definitions), nil
}
