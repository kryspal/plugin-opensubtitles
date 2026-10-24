# plugin-opensubtitles

Search and download subtitles in many languages from OpenSubtitles.com, matched by file hash, provider ID or title. Sign in with your own account. An official [Kryspal Media](https://github.com/kryspal) plugin, built on [plugin-sdk](https://github.com/kryspal/plugin-sdk).

```sh
go build -o opensubtitles .
go vet ./... && go test ./...
```

## Automatic download

A server that downloads subtitles by itself takes the hash and ID matches on its own and, besides them, only the candidates the plugin marks as confirmed (`SubtitleCandidate.confirmed`). A subtitle found by title is confirmed in two cases and no other:

- it was made for this very release: its release name equals the video's file name (extension removed, case and separators ignored, at least 8 characters); or
- OpenSubtitles says which work it belongs to and that is the video's work: the title equals the video's title or original title and, for a movie, the year is the same; for an episode, the series title and the season and episode numbers are the same.

A machine or AI translation is never confirmed. Hash and ID matches are not marked, because the server decides them without the flag. Candidates come best first: hash, IDs, made for this release, belongs to this work, then the rest by score. Everything not confirmed stays in the list for the user to choose from. A server that does not know the field ignores it.

Implementation notes: [docs/design.md](docs/design.md).

Licensed under the Apache License 2.0 (see `LICENSE` and `NOTICE`).

Official builds are signed and published through the Kryspal plugin catalog; builds from source are treated as third-party plugins.
