//go:build windows

package mediamtx

import (
	"archive/zip"
	"fmt"
	"path/filepath"
)

// The published Windows build of the pinned release, with the checksums of the
// archive and of the executable inside it.
const (
	downloadURL = "https://github.com/bluenviron/mediamtx/releases/download/" +
		Version + "/mediamtx_" + Version + "_windows_amd64.zip"
	archiveSHA256 = "a84224f5a490855684e62ba6fb71e80ea2a5e16aed61883bb58380290e93e467"
	binarySHA256  = "31bc3cc425a0278c7a5696c7ba309257235060157eb338eed20bb88dbe0faf87"
)

// extractBinary pulls exactly mediamtx.exe out of the archive, ignoring any
// other entry and any path inside the zip.
func extractBinary(archivePath, dest string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer zr.Close()

	for _, entry := range zr.File {
		if filepath.Base(entry.Name) != "mediamtx.exe" || entry.FileInfo().IsDir() {
			continue
		}
		rc, err := entry.Open()
		if err != nil {
			return err
		}
		defer rc.Close()
		return writeBinary(rc, dest)
	}
	return fmt.Errorf("в архиве нет mediamtx.exe")
}
