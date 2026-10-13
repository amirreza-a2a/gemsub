package ruleengine_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchitecturalBoundaries_NoForbiddenImports(t *testing.T) {
	// The ruleengine package must be a pure, modular primitive layer.
	// It must never import gemini, claude, or sing-box transport packages.
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working dir: %v", err)
	}

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		// Inspect all non-test .go files in internal/tester/ruleengine
		return strings.HasSuffix(fi.Name(), ".go") && !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("failed to parse directory %q: %v", dir, err)
	}

	forbiddenSubstrings := []string{
		"gemsub/internal/tester/gemini",
		"gemsub/internal/tester/claude",
		"sing-box",
	}

	for pkgName, pkg := range pkgs {
		for fileName, file := range pkg.Files {
			for _, imp := range file.Imports {
				importPath := strings.Trim(imp.Path.Value, `"`)
				for _, forbidden := range forbiddenSubstrings {
					if strings.Contains(importPath, forbidden) {
						t.Errorf("forbidden import %q found in file %s (package %s)",
							importPath, filepath.Base(fileName), pkgName)
					}
				}
			}
		}
	}
}
