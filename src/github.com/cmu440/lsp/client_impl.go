// Contains the implementation of an LSP client.

package lsp

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/cmu440/lspnet"
)

const (
	MAX_LENGTH           = 2000
	CHANNEL_LENGTH       = 1
	RECEIVED_WINDOW_SIZE = 0
)

type client struct {
	conn           *lspnet.UDPConn
	connId         int
	connected      chan struct{}
	rawMessages    chan Message // raw message from server
	readPayloads   chan []byte  // payload from server
	readRequest    chan struct{}
	sendPayloads   chan []byte // payload to server
	receivedRecord []Message
	sn             int // seqNum
	params         *Params
	sw             SlidingWindow
	backOffMap     map[int]*BackOff
	unsentMessages []Message
	closeRead      chan struct{}
	closeMain      chan struct{}
	closePending   chan struct{}
	isClosed       bool
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
// hostport is a colon-separated string identifying the server'sw host address
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
		connected:      make(chan struct{}),
		rawMessages:    make(chan Message, CHANNEL_LENGTH),
		readPayloads:   make(chan []byte, CHANNEL_LENGTH),
		readRequest:    make(chan struct{}, CHANNEL_LENGTH),
		sendPayloads:   make(chan []byte),
		receivedRecord: make([]Message, RECEIVED_WINDOW_SIZE),
		sn:             initialSeqNum,
		params:         params,
		sw:             SlidingWindow{make([]Message, 0)},
		backOffMap:     make(map[int]*BackOff),
		unsentMessages: make([]Message, 0),
		closeRead:      make(chan struct{}),
		closeMain:      make(chan struct{}, CHANNEL_LENGTH),
		closePending:   make(chan struct{}),
		isClosed:       false,
	}
	go c.mainRoutine()
	go c.readRoutine()
	<-c.connected

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

func (c *client) manageToSend() bool {
	isMessageSent := false
	for {
		if len(c.unsentMessages) == 0 {
			break
		}
		if c.sw.GetWindowSize() < c.params.WindowSize && c.sw.GetSize() < c.params.MaxUnackedMessages {
			message := c.unsentMessages[0]
			c.unsentMessages = c.unsentMessages[1:]
			c.sw.AddMessage(message)
			c.backOffMap[message.SeqNum] = &BackOff{0, 0}
			c.writeToServer(message)
			isMessageSent = true
		} else {
			break
		}
	}
	return isMessageSent
}

func (c *client) clientCloseCheck() bool {
	if len(c.unsentMessages) == 0 && len(c.receivedRecord) == 0 && len(c.readPayloads) == 0 {
		c.closePending <- struct{}{}
		return true
	}
	return false
}

