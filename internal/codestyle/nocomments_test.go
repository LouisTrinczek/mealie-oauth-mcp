package codestyle

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func filesWithExtension(t *testing.T, root, extension string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			relative, _ := filepath.Rel(root, path)
			name := entry.Name()
			if path != root && strings.HasPrefix(name, ".") || name == "testdata" || relative == "docs" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) == extension {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func readInRoot(t *testing.T, root, relative string) string {
	t.Helper()
	repo, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := repo.Close(); err != nil {
			t.Error(err)
		}
	})
	content, err := repo.ReadFile(relative)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func goCommentPositions(filename string, source any) ([]string, error) {
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, filename, source, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	var positions []string
	for _, group := range file.Comments {
		for _, comment := range group.List {
			if !strings.HasPrefix(comment.Text, "//go:") {
				position := fileSet.Position(comment.Pos())
				positions = append(positions, fmt.Sprintf("%s:%d", position.Filename, position.Line))
			}
		}
	}
	return positions, nil
}

func sqlCommentLines(content string) []int {
	var lines []int
	for index, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") && !strings.HasPrefix(trimmed, "-- +goose") {
			lines = append(lines, index+1)
		}
	}
	return lines
}

func TestNoCommentsInGo(t *testing.T) {
	root := repoRoot(t)
	for _, path := range filesWithExtension(t, root, ".go") {
		positions, err := goCommentPositions(path, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, position := range positions {
			relative, _ := filepath.Rel(root, position)
			t.Errorf("comment at %s", relative)
		}
	}
}

func TestNoCommentsInSQL(t *testing.T) {
	root := repoRoot(t)
	for _, path := range filesWithExtension(t, root, ".sql") {
		relative, _ := filepath.Rel(root, path)
		content := readInRoot(t, root, relative)
		for _, line := range sqlCommentLines(content) {
			t.Errorf("comment at %s:%d", relative, line)
		}
	}
}

func TestNoCommentsInTemplates(t *testing.T) {
	root := repoRoot(t)
	for _, path := range filesWithExtension(t, root, ".html") {
		relative, _ := filepath.Rel(root, path)
		content := readInRoot(t, root, relative)
		for _, marker := range []string{"<!--", "{{/*"} {
			if strings.Contains(content, marker) {
				t.Errorf("comment %q in %s", marker, relative)
			}
		}
	}
}

func TestCommentCheckers(t *testing.T) {
	goCases := []struct {
		name   string
		source string
		failed bool
	}{
		{"line comment", "package p\n// note\n", true},
		{"block comment", "package p\n/* note */\n", true},
		{"build directive", "//go:build integration\n\npackage p\n", false},
		{"embed directive", "package p\n\nimport _ \"embed\"\n\n//go:embed migrations\nvar m string\n", false},
		{"string literal", "package p\n\nvar s = \"// not a comment\"\n", false},
		{"url in string", "package p\n\nvar u = \"https://example.com/a\"\n", false},
	}
	for _, testCase := range goCases {
		t.Run("go "+testCase.name, func(t *testing.T) {
			positions, err := goCommentPositions("case.go", testCase.source)
			if err != nil {
				t.Fatal(err)
			}
			if (len(positions) > 0) != testCase.failed {
				t.Errorf("positions = %v, want failed = %v", positions, testCase.failed)
			}
		})
	}

	sqlCases := []struct {
		name   string
		source string
		failed bool
	}{
		{"plain comment", "-- note\nSELECT 1;\n", true},
		{"indented comment", "SELECT 1;\n   -- note\n", true},
		{"goose directive", "-- +goose Up\nSELECT 1;\n", false},
		{"no comment", "SELECT 1;\n", false},
	}
	for _, testCase := range sqlCases {
		t.Run("sql "+testCase.name, func(t *testing.T) {
			if got := len(sqlCommentLines(testCase.source)) > 0; got != testCase.failed {
				t.Errorf("failed = %v, want %v", got, testCase.failed)
			}
		})
	}
}
