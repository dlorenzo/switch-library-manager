package db

import (
	"os"
	"testing"
)

func TestParseTitleIdFromFileName(t *testing.T) {
	fileName := "Super Mario [0100000000010000][v0].nsp"
	titleId, err := parseTitleIdFromFileName(fileName)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if titleId == nil || *titleId != "0100000000010000" {
		if titleId == nil {
			t.Fatalf("expected title ID not nil")
		}
		t.Fatalf("expected 0100000000010000 got %v", *titleId)
	}
}

func TestParseTitleIdFromFileNameInvalid(t *testing.T) {
	fileName := "Invalid [01000000000100,0][v0].nsp"
	_, err := parseTitleIdFromFileName(fileName)
	if err == nil {
		t.Fatalf("expected error for invalid title id")
	}
}

func TestPersistentDB_AddEntries(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "slm-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	pdb, err := NewPersistentDB(tmpDir)
	if err != nil {
		t.Fatalf("failed to create persistent DB: %v", err)
	}
	defer pdb.Close()

	tableName := "test-table"
	entries := map[string]interface{}{
		"key1": "value1",
		"key2": "value2",
		"key3": 12345,
	}

	err = pdb.AddEntries(tableName, entries)
	if err != nil {
		t.Fatalf("failed to add entries: %v", err)
	}

	var val1 string
	err = pdb.GetEntry(tableName, "key1", &val1)
	if err != nil {
		t.Fatalf("failed to get key1: %v", err)
	}
	if val1 != "value1" {
		t.Errorf("expected value1, got %s", val1)
	}

	var val3 int
	err = pdb.GetEntry(tableName, "key3", &val3)
	if err != nil {
		t.Fatalf("failed to get key3: %v", err)
	}
	if val3 != 12345 {
		t.Errorf("expected 12345, got %d", val3)
	}
}
