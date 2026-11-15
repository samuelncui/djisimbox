package modem

import (
	"strings"
	"time"
	"unicode/utf8"
)

// MMSNotice is the metadata an MMS notification carries. The content itself stays on the carrier's
// server and can only be retrieved over the SIM's cellular data, which this service never uses.
type MMSNotice struct {
	Subject string
	Size    int64
	// Expiry is RFC 3339; empty when absent or not computable.
	Expiry string
}

const (
	wspPush            = 0x06
	wspMMSContentType  = 0x3e
	mmsContentType     = "application/vnd.wap.mms-message"
	mmsMessageType     = 0x8c
	mmsNotificationInd = 0x82
	mmsFrom            = 0x89
	mmsSubject         = 0x96
	mmsSize            = 0x8e
	mmsExpiry          = 0x88
	mmsAddressPresent  = 0x80
	mmsAbsoluteTime    = 0x80
	mmsRelativeTime    = 0x81
	charsetUTF8        = 106
	charsetASCII       = 3
)

// parseMMSNotification decodes a WAP Push SMS carrying an MMS M-Notification.ind. It returns the
// sender from the MMS From field (empty when absent) and false for any other or malformed payload.
func parseMMSNotification(data []byte, received string) (string, MMSNotice, bool) {
	// The WSP push header must declare the MMS content type.
	push := wspReader{data: data}
	if _, ok := push.next(); !ok {
		return "", MMSNotice{}, false
	}
	if kind, ok := push.next(); !ok || kind != wspPush {
		return "", MMSNotice{}, false
	}
	length, ok := push.uintvar()
	if !ok {
		return "", MMSNotice{}, false
	}
	headers, ok := push.take(length)
	if !ok || !isMMSContentType(headers) {
		return "", MMSNotice{}, false
	}

	// Read the MMS header fields; any malformed field rejects the whole notification.
	body := wspReader{data: push.rest()}
	var sender string
	var notice MMSNotice
	notification := false
	for !body.done() {
		field, _ := body.next()
		switch field {
		case mmsMessageType:
			value, ok := body.next()
			if !ok || value != mmsNotificationInd {
				return "", MMSNotice{}, false
			}
			notification = true
		case mmsFrom:
			value, ok := body.valueField()
			if !ok {
				return "", MMSNotice{}, false
			}
			if len(value) > 0 && value[0] == mmsAddressPresent {
				address := wspReader{data: value[1:]}
				text, ok := address.encodedString()
				if !ok {
					return "", MMSNotice{}, false
				}
				sender, _, _ = strings.Cut(text, "/TYPE=")
			}
		case mmsSubject:
			text, ok := body.encodedString()
			if !ok {
				return "", MMSNotice{}, false
			}
			notice.Subject = text
		case mmsSize:
			size, ok := body.longInteger()
			if !ok {
				return "", MMSNotice{}, false
			}
			notice.Size = size
		case mmsExpiry:
			value, ok := body.valueField()
			if !ok || len(value) == 0 {
				return "", MMSNotice{}, false
			}
			seconds := wspReader{data: value[1:]}
			delta, ok := seconds.integer()
			if !ok {
				return "", MMSNotice{}, false
			}
			notice.Expiry = expiryTime(value[0], delta, received)
		default:
			// Skip fields this notice does not display, including text-named extension headers.
			if field < 0x80 {
				if _, ok := body.text(); !ok {
					return "", MMSNotice{}, false
				}
			}
			if !body.skipValue() {
				return "", MMSNotice{}, false
			}
		}
	}
	return sender, notice, notification
}

func isMMSContentType(headers []byte) bool {
	// The content type is the first push header: well-known short integer, text, or general form.
	r := wspReader{data: headers}
	first, ok := r.peek()
	if !ok {
		return false
	}
	if first < 0x20 {
		length, ok := r.valueLength()
		if !ok {
			return false
		}
		media, ok := r.take(length)
		if !ok {
			return false
		}
		r = wspReader{data: media}
		if first, ok = r.peek(); !ok {
			return false
		}
	}
	if first >= 0x80 {
		return first&0x7f == wspMMSContentType
	}
	text, ok := r.text()
	return ok && text == mmsContentType
}

