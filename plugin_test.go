package main

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	"github.com/kryspal/plugin-sdk"
	pluginv1 "github.com/kryspal/plugin-sdk/pluginv1"
)

var han = regexp.MustCompile(`\p{Han}`)

func TestDescribe(t *testing.T) {
	p := newPlugin()
	for _, lang := range []string{"en", "zh-TW", "zh-CN", "", "ja"} {
		m, err := p.Describe(context.Background(), &pluginv1.DescribeRequest{Language: lang})
		if err != nil {
			t.Fatal(err)
		}
		if m.GetId() != "opensubtitles" || m.GetName() != "OpenSubtitles" || m.GetDescription() == "" || m.GetVersion() != version() ||
			!slices.Equal(m.GetCapabilities(), []string{pluginv1.CapabilitySubtitlesSearch}) ||
			len(m.GetFeatures()) != 0 || m.GetSuggestedConcurrency() != 1 || len(m.GetConfigFields()) != 5 {
			t.Errorf("Describe(%q) = %v", lang, m)
		}
		f := m.GetConfigFields()
		if !f[0].GetRequired() || f[0].GetType() != pluginv1.ConfigFieldType_CONFIG_FIELD_TYPE_STRING || !f[1].GetRequired() || f[1].GetType() != pluginv1.ConfigFieldType_CONFIG_FIELD_TYPE_SECRET ||
			f[2].GetKey() != keyIncludeAI || f[2].GetType() != pluginv1.ConfigFieldType_CONFIG_FIELD_TYPE_BOOL || f[2].GetDefaultValue() != "false" ||
			f[3].GetKey() != keyAPIKey || f[3].GetRequired() || f[3].GetType() != pluginv1.ConfigFieldType_CONFIG_FIELD_TYPE_SECRET || f[3].GetGroup() != "advanced" {
			t.Errorf("the user name is a required string, the password a required secret, the AI switch an optional bool off by default, the own key an optional advanced secret: %v", f)
		}
		if m.GetAttribution().GetText() != attributionText || m.GetAttribution().GetLink() != "https://www.opensubtitles.com" {
			t.Errorf("attribution = %v", m.GetAttribution())
		}
		if (lang == "en" || lang == "" || lang == "ja") && han.MatchString(m.GetDescription()) {
			t.Errorf("Describe(%q): Han characters in the English text", lang)
		}
	}
	for _, tag := range messages.Tags() {
		for key := range messageTables["en"] {
			if messages.For(tag).T(key) == "" {
				t.Errorf("%s: missing %s", tag, key)
			}
		}
	}
	if strings.ContainsAny(messageTables["zh-TW"]["desc"], "么识断读网为") {
		t.Error("the zh-TW text has simplified characters")
	}
	if strings.ContainsAny(messageTables["zh-CN"]["desc"], "麼識斷讀網為") {
		t.Error("the zh-CN text has traditional characters")
	}
}

// TestConfigureRejectsEmail: OpenSubtitles signs in with the user name only; an email address is refused in every language, with a message that says so.
func TestConfigureRejectsEmail(t *testing.T) {
	withAppKey(t, "built-in-key")
	for lang, want := range map[string]string{"en": "not the email", "zh-TW": "不是電子郵件", "zh-CN": "不是邮箱"} {
		resp, err := newPlugin().Configure(context.Background(), &pluginv1.ConfigureRequest{Language: lang, Values: map[string]string{keyUsername: "me@example.com", keyPassword: "pw"}})
		if err != nil || !strings.Contains(resp.GetFieldErrors()[keyUsername], want) {
			t.Errorf("%s: %v, %v; want it to say %q", lang, resp.GetFieldErrors(), err, want)
		}
	}
}

