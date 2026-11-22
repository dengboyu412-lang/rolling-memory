// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package core

import (
	"go/ast"
	"go/build"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// TestPortableCoreTypechecksWithoutAdapters type-checks the computational subset
// with the standard-library importer. It does not cover disk adapters or tokenzr.
// The single sanctioned external dependency is the stdlib-only leaf package
// termdoc: it is type-checked on its own first, then supplied to the core check
// through a custom importer that resolves only that one path. Every other
// non-stdlib import keeps failing the whitelist or the stdlib importer.
func TestPortableCoreTypechecksWithoutAdapters(t *testing.T) {
	if build.Default.GOROOT == "" {
		if out, err := exec.Command("go", "env", "GOROOT").Output(); err == nil {
			build.Default.GOROOT = strings.TrimSpace(string(out))
		}
	}
	fset := token.NewFileSet()
	files := []string{
		"calendar_occupancy.go",
		"cluster_state_aggregates.go",
		"index.go",
		"index_append_fast.go",
		"index_from_index.go",
		"pipeline.go",
		"pipeline_state.go",
		"pipeline_cluster.go",
		"pipeline_select.go",
		"sparse_vec.go",
		"stage3_hook.go",
		"unit_relations.go",
	}
	parsed := make([]*ast.File, 0, len(files)+1)
	for _, name := range files {
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if path != "math" && path != "sort" && path != "slices" {
				t.Fatalf("portable core %s imports %q", name, path)
			}
		}
		parsed = append(parsed, file)
	}

	const leafPath = "github.com/rocareer/rolling-memory/sdk/go/public/termdoc"
	isStdlib := func(path string) bool {
		return !strings.Contains(strings.SplitN(path, "/", 2)[0], ".")
	}
	// 叶目录在 canonical 与 mirror 两种布局下的相对位置。
	leafDir := ""
	for _, candidate := range []string{"../sdk/go/public/termdoc", "../../sdk/go/public/termdoc"} {
		if _, err := os.Stat(candidate); err == nil {
			leafDir = candidate
			break
		}
	}
	if leafDir == "" {
		t.Fatal("termdoc leaf dir not found in either canonical or mirror layout")
	}
	leafEntries, err := os.ReadDir(leafDir)
	if err != nil {
		t.Fatalf("read termdoc leaf dir: %v", err)
	}
	var leafParsed []*ast.File
	for _, entry := range leafEntries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, leafDir+"/"+name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if !isStdlib(path) {
				t.Fatalf("termdoc leaf %s imports non-stdlib package %q", name, path)
			}
		}
		leafParsed = append(leafParsed, file)
	}
	leafConf := types.Config{Importer: importer.Default()}
	leafPkg, err := leafConf.Check(leafPath, fset, leafParsed, nil)
	if err != nil {
		t.Fatalf("termdoc leaf typecheck: %v", err)
	}

	// termdoc_alias.go is the only core file allowed to import outside math/sort,
	// and its single legal import is the termdoc leaf checked above.
	aliasFile, err := parser.ParseFile(fset, "termdoc_alias.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range aliasFile.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Fatal(err)
		}
		if path != leafPath {
			t.Fatalf("portable core termdoc_alias.go imports %q, want only the termdoc leaf", path)
		}
	}
	parsed = append(parsed, aliasFile)
	conf := types.Config{Importer: portableTermdocImporter{leafPath: leafPath, leaf: leafPkg, fallback: importer.Default()}}
	if _, err := conf.Check("portable-rm", fset, parsed, nil); err != nil {
		t.Fatalf("portable core typecheck: %v", err)
	}
}

// portableTermdocImporter resolves exactly one pre-checked leaf package and
// delegates every other path to the standard-library importer.
type portableTermdocImporter struct {
	leafPath string
	leaf     *types.Package
	fallback types.Importer
}

func (i portableTermdocImporter) Import(path string) (*types.Package, error) {
	if path == i.leafPath {
		return i.leaf, nil
	}
	return i.fallback.Import(path)
}
