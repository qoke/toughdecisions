package council

import (
	"testing"
	"time"
)

func TestRegistryFanoutAndUnsubscribeOnFull(t *testing.T) {
	reg := NewRegistry(0)
	defer reg.Close()
	ch1, unsub1 := reg.Subscribe("req1")
	defer unsub1()
	ch2, unsub2 := reg.Subscribe("req1")
	defer unsub2()

	reg.Publish("req1", Event{Type: EventViewComplete, RequestID: "req1", At: time.Now()})
	for i, ch := range []<-chan Event{ch1, ch2} {
		select {
		case ev := <-ch:
			if ev.Type != EventViewComplete {
				t.Fatalf("sub %d type = %q", i, ev.Type)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("sub %d got no event", i)
		}
	}

	// Fill ch1 without reading; the publisher must unsubscribe it after 1s
	// rather than blocking forever.
	for i := 0; i < SubscriberBufferSize+1; i++ {
		reg.Publish("req1", Event{Type: EventDone, RequestID: "req1", At: time.Now()})
	}
	select {
	case _, ok := <-ch1:
		if !ok {
			t.Fatal("slow subscriber channel closed; want a drained event")
		}
		// Drained one; the slow subscriber must be gone by now.
	case <-time.After(3 * time.Second):
		t.Fatal("slow subscriber publish blocked forever")
	}
}

func TestRegistryRemoveAfterDone(t *testing.T) {
	reg := NewRegistry(50 * time.Millisecond)
	defer reg.Close()
	reg.Register("r1", func() {})
	reg.MarkDone("r1")
	if !reg.Has("r1") {
		t.Fatal("entry missing right after done")
	}
	deadline := time.Now().Add(3 * time.Second)
	for reg.Has("r1") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if reg.Has("r1") {
		t.Fatal("entry not removed after removeAfter")
	}
}

func TestRegistryCancelInvokesFunc(t *testing.T) {
	reg := NewRegistry(0)
	defer reg.Close()
	called := make(chan struct{}, 1)
	reg.Register("r9", func() { called <- struct{}{} })
	reg.Cancel("r9")
	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("cancel func not invoked")
	}
	reg.Cancel("missing") // must not panic
}
