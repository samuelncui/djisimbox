package setup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samuelncui/djisimbox/internal/config"
	"github.com/samuelncui/djisimbox/internal/modem"
	"github.com/samuelncui/djisimbox/internal/telegram"
)

const (
	token   = "123456789:ABCDEFGHIJKLMNOPQRSTUVWXYZ_abcdef"
	iccidA  = "89860000000000000001"
	iccidB  = "89860000000000000002"
	iccidC  = "89860000000000000003"
	numberA = "+6591234567"
	numberB = "+8613261231131"
)

type fakeBot struct {
	username string
	meErr    error
	chats    []telegram.Chat
	chatsErr error
}

func (b *fakeBot) Me(context.Context) (string, error) { return b.username, b.meErr }

func (b *fakeBot) RecentChats(context.Context) ([]telegram.Chat, error) { return b.chats, b.chatsErr }

type harness struct {
	t         *testing.T
	dir       string
	out       bytes.Buffer
	devices   []modem.Device
	bot       *fakeBot
	installed bool
	starts    int
	tokens    []string
	waits     int
	// appearAfter hides the SIMs until that many waits happened.
	appearAfter int
}

func newHarness(t *testing.T) *harness {
	return &harness{
		t:       t,
		dir:     t.TempDir(),
		devices: []modem.Device{{SIMID: iccidA, OwnNumber: numberA}},
		bot:     &fakeBot{username: "sim_bot", chats: []telegram.Chat{{ID: 7756947342, Name: "Lysa Chen"}}},
	}
}

func (h *harness) configPath() string  { return filepath.Join(h.dir, "config.yaml") }
func (h *harness) secretsPath() string { return filepath.Join(h.dir, "secrets.yaml") }

func (h *harness) env(in io.Reader) Env {
	return Env{
		In:  in,
		Out: &h.out,
		Devices: func(context.Context) ([]modem.Device, error) {
			if h.waits < h.appearAfter {
				return nil, nil
			}
			return h.devices, nil
		},
		NewBot: func(value string) (Bot, error) {
			h.tokens = append(h.tokens, value)
			return h.bot, nil
		},
		ServiceInstalled: func(context.Context) bool { return h.installed },
		StartService: func(context.Context) error {
			h.starts++
			return nil
		},
		Wait: func(context.Context, time.Duration) error {
			h.waits++
			return nil
		},
	}
}

func (h *harness) options(opts Options) Options {
	opts.ConfigPath = h.configPath()
	opts.SecretsFile = h.secretsPath()
	if opts.ServiceConfig == "" {
		opts.ServiceConfig = h.configPath()
	}
	return opts
}

func (h *harness) run(input string, opts Options) error {
	h.out.Reset()
	err := Run(context.Background(), h.options(opts), h.env(strings.NewReader(input)))
	if strings.Contains(h.out.String(), token) || (err != nil && strings.Contains(err.Error(), token)) {
		h.t.Error("the bot token leaked into output or an error")
	}
	return err
}

func (h *harness) load() *config.Config {
	h.t.Helper()
	cfg, err := config.Load(h.configPath())
	if err != nil {
		h.t.Fatalf("saved configuration does not load: %v", err)
	}
	return cfg
}

