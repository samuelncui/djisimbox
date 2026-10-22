package telegram

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxTextUnits is the conservative Telegram text limit in UTF-16 code units.
const MaxTextUnits = 4096

const maxHeaderFieldUnits = 256

// Message contains display metadata and an unmodified SMS body.
type Message struct {
	Label     string
	Sender    string
	Timestamp string
	Text      string
}

// Call contains observed incoming-call metadata for a text notification.
type Call struct {
	Label      string
	Number     string
	ObservedAt string
}

// FormatCall renders one bounded notification, preserving unknown optional metadata as absent.
func FormatCall(call Call) string {
	// A missing caller number must remain explicit after metadata normalization.
	number := headerValue(call.Number)
	if number == "" {
		number = "未知号码"
	}

	// Share the SMS metadata layout, bounded to fit one Telegram message.
	return formatHeader("来电通知", call.Label, number, call.ObservedAt)
}

// Format prepends one readable header and splits text at UTF-8 rune boundaries.
// Joining all returned parts reproduces the header and complete original SMS body.
// Metadata is bounded and kept on single lines; absent fields stay absent.
func Format(message Message) []string {
	// Share the call metadata layout and separate it from the unmodified SMS body.
	text := formatHeader("短信通知", message.Label, message.Sender, message.Timestamp) + "\n\n" + message.Text

	// Slice the original bytes at rune boundaries; no body bytes are inserted or removed.
	var parts []string
	start, units := 0, 0
	for offset, value := range text {
		size := 1
		if value > 0xffff {
			size = 2
		}
		if units+size > MaxTextUnits {
			parts = append(parts, text[start:offset])
			start, units = offset, 0
		}
		units += size
	}
	return append(parts, text[start:])
}

func formatHeader(title, label, number, timestamp string) string {
	// Keep metadata in the same order, with one bounded line per observed field.
	var header strings.Builder
	header.WriteString(title)
	for _, field := range []struct{ name, value string }{
		{"SIM: ", label},
		{"号码: ", number},
		{"时间: ", timestamp},
	} {
		if value := headerValue(field.value); value != "" {
			header.WriteByte('\n')
			header.WriteString(field.name)
			header.WriteString(value)
		}
	}
	return header.String()
}

func headerValue(value string) string {
	// Keep metadata from injecting lines while accepting arbitrary Unicode names.
	value = strings.Map(func(value rune) rune {
		if unicode.IsControl(value) || unicode.IsSpace(value) {
			return ' '
		}
		return value
	}, strings.ToValidUTF8(value, "�"))
	value = strings.TrimSpace(value)

	// Leave one unit for a visible truncation marker without splitting an emoji.
	units := 0
	for offset, character := range value {
		size := 1
		if character > 0xffff {
			size = 2
		}
		if units+size > maxHeaderFieldUnits-1 {
			return value[:offset] + "…"
		}
		units += size
	}
	return value
}

func textUnits(value string) int {
	units := utf8.RuneCountInString(value)
	for _, character := range value {
		if character > 0xffff {
			units++
		}
	}
	return units
}
