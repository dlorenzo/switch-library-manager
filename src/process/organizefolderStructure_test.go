package process

import (
	"os"
	"robpike.io/nihongo"
	"strings"
	"testing"
)

//var folderIllegalCharsRegex = regexp.MustCompile(`[./\\?%*:;=|"<>]`)

func TestRename(t *testing.T) {
	name := "Pokémon™: Let’s Go, Eevee! 포탈 나이츠"
	name = folderIllegalCharsRegex.ReplaceAllString(name, "")
	safe := cjk.FindAllString(name, -1)
	name = strings.Join(safe, "")
	name = nihongo.RomajiString(name)
}

func TestIsSplitPart(t *testing.T) {
	tests := []struct {
		fileName string
		prefix   string
		expected bool
	}{
		{"game.nsp.00", "game.nsp.", true},
		{"game.nsp.01", "game.nsp.", true},
		{"game.nsp.10", "game.nsp.", true},
		{"another_game.nsp.00", "game.nsp.", false},
		{"game.nsp.00.bak", "game.nsp.", false},
		{"00", "", true},
		{"01", "", true},
		{"readme.txt", "", false},
		{"game.nsp.00", "another.", false},
	}

	for _, tc := range tests {
		result := isSplitPart(tc.fileName, tc.prefix)
		if result != tc.expected {
			t.Errorf("isSplitPart(%q, %q) = %v; want %v", tc.fileName, tc.prefix, result, tc.expected)
		}
	}
}

func TestMoveFile(t *testing.T) {
	tempDir := t.TempDir()
	src := tempDir + "/source.txt"
	dst := tempDir + "/destination.txt"

	content := []byte("hello switch library manager")
	if err := os.WriteFile(src, content, 0644); err != nil {
		t.Fatalf("failed to write source: %v", err)
	}

	if err := moveFile(src, dst); err != nil {
		t.Fatalf("moveFile failed: %v", err)
	}

	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("expected source to no longer exist after move")
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("failed to read destination: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("expected %q, got %q", content, got)
	}
}

func TestCopyAndDelete(t *testing.T) {
	tempDir := t.TempDir()
	src := tempDir + "/src_copy.txt"
	dst := tempDir + "/dst_copy.txt"

	content := []byte("cross device test data")
	if err := os.WriteFile(src, content, 0644); err != nil {
		t.Fatalf("failed to write source: %v", err)
	}

	if err := copyAndDelete(src, dst); err != nil {
		t.Fatalf("copyAndDelete failed: %v", err)
	}

	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("expected source to no longer exist after copyAndDelete")
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("failed to read destination: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("expected %q, got %q", content, got)
	}
}
