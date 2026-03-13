package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/yuin/goldmark"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
)

// ---------------------------------------------------------------------------
// Markdown parser
// ---------------------------------------------------------------------------

var md = goldmark.New(
	goldmark.WithExtensions(
		extension.GFM,
		extension.Typographer,
		highlighting.NewHighlighting(
			highlighting.WithStyle("monokai"),
			highlighting.WithFormatOptions(
				chromahtml.WithClasses(true),
			),
		),
	),
	goldmark.WithParserOptions(
		parser.WithAutoHeadingID(),
	),
	goldmark.WithRendererOptions(
		html.WithHardWraps(),
		html.WithXHTML(),
	),
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

type Node struct {
	Name     string
	Path     string
	IsDir    bool
	Children []*Node
}

type PageData struct {
	Title   string
	Content template.HTML
	Nav     []*Node
	Active  string
	Style   template.CSS
}

type SearchEntry struct {
	Title   string
	Path    string
	Content string
}

type SearchResult struct {
	Title   string `json:"title"`
	Path    string `json:"path"`
	Snippet string `json:"snippet"`
}

type searchMatch struct {
	result     SearchResult
	titleMatch bool
}

// ---------------------------------------------------------------------------
// Caches and guards
// ---------------------------------------------------------------------------

type searchCache struct {
	mu      sync.RWMutex
	index   []SearchEntry
	builtAt time.Time
}

type navTreeCache struct {
	mu      sync.RWMutex
	tree    []*Node
	builtAt time.Time
}

type rateLimiter struct {
	mu          sync.Mutex
	clients     map[string]*clientBucket
	rate        float64
	burst       float64
	clientTTL   time.Duration
	lastCleanup time.Time
}

type clientBucket struct {
	tokens   float64
	lastSeen time.Time
}

const (
	searchCacheTTL       = 30 * time.Second
	navCacheTTL          = 30 * time.Second
	maxSearchResults     = 12
	maxSearchQueryLength = 120
	maxRequestBodyBytes  = int64(1 << 20)
	rateLimitPerSecond   = 10.0
	rateLimitBurst       = 40.0
	rateLimiterClientTTL = 10 * time.Minute
	serverReadTimeout    = 15 * time.Second
	serverWriteTimeout   = 30 * time.Second
	serverIdleTimeout    = 60 * time.Second
	serverHeaderTimeout  = 5 * time.Second
	serverMaxHeaderBytes = 1 << 20
)

var (
	cache          searchCache
	navCache       navTreeCache
	contentDir     string
	contentRoot    string
	highlightCSS   template.CSS
	pageTmpl       *template.Template
	requestLimiter = newRateLimiter(rateLimitPerSecond, rateLimitBurst, rateLimiterClientTTL)
	errOutsideRoot = errors.New("requested path escapes content root")
)

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

func main() {
	var port string
	var openBrowser bool
	var allowNetwork bool
	flag.StringVar(&port, "port", "", "Port to listen on (default: auto-select free port)")
	flag.StringVar(&contentDir, "dir", ".", "Directory containing markdown files")
	flag.BoolVar(&openBrowser, "open", true, "Open browser automatically after starting")
	flag.BoolVar(&allowNetwork, "network", false, "Listen on all interfaces (LAN-accessible)")
	flag.Parse()

	if envPort := os.Getenv("PORT"); envPort != "" {
		port = envPort
	}

	absPath, err := filepath.Abs(contentDir)
	if err == nil {
		contentDir = absPath
	}

	info, err := os.Stat(contentDir)
	if err != nil {
		log.Fatalf("Invalid content directory: %v", err)
	}
	if !info.IsDir() {
		log.Fatalf("Not a directory: %s", contentDir)
	}

	realRoot, err := filepath.EvalSymlinks(contentDir)
	if err != nil {
		log.Fatalf("Failed to resolve content directory symlinks: %v", err)
	}
	contentRoot = filepath.Clean(realRoot)

	highlightCSS = buildHighlightCSS()
	if err := initTemplate(); err != nil {
		log.Fatalf("Failed to parse templates: %v", err)
	}

	// Bind early so we know the actual port before printing/opening browser
	ln, err := listen(port, allowNetwork)
	if err != nil {
		log.Fatalf("Failed to bind: %v", err)
	}

	actualPort := ln.Addr().(*net.TCPAddr).Port
	localURL := fmt.Sprintf("http://localhost:%d", actualPort)
	networkURL := "disabled (use --network to enable)"
	if allowNetwork {
		networkURL = fmt.Sprintf("http://%s:%d", localIP(), actualPort)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/search", searchHandler)
	mux.HandleFunc("/", handler)

	srv := &http.Server{
		Handler:           withSecurityGuards(mux),
		ReadHeaderTimeout: serverHeaderTimeout,
		ReadTimeout:       serverReadTimeout,
		WriteTimeout:      serverWriteTimeout,
		IdleTimeout:       serverIdleTimeout,
		MaxHeaderBytes:    serverMaxHeaderBytes,
	}

	// Graceful shutdown on Ctrl+C / SIGTERM
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		fmt.Println("\nShutting down...")
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		srv.Shutdown(shutCtx)
	}()

	fmt.Printf("Serving docs from:  %s\n", contentDir)
	fmt.Printf("Local:              %s\n", localURL)
	fmt.Printf("Network:            %s\n", networkURL)
	fmt.Println("Press Ctrl+C to stop.")

	if openBrowser {
		// Poll until the server is actually accepting connections, then open
		go func() {
			for i := 0; i < 20; i++ {
				time.Sleep(50 * time.Millisecond)
				conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", actualPort), 100*time.Millisecond)
				if err == nil {
					conn.Close()
					break
				}
			}
			openURL(localURL)
		}()
	}

	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

// listen binds to localhost by default, or all interfaces when network access is enabled.
func listen(port string, allowNetwork bool) (net.Listener, error) {
	host := "127.0.0.1"
	if allowNetwork {
		host = ""
	}
	if port == "" {
		port = "0"
	}
	return net.Listen("tcp", net.JoinHostPort(host, port))
}

// openURL launches the system default browser cross-platform.
func openURL(url string) {
	var commands []*exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		commands = []*exec.Cmd{
			exec.Command("open", url),
		}
	case "windows":
		commands = []*exec.Cmd{
			// Uses ShellExecute via Win32.
			exec.Command("rundll32", "url.dll,FileProtocolHandler", url),
		}
	default:
		commands = []*exec.Cmd{
			exec.Command("xdg-open", url),
		}
	}

	var lastErr error
	for _, cmd := range commands {
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard

		// Start without waiting; some launchers can block with Run/Wait.
		if err := cmd.Start(); err != nil {
			lastErr = err
			continue
		}
		go cmd.Wait()
		return
	}

	if lastErr != nil {
		log.Printf("Could not open browser for %s: %v", url, lastErr)
	}
}

// localIP returns the machine's outbound LAN IP address.
func localIP() string {
	// Dial a public address over UDP; no traffic is sent, we just need the local IP.
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "localhost"
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

func buildHighlightCSS() template.CSS {
	var cssBuf bytes.Buffer
	formatter := chromahtml.New(chromahtml.WithClasses(true))
	style := styles.Get("monokai")
	if err := formatter.WriteCSS(&cssBuf, style); err != nil {
		log.Printf("Error generating CSS: %v", err)
		return ""
	}
	return template.CSS(cssBuf.String())
}

func initTemplate() error {
	tmpl, err := template.New("layout").Funcs(funcMap).Parse(navNodeTmpl + layout)
	if err != nil {
		return err
	}
	pageTmpl = tmpl
	return nil
}

func withSecurityGuards(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		}

		ip := clientIPFromRemoteAddr(r.RemoteAddr)
		if !requestLimiter.Allow(ip) {
			writePlainError(w, http.StatusTooManyRequests, "Too many requests")
			return
		}

		next.ServeHTTP(w, r)
	})
}

