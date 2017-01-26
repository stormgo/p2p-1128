// Package protocols helpers_test make it easier to
// write protocol tests by providing convenience functions and structures
// protocols uses these helpers for  its own tests
// but ideally should sit in p2p/protocols/testing/ subpackage
package testing

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/logger/glog"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/adapters"
)

// ExchangeTestSession assumes a network with a protocol running on multiple peer connection
// and is used to test scanarios of message exchange among a select array of nodes
// the scenarios are sets of exchanges, each with a trigger and an expectation
// This rigid regime is suitable for
// * unit testing protocol message exchanges (nodes are peers of a local node)
// * testing routed messaging between remote non-connected nodes within a group
type ExchangeTestSession struct {
	lock sync.Mutex
	Ids  []*adapters.NodeId
	TestNetAdapter
	TestMessenger
	t *testing.T
}

// implemented by simulations/
type TestNetAdapter interface {
	GetPeer(id *adapters.NodeId) *adapters.Peer
}

type TestMessenger interface {
	// MsgPipe([]byte, []byte) p2p,MsgPipe
	ExpectMsg(p2p.MsgReader, uint64, interface{}) error
	TriggerMsg(p2p.MsgWriter, uint64, interface{}) error
}

// exchanges are the basic units of protocol tests
// an exchange is defined on a session
type Exchange struct {
	Triggers []Trigger
	Expects  []Expect
}

// part of the exchange, incoming message from a set of peers
type Trigger struct {
	Msg     interface{}      // type of message to be sent
	Code    uint64           // code of message is given
	Peer    *adapters.NodeId // the peer to send the message to
	Timeout time.Duration    // timeout duration for the sending
}

type Expect struct {
	Msg     interface{}      // type of message to expect
	Code    uint64           // code of message is now given
	Peer    *adapters.NodeId // the peer that expects the message
	Timeout time.Duration    // timeout duration for receiving
}

type Disconnect struct {
	Peer  *adapters.NodeId // the peer that expects the message
	Error error
}

// NewExchangeTestSession takes a network session and Messenger
// and returns an exchange session test driver that can
// be used to unit test protocol communications
// it allows for resource-driven scenario testing
// disconnect reason errors are written in session.Errs
// (correcponding to session.Peers)
func NewExchangeTestSession(t *testing.T, n TestNetAdapter, m TestMessenger, ids []*adapters.NodeId) *ExchangeTestSession {
	return &ExchangeTestSession{
		Ids:            ids,
		TestNetAdapter: n,
		TestMessenger:  m,
		t:              t,
	}
}

type TestPeerInfo struct {
	RW     p2p.MsgReadWriter
	Flushc chan bool
	Errc   chan error
}

// trigger sends messages from peers
func (self *ExchangeTestSession) trigger(trig Trigger) error {
	peer := self.GetPeer(trig.Peer)
	if peer == nil {
		panic(fmt.Sprintf("trigger: peer %v does not exist (1- %v)", trig.Peer, len(self.Ids)))
	}
	rw := peer.RW
	if rw == nil {
		return fmt.Errorf("trigger: peer %v unreachable", trig.Peer)
	}
	errc := make(chan error)

	go func() {
		glog.V(6).Infof("trigger....")
		errc <- self.TriggerMsg(rw, trig.Code, trig.Msg)
		glog.V(6).Infof("triggered")
	}()

	t := trig.Timeout
	if t == time.Duration(0) {
		t = 1000 * time.Millisecond
	}
	alarm := time.NewTimer(t)
	select {
	case err := <-errc:
		return err
	case <-alarm.C:
		return fmt.Errorf("timout expecting %v to send to peer %v", trig.Msg, trig.Peer)
	}
}

func Key(id []byte) string {
	return string(id)
}

// expect checks an expectation
func (self *ExchangeTestSession) expect(exp Expect) error {
	if exp.Msg == nil {
		panic("no message to expect")
	}
	peer := self.GetPeer(exp.Peer)
	if peer == nil {
		panic(fmt.Sprintf("expect: peer %v does not exist (1- %v)", exp.Peer, len(self.Ids)))
	}
	rw := peer.RW
	if rw == nil {
		return fmt.Errorf("trigger: peer %v unreachable", exp.Peer)
	}

	errc := make(chan error)
	go func() {
		glog.V(6).Infof("waiting for msg, %v", exp.Msg)
		errc <- self.ExpectMsg(rw, exp.Code, exp.Msg)
	}()

	t := exp.Timeout
	if t == time.Duration(0) {
		t = 1000 * time.Millisecond
	}
	alarm := time.NewTimer(t)
	select {
	case err := <-errc:
		glog.V(6).Infof("expected msg arrives with error %v", err)
		return err
	case <-alarm.C:
		glog.V(6).Infof("caught timeout")
		return fmt.Errorf("timout expecting %v sent to peer %v", exp.Msg, exp.Peer)
	}
	// fatal upon encountering first exchange error
}

