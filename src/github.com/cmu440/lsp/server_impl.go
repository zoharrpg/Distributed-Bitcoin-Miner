// Contains the implementation of a LSP server.

package lsp

import (
	"errors"
	"strconv"

	"github.com/cmu440/lspnet"
)

type server struct {
	conn              *lspnet.UDPConn
	client_connection chan lspnet.UDPConn
	close_signal_main chan int
	close_signal_read chan int

	// TODO: Implement this!
}

// NewServer creates, initiates, and returns a new server. This function should
// NOT block. Instead, it should spawn one or more goroutines (to handle things
// like accepting incoming client connections, triggering epoch events at
// fixed intervals, synchronizing events using a for-select loop like you saw in
// project 0, etc.) and immediately return. It should return a non-nil error if
// there was an error resolving or listening on the specified port number.

func NewServer(port int, params *Params) (Server, error) {
	addr, err := lspnet.ResolveUDPAddr("udp", ":"+strconv.Itoa(port))
	if err != nil {
		return nil, err
	}
	conn, err := lspnet.ListenUDP("udp", addr)

	if err != nil {
		return nil, err
	}

	se := server{
		conn:              conn,
		client_connection: make(chan lspnet.UDPConn),
		close_signal_main: make(chan int),
		close_signal_read: make(chan int),
	}

	go se.Mainroutine()

	return &se, err
}
func (se *server) Mainroutine() {
	for {
		select {}
	}
	return
}

func (s *server) Read() (int, []byte, error) {
	// TODO: remove this line when you are ready to begin implementing this method.
	select {} // Blocks indefinitely.
	return -1, nil, errors.New("not yet implemented")
}

func (s *server) Write(connId int, payload []byte) error {
	return errors.New("not yet implemented")
}

func (s *server) CloseConn(connId int) error {
	return errors.New("not yet implemented")
}

func (s *server) Close() error {
	return errors.New("not yet implemented")
}
