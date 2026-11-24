package delta

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ForceCLI/sfdx-git-delta-plugin/gitadapter"
)

// ConfigError is a problem with the input of a run.
type ConfigError struct {
	Message string
}

func (e *ConfigError) Error() string {
	return e.Message
}

// Input is the input of a run as typed by the user.
type Input struct {
	Config
	// SourceDirs are the values of --source-dir before they are checked.
	SourceDirs []string
}

var (
	pathSeparators = regexp.MustCompile(`[/\\]+`)
	windowsDrive   = regexp.MustCompile(`^[A-Za-z]:[\\/]`)
)

func treatPathSep(value string) string {
	return pathSeparators.ReplaceAllString(value, "/")
}

// sanitizePath writes a path with forward slashes and without "." and
// ".." segments that can be resolved.  A trailing slash is kept.
func sanitizePath(value string) string {
	if value == "" {
		return value
	}
	treated := treatPathSep(value)
	cleaned := path.Clean(treated)
	if strings.HasSuffix(treated, "/") && cleaned != "/" {
		cleaned += "/"
	}
	return cleaned
}

var sourceDirMessages = map[string]string{
	"empty":    "error.SourceDirIsEmpty",
	"magic":    "error.SourceDirUsesPathspecMagic",
	"wildcard": "error.SourceDirContainsWildcard",
	"absolute": "error.SourceDirIsAbsolute",
	"escapes":  "error.SourceDirEscapesRepository",
}

// parseSourceDirs checks the source directories and returns them in a
// canonical form, "." for the repository itself, without duplicates.  The
// second result holds a message for each value that is refused: an empty
// value, git pathspec magic, a wildcard, an absolute path, or a path with a
// ".." segment.
func parseSourceDirs(raw []string) ([]string, []string) {
	var pathspecs, rejections []string
	seen := map[string]bool{}
	for _, value := range raw {
		reason := ""
		switch {
		case value == "":
			reason = "empty"
		case strings.HasPrefix(value, ":"):
			reason = "magic"
		case strings.ContainsAny(value, "*?["):
			reason = "wildcard"
		case windowsDrive.MatchString(value):
			reason = "absolute"
		default:
			for _, segment := range strings.Split(treatPathSep(value), "/") {
				if segment == ".." {
					reason = "escapes"
				}
			}
		}
		canonical := ""
		if reason == "" {
			canonical = sanitizePath(value)
			switch {
			case strings.HasPrefix(canonical, "/"):
				reason = "absolute"
			case canonical == "." || canonical == "./":
				canonical = "."
			default:
				canonical = strings.TrimRight(canonical, "/")
			}
		}
		if reason != "" {
			rejections = append(rejections, message(sourceDirMessages[reason], sanitizeForMessage(value)))
			continue
		}
		if !seen[canonical] {
			seen[canonical] = true
			pathspecs = append(pathspecs, canonical)
		}
	}
	return pathspecs, rejections
}

// latestAPIVersionURL lists the API versions of a Salesforce instance
// without authentication.  Upstream reads the same resource.
var latestAPIVersionURL = "https://appexchange.salesforce.com/services/data"

// lookUpLatestAPIVersion returns the highest API version the Salesforce
// AppExchange org supports.
func lookUpLatestAPIVersion() (int, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	response, err := client.Get(latestAPIVersionURL)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("%s answered %s", latestAPIVersionURL, response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return 0, err
	}
	var versions []struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &versions); err != nil {
		return 0, err
	}
	if len(versions) == 0 {
		return 0, fmt.Errorf("%s listed no API version", latestAPIVersionURL)
	}
	return toAPIVersion(versions[len(versions)-1].Version), nil
}

var leadingInteger = regexp.MustCompile(`^\s*[+-]?[0-9]+`)

// toAPIVersion is parseInt of a value: the integer at its start, or 0 when
// there is none or it is not positive.
func toAPIVersion(value any) int {
	text := ""
	switch typed := value.(type) {
	case string:
		text = typed
	case float64:
		text = strconv.FormatFloat(typed, 'f', -1, 64)
	case int:
		text = strconv.Itoa(typed)
	default:
		return 0
	}
	version, err := strconv.Atoi(strings.TrimSpace(leadingInteger.FindString(text)))
	if err != nil || version <= 0 {
		return 0
	}
	return version
}

