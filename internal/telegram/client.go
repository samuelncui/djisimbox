// Package telegram sends plain-text SMS notifications through the Telegram Bot API.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	apiURL           = "https://api.telegram.org/bot"
	requestTimeout   = 30 * time.Second
	maxResponseBytes = 64 * 1024
	// getUpdates carries message bodies, so it needs a larger bound than a send result.
	maxUpdatesBytes = 1 << 20
	updatesTimeout  = 10 * time.Second
)

var (
	tokenPattern    = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`)
	usernamePattern = regexp.MustCompile(`^@[A-Za-z][A-Za-z0-9_]{4,31}$`)
	chatIDPattern   = regexp.MustCompile(`^-?[0-9]+$`)
)

// Client sends one message per request; the caller owns retry and queue policy.
type Client struct {
	base string
	http *http.Client
}

// Error exposes retry policy without retaining response text or credential-bearing URLs.
// Code is the Telegram error code, or the HTTP status when no valid API error exists.
// A zero Code represents local validation, transport, or cancellation failure.
type Error struct {
	Code       int
	RetryAfter time.Duration
	Permanent  bool
	kind       string
	cause      error
}

// Error returns a fixed classification safe for application logs.
func (e *Error) Error() string {
	if e.Code != 0 {
		return fmt.Sprintf("telegram: %s (code %d)", e.kind, e.Code)
	}
	return "telegram: " + e.kind
}

// Unwrap preserves context cancellation checks while excluding raw transport errors.
func (e *Error) Unwrap() error {
	return e.cause
}

// New uses the official HTTPS endpoint and bounds each request to 30 seconds.
// A supplied HTTP client is copied, so redirect and timeout policy are private to Client.
func New(token string, client *http.Client) (*Client, error) {
	// Reject malformed path components before a credential-bearing URL is built.
	if len(token) > 512 || !tokenPattern.MatchString(token) {
		return nil, &Error{Permanent: true, kind: "invalid bot token"}
	}

	// Keep a token-bearing request from following redirects to any destination.
	httpClient := http.Client{}
	if client != nil {
		httpClient = *client
	}
	if httpClient.Timeout <= 0 || httpClient.Timeout > requestTimeout {
		httpClient.Timeout = requestTimeout
	}
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Client{base: apiURL + token + "/", http: &httpClient}, nil
}

// Send sends validated UTF-8 text without markup interpretation or link previews.
// Its error never contains the bot token, chat ID, message, or Telegram description.
func (c *Client) Send(ctx context.Context, chatID string, text string) (int64, error) {
	// Reject local mistakes before opening a connection or consuming rate limits.
	if !validChatID(chatID) {
		return 0, &Error{Permanent: true, kind: "invalid chat ID"}
	}
	if text == "" || !utf8.ValidString(text) || textUnits(text) > MaxTextUnits {
		return 0, &Error{Permanent: true, kind: "invalid message text"}
	}
	if err := ctx.Err(); err != nil {
		return 0, transportError("request canceled", err)
	}

	// Send JSON so arbitrary SMS punctuation is never interpreted as formatting.
	body, err := json.Marshal(struct {
		ChatID      string `json:"chat_id"`
		Text        string `json:"text"`
		LinkPreview struct {
			IsDisabled bool `json:"is_disabled"`
		} `json:"link_preview_options"`
	}{ChatID: chatID, Text: text, LinkPreview: struct {
		IsDisabled bool `json:"is_disabled"`
	}{IsDisabled: true}})
	if err != nil {
		return 0, &Error{Permanent: true, kind: "encode request failed"}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"sendMessage", bytes.NewReader(body))
	if err != nil {
		return 0, &Error{Permanent: true, kind: "create request failed"}
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return 0, transportError("request failed", err)
	}
	defer response.Body.Close()

	// Bound untrusted response data before decoding; never retain its description.
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return 0, transportError("read response failed", err)
	}
	if len(data) > maxResponseBytes {
		return 0, statusError(response.StatusCode, "response too large")
	}
	var result struct {
		OK        bool `json:"ok"`
		ErrorCode int  `json:"error_code"`
		Result    struct {
			MessageID int64 `json:"message_id"`
		} `json:"result"`
		Parameters struct {
			RetryAfter int64 `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return 0, statusError(response.StatusCode, "invalid response")
	}

	// Treat both HTTP status and the API envelope as authoritative failure boundaries.
	if response.StatusCode < 200 || response.StatusCode >= 300 || !result.OK {
		code := response.StatusCode
		if code >= 200 && code < 300 && result.ErrorCode >= 400 && result.ErrorCode <= 599 {
			code = result.ErrorCode
		}
		failure := statusError(code, "request rejected")
		if code == http.StatusTooManyRequests && result.Parameters.RetryAfter > 0 {
			seconds := min(result.Parameters.RetryAfter, int64((1<<63-1)/time.Second))
			failure.RetryAfter = time.Duration(seconds) * time.Second
		}
		return 0, failure
	}
	if result.Result.MessageID <= 0 {
		return 0, statusError(response.StatusCode, "invalid message result")
	}
	return result.Result.MessageID, nil
}

