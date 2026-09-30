package handlers

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// oldAccumulate is the pre-optimization stats algorithm, frozen as an oracle.
func oldAccumulate(content string, seen map[string]bool) (map[string]float64, float64) {
	nums := map[string]float64{}
	firstChat := float64(time.Date(9999, 12, 31, 23, 59, 59, 999000000, time.UTC).UnixMilli())
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var msg map[string]any
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		if mes, ok := msg["mes"].(string); ok && mes != "" {
			sum := sha256.Sum256([]byte(mes))
			key := fmt.Sprintf("%x", sum)
			if seen[key] {
				continue
			}
			seen[key] = true
		}
		if gs, ok := msg["gen_started"]; ok && msg["gen_finished"] != nil {
			if gf, ok := msg["gen_finished"]; ok && gf != nil {
				nums["total_gen_time"] += float64(parseStatsTimestamp(gf) - parseStatsTimestamp(gs))
			}
			if swipes, ok := msg["swipes"].([]any); ok && msg["swipe_info"] == nil {
				nums["total_gen_time"] += float64(parseStatsTimestamp(msg["gen_finished"])-parseStatsTimestamp(msg["gen_started"])) * float64(len(swipes))
			}
		}
		if mes, ok := msg["mes"].(string); ok && mes != "" {
			wc := float64(countStatsWords(mes))
			if isUser, _ := msg["is_user"].(bool); isUser {
				nums["user_word_count"] += wc
				nums["user_msg_count"]++
			} else {
				nums["non_user_word_count"] += wc
				nums["non_user_msg_count"]++
			}
		}
		if swipes, ok := msg["swipes"].([]any); ok && len(swipes) > 1 {
			nums["total_swipe_count"] += float64(len(swipes) - 1)
			for i := 1; i < len(swipes); i++ {
				if text, ok := swipes[i].(string); ok {
					wc := float64(countStatsWords(text))
					if isUser, _ := msg["is_user"].(bool); isUser {
						nums["user_word_count"] += wc
						nums["user_msg_count"]++
					} else {
						nums["non_user_word_count"] += wc
						nums["non_user_msg_count"]++
					}
				}
			}
		}
		if infos, ok := msg["swipe_info"].([]any); ok && len(infos) > 1 {
			for i := 1; i < len(infos); i++ {
				if info, ok := infos[i].(map[string]any); ok {
					if gs, ok := info["gen_started"]; ok && info["gen_finished"] != nil {
						if gf, ok := info["gen_finished"]; ok && gf != nil {
							nums["total_gen_time"] += float64(parseStatsTimestamp(gf) - parseStatsTimestamp(gs))
						}
					}
				}
			}
		}
		if isUser, _ := msg["is_user"].(bool); isUser {
			if ts := float64(parseStatsTimestamp(msg["send_date"])); ts < firstChat {
				firstChat = ts
			}
		}
	}
	return nums, firstChat
}

func statsFixture() []string {
	return []string{
		`{"chat_metadata":{},"user_name":"u","character_name":"c"}`,
		`{"name":"u","is_user":true,"mes":"hello brave world","send_date":"February 25, 2025 3:46pm"}`,
		`{"name":"c","is_user":false,"mes":"greetings traveler","send_date":1700000000000,"gen_started":"2024-01-01T00:00:00.000Z","gen_finished":"2024-01-01T00:00:02.000Z"}`,
		`{"name":"c","is_user":false,"mes":"pick one","swipes":["pick one","second choice here"],"send_date":1700000001000}`,
		`{"name":"c","is_user":false,"mes":"swipe info test","swipe_info":[{"gen_started":"2024-01-01T00:00:00.000Z"},{"gen_started":"2024-01-01T00:00:01.000Z","gen_finished":"2024-01-01T00:00:04.000Z"}],"send_date":1700000002000}`,
		`{"name":"u","is_user":true,"mes":"hello brave world","send_date":"February 25, 2025 3:47pm"}`,
		`{"name":"c","is_user":false,"mes":42,"gen_started":null,"gen_finished":null,"swipes":"oops","swipe_info":"oops"}`,
		`{"name":"c","is_user":"yes","mes":"nonbool user flag"}`,
		`not json`,
		``,
		`{"name":"x","is_user":false,"mes":"huge","extra":{"pad":"` + strings.Repeat("z", 200000) + `"}}`,
	}
}

func TestAccumulateMatchesOracle(t *testing.T) {
	lines := statsFixture()
	content := strings.Join(lines, "\n")
	dir := t.TempDir()
	path := filepath.Join(dir, "chat.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	oldSeen := map[string]bool{}
	wantNums, wantFirst := oldAccumulate(content, oldSeen)

	newSeen := map[uint64]bool{}
	got := accumulateChatFile(path, newSeen)
	for k, want := range wantNums {
		if got.nums[k] != want {
			t.Fatalf("nums[%q]: got %v want %v", k, got.nums[k], want)
		}
	}
	for k, v := range got.nums {
		if _, ok := wantNums[k]; !ok && v != 0 {
			t.Fatalf("unexpected nonzero nums[%q] = %v", k, v)
		}
	}
	if got.firstChat != wantFirst {
		t.Fatalf("firstChat: got %v want %v", got.firstChat, wantFirst)
	}
}

func TestCollectChatStatsShape(t *testing.T) {
	root := t.TempDir()
	chars := filepath.Join(root, "characters")
	chats := filepath.Join(root, "chats")
	charDir := filepath.Join(chats, "Char")
	if err := os.MkdirAll(chars, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(charDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chars, "Char.png"), []byte("fakepng"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chars, "NoChats.png"), []byte("fakepng"), 0o644); err != nil {
		t.Fatal(err)
	}
	content := strings.Join(statsFixture(), "\n")
	if err := os.WriteFile(filepath.Join(charDir, "a.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(charDir, "b.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got := collectChatStats(chats, chars)
	char, ok := got["Char.png"].(map[string]any)
	if !ok {
		t.Fatalf("missing Char.png entry: %v", got)
	}
	// Duplicated file must not double-count messages (dedup spans files).
	if c := char["user_msg_count"]; c != 1.0 {
		t.Fatalf("user_msg_count = %v, want 1 (second file fully deduped)", c)
	}
	if c := char["non_user_msg_count"]; c != 6.0 {
		t.Fatalf("non_user_msg_count = %v, want 6", c)
	}
	if c := char["total_gen_time"]; c != 5000.0 {
		t.Fatalf("total_gen_time = %v, want 5000", c)
	}
	if c := char["total_swipe_count"]; c != 1.0 {
		t.Fatalf("total_swipe_count = %v, want 1", c)
	}
	if c := char["date_first_chat"]; c != float64(parseStatsTimestamp("February 25, 2025 3:46pm")) {
		t.Fatalf("date_first_chat = %v", c)
	}
	if _, ok := got["NoChats.png"]; !ok {
		t.Fatal("missing default entry for character without chats")
	}
	if _, ok := got["timestamp"]; !ok {
		t.Fatal("missing timestamp")
	}
}
