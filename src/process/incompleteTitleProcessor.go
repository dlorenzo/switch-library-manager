package process

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/firebat20/switch-library-manager/db"
	"github.com/firebat20/switch-library-manager/switchfs"
	"go.uber.org/zap"
)

type IncompleteTitle struct {
	Attributes       db.TitleAttributes
	Meta             *switchfs.ContentMetaAttributes
	LocalUpdate      int      `json:"local_update"`
	LatestUpdate     int      `json:"latest_update"`
	LatestUpdateDate string   `json:"latest_update_date"`
	MissingDLC       []string `json:"missing_dlc"`
}

func ScanForMissingUpdates(localDB map[string]*db.SwitchGameFiles,
	switchDB map[string]*db.SwitchTitle,
	ignoreTitleIds map[string]struct{},
	ignoreDLCupdates bool) map[string]IncompleteTitle {

	result := map[string]IncompleteTitle{}

	//iterate over local files, and compare to remote versions
	for idPrefix, switchFile := range localDB {

		if !switchFile.BaseExist {
			zap.S().Infof("missing base for game %v", idPrefix)
			continue
		}

		// Hoist the repeated switchDB[idPrefix] lookups into one.
		remoteTitle, ok := switchDB[idPrefix]
		if !ok {
			continue
		}

		if _, ok := ignoreTitleIds[strings.ToLower(switchFile.File.Metadata.TitleId)]; ok {
			continue
		}

		switchTitle := IncompleteTitle{Attributes: remoteTitle.Attributes, Meta: switchFile.File.Metadata}

		// Only the highest version on each side matters, so scan for the max
		// in O(n) instead of materializing and sorting both version lists.
		switchTitle.LocalUpdate = 0
		for v := range switchFile.Updates {
			if v > switchTitle.LocalUpdate {
				switchTitle.LocalUpdate = v
			}
		}

		//process updates
		switchTitle.LatestUpdate = 0
		if len(remoteTitle.Updates) != 0 {
			for v := range remoteTitle.Updates {
				if v > switchTitle.LatestUpdate {
					switchTitle.LatestUpdate = v
				}
			}
			switchTitle.LatestUpdateDate = remoteTitle.Updates[switchTitle.LatestUpdate]
			if switchTitle.LocalUpdate < switchTitle.LatestUpdate {
				result[remoteTitle.Attributes.Id] = switchTitle
			}
		}

		if len(remoteTitle.Dlc) == 0 {
			continue
		}

		//process dlc
		if !ignoreDLCupdates {
			for k, availableDlc := range remoteTitle.Dlc {

				if localDlc, ok := switchFile.Dlc[k]; ok {
					latestDlcVersion, err := availableDlc.Version.Int64()
					if err != nil {
						continue
					}

					if localDlc.Metadata == nil {
						continue
					}

					if _, ok := ignoreTitleIds[strings.ToLower(localDlc.Metadata.TitleId)]; ok {
						continue
					}

					if localDlc.Metadata.Version < int(latestDlcVersion) {
						updateDate := "-"
						if availableDlc.ReleaseDate != 0 {
							updateDate = strconv.Itoa(availableDlc.ReleaseDate)
							if len(updateDate) > 7 {
								updateDate = updateDate[0:4] + "-" + updateDate[4:6] + "-" + updateDate[6:]
							}
						}

						result[availableDlc.Id] = IncompleteTitle{
							Attributes:       availableDlc,
							LatestUpdate:     int(latestDlcVersion),
							LocalUpdate:      localDlc.Metadata.Version,
							LatestUpdateDate: updateDate,
							Meta:             localDlc.Metadata}
					}
				}
			}
		}

	}
	return result
}

func ScanForMissingDLC(localDB map[string]*db.SwitchGameFiles,
	switchDB map[string]*db.SwitchTitle, ignoreTitleIds map[string]struct{}) map[string]IncompleteTitle {
	result := map[string]IncompleteTitle{}

	//iterate over local files, and compare to remote versions
	for idPrefix, switchFile := range localDB {

		if !switchFile.BaseExist {
			continue
		}

		remoteTitle, ok := switchDB[idPrefix]
		if !ok {
			continue
		}
		switchTitle := IncompleteTitle{Attributes: remoteTitle.Attributes}

		//process dlc
		if len(remoteTitle.Dlc) != 0 {
			for k, v := range remoteTitle.Dlc {
				if _, ok := ignoreTitleIds[strings.ToLower(k)]; ok {
					continue
				}

				if _, ok := switchFile.Dlc[k]; !ok {
					switchTitle.MissingDLC = append(switchTitle.MissingDLC, fmt.Sprintf("%v [%v]", v.Name, v.Id))
				}
			}
			if len(switchTitle.MissingDLC) != 0 {
				result[remoteTitle.Attributes.Id] = switchTitle
			}
		}
	}
	return result
}

func ScanForBrokenFiles(localDB map[string]*db.SwitchGameFiles) []db.SwitchFileInfo {
	var result []db.SwitchFileInfo

	//iterate over local files, and compare to remote versions
	for _, switchFile := range localDB {

		if !switchFile.BaseExist {
			for _, f := range switchFile.Dlc {
				result = append(result, f)
			}
			for _, f := range switchFile.Updates {
				result = append(result, f)
			}
		}
	}
	return result
}
