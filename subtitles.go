package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	pluginv1 "github.com/kryspal/plugin-sdk/pluginv1"
	"github.com/kryspal/plugin-sdk/sdk"
)

const (
	defaultLimit = 20
	maxFileBytes = 2 << 20
	// sessionTTL is how long the plugin trusts a login token (OpenSubtitles tokens live about 24 hours).
	sessionTTL = 20 * time.Hour
)

var (
	hashRE = regexp.MustCompile(`^[0-9a-fA-F]{16}$`)
	imdbRE = regexp.MustCompile(`^(?:tt)?([0-9]{1,9})$`)
	digits = regexp.MustCompile(`^[0-9]{1,9}$`)
)

// ---- Login ----

type loginResponse struct {
	Token   string `json:"token"`
	BaseURL string `json:"base_url"`
	User    struct {
		AllowedDownloads int `json:"allowed_downloads"`
	} `json:"user"`
}

// session returns the token and the API base to use, logging in when there is none or it is old. A rejected account (401, 400, 406 on /login) is remembered and answered with UNAUTHENTICATED
// without asking again (until new settings are saved); a refused application key (403) makes the plugin forget the account and report needs-configuration.
func (p *plugin) session(ctx context.Context, s settings, force bool) (token, base string, err error) {
	p.mu.Lock()
	if !force && p.token != "" && p.set == s && p.now().Sub(p.tokenAt) < sessionTTL {
		token, base = p.token, p.apiBase
		p.mu.Unlock()
		return token, base, nil
	}
	rejected := p.rejected == s
	p.mu.Unlock()
	if rejected {
		return "", "", sdk.Unauthenticated(errBadAccount.Error())
	}
	var lr loginResponse
	status, _, err := p.api.do(ctx, http.MethodPost, s.baseURL+"/login", s.key(), "", map[string]string{"username": s.username, "password": s.password}, &lr)
	if err != nil {
		return "", "", err
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusBadRequest || status == http.StatusNotAcceptable:
		p.mu.Lock()
		if p.set == s {
			p.rejected, p.token = s, ""
		}
		p.mu.Unlock()
		return "", "", sdk.Unauthenticated(errBadAccount.Error())
	case status == http.StatusForbidden:
		p.forgetAccount(s)
		return "", "", sdk.NeedsConfiguration(errBadKey.Error())
	case status != http.StatusOK || lr.Token == "":
		return "", "", sdk.Unavailable("opensubtitles: login failed with " + strconv.Itoa(status))
	}
	base = s.baseURL
	// VIP accounts are served from another host named in the reply; follow it only when it is an OpenSubtitles host and the base is the official one.
	if s.baseURL == defaultBaseURL && lr.BaseURL != "" {
		if u, perr := url.Parse("https://" + strings.TrimPrefix(lr.BaseURL, "https://")); perr == nil && hostOK(u.Hostname()) {
			base = "https://" + u.Host + "/api/v1"
		}
	}
	p.mu.Lock()
	if p.set == s {
		p.token, p.tokenAt, p.apiBase, p.allowed = lr.Token, p.now(), base, lr.User.AllowedDownloads
	}
	p.mu.Unlock()
	return lr.Token, base, nil
}

// withSession runs fn with a session; when fn reports the token as expired it logs in again once and retries.
func (p *plugin) withSession(ctx context.Context, s settings, fn func(token, base string) error) error {
	token, base, err := p.session(ctx, s, false)
	if err != nil {
		return err
	}
	if err = fn(token, base); errors.Is(err, errTokenGone) {
		if token, base, err = p.session(ctx, s, true); err != nil {
			return err
		}
		err = fn(token, base)
	}
	if errors.Is(err, errTokenGone) {
		return sdk.Unavailable("opensubtitles: the session was refused twice")
	}
	return err
}

// ---- Search ----

type searchResponse struct {
	Data []struct {
		Attributes struct {
			Language          string         `json:"language"`
			DownloadCount     int            `json:"download_count"`
			HearingImpaired   bool           `json:"hearing_impaired"`
			Ratings           float64        `json:"ratings"`
			Release           string         `json:"release"`
			MovieHashMatch    bool           `json:"moviehash_match"`
			MachineTranslated bool           `json:"machine_translated"`
			AITranslated      bool           `json:"ai_translated"`
			FeatureDetails    featureDetails `json:"feature_details"`
			Files             []struct {
				FileID   int64  `json:"file_id"`
				FileName string `json:"file_name"`
			} `json:"files"`
			FromTrusted bool `json:"from_trusted"`
		} `json:"attributes"`
	} `json:"data"`
}

