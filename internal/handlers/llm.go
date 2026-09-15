package handlers

import (
	"github.com/TurtleTavern/turtletavern/internal/config"
	"github.com/TurtleTavern/turtletavern/internal/llm"
	"github.com/go-chi/chi/v5"
)

func RegisterLLMRoutes(r chi.Router, cfg *config.Config) {
	llm.NewChatHandler(cfg).RegisterRoutes(r)
	llm.NewTextHandler(cfg).RegisterRoutes(r)
	llm.NewKoboldHandler(cfg).RegisterRoutes(r)
	llm.NewHordeHandler(cfg).RegisterRoutes(r)
	llm.NewNovelAIHandler(cfg).RegisterRoutes(r)
	llm.NewOpenRouterHandler(cfg).RegisterRoutes(r)
	RegisterStubRoutes(r)
}
