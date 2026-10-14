# plugin-opensubtitles

Search and download subtitles in many languages from OpenSubtitles.com, matched by file hash, provider ID or title. Sign in with your own account. An official [Kryspal Media](https://github.com/kryspal) plugin, built on [plugin-sdk](https://github.com/kryspal/plugin-sdk).

```sh
go build -o opensubtitles .
go vet ./... && go test ./...
```

Implementation notes: [docs/design.md](docs/design.md).

Licensed under the Apache License 2.0 (see `LICENSE` and `NOTICE`).

Official builds are signed and published through the Kryspal plugin catalog; builds from source are treated as third-party plugins.