func writePlainError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	if _, err := io.WriteString(w, message); err != nil {
		log.Printf("Error writing response: %v", err)
	}
}

func clientIPFromRemoteAddr(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

func newRateLimiter(ratePerSecond, burst float64, clientTTL time.Duration) *rateLimiter {
	return &rateLimiter{
		clients:   make(map[string]*clientBucket),
		rate:      ratePerSecond,
		burst:     burst,
		clientTTL: clientTTL,
	}
}

func (l *rateLimiter) Allow(key string) bool {
	if key == "" {
		key = "unknown"
	}

	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.lastCleanup.IsZero() || now.Sub(l.lastCleanup) > time.Minute {
		for clientKey, bucket := range l.clients {
			if now.Sub(bucket.lastSeen) > l.clientTTL {
				delete(l.clients, clientKey)
			}
		}
		l.lastCleanup = now
	}

	bucket, ok := l.clients[key]
	if !ok {
		bucket = &clientBucket{
			tokens:   l.burst,
			lastSeen: now,
		}
		l.clients[key] = bucket
	}

	elapsed := now.Sub(bucket.lastSeen).Seconds()
	bucket.tokens = math.Min(l.burst, bucket.tokens+elapsed*l.rate)
	bucket.lastSeen = now

	if bucket.tokens < 1 {
		return false
	}

	bucket.tokens--
	return true
}

func normalizeRequestPath(urlPath string) string {
	cleaned := path.Clean("/" + urlPath)
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "." {
		return ""
	}
	return cleaned
}

func isPathWithinRoot(root, target string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)

	if runtime.GOOS == "windows" {
		root = strings.ToLower(root)
		target = strings.ToLower(target)
	}

	if target == root {
		return true
	}
	return strings.HasPrefix(target, root+string(filepath.Separator))
}

func resolvePathWithinRoot(relPath string) (string, error) {
	candidate := filepath.Join(contentDir, filepath.FromSlash(relPath))
	candidateAbs, err := filepath.Abs(candidate)
	if err != nil {
		return "", err
	}

	if !isPathWithinRoot(contentDir, candidateAbs) {
		return "", errOutsideRoot
	}

	resolved, err := filepath.EvalSymlinks(candidateAbs)
	if err == nil {
		if !isPathWithinRoot(contentRoot, resolved) {
			return "", errOutsideRoot
		}
		return candidateAbs, nil
	}

	if os.IsNotExist(err) {
		return candidateAbs, nil
	}

	return "", err
}

