package delta

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ForceCLI/sfdx-git-delta-plugin/internal/gittest"
)

const classMeta = `<?xml version="1.0" encoding="UTF-8"?>
<ApexClass xmlns="http://soap.sforce.com/2006/04/metadata">
    <apiVersion>62.0</apiVersion>
</ApexClass>
`

func labels(entries ...string) string {
	var body strings.Builder
	body.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<CustomLabels xmlns=\"http://soap.sforce.com/2006/04/metadata\">\n")
	for i := 0; i+1 < len(entries); i += 2 {
		body.WriteString("    <labels>\n        <fullName>" + entries[i] + "</fullName>\n        <value>" + entries[i+1] + "</value>\n    </labels>\n")
	}
	body.WriteString("</CustomLabels>\n")
	return body.String()
}

// scenario is a repository with a "from" and a "to" commit and an output
// directory.
type scenario struct {
	t      *testing.T
	repo   *gittest.Repo
	from   string
	to     string
	output string
}

func newScenario(t *testing.T, before func(repo *gittest.Repo), after func(repo *gittest.Repo)) *scenario {
	t.Helper()
	repo := gittest.New(t)
	repo.Write("README.md", "project")
	before(repo)
	from := repo.Commit("from")
	after(repo)
	to := repo.Commit("to")
	return &scenario{t: t, repo: repo, from: from, to: to, output: filepath.Join(t.TempDir(), "output")}
}

func (s *scenario) run(change func(input *Input)) *Work {
	s.t.Helper()
	work, err := s.tryRun(change)
	if err != nil {
		s.t.Fatal(err)
	}
	return work
}

func (s *scenario) tryRun(change func(input *Input)) (*Work, error) {
	input := Input{
		Config:     Config{From: s.from, To: s.to, Output: s.output, Repo: s.repo.Dir, APIVersion: 62},
		SourceDirs: []string{"./"},
	}
	if change != nil {
		change(&input)
	}
	return Run(input)
}

func (s *scenario) read(path string) string {
	s.t.Helper()
	content, err := os.ReadFile(filepath.Join(s.output, filepath.FromSlash(path)))
	if err != nil {
		s.t.Fatal(err)
	}
	return string(content)
}

func (s *scenario) exists(path string) bool {
	_, err := os.Stat(filepath.Join(s.output, filepath.FromSlash(path)))
	return err == nil
}

func packageXML(version string, types ...string) string {
	var body strings.Builder
	body.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<Package xmlns=\"http://soap.sforce.com/2006/04/metadata\">\n")
	for _, block := range types {
		parts := strings.Split(block, ":")
		body.WriteString("    <types>\n")
		for _, member := range strings.Split(parts[1], ",") {
			body.WriteString("        <members>" + member + "</members>\n")
		}
		body.WriteString("        <name>" + parts[0] + "</name>\n    </types>\n")
	}
	body.WriteString("    <version>" + version + "</version>\n</Package>")
	return body.String()
}

func expectFile(t *testing.T, s *scenario, path, want string) {
	t.Helper()
	if got := s.read(path); got != want {
		t.Errorf("%s =\n%s\nwant\n%s", path, got, want)
	}
}

