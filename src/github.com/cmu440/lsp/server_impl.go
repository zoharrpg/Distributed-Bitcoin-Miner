// Contains the implementation of a LSP server.

package lsp

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	"container/list"

	"github.com/cmu440/lspnet"
)

type server struct {
	listener              *lspnet.UDPConn       // server listener
	packet_q              chan packet_message   // receive message from client channel
	close_server          chan int              // close server signal
	close_client          chan int              // close client signal
	client_id_counter     int                   // assign client id
	read_q                chan Message          // Read channel
	connID_to_seq         map[int]*client_seq   // ID to client seq map, to get client seq by connId
	server_message_q      chan server_packet    // send message from server queue channel for send message from server
	connid_to_readcounter map[int]int           // read counter for each connId
	connid_to_outorder    map[int]*list.List    // outorder list for each connId
	readList              *list.List            // store ordered message
	readRequest           chan int              // send read signal
	window_map            map[int][]Message     // slide window for each client
	window_state_map      map[int]*window_state // slide window state for each client
	window_outorder       map[int][]Message     // slide window outorder list

	window_size int
	max_unack   int
}

type window_state struct {
	unack_count int
	unack_index int
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
	connId   int
	playload []byte
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
		read_q:                make(chan Message, 1),
		connID_to_seq:         make(map[int]*client_seq),
		server_message_q:      make(chan server_packet),
		connid_to_readcounter: make(map[int]int),
		connid_to_outorder:    make(map[int]*list.List),
		readList:              list.New(),
		readRequest:           make(chan int),
		window_map:            make(map[int][]Message),
		window_state_map:      make(map[int]*window_state),
		window_outorder:       make(map[int][]Message),
		window_size:           params.WindowSize,
		max_unack:             params.MaxUnackedMessages,
	}

	go se.mainRoutine()
	go se.readRoutine()

	return &se, err
}

func (s *server) manageReadList() {
	if len(s.read_q) == 0 && s.readList.Len() > 0 {
		head := s.readList.Front()
		s.readList.Remove(head)
		s.read_q <- head.Value.(Message)
	}
}

