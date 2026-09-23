package llm

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var (
	claudeThinkingRe        = regexp.MustCompile(`^claude-(3-7|opus-4|sonnet-4|haiku-4-5|opus-4-5|opus-4-6|sonnet-4-6)`)
	claudeWebSearchRe       = regexp.MustCompile(`^claude-(3-5|3-7|opus-4|sonnet-4|haiku-4-5|opus-4-5|opus-4-6|sonnet-4-6)`)
	claudeLimitedSamplingRe = regexp.MustCompile(`^claude-(opus-4-1|sonnet-4-5|haiku-4-5|opus-4-5|opus-4-6|sonnet-4-6)`)
	claudeVerbosityRe       = regexp.MustCompile(`^claude-(opus-4-5|opus-4-6|sonnet-4-6)`)
	claudeNoPrefillRe       = regexp.MustCompile(`^claude-(opus-4-6|sonnet-4-6)`)
)

func (h *ChatHandler) sendClaude(w http.ResponseWriter, r *http.Request, body map[string]any) {
	reverseProxy := bodyStr(body, "reverse_proxy")
	apiURL := APIClaude
	if reverseProxy != "" {
		apiURL = reverseProxy
	}
	var apiKey string
	if reverseProxy != "" {
		apiKey = bodyStr(body, "proxy_password")
	} else {
		apiKey = h.secret(r, "api_key_claude")
	}
	if apiKey == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":true}`))
		return
	}
	betaHeaders := []string{"output-128k-2025-02-19", "context-1m-2025-08-07"}
	useTools := false
	if tools, ok := body["tools"].([]any); ok && len(tools) > 0 {
		useTools = true
	}
	useSysPrompt := bodyBool(body, "use_sysprompt")
	model := bodyStr(body, "model")
	msgs := bodyMessages(body)
	if msgs == nil {
		msgs = []any{}
	}
	converted, systemPrompt := ConvertClaudeMessages(msgs, bodyStr(body, "assistant_prefill"), useSysPrompt, useTools, PromptNamesFromBody(body))
	useThinking := claudeThinkingRe.MatchString(model)
	useWebSearch := claudeWebSearchRe.MatchString(model) && bodyBool(body, "enable_web_search")
	isLimitedSampling := claudeLimitedSamplingRe.MatchString(model)
	useVerbosity := claudeVerbosityRe.MatchString(model)
	noPrefillModel := claudeNoPrefillRe.MatchString(model)
	isAdaptiveModel := h.Cfg.Claude.EnableAdaptiveThinking && claudeNoPrefillRe.MatchString(model)

	var stopSequences []any
	if stop, ok := body["stop"].([]any); ok {
		stopSequences = append(stopSequences, stop...)
	}
	if stopSequences == nil {
		stopSequences = []any{}
	}
	requestBody := map[string]any{
		"messages": converted, "model": model,
		"max_tokens": body["max_tokens"], "stop_sequences": stopSequences,
		"temperature": body["temperature"], "top_p": body["top_p"], "top_k": body["top_k"],
		"stream": body["stream"],
	}
	if useSysPrompt {
		if h.Cfg.Claude.EnableSystemPromptCache {
			if len(systemPrompt) > 0 {
				if last, ok := systemPrompt[len(systemPrompt)-1].(map[string]any); ok {
					last["cache_control"] = map[string]any{"type": "ephemeral", "ttl": h.cacheTTL()}
				}
			}
		}
		requestBody["system"] = systemPrompt
	}
	if useTools {
		betaHeaders = append(betaHeaders, "tools-2024-05-16")
		requestBody["tool_choice"] = map[string]any{"type": body["tool_choice"]}
		var tools []any
		if rawTools, ok := body["tools"].([]any); ok {
			for _, t := range rawTools {
				tm, ok := t.(map[string]any)
				if !ok || tm["type"] != "function" {
					continue
				}
				fn, _ := tm["function"].(map[string]any)
				params, _ := fn["parameters"].(map[string]any)
				tools = append(tools, map[string]any{
					"name": fn["name"], "description": fn["description"],
					"input_schema": FlattenSchema(params, SrcClaude),
				})
			}
		}
		requestBody["tools"] = tools
		if h.Cfg.Claude.EnableSystemPromptCache {
			if arr, ok := requestBody["tools"].([]any); ok && len(arr) > 0 {
				if last, ok := arr[len(arr)-1].(map[string]any); ok {
					last["cache_control"] = map[string]any{"type": "ephemeral", "ttl": h.cacheTTL()}
				}
			}
		}
	}
	if js, ok := body["json_schema"].(map[string]any); ok {
		var tools []any
		if t, ok := requestBody["tools"].([]any); ok {
			tools = t
		}
		desc, _ := js["description"].(string)
		if desc == "" {
			desc = "Well-formed JSON object"
		}
		tools = append(tools, map[string]any{"name": js["name"], "description": desc, "input_schema": js["value"]})
		requestBody["tools"] = tools
		requestBody["tool_choice"] = map[string]any{"type": "tool", "name": js["name"]}
	}
	if useWebSearch {
		var tools []any
		if t, ok := requestBody["tools"].([]any); ok {
			tools = t
		}
		tools = append([]any{map[string]any{"type": "web_search_20250305", "name": "web_search"}}, tools...)
		requestBody["tools"] = tools
	}
	if h.cachingAtDepth() != -1 {
		CachingAtDepthForClaude(converted, h.cachingAtDepth(), h.cacheTTL())
	}
	if h.Cfg.Claude.EnableSystemPromptCache || h.cachingAtDepth() != -1 {
		betaHeaders = append(betaHeaders, "prompt-caching-2024-07-31", "extended-cache-ttl-2025-04-11")
	}
	if isLimitedSampling {
		if tp, ok := bodyFloat(body, "top_p"); ok && tp < 1 {
			delete(requestBody, "temperature")
		} else {
			delete(requestBody, "top_p")
		}
	}
	maxTokens, _ := bodyFloat(body, "max_tokens")
	budget := CalculateClaudeBudgetTokens(maxTokens, bodyStr(body, "reasoning_effort"), bodyBool(body, "stream"), isAdaptiveModel)
	fixThinkingPrefill := false
	if useThinking {
		if s, ok := budget.(string); ok {
			fixThinkingPrefill = true
			requestBody["thinking"] = map[string]any{"type": "adaptive"}
			oc, _ := requestBody["output_config"].(map[string]any)
			if oc == nil {
				oc = map[string]any{}
			}
			oc["effort"] = s
			requestBody["output_config"] = oc
			delete(requestBody, "top_k")
		} else if n, ok := budget.(int); ok {
			fixThinkingPrefill = true
			if maxTokens <= 1024 {
				maxTokens += 1024
				requestBody["max_tokens"] = maxTokens
			}
			requestBody["thinking"] = map[string]any{"type": "enabled", "budget_tokens": n}
			delete(requestBody, "temperature")
			delete(requestBody, "top_p")
			delete(requestBody, "top_k")
		}
	}
	if len(converted) > 0 {
		if last, ok := converted[len(converted)-1].(map[string]any); ok && last["role"] == "assistant" && (fixThinkingPrefill || noPrefillModel) {
			last["role"] = "user"
		}
	}
	if useVerbosity {
		if v, ok := body["verbosity"]; ok {
			if oc, ok := requestBody["output_config"].(map[string]any); !ok || oc["effort"] == nil {
				betaHeaders = append(betaHeaders, "effort-2025-11-24")
				if oc == nil {
					oc = map[string]any{}
				}
				oc["effort"] = v
				requestBody["output_config"] = oc
			}
		}
	}
	compactNils(requestBody)
	outHeaders := map[string]string{
		"Content-Type": "application/json", "anthropic-version": "2023-06-01", "x-api-key": apiKey,
	}
	if len(betaHeaders) > 0 {
		outHeaders["anthropic-beta"] = strings.Join(betaHeaders, ",")
	}
	endpoint := strings.TrimSuffix(apiURL, "/") + "/messages"
	if bodyBool(body, "stream") {
		upstream, err := DoStream(h.client, r.Context(), http.MethodPost, endpoint, outHeaders, requestBody)
		if err != nil {
			writeUpstreamError(w, http.StatusBadGateway, connectErrorMessage(err), false)
			return
		}
		ForwardStream(upstream, w, r)
		return
	}
	res, err := DoJSON(h.client, r.Context(), http.MethodPost, endpoint, outHeaders, requestBody)
	if err != nil {
		writeUpstreamError(w, http.StatusBadGateway, connectErrorMessage(err), false)
		return
	}
	if res.Status < 200 || res.Status >= 300 {
		log.Printf("Claude API returned error: %d %s", res.Status, string(res.Body))
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":true}`))
		return
	}
	var parsed map[string]any
	if err := json.Unmarshal(res.Body, &parsed); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":true}`))
		return
	}
	text := ""
	if content, ok := parsed["content"].([]any); ok && len(content) > 0 {
		if first, ok := content[0].(map[string]any); ok {
			text, _ = first["text"].(string)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"content": text}}},
		"content": parsed["content"],
	})
}

