package scanner

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

type FileRecord struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	Hash string `json:"hash"`
}

func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil { // f is an io.Reader
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Walk visits every regular file under root and calls fn with its record.
// A file that cannot be read is reported through onErr and skipped; one bad
// file must not stop the whole scan.
func Walk(root string, fn func(FileRecord) error, onErr func(path string, err error)) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			onErr(path, err)
			return nil // keep walking
		}
		if !d.Type().IsRegular() { // skips directories, symlinks, sockets...
			return nil
		}
		info, err := d.Info()
		if err != nil {
			onErr(path, err)
			return nil
		}
		hash, err := HashFile(path)
		if err != nil {
			onErr(path, err)
			return nil
		}
		return fn(FileRecord{Path: path, Size: info.Size(), Hash: hash})
	})
}
