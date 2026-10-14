package main

import (
	"context"
	_ "embed"
	"net/url"
	"strings"
	"sync"
	"time"

	pluginv1 "github.com/kryspal/plugin-sdk/pluginv1"
)

const (
	pluginID = "opensubtitles"

	keyUsername  = "username"
	keyPassword  = "password"
	keyAPIKey    = "api_key"
	keyBaseURL   = "base_url"
	keyIncludeAI = "include_ai"

	defaultBaseURL  = "https://api.opensubtitles.com/api/v1"
	attributionText = "Subtitles provided by OpenSubtitles.com"
	attributionLink = "https://www.opensubtitles.com"
)

// appKey is the application (consumer) key registered for Kryspal Media. It is not in the source: the release build injects it with
//
//	go build -ldflags "-X main.appKey=$OPENSUBTITLES_CONSUMER_KEY"
//
// A build without it (a fork, a local build) needs the user's own key in the advanced setting.
var appKey string

// versionFile is the single source of the plugin version: the manifest's version.
//
//go:embed VERSION
var versionFile string

func version() string { return strings.TrimSpace(versionFile) }

// settings are the values saved by Configure.
type settings struct {
	username, password string
	apiKey             string // the user's own key; empty means the built-in appKey
	baseURL            string
	includeAI          bool // also accept machine and AI translated subtitles
}

// key is the application key used for requests: the user's own when given, else the built-in one.
func (s settings) key() string {
	if s.apiKey != "" {
		return s.apiKey
	}
	return appKey
}

type plugin struct {
	pluginv1.UnimplementedPluginServer
	pluginv1.UnimplementedSubtitleProviderServer

	mu  sync.Mutex
	set settings
	// rejected is the account settings OpenSubtitles refused at login; while the settings stay the same the plugin does not try again (it would only
	// repeat the refusal) and answers UNAUTHENTICATED, which the host shows as a wrong user name or password. New settings clear it.
	rejected settings
	// The login session: the token and the API host the login named (VIP accounts use another host). A new Configure drops it.
	token     string
	tokenAt   time.Time
	apiBase   string
	allowed   int       // the account's daily download allowance, as the login reported it
	quotaLeft *int      // remaining downloads as the last download reported it
	quotaEnds time.Time // when the quota resets (when it is used up)
	quotaAt   time.Time // when quotaLeft was last learned (a download reply or /infos/user)
	quotaTry  time.Time // when /infos/user was last asked for, so a failing lookup is not repeated on every search
	api       *api      // replaceable in tests
	now       func() time.Time
	// noQuotaLookup turns the /infos/user reading off (tests that look at the exact requests of a search).
	noQuotaLookup bool
}

func newPlugin() *plugin {
	return &plugin{api: newAPI(), now: time.Now}
}

// Describe declares subtitles.search. The account is required: without it the plugin is in the needs-configuration state.
func (p *plugin) Describe(_ context.Context, req *pluginv1.DescribeRequest) (*pluginv1.Manifest, error) {
	t := messages.For(req.GetLanguage())
	return &pluginv1.Manifest{
		Id: pluginID, Name: "OpenSubtitles", Description: t.T("desc"), Version: version(), ProtocolVersion: pluginv1.ProtocolVersion,
		Capabilities: []string{pluginv1.CapabilitySubtitlesSearch},
		ConfigGroups: []*pluginv1.ConfigGroup{{Key: "advanced", Title: t.T("groupAdvanced")}},
		ConfigFields: []*pluginv1.ConfigField{
			{Key: keyUsername, Type: pluginv1.ConfigFieldType_CONFIG_FIELD_TYPE_STRING, Required: true, Label: t.T("userLabel"),
				Description: t.T("userHelp"), Link: "https://www.opensubtitles.com/", LinkLabel: t.T("userLink")},
			{Key: keyPassword, Type: pluginv1.ConfigFieldType_CONFIG_FIELD_TYPE_SECRET, Required: true, Label: t.T("passLabel"), Description: t.T("passHelp")},
			{Key: keyIncludeAI, Type: pluginv1.ConfigFieldType_CONFIG_FIELD_TYPE_BOOL, DefaultValue: "false", Label: t.T("aiLabel"), Description: t.T("aiHelp")},
			{Key: keyAPIKey, Type: pluginv1.ConfigFieldType_CONFIG_FIELD_TYPE_SECRET, Group: "advanced", Label: t.T("keyLabel"), Description: t.T("keyHelp"),
				Link: "https://www.opensubtitles.com/consumers", LinkLabel: t.T("keyLink")},
			{Key: keyBaseURL, Type: pluginv1.ConfigFieldType_CONFIG_FIELD_TYPE_STRING, Group: "advanced", Label: t.T("baseURLLabel"),
				Description: t.T("baseURLHelp"), Placeholder: defaultBaseURL},
		},
		Attribution:          &pluginv1.Attribution{Text: attributionText, Link: attributionLink},
		SuggestedConcurrency: 1,
	}, nil
}

// Configure validates the settings: account name (not an email address) and password are required; an application key must exist (built in or the user's own); the URL
// must be https. It does not log in (a wrong account is reported by the first call as needing configuration).
func (p *plugin) Configure(_ context.Context, req *pluginv1.ConfigureRequest) (*pluginv1.ConfigureResponse, error) {
	t := messages.For(req.GetLanguage())
	v := req.GetValues()
	resp := &pluginv1.ConfigureResponse{FieldErrors: map[string]string{}}
	s := settings{username: strings.TrimSpace(v[keyUsername]), password: v[keyPassword], apiKey: strings.TrimSpace(v[keyAPIKey]), baseURL: strings.TrimSpace(v[keyBaseURL]), includeAI: v[keyIncludeAI] == "true"}
	switch {
	case s.username == "":
		resp.FieldErrors[keyUsername] = t.T("errUserRequired")
	case strings.Contains(s.username, "@"):
		resp.FieldErrors[keyUsername] = t.T("errUserIsEmail") // OpenSubtitles signs in with the user name only
	}
	if s.password == "" {
		resp.FieldErrors[keyPassword] = t.T("errPassRequired")
	}
	if s.key() == "" {
		resp.FieldErrors[keyAPIKey] = t.T("errNoKey")
	}
	if s.baseURL == "" {
		s.baseURL = defaultBaseURL
	}
	if u, err := url.Parse(s.baseURL); err != nil || u.Scheme != "https" || u.Host == "" {
		resp.FieldErrors[keyBaseURL] = t.T("errHTTPS")
	}
	if len(resp.FieldErrors) > 0 {
		return resp, nil
	}
	s.baseURL = strings.TrimRight(s.baseURL, "/")
	p.mu.Lock()
	p.set = s
	p.token, p.apiBase, p.quotaLeft, p.quotaEnds, p.quotaAt, p.quotaTry, p.rejected = "", "", nil, time.Time{}, time.Time{}, time.Time{}, settings{} // a new account or key starts a new session
	p.mu.Unlock()
	return resp, nil
}

func (p *plugin) settings() settings {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.set
}

// forgetAccount clears the account after OpenSubtitles rejected it, unless new settings were saved in the meantime.
func (p *plugin) forgetAccount(rejected settings) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.set == rejected {
		p.set = settings{}
		p.token = ""
	}
}