func resolveRequestedFile(urlPath string) (string, error) {
	requestPath := normalizeRequestPath(urlPath)
	if requestPath == "" {
		requestPath = "README.md"
	}

	candidates := []string{requestPath}
	if !strings.HasSuffix(strings.ToLower(requestPath), ".md") {
		candidates = append(candidates, requestPath+".md")
	}

	for _, relPath := range candidates {
		fullPath, err := resolvePathWithinRoot(relPath)
		if err != nil {
			if errors.Is(err, errOutsideRoot) {
				return "", err
			}
			continue
		}

		info, err := os.Stat(fullPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", err
		}

		if info.IsDir() {
			readmeRel := path.Join(filepath.ToSlash(relPath), "README.md")
			readmePath, err := resolvePathWithinRoot(readmeRel)
			if err != nil {
				if errors.Is(err, errOutsideRoot) {
					return "", err
				}
				continue
			}
			readmeInfo, err := os.Stat(readmePath)
			if err == nil && !readmeInfo.IsDir() {
				return readmePath, nil
			}
			continue
		}

		return fullPath, nil
	}

	return "", os.ErrNotExist
}

func getSearchIndex() []SearchEntry {
	cache.mu.RLock()
	stale := len(cache.index) == 0 || time.Since(cache.builtAt) > searchCacheTTL
	cache.mu.RUnlock()

	if stale {
		cache.mu.Lock()
		if len(cache.index) == 0 || time.Since(cache.builtAt) > searchCacheTTL {
			cache.index = buildSearchIndex(contentDir, "")
			cache.builtAt = time.Now()
		}
		cache.mu.Unlock()
	}

	cache.mu.RLock()
	index := make([]SearchEntry, len(cache.index))
	copy(index, cache.index)
	cache.mu.RUnlock()

	return index
}

func getNavTree() []*Node {
	navCache.mu.RLock()
	stale := len(navCache.tree) == 0 || time.Since(navCache.builtAt) > navCacheTTL
	navCache.mu.RUnlock()

	if stale {
		navCache.mu.Lock()
		if len(navCache.tree) == 0 || time.Since(navCache.builtAt) > navCacheTTL {
			navCache.tree = buildNav(contentDir, "")
			navCache.builtAt = time.Now()
		}
		navCache.mu.Unlock()
	}

	navCache.mu.RLock()
	tree := navCache.tree
	navCache.mu.RUnlock()
	return tree
}

func searchDocs(query string) []SearchResult {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return nil
	}
	if len(trimmed) > maxSearchQueryLength {
		trimmed = trimmed[:maxSearchQueryLength]
	}

	q := strings.ToLower(trimmed)
	index := getSearchIndex()
	matches := make([]searchMatch, 0, maxSearchResults)

	for _, entry := range index {
		titleLower := strings.ToLower(entry.Title)
		contentLower := strings.ToLower(entry.Content)
		titleMatch := strings.Contains(titleLower, q)
		contentIdx := strings.Index(contentLower, q)
		if !titleMatch && contentIdx < 0 {
			continue
		}

		matches = append(matches, searchMatch{
			result: SearchResult{
				Title:   entry.Title,
				Path:    entry.Path,
				Snippet: getSnippet(entry.Content, q),
			},
			titleMatch: titleMatch,
		})
	}

	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].titleMatch != matches[j].titleMatch {
			return matches[i].titleMatch
		}
		return matches[i].result.Title < matches[j].result.Title
	})

	if len(matches) > maxSearchResults {
		matches = matches[:maxSearchResults]
	}

	results := make([]SearchResult, len(matches))
	for i := range matches {
		results[i] = matches[i].result
	}
	return results
}

func getSnippet(text, q string) string {
	idx := strings.Index(strings.ToLower(text), q)
	if idx < 0 {
		return ""
	}
	start := max(0, idx-40)
	end := min(len(text), idx+len(q)+80)
	snippet := strings.ReplaceAll(text[start:end], "\n", " ")
	snippet = strings.TrimSpace(snippet)
	if start > 0 {
		snippet = "..." + snippet
	}
	if end < len(text) {
		snippet += "..."
	}
	return snippet
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func activePathFor(fullPath string) string {
	relActive, err := filepath.Rel(contentDir, fullPath)
	if err != nil {
		return "README.md"
	}
	relActive = filepath.ToSlash(relActive)
	if relActive == "." || strings.HasPrefix(relActive, "../") {
		return "README.md"
	}
	return relActive
}

// ---------------------------------------------------------------------------
// Page handler
// ---------------------------------------------------------------------------

func handler(w http.ResponseWriter, r *http.Request) {
	fullPath, err := resolveRequestedFile(r.URL.Path)
	if err != nil {
		if errors.Is(err, errOutsideRoot) || errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		log.Printf("Path resolution error: %v", err)
		writePlainError(w, http.StatusInternalServerError, "Error resolving file")
		return
	}

	content, err := os.ReadFile(fullPath)
	if err != nil {
		writePlainError(w, http.StatusInternalServerError, "Error reading file")
		return
	}

	var buf bytes.Buffer
	if err := md.Convert(content, &buf); err != nil {
		writePlainError(w, http.StatusInternalServerError, "Error rendering markdown")
		return
	}

	// Partial response for SPA fetch requests - only the inner content HTML
	if r.Header.Get("X-Requested-With") == "spa-fetch" {
		relActive := activePathFor(fullPath)
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]string{
			"content": buf.String(),
			"title":   filepath.Base(fullPath),
			"active":  relActive,
		}); err != nil {
			log.Printf("Failed to encode SPA response: %v", err)
		}
		return
	}

	nav := getNavTree()
	relActive := activePathFor(fullPath)

	data := PageData{
		Title:   filepath.Base(fullPath),
		Content: template.HTML(buf.String()),
		Nav:     nav,
		Active:  relActive,
		Style:   highlightCSS,
	}

	renderTemplate(w, data)
}