func (h *harness) read(path string) string {
	h.t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		h.t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func (h *harness) mustRun(input string, opts Options) {
	h.t.Helper()
	if err := h.run(input, opts); err != nil {
		h.t.Fatalf("Run() error: %v\noutput:\n%s", err, h.out.String())
	}
}

func (h *harness) tokenFile() string {
	h.t.Helper()
	path := filepath.Join(h.dir, "token")
	if err := os.WriteFile(path, []byte(token+"\n"), 0600); err != nil {
		h.t.Fatal(err)
	}
	return path
}

func recipientIDs(route config.Route) []string {
	var ids []string
	for _, recipient := range route.Recipients {
		ids = append(ids, recipient.ChatID)
	}
	return ids
}

func assertRecipients(t *testing.T, cfg *config.Config, iccid string, want ...string) {
	t.Helper()
	route, ok := cfg.Route(iccid)
	if len(want) == 0 {
		if ok {
			t.Errorf("route %s exists, want none", iccid)
		}
		return
	}
	if !ok || strings.Join(recipientIDs(route), ",") != strings.Join(want, ",") {
		t.Errorf("route %s = %v (found %v), want %v", iccid, recipientIDs(route), ok, want)
	}
}

func (h *harness) firstRun() {
	h.t.Helper()
	// Chinese, new token, first chat in the list, save.
	h.mustRun("1\n"+token+"\n1\n\n", Options{})
}

func TestFirstRunChinese(t *testing.T) {
	h := newHarness(t)
	h.firstRun()

	// Both files are private, valid, and explained with comments.
	for _, path := range []string{h.configPath(), h.secretsPath()} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Errorf("%s: mode %v, err %v; want 0600", path, info.Mode().Perm(), err)
		}
	}
	cfg := h.load()
	assertRecipients(t, cfg, iccidA, "7756947342")
	if cfg.Language != config.LanguageChinese || cfg.Bots["default"] != token {
		t.Errorf("language %q or bot not saved", cfg.Language)
	}
	text := h.read(h.configPath())
	if !strings.Contains(text, "# +6591234567") || !strings.Contains(text, "# DJI SIM Box") {
		t.Errorf("config lacks comments:\n%s", text)
	}
	if strings.Contains(text, "\n    name:") || !strings.Contains(h.read(h.secretsPath()), "# ") {
		t.Errorf("unexpected name or missing secrets comment")
	}
	if !strings.Contains(h.out.String(), "Lysa Chen (7756947342)") {
		t.Errorf("chat list not shown:\n%s", h.out.String())
	}
}

func TestRerunKeepsEverything(t *testing.T) {
	h := newHarness(t)
	h.firstRun()
	before := h.read(h.configPath())

	// Enter at every prompt: language, bot, recipients, save.
	h.mustRun("\n\n\n\n", Options{})
	if after := h.read(h.configPath()); after != before {
		t.Errorf("config changed on a no-op rerun:\n%s\nvs\n%s", before, after)
	}
}

func TestChangeRecipients(t *testing.T) {
	h := newHarness(t)
	h.firstRun()

	// Mix a list number, raw IDs, and a group ID with its minus sign; switch to English.
	h.mustRun("2\n\n1, 99887766 -100123\n\n", Options{})
	cfg := h.load()
	assertRecipients(t, cfg, iccidA, "7756947342", "99887766", "-100123")
	if cfg.Language != config.LanguageEnglish {
		t.Errorf("language = %q, want en", cfg.Language)
	}
}

func TestRemoveRoute(t *testing.T) {
	h := newHarness(t)
	h.firstRun()
	h.mustRun("\n\n-\n\n", Options{})
	assertRecipients(t, h.load(), iccidA)
}

func TestDisconnectedRoutesKept(t *testing.T) {
	h := newHarness(t)
	h.firstRun()

	// Only another SIM is connected now.
	h.devices = []modem.Device{{SIMID: iccidB}}
	h.mustRun("\n\n12345\n\n", Options{})
	cfg := h.load()
	assertRecipients(t, cfg, iccidA, "7756947342")
	assertRecipients(t, cfg, iccidB, "12345")
	if !strings.Contains(h.out.String(), "1 配置") && !strings.Contains(h.out.String(), "另有 1 张") {
		t.Errorf("kept count not reported:\n%s", h.out.String())
	}
	if !strings.Contains(h.out.String(), "号码未知") {
		t.Errorf("unknown number not shown")
	}
}

func TestInvalidChatInputReprompts(t *testing.T) {
	h := newHarness(t)
	h.mustRun("2\n"+token+"\nabc\n0\n1\n\n", Options{})
	assertRecipients(t, h.load(), iccidA, "7756947342")
	if strings.Count(h.out.String(), "Not understood") != 2 {
		t.Errorf("expected two rejections:\n%s", h.out.String())
	}
}

func TestEmptyChatListRefreshes(t *testing.T) {
	h := newHarness(t)
	h.bot.chats = nil
	// Enter refreshes while the list is empty; a typed ID then ends the prompt.
	h.mustRun("2\n"+token+"\n\n5555\n\n", Options{})
	assertRecipients(t, h.load(), iccidA, "5555")
	if strings.Count(h.out.String(), "No chats yet") != 2 {
		t.Errorf("list was not refreshed:\n%s", h.out.String())
	}
}

