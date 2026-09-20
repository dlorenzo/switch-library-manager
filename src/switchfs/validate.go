package switchfs

import "errors"

const (
	maxNcaSectionSize   = 1 << 31 // 2 GiB - sanity cap for a single NCA FS section
	maxSplitParts       = 1024
	maxPfs0EntriesBytes = 4 << 20 // 4 MiB
)

// checkRange validates that [off, off+size) lies within [0, totalLen)
// without allowing uint64 wraparound to bypass the check.
func checkRange(off, size, totalLen uint64) error {
	if size > totalLen {
		return errors.New("offset/size out of range")
	}
	if off > totalLen {
		return errors.New("offset out of range")
	}
	if off+size < off {
		return errors.New("offset/size overflow")
	}
	if off+size > totalLen {
		return errors.New("range out of bounds")
	}
	return nil
}

// checkReadRange validates an absolute int64 offset/size pair for ReaderAt use.
func checkReadRange(off int64, size uint64) error {
	if off < 0 {
		return errors.New("negative offset")
	}
	if size > maxNcaSectionSize {
		return errors.New("section size exceeds sanity limit")
	}
	if size > 0 && uint64(off) > ^uint64(0)-size {
		return errors.New("offset/size overflow")
	}
	return nil
}
