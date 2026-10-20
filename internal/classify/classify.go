package classify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Kind string

const (
	KindPDF     Kind = "pdf"
	KindImage   Kind = "image"
	KindVideo   Kind = "video"
	KindAudio   Kind = "audio"
	KindText    Kind = "text"
	KindEmpty   Kind = "empty"
	KindCorrupt Kind = "corrupt"
	KindUnknown Kind = "unknown"
)

type Result struct {
	Kind     Kind   `json:"kind"`
	MIME     string `json:"mime"`
	Mismatch bool   `json:"mismatch"`
	Detail   string `json:"detail,omitempty"`
	Probe    *Probe `json:"probe,omitempty"`
}

type Classifier interface {
	Classify(ctx context.Context, path string) Result
}

type Default struct {
	ProbeTimeout time.Duration
}

func New() *Default { return &Default{ProbeTimeout: 10 * time.Second} }

type signature struct {
	offset int
	magic  string
	kind   Kind
	mime   string
}

var signatures = []signature{
	{0, "%PDF-", KindPDF, "application/pdf"},
	{0, "\xff\xd8\xff", KindImage, "image/jpeg"},
	{0, "\x89PNG\r\n\x1a\n", KindImage, "image/png"},
	{0, "GIF8", KindImage, "image/gif"},
	{4, "ftyp", KindVideo, "video/mp4"},
	{0, "\x1aE\xdf\xa3", KindVideo, "video/webm"},
	{0, "ID3", KindAudio, "audio/mpeg"},
	{0, "fLaC", KindAudio, "audio/flac"},
	{0, "OggS", KindAudio, "audio/ogg"},
}

func sniff(head []byte) (Kind, string, bool) {
	for _, s := range signatures {
		end := s.offset + len(s.magic)
		if len(head) >= end && string(head[s.offset:end]) == s.magic {
			return s.kind, s.mime, true
		}
	}

	if len(head) >= 12 && string(head[:4]) == "RIFF" {
		switch string(head[8:12]) {
		case "WAVE":
			return KindAudio, "audio/wav", true
		case "AVI ":
			return KindVideo, "video/x-msvideo", true
		}
	}

	mime := http.DetectContentType(head)
	if strings.HasPrefix(mime, "text/") {
		return KindText, mime, true
	}
	return KindUnknown, mime, false
}

func kindFromExt(path string) Kind {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".pdf":
		return KindPDF
	case ".jpg", ".jpeg", ".png", ".gif":
		return KindImage
	case ".mp4", ".mov", ".mkv", ".webm", ".avi":
		return KindVideo
	case ".wav", ".mp3", ".flac", ".ogg", ".m4a":
		return KindAudio
	case ".txt", ".md", ".csv", ".json":
		return KindText
	}
	return KindUnknown
}

func (d *Default) Classify(ctx context.Context, path string) Result {
	f, err := os.Open(path)
	if err != nil {
		return Result{Kind: KindUnknown, Detail: "cannot open: " + err.Error()}
	}
	defer f.Close()

	head := make([]byte, 512)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return Result{Kind: KindCorrupt, Detail: "read failed: " + err.Error()}
	}
	head = head[:n]

	if n == 0 {
		return Result{Kind: KindEmpty, MIME: "inode/x-empty"}
	}

	kind, mime, ok := sniff(head)
	claimed := kindFromExt(path)

	res := Result{Kind: kind, MIME: mime}
	if !ok {

		if claimed != KindUnknown && claimed != KindText {
			res.Kind = KindCorrupt
			res.Detail = fmt.Sprintf("named like %s but no valid signature", claimed)
		}
		return res
	}
	if claimed != KindUnknown && claimed != kind {
		res.Mismatch = true
		res.Detail = fmt.Sprintf("extension says %s, content is %s", claimed, kind)
	}

	switch kind {
	case KindImage:
		if _, _, err := imageConfig(f); err != nil {
			res.Kind, res.Detail = KindCorrupt, "image does not decode: "+err.Error()
		}
	case KindPDF:
		if !hasPDFEnd(f) {
			res.Kind, res.Detail = KindCorrupt, "PDF is truncated (no %%EOF)"
		}
	case KindVideo, KindAudio:
		p, err := RunProbe(ctx, d.ProbeTimeout, path)
		if err != nil {
			res.Kind, res.Detail = KindCorrupt, "ffprobe: "+err.Error()
			break
		}
		res.Probe = p

		if kind == KindVideo && !p.HasVideo() && p.HasAudio() {
			res.Kind = KindAudio
		}
	}
	return res
}

func imageConfig(f *os.File) (image.Config, string, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return image.Config{}, "", err
	}
	return image.DecodeConfig(f)
}

func hasPDFEnd(f *os.File) bool {
	st, err := f.Stat()
	if err != nil {
		return false
	}
	size := int64(1024)
	if st.Size() < size {
		size = st.Size()
	}
	tail := make([]byte, size)
	if _, err := f.ReadAt(tail, st.Size()-size); err != nil && !errors.Is(err, io.EOF) {
		return false
	}
	return bytes.Contains(tail, []byte("%%EOF"))
}
