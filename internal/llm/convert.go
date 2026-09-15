package llm

import (
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

const PromptPlaceholder = "[Start a new chat]"

const (
	ProcNone        = ""
	ProcClaude      = "claude"
	ProcMerge       = "merge"
	ProcMergeTools  = "merge_tools"
	ProcSemi        = "semi"
	ProcSemiTools   = "semi_tools"
	ProcStrict      = "strict"
	ProcStrictTools = "strict_tools"
	ProcSingle      = "single"
)

type PromptNames struct {
	CharName   string
	UserName   string
	GroupNames []string
}

func PromptNamesFromBody(body map[string]any) PromptNames {
	strVal := func(v any) string {
		if s, ok := v.(string); ok {
			return s
		}
		return ""
	}
	var groups []string
	if g, ok := body["group_names"].([]any); ok {
		for _, n := range g {
			groups = append(groups, strVal(n))
		}
	}
	return PromptNames{
		CharName:   strVal(body["char_name"]),
		UserName:   strVal(body["user_name"]),
		GroupNames: groups,
	}
}

func (n PromptNames) StartsWithGroupName(message string) bool {
	for _, name := range n.GroupNames {
		if strings.HasPrefix(message, name+": ") {
			return true
		}
	}
	return false
}

func msgString(m map[string]any, key string) string {
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

func msgName(m map[string]any) string { return msgString(m, "name") }

func toContentString(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, p := range c {
			if pm, ok := p.(map[string]any); ok {
				if t, ok := pm["text"].(string); ok {
					parts = append(parts, t)
				}
			} else if s, ok := p.(string); ok {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "\n\n")
	default:
		return ""
	}
}

func randB64(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)
}

func AddAssistantPrefix(prompt []any, tools []any, property string) []any {
	if len(prompt) == 0 {
		return prompt
	}
	hasTools := len(tools) > 0
	if !hasTools {
		for _, p := range prompt {
			if pm, ok := p.(map[string]any); ok && msgString(pm, "role") == "tool" {
				hasTools = true
				break
			}
		}
	}
	if last, ok := prompt[len(prompt)-1].(map[string]any); ok {
		if !hasTools && msgString(last, "role") == "assistant" {
			last[property] = true
		}
	}
	return prompt
}

type mergeOptions struct {
	strict       bool
	placeholders bool
	single       bool
	tools        bool
}

func PostProcessPrompt(messages []any, procType string, names PromptNames) []any {
	switch procType {
	case ProcMerge, ProcClaude:
		return MergeMessages(messages, names, mergeOptions{})
	case ProcMergeTools:
		return MergeMessages(messages, names, mergeOptions{tools: true})
	case ProcSemi:
		return MergeMessages(messages, names, mergeOptions{strict: true})
	case ProcSemiTools:
		return MergeMessages(messages, names, mergeOptions{strict: true, tools: true})
	case ProcStrict:
		return MergeMessages(messages, names, mergeOptions{strict: true, placeholders: true})
	case ProcStrictTools:
		return MergeMessages(messages, names, mergeOptions{strict: true, placeholders: true, tools: true})
	case ProcSingle:
		return MergeMessages(messages, names, mergeOptions{strict: true, single: true})
	default:
		return messages
	}
}

func MergeMessages(messages []any, names PromptNames, opts mergeOptions) []any {
	contentTokens := make(map[string]any)
	for _, m := range messages {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if msg["content"] == nil {
			msg["content"] = ""
		}
		if arr, ok := msg["content"].([]any); ok {
			var texts []string
			for _, c := range arr {
				cm, ok := c.(map[string]any)
				if !ok {
					continue
				}
				switch cm["type"] {
				case "text":
					if t, ok := cm["text"].(string); ok {
						texts = append(texts, t)
					}
				case "image_url", "video_url", "audio_url":
					token := randB64(32)
					contentTokens[token] = cm
					texts = append(texts, token)
				}
			}
			msg["content"] = strings.Join(texts, "\n\n")
		}
		content := toContentString(msg["content"])
		role := msgString(msg, "role")
		name := msgName(msg)
		if role == "system" && name == "example_assistant" {
			if names.CharName != "" && !strings.HasPrefix(content, names.CharName+": ") && !names.StartsWithGroupName(content) {
				content = names.CharName + ": " + content
			}
		}
		if role == "system" && name == "example_user" {
			if names.UserName != "" && !strings.HasPrefix(content, names.UserName+": ") {
				content = names.UserName + ": " + content
			}
		}
		if name != "" && role != "system" {
			if !strings.HasPrefix(content, name+": ") {
				content = name + ": " + content
			}
		}
		if role == "tool" && !opts.tools {
			msg["role"] = "user"
			role = "user"
		}
		if opts.single {
			if role == "assistant" {
				if names.CharName != "" && !strings.HasPrefix(content, names.CharName+": ") && !names.StartsWithGroupName(content) {
					content = names.CharName + ": " + content
				}
			}
			if role == "user" {
				if names.UserName != "" && !strings.HasPrefix(content, names.UserName+": ") {
					content = names.UserName + ": " + content
				}
			}
			msg["role"] = "user"
		}
		msg["content"] = content
		delete(msg, "name")
		if !opts.tools {
			delete(msg, "tool_calls")
			delete(msg, "tool_call_id")
		}
	}

	var merged []any
	for _, m := range messages {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		content := toContentString(msg["content"])
		role := msgString(msg, "role")
		if len(merged) > 0 {
			if last, ok := merged[len(merged)-1].(map[string]any); ok &&
				msgString(last, "role") == role && content != "" && role != "tool" {
				last["content"] = toContentString(last["content"]) + "\n\n" + content
				continue
			}
		}
		merged = append(merged, msg)
	}

	if len(merged) == 0 {
		merged = append(merged, map[string]any{"role": "user", "content": PromptPlaceholder})
	}

	if len(contentTokens) > 0 {
		for _, m := range merged {
			msg, ok := m.(map[string]any)
			if !ok {
				continue
			}
			content := toContentString(msg["content"])
			hit := false
			for token := range contentTokens {
				if strings.Contains(content, token) {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
			var rebuilt []any
			for _, part := range strings.Split(content, "\n\n") {
				if obj, ok := contentTokens[part]; ok {
					rebuilt = append(rebuilt, obj)
					continue
				}
				if len(rebuilt) > 0 {
					if last, ok := rebuilt[len(rebuilt)-1].(map[string]any); ok && last["type"] == "text" {
						last["text"] = toContentString(last["text"]) + "\n\n" + part
						continue
					}
				}
				rebuilt = append(rebuilt, map[string]any{"type": "text", "text": part})
			}
			msg["content"] = rebuilt
		}
	}

	if opts.strict {
		for i := range merged {
			if mm, ok := merged[i].(map[string]any); ok {
				if i > 0 && msgString(mm, "role") == "system" {
					mm["role"] = "user"
				}
			}
		}
		if opts.placeholders && len(merged) > 0 {
			first, _ := merged[0].(map[string]any)
			if first != nil {
				if msgString(first, "role") == "system" {
					if len(merged) == 1 {
						merged = append(merged[:1], append([]any{map[string]any{"role": "user", "content": PromptPlaceholder}}, merged[1:]...)...)
					} else if second, ok := merged[1].(map[string]any); !ok || msgString(second, "role") != "user" {
						merged = append(merged[:1], append([]any{map[string]any{"role": "user", "content": PromptPlaceholder}}, merged[1:]...)...)
					}
				} else if r := msgString(first, "role"); r != "system" && r != "user" {
					merged = append([]any{map[string]any{"role": "user", "content": PromptPlaceholder}}, merged...)
				}
			}
		}
		return MergeMessages(merged, names, mergeOptions{placeholders: opts.placeholders, tools: opts.tools})
	}

	return merged
}

func ConvertClaudeMessages(messages []any, prefillString string, useSysPrompt, useTools bool, names PromptNames) ([]any, []any) {
	var systemPrompt []any
	msgs := append([]any{}, messages...)
	toMapSlice := func(v []any) []map[string]any {
		var out []map[string]any
		for _, m := range v {
			if mm, ok := m.(map[string]any); ok {
				out = append(out, mm)
			}
		}
		return out
	}
	_ = toMapSlice
	if useSysPrompt {
		var i int
		for i = 0; i < len(msgs); i++ {
			mm, ok := msgs[i].(map[string]any)
			if !ok || msgString(mm, "role") != "system" {
				break
			}
			if names.UserName != "" && msgName(mm) == "example_user" {
				if c := toContentString(mm["content"]); !strings.HasPrefix(c, names.UserName+": ") {
					mm["content"] = names.UserName + ": " + c
				}
			}
			if names.CharName != "" && msgName(mm) == "example_assistant" {
				if c := toContentString(mm["content"]); !strings.HasPrefix(c, names.CharName+": ") && !names.StartsWithGroupName(c) {
					mm["content"] = names.CharName + ": " + c
				}
			}
			systemPrompt = append(systemPrompt, map[string]any{"type": "text", "text": toContentString(mm["content"])})
		}
		msgs = msgs[i:]
		if len(msgs) == 0 {
			msgs = append(msgs, map[string]any{"role": "user", "content": PromptPlaceholder})
		}
	}

	parseJSON := func(s string) any {
		var v any
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			return s
		}
		return v
	}

	for _, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if msgString(msg, "role") == "assistant" {
			if tc, ok := msg["tool_calls"].([]any); ok {
				var converted []any
				for _, t := range tc {
					tm, ok := t.(map[string]any)
					if !ok {
						continue
					}
					fn, _ := tm["function"].(map[string]any)
					args := ""
					if fn != nil {
						if a, ok := fn["arguments"]; ok {
							switch av := a.(type) {
							case string:
								args = av
							default:
								if b, err := json.Marshal(av); err == nil {
									args = string(b)
								}
							}
						}
					}
					id, _ := tm["id"].(string)
					name := ""
					if fn != nil {
						name, _ = fn["name"].(string)
					}
					converted = append(converted, map[string]any{
						"type": "tool_use", "id": id, "name": name, "input": parseJSON(args),
					})
				}
				msg["content"] = converted
			}
		}
		if msgString(msg, "role") == "tool" {
			msg["role"] = "user"
			msg["content"] = []any{map[string]any{
				"type": "tool_result", "tool_use_id": msgString(msg, "tool_call_id"), "content": toContentString(msg["content"]),
			}}
		}
		if msgString(msg, "role") == "system" {
			if names.UserName != "" && msgName(msg) == "example_user" {
				if c := toContentString(msg["content"]); !strings.HasPrefix(c, names.UserName+": ") {
					msg["content"] = names.UserName + ": " + c
				}
			}
			if names.CharName != "" && msgName(msg) == "example_assistant" {
				if c := toContentString(msg["content"]); !strings.HasPrefix(c, names.CharName+": ") && !names.StartsWithGroupName(c) {
					msg["content"] = names.CharName + ": " + c
				}
			}
			msg["role"] = "user"
			delete(msg, "name")
		}
		if s, ok := msg["content"].(string); ok {
			if n := msgName(msg); n != "" {
				s = n + ": " + s
			}
			msg["content"] = []any{map[string]any{"type": "text", "text": s}}
		} else if arr, ok := msg["content"].([]any); ok {
			var converted []any
			for _, c := range arr {
				cm, ok := c.(map[string]any)
				if !ok {
					continue
				}
				switch cm["type"] {
				case "image_url":
					iu, iuOk := cm["image_url"].(map[string]any)
					if !iuOk {
						continue
					}
					urlStr, _ := iu["url"].(string)
					parts := strings.SplitN(urlStr, ",", 2)
					header, data := "", ""
					if len(parts) == 2 {
						header, data = parts[0], parts[1]
					}
					mime := ""
					if h := strings.Split(header, ";"); len(h) > 0 {
						mime = strings.TrimPrefix(h[0], "data:")
					}
					converted = append(converted, map[string]any{
						"type":   "image",
						"source": map[string]any{"type": "base64", "media_type": mime, "data": data},
					})
				case "text":
					text := toContentString(cm["text"])
					if n := msgName(msg); n != "" {
						text = n + ": " + text
					}
					if text == "" {
						text = "​"
					}
					converted = append(converted, map[string]any{"type": "text", "text": text})
				default:
					converted = append(converted, cm)
				}
			}
			msg["content"] = converted
		}
		delete(msg, "name")
		delete(msg, "tool_calls")
		delete(msg, "tool_call_id")
	}

	for i := range msgs {
		mm, ok := msgs[i].(map[string]any)
		if !ok || msgString(mm, "role") != "assistant" {
			continue
		}
		arr, ok := mm["content"].([]any)
		if !ok {
			continue
		}
		hasImage := false
		for _, c := range arr {
			if cm, ok := c.(map[string]any); ok && cm["type"] == "image" {
				hasImage = true
				break
			}
		}
		if !hasImage {
			continue
		}
		j := i + 1
		for j < len(msgs) {
			if jm, ok := msgs[j].(map[string]any); ok && msgString(jm, "role") == "user" {
				break
			}
			j++
		}
		if j >= len(msgs) {
			msgs = append(msgs[:i+1], append([]any{map[string]any{"role": "user", "content": []any{}}}, msgs[i+1:]...)...)
		}
		target, ok := msgs[j].(map[string]any)
		if !ok {
			continue
		}
		tarr, _ := target["content"].([]any)
		var kept []any
		for _, c := range arr {
			if cm, ok := c.(map[string]any); ok && cm["type"] == "image" {
				tarr = append(tarr, cm)
			} else {
				kept = append(kept, c)
			}
		}
		target["content"] = tarr
		mm["content"] = kept
	}

	if prefillString != "" {
		msgs = append(msgs, map[string]any{
			"role":    "assistant",
			"content": []any{map[string]any{"type": "text", "text": strings.TrimRightFunc(prefillString, unicode.IsSpace)}},
		})
	}

	var merged []any
	for _, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if len(merged) > 0 {
			if last, ok := merged[len(merged)-1].(map[string]any); ok && msgString(last, "role") == msgString(mm, "role") {
				lc, _ := last["content"].([]any)
				mc, _ := mm["content"].([]any)
				last["content"] = append(lc, mc...)
				continue
			}
		}
		merged = append(merged, mm)
	}

	if !useTools {
		for _, m := range merged {
			mm, ok := m.(map[string]any)
			if !ok {
				continue
			}
			arr, ok := mm["content"].([]any)
			if !ok {
				continue
			}
			for _, c := range arr {
				cm, ok := c.(map[string]any)
				if !ok {
					continue
				}
				switch cm["type"] {
				case "tool_use":
					b, _ := json.Marshal(cm["input"])
					cm["type"] = "text"
					cm["text"] = string(b)
					delete(cm, "id")
					delete(cm, "name")
					delete(cm, "input")
				case "tool_result":
					cm["type"] = "text"
					cm["text"] = toContentString(cm["content"])
					delete(cm, "tool_use_id")
					delete(cm, "content")
				}
			}
		}
	}

	return merged, systemPrompt
}

