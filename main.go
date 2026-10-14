// Command opensubtitles is the official subtitle provider plugin for OpenSubtitles.com (change request 0146, capability subtitles.search).
//
// The user enters their own OpenSubtitles account (user name and password); the plugin logs in by itself for a token and searches by file hash,
// provider IDs, or title. The application (consumer) key is injected at build time and is not in the source; the settings page has an optional
// field for your own key.
package main

import "github.com/kryspal/plugin-sdk/sdk"

func main() {
	p := newPlugin()
	sdk.Main(sdk.Config{Plugin: p, SubtitleProvider: p})
}
