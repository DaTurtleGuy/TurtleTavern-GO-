package character

import "testing"

func TestNeedsRebuildOnIndexVersionMismatch(t *testing.T) {
	idx, err := OpenIndex(t.TempDir())
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	defer idx.Close()

	userFolder := t.TempDir()
	if idx.NeedsRebuild(userFolder) {
		t.Fatalf("fresh index at the current version must not rebuild an empty folder")
	}

	if _, err := idx.db.Exec("UPDATE meta SET value=? WHERE key='version'", indexVersion-1); err != nil {
		t.Fatalf("downgrade stored version: %v", err)
	}
	if !idx.NeedsRebuild(userFolder) {
		t.Fatalf("a stale index version must force a rebuild")
	}
}