func ConvertCohereMessages(messages []any, names PromptNames) []any {
	msgs := append([]any{}, messages...)
	if len(msgs) == 0 {
		msgs = append(msgs, map[string]any{"role": "user", "content": PromptPlaceholder})
	}
	for i, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if tc, ok := msg["tool_calls"].([]any); ok {
			if i > 0 {
				if prev, ok := msgs[i-1].(map[string]any); ok && msgString(prev, "role") == "assistant" {
					msg["content"] = toContentString(prev["content"])
					msgs = append(msgs[:i-1], msgs[i:]...)
				} else {
					var toolNames []string
					for _, t := range tc {
						if tm, ok := t.(map[string]any); ok {
							if fn, ok := tm["function"].(map[string]any); ok {
								if n, ok := fn["name"].(string); ok {
									toolNames = append(toolNames, n)
								}
							}
						}
					}
					msg["content"] = "I'm going to call a tool for that: " + strings.Join(toolNames, ", ")
				}
			} else {
				var toolNames []string
				for _, t := range tc {
					if tm, ok := t.(map[string]any); ok {
						if fn, ok := tm["function"].(map[string]any); ok {
							if n, ok := fn["name"].(string); ok {
								toolNames = append(toolNames, n)
							}
						}
					}
				}
				msg["content"] = "I'm going to call a tool for that: " + strings.Join(toolNames, ", ")
			}
		}
		if n := msgName(msg); n != "" {
			content := toContentString(msg["content"])
			role := msgString(msg, "role")
			if role == "system" && n == "example_assistant" {
				if names.CharName != "" && !strings.HasPrefix(content, names.CharName+": ") && !names.StartsWithGroupName(content) {
					content = names.CharName + ": " + content
				}
			} else if role == "system" && n == "example_user" {
				if names.UserName != "" && !strings.HasPrefix(content, names.UserName+": ") {
					content = names.UserName + ": " + content
				}
			} else if role != "system" && !strings.HasPrefix(content, n+": ") {
				content = n + ": " + content
			}
			msg["content"] = content
			delete(msg, "name")
		}
	}
	return msgs
}

