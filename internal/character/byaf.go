package character

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
)

var byafUserMacroRe = regexp.MustCompile(`(?i)#\{user\}:`)
var byafCharMacroRe = regexp.MustCompile(`(?i)#\{character\}:`)

// Manual scan: RE2 has no lookahead, and {{macro}} placeholders must survive.
func replaceBareMacro(s, macro, repl string) string {
	var b strings.Builder
	b.Grow(len(s))
	for {
		i := strings.Index(s, macro)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		rest := s[i+len(macro):]
		if len(rest) > 0 && rest[0] == '}' {
			b.WriteString(macro)
		} else {
			b.WriteString(repl)
		}
		s = rest
	}
}

func byafReplaceMacros(s string) string {
	s = byafUserMacroRe.ReplaceAllString(s, "{{user}}:")
	s = byafCharMacroRe.ReplaceAllString(s, "{{char}}:")
	s = replaceBareMacro(s, "{character}", "{{char}}")
	s = replaceBareMacro(s, "{user}", "{{user}}")
	return s
}

func byafStr(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

func byafFormatExampleMessages(examples []any) string {
	var parts []string
	for _, item := range examples {
		mm, ok := item.(map[string]any)
		if !ok {
			continue
		}
		text, _ := mm["text"].(string)
		if text == "" {
			continue
		}
		parts = append(parts, "<START>\n"+byafReplaceMacros(text)+"\n")
	}
	return strings.TrimRight(strings.Join(parts, ""), "\n")
}

func byafAlternateGreetings(scenarios []map[string]any) []string {
	if len(scenarios) <= 1 {
		return []string{}
	}
	var firstText string
	if len(scenarios) > 0 {
		if fm, ok := scenarios[0]["firstMessages"].([]any); ok && len(fm) > 0 {
			if m0, ok := fm[0].(map[string]any); ok {
				firstText, _ = m0["text"].(string)
			}
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, sc := range scenarios[1:] {
		fm, ok := sc["firstMessages"].([]any)
		if !ok || len(fm) == 0 {
			continue
		}
		m0, ok := fm[0].(map[string]any)
		if !ok {
			continue
		}
		text, _ := m0["text"].(string)
		if text == "" || text == firstText || seen[text] {
			continue
		}
		seen[text] = true
		out = append(out, byafReplaceMacros(text))
	}
	if out == nil {
		return []string{}
	}
	return out
}

func byafConvertBook(items []any) map[string]any {
	book := map[string]any{"entries": []any{}, "extensions": map[string]any{}}
	if len(items) == 0 {
		return nil
	}
	var entries []any
	for i, item := range items {
		mm, ok := item.(map[string]any)
		if !ok {
			continue
		}
		keys := []string{}
		if k, ok := mm["key"].(string); ok {
			for _, part := range strings.Split(byafReplaceMacros(k), ",") {
				if trimmed := strings.TrimSpace(part); trimmed != "" {
					keys = append(keys, trimmed)
				}
			}
		}
		val, _ := mm["value"].(string)
		entries = append(entries, map[string]any{
			"keys": keys, "content": byafReplaceMacros(val),
			"extensions": map[string]any{}, "enabled": true, "insertion_order": i,
		})
	}
	book["entries"] = entries
	return book
}

type ByafImage struct {
	Filename string
	Image    []byte
	Label    string
}

type ByafBackground struct {
	Name  string
	Data  []byte
	Paths []string
}

type ByafData struct {
	Card            map[string]any
	Images          []ByafImage
	Scenarios       []map[string]any
	ChatBackgrounds []ByafBackground
	Character       map[string]any
}

func extractZipFile(data []byte, name string) []byte {
	data = findZipStart(data)
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil
	}
	want := normalizeZipEntryPath(name)
	for _, f := range zr.File {
		if normalizeZipEntryPath(f.Name) == want {
			rc, err := f.Open()
			if err != nil {
				return nil
			}
			var buf []byte
			tmp := make([]byte, 32768)
			for {
				n, err := rc.Read(tmp)
				if n > 0 {
					buf = append(buf, tmp[:n]...)
				}
				if err != nil {
					break
				}
			}
			rc.Close()
			return buf
		}
	}
	return nil
}

func ParseBYAF(data []byte) (*ByafData, error) {
	manifestBuf := extractZipFile(data, "manifest.json")
	if manifestBuf == nil {
		return nil, fmt.Errorf("failed to extract manifest.json from BYAF file")
	}
	var manifest map[string]any
	if err := json.Unmarshal(manifestBuf, &manifest); err != nil {
		return nil, fmt.Errorf("invalid BYAF manifest")
	}

	charPaths, _ := manifest["characters"].([]any)
	if len(charPaths) == 0 {
		return nil, fmt.Errorf("invalid BYAF file: characters array is empty")
	}
	charPath, _ := charPaths[0].(string)
	if charPath == "" {
		return nil, fmt.Errorf("invalid BYAF file: missing character path")
	}
	charBuf := extractZipFile(data, charPath)
	if charBuf == nil {
		return nil, fmt.Errorf("invalid BYAF file: failed to extract character JSON")
	}
	var character map[string]any
	if err := json.Unmarshal(charBuf, &character); err != nil {
		return nil, fmt.Errorf("invalid BYAF file: character is not a valid JSON")
	}

	var scenarios []map[string]any
	if arr, ok := manifest["scenarios"].([]any); ok && len(arr) > 0 {
		for _, p := range arr {
			ps, _ := p.(string)
			if ps == "" {
				continue
			}
			buf := extractZipFile(data, ps)
			if buf == nil {
				continue
			}
			var sc map[string]any
			if err := json.Unmarshal(buf, &sc); err == nil {
				scenarios = append(scenarios, sc)
			}
		}
	}
	if len(scenarios) == 0 {
		scenarios = []map[string]any{{}}
	}

	images := byafCharacterImages(data, character, charPath)

	name, _ := character["name"].(string)
	displayName, _ := character["displayName"].(string)
	cardName := name
	if cardName == "" {
		cardName = displayName
	}
	persona, _ := character["persona"].(string)
	firstScenario := scenarios[0]
	firstMsgs, _ := firstScenario["firstMessages"].([]any)
	var firstMes, narrative, formatting, mesExample string
	narrative = byafReplaceMacros(byafStr(firstScenario, "narrative"))
	if len(firstMsgs) > 0 {
		if m0, ok := firstMsgs[0].(map[string]any); ok {
			firstMes = byafReplaceMacros(byafStr(m0, "text"))
		}
	}
	if em, ok := firstScenario["exampleMessages"]; ok {
		if arr, ok := em.([]any); ok {
			mesExample = byafFormatExampleMessages(arr)
		}
	}
	formatting = byafReplaceMacros(byafStr(firstScenario, "formattingInstructions"))
	author, _ := manifest["author"].(map[string]any)
	creatorNotes := ""
	creator := ""
	if author != nil {
		if u, ok := author["backyardURL"].(string); ok {
			creatorNotes = u
		}
		creator, _ = author["name"].(string)
	}
	tags := []string{}
	if nsfw, _ := character["isNSFW"].(bool); nsfw {
		tags = append(tags, "nsfw")
	}
	extensions := map[string]any{}
	if displayName != "" {
		extensions["display_name"] = displayName
	}
	var loreItems []any
	if arr, ok := character["loreItems"].([]any); ok {
		loreItems = arr
	}
	card := map[string]any{
		"spec": "chara_card_v2", "spec_version": "2.0",
		"data": map[string]any{
			"name": cardName, "description": byafReplaceMacros(persona),
			"personality": "", "scenario": narrative, "first_mes": firstMes,
			"mes_example": mesExample, "creator_notes": creatorNotes,
			"system_prompt": formatting, "post_history_instructions": "",
			"alternate_greetings": byafAlternateGreetings(scenarios),
			"character_book":      byafConvertBook(loreItems),
			"tags":                tags, "creator": creator, "character_version": "",
			"extensions": extensions,
		},
		"create_date": timeNowISO(),
	}

	var backgrounds []ByafBackground
	bgIndex := 1
	for _, sc := range scenarios {
		bgPath, _ := sc["backgroundImage"].(string)
		if bgPath == "" {
			continue
		}
		buf := extractZipFile(data, bgPath)
		if buf == nil {
			continue
		}
		dup := -1
		for i, bg := range backgrounds {
			if bytes.Equal(bg.Data, buf) {
				dup = i
				break
			}
		}
		if dup != -1 {
			backgrounds[dup].Paths = append(backgrounds[dup].Paths, bgPath)
			continue
		}
		backgrounds = append(backgrounds, ByafBackground{
			Name: fmt.Sprintf("%s bg %d", name, bgIndex), Data: buf, Paths: []string{bgPath},
		})
		bgIndex++
	}

	return &ByafData{
		Card: card, Images: images, Scenarios: scenarios,
		ChatBackgrounds: backgrounds, Character: character,
	}, nil
}

func byafCharacterImages(data []byte, character map[string]any, characterPath string) []ByafImage {
	imgs, _ := character["images"].([]any)
	if len(imgs) == 0 {
		return []ByafImage{{Filename: "", Image: DefaultAvatarPNG, Label: ""}}
	}
	dir := path.Dir(characterPath)
	var out []ByafImage
	for _, item := range imgs {
		mm, ok := item.(map[string]any)
		if !ok {
			continue
		}
		imgPath, _ := mm["path"].(string)
		if imgPath == "" {
			continue
		}
		full := path.Join(dir, imgPath)
		buf := extractZipFile(data, full)
		if buf == nil {
			continue
		}
		label, _ := mm["label"].(string)
		out = append(out, ByafImage{Filename: path.Base(imgPath), Image: buf, Label: label})
	}
	if len(out) == 0 {
		return []ByafImage{{Filename: "", Image: DefaultAvatarPNG, Label: ""}}
	}
	return out
}

func timeNowISO() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

func ByafChatFromScenario(scenario map[string]any, userName, characterName string, backgrounds []ByafBackground) string {
	strVal := func(m map[string]any, key string) string {
		s, _ := m[key].(string)
		return s
	}
	var messages []any
	if arr, ok := scenario["messages"].([]any); ok {
		messages = arr
	}
	var chatStartDate any = timeNowISO()
	if len(messages) > 0 {
		for _, m := range messages {
			if mm, ok := m.(map[string]any); ok {
				if created, ok := mm["createdAt"]; ok && created != nil {
					chatStartDate = created
					break
				}
			}
		}
	}
	var chatBgName string
	bgImage := strVal(scenario, "backgroundImage")
	for _, bg := range backgrounds {
		for _, p := range bg.Paths {
			if p == bgImage {
				chatBgName = bg.Name
				break
			}
		}
	}
	var emArr []any
	if em, ok := scenario["exampleMessages"].([]any); ok {
		emArr = em
	}
	chatMeta := map[string]any{
		"scenario":              strVal(scenario, "narrative"),
		"mes_example":           byafFormatExampleMessages(emArr),
		"system_prompt":         byafReplaceMacros(strVal(scenario, "formattingInstructions")),
		"mes_examples_optional": false,
		"byaf_model_settings": map[string]any{
			"model": strVal(scenario, "model"), "temperature": withDefault(scenario["temperature"], 1.2),
			"top_k": withDefault(scenario["topK"], 40.0), "top_p": withDefault(scenario["topP"], 0.9),
			"min_p": withDefault(scenario["minP"], 0.1), "min_p_enabled": withDefault(scenario["minPEnabled"], true),
			"repeat_penalty":        withDefault(scenario["repeatPenalty"], 1.05),
			"repeat_penalty_tokens": withDefault(scenario["repeatLastN"], 256.0),
			"by_prompt_template":    withDefault(scenario["promptTemplate"], "general"),
			"grammar":               scenario["grammar"],
		},
	}
	if chatBgName != "" {
		chatMeta["chat_backgrounds"] = []string{chatBgName}
		chatMeta["custom_background"] = `url("` + chatBgName + `")`
	} else {
		chatMeta["chat_backgrounds"] = []string{}
		chatMeta["custom_background"] = ""
	}
	canDelete := false
	if v, ok := scenario["canDeleteExampleMessages"].(bool); ok {
		canDelete = v
	}
	chatMeta["mes_examples_optional"] = canDelete
	lines := []string{}
	header, _ := json.Marshal(map[string]any{
		"user_name": "unused", "character_name": "unused", "chat_metadata": chatMeta,
	})
	lines = append(lines, string(header))
	if fm, ok := scenario["firstMessages"].([]any); ok && len(fm) > 0 {
		if m0, ok := fm[0].(map[string]any); ok {
			if text, _ := m0["text"].(string); text != "" {
				first, _ := json.Marshal(map[string]any{
					"name": characterName, "is_user": false,
					"send_date": chatStartDate, "mes": text,
				})
				lines = append(lines, string(first))
			}
		}
	}

	newestOutput := func(message map[string]any) (map[string]any, []string) {
		outputs, _ := message["outputs"].([]any)
		var newest map[string]any
		var newestTime string
		var swipes []string
		for _, o := range outputs {
			om, ok := o.(map[string]any)
			if !ok {
				continue
			}
			text, _ := om["text"].(string)
			swipes = append(swipes, text)
			ts, _ := om["activeTimestamp"].(string)
			if newest == nil || ts >= newestTime {
				newest = om
				newestTime = ts
			}
		}
		return newest, swipes
	}
	var humans, ais []map[string]any
	for _, m := range messages {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if typ, _ := mm["type"].(string); typ == "human" {
			humans = append(humans, mm)
		} else {
			ais = append(ais, mm)
		}
	}
	emit := func(name string, isUser bool, sendDate any, mes string, swipes []string, swipeID int, withSwipes bool) {
		entry := map[string]any{"name": name, "is_user": isUser, "send_date": sendDate, "mes": mes}
		if withSwipes {
			entry["swipes"] = swipes
			entry["swipe_id"] = swipeID
		}
		if b, err := json.Marshal(entry); err == nil {
			lines = append(lines, string(b))
		}
	}
	if len(humans) > 0 && len(ais) > 0 && len(humans) == len(ais) {
		for i := range humans {
			hText, _ := humans[i]["text"].(string)
			emit(userName, true, humans[i]["createdAt"], hText, nil, 0, false)
			newest, swipes := newestOutput(ais[i])
			newText := ""
			var newCreated any
			if newest != nil {
				newText, _ = newest["text"].(string)
				newCreated = newest["createdAt"]
			}
			swipeID := -1
			for j, s := range swipes {
				if s == newText {
					swipeID = j
					break
				}
			}
			emit(characterName, false, newCreated, newText, swipes, swipeID, true)
		}
	} else {
		for _, m := range messages {
			mm, ok := m.(map[string]any)
			if !ok {
				continue
			}
			isUser := mm["type"] == "human"
			if isUser {
				text, _ := mm["text"].(string)
				emit(userName, true, mm["createdAt"], text, nil, 0, false)
			} else {
				newest, swipes := newestOutput(mm)
				newText := ""
				var newCreated any
				if newest != nil {
					newText, _ = newest["text"].(string)
					newCreated = newest["createdAt"]
				}
				swipeID := -1
				for j, s := range swipes {
					if s == newText {
						swipeID = j
						break
					}
				}
				emit(characterName, false, newCreated, newText, swipes, swipeID, true)
			}
		}
	}
	return strings.Join(lines, "\n")
}
