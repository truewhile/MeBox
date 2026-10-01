// Package service — WebSocket hub used to push real-time progress events
// (scan / scrape / transcode) to subscribed clients.
package service

import (
	"sync"

	"go.uber.org/zap"
)

// Event is the JSON payload pushed to subscribers.
type Event struct {
	Topic   string `json:"topic"`
	Payload any    `json:"payload"`
}

// Subscriber is a single connected client; the hub writes events into Out
// and closes Done when the connection should be torn down.
type Subscriber struct {
	ID     string
	Out    chan Event
	topics map[string]struct{}
}

// Hub is a fan-out broker: services publish on a topic and every subscriber
// that opted into that topic receives the event.
type Hub struct {
	log    *zap.Logger
	mu     sync.RWMutex
	subs   map[string]*Subscriber
	in     chan Event
	stop   chan struct{}
	closed bool
}

// NewHub builds a Hub. Caller must invoke Run in its own goroutine.
func NewHub(log *zap.Logger) *Hub {
	return &Hub{
		log:  log,
		subs: make(map[string]*Subscriber),
		in:   make(chan Event, 256),
		stop: make(chan struct{}),
	}
}

// Run is the blocking event loop. Publish events with Hub.Publish.
func (h *Hub) Run() {
	for {
		select {
		case <-h.stop:
			return
		case ev := <-h.in:
			h.fanout(ev)
		}
	}
}

// Stop terminates the hub goroutine and disconnects every subscriber.
func (h *Hub) Stop() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	close(h.stop)
	for _, sub := range h.subs {
		close(sub.Out)
	}
	h.subs = nil
}

// Publish sends an event to every interested subscriber. Non-blocking: the
// event is dropped if the hub is full to avoid stalling the producer.
func (h *Hub) Publish(topic string, payload any) {
	select {
	case h.in <- Event{Topic: topic, Payload: payload}:
	default:
		h.log.Warn("ws hub overflow, dropping event", zap.String("topic", topic))
	}
}

// Subscribe registers a new connection for a given topic set. Pass an empty
// list to receive every topic.
func (h *Hub) Subscribe(id string, topics []string) *Subscriber {
	sub := &Subscriber{
		ID:     id,
		Out:    make(chan Event, 32),
		topics: map[string]struct{}{},
	}
	for _, t := range topics {
		sub.topics[t] = struct{}{}
	}
	h.mu.Lock()
	// Stop() 会把 subs 置 nil：停机窗口内仍在握手的 WS 连接若在此写入
	// nil map 会直接 panic。已关闭的 hub 返回一个立刻关闭的空订阅者。
	if h.subs == nil {
		h.mu.Unlock()
		close(sub.Out)
		return sub
	}
	h.subs[id] = sub
	h.mu.Unlock()
	return sub
}

// Unsubscribe disconnects the subscriber and closes its outbound channel.
func (h *Hub) Unsubscribe(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	sub, ok := h.subs[id]
	if !ok {
		return
	}
	delete(h.subs, id)
	close(sub.Out)
}

func (h *Hub) fanout(ev Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, sub := range h.subs {
		if len(sub.topics) > 0 {
			if _, ok := sub.topics[ev.Topic]; !ok {
				continue
			}
		}
		select {
		case sub.Out <- ev:
		default:
			// Slow consumer: drop the event for this subscriber.
		}
	}
}