var geminiImageModels = []string{
	"gemini-2.0-flash-exp", "gemini-2.0-flash-exp-image-generation",
	"gemini-2.0-flash-preview-image-generation", "gemini-2.5-flash-image-preview",
	"gemini-2.5-flash-image", "gemini-3-pro-image-preview", "gemini-3.1-flash-image-preview",
}

var geminiNoSearchModels = []string{
	"gemini-2.0-flash-lite", "gemini-2.0-flash-lite-001",
	"gemini-2.0-flash-lite-preview-02-05", "gemini-robotics-er-1.5-preview",
}

func geminiThinkingModel(m string) bool {
	if matched, _ := regexp.MatchString(`^gemini-2\.5-(flash|pro)`, m); matched {
		if matched2, _ := regexp.MatchString(`-image(-preview)?$`, m); !matched2 {
			return true
		}
	}
	if matched, _ := regexp.MatchString(`^gemini-3[.\d]*-(flash|pro)`, m); matched {
		return true
	}
	return false
}

func (h *ChatHandler) sendMakerSuite(w http.ResponseWriter, r *http.Request, body map[string]any) {
	useVertex := bodyStr(body, "chat_completion_source") == SrcVertexAI
	apiName := "Google AI Studio"
	if useVertex {
		apiName = "Google Vertex AI"
	}
	model := bodyStr(body, "model")
	stream := bodyBool(body, "stream")
	enableWebSearch := bodyBool(body, "enable_web_search")
	requestImages := bodyBool(body, "request_images")
	reasoningEffort := bodyStr(body, "reasoning_effort")
	includeReasoning := bodyBool(body, "include_reasoning")
	aspectRatio := bodyStr(body, "request_image_aspect_ratio")
	imageSize := bodyStr(body, "request_image_resolution")
	isGemma := strings.Contains(model, "gemma")
	isLearnLM := strings.Contains(model, "learnlm")

	var mimeType, schemaVal any
	if v, ok := body["responseMimeType"]; ok {
		mimeType = v
	} else if _, ok := body["json_schema"]; ok {
		mimeType = "application/json"
	}
	if v, ok := body["responseSchema"]; ok {
		schemaVal = v
	} else if js, ok := body["json_schema"].(map[string]any); ok {
		schemaVal = js["value"]
	}
	maxTokens, _ := bodyFloat(body, "max_tokens")
	generationConfig := map[string]any{
		"candidateCount": 1, "maxOutputTokens": maxTokens,
		"temperature": body["temperature"], "topP": body["top_p"],
		"responseMimeType": mimeType, "responseSchema": schemaVal,
		"seed": body["seed"],
	}
	if stop, ok := body["stop"].([]any); ok && len(stop) > 0 {
		generationConfig["stopSequences"] = stop
	}
	if tk, ok := body["top_k"]; ok {
		generationConfig["topK"] = tk
	}
	compactNils(generationConfig)

	enableImageModality := false
	if requestImages {
		for _, m := range geminiImageModels {
			if m == model {
				enableImageModality = true
				break
			}
		}
	}
	if enableImageModality {
		generationConfig["responseModalities"] = []string{"text", "image"}
		if aspectRatio != "" || imageSize != "" {
			imgCfg := map[string]any{}
			if imageSize != "" && strings.HasPrefix(model, "gemini-3") {
				imgCfg["imageSize"] = imageSize
			}
			if aspectRatio != "" {
				imgCfg["aspectRatio"] = aspectRatio
			}
			generationConfig["imageConfig"] = imgCfg
		}
	}
	useSysPrompt := !enableImageModality && !isGemma && bodyBool(body, "use_sysprompt")
	msgs := bodyMessages(body)
	if msgs == nil {
		msgs = []any{}
	}
	contents, sysInstruction := ConvertGooglePrompt(msgs, model, useSysPrompt, PromptNamesFromBody(body), h.Cfg.Gemini.ThoughtSignatures)
	safety := append([]any{}, geminiSafetySettings()...)
	if useVertex {
		safety = append(safety,
			map[string]any{"category": "HARM_CATEGORY_IMAGE_HATE", "threshold": "OFF"},
			map[string]any{"category": "HARM_CATEGORY_IMAGE_DANGEROUS_CONTENT", "threshold": "OFF"},
			map[string]any{"category": "HARM_CATEGORY_IMAGE_HARASSMENT", "threshold": "OFF"},
			map[string]any{"category": "HARM_CATEGORY_IMAGE_SEXUALLY_EXPLICIT", "threshold": "OFF"},
			map[string]any{"category": "HARM_CATEGORY_JAILBREAK", "threshold": "OFF"},
		)
	}
	var tools []any
	if rawTools, ok := body["tools"].([]any); ok && len(rawTools) > 0 && !enableImageModality && !isGemma {
		var fnDecls []any
		var customTools []any
		for _, t := range rawTools {
			tm, ok := t.(map[string]any)
			if !ok {
				continue
			}
			if tm["type"] == "function" {
				fn, _ := tm["function"].(map[string]any)
				if fn == nil {
					continue
				}
				if params, ok := fn["parameters"].(map[string]any); ok {
					delete(params, "$schema")
					if props, ok := params["properties"].(map[string]any); ok && len(props) == 0 {
						delete(fn, "parameters")
					}
				}
				fnDecls = append(fnDecls, fn)
			} else if typ, ok := tm["type"].(string); ok {
				if v, ok := tm[typ]; ok {
					customTools = append(customTools, map[string]any{typ: v})
				}
			}
		}
		if len(fnDecls) > 0 {
			tools = append(tools, map[string]any{"function_declarations": fnDecls})
		}
		if len(fnDecls) == 0 && len(customTools) > 0 {
			tools = append(tools, customTools...)
		}
	}
	noSearch := false
	for _, m := range geminiNoSearchModels {
		if m == model {
			noSearch = true
			break
		}
	}
	if enableWebSearch && !enableImageModality && !isGemma && !isLearnLM && !noSearch {
		hasFnDecl := false
		for _, t := range tools {
			if tm, ok := t.(map[string]any); ok {
				if _, ok := tm["function_declarations"]; ok {
					hasFnDecl = true
				}
			}
		}
		if !hasFnDecl {
			tools = append(tools, map[string]any{"google_search": map[string]any{}})
		}
	}
	if geminiThinkingModel(model) {
		tc := map[string]any{"includeThoughts": includeReasoning}
		budget := CalculateGoogleBudgetTokens(maxTokens, reasoningEffort, model)
		switch b := budget.(type) {
		case int:
			tc["thinkingBudget"] = b
		case string:
			if b != "" {
				tc["thinkingLevel"] = b
			}
		}
		if useVertex {
			if n, ok := tc["thinkingBudget"]; ok && n == 0 && includeReasoning {
				tc["includeThoughts"] = false
			}
		}
		generationConfig["thinkingConfig"] = tc
	}
	reqPayload := map[string]any{
		"contents": contents, "safetySettings": safety, "generationConfig": generationConfig,
	}
	if useSysPrompt {
		if si, ok := sysInstruction["parts"].([]any); ok && len(si) > 0 {
			reqPayload["systemInstruction"] = sysInstruction
		}
	}
	if len(tools) > 0 {
		reqPayload["tools"] = tools
		switch tc := body["tool_choice"].(type) {
		case string:
			var mode string
			switch tc {
			case "none":
				mode = "NONE"
			case "required":
				mode = "ANY"
			case "auto":
				mode = "AUTO"
			}
			if mode != "" {
				reqPayload["toolConfig"] = map[string]any{"functionCallingConfig": map[string]any{"mode": mode}}
			}
		case map[string]any:
			if fn, ok := tc["function"].(map[string]any); ok {
				if name, ok := fn["name"].(string); ok {
					reqPayload["toolConfig"] = map[string]any{"functionCallingConfig": map[string]any{
						"mode": "ANY", "allowedFunctionNames": []string{name},
					}}
				}
			}
		}
	}

	apiVersion := h.Cfg.Gemini.APIVersion
	if apiVersion == "" {
		apiVersion = "v1beta"
	}
	responseType := "generateContent"
	if stream {
		responseType = "streamGenerateContent"
	}
	var endpoint string
	outHeaders := map[string]string{"Content-Type": "application/json"}
	if useVertex {
		authMode := bodyStr(body, "vertexai_auth_mode")
		if authMode == "" {
			authMode = "express"
		}
		reverseProxy := bodyStr(body, "reverse_proxy")
		if reverseProxy != "" {
			endpoint = strings.TrimSuffix(reverseProxy, "/") + "/v1/publishers/google/models/" + model + ":" + responseType
			if stream {
				endpoint += "?alt=sse"
			}
			outHeaders["Authorization"] = "Bearer " + bodyStr(body, "proxy_password")
		} else if authMode == "express" {
			key := h.secret(r, "api_key_vertexai")
			if key == "" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":true}`))
				return
			}
			region := bodyStr(body, "vertexai_region")
			if region == "" {
				region = "us-central1"
			}
			projectID := bodyStr(body, "vertexai_express_project_id")
			var base string
			if region == "global" {
				base = "https://aiplatform.googleapis.com/v1"
			} else {
				base = "https://" + region + "-aiplatform.googleapis.com/v1"
			}
			if projectID != "" {
				endpoint = base + "/projects/" + projectID + "/locations/" + region + "/publishers/google/models/" + model + ":" + responseType + "?key=" + url.QueryEscape(key)
			} else {
				endpoint = base + "/publishers/google/models/" + model + ":" + responseType + "?key=" + url.QueryEscape(key)
			}
			if stream {
				endpoint += "&alt=sse"
			}
		} else if authMode == "full" {
			saJSON := h.secret(r, "vertexai_service_account_json")
			if saJSON == "" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":true}`))
				return
			}
			var sa map[string]any
			if err := json.Unmarshal([]byte(saJSON), &sa); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":true}`))
				return
			}
			projectID, _ := sa["project_id"].(string)
			if projectID == "" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":true}`))
				return
			}
			accessToken, err := vertexAccessToken(sa)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":true,"message":"` + err.Error() + `"}`))
				return
			}
			region := bodyStr(body, "vertexai_region")
			if region == "" {
				region = "us-central1"
			}
			if region == "global" {
				endpoint = "https://aiplatform.googleapis.com/v1/projects/" + projectID + "/locations/" + region + "/publishers/google/models/" + model + ":" + responseType
			} else {
				endpoint = "https://" + region + "-aiplatform.googleapis.com/v1/projects/" + projectID + "/locations/" + region + "/publishers/google/models/" + model + ":" + responseType
			}
			if stream {
				endpoint += "?alt=sse"
			}
			outHeaders["Authorization"] = "Bearer " + accessToken
		} else {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":true}`))
			return
		}
	} else {
		reverseProxy := bodyStr(body, "reverse_proxy")
		base := APIMakersuite
		if reverseProxy != "" {
			base = reverseProxy
		}
		key := ""
		if reverseProxy != "" {
			key = bodyStr(body, "proxy_password")
		} else {
			key = h.secret(r, "api_key_makersuite")
		}
		if reverseProxy == "" && key == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":true}`))
			return
		}
		endpoint = strings.TrimSuffix(base, "/") + "/" + apiVersion + "/models/" + model + ":" + responseType + "?key=" + url.QueryEscape(key)
		if stream {
			endpoint += "&alt=sse"
		}
	}

	if stream {
		upstream, err := DoStream(h.client, r.Context(), http.MethodPost, endpoint, outHeaders, reqPayload)
		if err != nil {
			writeUpstreamError(w, http.StatusInternalServerError, connectErrorMessage(err), false)
			return
		}
		ForwardStream(upstream, w, r)
		return
	}
	res, err := DoJSON(h.client, r.Context(), http.MethodPost, endpoint, outHeaders, reqPayload)
	if err != nil {
		writeUpstreamError(w, http.StatusInternalServerError, connectErrorMessage(err), false)
		return
	}
	if res.Status < 200 || res.Status >= 300 {
		log.Printf("%s API returned error: %d %s", apiName, res.Status, string(res.Body))
		w.WriteHeader(http.StatusInternalServerError)
		if parsed := TryParseJSON(res.Body); parsed != nil {
			_ = json.NewEncoder(w).Encode(parsed)
		} else {
			_, _ = w.Write([]byte(`{"error":true}`))
		}
		return
	}
	var parsed map[string]any
	if err := json.Unmarshal(res.Body, &parsed); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":true}`))
		return
	}
	candidates, _ := parsed["candidates"].([]any)
	if len(candidates) == 0 {
		msg := apiName + " API returned no candidate"
		if pf, ok := parsed["promptFeedback"].(map[string]any); ok {
			if br, ok := pf["blockReason"].(string); ok && br != "" {
				msg += "\nPrompt was blocked due to : " + br
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": msg}})
		return
	}
	first, _ := candidates[0].(map[string]any)
	content := first["content"]
	if content == nil {
		content = first["output"]
	}
	hasFunctionCall, hasInlineData := false, false
	var responseText string
	if cm, ok := content.(map[string]any); ok {
		if parts, ok := cm["parts"].([]any); ok {
			var texts []string
			for _, p := range parts {
				pm, ok := p.(map[string]any)
				if !ok {
					continue
				}
				if _, ok := pm["functionCall"]; ok {
					hasFunctionCall = true
				}
				if _, ok := pm["inlineData"]; ok {
					hasInlineData = true
				}
				if _, isThought := pm["thought"]; isThought {
					continue
				}
				if t, ok := pm["text"].(string); ok {
					texts = append(texts, t)
				}
			}
			responseText = strings.Join(texts, "\n\n")
		}
	} else if s, ok := content.(string); ok {
		responseText = s
	}
	if responseText == "" && !hasFunctionCall && !hasInlineData {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": apiName + " Candidate text empty"}})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices":         []any{map[string]any{"message": map[string]any{"content": responseText}}},
		"responseContent": content,
	})
}