var geminiMediaResolution = map[string]string{
	"low":  "media_resolution_low",
	"high": "media_resolution_high",
}

var gemini3Re = regexp.MustCompile(`gemini-3`)
var gemini25Re = regexp.MustCompile(`gemini-2\.5`)

func tryParseJSON(s string) any {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil
	}
	return v
}

func ConvertGooglePrompt(messages []any, model string, useSysPrompt bool, names PromptNames, thoughtSignatures bool) ([]any, map[string]any) {
	msgs := append([]any{}, messages...)
	var sysPrompt []string
	if useSysPrompt {
		for len(msgs) > 1 {
			first, ok := msgs[0].(map[string]any)
			if !ok || msgString(first, "role") != "system" {
				break
			}
			if names.UserName != "" && msgName(first) == "example_user" {
				if c := toContentString(first["content"]); !strings.HasPrefix(c, names.UserName+": ") {
					first["content"] = names.UserName + ": " + c
				}
			}
			if names.CharName != "" && msgName(first) == "example_assistant" {
				if c := toContentString(first["content"]); !strings.HasPrefix(c, names.CharName+": ") && !names.StartsWithGroupName(c) {
					first["content"] = names.CharName + ": " + c
				}
			}
			sysPrompt = append(sysPrompt, toContentString(first["content"]))
			msgs = msgs[1:]
		}
	}
	var sysParts []any
	for _, t := range sysPrompt {
		sysParts = append(sysParts, map[string]any{"text": t})
	}
	systemInstruction := map[string]any{"parts": sysParts}
	toolNameMap := map[string]string{}
	var contents []any

	for _, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		switch msgString(msg, "role") {
		case "system", "tool":
			msg["role"] = "user"
		case "assistant":
			msg["role"] = "model"
		}
		if _, ok := msg["content"].([]any); !ok {
			var wrapped map[string]any
			hasToolCalls := false
			if tc, ok := msg["tool_calls"].([]any); ok && len(tc) > 0 {
				hasToolCalls = true
				_ = tc
				wrapped = map[string]any{"type": "tool_calls", "tool_calls": msg["tool_calls"]}
			} else if tcid, ok := msg["tool_call_id"].(string); ok && tcid != "" {
				wrapped = map[string]any{"type": "tool_call_id", "tool_call_id": tcid, "content": fmt.Sprint(msg["content"])}
			} else {
				wrapped = map[string]any{"type": "text", "text": fmt.Sprint(msg["content"])}
			}
			_ = hasToolCalls
			msg["content"] = []any{wrapped}
		}
		if n := msgName(msg); n != "" {
			if arr, ok := msg["content"].([]any); ok {
				for _, c := range arr {
					cm, ok := c.(map[string]any)
					if !ok || cm["type"] != "text" {
						continue
					}
					text := toContentString(cm["text"])
					switch {
					case n == "example_user":
						if names.UserName != "" && !strings.HasPrefix(text, names.UserName+": ") {
							cm["text"] = names.UserName + ": " + text
						}
					case n == "example_assistant":
						if names.CharName != "" && !strings.HasPrefix(text, names.CharName+": ") && !names.StartsWithGroupName(text) {
							cm["text"] = names.CharName + ": " + text
						}
					default:
						if !strings.HasPrefix(text, n+": ") {
							cm["text"] = n + ": " + text
						}
					}
				}
			}
			delete(msg, "name")
		}
		var parts []any
		addDataURLPart := func(urlStr, defaultMime, detail string) {
			if urlStr == "" || !strings.HasPrefix(urlStr, "data:") {
				return
			}
			comma := strings.Index(urlStr, ",")
			if comma < 0 {
				return
			}
			header, b64 := urlStr[:comma], urlStr[comma+1:]
			mime := defaultMime
			if h := strings.Split(header, ";"); len(h) > 0 {
				mime = strings.TrimPrefix(h[0], "data:")
			}
			part := map[string]any{"inlineData": map[string]any{"mimeType": mime, "data": b64}}
			if mr, ok := geminiMediaResolution[detail]; ok && mr != "" && gemini3Re.MatchString(model) {
				part["mediaResolution"] = map[string]any{"level": mr}
			}
			parts = append(parts, part)
		}
		if arr, ok := msg["content"].([]any); ok {
			for _, c := range arr {
				cm, ok := c.(map[string]any)
				if !ok {
					continue
				}
				switch cm["type"] {
				case "text":
					parts = append(parts, map[string]any{"text": toContentString(cm["text"])})
				case "tool_call_id":
					tcid, _ := cm["tool_call_id"].(string)
					tname := toolNameMap[tcid]
					if tname == "" {
						tname = "unknown"
					}
					parts = append(parts, map[string]any{"functionResponse": map[string]any{
						"name":     tname,
						"response": map[string]any{"name": tname, "content": toContentString(cm["content"])},
					}})
				case "tool_calls":
					if tc, ok := cm["tool_calls"].([]any); ok {
						for _, t := range tc {
							tm, ok := t.(map[string]any)
							if !ok {
								continue
							}
							fn, _ := tm["function"].(map[string]any)
							var args any
							var fnName any
							if fn != nil {
								args = fn["arguments"]
								fnName = fn["name"]
							}
							if s, ok := args.(string); ok {
								if parsed := tryParseJSON(s); parsed != nil {
									args = parsed
								}
							}
							fc := map[string]any{"name": fnName, "args": args}
							part := map[string]any{"functionCall": fc}
							if sig, ok := tm["signature"].(string); ok && sig != "" {
								part["thoughtSignature"] = sig
							}
							parts = append(parts, part)
							if id, ok := tm["id"].(string); ok {
								if fnName, ok := fn["name"].(string); ok {
									toolNameMap[id] = fnName
								}
							}
						}
					}
				case "image_url":
					iu, iuOk := cm["image_url"].(map[string]any)
					if !iuOk {
						continue
					}
					urlStr, _ := iu["url"].(string)
					detail, _ := iu["detail"].(string)
					addDataURLPart(urlStr, "image/png", detail)
				case "video_url":
					vu, vuOk := cm["video_url"].(map[string]any)
					if !vuOk {
						continue
					}
					urlStr, _ := vu["url"].(string)
					detail, _ := vu["detail"].(string)
					addDataURLPart(urlStr, "video/mp4", detail)
				case "audio_url":
					au, auOk := cm["audio_url"].(map[string]any)
					if !auOk {
						continue
					}
					urlStr, _ := au["url"].(string)
					addDataURLPart(urlStr, "audio/mpeg", "")
				}
			}
		}
		if gemini3Re.MatchString(model) || gemini25Re.MatchString(model) {
			sig, _ := msg["signature"].(string)
			for _, p := range parts {
				pm, ok := p.(map[string]any)
				if !ok {
					continue
				}
				if thoughtSignatures && sig != "" {
					if _, ok := pm["text"].(string); ok {
						pm["thoughtSignature"] = sig
					}
				} else if gemini3Re.MatchString(model) {
					if _, ok := pm["functionCall"]; ok {
						if _, has := pm["thoughtSignature"]; !has {
							pm["thoughtSignature"] = "skip_thought_signature_validator"
						}
					}
					if strings.Contains(model, "-image") && msgString(msg, "role") == "model" {
						if _, ok := pm["text"].(string); ok {
							pm["thoughtSignature"] = "skip_thought_signature_validator"
						} else if _, ok := pm["inlineData"]; ok {
							pm["thoughtSignature"] = "skip_thought_signature_validator"
						}
					}
				}
			}
		}
		if len(contents) > 0 {
			if last, ok := contents[len(contents)-1].(map[string]any); ok && msgString(last, "role") == msgString(msg, "role") {
				lparts, _ := last["parts"].([]any)
				for _, p := range parts {
					pm, _ := p.(map[string]any)
					if t, ok := pm["text"].(string); ok && t != "" {
						merged := false
						for _, lp := range lparts {
							if lpm, ok := lp.(map[string]any); ok {
								if lt, ok := lpm["text"].(string); ok {
									lpm["text"] = lt + "\n\n" + t
									merged = true
									break
								}
							}
						}
						if !merged {
							lparts = append(lparts, p)
						}
					} else if pm["inlineData"] != nil || pm["functionCall"] != nil || pm["functionResponse"] != nil || pm["thoughtSignature"] != nil || pm["mediaResolution"] != nil {
						lparts = append(lparts, p)
					}
				}
				last["parts"] = lparts
				continue
			}
		}
		contents = append(contents, map[string]any{"role": msgString(msg, "role"), "parts": parts})
	}
	return contents, systemInstruction
}

