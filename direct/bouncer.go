package direct

import (
	"sync"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/space"
)

type StringMessage struct {
	topic      string
	msg        string
	sourceUUID uuid.UUID // zero value means no source (e.g. system-generated)
}

type DataMessage struct {
	topic      string
	msg        []byte
	sourceUUID uuid.UUID
}

// Bouncer fans the backend's broadcasts out to one connection. A dispatch
// goroutine forwards StringChOut/DataChOut to the connection's
// receiveSender until Stop. Stop is idempotent and, once it returns, the
// dispatch goroutine has exited and no further message will be delivered
// to the receiveSender. Sends in either direction never block past Stop:
// they select on quit alongside the channel send.
type Bouncer struct {
	nodeUUID    uuid.UUID
	stringChIn  chan StringMessage
	dataChIn    chan DataMessage
	StringChOut chan StringMessage
	DataChOut   chan DataMessage

	quit         chan struct{}
	dispatchDone chan struct{}
	stopOnce     sync.Once

	// sendMu makes "Stop returned" a hard barrier for the inbound
	// direction: SendString/SendData hold the read side across their
	// check-and-enqueue, Stop takes the write side after closing quit.
	// Any send that slips past the stopped check therefore completes
	// its enqueue BEFORE Stop returns — which is what lets DepartNode
	// enqueue the departure tombstones after Stop and know they are
	// FIFO-last (the resurrection defense, mechanism-design §3a).
	sendMu  sync.RWMutex
	stopped bool // under sendMu

	senderMu      sync.RWMutex
	receiveSender space.IMessageSender
}

func NewBouncer(nodeUUID uuid.UUID, stringChIn chan StringMessage, dataChIn chan DataMessage) *Bouncer {
	bouncer := &Bouncer{
		nodeUUID:     nodeUUID,
		stringChIn:   stringChIn,
		dataChIn:     dataChIn,
		StringChOut:  make(chan StringMessage, 100),
		DataChOut:    make(chan DataMessage, 100),
		quit:         make(chan struct{}),
		dispatchDone: make(chan struct{}),
	}

	go func() {
		defer close(bouncer.dispatchDone)
		for {
			// Non-blocking quit check first: when quit and a message are
			// both ready the blocking select below picks at random, so
			// without this a stopped bouncer could keep forwarding
			// whatever is still buffered.
			select {
			case <-bouncer.quit:
				return
			default:
			}
			select {
			case <-bouncer.quit:
				return
			case msg := <-bouncer.StringChOut:
				if sender := bouncer.getReceiveSender(); sender != nil {
					sender.SendString(msg.topic, msg.msg)
				}
			case msg := <-bouncer.DataChOut:
				if sender := bouncer.getReceiveSender(); sender != nil {
					sender.SendData(msg.topic, msg.msg)
				}
			}
		}
	}()
	return bouncer
}

// Stop shuts the dispatch goroutine down and detaches the receiveSender.
// Idempotent; safe to call from multiple goroutines. Once Stop returns:
// no further deliveries reach the receiveSender (the dispatch goroutine
// has exited), and no further inbound message can be enqueued on the
// backend channels (the sendMu barrier has been crossed — in-flight
// sends finished, later sends drop).
func (bouncer *Bouncer) Stop() {
	bouncer.stopOnce.Do(func() {
		// Close quit first: senders parked on a full backend channel
		// take the quit branch and release their read locks…
		close(bouncer.quit)
		// …then the write lock is the barrier: it is acquired only
		// after every in-flight send has completed or aborted.
		bouncer.sendMu.Lock()
		bouncer.stopped = true
		bouncer.sendMu.Unlock()
		<-bouncer.dispatchDone
		bouncer.SetReceiveSender(nil)
	})
}

// SendString forwards a message from the connection into the backend.
// Drops the message after Stop, and unblocks (dropping) if Stop fires
// while waiting on a full backend channel.
func (bouncer *Bouncer) SendString(topic string, msg string) {
	bouncer.sendMu.RLock()
	defer bouncer.sendMu.RUnlock()
	if bouncer.stopped {
		return
	}
	select {
	case bouncer.stringChIn <- StringMessage{topic: topic, msg: msg, sourceUUID: bouncer.nodeUUID}:
	case <-bouncer.quit:
	}
}

func (bouncer *Bouncer) SendData(topic string, data []byte) {
	bouncer.sendMu.RLock()
	defer bouncer.sendMu.RUnlock()
	if bouncer.stopped {
		return
	}
	select {
	case bouncer.dataChIn <- DataMessage{topic: topic, msg: data, sourceUUID: bouncer.nodeUUID}:
	case <-bouncer.quit:
	}
}

// DeliverString enqueues a broadcast for this connection. After Stop the
// message is dropped instead of blocking — the dispatch goroutine is gone
// and nothing drains the channel.
func (bouncer *Bouncer) DeliverString(msg StringMessage) {
	select {
	case <-bouncer.quit:
	case bouncer.StringChOut <- msg:
	}
}

// DeliverData is DeliverString for the data channel.
func (bouncer *Bouncer) DeliverData(msg DataMessage) {
	select {
	case <-bouncer.quit:
	case bouncer.DataChOut <- msg:
	}
}

func (bouncer *Bouncer) SetReceiveSender(receiveSender space.IMessageSender) {
	bouncer.senderMu.Lock()
	bouncer.receiveSender = receiveSender
	bouncer.senderMu.Unlock()
}

func (bouncer *Bouncer) getReceiveSender() space.IMessageSender {
	bouncer.senderMu.RLock()
	defer bouncer.senderMu.RUnlock()
	return bouncer.receiveSender
}
