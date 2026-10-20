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
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func Walk(root string, fn func(FileRecord) error, onErr func(path string, err error)) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			onErr(path, err)
			return nil
		}
		if !d.Type().IsRegular() {
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