func TestChatListFailureAllowsTypedIDs(t *testing.T) {
	h := newHarness(t)
	h.bot.chatsErr = errors.New("conflict")
	h.mustRun("2\n"+token+"\n5555\n\n", Options{})
	assertRecipients(t, h.load(), iccidA, "5555")
	if !strings.Contains(h.out.String(), "cannot be listed") {
		t.Errorf("failure not explained:\n%s", h.out.String())
	}
}

func TestNoSIMWaits(t *testing.T) {
	h := newHarness(t)
	h.devices = nil
	if err := h.run("2\n"+token+"\n\nq\n", Options{}); err == nil {
		t.Fatal("expected quit error")
	}
	if !strings.Contains(h.out.String(), "No SIM found") || strings.Count(h.out.String(), "No SIM found") != 2 {
		t.Errorf("no-SIM prompt not repeated:\n%s", h.out.String())
	}
}

func TestBadTokensGiveUp(t *testing.T) {
	h := newHarness(t)
	h.bot.meErr = errors.New("rejected")
	// One malformed token, then two that Telegram refuses.
	err := h.run("2\nnot-a-token\n"+token+"\n"+token+"\n", Options{})
	if err == nil || !strings.Contains(err.Error(), "3 attempts") {
		t.Fatalf("Run() error = %v, want give-up error", err)
	}
	if len(h.tokens) != 2 {
		t.Errorf("getMe attempts = %d, want 2", len(h.tokens))
	}
	if _, err := os.Stat(h.configPath()); err == nil {
		t.Error("config written after failure")
	}
}

func TestBrokenConfigDeclined(t *testing.T) {
	h := newHarness(t)
	if err := os.WriteFile(h.configPath(), []byte("version: 7\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := h.run("n\n", Options{Language: "en"}); err == nil {
		t.Fatal("expected an error when the user declines a new configuration")
	}
	if h.read(h.configPath()) != "version: 7\n" {
		t.Error("broken config was changed")
	}
	if _, err := os.Stat(h.secretsPath()); err == nil {
		t.Error("secrets written")
	}
	if !strings.Contains(h.out.String(), "cannot be used") {
		t.Errorf("error not shown:\n%s", h.out.String())
	}
}

func TestBrokenConfigStartOver(t *testing.T) {
	h := newHarness(t)
	if err := os.WriteFile(h.configPath(), []byte("version: 7\n"), 0600); err != nil {
		t.Fatal(err)
	}
	h.mustRun("y\n"+token+"\n1\n\n", Options{Language: "en"})
	assertRecipients(t, h.load(), iccidA, "7756947342")
}

func TestEnglishPrompts(t *testing.T) {
	h := newHarness(t)
	h.mustRun(token+"\n1\n\n", Options{Language: "en"})
	for _, want := range []string{"Paste the bot token", "Save? [Y/n]", "Bot verified: @sim_bot"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, h.out.String())
		}
	}
	if strings.Contains(h.out.String(), "Language / 语言") {
		t.Error("language prompt shown despite -language")
	}
}

func TestDeclineSave(t *testing.T) {
	h := newHarness(t)
	h.installed = true
	h.mustRun("2\n"+token+"\n1\nn\n", Options{})
	if _, err := os.Stat(h.configPath()); err == nil {
		t.Error("config written")
	}
	if h.starts != 0 || !strings.Contains(h.out.String(), "No changes saved") {
		t.Errorf("restarts=%d, output:\n%s", h.starts, h.out.String())
	}
}

func TestRestartOnlyWhenActive(t *testing.T) {
	for _, active := range []bool{false, true} {
		h := newHarness(t)
		h.installed = active
		h.mustRun("2\n"+token+"\n1\n\n", Options{})
		want := 0
		if active {
			want = 1
		}
		if h.starts != want {
			t.Errorf("active=%v: restarts = %d, want %d", active, h.starts, want)
		}
	}
}

