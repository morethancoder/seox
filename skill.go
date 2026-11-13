// Package seox embeds the files the binary carries (SKILL.md for `seox schema`).
package seox

import _ "embed"

// Skill is SKILL.md, printed by `seox schema` so an agent learns seox from the binary itself.
//
//go:embed SKILL.md
var Skill string
