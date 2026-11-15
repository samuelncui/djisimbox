// Package setup implements the interactive "djisimbox setup" guide.
package setup

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/samuelncui/djisimbox/internal/config"
	"github.com/samuelncui/djisimbox/internal/modem"
	"github.com/samuelncui/djisimbox/internal/telegram"
)

const (
	defaultBot      = "default"
	tokenAttempts   = 3
	pollInterval    = 2 * time.Second
	pollAttempts    = 30
	serviceTimeout  = 60 * time.Second
	maxTokenFile    = 4096
	languagePrompt  = "Language / 语言: 1) 中文  2) English [%d]: "
	languageDefault = config.LanguageChinese
)

// Options are the settings given on the command line.
type Options struct {
	ConfigPath string
	// Language skips the language prompt when set.
	Language string
	// SecretsFile overrides the secrets path of a new configuration; an existing configuration keeps its own.
	SecretsFile string
	// TokenFile names a file whose first line is the bot token.
	TokenFile string
	// Routes are "SIM=CHATS" entries: SIM is an ICCID, a phone number or "all"; CHATS is a comma-separated chat ID list or "-".
	Routes []string
	// Yes never prompts and saves directly.
	Yes bool
	// ServiceConfig is the configuration file the systemd service reads; the service is only touched when ConfigPath is the same file.
	ServiceConfig string
}

// Bot is the part of the Telegram client the guide needs.
type Bot interface {
	Me(ctx context.Context) (string, error)
	RecentChats(ctx context.Context) ([]telegram.Chat, error)
}

// Env carries the guide's inputs, outputs and side effects so tests can replace them.
type Env struct {
	In      io.Reader
	Out     io.Writer
	Devices func(ctx context.Context) ([]modem.Device, error)
	NewBot  func(token string) (Bot, error)
	// ServiceInstalled reports whether the systemd unit exists; StartService enables and restarts it.
	ServiceInstalled func(ctx context.Context) bool
	StartService     func(ctx context.Context) error
	// Wait sleeps for d or until ctx ends; nil uses a real timer.
	Wait func(ctx context.Context, d time.Duration) error
}

type session struct {
	ctx  context.Context
	opts Options
	env  Env
	in   *bufio.Reader
	t    *texts
	// currentRoutes are the saved routes being edited.
	currentRoutes []config.Route
}

type sim struct {
	ICCID  string
	Number string
}

// Run walks through the configuration and saves it when confirmed.
func Run(ctx context.Context, opts Options, env Env) error {
	// Reject unusable options before any prompt.
	if opts.Language != "" && opts.Language != config.LanguageChinese && opts.Language != config.LanguageEnglish {
		return errors.New("language must be zh-CN or en")
	}
	specs, err := parseRoutes(opts.Routes)
	if err != nil {
		return err
	}
	if env.Wait == nil {
		env.Wait = wait
	}
	s := &session{ctx: ctx, opts: opts, env: env, in: bufio.NewReader(env.In)}

	// Choose the language first so every later message uses it.
	existing, loadErr := loadExisting(opts.ConfigPath)
	language, err := s.chooseLanguage(existing)
	if err != nil {
		return err
	}
	s.t = textsFor(language)

	// Start from the saved configuration, or from a new one.
	next, err := s.startingPoint(existing, loadErr)
	if err != nil {
		return err
	}
	next.Language = language
	s.currentRoutes = next.Routes

	// Verify the bot that will deliver notifications.
	bot, username, err := s.chooseBot(next.Bots)
	if err != nil {
		return err
	}

	// Find the SIMs and decide their recipients.
	sims, err := s.findSIMs(specs)
	if err != nil {
		return err
	}
	p, err := newPlan(specs, sims)
	if err != nil {
		return err
	}
	names, err := s.askRecipients(bot, sims, p)
	if err != nil {
		return err
	}
	routes, kept := compose(next.Routes, p, sims)
	s.noteUnconfigured(next.Routes, p, sims)
	next.Routes = routes
	if kept > 0 {
		s.say(s.t.keptRoutes, kept)
	}

	// Confirm what will be written.
	s.summarize(next, username, sims, names)
	if !opts.Yes {
		save, err := s.confirm(s.t.askSave, true)
		if err != nil {
			return err
		}
		if !save {
			s.say("%s", s.t.noChanges)
			return nil
		}
	}

	// Write both files only after they pass the service's own validation.
	if err := ctx.Err(); err != nil {
		return err
	}
	numbers := make(map[string]string, len(sims))
	for _, item := range sims {
		numbers[item.ICCID] = item.Number
	}
	document, credentials, err := render(next, numbers, s.t)
	if err != nil {
		return err
	}
	if err := writeFiles(opts.ConfigPath, next.SecretsFile, document, credentials); err != nil {
		return err
	}
	s.say(s.t.saved, opts.ConfigPath, next.SecretsFile)

	// Apply the new settings to the service.
	return s.updateService()
}

