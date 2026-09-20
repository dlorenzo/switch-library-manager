package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCreateCsvFile_InvalidPath(t *testing.T) {
	// A path in a non-existent directory without mkdir should fail gracefully and return nil
	invalidPath := filepath.Join(t.TempDir(), "nonexistent_dir", "sub", "output.csv")
	csv := CreateCsvFile(invalidPath, []string{"Header1", "Header2"})
	if csv != nil {
		t.Fatalf("expected nil CsvFile for invalid path, got %v", csv)
	}

	// Calling Close() on nil should not panic
	csv.Close()
	// Calling Write() on nil should not panic
	csv.Write([]string{"A", "B"})
}

func TestCreateCsvFile_Success(t *testing.T) {
	tmpDir := t.TempDir()
	validPath := filepath.Join(tmpDir, "output.csv")
	csv := CreateCsvFile(validPath, []string{"Header1", "Header2"})
	if csv == nil {
		t.Fatalf("expected non-nil CsvFile for valid path")
	}

	csv.Write([]string{"val1", "val2"})
	csv.Close()

	content, err := os.ReadFile(validPath)
	if err != nil {
		t.Fatalf("failed to read created csv: %v", err)
	}
	expected := "Header1,Header2\nval1,val2\n"
	if string(content) != expected {
		t.Errorf("expected %q, got %q", expected, string(content))
	}
}
