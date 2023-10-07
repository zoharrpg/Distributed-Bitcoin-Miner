// Contains the implementation of a LSP server.

package lsp

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"container/list"

	"github.com/cmu440/lspnet"
)

type server struct {
	listener              *lspnet.UDPConn      // server listener
	packet_q              chan packet_message  // receive message from client channel
	close_server          chan int             // close server signal
	close_client          chan int             // close client signal
	client_id_counter     int                  // assign client id
	read_q                chan Message         // Read channel
	connID_to_seq         map[int]*client_seq  // ID to client seq map, to get client seq by connId
	server_message_q      chan server_packet   // send message from server queue channel for send message from server
	connid_to_readcounter map[int]int          // read counter for each connId
	connid_to_outorder    map[int]*list.List   // outorder list for each connId
	readList              *list.List           // store ordered message
	readRequest           chan int             // send read signal
	window_map            map[int][]Message    // slide window for each client
	window_outorder       map[int][]Message    // slide window outorder list
	message_dup_map       map[int]map[int]bool // id to receive request seq
	connection_dup_map    map[string]bool
	active_client_map     map[int]int  //connid to epoch
	message_sent          map[int]bool // in each epoch, True if there is message sent
	message_backoff       map[messageID]*backoffInfo
	close_pending         chan int
	close_id              int
	close_read            chan int
	close_flag            bool
	drop_id               chan int
	params                *Params
}

type messageID struct {
	connID             int
	server_message_seq int
}

type backoffInfo struct {
	runningBackoff int
	currentBackoff int
	total_back_off int
	m              Message
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
		window_outorder:       make(map[int][]Message),
		message_dup_map:       make(map[int]map[int]bool),
		connection_dup_map:    make(map[string]bool),
		active_client_map:     make(map[int]int),
		message_sent:          make(map[int]bool),
		message_backoff:       make(map[messageID]*backoffInfo),
		close_pending:         make(chan int),
		close_read:            make(chan int),
		close_id:              -1,
		close_flag:            false,
		drop_id:               make(chan int, 1),
		params:                params,
	}

	go se.mainRoutine()
	go se.readRoutine()

	return &se, err
}

func (s *server) manageReadList() {
	if len(s.read_q) == 0 && s.readList.Len() > 0 {
		head := s.readList.Front()
		s.readList.Remove(head)
		message := head.Value.(Message)
		s.read_q <- message
	}
}

