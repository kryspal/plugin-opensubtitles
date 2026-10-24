# Changelog

All notable changes to this plugin are listed here. The version in `VERSION` is the single source of the version number; a published version is never changed.

## 1.3.0

- A subtitle found by title is marked confirmed (`SubtitleCandidate.confirmed`) when it was made for this release (its release name equals the video's file name) or OpenSubtitles files it under the video's work (same title or original title, and the same year for a movie, or the same series, season and episode for an episode). Machine and AI translations are never confirmed. A server that downloads subtitles by itself takes hash and ID matches and confirmed candidates, in the order the plugin returns them; candidates are now ordered hash, IDs, made for this release, belongs to this work, then by score. A server that does not know the field ignores it.
- Built with plugin SDK v0.8.0.

## 1.2.5

- Built with plugin SDK v0.7.0. No functional change.

## 1.2.4

- Built with plugin SDK v0.5.0. No functional change.

## 1.2.3

- Built with plugin SDK v0.2.0. No functional change.
- Needs server 0.9.2 or newer.
- Includes the documentation, release workflow and source cleanup done since the last published version.

## 1.2.2

- Rebuilt with Go 1.27.2 and golang.org/x/net v0.60.0 (security fixes).
- Includes the changes since the last published catalog version (1.1.1): the subtitle plugin is never locked (availability is decided by the server), plus the documentation and source cleanup for the public repository.

## 1.2.1

- Subtitle search and download on OpenSubtitles.com; the plugin is never locked, availability is decided by the server.
- The application key is injected at release build time and is not in the source.
- Development history before this release lives in the Kryspal Media main repository; this repository starts from this version.
