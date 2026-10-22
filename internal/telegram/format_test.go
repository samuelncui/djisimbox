package telegram

import (
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"
)

func TestFormatCallMetadata(t *testing.T) {
	// Display only observed metadata, with an explicit fallback for an absent caller number.
	for _, test := range []struct {
		name string
		call Call
		want string
	}{
		{
			name: "known metadata",
			call: Call{Label: "测试 SIM 😀", Number: "+123", ObservedAt: "2026-10-22T12:00:00+08:00"},
			want: "来电通知\nSIM: 测试 SIM 😀\n号码: +123\n时间: 2026-10-22T12:00:00+08:00",
		},
		{
			name: "unknown number",
			call: Call{Label: "测试 SIM", ObservedAt: "2026-10-22T12:00:00+08:00"},
			want: "来电通知\nSIM: 测试 SIM\n号码: 未知号码\n时间: 2026-10-22T12:00:00+08:00",
		},
		{
			name: "whitespace number",
			call: Call{Number: " \t\n\u3000"},
			want: "来电通知\n号码: 未知号码",
		},
		{
			name: "unknown time",
			call: Call{Label: "测试 SIM", Number: "+123"},
			want: "来电通知\nSIM: 测试 SIM\n号码: +123",
		},
		{
			name: "unknown optional metadata",
			call: Call{Label: " \n", Number: "+123", ObservedAt: "\t\u3000"},
			want: "来电通知\n号码: +123",
		},
		{
			name: "single line fields",
			call: Call{Label: " SIM\n二\r", Number: " \t+123\n456\r ", ObservedAt: "time\u2028next\tline"},
			want: "来电通知\nSIM: SIM 二\n号码: +123 456\n时间: time next line",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Compare the complete notification to catch invented fields and extra lines.
			if got := FormatCall(test.call); got != test.want {
				t.Fatalf("FormatCall() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestFormatCallBoundsUnicodeFields(t *testing.T) {
	// Oversized Unicode fields must remain one notification with three metadata lines.
	text := FormatCall(Call{
		Label:      strings.Repeat("😀", 10000),
		Number:     strings.Repeat("☎️", 10000),
		ObservedAt: strings.Repeat("日😀", 10000),
	})
	if !utf8.ValidString(text) {
		t.Fatal("call notification split a UTF-8 rune")
	}
	if size := len(utf16.Encode([]rune(text))); size > 4096 {
		t.Fatalf("call notification has %d UTF-16 units", size)
	}
	lines := strings.Split(text, "\n")
	if len(lines) != 4 || lines[0] != "来电通知" {
		t.Fatalf("call notification line boundaries changed: %q", lines)
	}

	// Independently count each field's UTF-16 units and require visible truncation.
	for index, prefix := range []string{"SIM: ", "号码: ", "时间: "} {
		field, found := strings.CutPrefix(lines[index+1], prefix)
		if !found {
			t.Fatalf("call metadata line %d lacks its label", index+1)
		}
		if size := len(utf16.Encode([]rune(field))); size > 256 || !strings.HasSuffix(field, "…") {
			t.Errorf("call metadata field %d not bounded: %d units", index+1, size)
		}
	}
}

func TestFormatMetadata(t *testing.T) {
	// A normal notification must include readable metadata and unmodified plain text.
	message := Message{
		Label:     "测试 SIM 😀",
		Sender:    "+86123456",
		Timestamp: "2026-10-22T12:34:56+08:00",
		Text:      "【起点】验证码 123456。<b>原文</b>\n第二行",
	}
	parts := Format(message)
	want := "短信通知\nSIM: 测试 SIM 😀\n号码: +86123456\n时间: 2026-10-22T12:34:56+08:00\n\n" + message.Text
	if len(parts) != 1 || parts[0] != want {
		t.Fatalf("formatted notification = %q, want %q", parts, want)
	}

	// Unknown metadata remains absent so the sender or date is never invented.
	parts = Format(Message{Text: "body"})
	if len(parts) != 1 || parts[0] != "短信通知\n\nbody" {
		t.Errorf("absent metadata was filled: %q", parts)
	}
}

func TestFormatPreservesUnicodeBody(t *testing.T) {
	// Exercise ASCII, surrogate pairs, combining marks, and exact chunk boundaries.
	for _, test := range []struct {
		name string
		body string
	}{
		{"ASCII", strings.Repeat("a", 12000)},
		{"Chinese", strings.Repeat("短信测试", 3000)},
		{"emoji", strings.Repeat("😀", 6000)},
		{"mixed", strings.Repeat("中😀e\u0301👨‍👩‍👧‍👦\n", 1000)},
		{"exact first", strings.Repeat("a", 4090)},
		{"cross first", strings.Repeat("a", 4089) + "😀tail"},
		{"empty", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Rejoin parts to verify complete body preservation, independent of split strategy.
			parts := Format(Message{Text: test.body})
			if got := strings.Join(parts, ""); got != "短信通知\n\n"+test.body {
				t.Fatalf("body was changed or duplicated after chunking")
			}

			// Encode to UTF-16 independently of the production counter.
			for index, part := range parts {
				if !utf8.ValidString(part) {
					t.Errorf("part %d split a UTF-8 rune", index)
				}
				if size := len(utf16.Encode([]rune(part))); size > 4096 || size == 0 {
					t.Errorf("part %d has %d UTF-16 units", index, size)
				}
			}
		})
	}
}

func TestFormatBoundsHeader(t *testing.T) {
	// Oversized metadata and line breaks cannot displace or imitate the SMS body.
	message := Message{
		Label:     strings.Repeat("😀", 10000),
		Sender:    "  sender\nnew line\tvalue\r  ",
		Timestamp: strings.Repeat("日", 10000),
		Text:      "ORIGINAL BODY\nunchanged",
	}
	parts := Format(message)
	joined := strings.Join(parts, "")
	separator := strings.Index(joined, "\n\n")
	if separator < 0 || joined[separator+2:] != message.Text {
		t.Fatalf("metadata obscured or changed the SMS body")
	}

	// Each field stays on one bounded line and visibly records truncation.
	lines := strings.Split(joined[:separator], "\n")
	if len(lines) != 4 || lines[2] != "号码: sender new line value" {
		t.Fatalf("metadata line boundaries changed: %q", lines)
	}
	for _, line := range []string{strings.TrimPrefix(lines[1], "SIM: "), strings.TrimPrefix(lines[3], "时间: ")} {
		if size := len(utf16.Encode([]rune(line))); size > 256 || !strings.HasSuffix(line, "…") {
			t.Errorf("header field not bounded: %d units", size)
		}
	}
}
