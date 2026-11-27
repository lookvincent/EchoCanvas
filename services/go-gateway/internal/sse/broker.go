package sse

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/model"
	"github.com/redis/go-redis/v9"
)

const (
	streamMaxLen = 50
	streamTTL    = 24 * time.Hour
)

// Broker publishes, replays (Last-Event-ID), and streams SSE events per job.
type Broker interface {
	Publish(ctx context.Context, jobID string, eventName string, payload any) (model.SSEEvent, error)
	Replay(ctx context.Context, jobID string, lastEventID string) ([]model.SSEEvent, error)
	Subscribe(ctx context.Context, jobID string) (<-chan model.SSEEvent, func(), error)
}

// RedisBroker uses Redis Streams for replayable history and Pub/Sub for live streaming.
type RedisBroker struct {
	rdb *redis.Client
}

// NewRedisBroker constructs a RedisBroker.
func NewRedisBroker(rdb *redis.Client) *RedisBroker {
	return &RedisBroker{rdb: rdb}
}

func streamKey(jobID string) string {
	return "echocanvas:events:" + jobID
}

func pubsubChannel(jobID string) string {
	return "echocanvas:pubsub:" + jobID
}

func (b *RedisBroker) Publish(ctx context.Context, jobID string, eventName string, payload any) (model.SSEEvent, error) {
	dataBytes, err := json.Marshal(payload)
	if err != nil {
		return model.SSEEvent{}, fmt.Errorf("marshal sse payload: %w", err)
	}

	now := time.Now().UTC()
	sk := streamKey(jobID)
	streamID, err := b.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: sk,
		MaxLen: streamMaxLen,
		Approx: true,
		Values: map[string]any{
			"event":      eventName,
			"data":       string(dataBytes),
			"created_at": now.Format(time.RFC3339Nano),
		},
	}).Result()
	if err != nil {
		return model.SSEEvent{}, fmt.Errorf("xadd sse stream: %w", err)
	}
	_ = b.rdb.Expire(ctx, sk, streamTTL).Err()

	ev := model.SSEEvent{
		ID:        streamID,
		Event:     eventName,
		Data:      dataBytes,
		CreatedAt: now,
	}
	rawEv, err := json.Marshal(ev)
	if err != nil {
		return model.SSEEvent{}, err
	}
	if err := b.rdb.Publish(ctx, pubsubChannel(jobID), string(rawEv)).Err(); err != nil {
		return ev, fmt.Errorf("publish sse event: %w", err)
	}
	return ev, nil
}

func (b *RedisBroker) Replay(ctx context.Context, jobID string, lastEventID string) ([]model.SSEEvent, error) {
	start := "-"
	if lastEventID != "" {
		start = "(" + lastEventID
	}
	msgs, err := b.rdb.XRange(ctx, streamKey(jobID), start, "+").Result()
	if err != nil {
		return nil, fmt.Errorf("xrange sse stream: %w", err)
	}

	events := make([]model.SSEEvent, 0, len(msgs))
	for _, m := range msgs {
		evName, _ := m.Values["event"].(string)
		dataStr, _ := m.Values["data"].(string)
		createdStr, _ := m.Values["created_at"].(string)
		createdAt, _ := time.Parse(time.RFC3339Nano, createdStr)
		events = append(events, model.SSEEvent{
			ID:        m.ID,
			Event:     evName,
			Data:      json.RawMessage(dataStr),
			CreatedAt: createdAt,
		})
	}
	return events, nil
}

func (b *RedisBroker) Subscribe(ctx context.Context, jobID string) (<-chan model.SSEEvent, func(), error) {
	sub := b.rdb.Subscribe(ctx, pubsubChannel(jobID))
	if _, err := sub.Receive(ctx); err != nil {
		_ = sub.Close()
		return nil, nil, fmt.Errorf("subscribe pubsub: %w", err)
	}

	out := make(chan model.SSEEvent, 64)
	subCtx, cancel := context.WithCancel(ctx)
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			cancel()
			_ = sub.Close()
		})
	}

	go func() {
		defer close(out)
		defer cleanup()
		ch := sub.Channel()
		for {
			select {
			case <-subCtx.Done():
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				var ev model.SSEEvent
				if err := json.Unmarshal([]byte(msg.Payload), &ev); err != nil {
					continue
				}
				select {
				case out <- ev:
				case <-subCtx.Done():
					return
				}
			}
		}
	}()

	return out, cleanup, nil
}

// MemoryBroker provides a deterministic in-memory Broker for unit tests.
type MemoryBroker struct {
	mu      sync.Mutex
	seq     atomic.Int64
	history map[string][]model.SSEEvent
	subs    map[string]map[int]chan model.SSEEvent
	subSeq  int
}

// NewMemoryBroker constructs an in-memory SSE broker.
func NewMemoryBroker() *MemoryBroker {
	return &MemoryBroker{
		history: make(map[string][]model.SSEEvent),
		subs:    make(map[string]map[int]chan model.SSEEvent),
	}
}

func (m *MemoryBroker) Publish(_ context.Context, jobID string, eventName string, payload any) (model.SSEEvent, error) {
	dataBytes, err := json.Marshal(payload)
	if err != nil {
		return model.SSEEvent{}, err
	}

	seq := m.seq.Add(1)
	now := time.Now().UTC()
	ev := model.SSEEvent{
		ID:        fmt.Sprintf("%d-0", seq),
		Event:     eventName,
		Data:      dataBytes,
		CreatedAt: now,
	}

	m.mu.Lock()
	buf := append(m.history[jobID], ev)
	if len(buf) > streamMaxLen {
		buf = buf[len(buf)-streamMaxLen:]
	}
	m.history[jobID] = buf
	for _, ch := range m.subs[jobID] {
		select {
		case ch <- ev:
		default:
		}
	}
	m.mu.Unlock()
	return ev, nil
}

func (m *MemoryBroker) Replay(_ context.Context, jobID string, lastEventID string) ([]model.SSEEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	events := m.history[jobID]
	if lastEventID == "" {
		out := make([]model.SSEEvent, len(events))
		copy(out, events)
		return out, nil
	}
	idx := -1
	for i, ev := range events {
		if ev.ID == lastEventID {
			idx = i
			break
		}
	}
	if idx == -1 {
		out := make([]model.SSEEvent, len(events))
		copy(out, events)
		return out, nil
	}
	out := make([]model.SSEEvent, len(events)-(idx+1))
	copy(out, events[idx+1:])
	return out, nil
}

func (m *MemoryBroker) Subscribe(ctx context.Context, jobID string) (<-chan model.SSEEvent, func(), error) {
	m.mu.Lock()
	m.subSeq++
	id := m.subSeq
	ch := make(chan model.SSEEvent, 64)
	if m.subs[jobID] == nil {
		m.subs[jobID] = make(map[int]chan model.SSEEvent)
	}
	m.subs[jobID][id] = ch
	m.mu.Unlock()

	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			m.mu.Lock()
			if m.subs[jobID] != nil {
				delete(m.subs[jobID], id)
			}
			close(ch)
			m.mu.Unlock()
		})
	}
	go func() {
		<-ctx.Done()
		cleanup()
	}()
	return ch, cleanup, nil
}
