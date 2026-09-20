package switchfs

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/avast/retry-go/v5"
)

type ReadAtCloser interface {
	io.ReaderAt
	io.Closer
}

type splitFile struct {
	info      []os.FileInfo
	files     []ReadAtCloser
	path      string
	chunkSize int64
	mu        sync.Mutex
}

type fileWrapper struct {
	file ReadAtCloser
	path string
}

func NewFileWrapper(filePath string) (*fileWrapper, error) {
	result := fileWrapper{}
	result.path = filePath
	file, err := _openFile(filePath)
	if err != nil {
		return nil, err
	}
	result.file = file
	return &result, nil
}

func (sp *fileWrapper) ReadAt(p []byte, off int64) (n int, err error) {
	if sp.file != nil {
		return sp.file.ReadAt(p, off)
	}
	return 0, errors.New("file is not opened")
}

func (sp *fileWrapper) Close() error {

	if sp.file != nil {
		return sp.file.Close()
	}

	return nil
}

func isSplitPart(fileName string, prefix string) bool {
	if !strings.HasPrefix(fileName, prefix) {
		return false
	}
	suffix := fileName[len(prefix):]
	if len(suffix) == 0 {
		return false
	}
	for i := 0; i < len(suffix); i++ {
		if suffix[i] < '0' || suffix[i] > '9' {
			return false
		}
	}
	return true
}

func NewSplitFileReader(filePath string) (*splitFile, error) {
	result := splitFile{}
	dir := filepath.Dir(filePath)
	baseName := filepath.Base(filePath)

	i := len(baseName) - 1
	for i >= 0 && baseName[i] >= '0' && baseName[i] <= '9' {
		i--
	}
	prefix := baseName[:i+1]

	files, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	type partInfo struct {
		info os.FileInfo
		num  int
	}
	var matchedParts []partInfo
	maxPart := -1

	for _, file := range files {
		if file.IsDir() {
			continue
		}
		name := file.Name()
		if isSplitPart(name, prefix) {
			suffix := name[len(prefix):]
			partNum, err := strconv.Atoi(suffix)
			if err == nil {
				info, err := file.Info()
				if err == nil {
					matchedParts = append(matchedParts, partInfo{info: info, num: partNum})
					if partNum > maxPart {
						maxPart = partNum
					}
				}
			}
		}
	}

	if len(matchedParts) == 0 {
		return nil, errors.New("no split files found")
	}
	if maxPart >= maxSplitParts {
		return nil, fmt.Errorf("too many split parts: %d (max %d)", maxPart+1, maxSplitParts)
	}
	// Guard against sparse attacker-controlled numbering like "file.999999999"
	// creating a giant slice via maxPart+1.
	if len(matchedParts) < maxPart+1 && maxPart > 2*len(matchedParts)+10 {
		return nil, fmt.Errorf("sparse split numbering: max=%d count=%d", maxPart, len(matchedParts))
	}

	result.path = dir
	result.info = make([]os.FileInfo, maxPart+1)
	result.files = make([]ReadAtCloser, maxPart+1)

	for _, p := range matchedParts {
		if p.num < 0 || p.num > maxPart {
			continue
		}
		result.info[p.num] = p.info
	}

	// Chunk size must come from part 0; other parts may be short (last part).
	if result.info[0] == nil {
		return nil, errors.New("split set is missing part 0")
	}
	result.chunkSize = result.info[0].Size()

	if result.chunkSize == 0 {
		return nil, errors.New("chunk size is 0 or no parts found")
	}

	return &result, nil
}

// ReadAt implements io.ReaderAt across all parts of the split file.
//
// The previous implementation delegated the entire read to the single part
// containing the starting offset. os.File.ReadAt returns a short read + io.EOF
// at the end of that part even though the data continues in the next part, so
// any read that happened to straddle a part boundary failed spuriously. The
// io.ReaderAt contract requires either filling p completely or returning a
// non-nil error, so this version loops across parts until p is full, the file
// genuinely ends (io.EOF), or a real error occurs.
func (sp *splitFile) ReadAt(p []byte, off int64) (n int, err error) {
	if off < 0 {
		return 0, errors.New("negative offset")
	}
	if sp.chunkSize <= 0 {
		return 0, errors.New("invalid chunk size")
	}

	lastPart := len(sp.info) - 1

	for n < len(p) {
		pos := off + int64(n)
		if pos < off {
			return n, errors.New("offset overflow")
		}
		part := int(pos / sp.chunkSize)

		// Past the final part => genuine end of the (virtual) file.
		if part > lastPart {
			return n, io.EOF
		}
		if part < 0 || sp.info[part] == nil {
			// A hole in the middle of the sequence is a broken split set, not EOF.
			return n, errors.New("missing part " + strconv.Itoa(part))
		}

		sp.mu.Lock()
		if sp.files[part] == nil {
			file, ferr := _openFile(filepath.Join(sp.path, sp.info[part].Name()))
			if ferr != nil {
				sp.mu.Unlock()
				return n, ferr
			}
			sp.files[part] = file
		}
		f := sp.files[part]
		sp.mu.Unlock()

		partOff := pos - sp.chunkSize*int64(part)
		partSize := sp.info[part].Size()

		if partOff >= partSize {
			if part == lastPart {
				// Reading at/past the end of the final (possibly shorter) part.
				return n, io.EOF
			}
			// A middle part shorter than chunkSize means the set is truncated.
			return n, io.ErrUnexpectedEOF
		}

		// Clamp this iteration's read to what the current part can provide.
		toRead := int64(len(p) - n)
		if remain := partSize - partOff; toRead > remain {
			toRead = remain
		}

		m, rerr := f.ReadAt(p[n:n+int(toRead)], partOff)
		n += m

		if int64(m) < toRead {
			// The part is shorter than its recorded size (changed on disk?).
			if rerr == nil {
				rerr = io.ErrUnexpectedEOF
			}
			if rerr == io.EOF && part != lastPart {
				rerr = io.ErrUnexpectedEOF
			}
			return n, rerr
		}

		// Full clamped read succeeded. An io.EOF here just means we consumed
		// the part exactly to its end; if more parts follow, keep going. If it
		// was the last part and the caller wanted more, the next loop
		// iteration (or the check below) reports io.EOF.
		if rerr != nil && rerr != io.EOF {
			return n, rerr
		}
	}

	return n, nil
}

func _openFile(path string) (*os.File, error) {
	var file *os.File
	var err error
	err = retry.New(retry.Attempts(5)).Do(
		func() error {
			file, err = os.Open(path)
			return err
		},
	)
	return file, err
}

func (sp *splitFile) Close() error {
	var firstErr error
	sp.mu.Lock()
	defer sp.mu.Unlock()
	for _, file := range sp.files {
		if file != nil {
			if err := file.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func OpenFile(filePath string) (ReadAtCloser, error) {
	if len(filePath) == 0 {
		return nil, errors.New("empty file path")
	}
	//check if it's a split file
	if _, err := strconv.Atoi(filePath[len(filePath)-1:]); err == nil {
		return NewSplitFileReader(filePath)
	} else {
		return NewFileWrapper(filePath)
	}
}