func vertexAccessToken(serviceAccount map[string]any) (string, error) {
	email, _ := serviceAccount["client_email"].(string)
	keyPEM, _ := serviceAccount["private_key"].(string)
	if email == "" || keyPEM == "" {
		return "", fmt.Errorf("service account missing client_email or private_key")
	}
	block, _ := pem.Decode([]byte(keyPEM))
	if block == nil {
		return "", fmt.Errorf("failed to decode private key PEM")
	}
	var priv *rsa.PrivateKey
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		var ok bool
		priv, ok = k.(*rsa.PrivateKey)
		if !ok {
			return "", fmt.Errorf("private key is not RSA")
		}
	} else if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		priv = k
	} else {
		return "", fmt.Errorf("failed to parse private key")
	}
	now := time.Now().Unix()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payloadMap := map[string]any{
		"iss":   email,
		"scope": "https://www.googleapis.com/auth/cloud-platform",
		"aud":   "https://oauth2.googleapis.com/token",
		"iat":   now,
		"exp":   now + 3600,
	}
	payloadBytes, _ := json.Marshal(payloadMap)
	payload := base64.RawURLEncoding.EncodeToString(payloadBytes)
	signingInput := header + "." + payload
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	assertion := signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")
	form.Set("assertion", assertion)
	req, err := http.NewRequest(http.MethodPost, "https://oauth2.googleapis.com/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("failed to get access token: %s", string(data))
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", err
	}
	token, _ := parsed["access_token"].(string)
	if token == "" {
		return "", fmt.Errorf("no access_token in response")
	}
	return token, nil
}