func TestAddedModifiedAndDeletedClasses(t *testing.T) {
	s := newScenario(t, func(repo *gittest.Repo) {
		repo.Write(
			"force-app/main/default/classes/Modified.cls", "public class Modified {}",
			"force-app/main/default/classes/Modified.cls-meta.xml", classMeta,
			"force-app/main/default/classes/Deleted.cls", "public class Deleted {}",
			"force-app/main/default/classes/Deleted.cls-meta.xml", classMeta,
			"force-app/main/default/classes/Same.cls", "public class Same {}",
		)
	}, func(repo *gittest.Repo) {
		repo.Write(
			"force-app/main/default/classes/Modified.cls", "public class Modified { void run() {} }",
			"force-app/main/default/classes/Added.cls", "public class Added {}",
			"force-app/main/default/classes/Added.cls-meta.xml", classMeta,
			"docs/notes.txt", "not metadata",
		)
		repo.Remove("force-app/main/default/classes/Deleted.cls", "force-app/main/default/classes/Deleted.cls-meta.xml")
	})
	work := s.run(func(input *Input) { input.GenerateDelta = true })
	if len(work.Warnings) != 0 {
		t.Errorf("warnings = %q", work.Warnings)
	}
	expectFile(t, s, "package/package.xml", packageXML("62.0", "ApexClass:Added,Modified"))
	expectFile(t, s, "destructiveChanges/destructiveChanges.xml", packageXML("62.0", "ApexClass:Deleted"))
	expectFile(t, s, "destructiveChanges/package.xml", packageXML("62.0"))
	expectFile(t, s, "force-app/main/default/classes/Added.cls", "public class Added {}")
	expectFile(t, s, "force-app/main/default/classes/Added.cls-meta.xml", classMeta)
	// The meta file of a modified class is copied with it.
	expectFile(t, s, "force-app/main/default/classes/Modified.cls-meta.xml", classMeta)
	for _, absent := range []string{"force-app/main/default/classes/Same.cls", "force-app/main/default/classes/Deleted.cls", "docs/notes.txt"} {
		if s.exists(absent) {
			t.Errorf("%s was written", absent)
		}
	}
}

func TestManifestsWithoutGenerateDeltaWriteNoSource(t *testing.T) {
	s := newScenario(t, func(repo *gittest.Repo) {}, func(repo *gittest.Repo) {
		repo.Write("force-app/main/default/classes/Added.cls", "public class Added {}")
	})
	s.run(nil)
	expectFile(t, s, "package/package.xml", packageXML("62.0", "ApexClass:Added"))
	if s.exists("force-app") {
		t.Error("source files were written without generateDelta")
	}
}

func TestLabelsAreComparedElementByElement(t *testing.T) {
	path := "force-app/main/default/labels/CustomLabels.labels-meta.xml"
	s := newScenario(t, func(repo *gittest.Repo) {
		repo.Write(path, labels("Kept", "same", "Changed", "before", "Removed", "x"))
	}, func(repo *gittest.Repo) {
		repo.Write(path, labels("Kept", "same", "Changed", "after", "New", "y"))
	})
	s.run(func(input *Input) { input.GenerateDelta = true })
	expectFile(t, s, "package/package.xml", packageXML("62.0", "CustomLabel:Changed,New"))
	expectFile(t, s, "destructiveChanges/destructiveChanges.xml", packageXML("62.0", "CustomLabel:Removed"))
	expectFile(t, s, path, labels("Changed", "after", "New", "y"))
}

func TestMalformedXMLIsAWarning(t *testing.T) {
	path := "force-app/main/default/labels/CustomLabels.labels-meta.xml"
	s := newScenario(t, func(repo *gittest.Repo) {
		repo.Write(path, labels("A", "1"))
	}, func(repo *gittest.Repo) {
		repo.Write(path, "<CustomLabels><labels><fullName>A</fullName>")
	})
	work := s.run(nil)
	if len(work.Warnings) != 1 || !strings.HasPrefix(work.Warnings[0], "could not process '"+path+"'") {
		t.Errorf("warnings = %q", work.Warnings)
	}
	expectFile(t, s, "package/package.xml", packageXML("62.0"))
}

