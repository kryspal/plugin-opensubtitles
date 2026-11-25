package main

import (
	"context"
	"strings"
	"testing"

	pluginv1 "github.com/kryspal/plugin-sdk/pluginv1"
)

// A recorded-shape search response (made-up names and ids) in which the source gives every detail we map, one in which it gives none of
// them, and one with odd values that must not break the search.
const detailsBody = `{"total_pages":1,"total_count":3,"per_page":20,"page":1,"data":[
 {"id":"7001","type":"subtitle","attributes":{"subtitle_id":"7001","language":"en","download_count":1200,"new_download_count":40,"hearing_impaired":false,"hd":true,
   "fps":23.976,"votes":3,"points":9,"ratings":7.5,"from_trusted":true,"foreign_parts_only":false,"upload_date":"2021-03-04T12:34:56Z",
   "ai_translated":false,"machine_translated":true,"release":"Made.Up.Show.S01E01.720p","comments":"Synced to the\n  WEB-DL release.\t",
   "uploader":{"uploader_id":1,"name":"made-up-uploader","rank":"Trusted member"},
   "feature_details":{"feature_type":"Episode","title":"Made Up Show"},
   "files":[{"file_id":501,"cd_number":1,"file_name":"Made.Up.Show.S01E01.srt"},{"file_id":502,"cd_number":2,"file_name":"Made.Up.Show.S01E01.cd2.srt"}]}},
 {"id":"7002","type":"subtitle","attributes":{"subtitle_id":"7002","language":"fr","download_count":0,"hearing_impaired":false,"fps":0,"from_trusted":false,
   "upload_date":"","comments":"","release":"Made.Up.Show.S01E01","files":[{"file_id":503,"file_name":"x.srt"}]}},
 {"id":"7003","type":"subtitle","attributes":{"subtitle_id":"7003","language":"de","ai_translated":true,"fps":"n/a","upload_date":"last week","comments":null,
   "release":"Made.Up.Show.S01E01.HDTV","files":[{"file_id":504,"file_name":"y.srt"}]}}
]}`

func searchDetails(t *testing.T, body string) map[string]*pluginv1.SubtitleCandidate {
	t.Helper()
	f := newFake(t)
	f.json("/api/v1/subtitles", 200, body)
	p := configured(t, f)
	resp, err := p.Search(context.Background(), &pluginv1.SearchSubtitlesRequest{Target: movie(), Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*pluginv1.SubtitleCandidate{}
	for _, c := range resp.GetCandidates() {
		out[c.GetId()] = c
	}
	return out
}

func TestCandidateDetailsAreMappedFromTheSource(t *testing.T) {
	c := searchDetails(t, detailsBody)["501"]
	if c == nil {
		t.Fatal("candidate 501 missing")
	}
	if c.GetFrameRate() != 23.976 || c.GetUploadedAt() != "2021-03-04" || !c.GetMachineTranslated() || !c.GetTrusted() || c.GetFileCount() != 2 ||
		c.GetComment() != "Synced to the WEB-DL release." {
		t.Errorf("details = %v", c)
	}
	// The API gives no download size or duration in a search; they must stay unknown, not be guessed.
	if c.GetSizeBytes() != 0 || c.GetDurationSeconds() != 0 {
		t.Errorf("size %d, duration %d: the source does not give them", c.GetSizeBytes(), c.GetDurationSeconds())
	}
	// Existing fields are unchanged.
	if c.GetDownloadCount() != 1200 || c.GetRating() != 7.5 || c.GetLanguage() != "en" {
		t.Errorf("fields = %v", c)
	}
}

func TestCandidateDetailsStayUnknownWhenTheSourceGivesNone(t *testing.T) {
	c := searchDetails(t, detailsBody)["503"]
	if c == nil {
		t.Fatal("candidate 503 missing")
	}
	if c.GetFrameRate() != 0 || c.GetUploadedAt() != "" || c.GetMachineTranslated() || c.GetTrusted() || c.GetComment() != "" ||
		c.GetSizeBytes() != 0 || c.GetDurationSeconds() != 0 {
		t.Errorf("details = %v", c)
	}
	if c.GetFileCount() != 1 {
		t.Errorf("file_count = %d: the response lists one file", c.GetFileCount())
	}
}

func TestOddValuesLeaveDetailsUnknownAndKeepTheSearchWorking(t *testing.T) {
	cs := searchDetails(t, detailsBody)
	c := cs["504"]
	if c == nil || len(cs) != 3 {
		t.Fatalf("candidates = %v", cs)
	}
	// "fps":"n/a" and "upload_date":"last week" are not numbers or dates, "comments":null is no text; ai_translated counts as machine translated.
	if c.GetFrameRate() != 0 || c.GetUploadedAt() != "" || c.GetComment() != "" || !c.GetMachineTranslated() {
		t.Errorf("details = %v", c)
	}
}

func TestDetailsNeverMakeACandidateConfirmed(t *testing.T) {
	// Trusted uploaders, frame rates and comments are information for the person; only the release-name check sets confirmed.
	for id, c := range searchDetails(t, detailsBody) {
		if c.GetConfirmed() {
			t.Errorf("candidate %s is confirmed", id)
		}
	}
}

func TestUploadDate(t *testing.T) {
	for in, want := range map[string]string{
		"2021-03-04T12:34:56Z": "2021-03-04", "2021-03-04T23:59:59+08:00": "2021-03-04", "2021-03-04T12:34:56.000Z": "2021-03-04",
		"2021-03-04 12:34:56": "2021-03-04", "2021-03-04": "2021-03-04", "": "", "yesterday": "", "2021-13-40": "",
	} {
		if got := uploadDate(in); got != want {
			t.Errorf("uploadDate(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPlainComment(t *testing.T) {
	if got := plainComment("  a\r\nb\x00c\t\td  "); got != "a b c d" {
		t.Errorf("got %q", got)
	}
	long := strings.Repeat("字", 300)
	if got := plainComment(long); len([]rune(got)) != maxCommentRunes {
		t.Errorf("%d characters, want %d", len([]rune(got)), maxCommentRunes)
	}
	if got := plainComment(strings.Repeat("a", 199) + " " + strings.Repeat("b", 50)); strings.HasSuffix(got, " ") || len([]rune(got)) > maxCommentRunes {
		t.Errorf("got %d characters ending in a space: %q", len(got), got)
	}
}
