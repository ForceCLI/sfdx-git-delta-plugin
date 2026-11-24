package xmldiff

import "github.com/ForceCLI/sfdx-git-delta-plugin/registry"

// Child types of these parents are never listed in a package manifest.
var granularExcludedTypes = map[string]bool{
	"Translations":                true,
	"StandardValueSetTranslation": true,
	"GlobalValueSetTranslation":   true,
}

// AttributesOf returns the description of every type of the registry that
// is stored as a child element of a file, by the tag of the element.  A
// later definition with the tag of an earlier one replaces it.
func AttributesOf(repository *registry.Repository) Attributes {
	attributes := Attributes{}
	for _, metadata := range repository.Values() {
		if metadata.XMLTag == "" {
			continue
		}
		attributes[metadata.XMLTag] = Attribute{
			XMLName:  metadata.XMLName,
			Key:      metadata.Key,
			Excluded: granularExcludedTypes[metadata.ParentXMLName] || granularExcludedTypes[metadata.XMLName] || metadata.Excluded,
		}
	}
	return attributes
}
