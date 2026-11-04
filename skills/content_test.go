package skills

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

func TestContentFrontmatter(t *testing.T) {
	content := Content()
	if !strings.HasSuffix(content, "\n") {
		t.Error("skill must end with a newline")
	}
	if !strings.HasPrefix(content, "---\n") {
		t.Fatal("skill must start with YAML frontmatter")
	}
	frontmatter, body, ok := strings.Cut(strings.TrimPrefix(content, "---\n"), "\n---\n")
	if !ok {
		t.Fatal("skill frontmatter must have a closing delimiter")
	}
	var metadata struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(frontmatter), &metadata); err != nil {
		t.Fatalf("parse skill frontmatter: %v", err)
	}
	if metadata.Name != filepath.Base(filepath.Dir("gh-autoaccount/SKILL.md")) {
		t.Errorf("name %q does not match skill directory", metadata.Name)
	}
	if !regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`).MatchString(metadata.Name) || len(metadata.Name) > 64 {
		t.Errorf("invalid skill name %q", metadata.Name)
	}
	if strings.TrimSpace(metadata.Description) == "" || utf8.RuneCountInString(metadata.Description) > 1024 {
		t.Error("description must contain 1–1024 characters")
	}
	if utf8.RuneCountInString(strings.TrimSpace(body)) < 100 {
		t.Error("skill body is not substantive")
	}
	if strings.Contains(strings.ToUpper(content), "TODO") {
		t.Error("skill contains a TODO placeholder")
	}
}
