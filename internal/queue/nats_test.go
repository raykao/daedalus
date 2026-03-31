package queue

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

func TestConsumerPublishAndConsume(t *testing.T) {
	// Start in-process NATS server with JetStream enabled
	opts := natsserver.DefaultTestOptions
	opts.Port = -1 // random port
	opts.JetStream = true
	opts.StoreDir = t.TempDir()
	srv := natsserver.RunServer(&opts)
	defer srv.Shutdown()

	natsURL := srv.ClientURL()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	const streamName = "TEST_TASKS"
	const subject = "test.tasks.>"

	consumer, err := NewConsumer(ctx, Config{
		URL:     natsURL,
		Stream:  streamName,
		Subject: subject,
	})
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	defer consumer.Close()

	pub, err := NewPublisher(consumer.Conn(), nil)
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}

	// Publish a test message
	type testMsg struct {
		Text string `json:"text"`
	}
	payload := testMsg{Text: "hello queue"}
	if err := pub.PublishJSON(ctx, "test.tasks.foo", payload); err != nil {
		t.Fatalf("PublishJSON: %v", err)
	}

	// Run consumer and expect the message
	received := make(chan []byte, 1)
	runCtx, runCancel := context.WithCancel(ctx)
	go func() {
		consumer.Run(runCtx, func(ctx context.Context, data []byte) error {
			received <- data
			runCancel() // stop after first message
			return nil
		})
	}()

	select {
	case data := <-received:
		var got testMsg
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("unmarshal received: %v", err)
		}
		if got.Text != "hello queue" {
			t.Errorf("expected 'hello queue', got %q", got.Text)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for message")
	}
}

func TestPublisherCreatesStream(t *testing.T) {
	opts := natsserver.DefaultTestOptions
	opts.Port = -1
	opts.JetStream = true
	opts.StoreDir = t.TempDir()
	srv := natsserver.RunServer(&opts)
	defer srv.Shutdown()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer nc.Close()

	pub, err := NewPublisher(nc, nil)
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}

	// Create a stream first so we can publish to it
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	_, err = js.CreateStream(ctx, jetstream.StreamConfig{
		Name:     "RESULTS",
		Subjects: []string{"results.>"},
	})
	if err != nil {
		t.Fatalf("create stream: %v", err)
	}

	if err := pub.PublishJSON(ctx, "results.task-1", map[string]string{"status": "done"}); err != nil {
		t.Fatalf("PublishJSON: %v", err)
	}
}