func TestMovedComponentIsNeitherDeployedNorDeleted(t *testing.T) {
	s := newScenario(t, func(repo *gittest.Repo) {
		repo.Write(
			"sfdx-project.json", `{"packageDirectories":[{"path":"force-app"}]}`,
			"force-app/main/default/classes/Moved.cls", "public class Moved {}",
			"force-app/main/default/classes/Moved.cls-meta.xml", classMeta,
			"force-app/main/default/classes/MovedAndChanged.cls", "public class MovedAndChanged {}",
		)
	}, func(repo *gittest.Repo) {
		repo.Move("force-app/main/default/classes/Moved.cls", "force-app/feature/classes/Moved.cls")
		repo.Move("force-app/main/default/classes/Moved.cls-meta.xml", "force-app/feature/classes/Moved.cls-meta.xml")
		repo.Remove("force-app/main/default/classes/MovedAndChanged.cls")
		repo.Write("force-app/feature/classes/MovedAndChanged.cls", "public class MovedAndChanged { void run() {} }")
	})
	work := s.run(nil)
	if len(work.Warnings) != 0 {
		t.Errorf("warnings = %q", work.Warnings)
	}
	expectFile(t, s, "package/package.xml", packageXML("62.0", "ApexClass:MovedAndChanged"))
	expectFile(t, s, "destructiveChanges/destructiveChanges.xml", packageXML("62.0"))
}

