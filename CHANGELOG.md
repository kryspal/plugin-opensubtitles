# Changelog

All notable changes to this plugin are listed here. The version in `VERSION` is the single source of the version number; a published version is never changed.

## 1.2.2

- Rebuilt with Go 1.27.2 and golang.org/x/net v0.60.0 (security fixes).
- Includes the changes since the last published catalog version (1.1.1): the subtitle plugin is never locked (availability is decided by the server), plus the documentation and source cleanup for the public repository.

## 1.2.1

- Subtitle search and download on OpenSubtitles.com; the plugin is never locked, availability is decided by the server.
- The application key is injected at release build time and is not in the source.
- Development history before this release lives in the Kryspal Media main repository; this repository starts from this version.
