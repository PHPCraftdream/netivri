package angel

import (
	"errors"
	"os"
	"testing"
)

func TestLogFollowerPollReturnsOnlyNewEntries(t *testing.T) {
	home := t.TempDir()
	writeLog(t, home, "2026-10-24T10:00:00Z INFO first\n2026-10-24T10:01:00Z INFO second\n")
	f := NewLogFollower(home)
	q := LogQuery{MinLevel: LevelInfo}
	entries, offset, err := f.Poll(0, q)
	if err != nil || len(entries) != 2 || entries[0].Message != "first" || entries[1].Message != "second" {
		t.Fatalf("entries = %+v err %v", entries, err)
	}
	size := int64(len("2026-10-24T10:00:00Z INFO first\n2026-10-24T10:01:00Z INFO second\n"))
	if offset != size {
		t.Fatalf("offset = %d, want %d", offset, size)
	}
	appendLog(t, home, "2026-10-24T10:02:00Z INFO third\n")
	entries, offset, err = f.Poll(offset, q)
	if err != nil || len(entries) != 1 || entries[0].Message != "third" {
		t.Fatalf("entries = %+v err %v", entries, err)
	}
	if offset != size+int64(len("2026-10-24T10:02:00Z INFO third\n")) {
		t.Fatalf("offset = %d", offset)
	}
}

func TestLogFollowerPollSkipsPartialLastLine(t *testing.T) {
	home := t.TempDir()
	writeLog(t, home, "2026-10-24T10:00:00Z INFO first\n2026-10-24T10:01:00Z INFO second\n")
	f := NewLogFollower(home)
	q := LogQuery{MinLevel: LevelInfo}
	_, offset, err := f.Poll(0, q)
	if err != nil {
		t.Fatal(err)
	}
	// A one-shot read keeps a last line without its newline; a poll must not.
	appendLog(t, home, "2026-10-24T10:02:00Z INFO partial-no-newline")
	partial, pollOffset, err := f.Poll(offset, q)
	if err != nil {
		t.Fatal(err)
	}
	read, _, err := ReadLog(home, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(read) != 3 || read[2].Message != "partial-no-newline" {
		t.Fatalf("ReadLog kept %d entries: %+v", len(read), read)
	}
	if len(partial) != 0 {
		t.Fatalf("poll returned the partial line: %+v", partial)
	}
	if pollOffset != offset {
		t.Fatalf("pollOffset = %d, want %d", pollOffset, offset)
	}
	appendLog(t, home, "\n2026-10-24T10:03:00Z INFO fourth\n")
	entries, _, err := f.Poll(pollOffset, q)
	if err != nil || len(entries) != 2 || entries[0].Message != "partial-no-newline" || entries[1].Message != "fourth" {
		t.Fatalf("entries = %+v err %v", entries, err)
	}
}

func TestLogFollowerPollFiltersLevel(t *testing.T) {
	home := t.TempDir()
	writeLog(t, home, "2026-10-24T10:00:00Z DEBUG noisy\n2026-10-24T10:01:00Z WARN careful\n")
	f := NewLogFollower(home)
	entries, _, err := f.Poll(0, LogQuery{MinLevel: LevelWarn})
	if err != nil || len(entries) != 1 || entries[0].Level != LevelWarn || entries[0].Message != "careful" {
		t.Fatalf("entries = %+v err %v", entries, err)
	}
}

func TestLogFollowerPollMissingLogIsErrNotExist(t *testing.T) {
	f := NewLogFollower(t.TempDir())
	_, _, err := f.Poll(0, LogQuery{})
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want os.ErrNotExist", err)
	}
}

func TestLogFollowerPollRestartsWithRotation(t *testing.T) {
	home := t.TempDir()
	writeLog(t, home, "2026-10-24T10:00:00Z INFO first\n2026-10-24T10:01:00Z INFO second\n")
	f := NewLogFollower(home)
	_, offset, err := f.Poll(0, LogQuery{})
	if err != nil {
		t.Fatal(err)
	}
	// Rotation: the file is replaced by a shorter one.
	writeLog(t, home, "2026-10-24T10:02:00Z INFO third\n")
	entries, _, err := f.Poll(offset, LogQuery{})
	if err != nil || len(entries) != 1 || entries[0].Message != "third" {
		t.Fatalf("entries = %+v err %v", entries, err)
	}
}
