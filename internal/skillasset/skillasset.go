// Package skillasset embeds the AI agent skill that pubmir ships into
// every mirror repository, so the skill's content always matches the pubmir
// binary that installed it (no separate download or install step).
package skillasset

import _ "embed"

//go:embed pubmir-mirror/SKILL.md
var PubmirMirrorSkillMD []byte

// SkillRelPaths are the repository-root-relative locations the bundled skill
// is installed to, one per supported agent tool's project-skill convention:
// Claude Code (.claude/skills/<name>/SKILL.md) and opencode
// (.opencode/skills/<name>/SKILL.md). In both, the directory name is the
// invocable skill name.
var SkillRelPaths = []string{
	".claude/skills/pubmir-mirror/SKILL.md",
	".opencode/skills/pubmir-mirror/SKILL.md",
}