// Search asks OpenSubtitles with the provider IDs (and the file hash when there is one); when the IDs find nothing, or there are none, it asks by
// title, year, season and episode. Searching needs only the application key, not the account, so it works before the first login and does not
// use up the download quota. Machine and AI translations are left out unless the setting include_ai is on.
func (p *plugin) Search(ctx context.Context, req *pluginv1.SearchSubtitlesRequest) (*pluginv1.SearchSubtitlesResponse, error) {
	s := p.settings()
	if s.username == "" || s.password == "" || s.key() == "" {
		return nil, sdk.NeedsConfiguration("OpenSubtitles account is not configured")
	}
	t := req.GetTarget()
	limit := int(req.GetLimit())
	if limit <= 0 || limit > 50 {
		limit = defaultLimit
	}
	langs := searchLanguages(req.GetLanguages())
	var out []*pluginv1.SubtitleCandidate
	sure := map[*pluginv1.SubtitleCandidate]int{} // how sure the plugin is about a name match
	seen := map[int64]bool{}
	for _, q := range searchQueries(t, langs, req.GetIncludeHearingImpaired(), s.includeAI) {
		var resp searchResponse
		status, _, err := p.api.do(ctx, http.MethodGet, s.baseURL+"/subtitles?"+q.params, s.key(), "", nil, &resp)
		switch {
		case err != nil:
			return nil, err
		case status == http.StatusForbidden || status == http.StatusUnauthorized:
			p.forgetAccount(s)
			return nil, sdk.NeedsConfiguration(errBadKey.Error())
		case status != http.StatusOK:
			continue // 400 and 406 mean a query that cannot be answered, such as too few parameters; try the next one
		}
		for _, d := range resp.Data {
			a := d.Attributes
			if len(a.Files) == 0 || a.Files[0].FileID <= 0 || seen[a.Files[0].FileID] {
				continue
			}
			seen[a.Files[0].FileID] = true
			by := q.matchedBy
			if a.MovieHashMatch {
				by = pluginv1.SubtitleCandidate_MATCHED_BY_HASH
			}
			c := &pluginv1.SubtitleCandidate{Id: strconv.FormatInt(a.Files[0].FileID, 10), Language: bcp47(a.Language),
				ReleaseName: firstNonEmpty(a.Release, a.Files[0].FileName), Format: fileFormat(a.Files[0].FileName), HearingImpaired: a.HearingImpaired,
				MatchedBy: by, DownloadCount: int32(min(a.DownloadCount, 1<<30)), Rating: min(max(a.Ratings, 0), 10), Score: score(by, a.FromTrusted, a.DownloadCount)}
			if by == pluginv1.SubtitleCandidate_MATCHED_BY_NAME {
				sure[c] = confirmation(t, a.Release, a.FeatureDetails, a.MachineTranslated || a.AITranslated)
				c.Confirmed = sure[c] != notSure
			}
			out = append(out, c)
		}
		if len(out) > 0 {
			break // this query found subtitles; the next, looser one is not needed
		}
	}
	// Best first: a hash match, then IDs, then a subtitle made for this release, then one for this work, then the rest; by score within each.
	rank := func(c *pluginv1.SubtitleCandidate) int {
		switch c.GetMatchedBy() {
		case pluginv1.SubtitleCandidate_MATCHED_BY_HASH:
			return 10
		case pluginv1.SubtitleCandidate_MATCHED_BY_IDS:
			return 9
		}
		return sure[c]
	}
	sort.SliceStable(out, func(i, j int) bool {
		if ri, rj := rank(out[i]), rank(out[j]); ri != rj {
			return ri > rj
		}
		return out[i].GetScore() > out[j].GetScore()
	})
	if len(out) > limit {
		out = out[:limit]
	}
	logSearch(ctx, out)
	return &pluginv1.SearchSubtitlesResponse{Candidates: out, QuotaRemaining: p.knownQuota(ctx, s)}, nil
}

// logSearch writes one line per search: how many candidates, how many by hash, by IDs and confirmed. No titles or names.
func logSearch(ctx context.Context, out []*pluginv1.SubtitleCandidate) {
	var hash, ids, confirmed int
	for _, c := range out {
		switch {
		case c.GetMatchedBy() == pluginv1.SubtitleCandidate_MATCHED_BY_HASH:
			hash++
		case c.GetMatchedBy() == pluginv1.SubtitleCandidate_MATCHED_BY_IDS:
			ids++
		}
		if c.GetConfirmed() {
			confirmed++
		}
	}
	slog.InfoContext(ctx, "opensubtitles search", slog.Int("candidates", len(out)), slog.Int("hash", hash), slog.Int("ids", ids), slog.Int("confirmed", confirmed))
}

