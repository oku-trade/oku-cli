package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Backend runs Oku commands in-process. Call enforces its own path
// allowlist; Has reports whether a command exists in this build.
type Backend interface {
	Call(ctx context.Context, path string, opts map[string]any, args []string) (any, error)
	Has(path string) bool
}

// Config configures Serve.
type Config struct {
	Token   string
	BaseURL string // Telegram Bot API origin; DefaultBaseURL when empty
	Backend Backend
	// AllowedUserIDs are the only Telegram users the bot answers.
	AllowedUserIDs []int64
	// Profile is shown in the main menu header only.
	Profile string
	// Log receives diagnostics. It never receives the token, message text
	// or user IDs.
	Log io.Writer
	Now func() time.Time
}

const (
	readTimeout    = 30 * time.Second
	slowTimeout    = 2 * time.Minute
	prepareTimeout = 2 * time.Minute
	// submitTimeout covers onchain execution, which the core bounds at ten
	// minutes independent of the per-request timeout.
	submitTimeout = 12 * time.Minute
	maxBackoff    = 30 * time.Second
	queueDepth    = 16
)

type awaiting struct {
	kind    string
	expires time.Time
}

type bot struct {
	api     *Client
	be      Backend
	allowed map[int64]bool
	profile string
	log     io.Writer
	now     func() time.Time

	mu      sync.Mutex
	pending map[int64]*pendingAction
	await   map[int64]awaiting
	queues  map[int64]chan Update
	wg      sync.WaitGroup
}

// ErrNoAllowlist is returned when no user is allowed to use the bot.
var ErrNoAllowlist = errors.New("telegram: no allowed user IDs configured")

// ErrNoToken is returned when no bot token is configured.
var ErrNoToken = errors.New("telegram: no bot token configured")

func newBot(cfg Config) *bot {
	allowed := map[int64]bool{}
	for _, id := range cfg.AllowedUserIDs {
		if id > 0 {
			allowed[id] = true
		}
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	log := cfg.Log
	if log == nil {
		log = io.Discard
	}
	return &bot{
		api: NewClient(cfg.BaseURL, cfg.Token), be: cfg.Backend, allowed: allowed, profile: cfg.Profile,
		log: log, now: now,
		pending: map[int64]*pendingAction{}, await: map[int64]awaiting{}, queues: map[int64]chan Update{},
	}
}

// Serve long-polls Telegram until ctx is done. It refuses to start without a
// token or an allowlist, before any network call.
func Serve(ctx context.Context, cfg Config) error {
	if cfg.Token == "" {
		return ErrNoToken
	}
	if cfg.Backend == nil {
		return errors.New("telegram: no backend")
	}
	b := newBot(cfg)
	if len(b.allowed) == 0 {
		return ErrNoAllowlist
	}
	return b.run(ctx)
}

func (b *bot) logf(format string, a ...any) {
	fmt.Fprintf(b.log, format+"\n", a...)
}

// fatalPollError reports Bot API failures that retrying cannot fix.
func fatalPollError(err error) error {
	var ae *APIError
	if !errors.As(err, &ae) {
		return nil
	}
	switch ae.Status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return errors.New("telegram rejected the bot token (check OKU_TELEGRAM_TOKEN or telegram.token)")
	case http.StatusConflict:
		return errors.New("telegram reports another consumer for this bot (a webhook or another 'oku telegram serve')")
	}
	return nil
}

func (b *bot) run(ctx context.Context) error {
	defer b.wg.Wait()
	backoff := time.Second
	var offset int64
	drained := false
	for {
		if ctx.Err() != nil {
			return nil
		}
		var ups []Update
		var err error
		if !drained {
			// Skip updates queued while the bot was down so stale taps are
			// never replayed. offset -1 returns only the newest one.
			ups, err = b.api.GetUpdates(ctx, -1, 0)
		} else {
			ups, err = b.api.GetUpdates(ctx, offset, pollTimeout)
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if fatal := fatalPollError(err); fatal != nil {
				return fatal
			}
			wait := backoff
			var ae *APIError
			if errors.As(err, &ae) && ae.RetryAfter > 0 {
				wait = ae.RetryAfter
			}
			b.logf("oku telegram: getUpdates failed (%s); retrying in %s", errorCode(err), wait)
			if sleepCtx(ctx, wait) != nil {
				return nil
			}
			backoff = min(backoff*2, maxBackoff)
			continue
		}
		backoff = time.Second
		if !drained {
			drained = true
			if n := len(ups); n > 0 {
				offset = ups[n-1].UpdateID + 1
				b.logf("oku telegram: skipped pending updates from before startup")
			}
			continue
		}
		for _, u := range ups {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			b.dispatch(ctx, u)
		}
	}
}

// authorize returns the sender of an update from an allowlisted user in
// their own private chat.
func (b *bot) authorize(u Update) (int64, bool) {
	var from int64
	var chat Chat
	switch {
	case u.CallbackQuery != nil:
		if u.CallbackQuery.Message == nil {
			return 0, false
		}
		from, chat = u.CallbackQuery.From.ID, u.CallbackQuery.Message.Chat
	case u.Message != nil:
		if u.Message.From == nil {
			return 0, false
		}
		from, chat = u.Message.From.ID, u.Message.Chat
	default:
		return 0, false
	}
	if from <= 0 || !b.allowed[from] || chat.Type != "private" || chat.ID != from {
		return 0, false
	}
	return from, true
}

