package switchfs

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// buildSplitSet writes a split-file set into a temp dir and returns the path
// of part 0 plus the concatenated payload. partSizes[i] is the size of part i;
// a size of -1 means "do not create this part" (hole in the sequence).
func buildSplitSet(t *testing.T, partSizes []int) (string, []byte) {
	t.Helper()
	dir := t.TempDir()

	var payload []byte
	next := byte(0)
	for i, size := range partSizes {
		if size < 0 {
			continue
		}
		data := make([]byte, size)
		for j := range data {
			data[j] = next
			next++
		}
		payload = append(payload, data...)
		name := filepath.Join(dir, "data.bin."+string(rune('0'+i)))
		if err := os.WriteFile(name, data, 0600); err != nil {
			t.Fatalf("failed to write part %d: %v", i, err)
		}
	}
	return filepath.Join(dir, "data.bin.0"), payload
}

func TestSplitFileReadAt(t *testing.T) {
	// parts: 10 + 10 + 5 bytes => virtual file of 25 sequential bytes
	part0Path, payload := buildSplitSet(t, []int{10, 10, 5})

	r, err := NewSplitFileReader(part0Path)
	if err != nil {
		t.Fatalf("failed to open split reader: %v", err)
	}
	defer r.Close()

	cases := []struct {
		name    string
		off     int64
		length  int
		wantN   int
		wantErr error // nil means "nil error required"; io.EOF means EOF required
	}{
		{name: "within single part", off: 2, length: 5, wantN: 5, wantErr: nil},
		{name: "spanning first boundary", off: 5, length: 10, wantN: 10, wantErr: nil},
		{name: "spanning two boundaries", off: 8, length: 14, wantN: 14, wantErr: nil},
		{name: "entire file", off: 0, length: 25, wantN: 25, wantErr: nil},
		{name: "starting exactly on boundary", off: 10, length: 5, wantN: 5, wantErr: nil},
		{name: "exact read to end of file", off: 20, length: 5, wantN: 5, wantErr: nil},
		{name: "read past end returns partial and EOF", off: 20, length: 10, wantN: 5, wantErr: io.EOF},
		{name: "read entirely past end", off: 30, length: 4, wantN: 0, wantErr: io.EOF},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := make([]byte, tc.length)
			n, err := r.ReadAt(buf, tc.off)

			if n != tc.wantN {
				t.Fatalf("n = %d, want %d (err=%v)", n, tc.wantN, err)
			}
			if tc.wantErr == nil && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantErr == io.EOF && err != io.EOF {
				t.Fatalf("err = %v, want io.EOF", err)
			}
			if !bytes.Equal(buf[:n], payload[tc.off:tc.off+int64(n)]) {
				t.Fatalf("data mismatch at off=%d len=%d", tc.off, n)
			}
		})
	}
}

func TestSplitFileReadAtMissingMiddlePart(t *testing.T) {
	// part 1 is absent: reads that need it must fail with a real error, not EOF
	part0Path, _ := buildSplitSet(t, []int{10, -1, 10})

	r, err := NewSplitFileReader(part0Path)
	if err != nil {
		t.Fatalf("failed to open split reader: %v", err)
	}
	defer r.Close()

	// Read that starts in part 0 and crosses into the missing part 1.
	buf := make([]byte, 15)
	n, err := r.ReadAt(buf, 5)
	if err == nil || err == io.EOF {
		t.Fatalf("expected a hard error for missing middle part, got n=%d err=%v", n, err)
	}
	if n != 5 {
		t.Fatalf("expected the 5 available bytes from part 0, got n=%d", n)
	}
}

func TestSplitFileReadAtNegativeOffset(t *testing.T) {
	part0Path, _ := buildSplitSet(t, []int{10, 10})

	r, err := NewSplitFileReader(part0Path)
	if err != nil {
		t.Fatalf("failed to open split reader: %v", err)
	}
	defer r.Close()

	if _, err := r.ReadAt(make([]byte, 4), -1); err == nil {
		t.Fatal("expected an error for negative offset")
	}
}
