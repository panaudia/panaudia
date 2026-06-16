package direct

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

// countingSender counts deliveries; the gate lets tests freeze the count
// at a known moment (e.g. the instant Stop returns).
type countingSender struct {
	strings atomic.Int64
	datas   atomic.Int64
}

func (s *countingSender) SendString(topic string, msg string) { s.strings.Add(1) }
func (s *countingSender) SendData(topic string, data []byte)  { s.datas.Add(1) }

func newTestChannels() (chan StringMessage, chan DataMessage) {
	return make(chan StringMessage, 1000), make(chan DataMessage, 1000)
}

// TestBouncerStopTerminatesGoroutine: the dispatch goroutine must exit on
// Stop — the pre-fix `break`-in-select left it running forever
// (plan/history/state-cleanup/findings.md §2.1).
func TestBouncerStopTerminatesGoroutine(t *testing.T) {
	strIn, datIn := newTestChannels()
	bouncer := NewBouncer(uuid.UUID{1}, strIn, datIn)
	bouncer.SetReceiveSender(&countingSender{})

	done := make(chan struct{})
	go func() {
		bouncer.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return — dispatch goroutine still running")
	}

	// The dispatchDone channel is the goroutine's own exit signal.
	select {
	case <-bouncer.dispatchDone:
	default:
		t.Fatal("dispatch goroutine has not exited after Stop returned")
	}
}

// TestBouncerDoubleStop: Stop is idempotent — second and concurrent calls
// must neither panic nor block.
func TestBouncerDoubleStop(t *testing.T) {
	strIn, datIn := newTestChannels()
	bouncer := NewBouncer(uuid.UUID{1}, strIn, datIn)

	done := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				bouncer.Stop()
			}()
		}
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent Stop calls blocked")
	}
}

// TestBouncerNoDeliveryAfterStop: once Stop has returned, nothing more
// reaches the receiveSender — the pre-fix leak forwarded every future
// broadcast to the dead connection's writer.
func TestBouncerNoDeliveryAfterStop(t *testing.T) {
	for round := 0; round < 20; round++ {
		strIn, datIn := newTestChannels()
		bouncer := NewBouncer(uuid.UUID{1}, strIn, datIn)
		sender := &countingSender{}
		bouncer.SetReceiveSender(sender)

		// Race 100 deliveries against Stop.
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				bouncer.DeliverString(StringMessage{topic: "attributes", msg: "x"})
			}
		}()
		bouncer.Stop()
		atStop := sender.strings.Load()

		// Anything delivered after Stop returned is a violation.
		time.Sleep(20 * time.Millisecond)
		if got := sender.strings.Load(); got != atStop {
			t.Fatalf("round %d: %d messages delivered after Stop returned", round, got-atStop)
		}
		wg.Wait()
	}
}

// TestBouncerDeliverAfterStopDoesNotBlock: broadcasts to a stopped bouncer
// must drop, not block — the dispatch goroutine no longer drains the
// channel, and the backend broadcast loop holds the backend lock while
// delivering.
func TestBouncerDeliverAfterStopDoesNotBlock(t *testing.T) {
	strIn, datIn := newTestChannels()
	bouncer := NewBouncer(uuid.UUID{1}, strIn, datIn)
	bouncer.Stop()

	done := make(chan struct{})
	go func() {
		// More than the channel buffer (100) so a non-dropping send
		// would block.
		for i := 0; i < 300; i++ {
			bouncer.DeliverString(StringMessage{topic: "attributes", msg: "x"})
			bouncer.DeliverData(DataMessage{topic: "state", msg: []byte{1}})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Deliver blocked on a stopped bouncer")
	}
}

// TestBouncerSendAfterStopDrops: the connection-to-backend direction is
// also guarded — a late write from a dying connection is dropped rather
// than enqueued behind the departure.
func TestBouncerSendAfterStopDrops(t *testing.T) {
	strIn, datIn := newTestChannels()
	bouncer := NewBouncer(uuid.UUID{1}, strIn, datIn)
	bouncer.Stop()

	bouncer.SendString("attributes", "late")
	bouncer.SendData("state", []byte{1})

	select {
	case msg := <-strIn:
		t.Fatalf("send after Stop reached the backend: %v", msg)
	case msg := <-datIn:
		t.Fatalf("data send after Stop reached the backend: %v", msg)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestBouncerSetReceiveSenderRace: SetReceiveSender concurrent with
// dispatch deliveries must be race-clean (run with -race).
func TestBouncerSetReceiveSenderRace(t *testing.T) {
	strIn, datIn := newTestChannels()
	bouncer := NewBouncer(uuid.UUID{1}, strIn, datIn)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			bouncer.SetReceiveSender(&countingSender{})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			bouncer.DeliverString(StringMessage{topic: "attributes", msg: "x"})
		}
	}()
	wg.Wait()
	bouncer.Stop()
}

// TestBouncerDeliveryStillWorks: sanity — a live bouncer forwards both
// channels to the receiveSender.
func TestBouncerDeliveryStillWorks(t *testing.T) {
	strIn, datIn := newTestChannels()
	bouncer := NewBouncer(uuid.UUID{1}, strIn, datIn)
	sender := &countingSender{}
	bouncer.SetReceiveSender(sender)

	for i := 0; i < 10; i++ {
		bouncer.DeliverString(StringMessage{topic: "attributes", msg: "x"})
		bouncer.DeliverData(DataMessage{topic: "state", msg: []byte{1}})
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sender.strings.Load() == 10 && sender.datas.Load() == 10 {
			bouncer.Stop()
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("expected 10/10 deliveries, got %d/%d", sender.strings.Load(), sender.datas.Load())
}

// TestBouncerSendStillWorks: sanity — the connection-to-backend direction
// forwards into the shared channels with the node's uuid stamped on.
func TestBouncerSendStillWorks(t *testing.T) {
	strIn, datIn := newTestChannels()
	nodeID := uuid.UUID{7}
	bouncer := NewBouncer(nodeID, strIn, datIn)

	bouncer.SendString("attributes", "hello")
	bouncer.SendData("state", []byte{1, 2})

	select {
	case msg := <-strIn:
		if msg.sourceUUID != nodeID || msg.msg != "hello" {
			t.Fatalf("unexpected string message: %+v", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("string message did not arrive")
	}
	select {
	case msg := <-datIn:
		if msg.sourceUUID != nodeID || len(msg.msg) != 2 {
			t.Fatalf("unexpected data message: %+v", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("data message did not arrive")
	}
	bouncer.Stop()
}
