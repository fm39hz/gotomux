package event_test

import (
	"context"
	"testing"

	"github.com/fm39hz/gotomux/internal/event"
)

func TestBusOnEmit(t *testing.T) {
	b := event.New()
	calls := 0
	b.On(event.FreezeDone, func(ctx context.Context, args ...any) {
		calls++
		if len(args) != 2 {
			t.Errorf("got %d args, want 2", len(args))
		}
	})
	b.Emit(context.Background(), event.FreezeDone, "a", "b")
	if calls != 1 {
		t.Errorf("handler called %d times, want 1", calls)
	}
}

func TestBusMultipleHandlers(t *testing.T) {
	b := event.New()
	var order []string
	b.On(event.FreezeDone, func(ctx context.Context, args ...any) { order = append(order, "a") })
	b.On(event.FreezeDone, func(ctx context.Context, args ...any) { order = append(order, "b") })
	b.On(event.ShapeSaved, func(ctx context.Context, args ...any) { order = append(order, "c") })

	b.Emit(context.Background(), event.FreezeDone)
	if len(order) != 2 {
		t.Fatalf("got %d calls, want 2", len(order))
	}
	if order[0] != "a" || order[1] != "b" {
		t.Errorf("order = %v, want [a b]", order)
	}
}

func TestBusNoHandler(t *testing.T) {
	b := event.New()
	b.Emit(context.Background(), event.FreezeDone) // should not panic
}

func TestBusDifferentEvent(t *testing.T) {
	b := event.New()
	calls := 0
	b.On(event.FreezeDone, func(ctx context.Context, args ...any) { calls++ })
	b.Emit(context.Background(), event.ShapeSaved)
	if calls != 0 {
		t.Errorf("handler called for wrong event")
	}
}
