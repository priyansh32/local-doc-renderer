package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func writeTestFile(t *testing.T, root, relPath, content string) {
	t.Helper()

	fullPath := filepath.Join(root, relPath)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatalf("failed to create parent dirs for %s: %v", relPath, err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write %s: %v", relPath, err)
	}
}

func useContentRootForTest(t *testing.T, root string) {
	t.Helper()

	oldContentDir := contentDir
	oldContentRoot := contentRoot

	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("failed to resolve root symlinks: %v", err)
	}

	contentDir = filepath.Clean(root)
	contentRoot = filepath.Clean(realRoot)
	cache = searchCache{}
	navCache = navTreeCache{}

	t.Cleanup(func() {
		contentDir = oldContentDir
		contentRoot = oldContentRoot
		cache = searchCache{}
		navCache = navTreeCache{}
	})
}

func TestNormalizeRequestPath(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantOut string
	}{
		{name: "empty", input: "", wantOut: ""},
		{name: "root", input: "/", wantOut: ""},
		{name: "relative", input: "guide/start", wantOut: "guide/start"},
		{name: "cleans_dot_segments", input: "/guide/../start.md", wantOut: "start.md"},
		{name: "blocks_path_escape", input: "../../etc/passwd", wantOut: "etc/passwd"},
	}

	for _, tc := range cases {
		got := normalizeRequestPath(tc.input)
		if got != tc.wantOut {
			t.Fatalf("%s: normalizeRequestPath(%q) = %q, want %q", tc.name, tc.input, got, tc.wantOut)
		}
	}
}

func TestResolveRequestedFileHandlesDefaultFallbackAndDirectoryReadme(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "README.md", "# Root")
	writeTestFile(t, root, "quick-reference.md", "# Quick")
	writeTestFile(t, root, "guide/README.md", "# Guide")
	writeTestFile(t, root, "guide/intro.md", "# Intro")

	useContentRootForTest(t, root)

	tests := []struct {
		name    string
		path    string
		wantRel string
	}{
		{name: "default_root_readme", path: "/", wantRel: "README.md"},
		{name: "markdown_extension_fallback", path: "/quick-reference", wantRel: "quick-reference.md"},
		{name: "directory_readme", path: "/guide", wantRel: filepath.Join("guide", "README.md")},
		{name: "direct_markdown_path", path: "/guide/intro", wantRel: filepath.Join("guide", "intro.md")},
	}

	for _, tc := range tests {
		got, err := resolveRequestedFile(tc.path)
		if err != nil {
			t.Fatalf("%s: resolveRequestedFile(%q) returned error: %v", tc.name, tc.path, err)
		}

		want := filepath.Join(root, tc.wantRel)
		if filepath.Clean(got) != filepath.Clean(want) {
			t.Fatalf("%s: resolveRequestedFile(%q) = %q, want %q", tc.name, tc.path, got, want)
		}
	}

	if _, err := resolveRequestedFile("/does-not-exist"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file should return os.ErrNotExist, got: %v", err)
	}
}

func TestResolveRequestedFileRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior is inconsistent on Windows CI environments")
	}

	root := t.TempDir()
	outside := t.TempDir()
	writeTestFile(t, root, "README.md", "# Root")
	writeTestFile(t, outside, "secret.md", "top secret")

	linkPath := filepath.Join(root, "outside")
	if err := os.Symlink(outside, linkPath); err != nil {
		t.Skipf("symlink not available in this environment: %v", err)
	}

	useContentRootForTest(t, root)

	_, err := resolveRequestedFile("/outside/secret")
	if !errors.Is(err, errOutsideRoot) {
		t.Fatalf("expected errOutsideRoot for symlink escape, got: %v", err)
	}
}

func TestBuildNavSkipsKnownEntriesAndSortsDirsFirst(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "README.md", "# Root")
	writeTestFile(t, root, "alpha.md", "# Alpha")
	writeTestFile(t, root, "zeta.md", "# Zeta")
	writeTestFile(t, root, ".hidden.md", "# Hidden")
	writeTestFile(t, root, "go.mod", "module example")
	writeTestFile(t, root, "main.go", "package main")
	writeTestFile(t, root, "node_modules/ignore.md", "ignored")
	writeTestFile(t, root, "vendor/ignore.md", "ignored")
	writeTestFile(t, root, "docs/README.md", "# Docs")
	writeTestFile(t, root, "docs/b.md", "# B")

	nodes := buildNav(root, "")
	if len(nodes) != 4 {
		t.Fatalf("unexpected root nav length: got %d, want 4", len(nodes))
	}

	if !nodes[0].IsDir || nodes[0].Name != "docs" {
		t.Fatalf("expected first node to be docs directory, got %#v", nodes[0])
	}
	if nodes[1].IsDir || nodes[1].Name != "README" {
		t.Fatalf("expected second node to be README file, got %#v", nodes[1])
	}
	if nodes[2].Name != "alpha" || nodes[3].Name != "zeta" {
		t.Fatalf("expected alphabetical file order after directory, got %q then %q", nodes[2].Name, nodes[3].Name)
	}

	docsChildren := nodes[0].Children
	if len(docsChildren) != 2 {
		t.Fatalf("unexpected docs child count: got %d, want 2", len(docsChildren))
	}
	if docsChildren[0].Name != "README" || docsChildren[1].Name != "b" {
		t.Fatalf("unexpected docs child ordering: got %q then %q", docsChildren[0].Name, docsChildren[1].Name)
	}
}

func TestActivePathForUsesReadmeFallbackOutsideRoot(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "README.md", "# Root")
	useContentRootForTest(t, root)

	insidePath := filepath.Join(root, "guide", "README.md")
	if got := activePathFor(insidePath); got != filepath.ToSlash(filepath.Join("guide", "README.md")) {
		t.Fatalf("inside path active value mismatch: got %q", got)
	}

	outsidePath := filepath.Join(t.TempDir(), "other.md")
	if got := activePathFor(outsidePath); got != "README.md" {
		t.Fatalf("outside path should fall back to README.md, got %q", got)
	}
}

func TestSearchDocsPrioritizesTitleMatches(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "README.md", "# Root")
	writeTestFile(t, root, "target-guide.md", "nothing to see here")
	writeTestFile(t, root, "notes.md", "this content mentions target once")

	useContentRootForTest(t, root)

	results := searchDocs("target")
	if len(results) < 2 {
		t.Fatalf("expected at least two search results, got %d", len(results))
	}
	if results[0].Title != "target-guide" {
		t.Fatalf("title match should be ranked first, got first result %q", results[0].Title)
	}
}
