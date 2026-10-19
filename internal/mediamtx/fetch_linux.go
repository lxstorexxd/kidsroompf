//go:build linux

package mediamtx

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
)

// The published Linux build of the pinned release, with the checksums of the
// archive and of the binary inside it.
const (
	downloadURL = "https://github.com/bluenviron/mediamtx/releases/download/" +
		Version + "/mediamtx_" + Version + "_linux_amd64.tar.gz"
	archiveSHA256 = "121be6e00397e473a95d83c75f4fcd3819a6af3b0e1a50889cba43ab4fab6a27"
	binarySHA256  = "0ffb7f18210d08ee38cb16668c4f3a2c96622e69d401119c0026d078a2b89523"
)

// extractBinary pulls exactly the mediamtx binary out of the tarball.
//
// The archive also carries mediamtx.yml and a licence, and neither is wanted:
// the agent writes the configuration itself. Only the base name is matched, so
// no entry can place a file outside dest however its path is spelled.
func extractBinary(archivePath, dest string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg || path.Base(hdr.Name) != "mediamtx" {
			continue
		}
		return writeBinary(tr, dest)
	}
	return fmt.Errorf("в архиве нет mediamtx")
}
