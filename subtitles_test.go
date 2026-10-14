package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pluginv1 "github.com/kryspal/plugin-sdk/pluginv1"
	"github.com/kryspal/plugin-sdk/sdk"
)

const (
	testAppKey = "app-key-for-tests"
	testToken  = "session-token-for-tests"
)

// A recorded-shape search response (made-up names and ids).
const searchBody = `{"total_pages":1,"total_count":4,"per_page":20,"page":1,"data":[
 {"id":"9001","type":"subtitle","attributes":{"subtitle_id":"9001","language":"zh-TW","download_count":5200,"hearing_impaired":false,"ratings":8.5,"from_trusted":true,
   "release":"Made.Up.Movie.2019.1080p.BluRay","moviehash_match":true,"files":[{"file_id":111,"cd_number":1,"file_name":"Made.Up.Movie.2019.1080p.BluRay.zh-tw.srt"}]}},
 {"id":"9002","type":"subtitle","attributes":{"subtitle_id":"9002","language":"en","download_count":90000,"hearing_impaired":true,"ratings":99,"from_trusted":false,
   "release":"","files":[{"file_id":222,"cd_number":1,"file_name":"Made Up Movie (2019).ass"}]}},
 {"id":"9003","type":"subtitle","attributes":{"subtitle_id":"9003","language":"pt-BR","download_count":3,"files":[]}},
 {"id":"9004","type":"subtitle","attributes":{"subtitle_id":"9004","language":"zh-CN","download_count":10,"files":[{"file_id":111,"file_name":"duplicate-of-111.srt"}]}}
]}`

type fake struct {
	*httptest.Server
	mu       sync.Mutex
	hits     []string // "METHOD URI"
	keys     []string // Api-Key of every API request
	agents   []string
	auths    []string // Authorization of the download requests
	logins   int
	handlers map[string]func(w http.ResponseWriter, r *http.Request)
}

func newFake(t *testing.T) *fake {
	t.Helper()
	f := &fake{handlers: map[string]func(http.ResponseWriter, *http.Request){}}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.hits = append(f.hits, r.Method+" "+r.URL.RequestURI())
		f.agents = append(f.agents, r.Header.Get("User-Agent"))
		if strings.HasPrefix(r.URL.Path, "/api/v1/") {
			f.keys = append(f.keys, r.Header.Get("Api-Key"))
		}
		if r.URL.Path == "/api/v1/login" {
			f.logins++
		}
		if r.URL.Path == "/api/v1/download" {
			f.auths = append(f.auths, r.Header.Get("Authorization"))
		}
		h := f.handlers[r.URL.Path]
		f.mu.Unlock()
		if h == nil {
			http.NotFound(w, r)
			return
		}
		h(w, r)
	}))
	t.Cleanup(f.Close)
	f.json("/api/v1/login", 200, `{"user":{"allowed_downloads":20,"level":"Sub leecher","vip":false},"token":"`+testToken+`","status":200}`)
	return f
}

func (f *fake) json(path string, code int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[path] = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}
}

func (f *fake) file(path string, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[path] = func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }
}

func (f *fake) count(prefix string) (n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, h := range f.hits {
		if strings.Contains(h, prefix) {
			n++
		}
	}
	return n
}

func configured(t *testing.T, f *fake) *plugin {
	t.Helper()
	withAppKey(t, testAppKey)
	p := newPlugin()
	p.noQuotaLookup = true // the quota lookup has its own tests; the others look at the exact requests
	p.api.http = f.Client()
	p.api.interval = 0
	resp, err := p.Configure(context.Background(), &pluginv1.ConfigureRequest{Values: map[string]string{
		keyUsername: "someone", keyPassword: "pw", keyBaseURL: f.URL + "/api/v1"}})
	if err != nil || len(resp.GetFieldErrors()) != 0 {
		t.Fatalf("Configure = %v, %v", resp, err)
	}
	return p
}

func movie() *pluginv1.SubtitleTarget {
	return &pluginv1.SubtitleTarget{Type: pluginv1.ItemType_ITEM_TYPE_MOVIE, Title: "Made Up Movie", Year: 2019, FileName: "Made.Up.Movie.2019.mkv",
		ProviderIds: map[string]string{"imdb": "tt0001234", "tmdb": "4321"}, MovieHash: "8E245D9679D31E12"}
}

