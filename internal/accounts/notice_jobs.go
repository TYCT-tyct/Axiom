package accounts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"math/big"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"uuid"

	"github.com/riverqueue/river"

	"axiom/internal/platform/egress"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/mail"
	"axiom/internal/platform/pg"
)

type noticeArgs struct {
	ID uuid.UUID `json:"id"`
}

func (noticeArgs) Kind() string { return "accounts.notice_webhook" }
func (noticeArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: pg.QueueNotifications, MaxAttempts: 5}
}

type noticeWorker struct {
	db    *pg.DB
	scope pg.Scope
	exit  *egress.Client
}

// RegisterUserNotices installs webhook delivery; the composition root drives journal consumption and DispatchUserNotices.
func RegisterUserNotices(db *pg.DB, scope pg.Scope, q *pg.Queue, exit *egress.Client) error {
	w := &noticeWorker{db: db, scope: scope, exit: exit}
	return river.AddWorkerSafely(q.Workers(), river.WorkFunc(w.work))
}

type userNotice struct {
	ID                          uuid.UUID `json:"id"`
	Kind                        string    `json:"kind"`
	Value                       string    `json:"value"`
	At                          time.Time `json:"at"`
	Currency                    string    `json:"currency,omitempty"`
	Detail                      string    `json:"detail,omitempty"`
	site, account, user         uuid.UUID
	email, brand, url, language string
	emailDone                   bool
	preferences                 NotificationPreferences
}

// noticeKinds are the notice kinds this code can render. A dispatcher claims only these: during a rolling release a
// newer node may write a kind this node does not know, and marking it dispatched without sending it would lose the
// notice for good. It waits for a node that understands it.
var noticeKinds = []string{"low_balance", "top_up", "factor_changed", "spend_anomaly", "site_payment_changed"}

// DispatchUserNotices freezes destinations for at most 100 notices and returns how many were dispatched.
func DispatchUserNotices(ctx context.Context, tx pg.Tx, q *pg.Queue, m *mail.Mailer) (int, error) {
	p := notificationDefaults()
	rows, err := tx.Query(ctx, `SELECT n.id,n.site_id,n.account_id,n.user_id,n.kind,n.value::text,n.detail,n.at,n.email_done,u.email,s.name,s.currency,coalesce(p.low_balance,$1),coalesce(p.top_up,$2),coalesce(p.factor_changed,$3),coalesce(p.spend_anomaly,$4),coalesce(p.webhook_url,''),coalesce(p.language,$5)
 FROM accounts.user_notices n JOIN accounts.users u ON u.id=n.user_id JOIN accounts.sites s ON s.id=n.site_id LEFT JOIN accounts.notification_preferences p ON p.user_id=n.user_id
 WHERE NOT n.dispatched AND n.kind = ANY($6) ORDER BY n.id FOR UPDATE OF n SKIP LOCKED LIMIT 100`, p.LowBalance, p.TopUp, p.FactorChanged, p.SpendAnomaly, p.Language, noticeKinds)
	if err != nil {
		return 0, err
	}
	var notices []userNotice
	for rows.Next() {
		var n userNotice
		if err = rows.Scan(&n.ID, &n.site, &n.account, &n.user, &n.Kind, &n.Value, &n.Detail, &n.At, &n.emailDone, &n.email, &n.brand, &n.Currency, &n.preferences.LowBalance, &n.preferences.TopUp, &n.preferences.FactorChanged, &n.preferences.SpendAnomaly, &n.preferences.WebhookURL, &n.preferences.Language); err != nil {
			break
		}
		notices = append(notices, n)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return 0, err
	}
	dispatched := 0
	for _, n := range notices {
		if !slices.Contains(noticeKinds, n.Kind) {
			continue // the query filters these; never mark a kind this node cannot render
		}
		done, err := dispatchUserNotice(ctx, tx, q, m, n)
		if err != nil {
			return 0, err
		}
		if done {
			dispatched++
		}
	}
	return dispatched, nil
}

