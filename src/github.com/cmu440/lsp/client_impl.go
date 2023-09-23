// Contains the implementation of an LSP client.

package lsp

import (
	"errors"
	"fmt"

	"encoding/json"

	"github.com/cmu440/lspnet"
)

const (
	MAX_LENGTH           = 2000
	RAW_MESSAGE_LENGTH   = 100
	RECEIVED_WINDOW_SIZE = 10
)

type SlidingWindow struct {
	window []int
	size   int
}

func (s *SlidingWindow) moveWindow() {
	i := 0
	for j := 0; j < len(s.window)-1; j++ {
		if s.window[j] != 0 {
			s.window[i] = s.window[j]
			if j != i {
				s.window[j] = 0
			}
			i++
		}
	}
}

func (s *SlidingWindow) AddSeqNum(sn int) {
	s.window[s.size] = sn
	s.size = s.size + 1
}

func (s *SlidingWindow) RemoveSeqNum(sn int) {
	for i := 0; i < s.size; i++ {
		if s.window[i] == sn {
			s.window[i] = 0
			s.size--
			break
		}
	}
	s.moveWindow()
}

func (s *SlidingWindow) RemoveBeforeSeqNum(sn int) {
	breakFlag := false
	size := s.size
	for i := 0; i < size; i++ {
		if s.window[i] <= sn {
			if s.window[i] == sn {
				breakFlag = true
			}
			s.window[i] = 0
			s.size--
			if breakFlag {
				break
			}
		}
	}
	s.moveWindow()
}

func (s *SlidingWindow) getHead() int {
	return s.window[0]
}

func (s *SlidingWindow) getSize() int {
	return s.size
}

func (s *SlidingWindow) print() {
	fmt.Printf("size: %d\n", s.size)
	fmt.Printf("window: %v\n", s.window)
}

type client struct {
	conn               *lspnet.UDPConn
	connId             int
	rawMessages        chan Message // raw message from server
	readPayloads       chan []byte  // payload from server
	sendPayloads       chan []byte  // payload to server
	sendingWindow      SlidingWindow
	receivedRecord     SlidingWindow
	sn                 int // seqNum
	accessSn           chan struct{}
	getSn              chan int
	MaxUnackedMessages int
}

// NewClient creates, initiates, and returns a new client. This function
// should return after a connection with the server has been established
// (i.e., the client has received an Ack message from the server in response
// to its connection request), and should return a non-nil error if a
// connection could not be made (i.e., if after K epochs, the client still
// hasn't received an Ack message from the server in response to its K
// connection requests).
//
// initialSeqNum is an int representing the Initial Sequence Number (ISN) this
// client must use. You may assume that sequence numbers do not wrap around.
//
// hostport is a colon-separated string identifying the server's host address
// and port number (i.e., "localhost:9999").
func NewClient(hostport string, initialSeqNum int, params *Params) (Client, error) {
	addr, err := lspnet.ResolveUDPAddr("udp", hostport)
	if err != nil {
		return nil, err
	}

	connection, err := lspnet.DialUDP("udp", nil, addr)
	if err != nil {
		return nil, err
	}

	connectRequest := *NewConnect(initialSeqNum)
	marConnReq, err := json.Marshal(connectRequest)
	if err != nil {
		return nil, err
	}

	_, err = connection.Write(marConnReq)
	if err != nil {
		return nil, err
	}

	// length
	readMessage := make([]byte, MAX_LENGTH)
	_, err = connection.Read(readMessage)
	if err != nil {
		return nil, err
	}

	var ackMessage Message
	err = json.Unmarshal(readMessage, &ackMessage)
	if err != nil {
		return nil, err
	}

	if ackMessage.Type != MsgAck {
		return nil, errors.New("Ack message error")
	}

	connId := ackMessage.ConnID

	c := client{
		conn:               connection,
		connId:             connId,
		rawMessages:        make(chan Message, RAW_MESSAGE_LENGTH),
		readPayloads:       make(chan []byte, RAW_MESSAGE_LENGTH),
		sendPayloads:       make(chan []byte, RAW_MESSAGE_LENGTH),
		sendingWindow:      SlidingWindow{window: make([]int, params.WindowSize), size: 0},
		receivedRecord:     SlidingWindow{window: make([]int, RECEIVED_WINDOW_SIZE), size: 0},
		sn:                 initialSeqNum,
		accessSn:           make(chan struct{}),
		getSn:              make(chan int),
		MaxUnackedMessages: params.MaxUnackedMessages,
	}
	go c.mainRoutine()
	go c.readRoutine()

	return &c, nil
}

