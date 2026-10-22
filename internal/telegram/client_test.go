package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestSendPlainText(t *testing.T) {
	// Inspect the actual request while replacing only the external HTTP transport.
	const text = "【测试】验证码 123456 <b>& _中文_ 😀\nhttps://example.org"
	client, err := New("1234:FAKE_TOKEN", &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		// Verify the fixed credential destination and JSON request contract.
		if request.Method != http.MethodPost || request.URL.String() != "https://api.telegram.org/bot1234:FAKE_TOKEN/sendMessage" {
			t.Errorf("unexpected destination: %s %s", request.Method, request.URL)
		}
		if request.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected content type: %q", request.Header.Get("Content-Type"))
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("decode outgoing JSON: %v", err)
		}
		var gotText, gotChat string
		if err := json.Unmarshal(body["text"], &gotText); err != nil {
			t.Fatalf("decode outgoing text: %v", err)
		}
		if err := json.Unmarshal(body["chat_id"], &gotChat); err != nil {
			t.Fatalf("decode outgoing recipient: %v", err)
		}
		if gotText != text || gotChat != "-1001234567890" {
			t.Errorf("message changed: chat=%q, text=%q", gotChat, gotText)
		}
		if _, exists := body["parse_mode"]; exists {
			t.Error("arbitrary SMS text must not be parsed as markup")
		}
		var preview struct {
			IsDisabled bool `json:"is_disabled"`
		}
		if err := json.Unmarshal(body["link_preview_options"], &preview); err != nil || !preview.IsDisabled {
			t.Errorf("link previews were not disabled: %v", err)
		}

		// Simulate a successful API response with a stable delivery identifier.
		return response(http.StatusOK, `{"ok":true,"result":{"message_id":42}}`), nil
	})})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}

	// Return the Telegram identifier to the delivery caller.
	messageID, err := client.Send(context.Background(), "-1001234567890", text)
	if err != nil || messageID != 42 {
		t.Fatalf("Send() = (%d, %v), want (42, nil)", messageID, err)
	}
}

func TestSendFailurePolicy(t *testing.T) {
	// Cover API-envelope errors as well as HTTP failures, without real network calls.
	cases := []struct {
		name       string
		status     int
		body       string
		code       int
		permanent  bool
		retryAfter time.Duration
	}{
		{"API rate limit", 200, `{"ok":false,"error_code":429,"parameters":{"retry_after":19}}`, 429, false, 19 * time.Second},
		{"HTTP rate limit", 429, `{"ok":false,"parameters":{"retry_after":23}}`, 429, false, 23 * time.Second},
		{"bad request", 400, `{"ok":false,"error_code":400}`, 400, true, 0},
		{"invalid token", 401, `{"ok":false,"error_code":401}`, 401, true, 0},
		{"blocked bot", 403, `{"ok":false,"error_code":403}`, 403, true, 0},
		{"API forbidden", 200, `{"ok":false,"error_code":403}`, 403, true, 0},
		{"not found", 404, `not JSON`, 404, true, 0},
		{"timeout status", 408, `{"ok":false}`, 408, false, 0},
		{"unavailable", 503, `{"ok":false,"error_code":503}`, 503, false, 0},
		{"server HTML", 502, `<html>temporary upstream failure</html>`, 502, false, 0},
		{"status wins", 500, `{"ok":true,"result":{"message_id":1}}`, 500, false, 0},
		{"malformed success", 200, `{"ok":true,`, 200, false, 0},
		{"missing envelope", 200, `{}`, 200, false, 0},
		{"missing result", 200, `{"ok":true}`, 200, false, 0},
		{"trailing response", 200, `{"ok":true,"result":{"message_id":1}} {}`, 200, false, 0},
		{"negative retry", 429, `{"ok":false,"parameters":{"retry_after":-1}}`, 429, false, 0},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			// Isolate one response so classification cannot depend on a previous request.
			client, err := New("1234:FAKE_TOKEN", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return response(test.status, test.body), nil
			})})
			if err != nil {
				t.Fatalf("create client: %v", err)
			}

			// Expose only structured retry decisions to the queue.
			_, err = client.Send(context.Background(), "12345", "test")
			var failure *Error
			if !errors.As(err, &failure) {
				t.Fatalf("expected Telegram Error, got %T: %v", err, err)
			}
			if failure.Code != test.code || failure.Permanent != test.permanent || failure.RetryAfter != test.retryAfter {
				t.Errorf("failure policy = code %d permanent %v retry %s; want %d %v %s",
					failure.Code, failure.Permanent, failure.RetryAfter, test.code, test.permanent, test.retryAfter)
			}
		})
	}
}