type searchQuery struct {
	params    string
	matchedBy pluginv1.SubtitleCandidate_MatchedBy
}

// searchQueries lists the queries to try in order: by provider IDs (with the file hash), then by title. OpenSubtitles wants the parameters in
// lower case and sorted by name (it caches on the exact query), which url.Values.Encode does.
func searchQueries(t *pluginv1.SubtitleTarget, langs string, hearing, includeAI bool) []searchQuery {
	episode := t.GetType() == pluginv1.ItemType_ITEM_TYPE_EPISODE
	base := func() url.Values {
		v := url.Values{"machine_translated": {"exclude"}, "ai_translated": {"exclude"}}
		if includeAI {
			v.Set("machine_translated", "include")
			v.Set("ai_translated", "include")
		}
		if langs != "" {
			v.Set("languages", langs)
		}
		if hearing {
			v.Set("hearing_impaired", "include")
		} else {
			v.Set("hearing_impaired", "exclude")
		}
		if episode {
			v.Set("type", "episode")
			if t.SeasonNumber != nil && t.EpisodeNumber != nil {
				v.Set("season_number", strconv.Itoa(int(t.GetSeasonNumber())))
				v.Set("episode_number", strconv.Itoa(int(t.GetEpisodeNumber())))
			}
		} else {
			v.Set("type", "movie")
		}
		if h := strings.ToLower(t.GetMovieHash()); hashRE.MatchString(h) {
			v.Set("moviehash", h)
		}
		return v
	}
	var qs []searchQuery
	ids := base()
	n := 0
	prefix := ""
	if episode {
		prefix = "parent_"
		if t.SeasonNumber == nil || t.EpisodeNumber == nil {
			ids = nil // an episode without numbers cannot be found by series ID
		}
	}
	if ids != nil {
		pid := t.GetProviderIds()
		if m := imdbRE.FindStringSubmatch(pid["imdb"]); m != nil {
			ids.Set(prefix+"imdb_id", strings.TrimLeft(m[1], "0"))
			n++
		}
		if digits.MatchString(pid["tmdb"]) {
			ids.Set(prefix+"tmdb_id", pid["tmdb"])
			n++
		}
		if n > 0 {
			qs = append(qs, searchQuery{params: ids.Encode(), matchedBy: pluginv1.SubtitleCandidate_MATCHED_BY_IDS})
		}
	}
	// A hash with no IDs still has to be tried: a hash-only query finds the exact file.
	if n == 0 && base().Get("moviehash") != "" {
		qs = append(qs, searchQuery{params: base().Encode(), matchedBy: pluginv1.SubtitleCandidate_MATCHED_BY_NAME})
	}
	if title := strings.TrimSpace(t.GetTitle()); len([]rune(title)) >= 2 {
		v := base()
		v.Del("moviehash")
		v.Set("query", strings.ToLower(title))
		if t.GetYear() > 0 && !episode {
			v.Set("year", strconv.Itoa(int(t.GetYear())))
		}
		qs = append(qs, searchQuery{params: v.Encode(), matchedBy: pluginv1.SubtitleCandidate_MATCHED_BY_NAME})
	}
	return qs
}