func ConvertAI21Messages(messages []any, names PromptNames) []any {
	msgs := append([]any{}, messages...)
	var i int
	var systemPrompt strings.Builder
	for i = 0; i < len(msgs); i++ {
		mm, ok := msgs[i].(map[string]any)
		if !ok || msgString(mm, "role") != "system" {
			break
		}
		if names.UserName != "" && msgName(mm) == "example_user" {
			if c := toContentString(mm["content"]); !strings.HasPrefix(c, names.UserName+": ") {
				mm["content"] = names.UserName + ": " + c
			}
		}
		if names.CharName != "" && msgName(mm) == "example_assistant" {
			if c := toContentString(mm["content"]); !strings.HasPrefix(c, names.CharName+": ") && !names.StartsWithGroupName(c) {
				mm["content"] = names.CharName + ": " + c
			}
		}
		systemPrompt.WriteString(toContentString(mm["content"]))
		systemPrompt.WriteString("\n\n")
	}
	msgs = msgs[i:]
	if len(msgs) == 0 {
		msgs = append(msgs, map[string]any{"role": "user", "content": PromptPlaceholder})
	}
	if sp := strings.TrimSpace(systemPrompt.String()); sp != "" {
		msgs = append([]any{map[string]any{"role": "system", "content": sp}}, msgs...)
	}
	for _, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if n, has := mm["name"]; has {
			name, _ := n.(string)
			if msgString(mm, "role") != "system" {
				if c := toContentString(mm["content"]); !strings.HasPrefix(c, name+": ") {
					mm["content"] = name + ": " + c
				}
			}
			delete(mm, "name")
		}
	}
	var merged []any
	for _, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if len(merged) > 0 {
			if last, ok := merged[len(merged)-1].(map[string]any); ok && msgString(last, "role") == msgString(mm, "role") {
				last["content"] = toContentString(last["content"]) + "\n\n" + toContentString(mm["content"])
				continue
			}
		}
		merged = append(merged, mm)
	}
	return merged
}

