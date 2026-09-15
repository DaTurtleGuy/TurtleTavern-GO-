package character

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/TurtleTavern/turtletavern/internal/models"
	"github.com/TurtleTavern/turtletavern/internal/util"
)

func CharaFormatData(formData map[string]any, dirs models.UserDirectories) map[string]any {
	jsonDataRaw, _ := formData["json_data"].(string)
	char := make(map[string]any)
	if jsonDataRaw != "" {
		json.Unmarshal([]byte(jsonDataRaw), &char)
	}
	delete(char, "json_data")

	chName, _ := formData["ch_name"].(string)

	setIfPresent := func(target map[string]any, key string, src map[string]any) {
		if v, ok := src[key]; ok {
			target[key] = v
		} else {
			target[key] = ""
		}
	}
	_ = setIfPresent

	char["name"] = chName
	char["description"] = orString(formData["description"])
	char["personality"] = orString(formData["personality"])
	char["scenario"] = orString(formData["scenario"])
	char["first_mes"] = orString(formData["first_mes"])
	char["mes_example"] = orString(formData["mes_example"])
	char["creatorcomment"] = orString(formData["creator_notes"])
	char["avatar"] = "none"
	char["chat"] = chName + " - " + util.HumanizedDateTime(0)
	char["talkativeness"] = orDefault(formData["talkativeness"], 0.5)
	char["fav"] = fmt.Sprintf("%v", formData["fav"]) == "true"

	tags := parseTags(formData["tags"])
	char["tags"] = tags

	char["spec"] = "chara_card_v2"
	char["spec_version"] = "2.0"

	data := make(map[string]any)
	data["name"] = chName
	data["description"] = orString(formData["description"])
	data["personality"] = orString(formData["personality"])
	data["scenario"] = orString(formData["scenario"])
	data["first_mes"] = orString(formData["first_mes"])
	data["mes_example"] = orString(formData["mes_example"])
	data["creator_notes"] = orString(formData["creator_notes"])
	data["system_prompt"] = orString(formData["system_prompt"])
	data["post_history_instructions"] = orString(formData["post_history_instructions"])
	data["tags"] = tags
	data["creator"] = orString(formData["creator"])
	data["character_version"] = orString(formData["character_version"])

	altGreetings := getAlternateGreetings(formData["alternate_greetings"])
	data["alternate_greetings"] = altGreetings

	extensions := make(map[string]any)
	extensions["talkativeness"] = orDefault(formData["talkativeness"], 0.5)
	extensions["fav"] = fmt.Sprintf("%v", formData["fav"]) == "true"
	extensions["world"] = orString(formData["world"])

	depthPrompt := make(map[string]any)
	depthPrompt["prompt"] = orString(formData["depth_prompt_prompt"])
	depthPrompt["depth"] = jsNumberOr(formData["depth_prompt_depth"], 4)
	roleVal := "system"
	if v, ok := formData["depth_prompt_role"]; ok && v != nil {
		if s, ok := v.(string); ok {
			roleVal = s
		}
	}
	depthPrompt["role"] = roleVal
	extensions["depth_prompt"] = depthPrompt

	data["extensions"] = extensions
	char["data"] = data

	if extStr, ok := formData["extensions"].(string); ok && extStr != "" {
		var extMap map[string]any
		if err := json.Unmarshal([]byte(extStr), &extMap); err == nil {
			data["extensions"] = util.DeepMerge(extensions, extMap)
		}
	}

	if world, _ := formData["world"].(string); world != "" {
		if book := worldToCharacterBook(world, dirs); book != nil {
			data["character_book"] = book
		}
	}

	return char
}

func worldToCharacterBook(world string, dirs models.UserDirectories) any {
	path := filepath.Join(dirs.Worlds, util.SanitizeFileName(world+".json"))
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var file map[string]any
	if err := json.Unmarshal(data, &file); err != nil {
		return nil
	}
	if original, ok := file["originalData"]; ok && original != nil {
		return original
	}
	entries, ok := file["entries"]
	if !ok {
		return nil
	}
	return convertWorldInfoToCharacterBook(world, entries)
}

func worldEntryStr(entry map[string]any, key string) string {
	s, _ := entry[key].(string)
	return s
}

