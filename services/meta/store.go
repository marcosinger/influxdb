package meta

import (
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"github.com/hashicorp/raft"
)

const raftListenerStartupTimeout = time.Second

type store struct {
	mu      sync.RWMutex
	closing chan struct{}

	config      *Config
	data        *Data
	raftState   *raftState
	dataChanged chan struct{}
	path        string
	opened      bool
	logger      *log.Logger

	// Authentication cache.
	authCache map[string]authUser
}

type authUser struct {
	salt []byte
	hash []byte
}

// newStore will create a new metastore with the passed in config
func newStore(c *Config) *store {
	s := store{
		data: &Data{
			Index: 1,
		},
		closing:     make(chan struct{}),
		dataChanged: make(chan struct{}),
		path:        c.Dir,
		config:      c,
	}
	if c.LoggingEnabled {
		s.logger = log.New(os.Stderr, "[metastore] ", log.LstdFlags)
	} else {
		s.logger = log.New(ioutil.Discard, "", 0)
	}

	return &s
}

// open opens and initializes the raft store.
func (s *store) open(addr string, raftln net.Listener) error {
	s.logger.Printf("Using data dir: %v", s.path)

	// wait for the raft listener to start
	timeout := time.Now().Add(raftListenerStartupTimeout)
	for {
		if raftln.Addr() != nil {
			break
		}

		if time.Now().After(timeout) {
			return fmt.Errorf("unable to open without raft listener running")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// See if this server needs to join the raft consensus group
	var initializePeers []string
	if len(s.config.JoinPeers) > 0 {
		var err error
		initializePeers, err = s.joinCluster(addr, raftln.Addr().String(), s.config.JoinPeers)
		if err != nil {
			return err
		}
	}

	if err := func() error {
		s.mu.Lock()
		defer s.mu.Unlock()

		// Check if store has already been opened.
		if s.opened {
			return ErrStoreOpen
		}
		s.opened = true

		// Create the root directory if it doesn't already exist.
		if err := os.MkdirAll(s.path, 0777); err != nil {
			return fmt.Errorf("mkdir all: %s", err)
		}

		// Open the raft store.
		if err := s.openRaft(initializePeers, raftln); err != nil {
			return fmt.Errorf("raft: %s", err)
		}

		return nil
	}(); err != nil {
		return err
	}

	// Wait for a leader to be elected so we know the raft log is loaded
	// and up to date
	return s.waitForLeader(0)
}

func (s *store) openRaft(initializePeers []string, raftln net.Listener) error {
	rs := newRaftState(s.config)
	rs.logger = s.logger
	rs.path = s.path

	if err := rs.open(s, raftln, initializePeers); err != nil {
		return err
	}
	s.raftState = rs

	return nil
}

func (s *store) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	close(s.closing)
	return nil
}

func (s *store) snapshot() (*Data, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.Clone(), nil
}

// afterIndex returns a channel that will be closed to signal
// the caller when an updated snapshot is available.
func (s *store) afterIndex(index uint64) <-chan struct{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if index < s.data.Index {
		// Client needs update so return a closed channel.
		ch := make(chan struct{})
		close(ch)
		return ch
	}

	return s.dataChanged
}

// WaitForLeader sleeps until a leader is found or a timeout occurs.
// timeout == 0 means to wait forever.
func (s *store) waitForLeader(timeout time.Duration) error {
	// Begin timeout timer.
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	// Continually check for leader until timeout.
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.closing:
			return errors.New("closing")
		case <-timer.C:
			if timeout != 0 {
				return errors.New("timeout")
			}
		case <-ticker.C:
			if s.leader() != "" {
				return nil
			}
		}
	}
}

// isLeader returns true if the store is currently the leader.
func (s *store) isLeader() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.raftState == nil {
		return false
	}
	return s.raftState.raft.State() == raft.Leader
}

// leader returns what the store thinks is the current leader. An empty
// string indicates no leader exists.
func (s *store) leader() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.raftState == nil {
		return ""
	}
	return s.raftState.raft.Leader()
}

// index returns the current store index.
func (s *store) index() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.Index
}

// apply applies a command to raft.
func (s *store) apply(b []byte) error {
	return s.raftState.apply(b)
}

// joinCluster will use the metaclient to join this server to the cluster and
// return the raft peers so that raft can be started
func (s *store) joinCluster(httpAddr, raftAddr string, metaServers []string) (raftPeers []string, err error) {
	c := NewClient(metaServers, s.config.HTTPSEnabled)
	if err := c.CreateMetaNode(httpAddr, raftAddr); err != nil {
		return nil, err
	}
	data := c.retryUntilSnapshot()

	for _, n := range data.MetaNodes {
		raftPeers = append(raftPeers, n.TCPHost)
	}

	return
}

// RetentionPolicyUpdate represents retention policy fields to be updated.
type RetentionPolicyUpdate struct {
	Name     *string
	Duration *time.Duration
	ReplicaN *int
}

// SetName sets the RetentionPolicyUpdate.Name
func (rpu *RetentionPolicyUpdate) SetName(v string) { rpu.Name = &v }

// SetDuration sets the RetentionPolicyUpdate.Duration
func (rpu *RetentionPolicyUpdate) SetDuration(v time.Duration) { rpu.Duration = &v }

// SetReplicaN sets the RetentionPolicyUpdate.ReplicaN
func (rpu *RetentionPolicyUpdate) SetReplicaN(v int) { rpu.ReplicaN = &v }