func TestSendValidation(t *testing.T) {
	// Count attempted calls so invalid local data cannot consume API requests.
	calls := 0
	client, err := New("1234:FAKE_TOKEN", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return response(200, `{"ok":true,"result":{"message_id":1}}`), nil
	})})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}

	// Cover empty, malformed, and Unicode-sized inputs independently.
	for _, test := range []struct{ chat, text string }{
		{"", "body"}, {"0", "body"}, {"+123", "body"}, {"recipient@example.org", "body"}, {"@bad", "body"},
		{"123", ""}, {"123", string([]byte{0xff})}, {"123", strings.Repeat("a", 4097)},
		{"123", strings.Repeat("😀", 2049)},
	} {
		_, err := client.Send(context.Background(), test.chat, test.text)
		var failure *Error
		if !errors.As(err, &failure) || !failure.Permanent {
			t.Errorf("invalid input was not a permanent local failure: %v", err)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid inputs caused %d API requests", calls)
	}

	// Allow exactly the advertised limit, including surrogate-pair characters.
	for _, test := range []struct{ chat, text string }{
		{"123", strings.Repeat("a", 4096)},
		{"-100123", strings.Repeat("😀", 2048)},
		{"@valid_channel", "a"},
	} {
		if _, err := client.Send(context.Background(), test.chat, test.text); err != nil {
			t.Errorf("valid boundary input rejected: %v", err)
		}
	}
	if calls != 3 {
		t.Errorf("valid inputs caused %d API requests, want 3", calls)
	}
}

func TestSendRedactsFailures(t *testing.T) {
	// Put every sensitive field into upstream failures to exercise log redaction.
	const token, chat, text = "1234:PRIVATE_TOKEN", "87654321", "PRIVATE SMS CONTENT"
	leak := token + " " + chat + " " + text
	cases := []struct {
		name      string
		transport roundTripFunc
	}{
		{"transport", func(request *http.Request) (*http.Response, error) {
			return nil, &url.Error{Op: "Post", URL: request.URL.String(), Err: errors.New(leak)}
		}},
		{"API description", func(*http.Request) (*http.Response, error) {
			body, err := json.Marshal(map[string]any{"ok": false, "error_code": 400, "description": leak})
			if err != nil {
				t.Fatalf("encode fixture: %v", err)
			}
			return response(400, string(body)), nil
		}},
		{"malformed response", func(*http.Request) (*http.Response, error) {
			return response(200, leak), nil
		}},
		{"body read", func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(failingReader{err: errors.New(leak)})}, nil
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			// Obtain the production error through the same client entry point as delivery.
			client, err := New(token, &http.Client{Transport: test.transport})
			if err != nil {
				t.Fatalf("create client: %v", err)
			}
			_, err = client.Send(context.Background(), chat, text)
			var failure *Error
			if !errors.As(err, &failure) {
				t.Fatalf("expected classified error, got %v", err)
			}

			// Neither ordinary formatting nor traversing the error chain may reveal secrets.
			for current := err; current != nil; current = errors.Unwrap(current) {
				printed := fmt.Sprintf("%s %+v %#v", current, current, current)
				for _, secret := range []string{token, chat, text, "https://"} {
					if strings.Contains(printed, secret) {
						t.Errorf("error exposed sensitive value %q", secret)
					}
				}
			}
		})
	}
}

func TestSendContextErrors(t *testing.T) {
	// Distinguish cancellation from generic network failure without leaking URLs.
	for _, test := range []struct {
		name   string
		cause  error
		before bool
	}{
		{"already canceled", context.Canceled, true},
		{"transport cancellation", context.Canceled, false},
		{"transport deadline", context.DeadlineExceeded, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Inject the sentinel inside the URL-bearing error normally returned by net/http.
			called := false
			client, err := New("1234:FAKE_TOKEN", &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				called = true
				return nil, &url.Error{Op: "Post", URL: request.URL.String(), Err: test.cause}
			})})
			if err != nil {
				t.Fatalf("create client: %v", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if test.before {
				cancel()
			}

			// Preserve errors.Is and errors.As for callers deciding whether to stop work.
			_, err = client.Send(ctx, "123", "body")
			var failure *Error
			if !errors.As(err, &failure) || !errors.Is(err, test.cause) {
				t.Fatalf("context identity was lost: %v", err)
			}
			if failure.Permanent || failure.Code != 0 {
				t.Errorf("context failure policy = permanent %v, code %d", failure.Permanent, failure.Code)
			}
			if test.before && called {
				t.Error("already-canceled request reached the transport")
			}
		})
	}
}

