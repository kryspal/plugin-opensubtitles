package main

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	pluginv1 "github.com/kryspal/plugin-sdk/pluginv1"
)

// maxCommentRunes is the longest note the protocol allows in SubtitleCandidate.comment.
const maxCommentRunes = 200

// looseString reads a JSON string and leaves anything else (null, a number, an object) empty, so one odd value never fails the whole search.
type looseString string

func (l *looseString) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(bytes.TrimSpace(b), &s); err != nil {
		*l = ""
		return nil
	}
	*l = looseString(s)
	return nil
}

// looseNumber reads a JSON number and leaves anything else empty (0, unknown).
type looseNumber float64

func (l *looseNumber) UnmarshalJSON(b []byte) error {
	var f float64
	if err := json.Unmarshal(bytes.TrimSpace(b), &f); err != nil {
		*l = 0
		return nil
	}
	*l = looseNumber(f)
	return nil
}

// addDetails copies what OpenSubtitles says about a subtitle into the candidate, so the person can compare before downloading. It fills only
// what the source gives: a field the source leaves out stays at its default (unknown). Nothing is derived from the file name.
// OpenSubtitles has no download size or duration in its search results, so size_bytes and duration_seconds stay unknown.
func addDetails(c *pluginv1.SubtitleCandidate, a subAttributes) {
	if f := float64(a.FPS); f > 0 && f < 1000 && !math.IsNaN(f) {
		c.FrameRate = f
	}
	c.UploadedAt = uploadDate(string(a.UploadDate))
	c.MachineTranslated = a.MachineTranslated || a.AITranslated
	c.Trusted = a.FromTrusted
	c.FileCount = int32(len(a.Files))
	c.Comment = plainComment(string(a.Comments))
}

// uploadDate turns the ISO timestamp the API sends into YYYY-MM-DD; a value that is not one gives "" (unknown).
func uploadDate(s string) string {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05.999999999Z0700", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format("2006-01-02")
		}
	}
	return ""
}

// plainComment makes the uploader's note plain text of at most 200 characters on one line: control characters and line breaks become spaces,
// runs of spaces one space.
func plainComment(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == ' ' || r == ' ' || r == utf8.RuneError {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxCommentRunes {
		s = strings.TrimSpace(string(r[:maxCommentRunes]))
	}
	return s
}
