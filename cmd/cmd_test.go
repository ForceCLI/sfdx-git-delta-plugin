package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ForceCLI/sfdx-git-delta-plugin/internal/gittest"
)

func run(args ...string) (string, string, int) {
	var out, errOut bytes.Buffer
	status := Run(args, &out, &errOut)
	return out.String(), errOut.String(), status
}

type envelope struct {
	Status int `json:"status"`
	Result struct {
		OutputDir string `json:"output-dir"`
		Error     string `json:"error"`
	} `json:"result"`
	Warnings []string `json:"warnings"`
	Message  string   `json:"message"`
	ExitCode int      `json:"exitCode"`
}

func decode(t *testing.T, text string) envelope {
	t.Helper()
	var parsed envelope
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, text)
	}
	return parsed
}

// project returns a repository with one commit that adds a class after an
// empty first commit, and an output directory.
func project(t *testing.T) (repo *gittest.Repo, output string) {
	t.Helper()
	t.Setenv("SF_ORG_API_VERSION", "")
	repo = gittest.New(t)
	repo.Write("README.md", "x")
	repo.Commit("first")
	repo.Write("force-app/main/default/classes/A.cls", "public class A {}")
	repo.Commit("second")
	output = filepath.Join(t.TempDir(), "out")
	if err := os.MkdirAll(output, 0o755); err != nil {
		t.Fatal(err)
	}
	return repo, output
}

func TestManifestIsValid(t *testing.T) {
	manifest := Manifest()
	if err := manifest.Validate(); err != nil {
		t.Fatal(err)
	}
	if manifest.Commands[0].ID != "sgd source delta" || manifest.Commands[0].OrgRequirement() != "none" {
		t.Errorf("manifest = %+v", manifest)
	}
}

func TestHelp(t *testing.T) {
	out, _, status := run("sgd", "source", "delta", "--help")
	if status != 0 || !strings.Contains(out, "incremental") || !strings.Contains(out, "--changes-manifest") {
		t.Errorf("status %d, stdout %s", status, out)
	}
}

func TestMissingFromExitsWithStatus2(t *testing.T) {
	out, _, status := run("sgd", "source", "delta", "--json")
	parsed := decode(t, out)
	if status != 2 || parsed.ExitCode != 2 || !strings.Contains(parsed.Message, "Missing required flag from") {
		t.Errorf("status %d, envelope %+v", status, parsed)
	}
	_, errOut, status := run("sgd", "source", "delta")
	if status != 2 || !strings.Contains(errOut, "Missing required flag from") {
		t.Errorf("without --json: status %d, stderr %s", status, errOut)
	}
}

func TestUnknownFlagExitsWithStatus2(t *testing.T) {
	_, errOut, status := run("sgd", "source", "delta", "--from", "HEAD", "--nosuch")
	if status != 2 || !strings.Contains(errOut, "Nonexistent flag: --nosuch") {
		t.Errorf("status %d, stderr %s", status, errOut)
	}
}

func TestPathsThatMustExist(t *testing.T) {
	for flag, want := range map[string]string{
		"--repo-dir":                     "No directory found at /non/existing/path",
		"--output-dir":                   "No directory found at /non/existing/path",
		"--ignore-file":                  "No file found at /non/existing/path",
		"--ignore-destructive-file":      "No file found at /non/existing/path",
		"--include-file":                 "No file found at /non/existing/path",
		"--include-destructive-file":     "No file found at /non/existing/path",
		"--additional-metadata-registry": "No file found at /non/existing/path",
	} {
		out, _, status := run("sgd", "source", "delta", "--from", "HEAD~1", flag, "/non/existing/path", "--json")
		parsed := decode(t, out)
		if status != 1 || parsed.Message != want {
			t.Errorf("%s: status %d, message %q, want %q", flag, status, parsed.Message, want)
		}
	}
}