func sha512Hex9(id string) string {
	sum := sha512.Sum512([]byte(id))
	hexStr := fmt.Sprintf("%x", sum)
	if len(hexStr) > 9 {
		return hexStr[:9]
	}
	return hexStr
}

func ConvertMistralMessages(messages []any, names PromptNames, prefixEnabled bool) []any {
	msgs := append([]any{}, messages...)
	if prefixEnabled && len(msgs) > 0 {
		if last, ok := msgs[len(msgs)-1].(map[string]any); ok && msgString(last, "role") == "assistant" {
			last["prefix"] = true
		}
	}
	for _, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if tc, ok := msg["tool_calls"].([]any); ok {
			for _, t := range tc {
				if tm, ok := t.(map[string]any); ok {
					if id, ok := tm["id"].(string); ok {
						tm["id"] = sha512Hex9(id)
					}
				}
			}
		}
		if tcid, ok := msg["tool_call_id"].(string); ok && msgString(msg, "role") == "tool" && tcid != "" {
			msg["tool_call_id"] = sha512Hex9(tcid)
		}
		if msgString(msg, "role") == "system" && msgName(msg) == "example_assistant" {
			if names.CharName != "" {
				if c := toContentString(msg["content"]); !strings.HasPrefix(c, names.CharName+": ") && !names.StartsWithGroupName(c) {
					msg["content"] = names.CharName + ": " + c
				}
			}
			delete(msg, "name")
		}
		if msgString(msg, "role") == "system" && msgName(msg) == "example_user" {
			if names.UserName != "" {
				if c := toContentString(msg["content"]); !strings.HasPrefix(c, names.UserName+": ") {
					msg["content"] = names.UserName + ": " + c
				}
			}
			delete(msg, "name")
		}
		if n := msgName(msg); n != "" && msgString(msg, "role") != "system" {
			if c := toContentString(msg["content"]); !strings.HasPrefix(c, n+": ") {
				msg["content"] = n + ": " + c
			}
			delete(msg, "name")
		}
	}
	for {
		rerun := false
		for i := 0; i < len(msgs); i++ {
			mm, ok := msgs[i].(map[string]any)
			if !ok || i == len(msgs)-1 {
				continue
			}
			nm, ok := msgs[i+1].(map[string]any)
			if !ok {
				continue
			}
			if msgString(mm, "role") == "tool" && msgString(nm, "role") == "user" {
				lastUser := -1
				for k := 0; k < i; k++ {
					if km, ok := msgs[k].(map[string]any); ok && msgString(km, "role") == "user" && toContentString(km["content"]) != "" {
						lastUser = k
					}
				}
				if lastUser != -1 {
					lm, ok := msgs[lastUser].(map[string]any)
					if !ok {
						continue
					}
					lm["content"] = toContentString(lm["content"]) + "\n\n" + toContentString(nm["content"])
					msgs = append(msgs[:i+1], msgs[i+2:]...)
					rerun = true
					break
				}
			}
		}
		if !rerun {
			break
		}
	}
	for i := 0; i < len(msgs)-1; i++ {
		a, ok1 := msgs[i].(map[string]any)
		b, ok2 := msgs[i+1].(map[string]any)
		if ok1 && ok2 && msgString(a, "role") == "assistant" && msgString(b, "role") == "system" {
			b["role"] = "user"
		}
	}
	return msgs
}