func ids(r *pluginv1.SearchSubtitlesResponse) string {
	var s []string
	for _, c := range r.GetCandidates() {
		s = append(s, c.GetId()+":"+c.GetLanguage())
	}
	return strings.Join(s, ",")
}

func query(t *testing.T, hit string) url.Values {
	t.Helper()
	u, err := url.Parse(strings.TrimPrefix(strings.TrimPrefix(hit, "GET "), "POST "))
	if err != nil {
		t.Fatal(err)
	}
	return u.Query()
}

func TestSearchByIDsAndHash(t *testing.T) {
	f := newFake(t)
	f.json("/api/v1/subtitles", 200, searchBody)
	p := configured(t, f)
	resp, err := p.Search(context.Background(), &pluginv1.SearchSubtitlesRequest{Target: movie(), Languages: []string{"zh-TW", "en-US", "zh-Hant"}})
	if err != nil {
		t.Fatal(err)
	}
	// The hash match comes first even though the other has far more downloads; the file-less one and the duplicate file id are dropped.
	if got := ids(resp); got != "111:zh-TW,222:en" {
		t.Fatalf("candidates = %s", got)
	}
	h, n := resp.GetCandidates()[0], resp.GetCandidates()[1]
	if h.GetMatchedBy() != pluginv1.SubtitleCandidate_MATCHED_BY_HASH || h.GetFormat() != "srt" || h.GetReleaseName() != "Made.Up.Movie.2019.1080p.BluRay" || h.GetDownloadCount() != 5200 || h.GetRating() != 8.5 {
		t.Errorf("hash candidate = %v", h)
	}
	if n.GetMatchedBy() != pluginv1.SubtitleCandidate_MATCHED_BY_IDS || n.GetFormat() != "ass" || !n.GetHearingImpaired() || n.GetRating() != 10 ||
		n.GetReleaseName() != "Made Up Movie (2019).ass" || n.GetScore() > h.GetScore() {
		t.Errorf("ids candidate = %v", n)
	}
	q := query(t, f.hits[0])
	if q.Get("imdb_id") != "1234" || q.Get("tmdb_id") != "4321" || q.Get("moviehash") != "8e245d9679d31e12" || q.Get("type") != "movie" ||
		q.Get("languages") != "en,zh-tw" || q.Get("machine_translated") != "exclude" || q.Get("ai_translated") != "exclude" || q.Get("hearing_impaired") != "exclude" || q.Get("query") != "" {
		t.Errorf("query = %v (IDs without tt and leading zeros, the hash lower case, languages sorted)", q)
	}
	if f.count("/login") != 0 {
		t.Error("searching must not log in: it needs only the application key")
	}
	if f.keys[0] != testAppKey || f.agents[0] != "Kryspal Media v"+version() {
		t.Errorf("headers: key %q agent %q", f.keys[0], f.agents[0])
	}
	if f.count("/subtitles") != 1 {
		t.Errorf("%d searches; results from the IDs must not trigger a title query", f.count("/subtitles"))
	}
}