func TestConfigure(t *testing.T) {
	withAppKey(t, "built-in-key")
	acct := func(extra map[string]string) map[string]string {
		m := map[string]string{keyUsername: "someone", keyPassword: "pw"}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	p := newPlugin()
	for name, c := range map[string]struct {
		values map[string]string
		field  string // the field expected to error; empty means it passes
	}{
		"account only":   {acct(nil), ""},
		"no user":        {map[string]string{keyPassword: "pw"}, keyUsername},
		"no password":    {map[string]string{keyUsername: "someone"}, keyPassword},
		"own key":        {acct(map[string]string{keyAPIKey: "my-own-key"}), ""},
		"http base url":  {acct(map[string]string{keyBaseURL: "http://proxy.example"}), keyBaseURL},
		"https base url": {acct(map[string]string{keyBaseURL: "https://proxy.example/api/v1/"}), ""},
		"blank user":     {map[string]string{keyUsername: "  ", keyPassword: "pw"}, keyUsername},
		"nothing at all": {map[string]string{}, keyUsername},
		"email as user":  {map[string]string{keyUsername: "me@example.com", keyPassword: "pw"}, keyUsername},
	} {
		resp, err := p.Configure(context.Background(), &pluginv1.ConfigureRequest{Values: c.values})
		if err != nil {
			t.Fatal(err)
		}
		if c.field == "" && len(resp.GetFieldErrors()) != 0 || c.field != "" && resp.GetFieldErrors()[c.field] == "" {
			t.Errorf("%s: field errors = %v, want %q", name, resp.GetFieldErrors(), c.field)
		}
	}
	_, _ = p.Configure(context.Background(), &pluginv1.ConfigureRequest{Values: acct(map[string]string{keyUsername: " someone ", keyAPIKey: " my-own-key ", keyBaseURL: "https://proxy.example/api/v1/"})})
	if s := p.settings(); s.username != "someone" || s.password != "pw" || s.key() != "my-own-key" || s.baseURL != "https://proxy.example/api/v1" {
		t.Errorf("settings = %+v", s)
	}
	// A build without a built-in key needs the user's own.
	withAppKey(t, "")
	resp, _ := p.Configure(context.Background(), &pluginv1.ConfigureRequest{Values: acct(nil)})
	if resp.GetFieldErrors()[keyAPIKey] == "" {
		t.Errorf("without a built-in key the application key field must error: %v", resp.GetFieldErrors())
	}
	if resp, _ := p.Configure(context.Background(), &pluginv1.ConfigureRequest{Values: acct(map[string]string{keyAPIKey: "mine"})}); len(resp.GetFieldErrors()) != 0 {
		t.Errorf("own key without a built-in one = %v", resp.GetFieldErrors())
	}
}

// withAppKey sets the built-in application key for one test.
func withAppKey(t *testing.T, k string) {
	t.Helper()
	old := appKey
	appKey = k
	t.Cleanup(func() { appKey = old })
}

// TestPluginProcess is the conformance check of the plugin process (the host's way): it compiles the executable and starts it with the host's environment
// variables, checking the handshake, health check, Describe, Search (no account is needs-configuration, no network) and graceful exit after SIGTERM.
func TestPluginProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the plugin binary")
	}
	exe := filepath.Join(t.TempDir(), "opensubtitles")
	if out, err := exec.Command("go", "build", "-o", exe, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	sockDir, err := os.MkdirTemp("", "os-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(sockDir) }()
	cmd := exec.Command(exe)
	cmd.Env = []string{pluginapi.EnvCookie + "=" + pluginapi.CookieValue, pluginapi.EnvProtocols + "=1", pluginapi.EnvSocketDir + "=" + sockDir, "HOME=" + t.TempDir()}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	defer func() {
		if !stopped {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	hs, err := pluginapi.ParseHandshake(strings.TrimSpace(line))
	if err != nil || hs.Protocol != 1 || hs.Network != pluginapi.NetworkUnix {
		t.Fatalf("handshake %q: %+v, %v", line, hs, err)
	}
	conn, err := grpc.NewClient("unix:"+hs.Address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	hc := healthpb.NewHealthClient(conn)
	for _, svc := range []string{"", pluginv1.HealthServiceSubtitleProvider} {
		if r, err := hc.Check(ctx, &healthpb.HealthCheckRequest{Service: svc}); err != nil || r.GetStatus() != healthpb.HealthCheckResponse_SERVING {
			t.Errorf("health %q = %v, %v", svc, r, err)
		}
	}
	pc := pluginv1.NewPluginClient(conn)
	m, err := pc.Describe(ctx, &pluginv1.DescribeRequest{Language: "zh-TW"})
	if err != nil || m.GetId() != "opensubtitles" || m.GetProtocolVersion() != pluginv1.ProtocolVersion {
		t.Fatalf("Describe = %v, %v", m, err)
	}
	// Without an account the plugin needs configuration, and no network request is made.
	if _, err := pluginv1.NewSubtitleProviderClient(conn).Search(ctx, &pluginv1.SearchSubtitlesRequest{}); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("Search without an account = %v, want FAILED_PRECONDITION", err)
	}
	if _, err := pluginv1.NewMetadataProviderClient(conn).Search(ctx, &pluginv1.SearchRequest{}); err == nil {
		t.Error("the metadata service must not be registered")
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	stopped = true
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("exit after SIGTERM: %v", err)
		}
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		t.Error("the plugin did not exit after SIGTERM")
	}
}
