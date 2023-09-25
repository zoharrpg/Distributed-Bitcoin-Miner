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
	listener              *lspnet.UDPConn     // server listener
	packet_q              chan packet_message // receive message from client channel
	close_server          chan int            // close server signal
	close_client          chan int            // close client signal
	client_id_counter     int                 // assign client id
	read_q                chan Message        // Read channel
	connID_to_seq         map[int]*client_seq // ID to client seq map, to get client seq by connId
	server_message_q      chan server_packet  // send message from server queue channel for send message from server
	connid_to_readcounter map[int]int         // read counter for each connId
	connid_to_outorder    map[int]*list.List  // outorder list for each connId
	read_list             *list.List          // store ordered message
	read_request          chan int            // send read signal
	read_error            chan int            // read error signal
}

// packet message from client struct
// store message and connection address
type packet_message struct {
	packetAddr *lspnet.UDPAddr
	message    Message
}

// client info struct for store connId and connection address
type client_info struct {
	packetAddr *lspnet.UDPAddr
	connId     int
}

// store client seq with address
type client_seq struct {
	packetAddr *lspnet.UDPAddr
	serverSeq  int
}

// store client and connect id and c
type server_packet struct {
	c      client_seq
	packet Message
	connId int
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

	se := server{
		listener:              listener,
		packet_q:              make(chan packet_message),
		close_server:          make(chan int),
		close_client:          make(chan int),
		client_id_counter:     1,
		read_q:                make(chan Message),
		connID_to_seq:         make(map[int]*client_seq),
		server_message_q:      make(chan server_packet),
		connid_to_readcounter: make(map[int]int),
		connid_to_outorder:    make(map[int]*list.List),
		read_list:             list.New(),
		read_request:          make(chan int),
		read_error:            make(chan int),
	}

	go se.mainRoutine()
	go se.readRoutine()

	return &se, err
}

// handle message for each case
func (s *server) mainRoutine() error {
	for {
		select {
		case packet := <-s.packet_q:
			// Message seqNum
			sn := packet.message.SeqNum
			client := client_info{packetAddr: packet.packetAddr, connId: packet.message.ConnID}

			switch packet.message.Type {
			case MsgConnect:
				// initialize map
				s.connID_to_seq[client.connId] = &client_seq{packetAddr: packet.packetAddr, serverSeq: sn}
				// message start from sn+1
				s.connid_to_readcounter[client.connId] = sn + 1
				s.connid_to_outorder[client.connId].Init()
				ack_message := *NewAck(s.client_id_counter, sn)
				ack_message_mar, err := json.Marshal(ack_message)
				if err != nil {
					return err
				}

				_, err = s.listener.WriteToUDP(ack_message_mar, client.packetAddr)
				if err != nil {
					return err
				}
				// add id counter
				s.client_id_counter++
			case MsgData:
				// if received message match the read counter, just add it to to read_list
				if sn == s.connid_to_readcounter[client.connId] {
					s.read_list.PushBack(packet.message)
					s.connid_to_readcounter[client.connId]++
					// check if there is an element in outorder list that match the seq number after
					element := s.findElement(client.connId, s.connid_to_readcounter[client.connId])
					// add element until there is no element in outorder list that match seq number
					for element != nil {
						s.read_list.PushBack(element.Value.(Message))
						s.connid_to_readcounter[client.connId]++
						element = s.findElement(client.connId, s.connid_to_readcounter[client.connId])
					}
				} else {
					// outorder element
					s.connid_to_outorder[client.connId].PushBack(packet.message)
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
				continue
			case MsgCAck:
				continue
			}
			// if send message to client
		case message_to_client := <-s.server_message_q:
			mar_message, _ := json.Marshal(message_to_client.packet)
			_, err := s.listener.WriteToUDP(mar_message, message_to_client.c.packetAddr)

			if err != nil {
				fmt.Printf("Write error")
				return err
			}
			// update seq id
			s.connID_to_seq[message_to_client.connId].serverSeq++

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

// findElement find next element in out order list that match seq number
func (s *server) findElement(connId int, seq int) *list.Element {
	for m := s.connid_to_outorder[connId].Front(); m != nil; m = m.Next() {
		if m.Value.(Message).SeqNum == seq {
			s.connid_to_outorder[connId].Remove(m)
			return m
		}
	}
	return nil
}

// read message from client
func (s *server) readRoutine() error {
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
		p := packet_message{packetAddr: addr, message: message}
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
		return -1, nil, errors.New("read_error")
	}
}

func (s *server) Write(connId int, payload []byte) error {
	server_message_info, ok := s.connID_to_seq[connId]
	if !ok {
		return errors.New("cannot find connId")
	}
	checksum := CalculateChecksum(connId, server_message_info.serverSeq, len(payload), payload)
	message := *NewData(connId, server_message_info.serverSeq, len(payload), payload, checksum)
	sp := server_packet{c: *server_message_info, packet: message, connId: connId}
	s.server_message_q <- sp
	return nil
}

func (s *server) CloseConn(connId int) error {
	s.close_client <- connId
	return nil
}

func (s *server) Close() error {
	s.close_server <- 1
	return nil
}