func (h *ChatHandler) sendAI21(w http.ResponseWriter, r *http.Request, body map[string]any) {
	apiKey := h.secret(r, "api_key_ai21")
	if apiKey == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":true}`))
		return
	}
	bodyParams := map[string]any{}
	if js, ok := body["json_schema"].(map[string]any); ok {
		bodyParams["response_format"] = map[string]any{"type": "json_object"}
		if msgs := bodyMessages(body); msgs != nil {
			schemaJSON, _ := json.MarshalIndent(js["value"], "", "    ")
			msgs = append(msgs, map[string]any{"role": "user", "content": "JSON schema for the response:\n" + string(schemaJSON)})
			body["messages"] = msgs
		}
	}
	converted := ConvertAI21Messages(bodyMessages(body), PromptNamesFromBody(body))
	reqBody := map[string]any{
		"messages": converted, "model": body["model"], "max_tokens": body["max_tokens"],
		"temperature": body["temperature"], "top_p": body["top_p"], "stop": body["stop"],
		"stream": body["stream"], "tools": body["tools"],
	}
	for k, v := range bodyParams {
		if v != nil {
			reqBody[k] = v
		}
	}
	compactNils(reqBody)
	h.postUpstream(w, r, body, APIAI21+"/chat/completions", map[string]string{
		"Content-Type": "application/json", "accept": "application/json", "Authorization": "Bearer " + apiKey,
	}, reqBody, "AI21")
}