func convertWorldInfoToCharacterBook(name string, entries any) map[string]any {
	result := map[string]any{"entries": []any{}, "name": name}
	var list []any
	if arr, ok := entries.([]any); ok {
		list = arr
	} else if m, ok := entries.(map[string]any); ok {
		for _, v := range m {
			list = append(list, v)
		}
	}
	out := []any{}
	for _, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		ext := map[string]any{}
		if e, ok := entry["extensions"].(map[string]any); ok {
			for k, v := range e {
				ext[k] = v
			}
		}
		position := 0.0
		if p, ok := entry["position"].(float64); ok {
			position = p
		}
		posStr := "after_char"
		if position == 0 {
			posStr = "before_char"
		}
		ext["position"] = entry["position"]
		ext["exclude_recursion"] = withDefaultBool(entry["excludeRecursion"], false)
		ext["display_index"] = withDefault(entry["displayIndex"], "")
		ext["probability"] = withDefault(entry["probability"], nil)
		ext["useProbability"] = withDefaultBool(entry["useProbability"], false)
		ext["depth"] = withDefault(entry["depth"], 4.0)
		ext["selectiveLogic"] = withDefault(entry["selectiveLogic"], 0.0)
		ext["outlet_name"] = withDefault(entry["outletName"], "")
		ext["group"] = withDefault(entry["group"], "")
		ext["group_override"] = withDefaultBool(entry["groupOverride"], false)
		ext["group_weight"] = withDefault(entry["groupWeight"], nil)
		ext["prevent_recursion"] = withDefaultBool(entry["preventRecursion"], false)
		ext["delay_until_recursion"] = withDefaultBool(entry["delayUntilRecursion"], false)
		ext["scan_depth"] = withDefault(entry["scanDepth"], nil)
		ext["match_whole_words"] = withDefault(entry["matchWholeWords"], nil)
		ext["use_group_scoring"] = withDefaultBool(entry["useGroupScoring"], false)
		ext["case_sensitive"] = withDefault(entry["caseSensitive"], nil)
		ext["automation_id"] = withDefault(entry["automationId"], "")
		ext["role"] = withDefault(entry["role"], 0.0)
		ext["vectorized"] = withDefaultBool(entry["vectorized"], false)
		ext["sticky"] = withDefault(entry["sticky"], nil)
		ext["cooldown"] = withDefault(entry["cooldown"], nil)
		ext["delay"] = withDefault(entry["delay"], nil)
		ext["match_persona_description"] = withDefaultBool(entry["matchPersonaDescription"], false)
		ext["match_character_description"] = withDefaultBool(entry["matchCharacterDescription"], false)
		ext["match_character_personality"] = withDefaultBool(entry["matchCharacterPersonality"], false)
		ext["match_character_depth_prompt"] = withDefaultBool(entry["matchCharacterDepthPrompt"], false)
		ext["match_scenario"] = withDefaultBool(entry["matchScenario"], false)
		ext["match_creator_notes"] = withDefaultBool(entry["matchCreatorNotes"], false)
		ext["triggers"] = withDefault(entry["triggers"], []any{})
		ext["ignore_budget"] = withDefaultBool(entry["ignoreBudget"], false)
		disabled, _ := entry["disable"].(bool)
		original := map[string]any{
			"id":              entry["uid"],
			"keys":            entry["key"],
			"secondary_keys":  entry["keysecondary"],
			"comment":         worldEntryStr(entry, "comment"),
			"content":         worldEntryStr(entry, "content"),
			"constant":        withDefaultBool(entry["constant"], false),
			"selective":       withDefaultBool(entry["selective"], false),
			"insertion_order": withDefault(entry["order"], 0.0),
			"enabled":         !disabled,
			"position":        posStr,
			"use_regex":       true,
			"extensions":      ext,
		}
		out = append(out, original)
	}
	result["entries"] = out
	return result
}

func withDefault(v any, def any) any {
	if v == nil {
		return def
	}
	return v
}

func withDefaultBool(v any, def bool) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return def
}

func jsNumberOr(v any, def float64) float64 {
	switch t := v.(type) {
	case nil:
		return def
	case float64:
		if t != t {
			return def
		}
		return t
	case string:
		var f float64
		if _, err := fmt.Sscanf(strings.TrimSpace(t), "%g", &f); err == nil {
			return f
		}
		if strings.TrimSpace(t) == "" {
			return 0
		}
		return def
	default:
		return def
	}
}

func orDefault(v any, def float64) float64 {
	switch t := v.(type) {
	case nil:
		return def
	case bool:
		if !t {
			return def
		}
		return 1
	case string:
		if t == "" {
			return def
		}
		if f, err := strconv.ParseFloat(t, 64); err == nil {
			if f == 0 {
				return def
			}
			return f
		}
		return def
	case float64:
		if t == 0 {
			return def
		}
		return t
	default:
		return def
	}
}

