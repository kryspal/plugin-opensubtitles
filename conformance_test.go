package main

import (
	"net/http"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/kryspal/plugin-sdk/plugintest"
	pluginv1 "github.com/kryspal/plugin-sdk/pluginv1"
)

// TestConformance builds the plugin and runs the SDK conformance suite against the binary, with a fake OpenSubtitles over HTTPS
// (the plugin only accepts an https base URL, and trusts the fake's certificate through SSL_CERT_FILE).
func TestConformance(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "plugin")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	up := plugintest.NewTLSUpstream(t)
	up.Handle("POST /login", plugintest.JSON(http.StatusOK, map[string]any{"token": "test-token", "user": map[string]any{"allowed_downloads": 100}}))
	up.Handle("GET /infos/user", plugintest.JSON(http.StatusOK, map[string]any{"data": map[string]any{"remaining_downloads": 95}}))
	up.Handle("GET /subtitles", plugintest.JSON(http.StatusOK, map[string]any{"data": []any{map[string]any{"attributes": map[string]any{
		"language": "en", "download_count": 42, "ratings": 7.5, "release": "Sample.Movie.2020.1080p", "from_trusted": true,
		"files": []any{map[string]any{"file_id": 1001, "file_name": "Sample.Movie.2020.srt"}},
	}}}}))
	plugintest.Run(t, bin, plugintest.Options{
		Config:    map[string]string{"username": "test", "password": "test", "api_key": "test", "base_url": up.URL},
		Env:       up.Env(),
		Subtitles: &plugintest.SubtitleCase{Type: pluginv1.ItemType_ITEM_TYPE_MOVIE, Title: "Sample Movie", Year: 2020, ProviderIDs: map[string]string{"imdb": "tt0000001"}},
	})
}
