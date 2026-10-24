package main

import (
	"context"
	"strings"
	"testing"

	pluginv1 "github.com/kryspal/plugin-sdk/pluginv1"
)

func TestSameRelease(t *testing.T) {
	for _, tc := range []struct {
		release, file string
		want          bool
	}{
		{"Made.Up.Movie.2019.1080p.BluRay.x264-GRP", "Made.Up.Movie.2019.1080p.BluRay.x264-GRP.mkv", true},
		{"made up movie 2019 1080p bluray x264-grp", "Made.Up.Movie.2019.1080p.BluRay.x264-GRP.mkv", true},
		{"Made.Up.Movie.2019.720p.WEB", "Made.Up.Movie.2019.1080p.BluRay.x264-GRP.mkv", false},
		{"Made.Up.Movie.2019", "Made.Up.Movie.2019.1080p.BluRay.x264-GRP.mkv", false},
		{"", "Made.Up.Movie.2019.mkv", false},
		{"x", "x.mkv", false}, // a stem this short says nothing
		{"Made.Up.Movie.2019", "", false},
	} {
		if got := sameRelease(tc.release, tc.file); got != tc.want {
			t.Errorf("sameRelease(%q, %q) = %v, want %v", tc.release, tc.file, got, tc.want)
		}
	}
}

func TestSameWork(t *testing.T) {
	m := &pluginv1.SubtitleTarget{Type: pluginv1.ItemType_ITEM_TYPE_MOVIE, Title: "Made Up Movie", OriginalTitle: "Made-Up Movie Original", Year: 2019}
	s, e := int32(2), int32(5)
	ep := &pluginv1.SubtitleTarget{Type: pluginv1.ItemType_ITEM_TYPE_EPISODE, Title: "Pilot", OriginalTitle: "Made Up Show", SeasonNumber: &s, EpisodeNumber: &e}
	for _, tc := range []struct {
		name string
		t    *pluginv1.SubtitleTarget
		d    featureDetails
		want bool
	}{
		{"movie title and year", m, featureDetails{Title: "Made Up Movie", Year: 2019}, true},
		{"movie name instead of title", m, featureDetails{MovieName: "Made Up Movie (2019)", Title: "x", Year: 2019}, false},
		{"movie original title", m, featureDetails{Title: "Made-Up Movie Original!", Year: 2019}, true},
		{"movie other year", m, featureDetails{Title: "Made Up Movie", Year: 2018}, false},
		{"movie other title", m, featureDetails{Title: "Made Up Movies", Year: 2019}, false},
		{"movie no details", m, featureDetails{}, false},
		{"movie without a year", &pluginv1.SubtitleTarget{Type: pluginv1.ItemType_ITEM_TYPE_MOVIE, Title: "Made Up Movie"}, featureDetails{Title: "Made Up Movie"}, false},
		{"episode series and numbers", ep, featureDetails{ParentTitle: "Made Up Show", SeasonNumber: 2, EpisodeNumber: 5}, true},
		{"episode other number", ep, featureDetails{ParentTitle: "Made Up Show", SeasonNumber: 2, EpisodeNumber: 6}, false},
		{"episode other series", ep, featureDetails{ParentTitle: "Another Show", SeasonNumber: 2, EpisodeNumber: 5}, false},
		{"episode no series title", ep, featureDetails{SeasonNumber: 2, EpisodeNumber: 5}, false},
		{"episode without numbers", &pluginv1.SubtitleTarget{Type: pluginv1.ItemType_ITEM_TYPE_EPISODE, OriginalTitle: "Made Up Show"}, featureDetails{ParentTitle: "Made Up Show"}, false},
	} {
		if got := sameWork(tc.t, tc.d); got != tc.want {
			t.Errorf("%s: sameWork = %v, want %v", tc.name, got, tc.want)
		}
	}
}

