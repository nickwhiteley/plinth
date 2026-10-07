package alert

import (
	"context"

	"github.com/nickwhiteley/plinth/email"
	"github.com/nickwhiteley/plinth/settings"
)

// MailSink delivers alerts as operator_report mail through the product's mailer.
type MailSink struct {
	Sender email.Sender
	To     string
}

func (m MailSink) Deliver(ctx context.Context, subject, body string) error {
	return m.Sender.Send(ctx, email.Message{Kind: email.KindOperatorReport, To: m.To, Subject: subject, TextBody: body})
}

// SinkFromSnapshot is the sink the settings describe, for Reporter.Rebind: mail to ops_email, or
// nil (count and discard) while it is empty, which is reporting turned off.
func SinkFromSnapshot(s settings.Snapshot, sender email.Sender) Sink {
	to := s.String(KeyOpsEmail)
	if to == "" || sender == nil {
		return nil
	}
	return MailSink{Sender: sender, To: to}
}
