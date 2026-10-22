package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testToken = "123456789:synthetic_test_token_0123456789"

func TestChatIDsAreCanonicalBeforeDelivery(t *testing.T) {
	// Numeric aliases accepted by parsing must become valid Telegram request identifiers.
	document := baseDocument()
	document["routes"] = []Route{{ICCID: "89000000000000000001", Name: "SIM",
		Recipients: []Recipient{{Bot: "default", ChatID: "+00123"}, {Bot: "default", ChatID: "-00456"}}}}
	path := writeFixture(t, document, validSecrets(), 0600)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	route, ok := cfg.Route("89000000000000000001")
	if !ok || route.Recipients[0].ChatID != "123" || route.Recipients[1].ChatID != "-456" {
		t.Fatal("numeric aliases reached the delivery client")
	}
}

func TestLoadDefaultsAndSIMRoutes(t *testing.T) {
	// Give two SIMs independent recipients and one shared bot credential.
	document := baseDocument()
	document["routes"] = []Route{
		{ICCID: "89000000000000000001", Name: "Personal", Recipients: []Recipient{{Bot: "default", ChatID: "123456789"}}},
		{ICCID: "89000000000000000002", Name: "工作", Recipients: []Recipient{
			{Bot: "work", ChatID: "-100000000001"},
			{Bot: "default", ChatID: "987654321"},
		}},
	}
	path := writeFixture(t, document, `{"bots":{"default":"`+testToken+`","work":"`+testToken+`"}}`, 0600)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Omitted values use service defaults while routes preserve recipient order.
	if cfg.RuntimeDir != "/run/djisimbox" || cfg.MaxPending != 10000 || cfg.MaxBytes != 16777216 {
		t.Fatalf("unexpected defaults: runtime_dir=%q max_pending=%d max_bytes=%d",
			cfg.RuntimeDir, cfg.MaxPending, cfg.MaxBytes)
	}
	if len(cfg.Bots) != 2 || cfg.Bots["work"] != testToken {
		t.Fatal("named bot credentials were not loaded")
	}
	personal, ok := cfg.Route("89000000000000000001")
	if !ok || personal.Name != "Personal" || len(personal.Recipients) != 1 || personal.Recipients[0].ChatID != "123456789" {
		t.Fatal("personal SIM did not map to its recipient")
	}
	work, ok := cfg.Route("89000000000000000002")
	if !ok || work.Name != "工作" || len(work.Recipients) != 2 || work.Recipients[0].Bot != "work" {
		t.Fatal("work SIM did not map to its ordered recipients")
	}
	if _, ok := cfg.Route("89000000000000000003"); ok {
		t.Fatal("unknown SIM unexpectedly received a route")
	}
}

func TestLoadEmptyRoutesQuarantinesAllSIMs(t *testing.T) {
	// An operator can start safely before any SIM mapping or credential exists.
	document := baseDocument()
	document["routes"] = []Route{}
	path := writeFixture(t, document, `{"bots":{}}`, 0400)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := cfg.Route("89000000000000000001"); ok {
		t.Fatal("empty routing configuration matched a SIM")
	}
}

func TestLoadConfigurationValidation(t *testing.T) {
	// Every case changes one valid configuration rule while keeping secrets valid.
	cases := []struct {
		name   string
		change func(map[string]any)
	}{
		{"missing version", func(doc map[string]any) { delete(doc, "version") }},
		{"unsupported version", func(doc map[string]any) { doc["version"] = 2 }},
		{"relative runtime", func(doc map[string]any) { doc["runtime_dir"] = "djisimbox" }},
		{"empty runtime", func(doc map[string]any) { doc["runtime_dir"] = "" }},
		{"unclean runtime", func(doc map[string]any) { doc["runtime_dir"] = "/run/../tmp/djisimbox" }},
		{"repeated slash runtime", func(doc map[string]any) { doc["runtime_dir"] = "/run//djisimbox" }},
		{"trailing slash runtime", func(doc map[string]any) { doc["runtime_dir"] = "/run/djisimbox/" }},
		{"NUL runtime", func(doc map[string]any) { doc["runtime_dir"] = "/run/djisimbox\x00" }},
		{"relative secrets", func(doc map[string]any) { doc["secrets_file"] = "credentials.json" }},
		{"empty secrets", func(doc map[string]any) { doc["secrets_file"] = "" }},
		{"zero pending", func(doc map[string]any) { doc["max_pending"] = 0 }},
		{"negative pending", func(doc map[string]any) { doc["max_pending"] = -1 }},
		{"excess pending", func(doc map[string]any) { doc["max_pending"] = 1000001 }},
		{"zero bytes", func(doc map[string]any) { doc["max_bytes"] = 0 }},
		{"negative bytes", func(doc map[string]any) { doc["max_bytes"] = -1 }},
		{"insufficient bytes", func(doc map[string]any) { doc["max_bytes"] = 1023 }},
		{"excess bytes", func(doc map[string]any) { doc["max_bytes"] = 1073741825 }},
		{"removed database", func(doc map[string]any) { doc["database"] = "/var/lib/djisimbox/messages.db" }},
		{"removed retention", func(doc map[string]any) { doc["retention_days"] = 30 }},
		{"unknown field", func(doc map[string]any) { doc["typo"] = "private value" }},
		{"inline credentials", func(doc map[string]any) { doc["bots"] = map[string]string{"default": testToken} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Apply the invalid field after installing the private fixture's path.
			path := writeFixture(t, baseDocument(), validSecrets(), 0600)
			document := readDocument(t, path)
			tc.change(document)
			writeDocument(t, path, document)
			if _, err := Load(path); err == nil {
				t.Fatal("invalid configuration was accepted")
			}
		})
	}
}

