package character

import (
	"strings"
	"testing"
)

func TestEstimateCharacterTokensExcludesLorebook(t *testing.T) {
	base := map[string]any{
		"name":        "Card",
		"description": "hello world",
		"first_mes":   "hi",
	}
	withBook := map[string]any{
		"name":           "Card",
		"description":    "hello world",
		"first_mes":      "hi",
		"character_book": map[string]any{"blob": strings.Repeat("x", 200000)},
	}

	plain := EstimateCharacterTokens(base)
	if plain != EstimateCharacterTokens(withBook) {
		t.Fatalf("lorebook must not change the token estimate: %d vs %d", plain, EstimateCharacterTokens(withBook))
	}
	if want := countUIField("Card") + countUIField("hello world") + countUIField("hi"); plain != want {
		t.Fatalf("estimate = %d, want %d", plain, want)
	}
}

func TestEstimateCharacterTokensCountsDepthPrompt(t *testing.T) {
	card := map[string]any{
		"name":       "Card",
		"extensions": map[string]any{"depth_prompt": map[string]any{"prompt": "abc"}},
	}
	if got, want := EstimateCharacterTokens(card), countUIField("Card")+countUIField("abc"); got != want {
		t.Fatalf("estimate = %d, want %d", got, want)
	}
}
