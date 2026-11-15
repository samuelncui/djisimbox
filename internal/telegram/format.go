package telegram

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxTextUnits is the conservative Telegram text limit in UTF-16 code units.
const MaxTextUnits = 4096

const maxHeaderFieldUnits = 256

// wording holds the fixed notification text for one language.
type wording struct {
	sms, call, mms, from, time, unknownNumber, subject, size, expiry, notDownloaded, iccidEnding string
}

// wordings is keyed by the configuration language; Chinese is the default.
var wordings = map[string]wording{
	"zh-CN": {sms: "短信通知", call: "来电通知", mms: "彩信通知", from: "号码: ", time: "时间: ",
		unknownNumber: "未知号码", subject: "主题: ", size: "大小: ", expiry: "有效期至: ",
		notDownloaded: "内容未下载：彩信内容需要通过这张卡的蜂窝数据获取。", iccidEnding: "ICCID 尾号 "},
	"en": {sms: "SMS", call: "Incoming call", mms: "MMS", from: "From: ", time: "Time: ",
		unknownNumber: "Unknown number", subject: "Subject: ", size: "Size: ", expiry: "Expires: ",
		notDownloaded: "Content not downloaded: MMS content can only be fetched over this SIM's cellular data.",
		iccidEnding:   "ICCID ending "},
}

func wordingFor(language string) wording {
	if w, ok := wordings[language]; ok {
		return w
	}
	return wordings["zh-CN"]
}

// ICCIDEndingLabel names a SIM by its ICCID's last digits when no name or number is known.
func ICCIDEndingLabel(language, suffix string) string {
	return wordingFor(language).iccidEnding + suffix
}

// Message contains display metadata and an unmodified SMS body.
type Message struct {
	Language  string
	Label     string
	Sender    string
	Timestamp string
	Text      string
}

// Call contains observed incoming-call metadata for a text notification.
type Call struct {
	Language   string
	Label      string
	Number     string
	ObservedAt string
}

// MMS contains an MMS notification's metadata; the carrier keeps the content.
type MMS struct {
	Language  string
	Label     string
	Sender    string
	Timestamp string
	Subject   string
	Size      int64
	Expiry    string
}

// FormatMMS renders one bounded notification, preserving unknown optional metadata as absent.
func FormatMMS(mms MMS) string {
	// Share the SMS metadata layout, then list what the notification says about the stored content.
	w := wordingFor(mms.Language)
	var text strings.Builder
	text.WriteString(formatHeader(w, w.mms, mms.Label, mms.Sender, mms.Timestamp))
	text.WriteByte('\n')
	for _, field := range []struct{ name, value string }{
		{w.subject, headerValue(mms.Subject)},
		{w.size, formatSize(mms.Size)},
		{w.expiry, headerValue(mms.Expiry)},
	} {
		if field.value != "" {
			text.WriteString("\n" + field.name + field.value)
		}
	}

	// The content needs the SIM's cellular data, which the service never uses.
	text.WriteString("\n" + w.notDownloaded)
	return text.String()
}

func formatSize(size int64) string {
	switch {
	case size <= 0:
		return ""
	case size < 1024:
		return fmt.Sprintf("%d B", size)
	case size < 1024*1024:
		return fmt.Sprintf("%d KB", (size+512)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(size)/(1024*1024))
	}
}

// FormatCall renders one bounded notification, preserving unknown optional metadata as absent.
func FormatCall(call Call) string {
	// A missing caller number must remain explicit after metadata normalization.
	w := wordingFor(call.Language)
	number := headerValue(call.Number)
	if number == "" {
		number = w.unknownNumber
	}

	// Share the SMS metadata layout, bounded to fit one Telegram message.
	return formatHeader(w, w.call, call.Label, number, call.ObservedAt)
}

// Format prepends one readable header and splits text at UTF-8 rune boundaries.
// Joining all returned parts reproduces the header and complete original SMS body.
// Metadata is bounded and kept on single lines; absent fields stay absent.
func Format(message Message) []string {
	// Share the call metadata layout and separate it from the unmodified SMS body.
	w := wordingFor(message.Language)
	text := formatHeader(w, w.sms, message.Label, message.Sender, message.Timestamp) + "\n\n" + message.Text

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

func formatHeader(w wording, title, label, number, timestamp string) string {
	// Keep metadata in the same order, with one bounded line per observed field.
	var header strings.Builder
	header.WriteString(title)
	for _, field := range []struct{ name, value string }{
		{"SIM: ", label},
		{w.from, number},
		{w.time, timestamp},
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
