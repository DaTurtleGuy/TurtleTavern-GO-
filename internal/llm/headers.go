package llm

import (
	"net/url"
	"strings"

	"github.com/TurtleTavern/turtletavern/internal/config"
	"github.com/TurtleTavern/turtletavern/internal/secrets"
)

const (
	TextGenOoba        = "ooba"
	TextGenMancer      = "mancer"
	TextGenVLLM        = "vllm"
	TextGenAphrodite   = "aphrodite"
	TextGenTabby       = "tabby"
	TextGenKoboldCpp   = "koboldcpp"
	TextGenTogetherAI  = "togetherai"
	TextGenLlamaCpp    = "llamacpp"
	TextGenOllama      = "ollama"
	TextGenInfermatic  = "infermaticai"
	TextGenDreamGen    = "dreamgen"
	TextGenOpenRouter  = "openrouter"
	TextGenFeatherless = "featherless"
	TextGenHuggingFace = "huggingface"
	TextGenGeneric     = "generic"
)

var openRouterHeaders = map[string]string{
	"HTTP-Referer": "https://sillytavern.app",
	"X-Title":      "SillyTavern",
}

var aimlapiHeaders = map[string]string{
	"HTTP-Referer": "https://sillytavern.app",
	"X-Title":      "SillyTavern",
}

var featherlessHeaders = map[string]string{
	"HTTP-Referer": "https://sillytavern.app",
	"X-Title":      "SillyTavern",
}

func textGenSecretKey(apiType string) string {
	switch apiType {
	case TextGenMancer:
		return "api_key_mancer"
	case TextGenVLLM:
		return "api_key_vllm"
	case TextGenAphrodite:
		return "api_key_aphrodite"
	case TextGenTabby:
		return "api_key_tabby"
	case TextGenTogetherAI:
		return "api_key_togetherai"
	case TextGenOoba:
		return "api_key_ooba"
	case TextGenInfermatic:
		return "api_key_infermaticai"
	case TextGenDreamGen:
		return "api_key_dreamgen"
	case TextGenOpenRouter:
		return "api_key_openrouter"
	case TextGenKoboldCpp:
		return "api_key_koboldcpp"
	case TextGenLlamaCpp:
		return "api_key_llamacpp"
	case TextGenFeatherless:
		return "api_key_featherless"
	case TextGenHuggingFace:
		return "api_key_huggingface"
	case TextGenGeneric:
		return "api_key_generic"
	default:
		return ""
	}
}

func AdditionalHeadersByType(apiType, server, userRoot string, cfg *config.Config) map[string]string {
	headers := map[string]string{}
	key := textGenSecretKey(apiType)
	apiKey := ""
	if key != "" {
		apiKey = secrets.ReadActiveSecret(userRoot, key)
	}
	switch apiType {
	case TextGenMancer, TextGenAphrodite:
		if apiKey != "" {
			headers["X-API-KEY"] = apiKey
			headers["Authorization"] = "Bearer " + apiKey
		}
	case TextGenTabby:
		if apiKey != "" {
			headers["x-api-key"] = apiKey
			headers["Authorization"] = "Bearer " + apiKey
		}
	case TextGenOpenRouter:
		for k, v := range openRouterHeaders {
			headers[k] = v
		}
		if apiKey != "" {
			headers["Authorization"] = "Bearer " + apiKey
		}
	case TextGenFeatherless:
		for k, v := range featherlessHeaders {
			headers[k] = v
		}
		if apiKey != "" {
			headers["Authorization"] = "Bearer " + apiKey
		}
	case TextGenVLLM, TextGenTogetherAI, TextGenOoba, TextGenInfermatic,
		TextGenDreamGen, TextGenKoboldCpp, TextGenLlamaCpp,
		TextGenHuggingFace, TextGenGeneric:
		if apiKey != "" {
			headers["Authorization"] = "Bearer " + apiKey
		}
	}
	if server != "" && cfg != nil {
		if u, err := url.Parse(server); err == nil {
			for k, v := range OverrideHeaders(cfg, u.Host) {
				headers[k] = v
			}
		}
	}
	return headers
}

func OverrideHeaders(cfg *config.Config, host string) map[string]string {
	out := map[string]string{}
	if cfg == nil || host == "" {
		return out
	}
	for _, o := range cfg.RequestOverrides {
		for _, h := range o.Hosts {
			if h == host {
				for k, v := range o.Headers {
					out[k] = v
				}
				return out
			}
		}
	}
	return out
}

func TrimV1(s string) string {
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, "/v1")
	return s
}

func TrimTrailingSlash(s string) string {
	return strings.TrimSuffix(s, "/")
}