func TestIgnoreAndIncludeFiles(t *testing.T) {
	s := newScenario(t, func(repo *gittest.Repo) {
		repo.Write(
			"force-app/main/default/classes/Unchanged.cls", "public class Unchanged {}",
			"force-app/main/default/classes/DeletedButKept.cls", "x",
		)
	}, func(repo *gittest.Repo) {
		repo.Write(
			"force-app/main/default/classes/Ignored.cls", "public class Ignored {}",
			"force-app/main/default/classes/Added.cls", "public class Added {}",
		)
		repo.Remove("force-app/main/default/classes/DeletedButKept.cls")
	})
	controls := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(controls, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	s.run(func(input *Input) {
		input.Ignore = write("ignore", "**/Ignored.cls\n")
		input.IgnoreDestructive = write("ignoreDestructive", "**/DeletedButKept.cls\n")
		input.Include = write("include", "**/Unchanged.cls\n")
	})
	expectFile(t, s, "package/package.xml", packageXML("62.0", "ApexClass:Added,Unchanged"))
	expectFile(t, s, "destructiveChanges/destructiveChanges.xml", packageXML("62.0"))
}

func TestDeletedRecordTypeIsOmittedWithAWarning(t *testing.T) {
	path := "force-app/main/default/objects/Account/recordTypes/Partner.recordType-meta.xml"
	s := newScenario(t, func(repo *gittest.Repo) {
		repo.Write(path, "<RecordType/>", "force-app/main/default/classes/Gone.cls", "x")
	}, func(repo *gittest.Repo) {
		repo.Remove(path, "force-app/main/default/classes/Gone.cls")
	})
	work := s.run(nil)
	want := "The Metadata API cannot delete 'RecordType' components, so destructiveChanges.xml omits 'Account.Partner'. Remove them from the target org manually in Setup."
	if !reflect.DeepEqual(work.Warnings, []string{want}) {
		t.Errorf("warnings = %q", work.Warnings)
	}
	expectFile(t, s, "destructiveChanges/destructiveChanges.xml", packageXML("62.0", "ApexClass:Gone"))
}

func TestChangesManifestListsARenameOnce(t *testing.T) {
	content := strings.Repeat("public class Renamed { void run() {} }\n", 20)
	s := newScenario(t, func(repo *gittest.Repo) {
		repo.Write("force-app/main/default/classes/Before.cls", content, "force-app/main/default/classes/Edited.cls", "a")
	}, func(repo *gittest.Repo) {
		repo.Move("force-app/main/default/classes/Before.cls", "force-app/main/default/classes/After.cls")
		repo.Write("force-app/main/default/classes/Edited.cls", "b")
	})
	manifest := filepath.Join(t.TempDir(), "nested", "changes.json")
	s.run(func(input *Input) { input.ChangesManifest = manifest })
	got, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "add": {},
  "modify": {
    "ApexClass": [
      "Edited"
    ]
  },
  "delete": {},
  "rename": {
    "ApexClass": [
      {
        "from": "Before",
        "to": "After"
      }
    ]
  }
}`
	if string(got) != want {
		t.Errorf("changes manifest =\n%s\nwant\n%s", got, want)
	}
	// The manifests are the same as without rename detection.
	expectFile(t, s, "package/package.xml", packageXML("62.0", "ApexClass:After,Edited"))
	expectFile(t, s, "destructiveChanges/destructiveChanges.xml", packageXML("62.0", "ApexClass:Before"))
}

func TestWhitespaceOnlyChangeIsLeftOutOnRequest(t *testing.T) {
	s := newScenario(t, func(repo *gittest.Repo) {
		repo.Write("force-app/main/default/classes/A.cls", "public class A {\n}\n")
	}, func(repo *gittest.Repo) {
		repo.Write("force-app/main/default/classes/A.cls", "public class A {\n\n    }\n")
	})
	s.run(nil)
	expectFile(t, s, "package/package.xml", packageXML("62.0", "ApexClass:A"))
	s.run(func(input *Input) { input.IgnoreWhitespace = true })
	expectFile(t, s, "package/package.xml", packageXML("62.0"))
}

func TestSourceDirectoryScope(t *testing.T) {
	s := newScenario(t, func(repo *gittest.Repo) {}, func(repo *gittest.Repo) {
		repo.Write("force-app/main/default/classes/In.cls", "x", "other/classes/Out.cls", "y")
	})
	s.run(func(input *Input) { input.SourceDirs = []string{"force-app"} })
	expectFile(t, s, "package/package.xml", packageXML("62.0", "ApexClass:In"))

	work := s.run(func(input *Input) { input.SourceDirs = []string{"nowhere"} })
	if len(work.Warnings) != 1 || !strings.HasPrefix(work.Warnings[0], "No changes found under 'nowhere' between") {
		t.Errorf("warnings = %q", work.Warnings)
	}

	_, err := s.tryRun(func(input *Input) { input.SourceDirs = []string{"force-*", "../x", ""} })
	var configError *ConfigError
	if !errors.As(err, &configError) || !strings.Contains(err.Error(), "does not accept wildcards") || !strings.Contains(err.Error(), "'..' path segments") || !strings.Contains(err.Error(), "empty value") {
		t.Errorf("refused source directories: %v", err)
	}
}

func TestAPIVersionSources(t *testing.T) {
	s := newScenario(t, func(repo *gittest.Repo) {
		repo.Write("sfdx-project.json", `{"packageDirectories":[{"path":"force-app"}],"sourceApiVersion":"59.0"}`)
	}, func(repo *gittest.Repo) {
		repo.Write("force-app/main/default/classes/A.cls", "x")
	})
	s.run(func(input *Input) { input.APIVersion = 0 })
	expectFile(t, s, "package/package.xml", packageXML("59.0", "ApexClass:A"))

	// Without a version in the project file the highest version of the
	// lookup resource is used, with a warning.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"version":"66.0"},{"version":"67.0"}]`))
	}))
	defer server.Close()
	previous := latestAPIVersionURL
	latestAPIVersionURL = server.URL
	defer func() { latestAPIVersionURL = previous }()
	plain := newScenario(t, func(repo *gittest.Repo) {}, func(repo *gittest.Repo) {
		repo.Write("force-app/main/default/classes/A.cls", "x")
	})
	work := plain.run(func(input *Input) { input.APIVersion = 0 })
	if !reflect.DeepEqual(work.Warnings, []string{"No API version found (no --api-version flag, no sourceApiVersion in sfdx-project.json at --to), using '67'"}) {
		t.Errorf("warnings = %q", work.Warnings)
	}
	expectFile(t, plain, "package/package.xml", packageXML("67.0", "ApexClass:A"))

	latestAPIVersionURL = "http://127.0.0.1:1/unreachable"
	_, err := plain.tryRun(func(input *Input) { input.APIVersion = 0 })
	if err == nil || !strings.HasPrefix(err.Error(), "Unable to resolve the Salesforce API version.") {
		t.Errorf("failed lookup: %v", err)
	}
}

