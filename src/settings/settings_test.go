package settings

import (
	"testing"
)

func TestVerifySettings_MissingFilesClearsETag(t *testing.T) {
	tmpDir := t.TempDir()

	s := &AppSettings{
		TitlesEtag:   "W/\"test-titles-etag\"",
		VersionsEtag: "W/\"test-versions-etag\"",
	}

	verified := verifySettings(tmpDir, s)

	// Since neither titles.json nor versions.json exists in tmpDir,
	// ETags must be reset to "" so a full download is performed.
	if verified.TitlesEtag != "" {
		t.Errorf("expected empty TitlesEtag for missing file, got %q", verified.TitlesEtag)
	}
	if verified.VersionsEtag != "" {
		t.Errorf("expected empty VersionsEtag for missing file, got %q", verified.VersionsEtag)
	}
	if verified.WindowWidth != 1200 {
		t.Errorf("expected default WindowWidth 1200, got %d", verified.WindowWidth)
	}
	if verified.WindowHeight != 600 {
		t.Errorf("expected default WindowHeight 600, got %d", verified.WindowHeight)
	}
}
