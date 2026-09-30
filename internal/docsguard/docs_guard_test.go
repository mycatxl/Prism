package docsguard

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// repoRoot locates the repository from this package's directory. The package
// lives at <root>/internal/docsguard, so the parent of the parent is the root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root := filepath.Dir(filepath.Dir(dir))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repository root %q has no go.mod: %v", root, err)
	}
	return root
}

func readFile(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

// goDirectiveVersion reads the `go 1.x.y` line of go.mod, which is the single
// source of truth for the toolchain baseline.
func goDirectiveVersion(t *testing.T, root string) string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^go\s+(\d+\.\d+(?:\.\d+)?)\s*$`)
	match := re.FindStringSubmatch(readFile(t, root, "go.mod"))
	if match == nil {
		t.Fatal("go.mod has no `go <version>` directive")
	}
	return match[1]
}

// TestGoVersionBaselineIsConsistent is DOC-01's gate. It failed in the audit
// because three documents still restated the baseline as 1.26 while go.mod and
// the Dockerfile had moved to 1.27, and one of them contradicted itself two
// paragraphs later.
//
// The rule is narrow on purpose: only lines that *restate the declared
// baseline* are checked, meaning a line that mentions both `go.mod` and a
// version. A historical sentence such as "the upstream module declared go
// 1.25.5" is a fact about Resin, not a claim about this repository, and must
// stay writable.
func TestGoVersionBaselineIsConsistent(t *testing.T) {
	root := repoRoot(t)
	baseline := goDirectiveVersion(t, root)
	short := baseline
	if parts := strings.Split(baseline, "."); len(parts) == 3 {
		short = parts[0] + "." + parts[1]
	}

	files := []string{
		"README.md",
		"README.zh-CN.md",
		"docs/deployment.md",
		"docs/UPSTREAM_BASELINE.md",
	}
	// A version literal inside one of these markers is a historical quotation.
	historicalMarkers := []string{"原 Resin", "upstream Resin", "Resin module"}

	declaresGoMod := regexp.MustCompile(`go\.mod`)
	versionLiteral := regexp.MustCompile(`(?i)\bgo(?:lang)?[ \t:]+(\d+\.\d+(?:\.\d+)?)\b`)

	for _, rel := range files {
		for i, line := range strings.Split(readFile(t, root, rel), "\n") {
			if !declaresGoMod.MatchString(line) {
				continue
			}
			for _, match := range versionLiteral.FindAllStringSubmatch(line, -1) {
				found := match[1]
				if found == baseline || found == short {
					continue
				}
				if hasAnyMarker(line, historicalMarkers) {
					continue
				}
				t.Errorf("%s:%d restates the Go baseline as %s but go.mod declares %s: %s",
					rel, i+1, found, baseline, strings.TrimSpace(line))
			}
		}
	}
}

func hasAnyMarker(line string, markers []string) bool {
	for _, marker := range markers {
		if strings.Contains(line, marker) {
			return true
		}
	}
	return false
}

// TestNodeVersionBaselineIsConsistent is the panel-side twin of the Go check.
// package.json's `engines` is the floor; CI and the Dockerfile pick a build
// baseline that must not be lower than it, and prose that states a Node
// requirement must not contradict either.
func TestNodeVersionBaselineIsConsistent(t *testing.T) {
	root := repoRoot(t)

	enginesSrc := readFile(t, root, "internal/api/web/package.json")
	enginesRe := regexp.MustCompile(`"node"\s*:\s*">=(\d+)(?:\.(\d+))?`)
	em := enginesRe.FindStringSubmatch(enginesSrc)
	if em == nil {
		t.Fatal("package.json has no `\"node\": \">=x.y\"` engines floor; update this gate with the new shape")
	}
	floorMajor, floorMinor := em[1], em[2]

	// CI and the Dockerfile choose the build baseline. It must be at least the
	// floor, or the documented floor is a lie.
	baselineRe := regexp.MustCompile(`node:(\d+)-alpine|node-version:\s*(\d+)`)
	baselines := map[string]string{}
	for _, rel := range []string{"Dockerfile", ".github/workflows/ci.yml", ".github/workflows/release.yml"} {
		for _, bm := range baselineRe.FindAllStringSubmatch(readFile(t, root, rel), -1) {
			major := bm[1]
			if major == "" {
				major = bm[2]
			}
			baselines[rel] = major
		}
	}
	if len(baselines) == 0 {
		t.Fatal("no Node build baseline found in Dockerfile or the workflows")
	}
	for rel, major := range baselines {
		if compareMajor(major, floorMajor) < 0 {
			t.Errorf("%s builds with Node %s but package.json declares >=%s.%s; the build baseline cannot be below the declared floor",
				rel, major, floorMajor, floorMinor)
		}
	}

	// Prose must not advertise a floor above the declared one. `internal/api/web/
	// README.md` said "Node.js 24+" while engines said >=22.12.0, which tells a
	// reader on Node 22 that the project does not support them.
	claimRe := regexp.MustCompile(`Node\.js\s+(\d+)\+`)
	for _, rel := range []string{"internal/api/web/README.md", "docs/deployment.md", "README.md"} {
		lines := strings.Split(readFile(t, root, rel), "\n")
		for i, line := range lines {
			for _, cm := range claimRe.FindAllStringSubmatch(line, -1) {
				if compareMajor(cm[1], floorMajor) > 0 {
					t.Errorf("%s:%d advertises Node.js %s+ but package.json declares >=%s.%s: %s",
						rel, i+1, cm[1], floorMajor, floorMinor, strings.TrimSpace(line))
				}
			}
		}
	}
}

