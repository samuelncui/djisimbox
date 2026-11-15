package modem

import (
	"bytes"
	"testing"

	"github.com/godbus/dbus/v5"
)

const mmsReceived = "2026-11-07T16:44:58+08"

// mmsPush builds a WAP Push SMS payload around MMS header fields, mirroring carrier notifications.
func mmsPush(contentType []byte, fields ...[]byte) []byte {
	headers := append(append([]byte{}, contentType...), 0xaf, 0x84)
	push := []byte{0x01, wspPush, byte(len(headers))}
	push = append(push, headers...)
	for _, field := range fields {
		push = append(push, field...)
	}
	return push
}

var (
	textContentType  = append([]byte(mmsContentType), 0)
	notificationType = []byte{mmsMessageType, mmsNotificationInd}
	transactionID    = []byte{0x98, 'T', '1', 0}
	mmsVersion       = []byte{0x8d, 0x90}
	fromAddress      = append([]byte{mmsFrom, 23, mmsAddressPresent}, append([]byte("10690000000/TYPE=PLMN"), 0)...)
	utf8Subject      = append([]byte{mmsSubject, 8, 0xea}, append([]byte("测试"), 0)...)
	messageClass     = []byte{0x8a, 0x80}
	messageSize      = []byte{mmsSize, 3, 0x0a, 0x50, 0xc6}
	relativeExpiry   = []byte{mmsExpiry, 5, mmsRelativeTime, 3, 0x03, 0xf4, 0x80}
	contentLocation  = append([]byte{0x83}, append([]byte("http://mmsc.example/abc"), 0)...)
)

func TestParseMMSNotification(t *testing.T) {
	// A carrier-style notification yields the MMS sender, subject, size and an absolute expiry.
	data := mmsPush(textContentType, notificationType, transactionID, mmsVersion, contentLocation,
		relativeExpiry, fromAddress, messageClass, messageSize, utf8Subject)
	sender, notice, ok := parseMMSNotification(data, mmsReceived)
	if !ok {
		t.Fatal("notification rejected")
	}
	want := MMSNotice{Subject: "测试", Size: 676038, Expiry: "2026-11-10T16:44:58+08:00"}
	if sender != "10690000000" || notice != want {
		t.Fatalf("sender=%q notice=%+v", sender, notice)
	}
}

func TestParseMMSNotificationVariants(t *testing.T) {
	// Each case changes one field encoding; unknown metadata stays absent instead of being guessed.
	absoluteExpiry := []byte{mmsExpiry, 6, mmsAbsoluteTime, 4, 0x69, 0x11, 0xa3, 0x3a}
	insertAddress := []byte{mmsFrom, 1, 0x81}
	gbkSubject := []byte{mmsSubject, 6, 2, 0x07, 0xe9, 'x', 'y', 0}
	plainSubject := append([]byte{mmsSubject}, append([]byte("plain"), 0)...)
	extension := append(append([]byte("X-Custom"), 0), append([]byte("value"), 0)...)
	for _, tc := range []struct {
		name        string
		contentType []byte
		fields      [][]byte
		received    string
		sender      string
		notice      MMSNotice
	}{
		{"well-known content type", []byte{0x80 | wspMMSContentType}, [][]byte{notificationType, messageSize},
			mmsReceived, "", MMSNotice{Size: 676038}},
		{"general content type form", append([]byte{0x1f, byte(len(textContentType))}, textContentType...),
			[][]byte{notificationType}, mmsReceived, "", MMSNotice{}},
		{"absolute expiry in SMS zone", textContentType, [][]byte{notificationType, absoluteExpiry},
			mmsReceived, "", MMSNotice{Expiry: "2025-11-10T16:32:58+08:00"}},
		{"relative expiry without SMS time", textContentType, [][]byte{notificationType, relativeExpiry},
			"", "", MMSNotice{}},
		{"inserted sender", textContentType, [][]byte{notificationType, insertAddress}, mmsReceived, "", MMSNotice{}},
		{"unsupported charset", textContentType, [][]byte{notificationType, gbkSubject}, mmsReceived, "",
			MMSNotice{}},
		{"plain subject and extension header", textContentType, [][]byte{notificationType, plainSubject, extension},
			mmsReceived, "", MMSNotice{Subject: "plain"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sender, notice, ok := parseMMSNotification(mmsPush(tc.contentType, tc.fields...), tc.received)
			if !ok || sender != tc.sender || notice != tc.notice {
				t.Fatalf("ok=%t sender=%q notice=%+v", ok, sender, notice)
			}
		})
	}
}

func TestParseMMSNotificationRejectsOtherPayloads(t *testing.T) {
	// Binary SMS that are not MMS notifications stay unsupported.
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"not a push", []byte{0x01, 0x07, 0x01, 0xbe, 0x8c, 0x82}},
		{"service indication", mmsPush([]byte{0xae}, notificationType)},
		{"other text content type", mmsPush(append([]byte("text/plain"), 0), notificationType)},
		{"retrieved message", mmsPush(textContentType, []byte{mmsMessageType, 0x84})},
		{"missing message type", mmsPush(textContentType, messageSize, fromAddress)},
		{"oversized integer", mmsPush(textContentType, notificationType, []byte{mmsSize, 9, 1, 2, 3, 4, 5, 6, 7, 8, 9})},
		{"unterminated text", mmsPush(textContentType, notificationType, []byte{0x98, 'T'})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, ok := parseMMSNotification(tc.data, mmsReceived); ok {
				t.Fatal("unsupported payload accepted")
			}
		})
	}
}

func TestParseMMSNotificationTruncationIsSafe(t *testing.T) {
	// Every prefix of a valid payload decodes without panicking; cuts inside a field are rejected.
	data := mmsPush(textContentType, notificationType, transactionID, fromAddress, utf8Subject, messageSize,
		relativeExpiry)
	for n := range len(data) {
		_, _, _ = parseMMSNotification(data[:n], mmsReceived)
	}
	cut := bytes.Index(data, utf8Subject) + 4
	if _, _, ok := parseMMSNotification(data[:cut], mmsReceived); ok {
		t.Fatal("payload truncated inside the subject was accepted")
	}
}

func TestIncomingMMSNotification(t *testing.T) {
	// A complete binary MMS notification becomes an MMS notice from the MMS sender, not the push gateway.
	device := Device{Path: root + "/Modem/0", owner: ":1.2", SIMID: "8901000000000000001", OwnNumber: "+8613261231131"}
	data := mmsPush(textContentType, notificationType, fromAddress, messageSize, relativeExpiry)
	values := map[string]dbus.Variant{
		"State": dbus.MakeVariant(smsReceived), "PduType": dbus.MakeVariant(pduDeliver),
		"Text": dbus.MakeVariant(""), "Data": dbus.MakeVariant(data),
		"Number": dbus.MakeVariant("10655"), "Timestamp": dbus.MakeVariant(mmsReceived),
	}
	message, ready := incoming("boot", device, root+"/SMS/0", values)
	if !ready || message.Kind != MMS || message.Sender != "10690000000" || message.Text != "" {
		t.Fatalf("ready=%t message=%+v", ready, message)
	}
	if message.MMS.Size != 676038 || message.MMS.Expiry != "2026-11-10T16:44:58+08:00" ||
		message.OwnNumber != device.OwnNumber || message.SourceKey == "" {
		t.Fatalf("notice metadata or identity missing: %+v", message)
	}
}