const confirmBody = `{"total_count":6,"data":[
 {"attributes":{"language":"en","download_count":10,"release":"Other.Release.2019","files":[{"file_id":1,"file_name":"a.srt"}],
   "feature_details":{"title":"Made Up Movie","year":2019}}},
 {"attributes":{"language":"en","download_count":90000,"release":"Other.Release.2019","files":[{"file_id":2,"file_name":"b.srt"}],
   "feature_details":{"title":"Made Up Movie","year":2011}}},
 {"attributes":{"language":"en","download_count":5,"release":"Made.Up.Movie.2019.1080p.BluRay.x264-GRP","files":[{"file_id":3,"file_name":"c.srt"}]}},
 {"attributes":{"language":"en","download_count":3,"release":"Made.Up.Movie.2019.1080p.BluRay.x264-GRP","machine_translated":true,"files":[{"file_id":4,"file_name":"d.srt"}],
   "feature_details":{"title":"Made Up Movie","year":2019}}},
 {"attributes":{"language":"en","download_count":2,"ai_translated":true,"release":"Other.Release.2019","files":[{"file_id":5,"file_name":"e.srt"}],
   "feature_details":{"title":"Made Up Movie","year":2019}}},
 {"attributes":{"language":"en","download_count":1,"release":"Other.Release.2019","files":[{"file_id":6,"file_name":"f.srt"}]}}
]}`

// Found by title: a subtitle is confirmed when it was made for this release or OpenSubtitles says it belongs to this work (title and year); machine
// and AI translations never are. Confirmed candidates come first, and a hash match is not marked (the host takes it on its own).
func TestNameMatchesAreConfirmedOnlyWhenSure(t *testing.T) {
	f := newFake(t)
	f.json("/api/v1/subtitles", 200, confirmBody)
	p := configured(t, f)
	if _, err := p.Configure(context.Background(), &pluginv1.ConfigureRequest{Values: map[string]string{
		keyUsername: "someone", keyPassword: "pw", keyBaseURL: f.URL + "/api/v1", keyIncludeAI: "true"}}); err != nil {
		t.Fatal(err)
	}
	target := &pluginv1.SubtitleTarget{Type: pluginv1.ItemType_ITEM_TYPE_MOVIE, Title: "Made Up Movie", Year: 2019, FileName: "Made.Up.Movie.2019.1080p.BluRay.x264-GRP.mkv"}
	resp, err := p.Search(context.Background(), &pluginv1.SearchSubtitlesRequest{Target: target})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range resp.GetCandidates() {
		got = append(got, c.GetId()+map[bool]string{true: "+", false: "-"}[c.GetConfirmed()])
	}
	// 1 is the work (title and year), 3 is this release; 2 is another year, 4 and 5 are machine and AI translations, 6 has no details.
	if want := "3+,1+,2-,4-,5-,6-"; strings.Join(got, ",") != want {
		t.Errorf("candidates = %s, want %s", strings.Join(got, ","), want)
	}
	for _, c := range resp.GetCandidates() {
		if c.GetMatchedBy() != pluginv1.SubtitleCandidate_MATCHED_BY_NAME {
			t.Errorf("matched by %v", c.GetMatchedBy())
		}
	}
}

// A hash match and an ID match are decided by the host without the flag; the plugin leaves it unset so that it keeps one meaning.
func TestHashAndIDMatchesAreNotMarkedConfirmed(t *testing.T) {
	f := newFake(t)
	f.json("/api/v1/subtitles", 200, searchBody)
	p := configured(t, f)
	resp, err := p.Search(context.Background(), &pluginv1.SearchSubtitlesRequest{Target: movie(), Languages: []string{"zh-TW", "en"}})
	if err != nil || len(resp.GetCandidates()) == 0 {
		t.Fatalf("Search = %v, %v", resp, err)
	}
	for _, c := range resp.GetCandidates() {
		if c.GetConfirmed() {
			t.Errorf("hash or ID candidate marked confirmed: %v", c)
		}
	}
}
