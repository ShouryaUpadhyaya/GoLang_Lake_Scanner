// Package classify decides what a file really is, by looking at its bytes
// rather than trusting its name.
package classify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // blank imports register decoders with image.DecodeConfig
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Kind is a named string type. Using a type (not a bare string) lets the
// compiler catch typos, and the constants below act like an enum.
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

// Result is the outcome of classifying one file.
type Result struct {
	Kind     Kind   `json:"kind"`
	MIME     string `json:"mime"`
	Mismatch bool   `json:"mismatch"`         // extension disagrees with content
	Detail   string `json:"detail,omitempty"` // why, in plain words
	Probe    *Probe `json:"probe,omitempty"`  // ffprobe info for audio/video
}

// Classifier is an interface: anything with this method qualifies. No
// "implements" keyword is needed, which makes fakes in tests trivial.
type Classifier interface {
	Classify(ctx context.Context, path string) Result
}

// Default is the real classifier.
type Default struct {
	ProbeTimeout time.Duration
}

func New() *Default { return &Default{ProbeTimeout: 10 * time.Second} }

// signature maps leading bytes (at an offset) to a kind and MIME type.
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
	{4, "ftyp", KindVideo, "video/mp4"}, // also m4a; ffprobe sorts that out
	{0, "\x1aE\xdf\xa3", KindVideo, "video/webm"},
	{0, "ID3", KindAudio, "audio/mpeg"},
	{0, "fLaC", KindAudio, "audio/flac"},
	{0, "OggS", KindAudio, "audio/ogg"},
}

// sniff returns the kind and MIME from the first bytes of a file.
func sniff(head []byte) (Kind, string, bool) {
	for _, s := range signatures {
		end := s.offset + len(s.magic)
		if len(head) >= end && string(head[s.offset:end]) == s.magic {
			return s.kind, s.mime, true
		}
	}
	// RIFF containers: the type lives at bytes 8-12.
	if len(head) >= 12 && string(head[:4]) == "RIFF" {
		switch string(head[8:12]) {
		case "WAVE":
			return KindAudio, "audio/wav", true
		case "AVI ":
			return KindVideo, "video/x-msvideo", true
		}
	}
	// Fall back to the standard library's sniffer, text only: anything
	// else it says is just "application/octet-stream".
	mime := http.DetectContentType(head)
	if strings.HasPrefix(mime, "text/") {
		return KindText, mime, true
	}
	return KindUnknown, mime, false
}

// kindFromExt is what the filename CLAIMS the file is.
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

// Classify never panics and never returns an error: problems become part of
// the Result (KindCorrupt / KindUnknown with a Detail), so one bad file can't
// stop a scan.
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
	head = head[:n] // keep only what was actually read

	if n == 0 {
		return Result{Kind: KindEmpty, MIME: "inode/x-empty"}
	}

	kind, mime, ok := sniff(head)
	claimed := kindFromExt(path)

	res := Result{Kind: kind, MIME: mime}
	if !ok {
		// No known signature. If the name promised a real format, the
		// content is garbage: call it corrupt. Otherwise it's just unknown.
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

	// Deeper validation per kind.
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
		// An .mp4 container with only audio is audio.
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

// hasPDFEnd looks for %%EOF in the last KB, as valid PDFs end with it.
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
