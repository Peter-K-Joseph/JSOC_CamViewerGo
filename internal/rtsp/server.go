package rtsp

import (
	"fmt"
	"log"
	"net"

	"github.com/jsoc/camviewer/internal/netutil"
	"github.com/jsoc/camviewer/internal/streaming"
)

// Server is a minimal RTSP/1.0 TCP server.
type Server struct {
	host      string
	startPort int
	prefix    string
	trackFunc func(streamKey string) *streaming.Track

	// BoundPort is set after ListenAndServe binds successfully.
	BoundPort int
}

func NewServer(host string, port int, prefix string, trackFunc func(string) *streaming.Track) *Server {
	return &Server{
		host:      host,
		startPort: port,
		prefix:    prefix,
		trackFunc: trackFunc,
	}
}

// ListenAndServe binds to the first free port at or after startPort, then blocks accepting connections.
func (srv *Server) ListenAndServe() error {
	ln, port, err := netutil.ListenTCP("0.0.0.0", srv.startPort)
	if err != nil {
		return fmt.Errorf("rtsp: %w", err)
	}
	srv.BoundPort = port
	if port != srv.startPort {
		log.Printf("[rtsp] port %d in use, bound to %d instead", srv.startPort, port)
	}
	log.Printf("[rtsp] listening on rtsp://%s:%d/%s/<stream-key>", srv.host, port, srv.prefix)

	return Serve(ln, srv.trackFunc, srv.host, srv.prefix)
}

// Serve accepts connections on an already-bound listener.
func Serve(ln net.Listener, trackFunc func(string) *streaming.Track, host, prefix string) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go ServeConn(conn, trackFunc, host, prefix)
	}
}

// ServeConn handles a single RTSP client connection.
func ServeConn(conn net.Conn, trackFunc func(string) *streaming.Track, host, prefix string) {
	newSession(conn, trackFunc, host, prefix).serve()
}