// ---------------------------------------------------------------------------
// Search handler
// ---------------------------------------------------------------------------

func searchHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writePlainError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")

	results := searchDocs(r.URL.Query().Get("q"))
	if err := json.NewEncoder(w).Encode(results); err != nil {
		log.Printf("Failed to encode search response: %v", err)
	}
}

func buildSearchIndex(baseDir, subDir string) []SearchEntry {
	var entries []SearchEntry
	fullPath := filepath.Join(baseDir, subDir)
	dirEntries, err := os.ReadDir(fullPath)
	if err != nil {
		return entries
	}
	for _, e := range dirEntries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" {
			continue
		}
		if e.Type()&os.ModeSymlink != 0 {
			continue
		}
		relPath := filepath.Join(subDir, name)
		if e.IsDir() {
			entries = append(entries, buildSearchIndex(baseDir, relPath)...)
		} else if strings.HasSuffix(name, ".md") {
			content, err := os.ReadFile(filepath.Join(baseDir, relPath))
			if err != nil {
				continue
			}
			title := strings.TrimSuffix(name, ".md")
			entries = append(entries, SearchEntry{
				Title:   title,
				Path:    filepath.ToSlash(relPath),
				Content: string(content),
			})
		}
	}
	return entries
}

// ---------------------------------------------------------------------------
// Nav tree
// ---------------------------------------------------------------------------

var skipNames = map[string]bool{
	"node_modules": true, "vendor": true, "main.go": true,
	"go.mod": true, "go.sum": true,
}

func buildNav(baseDir, subDir string) []*Node {
	var nodes []*Node
	fullPath := filepath.Join(baseDir, subDir)
	entries, err := os.ReadDir(fullPath)
	if err != nil {
		return nodes
	}

	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || skipNames[name] {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		relPath := filepath.Join(subDir, name)
		if entry.IsDir() {
			children := buildNav(baseDir, relPath)
			if len(children) > 0 {
				nodes = append(nodes, &Node{
					Name:     name,
					Path:     filepath.ToSlash(relPath),
					IsDir:    true,
					Children: children,
				})
			}
		} else if strings.HasSuffix(name, ".md") {
			nodes = append(nodes, &Node{
				Name:  strings.TrimSuffix(name, ".md"),
				Path:  filepath.ToSlash(relPath),
				IsDir: false,
			})
		}
	}

	sort.Slice(nodes, func(i, j int) bool {
		// Dirs first, then files
		if nodes[i].IsDir != nodes[j].IsDir {
			return nodes[i].IsDir
		}
		return nodes[i].Name < nodes[j].Name
	})

	return nodes
}

// ---------------------------------------------------------------------------
// Template
// ---------------------------------------------------------------------------