func ConvertXAIMessages(messages []any, names PromptNames) []any {
	msgs := append([]any{}, messages...)
	for _, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		n := msgName(msg)
		if n == "" || msgString(msg, "role") == "user" {
			continue
		}
		content := toContentString(msg["content"])
		role := msgString(msg, "role")
		applied := false
		switch {
		case role == "assistant" && names.CharName != "" && !strings.HasPrefix(content, names.CharName+": ") && !names.StartsWithGroupName(content):
			msg["content"] = names.CharName + ": " + content
			applied = true
		case role == "system" && n == "example_assistant" && names.CharName != "" && !strings.HasPrefix(content, names.CharName+": ") && !names.StartsWithGroupName(content):
			msg["content"] = names.CharName + ": " + content
			applied = true
		case role == "system" && n == "example_user" && names.UserName != "" && !strings.HasPrefix(content, names.UserName+": "):
			msg["content"] = names.UserName + ": " + content
			applied = true
		}
		_ = applied
		delete(msg, "name")
	}
	return msgs
}

func ConvertTextCompletionPrompt(messages any) string {
	if s, ok := messages.(string); ok {
		return s
	}
	arr, ok := messages.([]any)
	if !ok {
		return ""
	}
	var lines []string
	for _, m := range arr {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		role := msgString(mm, "role")
		content := toContentString(mm["content"])
		if role == "system" {
			if n := msgName(mm); n != "" {
				lines = append(lines, n+": "+content)
			} else {
				lines = append(lines, "System: "+content)
			}
		} else {
			lines = append(lines, role+": "+content)
		}
	}
	return strings.Join(lines, "\n") + "\nassistant:"
}

func CachingAtDepthForClaude(messages []any, cachingAtDepth int, ttl string) {
	passedPrefill := false
	depth := 0
	previousRole := ""
	for i := len(messages) - 1; i >= 0; i-- {
		mm, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		if !passedPrefill && msgString(mm, "role") == "assistant" {
			continue
		}
		passedPrefill = true
		if msgString(mm, "role") != previousRole {
			if depth == cachingAtDepth || depth == cachingAtDepth+2 {
				if arr, ok := mm["content"].([]any); ok && len(arr) > 0 {
					if last, ok := arr[len(arr)-1].(map[string]any); ok {
						last["cache_control"] = map[string]any{"type": "ephemeral", "ttl": ttl}
					}
				}
			}
			if depth == cachingAtDepth+2 {
				break
			}
			depth++
			previousRole = msgString(mm, "role")
		}
	}
}

func CachingAtDepthForOpenRouterClaude(messages []any, cachingAtDepth int, ttl string) {
	passedPrefill := false
	depth := 0
	previousRole := ""
	for i := len(messages) - 1; i >= 0; i-- {
		mm, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		if !passedPrefill && msgString(mm, "role") == "assistant" {
			continue
		}
		passedPrefill = true
		if msgString(mm, "role") == "system" {
			continue
		}
		if msgString(mm, "role") != previousRole {
			if depth == cachingAtDepth || depth == cachingAtDepth+2 {
				if s, ok := mm["content"].(string); ok {
					mm["content"] = []any{map[string]any{
						"type": "text", "text": s,
						"cache_control": map[string]any{"type": "ephemeral", "ttl": ttl},
					}}
				} else if arr, ok := mm["content"].([]any); ok && len(arr) > 0 {
					if last, ok := arr[len(arr)-1].(map[string]any); ok {
						last["cache_control"] = map[string]any{"type": "ephemeral", "ttl": ttl}
					}
				}
			}
			if depth == cachingAtDepth+2 {
				break
			}
			depth++
			previousRole = msgString(mm, "role")
		}
	}
}