func TestRunWritesManifestsAndPrintsTheEnvelope(t *testing.T) {
	repo, output := project(t)
	out, errOut, status := run("sgd", "source", "delta", "--from", "HEAD~1", "-r", repo.Dir, "-o", output, "-a", "62.0", "--json")
	parsed := decode(t, out)
	if status != 0 || parsed.Status != 0 || parsed.Result.OutputDir != output || parsed.Result.Error != "" || len(parsed.Warnings) != 0 || errOut != "" {
		t.Fatalf("status %d, envelope %+v, stderr %s", status, parsed, errOut)
	}
	manifest, err := os.ReadFile(filepath.Join(output, "package", "package.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifest), "<members>A</members>") || !strings.Contains(string(manifest), "<version>62.0</version>") {
		t.Errorf("package.xml = %s", manifest)
	}
}

func TestColonFormAndHumanOutput(t *testing.T) {
	repo, output := project(t)
	out, errOut, status := run("sgd:source:delta", "--from", "HEAD~1", "--repo-dir", repo.Dir, "--output-dir", output, "--api-version", "62")
	if status != 0 || out != "Generating incremental package... Success\n" || errOut != "" {
		t.Errorf("status %d, stdout %q, stderr %q", status, out, errOut)
	}
}

func TestFailureExitsWithStatus1AndReportsTheError(t *testing.T) {
	repo, output := project(t)
	out, _, status := run("sgd", "source", "delta", "--from", "invalid_sha_that_does_not_exist", "-r", repo.Dir, "-o", output, "-a", "62", "--json")
	parsed := decode(t, out)
	if status != 1 || parsed.Status != 1 || !strings.Contains(parsed.Result.Error, "--from is not a valid sha pointer: 'invalid_sha_that_does_not_exist'") {
		t.Errorf("status %d, envelope %+v", status, parsed)
	}
	out, _, status = run("sgd", "source", "delta", "--from", "HEAD", "--to", "HEAD:force-app", "-r", repo.Dir, "-o", output, "-a", "62")
	if status != 1 || !strings.HasPrefix(out, "Generating incremental package... Failure: --to must resolve to a commit: 'HEAD:force-app' resolves to a tree") {
		t.Errorf("status %d, stdout %q", status, out)
	}
}

func TestChangesManifestWithoutAValueUsesTheOutputDirectory(t *testing.T) {
	repo, output := project(t)
	for _, args := range [][]string{
		{"--changes-manifest"},
		{"-c", "--json"},
	} {
		os.Remove(filepath.Join(output, "changes.manifest.json"))
		full := append([]string{"sgd", "source", "delta", "--from", "HEAD~1", "-r", repo.Dir, "-o", output, "-a", "62"}, args...)
		if _, errOut, status := run(full...); status != 0 {
			t.Fatalf("%v: status %d, stderr %s", args, status, errOut)
		}
		content, err := os.ReadFile(filepath.Join(output, "changes.manifest.json"))
		if err != nil || !strings.Contains(string(content), `"ApexClass"`) {
			t.Errorf("%v: changes.manifest.json = %s, %v", args, content, err)
		}
	}
	explicit := filepath.Join(t.TempDir(), "changes.json")
	if _, _, status := run("sgd", "source", "delta", "--from", "HEAD~1", "-r", repo.Dir, "-o", output, "-a", "62", "-c", explicit); status != 0 {
		t.Fatalf("status %d", status)
	}
	if _, err := os.Stat(explicit); err != nil {
		t.Errorf("the named changes manifest was not written: %v", err)
	}
}

func TestDeprecatedFlagNamesWarn(t *testing.T) {
	repo, output := project(t)
	out, _, status := run("sgd", "source", "delta", "--from", "HEAD~1", "--repo", repo.Dir, "--output", output, "--source", "force-app", "-a", "62", "--json")
	parsed := decode(t, out)
	if status != 0 || len(parsed.Warnings) != 3 {
		t.Fatalf("status %d, envelope %+v", status, parsed)
	}
	if parsed.Warnings[0] != `The "--output" flag has been deprecated. Use "--output-dir | -o" instead.` {
		t.Errorf("first warning = %s", parsed.Warnings[0])
	}
	if _, err := os.Stat(filepath.Join(output, "package", "package.xml")); err != nil {
		t.Errorf("the former flag names did not set the output directory: %v", err)
	}
}

func TestAPIVersionFromTheEnvironment(t *testing.T) {
	repo, output := project(t)
	t.Setenv("SF_ORG_API_VERSION", "58.0")
	if _, errOut, status := run("sgd", "source", "delta", "--from", "HEAD~1", "-r", repo.Dir, "-o", output); status != 0 {
		t.Fatalf("status %d, stderr %s", status, errOut)
	}
	manifest, _ := os.ReadFile(filepath.Join(output, "package", "package.xml"))
	if !strings.Contains(string(manifest), "<version>58.0</version>") {
		t.Errorf("package.xml = %s", manifest)
	}
	_, errOut, status := run("sgd", "source", "delta", "--from", "HEAD~1", "-r", repo.Dir, "-o", output, "-a", "latest")
	if status != 2 || !strings.Contains(errOut, "is not a valid API version") {
		t.Errorf("invalid version: status %d, stderr %s", status, errOut)
	}
}

func TestRepeatedSourceDirectories(t *testing.T) {
	repo, output := project(t)
	repo.Write("other/classes/B.cls", "public class B {}", "third/classes/C.cls", "public class C {}")
	repo.Commit("more")
	if _, errOut, status := run("sgd", "source", "delta", "--from", "HEAD~2", "-r", repo.Dir, "-o", output, "-a", "62", "-s", "force-app", "-s", "other"); status != 0 {
		t.Fatalf("status %d, stderr %s", status, errOut)
	}
	manifest, _ := os.ReadFile(filepath.Join(output, "package", "package.xml"))
	if !strings.Contains(string(manifest), "<members>A</members>") || !strings.Contains(string(manifest), "<members>B</members>") || strings.Contains(string(manifest), "<members>C</members>") {
		t.Errorf("package.xml = %s", manifest)
	}
}
