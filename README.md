# opensubtitles: OpenSubtitles.com subtitle provider plugin

An official Kryspal Media plugin that searches and downloads subtitles from OpenSubtitles.com. It implements the protocol v1 `SubtitleProvider` service with [`github.com/kryspal/plugin-sdk`](https://github.com/kryspal/plugin-sdk) and declares the capability `subtitles.search`. It is disabled by default; after the administrator enters their OpenSubtitles account and enables it, the server's subtitle search uses it together with the other providers. The plugin only returns subtitle bytes; saving next to the video is the server's job.

## Application key (build-time injection)

The OpenSubtitles application (consumer) key belongs to the developer and is sent with every request (`Api-Key`). It is **not in the source**: the release build injects it,

```
go build -ldflags "-X main.appKey=$OPENSUBTITLES_CONSUMER_KEY" .
```

(The release workflow passes the key from a CI secret into this flag; the key never appears in logs, `Describe`, tests or the repository.) A build without it (a fork, a local build) works with the user's own key, entered in the advanced setting "Your own application key"; `Configure` reports a field error when neither exists. A user-entered key always wins over the built-in one.

## Account, session and quota

- The user enters their own OpenSubtitles.com **user name and password** (the password is a secret setting). The plugin logs in by itself (`POST /login`) the first time a download needs it, keeps the token for 20 hours (tokens live about 24), logs in again once when a download answers 401, and drops everything when new settings are saved. **Searching does not need the account** (only the application key) and never uses a download. It does read the remaining download count once for the admin console (below), which signs in the first time.
- A wrong user name or password (login 400/401/406) is remembered and answered with **`UNAUTHENTICATED`** without asking OpenSubtitles again until new settings are saved (the settings stay, so the admin console can say "wrong user name or password"; the host reports it as `LastErrorKind` `auth_failed` while the plugin stays usable for searching). A refused application key (403) still makes the plugin forget the account and report needs-configuration.
- VIP accounts are served from another host that the login names; the plugin follows it only when it is an `opensubtitles.com` host and the configured base is the official one.
- **Download quota** (each account has a daily allowance; the free allowance is small): `POST /download` returns the remaining count, which is passed on as `quota_remaining` on every download (-1 when a reply does not say, never 0 by default). When the quota is used up (406, or the plugin already knows it is 0 until the reset time) it returns `RESOURCE_EXHAUSTED` with the reset time and the credential scope, so the host pauses downloads but searching keeps working; no request is sent while the reset time has not come.
- **Quota before the first download**: a search also reports the account's remaining downloads (`SearchSubtitlesResponse.quota_remaining`). The plugin reads `GET /infos/user` (`remaining_downloads`) after signing in, caches it for an hour, and a download reply updates it. The lookup never fails or delays a search beyond 8 seconds: when it cannot be made (account refused, no network, no number in the reply) the search reports nothing, and a failed lookup is not repeated for 10 minutes.
- Requests are one at a time, at least 400 ms apart; a 429 returns `RESOURCE_EXHAUSTED` with `Retry-After`. The `User-Agent` is `Kryspal Media v<version>` (OpenSubtitles requires a recognizable agent).
- **Attribution**: "Subtitles provided by OpenSubtitles.com" in the manifest's attribution (the source line on the plugin page).

## Search

`GET {base}/subtitles` with lower-case parameters (`url.Values.Encode` keeps them sorted, which OpenSubtitles wants for its cache):

1. **By provider IDs and the file hash**: movies `imdb_id` (without `tt` and leading zeros) and `tmdb_id`; episodes `parent_imdb_id` / `parent_tmdb_id` with `season_number` and `episode_number` (an episode without numbers skips this step); `moviehash` when the host has one (16 hex digits). Results flagged `moviehash_match` are **matched by hash**, the others by IDs.
2. Only when step 1 found nothing, or there were no IDs: **by title** (`query`, lower case, with `year` for movies). A hash with no IDs is also tried alone first.
- Common parameters: `type` (movie or episode), `languages` (BCP 47 turned into OpenSubtitles' list: `zh-TW`, `zh-Hant`, `zh-HK` → `zh-tw`; `zh-CN`, `zh-Hans`, `zh-SG` → `zh-cn`; `pt-BR` stays; other regions drop to the language), `hearing_impaired` (`include` or `exclude`), and `machine_translated=exclude`, `ai_translated=exclude` (both `include` when the setting `include_ai` is on).
- Candidates: the first file of each result (`file_id` is the candidate id; duplicates and results without a file are dropped); language back to BCP 47 case; format from the file name when it is a subtitle extension, else `srt`; rating clamped to 0–10. Score: 0.9 for a hash match, 0.6 for IDs, 0.3 for a name match, plus up to 0.05 for a trusted uploader and 0.05 for popularity. Sorted by score, at most 20 (50 max).
- A 400 or 406 (a query OpenSubtitles cannot answer) moves to the next query; nothing to ask (no usable hash, ID or title of two characters) sends no request.

## Download

`POST {base}/download {file_id}` with the token, then a plain GET of the returned link (an `http://` link is upgraded to https; no key or token is sent to it), at most 2 MB. The file name's directory part is dropped. Nothing is kept: links are used right away. 404/410 are not found.

## Settings

| Key | Type | Description |
| --- | --- | --- |
| `username` | string, required | The OpenSubtitles.com user name, **not the email address** (OpenSubtitles refuses it): a value containing `@` is rejected by `Configure` with a field error in en, zh-TW and zh-CN |
| `password` | secret, required | The account password |
| `include_ai` | bool, default off | Also search machine and AI translated subtitles (`machine_translated` and `ai_translated` become `include`; they are excluded by default) |
| `api_key` | secret (advanced) | Your own application key; empty uses the built-in one |
| `base_url` | string (advanced) | Defaults to `https://api.opensubtitles.com/api/v1`; use only with a trusted HTTPS proxy |

## Version and bundling

The version number is written only in `VERSION` (embedded with `go:embed`; it is also the plugin's reported version). `CATALOG.json` holds the display data for the plugin catalog. Releases are built by the CI workflows in `.github/workflows/`: a `v*` tag builds linux/amd64 and linux/arm64 packages and creates a GitHub Release. Any change that alters the binary needs a new `VERSION`; a published version is never changed.

## Tests

`go test ./...` (`-short` skips the process test): `subtitles_test.go` runs against a fake HTTPS OpenSubtitles with recorded-shape responses and made-up names and IDs (queries by IDs and hash, title fallback, episodes, hash-only, nothing to ask, language lists, search statuses, login once and reuse, 401 re-login, VIP host rule, quota used up and the reset, reset time parsing, account errors, download errors, spacing, formats); `plugin_test.go` has `TestDescribe`, `TestConfigure` (including a build without a built-in key) and `TestPluginProcess`. Tests never call the real API.

## License

Apache License 2.0, see `LICENSE` and `NOTICE`.
