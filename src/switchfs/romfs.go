package switchfs

import (
	"encoding/binary"
	"errors"
	"fmt"
)

type RomfsHeader struct {
	HeaderSize          uint64
	DirHashTableOffset  uint64
	DirHashTableSize    uint64
	DirMetaTableOffset  uint64
	DirMetaTableSize    uint64
	FileHashTableOffset uint64
	FileHashTableSize   uint64
	FileMetaTableOffset uint64
	FileMetaTableSize   uint64
	DataOffset          uint64
}

type RomfsFileEntry struct {
	parent    uint32
	sibling   uint32
	offset    uint64
	size      uint64
	hash      uint32
	name_size uint32
	name      string
}

func readRomfsHeader(data []byte) (RomfsHeader, error) {
	header := RomfsHeader{}
	// The header we read spans 10 uint64 fields (0x50 bytes).
	if len(data) < 0x50 {
		return header, errors.New("failed to read romfs: truncated header")
	}
	header.HeaderSize = binary.LittleEndian.Uint64(data[0x0+(0x8*0) : 0x0+(0x8*1)])
	header.DirHashTableOffset = binary.LittleEndian.Uint64(data[0x0+(0x8*1) : 0x0+(0x8*2)])
	header.DirHashTableSize = binary.LittleEndian.Uint64(data[0x0+(0x8*2) : 0x0+(0x8*3)])
	header.DirMetaTableOffset = binary.LittleEndian.Uint64(data[0x0+(0x8*3) : 0x0+(0x8*4)])
	header.DirMetaTableSize = binary.LittleEndian.Uint64(data[0x0+(0x8*4) : 0x0+(0x8*5)])
	header.FileHashTableOffset = binary.LittleEndian.Uint64(data[0x0+(0x8*5) : 0x0+(0x8*6)])
	header.FileHashTableSize = binary.LittleEndian.Uint64(data[0x0+(0x8*6) : 0x0+(0x8*7)])
	header.FileMetaTableOffset = binary.LittleEndian.Uint64(data[0x0+(0x8*7) : 0x0+(0x8*8)])
	header.FileMetaTableSize = binary.LittleEndian.Uint64(data[0x0+(0x8*8) : 0x0+(0x8*9)])
	header.DataOffset = binary.LittleEndian.Uint64(data[0x0+(0x8*9) : 0x0+(0x8*10)])
	return header, nil
}

func readRomfsFileEntry(data []byte, header RomfsHeader) (map[string]RomfsFileEntry, error) {
	if err := checkRange(header.FileMetaTableOffset, header.FileMetaTableSize, uint64(len(data))); err != nil {
		return nil, fmt.Errorf("failed to read romfs: %w", err)
	}
	dirBytes := data[header.FileMetaTableOffset : header.FileMetaTableOffset+header.FileMetaTableSize]
	result := map[string]RomfsFileEntry{}
	dirLen := uint64(len(dirBytes))
	offset := uint64(0x0)
	for offset < dirLen {
		// Need at least the 0x20-byte fixed entry header before reading fields.
		if offset+0x20 > dirLen || offset+0x20 < offset {
			return nil, errors.New("failed to read romfs: truncated entry")
		}
		entry := RomfsFileEntry{}
		entry.parent = binary.LittleEndian.Uint32(dirBytes[offset : offset+0x4])
		entry.sibling = binary.LittleEndian.Uint32(dirBytes[offset+0x4 : offset+0x8])
		entry.offset = binary.LittleEndian.Uint64(dirBytes[offset+0x8 : offset+0x10])
		entry.size = binary.LittleEndian.Uint64(dirBytes[offset+0x10 : offset+0x18])
		entry.hash = binary.LittleEndian.Uint32(dirBytes[offset+0x18 : offset+0x1C])
		entry.name_size = binary.LittleEndian.Uint32(dirBytes[offset+0x1C : offset+0x20])

		// The name length is file-controlled; bound it against the buffer.
		nameStart := offset + 0x20
		nameEnd := nameStart + uint64(entry.name_size)
		if nameEnd < nameStart || nameEnd > dirLen {
			return nil, errors.New("failed to read romfs: entry name out of range")
		}
		entry.name = string(dirBytes[nameStart:nameEnd])
		result[entry.name] = entry
		offset = nameEnd
	}
	return result, nil

	//fmt.Println(string(section[DataOffset+offset+0x3060:DataOffset+offset+0x3060 +0x10]))
}