// validate checks the configuration, opens the repository, resolves the
// revisions to commit ids and the API version, and replaces from by the
// merge base when asked.  It returns the warnings of these steps.
func validate(config *Config, rejections []string) (*gitadapter.Adapter, []string, error) {
	requestedFrom, requestedTo := config.From, config.To
	if config.APIVersion < 0 {
		config.APIVersion = 0
	}
	config.Repo = sanitizePath(config.Repo)
	config.Output = sanitizePath(config.Output)
	config.Ignore = sanitizePath(config.Ignore)
	config.IgnoreDestructive = sanitizePath(config.IgnoreDestructive)
	config.Include = sanitizePath(config.Include)
	config.IncludeDestructive = sanitizePath(config.IncludeDestructive)
	config.AdditionalMetadataRegistryPath = sanitizePath(config.AdditionalMetadataRegistryPath)
	config.ChangesManifest = sanitizePath(config.ChangesManifest)
	if len(rejections) > 0 {
		return nil, nil, &ConfigError{Message: strings.Join(rejections, ", ")}
	}

	var inputErrors []string
	addError := func(text string) {
		for _, existing := range inputErrors {
			if existing == text {
				return
			}
		}
		inputErrors = append(inputErrors, text)
	}
	absoluteRepo, err := filepath.Abs(config.Repo)
	if err != nil {
		return nil, nil, err
	}
	notGit := message("error.PathIsNotGit", sanitizeForMessage(filepath.ToSlash(absoluteRepo)))
	if _, err := os.Stat(filepath.Join(config.Repo, ".git")); err != nil {
		addError(notGit)
	}
	git, openErr := gitadapter.Open(config.Repo)
	if openErr != nil {
		var refusal *gitadapter.RepositoryRefusalError
		if errors.As(openErr, &refusal) {
			addError(refusal.Message)
		} else {
			addError(openErr.Error())
		}
	}

	// The API version is read from sfdx-project.json at the revision as it
	// was typed.
	var apiWarnings []string
	apiRefusal, unreadableProject := "", ""
	if git != nil && config.APIVersion == 0 {
		project, err := loadSfdxProject(&RunContext{Git: git}, config.To)
		if err != nil {
			unreadableProject = message("error.SfdxProjectUnusable", sanitizeForMessage(config.To), sanitizeForMessage(err.Error()))
		} else {
			if project.hasAPIVersion {
				config.APIVersion = toAPIVersion(project.sourceAPIVersion)
			}
			if config.APIVersion == 0 {
				latest, err := lookUpLatestAPIVersion()
				switch {
				case err != nil:
					apiRefusal = message("error.ApiVersionRetrievalFailed", err.Error())
				case latest <= 0:
					apiRefusal = message("error.ApiVersionRetrievalFailed", message("error.ApiVersionLookupUnusable", strconv.Itoa(latest)))
				default:
					config.APIVersion = latest
					apiWarnings = append(apiWarnings, message("warning.ApiVersionDefaulted", strconv.Itoa(latest)))
				}
			}
		}
	}

	if git != nil {
		resolve := func(parameter string, value *string) {
			resolved, err := git.ResolveCommit(*value)
			if err == nil {
				*value = resolved
				return
			}
			var refusal *gitadapter.RepositoryRefusalError
			var notACommit *gitadapter.NotACommitError
			switch {
			case errors.As(err, &refusal):
				addError(refusal.Message)
			case errors.As(err, &notACommit):
				addError(message("error.ParameterIsNotCommit", parameter, sanitizeForMessage(*value), notACommit.ObjectType))
			default:
				addError(message("error.ParameterIsNotGitSHA", parameter, sanitizeForMessage(*value)))
			}
		}
		resolve("from", &config.From)
		resolve("to", &config.To)
	}
	if target := config.ChangesManifest; target != "" {
		info, err := os.Stat(target)
		switch {
		case err == nil && !info.Mode().IsRegular():
			addError(message("error.ChangesManifestNotAFile", target))
		case err != nil && !errors.Is(err, fs.ErrNotExist):
			addError(message("error.ChangesManifestStatFailed", target, err.Error()))
		}
	}

	if apiRefusal != "" {
		addError(apiRefusal)
	} else if unreadableProject != "" && len(inputErrors) == 0 {
		addError(unreadableProject)
	}
	if len(inputErrors) > 0 {
		return nil, nil, &ConfigError{Message: strings.Join(inputErrors, ", ")}
	}

	if config.MergeBase {
		base, found, err := git.MergeBase(config.From, config.To)
		if err != nil {
			return nil, nil, err
		}
		if !found {
			return nil, nil, &ConfigError{Message: message("error.MergeBaseNotFound", sanitizeForMessage(requestedFrom), sanitizeForMessage(requestedTo))}
		}
		config.From = base
	}
	return git, apiWarnings, nil
}
