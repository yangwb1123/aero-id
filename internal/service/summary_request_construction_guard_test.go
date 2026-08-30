package service

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	pathpkg "path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
)

const summaryRequestImportPath = "github.com/aero/aero-id/internal/connector"

func TestSummaryRequestConstructionIsCentralized(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate construction guard")
	}
	_, violations, err := scanSummaryRequestTree(filepath.Dir(thisFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, violation := range violations {
		t.Errorf("%s; use connector.NewTrustedSummaryRequest", violation)
	}
}

func TestSummaryRequestConstructionGuardFixtures(t *testing.T) {
	const redAlias = `package service
import c "github.com/aero/aero-id/internal/connector"
var _ = c.SourceAeroVault
var _ = c.SummaryRequest{}
`
	const redDot = `package service
import . "github.com/aero/aero-id/internal/connector"
var _ = SummaryRequest{}
`
	const redNested = `package service
import renamed "github.com/aero/aero-id/internal/connector"
var _ = &renamed.SummaryRequest{}
`
	const greenCompatibility = `package service
import c "github.com/aero/aero-id/internal/connector"
func compatibility() c.SummaryRequest { return c.SummaryRequest{} }
`

	tests := []struct {
		name          string
		path          string
		source        string
		wantViolation bool
	}{
		{name: "aliased import is red", path: "unsafe_alias.go", source: redAlias, wantViolation: true},
		{name: "dot import is red", path: "unsafe_dot.go", source: redDot, wantViolation: true},
		{name: "nested source is red", path: filepath.Join("nested", "unsafe.go"), source: redNested, wantViolation: true},
		{name: "compatibility helper is green", path: "summary_request_compat.go", source: greenCompatibility, wantViolation: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			violations, err := directSummaryRequestConstructions(test.path, []byte(test.source))
			if err != nil {
				t.Fatal(err)
			}
			violations = rejectNonCompatibilityConstructions(test.path, "summary_request_compat.go", violations)
			if got := len(violations) != 0; got != test.wantViolation {
				t.Fatalf("want violation=%t, got %v", test.wantViolation, violations)
			}
		})
	}
}

func TestSummaryRequestConstructionGuardWalksRecursiveDirectories(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "nested", "unsafe.go")
	source := []byte(`package service
import . "github.com/aero/aero-id/internal/connector"
var _ = SummaryRequest{}
`)
	if err := os.WriteFile(path, source, 0o600); err != nil {
		t.Fatal(err)
	}

	_, violations, err := scanSummaryRequestTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 || !strings.Contains(violations[0], "nested") {
		t.Fatalf("recursive construction was not rejected: %v", violations)
	}
}

func scanSummaryRequestTree(root string) ([]string, []string, error) {
	allowed := filepath.Join(root, "summary_request_compat.go")
	var files []string
	var violations []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		files = append(files, path)
		found, err := directSummaryRequestConstructions(path, nil)
		if err != nil {
			return err
		}
		violations = append(violations, rejectNonCompatibilityConstructions(path, allowed, found)...)
		return nil
	})
	return files, violations, err
}

func rejectNonCompatibilityConstructions(path, allowed string, constructions []string) []string {
	if filepath.Clean(path) == filepath.Clean(allowed) {
		return nil
	}
	return constructions
}

func directSummaryRequestConstructions(path string, source []byte) ([]string, error) {
	if source == nil {
		var err error
		source, err = os.ReadFile(path)
		if err != nil {
			return nil, err
		}
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, source, 0)
	if err != nil {
		return nil, err
	}

	aliases, dotImported := connectorImportNames(file)
	if len(aliases) == 0 && !dotImported {
		return nil, nil
	}

	var violations []string
	ast.Inspect(file, func(node ast.Node) bool {
		literal, ok := node.(*ast.CompositeLit)
		if !ok || !isSummaryRequestType(literal.Type, aliases, dotImported) {
			return true
		}
		position := fset.Position(literal.Type.Pos())
		violations = append(violations, position.String()+": direct connector.SummaryRequest construction")
		return true
	})
	return violations, nil
}

func connectorImportNames(file *ast.File) (map[string]bool, bool) {
	aliases := make(map[string]bool)
	dotImported := false
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil || path != summaryRequestImportPath {
			continue
		}
		if spec.Name == nil {
			aliases[pathpkg.Base(path)] = true
			continue
		}
		switch spec.Name.Name {
		case ".":
			dotImported = true
		case "_":
			// A blank import cannot be used to construct a value.
		default:
			aliases[spec.Name.Name] = true
		}
	}
	return aliases, dotImported
}

func TestCompatibilitySummaryRequestRejectsBoundSources(t *testing.T) {
	account := &domain.Account{ID: domain.ID{1}, TenantID: "tenant", CanonicalUID: "uid", HomeRegion: "region"}
	for _, source := range []connector.Source{connector.SourceAeroIM, connector.SourceAeroVault} {
		t.Run(string(source), func(t *testing.T) {
			request, err := compatibilitySummaryRequest(source, account, "actor", account.TenantID,
				"request", "operation", nil, false)
			if err == nil || err.Error() != "bound source requires trusted summary request" || request.AccountID != (domain.ID{}) {
				t.Fatalf("request/error = %#v/%v", request, err)
			}
		})
	}
}

func isSummaryRequestType(expr ast.Expr, aliases map[string]bool, dotImported bool) bool {
	switch typeExpr := expr.(type) {
	case *ast.Ident:
		return dotImported && typeExpr.Name == "SummaryRequest"
	case *ast.SelectorExpr:
		packageName, ok := typeExpr.X.(*ast.Ident)
		return ok && aliases[packageName.Name] && typeExpr.Sel.Name == "SummaryRequest"
	case *ast.ParenExpr:
		return isSummaryRequestType(typeExpr.X, aliases, dotImported)
	case *ast.StarExpr:
		return isSummaryRequestType(typeExpr.X, aliases, dotImported)
	default:
		return false
	}
}
