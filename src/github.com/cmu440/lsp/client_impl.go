// Contains the implementation of a LSP client.

package lsp

import (
	"errors"
	"fmt"

	"encoding/json"

	"github.com/cmu440/lspnet"
)

const MAX_LENGTH = 2000

type client struct {
	conn          *lspnet.UDPConn
	connection_id int
	// for write, send message to server
	message_q chan Message
	// for read from server
	server_message chan Message
	// seqNum
	sn                int
	close_signal_main chan int
	close_signal_read chan int

	// TODO: implement this!
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
	initial_message := *NewConnect(initialSeqNum)
	mar_message, err := json.Marshal(initial_message)

	if err != nil {
		return nil, err
	}

	_, err = connection.Write(mar_message)

	if err != nil {
		return nil, err
	}
	// length
	read_message := make([]byte, MAX_LENGTH)

	_, err = connection.Read(read_message)
	if err != nil {
		return nil, err
	}
	var ack_message Message
	json.Unmarshal(read_message, &ack_message)

	if ack_message.Type != MsgAck {
		return nil, errors.New("Ack message error")
	}

	id := ack_message.ConnID

	c := client{
		conn:              connection,
		connection_id:     id,
		message_q:         make(chan Message),
		server_message:    make(chan Message),
		sn:                initialSeqNum,
		close_signal_main: make(chan int),
		close_signal_read: make(chan int),
	}
	go c.Mainroutine()
	go c.Readroutine()

	return &c, nil
}
func (c *client) Mainroutine() error {
	for {
		select {
		case message := <-c.message_q:
			mar_message, _ := json.Marshal(message)

			_, err := c.conn.Write(mar_message)
			if err != nil {
				fmt.Printf("Write error")
				return err
			}

		case <-c.close_signal_main:

			c.conn.Close()
			return nil

		}

	}

}
func (c *client) Readroutine() error {

	for {
		select {
		case <-c.close_signal_read:
			return nil
		default:
			read_message := make([]byte, MAX_LENGTH)

			_, err := c.conn.Read(read_message)
			if err != nil {
				fmt.Printf("read error\n")
				return err
			}
			var message Message
			json.Unmarshal(read_message, &message)

			c.server_message <- message

		}

	}

}

func (c *client) ConnID() int {
	return c.connection_id
}

func (c *client) Read() ([]byte, error) {
	// TODO: remove this line when you are ready to begin implementing this method.
	select {
	case message := <-c.server_message:
		return message.Payload, nil

	} // Blocks indefinitely.

	//return nil, errors.New("not yet implemented")
}

func (c *client) Write(payload []byte) error {
	c.sn++
	checksum := CalculateChecksum(c.ConnID(), c.sn, len(payload), payload)

	message := NewData(c.connection_id, c.sn, len(payload), payload, checksum)

	c.message_q <- *message
	return nil
	// return errors.New("not yet implemented")
}

func (c *client) Close() error {
	c.close_signal_main <- 1
	c.close_signal_read <- 1
	return nil
}