func TestYesFirstRunRoutes(t *testing.T) {
	h := newHarness(t)
	h.devices = []modem.Device{{SIMID: iccidA, OwnNumber: numberA}, {SIMID: iccidB, OwnNumber: numberB}}
	h.installed = true

	// ICCID, phone number, and a SIM that is not connected yet.
	h.mustRun("", Options{Yes: true, Language: "en", TokenFile: h.tokenFile(), Routes: []string{
		iccidA + "=111,-222", numberB + "=333", iccidC + "=444",
	}})
	cfg := h.load()
	assertRecipients(t, cfg, iccidA, "111", "-222")
	assertRecipients(t, cfg, iccidB, "333")
	assertRecipients(t, cfg, iccidC, "444")
	if h.starts != 1 {
		t.Errorf("restarts = %d, want 1", h.starts)
	}

	// A rerun without options keeps everything.
	before := h.read(h.configPath())
	h.mustRun("", Options{Yes: true})
	if h.read(h.configPath()) != before {
		t.Error("-yes rerun changed the config")
	}

	// Later options override earlier ones; "-" removes.
	h.mustRun("", Options{Yes: true, Routes: []string{"all=555", numberA + "=-"}})
	cfg = h.load()
	assertRecipients(t, cfg, iccidA)
	assertRecipients(t, cfg, iccidB, "555")
	assertRecipients(t, cfg, iccidC, "444")
}

func TestYesAllAndNote(t *testing.T) {
	h := newHarness(t)
	h.devices = []modem.Device{{SIMID: iccidA, OwnNumber: numberA}, {SIMID: iccidB}}
	h.mustRun("", Options{Yes: true, TokenFile: h.tokenFile(), Routes: []string{"all=9"}})
	cfg := h.load()
	assertRecipients(t, cfg, iccidA, "9")
	assertRecipients(t, cfg, iccidB, "9")

	// A SIM without route or option is reported, not configured.
	h2 := newHarness(t)
	h2.mustRun("", Options{Yes: true, Language: "en", TokenFile: h2.tokenFile()})
	if !strings.Contains(h2.out.String(), "no recipients yet") {
		t.Errorf("note missing:\n%s", h2.out.String())
	}
}

func TestRouteTargetsMustResolve(t *testing.T) {
	h := newHarness(t)
	opts := Options{Yes: true, TokenFile: h.tokenFile()}
	opts.Routes = []string{"+000=1"}
	if err := h.run("", opts); err == nil {
		t.Error("unknown phone number accepted")
	}
	h.devices = nil
	opts.Routes = []string{"all=1"}
	if err := h.run("", opts); err == nil {
		t.Error("all accepted with no SIM")
	}
	opts.Routes = []string{"bad"}
	if err := h.run("", opts); err == nil {
		t.Error("malformed route accepted")
	}
	if _, err := os.Stat(h.configPath()); err == nil {
		t.Error("config written after errors")
	}
}

func TestBadTokenFileFails(t *testing.T) {
	h := newHarness(t)
	path := filepath.Join(h.dir, "token")
	secret := "bad-token-value-xyz"
	if err := os.WriteFile(path, []byte(secret+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	err := h.run("", Options{Yes: true, TokenFile: path})
	if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(h.out.String(), secret) {
		t.Errorf("Run() error = %v; want an error without the token", err)
	}

	// A token Telegram refuses also fails without a prompt.
	h.bot.meErr = errors.New("rejected")
	if err := h.run("", Options{Yes: true, TokenFile: h.tokenFile()}); err == nil {
		t.Error("rejected token accepted")
	}
}

func TestYesWithoutTokenFails(t *testing.T) {
	h := newHarness(t)
	err := h.run("", Options{Yes: true})
	if err == nil || !strings.Contains(err.Error(), "--token-file") {
		t.Errorf("Run() error = %v, want token-file hint", err)
	}
}

func TestCancelDuringPrompt(t *testing.T) {
	h := newHarness(t)
	reader, writer := io.Pipe()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, h.options(Options{Language: "en"}), h.env(reader)) }()

	// Cancel while the token prompt waits for input that never comes.
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
	if _, err := os.Stat(h.configPath()); err == nil || h.starts != 0 {
		t.Error("files written or service touched after cancellation")
	}
}

func TestServiceUntouchedForCustomConfig(t *testing.T) {
	h := newHarness(t)
	h.installed = true
	h.mustRun("2\n"+token+"\n1\n\n", Options{ServiceConfig: "/etc/djisimbox/config.yaml"})
	if h.starts != 0 || !strings.Contains(h.out.String(), "was not changed") {
		t.Errorf("starts=%d, output:\n%s", h.starts, h.out.String())
	}
}

