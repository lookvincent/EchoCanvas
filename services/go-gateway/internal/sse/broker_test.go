package sse

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func TestMemoryBroker_PublishReplayAndSubscribe(t *testing.T) {
	b := NewMemoryBroker()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	jobID := uuid.NewString()
	ch, cleanup, err := b.Subscribe(ctx, jobID)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer cleanup()

	ev1, err := b.Publish(ctx, jobID, "stage_changed", map[string]any{"step": 1})
	if err != nil {
		t.Fatalf("publish 1: %v", err)
	}
	ev2, err := b.Publish(ctx, jobID, "frame_completed", map[string]any{"step": 2})
	if err != nil {
		t.Fatalf("publish 2: %v", err)
	}

	replayed, err := b.Replay(ctx, jobID, ev1.ID)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(replayed) != 1 || replayed[0].ID != ev2.ID {
		t.Fatalf("expected only ev2 on replay after ev1, got %+v", replayed)
	}

	got := <-ch
	if got.ID != ev1.ID {
		t.Fatalf("expected live ev1 %s, got %s", ev1.ID, got.ID)
	}
}

func TestRedisIntegration_BrokerReplayAndLive(t *testing.T) {
	strict := os.Getenv("RUN_REDIS_INTEGRATION") == "1"
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}

	rdb := redis.NewClient(&redis.Options{Addr: addr})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		if strict {
			t.Fatalf("RUN_REDIS_INTEGRATION=1 failed to ping redis at %s: %v", addr, err)
		}
		t.Skipf("skipping redis integration test: %v", err)
	}
	defer func() { _ = rdb.Close() }()

	jobID := "test_" + uuid.NewString()
	t.Cleanup(func() {
		_ = rdb.Del(context.Background(), streamKey(jobID)).Err()
	})

	broker := NewRedisBroker(rdb)
	ev1, err := broker.Publish(ctx, jobID, "stage_changed", map[string]any{"n": 1})
	if err != nil {
		t.Fatalf("publish 1: %v", err)
	}
	ev2, err := broker.Publish(ctx, jobID, "frame_progress", map[string]any{"n": 2})
	if err != nil {
		t.Fatalf("publish 2: %v", err)
	}

	missed, err := broker.Replay(ctx, jobID, ev1.ID)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(missed) != 1 || missed[0].ID != ev2.ID {
		t.Fatalf("expected only ev2 after Last-Event-ID=%s, got %+v", ev1.ID, missed)
	}

	subCtx, cancelSub := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelSub()
	ch, cleanup, err := broker.Subscribe(subCtx, jobID)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer cleanup()

	ev3, err := broker.Publish(ctx, jobID, "frame_completed", map[string]any{"n": 3})
	if err != nil {
		t.Fatalf("publish 3: %v", err)
	}

	select {
	case liveEv := <-ch:
		if liveEv.ID != ev3.ID || liveEv.Event != "frame_completed" {
			t.Fatalf("unexpected live event: %+v", liveEv)
		}
	case <-subCtx.Done():
		t.Fatalf("timed out waiting for live redis pubsub event")
	}
}
