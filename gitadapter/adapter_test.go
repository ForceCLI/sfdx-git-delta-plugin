package gitadapter

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ForceCLI/sfdx-git-delta-plugin/internal/gittest"
)

func open(t *testing.T, repo *gittest.Repo) *Adapter {
	t.Helper()
	adapter, err := Open(repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func TestOpenRefusesADirectoryThatIsNotARepository(t *testing.T) {
	_, err := Open(t.TempDir())
	var refusal *RepositoryRefusalError
	if !errors.As(err, &refusal) || !strings.HasSuffix(refusal.Message, "is not a git repository") {
		t.Errorf("Open of a plain directory = %v", err)
	}
}

func TestResolveCommit(t *testing.T) {
	repo := gittest.New(t)
	repo.Write("classes/A.cls", "a")
	first := repo.Commit("first")
	repo.Write("classes/A.cls", "b")
	second := repo.Commit("second")
	repo.Tag("v1", first)
	repo.Branch("feature", first)
	adapter := open(t, repo)

	for ref, want := range map[string]string{
		"HEAD":         second,
		"HEAD~1":       first,
		"HEAD^":        first,
		"feature":      first,
		"v1":           first,
		first:          first,
		first[:8]:      first,
		"refs/tags/v1": first,
	} {
		got, err := adapter.ResolveCommit(ref)
		if err != nil || got != want {
			t.Errorf("ResolveCommit(%q) = %q, %v; want %q", ref, got, err, want)
		}
	}

	_, err := adapter.ResolveCommit("nosuch")
	var invalid *InvalidRevisionError
	if !errors.As(err, &invalid) || err.Error() != "nosuch: not a valid git revision" {
		t.Errorf("ResolveCommit of an unknown name = %v", err)
	}
	for ref, objectType := range map[string]string{"HEAD:classes": "tree", "HEAD:classes/A.cls": "blob", "HEAD:": "tree"} {
		_, err := adapter.ResolveCommit(ref)
		var notACommit *NotACommitError
		if !errors.As(err, &notACommit) || notACommit.ObjectType != objectType || notACommit.Label != ref {
			t.Errorf("ResolveCommit(%q) = %v, want a %s", ref, err, objectType)
		}
	}
}

func TestMergeBase(t *testing.T) {
	repo := gittest.New(t)
	repo.Write("a", "1")
	base := repo.Commit("base")
	repo.Write("a", "2")
	left := repo.Commit("left")
	repo.Checkout(base)
	repo.Write("b", "3")
	right := repo.Commit("right")
	adapter := open(t, repo)
	got, found, err := adapter.MergeBase(left, right)
	if err != nil || !found || got != base {
		t.Errorf("MergeBase = %q, %v, %v; want %q", got, found, err, base)
	}
}

func TestDiffLines(t *testing.T) {
	repo := gittest.New(t)
	repo.Write(
		"force-app/classes/Kept.cls", "same",
		"force-app/classes/Modified.cls", "before",
		"force-app/classes/Deleted.cls", "gone",
		"force-app/classes/Spaces.cls", "a b\n  c\n",
		"other/classes/Other.cls", "before",
	)
	from := repo.Commit("from")
	repo.Remove("force-app/classes/Deleted.cls")
	repo.Write(
		"force-app/classes/Modified.cls", "after",
		"force-app/classes/Added.cls", "new",
		"force-app/classes/Spaces.cls", "a  b\n\n\tc\n\n",
		"other/classes/Other.cls", "after",
	)
	to := repo.Commit("to")
	adapter := open(t, repo)

	result, err := adapter.DiffLines(DiffSpec{From: from, To: to}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"A\tforce-app/classes/Added.cls",
		"D\tforce-app/classes/Deleted.cls",
		"M\tforce-app/classes/Modified.cls",
		"M\tforce-app/classes/Spaces.cls",
		"M\tother/classes/Other.cls",
	}
	if !reflect.DeepEqual(result.Lines, want) || result.ChangesSeen != 5 {
		t.Errorf("lines = %q (%d changes), want %q", result.Lines, result.ChangesSeen, want)
	}

	result, err = adapter.DiffLines(DiffSpec{From: from, To: to, IgnoreWhitespace: true}, []string{"force-app"})
	if err != nil {
		t.Fatal(err)
	}
	want = []string{
		"A\tforce-app/classes/Added.cls",
		"D\tforce-app/classes/Deleted.cls",
		"M\tforce-app/classes/Modified.cls",
	}
	if !reflect.DeepEqual(result.Lines, want) || result.ChangesSeen != 4 {
		t.Errorf("scoped lines without white space changes = %q (%d changes), want %q", result.Lines, result.ChangesSeen, want)
	}

	result, err = adapter.DiffLines(DiffSpec{From: from, To: to}, []string{"nowhere"})
	if err != nil || len(result.Lines) != 0 || result.ChangesSeen != 5 {
		t.Errorf("lines outside every scope = %q (%d changes), %v", result.Lines, result.ChangesSeen, err)
	}
}

func TestEqualIgnoringWhitespace(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"a b\n", "a  b\n", true},
		{"a b\n", "ab\n", true},
		{"a\nb\n", "a\n\n\nb", true},
		{"a\r\nb\r\n", "a\nb\n", true},
		{"  a\n", "a", true},
		{"ab\n", "a\nb\n", false},
		{"a\n", "b\n", false},
		{"a\n", "a\nb\n", false},
		{"", "\n \n", true},
	}
	for _, test := range tests {
		if got := equalIgnoringWhitespace([]byte(test.a), []byte(test.b)); got != test.want {
			t.Errorf("equalIgnoringWhitespace(%q, %q) = %v, want %v", test.a, test.b, got, test.want)
		}
	}
}

