package web

import "encoding/json"

// hub sends board updates to the browsers watching a poll. There's no mutex,
// only run touches the subscriber map and everything else goes through channels.
type hub struct {
	subscribe   chan *subscriber
	unsubscribe chan *subscriber
	publish     chan event
	done        chan struct{} // Closed when the hub stops
}

type subscriber struct {
	pollID int64
	events chan []byte
}

type event struct {
	pollID int64
	data   []byte
}

func newHub() *hub {
	return &hub{
		subscribe:   make(chan *subscriber),
		unsubscribe: make(chan *subscriber),
		publish:     make(chan event, 16),
		done:        make(chan struct{}),
	}
}

func (h *hub) run() {
	polls := map[int64]map[*subscriber]struct{}{}

	for {
		select {
		case <-h.done:
			return

		case sub := <-h.subscribe:
			if polls[sub.pollID] == nil {
				polls[sub.pollID] = map[*subscriber]struct{}{}
			}
			polls[sub.pollID][sub] = struct{}{}

		case sub := <-h.unsubscribe:
			if subs := polls[sub.pollID]; subs != nil {
				delete(subs, sub)
				if len(subs) == 0 {
					delete(polls, sub.pollID)
				}
			}
			// Safe, it's out of the map and publish runs in this goroutine too
			close(sub.events)

		case ev := <-h.publish:
			for sub := range polls[ev.pollID] {
				select {
				case sub.events <- ev.data:
				default:
					// Slow reader, skip it. It's only a nudge to refresh.
				}
			}
		}
	}
}

func (h *hub) stop() { close(h.done) }

// broadcast tells a poll's watchers that something changed. It never blocks,
// a dropped nudge doesn't matter.
func (h *hub) broadcast(pollID int64, kind string, movieID int64) {
	data, err := json.Marshal(struct {
		Kind  string `json:"kind"`
		Movie int64  `json:"movie"`
	}{kind, movieID})
	if err != nil {
		return
	}
	select {
	case h.publish <- event{pollID: pollID, data: data}:
	default:
	}
}
