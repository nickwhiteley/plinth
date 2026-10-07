// Package alert decides what to report, how often, and with what removed.
//
// Pure: no context, no store, no *http.Request. The sending lives in the shell
// beside the mailer, and this package is the part worth testing — which is why
// it runs in about twenty milliseconds (CLAUDE.md, *Where decisions live*).
//
// # Why there is a budget at all
//
// Today the first person to know a paid product is down is a paying customer,
// and an error in a handler scrolls past in a log nobody is reading
// (backlog E4). The fix is to send somebody a message — and the failure mode
// of sending messages is that four hundred copies of one failure is
// indistinguishable from no alerting at all. So Fingerprint groups a failure,
// Due rations it, and Redact decides what may leave the process.
package alert

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Level is the severity of an event, in slog's vocabulary.
type Level string

const (
	LevelError Level = "ERROR"
	LevelWarn  Level = "WARN"
)

// Kind separates the things that arrive here, for the subject line and for the
// report. They are treated identically otherwise.
type Kind string

const (
	// KindServer is an error logged by the API. A product defines its own kinds
	// beside it (a sheet that failed to print in somebody's browser, say), and
	// they're named in the subject line.
	KindServer Kind = "server"
)

// Event is one thing worth telling somebody about.
type Event struct {
	Kind    Kind
	Level   Level
	Message string
	At      time.Time
	// Fields are the structured pairs the log carried, already stringified.
	Fields map[string]string
}

// ---------------------------------------------------------------- grouping

var (
	// Anything that identifies one occurrence rather than one fault: ids,
	// UUIDs, hex, numbers, quoted strings and addresses. Replaced rather than
	// removed, so "project X not found" and "project Y not found" collapse to
	// one fingerprint while staying legible.
	idish = regexp.MustCompile(`\b[a-z]{3}_[0-9a-z]{6,}\b`)
	uuid  = regexp.MustCompile(`\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	hex   = regexp.MustCompile(`\b[0-9a-f]{12,}\b`)
	// A namespaced record or kind id: plant:rosa-gallica, builtin:rowan.
	namespaced = regexp.MustCompile(`\b[a-z]+:[a-z0-9][a-z0-9:-]*\b`)
	quoted     = regexp.MustCompile(`"[^"]*"`)
	numbers    = regexp.MustCompile(`\b\d+\b`)
	addresses  = regexp.MustCompile(`\b[^\s@]+@[^\s@]+\.[^\s@]+\b`)
	spaces     = regexp.MustCompile(`\s+`)
)

// Fingerprint groups the same failure recurring.
//
// A 500 in a hot path is one email, not four hundred — and the grouping has to
// be about *this* codebase's error strings, which is the reason it is written
// here rather than delegated to a product that groups by stack frame. A Go
// HTTP handler's frames collapse to the same three for unrelated failures.
func Fingerprint(e Event) string {
	s := strings.ToLower(e.Message)
	for _, re := range []*regexp.Regexp{addresses, uuid, idish, namespaced, hex, quoted, numbers} {
		s = re.ReplaceAllString(s, "*")
	}
	s = strings.TrimSpace(spaces.ReplaceAllString(s, " "))
	return string(e.Kind) + ":" + s
}

// ---------------------------------------------------------------- redaction

// Never lists the field names that may not leave the process, whatever they
// hold. A function rather than a convention, because a convention loses.
var Never = map[string]bool{
	"password": true, "pass": true, "secret": true, "token": true,
	"authorization": true, "cookie": true, "session": true, "sid": true,
	"card": true, "pan": true, "cvv": true, "body": true, "document": true,
	// An address is not needed to find an account — the user id is — and an
	// alert mailbox is not a place personal data should accumulate.
	"email": true, "address": true, "to": true, "from": true,
}

const redacted = "[redacted]"

// Redact removes what may never be in an alert: credentials, session material,
// card data the system never holds anyway, request bodies, and email
// addresses. A user id is enough to find the account.
func Redact(e Event) Event {
	out := e
	out.Message = addresses.ReplaceAllString(e.Message, redacted)
	if len(e.Fields) == 0 {
		return out
	}
	fields := make(map[string]string, len(e.Fields))
	for k, v := range e.Fields {
		if Never[strings.ToLower(k)] {
			fields[k] = redacted
			continue
		}
		fields[k] = addresses.ReplaceAllString(v, redacted)
	}
	out.Fields = fields
	return out
}

