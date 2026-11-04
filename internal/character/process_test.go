package character

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/TurtleTavern/turtletavern/internal/models"
)

func TestToShallowKeepsInt64ChatSizeAndIntDataSize(t *testing.T) {
	sc := toShallow(map[string]any{
		"name":           "Card",
		"avatar":         "card.png",
		"date_added":     float64(1700000000000),
		"date_last_chat": float64(1700000001000),
		"chat_size":      int64(1536),
		"data_size":      int(2048),
	})

	if sc.ChatSize != 1536 {
		t.Fatalf("ChatSize = %d, want 1536 (a concrete int64 must not collapse to 0)", sc.ChatSize)
	}
	if sc.DataSize != 2048 {
		t.Fatalf("DataSize = %d, want 2048 (a concrete int must not collapse to 0)", sc.DataSize)
	}
	if sc.DateLastChat != 1700000001000 {
		t.Fatalf("DateLastChat = %v, want 1700000001000", sc.DateLastChat)
	}
}

func TestProcessCharacterPopulatesChatSize(t *testing.T) {
	root := t.TempDir()
	charsDir := filepath.Join(root, "characters")
	chatsDir := filepath.Join(root, "chats")
	if err := os.MkdirAll(charsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(chatsDir, "Card"), 0o755); err != nil {
		t.Fatal(err)
	}

	dirs := models.UserDirectories{Characters: charsDir, Chats: chatsDir}
	card := `{"spec":"chara_card_v2","spec_version":"2.0","data":{"name":"Card","description":"d"}}`
	if err := WriteCharacterDataToFile(nil, card, "Card", dirs); err != nil {
		t.Fatalf("write card: %v", err)
	}

	chatPath := filepath.Join(chatsDir, "Card", "chat1.jsonl")
	msg := `{"name":"Card","is_user":false,"send_date":1700000000000,"mes":"hi"}`
	if err := os.WriteFile(chatPath, []byte(msg+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(chatPath)
	if err != nil {
		t.Fatal(err)
	}

	sc, err := ProcessCharacter("Card.png", dirs, true)
	if err != nil {
		t.Fatalf("process character: %v", err)
	}
	if sc.ChatSize != info.Size() {
		t.Fatalf("ChatSize = %d, want %d", sc.ChatSize, info.Size())
	}
	if sc.DateLastChat == 0 {
		t.Fatalf("DateLastChat = 0, want the message send_date")
	}
}

func TestProcessCharacterRepairsZeroDateAdded(t *testing.T) {
	root := t.TempDir()
	charsDir := filepath.Join(root, "characters")
	chatsDir := filepath.Join(root, "chats")
	if err := os.MkdirAll(charsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(chatsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	dirs := models.UserDirectories{Characters: charsDir, Chats: chatsDir}
	card := `{"spec":"chara_card_v2","spec_version":"2.0","data":{"name":"Card","description":"d"}}`
	if err := WriteCharacterDataToFile(nil, card, "Card", dirs); err != nil {
		t.Fatalf("write card: %v", err)
	}

	// Poison the entry the way legacy birthtime-less files did: a stored 0.
	SetDateAddedEntry(charsDir, "Card", 0)

	sc, err := ProcessCharacter("Card.png", dirs, true)
	if err != nil {
		t.Fatalf("process character: %v", err)
	}
	if sc.DateAdded <= 0 {
		t.Fatalf("DateAdded = %v, want the file mtime fallback (a 0 must never stick)", sc.DateAdded)
	}
	if cd, ok := toFloat64(sc.CreateDate); !ok || cd <= 0 {
		t.Fatalf("CreateDate = %v, want a repaired non-zero date", sc.CreateDate)
	}

	// A second pass (what a rebuild does) must keep the repaired date,
	// not flip back to 0.
	sc2, err := ProcessCharacter("Card.png", dirs, true)
	if err != nil {
		t.Fatalf("process character again: %v", err)
	}
	if sc2.DateAdded != sc.DateAdded {
		t.Fatalf("DateAdded changed across passes: %v vs %v (repaired date must stick)", sc2.DateAdded, sc.DateAdded)
	}
}

func TestProcessCharacterIgnoresZeroSendDate(t *testing.T) {
	root := t.TempDir()
	charsDir := filepath.Join(root, "characters")
	chatsDir := filepath.Join(root, "chats")
	if err := os.MkdirAll(charsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(chatsDir, "Card"), 0o755); err != nil {
		t.Fatal(err)
	}

	dirs := models.UserDirectories{Characters: charsDir, Chats: chatsDir}
	card := `{"spec":"chara_card_v2","spec_version":"2.0","data":{"name":"Card","description":"d"}}`
	if err := WriteCharacterDataToFile(nil, card, "Card", dirs); err != nil {
		t.Fatalf("write card: %v", err)
	}

	chatPath := filepath.Join(chatsDir, "Card", "chat1.jsonl")
	msg := `{"name":"Card","is_user":false,"send_date":0,"mes":"hi"}`
	if err := os.WriteFile(chatPath, []byte(msg+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sc, err := ProcessCharacter("Card.png", dirs, true)
	if err != nil {
		t.Fatalf("process character: %v", err)
	}
	if sc.DateAdded <= 0 {
		t.Fatalf("DateAdded = %v, want mtime fallback (send_date 0 is not a date)", sc.DateAdded)
	}
}

func TestProcessCharacterPrefersCardDateOverMtime(t *testing.T) {
	root := t.TempDir()
	charsDir := filepath.Join(root, "characters")
	chatsDir := filepath.Join(root, "chats")
	if err := os.MkdirAll(charsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(chatsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	dirs := models.UserDirectories{Characters: charsDir, Chats: chatsDir}
	// A group-chat-only character: no solo chats dir, but an old embedded
	// import date. A rebuild must restore that date, not stamp the restore
	// copy time (mtime) and push it as new.
	card := `{"spec":"chara_card_v2","spec_version":"2.0","create_date":"2020-05-01T12:00:00.000Z","data":{"name":"Card","description":"d"}}`
	if err := WriteCharacterDataToFile(nil, card, "Card", dirs); err != nil {
		t.Fatalf("write card: %v", err)
	}
	want, err := time.Parse(time.RFC3339Nano, "2020-05-01T12:00:00.000Z")
	if err != nil {
		t.Fatal(err)
	}

	SetDateAddedEntry(charsDir, "Card", 0)

	sc, err := ProcessCharacter("Card.png", dirs, true)
	if err != nil {
		t.Fatalf("process character: %v", err)
	}
	if sc.DateAdded != float64(want.UnixMilli()) {
		t.Fatalf("DateAdded = %v, want card date %v (mtime must not promote old cards to new)", sc.DateAdded, want.UnixMilli())
	}
}
