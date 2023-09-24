// Contains the implementation of a LSP server.

package lsp

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"container/list"

	"github.com/cmu440/lspnet"
)

type server struct {
	// server listener
	listener *lspnet.UDPConn
	// receive message from client channel
	packet_q chan packet_message
	// close server signal
	close_server chan int
	// close client signal
	close_client chan int

	// assign client id
	client_id_counter int
	// Read channel
	read_q chan Message

	// ID to client seq map, to get client seq by connid
	connID_to_seq map[int]*client_seq
	// send message from server queue channel for send message from server
	server_message_q chan server_packet

	// read counter for each connid
	connid_to_readcounter map[int]int
	// outorder list for each connid
	connid_to_outorder map[int]*list.List
	// store ordered message
	read_list *list.List
	// send read signal
	read_request chan int
	// read error signal
	read_error chan int

	// TODO: Implement this!
}

/*
packet message from client struct
store meessage and connection address
*/
type packet_message struct {
	packet_addr *lspnet.UDPAddr
	message     Message
}

/*
*
client info struct for store connId and connection address
*/
type client_info struct {
	packet_addr *lspnet.UDPAddr
	conn_id     int
}

/* store client seq with address */
type client_seq struct {
	packet_addr *lspnet.UDPAddr
	server_seq  int
}

/*store client and connect id and c*/
type server_packet struct {
	c      client_seq
	packet Message
	connid int
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
	listener, err := lspnet.ListenUDP("udp", addr)

	if err != nil {
		return nil, err
	}
	read_list := list.New()

	se := server{
		listener:          listener,
		packet_q:          make(chan packet_message),
		close_server:      make(chan int),
		close_client:      make(chan int),
		client_id_counter: 1,
		read_q:            make(chan Message),

		connID_to_seq:         make(map[int]*client_seq),
		server_message_q:      make(chan server_packet),
		connid_to_readcounter: make(map[int]int),
		connid_to_outorder:    make(map[int]*list.List),
		read_list:             read_list,
		read_request:          make(chan int),
		read_error:            make(chan int),
	}

	go se.Mainroutine()
	go se.Readroutine()

	return &se, err
}

// handle message for each case
func (s *server) Mainroutine() error {
	for {
		select {
		case packet := <-s.packet_q:
			// Message seqNum
			sn := packet.message.SeqNum
			client := client_info{packet_addr: packet.packet_addr, conn_id: packet.message.ConnID}

			switch packet.message.Type {
			case MsgConnect:
				// initialize map
				s.connID_to_seq[client.conn_id] = &client_seq{packet_addr: packet.packet_addr, server_seq: sn}
				// message start from sn+1
				s.connid_to_readcounter[client.conn_id] = sn + 1

				s.connid_to_outorder[client.conn_id].Init()

				ack_message := *NewAck(s.client_id_counter, sn)

				ack_message_mar, err := json.Marshal(ack_message)

				if err != nil {
					return err
				}

				_, err = s.listener.WriteToUDP(ack_message_mar, client.packet_addr)

				if err != nil {
					return err
				}
				// add id counter
				s.client_id_counter++
			case MsgData:
				// if received message match the read counter, just add it to to read_list
				if sn == s.connid_to_readcounter[client.conn_id] {

					s.read_list.PushBack(packet.message)
					s.connid_to_readcounter[client.conn_id]++
					// check if there is an element in outorder list that match the seq number after
					element := s.Findelement(client.conn_id, s.connid_to_readcounter[client.conn_id])

					// add element until there is no element in outorder list that match seq number
					for element != nil {
						s.read_list.PushBack(element.Value.(Message))
						s.connid_to_readcounter[client.conn_id]++

						element = s.Findelement(client.conn_id, s.connid_to_readcounter[client.conn_id])

					}

				} else {
					// outorder element
					s.connid_to_outorder[client.conn_id].PushBack(packet.message)

				}

				////ack_message := *NewAck(client.conn_id, sn)
				//
				////ack_message_mar, err := json.Marshal(ack_message)
				////if err != nil {
				//	return err
				//}
				//_, err = s.listener.WriteToUDP(ack_message_mar, client.packet_addr)
				//
				//if err != nil {
				//	return err
				//}

			case MsgAck:

			case MsgCAck:

			}
			// if send message to client
		case message_to_client := <-s.server_message_q:
			mar_message, _ := json.Marshal(message_to_client.packet)
			_, err := s.listener.WriteToUDP(mar_message, message_to_client.c.packet_addr)

			if err != nil {
				fmt.Printf("Write error")
				return err
			}
			// update seq id
			s.connID_to_seq[message_to_client.connid].server_seq++

		case <-s.close_server:
			s.listener.Close()
			return nil
		// close connection for the close client
		case connId := <-s.close_client:
			//addr := s.connID_to_seq[connId].packet_addr
			//cl := client_info{packet_addr: addr, conn_id: connId}

			delete(s.connID_to_seq, connId)
			delete(s.connid_to_readcounter, connId)
			delete(s.connid_to_outorder, connId)
		// pop message from the read_list
		case <-s.read_request:
			if s.read_list.Len() != 0 {
				head := s.read_list.Front()
				s.read_list.Remove(head)
				s.read_q <- head.Value.(Message)

			} else {
				s.read_error <- 1

			}
		}
	}
	//return nil
}

// find next element in out order list that mathc seq number
func (s *server) Findelement(connid int, seq int) *list.Element {
	for m := s.connid_to_outorder[connid].Front(); m != nil; m = m.Next() {
		if m.Value.(Message).SeqNum == seq {
			s.connid_to_outorder[connid].Remove(m)
			return m

		}

	}
	return nil

}

// read message from client
func (s *server) Readroutine() error {
	for {
		tmp := make([]byte, MAX_LENGTH)
		_, addr, err := s.listener.ReadFromUDP(tmp)
		if err != nil {
			return err
		}

		var message Message
		err = json.Unmarshal(tmp, &message)

		if err != nil {
			return err
		}

		p := packet_message{packet_addr: addr,
			message: message}

		s.packet_q <- p

	}
}

// read message from server
func (s *server) Read() (int, []byte, error) {

	// TODO: remove this line when you are ready to begin implementing this method.

	s.read_request <- 1

	select {
	case m := <-s.read_q:
		return m.ConnID, m.Payload, nil
	case <-s.read_error:
		return -1, nil, errors.New("read_eror")

	}

}

func (s *server) Write(connId int, payload []byte) error {

	server_message_info, ok := s.connID_to_seq[connId]
	if ok {
		checksum := CalculateChecksum(connId, server_message_info.server_seq, len(payload), payload)

		message := *NewData(connId, server_message_info.server_seq, len(payload), payload, checksum)

		sp := server_packet{c: *server_message_info, packet: message, connid: connId}

		s.server_message_q <- sp

		return nil

	}
	return errors.New("Cannot find connId")
}

func (s *server) CloseConn(connId int) error {
	s.close_client <- connId
	return nil
}

func (s *server) Close() error {
	s.close_server <- 1

	return nil
}
