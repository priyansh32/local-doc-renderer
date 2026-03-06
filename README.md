# Local Doc Renderer

A simple Markdown-based documentation renderer written in Go. It renders Markdown files from a directory into a navigable web interface.

## Features

- **Automatic Navigation**: Builds a sidebar based on the directory structure.
- **GFM Support**: Supports GitHub Flavored Markdown.
- **Syntax Highlighting**: Built-in syntax highlighting for code blocks.
- **Responsive Design**: Clean, responsive layout for easy reading.

## Usage

### Running the pre-built binary

You can run the `local-doc-renderer` binary directly. By default, it looks for Markdown files in the current directory.

```bash
./local-doc-renderer --dir path/to/markdown/files
```

Options:
- `--dir`: The directory containing your Markdown files (default: `.`)
- `PORT` environment variable: Set the port for the server (default: `8080`)

### Building from source

If you have Go installed, you can build the binary yourself:

```bash
go build -o local-doc-renderer main.go
```

## Project Structure

- `main.go`: The core engine logic.
- `go.mod` / `go.sum`: Go module dependencies.
- `README.md`: This documentation.