// TestExchange tests a series of exchanges againsts the session
func (self *ExchangeTestSession) TestExchanges(exchanges ...Exchange) {
	// launch all triggers of this exchanges

	for i, e := range exchanges {
		errc := make(chan error)
		wg := &sync.WaitGroup{}
		for _, trig := range e.Triggers {
			wg.Add(1)
			// separate go routing to allow parallel requests
			go func(t Trigger) {
				defer wg.Done()
				err := self.trigger(t)
				if err != nil {
					errc <- err
				}
			}(trig)
		}

		// each expectation is spawned in separate go-routine
		// expectations of an exchange are conjunctive but uordered, i.e., only all of them arriving constitutes a pass
		// each expectation is meant to be for a different peer, otherwise they are expected to panic
		// testing of an exchange blocks until all expectations are decided
		// an expectation is decided if
		//  expected message arrives OR
		// an unexpected message arrives (panic)
		// times out on their individual tiemeout
		for _, ex := range e.Expects {
			wg.Add(1)
			// expect msg spawned to separate go routine
			go func(exp Expect) {
				defer wg.Done()
				err := self.expect(exp)
				if err != nil {
					glog.V(6).Infof("expect msg fails %v", err)
					errc <- err
				}
			}(ex)
		}

		// wait for all expectations
		go func() {
			wg.Wait()
			close(errc)
		}()

		// time out globally or finish when all expectations satisfied
		alarm := time.NewTimer(1000 * time.Millisecond)
		select {

		case err := <-errc:
			if err != nil {
				self.t.Fatalf("exchange failed with: %v", err)
			} else {
				glog.V(6).Infof("exchange %v run successfully", i)
			}
		case <-alarm.C:
			self.t.Fatalf("exchange timed out")
		}
	}
}

type flushMsg struct{}

func flushExchange(c int, ids ...*adapters.NodeId) Exchange {
	var triggers []Trigger
	for _, id := range ids {
		triggers = append(triggers,
			Trigger{
				Code: uint64(c),
				Msg:  &flushMsg{},
				Peer: id,
			})
	}
	return Exchange{
		Triggers: triggers,
	}
}

var FlushMsg = &flushMsg{}

func (self *ExchangeTestSession) TestConnected(flush bool, peers ...*adapters.NodeId) {
	timeout := time.NewTimer(1000 * time.Millisecond)
	var flushc chan bool
	if !flush {
		flushc = make(chan bool)
		close(flushc)
	}
	wg := &sync.WaitGroup{}
	wg.Add(len(peers))
	for _, id := range peers {
		ticker := time.NewTicker(100 * time.Millisecond)
		go func(p *adapters.NodeId) {
			defer wg.Done()
			for {
				peer := self.GetPeer(p)
				if peer != nil {
					if flush {
						flushc = peer.Flushc
					}
					select {
					case <-timeout.C:
						self.t.Fatalf("exchange timed out waiting for peer %v to flush", p)
					case err := <-peer.Errc:
						self.t.Fatalf("peer %v disconnected with error %v", p, err)
					case <-flushc:
						glog.V(6).Infof("peer %v is connected", p)
						return
					}
				}
				select {
				case <-ticker.C:
					glog.V(6).Infof("waiting for %v to connect", p)
				case <-timeout.C:
					self.t.Fatalf("exchange timed out waiting for peer %v to connect", p)
				}
			}
		}(id)
	}
	wg.Wait()
	glog.V(6).Infof("checking complete")

}

func (self *ExchangeTestSession) TestDisconnected(disconnects ...*Disconnect) {
	for _, disconnect := range disconnects {
		id := disconnect.Peer
		err := disconnect.Error
		errc := self.GetPeer(id).Errc
		alarm := time.NewTimer(1000 * time.Millisecond)
		select {
		case derr := <-errc:
			if !((err == nil && derr == nil) || err != nil && derr != nil && err.Error() == derr.Error()) {
				self.t.Fatalf("unexpected error on peer %v: '%v', wanted '%v'", id, derr, err)
			}
		case <-alarm.C:
			self.t.Fatalf("exchange timed out waiting for peer %v to disconnect", id)
		}
	}
}