func (h *ChatHandler) sendMistral(w http.ResponseWriter, r *http.Request, body map[string]any) {
	reverseProxy := bodyStr(body, "reverse_proxy")
	apiURL := APIMistral
	if reverseProxy != "" {
		apiURL = reverseProxy
	}
	var apiKey string
	if reverseProxy != "" {
		apiKey = bodyStr(body, "proxy_password")
	} else {
		apiKey = h.secret(r, "api_key_mistralai")
	}
	if apiKey == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":true}`))
		return
	}
	messages := ConvertMistralMessages(bodyMessages(body), PromptNamesFromBody(body), h.Cfg.Mistral.EnablePrefix)
	requestBody := map[string]any{
		"model": body["model"], "messages": messages,
		"temperature": body["temperature"], "top_p": body["top_p"],
		"frequency_penalty": body["frequency_penalty"], "presence_penalty": body["presence_penalty"],
		"max_tokens": body["max_tokens"], "stream": body["stream"],
		"safe_prompt": body["safe_prompt"],
	}
	if seed, ok := bodyFloat(body, "seed"); ok && seed != -1 {
		requestBody["random_seed"] = seed
	}
	if stop, ok := body["stop"].([]any); ok && len(stop) > 0 {
		requestBody["stop"] = stop
	}
	if tools, ok := body["tools"].([]any); ok && len(tools) > 0 {
		requestBody["tools"] = tools
		requestBody["tool_choice"] = body["tool_choice"]
	}
	if js, ok := body["json_schema"].(map[string]any); ok {
		requestBody["response_format"] = map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name": js["name"], "description": js["description"],
				"schema": js["value"], "strict": withDefault(js["strict"], true),
			},
		}
	}
	compactNils(requestBody)
	h.postUpstream(w, r, body, strings.TrimSuffix(apiURL, "/")+"/chat/completions", map[string]string{
		"Content-Type": "application/json", "Authorization": "Bearer " + apiKey,
	}, requestBody, "MistralAI")
}