// handle message for each case
func (s *server) mainRoutine() {
	for {
		select {
		case packet := <-s.packet_q:
			// Message seqNum
			sn := packet.message.SeqNum
			client := client_info{packetAddr: packet.packetAddr, connId: packet.message.ConnID}

			switch packet.message.Type {
			case MsgAck:
				length := len(s.window_map[client.connId])
				s.window_map[client.connId] = removeFromSlice(s.window_map[client.connId], sn)
				if len(s.window_map[client.connId]) == length {
					fmt.Println("Slice Find Error")
				}
				s.window_state_map[client.connId].unack_index = s.window_map[client.connId][0].SeqNum
				s.window_state_map[client.connId].unack_count -= 1

				for _, m := range s.window_outorder[client.connId] {
					if m.SeqNum < s.window_state_map[client.connId].unack_index+s.window_size && s.window_state_map[client.connId].unack_count < s.max_unack {
						marMessage, _ := json.Marshal(m)
						_, err := s.listener.WriteToUDP(marMessage, client.packetAddr)
						if err != nil {
							fmt.Println(err)
						}
						// update unack count
						s.window_state_map[client.connId].unack_count++
						s.window_map[client.connId] = append(s.window_map[client.connId], m)
						sort.Slice(s.window_map[client.connId], func(i, j int) bool {
							return s.window_map[client.connId][i].SeqNum < s.window_map[client.connId][j].SeqNum
						})

					}

				}

			case MsgCAck:
				length := len(s.window_map[client.connId])
				s.window_map[client.connId] = removeFromSlice_CAck(s.window_map[client.connId], sn)
				if len(s.window_map[client.connId]) == length {
					fmt.Println("Slice Find Error")
				}
				s.window_state_map[client.connId].unack_index = s.window_map[client.connId][0].SeqNum
				s.window_state_map[client.connId].unack_count -= length - len(s.window_map[client.connId])

				for _, m := range s.window_outorder[client.connId] {
					if m.SeqNum < s.window_state_map[client.connId].unack_index+s.window_size && s.window_state_map[client.connId].unack_count < s.max_unack {
						marMessage, _ := json.Marshal(m)
						_, err := s.listener.WriteToUDP(marMessage, client.packetAddr)
						if err != nil {
							fmt.Println(err)
						}
						// update unack count
						s.window_state_map[client.connId].unack_count++
						s.window_map[client.connId] = append(s.window_map[client.connId], m)
						sort.Slice(s.window_map[client.connId], func(i, j int) bool {
							return s.window_map[client.connId][i].SeqNum < s.window_map[client.connId][j].SeqNum
						})

					}

				}

			case MsgConnect:
				ackMessage := *NewAck(s.client_id_counter, sn)
				ackMessageMar, err := json.Marshal(ackMessage)
				if err != nil {
					fmt.Println(err)
				}

				_, err = s.listener.WriteToUDP(ackMessageMar, client.packetAddr)
				if err != nil {
					fmt.Println(err)
				}
				// initialize map
				s.connID_to_seq[s.client_id_counter] = &client_seq{packetAddr: packet.packetAddr, serverSeq: sn}
				// message start from sn+1
				s.connid_to_readcounter[s.client_id_counter] = sn + 1
				s.connid_to_outorder[s.client_id_counter] = list.New()

				s.window_map[s.client_id_counter] = make([]Message, 0)
				s.window_state_map[s.client_id_counter] = &window_state{unack_count: 0, unack_index: sn + 1}
				s.window_outorder[s.client_id_counter] = make([]Message, 0)

				// add id counter
				s.client_id_counter++
			case MsgData:
				ackMessage := *NewAck(client.connId, sn)
				ackMessageMar, err := json.Marshal(ackMessage)
				if err != nil {
					fmt.Println(err)
				}
				_, err = s.listener.WriteToUDP(ackMessageMar, client.packetAddr)

				// fmt.Println("send ack")

				if err != nil {
					fmt.Println(err)
				}

				// if received message match the read counter, just add it to read_list
				if sn == s.connid_to_readcounter[client.connId] {

					s.readList.PushBack(packet.message)
					s.connid_to_readcounter[client.connId]++
					// check if there is an element in out-of-order list that match the seq number after
					element := s.findElement(client.connId, s.connid_to_readcounter[client.connId])
					// add element until there is no element in out-of-order list that match seq number
					for element != nil {
						s.readList.PushBack(element.Value.(Message))
						s.connid_to_readcounter[client.connId]++
						element = s.findElement(client.connId, s.connid_to_readcounter[client.connId])
					}
				} else {
					if s.connid_to_outorder[client.connId] == nil {
						s.connid_to_outorder[client.connId] = list.New()
					}
					// out-of-order element
					s.connid_to_outorder[client.connId].PushBack(packet.message)
				}

				s.manageReadList()
			}
		case <-s.readRequest:
			s.manageReadList()
		// if send message to client
		case messageToClient := <-s.server_message_q:
			serverMessageInfo, ok := s.connID_to_seq[messageToClient.connId]

			if ok {
				// message
				checksum := CalculateChecksum(messageToClient.connId, serverMessageInfo.serverSeq, len(messageToClient.playload), messageToClient.playload)
				// message
				message := *NewData(messageToClient.connId, serverMessageInfo.serverSeq, len(messageToClient.playload), messageToClient.playload, checksum)

				if message.SeqNum < s.window_state_map[messageToClient.connId].unack_index+s.window_size && s.window_state_map[messageToClient.connId].unack_count < s.max_unack {
					marMessage, _ := json.Marshal(message)
					_, err := s.listener.WriteToUDP(marMessage, serverMessageInfo.packetAddr)
					if err != nil {
						fmt.Println(err)
					}
					// update unack count
					s.window_state_map[messageToClient.connId].unack_count++
					s.window_map[messageToClient.connId] = append(s.window_map[messageToClient.connId], message)
					sort.Slice(s.window_map[messageToClient.connId], func(i, j int) bool {
						return s.window_map[messageToClient.connId][i].SeqNum < s.window_map[messageToClient.connId][j].SeqNum
					})
				} else {
					s.window_outorder[messageToClient.connId] = append(s.window_outorder[messageToClient.connId], message)
					sort.Slice(s.window_outorder[messageToClient.connId], func(i, j int) bool {
						return s.window_outorder[messageToClient.connId][i].SeqNum < s.window_outorder[messageToClient.connId][j].SeqNum
					})
				}

				// update seq id
				s.connID_to_seq[messageToClient.connId].serverSeq++

			}

		case <-s.close_server:
			err := s.listener.Close()
			if err != nil {
				return
			}
		// close connection for the close client
		case connId := <-s.close_client:
			//addr := s.connID_to_seq[connId].packet_addr
			//cl := client_info{packet_addr: addr, conn_id: connId}

			delete(s.connID_to_seq, connId)
			delete(s.connid_to_readcounter, connId)
			delete(s.connid_to_outorder, connId)
			// pop message from the read_list

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
func (s *server) readRoutine() {
	for {
		tmp := make([]byte, MAX_LENGTH)
		n, addr, err := s.listener.ReadFromUDP(tmp)

		if err != nil {
			fmt.Println(err)
		}

		var message Message
		err = json.Unmarshal(tmp[:n], &message)
		if err != nil {
			fmt.Println(err)
		}
		p := packet_message{packetAddr: addr, message: message}
		s.packet_q <- p
	}
}

// read message from server
func (s *server) Read() (int, []byte, error) {
	s.readRequest <- 1
	message := <-s.read_q
	return message.ConnID, message.Payload, nil
}

func (s *server) Write(connId int, payload []byte) error {
	//serverMessageInfo, ok := s.connID_to_seq[connId]
	// if !ok {
	// 	return errors.New("cannot find connId")
	// }
	// checksum := CalculateChecksum(connId, serverMessageInfo.serverSeq, len(payload), payload)
	// message := *NewData(connId, serverMessageInfo.serverSeq, len(payload), payload, checksum)
	sp := server_packet{connId: connId, playload: payload}
	s.server_message_q <- sp
	return nil
}

func (s *server) CloseConn(connId int) error {
	fmt.Println("Server: CloseConn", connId)
	s.close_client <- connId
	return nil
}

func (s *server) Close() error {
	fmt.Println("Server: Closing")
	s.close_server <- 1
	return nil
}
