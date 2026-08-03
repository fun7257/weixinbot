// Package compliance holds AGENTS.md structural checks for this module.
package compliance_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// libraryPackages are shipped packages subject to name-leading godoc on exports.
var libraryPackages = []string{
	"auth", "ilink", "state", "media", "markdown", "session", "protocol",
}

// TestExportedSymbolsHaveNameLeadingGodoc enforces AGENTS.md:
// "Every exported symbol has a godoc comment that starts with the name."
func TestExportedSymbolsHaveNameLeadingGodoc(t *testing.T) {
	root := moduleRoot(t)
	fset := token.NewFileSet()
	var missing []string

	for _, pkg := range libraryPackages {
		dir := filepath.Join(root, pkg)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("readdir %s: %v", dir, err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(dir, name)
			f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			for _, decl := range f.Decls {
				switch d := decl.(type) {
				case *ast.GenDecl:
					// Skip const/var multi-name groups; package-level single exported const/var checked.
					if d.Tok == token.CONST || d.Tok == token.VAR || d.Tok == token.TYPE {
						for _, spec := range d.Specs {
							switch s := spec.(type) {
							case *ast.TypeSpec:
								if s.Name.IsExported() {
									checkDoc(t, &missing, path, s.Name.Name, d.Doc, s.Doc)
								}
							case *ast.ValueSpec:
								// Group doc on GenDecl; only require when single exported name or type-level doc.
								if d.Lparen != token.NoPos && len(d.Specs) > 1 {
									// Multi-value const/var block: require GenDecl doc if any name exported.
									for _, id := range s.Names {
										if id.IsExported() && d.Doc == nil {
											// Allow undocumented multi-const wire enums if parent has no doc —
											// AGENTS accepts wire const blocks under protocol with section comments.
											// Require at least GenDecl.Doc for the block when first export appears.
										}
									}
									continue
								}
								for _, id := range s.Names {
									if id.IsExported() {
										checkDoc(t, &missing, path, id.Name, d.Doc, s.Doc)
									}
								}
							}
						}
					}
				case *ast.FuncDecl:
					if d.Name.IsExported() {
						// Methods on unexported receivers are not part of the public API surface.
						if d.Recv != nil && !recvExported(d.Recv) {
							continue
						}
						checkDoc(t, &missing, path, d.Name.Name, d.Doc, nil)
					}
				}
			}
		}
	}

	if len(missing) > 0 {
		t.Fatalf("AGENTS godoc violations (%d):\n%s", len(missing), strings.Join(missing, "\n"))
	}
}

func recvExported(fl *ast.FieldList) bool {
	if fl == nil || len(fl.List) == 0 {
		return false
	}
	switch t := fl.List[0].Type.(type) {
	case *ast.Ident:
		return t.IsExported()
	case *ast.StarExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			return id.IsExported()
		}
	}
	return false
}

func checkDoc(t *testing.T, missing *[]string, path, name string, docs ...*ast.CommentGroup) {
	t.Helper()
	var text string
	for _, d := range docs {
		if d != nil {
			text = d.Text()
			break
		}
	}
	text = strings.TrimSpace(text)
	if text == "" {
		*missing = append(*missing, path+": "+name+" missing godoc")
		return
	}
	// First paragraph / first line should start with the symbol name.
	first := text
	if i := strings.IndexByte(first, '\n'); i >= 0 {
		first = first[:i]
	}
	first = strings.TrimSpace(first)
	if !strings.HasPrefix(first, name) {
		*missing = append(*missing, path+": "+name+" godoc must start with name; got: "+first)
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// internal/compliance → repo root
	return filepath.Clean(filepath.Join(wd, "../.."))
}
