package email

import (
	"context"
	"crypto/rand"
	"log/slog"
	"time"

	"github.com/nickwhiteley/plinth/code"
	"github.com/nickwhiteley/plinth/ids"
)

// Status is how a send ended.
type Status string

const (
	StatusQueued Status = "queued"
	StatusSent   Status = "sent"
	StatusFailed Status = "failed"
)

// Communication is a row of communication_log: one message, recorded before it is sent.
type Communication struct {
	ID        ids.UUID
	Kind      Kind
	Category  Category
	AccountID *ids.UUID
	Recipient string
	Subject   string
	// Body is the text sent, or empty with BodyWithheld for a kind that carries a credential.
	Body         string
	BodyWithheld bool
	RelatedType  string
	RelatedID    string

	Status            Status
	Provider          string
	ProviderMessageID string
	Error             string

	CreatedAt time.Time
	SentAt    *time.Time
}

// Outcome is how a recorded send ended.
type Outcome struct {
	Status            Status
	ProviderMessageID string
	Error             string
	At                time.Time
}

// Query filters the communication log. A zero field doesn't filter.
type Query struct {
	Category  Category
	Kind      Kind
	Status    Status
	AccountID *ids.UUID
	// Before pages: only records whose id sorts before it. Ids are UUIDv7, so newest first and
	// the last id of a page is the next page's cursor.
	Before *ids.UUID
	// Limit caps the page at PageSize.
	Limit int
}

// PageSize is the default and largest page.
const PageSize = 100

// ErrTooRecent is a prune whose cutoff is younger than MinRetention: the application may prune
// what has expired, never what is recent.
var ErrTooRecent = code.New("email.too_recent")

// Store holds the communication log (spec.md §3).
type Store interface {
	Record(ctx context.Context, c Communication) error
	// Finish records how a send ended. ErrNotFound for an unknown record.
	Finish(ctx context.Context, id ids.UUID, o Outcome) error
	// Communications returns records newest first.
	Communications(ctx context.Context, q Query) ([]Communication, error)
	// Prune deletes one category's records created before the cutoff, and returns how many.
	// ErrTooRecent if the cutoff is younger than MinRetention.
	Prune(ctx context.Context, c Category, before time.Time) (int, error)
}

// ErrNotFound is a record that doesn't exist.
var ErrNotFound = code.New("email.not_found")

// Logged records every message before sending it, and how the send ended.
//
// Recording never stops a send: a reset link that doesn't arrive because the log table is down
// is worse than a gap in the log, which is logged loudly instead.
type Logged struct {
	Next  Sender
	Kinds *Registry
	Store Store // nil: enforce kinds, record nothing
	Log   *slog.Logger
	Now   func() time.Time
}

var _ Sender = Logged{}

func (l Logged) Unwrap() Sender { return l.Next }

func (l Logged) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func (l Logged) Send(ctx context.Context, msg Message) error {
	spec, ok := l.Kinds.Lookup(msg.Kind)
	if !ok {
		return ErrUnknownKind.With("kind", string(msg.Kind))
	}
	if msg.Broadcast != (spec.Category == CategoryMarketing) {
		return ErrBroadcastMismatch.With("kind", string(msg.Kind))
	}
	now := l.now()
	row := Communication{ID: ids.NewAt(now, rand.Reader), Kind: msg.Kind, Category: spec.Category, AccountID: msg.AccountID,
		Recipient: msg.To, Subject: msg.Subject, RelatedType: msg.RelatedType, RelatedID: msg.RelatedID,
		Status: StatusQueued, Provider: providerOf(l.Next), CreatedAt: now}
	if spec.CarriesCredential {
		row.BodyWithheld = true
	} else if row.Body = msg.TextBody; row.Body == "" {
		row.Body = msg.HTMLBody
	}
	recorded := false
	if l.Store != nil {
		if err := l.Store.Record(ctx, row); err != nil {
			l.failed(spec, msg.Kind, "recording an email before sending it", err)
		} else {
			recorded = true
		}
	}
	var providerID string
	var sendErr error
	if withID, ok := l.Next.(IDSender); ok {
		providerID, sendErr = withID.SendWithID(ctx, msg)
	} else {
		sendErr = l.Next.Send(ctx, msg)
	}
	if recorded {
		out := Outcome{Status: StatusSent, ProviderMessageID: providerID, At: l.now()}
		if sendErr != nil {
			out.Status, out.Error = StatusFailed, sendErr.Error()
		}
		if err := l.Store.Finish(ctx, row.ID, out); err != nil {
			l.failed(spec, msg.Kind, "recording how an email send ended", err)
		}
	}
	return sendErr
}

// failed logs a recording failure: an error (so it alerts) unless the mail was itself an alert,
// which would otherwise feed itself.
func (l Logged) failed(spec KindSpec, kind Kind, what string, err error) {
	if l.Log == nil {
		return
	}
	if spec.Category == CategoryInternal {
		l.Log.Warn(what, "kind", kind, "err", err)
		return
	}
	l.Log.Error(what, "kind", kind, "err", err)
}

func providerOf(s Sender) string {
	if p, ok := s.(Provider); ok {
		return p.Provider()
	}
	return "unknown"
}