// searchLanguages turns BCP 47 tags into OpenSubtitles' list: lower case, "zh-CN" stays "zh-cn", "pt-BR" is "pt-br", and a tag with another region
// keeps only the language ("en-US" → "en"). Duplicates are removed; the list is sorted (the API wants a stable query).
func searchLanguages(tags []string) string {
	seen := map[string]bool{}
	var out []string
	for _, tag := range tags {
		l := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(tag), "_", "-"))
		switch l {
		case "":
			continue
		case "zh-hans", "zh-sg":
			l = "zh-cn"
		case "zh-hant", "zh-hk", "zh-mo":
			l = "zh-tw"
		case "pt-br", "zh-cn", "zh-tw":
		default:
			l = strings.SplitN(l, "-", 2)[0]
		}
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// bcp47 normalizes OpenSubtitles' language ("zh-CN", "pt-BR", "en") to BCP 47 case ("zh-CN", "pt-BR", "en").
func bcp47(l string) string {
	parts := strings.Split(strings.ReplaceAll(strings.TrimSpace(l), "_", "-"), "-")
	for i := range parts {
		switch {
		case i == 0:
			parts[i] = strings.ToLower(parts[i])
		case len(parts[i]) == 2:
			parts[i] = strings.ToUpper(parts[i])
		case len(parts[i]) == 4:
			parts[i] = strings.ToUpper(parts[i][:1]) + strings.ToLower(parts[i][1:])
		}
	}
	return strings.Join(parts, "-")
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// knownFormats are the subtitle formats OpenSubtitles serves.
var knownFormats = map[string]bool{"srt": true, "ass": true, "ssa": true, "vtt": true, "sub": true, "smi": true, "txt": true}

// fileFormat is the lower-case extension of the file name when it is a subtitle format. File names often end in a release group
// ("Movie.2010.BluRay.x264-GRP"), whose "extension" is not one; OpenSubtitles serves srt unless asked for another format.
func fileFormat(name string) string {
	if e := strings.ToLower(strings.TrimPrefix(path.Ext(name), ".")); knownFormats[e] {
		return e
	}
	return "srt"
}

// score is the plugin's own 0–1 rating: a hash match first, then provider IDs, trusted uploaders and popularity add a little.
func score(by pluginv1.SubtitleCandidate_MatchedBy, trusted bool, downloads int) float64 {
	v := 0.3
	switch by {
	case pluginv1.SubtitleCandidate_MATCHED_BY_HASH:
		v = 0.9
	case pluginv1.SubtitleCandidate_MATCHED_BY_IDS:
		v = 0.6
	}
	if trusted {
		v += 0.05
	}
	v += min(float64(downloads), 100000) / 100000 * 0.05
	return min(v, 1)
}

// ---- Download ----

type downloadResponse struct {
	Link      string `json:"link"`
	FileName  string `json:"file_name"`
	Remaining *int   `json:"remaining"` // nil when the reply does not say
	Message   string `json:"message"`
	ResetUTC  string `json:"reset_time_utc"`
	ResetIn   string `json:"reset_time"`
}

// Download asks for a download link (this uses one of the account's daily downloads), then fetches the file. When the daily quota is used up
// (406 from OpenSubtitles, or the plugin already knows it is 0 until the reset time) it returns a rate limit that concerns only the credential,
// so searching keeps working. The reply's "remaining" is passed on as the quota.
func (p *plugin) Download(ctx context.Context, req *pluginv1.DownloadSubtitleRequest) (*pluginv1.DownloadSubtitleResponse, error) {
	s := p.settings()
	if s.username == "" || s.password == "" || s.key() == "" {
		return nil, sdk.NeedsConfiguration("OpenSubtitles account is not configured")
	}
	id, err := strconv.ParseInt(req.GetId(), 10, 64)
	if err != nil || id <= 0 {
		return nil, sdk.InvalidArgument("opensubtitles: bad subtitle id")
	}
	if d, ok := p.quotaBlocked(); ok {
		return nil, sdk.RateLimitedCredential(d, "opensubtitles: the daily download quota is used up")
	}
	var dl downloadResponse
	err = p.withSession(ctx, s, func(token, base string) error {
		status, raw, err := p.api.do(ctx, http.MethodPost, base+"/download", s.key(), token, map[string]any{"file_id": id}, &dl)
		if err != nil {
			return err
		}
		switch status {
		case http.StatusOK:
			return nil
		case http.StatusUnauthorized:
			return errTokenGone
		case http.StatusForbidden:
			p.forgetAccount(s)
			return sdk.NeedsConfiguration(errBadKey.Error())
		case http.StatusNotAcceptable: // the quota is used up
			var q downloadResponse
			_ = jsonUnmarshal(raw, &q)
			return &quotaError{resetAt: parseReset(q, p.now()), remaining: 0}
		case http.StatusNotFound, http.StatusGone:
			return sdk.NotFound("opensubtitles: the subtitle no longer exists")
		}
		return sdk.InvalidArgument("opensubtitles: download refused with status " + strconv.Itoa(status))
	})
	if qe, ok := errors.AsType[*quotaError](err); ok {
		p.setQuota(0, qe.resetAt)
		return nil, sdk.RateLimitedCredential(max(time.Until(qe.resetAt), time.Minute), qe.Error())
	}
	if err != nil {
		return nil, err
	}
	left := int32(-1) // unknown: the reply did not say
	if dl.Remaining != nil {
		left = int32(*dl.Remaining)
		p.setQuota(*dl.Remaining, parseReset(dl, p.now()))
	}
	if dl.Link == "" {
		return nil, sdk.NotFound("opensubtitles: no download link")
	}
	data, err := p.api.fetch(ctx, dl.Link, maxFileBytes)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, sdk.NotFound("opensubtitles: the subtitle file could not be fetched")
	}
	name := path.Base(strings.ReplaceAll(dl.FileName, "\\", "/"))
	if name == "." || name == "/" {
		name = ""
	}
	return &pluginv1.DownloadSubtitleResponse{QuotaRemaining: left, Files: []*pluginv1.SubtitleFile{{Name: name, Data: data}}}, nil
}