// compareMajor orders two numeric major versions as strings.
func compareMajor(a, b string) int {
	ai, aErr := strconv.Atoi(a)
	bi, bErr := strconv.Atoi(b)
	if aErr != nil || bErr != nil {
		return 0
	}
	switch {
	case ai < bi:
		return -1
	case ai > bi:
		return 1
	default:
		return 0
	}
}

// TestPanelPortIsConsistent is DOC-02's gate. server/config.mjs is the single
// source of truth for the panel port; the README and the example environment
// file must not advertise a different one.
//
// The check reads the default out of config.mjs rather than hardcoding 1262, so
// changing the default in one place cannot silently desynchronise the prose.
func TestPanelPortIsConsistent(t *testing.T) {
	root := repoRoot(t)

	configSrc := readFile(t, root, "internal/api/web/server/config.mjs")
	defaultRe := regexp.MustCompile(`env\.PRISM_UI_PORT\s*\?\?\s*"(\d+)"`)
	match := defaultRe.FindStringSubmatch(configSrc)
	if match == nil {
		t.Fatal("server/config.mjs no longer exposes a `env.PRISM_UI_PORT ?? \"<port>\"` default; update this gate with the new shape")
	}
	port := match[1]

	// Every other port that legitimately appears in these files, so the check
	// can tell "the panel port is wrong" from "this is the backend port". The
	// backend default is `envInt("PRISM_PORT", 2260, &errs)` in env.go.
	backendPortRe := regexp.MustCompile(`envInt\("PRISM_PORT",\s*(\d+)`)
	backendPorts := map[string]bool{}
	if m := backendPortRe.FindStringSubmatch(readFile(t, root, "internal/config/env.go")); m != nil {
		backendPorts[m[1]] = true
	}
	if len(backendPorts) == 0 {
		t.Fatal("could not read the PRISM_PORT default from internal/config/env.go; update this gate with the new shape")
	}

	for _, rel := range []string{"internal/api/web/README.md", "internal/api/web/.env.example"} {
		content := readFile(t, root, rel)
		lines := strings.Split(content, "\n")

		// The example env file states the panel port exactly once, as an
		// assignment. That is a direct claim and must match.
		if rel == "internal/api/web/.env.example" {
			assignRe := regexp.MustCompile(`(?m)^PRISM_UI_PORT=(\d+)\s*$`)
			am := assignRe.FindStringSubmatch(content)
			if am == nil {
				t.Errorf("%s: no `PRISM_UI_PORT=<port>` assignment to check", rel)
			} else if am[1] != port {
				t.Errorf("%s: PRISM_UI_PORT=%s but server/config.mjs defaults to %s", rel, am[1], port)
			}
			continue
		}

		// The README names the panel port in prose. Any four- or five-digit
		// number that is not the panel port and not a known backend port is a
		// stale claim about the panel, which is exactly what the audit found
		// (line 5 said 1262 while line 49 still said 8080).
		numberRe := regexp.MustCompile(`\b(\d{4,5})\b`)
		for i, line := range lines {
			for _, nm := range numberRe.FindAllStringSubmatch(line, -1) {
				value := nm[1]
				if value == port || backendPorts[value] {
					continue
				}
				// Versions, ports in unrelated URLs and dates are not panel
				// claims; only flag numbers on lines that talk about the panel.
				if !mentionsPanel(line) {
					continue
				}
				t.Errorf("%s:%d mentions the management panel together with port %s, but server/config.mjs defaults to %s: %s",
					rel, i+1, value, port, strings.TrimSpace(line))
			}
		}
	}
}

