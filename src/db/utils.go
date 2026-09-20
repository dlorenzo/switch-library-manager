package db

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"go.uber.org/zap"
)

// maxDownloadBytes caps how much data we will read from a remote URL. The
// titles/versions JSON files are a few MB at most; anything wildly larger is
// almost certainly a misconfigured or hostile endpoint. Because the download
// URLs are user-editable in settings.json we treat the response as untrusted
// and refuse to buffer an unbounded amount of it into memory.
const maxDownloadBytes = 256 * 1024 * 1024 // 256 MB

// httpTimeout bounds the *entire* request (connect + headers + body). The
// previous implementation only set a dial timeout, so a server that accepted
// the connection but then stalled the body would hang the app indefinitely.
const httpTimeout = 60 * time.Second

type ProgressUpdater interface {
	UpdateProgress(curr int, total int, message string)
}

var sharedHTTPClient = &http.Client{
	Transport: &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: 3 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		MaxIdleConns:          10,
		IdleConnTimeout:       60 * time.Second,
	},
	Timeout: httpTimeout,
}

func LoadAndUpdateFile(url string, filePath string, etag string) (*os.File, string, error) {

	var file *os.File = nil

	//try to check if there is a new version
	//if so, save the file
	bytes, newEtag, err := downloadBytesFromUrl(url, etag)
	if err == nil {
		//validate json structure without reflection-based allocations
		if json.Valid(bytes) {
			file, err = saveFile(bytes, filePath)
			etag = newEtag
		} else {
			zap.S().Infof("ignoring new update [%v], reason - [malformed json file]", url)
		}
	} else {
		zap.S().Infof("file [%v] was not downloaded, reason - [%v]", url, err)
	}

	if file == nil {
		//load file
		file, err = os.Open(filePath)
		if err != nil {
			zap.S().Infof("ignoring new update [%v], reason - [malformed json file]", url)
			return nil, "", err
		}

		fileInfo, err := os.Stat(filePath)
		if err != nil || fileInfo.Size() == 0 {
			file.Close()
			zap.S().Infof("Local file is empty, or corrupted")
			return nil, "", errors.New("unable to download switch titles db")
		}
	}

	return file, etag, err
}

func decodeToJsonObject(reader io.Reader, target interface{}) error {
	err := json.NewDecoder(reader).Decode(target)
	return err
}

func downloadBytesFromUrl(url string, etag string) ([]byte, string, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, "", err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	req.Header.Set("User-Agent", "switch-library-manager")
	resp, err := sharedHTTPClient.Do(req)
	if err != nil {
		return nil, "", err
	}

	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, "", errors.New("got a non 200 response - " + resp.Status)
	}
	//getting the new etag
	etag = resp.Header.Get("Etag")

	if resp.StatusCode == http.StatusOK {
		// Bound the amount we read into memory. io.LimitReader with (max+1)
		// lets us detect an over-limit body instead of silently truncating it.
		limited := io.LimitReader(resp.Body, maxDownloadBytes+1)
		body, err := io.ReadAll(limited)
		if err != nil {
			return nil, "", err
		}
		if int64(len(body)) > maxDownloadBytes {
			return nil, "", errors.New("remote file exceeds maximum allowed size")
		}
		return body, etag, nil
	}

	return nil, "", errors.New("no new updates")
}

func saveFile(data []byte, fileName string) (*os.File, error) {

	dir := filepath.Dir(fileName)
	tmp, err := os.CreateTemp(dir, ".tmp-*.json")
	if err != nil {
		return nil, err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return nil, err
	}
	if err := os.Chmod(tmpName, 0644); err != nil {
		zap.S().Warnf("failed to chmod temp file: %v", err)
	}
	if err := os.Rename(tmpName, fileName); err != nil {
		os.Remove(tmpName)
		return nil, err
	}

	file, err := os.Open(fileName)
	if err != nil {
		return nil, err
	}
	return file, nil
}
