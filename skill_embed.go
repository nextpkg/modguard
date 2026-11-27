// Package modguard embeds reusable assets distributed with the modguard CLI.
package modguard

import "embed"

// SkillFS embeds the bundled modguard agent skill under skill/.
//
//go:embed skill/SKILL.md skill/references/*.md
var SkillFS embed.FS