func TestListNumberMustBeInRange(t *testing.T) {
	h := newHarness(t)
	h.bot.chats = []telegram.Chat{{ID: 1111, Name: "A"}, {ID: 2222, Name: "B"}, {ID: 3333, Name: "C"}}
	h.mustRun("2\n"+token+"\n4\n0\n3\n\n", Options{})
	assertRecipients(t, h.load(), iccidA, "3333")
	if strings.Count(h.out.String(), "Not understood") != 2 {
		t.Errorf("out-of-range numbers were not rejected:\n%s", h.out.String())
	}

	// Without a list, a short number cannot be a chat ID either.
	h = newHarness(t)
	h.bot.chats = nil
	h.mustRun("2\n"+token+"\n1\n-1001234\n\n", Options{})
	assertRecipients(t, h.load(), iccidA, "-1001234")
}

func TestYesWaitsForSIM(t *testing.T) {
	h := newHarness(t)
	h.appearAfter = 3
	h.mustRun("", Options{Yes: true, Language: "en", TokenFile: h.tokenFile(), Routes: []string{"all=1"}})
	assertRecipients(t, h.load(), iccidA, "1")
	if h.waits != 3 || strings.Count(h.out.String(), "Waiting for the SIM") != 1 {
		t.Errorf("waits=%d, output:\n%s", h.waits, h.out.String())
	}

	// A SIM that never appears ends in an error after the full polling window.
	h = newHarness(t)
	h.devices = nil
	if err := h.run("", Options{Yes: true, TokenFile: h.tokenFile(), Routes: []string{"all=1"}}); err == nil || h.waits != pollAttempts-1 {
		t.Errorf("err=%v waits=%d, want error after %d waits", err, h.waits, pollAttempts-1)
	}
}

func TestBrokenConfigKeepsOtherBots(t *testing.T) {
	h := newHarness(t)
	other := "222222222:ABCDEFGHIJKLMNOPQRSTUVWXYZ_other"
	if err := os.WriteFile(h.configPath(), []byte("version: 7\n"), 0600); err != nil {
		t.Fatal(err)
	}
	secrets := "bots:\n  default: " + token + "\n  other: " + other + "\n"
	if err := os.WriteFile(h.secretsPath(), []byte(secrets), 0600); err != nil {
		t.Fatal(err)
	}

	// Start over, keep the saved default bot, route the SIM, save.
	h.mustRun("y\n\n1\n\n", Options{Language: "en"})
	cfg := h.load()
	if cfg.Bots["other"] != other || cfg.Bots["default"] != token {
		t.Errorf("bots not carried over: %d entries", len(cfg.Bots))
	}
}

func TestEmptyBotsDoNotPanic(t *testing.T) {
	h := newHarness(t)
	document := "version: 1\nroutes: []\nsecrets_file: " + h.secretsPath() + "\n"
	if err := os.WriteFile(h.configPath(), []byte(document), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.secretsPath(), []byte("bots: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	h.mustRun("2\n"+token+"\n1\n\n", Options{})
	assertRecipients(t, h.load(), iccidA, "7756947342")
}

func TestUnreachableTelegram(t *testing.T) {
	h := newHarness(t)
	h.bot.meErr = &telegram.Error{}

	// Declining the retry stops at once instead of burning attempts.
	err := h.run("2\n"+token+"\nn\n", Options{})
	if err == nil || !strings.Contains(err.Error(), "cannot reach Telegram") || len(h.tokens) != 1 {
		t.Errorf("err=%v attempts=%d", err, len(h.tokens))
	}
	if !strings.Contains(h.out.String(), "Cannot reach Telegram") || strings.Contains(h.out.String(), "did not accept") {
		t.Errorf("wrong explanation:\n%s", h.out.String())
	}

	// Retrying asks Telegram again with the same token.
	h = newHarness(t)
	h.bot.meErr = &telegram.Error{}
	_ = h.run("2\n"+token+"\ny\nn\n", Options{})
	if len(h.tokens) != 2 {
		t.Errorf("attempts = %d, want 2", len(h.tokens))
	}

	// Non-interactive runs fail with the same message.
	h = newHarness(t)
	h.bot.meErr = &telegram.Error{}
	err = h.run("", Options{Yes: true, TokenFile: h.tokenFile()})
	if err == nil || !strings.Contains(err.Error(), "cannot reach Telegram") {
		t.Errorf("err = %v", err)
	}
}
