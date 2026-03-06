package main

import (
	"bytes"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark-highlighting/v2"
	"github.com/alecthomas/chroma/v2/styles"
	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
)

// Global Markdown parser
var md = goldmark.New(
	goldmark.WithExtensions(
		extension.GFM,
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

type Node struct {
	Name     string
	Path     string
	IsDir    bool
	Children []*Node
}

type PageData struct {
	Title    string
	Content  template.HTML
	Nav      []*Node
	Active   string
	Style    template.CSS
}

var contentDir string

func main() {
	var port string
	flag.StringVar(&port, "port", "8080", "Port to listen on")
	flag.StringVar(&contentDir, "dir", ".", "Directory containing markdown files")
	flag.Parse()

	if envPort := os.Getenv("PORT"); envPort != "" {
		port = envPort
	}

	absPath, err := filepath.Abs(contentDir)
	if err == nil {
		contentDir = absPath
	}

	http.HandleFunc("/", handler)
	fmt.Printf("Starting server on http://localhost:%s serving from %s\n", port, contentDir)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func handler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "README.md"
	}

	// Handle directory or non-existent file
	fullPath := filepath.Join(contentDir, path)
	info, err := os.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) && !strings.HasSuffix(path, ".md") {
			fullPath += ".md"
			info, err = os.Stat(fullPath)
		}
	}

	if err != nil || (info != nil && info.IsDir()) {
		if info != nil && info.IsDir() {
			readme := filepath.Join(fullPath, "README.md")
			if _, err := os.Stat(readme); err == nil {
				fullPath = readme
			} else {
				http.NotFound(w, r)
				return
			}
		} else {
			http.NotFound(w, r)
			return
		}
	}

	content, err := os.ReadFile(fullPath)
	if err != nil {
		http.Error(w, "Error reading file", 500)
		return
	}

	var buf bytes.Buffer
	if err := md.Convert(content, &buf); err != nil {
		http.Error(w, "Error rendering markdown", 500)
		return
	}

	nav := buildNav(contentDir, "")

	var cssBuf bytes.Buffer
	formatter := chromahtml.New(chromahtml.WithClasses(true))
	style := styles.Get("monokai")
	if err := formatter.WriteCSS(&cssBuf, style); err != nil {
		log.Printf("Error generating CSS: %v", err)
	}

	// Calculate active path relative to contentDir for nav highlighting
	relActive, _ := filepath.Rel(contentDir, fullPath)
	relActive = filepath.ToSlash(relActive)

	data := PageData{
		Title:   filepath.Base(fullPath),
		Content: template.HTML(buf.String()),
		Nav:     nav,
		Active:  relActive,
		Style:   template.CSS(cssBuf.String()),
	}

	renderTemplate(w, data)
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
		if name == "." || name == ".." || strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == "main.go" || name == "go.mod" || name == "go.sum" {
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
		return nodes[i].Name < nodes[j].Name
	})

	return nodes
}

const layout = `
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>{{.Title}} - Local Doc Renderer</title>
    <style>
        :root {
            --sidebar-width: 300px;
            --primary-color: #0366d6;
            --bg-color: #ffffff;
            --sidebar-bg: #f6f8fa;
            --text-color: #24292e;
            --border-color: #e1e4e8;
        }

        body {
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif;
            line-height: 1.6;
            color: var(--text-color);
            margin: 0;
            display: flex;
            height: 100vh;
        }

        aside {
            width: var(--sidebar-width);
            background: var(--sidebar-bg);
            border-right: 1px solid var(--border-color);
            overflow-y: auto;
            padding: 20px;
            flex-shrink: 0;
        }

        main {
            flex-grow: 1;
            overflow-y: auto;
            padding: 40px 60px;
            background: var(--bg-color);
        }

        .content {
            max-width: 800px;
            margin: 0 auto;
        }

        nav ul {
            list-style: none;
            padding: 0;
            margin: 0;
        }

        nav li {
            margin: 4px 0;
        }

        nav a {
            text-decoration: none;
            color: var(--text-color);
            font-size: 14px;
            display: block;
            padding: 4px 8px;
            border-radius: 4px;
        }

        nav a:hover {
            background: #e1e4e8;
        }

        nav .active {
            background: #e1e4e8;
            font-weight: 600;
            color: var(--primary-color);
        }

        .dir-label {
            font-weight: bold;
            font-size: 12px;
            text-transform: uppercase;
            color: #586069;
            margin-top: 16px;
            margin-bottom: 8px;
            display: block;
        }

        /* Markdown Styles */
        {{.Style}}
        
        pre {
            padding: 16px;
            border-radius: 6px;
            overflow: auto;
            line-height: 1.45;
        }
        pre code {
            background: none;
            padding: 0;
            border-radius: 0;
            color: inherit;
            font-size: 13px;
        }
        blockquote {
            border-left: 4px solid #dfe2e5;
            color: #6a737d;
            padding-left: 16px;
            margin-left: 0;
        }
        table {
            border-collapse: collapse;
            width: 100%;
            margin-bottom: 16px;
        }
        table th, table td {
            border: 1px solid #dfe2e5;
            padding: 6px 13px;
        }
        table tr:nth-child(2n) {
            background-color: #f6f8fa;
        }
        img {
            max-width: 100%;
        }

        @media (max-width: 768px) {
            body {
                flex-direction: column;
            }
            aside {
                width: 100%;
                height: auto;
                border-right: none;
                border-bottom: 1px solid var(--border-color);
            }
        }
    </style>
</head>
<body>
    <aside>
        <nav>
            <a href="/" class="{{if eq .Active "README.md"}}active{{end}}">🏠 Home</a>
            {{range .Nav}}
                {{if .IsDir}}
                    <span class="dir-label">{{.Name}}</span>
                    <ul>
                        {{range .Children}}
                            <li><a href="/{{.Path}}" class="{{if eq $.Active .Path}}active{{end}}">{{.Name}}</a></li>
                        {{end}}
                    </ul>
                {{else}}
                    {{if ne .Name "README"}}
                        <li><a href="/{{.Path}}" class="{{if eq $.Active .Path}}active{{end}}">{{.Name}}</a></li>
                    {{end}}
                {{end}}
            {{end}}
        </nav>
    </aside>
    <main>
        <article class="content">
            {{.Content}}
        </article>
    </main>
</body>
</html>
`

func renderTemplate(w http.ResponseWriter, data PageData) {
	tmpl, err := template.New("layout").Parse(layout)
	if err != nil {
		http.Error(w, "Template error", 500)
		return
	}
	tmpl.Execute(w, data)
}