// updateService enables and restarts the service, but only when the saved file is the one it reads.
func (s *session) updateService() error {
	if filepath.Clean(s.opts.ConfigPath) != filepath.Clean(s.opts.ServiceConfig) {
		s.say(s.t.serviceNotChanged, s.opts.ConfigPath)
		return nil
	}

	// Cancellation must not abandon a half-applied change, so use a fresh bounded context.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), serviceTimeout)
	defer cancel()
	if !s.env.ServiceInstalled(ctx) {
		s.say("%s", s.t.serviceNotInstalled)
		return nil
	}
	if err := s.env.StartService(ctx); err != nil {
		s.say("%s: %v", s.t.restartFailed, err)
		return fmt.Errorf("start service: %w", err)
	}
	s.say("%s", s.t.serviceStarted)
	return nil
}

func wait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func loadExisting(path string) (*config.Config, error) {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return config.Load(path)
}

func (s *session) chooseLanguage(existing *config.Config) (string, error) {
	// Options and non-interactive runs decide without asking.
	fallback := languageDefault
	if existing != nil {
		fallback = existing.Language
	}
	if s.opts.Language != "" {
		return s.opts.Language, nil
	}
	if s.opts.Yes {
		return fallback, nil
	}

	// Ask until the answer is understood.
	number := 1
	if fallback == config.LanguageEnglish {
		number = 2
	}
	for {
		fmt.Fprintf(s.env.Out, languagePrompt, number)
		line, err := s.readLine()
		if err != nil {
			return "", err
		}
		switch strings.ToLower(line) {
		case "":
			return fallback, nil
		case "1", "zh", "zh-cn":
			return config.LanguageChinese, nil
		case "2", "en":
			return config.LanguageEnglish, nil
		}
	}
}

func (s *session) startingPoint(existing *config.Config, loadErr error) (*config.Config, error) {
	// Edit a working configuration in place.
	if existing != nil {
		next := *existing
		next.Routes = slices.Clone(existing.Routes)
		next.Bots = maps.Clone(existing.Bots)
		if next.Bots == nil {
			next.Bots = map[string]string{}
		}
		return &next, nil
	}

	// A broken file is replaced only on explicit request.
	if loadErr != nil {
		if s.opts.Yes {
			return nil, fmt.Errorf("existing configuration cannot be used: %w", loadErr)
		}
		s.say(s.t.badConfig, loadErr)
		again, err := s.confirm(s.t.startNew, false)
		if err != nil {
			return nil, err
		}
		if !again {
			return nil, errors.New("setup stopped; the existing configuration was not changed")
		}
	}

	// Defaults come from the service's own parser.
	fresh, err := config.Parse([]byte("version: 1\n"), []byte("bots: {}\n"))
	if err != nil {
		return nil, fmt.Errorf("load default settings: %w", err)
	}
	if s.opts.SecretsFile != "" {
		fresh.SecretsFile = s.opts.SecretsFile
	}

	// Keep the tokens of other bots when the old secrets file is still valid.
	fresh.Bots = savedBots(fresh.SecretsFile)
	return fresh, nil
}

// savedBots returns the bots of a valid secrets file, or none.
func savedBots(path string) map[string]string {
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 1<<20 {
		return map[string]string{}
	}
	parsed, err := config.Parse([]byte("version: 1\n"), data)
	if err != nil || parsed.Bots == nil {
		return map[string]string{}
	}
	return parsed.Bots
}