const layout = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>{{.Title}}</title>
  <style>
    /* ── Reset & tokens ── */
    *, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }
    :root {
      --sidebar-w: 272px;
      --toc-w: 224px;
      --accent: #5b8dee;
      --accent-bg: #eef3fd;
      --bg: #ffffff;
      --sidebar-bg: #f7f8fa;
      --border: #e2e5ea;
      --text: #1c1e21;
      --muted: #6b7280;
      --radius: 6px;
      --font: -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif;
      --mono: "SFMono-Regular", Consolas, "Liberation Mono", Menlo, monospace;
    }
    body {
      font-family: var(--font);
      font-size: 15px;
      line-height: 1.7;
      color: var(--text);
      background: var(--bg);
      display: flex;
      height: 100vh;
      overflow: hidden;
    }

    /* ── Sidebar ── */
    #sidebar {
      width: var(--sidebar-w);
      flex-shrink: 0;
      background: var(--sidebar-bg);
      border-right: 1px solid var(--border);
      display: flex;
      flex-direction: column;
      height: 100vh;
      overflow: hidden;
      transition: transform 0.25s ease;
    }
    #sidebar-inner {
      flex: 1;
      overflow-y: auto;
      padding: 12px 8px 24px;
    }
    #sidebar-header {
      padding: 14px 14px 10px;
      border-bottom: 1px solid var(--border);
      flex-shrink: 0;
    }
    #sidebar-header .site-name {
      font-weight: 700;
      font-size: 15px;
      color: var(--text);
      text-decoration: none;
      display: flex;
      align-items: center;
      gap: 6px;
    }

    /* Search */
    #search-wrap {
      padding: 10px 14px;
      border-bottom: 1px solid var(--border);
      flex-shrink: 0;
      position: relative;
    }
    #search {
      width: 100%;
      padding: 6px 10px 6px 30px;
      font-size: 13px;
      border: 1px solid var(--border);
      border-radius: var(--radius);
      background: var(--bg);
      color: var(--text);
      outline: none;
      transition: border-color 0.15s;
    }
    #search:focus { border-color: var(--accent); }
    #search-icon {
      position: absolute;
      left: 23px;
      top: 50%;
      transform: translateY(-50%);
      color: var(--muted);
      pointer-events: none;
      font-size: 13px;
    }
    #search-results {
      display: none;
      background: var(--bg);
      border: 1px solid var(--border);
      border-radius: var(--radius);
      box-shadow: 0 4px 16px rgba(0,0,0,.1);
      position: absolute;
      left: 8px;
      right: 8px;
      top: calc(100% - 4px);
      z-index: 999;
      max-height: 320px;
      overflow-y: auto;
    }
    #search-results a {
      display: block;
      padding: 8px 12px;
      font-size: 13px;
      color: var(--text);
      text-decoration: none;
      border-bottom: 1px solid var(--border);
    }
    #search-results a:last-child { border-bottom: none; }
    #search-results a:hover, #search-results a.focused { background: var(--accent-bg); color: var(--accent); }
    #search-results .res-title { font-weight: 600; }
    #search-results .res-snippet { font-size: 11px; color: var(--muted); margin-top: 2px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }

    /* Nav tree */
    nav ul { list-style: none; padding: 0; margin: 0; }
    nav li { margin: 1px 0; }
    nav a {
      display: block;
      padding: 5px 10px;
      font-size: 13.5px;
      color: var(--text);
      text-decoration: none;
      border-radius: var(--radius);
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
    }
    nav a:hover { background: var(--border); }
    nav a.active { background: var(--accent-bg); color: var(--accent); font-weight: 600; }
    .dir-row {
      display: flex;
      align-items: center;
      gap: 4px;
      padding: 6px 10px 3px;
      cursor: pointer;
      user-select: none;
    }
    .dir-row:hover { background: var(--border); border-radius: var(--radius); }
    .dir-label {
      font-size: 11.5px;
      font-weight: 700;
      text-transform: uppercase;
      letter-spacing: .04em;
      color: var(--muted);
      flex: 1;
    }
    .dir-toggle {
      font-size: 10px;
      color: var(--muted);
      transition: transform 0.18s;
      display: inline-block;
    }
    .dir-children {
      padding-left: 12px;
      border-left: 1px solid var(--border);
      margin-left: 14px;
      overflow: hidden;
    }
    .dir-children.collapsed { display: none; }

    /* ── Main ── */
    #main {
      flex: 1;
      display: flex;
      overflow: hidden;
    }
    #content-wrap {
      flex: 1;
      overflow-y: auto;
      padding: 48px 56px 80px;
    }
    #content {
      max-width: 760px;
      margin: 0 auto;
    }

    /* ── TOC ── */
    #toc-wrap {
      width: var(--toc-w);
      flex-shrink: 0;
      overflow-y: auto;
      padding: 48px 16px 48px 8px;
      border-left: 1px solid var(--border);
    }
    #toc { position: sticky; top: 0; }
    #toc-title {
      font-size: 11px;
      font-weight: 700;
      text-transform: uppercase;
      letter-spacing: .06em;
      color: var(--muted);
      margin-bottom: 10px;
    }
    #toc ul { list-style: none; padding: 0; }
    #toc li { margin: 3px 0; }
    #toc a {
      font-size: 12.5px;
      color: var(--muted);
      text-decoration: none;
      display: block;
      padding: 2px 6px;
      border-radius: 4px;
      border-left: 2px solid transparent;
      line-height: 1.4;
    }
    #toc a:hover { color: var(--accent); }
    #toc a.active { color: var(--accent); border-left-color: var(--accent); font-weight: 500; }
    #toc li[data-level="2"] { padding-left: 12px; }
    #toc li[data-level="3"] { padding-left: 24px; }

    /* ── Loading bar ── */
    #progress {
      position: fixed;
      top: 0; left: 0;
      height: 2px;
      background: var(--accent);
      width: 0;
      transition: width 0.2s ease, opacity 0.3s ease;
      z-index: 9999;
      opacity: 0;
    }

    /* ── Mobile toggle ── */
    #sidebar-toggle {
      display: none;
      position: fixed;
      bottom: 20px;
      left: 20px;
      z-index: 1000;
      background: var(--accent);
      color: #fff;
      border: none;
      border-radius: 50%;
      width: 44px;
      height: 44px;
      font-size: 20px;
      cursor: pointer;
      box-shadow: 0 2px 8px rgba(0,0,0,.2);
    }

    /* ── Markdown content styles ── */
    {{.Style}}

    #content h1,#content h2,#content h3,#content h4,#content h5,#content h6 {
      margin-top: 1.6em;
      margin-bottom: .5em;
      line-height: 1.3;
      font-weight: 700;
    }
    #content h1 { font-size: 2em; border-bottom: 1px solid var(--border); padding-bottom: .3em; }
    #content h2 { font-size: 1.45em; border-bottom: 1px solid var(--border); padding-bottom: .2em; }
    #content h3 { font-size: 1.2em; }
    #content p { margin-bottom: 1em; }
    #content a { color: var(--accent); text-decoration: none; }
    #content a:hover { text-decoration: underline; }
    #content pre {
      padding: 16px;
      border-radius: var(--radius);
      overflow-x: auto;
      line-height: 1.5;
      margin-bottom: 1.2em;
      font-size: 13px;
    }
    #content pre code { background: none; padding: 0; font-size: inherit; border-radius: 0; }
    #content code {
      font-family: var(--mono);
      font-size: 0.875em;
      background: #f0f2f5;
      padding: 2px 5px;
      border-radius: 4px;
    }
    #content blockquote {
      border-left: 4px solid var(--accent);
      margin: 0 0 1em;
      padding: .5em 1em;
      color: var(--muted);
      background: var(--accent-bg);
      border-radius: 0 var(--radius) var(--radius) 0;
    }
    #content table { border-collapse: collapse; width: 100%; margin-bottom: 1.2em; font-size: 14px; }
    #content th, #content td { border: 1px solid var(--border); padding: 7px 12px; text-align: left; }
    #content th { background: var(--sidebar-bg); font-weight: 600; }
    #content tr:nth-child(2n) td { background: #fafbfc; }
    #content img { max-width: 100%; border-radius: var(--radius); }
    #content ul, #content ol { padding-left: 1.5em; margin-bottom: 1em; }
    #content li { margin-bottom: .25em; }
    #content hr { border: none; border-top: 1px solid var(--border); margin: 2em 0; }

    /* ── Responsive ── */
    @media (max-width: 1100px) {
      #toc-wrap { display: none; }
    }
    @media (max-width: 768px) {
      #sidebar {
        position: fixed;
        left: 0; top: 0;
        z-index: 500;
        transform: translateX(-100%);
        height: 100vh;
      }
      #sidebar.open { transform: translateX(0); }
      #sidebar-toggle { display: flex; align-items: center; justify-content: center; }
      #content-wrap { padding: 32px 20px 60px; }
    }
  </style>
