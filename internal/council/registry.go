package council

import (
	"context"
	"sync"
	"time"
)

// Event types published by the runner.
const (
	EventViewComplete  = "view_complete"
	EventViewFailed    = "view_failed"
	EventViewLate      = "view_late"
	EventDanger        = "danger"
	EventJudgeStarted  = "judge_started"
	EventJudgeComplete = "judge_complete"
	EventJudgeFailed   = "judge_failed"
	EventSuperseded    = "superseded"
	EventDone          = "done"
)

// SubscriberBufferSize is the per-subscriber channel capacity.
const SubscriberBufferSize = 32

// SubscriberBlockTimeout bounds how long Publish waits for space in a
// full subscriber channel before unsubscribing it.
const SubscriberBlockTimeout = time.Second

// Event is one live-run notification.
type Event struct {
	Type      string
	RequestID string
	At        time.Time
	Payload   any
}

// subscriber is one fan-out target with its own buffered channel.
type subscriber struct {
	mu     sync.Mutex
	ch     chan Event
	closed bool
	done   chan struct{}
}

// run tracks one live request: its cancel func plus subscribers.
type run struct {
	cancel  context.CancelFunc
	subs    map[*subscriber]struct{}
	doneAt  time.Time
	hasDone bool
}

// Registry fans out run events to SSE subscribers and holds per-run
// cancel funcs so supersede can cancel in-flight work.
// It is safe for concurrent use.
type Registry struct {
	mu          sync.Mutex
	runs        map[string]*run
	removeAfter time.Duration
	stopCleaner chan struct{}
}

// NewRegistry builds a Registry. Entries are removed removeAfter after
// done; values <= 0 disable the background cleaner.
func NewRegistry(removeAfter time.Duration) *Registry {
	r := &Registry{runs: map[string]*run{}, removeAfter: removeAfter}
	if removeAfter > 0 {
		r.stopCleaner = make(chan struct{})
		go r.cleaner()
	}
	return r
}

func (r *Registry) cleaner() {
	t := time.NewTicker(r.removeAfter)
	defer t.Stop()
	for {
		select {
		case <-r.stopCleaner:
			return
		case now := <-t.C:
			r.mu.Lock()
			for id, rn := range r.runs {
				if rn.hasDone && now.Sub(rn.doneAt) >= r.removeAfter {
					for sub := range rn.subs {
						sub.close()
					}
					delete(r.runs, id)
				}
			}
			r.mu.Unlock()
		}
	}
}

// Close stops the background cleaner.
func (r *Registry) Close() {
	if r.stopCleaner != nil {
		select {
		case <-r.stopCleaner:
		default:
			close(r.stopCleaner)
		}
	}
}

// Register records the cancel func for a request id.
func (r *Registry) Register(requestID string, cancel func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rn, ok := r.runs[requestID]
	if !ok {
		rn = &run{subs: map[*subscriber]struct{}{}}
		r.runs[requestID] = rn
	}
	rn.cancel = cancel
}

// Cancel invokes the stored cancel func for a request id, if any.
func (r *Registry) Cancel(requestID string) {
	r.mu.Lock()
	rn, ok := r.runs[requestID]
	r.mu.Unlock()
	if !ok || rn.cancel == nil {
		return
	}
	rn.cancel()
}

// Subscribe returns a buffered channel receiving this request's events
// plus an unsubscribe func. The channel is closed on unsubscribe and on
// run removal.
func (r *Registry) Subscribe(requestID string) (ch <-chan Event, unsubscribe func()) {
	sub := &subscriber{ch: make(chan Event, SubscriberBufferSize), done: make(chan struct{})}
	r.mu.Lock()
	rn, ok := r.runs[requestID]
	if !ok {
		rn = &run{subs: map[*subscriber]struct{}{}}
		r.runs[requestID] = rn
	}
	rn.subs[sub] = struct{}{}
	r.mu.Unlock()
	var once sync.Once
	unsub := func() {
		once.Do(func() {
			r.mu.Lock()
			if cur, ok := r.runs[requestID]; ok {
				delete(cur.subs, sub)
			}
			r.mu.Unlock()
			sub.close()
		})
	}
	return sub.ch, unsub
}

// MarkDone records completion time so the cleaner can remove the entry.
func (r *Registry) MarkDone(requestID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rn, ok := r.runs[requestID]
	if !ok {
		rn = &run{subs: map[*subscriber]struct{}{}}
		r.runs[requestID] = rn
	}
	rn.doneAt = time.Now()
	rn.hasDone = true
}

// Has reports whether the registry still holds an entry for requestID.
func (r *Registry) Has(requestID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.runs[requestID]
	return ok
}

// Publish fans out ev to every subscriber of requestID. A full subscriber
// blocks up to SubscriberBlockTimeout for space, then is unsubscribed.
// Publish never blocks forever and never drops messages silently: a slow
// subscriber is removed.
func (r *Registry) Publish(requestID string, ev Event) {
	r.mu.Lock()
	rn, ok := r.runs[requestID]
	if !ok {
		r.mu.Unlock()
		return
	}
	subs := make([]*subscriber, 0, len(rn.subs))
	for sub := range rn.subs {
		subs = append(subs, sub)
	}
	r.mu.Unlock()

	for _, sub := range subs {
		if sub.send(ev) {
			continue
		}
		r.mu.Lock()
		if cur, ok := r.runs[requestID]; ok {
			delete(cur.subs, sub)
		}
		r.mu.Unlock()
		sub.close()
	}
}

// send delivers ev with backpressure; false means the subscriber timed out.
func (s *subscriber) send(ev Event) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return true
	}
	select {
	case s.ch <- ev:
		return true
	default:
	}
	timer := time.NewTimer(SubscriberBlockTimeout)
	defer timer.Stop()
	select {
	case s.ch <- ev:
		return true
	case <-timer.C:
		return false
	case <-s.done:
		return true
	}
}

func (s *subscriber) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.done)
	close(s.ch)
}