func (h *ChatHandler) sendCohere(w http.ResponseWriter, r *http.Request, body map[string]any) {
	apiKey := h.secret(r, "api_key_cohere")
	if apiKey == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":true}`))
		return
	}
	converted := ConvertCohereMessages(bodyMessages(body), PromptNamesFromBody(body))
	chatHistory := converted
	var tools []any
	if rawTools, ok := body["tools"].([]any); ok && len(rawTools) > 0 {
		for _, t := range rawTools {
			if tm, ok := t.(map[string]any); ok {
				if fn, ok := tm["function"].(map[string]any); ok {
					if params, ok := fn["parameters"].(map[string]any); ok {
						delete(params, "$schema")
					}
				}
				tools = append(tools, tm)
			}
		}
	}
	if tools == nil {
		tools = []any{}
	}
	model := bodyStr(body, "model")
	requestBody := map[string]any{
		"stream": bodyBool(body, "stream"), "model": model, "messages": chatHistory,
		"temperature": body["temperature"], "max_tokens": body["max_tokens"],
		"k": body["top_k"], "p": body["top_p"], "seed": body["seed"],
		"stop_sequences": body["stop"], "frequency_penalty": body["frequency_penalty"],
		"presence_penalty": body["presence_penalty"], "documents": []any{}, "tools": tools,
	}
	if strings.HasSuffix(model, "08-2024") {
		requestBody["safety_mode"] = "OFF"
	}
	if js, ok := body["json_schema"].(map[string]any); ok {
		requestBody["response_format"] = map[string]any{
			"type": "json_schema", "schema": js["value"],
		}
	}
	compactNils(requestBody)
	h.postUpstream(w, r, body, APICohereV2+"/chat", map[string]string{
		"Content-Type": "application/json", "Authorization": "Bearer " + apiKey,
	}, requestBody, "Cohere")
}

func (h *ChatHandler) sendDeepSeek(w http.ResponseWriter, r *http.Request, body map[string]any) {
	reverseProxy := bodyStr(body, "reverse_proxy")
	apiURL := APIDeepSeek
	if reverseProxy != "" {
		apiURL = reverseProxy
	}
	var apiKey string
	if reverseProxy != "" {
		apiKey = bodyStr(body, "proxy_password")
	} else {
		apiKey = h.secret(r, "api_key_deepseek")
	}
	if apiKey == "" && reverseProxy == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":true}`))
		return
	}
	bodyParams := map[string]any{}
	if lp, ok := bodyFloat(body, "logprobs"); ok && lp > 0 {
		bodyParams["top_logprobs"] = lp
		bodyParams["logprobs"] = true
	}
	if tools, ok := body["tools"].([]any); ok && len(tools) > 0 {
		for _, t := range tools {
			if tm, ok := t.(map[string]any); ok {
				if fn, ok := tm["function"].(map[string]any); ok {
					if params, ok := fn["parameters"].(map[string]any); ok {
						if req, ok := params["required"].([]any); ok && len(req) == 0 {
							delete(params, "required")
						}
					}
				}
			}
		}
		bodyParams["tools"] = tools
		bodyParams["tool_choice"] = body["tool_choice"]
	}
	if js, ok := body["json_schema"].(map[string]any); ok {
		bodyParams["response_format"] = map[string]any{"type": "json_object"}
		schemaJSON, _ := json.MarshalIndent(js["value"], "", "    ")
		if msgs := bodyMessages(body); msgs != nil {
			msgs = append(msgs, map[string]any{"role": "user", "content": "JSON schema for the response:\n" + string(schemaJSON)})
			body["messages"] = msgs
		}
	}
	model := bodyStr(body, "model")
	processed := PostProcessPrompt(bodyMessages(body), ProcSemiTools, PromptNamesFromBody(body))
	if matched, _ := regexp.MatchString(`-reasoner`, model); matched {
		AddReasoningContentToToolCalls(processed)
	}
	requestBody := map[string]any{
		"messages": processed, "model": model,
		"temperature": body["temperature"], "max_tokens": body["max_tokens"],
		"stream": body["stream"], "presence_penalty": body["presence_penalty"],
		"frequency_penalty": body["frequency_penalty"], "top_p": body["top_p"],
		"stop": body["stop"], "seed": body["seed"],
	}
	for k, v := range bodyParams {
		if v != nil {
			requestBody[k] = v
		}
	}
	compactNils(requestBody)
	h.postUpstream(w, r, body, strings.TrimSuffix(apiURL, "/")+"/chat/completions", map[string]string{
		"Content-Type": "application/json", "Authorization": "Bearer " + apiKey,
	}, requestBody, "DeepSeek")
}