func (s *session) chooseBot(bots map[string]string) (Bot, string, error) {
	// A token from the command line side wins over the saved one and is never re-prompted.
	token, err := providedToken(s.opts.TokenFile)
	if err != nil {
		return nil, "", err
	}
	if token != "" {
		bot, username, err := s.verify(token)
		if unreachable(err) {
			return nil, "", errors.New(errUnreachable)
		}
		if err != nil {
			return nil, "", fmt.Errorf("bot token from the token file: %w", err)
		}
		bots[defaultBot] = token
		s.say(s.t.botReady, username)
		return bot, username, nil
	}

	// Offer to keep the saved bot.
	if saved, ok := bots[defaultBot]; ok {
		if bot, err := s.env.NewBot(saved); err == nil {
			username, _ := bot.Me(s.ctx)
			if s.opts.Yes {
				return bot, username, nil
			}
			prompt := s.t.keepBotUnknown
			if username != "" {
				prompt = fmt.Sprintf(s.t.keepBot, username)
			}
			keep, err := s.confirm(prompt, true)
			if err != nil {
				return nil, "", err
			}
			if keep {
				return bot, username, nil
			}
		}
	}
	if s.opts.Yes {
		return nil, "", errors.New("no bot token available; use --token-file PATH")
	}

	// Ask for a new token a few times.
	for range tokenAttempts {
		s.say("%s", s.t.askToken)
		token, err := s.secret()
		if err != nil {
			return nil, "", err
		}
		if !config.ValidBotToken(token) {
			s.say("%s", s.t.badToken)
			continue
		}
		for {
			bot, username, err := s.verify(token)
			if err == nil {
				bots[defaultBot] = token
				s.say(s.t.botReady, username)
				return bot, username, nil
			}
			if !unreachable(err) {
				s.say(s.t.tokenRejected, err)
				break
			}

			// A network failure says nothing about the token; let the user decide.
			s.say("%s", s.t.cannotReach)
			again, err := s.confirm(s.t.retryPrompt, true)
			if err != nil {
				return nil, "", err
			}
			if !again {
				return nil, "", errors.New(errUnreachable)
			}
		}
	}
	return nil, "", errors.New(s.t.tooManyTokens)
}

// verify checks the token format and asks Telegram who owns it. Its errors never contain the token.
func (s *session) verify(token string) (Bot, string, error) {
	if !config.ValidBotToken(token) {
		return nil, "", errors.New("not a valid bot token")
	}
	bot, err := s.env.NewBot(token)
	if err != nil {
		return nil, "", err
	}
	username, err := bot.Me(s.ctx)
	if err != nil {
		return nil, "", err
	}
	return bot, username, nil
}

const errUnreachable = "cannot reach Telegram: check the network connection"

// unreachable reports a transport failure, as opposed to an answer from Telegram or a local rejection.
func unreachable(err error) bool {
	var failure *telegram.Error
	return errors.As(err, &failure) && failure.Code == 0 && !failure.Permanent
}

// providedToken reads the token from the first line of path; it returns "" when no file is given.
func providedToken(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open token file: %w", err)
	}
	defer file.Close()
	line, err := bufio.NewReader(io.LimitReader(file, maxTokenFile)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read token file: %w", err)
	}
	return strings.TrimSpace(line), nil
}

func (s *session) findSIMs(specs []routeSpec) ([]sim, error) {
	waited := false
	for attempt := 1; ; attempt++ {
		// List modems; only SIMs with an identifier can be routed.
		devices, err := s.env.Devices(s.ctx)
		if err != nil {
			return nil, fmt.Errorf("list modems: %w", err)
		}
		var sims []sim
		for _, device := range devices {
			if device.SIMID != "" {
				sims = append(sims, sim{ICCID: device.SIMID, Number: device.OwnNumber})
			}
		}

		// A non-interactive run right after boot may see modems before their SIMs are ready.
		if s.opts.Yes {
			if s.simsReady(specs, sims) || attempt >= pollAttempts {
				s.listSIMs(sims)
				return sims, nil
			}
			if !waited {
				s.say("%s", s.t.waitingSIM)
				waited = true
			}
			if err := s.env.Wait(s.ctx, pollInterval); err != nil {
				return nil, err
			}
			continue
		}
		if len(sims) > 0 {
			s.listSIMs(sims)
			return sims, nil
		}

		// Wait for the user to connect a SIM.
		s.say("%s", s.t.noSIM)
		line, err := s.readLine()
		if err != nil {
			return nil, err
		}
		if strings.EqualFold(line, "q") {
			return nil, errors.New(s.t.noSIMQuit)
		}
	}
}

