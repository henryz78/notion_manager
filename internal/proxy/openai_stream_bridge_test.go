package proxy

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAnthropicStreamBridgeAbortUnblocksWriter(t *testing.T) {
	bridge := newAnthropicStreamBridgeWriter()
	writeDone := make(chan error, 1)
	go func() {
		var err error
		for i := 0; i < cap(bridge.frames)+10; i++ {
			_, err = bridge.Write([]byte("event: ping\ndata: {}\n\n"))
			if err != nil {
				break
			}
		}
		writeDone <- err
		bridge.Close()
	}()

	deadline := time.After(2 * time.Second)
	for len(bridge.frames) < cap(bridge.frames) {
		select {
		case <-deadline:
			t.Fatal("bridge writer did not fill its frame buffer")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	bridge.Abort()

	select {
	case err := <-writeDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("writer error=%v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("aborted bridge left its producer blocked")
	}
}