func TestMachineAndAITranslationsAreLeftOutUnlessAsked(t *testing.T) {
	f := newFake(t)
	f.json("/api/v1/subtitles", 200, searchBody)
	p := configured(t, f)
	if _, err := p.Search(context.Background(), &pluginv1.SearchSubtitlesRequest{Target: movie()}); err != nil {
		t.Fatal(err)
	}
	if q := query(t, f.hits[0]); q.Get("machine_translated") != "exclude" || q.Get("ai_translated") != "exclude" {
		t.Errorf("default query = %v", q)
	}
	if _, err := p.Configure(context.Background(), &pluginv1.ConfigureRequest{Values: map[string]string{
		keyUsername: "someone", keyPassword: "pw", keyBaseURL: f.URL + "/api/v1", keyIncludeAI: "true"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Search(context.Background(), &pluginv1.SearchSubtitlesRequest{Target: movie()}); err != nil {
		t.Fatal(err)
	}
	if q := query(t, f.hits[1]); q.Get("machine_translated") != "include" || q.Get("ai_translated") != "include" {
		t.Errorf("with include_ai the query = %v", q)
	}
}

func TestSearchFallsBackToTheTitle(t *testing.T) {
	f := newFake(t)
	f.handlers["/api/v1/subtitles"] = func(w http.ResponseWriter, r *http.Request) {
		body := `{"total_count":0,"data":[]}`
		if r.URL.Query().Get("query") != "" {
			body = searchBody
		}
		_, _ = w.Write([]byte(body))
	}
	p := configured(t, f)
	resp, err := p.Search(context.Background(), &pluginv1.SearchSubtitlesRequest{Target: movie(), IncludeHearingImpaired: true})
	if err != nil || ids(resp) != "111:zh-TW,222:en" {
		t.Fatalf("Search = %s, %v", ids(resp), err)
	}
	if len(f.hits) != 2 {
		t.Fatalf("requests = %v", f.hits)
	}
	q := query(t, f.hits[1])
	if q.Get("query") != "made up movie" || q.Get("year") != "2019" || q.Get("moviehash") != "" || q.Get("imdb_id") != "" || q.Get("hearing_impaired") != "include" {
		t.Errorf("title query = %v", q)
	}
	// Found by title, a candidate with no hash match is matched by name.
	if resp.GetCandidates()[1].GetMatchedBy() != pluginv1.SubtitleCandidate_MATCHED_BY_NAME {
		t.Errorf("a title hit is matched by name: %v", resp.GetCandidates()[1])
	}
}

func TestEpisodeSearchUsesTheSeriesIDs(t *testing.T) {
	f := newFake(t)
	f.json("/api/v1/subtitles", 200, searchBody)
	p := configured(t, f)
	s, e := int32(2), int32(5)
	target := &pluginv1.SubtitleTarget{Type: pluginv1.ItemType_ITEM_TYPE_EPISODE, Title: "Made Up Show", SeasonNumber: &s, EpisodeNumber: &e,
		ProviderIds: map[string]string{"imdb": "tt7654321", "tvdb": "9"}}
	if _, err := p.Search(context.Background(), &pluginv1.SearchSubtitlesRequest{Target: target, Limit: 1}); err != nil {
		t.Fatal(err)
	}
	q := query(t, f.hits[0])
	if q.Get("parent_imdb_id") != "7654321" || q.Get("season_number") != "2" || q.Get("episode_number") != "5" || q.Get("type") != "episode" || q.Get("imdb_id") != "" || q.Get("tmdb_id") != "" {
		t.Errorf("episode query = %v", q)
	}
	// An episode without numbers cannot be searched by series ID: only the title query goes out, with no year.
	f.hits = nil
	target.SeasonNumber, target.EpisodeNumber = nil, nil
	target.Year = 2020
	_, _ = p.Search(context.Background(), &pluginv1.SearchSubtitlesRequest{Target: target})
	if len(f.hits) != 1 || query(t, f.hits[0]).Get("query") != "made up show" || query(t, f.hits[0]).Get("parent_imdb_id") != "" || query(t, f.hits[0]).Get("year") != "" {
		t.Errorf("requests = %v", f.hits)
	}
}

func TestHashOnlyAndNothingToAsk(t *testing.T) {
	f := newFake(t)
	f.json("/api/v1/subtitles", 200, searchBody)
	p := configured(t, f)
	hashOnly := &pluginv1.SubtitleTarget{Type: pluginv1.ItemType_ITEM_TYPE_MOVIE, MovieHash: "8e245d9679d31e12"}
	if _, err := p.Search(context.Background(), &pluginv1.SearchSubtitlesRequest{Target: hashOnly}); err != nil {
		t.Fatal(err)
	}
	if len(f.hits) != 1 || query(t, f.hits[0]).Get("moviehash") == "" {
		t.Errorf("hash-only requests = %v", f.hits)
	}
	f.hits = nil
	for name, target := range map[string]*pluginv1.SubtitleTarget{
		"empty":      {Type: pluginv1.ItemType_ITEM_TYPE_MOVIE},
		"bad hash":   {Type: pluginv1.ItemType_ITEM_TYPE_MOVIE, MovieHash: "xyz"},
		"bad ids":    {Type: pluginv1.ItemType_ITEM_TYPE_MOVIE, ProviderIds: map[string]string{"imdb": "nm1", "tmdb": "../x"}},
		"one letter": {Type: pluginv1.ItemType_ITEM_TYPE_MOVIE, Title: "A"},
	} {
		resp, err := p.Search(context.Background(), &pluginv1.SearchSubtitlesRequest{Target: target})
		if err != nil || len(resp.GetCandidates()) != 0 {
			t.Errorf("%s: %v, %v", name, resp, err)
		}
	}
	if len(f.hits) != 0 {
		t.Errorf("nothing to ask but %v was sent", f.hits)
	}
}

func TestLanguageLists(t *testing.T) {
	for in, want := range map[string]string{
		"en": "en", "zh-TW,en": "en,zh-tw", "zh-Hant,zh-HK,zh-tw": "zh-tw", "zh-Hans": "zh-cn", "pt-BR,pt": "pt,pt-br", "en-US,en-GB": "en", "ja_JP": "ja", "": "",
	} {
		if got := searchLanguages(strings.Split(in, ",")); got != want {
			t.Errorf("searchLanguages(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"zh-CN": "zh-CN", "zh-cn": "zh-CN", "pt-br": "pt-BR", "EN": "en", "zh-hant": "zh-Hant"} {
		if got := bcp47(in); got != want {
			t.Errorf("bcp47(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSearchStatuses(t *testing.T) {
	for _, c := range []struct {
		name string
		http int
		hdr  map[string]string
		code codes.Code
		rate time.Duration
	}{
		{"unprocessable query", 400, nil, codes.OK, 0},
		{"not acceptable", 406, nil, codes.OK, 0},
		{"rate limited", 429, map[string]string{"Retry-After": "7"}, codes.Unknown, 7 * time.Second},
		{"server error", 503, nil, codes.Unavailable, 0},
		{"key refused", 403, nil, codes.FailedPrecondition, 0},
	} {
		f := newFake(t)
		f.handlers["/api/v1/subtitles"] = func(w http.ResponseWriter, r *http.Request) {
			for k, v := range c.hdr {
				w.Header().Set(k, v)
			}
			w.WriteHeader(c.http)
			_, _ = w.Write([]byte(`{"message":"x"}`))
		}
		p := configured(t, f)
		resp, err := p.Search(context.Background(), &pluginv1.SearchSubtitlesRequest{Target: movie()})
		if c.rate > 0 {
			if rl, ok := errors.AsType[*sdk.RateLimitError](err); !ok || rl.RetryAfter != c.rate {
				t.Errorf("%s: %v, want a rate limit of %v", c.name, err, c.rate)
			}
			continue
		}
		if status.Code(err) != c.code {
			t.Errorf("%s: code %v (%v), want %v", c.name, status.Code(err), err, c.code)
		}
		if err == nil && len(resp.GetCandidates()) != 0 {
			t.Errorf("%s: %v", c.name, resp)
		}
	}
}

// ---- Download ----

// downloadable makes /download answer with a link to the fake's own file (an http:// link: the plugin upgrades it to https) and the given remaining count.
func downloadable(f *fake, remaining int) {
	f.mu.Lock()
	f.handlers["/api/v1/download"] = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"link":"` + strings.Replace(f.URL, "https://", "http://", 1) + `/files/sub.srt","file_name":"dir\\Made.Up.Movie.srt","requests":3,"remaining":` +
			strconv.Itoa(remaining) + `,"reset_time_utc":"2026-10-14T00:00:00.000Z"}`))
	}
	f.mu.Unlock()
	f.file("/files/sub.srt", []byte("1\n00:00:01,000 --> 00:00:02,000\nhello\n"))
}

func TestDownloadLogsInOnceAndReportsTheQuota(t *testing.T) {
	f := newFake(t)
	downloadable(f, 17)
	p := configured(t, f)
	for range 2 {
		resp, err := p.Download(context.Background(), &pluginv1.DownloadSubtitleRequest{Id: "111"})
		if err != nil {
			t.Fatal(err)
		}
		if len(resp.GetFiles()) != 1 || resp.GetFiles()[0].GetName() != "Made.Up.Movie.srt" || !strings.Contains(string(resp.GetFiles()[0].GetData()), "hello") || resp.GetQuotaRemaining() != 17 {
			t.Fatalf("Download = %v", resp)
		}
	}
	if f.logins != 1 {
		t.Errorf("%d logins; the session token must be reused", f.logins)
	}
	if f.auths[0] != "Bearer "+testToken || f.keys[0] != testAppKey {
		t.Errorf("auth %q key %q", f.auths[0], f.keys[0])
	}
	if !strings.Contains(f.hits[1], "POST /api/v1/download") {
		t.Errorf("requests = %v", f.hits)
	}
	// The file link is a plain request: neither the key nor the token goes to it.
	for i, h := range f.hits {
		if strings.Contains(h, "/files/") && i < len(f.agents) && f.agents[i] == "" {
			t.Error("the file request has no User-Agent")
		}
	}
}

func TestExpiredTokenLogsInAgain(t *testing.T) {
	f := newFake(t)
	downloadable(f, 5)
	var mu sync.Mutex
	first := true
	good := f.handlers["/api/v1/download"]
	f.handlers["/api/v1/download"] = func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		expired := first
		first = false
		mu.Unlock()
		if expired {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"invalid token"}`))
			return
		}
		good(w, r)
	}
	p := configured(t, f)
	resp, err := p.Download(context.Background(), &pluginv1.DownloadSubtitleRequest{Id: "111"})
	if err != nil || len(resp.GetFiles()) != 1 || f.logins != 2 {
		t.Errorf("Download = %v, %v; logins %d, want a second login after the 401", resp, err, f.logins)
	}
}

func TestVIPHostIsFollowedOnlyWithinOpenSubtitles(t *testing.T) {
	f := newFake(t)
	f.json("/api/v1/login", 200, `{"token":"`+testToken+`","base_url":"evil.example","user":{"allowed_downloads":1000}}`)
	downloadable(f, 5)
	p := configured(t, f)
	if _, err := p.Download(context.Background(), &pluginv1.DownloadSubtitleRequest{Id: "111"}); err != nil {
		t.Fatal(err)
	}
	if f.count("POST /api/v1/download") != 1 {
		t.Errorf("the download must go to the configured host, not the one the login named: %v", f.hits)
	}
	for in, want := range map[string]bool{"vip-api.opensubtitles.com": true, "api.opensubtitles.com": true, "opensubtitles.com": true, "evil-opensubtitles.com": false, "opensubtitles.com.evil.example": false, "": false} {
		if hostOK(in) != want {
			t.Errorf("hostOK(%q) = %v", in, !want)
		}
	}
}

func TestQuotaUsedUp(t *testing.T) {
	f := newFake(t)
	f.json("/api/v1/download", 406, `{"requests":20,"remaining":0,"message":"You have downloaded your allowed 20 subtitles for 24 hours","reset_time":"09 hours and 06 minutes","reset_time_utc":"2026-10-14T03:00:00.000Z"}`)
	p := configured(t, f)
	now := time.Date(2026, 10, 13, 20, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return now }
	_, err := p.Download(context.Background(), &pluginv1.DownloadSubtitleRequest{Id: "111"})
	rl, ok := errors.AsType[*sdk.RateLimitError](err)
	if !ok || !rl.Credential {
		t.Fatalf("quota used up = %v, want a rate limit of the credential (searching must keep working)", err)
	}
	if rl.RetryAfter < time.Minute {
		t.Errorf("retry after %v", rl.RetryAfter)
	}
	// While the reset time has not come, the next download is refused without a request.
	hits := len(f.hits)
	if _, err := p.Download(context.Background(), &pluginv1.DownloadSubtitleRequest{Id: "222"}); !errors.As(err, new(*sdk.RateLimitError)) || len(f.hits) != hits {
		t.Errorf("second download = %v, %d new requests", err, len(f.hits)-hits)
	}
	// After the reset time the plugin asks again.
	now = now.Add(30 * time.Hour)
	f.json("/api/v1/download", 406, `{"remaining":0,"reset_time":"1 hour"}`)
	_, _ = p.Download(context.Background(), &pluginv1.DownloadSubtitleRequest{Id: "222"})
	if len(f.hits) == hits {
		t.Error("after the reset time the plugin must ask OpenSubtitles again")
	}
}

func TestParseReset(t *testing.T) {
	now := time.Date(2026, 10, 13, 20, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		r    downloadResponse
		want time.Time
	}{
		{downloadResponse{ResetUTC: "2026-10-14T03:00:00.000Z"}, time.Date(2026, 10, 14, 3, 0, 0, 0, time.UTC)},
		{downloadResponse{ResetIn: "09 hours and 06 minutes"}, now.Add(9*time.Hour + 6*time.Minute)},
		{downloadResponse{ResetIn: "45 seconds"}, now.Add(45 * time.Second)},
		{downloadResponse{}, now.Add(time.Hour)},
	} {
		if got := parseReset(c.r, now); !got.Equal(c.want) {
			t.Errorf("parseReset(%+v) = %v, want %v", c.r, got, c.want)
		}
	}
}

func TestAccountErrors(t *testing.T) {
	for _, c := range []struct {
		name  string
		login int
		code  codes.Code
	}{{"wrong password", 401, codes.Unauthenticated}, {"bad request", 400, codes.Unauthenticated}, {"not acceptable", 406, codes.Unauthenticated}, {"key refused", 403, codes.FailedPrecondition}, {"down", 503, codes.Unavailable}} {
		f := newFake(t)
		f.json("/api/v1/login", c.login, `{"message":"x"}`)
		downloadable(f, 5)
		p := configured(t, f)
		_, err := p.Download(context.Background(), &pluginv1.DownloadSubtitleRequest{Id: "111"})
		if status.Code(err) != c.code {
			t.Errorf("%s: %v, want %v", c.name, err, c.code)
		}
		if c.code == codes.FailedPrecondition || c.code == codes.Unauthenticated {
			n := len(f.hits)
			if _, err := p.Download(context.Background(), &pluginv1.DownloadSubtitleRequest{Id: "111"}); status.Code(err) != c.code || len(f.hits) != n {
				t.Errorf("%s: after a rejected account: %v, %d new requests", c.name, err, len(f.hits)-n)
			}
		}
	}
}

func TestDownloadErrors(t *testing.T) {
	f := newFake(t)
	p := configured(t, f)
	for _, id := range []string{"", "abc", "-1", "0"} {
		if _, err := p.Download(context.Background(), &pluginv1.DownloadSubtitleRequest{Id: id}); status.Code(err) != codes.InvalidArgument {
			t.Errorf("id %q = %v, want INVALID_ARGUMENT", id, err)
		}
	}
	if len(f.hits) != 0 {
		t.Errorf("bad ids sent %v", f.hits)
	}
	f.json("/api/v1/download", 404, `{}`)
	if _, err := p.Download(context.Background(), &pluginv1.DownloadSubtitleRequest{Id: "9"}); status.Code(err) != codes.NotFound {
		t.Errorf("missing file = %v, want NOT_FOUND", err)
	}
	f.json("/api/v1/download", 200, `{"remaining":4}`)
	if _, err := p.Download(context.Background(), &pluginv1.DownloadSubtitleRequest{Id: "9"}); status.Code(err) != codes.NotFound {
		t.Errorf("no link = %v, want NOT_FOUND", err)
	}
	// A file over the limit is not returned.
	downloadable(f, 3)
	f.file("/files/sub.srt", make([]byte, maxFileBytes+1))
	if _, err := p.Download(context.Background(), &pluginv1.DownloadSubtitleRequest{Id: "9"}); status.Code(err) != codes.NotFound {
		t.Errorf("oversize file = %v, want NOT_FOUND", err)
	}
	// A reply that does not say how many are left reports "unknown" (-1), not zero (which would stop the downloads).
	f.json("/api/v1/download", 200, `{"link":"`+strings.Replace(f.URL, "https://", "http://", 1)+`/files/small.srt"}`)
	f.file("/files/small.srt", []byte("1\n"))
	if r, err := p.Download(context.Background(), &pluginv1.DownloadSubtitleRequest{Id: "9"}); err != nil || r.GetQuotaRemaining() != -1 {
		t.Errorf("no remaining in the reply: %v, %v; want -1", r.GetQuotaRemaining(), err)
	}
	if _, blocked := p.quotaBlocked(); blocked {
		t.Error("an unknown quota blocked the downloads")
	}
	var noAccount plugin
	if _, err := (&plugin{api: newAPI(), now: time.Now, set: noAccount.set}).Download(context.Background(), &pluginv1.DownloadSubtitleRequest{Id: "9"}); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("no account = %v", err)
	}
}

func TestRequestsAreSpaced(t *testing.T) {
	f := newFake(t)
	f.json("/api/v1/subtitles", 200, `{"data":[]}`)
	p := configured(t, f)
	p.api.interval = 80 * time.Millisecond
	start := time.Now()
	done := make(chan struct{})
	for range 3 {
		go func() {
			_, _ = p.Search(context.Background(), &pluginv1.SearchSubtitlesRequest{Target: &pluginv1.SubtitleTarget{Title: "Made Up Movie"}})
			done <- struct{}{}
		}()
	}
	for range 3 {
		<-done
	}
	if time.Since(start) < 160*time.Millisecond {
		t.Errorf("3 requests took %v", time.Since(start))
	}
}

func TestFileFormat(t *testing.T) {
	for in, want := range map[string]string{"a.srt": "srt", "A.ASS": "ass", "Made.Up.Movie.2010.BluRay.x264-GRP": "srt", "Inception.2010.eng": "srt", "": "srt", "x.vtt": "vtt"} {
		if got := fileFormat(in); got != want {
			t.Errorf("fileFormat(%q) = %q, want %q", in, got, want)
		}
	}
}

// The search reports the account's remaining downloads: read once from /infos/user (this signs in), then cached; a download reply updates it.
func TestSearchReportsTheQuotaBeforeAnyDownload(t *testing.T) {
	f := newFake(t)
	f.json("/api/v1/subtitles", 200, searchBody)
	f.json("/api/v1/infos/user", 200, `{"data":{"allowed_downloads":100,"downloads_count":3,"remaining_downloads":97,"level":"Sub leecher"}}`)
	p := configured(t, f)
	p.noQuotaLookup = false
	req := &pluginv1.SearchSubtitlesRequest{Target: movie()}
	for i := 0; i < 3; i++ {
		resp, err := p.Search(context.Background(), req)
		if err != nil || resp.QuotaRemaining == nil || resp.GetQuotaRemaining() != 97 {
			t.Fatalf("search %d: quota %v, %v; want 97", i, resp.QuotaRemaining, err)
		}
	}
	if f.count("/infos/user") != 1 || f.count("/login") != 1 {
		t.Errorf("%d lookups and %d logins over three searches; the reading must be cached", f.count("/infos/user"), f.count("/login"))
	}
	// A download reply says how many are left now.
	downloadable(f, 96)
	if _, err := p.Download(context.Background(), &pluginv1.DownloadSubtitleRequest{Id: "111"}); err != nil {
		t.Fatal(err)
	}
	if resp, _ := p.Search(context.Background(), req); resp.GetQuotaRemaining() != 96 || f.count("/infos/user") != 1 {
		t.Errorf("after a download: quota %v, %d lookups", resp.QuotaRemaining, f.count("/infos/user"))
	}
	// An old reading is read again.
	now := time.Now().Add(2 * quotaMaxAge)
	p.now = func() time.Time { return now }
	if resp, _ := p.Search(context.Background(), req); resp.GetQuotaRemaining() != 97 || f.count("/infos/user") != 2 {
		t.Errorf("a stale reading: quota %v, %d lookups", resp.QuotaRemaining, f.count("/infos/user"))
	}
}

// A lookup that cannot be made never fails the search: no quota is reported, and the failed lookup is not repeated on every search.
func TestQuotaLookupFailuresDoNotFailTheSearch(t *testing.T) {
	for name, setup := range map[string]func(f *fake){
		"no infos endpoint": func(f *fake) {},
		"login refused":     func(f *fake) { f.json("/api/v1/login", 401, `{}`) },
		"no number":         func(f *fake) { f.json("/api/v1/infos/user", 200, `{"data":{}}`) },
	} {
		f := newFake(t)
		f.json("/api/v1/subtitles", 200, searchBody)
		setup(f)
		p := configured(t, f)
		p.noQuotaLookup = false
		for i := 0; i < 3; i++ {
			resp, err := p.Search(context.Background(), &pluginv1.SearchSubtitlesRequest{Target: movie()})
			if err != nil || len(resp.GetCandidates()) == 0 || resp.QuotaRemaining != nil {
				t.Fatalf("%s: search %d = %d candidates, quota %v, %v", name, i, len(resp.GetCandidates()), resp.QuotaRemaining, err)
			}
		}
		if n := f.count("/infos/user") + f.count("/login"); n > 2 {
			t.Errorf("%s: %d lookup requests over three searches; a failure must not be retried at once", name, n)
		}
	}
}