func TestDiffLinesWithRenames(t *testing.T) {
	repo := gittest.New(t)
	content := strings.Repeat("public class A { void run() {} }\n", 20)
	repo.Write("force-app/classes/A.cls", content, "force-app/classes/Stay.cls", "x")
	from := repo.Commit("from")
	repo.Move("force-app/classes/A.cls", "force-app/classes/B.cls")
	to := repo.Commit("to")
	adapter := open(t, repo)

	plain, err := adapter.DiffLines(DiffSpec{From: from, To: to}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"D\tforce-app/classes/A.cls", "A\tforce-app/classes/B.cls"}; !reflect.DeepEqual(plain.Lines, want) {
		t.Errorf("without rename detection: %q, want %q", plain.Lines, want)
	}
	renamed, err := adapter.DiffLines(DiffSpec{From: from, To: to, DetectRenames: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"R100\tforce-app/classes/A.cls\tforce-app/classes/B.cls"}; !reflect.DeepEqual(renamed.Lines, want) {
		t.Errorf("with rename detection: %q, want %q", renamed.Lines, want)
	}
	// A rename with one side outside the scope is the change of the side
	// that is in scope.
	repo.Move("force-app/classes/B.cls", "other/classes/B.cls")
	moved := repo.Commit("moved out")
	scoped, err := adapter.DiffLines(DiffSpec{From: to, To: moved, DetectRenames: true}, []string{"force-app"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"D\tforce-app/classes/B.cls"}; !reflect.DeepEqual(scoped.Lines, want) {
		t.Errorf("rename out of scope: %q, want %q", scoped.Lines, want)
	}
}

func TestContentTreeIndexAndGrep(t *testing.T) {
	repo := gittest.New(t)
	oid := strings.Repeat("ab", 32)
	repo.Write(
		"force-app/objects/Account/Account.object-meta.xml", "<CustomObject/>",
		"force-app/objects/Account/fields/Parent__c.field-meta.xml", "<type>MasterDetail</type>",
		"force-app/objects/Account/fields/Name__c.field-meta.xml", "<type>Text</type>",
		"force-app/staticresources/big.bin", "version https://git-lfs.github.com/spec/v1\noid sha256:"+oid+"\nsize 5\n",
		"sfdx-project.json", `{"sourceApiVersion":"61.0"}`,
	)
	commit := repo.Commit("one")
	lfsDir := filepath.Join(repo.Dir, ".git", "lfs", "objects", oid[0:2], oid[2:4])
	if err := os.MkdirAll(lfsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lfsDir, oid), []byte("large"), 0o644); err != nil {
		t.Fatal(err)
	}
	adapter := open(t, repo)

	if content, err := adapter.StringContent("force-app/objects/Account/Account.object-meta.xml", commit); err != nil || content != "<CustomObject/>" {
		t.Errorf("StringContent = %q, %v", content, err)
	}
	if content, err := adapter.StringContent("force-app/staticresources/big.bin", commit); err != nil || content != "large" {
		t.Errorf("content of a Git LFS pointer = %q, %v; want the object it names", content, err)
	}
	if _, err := adapter.StringContent("missing", commit); err == nil {
		t.Error("StringContent of a missing path succeeded")
	}
	if content, present, err := adapter.RootFileContent(commit, "sfdx-project.json"); err != nil || !present || !strings.Contains(content, "61.0") {
		t.Errorf("RootFileContent = %q, %v, %v", content, present, err)
	}
	if _, present, err := adapter.RootFileContent(commit, "absent.json"); err != nil || present {
		t.Errorf("RootFileContent of an absent file: present %v, err %v", present, err)
	}

	got := adapter.GrepUnderPaths("<type>MasterDetail</type>", []string{"force-app/objects/Account/fields"}, commit)
	if want := []string{"force-app/objects/Account/fields/Parent__c.field-meta.xml"}; !reflect.DeepEqual(got, want) {
		t.Errorf("GrepUnderPaths = %q, want %q", got, want)
	}
	got = adapter.GrepMatchingPathspecs("Text", []string{"./*.field-meta.xml"}, commit)
	if want := []string{"force-app/objects/Account/fields/Name__c.field-meta.xml"}; !reflect.DeepEqual(got, want) {
		t.Errorf("GrepMatchingPathspecs = %q, want %q", got, want)
	}

	index := adapter.BuildTreeIndex(commit, []string{"force-app/objects"})
	if index == nil || index.Size() != 3 {
		t.Fatalf("scoped index = %+v", index)
	}
	if !index.FileExists("force-app/objects/Account/Account.object-meta.xml") || index.FileExists("force-app/objects/Account") || !index.PathExists("force-app/objects/Account") || index.PathExists("force-app/staticresources") {
		t.Error("the scoped index answers path questions wrongly")
	}
	if children := index.ListChildren("force-app/objects/Account"); !reflect.DeepEqual(children, []string{"Account.object-meta.xml", "fields"}) {
		t.Errorf("ListChildren = %q", children)
	}
	if files := index.FilesUnder("force-app/objects/Account/fields"); len(files) != 2 {
		t.Errorf("FilesUnder = %q", files)
	}
	if adapter.BuildTreeIndex("nosuch", nil) != nil {
		t.Error("an index was built for an unknown revision")
	}
	if empty := adapter.BuildTreeIndex(EmptyTreeID, nil); empty == nil || empty.Size() != 0 {
		t.Error("the empty tree is not an empty index")
	}
}

func TestScopeAndPathspecMatchers(t *testing.T) {
	inScope := ScopeMatcher([]string{"force-app", "other/dir"})
	for path, want := range map[string]bool{
		"force-app/classes/A.cls": true,
		"force-app":               true,
		"force-application/A.cls": false,
		"other/dir/x":             true,
		"other/x":                 false,
		"nested/force-app/A.cls":  false,
	} {
		if got := inScope(path); got != want {
			t.Errorf("ScopeMatcher(%q) = %v, want %v", path, got, want)
		}
	}
	if !ScopeMatcher([]string{"."})("anything/at/all") {
		t.Error("the root scope does not match every path")
	}
	matches := PathspecMatcher([]string{"./force-app/*.translation-meta.xml", "docs"})
	for path, want := range map[string]bool{
		"force-app/main/translations/fr.translation-meta.xml": true,
		"other/translations/fr.translation-meta.xml":          false,
		"docs/readme.md":  true,
		"docsx/readme.md": false,
	} {
		if got := matches(path); got != want {
			t.Errorf("PathspecMatcher(%q) = %v, want %v", path, got, want)
		}
	}
}
