package actor

import (
	"context"
	"testing"

	"github.com/nickwhiteley/plinth/ids"
)

func TestActor(t *testing.T) {
	if _, ok := From(context.Background()); ok {
		t.Fatal("a bare context has an actor")
	}
	a := ids.New()
	if got, ok := From(With(context.Background(), a)); !ok || got != a {
		t.Fatalf("From = %v, %v", got, ok)
	}
	if _, ok := From(With(context.Background(), ids.UUID{})); ok {
		t.Fatal("the zero id is no actor")
	}
}