func dispatchUserNotice(ctx context.Context, tx pg.Tx, q *pg.Queue, m *mail.Mailer, n userNotice) (bool, error) {
	p := n.preferences
	enabled := noticeEnabled(n.Kind, p)
	needsEmail := enabled && !n.emailDone
	if needsEmail && !m.Configured() {
		return false, nil
	}
	state, url := "none", ""
	emailFailed := needsEmail && !emailAllowed(n.email)
	n.language = p.Language
	if needsEmail && !emailFailed {
		if err := m.Enqueue(ctx, tx, mail.Tenant{Site: n.site, Account: n.account}, noticeMail(n)); err != nil {
			return false, err
		}
	}
	if enabled && p.WebhookURL != "" {
		state, url = "queued", p.WebhookURL
		if _, err := q.InsertManyTx(ctx, tx, []river.InsertManyParams{{Args: noticeArgs{ID: n.ID}}}); err != nil {
			return false, err
		}
	}
	_, err := tx.Exec(ctx, `UPDATE accounts.user_notices SET dispatched=true,webhook_url=$2,webhook_state=$3,email_failed=$4 WHERE id=$1`, n.ID, url, state, emailFailed)
	return err == nil, err
}

// noticeEnabled answers whether the account wants this kind; the mandatory site payment notice has no switch. A kind
// this code does not know is off: it is never claimed (see noticeKinds), and if it ever reached here it must not be
// marked sent.
func noticeEnabled(kind string, p NotificationPreferences) bool {
	switch kind {
	case "low_balance":
		return p.LowBalance
	case "top_up":
		return p.TopUp
	case "factor_changed":
		return p.FactorChanged
	case "spend_anomaly":
		return p.SpendAnomaly
	case "site_payment_changed":
		return true // a site owner's record of who changed its payment settings is not optional
	default:
		return false
	}
}

func noticeMail(n userNotice) mail.Message {
	names := noticeNames(n.language)
	brand := strings.Map(func(r rune) rune {
		if r < ' ' || r == 127 {
			return -1
		}
		return r
	}, n.brand)
	value := n.Value
	if n.Kind != "factor_changed" && n.Kind != "site_payment_changed" && (n.Kind != "spend_anomaly" || n.Value != "0") {
		amount, _ := new(big.Int).SetString(n.Value, 10)
		value = new(big.Rat).SetFrac(amount, big.NewInt(1_000_000_000)).FloatString(9) + " " + n.Currency
	}
	detail := noticeDetail(n)
	text := brand + "\n\n" + names[n.Kind]
	if detail != "" {
		text += noticeDetailLine(n.language, detail)
	}
	if (n.Kind != "spend_anomaly" && n.Kind != "site_payment_changed") || n.Value != "0" {
		text += "\n" + value
	}
	if advice := noticeAdvice(n); advice != "" {
		text += "\n" + advice
	}
	return mail.Message{To: n.email, Subject: noticeSubject(brand, names[n.Kind], detail), Text: text, HTML: "<p>" + html.EscapeString(text) + "</p>"}
}

// noticeDetail is the changed secondary site's name for a site payment notice; other kinds have none.
func noticeDetail(n userNotice) string {
	if n.Kind != "site_payment_changed" {
		return ""
	}
	return strings.Map(func(r rune) rune {
		if r < ' ' || r == 127 {
			return -1
		}
		return r
	}, n.Detail)
}

// noticeDetailLine names the changed site after the notice title.
func noticeDetailLine(language, detail string) string {
	if language == "zh" {
		return "\n站点：" + detail
	}
	return "\nSite: " + detail
}

// noticeSubject adds the changed site to the subject so two child sites do not look identical in a mailbox.
func noticeSubject(brand, title, detail string) string {
	if detail == "" {
		return brand + " — " + title
	}
	return brand + " — " + title + " (" + detail + ")"
}

// noticeNames is the localized title of every notice kind.
func noticeNames(language string) map[string]string {
	if language == "zh" {
		return map[string]string{"low_balance": "余额不足", "top_up": "充值到账", "factor_changed": "认证因素已变更", "spend_anomaly": "账户异常活动", "site_payment_changed": "收款配置已变更"}
	}
	return map[string]string{"low_balance": "Low balance", "top_up": "Top-up credited", "factor_changed": "Authentication factors changed", "spend_anomaly": "Unusual account activity", "site_payment_changed": "Payment settings changed"}
}

