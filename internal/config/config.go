// Package config loads and validates the service's routing and bot credentials.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxFileSize = 1 << 20

var (
	botNamePattern  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	botTokenPattern = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]{20,128}$`)
)

// Config is the validated, immutable configuration returned by Load.
type Config struct {
	Version     int               `json:"version"`
	RuntimeDir  string            `json:"runtime_dir"`
	SecretsFile string            `json:"secrets_file"`
	MaxPending  int               `json:"max_pending"`
	MaxBytes    int64             `json:"max_bytes"`
	Routes      []Route           `json:"routes"`
	Bots        map[string]string `json:"-"`

	routeIndexes map[string]int
}

// Route assigns a SIM to a display name and one or more Telegram recipients.
type Route struct {
	ICCID      string      `json:"iccid"`
	Name       string      `json:"name"`
	Recipients []Recipient `json:"recipients"`
}

// Recipient selects a named bot credential and a Telegram chat.
type Recipient struct {
	Bot    string `json:"bot"`
	ChatID string `json:"chat_id"`
}

type secrets struct {
	Bots map[string]string `json:"bots"`
}

// Load reads strict JSON configuration and a separate private secrets file.
// Errors deliberately exclude input values because callers may log them.
func Load(path string) (*Config, error) {
	// Seed defaults before decoding so explicit zero limits remain invalid.
	cfg := &Config{
		RuntimeDir:  "/run/djisimbox",
		SecretsFile: "/etc/djisimbox/secrets.json",
		MaxPending:  10000,
		MaxBytes:    16777216,
	}
	if err := readJSON(path, "config", false, cfg); err != nil {
		return nil, fmt.Errorf("load configuration: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("validate configuration: %w", err)
	}

	// Keep credentials in a private file and validate unused bot entries too.
	var credentials secrets
	if err := readJSON(cfg.SecretsFile, "secrets", true, &credentials); err != nil {
		return nil, fmt.Errorf("load credentials: %w", err)
	}
	for name, token := range credentials.Bots {
		if !botNamePattern.MatchString(name) {
			return nil, errors.New("credentials contain an invalid bot name")
		}
		if !botTokenPattern.MatchString(token) {
			return nil, errors.New("credentials contain an invalid bot token")
		}
	}
	cfg.Bots = credentials.Bots

	// Reject missing credentials before processing any SMS for a configured SIM.
	for i, route := range cfg.Routes {
		for j, recipient := range route.Recipients {
			if _, ok := cfg.Bots[recipient.Bot]; !ok {
				return nil, fmt.Errorf("route %d recipient %d references a missing bot", i+1, j+1)
			}
		}
	}
	return cfg, nil
}

// Route looks up a configured SIM in constant time. An unknown SIM has no route.
func (c *Config) Route(iccid string) (Route, bool) {
	index, ok := c.routeIndexes[iccid]
	if !ok {
		return Route{}, false
	}
	return c.Routes[index], true
}

func (c *Config) validate() error {
	// Validate process-wide limits before building any routing state.
	if c.Version != 1 {
		return errors.New("version must be 1")
	}
	if !filepath.IsAbs(c.RuntimeDir) || filepath.Clean(c.RuntimeDir) != c.RuntimeDir {
		return errors.New("runtime_dir must be an absolute clean path")
	}
	if strings.IndexByte(c.RuntimeDir, 0) >= 0 {
		return errors.New("runtime_dir must not contain NUL characters")
	}
	if !filepath.IsAbs(c.SecretsFile) {
		return errors.New("secrets_file must be an absolute path")
	}
	if c.MaxPending < 1 || c.MaxPending > 1000000 {
		return errors.New("max_pending must be between 1 and 1000000")
	}
	if c.MaxBytes < 1024 || c.MaxBytes > 1073741824 {
		return errors.New("max_bytes must be between 1024 and 1073741824")
	}

	// Each SIM owns one explicit recipient set; absent routes stay quarantined.
	c.routeIndexes = make(map[string]int, len(c.Routes))
	for i, route := range c.Routes {
		if len(route.ICCID) < 18 || len(route.ICCID) > 22 || !digits(route.ICCID) {
			return fmt.Errorf("route %d has an invalid ICCID", i+1)
		}
		if _, ok := c.routeIndexes[route.ICCID]; ok {
			return fmt.Errorf("route %d has a duplicate ICCID", i+1)
		}
		if strings.TrimSpace(route.Name) == "" || utf8.RuneCountInString(route.Name) > 80 {
			return fmt.Errorf("route %d name must contain 1 to 80 characters", i+1)
		}
		if strings.ContainsFunc(route.Name, lineBreakOrControl) {
			return fmt.Errorf("route %d name must be a single line without control characters", i+1)
		}
		if len(route.Recipients) == 0 {
			return fmt.Errorf("route %d must have at least one recipient", i+1)
		}
		if err := validateRecipients(route.Recipients); err != nil {
			return fmt.Errorf("route %d: %w", i+1, err)
		}
		c.routeIndexes[route.ICCID] = i
	}
	return nil
}

func validateRecipients(recipients []Recipient) error {
	// Canonical chat IDs prevent duplicate delivery through alternate spellings.
	found := make(map[string]struct{}, len(recipients))
	for i, recipient := range recipients {
		if !botNamePattern.MatchString(recipient.Bot) {
			return fmt.Errorf("recipient %d has an invalid bot name", i+1)
		}
		chatID, err := strconv.ParseInt(recipient.ChatID, 10, 64)
		if err != nil || chatID == 0 {
			return fmt.Errorf("recipient %d has an invalid chat_id", i+1)
		}
		key := recipient.Bot + ":" + strconv.FormatInt(chatID, 10)
		if _, ok := found[key]; ok {
			return fmt.Errorf("recipient %d duplicates a bot and chat_id", i+1)
		}
		found[key] = struct{}{}
		recipients[i].ChatID = strconv.FormatInt(chatID, 10)
	}
	return nil
}

func readJSON(path, kind string, private bool, target any) error {
	// Reject special files before opening, then verify the opened file itself.
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s file is unavailable", kind)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s file must be regular", kind)
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("%s file could not be opened", kind)
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		return fmt.Errorf("%s file could not be inspected", kind)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s file must be regular", kind)
	}
	if private && info.Mode().Perm()&0077 != 0 {
		return errors.New("secrets file must have no group or other permissions")
	}
	if info.Size() > maxFileSize {
		return fmt.Errorf("%s file exceeds 1 MiB", kind)
	}

	// Bound reads even when a file grows after inspection and require an object.
	data, err := io.ReadAll(io.LimitReader(file, maxFileSize+1))
	if err != nil {
		return fmt.Errorf("%s file could not be read", kind)
	}
	if len(data) > maxFileSize {
		return fmt.Errorf("%s file exceeds 1 MiB", kind)
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' || !utf8.Valid(data) {
		return fmt.Errorf("%s file must contain a JSON object", kind)
	}

	// Decode errors can contain credential values or keys, so keep errors static.
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%s file contains invalid or unknown JSON fields", kind)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("%s file contains trailing JSON data", kind)
	}
	return nil
}

func digits(value string) bool {
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return value != ""
}

func lineBreakOrControl(char rune) bool {
	return unicode.IsControl(char) || char == '\u2028' || char == '\u2029'
}
