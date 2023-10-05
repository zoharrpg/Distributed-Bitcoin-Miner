// Contains the implementation of an LSP client.

package lsp

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/cmu440/lspnet"
)

const (
	MAX_LENGTH           = 2000
	RAW_MESSAGE_LENGTH   = 1
	RECEIVED_WINDOW_SIZE = 0
)

type client struct {
	conn           *lspnet.UDPConn
	connId         int
	rawMessages    chan Message // raw message from server
	readPayloads   chan []byte  // payload from server
	readRequest    chan struct{}
	sendRequest    chan struct{}
	sendPayloads   chan []byte // payload to server
	receivedRecord []Message
	sn             int // seqNum
	params         *Params
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

	c := client{
		conn:           connection,
		connId:         -1,
		rawMessages:    make(chan Message, RAW_MESSAGE_LENGTH),
		readPayloads:   make(chan []byte, RAW_MESSAGE_LENGTH),
		readRequest:    make(chan struct{}),
		sendRequest:    make(chan struct{}),
		sendPayloads:   make(chan []byte, RAW_MESSAGE_LENGTH),
		receivedRecord: make([]Message, RECEIVED_WINDOW_SIZE),
		sn:             initialSeqNum,
		params:         params,
	}
	go c.mainRoutine()
	go c.readRoutine()

	return &c, nil
}

func (c *client) manageReceived(receiveSeqNum int) int {
	if len(c.readPayloads) != 0 {
		return receiveSeqNum
	}
	sort.Sort(BySeqNum(c.receivedRecord))
	if len(c.receivedRecord) > 0 && c.receivedRecord[0].SeqNum == receiveSeqNum {
		c.readPayloads <- c.receivedRecord[0].Payload
		c.receivedRecord = c.receivedRecord[1:]
		receiveSeqNum++
	}
	return receiveSeqNum
}

func (c *client) writeToServer(message Message) {
	marMessage, err := json.Marshal(message)
	if err != nil {
		fmt.Println(err)
	}
	_, err = c.conn.Write(marMessage)
	if err != nil {
		fmt.Println(err)
	}
}

func (c *client) mainRoutine() {
	sendingSeqNum := c.sn
	receiveSeqNum := c.sn
	isMessageSent := false
	idleEpochTime := 0
	backOffMap := make(map[int]*BackOff)
	unsentMessages := make([]Message, 0)

	connectRequest := NewConnect(c.sn)
	c.writeToServer(*connectRequest)
	isMessageSent = true
	s := SlidingWindow{make([]Message, 0)}
	s.AddMessage(*connectRequest)
	backOffMap[connectRequest.SeqNum] = &BackOff{0, 0}

	ticker := time.NewTicker(time.Duration(c.params.EpochMillis))
	defer ticker.Stop()

	heartBeat := Message{}

	for {
		select {
		case <-ticker.C:
			if !isMessageSent {
				c.writeToServer(heartBeat)
			}
			isMessageSent = false
			if (idleEpochTime >= c.params.EpochLimit) && (len(c.receivedRecord) == 0) && (len(c.readPayloads) == 0) {
				c.Close()
				return
			}
			idleEpochTime++
			// TODO: check back off and trigger resend
			for seqNum, message := range s.window {
				if backOffMap[seqNum].epochElapsed >= backOffMap[seqNum].currentBackoff {
					if backOffMap[seqNum].currentBackoff == 0 {
						backOffMap[seqNum].currentBackoff = 1
					} else {
						backOffMap[seqNum].currentBackoff = backOffMap[seqNum].currentBackoff * 2
						if backOffMap[seqNum].currentBackoff > c.params.MaxBackOffInterval {
							backOffMap[seqNum].currentBackoff = c.params.MaxBackOffInterval // TODO: check if it is correct
						}
					}
					backOffMap[seqNum].epochElapsed = 0
					c.writeToServer(message)
					isMessageSent = true
				}
				backOffMap[seqNum].epochElapsed++
			}
		case <-c.readRequest:
			receiveSeqNum = c.manageReceived(receiveSeqNum)
		case <-c.sendRequest:
			for {
				if s.GetWindowSize() <= c.params.WindowSize && s.GetSize() <= c.params.MaxUnackedMessages {
					message := unsentMessages[0]
					unsentMessages = unsentMessages[1:]
					s.AddMessage(message)
					backOffMap[message.SeqNum] = &BackOff{0, 0}
					c.writeToServer(message)
					isMessageSent = true
				} else {
					break
				}
			}
		case payload := <-c.sendPayloads:
			sendingSeqNum++
			checksum := CalculateChecksum(c.ConnID(), sendingSeqNum, len(payload), payload)
			message := NewData(c.connId, sendingSeqNum, len(payload), payload, checksum)
			unsentMessages = append(unsentMessages, *message)
			// TODO: check if needs sort
			c.sendRequest <- struct{}{}

		case message, ok := <-c.rawMessages:
			if !ok {
				return // TODO: check if it would successfully return
			}
			switch message.Type {
			case MsgConnect:
				continue // do nothing
			case MsgAck:
				if c.connId == -1 {
					c.connId = message.ConnID
					heartBeat = *NewAck(c.connId, 0)
				}
				s.RemoveSeqNum(message.SeqNum)
				delete(backOffMap, message.SeqNum)
				c.sendRequest <- struct{}{}
			case MsgCAck:
				if c.connId == -1 {
					c.connId = message.ConnID
					heartBeat = *NewAck(c.connId, 0)
				}
				s.RemoveBeforeSeqNum(message.SeqNum)
				for key := range backOffMap {
					if key <= message.SeqNum {
						delete(backOffMap, key)
					}
				}
				c.sendRequest <- struct{}{}
			case MsgData:
				c.receivedRecord = append(c.receivedRecord, message)
				receiveSeqNum = c.manageReceived(receiveSeqNum)
				// send ack
				ackMessage := *NewAck(c.connId, message.SeqNum)
				c.writeToServer(ackMessage)
			}
		}
	}
}

