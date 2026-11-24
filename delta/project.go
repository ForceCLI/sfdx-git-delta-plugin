package delta

import (
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
)

const sfdxProjectFile = "sfdx-project.json"

// noPackageDirectory is the key of a path that is in no package directory.
const noPackageDirectory = "\x00"

// sfdxProject is the part of sfdx-project.json this program uses.
type sfdxProject struct {
	// entries are the package directories, longest first.
	entries          []string
	sourceAPIVersion any
	hasAPIVersion    bool
}

func normalisePackageDirectory(raw string) string {
	normalised := path.Clean(strings.ReplaceAll(raw, `\`, "/"))
	normalised = strings.TrimPrefix(normalised, "./")
	normalised = strings.TrimRight(normalised, "/")
	if normalised == "." {
		return ""
	}
	return normalised
}

func parseSfdxProject(content string) (*sfdxProject, error) {
	invalid := func(reason string) error {
		return fmt.Errorf("Invalid %s: %s", sfdxProjectFile, reason)
	}
	var parsed any
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return nil, invalid(err.Error())
	}
	object, ok := parsed.(map[string]any)
	if !ok {
		return nil, invalid("Invalid input: expected object")
	}
	project := &sfdxProject{}
	project.sourceAPIVersion, project.hasAPIVersion = object["sourceApiVersion"]
	if raw, present := object["packageDirectories"]; present && raw != nil {
		directories, isList := raw.([]any)
		if !isList {
			return nil, invalid("packageDirectories: Invalid input: expected array")
		}
		seen := map[string]bool{}
		for index, item := range directories {
			directory, isObject := item.(map[string]any)
			if !isObject {
				return nil, invalid(fmt.Sprintf("packageDirectories.%d: Invalid input: expected object", index))
			}
			directoryPath, isText := directory["path"].(string)
			if !isText {
				return nil, invalid(fmt.Sprintf("packageDirectories.%d.path: Invalid input: expected string", index))
			}
			entry := normalisePackageDirectory(directoryPath)
			if !seen[entry] {
				seen[entry] = true
				project.entries = append(project.entries, entry)
			}
		}
		sort.SliceStable(project.entries, func(i, j int) bool { return len(project.entries[i]) > len(project.entries[j]) })
	}
	return project, nil
}

// packageDirectoryOf returns the longest package directory that contains
// the path, or noPackageDirectory.
func (p *sfdxProject) packageDirectoryOf(filePath string) string {
	for _, entry := range p.entries {
		if entry == "" || filePath == entry || strings.HasPrefix(filePath, entry+"/") {
			return entry
		}
	}
	return noPackageDirectory
}

// loadSfdxProject reads sfdx-project.json at a revision.  A revision
// without the file has no package directories.
func loadSfdxProject(ctx *RunContext, revision string) (*sfdxProject, error) {
	content, present, err := ctx.Git.RootFileContent(revision, sfdxProjectFile)
	if err != nil {
		return nil, fmt.Errorf("Cannot read %s: %s", sfdxProjectFile, err.Error())
	}
	if !present {
		return &sfdxProject{}, nil
	}
	return parseSfdxProject(content)
}
