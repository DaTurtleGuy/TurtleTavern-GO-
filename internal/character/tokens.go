package character

import "math"

const uiBytesPerToken = 3.35

// openAIChatFramingTokens is the fixed per-field overhead the frontend's OpenAI
// token counter applies: the endpoint charges 3 tokens per message plus 3 for
// priming the reply, and the message always carries role "system" (2 estimated
// tokens). See RA_CountCharTokens -> countTokensOpenAIAsync -> /api/tokenizers/openai/count.
const openAIChatFramingTokens = 8

// uiTokenFields are the character-editor inputs the frontend counts for the
// "Tokens" readout in the character panel. character_book (lorebook),
// alternate_greetings and creator_notes are deliberately absent: the UI does
// not count them, so the "Most/Least tokens" sort must not either.
var uiTokenFields = [...]string{
	"name", "description", "personality", "scenario",
	"first_mes", "mes_example", "system_prompt", "post_history_instructions",
}

// EstimateCharacterTokens reproduces the frontend's character token counter for
// the app's setup (OpenAI-compatible custom endpoint whose model has no exact
// tokenizer, so the backend estimates): ceil(utf8 bytes/3.35) per field plus the
// chat framing. It is a stable proxy for the UI's number, which is what the
// "Most/Least tokens" sort compares.
func EstimateCharacterTokens(data any) int {
	m, ok := data.(map[string]any)
	if !ok {
		return 0
	}
	total := 0
	for _, f := range uiTokenFields {
		total += countUIField(getString(m, f))
	}
	if ext, ok := m["extensions"].(map[string]any); ok {
		if dp, ok := ext["depth_prompt"].(map[string]any); ok {
			s, _ := dp["prompt"].(string)
			total += countUIField(s)
		}
	}
	return total
}

func countUIField(s string) int {
	if s == "" {
		return 0
	}
	return openAIChatFramingTokens + int(math.Ceil(float64(len(s))/uiBytesPerToken))
}
