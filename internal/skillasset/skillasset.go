// Package skillasset embeds the Claude Code skill that pubmir ships into
// every mirror repository, so the skill's content always matches the pubmir
// binary that installed it (no separate download or install step).
package skillasset

import _ "embed"

//go:embed pubmir-mirror/SKILL.md
var PubmirMirrorSkillMD []byte

// PubmirMirrorSkillRelPath is where the skill belongs relative to a
// repository root, per Claude Code's project-skill convention
// (.claude/skills/<name>/SKILL.md — the directory name is the invocable
// skill name).
const PubmirMirrorSkillRelPath = ".claude/skills/pubmir-mirror/SKILL.md"