func TestInputErrors(t *testing.T) {
	s := newScenario(t, func(repo *gittest.Repo) {
		repo.Write("force-app/main/default/classes/A.cls", "x")
	}, func(repo *gittest.Repo) {})

	_, err := s.tryRun(func(input *Input) { input.From = "nosuch"; input.To = "HEAD:force-app" })
	want := "--from is not a valid sha pointer: 'nosuch' (If in CI/CD context, check the fetch depth is properly set), --to must resolve to a commit: 'HEAD:force-app' resolves to a tree. Use a commit sha, a branch, or a tag that points to a commit"
	if err == nil || err.Error() != want {
		t.Errorf("bad revisions: %v", err)
	}

	plain := t.TempDir()
	_, err = s.tryRun(func(input *Input) { input.Repo = plain })
	if err == nil || !strings.HasSuffix(err.Error(), "is not a git repository") || strings.Contains(err.Error(), ", ") {
		t.Errorf("not a repository: %v", err)
	}

	_, err = s.tryRun(func(input *Input) { input.ChangesManifest = plain })
	if err == nil || !strings.Contains(err.Error(), "already exists and is not a regular file") {
		t.Errorf("changes manifest that is a directory: %v", err)
	}
}

func TestMergeBaseReplacesFrom(t *testing.T) {
	repo := gittest.New(t)
	repo.Write("force-app/main/default/classes/Base.cls", "x")
	base := repo.Commit("base")
	repo.Write("force-app/main/default/classes/OnMain.cls", "x")
	main := repo.Commit("main")
	repo.Checkout(base)
	repo.Write("force-app/main/default/classes/OnBranch.cls", "x")
	branch := repo.Commit("branch")
	s := &scenario{t: t, repo: repo, from: main, to: branch, output: filepath.Join(t.TempDir(), "output")}

	s.run(nil)
	expectFile(t, s, "package/package.xml", packageXML("62.0", "ApexClass:OnBranch"))
	expectFile(t, s, "destructiveChanges/destructiveChanges.xml", packageXML("62.0", "ApexClass:OnMain"))

	s.run(func(input *Input) { input.MergeBase = true })
	expectFile(t, s, "package/package.xml", packageXML("62.0", "ApexClass:OnBranch"))
	expectFile(t, s, "destructiveChanges/destructiveChanges.xml", packageXML("62.0"))
}

func TestOutputWriteFailureIsAWarning(t *testing.T) {
	s := newScenario(t, func(repo *gittest.Repo) {}, func(repo *gittest.Repo) {
		repo.Write("force-app/main/default/classes/A.cls", "x")
	})
	if err := os.MkdirAll(filepath.Join(s.output, "package", "package.xml"), 0o755); err != nil {
		t.Fatal(err)
	}
	work := s.run(nil)
	if len(work.Warnings) != 1 || !strings.HasPrefix(work.Warnings[0], "PackageGenerator: Unable to write '") || !strings.Contains(work.Warnings[0], "package.xml") {
		t.Errorf("warnings = %q", work.Warnings)
	}
}

func TestPackageMembersAreEscapedAndCollated(t *testing.T) {
	manifest := newManifest()
	for _, member := range []string{"b", "A", "a", "B", "_x", "1", "A&B<C>"} {
		manifest.add("CustomLabel", member)
	}
	manifest.add("ApexClass", "Z")
	var out strings.Builder
	if err := WritePackage(&out, manifest, 62); err != nil {
		t.Fatal(err)
	}
	want := packageXML("62.0", "ApexClass:Z", "CustomLabel:_x,1,a,A,A&amp;B&lt;C&gt;,b,B")
	if out.String() != want {
		t.Errorf("package =\n%s\nwant\n%s", out.String(), want)
	}
}