func TestSendBoundsResponse(t *testing.T) {
	// Track bytes and closure on an oversized upstream payload.
	body := &countedBody{Reader: strings.NewReader(strings.Repeat("x", 2*maxResponseBytes))}
	client, err := New("1234:FAKE_TOKEN", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: body}, nil
	})})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}

	// A bounded read must fail safely while releasing the response resource.
	_, err = client.Send(context.Background(), "123", "body")
	var failure *Error
	if !errors.As(err, &failure) || failure.Permanent {
		t.Fatalf("expected transient oversized-response error, got %v", err)
	}
	if body.read > maxResponseBytes+1 || !body.closed {
		t.Errorf("response consumed %d bytes, closed=%v", body.read, body.closed)
	}
}

func TestSendTimeout(t *testing.T) {
	// Let net/http apply a short timeout to a transport waiting on the real request context.
	provided := &http.Client{Timeout: 10 * time.Millisecond, Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
	client, err := New("1234:FAKE_TOKEN", provided)
	if err != nil {
		t.Fatalf("create client: %v", err)
	}

	// A stalled HTTP exchange becomes a retryable deadline failure for the queue.
	_, err = client.Send(context.Background(), "123", "body")
	var failure *Error
	if !errors.As(err, &failure) || !errors.Is(err, context.DeadlineExceeded) || failure.Permanent {
		t.Fatalf("timeout was not preserved as a transient deadline: %v", err)
	}
	if client.http.Timeout != provided.Timeout {
		t.Error("client's shorter timeout was not preserved")
	}
}

func TestSendRefusesRedirects(t *testing.T) {
	// Simulate a redirect containing the token and count destination requests.
	calls := 0
	provided := &http.Client{Timeout: time.Hour, Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		result := response(http.StatusTemporaryRedirect, `{}`)
		result.Header = http.Header{"Location": {"https://attacker.example/1234:FAKE_TOKEN"}}
		return result, nil
	})}
	client, err := New("1234:FAKE_TOKEN", provided)
	if err != nil {
		t.Fatalf("create client: %v", err)
	}

	// Stop at the original response and leave the caller's HTTP policy untouched.
	_, err = client.Send(context.Background(), "123", "body")
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != 307 || !failure.Permanent {
		t.Fatalf("redirect was not rejected: %v", err)
	}
	if calls != 1 || provided.CheckRedirect != nil || provided.Timeout != time.Hour {
		t.Errorf("redirect calls=%d; caller policy was changed=%v", calls, provided.CheckRedirect != nil)
	}
	if client.http.Timeout != requestTimeout {
		t.Errorf("request timeout is unbounded: %v", client.http.Timeout)
	}
}

func TestNewValidation(t *testing.T) {
	// Reject path injection and malformed credentials without reproducing their contents.
	for _, token := range []string{"", "PRIVATE", "123:PRIVATE/../bad", "123:PRIVATE\n", strings.Repeat("1", 513) + ":PRIVATE"} {
		_, err := New(token, nil)
		var failure *Error
		if !errors.As(err, &failure) || !failure.Permanent || strings.Contains(err.Error(), "PRIVATE") {
			t.Errorf("unsafe token validation result: %v", err)
		}
	}

	// The default client still receives a timeout and redirect policy.
	client, err := New("1234:FAKE_TOKEN", nil)
	if err != nil || client.http.Timeout != requestTimeout || client.http.CheckRedirect == nil {
		t.Fatalf("default HTTP policy missing: %v", err)
	}
}

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) {
	return 0, r.err
}

type countedBody struct {
	io.Reader
	read   int
	closed bool
}

func (b *countedBody) Read(value []byte) (int, error) {
	n, err := b.Reader.Read(value)
	b.read += n
	return n, err
}

func (b *countedBody) Close() error {
	b.closed = true
	return nil
}
