// Package upgrade finds newer mobile-release releases and plans the project
// changes that move a project to them: the managed Makefile block and the
// release workflow pins.
package upgrade

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/adopt"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/config"
)

// Release is one published version.
type Release struct {
	Tag   string `json:"tag_name"`
	Notes string `json:"body"`
	URL   string `json:"html_url"`
	Draft bool   `json:"draft"`
	Pre   bool   `json:"prerelease"`
}

// Version is a parsed vMAJOR.MINOR.PATCH.
type Version struct {
	Major, Minor, Patch int
	Pre                 string
}

var semver = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$`)

// Parse reads a version tag.
func Parse(tag string) (Version, bool) {
	match := semver.FindStringSubmatch(tag)
	if match == nil {
		return Version{}, false
	}
	major, _ := strconv.Atoi(match[1])
	minor, _ := strconv.Atoi(match[2])
	patch, _ := strconv.Atoi(match[3])
	return Version{Major: major, Minor: minor, Patch: patch, Pre: match[4]}, true
}

// Less orders versions; a pre-release sorts before its release.
func (v Version) Less(other Version) bool {
	if v.Major != other.Major {
		return v.Major < other.Major
	}
	if v.Minor != other.Minor {
		return v.Minor < other.Minor
	}
	if v.Patch != other.Patch {
		return v.Patch < other.Patch
	}
	return v.Pre != "" && (other.Pre == "" || v.Pre < other.Pre)
}

// Fetch lists the published releases from GitHub.
func Fetch(ctx context.Context, client *http.Client) ([]Release, error) {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.github.com/repos/"+adopt.Repository+"/releases?per_page=50", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "mobile-release")
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("list releases: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list releases: GitHub answered %s", response.Status)
	}
	var releases []Release
	if err := json.NewDecoder(response.Body).Decode(&releases); err != nil {
		return nil, fmt.Errorf("list releases: %w", err)
	}
	return releases, nil
}

// Stable keeps published, non-prerelease, parseable releases, newest first.
func Stable(releases []Release) []Release {
	var stable []Release
	for _, release := range releases {
		if _, ok := Parse(release.Tag); ok && !release.Draft && !release.Pre {
			stable = append(stable, release)
		}
	}
	sort.Slice(stable, func(i, j int) bool {
		left, _ := Parse(stable[i].Tag)
		right, _ := Parse(stable[j].Tag)
		return right.Less(left)
	})
	return stable
}

// Target is the newest release within major (same module path), and the
// newest release overall when that is a new major.
func Target(stable []Release, major int) (sameMajor, nextMajor *Release) {
	for i := range stable {
		version, _ := Parse(stable[i].Tag)
		if version.Major == major && sameMajor == nil {
			sameMajor = &stable[i]
		}
		if version.Major > major && nextMajor == nil {
			nextMajor = &stable[i]
		}
	}
	return sameMajor, nextMajor
}

// Between returns the releases newer than from up to and including to,
// oldest first.
func Between(stable []Release, from, to string) []Release {
	lower, okFrom := Parse(from)
	upper, okTo := Parse(to)
	var out []Release
	for _, release := range stable {
		version, _ := Parse(release.Tag)
		if okTo && upper.Less(version) {
			continue
		}
		if okFrom && !lower.Less(version) {
			continue
		}
		out = append(out, release)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// Pins are the versions a project currently uses.
type Pins struct {
	Makefile       string // managed block MOBILE_RELEASE_VERSION
	MakefileManual bool   // targets written by hand, no managed block
	Workflow       string // mobile-release-version input
	WorkflowRef    string // reusable workflow ref (commit or tag)
}

var (
	makefileVersion = regexp.MustCompile(`(?m)^MOBILE_RELEASE_VERSION \?= (v\S+)`)
	workflowUses    = regexp.MustCompile(`(` + regexp.QuoteMeta(adopt.Repository) + `/\.github/workflows/reusable-release\.yml@)(\S+)( # v\S+)?`)
	workflowVersion = regexp.MustCompile(`(mobile-release-version: )(\S+)`)
)

// WorkflowPath is the workflow adopt creates.
const WorkflowPath = ".github/workflows/release.yml"

// ReadPins inspects the project.
func ReadPins(root string) Pins {
	var pins Pins
	if data, err := os.ReadFile(filepath.Join(root, "Makefile")); err == nil {
		text := string(data)
		if strings.Contains(text, adopt.BeginMarker) {
			if match := makefileVersion.FindStringSubmatch(text[strings.Index(text, adopt.BeginMarker):]); match != nil {
				pins.Makefile = match[1]
			}
		} else if match := makefileVersion.FindStringSubmatch(text); match != nil {
			pins.Makefile = match[1]
			pins.MakefileManual = true
		}
	}
	if data, err := os.ReadFile(filepath.Join(root, WorkflowPath)); err == nil {
		if match := workflowUses.FindStringSubmatch(string(data)); match != nil {
			pins.WorkflowRef = match[2]
		}
		if match := workflowVersion.FindStringSubmatch(string(data)); match != nil {
			pins.Workflow = strings.Split(match[2], " ")[0]
		}
	}
	return pins
}

// Oldest returns the oldest pinned version, or "".
func (p Pins) Oldest() string {
	oldest := ""
	for _, value := range []string{p.Makefile, p.Workflow} {
		version, ok := Parse(value)
		if !ok {
			continue
		}
		current, _ := Parse(oldest)
		if oldest == "" || version.Less(current) {
			oldest = value
		}
	}
	return oldest
}

// PlanProject lists the changes that pin the project to version (ref is the
// release's commit, or "" to use the tag).
func PlanProject(root string, c config.Config, version, ref string) ([]adopt.Change, error) {
	var changes []adopt.Change
	pins := ReadPins(root)
	switch {
	case pins.MakefileManual:
		changes = append(changes, adopt.Change{Path: "Makefile", Action: adopt.Skip,
			Reason: "MOBILE_RELEASE_VERSION is written by hand; update it yourself, or remove the targets and run `mobile-release adopt` to let upgrades manage them"})
	case pins.Makefile != "":
		all, err := adopt.Plan(adopt.Options{Root: root, Config: c, Version: version})
		if err != nil {
			return nil, err
		}
		for _, change := range all {
			if change.Path == "Makefile" {
				changes = append(changes, change)
			}
		}
	default:
		changes = append(changes, adopt.Change{Path: "Makefile", Action: adopt.Skip, Reason: "no managed block (run `mobile-release adopt`)"})
	}

	data, err := os.ReadFile(filepath.Join(root, WorkflowPath))
	if err != nil {
		return changes, nil
	}
	if ref == "" {
		ref = version
	}
	text := string(data)
	updated := workflowUses.ReplaceAllString(text, "${1}"+ref+" # "+version)
	updated = workflowVersion.ReplaceAllString(updated, "${1}"+version)
	switch {
	case !workflowUses.MatchString(text):
		changes = append(changes, adopt.Change{Path: WorkflowPath, Action: adopt.Skip, Reason: "does not use the mobile-release reusable workflow"})
	case updated == text:
		changes = append(changes, adopt.Change{Path: WorkflowPath, Action: adopt.Skip, Reason: "already up to date"})
	default:
		changes = append(changes, adopt.Change{Path: WorkflowPath, Action: adopt.Update, Reason: "pins " + version, Content: updated})
	}
	return changes, nil
}