func (c *client) readRoutine() {
	readMessage := make([]byte, MAX_LENGTH)
	for {
		n, err := c.conn.Read(readMessage)
		if err != nil {
			fmt.Println(err)
			return
		}

		var message Message
		err = json.Unmarshal(readMessage[:n], &message)
		if err != nil {
			fmt.Println(err)
		}
		c.rawMessages <- message
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
//	(2) the connection has been lost due to an epoch timeout
//		and no other messages are waiting to be returned by read
//	(3) the server is closed. Note that in the third case,
//		it is also ok for Read to never return anything.
//
// If the Read function gets called multiple times, we expect
// all messages received from the server to be returned by Read
// in order by SeqNum without skipping or repeating any SeqNum.
func (c *client) Read() ([]byte, error) {
	c.readRequest <- struct{}{}
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
	c.sendPayloads <- payload
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

type BackOff struct {
	currentBackoff int
	epochElapsed   int
}

type BySeqNum []Message

func (a BySeqNum) Len() int           { return len(a) }
func (a BySeqNum) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a BySeqNum) Less(i, j int) bool { return a[i].SeqNum < a[j].SeqNum }

type SlidingWindow struct {
	window []Message
}

func (s *SlidingWindow) AddMessage(m Message) {
	s.window = append(s.window, m)
	sort.Sort(BySeqNum(s.window)) // TODO: check if it needs sort
}

func (s *SlidingWindow) getIndex(sn int) int {
	for i := 0; i < len(s.window); i++ {
		if s.window[i].SeqNum == sn {
			return i
		}
	}
	return -1
}

func (s *SlidingWindow) PeakSeqNum() int {
	return s.window[0].SeqNum
}

func (s *SlidingWindow) Find(sn int) Message {
	i := s.getIndex(sn)
	if i == -1 {
		return Message{}
	}
	return s.window[i]
}

func (s *SlidingWindow) RemoveSeqNum(sn int) {
	i := s.getIndex(sn)
	if i == -1 {
		return
	}
	s.window = append(s.window[:i], s.window[i+1:]...)
}

func (s *SlidingWindow) RemoveBeforeSeqNum(sn int) {
	i := s.getIndex(sn)
	if i == -1 {
		return
	}
	s.window = s.window[i+1:]
}

func (s *SlidingWindow) GetSize() int {
	return len(s.window)
}

func (s *SlidingWindow) GetWindowSize() int {
	return s.window[len(s.window)-1].SeqNum - s.window[0].SeqNum + 1
}

func (s *SlidingWindow) print() {
	fmt.Printf("size: %d ", s.GetSize())
	fmt.Printf(", window size: %d ", s.GetWindowSize())
	fmt.Printf("window: %v\n", s.window)
}