// simsReady reports whether the routes can be resolved; ICCID routes never need a connected SIM.
func (s *session) simsReady(specs []routeSpec, sims []sim) bool {
	if _, err := newPlan(specs, sims); err != nil {
		return false
	}
	if len(sims) > 0 {
		return true
	}
	return len(specs) > 0 && !slices.ContainsFunc(specs, func(spec routeSpec) bool { return !isICCID(spec.target) })
}

func (s *session) listSIMs(sims []sim) {
	if len(sims) == 0 {
		return
	}
	s.say("%s", s.t.simsFound)
	for _, item := range sims {
		s.say("  %s", s.simLabel(item.Number, item.ICCID))
	}
}

func (s *session) simLabel(number, iccid string) string {
	if number == "" {
		number = s.t.numberUnknown
	}
	return fmt.Sprintf(s.t.simLabel, number, iccid[max(0, len(iccid)-4):])
}

// askRecipients prompts for every connected SIM that the options did not settle.
// It returns the chat names seen, for display.
func (s *session) askRecipients(bot Bot, sims []sim, p *plan) (map[int64]string, error) {
	// Only list chats when someone has to choose from them.
	var pending []sim
	for _, item := range sims {
		if _, done := p.set[item.ICCID]; !done {
			pending = append(pending, item)
		}
	}
	names := map[int64]string{}
	if s.opts.Yes || len(pending) == 0 {
		return names, nil
	}
	list := &chatList{}
	s.refreshChats(bot, list)
	for _, item := range list.chats {
		names[item.ID] = item.Name
	}

	// Ask per SIM.
	for _, item := range pending {
		recipients, decided, err := s.askSIM(bot, item, list)
		if err != nil {
			return nil, err
		}
		if decided {
			p.put(item.ICCID, recipients)
		}
	}
	return names, nil
}

type chatList struct {
	chats  []telegram.Chat
	failed bool
}

// refreshChats loads and prints the recent chats; a failure only disables the list.
func (s *session) refreshChats(bot Bot, list *chatList) {
	chats, err := bot.RecentChats(s.ctx)
	list.chats, list.failed = chats, err != nil
	switch {
	case list.failed:
		s.say("%s", s.t.chatsFailed)
	case len(chats) == 0:
		s.say("%s", s.t.chatsEmpty)
	default:
		s.say("%s", s.t.chatsHeader)
		for i, chat := range chats {
			s.say("  %d) %s", i+1, chatLabel(chat))
		}
	}
}

func chatLabel(chat telegram.Chat) string {
	if chat.Name == "" {
		return strconv.FormatInt(chat.ID, 10)
	}
	return fmt.Sprintf("%s (%d)", chat.Name, chat.ID)
}

// askSIM returns the recipients for one SIM; decided is false when the current state stays as it is.
func (s *session) askSIM(bot Bot, item sim, list *chatList) (recipients []config.Recipient, decided bool, err error) {
	cfgRoute := s.currentRoute(item.ICCID)
	for {
		// Show the SIM and its current recipients.
		s.say("")
		s.say("%s", s.simLabel(item.Number, item.ICCID))
		if cfgRoute != nil {
			s.say(s.t.currentRecips, s.recipientNames(cfgRoute.Recipients, list))
			s.say("%s", s.t.hintKeep)
		} else {
			s.say("%s", s.t.hintSkip)
		}
		line, err := s.readLine()
		if err != nil {
			return nil, false, err
		}

		// Interpret the answer.
		switch {
		case line == "" && cfgRoute != nil:
			return nil, false, nil
		case line == "" && len(list.chats) == 0 && !list.failed:
			s.refreshChats(bot, list)
		case line == "":
			return nil, false, nil
		case line == "-":
			return nil, true, nil
		default:
			ids, bad := parseChoice(line, list.chats)
			if bad != "" {
				s.say(s.t.badRecipients, bad)
				continue
			}
			return recipientsFor(ids), true, nil
		}
	}
}

// currentRoute returns the saved route for iccid, if any.
func (s *session) currentRoute(iccid string) *config.Route {
	for _, route := range s.currentRoutes {
		if route.ICCID == iccid {
			return &route
		}
	}
	return nil
}
