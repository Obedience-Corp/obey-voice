package events

import (
	"reflect"
	"sync"
)

// Handler is a function that handles an event.
type Handler func(Event)

// Bus is a simple typed pub/sub event bus safe for concurrent use.
type Bus struct {
	mu          sync.RWMutex
	handlers    map[reflect.Type][]Handler
	allHandlers []allEntry
	observers   []allEntry
	nextAllID   int
}

// ObserveBeforeFanout registers a detachable, memory-only diagnostic observer.
// It runs before typed consumers. Observers must not perform I/O or emit events
// recursively; they may sequence and enqueue copied diagnostics without waiting
// for their writer. SubscribeAll retains its existing after-typed ordering.
func (b *Bus) ObserveBeforeFanout(handler Handler) (remove func()) {
	b.mu.Lock()
	id := b.nextAllID
	b.nextAllID++
	b.observers = append(b.observers, allEntry{id: id, h: handler})
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		for i, e := range b.observers {
			if e.id == id {
				b.observers = append(b.observers[:i], b.observers[i+1:]...)
				return
			}
		}
	}
}

type allEntry struct {
	id int
	h  Handler
}

// NewBus creates a new event bus.
func NewBus() *Bus {
	return &Bus{
		handlers: make(map[reflect.Type][]Handler),
	}
}

// Subscribe registers a handler for events of a specific type.
// The handler will be called for any event matching the registered type.
func Subscribe[T Event](b *Bus, handler func(T)) {
	b.mu.Lock()
	defer b.mu.Unlock()

	t := reflect.TypeFor[T]()
	b.handlers[t] = append(b.handlers[t], func(e Event) {
		handler(e.(T))
	})
}

// SubscribeAll registers a handler invoked for every event regardless of
// type, and returns a function that removes it. Unlike Subscribe, these
// handlers are detachable — per-connection consumers (network streams) must
// not leak handlers for the process lifetime. Removal is idempotent.
func (b *Bus) SubscribeAll(handler Handler) (remove func()) {
	b.mu.Lock()
	defer b.mu.Unlock()

	id := b.nextAllID
	b.nextAllID++
	b.allHandlers = append(b.allHandlers, allEntry{id: id, h: handler})

	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		for i, e := range b.allHandlers {
			if e.id == id {
				b.allHandlers = append(b.allHandlers[:i], b.allHandlers[i+1:]...)
				return
			}
		}
	}
}

// Emit sends an event to all registered handlers for that event type, then
// to SubscribeAll handlers. Handlers are called synchronously in
// registration order.
func (b *Bus) Emit(event Event) {
	b.mu.RLock()
	t := reflect.TypeOf(event)
	handlers := make([]Handler, 0, len(b.observers)+len(b.handlers[t])+len(b.allHandlers))
	for _, e := range b.observers {
		handlers = append(handlers, e.h)
	}
	handlers = append(handlers, b.handlers[t]...)
	for _, e := range b.allHandlers {
		handlers = append(handlers, e.h)
	}
	b.mu.RUnlock()

	for _, h := range handlers {
		h(event)
	}
}