// handle message for each case
func (s *server) mainRoutine() {
	ticker := time.NewTicker(time.Duration(s.params.EpochMillis) * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case packet := <-s.packet_q:
			// Message seqNum
			sn := packet.message.SeqNum
			client := client_info{packetAddr: packet.packetAddr, connId: packet.message.ConnID}
			switch packet.message.Type {
			case MsgAck:
				if _, exist := s.active_client_map[client.connId]; exist {
					s.active_client_map[client.connId] = 0
				} else {
					continue
				}

				length := len(s.window_map[client.connId])
				s.window_map[client.connId] = removeFromSlice(s.window_map[client.connId], sn)
				if len(s.window_map[client.connId]) == length {
					//fmt.Println("Slice Find Error")
				}

				m_id := messageID{connID: client.connId, server_message_seq: sn}
				if _, exist := s.message_backoff[m_id]; exist {
					delete(s.message_backoff, m_id)
				} else {
					//fmt.Println("backoff map error")
					continue
				}

				var tmp []int

				for _, m := range s.window_outorder[client.connId] {
					if len(s.window_map[client.connId]) == 0 || (m.SeqNum < s.window_map[client.connId][0].SeqNum+s.params.WindowSize && len(s.window_map[client.connId]) < s.params.MaxUnackedMessages) {
						marMessage, _ := json.Marshal(m)
						_, err := s.listener.WriteToUDP(marMessage, client.packetAddr)

						if err != nil {
							fmt.Println(err)
						}
						tmp = append(tmp, m.SeqNum)

						s.message_backoff[messageID{connID: m.ConnID, server_message_seq: m.SeqNum}] = &backoffInfo{currentBackoff: 0, runningBackoff: 0, m: m, total_back_off: 0}
						s.window_map[client.connId] = append(s.window_map[client.connId], m)
						sort.Slice(s.window_map[client.connId], func(i, j int) bool {
							return s.window_map[client.connId][i].SeqNum < s.window_map[client.connId][j].SeqNum
						})

					}

				}
				for _, sn := range tmp {
					s.window_outorder[client.connId] = removeFromSlice(s.window_outorder[client.connId], sn)
				}
				if client.connId == s.close_id && len(s.window_map[client.connId])+len(s.window_outorder[client.connId]) == 0 {
					s.endConnection(s.close_id)
					s.close_id = -1

				}
				if s.close_flag {
					all_zero := true
					for _, v := range s.window_map {
						if len(v) != 0 {
							all_zero = false
							break
						}
					}
					for _, v := range s.window_outorder {
						if len(v) != 0 {
							all_zero = false
							break
						}
					}

					if all_zero {
						s.close_pending <- 1
						return

					}

				}

			case MsgCAck:
				if _, exist := s.active_client_map[client.connId]; exist {
					s.active_client_map[client.connId] = 0

				}

				length := len(s.window_map[client.connId])
				for _, m := range s.window_map[client.connId] {
					m_id := messageID{connID: m.ConnID, server_message_seq: m.SeqNum}
					if _, exist := s.message_backoff[m_id]; exist {
						delete(s.message_backoff, m_id)
					} else {
						continue

					}

				}
				s.window_map[client.connId] = removeFromSlice_CAck(s.window_map[client.connId], sn)
				if len(s.window_map[client.connId]) == length {
					//fmt.Println("Slice Find Error")
				}

				for _, m := range s.window_outorder[client.connId] {
					if len(s.window_map[client.connId]) == 0 || (m.SeqNum < s.window_map[client.connId][0].SeqNum+s.params.WindowSize && len(s.window_map[client.connId]) < s.params.MaxUnackedMessages) {
						marMessage, _ := json.Marshal(m)
						_, err := s.listener.WriteToUDP(marMessage, client.packetAddr)
						if err != nil {
							fmt.Println(err)
						}
						// update unack count
						s.window_map[client.connId] = append(s.window_map[client.connId], m)
						s.message_backoff[messageID{connID: m.ConnID, server_message_seq: m.SeqNum}] = &backoffInfo{currentBackoff: 0, runningBackoff: 0, m: m, total_back_off: 0}
						sort.Slice(s.window_map[client.connId], func(i, j int) bool {
							return s.window_map[client.connId][i].SeqNum < s.window_map[client.connId][j].SeqNum
						})

					}

				}
				if s.close_id != -1 && len(s.window_map[s.close_id])+len(s.window_outorder[s.close_id]) == 0 {
					s.endConnection(s.close_id)
					s.close_id = -1

				}

			case MsgConnect:

				//ip := *client.packetAddr

				ackMessage := *NewAck(s.client_id_counter, sn)
				ackMessageMar, err := json.Marshal(ackMessage)

				if err != nil {
					fmt.Println(err)
				}

				_, err = s.listener.WriteToUDP(ackMessageMar, client.packetAddr)

				if err != nil {
					fmt.Println(err)
				}
				if _, exist := s.connection_dup_map[packet.packetAddr.String()]; exist {
					continue
				}
				// initialize map
				s.connID_to_seq[s.client_id_counter] = &client_seq{packetAddr: packet.packetAddr, serverSeq: sn + 1}
				// message start from sn+1
				s.connid_to_readcounter[s.client_id_counter] = sn + 1
				s.connid_to_outorder[s.client_id_counter] = list.New()

				s.window_map[s.client_id_counter] = make([]Message, 0)
				s.window_outorder[s.client_id_counter] = make([]Message, 0)
				s.message_dup_map[s.client_id_counter] = make(map[int]bool)
				// mark connect this
				s.connection_dup_map[packet.packetAddr.String()] = true
				s.active_client_map[s.client_id_counter] = 0
				s.message_sent[s.client_id_counter] = false

				// add id counter
				s.client_id_counter++

			case MsgData:
				if _, exist := s.active_client_map[client.connId]; exist {
					s.active_client_map[client.connId] = 0
				}

				ackMessage := *NewAck(client.connId, sn)
				ackMessageMar, err := json.Marshal(ackMessage)
				if err != nil {
					fmt.Println(err)
				}
				_, err = s.listener.WriteToUDP(ackMessageMar, client.packetAddr)

				if _, exist := s.message_dup_map[client.connId][sn]; exist {
					continue
				}
				// mark receive, for dup reason
				s.message_dup_map[client.connId][sn] = true
				// make active_client become 0
				s.active_client_map[client.connId] = 0

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
		case messageToClient := <-s.server_message_q:
			serverMessageInfo, ok := s.connID_to_seq[messageToClient.connId]
			s.message_sent[messageToClient.connId] = true

			if ok {
				checksum := CalculateChecksum(messageToClient.connId, serverMessageInfo.serverSeq, len(messageToClient.playload), messageToClient.playload)
				message := *NewData(messageToClient.connId, serverMessageInfo.serverSeq, len(messageToClient.playload), messageToClient.playload, checksum)

				if len(s.window_map[messageToClient.connId]) == 0 || (message.SeqNum < s.window_map[messageToClient.connId][0].SeqNum+s.params.WindowSize && len(s.window_map[messageToClient.connId]) < s.params.MaxUnackedMessages) {
					marMessage, _ := json.Marshal(message)

					_, err := s.listener.WriteToUDP(marMessage, serverMessageInfo.packetAddr)

					if err != nil {
						fmt.Println(err)
					}
					s.message_backoff[messageID{connID: message.ConnID, server_message_seq: message.SeqNum}] = &backoffInfo{currentBackoff: 0, runningBackoff: 0, m: message, total_back_off: 0}

					// update unack count
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

		case <-ticker.C:

			// add epoch to every client state count
			for k, _ := range s.active_client_map {
				s.active_client_map[k]++
				// end the connection
				if s.active_client_map[k] >= s.params.EpochLimit {
					if !s.containID(s.readList, k) && s.connid_to_outorder[k].Len() == 0 {
						if len(s.drop_id) == 0 {
							s.drop_id <- k
							s.endConnection(k)
						}
					}

				}
			}

			// send heartbeat
			for k, v := range s.message_sent {
				if !v {
					ackMessage := *NewAck(k, 0)
					ackMessageMar, err := json.Marshal(ackMessage)
					if err != nil {
						fmt.Println(err)
					}

					_, err = s.listener.WriteToUDP(ackMessageMar, s.connID_to_seq[k].packetAddr)
					if err != nil {
						fmt.Println(err)
					}

				}
				s.message_sent[k] = false
			}

			for k, v := range s.message_backoff {
				if v.total_back_off == s.params.EpochLimit {
					s.endConnection(k.connID)
					continue
				}
				//if v.currentBackoff == s.params.MaxBackOffInterval {
				//	addr := s.connID_to_seq[k.connID].packetAddr
				//	marMessage, _ := json.Marshal(v.m)
				//	_, err := s.listener.WriteToUDP(marMessage, addr)
				//
				//	if err != nil {
				//		fmt.Println(err)
				//	}
				//	s.message_backoff[k].runningBackoff = 0
				//	continue
				//
				//}
				if v.runningBackoff >= v.currentBackoff {
					addr := s.connID_to_seq[k.connID].packetAddr
					marMessage, _ := json.Marshal(v.m)
					_, err := s.listener.WriteToUDP(marMessage, addr)

					if err != nil {
						fmt.Println(err)
					}
					if s.message_backoff[k].currentBackoff == 0 {
						s.message_backoff[k].currentBackoff++
					} else {
						s.message_backoff[k].currentBackoff *= 2
					}
					if s.message_backoff[k].currentBackoff > s.params.MaxBackOffInterval {
						s.message_backoff[k].currentBackoff = s.params.MaxBackOffInterval
					}

					s.message_backoff[k].runningBackoff = 0
					s.message_backoff[k].total_back_off++
					continue
				}
				if v.runningBackoff < v.currentBackoff {
					s.message_backoff[k].runningBackoff++
					s.message_backoff[k].total_back_off++
				}
			}

		case <-s.close_server:
			all_zero := true
			for _, v := range s.window_map {
				if len(v) != 0 {
					s.close_flag = true
					all_zero = false
					break
				}
			}
			for _, v := range s.window_outorder {
				if len(v) != 0 {
					s.close_flag = true
					all_zero = false
					break
				}
			}

			if all_zero {
				s.close_pending <- 1
				return
			}

		// close connection for the close client
		case connId := <-s.close_client:
			if len(s.window_map[connId])+len(s.window_outorder[connId]) == 0 {
				s.endConnection(connId)
			} else {
				s.close_id = connId
			}
		}
	}
	//return nil
}
func (s *server) containID(list2 *list.List, id int) bool {
	for element := list2.Front(); element != nil; element = element.Next() {
		if element.Value.(Message).ConnID == id {
			return true
		}
	}
	return false

}

func (s *server) endConnection(connId int) {
	delete(s.connection_dup_map, s.connID_to_seq[connId].packetAddr.String())
	var tmp []messageID

	for k, _ := range s.message_backoff {
		if k.connID == connId {
			tmp = append(tmp, k)
		}
	}
	for _, m_id := range tmp {
		delete(s.message_backoff, m_id)

	}

	delete(s.connID_to_seq, connId)
	delete(s.connid_to_readcounter, connId)
	delete(s.connid_to_outorder, connId)
	delete(s.window_map, connId)
	delete(s.window_outorder, connId)
	delete(s.active_client_map, connId)
	delete(s.message_dup_map, connId)
	delete(s.active_client_map, connId)
	delete(s.message_sent, connId)

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
		select {
		case <-s.close_read:
			return

		default:

			tmp := make([]byte, MAX_LENGTH)
			n, addr, err := s.listener.ReadFromUDP(tmp)

			if err != nil {
				//fmt.Println("listen error")
				return
			}

			var message Message
			err = json.Unmarshal(tmp[:n], &message)
			if err != nil {
				fmt.Println(err)
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
			p := packet_message{packetAddr: addr, message: message}
			s.packet_q <- p

		}

	}
}

// read message from server
func (s *server) Read() (int, []byte, error) {
	s.readRequest <- 1

	select {
	case message, ok := <-s.read_q:
		if !ok {
			return 0, nil, errors.New("server closed")
		}
		return message.ConnID, message.Payload, nil

	case id := <-s.drop_id:
		return id, nil, errors.New("Connection drop")
	}
}

func (s *server) Write(connId int, payload []byte) error {
	sp := server_packet{connId: connId, playload: payload}
	s.server_message_q <- sp
	return nil
}

func (s *server) CloseConn(connId int) error {
	s.close_client <- connId
	return nil
}

func (s *server) Close() error {
	s.close_server <- 1
	<-s.close_pending
	err := s.listener.Close()
	if err != nil {
		return err
	}
	return nil
}
