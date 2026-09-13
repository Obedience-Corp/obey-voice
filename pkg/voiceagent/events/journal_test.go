package events

import "testing"

func TestDiagnosticObserverRunsBeforeFanoutAndDetaches(t *testing.T) {
	b := NewBus()
	var order []string
	remove := b.ObserveBeforeFanout(func(Event) { order = append(order, "diagnostic") })
	Subscribe(b, func(Info) { order = append(order, "typed") })
	b.SubscribeAll(func(Event) { order = append(order, "all") })
	b.Emit(Info{})
	if len(order) != 3 || order[0] != "diagnostic" || order[1] != "typed" || order[2] != "all" {
		t.Fatalf("order=%v", order)
	}
	remove()
	remove()
	order = nil
	b.Emit(Info{})
	if len(order) != 2 || order[0] != "typed" {
		t.Fatalf("after detach=%v", order)
	}
}
