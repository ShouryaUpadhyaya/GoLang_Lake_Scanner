package classify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Probe mirrors the parts of ffprobe's JSON output we care about. Fields not
// listed here are simply ignored by encoding/json.
type Probe struct {
	Streams []Stream `json:"streams"`
	Format  Format   `json:"format"`
}

type Stream struct {
	Index     int    `json:"index"`
	CodecName string `json:"codec_name"`
	CodecType string `json:"codec_type"` // "video" or "audio"
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
	// ffprobe prints numbers like duration as strings, so we keep strings.
	Duration   string `json:"duration,omitempty"`
	SampleRate string `json:"sample_rate,omitempty"`
}

type Format struct {
	FormatName string `json:"format_name"`
	Duration   string `json:"duration"`
}

func (p *Probe) has(t string) bool {
	for _, s := range p.Streams {
		if s.CodecType == t {
			return true
		}
	}
	return false
}
func (p *Probe) HasVideo() bool { return p.has("video") }
func (p *Probe) HasAudio() bool { return p.has("audio") }

// RunProbe runs ffprobe with a timeout. CommandContext kills the process if
// the context expires, so a hung ffprobe can't hang the scanner.
func RunProbe(ctx context.Context, timeout time.Duration, path string) (*Probe, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "error", "-print_format", "json", "-show_format", "-show_streams", path)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("timed out: %w", ctx.Err())
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%s", msg)
	}

	var p Probe
	if err := json.Unmarshal(stdout.Bytes(), &p); err != nil {
		return nil, fmt.Errorf("bad ffprobe JSON: %w", err)
	}
	if len(p.Streams) == 0 {
		return nil, fmt.Errorf("no streams found")
	}
	return &p, nil
}
