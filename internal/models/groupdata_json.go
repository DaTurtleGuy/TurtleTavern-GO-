package models

import (
	"encoding/json"
	"strconv"
)

// Groups written by older SillyTavern builds do not match this struct's types:
// ids are numbers, activation_strategy is a boolean, chats may be numbers, and so
// on. A strict decode rejects the whole record, which silently dropped 43 of 155
// groups from the API. Every field is coerced leniently here instead.
func (g *GroupData) UnmarshalJSON(data []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	g.ID = asString(raw["id"])
	g.Name = asString(raw["name"])
	g.Members = asStringSlice(raw["members"])
	g.AvatarURL = asString(raw["avatar_url"])
	g.AllowSelfResponses = asBool(raw["allow_self_responses"])
	g.ActivationStrategy = asInt(raw["activation_strategy"])
	g.GenerationMode = asInt(raw["generation_mode"])
	g.DisabledMembers = asStringSlice(raw["disabled_members"])
	g.Fav = raw["fav"]
	g.ChatID = asString(raw["chat_id"])
	g.Chats = asStringSlice(raw["chats"])
	g.AutoModeDelay = asInt(raw["auto_mode_delay"])
	g.GenerationModeJoinPrefix = asString(raw["generation_mode_join_prefix"])
	g.GenerationModeJoinSuffix = asString(raw["generation_mode_join_suffix"])
	g.DateAdded = asFloat(raw["date_added"])
	g.CreateDate = asString(raw["create_date"])
	g.DateLastChat = asFloat(raw["date_last_chat"])
	g.ChatSize = int64(asFloat(raw["chat_size"]))
	return nil
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		return ""
	}
}

func asStringSlice(v any) []string {
	items, ok := v.([]any)
	if !ok {
		if existing, ok := v.([]string); ok {
			return existing
		}
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s := asString(item); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func asInt(v any) int {
	return int(asFloat(v))
}

func asFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case json.Number:
		f, _ := t.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(t, 64)
		return f
	case bool:
		if t {
			return 1
		}
		return 0
	default:
		return 0
	}
}

func asBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case float64:
		return t != 0
	case string:
		b, _ := strconv.ParseBool(t)
		return b
	default:
		return false
	}
}
