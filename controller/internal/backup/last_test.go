package backup

import (
	"testing"
	"time"
)

// Last and Done: never, then now; the record itself is in no backup.
func TestLast(t *testing.T) {
	src := dataDir(t)
	if l := Last(src); !l.IsZero() {
		t.Fatalf("before any: %v", l)
	}
	_, before := backup(t, src, Options{})
	if err := Done(src); err != nil {
		t.Fatal(err)
	}
	if l := Last(src); l.IsZero() || time.Since(l) > time.Minute {
		t.Fatalf("after one: %v", l)
	}
	if _, after := backup(t, src, Options{}); after.Files != before.Files {
		t.Errorf("the record backed up: %d files, %d before", after.Files, before.Files)
	}
}