func (h *ChatHandler) sendXAI(w http.ResponseWriter, r *http.Request, body map[string]any) {
	reverseProxy := bodyStr(body, "reverse_proxy")
	apiURL := APIXAI
	if reverseProxy != "" {
		apiURL = reverseProxy
	}
	var apiKey string
	if reverseProxy != "" {
		apiKey = bodyStr(body, "proxy_password")
	} else {
		apiKey = h.secret(r, "api_key_xai")
	}
	if apiKey == "" && reverseProxy == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":true}`))
		return
	}
	bodyParams := map[string]any{}
	if lp, ok := bodyFloat(body, "logprobs"); ok && lp > 0 {
		bodyParams["top_logprobs"] = lp
		bodyParams["logprobs"] = true
	}
	if tools, ok := body["tools"].([]any); ok && len(tools) > 0 {
		bodyParams["tools"] = tools
		bodyParams["tool_choice"] = body["tool_choice"]
	}
	if stop, ok := body["stop"].([]any); ok && len(stop) > 0 {
		bodyParams["stop"] = stop
	}
	if re, ok := body["reasoning_effort"].(string); ok && re != "" {
		if re == "high" {
			bodyParams["reasoning_effort"] = "high"
		} else {
			bodyParams["reasoning_effort"] = "low"
		}
	}
	if js, ok := body["json_schema"].(map[string]any); ok {
		bodyParams["response_format"] = map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name": js["name"], "strict": withDefault(js["strict"], true), "schema": js["value"],
			},
		}
	}
	processed := ConvertXAIMessages(bodyMessages(body), PromptNamesFromBody(body))
	requestBody := map[string]any{
		"messages": processed, "model": body["model"],
		"temperature": body["temperature"], "max_tokens": body["max_tokens"],
		"max_completion_tokens": body["max_completion_tokens"], "stream": body["stream"],
		"presence_penalty": body["presence_penalty"], "frequency_penalty": body["frequency_penalty"],
		"top_p": body["top_p"], "seed": body["seed"], "n": body["n"],
	}
	for k, v := range bodyParams {
		if v != nil {
			requestBody[k] = v
		}
	}
	compactNils(requestBody)
	h.postUpstream(w, r, body, strings.TrimSuffix(apiURL, "/")+"/chat/completions", map[string]string{
		"Content-Type": "application/json", "Authorization": "Bearer " + apiKey,
	}, requestBody, "xAI")
}

func jsonSchemaResponseFormat(js map[string]any) map[string]any {
	return map[string]any{
		"type": "json_schema",
		"json_schema": map[string]any{
			"name": js["name"], "description": js["description"],
			"schema": js["value"], "strict": withDefault(js["strict"], true),
		},
	}
}

func (h *ChatHandler) sendAIMLAPI(w http.ResponseWriter, r *http.Request, body map[string]any) {
	apiKey := h.secret(r, "api_key_aimlapi")
	if apiKey == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":true}`))
		return
	}
	bodyParams := map[string]any{}
	if lp, ok := bodyFloat(body, "logprobs"); ok && lp > 0 {
		bodyParams["top_logprobs"] = lp
		bodyParams["logprobs"] = true
	}
	if tools, ok := body["tools"].([]any); ok && len(tools) > 0 {
		bodyParams["tools"] = tools
		bodyParams["tool_choice"] = body["tool_choice"]
	}
	if stop, ok := body["stop"].([]any); ok && len(stop) > 0 {
		bodyParams["stop"] = stop
	}
	if re, ok := body["reasoning_effort"]; ok {
		bodyParams["reasoning_effort"] = re
	}
	if js, ok := body["json_schema"].(map[string]any); ok {
		bodyParams["response_format"] = jsonSchemaResponseFormat(js)
	}
	requestBody := map[string]any{
		"messages": body["messages"], "model": body["model"],
		"temperature": body["temperature"], "max_tokens": body["max_tokens"],
		"stream": body["stream"], "presence_penalty": body["presence_penalty"],
		"frequency_penalty": body["frequency_penalty"], "top_p": body["top_p"],
		"seed": body["seed"], "n": body["n"],
	}
	for k, v := range bodyParams {
		if v != nil {
			requestBody[k] = v
		}
	}
	compactNils(requestBody)
	headers := map[string]string{"Content-Type": "application/json", "Authorization": "Bearer " + apiKey}
	for k, v := range aimlapiHeaders {
		headers[k] = v
	}
	h.postUpstream(w, r, body, APIAIMLAPI+"/chat/completions", headers, requestBody, "AI/ML API")
}

func (h *ChatHandler) sendElectronHub(w http.ResponseWriter, r *http.Request, body map[string]any) {
	apiKey := h.secret(r, "api_key_electronhub")
	if apiKey == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":true}`))
		return
	}
	bodyParams := map[string]any{}
	if bodyBool(body, "enable_web_search") {
		bodyParams["web_search"] = true
	}
	if tools, ok := body["tools"].([]any); ok && len(tools) > 0 {
		bodyParams["tools"] = tools
		bodyParams["tool_choice"] = body["tool_choice"]
	}
	if re, ok := body["reasoning_effort"]; ok {
		bodyParams["reasoning_effort"] = re
	}
	if js, ok := body["json_schema"].(map[string]any); ok {
		bodyParams["response_format"] = jsonSchemaResponseFormat(js)
	}
	model := bodyStr(body, "model")
	if matched, _ := regexp.MatchString(`^claude-`, model); matched {
		if msgs := bodyMessages(body); msgs != nil {
			if h.Cfg.Claude.EnableSystemPromptCache {
				CachingSystemPromptForOpenRouter(msgs, h.cacheTTL())
			}
			if h.cachingAtDepth() != -1 {
				CachingAtDepthForOpenRouterClaude(msgs, h.cachingAtDepth(), h.cacheTTL())
			}
		}
	}
	requestBody := map[string]any{
		"messages": body["messages"], "model": model,
		"temperature": body["temperature"], "max_tokens": body["max_tokens"],
		"stream": body["stream"], "presence_penalty": body["presence_penalty"],
		"frequency_penalty": body["frequency_penalty"], "top_p": body["top_p"],
		"top_k": body["top_k"], "logit_bias": body["logit_bias"], "seed": body["seed"],
	}
	for k, v := range bodyParams {
		if v != nil {
			requestBody[k] = v
		}
	}
	compactNils(requestBody)
	h.postUpstream(w, r, body, APIElectronHub+"/chat/completions", map[string]string{
		"Content-Type": "application/json", "Authorization": "Bearer " + apiKey,
	}, requestBody, "Electron Hub")
}