func (c *client) mainRoutine() {
	for {
		select {
		case <-c.accessSn:
			c.sn++
			c.getSn <- c.sn
		case message, ok := <-c.rawMessages:
			if !ok {
				return // TODO: check if it would successfully return
			}
			switch message.Type {
			case MsgAck:
				c.sendingWindow.RemoveSeqNum(message.SeqNum) // TODO: race condition
			case MsgCAck:
				c.sendingWindow.RemoveBeforeSeqNum(message.SeqNum) // TODO: race condition
			case MsgData:
				// If the Read function gets called multiple times, we expect
				// all messages received from the server to be returned by Read
				// in order by SeqNum without skipping or repeating any SeqNum.
				// TODO: put the payload in the right order
				if message.ConnID != c.connId {
					continue
				}
				c.readPayloads <- message.Payload
				// send ack
			case MsgConnect:
				continue // do nothing
			}
		}
	}
}

func (c *client) readRoutine() {
	readMessage := make([]byte, MAX_LENGTH)
	for {
		select {
		case <-c.close_signal_read:
			return nil // TODO: remove select case

		default:
			read_message := make([]byte, MAX_LENGTH)

			_, err := c.conn.Read(read_message)
			if err != nil {
				fmt.Printf("read error\n")
				return err
			}
			var message Message
			json.Unmarshal(read_message, &message)

		// TODO: check if it is necessary to clear the buffer
		for i := range readMessage {
			readMessage[i] = 0
		}
	}
}

func (c *client) ConnID() int {
	return c.connId
}

// Read reads a data message from the server and returns its payload.
// This method should block until data has been received from the server and
// is ready to be returned. It should return a non-nil error if either
//
//	(1) the connection has been explicitly closed,
//	(2) the connection has been lost due to an epoch timeout TODO: in final
//		and no other messages are waiting to be returned by read
//	(3) the server is closed. Note that in the third case, TODO: check the server's status
//		it is also ok for Read to never return anything.
//
// If the Read function gets called multiple times, we expect
// all messages received from the server to be returned by Read
// in order by SeqNum without skipping or repeating any SeqNum.
func (c *client) Read() ([]byte, error) {
	// TODO: implement this method.
	payload, ok := <-c.readPayloads // Blocks indefinitely.
	if !ok {
		return nil, errors.New("connection has been explicitly closed")
	}
	return payload, nil
}

// Write sends a data message with the specified payload to the server.
// This method should NOT block, and should return a non-nil error
// if the connection with the server has been lost.
// If Close has been called on the client, it is safe to assume
// no further calls to Write will be made. In this case,
// Write must either return a non-nil error, or never return anything.
// TODO: what if the server is closed?
func (c *client) Write(payload []byte) error {
	c.accessSn <- struct{}{}
	sn := <-c.getSn
	for {
		// TODO: race condition check
		if c.sendingWindow.getSize() < c.MaxUnackedMessages && sn-c.sendingWindow.getHead() < len(c.sendingWindow.window) {
			break
		}
	}
	checksum := CalculateChecksum(c.ConnID(), sn, len(payload), payload)
	message := NewData(c.connId, sn, len(payload), payload, checksum)
	marMessage, err := json.Marshal(message)
	if err != nil {
		return err
	}
	_, err = c.conn.Write(marMessage)
	c.sendingWindow.AddSeqNum(sn) // TODO: race condition
	if err != nil {
		return err
	}
	return nil
}

// Close terminates the client's connection with the server. It should block
// until all pending messages to the server have been sent and acknowledged.
// Once it returns, all goroutines running in the background should exit.
//
// After Close is called, it is safe to assume no further calls to Read, Write,
// and Close will be made. In this case, Close must either return a non-nil error,
// or never return anything.
func (c *client) Close() error {
	close(c.readPayloads) // signal read to return
	err := c.conn.Close() // signal readRoutine to stop, and write to return
	if err != nil {
		return err
	}
	close(c.rawMessages) // signal mainRoutine to stop
	return nil
}