func TestLoadAcceptedBoundaries(t *testing.T) {
	// Exercise the inclusive limits and count display names in runes, not bytes.
	cases := []struct {
		name    string
		pending int
		bytes   int64
		iccid   string
		chatID  string
	}{
		{"minimum", 1, 1024, "890000000000000001", "-9223372036854775808"},
		{"maximum", 1000000, 1073741824, "8900000000000000000001", "9223372036854775807"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Each endpoint remains a usable route after loading.
			document := baseDocument()
			document["max_pending"] = tc.pending
			document["max_bytes"] = tc.bytes
			document["routes"] = []Route{{
				ICCID: tc.iccid, Name: strings.Repeat("中", 80),
				Recipients: []Recipient{{Bot: "default", ChatID: tc.chatID}},
			}}
			path := writeFixture(t, document, validSecrets(), 0600)
			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("boundary configuration: %v", err)
			}
			if _, ok := cfg.Route(tc.iccid); !ok {
				t.Fatal("accepted SIM was absent from the routing index")
			}
			if cfg.MaxPending != tc.pending || cfg.MaxBytes != tc.bytes {
				t.Fatal("explicit queue limits were not preserved")
			}
		})
	}
}

func TestLoadRouteValidation(t *testing.T) {
	// Invalid routing must fail before any message can be sent to a recipient.
	cases := []struct {
		name   string
		change func(*Route)
	}{
		{"short ICCID", func(route *Route) { route.ICCID = "123" }},
		{"long ICCID", func(route *Route) { route.ICCID = strings.Repeat("1", 23) }},
		{"nondigit ICCID", func(route *Route) { route.ICCID = "8900000000000000000x" }},
		{"blank name", func(route *Route) { route.Name = "  " }},
		{"long name", func(route *Route) { route.Name = strings.Repeat("中", 81) }},
		{"multiline name", func(route *Route) { route.Name = "SIM\nsecond line" }},
		{"unicode line separator", func(route *Route) { route.Name = "SIM\u2028second line" }},
		{"control in name", func(route *Route) { route.Name = "SIM\x00name" }},
		{"no recipients", func(route *Route) { route.Recipients = nil }},
		{"invalid bot name", func(route *Route) { route.Recipients[0].Bot = "invalid.bot" }},
		{"missing bot", func(route *Route) { route.Recipients[0].Bot = "missing" }},
		{"empty chat", func(route *Route) { route.Recipients[0].ChatID = "" }},
		{"noninteger chat", func(route *Route) { route.Recipients[0].ChatID = "@example" }},
		{"zero chat", func(route *Route) { route.Recipients[0].ChatID = "0" }},
		{"overflow chat", func(route *Route) { route.Recipients[0].ChatID = "9223372036854775808" }},
		{"duplicate recipient", func(route *Route) { route.Recipients = append(route.Recipients, route.Recipients[0]) }},
		{"equivalent chat", func(route *Route) {
			route.Recipients = append(route.Recipients, Recipient{Bot: "default", ChatID: "+123456789"})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Keep the secret and other routes valid to isolate the rejected rule.
			route := baseRoute()
			tc.change(&route)
			document := baseDocument()
			document["routes"] = []Route{route}
			path := writeFixture(t, document, validSecrets(), 0600)
			if _, err := Load(path); err == nil {
				t.Fatal("invalid SIM route was accepted")
			}
		})
	}
}

func TestLoadRejectsDuplicateSIM(t *testing.T) {
	// One SIM cannot silently replace an earlier recipient mapping.
	document := baseDocument()
	document["routes"] = []Route{baseRoute(), baseRoute()}
	path := writeFixture(t, document, validSecrets(), 0600)
	if _, err := Load(path); err == nil {
		t.Fatal("duplicate SIM routes were accepted")
	}
}

