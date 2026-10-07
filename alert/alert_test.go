package alert

import (
	"strings"
	"testing"
	"time"
)

func at(hour int) time.Time { return time.Date(2026, 9, 22, hour, 0, 0, 0, time.UTC) }

// The grouping has to be about this codebase's error strings: one fault
// recurring four hundred times is one message.
func TestFingerprintGroupsOneFaultAcrossOccurrences(t *testing.T) {
	a := Event{Kind: KindServer, Message: `project prj_2552tq2rmyl44b7m not found`}
	b := Event{Kind: KindServer, Message: `project prj_9xk1pp8bqqz00c3d not found`}
	if Fingerprint(a) != Fingerprint(b) {
		t.Errorf("two occurrences of one fault fingerprinted apart:\n  %q\n  %q",
			Fingerprint(a), Fingerprint(b))
	}

	// And two different faults must not collapse into one, which is the
	// failure that makes an alert mailbox useless in the other direction.
	c := Event{Kind: KindServer, Message: `saving head: revision conflict`}
	if Fingerprint(a) == Fingerprint(c) {
		t.Error("two different faults share a fingerprint")
	}
	// A print failure and a server error are never the same thing.
	if Fingerprint(a) == Fingerprint(Event{Kind: Kind("print"), Message: a.Message}) {
		t.Error("kind is not part of the fingerprint")
	}
}

func TestFingerprintIgnoresIdentifiers(t *testing.T) {
	for _, pair := range [][2]string{
		{"record plant:rosa-gallica is deprecated", "record plant:abies-alba is deprecated"},
		{"user usr_abc123def456 failed", "user usr_zzz999yyy888 failed"},
		{`kind "builtin:rowan" unknown`, `kind "flora:abies-alba" unknown`},
		{"upstream answered 502", "upstream answered 503"},
		{"could not mail nick@example.com", "could not mail someone@elsewhere.org"},
	} {
		a := Event{Kind: KindServer, Message: pair[0]}
		b := Event{Kind: KindServer, Message: pair[1]}
		if Fingerprint(a) != Fingerprint(b) {
			t.Errorf("%q and %q fingerprinted apart", pair[0], pair[1])
		}
	}
}

// What may never be in an alert. A table of inputs that must not appear in the
// output is the shape that catches the next field somebody adds.
func TestRedactRemovesWhatMayNeverLeave(t *testing.T) {
	e := Event{
		Kind:    KindServer,
		Message: "login failed for nick@example.com",
		Fields: map[string]string{
			"password":      "hunter2",
			"token":         "sess_abcdef",
			"Authorization": "Bearer xyz",
			"cookie":        "sid=abc",
			"email":         "nick@example.com",
			"body":          `{"design":"…"}`,
			"user":          "usr_abc123",
			"path":          "/api/projects/prj_1",
		},
	}
	got := Redact(e)

	for _, forbidden := range []string{"hunter2", "sess_abcdef", "Bearer xyz", "sid=abc",
		"nick@example.com", `{"design"`} {
		if strings.Contains(got.Message, forbidden) {
			t.Errorf("message leaked %q", forbidden)
		}
		for k, v := range got.Fields {
			if strings.Contains(v, forbidden) {
				t.Errorf("field %s leaked %q", k, forbidden)
			}
		}
	}

	// And what must survive, because without it an alert names no account and
	// no route, which is an alert nobody can act on.
	if got.Fields["user"] != "usr_abc123" || got.Fields["path"] != "/api/projects/prj_1" {
		t.Errorf("redaction took the actionable fields too: %v", got.Fields)
	}
	// The original is untouched: Redact returns a copy.
	if e.Fields["password"] != "hunter2" {
		t.Error("Redact mutated its argument")
	}
}