// noticeAdvice is the one line a security reminder adds after its facts; other notices have none.
func noticeAdvice(n userNotice) string {
	switch n.Kind {
	case "spend_anomaly":
		if n.language == "zh" {
			return "如果不是您本人操作，请立即停用可疑的 API key 并联系站点管理员。"
		}
		return "If this was not you, disable any API keys you do not recognize and contact the site operator immediately."
	case "factor_changed":
		if n.language == "zh" {
			return "如果不是您本人操作，请立即联系站点管理员。"
		}
		return "If this was not you, contact the site operator immediately."
	case "site_payment_changed":
		if n.language == "zh" {
			return "如果不是您本人操作，请立即联系平台运营。"
		}
		return "If you did not make this change, contact the platform operator immediately."
	}
	return ""
}

// MaintainUserNotices reconciles webhook receipts and retention once per evaluation run.
func MaintainUserNotices(ctx context.Context, tx pg.Tx) error {
	// A rescue/cancellation can bypass the worker's last attempt; do not retain a queued receipt forever.
	rows, err := tx.Query(ctx, `SELECT id FROM accounts.user_notices WHERE dispatched AND webhook_state='queued' ORDER BY id LIMIT 100`)
	if err != nil {
		return err
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		pending, e := pg.HasPendingJob(ctx, tx, "accounts.notice_webhook", noticeArgs{ID: id})
		if e != nil {
			return e
		}
		if !pending {
			if _, err = tx.Exec(ctx, `UPDATE accounts.user_notices SET webhook_state='failed' WHERE id=$1 AND webhook_state='queued'`, id); err != nil {
				return err
			}
		}
	}
	if _, err = tx.Exec(ctx, `DELETE FROM accounts.user_notices WHERE id IN (SELECT id FROM accounts.user_notices WHERE dispatched AND webhook_state<>'queued' AND at<statement_timestamp()-interval '30 days' ORDER BY at LIMIT 100)`); err != nil {
		return err
	}
	// Abandoned sign-in ceremonies expire in minutes; deleting them here keeps the flow tables small without a job of
	// their own.
	_, err = New().DeleteExpiredAuthFlows(ctx, tx)
	return err
}

func (w *noticeWorker) work(ctx context.Context, job *river.Job[noticeArgs]) error {
	var n userNotice
	err := w.db.ReadTx(ctx, w.scope, func(tx pg.Tx) error {
		return tx.QueryRow(ctx, `SELECT n.id,n.kind,n.value::text,n.at,n.webhook_url,s.currency FROM accounts.user_notices n JOIN accounts.sites s ON s.id=n.site_id WHERE n.id=$1 AND n.webhook_state='queued'`, job.Args.ID).Scan(&n.ID, &n.Kind, &n.Value, &n.At, &n.url, &n.Currency)
	})
	if errors.Is(err, pg.ErrNoRows) {
		return nil
	}
	if err != nil {
		return errors.New("accounts: cannot load notice")
	}
	if err = w.send(ctx, n); err != nil && job.Attempt < job.MaxAttempts {
		return errors.New("accounts: webhook delivery failed")
	}
	state := "delivered"
	if err != nil {
		state = "failed"
	}
	if err = w.db.Tx(ctx, w.scope, func(tx pg.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE accounts.user_notices SET webhook_state=$2 WHERE id=$1 AND webhook_state='queued'`, n.ID, state)
		return e
	}); err != nil {
		return errors.New("accounts: cannot persist webhook result")
	}
	return nil
}

func (w *noticeWorker) send(ctx context.Context, n userNotice) error {
	if _, err := ValidateNotificationWebhookURL(n.url); err != nil {
		return err
	}
	payload, err := jsonx.MarshalCanonical(n)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.exit.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck // A successful response header acknowledges delivery; no response body is used.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("accounts: webhook status %s", strconv.Itoa(resp.StatusCode))
	}
	return nil
}
