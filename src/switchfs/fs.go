package switchfs

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
)

type fsHeader struct {
	encType       byte //(0 = Auto, 1 = None, 2 = AesCtrOld, 3 = AesCtr, 4 = AesCtrEx)
	fsType        byte //(0 = RomFs, 1 = PartitionFs)
	hashType      byte // (0 = Auto, 2 = HierarchicalSha256, 3 = HierarchicalIntegrity (Ivfc))
	fsHeaderBytes []byte
	generation    uint32
}

type fsEntry struct {
	StartOffset uint32
	EndOffset   uint32
	Size        uint32
}

type hashInfo struct {
	pfs0HeaderOffset uint64
	pfs0size         uint64
}

func getFsEntry(ncaHeader *ncaHeader, index int) (fsEntry, error) {
	if ncaHeader == nil || len(ncaHeader.headerBytes) < 0xC00 {
		return fsEntry{}, errors.New("invalid NCA header")
	}
	if index < 0 || index >= 4 {
		return fsEntry{}, errors.New("invalid FS section index")
	}
	fsEntryOffset := 0x240 + 0x10*index
	fsEntryBytes := ncaHeader.headerBytes[fsEntryOffset : fsEntryOffset+0x10]

	entryStartOffset := uint64(binary.LittleEndian.Uint32(fsEntryBytes[0x0:0x4])) * 0x200
	entryEndOffset := uint64(binary.LittleEndian.Uint32(fsEntryBytes[0x4:0x8])) * 0x200
	if entryEndOffset < entryStartOffset {
		return fsEntry{}, errors.New("invalid FS entry: end before start")
	}
	size := entryEndOffset - entryStartOffset
	if size > maxNcaSectionSize {
		return fsEntry{}, errors.New("invalid FS entry: size exceeds sanity limit")
	}
	if entryStartOffset > 0xFFFFFFFF || entryEndOffset > 0xFFFFFFFF {
		return fsEntry{}, errors.New("invalid FS entry: offset out of range")
	}

	return fsEntry{StartOffset: uint32(entryStartOffset), EndOffset: uint32(entryEndOffset), Size: uint32(size)}, nil
}

func getFsHeader(ncaHeader *ncaHeader, index int) (*fsHeader, error) {
	if ncaHeader == nil || len(ncaHeader.headerBytes) < 0xC00 {
		return nil, errors.New("invalid NCA header")
	}
	if index < 0 || index >= 4 {
		return nil, errors.New("invalid FS section index")
	}
	fsHeaderHashOffset := /*hash pfs0HeaderOffset*/ 0x280 + /*hash pfs0size*/ 0x20*index
	fsHeaderHash := ncaHeader.headerBytes[fsHeaderHashOffset : fsHeaderHashOffset+0x20]

	fsHeaderOffset := 0x400 + 0x200*index
	fsHeaderBytes := ncaHeader.headerBytes[fsHeaderOffset : fsHeaderOffset+0x200]

	actualHash := sha256.Sum256(fsHeaderBytes)

	if !bytes.Equal(actualHash[:], fsHeaderHash) {
		return nil, errors.New("fs headerBytes hash mismatch")
	}

	result := fsHeader{fsHeaderBytes: fsHeaderBytes}

	result.fsType = fsHeaderBytes[0x2:0x3][0]
	result.hashType = fsHeaderBytes[0x3:0x4][0]
	result.encType = fsHeaderBytes[0x4:0x5][0]

	generationBytes := fsHeaderBytes[0x140 : 0x140+0x4] //generation
	result.generation = binary.LittleEndian.Uint32(generationBytes)

	return &result, nil
}

func (fh *fsHeader) getHashInfo() (*hashInfo, error) {
	if fh == nil || len(fh.fsHeaderBytes) < 0x200 {
		return nil, errors.New("invalid FS header")
	}
	hashInfoBytes := fh.fsHeaderBytes[0x8:0x100]
	result := hashInfo{}
	if fh.hashType == 2 {

		result.pfs0HeaderOffset = binary.LittleEndian.Uint64(hashInfoBytes[0x38 : 0x38+0x8])
		result.pfs0size = binary.LittleEndian.Uint64(hashInfoBytes[0x40 : 0x40+0x8])
		return &result, nil
	} else if fh.hashType == 3 {
		result.pfs0HeaderOffset = binary.LittleEndian.Uint64(hashInfoBytes[0x88 : 0x88+0x8])
		result.pfs0size = binary.LittleEndian.Uint64(hashInfoBytes[0x90 : 0x90+0x8])
		return &result, nil
	}
	return nil, errors.New("non supported hash type")
}
