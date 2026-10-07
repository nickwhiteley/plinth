package alert

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type capture struct {
	mu   sync.Mutex
	sent []string
	err  error
}

func (c *capture) Deliver(_ context.Context, subject, body string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, subject+"\n"+body)
	return c.err
}

func (c *capture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.sent)
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(nopWriter{}, nil)) }

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

// The seam is a log handler: a handler that logs an error is instrumented by
// having done nothing.
func TestErrorsLoggedBecomeAlerts(t *testing.T) {
	sink := &capture{}
	r := NewReporter(sink, DefaultBudget(20), "Test", quiet())
	log := slog.New(NewHandler(slog.NewTextHandler(nopWriter{}, nil), r))

	log.Info("a request", "path", "/api/x")
	log.Warn("something odd", "path", "/api/x")
	log.Error("saving head", "err", "upstream unavailable", "user", "usr_1")
	r.Stop()

	if sink.count() != 1 {
		t.Fatalf("sent %d alerts, want only the error", sink.count())
	}
	got := sink.sent[0]
	// The attributes travel with it, or the alert names no route.
	if !strings.Contains(got, "usr_1") || !strings.Contains(got, "saving head") {
		t.Errorf("alert lost its context:\n%s", got)
	}
}

// Attributes attached with With() are part of the event too — that is how the
// request logger carries the path.
func TestHandlerCarriesWithAttrs(t *testing.T) {
	sink := &capture{}
	r := NewReporter(sink, DefaultBudget(20), "Test", quiet())
	log := slog.New(NewHandler(slog.NewTextHandler(nopWriter{}, nil), r)).With("component", "billing")

	log.Error("provider refused")
	r.Stop()

	if sink.count() != 1 || !strings.Contains(sink.sent[0], "billing") {
		t.Errorf("With attributes did not reach the alert: %v", sink.sent)
	}
}

// Four hundred copies of one failure is one message.
func TestOneFaultRepeatingIsOneAlert(t *testing.T) {
	sink := &capture{}
	r := NewReporter(sink, DefaultBudget(20), "Test", quiet())
	for i := 0; i < 400; i++ {
		r.Report(Event{Kind: KindServer, Message: "project prj_2552tq2rmyl44b7m not found"})
	}
	r.Stop()

	if sink.count() != 1 {
		t.Errorf("sent %d alerts for one recurring fault, want 1", sink.count())
	}
}

// Redaction happens before anything is sent, not at the call site.
func TestNothingSentCarriesACredential(t *testing.T) {
	sink := &capture{}
	r := NewReporter(sink, DefaultBudget(20), "Test", quiet())
	r.Report(Event{Kind: KindServer, Message: "login failed for nick@example.com",
		Fields: map[string]string{"password": "hunter2", "token": "sess_x"}})
	r.Stop()

	for _, forbidden := range []string{"hunter2", "sess_x", "nick@example.com"} {
		if strings.Contains(sink.sent[0], forbidden) {
			t.Errorf("an alert carried %q", forbidden)
		}
	}
}

// A monitoring system that slows the thing it monitors is a second outage, so
// a full queue drops and counts rather than blocking the caller.
func TestAFullQueueDropsRatherThanBlocking(t *testing.T) {
	blocked := make(chan struct{})
	slow := sinkFunc(func(context.Context, string, string) error {
		<-blocked
		return nil
	})
	r := NewReporter(slow, DefaultBudget(1000), "Test", quiet())

	done := make(chan struct{})
	go func() {
		for i := 0; i < QueueDepth*4; i++ {
			r.Report(Event{Kind: KindServer, Message: "fault " + string(rune('a'+i%26))})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Report blocked on a full queue")
	}
	if r.Dropped() == 0 {
		t.Error("nothing was dropped, so the queue is unbounded")
	}
	close(blocked)
	r.Stop()
}

// An alert that fails to send must not become an error that queues another
// alert — the loop that turns one outage into a mailbox full of them.
func TestASendFailureDoesNotFeedItself(t *testing.T) {
	sink := &capture{err: context.DeadlineExceeded}
	r := NewReporter(sink, DefaultBudget(20), "Test", quiet())
	r.Report(Event{Kind: KindServer, Message: "upstream unavailable"})
	r.Stop()

	if sink.count() != 1 {
		t.Errorf("attempted %d sends, want exactly one", sink.count())
	}
}

type sinkFunc func(context.Context, string, string) error

func (f sinkFunc) Deliver(ctx context.Context, s, b string) error { return f(ctx, s, b) }
