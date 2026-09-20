package switchfs

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/firebat20/switch-library-manager/settings"
	"github.com/firebat20/switch-library-manager/switchfs/_crypto"
)

const (
	NcaSectionType_Code = iota
	NcaSectionType_Data
	NcaSectionType_Logo
)
const (
	NcaContentType_Program = iota
	NcaContentType_Meta
	NcaContentType_Control
	NcaContentType_Manual
	NcaContentType_Data
	NcaContentType_PublicData
)

func openMetaNcaDataSection(reader io.ReaderAt, ncaOffset int64) (*fsHeader, []byte, error) {
	if ncaOffset < 0 {
		return nil, nil, fmt.Errorf("invalid NCA offset %d", ncaOffset)
	}
	//read the NCA headerBytes
	encNcaHeader := make([]byte, 0xC00)
	n, err := reader.ReadAt(encNcaHeader, ncaOffset)

	if err != nil {
		return nil, nil, fmt.Errorf("failed to read NCA header at offset %d: %w", ncaOffset, err)
	}
	if n != 0xC00 {
		return nil, nil, fmt.Errorf("failed to read NCA header at offset %d: short read %d", ncaOffset, n)
	}

	keys, err := settings.SwitchKeys()
	if err != nil {
		return nil, nil, err
	}
	headerKey := keys.GetKey("header_key")
	if headerKey == "" {
		return nil, nil, errors.New("missing key - header_key")
	}
	ncaHeader, err := DecryptNcaHeader(headerKey, encNcaHeader)
	if err != nil {
		return nil, nil, err
	}

	if ncaHeader.HasRightsId() {
		//fail - need title keys
		return nil, nil, errors.New("non standard encryption is not supported")
	}

	/*if ncaHeader.contentType != NcaContentType_Meta {
		return nil, errors.New("not a meta NCA")
	}*/

	dataSectionIndex := 0

	fsHeader, err := getFsHeader(ncaHeader, dataSectionIndex)
	if err != nil {
		return nil, nil, err
	}

	entry, err := getFsEntry(ncaHeader, dataSectionIndex)
	if err != nil {
		return nil, nil, err
	}

	if entry.Size == 0 {
		return nil, nil, errors.New("empty section")
	}
	if entry.Size > maxNcaSectionSize {
		return nil, nil, fmt.Errorf("invalid section size %d", entry.Size)
	}
	if err := checkReadRange(ncaOffset+int64(entry.StartOffset), uint64(entry.Size)); err != nil {
		return nil, nil, err
	}

	encodedEntryContent := make([]byte, entry.Size)
	entryOffset := ncaOffset + int64(entry.StartOffset)
	n, err = reader.ReadAt(encodedEntryContent, entryOffset)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read NCA section at offset %d: %w", entryOffset, err)
	}
	if n != int(entry.Size) {
		return nil, nil, fmt.Errorf("short read of NCA section: got %d want %d", n, entry.Size)
	}
	if fsHeader.encType != 3 {
		return nil, nil, fmt.Errorf("non supported encryption type [encryption type:%d]", fsHeader.encType)
	}

	/*if fsHeader.hashType != 2 { //Sha256 (FS_TYPE_PFS0)
		return nil, errors.New("non FS_TYPE_PFS0")
	}*/
	decoded, err := decryptAesCtr(ncaHeader, fsHeader, entry.StartOffset, entry.Size, encodedEntryContent)
	if err != nil {
		return nil, nil, err
	}
	hashInfo, err := fsHeader.getHashInfo()
	if err != nil {
		return nil, nil, err
	}
	if err := checkRange(hashInfo.pfs0HeaderOffset, 0xC, uint64(len(decoded))); err != nil {
		return nil, nil, fmt.Errorf("invalid pfs0 header offset %d: %w", hashInfo.pfs0HeaderOffset, err)
	}

	return fsHeader, decoded[hashInfo.pfs0HeaderOffset:], nil
}

func decryptAesCtr(ncaHeader *ncaHeader, fsHeader *fsHeader, offset uint32, size uint32, encoded []byte) ([]byte, error) {
	if ncaHeader == nil || fsHeader == nil {
		return nil, errors.New("invalid NCA headers")
	}
	if uint64(size) != uint64(len(encoded)) {
		return nil, fmt.Errorf("encoded size mismatch: got %d want %d", len(encoded), size)
	}
	keyRevision := ncaHeader.getKeyRevision()
	cryptoType := ncaHeader.cryptoType

	if cryptoType != 0 {
		return []byte{}, errors.New("unsupported crypto type")
	}

	keys, err := settings.SwitchKeys()
	if err != nil {
		return nil, err
	}
	if keys == nil {
		return nil, errors.New("prod.keys not loaded")
	}

	keyName := fmt.Sprintf("key_area_key_application_%02x", keyRevision)
	KeyString := keys.GetKey(keyName)
	if KeyString == "" {
		return nil, errors.New(fmt.Sprintf("missing Key_area_key[%v]", keyName))
	}
	key, err := hex.DecodeString(KeyString)
	if err != nil {
		return nil, fmt.Errorf("invalid key %s: %w", keyName, err)
	}
	if len(key) != 16 {
		return nil, fmt.Errorf("invalid key length for %s: %d", keyName, len(key))
	}
	if len(ncaHeader.encryptedKeys) < 0x30 {
		return nil, errors.New("invalid NCA header: truncated encrypted keys")
	}

	decKey, err := _crypto.DecryptAes128Ecb(ncaHeader.encryptedKeys[0x20:0x30], key)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt key area: %w", err)
	}

	counter := make([]byte, 0x10)
	binary.BigEndian.PutUint64(counter, uint64(fsHeader.generation))
	binary.BigEndian.PutUint64(counter[8:], uint64(offset/0x10))

	c, err := aes.NewCipher(decKey)
	if err != nil {
		return nil, fmt.Errorf("failed to init cipher: %w", err)
	}

	decContent := make([]byte, size)

	s := cipher.NewCTR(c, counter)
	s.XORKeyStream(decContent, encoded[0:size])

	return decContent, nil
}