func mentionsPanel(line string) bool {
	lower := strings.ToLower(line)
	for _, marker := range []string{"面板", "management panel", "panel port", "ui port", "prism_ui_port"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// TestShippedFeaturesAreNotDocumentedAsMissing is DOC-03's gate, the one with
// the highest value in the audit: two shipped capabilities (intel.db, `prism
// import-resin`) were described as "not implemented yet" in the operations
// documentation.
//
// The rule is deliberately conservative. A document may legitimately list what
// this version does not ship — docs/API.md and docs/deployment.md both do, and
// every item there was verified as true. So this check does not ban the phrase;
// it bans the phrase on a line that names a *subcommand or database that
// exists*. That is the shape of the actual bug and it cannot fire on a truthful
// "not implemented" list.
func TestShippedFeaturesAreNotDocumentedAsMissing(t *testing.T) {
	root := repoRoot(t)

	// The subcommands the binary really dispatches (cmd/prism/subcommands.go
	// runCLI). Adding one here without implementing it would be a different bug.
	shippedSubcommands := []string{"run", "init", "version", "check-config", "backup", "restore", "import-resin"}

	// The database files the service really opens. Read from the production
	// sources so a rename cannot leave this list stale.
	dbFileRe := regexp.MustCompile(`"([a-z_]+\.db)"`)
	shippedDBs := map[string]bool{}
	for _, rel := range []string{
		"internal/state/persistence_bootstrap.go",
		"internal/intel/store/store.go",
		"cmd/prism/app_runtime.go",
		"cmd/prism/subcommands.go",
	} {
		for _, m := range dbFileRe.FindAllStringSubmatch(readFile(t, root, rel), -1) {
			shippedDBs[m[1]] = true
		}
	}
	if len(shippedDBs) == 0 {
		t.Fatal("no database file names found in the production sources; the gate cannot work")
	}

	missingRe := regexp.MustCompile(`(?i)not implemented|not available|not yet|未实现|尚未实现|暂未实现|不可用|未落地`)

	// Historical records of what was not ported are not claims about the
	// current version. The release notes are an immutable record, and the two
	// migration documents describe decisions taken when the port happened.
	excludedFiles := map[string]bool{
		"docs/release-notes/v0.1.0-rc2.md": true,
	}
	excludedLineMarkers := []string{
		// A line that explicitly says the capability *is* implemented.
		"is **not** on this list",
		"not on this list",
		"已补齐",
		"现已补齐",
		"is ported now",
		"is implemented",
	}

	docFiles := markdownFiles(t, root, "docs")

	for _, rel := range docFiles {
		if excludedFiles[rel] {
			continue
		}
		lines := strings.Split(readFile(t, root, rel), "\n")
		for i, line := range lines {
			if !missingRe.MatchString(line) {
				continue
			}
			if hasAnyMarker(line, excludedLineMarkers) {
				continue
			}
			if offender, ok := namesShippedThing(line, shippedSubcommands, shippedDBs); ok {
				t.Errorf("%s:%d says something is unavailable while naming the shipped %s: %s",
					rel, i+1, offender, strings.TrimSpace(line))
			}
		}
	}
}

// namesShippedThing reports the shipped subcommand or database file the line
// names, if any.
func namesShippedThing(line string, subcommands []string, dbs map[string]bool) (string, bool) {
	for db := range dbs {
		if strings.Contains(line, db) {
			return db, true
		}
	}
	for _, command := range subcommands {
		// `prism <command>` is how the documentation always spells it, so match
		// the whole invocation to avoid firing on the bare word "backup" in
		// prose about something else.
		if strings.Contains(line, "prism "+command) || strings.Contains(line, "`"+command+"`") {
			return "prism " + command, true
		}
	}
	return "", false
}

// markdownFiles lists every .md file under dir, as repository-relative slash
// paths, sorted for a stable report.
func markdownFiles(t *testing.T, root, dir string) []string {
	t.Helper()
	var out []string
	base := filepath.Join(root, filepath.FromSlash(dir))
	err := filepath.WalkDir(base, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	sort.Strings(out)
	return out
}

// TestDirectoryListingMatchesTheTree is DOC-04's gate. The audit found the
// §4.3 listing in docs/DESIGN.md naming seven directories that do not exist
// (`internal/app`, `internal/inspection`, `internal/policy`, …) while omitting
// nine that do, which makes the section actively misleading.
//
// The listing is read from the first ```text block of the document, and the
// real tree from the filesystem, so neither side is a copy of the other.
func TestDirectoryListingMatchesTheTree(t *testing.T) {
	root := repoRoot(t)
	doc := readFile(t, root, "docs/DESIGN.md")

	block := firstFencedBlock(t, doc, "text")
	if block == "" {
		t.Fatal("docs/DESIGN.md has no ```text block; the §4.3 listing moved or changed shape")
	}

	// Each listed directory line looks like `internal/<name>/` followed by a
	// comment. Only `internal/` entries are compared: the block also names
	// cmd/prism/ and the nested internal/api/web/, which are not top-level
	// packages.
	listedRe := regexp.MustCompile(`(?m)^internal/([a-z0-9_]+)/`)
	listed := map[string]bool{}
	for _, m := range listedRe.FindAllStringSubmatch(block, -1) {
		listed[m[1]] = true
	}
	if len(listed) == 0 {
		t.Fatal("no `internal/<name>/` entries found in the docs/DESIGN.md listing")
	}

	actual := map[string]bool{}
	entries, err := os.ReadDir(filepath.Join(root, "internal"))
	if err != nil {
		t.Fatalf("read internal/: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			actual[entry.Name()] = true
		}
	}

	var missing, stale []string
	for name := range actual {
		if !listed[name] {
			missing = append(missing, name)
		}
	}
	for name := range listed {
		if !actual[name] {
			stale = append(stale, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)

	if len(missing) > 0 {
		t.Errorf("docs/DESIGN.md §4.3 listing omits %d real directories: %s",
			len(missing), strings.Join(missing, ", "))
	}
	if len(stale) > 0 {
		t.Errorf("docs/DESIGN.md §4.3 listing names %d directories that do not exist: %s",
			len(stale), strings.Join(stale, ", "))
	}
}

// firstFencedBlock returns the body of the first ```<language> block.
func firstFencedBlock(t *testing.T, doc, language string) string {
	t.Helper()
	opening := "```" + language
	start := strings.Index(doc, opening)
	if start < 0 {
		return ""
	}
	rest := doc[start+len(opening):]
	if nl := strings.Index(rest, "\n"); nl >= 0 {
		rest = rest[nl+1:]
	}
	end := strings.Index(rest, "```")
	if end < 0 {
		return ""
	}
	return rest[:end]
}
