# Changelog

All notable changes to this plugin are listed here. The version in `VERSION` is the single source of the version number; a published version is never changed.

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
