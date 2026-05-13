// scaffold-service emits the standard service layout described in CLAUDE.md
// Section 6 (flat package main + deploy/ subdir). See README.md alongside.
package main

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
)

//go:embed templates/*.tmpl
var templates embed.FS

var serviceNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{1,38}[a-z0-9]$`)

type tmplData struct {
	Name string
}

type fileSpec struct {
	template string
	out      string
}

// Layout maps template file → relative output path under the new service dir.
// Keep template basenames lowercase + .tmpl so embed.FS finds them.
var layout = []fileSpec{
	{"main.go.tmpl", "main.go"},
	{"handler.go.tmpl", "handler.go"},
	{"routes.go.tmpl", "routes.go"},
	{"store.go.tmpl", "store.go"},
	{"handler_test.go.tmpl", "handler_test.go"},
	{"Dockerfile.tmpl", "deploy/Dockerfile"},
	{"docker-compose.yml.tmpl", "deploy/docker-compose.yml"},
	{"azure-pipelines.yml.tmpl", "deploy/azure-pipelines.yml"},
}

func main() {
	name := flag.String("name", "", "service name (lowercase, hyphenated, e.g. presence-service)")
	root := flag.String("root", ".", "repo root (defaults to cwd)")
	force := flag.Bool("force", false, "overwrite existing files (default: refuse if dir exists)")
	flag.Parse()

	if err := run(*name, *root, *force); err != nil {
		fmt.Fprintf(os.Stderr, "scaffold-service: %v\n", err)
		os.Exit(1)
	}
}

func run(name, root string, force bool) error {
	if name == "" {
		return fmt.Errorf("-name is required")
	}
	if !serviceNameRE.MatchString(name) {
		return fmt.Errorf("invalid name %q: must match %s", name, serviceNameRE)
	}

	serviceDir := filepath.Join(root, name)
	if _, err := os.Stat(serviceDir); err == nil && !force {
		return fmt.Errorf("%s already exists; pass -force to overwrite", serviceDir)
	}

	for _, f := range layout {
		body, err := renderTemplate(f.template, tmplData{Name: name})
		if err != nil {
			return fmt.Errorf("render %s: %w", f.template, err)
		}
		outPath := filepath.Join(serviceDir, f.out)
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(outPath), err)
		}
		if err := os.WriteFile(outPath, body, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", outPath, err)
		}
		fmt.Println("wrote", outPath)
	}

	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  1. go mod tidy")
	fmt.Println("  2. go test ./" + name + "/")
	fmt.Println("  3. go build ./" + name + "/")
	fmt.Println("  4. See docs/service-creation-guide.md for what to wire up next.")
	return nil
}

func renderTemplate(name string, data tmplData) ([]byte, error) {
	body, err := fs.ReadFile(templates, filepath.ToSlash(filepath.Join("templates", name)))
	if err != nil {
		return nil, err
	}
	tmpl, err := template.New(name).Parse(string(body))
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, data); err != nil {
		return nil, fmt.Errorf("execute: %w", err)
	}
	return []byte(b.String()), nil
}