func (h *ChatHandler) sendChutes(w http.ResponseWriter, r *http.Request, body map[string]any) {
	apiKey := h.secret(r, "api_key_chutes")
	if apiKey == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":true}`))
		return
	}
	bodyParams := map[string]any{}
	if tools, ok := body["tools"].([]any); ok && len(tools) > 0 {
		bodyParams["tools"] = tools
		bodyParams["tool_choice"] = body["tool_choice"]
	}
	if lp, ok := bodyFloat(body, "logprobs"); ok && lp > 0 {
		bodyParams["top_logprobs"] = lp
		bodyParams["logprobs"] = true
	}
	if js, ok := body["json_schema"].(map[string]any); ok {
		bodyParams["response_format"] = jsonSchemaResponseFormat(js)
	}
	requestBody := map[string]any{
		"messages": body["messages"], "model": body["model"],
		"temperature": body["temperature"], "max_tokens": body["max_tokens"],
		"stream": body["stream"], "presence_penalty": body["presence_penalty"],
		"frequency_penalty": body["frequency_penalty"], "repetition_penalty": body["repetition_penalty"],
		"min_p": body["min_p"], "top_p": body["top_p"], "top_k": body["top_k"],
		"seed": body["seed"], "stop": body["stop"],
		"reasoning_effort": body["reasoning_effort"], "logit_bias": body["logit_bias"],
	}
	for k, v := range bodyParams {
		if v != nil {
			requestBody[k] = v
		}
	}
	compactNils(requestBody)
	h.postUpstream(w, r, body, APIChutes+"/chat/completions", map[string]string{
		"Content-Type": "application/json", "Authorization": "Bearer " + apiKey,
	}, requestBody, "Chutes")
}

var azureOpenAIKeys = []string{
	"messages", "temperature", "frequency_penalty", "presence_penalty", "top_p",
	"max_tokens", "max_completion_tokens", "stream", "logit_bias", "stop",
	"n", "logprobs", "seed", "tools", "tool_choice", "reasoning_effort",
}

func (h *ChatHandler) sendAzure(w http.ResponseWriter, r *http.Request, body map[string]any) {
	baseURL := bodyStr(body, "azure_base_url")
	deployment := bodyStr(body, "azure_deployment_name")
	apiVersion := bodyStr(body, "azure_api_version")
	apiKey := h.secret(r, "api_key_azure_openai")
	if baseURL == "" || deployment == "" || apiVersion == "" || apiKey == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
			"message": "Azure OpenAI configuration is incomplete. Please provide Base URL, Deployment Name, API Version, and API Key in the connection settings.",
		}})
		return
	}
	u, err := url.Parse(strings.TrimSuffix(baseURL, "/") + "/openai/deployments/" + deployment + "/chat/completions")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":true}`))
		return
	}
	q := u.Query()
	q.Set("api-version", apiVersion)
	u.RawQuery = q.Encode()
	apiRequestBody := map[string]any{}
	for _, k := range azureOpenAIKeys {
		if v, ok := body[k]; ok {
			apiRequestBody[k] = v
		}
	}
	if js, ok := body["json_schema"].(map[string]any); ok {
		apiRequestBody["response_format"] = map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name": js["name"], "strict": withDefault(js["strict"], true), "schema": js["value"],
			},
		}
	}
	if lp, ok := apiRequestBody["logprobs"].(float64); ok && lp > 0 {
		apiRequestBody["top_logprobs"] = lp
		apiRequestBody["logprobs"] = true
	}
	model := bodyStr(body, "model")
	matchedEffort := false
	for _, m := range openaiReasoningEffortModels {
		if m == model {
			matchedEffort = true
			break
		}
	}
	if matchedEffort {
		if fixed, ok := openaiFixedReasoningEffort[model]; ok {
			apiRequestBody["reasoning_effort"] = fixed
		} else if rs, ok := body["reasoning_effort"].(string); ok {
			if mapped, ok := openaiReasoningEffortMap[rs]; ok {
				apiRequestBody["reasoning_effort"] = mapped
			} else {
				apiRequestBody["reasoning_effort"] = body["reasoning_effort"]
			}
		} else {
			apiRequestBody["reasoning_effort"] = body["reasoning_effort"]
		}
	} else {
		delete(apiRequestBody, "reasoning_effort")
	}
	compactNils(apiRequestBody)
	h.postUpstream(w, r, body, u.String(), map[string]string{
		"Content-Type": "application/json", "api-key": apiKey,
	}, apiRequestBody, "Azure OpenAI")
}

func (h *ChatHandler) postUpstream(w http.ResponseWriter, r *http.Request, body map[string]any, endpoint string, headers map[string]string, requestBody map[string]any, provider string) {
	if bodyBool(body, "stream") {
		upstream, err := DoStream(h.client, r.Context(), http.MethodPost, endpoint, headers, requestBody)
		if err != nil {
			writeUpstreamError(w, http.StatusBadGateway, connectErrorMessage(err), false)
			return
		}
		ForwardStream(upstream, w, r)
		return
	}
	res, err := DoJSON(h.client, r.Context(), http.MethodPost, endpoint, headers, requestBody)
	if err != nil {
		writeUpstreamError(w, http.StatusBadGateway, connectErrorMessage(err), false)
		return
	}
	h.finishNonStream(w, res, provider)
}
