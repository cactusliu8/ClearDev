package skillassets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"
)

func TestEmbeddedSkillFrontmatterIsValidYAML(t *testing.T) {
	body, err := files.ReadFile("using-ao/SKILL.md")
	if err != nil {
		t.Fatalf("read embedded SKILL.md: %v", err)
	}

	parts := strings.SplitN(string(body), "---", 3)
	if len(parts) != 3 {
		t.Fatal("embedded SKILL.md is missing YAML frontmatter delimiters")
	}

	var frontmatter struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
		Trigger     string `yaml:"trigger"`
	}
	if err := yaml.Unmarshal([]byte(parts[1]), &frontmatter); err != nil {
		t.Fatalf("parse embedded SKILL.md frontmatter: %v", err)
	}
	if frontmatter.Name != SkillName {
		t.Fatalf("frontmatter name = %q, want %q", frontmatter.Name, SkillName)
	}
	if strings.TrimSpace(frontmatter.Description) == "" {
		t.Fatal("frontmatter description is empty")
	}
	if strings.TrimSpace(frontmatter.Trigger) == "" {
		t.Fatal("frontmatter trigger is empty")
	}
}

func TestEmbeddedPreviewGuidanceDoesNotScaffoldStaticSites(t *testing.T) {
	previewBody, err := files.ReadFile("using-ao/commands/preview.md")
	if err != nil {
		t.Fatalf("read embedded preview guidance: %v", err)
	}
	previewText := string(previewBody)
	normalizedPreviewText := strings.Join(strings.Fields(previewText), " ")
	for _, required := range []string{
		"这是公开快照中的兼容占位说明",
		"优先复用仓库已经存在的预览或开发入口",
		"不要为了预览静态 HTML、Markdown 或其他简单文件而新建项目配置",
		"修改依赖或搭建新的开发服务器",
	} {
		if !strings.Contains(normalizedPreviewText, required) {
			t.Fatalf("preview guidance missing %q:\n%s", required, previewText)
		}
	}

	skillBody, err := files.ReadFile("using-ao/SKILL.md")
	if err != nil {
		t.Fatalf("read embedded SKILL.md: %v", err)
	}
	skillText := string(skillBody)
	normalizedSkillText := strings.Join(strings.Fields(skillText), " ")
	if !strings.Contains(normalizedSkillText, "[浏览器说明](commands/browser.md)") ||
		!strings.Contains(normalizedSkillText, "[预览说明](commands/preview.md)") ||
		!strings.Contains(normalizedSkillText, "原 Agent Orchestrator 操作文档未随公开仓库发布") ||
		!strings.Contains(normalizedSkillText, "不要自动创建发布、部署或远程操作") {
		t.Fatalf("skill catalog is missing focused preview/browser routing:\n%s", skillText)
	}
}

func TestEmbeddedBrowserGuidanceKeepsNetworkCaptureOptional(t *testing.T) {
	body, err := files.ReadFile("using-ao/commands/browser.md")
	if err != nil {
		t.Fatalf("read embedded browser guidance: %v", err)
	}
	text := strings.Join(strings.Fields(string(body)), " ")
	for _, required := range []string{
		"这是公开快照中的兼容占位说明",
		"仅在当前任务明确需要浏览器时使用已有浏览器能力",
		"不要自动开启网络抓包",
		"不要收集凭据、Cookie、请求体或其他敏感信息",
		"不要为了完成普通页面操作额外扩大访问范围",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("browser guidance missing %q:\n%s", required, body)
		}
	}
}

// TestInstall_WritesSkillAndIsIdempotent: Install must lay down the embedded
// skill (SKILL.md plus a commands file) under <dataDir>/skills/using-ao, and a
// second run must clobber cleanly, leaving no stale files. This is the whole
// contract the daemon boot hook relies on.
func TestInstall_WritesSkillAndIsIdempotent(t *testing.T) {
	dataDir := t.TempDir()

	if err := Install(dataDir); err != nil {
		t.Fatalf("Install: %v", err)
	}

	skillFile := filepath.Join(Dir(dataDir), "SKILL.md")
	if b, err := os.ReadFile(skillFile); err != nil {
		t.Fatalf("read %s: %v", skillFile, err)
	} else if len(b) == 0 {
		t.Fatalf("SKILL.md is empty")
	}
	if _, err := os.Stat(filepath.Join(Dir(dataDir), "commands", "spawn.md")); err != nil {
		t.Fatalf("commands/spawn.md missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(Dir(dataDir), "commands", "browser.md")); err != nil {
		t.Fatalf("commands/browser.md missing: %v", err)
	}

	// A stale file inside the skill dir must not survive a reinstall (clobber).
	stale := filepath.Join(Dir(dataDir), "stale.md")
	if err := os.WriteFile(stale, []byte("old"), 0o644); err != nil {
		t.Fatalf("seed stale file: %v", err)
	}
	if err := Install(dataDir); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale file survived reinstall (err=%v)", err)
	}
}

// TestMaterialize_WritesIntoArbitraryDest covers the opencode adapter path:
// materialize the skill into .opencode/skills/using-ao (not the data-dir layout).
func TestMaterialize_WritesIntoArbitraryDest(t *testing.T) {
	dest := filepath.Join(t.TempDir(), ".opencode", "skills", SkillName)
	if err := Materialize(dest); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "SKILL.md")); err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	} else if len(b) == 0 {
		t.Fatal("SKILL.md is empty")
	}
	if _, err := os.Stat(filepath.Join(dest, "commands", "spawn.md")); err != nil {
		t.Fatalf("commands/spawn.md missing: %v", err)
	}
}
