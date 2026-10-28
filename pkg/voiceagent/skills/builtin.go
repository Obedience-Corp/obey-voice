package skills

import _ "embed"

// BuiltinRoot identifies embedded provenance; it is not a filesystem path.
const BuiltinRoot = "builtin:obey-voice"

//go:embed builtin/obey-voice/SKILL.md
var obeyVoiceSkillDocument []byte

func builtinObeyVoice() (Skill, error) {
	return parseSkill(obeyVoiceSkillDocument, BuiltinRoot+"/SKILL.md")
}

// ApplicationInstructions supplies the self-contained skill to native-tool
// providers, which do not use Ollama's read_skill catalog.
func ApplicationInstructions() string {
	_, body, _ := splitFrontmatter(obeyVoiceSkillDocument)
	return body
}