func TestDueRationsOneFaultPerHour(t *testing.T) {
	b := DefaultBudget(20)
	e := Event{Kind: KindServer, Message: "upstream unavailable"}
	l := Ledger{}

	if got := Due(e, l, at(9), b); got != Send {
		t.Fatalf("first occurrence = %v, want Send", got)
	}
	l = Record(e, l, at(9), Send)

	if got := Due(e, l, at(9).Add(30*time.Minute), b); got != Hold {
		t.Errorf("same fault half an hour later = %v, want Hold", got)
	}
	if got := Due(e, l, at(10).Add(time.Minute), b); got != Send {
		t.Errorf("same fault an hour later = %v, want Send", got)
	}
	// A different fault is never held behind another one.
	other := Event{Kind: KindServer, Message: "saving head: revision conflict"}
	if got := Due(other, l, at(9).Add(time.Minute), b); got != Send {
		t.Errorf("a different fault = %v, want Send", got)
	}
}

// The ceiling exists to make flooding loud rather than silent: the last
// message says what is being hidden, and the rest are counted.
func TestDueStopsAtTheDailyCeilingAndSaysSo(t *testing.T) {
	b := Budget{PerFingerprint: time.Hour, Daily: 3}
	l := Ledger{Day: at(0)}

	var decisions []Decision
	for i := range 5 {
		e := Event{Kind: KindServer, Message: "fault number " + string(rune('a'+i))}
		d := Due(e, l, at(9), b)
		decisions = append(decisions, d)
		l = Record(e, l, at(9), d)
	}

	want := []Decision{Send, Send, Last, Ceiling, Ceiling}
	for i := range want {
		if decisions[i] != want[i] {
			t.Errorf("message %d = %v, want %v (all: %v)", i, decisions[i], want[i], decisions)
		}
	}
	if len(l.Suppressed) != 2 {
		t.Errorf("suppressed %d distinct faults, want 2", len(l.Suppressed))
	}

	// A new day starts the budget again, rather than staying silent for ever.
	if got := Due(Event{Kind: KindServer, Message: "fault number a"}, l, at(9).Add(24*time.Hour), b); got != Send {
		t.Errorf("the next day = %v, want Send", got)
	}
}

func TestBodyNamesTheSuppressionOnlyWhenThereIsSome(t *testing.T) {
	e := Event{Kind: KindServer, Level: LevelError, Message: "upstream unavailable",
		At: at(9), Fields: map[string]string{"path": "/api/x", "user": "usr_1"}}

	plain := Body(e, 0)
	if strings.Contains(plain, "ceiling") {
		t.Error("an ordinary alert mentions the ceiling")
	}
	// Fields sorted, so two copies of one fault read the same way.
	if strings.Index(plain, "path:") > strings.Index(plain, "user:") {
		t.Error("fields are not sorted")
	}

	if !strings.Contains(Body(e, 7), "7 further distinct faults") {
		t.Error("the last message of the day does not say what is being hidden")
	}
}

func TestSubjectNamesTheKind(t *testing.T) {
	if got := Subject("Furniture Magic", Event{Kind: Kind("print"), Message: "no glyph for ǂ"}); got != "Furniture Magic (print): no glyph for ǂ" {
		t.Errorf("print subject = %q", got)
	}
	long := strings.Repeat("x", 200)
	if got := Subject("Furniture Magic", Event{Kind: KindServer, Message: long}); len([]rune(got)) > 100 {
		t.Errorf("subject is %d runes, want it trimmed", len([]rune(got)))
	}
}

// plinth's ids are upper-case Crockford; one fault naming two of them is still one fault.
func TestFingerprintGroupsPlinthIDs(t *testing.T) {
	a := Event{Kind: KindServer, Message: "account acc_01M483M2YGE1CTRQSGT39SE6KV could not sign in"}
	b := Event{Kind: KindServer, Message: "account acc_01M483M7TRFFY9CMAFAN6P9AGX could not sign in"}
	if Fingerprint(a) != Fingerprint(b) {
		t.Errorf("%q and %q", Fingerprint(a), Fingerprint(b))
	}
}