// setQuota records the remaining downloads and when they reset.
func (p *plugin) setQuota(remaining int, resetAt time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.quotaLeft, p.quotaEnds, p.quotaAt = &remaining, resetAt, p.now()
}

const (
	// quotaMaxAge is how long a known remaining count is trusted before a search reads it again from /infos/user (downloads made elsewhere,
	// or a new day, change it).
	quotaMaxAge = time.Hour
	// quotaRetry is the shortest time between two lookups that failed.
	quotaRetry = 10 * time.Minute
	// quotaLookupTime bounds the lookup, which rides on a search and must not hold it up.
	quotaLookupTime = 8 * time.Second
)

type userInfoResponse struct {
	Data struct {
		Remaining *int `json:"remaining_downloads"`
	} `json:"data"`
}

// knownQuota returns the remaining downloads to report with a search: the last known count while it is fresh, otherwise a new reading from
// /infos/user (this signs in the first time, so the admin console shows the quota before the first download). It never fails the search: when
// the lookup cannot be made (rejected account, no network) the result is nil, and a failed lookup is not repeated for quotaRetry.
func (p *plugin) knownQuota(ctx context.Context, s settings) *int32 {
	if p.noQuotaLookup {
		return nil
	}
	p.mu.Lock()
	left, at, tried := p.quotaLeft, p.quotaAt, p.quotaTry
	if left != nil && (p.now().Sub(at) < quotaMaxAge || *left == 0 && p.quotaEnds.After(p.now())) {
		n := int32(*left)
		p.mu.Unlock()
		return &n
	}
	if !tried.IsZero() && p.now().Sub(tried) < quotaRetry {
		p.mu.Unlock()
		return nil
	}
	p.quotaTry = p.now()
	p.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, quotaLookupTime)
	defer cancel()
	token, base, err := p.session(ctx, s, false)
	if err != nil {
		return nil
	}
	var info userInfoResponse
	status, _, err := p.api.do(ctx, http.MethodGet, base+"/infos/user", s.key(), token, nil, &info)
	if err != nil || status != http.StatusOK || info.Data.Remaining == nil || *info.Data.Remaining < 0 {
		if status == http.StatusUnauthorized {
			p.mu.Lock()
			p.token = "" // the next call signs in again
			p.mu.Unlock()
		}
		return nil
	}
	p.setQuota(*info.Data.Remaining, time.Time{}) // the reading has no reset time; a 0 here is only reported, the download itself is what finds out
	n := int32(*info.Data.Remaining)
	return &n
}

// quotaBlocked reports whether the last download said the quota is 0 and the reset time has not come yet, and how long to wait.
func (p *plugin) quotaBlocked() (time.Duration, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.quotaLeft == nil || *p.quotaLeft > 0 {
		return 0, false
	}
	if d := p.quotaEnds.Sub(p.now()); d > 0 {
		return d, true
	}
	p.quotaLeft = nil
	return 0, false
}

// parseReset reads the reset time from a download reply: reset_time_utc (RFC 3339) first; otherwise "reset_time" like "23 hours and 5 minutes"
// is read as hours and minutes; with neither, an hour from now.
func parseReset(r downloadResponse, now time.Time) time.Time {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000Z", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, r.ResetUTC); err == nil {
			return t.UTC()
		}
	}
	var d time.Duration
	for _, m := range regexp.MustCompile(`(\d+)\s*(hour|minute|second)`).FindAllStringSubmatch(r.ResetIn, -1) {
		n, _ := strconv.Atoi(m[1])
		switch m[2] {
		case "hour":
			d += time.Duration(n) * time.Hour
		case "minute":
			d += time.Duration(n) * time.Minute
		case "second":
			d += time.Duration(n) * time.Second
		}
	}
	if d <= 0 {
		d = time.Hour
	}
	return now.Add(d).UTC()
}
