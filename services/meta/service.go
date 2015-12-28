package meta

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/influxdb/influxdb"
)

const (
	MuxHeader = 8
)

type Service struct {
	RaftListener net.Listener

	config   *Config
	node     *influxdb.Node
	handler  *handler
	ln       net.Listener
	raftAddr string
	httpAddr string
	https    bool
	cert     string
	err      chan error
	Logger   *log.Logger
	store    *store
}

// NewService returns a new instance of Service.
func NewService(c *Config, node *influxdb.Node) *Service {
	s := &Service{
		config:   c,
		raftAddr: c.BindAddress,
		httpAddr: c.HTTPBindAddress,
		https:    c.HTTPSEnabled,
		cert:     c.HTTPSCertificate,
		err:      make(chan error),
		Logger:   log.New(os.Stderr, "[meta] ", log.LstdFlags),
	}
	return s
}

// Open starts the service
func (s *Service) Open() error {
	s.Logger.Println("Starting meta service")

	if s.RaftListener == nil {
		panic("no raft listener set")
	}

	// Open the store
	s.store = newStore(s.config)
	if err := s.store.open(s.ln, s.RaftListener); err != nil {
		return err
	}

	handler := newHandler(s.config)
	handler.logger = s.Logger
	handler.store = s.store
	s.handler = handler

	// Open listener.
	if s.https {
		cert, err := tls.LoadX509KeyPair(s.cert, s.cert)
		if err != nil {
			return err
		}

		listener, err := tls.Listen("tcp", s.httpAddr, &tls.Config{
			Certificates: []tls.Certificate{cert},
		})
		if err != nil {
			return err
		}

		s.Logger.Println("Listening on HTTPS:", listener.Addr().String())
		s.ln = listener
	} else {
		listener, err := net.Listen("tcp", s.httpAddr)
		if err != nil {
			return err
		}

		s.Logger.Println("Listening on HTTP:", listener.Addr().String())
		s.ln = listener
	}
	s.httpAddr = s.ln.Addr().String()

	// Begin listening for requests in a separate goroutine.
	go s.serve()
	return nil
}

// serve serves the handler from the listener.
func (s *Service) serve() {
	// The listener was closed so exit
	// See https://github.com/golang/go/issues/4373
	err := http.Serve(s.ln, s.handler)
	if err != nil && !strings.Contains(err.Error(), "closed") {
		s.err <- fmt.Errorf("listener failed: addr=%s, err=%s", s.Addr(), err)
	}
}

// Close closes the underlying listener.
func (s *Service) Close() error {
	if s.ln != nil {
		return s.ln.Close()
	}

	if err := s.store.close(); err != nil {
		return err
	}

	return nil
}

// URL returns the HTTP URL.
func (s *Service) URL() string {
	return s.httpAddr
}

// Err returns a channel for fatal errors that occur on the listener.
func (s *Service) Err() <-chan error { return s.err }

// SetLogger sets the internal logger to the logger passed in.
func (s *Service) SetLogger(l *log.Logger) {
	s.Logger = l
}

// Addr returns the listener's address. Returns nil if listener is closed.
func (s *Service) Addr() net.Addr {
	if s.ln != nil {
		return s.ln.Addr()
	}
	return nil
}