func TestLoadStrictJSONAndNoSecretLeakage(t *testing.T) {
	// Exercise decoder failures whose underlying errors can contain dynamic values.
	cases := []struct {
		name    string
		secrets string
	}{
		{"unknown field", `{"bots":{"default":"` + testToken + `"},"` + testToken + `":true}`},
		{"unknown nested field", `{"bots":{"default":{"` + testToken + `":true}}}`},
		{"trailing object", validSecrets() + ` {"token":"` + testToken + `"}`},
		{"trailing null", validSecrets() + ` null`},
		{"trailing garbage", validSecrets() + testToken},
		{"not object", `"` + testToken + `"`},
		{"null", `null`},
		{"invalid bot name", `{"bots":{"` + testToken + `":"` + testToken + `"}}`},
		{"invalid token prefix", `{"bots":{"default":"secret:synthetic_test_token_0123456789"}}`},
		{"short token", `{"bots":{"default":"123456789:short"}}`},
		{"long token", `{"bots":{"default":"123456789:` + strings.Repeat("x", 129) + `"}}`},
		{"invalid token characters", `{"bots":{"default":"123456789:synthetic_test_token/0123456789"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Errors are safe for production logs regardless of a malformed body.
			path := writeFixture(t, baseDocument(), tc.secrets, 0600)
			_, err := Load(path)
			if err == nil {
				t.Fatal("invalid secrets were accepted")
			}
			for _, sensitive := range []string{testToken, "89000000000000000001", "123456789", "synthetic_test_token"} {
				if strings.Contains(err.Error(), sensitive) {
					t.Fatal("error exposed credential or routing data")
				}
			}
		})
	}
}

func TestLoadStrictConfigJSON(t *testing.T) {
	// Config decoding also sanitizes untrusted keys and rejects extra documents.
	cases := []string{
		`{"version":1,"` + testToken + `":true}`,
		`{"version":1,"routes":{"` + testToken + `":true}}`,
		`{"version":1} {}`,
		`{"version":1} null`,
		`null`,
		`[]`,
	}
	for _, data := range cases {
		// Keep each malformed document isolated from previous decoder state.
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := Load(path)
		if err == nil {
			t.Fatal("invalid configuration JSON was accepted")
		}
		if strings.Contains(err.Error(), testToken) {
			t.Fatal("config decoder exposed an untrusted key")
		}
	}
}

func TestLoadPrivateSecretsPermissions(t *testing.T) {
	// Read-only owner credentials work; any group or other access is rejected.
	for _, mode := range []os.FileMode{0400, 0600, 0640, 0604, 0644, 0700} {
		t.Run(mode.String(), func(t *testing.T) {
			// Assert the same permissions policy independently of the current umask.
			path := writeFixture(t, baseDocument(), validSecrets(), mode)
			_, err := Load(path)
			if mode.Perm()&0077 == 0 {
				if err != nil {
					t.Fatalf("private file was rejected: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("shared credential file was accepted")
			}
		})
	}
}

func TestLoadFileBounds(t *testing.T) {
	// Oversized files are rejected before parsing or exposing their contents.
	path := writeFixture(t, baseDocument(), strings.Repeat(" ", maxFileSize+1), 0600)
	if _, err := Load(path); err == nil {
		t.Fatal("oversized credentials were accepted")
	}
	if err := os.WriteFile(path, []byte(strings.Repeat(" ", maxFileSize+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("oversized configuration was accepted")
	}

	// Neither directory inputs nor missing files are valid JSON documents.
	if _, err := Load(t.TempDir()); err == nil {
		t.Fatal("directory configuration was accepted")
	}
	if _, err := Load(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Fatal("missing configuration was accepted")
	}
}

func baseDocument() map[string]any {
	return map[string]any{"version": 1, "routes": []Route{baseRoute()}}
}

func baseRoute() Route {
	return Route{
		ICCID: "89000000000000000001", Name: "Test SIM",
		Recipients: []Recipient{{Bot: "default", ChatID: "123456789"}},
	}
}

func validSecrets() string {
	return `{"bots":{"default":"` + testToken + `"}}`
}

func writeFixture(t *testing.T, document map[string]any, credentials string, mode os.FileMode) string {
	t.Helper()
	// Materialize private credentials separately from the routing document.
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "secrets.json")
	if err := os.WriteFile(secretPath, []byte(credentials), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(secretPath, mode); err != nil {
		t.Fatal(err)
	}
	document["secrets_file"] = secretPath

	// Return the actual on-disk path so tests use the public load boundary.
	path := filepath.Join(dir, "config.json")
	writeDocument(t, path, document)
	return path
}

func writeDocument(t *testing.T, path string, document map[string]any) {
	t.Helper()
	// Marshal fixtures with the standard schema rather than string interpolation.
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func readDocument(t *testing.T, path string) map[string]any {
	t.Helper()
	// Decode a fixture so individual invalid fields can be changed after setup.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	return document
}
