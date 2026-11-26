package observability

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/secret"
)

// channelKind is one kind of notification channel (docs/domains/observability.md「通知渠道」): which AlertChannel member
// names it, how its destination is checked, what secret it takes, and, for one that posts over HTTPS, how a
// notification is posted and what answer is a delivery. Email has no post: it goes through the mail outbox.
type channelKind struct {
	name   string
	of     func(AlertChannel) string             // the member that names the kind; "" when ch is of another kind
	check  func(string) (host string, err error) // the destination's form, and the host a post connects to
	secret secretRule
	// post is the request that delivers text on ch with the channel's live secrets, nil for email. Webhook posts the
	// frozen signed event instead (webhookWorker.send).
	post func(ctx context.Context, ch AlertChannel, text string, keys []secret.Secret, now time.Time) (*http.Request, error)
	// delivered reads a 2xx answer's body: the bots answer 200 with an error code for a refusal.
	delivered func(body []byte) bool
}

// secretRule is what a kind's secret is. A kind with max 0 takes none. A required secret missing fails the delivery
// without a request; an optional one is left out. A replaced secret keeps working for overlap, for a receiver that
// checks either while it switches; a bot checks only the one it holds, so its old secret goes at once.
type secretRule struct {
	min, max int
	required bool
	overlap  time.Duration
	form     *regexp.Regexp // the secret's shape, when the receiver gives it one
}

var (
	telegramChat  = regexp.MustCompile(`^(-?[0-9]{1,20}|@[A-Za-z][A-Za-z0-9_]{4,31})$`)
	telegramToken = regexp.MustCompile(`^[0-9]{1,20}:[A-Za-z0-9_-]{20,100}$`)
)

// telegramAPI is the Bot API's origin; it is fixed, so a chat ID is the whole destination.
const telegramAPI = "https://api.telegram.org"

// channelKinds is every kind, the table the configuration, the dispatch and the worker read. The bots' hosts are their
// official ones: a URL elsewhere is a typo or someone else's endpoint, and a bot's answer is read by its own rules.
var channelKinds = []channelKind{
	{name: "email", of: func(c AlertChannel) string { return c.Email }, check: checkEmail},
	{
		name: "webhook", of: func(c AlertChannel) string { return c.WebhookURL }, check: httpsHost(),
		secret: secretRule{min: 32, max: 512, required: true, overlap: time.Hour}, // the receiver checks either while it switches
	},
	{
		name: "feishu", of: func(c AlertChannel) string { return c.FeishuURL }, check: httpsHost("open.feishu.cn", "open.larksuite.com"),
		secret: secretRule{min: 1, max: 512}, post: postFeishu, delivered: zeroCode("code", "StatusCode"),
	},
	{
		name: "wecom", of: func(c AlertChannel) string { return c.WeComURL }, check: httpsHost("qyapi.weixin.qq.com"),
		post: postWeCom, delivered: zeroCode("errcode"),
	},
	{
		name: "telegram", of: func(c AlertChannel) string { return c.TelegramChatID }, check: checkTelegramChat,
		secret: secretRule{min: 1, max: 512, required: true, form: telegramToken}, post: postTelegram, delivered: telegramOK,
	},
}

// kindOf is ch's kind and destination; false unless exactly one destination member is set.
func kindOf(ch AlertChannel) (*channelKind, string, bool) {
	var found *channelKind
	var dest string
	for i := range channelKinds {
		if v := channelKinds[i].of(ch); v != "" {
			if found != nil {
				return nil, "", false
			}
			found, dest = &channelKinds[i], v
		}
	}
	return found, dest, found != nil
}

// destination is what ch's self-test and secrets are bound to: changing it tests the channel again and leaves the
// secrets of the old one behind. Email and webhook are their address alone, as they were before the other kinds; the
// others carry their kind, so that one value can never name two kinds.
func destination(ch AlertChannel) string {
	k, dest, ok := kindOf(ch)
	if !ok || k.name == "email" || k.name == "webhook" {
		return dest
	}
	return k.name + ":" + dest
}

// posted reports whether ch's notifications go over HTTPS by the webhook job rather than through the mail outbox.
func posted(ch AlertChannel) bool {
	k, _, ok := kindOf(ch)
	return ok && k.name != "email"
}

func checkTelegramChat(chat string) (string, error) {
	if !telegramChat.MatchString(chat) {
		return "", ErrAlertConfiguration
	}
	return strings.TrimPrefix(telegramAPI, "https://"), nil
}

// botTextLimit keeps a message under the smallest bot limit, WeCom's 2048 bytes of text.
const botTextLimit = 2000