func expiryTime(token byte, value int64, received string) string {
	// Absolute expiry is seconds since the epoch; relative expiry counts from the SMS timestamp.
	switch token {
	case mmsAbsoluteTime:
		zone := time.UTC
		if base, ok := parseSMSTime(received); ok {
			zone = base.Location()
		}
		return time.Unix(value, 0).In(zone).Format(time.RFC3339)
	case mmsRelativeTime:
		base, ok := parseSMSTime(received)
		if !ok {
			return ""
		}
		return base.Add(time.Duration(value) * time.Second).Format(time.RFC3339)
	default:
		return ""
	}
}

func parseSMSTime(value string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05-07", "2006-01-02T15:04:05-0700"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

// wspReader decodes WAP binary (WSP/MMS) values with bounds checks on every read.
type wspReader struct {
	data []byte
	pos  int
}

func (r *wspReader) done() bool { return r.pos >= len(r.data) }

func (r *wspReader) rest() []byte { return r.data[r.pos:] }

func (r *wspReader) peek() (byte, bool) {
	if r.done() {
		return 0, false
	}
	return r.data[r.pos], true
}

func (r *wspReader) next() (byte, bool) {
	value, ok := r.peek()
	if ok {
		r.pos++
	}
	return value, ok
}

func (r *wspReader) take(n int) ([]byte, bool) {
	if n < 0 || n > len(r.data)-r.pos {
		return nil, false
	}
	value := r.data[r.pos : r.pos+n]
	r.pos += n
	return value, true
}

func (r *wspReader) uintvar() (int, bool) {
	// At most five bytes encode a 32-bit unsigned value.
	value := 0
	for range 5 {
		b, ok := r.next()
		if !ok {
			return 0, false
		}
		value = value<<7 | int(b&0x7f)
		if b&0x80 == 0 {
			return value, true
		}
	}
	return 0, false
}

func (r *wspReader) valueLength() (int, bool) {
	first, ok := r.next()
	switch {
	case !ok:
		return 0, false
	case first < 31:
		return int(first), true
	case first == 31:
		return r.uintvar()
	default:
		return 0, false
	}
}

// valueField returns the bytes of a length-prefixed value.
func (r *wspReader) valueField() ([]byte, bool) {
	length, ok := r.valueLength()
	if !ok {
		return nil, false
	}
	return r.take(length)
}

func (r *wspReader) text() (string, bool) {
	// A text string ends at NUL; a leading quote byte only escapes the first character.
	end := -1
	for i := r.pos; i < len(r.data); i++ {
		if r.data[i] == 0 {
			end = i
			break
		}
	}
	if end < 0 {
		return "", false
	}
	value := r.data[r.pos:end]
	r.pos = end + 1
	if len(value) > 0 && (value[0] == 0x7f || value[0] == '"') {
		value = value[1:]
	}
	return string(value), true
}

// encodedString returns text in a known charset; other charsets leave the value unknown (empty).
func (r *wspReader) encodedString() (string, bool) {
	first, ok := r.peek()
	if !ok {
		return "", false
	}
	charset := int64(charsetUTF8)
	source := r
	if first <= 31 {
		value, ok := r.valueField()
		if !ok {
			return "", false
		}
		source = &wspReader{data: value}
		if charset, ok = source.integer(); !ok {
			return "", false
		}
	}
	text, ok := source.text()
	if !ok {
		return "", false
	}
	if (charset != charsetUTF8 && charset != charsetASCII) || !utf8.ValidString(text) {
		return "", true
	}
	return text, true
}

// integer reads an Integer-value: a short integer or a long integer.
func (r *wspReader) integer() (int64, bool) {
	first, ok := r.peek()
	if !ok {
		return 0, false
	}
	if first >= 0x80 {
		r.pos++
		return int64(first & 0x7f), true
	}
	return r.longInteger()
}

func (r *wspReader) longInteger() (int64, bool) {
	length, ok := r.next()
	if !ok || length == 0 || length > 8 {
		return 0, false
	}
	value, ok := r.take(int(length))
	if !ok {
		return 0, false
	}
	var result int64
	for _, b := range value {
		result = result<<8 | int64(b)
	}
	return result, result >= 0
}

func (r *wspReader) skipValue() bool {
	// Every WSP value is a length-prefixed block, a NUL-terminated text, or one short-integer byte.
	first, ok := r.peek()
	if !ok {
		return false
	}
	switch {
	case first <= 31:
		_, ok := r.valueField()
		return ok
	case first < 0x80:
		_, ok := r.text()
		return ok
	default:
		r.pos++
		return true
	}
}