func CachingSystemPromptForOpenRouter(messages []any, ttl string) {
	if len(messages) == 0 {
		return
	}
	var sysMsg map[string]any
	for _, m := range messages {
		if mm, ok := m.(map[string]any); ok && msgString(mm, "role") == "system" {
			sysMsg = mm
			break
		}
	}
	if sysMsg == nil || sysMsg["cache_control"] != nil {
		return
	}
	var cc map[string]any
	if ttl != "" {
		cc = map[string]any{"type": "ephemeral", "ttl": ttl}
	} else {
		cc = map[string]any{"type": "ephemeral"}
	}
	if arr, ok := sysMsg["content"].([]any); ok {
		for _, p := range arr {
			if pm, ok := p.(map[string]any); ok && pm["cache_control"] != nil {
				return
			}
		}
		for i := len(arr) - 1; i >= 0; i-- {
			if pm, ok := arr[i].(map[string]any); ok && pm["type"] == "text" {
				pm["cache_control"] = cc
				return
			}
		}
	} else if s, ok := sysMsg["content"].(string); ok {
		sysMsg["content"] = []any{map[string]any{"type": "text", "text": s, "cache_control": cc}}
	}
}

func CalculateClaudeBudgetTokens(maxTokens float64, reasoningEffort string, stream, adaptive bool) any {
	if adaptive {
		switch reasoningEffort {
		case "min", "low":
			return "low"
		case "medium":
			return "medium"
		case "high":
			return "high"
		case "max":
			return "max"
		default:
			return nil
		}
	}
	var budget float64
	switch reasoningEffort {
	case "auto":
		return nil
	case "min":
		budget = 1024
	case "low":
		budget = maxTokens * 0.1
	case "medium":
		budget = maxTokens * 0.25
	case "high":
		budget = maxTokens * 0.5
	case "max":
		budget = maxTokens * 0.95
	default:
		return nil
	}
	if budget < 1024 {
		budget = 1024
	}
	if !stream && budget > 21333 {
		budget = 21333
	}
	return int(budget)
}

var gemini3ProRe = regexp.MustCompile(`gemini-3[.\d]*-pro`)
var gemini3FlashRe = regexp.MustCompile(`gemini-3[.\d]*-flash`)
var flashLiteRe = regexp.MustCompile(`flash-lite`)
var flashRe = regexp.MustCompile(`flash`)
var proRe = regexp.MustCompile(`pro`)

func CalculateGoogleBudgetTokens(maxTokens float64, reasoningEffort, model string) any {
	flash := func() any {
		switch reasoningEffort {
		case "auto":
			return -1
		case "min":
			return 0
		case "low":
			return int(maxTokens * 0.1)
		case "medium":
			return int(maxTokens * 0.25)
		case "high":
			return int(maxTokens * 0.5)
		case "max":
			return int(maxTokens)
		default:
			return nil
		}
	}
	_ = flash
	switch {
	case gemini3ProRe.MatchString(model):
		switch reasoningEffort {
		case "min", "low", "medium":
			return "low"
		case "high", "max":
			return "high"
		default:
			return nil
		}
	case gemini3FlashRe.MatchString(model):
		switch reasoningEffort {
		case "min":
			return "minimal"
		case "low":
			return "low"
		case "medium":
			return "medium"
		case "high", "max":
			return "high"
		default:
			return nil
		}
	case flashLiteRe.MatchString(model):
		var budget float64
		switch reasoningEffort {
		case "auto":
			return -1
		case "min":
			return 0
		case "low":
			budget = maxTokens * 0.1
		case "medium":
			budget = maxTokens * 0.25
		case "high":
			budget = maxTokens * 0.5
		case "max":
			budget = maxTokens
		default:
			return nil
		}
		if budget > 24576 {
			budget = 24576
		}
		if budget < 512 {
			budget = 512
		}
		return int(budget)
	case flashRe.MatchString(model):
		var budget float64
		switch reasoningEffort {
		case "auto":
			return -1
		case "min":
			return 0
		case "low":
			budget = maxTokens * 0.1
		case "medium":
			budget = maxTokens * 0.25
		case "high":
			budget = maxTokens * 0.5
		case "max":
			budget = maxTokens
		default:
			return nil
		}
		if budget > 24576 {
			budget = 24576
		}
		return int(budget)
	case proRe.MatchString(model):
		var budget float64
		switch reasoningEffort {
		case "auto":
			return -1
		case "min":
			return 128
		case "low":
			budget = maxTokens * 0.1
		case "medium":
			budget = maxTokens * 0.25
		case "high":
			budget = maxTokens * 0.5
		case "max":
			budget = maxTokens
		default:
			return nil
		}
		if budget > 32768 {
			budget = 32768
		}
		if budget < 128 {
			budget = 128
		}
		return int(budget)
	default:
		return nil
	}
}

var audioFormatMap = map[string]string{"audio/mpeg": "mp3", "audio/wav": "wav"}

func EmbedOpenRouterMedia(messages []any, audio, video bool) {
	for _, m := range messages {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		arr, ok := mm["content"].([]any)
		if !ok {
			continue
		}
		for _, c := range arr {
			cm, ok := c.(map[string]any)
			if !ok {
				continue
			}
			if video {
				if cm["type"] == "video_url" {
					if vu, ok := cm["video_url"].(map[string]any); ok {
						if urlStr, ok := vu["url"].(string); ok && strings.HasPrefix(urlStr, "data:") {
							cm["type"] = "video_url"
						}
					}
				}
			}
			if audio && cm["type"] == "audio_url" {
				au, ok := cm["audio_url"].(map[string]any)
				if !ok {
					continue
				}
				urlStr, ok := au["url"].(string)
				if !ok || !strings.HasPrefix(urlStr, "data:") {
					continue
				}
				comma := strings.Index(urlStr, ",")
				if comma < 0 {
					continue
				}
				header, b64 := urlStr[:comma], urlStr[comma+1:]
				mime := "audio/mpeg"
				if parts := strings.Split(header, ";"); len(parts) > 0 {
					mime = strings.TrimPrefix(parts[0], "data:")
				}
				format := audioFormatMap[mime]
				if format == "" {
					format = "mp3"
				}
				cm["type"] = "input_audio"
				cm["input_audio"] = map[string]any{"format": format, "data": b64}
				delete(cm, "audio_url")
			}
		}
	}
}