// noticeText is what a person reads of delivery d, in every channel but the webhook's JSON: the incident, and for an
// approval the request and the admin page that decides it. origin is the admin's, "" when none is known yet.
func noticeText(d alertDelivery, origin string) string {
	phase := "firing"
	switch {
	case d.kind == "alert.test":
		phase = "test"
	case d.seq == -1:
		phase = "resolved"
	}
	var b strings.Builder
	var f map[string]string
	// The fields were encoded from such a map; should they not read back, the approval is told as any other incident.
	if d.kind == "operations.approval_pending" && phase == "firing" && jsonx.Unmarshal(d.fields, &f) == nil {
		b.WriteString("Axiom approval needed\nTool: " + f["tool"] + "\nTarget: " + f["target"] + "\nExpires: " + f["expires_at"])
		if origin != "" {
			b.WriteString("\nDecide: " + origin + "/approvals/" + d.key)
		}
	} else {
		b.WriteString("Axiom alert " + phase + ": " + d.kind + " (" + d.key + ")\nSeverity: " + d.severity + "\nFields: " + string(d.fields))
	}
	b.WriteString("\nNotification: " + d.id.String())
	return b.String()
}

// clip cuts text to at most n bytes on a rune boundary.
func clip(text string, n int) string {
	if len(text) <= n {
		return text
	}
	for n > 0 && !utf8.RuneStart(text[n]) {
		n--
	}
	return text[:n]
}

func postJSON(ctx context.Context, target string, body any) (*http.Request, error) {
	raw, err := jsonx.MarshalCanonical(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(raw))
	if err != nil {
		return nil, errors.New("bot: invalid destination")
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

// postFeishu is a custom bot's text message; with a signing secret it carries the timestamp and the signature, the
// base64 HMAC-SHA256 whose key is the timestamp, a newline and the secret, over nothing (Feishu's「自定义机器人」).
func postFeishu(ctx context.Context, ch AlertChannel, text string, keys []secret.Secret, now time.Time) (*http.Request, error) {
	body := map[string]any{"msg_type": "text", "content": map[string]string{"text": text}}
	if len(keys) > 0 {
		stamp := strconv.FormatInt(now.Unix(), 10)
		mac := hmac.New(sha256.New, []byte(stamp+"\n"+keys[0].Reveal()))
		body["timestamp"], body["sign"] = stamp, base64.StdEncoding.EncodeToString(mac.Sum(nil))
	}
	return postJSON(ctx, ch.FeishuURL, body)
}

// postWeCom is a group bot's text message; the key in the address is its only credential.
func postWeCom(ctx context.Context, ch AlertChannel, text string, _ []secret.Secret, _ time.Time) (*http.Request, error) {
	return postJSON(ctx, ch.WeComURL, map[string]any{"msgtype": "text", "text": map[string]string{"content": text}})
}

// postTelegram is the Bot API's sendMessage; the token is part of the path, so the request is never logged whole.
func postTelegram(ctx context.Context, ch AlertChannel, text string, keys []secret.Secret, _ time.Time) (*http.Request, error) {
	return postJSON(ctx, telegramAPI+"/bot"+keys[0].Reveal()+"/sendMessage",
		map[string]any{"chat_id": ch.TelegramChatID, "text": text, "disable_web_page_preview": true})
}

// zeroCode accepts an answer whose first present member of names is 0: Feishu answers code, its older bots StatusCode,
// WeCom errcode. An answer with none of them is not a delivery.
func zeroCode(names ...string) func([]byte) bool {
	return func(body []byte) bool {
		var answer map[string]any
		if jsonx.Unmarshal(body, &answer) != nil {
			return false
		}
		for _, name := range names {
			if code, ok := answer[name].(float64); ok {
				return code == 0
			}
		}
		return false
	}
}

func telegramOK(body []byte) bool {
	var answer struct {
		OK bool `json:"ok"`
	}
	return jsonx.Unmarshal(body, &answer) == nil && answer.OK
}

// sendBot posts d's frozen text on its bot channel. The error is a category only: an address or a token never reaches
// a job's errors or the logs.
func (w *webhookWorker) sendBot(ctx context.Context, k *channelKind, d alertDelivery, keys []secret.Secret) error {
	var frozen struct {
		Text string `json:"text"`
	}
	if err := jsonx.Unmarshal(d.payload, &frozen); err != nil {
		return errors.New("bot: unreadable notice")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := k.post(ctx, d.ch, frozen.Text, keys, w.now().UTC())
	if err != nil {
		return err
	}
	resp, err := w.exit.Do(req)
	if err != nil {
		return errors.New("bot: request failed")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if closed := resp.Body.Close(); err == nil {
		err = closed
	}
	if err != nil {
		return errors.New("bot: answer unreadable")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("bot: HTTP status %d", resp.StatusCode)
	}
	if !k.delivered(body) {
		return errors.New("bot: refused")
	}
	return nil
}
