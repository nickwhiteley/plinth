package alert

import (
	"context"
	"testing"

	"github.com/nickwhiteley/plinth/email"
	"github.com/nickwhiteley/plinth/settings"
)

type mailCapture struct{ got []email.Message }

func (c *mailCapture) Send(_ context.Context, m email.Message) error {
	c.got = append(c.got, m)
	return nil
}

func TestMailSink(t *testing.T) {
	r := settings.NewRegistry().Declare(Settings...)
	c := &mailCapture{}
	if SinkFromSnapshot(r.Snapshot(nil), c) != nil {
		t.Error("with no ops_email, reporting must be off")
	}
	sink := SinkFromSnapshot(r.Snapshot([]settings.State{{Key: KeyOpsEmail, Value: "ops@example.com"}}), c)
	if err := sink.Deliver(context.Background(), "Test: it broke", "the body"); err != nil {
		t.Fatal(err)
	}
	if len(c.got) != 1 || c.got[0].Kind != email.KindOperatorReport || c.got[0].To != "ops@example.com" || c.got[0].Subject != "Test: it broke" {
		t.Errorf("sent %+v", c.got)
	}
}