</head>
<body>
  <div id="progress"></div>

  <aside id="sidebar">
    <div id="search-wrap">
      <span id="search-icon">🔍</span>
      <input id="search" type="search" placeholder="Search…" autocomplete="off" spellcheck="false">
      <div id="search-results"></div>
    </div>
    <div id="sidebar-inner">
      <nav id="nav-tree">
        <ul>
          <li><a href="/" data-spa class="{{if eq .Active "README.md"}}active{{end}}">Home</a></li>
          {{range .Nav}}{{template "navNode" (dict "Node" . "Active" $.Active)}}{{end}}
        </ul>
      </nav>
    </div>
  </aside>

  <div id="main">
    <div id="content-wrap">
      <article id="content">
        {{.Content}}
      </article>
    </div>
    <div id="toc-wrap">
      <nav id="toc">
        <div id="toc-title">On this page</div>
        <ul id="toc-list"></ul>
      </nav>
    </div>
  </div>

  <button id="sidebar-toggle" onclick="toggleSidebar()" title="Toggle sidebar">Menu</button>

<script>
// ── State ──────────────────────────────────────────────────────────────────
const OPEN_DIRS_KEY = 'openDirs';
let searchRequestCounter = 0;
let currentActive = {{.Active | js}};

function resolveInternalUrl(url) {
  const resolved = new URL(url, window.location.href);
  return {
    fetchUrl: resolved.pathname + resolved.search,
    historyUrl: resolved.pathname + resolved.search + resolved.hash,
    hash: resolved.hash,
  };
}

