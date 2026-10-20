package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

type repositoryTestRelocation struct {
	SchemaVersion   int                    `json:"schema_version"`
	TestCount       int                    `json:"test_count"`
	TestNamesSHA256 string                 `json:"test_names_sha256"`
	Files           []repositoryTestSource `json:"files"`
	SharedFunctions []string               `json:"shared_functions"`
}

type repositoryTestSource struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

func TestIntegrationRunnerRepositoryRelocationPreservesOriginalTests(t *testing.T) {
	data, err := os.ReadFile("testdata/repository-test-relocation.json")
	if err != nil {
		t.Fatal(err)
	}
	var baseline repositoryTestRelocation
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&baseline); err != nil {
		t.Fatal(err)
	}
	if err := verifyRepositoryTestRelocation("../repository", baseline); err != nil {
		t.Fatal(err)
	}
}

func verifyRepositoryTestRelocation(root string, baseline repositoryTestRelocation) error {
	if baseline.SchemaVersion != 1 || baseline.TestCount < 1 || len(baseline.Files) == 0 {
		return fmt.Errorf("repository test relocation baseline is incomplete")
	}
	seen := make(map[string]bool)
	for _, file := range baseline.Files {
		if filepath.Base(file.Name) != file.Name || !strings.HasSuffix(file.Name, "_test.go") || seen[file.Name] {
			return fmt.Errorf("invalid or repeated relocation file name")
		}
		seen[file.Name] = true
		if err := verifyRelocatedRepositoryFile(root, file); err != nil {
			return err
		}
	}
	names, err := repositoryTestNames(root)
	if err != nil {
		return err
	}
	if len(names) != baseline.TestCount || repositoryTestNameDigest(names) != baseline.TestNamesSHA256 {
		return fmt.Errorf("repository test inventory differs from the reviewed original %d tests", baseline.TestCount)
	}
	return verifyRepositoryFixtureCopies(root, baseline.SharedFunctions)
}

func verifyRelocatedRepositoryFile(root string, file repositoryTestSource) error {
	if _, err := os.Stat(filepath.Join(root, file.Name)); !os.IsNotExist(err) {
		return fmt.Errorf("original repository test still exists or cannot be inspected: %s", file.Name)
	}
	data, err := readRepositoryTestSource(filepath.Join(root, "auditintegration", file.Name))
	if err != nil {
		return err
	}
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file.Name, data, parser.SkipObjectResolution)
	if err != nil {
		return err
	}
	if parsed.Name.Name != "auditintegration" {
		return fmt.Errorf("relocated test has an unexpected package: %s", file.Name)
	}
	start, end := fset.Position(parsed.Name.Pos()).Offset, fset.Position(parsed.Name.End()).Offset
	normalized := append([]byte(nil), data[:start]...)
	normalized = append(normalized, "repository_test"...)
	normalized = append(normalized, data[end:]...)
	if fmt.Sprintf("%x", sha256.Sum256(normalized)) != file.SHA256 {
		return fmt.Errorf("relocated test differs beyond the package identifier: %s", file.Name)
	}
	return nil
}

func readRepositoryTestSource(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, fmt.Errorf("repository test source must be a bounded regular file")
	}
	return os.ReadFile(path)
}

func repositoryTestNames(root string) ([]string, error) {
	seen := make(map[string]bool)
	var names []string
	for _, dir := range []string{root, filepath.Join(root, "auditintegration")} {
		files, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, file := range files {
			if !strings.HasSuffix(file.Name(), ".go") {
				continue
			}
			if dir != root && !strings.HasSuffix(file.Name(), "_test.go") {
				return nil, fmt.Errorf("audit integration package must contain test-only Go files")
			}
			if !strings.HasSuffix(file.Name(), "_test.go") {
				continue
			}
			current, err := repositorySourceTestNames(filepath.Join(dir, file.Name()))
			if err != nil {
				return nil, err
			}
			for _, name := range current {
				if seen[name] {
					return nil, fmt.Errorf("duplicate repository test: %s", name)
				}
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)
	return names, nil
}

func repositorySourceTestNames(path string) ([]string, error) {
	data, err := readRepositoryTestSource(path)
	if err != nil {
		return nil, err
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), path, data, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil {
			continue
		}
		if fn.Name.Name == "TestMain" {
			return nil, fmt.Errorf("relocation cannot introduce a repository TestMain")
		}
		if strings.HasPrefix(fn.Name.Name, "Test") || strings.HasPrefix(fn.Name.Name, "Fuzz") || strings.HasPrefix(fn.Name.Name, "Example") {
			names = append(names, fn.Name.Name)
		}
	}
	return names, nil
}

func repositoryTestNameDigest(names []string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(names, "\n")+"\n")))
}