func validChatID(value string) bool {
	// Usernames are accepted only in Telegram's explicit channel-recipient form.
	if usernamePattern.MatchString(value) {
		return true
	}

	// Numeric recipients must fit the API integer representation without extra syntax.
	if !chatIDPattern.MatchString(value) {
		return false
	}
	id, err := strconv.ParseInt(value, 10, 64)
	return err == nil && id != 0
}

func statusError(code int, kind string) *Error {
	return &Error{
		Code:      code,
		Permanent: code >= 300 && code < 500 && code != http.StatusRequestTimeout && code != http.StatusTooManyRequests,
		kind:      kind,
	}
}

func transportError(kind string, err error) *Error {
	// Retain only known safe sentinels; url.Error can contain the complete bot token.
	failure := &Error{kind: kind}
	if errors.Is(err, context.Canceled) {
		failure.cause = context.Canceled
	} else if errors.Is(err, context.DeadlineExceeded) {
		failure.cause = context.DeadlineExceeded
	}
	return failure
}

// Chat is a conversation seen in recent bot updates.
type Chat struct {
	ID   int64
	Name string
}

// Me returns the bot's username without the leading @.
// Its error never contains the bot token or Telegram description.
func (c *Client) Me(ctx context.Context) (string, error) {
	// Ask Telegram who owns the token.
	var result struct {
		Username string `json:"username"`
	}
	if err := c.call(ctx, "getMe", struct{}{}, maxResponseBytes, &result); err != nil {
		return "", err
	}
	if result.Username == "" {
		return "", statusError(http.StatusOK, "invalid bot result")
	}
	return result.Username, nil
}

// RecentChats lists unique chats from pending updates, newest first.
// It sends no offset, so updates stay unconfirmed and other consumers still receive them.
func (c *Client) RecentChats(ctx context.Context) ([]Chat, error) {
	// Poll without long-waiting; the request must stay short for an interactive guide.
	ctx, cancel := context.WithTimeout(ctx, updatesTimeout)
	defer cancel()
	type holder struct {
		Chat *struct {
			ID        int64  `json:"id"`
			Title     string `json:"title"`
			Username  string `json:"username"`
			FirstName string `json:"first_name"`
			LastName  string `json:"last_name"`
		} `json:"chat"`
	}
	var updates []struct {
		Message       *holder `json:"message"`
		EditedMessage *holder `json:"edited_message"`
		ChannelPost   *holder `json:"channel_post"`
		MyChatMember  *holder `json:"my_chat_member"`
	}
	params := struct {
		Timeout int `json:"timeout"`
		Limit   int `json:"limit"`
	}{Limit: 100}
	if err := c.call(ctx, "getUpdates", params, maxUpdatesBytes, &updates); err != nil {
		return nil, err
	}

	// Keep the newest name for each chat.
	var chats []Chat
	seen := make(map[int64]struct{})
	for i := len(updates) - 1; i >= 0; i-- {
		for _, item := range []*holder{updates[i].Message, updates[i].EditedMessage, updates[i].ChannelPost, updates[i].MyChatMember} {
			if item == nil || item.Chat == nil || item.Chat.ID == 0 {
				continue
			}
			if _, ok := seen[item.Chat.ID]; ok {
				continue
			}
			seen[item.Chat.ID] = struct{}{}
			name := strings.TrimSpace(item.Chat.FirstName + " " + item.Chat.LastName)
			if name == "" {
				name = item.Chat.Title
			}
			if name == "" && item.Chat.Username != "" {
				name = "@" + item.Chat.Username
			}
			chats = append(chats, Chat{ID: item.Chat.ID, Name: name})
		}
	}
	return chats, nil
}

// call posts one read-only method and decodes its result with the same redacted error policy as Send.
func (c *Client) call(ctx context.Context, method string, params any, limit int64, result any) error {
	// Build and send the request.
	body, err := json.Marshal(params)
	if err != nil {
		return &Error{Permanent: true, kind: "encode request failed"}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+method, bytes.NewReader(body))
	if err != nil {
		return &Error{Permanent: true, kind: "create request failed"}
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return transportError("request failed", err)
	}
	defer response.Body.Close()

	// Bound untrusted response data before decoding.
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return transportError("read response failed", err)
	}
	if int64(len(data)) > limit {
		return statusError(response.StatusCode, "response too large")
	}
	var envelope struct {
		OK        bool            `json:"ok"`
		ErrorCode int             `json:"error_code"`
		Result    json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return statusError(response.StatusCode, "invalid response")
	}

	// Treat both HTTP status and the API envelope as authoritative failure boundaries.
	if response.StatusCode < 200 || response.StatusCode >= 300 || !envelope.OK {
		code := response.StatusCode
		if code >= 200 && code < 300 && envelope.ErrorCode >= 400 && envelope.ErrorCode <= 599 {
			code = envelope.ErrorCode
		}
		return statusError(code, "request rejected")
	}
	if err := json.Unmarshal(envelope.Result, result); err != nil {
		return statusError(response.StatusCode, "invalid result")
	}
	return nil
}
