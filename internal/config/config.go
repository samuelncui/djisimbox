// Package config loads and validates the service's routing and bot credentials.
package config

import (
	"bytes"
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

	"go.yaml.in/yaml/v3"
)

const maxFileSize = 1 << 20

var (
	botNamePattern  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	botTokenPattern = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]{20,128}$`)
	yamlLinePattern = regexp.MustCompile(`line (\d+)`)
)

// Supported languages for notifications and setup prompts.
const (
	LanguageChinese = "zh-CN"
	LanguageEnglish = "en"
)

// Config is the validated, immutable configuration returned by Load.
type Config struct {
	Version     int               `yaml:"version"`
	Language    string            `yaml:"language,omitempty"`
	RuntimeDir  string            `yaml:"runtime_dir"`
	SecretsFile string            `yaml:"secrets_file"`
	MaxPending  int               `yaml:"max_pending"`
	MaxBytes    int64             `yaml:"max_bytes"`
	Routes      []Route           `yaml:"routes"`
	Bots        map[string]string `yaml:"-"`

	routeIndexes map[string]int
}

// Route assigns a SIM to one or more Telegram recipients.
// An empty Name lets notifications show the SIM's own number instead.
type Route struct {
	ICCID      string      `yaml:"iccid"`
	Name       string      `yaml:"name,omitempty"`
	Recipients []Recipient `yaml:"recipients"`
}

// Recipient selects a named bot credential and a Telegram chat.
type Recipient struct {
	Bot    string `yaml:"bot"`
	ChatID string `yaml:"chat_id"`
}

type secrets struct {
	Bots map[string]string `yaml:"bots"`
}

// Load reads strict YAML configuration and a separate private secrets file.
// Errors deliberately exclude input values because callers may log them.
func Load(path string) (*Config, error) {
	// Read the routing document first; it names the private secrets file.
	document, err := readFile(path, "config", false)
	if err != nil {
		return nil, fmt.Errorf("load configuration: %w", err)
	}
	cfg, err := parseDocument(document)
	if err != nil {
		return nil, err
	}

	// Keep credentials in a private file and validate unused bot entries too.
	credentials, err := readFile(cfg.SecretsFile, "secrets", true)
	if err != nil {
		return nil, fmt.Errorf("load credentials: %w", err)
	}
	if err := cfg.attachSecrets(credentials); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Parse validates configuration and secrets documents held in memory, such as files about to be written.
func Parse(document, credentials []byte) (*Config, error) {
	cfg, err := parseDocument(document)
	if err != nil {
		return nil, err
	}
	if err := cfg.attachSecrets(credentials); err != nil {
		return nil, err
	}
	return cfg, nil
}

func parseDocument(document []byte) (*Config, error) {
	// Seed defaults before decoding so explicit zero limits remain invalid.
	cfg := &Config{
		Language:    LanguageChinese,
		RuntimeDir:  "/run/djisimbox",
		SecretsFile: "/etc/djisimbox/secrets.yaml",
		MaxPending:  10000,
		MaxBytes:    16777216,
	}
	if err := decodeYAML(document, "config", cfg); err != nil {
		return nil, fmt.Errorf("load configuration: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("validate configuration: %w", err)
	}
	return cfg, nil
}

func (c *Config) attachSecrets(credentials []byte) error {
	// Validate every named bot, including ones no route uses yet.
	var parsed secrets
	if err := decodeYAML(credentials, "secrets", &parsed); err != nil {
		return fmt.Errorf("load credentials: %w", err)
	}
	for name, token := range parsed.Bots {
		if !botNamePattern.MatchString(name) {
			return errors.New("credentials contain an invalid bot name")
		}
		if !botTokenPattern.MatchString(token) {
			return errors.New("credentials contain an invalid bot token")
		}
	}
	c.Bots = parsed.Bots

	// Reject missing credentials before processing any SMS for a configured SIM.
	for i, route := range c.Routes {
		for j, recipient := range route.Recipients {
			if _, ok := c.Bots[recipient.Bot]; !ok {
				return fmt.Errorf("route %d recipient %d references a missing bot", i+1, j+1)
			}
		}
	}
	return nil
}

// ValidBotToken reports whether token has the format accepted in the secrets file.
func ValidBotToken(token string) bool {
	return botTokenPattern.MatchString(token)
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
	if c.Language != LanguageChinese && c.Language != LanguageEnglish {
		return errors.New("language must be zh-CN or en")
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
		if route.Name != "" && (strings.TrimSpace(route.Name) == "" || utf8.RuneCountInString(route.Name) > 80) {
			return fmt.Errorf("route %d name must be omitted or contain 1 to 80 characters", i+1)
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

func readFile(path, kind string, private bool) ([]byte, error) {
	// Reject special files before opening, then verify the opened file itself.
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("%s file is unavailable", kind)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s file must be regular", kind)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%s file could not be opened", kind)
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		return nil, fmt.Errorf("%s file could not be inspected", kind)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s file must be regular", kind)
	}
	if private && info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("secrets file must have no group or other permissions")
	}
	if info.Size() > maxFileSize {
		return nil, fmt.Errorf("%s file exceeds 1 MiB", kind)
	}

	// Bound reads even when a file grows after inspection.
	data, err := io.ReadAll(io.LimitReader(file, maxFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("%s file could not be read", kind)
	}
	if len(data) > maxFileSize {
		return nil, fmt.Errorf("%s file exceeds 1 MiB", kind)
	}
	return data, nil
}

func decodeYAML(data []byte, kind string, target any) error {
	// Bounded documents must be UTF-8 text.
	if len(data) > maxFileSize {
		return fmt.Errorf("%s file exceeds 1 MiB", kind)
	}
	if !utf8.Valid(data) {
		return fmt.Errorf("%s file must be UTF-8 text", kind)
	}

	// Require exactly one top-level mapping; null, scalar and list documents are not empty settings.
	documents := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := documents.Decode(&document); err != nil {
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%s file must contain a YAML mapping", kind)
		}
		return yamlError(kind, err)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("%s file must contain a YAML mapping", kind)
	}
	err := documents.Decode(new(yaml.Node))
	if err == nil {
		return fmt.Errorf("%s file must contain one YAML document", kind)
	}
	if !errors.Is(err, io.EOF) {
		return yamlError(kind, err)
	}

	// Reject misspelled or removed fields rather than silently ignoring them.
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(target); err != nil {
		return yamlError(kind, err)
	}
	return nil
}

// yamlError keeps the reported line number but drops decoder text, which can quote keys or credential values.
func yamlError(kind string, err error) error {
	match := yamlLinePattern.FindStringSubmatch(err.Error())
	if match == nil {
		return fmt.Errorf("%s file contains invalid or unknown YAML fields", kind)
	}
	return fmt.Errorf("%s file contains invalid or unknown YAML fields at line %s", kind, match[1])
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