func verifyRepositoryFixtureCopies(root string, names []string) error {
	path := filepath.Join(root, "auditintegration", "audit_fixture_helpers_test.go")
	if len(names) == 0 {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			return fmt.Errorf("unreviewed copied fixture file")
		}
		return nil
	}
	original, err := repositoryFixtureFunctions(filepath.Join(root, "audit_wiring_integration_test.go"))
	if err != nil {
		return err
	}
	copied, err := repositoryFixtureFunctions(path)
	if err != nil {
		return err
	}
	if len(copied) != len(names) {
		return fmt.Errorf("copied fixture declarations differ from the reviewed inventory")
	}
	seen := make(map[string]bool)
	for _, name := range names {
		if seen[name] || original[name] == "" || copied[name] != original[name] {
			return fmt.Errorf("copied audit fixture differs from its root definition: %s", name)
		}
		seen[name] = true
	}
	return nil
}

func repositoryFixtureFunctions(path string) (map[string]string, error) {
	data, err := readRepositoryTestSource(path)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, path, data, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	functions := make(map[string]string)
	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		var normalized bytes.Buffer
		if err := format.Node(&normalized, fset, fn); err != nil {
			return nil, err
		}
		functions[fn.Name.Name] = normalized.String()
	}
	return functions, nil
}

func TestIntegrationRunnerRepositoryRelocationRejectsDrift(t *testing.T) {
	original := "//go:build integration\n\npackage repository_test\n\nimport \"testing\"\n\nfunc TestReceipt(t *testing.T) { t.Fatal(\"required\") }\n"
	moved := strings.Replace(original, "package repository_test", "package auditintegration", 1)
	baseline := repositoryTestRelocation{SchemaVersion: 1, TestCount: 1,
		TestNamesSHA256: repositoryTestNameDigest([]string{"TestReceipt"}),
		Files:           []repositoryTestSource{{Name: "receipt_test.go", SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(original)))}}}
	cases := map[string]string{
		"unchanged": moved,
		"assertion": strings.ReplaceAll(moved, "t.Fatal", "t.Log"),
		"build tag": strings.ReplaceAll(moved, "//go:build integration", "//go:build never_run"),
		"package":   strings.ReplaceAll(moved, "package auditintegration", "package ignored"),
		"renamed":   strings.ReplaceAll(moved, "TestReceipt", "TestLostReceipt"),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeRepositoryRelocationProbe(t, filepath.Join(root, "auditintegration", "receipt_test.go"), data)
			err := verifyRepositoryTestRelocation(root, baseline)
			if (err == nil) != (name == "unchanged") {
				t.Fatalf("relocation drift result: %v", err)
			}
		})
	}
	for _, name := range []string{"duplicate test", "old copy", "missing file", "test main", "production file", "fixture drift"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeRepositoryRelocationProbe(t, filepath.Join(root, "auditintegration", "receipt_test.go"), moved)
			switch name {
			case "duplicate test":
				writeRepositoryRelocationProbe(t, filepath.Join(root, "duplicate_test.go"), original)
			case "old copy":
				writeRepositoryRelocationProbe(t, filepath.Join(root, "receipt_test.go"), original)
			case "missing file":
				if err := os.Remove(filepath.Join(root, "auditintegration", "receipt_test.go")); err != nil {
					t.Fatal(err)
				}
			case "test main":
				writeRepositoryRelocationProbe(t, filepath.Join(root, "main_test.go"), "package repository_test\nfunc TestMain() {}\n")
			case "production file":
				writeRepositoryRelocationProbe(t, filepath.Join(root, "auditintegration", "runtime.go"), "package auditintegration\n")
			case "fixture drift":
				writeRepositoryRelocationProbe(t, filepath.Join(root, "auditintegration", "audit_fixture_helpers_test.go"), "package auditintegration\nfunc hiddenHelper() {}\n")
			}
			if err := verifyRepositoryTestRelocation(root, baseline); err == nil {
				t.Fatal("repository drift accepted")
			}
		})
	}
}

func TestIntegrationRunnerRepositoryFixtureCopiesRejectChangedBody(t *testing.T) {
	root := t.TempDir()
	original := "package repository_test\nfunc countEvidence() int { return 1 }\n"
	copyPath := filepath.Join(root, "auditintegration", "audit_fixture_helpers_test.go")
	writeRepositoryRelocationProbe(t, filepath.Join(root, "audit_wiring_integration_test.go"), original)
	writeRepositoryRelocationProbe(t, copyPath, original)
	if err := verifyRepositoryFixtureCopies(root, []string{"countEvidence"}); err != nil {
		t.Fatal(err)
	}
	writeRepositoryRelocationProbe(t, copyPath, strings.ReplaceAll(original, "return 1", "return 2"))
	if err := verifyRepositoryFixtureCopies(root, []string{"countEvidence"}); err == nil {
		t.Fatal("changed copied fixture body accepted")
	}
}

func writeRepositoryRelocationProbe(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}
