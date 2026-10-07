// Package email sends mail and records every send (spec.md §9). Lifted from Bloomprint's email
// package.
//
// Email is the only text plinth renders, and it renders it in the recipient's locale from message
// catalogues (Catalogue), the same message files a product's web app uses. Kinds are declared
// into a Registry: plinth declares its own (a reset link, an address change, an operator alert),
// and a product adds its own.
package email

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/nickwhiteley/plinth/code"
	"github.com/nickwhiteley/plinth/ids"
)

// Message is one email to one recipient, already rendered.
type Message struct {
	Kind     Kind
	To       string
	ReplyTo  string
	Subject  string
	TextBody string
	HTMLBody string
	// AccountID is the recipient's account, when there is one, for the communication log.
	AccountID *ids.UUID
	// RelatedType and RelatedID name what the message is about (a project, a subscription).
	RelatedType string
	RelatedID   string
	// Broadcast sends on the marketing stream. Only marketing kinds may, and they must.
	Broadcast bool
}

// Sender sends one message.
type Sender interface {
	Send(ctx context.Context, msg Message) error
}

// IDSender also returns the provider's id for the message.
type IDSender interface {
	SendWithID(ctx context.Context, msg Message) (string, error)
}

// Provider names a sender in the communication log.
type Provider interface{ Provider() string }

// NoOp discards every message. It is what a deployment with no mail configured runs, and Discards
// reports it so a product can log reset links instead of losing them.
type NoOp struct{}

func (NoOp) Send(context.Context, Message) error { return nil }
func (NoOp) Provider() string                    { return "noop" }

// Discards reports whether s, looking through wrappers, throws mail away.
func Discards(s Sender) bool {
	for {
		switch t := s.(type) {
		case NoOp:
			return true
		case interface{ Unwrap() Sender }:
			s = t.Unwrap()
		default:
			return false
		}
	}
}

var (
	// ErrInvalidAddress is an empty address, one with line breaks or NULs (a header injection),
	// or one without an @. It carries "field".
	ErrInvalidAddress = code.New("email.invalid_address")
	// ErrInvalidSubject is a subject with line breaks or NULs.
	ErrInvalidSubject = code.New("email.invalid_subject")
	// ErrUnknownKind is a kind no declaration names.
	ErrUnknownKind = code.New("email.unknown_kind")
	// ErrBroadcastMismatch is a marketing kind sent off the broadcast stream, or another kind on it.
	ErrBroadcastMismatch = code.New("email.broadcast_mismatch")
	// ErrUnconfigured is a provider missing a setting. It carries "missing".
	ErrUnconfigured = code.New("email.unconfigured")
	// ErrNoBroadcastStream is marketing mail with no broadcast stream configured.
	ErrNoBroadcastStream = code.New("email.no_broadcast_stream")
	// ErrProviderFailed is a provider that refused or failed the send. The detail is wrapped
	// around it for the log.
	ErrProviderFailed = code.New("email.provider_failed")
)

// ValidateAddress checks an address for what makes it dangerous, not for RFC 5322.
func ValidateAddress(field, addr string) error {
	if addr == "" || strings.ContainsAny(addr, "\r\n\x00") || !strings.Contains(addr, "@") {
		return ErrInvalidAddress.With("field", field)
	}
	return nil
}

// ValidateSubject refuses a subject that could inject a header.
func ValidateSubject(subject string) error {
	if strings.ContainsAny(subject, "\r\n\x00") {
		return ErrInvalidSubject
	}
	return nil
}

// Kind is what a message is, which decides its category.
type Kind string

// Category decides a message's retention, and whether it may be broadcast.
type Category string

const (
	// CategoryRegulatory is mail the law says must be kept: receipts, cancellations.
	CategoryRegulatory Category = "regulatory"
	// CategoryOperational is mail about the account: resets, shares, failed payments.
	CategoryOperational Category = "operational"
	// CategoryMarketing is broadcast mail, sent on its own stream.
	CategoryMarketing Category = "marketing"
	// CategoryInternal is mail to the operators.
	CategoryInternal Category = "internal"
)

// Retention is how long each category's records are kept.
var Retention = map[Category]time.Duration{
	CategoryRegulatory:  6 * 365 * 24 * time.Hour,
	CategoryOperational: 400 * 24 * time.Hour,
	CategoryMarketing:   400 * 24 * time.Hour,
	CategoryInternal:    90 * 24 * time.Hour,
}

// MinRetention is the youngest a pruned record may be: the shortest retention.
const MinRetention = 90 * 24 * time.Hour

// KindSpec is a kind's declaration.
type KindSpec struct {
	Category Category
	// CarriesCredential withholds the body from the communication log: a reset link in a support
	// screen is a takeover.
	CarriesCredential bool
}

// plinth's own kinds.
const (
	KindPasswordReset  Kind = "password_reset"
	KindVerifyAddress  Kind = "verify_address"
	KindAddressChanged Kind = "address_changed"
	KindOperatorReport Kind = "operator_report"
)

// Kinds declares plinth's own kinds. A product declares them with its own.
var Kinds = map[Kind]KindSpec{
	KindPasswordReset:  {Category: CategoryOperational, CarriesCredential: true},
	KindVerifyAddress:  {Category: CategoryOperational, CarriesCredential: true},
	KindAddressChanged: {Category: CategoryOperational},
	KindOperatorReport: {Category: CategoryInternal},
}

var kindRe = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)

// Registry is the set of declared kinds.
type Registry struct{ kinds map[Kind]KindSpec }

func NewRegistry() *Registry { return &Registry{kinds: map[Kind]KindSpec{}} }

// Declare adds kinds. A kind declared twice, a malformed one, or one with an unknown category
// panics.
func (r *Registry) Declare(kinds map[Kind]KindSpec) *Registry {
	for k, spec := range kinds {
		if _, dup := r.kinds[k]; dup || !kindRe.MatchString(string(k)) {
			panic("email: kind " + string(k) + " is declared twice or malformed")
		}
		if _, ok := Retention[spec.Category]; !ok {
			panic("email: kind " + string(k) + " has an unknown category")
		}
		r.kinds[k] = spec
	}
	return r
}

func (r *Registry) Lookup(k Kind) (KindSpec, bool) {
	s, ok := r.kinds[k]
	return s, ok
}