function scrollToHash(hash, behavior = 'smooth') {
  if (!hash) return false;

  let targetId = hash.replace(/^#/, '');
  if (!targetId) return false;

  try {
    targetId = decodeURIComponent(targetId);
  } catch (_) {}

  const target = document.getElementById(targetId);
  if (!target) return false;

  target.scrollIntoView({ behavior, block: 'start' });
  return true;
}

// ── Progress bar ───────────────────────────────────────────────────────────
const progress = document.getElementById('progress');
function showProgress() {
  progress.style.opacity = '1';
  progress.style.width = '60%';
}
function doneProgress() {
  progress.style.width = '100%';
  setTimeout(() => { progress.style.opacity = '0'; progress.style.width = '0'; }, 300);
}

// ── SPA Navigation ─────────────────────────────────────────────────────────
async function navigate(url, pushState = true) {
  const { fetchUrl, historyUrl, hash } = resolveInternalUrl(url);

  showProgress();
  try {
    const res = await fetch(fetchUrl, {
      headers: { 'X-Requested-With': 'spa-fetch' }
    });
    if (!res.ok) { window.location.href = fetchUrl; return; }
    const data = await res.json();

    document.getElementById('content').innerHTML = data.content;
    document.title = data.title;
    currentActive = data.active;

    if (pushState) history.pushState({ url: historyUrl }, data.title, historyUrl);

    // Update active link in nav
    document.querySelectorAll('#nav-tree a[data-spa]').forEach(a => {
      const aPath = a.getAttribute('href').replace(/^\//, '');
      a.classList.toggle('active', aPath === currentActive || aPath === currentActive.replace(/\.md$/, ''));
    });
    // Home special case
    const homeLink = document.querySelector('#nav-tree a[href="/"]');
    if (homeLink) homeLink.classList.toggle('active', currentActive === 'README.md');
    expandActiveFolders();

    buildTOC();
    if (!scrollToHash(hash, 'auto')) scrollToTop();
  } catch (e) {
    window.location.href = historyUrl;
  }
  doneProgress();
}

function scrollToTop() {
  document.getElementById('content-wrap').scrollTop = 0;
}

// Intercept all clicks
document.addEventListener('click', e => {
  const link = e.target.closest('a');
  if (!link) return;
  const href = link.getAttribute('href');
  if (!href) return;
  // Skip: external, mailto, data attrs
  if (href.startsWith('http') || href.startsWith('//') || href.startsWith('mailto:') || href.startsWith('data:')) return;
  if (href.startsWith('#')) {
    e.preventDefault();
    if (scrollToHash(href)) {
      const nextUrl = window.location.pathname + window.location.search + href;
      history.pushState({ url: nextUrl }, document.title, nextUrl);
    }
    return;
  }
  // Skip non-doc links
  if (link.target === '_blank') return;
  e.preventDefault();
  navigate(href);
});

window.addEventListener('popstate', e => {
  const url = (e.state && e.state.url) || (window.location.pathname + window.location.search + window.location.hash);
  const { fetchUrl, hash } = resolveInternalUrl(url);
  const currentUrl = window.location.pathname + window.location.search;
  if (fetchUrl === currentUrl) {
    if (!scrollToHash(hash, 'auto')) scrollToTop();
    return;
  }
  navigate(url, false);
});

// ── Table of Contents ──────────────────────────────────────────────────────
function buildTOC() {
  const content = document.getElementById('content');
  const headings = content.querySelectorAll('h1, h2, h3');
  const list = document.getElementById('toc-list');
  list.innerHTML = '';

  if (headings.length < 2) {
    document.getElementById('toc-wrap').style.display = 'none';
    return;
  }
  document.getElementById('toc-wrap').style.display = '';

  headings.forEach(h => {
    const level = parseInt(h.tagName[1]);
    const li = document.createElement('li');
    li.dataset.level = level;
    const a = document.createElement('a');
    a.href = '#' + h.id;
    a.textContent = h.textContent;
    a.addEventListener('click', ev => {
      ev.preventDefault();
      if (scrollToHash(a.getAttribute('href'))) {
        const nextUrl = window.location.pathname + window.location.search + a.getAttribute('href');
        history.pushState({ url: nextUrl }, document.title, nextUrl);
      }
    });
    li.appendChild(a);
    list.appendChild(li);
  });

  // IntersectionObserver for active TOC highlighting
  if (window._tocObserver) window._tocObserver.disconnect();
  window._tocObserver = new IntersectionObserver(entries => {
    entries.forEach(entry => {
      if (entry.isIntersecting) {
        list.querySelectorAll('a').forEach(a => a.classList.remove('active'));
        const active = list.querySelector('a[href="#' + entry.target.id + '"]');
        if (active) active.classList.add('active');
      }
    });
  }, { rootMargin: '-10% 0px -80% 0px' });
  headings.forEach(h => window._tocObserver.observe(h));
}

// ── Collapsible sidebar ────────────────────────────────────────────────────
function getOpenDirs() {
  try { return new Set(JSON.parse(localStorage.getItem(OPEN_DIRS_KEY) || '[]')); } catch { return new Set(); }
}
function saveOpenDirs(set) {
  try { localStorage.setItem(OPEN_DIRS_KEY, JSON.stringify([...set])); } catch {}
}

function initSidebar() {
  const openDirs = getOpenDirs();
  document.querySelectorAll('.dir-row').forEach(row => {
    const dirPath = row.dataset.path;
    const children = row.nextElementSibling;
    const toggle = row.querySelector('.dir-toggle');
    const isOpen = openDirs.has(dirPath);

    if (isOpen) {
      children.classList.remove('collapsed');
      toggle.style.transform = 'rotate(90deg)';
    } else {
      children.classList.add('collapsed');
    }

    row.addEventListener('click', () => {
      const open = !children.classList.contains('collapsed');
      const dirs = getOpenDirs();
      if (open) {
        children.classList.add('collapsed');
        toggle.style.transform = '';
        dirs.delete(dirPath);
      } else {
        children.classList.remove('collapsed');
        toggle.style.transform = 'rotate(90deg)';
        dirs.add(dirPath);
      }
      saveOpenDirs(dirs);
    });
  });
}

// Expand the folder containing the current active file
function expandActiveFolders() {
  const openDirs = getOpenDirs();
  // Find active link and walk up to expand its parents
  const activeLink = document.querySelector('#nav-tree a.active');
  if (!activeLink) return;
  let el = activeLink.parentElement;
  while (el && el.id !== 'nav-tree') {
    if (el.classList.contains('dir-children')) {
      el.classList.remove('collapsed');
      const row = el.previousElementSibling;
      if (row && row.dataset.path) {
        openDirs.add(row.dataset.path);
        const t = row.querySelector('.dir-toggle');
        if (t) t.style.transform = 'rotate(90deg)';
      }
    }
    el = el.parentElement;
  }
  saveOpenDirs(openDirs);
}

// ── Search ─────────────────────────────────────────────────────────────────
const searchInput = document.getElementById('search');
const searchResults = document.getElementById('search-results');
let searchDebounce;
let focusedIdx = -1;

searchInput.addEventListener('input', () => {
  clearTimeout(searchDebounce);
  searchDebounce = setTimeout(() => { void doSearch(); }, 120);
});

searchInput.addEventListener('keydown', e => {
  const items = searchResults.querySelectorAll('a');
  if (e.key === 'ArrowDown') { focusedIdx = Math.min(focusedIdx + 1, items.length - 1); updateFocus(items); e.preventDefault(); }
  else if (e.key === 'ArrowUp') { focusedIdx = Math.max(focusedIdx - 1, 0); updateFocus(items); e.preventDefault(); }
  else if (e.key === 'Enter' && focusedIdx >= 0) { items[focusedIdx]?.click(); e.preventDefault(); }
  else if (e.key === 'Escape') { searchResults.style.display = 'none'; searchInput.blur(); }
});

function updateFocus(items) {
  items.forEach((a, i) => a.classList.toggle('focused', i === focusedIdx));
  items[focusedIdx]?.scrollIntoView({ block: 'nearest' });
}

async function doSearch() {
  const q = searchInput.value.trim();
  if (!q) { searchResults.style.display = 'none'; return; }

  const requestId = ++searchRequestCounter;
  try {
    const res = await fetch('/search?q=' + encodeURIComponent(q), { headers: { 'X-Requested-With': 'spa-fetch' } });
    if (!res.ok) { searchResults.style.display = 'none'; return; }

    const results = await res.json();
    if (requestId !== searchRequestCounter) return;

    if (!Array.isArray(results) || results.length === 0) {
      searchResults.innerHTML = '<a style="color:var(--muted);cursor:default">No results</a>';
      searchResults.style.display = 'block';
      return;
    }

    searchResults.innerHTML = results.map(entry => {
      return '<a href="/' + entry.path + '" data-spa>' +
        '<div class="res-title">' + escHtml(entry.title || '') + '</div>' +
        (entry.snippet ? '<div class="res-snippet">' + escHtml(entry.snippet) + '</div>' : '') +
        '</a>';
    }).join('');

    searchResults.style.display = 'block';
    focusedIdx = -1;
  } catch (e) {
    if (requestId === searchRequestCounter) {
      searchResults.style.display = 'none';
    }
  }
}

function escHtml(s) {
  return s.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
}

document.addEventListener('click', e => {
  if (!document.getElementById('search-wrap').contains(e.target)) {
    searchResults.style.display = 'none';
  }
});

// ── Mobile sidebar ─────────────────────────────────────────────────────────
function toggleSidebar() {
  document.getElementById('sidebar').classList.toggle('open');
}

// ── Boot ───────────────────────────────────────────────────────────────────
buildTOC();
initSidebar();
expandActiveFolders();
if (window.location.hash) {
  scrollToHash(window.location.hash, 'auto');
}

// Push initial state so popstate works
const initialUrl = window.location.pathname + window.location.search + window.location.hash;
history.replaceState({ url: initialUrl }, document.title, initialUrl);
</script>
</body>
</html>
`

// ---------------------------------------------------------------------------
// Template helpers
// ---------------------------------------------------------------------------

var funcMap = template.FuncMap{
	"dict": func(pairs ...interface{}) map[string]interface{} {
		m := make(map[string]interface{})
		for i := 0; i+1 < len(pairs); i += 2 {
			m[pairs[i].(string)] = pairs[i+1]
		}
		return m
	},
}

const navNodeTmpl = `{{define "navNode"}}
{{- $node := .Node}}{{- $active := .Active -}}
{{if $node.IsDir}}
  <li>
    <div class="dir-row" data-path="{{$node.Path}}">
      <span class="dir-label">{{$node.Name}}</span>
      <span class="dir-toggle">▶</span>
    </div>
    <ul class="dir-children collapsed">
      {{range $node.Children}}{{template "navNode" (dict "Node" . "Active" $active)}}{{end}}
    </ul>
  </li>
{{else}}
  {{if ne $node.Path "README.md"}}
  <li><a href="/{{$node.Path}}" data-spa class="{{if eq $active $node.Path}}active{{end}}">{{$node.Name}}</a></li>
  {{end}}
{{end}}
{{end}}`

func renderTemplate(w http.ResponseWriter, data PageData) {
	if pageTmpl == nil {
		writePlainError(w, http.StatusInternalServerError, "Template not initialized")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pageTmpl.ExecuteTemplate(w, "layout", data); err != nil {
		log.Printf("Template execute error: %v", err)
	}
}