// ------------------------------------------------------------------ budget

// Budget is how much may be sent.
type Budget struct {
	// PerFingerprint is how long one fault stays quiet after being reported.
	PerFingerprint time.Duration
	// Daily is the ceiling on messages in one day. A mailbox with 900 alerts
	// is indistinguishable from no alerting, so the ceiling exists to make
	// that failure loud rather than silent.
	Daily int
}

// DefaultBudget is one message per fault per hour, twenty a day.
func DefaultBudget(daily int) Budget {
	if daily <= 0 {
		daily = 20
	}
	return Budget{PerFingerprint: time.Hour, Daily: daily}
}

// Ledger is what has been sent. Plain data, so Due is a function of it: the
// concurrency belongs to whoever holds the ledger, not to the decision.
type Ledger struct {
	// Sent maps fingerprint to when it was last reported.
	Sent map[string]time.Time
	// Day is the date the count below belongs to.
	Day time.Time
	// Count is how many messages have been sent on that date.
	Count int
	// Suppressed counts distinct fingerprints refused since the ceiling was
	// reached, so the last message of the day can say what is being hidden.
	Suppressed map[string]bool
}

// Decision is what to do with one event.
type Decision int

const (
	// Send it.
	Send Decision = iota
	// Hold it: this fault was reported recently.
	Hold
	// Ceiling: the day's budget is spent, and this is being counted instead.
	Ceiling
	// Last is Send, and this message must also say how much is being
	// suppressed — it is the one that takes the budget to its limit.
	Last
)

// Due answers whether an event may be sent now, given what has been sent.
//
// Pure, and the ledger is not mutated: Record does that, so a caller can ask
// without committing.
func Due(e Event, l Ledger, now time.Time, b Budget) Decision {
	if sameDay(l.Day, now) && l.Count >= b.Daily {
		return Ceiling
	}
	if last, seen := l.Sent[Fingerprint(e)]; seen && now.Sub(last) < b.PerFingerprint {
		return Hold
	}
	if sameDay(l.Day, now) && l.Count+1 == b.Daily {
		return Last
	}
	return Send
}

// Record folds a decision back into the ledger.
func Record(e Event, l Ledger, now time.Time, d Decision) Ledger {
	if l.Sent == nil {
		l.Sent = map[string]time.Time{}
	}
	if l.Suppressed == nil {
		l.Suppressed = map[string]bool{}
	}
	if !sameDay(l.Day, now) {
		l.Day, l.Count, l.Suppressed = now, 0, map[string]bool{}
	}
	switch d {
	case Send, Last:
		l.Sent[Fingerprint(e)] = now
		l.Count++
	case Ceiling:
		l.Suppressed[Fingerprint(e)] = true
	}
	return l
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.UTC().Date()
	by, bm, bd := b.UTC().Date()
	return ay == by && am == bm && ad == bd
}

// ------------------------------------------------------------------ message

// Subject is the one line somebody reads on a phone. It names the product (its
// prefix), the kind and the fault, never an identifier.
func Subject(prefix string, e Event) string {
	message := e.Message
	if len([]rune(message)) > 80 {
		message = string([]rune(message)[:77]) + "…"
	}
	if e.Kind == "" || e.Kind == KindServer {
		return prefix + ": " + message
	}
	return prefix + " (" + string(e.Kind) + "): " + message
}

// Body renders an event as the plain text of a message, fields sorted so two
// copies of one fault read the same way.
func Body(e Event, suppressed int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n\n%s\n", e.At.UTC().Format(time.RFC3339), e.Level, e.Message)
	if len(e.Fields) > 0 {
		keys := make([]string, 0, len(e.Fields))
		for k := range e.Fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString("\n")
		for _, k := range keys {
			fmt.Fprintf(&b, "  %s: %s\n", k, e.Fields[k])
		}
	}
	if suppressed > 0 {
		fmt.Fprintf(&b, "\nThe daily alert ceiling is reached. %d further distinct faults "+
			"are being counted rather than sent; the log has them all.\n", suppressed)
	}
	return b.String()
}
