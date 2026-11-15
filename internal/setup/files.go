package setup

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/samuelncui/djisimbox/internal/config"
)

const serviceAccount = "djisimbox"

// render produces both documents and checks them with the service's own validation.
func render(cfg *config.Config, numbers map[string]string, t *texts) (document, credentials []byte, err error) {
	// Settings that differ from the defaults must survive a rerun.
	defaults, err := config.Parse([]byte("version: 1\n"), []byte("bots: {}\n"))
	if err != nil {
		return nil, nil, fmt.Errorf("load default settings: %w", err)
	}

	// Write the routing document with the phone number above each SIM for readability.
	var b strings.Builder
	b.WriteString(t.configHeader)
	fmt.Fprintf(&b, "version: 1\nlanguage: %s\n\n", scalar(cfg.Language))
	if len(cfg.Routes) == 0 {
		b.WriteString("routes: []\n")
	} else {
		b.WriteString("routes:\n")
	}
	for _, route := range cfg.Routes {
		if number := cleanNumber(numbers[route.ICCID]); number != "" {
			fmt.Fprintf(&b, "  # %s\n", number)
		}
		fmt.Fprintf(&b, "  - iccid: %s\n", scalar(route.ICCID))
		if route.Name != "" {
			fmt.Fprintf(&b, "    name: %s\n", scalar(route.Name))
		}
		b.WriteString("    recipients:\n")
		for _, recipient := range route.Recipients {
			fmt.Fprintf(&b, "      - bot: %s\n        chat_id: %s\n", scalar(recipient.Bot), scalar(recipient.ChatID))
		}
	}
	if cfg.SecretsFile != defaults.SecretsFile {
		fmt.Fprintf(&b, "\nsecrets_file: %s\n", scalar(cfg.SecretsFile))
	}
	if cfg.MaxPending != defaults.MaxPending {
		fmt.Fprintf(&b, "\nmax_pending: %d\n", cfg.MaxPending)
	}
	if cfg.MaxBytes != defaults.MaxBytes {
		fmt.Fprintf(&b, "\nmax_bytes: %d\n", cfg.MaxBytes)
	}
	if cfg.RuntimeDir != defaults.RuntimeDir {
		fmt.Fprintf(&b, "\nruntime_dir: %s\n", scalar(cfg.RuntimeDir))
	}
	document = []byte(b.String())

	// Marshal tokens as data so no token character can alter the document structure.
	body, err := yaml.Marshal(struct {
		Bots map[string]string `yaml:"bots"`
	}{cfg.Bots})
	if err != nil {
		return nil, nil, fmt.Errorf("encode credentials: %w", err)
	}
	credentials = append([]byte("# Telegram bot tokens by name. Keep this file private: mode 0600.\n"), body...)

	// Refuse to write anything the service would reject at startup.
	if _, err := config.Parse(document, credentials); err != nil {
		return nil, nil, fmt.Errorf("validate generated files: %w", err)
	}
	return document, credentials, nil
}

// scalar encodes one string as a YAML scalar, quoting it when needed.
func scalar(value string) string {
	encoded, err := yaml.Marshal(value)
	if err != nil {
		return strconv.Quote(value)
	}
	return strings.TrimSuffix(string(encoded), "\n")
}

// cleanNumber keeps a phone number safe to place inside a one-line comment.
func cleanNumber(number string) string {
	return strings.Map(func(char rune) rune {
		if (char >= '0' && char <= '9') || strings.ContainsRune("+ -()", char) {
			return char
		}
		return -1
	}, number)
}

// writeFiles stores credentials first so the routing file never names a missing bot.
func writeFiles(configPath, secretsPath string, document, credentials []byte) error {
	// Create directories the service account can traverse.
	for _, dir := range []string{filepath.Dir(secretsPath), filepath.Dir(configPath)} {
		if err := ensureDir(dir); err != nil {
			return err
		}
	}

	// Replace each file atomically so a crash never leaves a partial file.
	if err := writeAtomic(secretsPath, credentials); err != nil {
		return fmt.Errorf("write secrets: %w", err)
	}
	if err := writeAtomic(configPath, document); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

func ensureDir(dir string) error {
	if _, err := os.Stat(dir); err == nil {
		return nil
	}
	if err := os.MkdirAll(dir, 0750); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}
	if err := os.Chmod(dir, 0750); err != nil {
		return fmt.Errorf("set directory mode: %w", err)
	}
	if _, gid, ok := serviceIDs(); ok {
		if err := os.Chown(dir, -1, gid); err != nil {
			return fmt.Errorf("set directory group: %w", err)
		}
	}
	return nil
}

func writeAtomic(path string, data []byte) error {
	// Stage next to the target so rename stays on one filesystem.
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	staged := file.Name()
	defer os.Remove(staged)
	defer file.Close()

	// Make the staged file private and owned by the service before it becomes visible.
	if err := file.Chmod(0600); err != nil {
		return fmt.Errorf("set file mode: %w", err)
	}
	if uid, gid, ok := serviceIDs(); ok {
		if err := file.Chown(uid, gid); err != nil {
			return fmt.Errorf("set file owner: %w", err)
		}
	}
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}

	// Publish the complete file.
	if err := os.Rename(staged, path); err != nil {
		return fmt.Errorf("replace file: %w", err)
	}
	return nil
}

// serviceIDs finds the service account, when it exists, so the service can read its files.
func serviceIDs() (uid, gid int, ok bool) {
	account, err := user.Lookup(serviceAccount)
	if err != nil {
		return 0, 0, false
	}
	uid, uidErr := strconv.Atoi(account.Uid)
	gid, gidErr := strconv.Atoi(account.Gid)
	return uid, gid, uidErr == nil && gidErr == nil
}
