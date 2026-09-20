package switchfs

import (
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"strings"
)

const (
	ContentMetaType_SystemProgram        = 1
	ContentMetaType_SystemData           = 2
	ContentMetaType_SystemUpdate         = 3
	ContentMetaType_BootImagePackage     = 4
	ContentMetaType_BootImagePackageSafe = 5
	ContentMetaType_Application          = 0x80
	ContentMetaType_Patch                = 0x81
	ContentMetaType_AddOnContent         = 0x82
	ContentMetaType_Delta                = 0x83
)

type Content struct {
	Text          string `xml:",chardata"`
	Type          string `xml:"Type"`
	ID            string `xml:"Id"`
	Size          string `xml:"Size"`
	Hash          string `xml:"Hash"`
	KeyGeneration string `xml:"KeyGeneration"`
}

type ContentMetaAttributes struct {
	TitleId  string `json:"title_id"`
	Version  int    `json:"version"`
	Type     string `json:"type"`
	Contents map[string]Content
	Ncap     *Nacp
}

type ContentMeta struct {
	XMLName                       xml.Name `xml:"ContentMeta"`
	Text                          string   `xml:",chardata"`
	Type                          string   `xml:"Type"`
	ID                            string   `xml:"Id"`
	Version                       int      `xml:"Version"`
	RequiredDownloadSystemVersion string   `xml:"RequiredDownloadSystemVersion"`
	Content                       []struct {
		Text          string `xml:",chardata"`
		Type          string `xml:"Type"`
		ID            string `xml:"Id"`
		Size          string `xml:"Size"`
		Hash          string `xml:"Hash"`
		KeyGeneration string `xml:"KeyGeneration"`
	} `xml:"Content"`
	Digest                string `xml:"Digest"`
	KeyGenerationMin      string `xml:"KeyGenerationMin"`
	RequiredSystemVersion string `xml:"RequiredSystemVersion"`
	OriginalId            string `xml:"OriginalId"`
}

func readBinaryCnmt(pfs0 *PFS0, data []byte) (*ContentMetaAttributes, error) {
	if pfs0 == nil || len(pfs0.Files) != 1 {
		return nil, errors.New("unexpected pfs0")
	}
	cnmtFile := pfs0.Files[0]
	if cnmtFile.StartOffset > uint64(len(data)) {
		return nil, errors.New("invalid cnmt: start offset out of range")
	}
	cnmt := data[int(cnmtFile.StartOffset):]
	// The cnmt header is 0x20 bytes; require at least that before reading fixed fields.
	if len(cnmt) < 0x20 {
		return nil, errors.New("invalid cnmt: truncated header")
	}
	titleId := binary.LittleEndian.Uint64(cnmt[0:0x8])
	version := binary.LittleEndian.Uint32(cnmt[0x8:0xC])
	tableOffset := binary.LittleEndian.Uint16(cnmt[0xE:0x10])
	contentEntryCount := binary.LittleEndian.Uint16(cnmt[0x10:0x12])
	//metaEntryCount := binary.LittleEndian.Uint16(cnmt[0x12:0x14])
	contents := map[string]Content{}
	for i := uint16(0); i < contentEntryCount; i++ {
		// Compute the entry position in a wide int so the uint16 math cannot wrap,
		// then bound-check against the buffer before slicing.
		position := int(0x20) /*size of cnmt header*/ + int(tableOffset) + int(i)*0x38
		if position+0x38 > len(cnmt) || position < 0 {
			return nil, errors.New("invalid cnmt: content entry out of range")
		}
		ncaId := cnmt[position+0x20 : position+0x20+0x10]
		//fmt.Println(fmt.Sprintf("0%x", ncaId))
		contentType := ""
		switch cnmt[position+0x36 : position+0x36+1][0] {
		case 0:
			contentType = "Meta"
		case 1:
			contentType = "Program"
		case 2:
			contentType = "Data"
		case 3:
			contentType = "Control"
		case 4:
			contentType = "HtmlDocument"
		case 5:
			contentType = "LegalInformation"
		case 6:
			contentType = "DeltaFragment"
		default:
			return nil, fmt.Errorf("invalid cnmt: unknown content type %d", cnmt[position+0x36])
		}
		contents[contentType] = Content{ID: fmt.Sprintf("%x", ncaId)}
	}
	metaType := ""
	switch cnmt[0xC:0xD][0] {
	case ContentMetaType_Application:
		metaType = "BASE"
	case ContentMetaType_AddOnContent:
		metaType = "DLC"
	case ContentMetaType_Patch:
		metaType = "UPD"
	default:
		return nil, fmt.Errorf("invalid cnmt: unknown meta type 0x%x", cnmt[0xC])
	}

	return &ContentMetaAttributes{Contents: contents, Version: int(version), TitleId: fmt.Sprintf("%016x", titleId), Type: metaType}, nil
}

func readXmlCnmt(xmlBytes []byte) (*ContentMetaAttributes, error) {
	cmt := &ContentMeta{}
	err := xml.Unmarshal(xmlBytes, &cmt)
	if err != nil {
		return nil, err
	}
	titleId := strings.Replace(cmt.ID, "0x", "", 1)
	return &ContentMetaAttributes{Version: cmt.Version, TitleId: titleId, Type: cmt.Type}, nil
}