// dispatch queues an authorized update on its user's serial queue. Updates
// from anyone else get no reply and leave no trace of their content.
func (b *bot) dispatch(ctx context.Context, u Update) {
	user, ok := b.authorize(u)
	if !ok {
		b.logf("oku telegram: ignored an update from a user or chat that is not allowed")
		return
	}
	if cq := u.CallbackQuery; cq != nil {
		b.wg.Add(1)
		go func() {
			defer b.wg.Done()
			actx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if err := b.api.AnswerCallbackQuery(actx, cq.ID); err != nil {
				b.logf("oku telegram: answerCallbackQuery failed (%s)", errorCode(err))
			}
		}()
	}
	b.mu.Lock()
	q := b.queues[user]
	if q == nil {
		q = make(chan Update, queueDepth)
		b.queues[user] = q
		b.wg.Add(1)
		go b.worker(ctx, q)
	}
	b.mu.Unlock()
	select {
	case q <- u:
	default:
		b.logf("oku telegram: dropped an update because the user's queue is full")
	}
}

func (b *bot) worker(ctx context.Context, q chan Update) {
	defer b.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case u := <-q:
			b.handle(ctx, u)
		}
	}
}

// handle processes one authorized update. A panic is logged without
// details and answered with a generic message.
func (b *bot) handle(ctx context.Context, u Update) {
	var chat int64
	if u.CallbackQuery != nil && u.CallbackQuery.Message != nil {
		chat = u.CallbackQuery.Message.Chat.ID
	} else if u.Message != nil {
		chat = u.Message.Chat.ID
	}
	defer func() {
		if r := recover(); r != nil {
			b.logf("oku telegram: recovered from a panic while handling an update")
			if chat != 0 {
				b.send(ctx, chat, view{text: genericError, kb: mainOnly()})
			}
		}
	}()
	if u.CallbackQuery != nil {
		b.onCallback(ctx, u.CallbackQuery)
		return
	}
	if u.Message != nil {
		b.onMessage(ctx, u.Message)
	}
}

type view struct {
	text string
	kb   *Keyboard
}

func (b *bot) edit(ctx context.Context, chat, msg int64, v view) {
	if err := b.api.EditMessageText(ctx, chat, msg, v.text, v.kb); err != nil {
		b.logf("oku telegram: editMessageText failed (%s)", errorCode(err))
	}
}

func (b *bot) send(ctx context.Context, chat int64, v view) int64 {
	m, err := b.api.SendMessage(ctx, chat, v.text, v.kb)
	if err != nil {
		b.logf("oku telegram: sendMessage failed (%s)", errorCode(err))
		return 0
	}
	return m.MessageID
}

func (b *bot) call(ctx context.Context, timeout time.Duration, path string, opts map[string]any) (any, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if opts == nil {
		opts = map[string]any{}
	}
	res, err := b.be.Call(cctx, path, opts, nil)
	if err != nil {
		b.logf("oku telegram: %s failed (%s)", path, errorCode(err))
	}
	return res, err
}

func (b *bot) onCallback(ctx context.Context, cq *CallbackQuery) {
	user, chat, msg := cq.From.ID, cq.Message.Chat.ID, cq.Message.MessageID
	data := cq.Data
	b.clearAwait(user)
	switch {
	case strings.HasPrefix(data, "c:"):
		b.confirm(ctx, user, chat, msg, strings.TrimPrefix(data, "c:"))
	case strings.HasPrefix(data, "x:"):
		b.cancel(ctx, user, chat, msg, strings.TrimPrefix(data, "x:"))
	case strings.HasPrefix(data, "m:"):
		b.edit(ctx, chat, msg, b.screen(ctx, user, strings.TrimPrefix(data, "m:")))
	case strings.HasPrefix(data, "po:"):
		b.startPerpsCancel(ctx, user, chat, msg, strings.TrimPrefix(data, "po:"))
	case strings.HasPrefix(data, "so:"):
		b.startSwapOrderCancel(ctx, user, chat, msg, strings.TrimPrefix(data, "so:"))
	case data == "cl":
		b.startClaim(ctx, user, chat, msg)
	default:
		b.edit(ctx, chat, msg, b.screen(ctx, user, "main"))
	}
}

func (b *bot) setAwait(user int64, kind string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.pending, user)
	b.await[user] = awaiting{kind: kind, expires: b.now().Add(inputTTL)}
}

func (b *bot) clearAwait(user int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.await, user)
}

func (b *bot) awaitKind(user int64) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	a, ok := b.await[user]
	if !ok {
		return ""
	}
	if !b.now().Before(a.expires) {
		delete(b.await, user)
		return ""
	}
	return a.kind
}

func (b *bot) onMessage(ctx context.Context, m *Message) {
	user, chat := m.From.ID, m.Chat.ID
	text := strings.TrimSpace(m.Text)
	kind := b.awaitKind(user)
	if kind == "" || strings.HasPrefix(text, "/") {
		b.clearAwait(user)
		b.send(ctx, chat, b.screen(ctx, user, "main"))
		return
	}
	flags, err := parseInput(kind, m.Text)
	if err != nil {
		b.send(ctx, chat, view{text: "Could not read that: " + err.Error() + ".\n\n" + inputUsage[kind] + "\n\nSend the line again, or go back.", kb: kb(row(btn("Back", "m:main")))})
		return
	}
	b.clearAwait(user)
	b.prepareFlow(ctx, user, chat, kind, flags)
}