func ConvertToV2(char map[string]any, dirs models.UserDirectories) map[string]any {
	result := CharaFormatData(map[string]any{
		"json_data":           toJSON(char),
		"ch_name":             char["name"],
		"description":         char["description"],
		"personality":         char["personality"],
		"scenario":            char["scenario"],
		"first_mes":           char["first_mes"],
		"mes_example":         char["mes_example"],
		"creator_notes":       char["creatorcomment"],
		"talkativeness":       char["talkativeness"],
		"fav":                 char["fav"],
		"creator":             char["creator"],
		"tags":                char["tags"],
		"depth_prompt_prompt": char["depth_prompt_prompt"],
		"depth_prompt_depth":  char["depth_prompt_depth"],
		"depth_prompt_role":   char["depth_prompt_role"],
	}, dirs)
	if chat, ok := char["chat"].(string); ok && chat != "" {
		result["chat"] = chat
	}
	if cd, ok := char["create_date"].(string); ok && cd != "" {
		result["create_date"] = cd
	}
	return result
}

func ReadFromV2(char map[string]any) (map[string]any, bool) {
	wasFixed := false
	dataRaw, exists := char["data"]
	if !exists {
		return char, false
	}
	data, ok := dataRaw.(map[string]any)
	if !ok {
		return char, false
	}

	delete(char, "json_data")

	fieldMap := map[string]string{
		"name":          "name",
		"description":   "description",
		"personality":   "personality",
		"scenario":      "scenario",
		"first_mes":     "first_mes",
		"mes_example":   "mes_example",
		"talkativeness": "extensions.talkativeness",
		"fav":           "extensions.fav",
		"tags":          "tags",
	}
	defaults := map[string]any{
		"talkativeness": 0.5,
		"fav":           false,
	}

	for charField, v2Path := range fieldMap {
		v2Value := getNestedValue(data, v2Path)
		if v2Value == nil {
			if dv, ok := defaults[charField]; ok {
				v2Value = dv
			} else {
				continue
			}
		}
		tgt := char[charField]
		if tgt != nil && v2Value != nil && fmt.Sprintf("%v", tgt) != fmt.Sprintf("%v", v2Value) {
			wasFixed = true
		}
		char[charField] = v2Value
	}

	if char["chat"] == nil {
		char["chat"] = fmt.Sprintf("%v", char["name"]) + " - " + util.HumanizedDateTime(0)
	}

	return char, wasFixed
}

func GetCharaCardV2(jsonObject map[string]any, dirs models.UserDirectories) (map[string]any, bool) {
	spec, exists := jsonObject["spec"]
	if !exists || spec == nil {
		result := ConvertToV2(jsonObject, dirs)
		return result, false
	}
	return ReadFromV2(jsonObject)
}

func getAlternateGreetings(raw any) []string {
	switch v := raw.(type) {
	case []any:
		var result []string
		for _, item := range v {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
		return result
	case string:
		return []string{v}
	default:
		return []string{}
	}
}

func parseTags(raw any) []string {
	switch v := raw.(type) {
	case string:
		if v == "" {
			return []string{}
		}
		parts := strings.Split(v, ",")
		var result []string
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				result = append(result, p)
			}
		}
		return result
	case []any:
		var result []string
		for _, item := range v {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
		return result
	default:
		return []string{}
	}
}

func orString(v any) string {
	if v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return fmt.Sprintf("%v", v)
	}
	return s
}

func orFloat64(v any, def float64) float64 {
	if v == nil {
		return def
	}
	switch val := v.(type) {
	case float64:
		return val
	case string:
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			return f
		}
	}
	return def
}

func getNestedValue(m map[string]any, path string) any {
	parts := strings.Split(path, ".")
	var current any = m
	for _, p := range parts {
		cm, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current, ok = cm[p]
		if !ok {
			return nil
		}
	}
	return current
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func SetNestedValue(m map[string]any, path string, value any) {
	parts := strings.Split(path, ".")
	current := m
	for i := 0; i < len(parts)-1; i++ {
		if next, ok := current[parts[i]].(map[string]any); ok {
			current = next
		} else {
			next = make(map[string]any)
			current[parts[i]] = next
			current = next
		}
	}
	current[parts[len(parts)-1]] = value
}