func (c *client) mainRoutine() {
	sendingSeqNum := c.sn + 1
	receiveSeqNum := c.sn + 1
	isMessageSent := false
	idleEpochTime := 0

	connectRequest := NewConnect(c.sn)
	c.writeToServer(*connectRequest)
	isMessageSent = true
	c.sw.AddMessage(*connectRequest)
	c.backOffMap[connectRequest.SeqNum] = &BackOff{0, 0}

	ticker := time.NewTicker(time.Duration(c.params.EpochMillis) * time.Millisecond)
	defer ticker.Stop()

	heartBeat := Message{}

	for {
		select {
		case <-c.closeMain:
			c.isClosed = true
			if c.clientCloseCheck() {
				return
			}
		case <-ticker.C:
			if !isMessageSent {
				c.writeToServer(heartBeat)
			}
			isMessageSent = false
			if (idleEpochTime >= c.params.EpochLimit) && (len(c.receivedRecord) == 0) && (len(c.readPayloads) == 0) {
				c.conn.Close() // TODO: check if it's redundant
				close(c.readPayloads)
				c.closePending <- struct{}{}
				return
			}
			idleEpochTime++
			for _, message := range c.sw.window {
				seqNum := message.SeqNum
				if c.backOffMap[seqNum].epochElapsed >= c.backOffMap[seqNum].currentBackoff {
					if c.backOffMap[seqNum].currentBackoff == 0 {
						c.backOffMap[seqNum].currentBackoff = 1
					} else {
						c.backOffMap[seqNum].currentBackoff = c.backOffMap[seqNum].currentBackoff * 2
					}
					if c.backOffMap[seqNum].currentBackoff > c.params.MaxBackOffInterval {
						c.backOffMap[seqNum].currentBackoff = c.params.MaxBackOffInterval
					}
					if message.Type == MsgConnect {
						c.backOffMap[seqNum].currentBackoff = 0
					}
					c.backOffMap[seqNum].epochElapsed = 0
					c.writeToServer(message)
					isMessageSent = true
				}
				c.backOffMap[seqNum].epochElapsed++
			}
		case <-c.readRequest:
			receiveSeqNum = c.manageReceived(receiveSeqNum)

		case payload := <-c.sendPayloads:
			checksum := CalculateChecksum(c.ConnID(), sendingSeqNum, len(payload), payload)
			message := NewData(c.connId, sendingSeqNum, len(payload), payload, checksum)
			sendingSeqNum++
			c.unsentMessages = append(c.unsentMessages, *message)
			if c.manageToSend() == true {
				isMessageSent = true
			}

		case message, ok := <-c.rawMessages:
			idleEpochTime = 0
			if !ok {
				return // TODO: check if it would successfully return
			}
			switch message.Type {
			case MsgConnect:
				continue
			case MsgAck:
				if c.connId == -1 {
					c.connId = message.ConnID
					heartBeat = *NewAck(c.connId, 0)
					c.connected <- struct{}{}
				}
				c.sw.RemoveSeqNum(message.SeqNum)
				if c.isClosed {
					if c.clientCloseCheck() {
						return
					}
				}
				delete(c.backOffMap, message.SeqNum)
				if c.manageToSend() == true {
					isMessageSent = true
				}
			case MsgCAck:
				if c.connId == -1 {
					c.connId = message.ConnID
					heartBeat = *NewAck(c.connId, 0)
					c.connected <- struct{}{}
				}
				c.sw.RemoveBeforeSeqNum(message.SeqNum)
				if c.isClosed {
					if c.clientCloseCheck() {
						return
					}
				}
				for key := range c.backOffMap {
					if key <= message.SeqNum {
						delete(c.backOffMap, key)
					}
				}
				if c.manageToSend() == true {
					isMessageSent = true
				}
			case MsgData:
				if message.SeqNum >= receiveSeqNum {
					isDuplicate := false
					for _, m := range c.receivedRecord {
						if message.SeqNum == m.SeqNum {
							isDuplicate = true
							break
						}
					}
					if !isDuplicate {
						c.receivedRecord = append(c.receivedRecord, message)
					}
				}
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
			if c.connId == -1 {
				continue
			} else {
				log.Println(err)
				return
			}
		}

		var message Message
		err = json.Unmarshal(readMessage[:n], &message)
		if err != nil {
			log.Println(err)
		}
		if message.Type == MsgData {
			if message.Size == len(message.Payload) {
				checksum := CalculateChecksum(message.ConnID, message.SeqNum, message.Size, message.Payload)
				if checksum != message.Checksum {
					continue
				}
			} else if message.Size > len(message.Payload) {
				continue
			} else {
				message.Payload = message.Payload[:message.Size]
			}
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
func (c *client) Write(payload []byte) error {
	c.sendPayloads <- payload
	return nil
}

// Close terminates the client'sw connection with the server. It should block
// until all pending messages to the server have been sent and acknowledged.
// Once it returns, all goroutines running in the background should exit.
//
// After Close is called, it is safe to assume no further calls to Read, Write,
// and Close will be made. In this case, Close must either return a non-nil error,
// or never return anything.
func (c *client) Close() error {
	c.closeMain <- struct{}{} // signal mainRoutine to stop
	<-c.closePending          // wait for mainRoutine to stop
	err := c.conn.Close()     // signal readRoutine to stop
	if err != nil {
		return err
	}
	return nil
}
