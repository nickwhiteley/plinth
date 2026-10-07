package alert

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// The shell around the decisions in alert.go: a slog.Handler that tees errors
// to a reporter, and a reporter that sends mail.
//
// **The seam is a log handler, not a call site.** Nothing else changes: no
// handler gains a second line, no package imports a reporting SDK, and a
// handler that logs an error today is instrumented tomorrow by having done
// nothing. The alternative — an explicit report at each interesting failure —
// is the version that goes stale, because the next error path is written by
// somebody who has not read the spec and will write s.log.Error anyway
//.

// Sink receives events that survived the budget.
//
// The mail sink arrives with package email. It's an interface so that the day email stops being enough, a Sentry-shaped
// implementation is one file and no call sites.
type Sink interface {
	Deliver(ctx context.Context, subject, body string) error
}

// Reporter rations events and hands the survivors to a Sink.
//
// Safe for concurrent use. The ledger is the only mutable state and the
// decisions about it are pure (Due, Record); this type is the mutex around
// them and the queue in front of the sink.
type Reporter struct {
	prefix string
	sink   Sink
	budget Budget
	now    func() time.Time
	log    *slog.Logger

	mu     sync.Mutex
	ledger Ledger

	// queue is bounded and dropped from rather than blocked on. A monitoring
	// system that slows the thing it monitors is a second outage.
	queue   chan Event
	dropped int64
	wg      sync.WaitGroup
	stop    chan struct{}
	once    sync.Once
}

// QueueDepth is deliberately small: an alert that is minutes stale is useless,
// so a backlog is a reason to drop rather than to buffer.
const QueueDepth = 64

// NewReporter starts a reporter. Stop must be called to drain it.
//
// A nil or zero-address sink yields a reporter that counts and discards, which
// is what development and the test suite run with — and what a deployment with
// no ops_email set runs with, deliberately: reporting is off until somebody
// says where to send it.
//
// prefix names the product in every subject line. NewReporter starts the one
// goroutine that delivers, which is the product asking for it.
func NewReporter(sink Sink, budget Budget, prefix string, log *slog.Logger) *Reporter {
	r := &Reporter{
		prefix: prefix,
		sink:   sink,
		budget: budget,
		now:    time.Now,
		log:    log,
		ledger: Ledger{},
		queue:  make(chan Event, QueueDepth),
		stop:   make(chan struct{}),
	}
	r.wg.Add(1)
	go r.drain()
	return r
}

// Report offers an event. It never blocks and never returns an error: the
// caller is a log statement, and a log statement that can fail is a trap.
func (r *Reporter) Report(e Event) {
	if r == nil {
		return
	}
	if e.At.IsZero() {
		e.At = r.now()
	}
	select {
	case r.queue <- e:
	default:
		r.mu.Lock()
		r.dropped++
		r.mu.Unlock()
	}
}

// Stop drains the queue and waits for the sender to finish.
func (r *Reporter) Stop() {
	if r == nil {
		return
	}
	r.once.Do(func() {
		close(r.stop)
		r.wg.Wait()
	})
}

// Rebind swaps the sink and the budget on a running reporter.
//
// **The ledger survives**, deliberately: an administrator changing the address
// must not reset what has already been sent today, or a flood plus a settings
// edit would be a way round the ceiling.
func (r *Reporter) Rebind(sink Sink, budget Budget) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sink, r.budget = sink, budget
}

// Dropped is how many events were discarded because the queue was full.
func (r *Reporter) Dropped() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dropped
}

func (r *Reporter) drain() {
	defer r.wg.Done()
	for {
		select {
		case e := <-r.queue:
			r.deliver(e)
		case <-r.stop:
			// Whatever is already queued still goes: a shutdown is exactly
			// when the last error matters most.
			for {
				select {
				case e := <-r.queue:
					r.deliver(e)
				default:
					return
				}
			}
		}
	}
}

func (r *Reporter) deliver(e Event) {
	e = Redact(e)

	r.mu.Lock()
	sink, budget := r.sink, r.budget
	if sink == nil {
		r.mu.Unlock()
		return
	}
	now := r.now()
	decision := Due(e, r.ledger, now, budget)
	r.ledger = Record(e, r.ledger, now, decision)
	suppressed := len(r.ledger.Suppressed)
	r.mu.Unlock()

	switch decision {
	case Hold, Ceiling:
		return
	case Last:
		// The one message that says what is being hidden.
	}
	if decision != Last {
		suppressed = 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := sink.Deliver(ctx, Subject(r.prefix, e), Body(e, suppressed)); err != nil {
		// Logged and not retried, and **never through the handler that feeds
		// this reporter**: an alert that fails to send must not become an
		// error that queues another alert.
		r.log.Warn("could not send an alert", "err", err)
	}
}

// ------------------------------------------------------------- slog handler

// Handler wraps another slog.Handler and reports records at ERROR.
//
// Levels below ERROR pass straight through: a WARN is a thing somebody should
// see in a log, not a thing worth waking them for, and the line between them
// is the only judgement this type makes.
type Handler struct {
	inner    slog.Handler
	reporter *Reporter
	attrs    []slog.Attr
	group    string
}

func NewHandler(inner slog.Handler, r *Reporter) *Handler {
	return &Handler{inner: inner, reporter: r}
}

func (h *Handler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *Handler) Handle(ctx context.Context, rec slog.Record) error {
	if rec.Level >= slog.LevelError && h.reporter != nil {
		fields := map[string]string{}
		for _, a := range h.attrs {
			fields[a.Key] = a.Value.String()
		}
		rec.Attrs(func(a slog.Attr) bool {
			fields[a.Key] = a.Value.String()
			return true
		})
		h.reporter.Report(Event{
			Kind: KindServer, Level: LevelError,
			Message: rec.Message, At: rec.Time, Fields: fields,
		})
	}
	return h.inner.Handle(ctx, rec)
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := *h
	next.inner = h.inner.WithAttrs(attrs)
	next.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &next
}

func (h *Handler) WithGroup(name string) slog.Handler {
	next := *h
	next.inner = h.inner.WithGroup(name)
	next.group = name
	return &next
}
