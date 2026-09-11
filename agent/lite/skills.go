// skills.go SKILL.md 技能加载与提示词注入。
//
// 与 eino 版 tool/skill 的差异:lite 无工具注册体系,技能以提示词注入——
// skillsDir 非空时追加一个只读 skill 工具 + 技能清单进 systemPrompt,
// 模型按需取全文。
package lite

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// skillEntry 一个已加载的技能(SKILL.md frontmatter + 全文)。
type skillEntry struct {
	Name        string
	Description string
	Content     string
}

// loadSkills 扫描 skillsDir 下的 */SKILL.md(frontmatter name/description/enabled)。
// enabled:false 的技能不加载;字段缺省视为启用(手写 SKILL.md 无需关心此字段)。
func loadSkills(dir string) []skillEntry {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []skillEntry
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name(), "SKILL.md"))
		if err != nil {
			continue
		}
		name, desc, content, enabled := parseSkillFrontmatter(string(data), e.Name())
		if !enabled {
			continue
		}
		out = append(out, skillEntry{Name: name, Description: desc, Content: content})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// parseSkillFrontmatter 解析 YAML frontmatter 的 name/description/enabled
// (容错:name 缺省用目录名,enabled 缺省为 true)。
func parseSkillFrontmatter(text, fallback string) (name, desc, content string, enabled bool) {
	name = fallback
	content = text
	enabled = true
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "---") {
		return
	}
	body := trimmed[3:]
	end := strings.Index(body, "\n---")
	if end < 0 {
		return
	}
	fm := body[:end]
	content = strings.TrimSpace(body[end+4:])
	for _, line := range strings.Split(fm, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch strings.TrimSpace(k) {
		case "name":
			if v != "" {
				name = v
			}
		case "description":
			desc = v
		case "enabled":
			enabled = v != "false"
		}
	}
	return
}

// appendSkillSection 技能清单注入 systemPrompt(模型据此决定何时调 skill 工具)。
func appendSkillSection(prompt string, skills []skillEntry) string {
	if len(skills) == 0 {
		return prompt
	}
	var b strings.Builder
	b.WriteString(prompt)
	b.WriteString("\n\n## 可用技能\n\n")
	for _, s := range skills {
		fmt.Fprintf(&b, "<skill name=\"%s\">%s</skill>\n", s.Name, s.Description)
	}
	b.WriteString("\n处理上述领域的问题前,先用 skill 工具读取对应技能全文,再按其流程执行。\n")
	return b.String()
}
