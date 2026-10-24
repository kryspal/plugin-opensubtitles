package main

import (
	"path"
	"strings"
	"unicode"

	pluginv1 "github.com/kryspal/plugin-sdk/pluginv1"
)

// A subtitle found by name is only a guess about which work it belongs to, so the plugin marks it confirmed (SubtitleCandidate.confirmed) in two cases
// and no other. A host that downloads automatically takes hash and ID matches by itself and, besides them, only confirmed candidates.
//
//  1. The subtitle was made for this very release: its release name equals the video's file name (extension removed, separators and case ignored).
//  2. OpenSubtitles says which work the subtitle belongs to (feature_details) and it is the video's work: the title equals the video's title or its
//     original title, and for a movie the year is the same; for an episode the series title and the season and episode numbers are the same.
//
// A machine or AI translation is never confirmed, whatever else matches. Without the details of the work, or without a year for a movie, case 2 does
// not apply.

// featureDetails is what OpenSubtitles tells about the work a subtitle belongs to.
type featureDetails struct {
	Title         string `json:"title"`
	MovieName     string `json:"movie_name"`
	Year          int    `json:"year"`
	ParentTitle   string `json:"parent_title"`
	SeasonNumber  int    `json:"season_number"`
	EpisodeNumber int    `json:"episode_number"`
}

// folded makes a name comparable: lower case, every run of characters that are not letters or digits becomes one space.
func folded(s string) string {
	var b strings.Builder
	gap := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if gap && b.Len() > 0 {
				b.WriteByte(' ')
			}
			gap = false
			b.WriteRune(r)
		} else {
			gap = true
		}
	}
	return b.String()
}

// sameRelease reports whether a release name is the video's file name.
func sameRelease(release, fileName string) bool {
	stem := folded(strings.TrimSuffix(fileName, path.Ext(fileName)))
	// A release name that ends in a subtitle extension ("....zh-tw.srt") is the subtitle's own file name; its video part is not comparable.
	return stem != "" && len(stem) >= 8 && folded(release) == stem
}

// sameWork reports whether details describe the work the target is.
func sameWork(t *pluginv1.SubtitleTarget, d featureDetails) bool {
	titles := map[string]bool{}
	for _, s := range []string{t.GetTitle(), t.GetOriginalTitle()} {
		if f := folded(s); f != "" {
			titles[f] = true
		}
	}
	if t.GetType() == pluginv1.ItemType_ITEM_TYPE_EPISODE {
		if t.SeasonNumber == nil || t.EpisodeNumber == nil || d.SeasonNumber != int(t.GetSeasonNumber()) || d.EpisodeNumber != int(t.GetEpisodeNumber()) {
			return false
		}
		return d.ParentTitle != "" && titles[folded(d.ParentTitle)]
	}
	if t.GetYear() <= 0 || d.Year != int(t.GetYear()) {
		return false
	}
	return (d.Title != "" && titles[folded(d.Title)]) || (d.MovieName != "" && titles[folded(d.MovieName)])
}

// otherWorkScore is the most a name match may score when OpenSubtitles says it belongs to a different work than the video.
const otherWorkScore = 0.1

// otherWork reports whether OpenSubtitles files the subtitle under a work that is clearly not the video's: it names a title and that title is none of the
// video's titles. Without details, or with a matching title, the subtitle is not judged.
func otherWork(t *pluginv1.SubtitleTarget, d featureDetails) bool {
	name := firstNonEmpty(d.Title, d.MovieName, d.ParentTitle)
	if name == "" {
		return false
	}
	for _, s := range []string{t.GetTitle(), t.GetOriginalTitle()} {
		f := folded(s)
		if f == "" {
			continue
		}
		if f == folded(name) || strings.Contains(folded(name), f) || strings.Contains(f, folded(name)) {
			return false
		}
	}
	return folded(t.GetTitle()) != "" || folded(t.GetOriginalTitle()) != ""
}

// How sure the plugin is about a subtitle found by name.
const (
	notSure       = iota
	sureOfWork    // the subtitle belongs to the video's work
	sureOfRelease // the subtitle was made for this very release
)

// confirmation decides how sure the plugin is about a subtitle found by name. A subtitle made for this release is the surest.
func confirmation(t *pluginv1.SubtitleTarget, release string, d featureDetails, machine bool) int {
	switch {
	case machine:
		return notSure
	case sameRelease(release, t.GetFileName()):
		return sureOfRelease
	case sameWork(t, d):
		return sureOfWork
	}
	return notSure
}
