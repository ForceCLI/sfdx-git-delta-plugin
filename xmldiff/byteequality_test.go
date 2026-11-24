package xmldiff

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ForceCLI/sfdx-git-delta-plugin/registry"
)

type expectedEntry struct {
	Type   string `json:"type"`
	Member string `json:"member"`
}

type expectation struct {
	HasPackageContent bool            `json:"hasPackageContent"`
	Added             []expectedEntry `json:"added"`
	Modified          []expectedEntry `json:"modified"`
	Deleted           []expectedEntry `json:"deleted"`
}

func entries(list []CompareEntry) []expectedEntry {
	result := []expectedEntry{}
	for _, entry := range list {
		result = append(result, expectedEntry{Type: entry.Type, Member: entry.Member})
	}
	return result
}

// The fixtures are the ones of the upstream project: two revisions of a
// file, the expected manifest entries, and the exact bytes of the pruned
// file when one is written.
func TestByteEqualityFixtures(t *testing.T) {
	repository, err := registry.Load("")
	if err != nil {
		t.Fatal(err)
	}
	attributes := AttributesOf(repository)
	root := filepath.Join("testdata", "byteEquality")
	directories, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(directories) != 17 {
		t.Fatalf("found %d fixtures, want 17", len(directories))
	}
	for _, directory := range directories {
		name := directory.Name()
		read := func(file string) string {
			data, err := os.ReadFile(filepath.Join(root, name, file))
			if err != nil {
				t.Fatal(err)
			}
			return string(data)
		}
		for _, generateDelta := range []bool{true, false} {
			label := name
			if !generateDelta {
				label += "/no-delta"
			}
			t.Run(label, func(t *testing.T) {
				var want expectation
				if err := json.Unmarshal([]byte(read("expected.json")), &want); err != nil {
					t.Fatal(err)
				}
				outcome, err := Compare(attributes, generateDelta, read("from.xml"), read("to.xml"))
				if err != nil {
					t.Fatal(err)
				}
				if outcome.HasPackageContent != want.HasPackageContent {
					t.Errorf("hasPackageContent = %v, want %v", outcome.HasPackageContent, want.HasPackageContent)
				}
				for _, check := range []struct {
					kind      string
					got, want []expectedEntry
				}{
					{"added", entries(outcome.Added), want.Added},
					{"modified", entries(outcome.Modified), want.Modified},
					{"deleted", entries(outcome.Deleted), want.Deleted},
				} {
					if !reflect.DeepEqual(check.got, check.want) {
						t.Errorf("%s = %v, want %v", check.kind, check.got, check.want)
					}
				}
				expected, err := os.ReadFile(filepath.Join(root, name, "expected.xml"))
				hasSnapshot := !errors.Is(err, fs.ErrNotExist)
				if !generateDelta {
					if outcome.Writer != nil {
						t.Error("a writer was produced without generateDelta")
					}
					return
				}
				if outcome.Writer == nil {
					if hasSnapshot {
						t.Error("no writer was produced, but the fixture has expected.xml")
					}
					return
				}
				if !hasSnapshot {
					t.Fatal("a writer was produced, but the fixture has no expected.xml")
				}
				var out bytes.Buffer
				if err := outcome.Writer(&out); err != nil {
					t.Fatal(err)
				}
				if out.String() != string(expected) {
					t.Errorf("written file differs from expected.xml\n--- got ---\n%s\n--- want ---\n%s", out.String(), expected)
				}
			})
		}
	}
}
