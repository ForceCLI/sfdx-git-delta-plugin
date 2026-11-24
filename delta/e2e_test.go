package delta

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ForceCLI/sfdx-git-delta-plugin/gitadapter"
)

// upstreamRepo returns the path of a checkout of sfdx-git-delta that has
// the branches origin/e2e/base and origin/e2e/head, or skips the test.
func upstreamRepo(t *testing.T) string {
	t.Helper()
	repo := os.Getenv("SGD_UPSTREAM_REPO")
	if repo == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skip("no home directory")
		}
		repo = filepath.Join(home, "git", "sfdx-git-delta")
	}
	if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
		t.Skipf("no sfdx-git-delta checkout at %s; set SGD_UPSTREAM_REPO", repo)
	}
	return repo
}

// The upstream project keeps, on the branch e2e/head, the output of the
// command below under expected/.  The port must write the same bytes.
//
//	sgd source delta --from origin/e2e/base --to origin/e2e/head -o expected -d -r . -s test
//	  -n .sgdinclude -N .sgdincludeDestructive -i .sgdignore -D .sgdignoreDestructive -W -a 67.0
func TestUpstreamEndToEndFixtures(t *testing.T) {
	repo := upstreamRepo(t)
	git, err := gitadapter.Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	const head = "origin/e2e/head"
	if _, err := git.ResolveCommit(head); err != nil {
		t.Skipf("%s is not in %s: %v", head, repo, err)
	}
	work := t.TempDir()
	controlFile := func(name string) string {
		content, err := git.StringContent(name, head)
		if err != nil {
			t.Fatalf("%s at %s: %v", name, head, err)
		}
		path := filepath.Join(work, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	output := filepath.Join(work, "expected")
	if err := os.MkdirAll(output, 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := Run(Input{
		Config: Config{
			From:               "origin/e2e/base",
			To:                 head,
			Output:             output,
			Repo:               repo,
			GenerateDelta:      true,
			IgnoreWhitespace:   true,
			APIVersion:         67,
			Include:            controlFile(".sgdinclude"),
			IncludeDestructive: controlFile(".sgdincludeDestructive"),
			Ignore:             controlFile(".sgdignore"),
			IgnoreDestructive:  controlFile(".sgdignoreDestructive"),
		},
		SourceDirs: []string{"test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, warning := range result.Warnings {
		t.Logf("warning: %s", warning)
	}

	expectedPaths, err := git.ArchivePaths("expected", head)
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]string{}
	for _, path := range expectedPaths {
		content, err := git.StringContent(path, head)
		if err != nil {
			t.Fatal(err)
		}
		expected[strings.TrimPrefix(path, "expected/")] = content
	}
	written := map[string]string{}
	err = filepath.Walk(output, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(output, path)
		written[filepath.ToSlash(relative)] = string(content)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var missing, unexpected, different []string
	for path, content := range expected {
		got, ok := written[path]
		switch {
		case !ok:
			missing = append(missing, path)
		case got != content:
			different = append(different, path)
		}
	}
	for path := range written {
		if _, ok := expected[path]; !ok {
			unexpected = append(unexpected, path)
		}
	}
	sort.Strings(missing)
	sort.Strings(unexpected)
	sort.Strings(different)
	t.Logf("%d expected files, %d written", len(expected), len(written))
	for _, path := range missing {
		t.Errorf("not written: %s", path)
	}
	for _, path := range unexpected {
		t.Errorf("written but not expected: %s", path)
	}
	for _, path := range different {
		t.Errorf("content differs: %s\n--- got ---\n%s\n--- want ---\n%s", path, written[path], expected[path])
	}
}
