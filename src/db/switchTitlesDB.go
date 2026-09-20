package db

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

type TitleAttributes struct {
	Id                string      `json:"id"`
	Name              string      `json:"name,omitempty"`
	Version           json.Number `json:"version,omitempty"`
	Region            string      `json:"region,omitempty"`
	ReleaseDate       int         `json:"releaseDate,omitempty"`
	ParsedReleaseDate string
	Publisher         string   `json:"publisher,omitempty"`
	IconUrl           string   `json:"iconUrl,omitempty"`
	Screenshots       []string `json:"screenshots,omitempty"`
	BannerUrl         string   `json:"bannerUrl,omitempty"`
	Description       string   `json:"description,omitempty"`
	Size              int      `json:"size,omitempty"`
	IsDemo            bool     `json:"isDemo,omitempty"`
}

type SwitchTitle struct {
	Attributes TitleAttributes
	Updates    map[int]string
	Dlc        map[string]TitleAttributes
}

type SwitchTitlesDB struct {
	TitlesMap map[string]*SwitchTitle
}

func CreateSwitchTitleDB(titlesFile, versionsFile io.Reader) (*SwitchTitlesDB, error) {
	if closer, ok := titlesFile.(io.Closer); ok {
		defer closer.Close()
	}
	if closer, ok := versionsFile.(io.Closer); ok {
		defer closer.Close()
	}

	//parse the titles objects
	var titles = map[string]TitleAttributes{}
	err := decodeToJsonObject(titlesFile, &titles)
	if err != nil {
		return nil, err
	}

	//parse the titles objects
	//titleID -> versionId-> release date
	var versions = map[string]map[int]string{}
	err = decodeToJsonObject(versionsFile, &versions)
	if err != nil {
		return nil, err
	}

	// Pre-size the result map: with tens of thousands of titles, letting the
	// map grow incrementally causes repeated rehash/regrow churn. Every entry
	// in `titles` maps to at most one idPrefix bucket, so len(titles) is a
	// safe upper bound.
	result := SwitchTitlesDB{TitlesMap: make(map[string]*SwitchTitle, len(titles))}
	for id, attr := range titles {
		if len(id) != 16 {
			continue
		}
		if _, err := strconv.ParseUint(id, 16, 64); err != nil {
			continue
		}
		id = strings.ToLower(id)

		//TitleAttributes id rules:
		//main TitleAttributes ends with 000
		//Updates ends with 800
		//Dlc adds 1 to 4th char starting from the right (always odd) and
		//have a running counter (starting with 001) in the 3 last chars
		idPrefix := id[0 : len(id)-3]
		if !(strings.HasSuffix(id, "000") || strings.HasSuffix(id, "800")) {
			intVar, err := strconv.ParseUint(id[len(id)-4:len(id)-3], 16, 64)
			if err != nil || intVar == 0 {
				continue
			}
			h := fmt.Sprintf("%x", intVar-1)
			idPrefix = id[0:len(id)-4] + h
		}

		switchTitle, ok := result.TitlesMap[idPrefix]
		if !ok {
			switchTitle = &SwitchTitle{Dlc: map[string]TitleAttributes{}}
			result.TitlesMap[idPrefix] = switchTitle
		}

		// parse the release date to a date string
		prd := strconv.Itoa(attr.ReleaseDate)
		if len(prd) == 8 {
			attr.ParsedReleaseDate = prd[0:4] + "-" + prd[4:6] + "-" + prd[6:8]
		} else {
			attr.ParsedReleaseDate = prd
		}

		//process Updates
		if strings.HasSuffix(id, "800") {
			updates := versions[id[0:len(id)-3]+"000"]
			switchTitle.Updates = updates
			continue
		}

		//process main TitleAttributes
		if strings.HasSuffix(id, "000") {
			switchTitle.Attributes = attr
			continue
		}

		//not an update, and not main TitleAttributes, so treat it as a DLC
		switchTitle.Dlc[id] = attr

	}

	return &result, nil
}