func AddReasoningContentToToolCalls(messages []any) {
	for _, m := range messages {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if _, ok := mm["tool_calls"].([]any); !ok {
			continue
		}
		if _, has := mm["reasoning_content"]; has {
			continue
		}
		mm["reasoning_content"] = ""
	}
}

func openRouterSignatureFormat(model string) string {
	switch {
	case regexp.MustCompile(`google/gemini`).MatchString(model):
		return "google-gemini-v1"
	case regexp.MustCompile(`anthropic/claude`).MatchString(model):
		return "anthropic-claude-v1"
	case regexp.MustCompile(`openai/gpt`).MatchString(model):
		return "openai-responses-v1"
	case regexp.MustCompile(`x-ai/grok`).MatchString(model):
		return "xai-responses-v1"
	default:
		return "unknown"
	}
}

func AddOpenRouterSignatures(messages []any, model string, thoughtSignatures bool) {
	for _, m := range messages {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		var details []any
		addDetail := func(data, id string) {
			if data == "" {
				return
			}
			dID := id
			if dID == "" {
				dID = fmt.Sprintf("signature-%d", len(details))
			}
			details = append(details, map[string]any{
				"index": len(details), "id": dID, "type": "reasoning.encrypted",
				"data": data, "format": openRouterSignatureFormat(model),
			})
		}
		if sig, ok := mm["signature"].(string); ok {
			if thoughtSignatures {
				addDetail(sig, "")
			}
			delete(mm, "signature")
		}
		if tc, ok := mm["tool_calls"].([]any); ok {
			for _, t := range tc {
				if tm, ok := t.(map[string]any); ok {
					if sig, ok := tm["signature"].(string); ok {
						id, _ := tm["id"].(string)
						addDetail(sig, id)
						delete(tm, "signature")
					}
				}
			}
		}
		if len(details) > 0 {
			mm["reasoning_details"] = details
		}
	}
}

func SetJSONObjectFormat(bodyParams map[string]any, messages []any, jsonSchema map[string]any) []any {
	bodyParams["response_format"] = map[string]any{"type": "json_object"}
	schemaVal, _ := jsonSchema["value"]
	schemaJSON, _ := json.MarshalIndent(schemaVal, "", "    ")
	msgs := append(messages, map[string]any{
		"role":    "user",
		"content": "JSON schema for the response:\n" + string(schemaJSON),
	})
	return msgs
}

func FlattenSchema(schema any, api string) any {
	m, ok := schema.(map[string]any)
	if !ok {
		return schema
	}
	isGoogle := api == "vertexai" || api == "makersuite"
	var defs map[string]any
	if d, ok := m["$defs"].(map[string]any); ok {
		defs = d
	} else {
		defs = map[string]any{}
	}
	var resolve func(obj any, parents []string) any
	resolve = func(obj any, parents []string) any {
		om, ok := obj.(map[string]any)
		if !ok {
			if arr, ok := obj.([]any); ok {
				out := make([]any, 0, len(arr))
				for _, item := range arr {
					out = append(out, resolve(item, parents))
				}
				return out
			}
			return obj
		}
		if ref, ok := om["$ref"].(string); ok && strings.HasPrefix(ref, "#/$defs/") {
			name := ref[strings.LastIndex(ref, "/")+1:]
			for _, p := range parents {
				if p == name {
					return map[string]any{}
				}
			}
			if def, ok := defs[name]; ok {
				return resolve(def, append(parents, name))
			}
			return map[string]any{}
		}
		result := make(map[string]any, len(om))
		for k, v := range om {
			if k == "$defs" {
				continue
			}
			if isGoogle && (k == "default" || k == "additionalProperties" || k == "exclusiveMinimum" || k == "propertyNames") {
				continue
			}
			result[k] = resolve(v, parents)
		}
		return result
	}
	out := resolve(m, nil)
	if om, ok := out.(map[string]any); ok {
		delete(om, "$schema")
		return om
	}
	return out
}

func MergeObjectWithYAML(obj map[string]any, yamlString string) {
	if yamlString == "" {
		return
	}
	parsed := parseSimpleYAMLMap(yamlString)
	for k, v := range parsed {
		obj[k] = v
	}
}

func MergeHeadersWithYAML(headers map[string]string, yamlString string) {
	if yamlString == "" {
		return
	}
	for k, v := range parseSimpleYAMLMap(yamlString) {
		headers[k] = fmt.Sprint(v)
	}
}

func ExcludeKeysByYAML(obj map[string]any, yamlString string) {
	if yamlString == "" {
		return
	}
	parsed := parseSimpleYAMLMap(yamlString)
	for k := range parsed {
		delete(obj, k)
	}
	if s := strings.TrimSpace(yamlString); s != "" && !strings.ContainsAny(s, ":\n-[{") {
		delete(obj, s)
	}
}

func parseSimpleYAMLMap(yamlString string) map[string]any {
	out := make(map[string]any)
	var v any
	if err := yaml.Unmarshal([]byte(yamlString), &v); err != nil {
		return out
	}
	switch t := v.(type) {
	case map[string]any:
		return t
	case []any:
		for _, item := range t {
			if m, ok := item.(map[string]any); ok {
				for k, val := range m {
					out[k] = val
				}
			} else if s, ok := item.(string); ok {
				out[s] = true
			}
		}
		return out
	case string:
		if t != "" {
			out[t] = true
		}
		return out
	default:
		return out
	}
}
