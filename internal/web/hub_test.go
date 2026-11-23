package web

import (
	"testing"
	"time"
)

func TestHubDeliversOnlyToTheRightPoll(t *testing.T) {
	h := newHub()
	go h.run()
	defer h.stop()

	watching := &subscriber{pollID: 1, events: make(chan []byte, 4)}
	elsewhere := &subscriber{pollID: 2, events: make(chan []byte, 4)}
	h.subscribe <- watching
	h.subscribe <- elsewhere

	h.broadcast(1, "voted", 42)

	select {
	case got := <-watching.events:
		if string(got) != `{"kind":"voted","movie":42}` {
			t.Errorf("payload = %s", got)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber on poll 1 got nothing")
	}

	select {
	case got := <-elsewhere.events:
		t.Errorf("subscriber on poll 2 received %s. Polls must not leak into each other", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHubDropsRatherThanBlocking(t *testing.T) {
	h := newHub()
	go h.run()
	defer h.stop()

	// A reader that never reads (like a browser on a stalled connection)
	wedged := &subscriber{pollID: 1, events: make(chan []byte, 1)}
	healthy := &subscriber{pollID: 1, events: make(chan []byte, 8)}
	h.subscribe <- wedged
	h.subscribe <- healthy

	for i := 0; i < 20; i++ {
		h.broadcast(1, "voted", int64(i))
	}

	// The healthy subscriber still gets its event despite the wedged one.
	select {
	case <-healthy.events:
	case <-time.After(time.Second):
		t.Fatal("one stalled reader blocked the hub")
	}
}

func TestUnsubscribeClosesTheChannel(t *testing.T) {
	h := newHub()
	go h.run()
	defer h.stop()

	sub := &subscriber{pollID: 1, events: make(chan []byte, 1)}
	h.subscribe <- sub
	h.unsubscribe <- sub

	select {
	case _, ok := <-sub.events:
		if ok {
			t.Error("expected a closed channel, got a value")
		}
	case <-time.After(time.Second):
		t.Fatal("channel was not closed on unsubscribe")
	}
}
