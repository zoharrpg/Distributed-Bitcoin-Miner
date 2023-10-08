// Contains the implementation of a LSP server.

package lsp

import (
	"encoding/json"
	"errors"
	"log"
	"sort"
	"strconv"
	"time"

	"container/list"

	"github.com/cmu440/lspnet"
)

type server struct {
	listener         *lspnet.UDPConn      // server listener
	rawMessages      chan MessagePacket   // receive message from client channel
	closeMain        chan int             // close server signal
	closeClient      chan int             // close client signal
	readMessages     chan Message         // Read channel
	addrAndSeqNum    map[int]*Client_seq  // ID to client seq map, to get client seq by connId
	readCounters     map[int]int          // read counter for each connId
	outOfOrderList   map[int]*list.List   // outorder list for each connId
	windows          map[int][]Message    // slide window for each client
	windowOutOfOrder map[int][]Message    // slide window outorder list
	isMessageSent    map[int]bool         // in each epoch, True if there is message sent
	sendMessages     chan SendPackage     // send message from server queue channel for send message from server
	readList         *list.List           // store ordered message
	readRequest      chan int             // send read signal
	messageDupMap    map[int]map[int]bool // id to receive request seq
	connectionDupMap map[string]bool
	idleEpochElapsed map[int]int //connid to epoch
	messageBackoff   map[MessageId]*BackOffInfo
	closePending     chan int
	closeId          int
	isClosed         bool
	droppedId        chan int
	params           *Params
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
		listener:         listener,
		rawMessages:      make(chan MessagePacket),
		closeMain:        make(chan int),
		closeClient:      make(chan int),
		readMessages:     make(chan Message, 1),
		addrAndSeqNum:    make(map[int]*Client_seq),
		sendMessages:     make(chan SendPackage),
		readCounters:     make(map[int]int),
		outOfOrderList:   make(map[int]*list.List),
		readList:         list.New(),
		readRequest:      make(chan int),
		windows:          make(map[int][]Message),
		windowOutOfOrder: make(map[int][]Message),
		messageDupMap:    make(map[int]map[int]bool),
		connectionDupMap: make(map[string]bool),
		idleEpochElapsed: make(map[int]int),
		isMessageSent:    make(map[int]bool),
		messageBackoff:   make(map[MessageId]*BackOffInfo),
		closePending:     make(chan int),
		closeId:          -1,
		isClosed:         false,
		droppedId:        make(chan int, 1),
		params:           params,
	}

	go se.mainRoutine()
	go se.readRoutine()

	return &se, err
}

func (s *server) manageReadList() {
	if len(s.readMessages) == 0 && s.readList.Len() > 0 {
		head := s.readList.Front()
		s.readList.Remove(head)
		message := head.Value.(Message)
		s.readMessages <- message
	}
}

func (s *server) checkClosed() bool {
	isCleared := true
	for _, v := range s.windows {
		if len(v) != 0 {
			isCleared = false
			break
		}
	}
	for _, v := range s.windowOutOfOrder {
		if len(v) != 0 {
			isCleared = false
			break
		}
	}

	if isCleared {
		s.closePending <- 1
		return true
	}
	return false
}

// handle message for each case
func (s *server) mainRoutine() {
	ticker := time.NewTicker(time.Duration(s.params.EpochMillis) * time.Millisecond)
	clientIdCounter := 1
	defer ticker.Stop()
	for {
		select {
		case packet := <-s.rawMessages:
			message := packet.message
			sn := message.SeqNum
			messageAddr := packet.packetAddr
			connId := message.ConnID
			switch packet.message.Type {
			case MsgAck:
				if _, exist := s.idleEpochElapsed[connId]; exist {
					s.idleEpochElapsed[connId] = 0
				} else {
					continue // TODO: check if this can be removed
				}

				s.windows[connId] = removeFromSlice(s.windows[connId], sn)
				messageId := MessageId{connId: connId, seqNum: sn}
				if _, exist := s.messageBackoff[messageId]; exist {
					delete(s.messageBackoff, messageId)
				} else {
					continue
				}

				var tmp []int
				for _, m := range s.windowOutOfOrder[connId] {
					if len(s.windows[connId]) == 0 || (m.SeqNum < s.windows[connId][0].SeqNum+s.params.WindowSize && len(s.windows[connId]) < s.params.MaxUnackedMessages) {
						marMessage, _ := json.Marshal(m)
						_, err := s.listener.WriteToUDP(marMessage, messageAddr)
						if err != nil {
							log.Println(err)
						}

						tmp = append(tmp, m.SeqNum)
						s.messageBackoff[MessageId{m.ConnID, m.SeqNum}] = &BackOffInfo{0, 0, 0, m}
						s.windows[connId] = append(s.windows[connId], m)
						sort.Slice(s.windows[connId], func(i, j int) bool {
							return s.windows[connId][i].SeqNum < s.windows[connId][j].SeqNum
						})
					}
				}
				for _, seqNum := range tmp {
					s.windowOutOfOrder[connId] = removeFromSlice(s.windowOutOfOrder[connId], seqNum)
				}
				if connId == s.closeId && len(s.windows[connId])+len(s.windowOutOfOrder[connId]) == 0 {
					s.endConnection(s.closeId)
					s.closeId = -1

				}

				if s.isClosed && s.checkClosed() {
					return
				}

			case MsgCAck:
				if _, exist := s.idleEpochElapsed[connId]; exist {
					s.idleEpochElapsed[connId] = 0

				}

				for _, m := range s.windows[connId] {
					m_id := MessageId{connId: m.ConnID, seqNum: m.SeqNum}
					if _, exist := s.messageBackoff[m_id]; exist {
						delete(s.messageBackoff, m_id)
					} else {
						continue
					}
				}

				s.windows[connId] = removeFromSliceCAck(s.windows[connId], sn)

				for _, m := range s.windowOutOfOrder[connId] {
					if len(s.windows[connId]) == 0 || (m.SeqNum < s.windows[connId][0].SeqNum+s.params.WindowSize && len(s.windows[connId]) < s.params.MaxUnackedMessages) {
						marMessage, _ := json.Marshal(m)
						_, err := s.listener.WriteToUDP(marMessage, messageAddr)
						if err != nil {
							log.Println(err)
						}
						// update unack count
						s.windows[connId] = append(s.windows[connId], m)
						s.messageBackoff[MessageId{connId: m.ConnID, seqNum: m.SeqNum}] = &BackOffInfo{0, 0, 0, m}
						sort.Slice(s.windows[connId], func(i, j int) bool {
							return s.windows[connId][i].SeqNum < s.windows[connId][j].SeqNum
						})
					}
				}

				if s.closeId != -1 && len(s.windows[s.closeId])+len(s.windowOutOfOrder[s.closeId]) == 0 {
					s.endConnection(s.closeId)
					s.closeId = -1
				}

				if s.isClosed && s.checkClosed() {
					return
				}

			case MsgConnect:
				ackMessage := *NewAck(clientIdCounter, sn)
				ackMessageMar, err := json.Marshal(ackMessage)
				if err != nil {
					log.Println(err)
				}

				_, err = s.listener.WriteToUDP(ackMessageMar, messageAddr)
				if err != nil {
					log.Println(err)
				}
				if _, exist := s.connectionDupMap[packet.packetAddr.String()]; exist {
					continue
				}
				// initialize map
				s.addrAndSeqNum[clientIdCounter] = &Client_seq{packet.packetAddr, sn + 1}
				// message start from sn+1
				s.readCounters[clientIdCounter] = sn + 1
				s.outOfOrderList[clientIdCounter] = list.New()

				s.windows[clientIdCounter] = make([]Message, 0)
				s.windowOutOfOrder[clientIdCounter] = make([]Message, 0)
				s.messageDupMap[clientIdCounter] = make(map[int]bool)
				// mark connect this
				s.connectionDupMap[packet.packetAddr.String()] = true
				s.idleEpochElapsed[clientIdCounter] = 0
				s.isMessageSent[clientIdCounter] = false

				// add id counter
				clientIdCounter++

			case MsgData:
				if _, exist := s.idleEpochElapsed[connId]; exist {
					s.idleEpochElapsed[connId] = 0
				}

				ackMessage := *NewAck(connId, sn)
				ackMessageMar, err := json.Marshal(ackMessage)
				if err != nil {
					log.Println(err)
				}
				_, err = s.listener.WriteToUDP(ackMessageMar, messageAddr)

				if _, exist := s.messageDupMap[connId][sn]; exist {
					continue
				}
				// mark receive, for dup reason
				s.messageDupMap[connId][sn] = true
				// make active_client become 0
				s.idleEpochElapsed[connId] = 0

				if err != nil {
					log.Println(err)
				}

				// if received message match the read counter, just add it to read_list
				if sn == s.readCounters[connId] {
					s.readList.PushBack(packet.message)
					s.readCounters[connId]++
					// check if there is an element in out-of-order list that match the seq number after
					element := s.findElement(connId, s.readCounters[connId])
					// add element until there is no element in out-of-order list that match seq number
					for element != nil {
						s.readList.PushBack(element.Value.(Message))
						s.readCounters[connId]++
						element = s.findElement(connId, s.readCounters[connId])
					}
				} else {
					if s.outOfOrderList[connId] == nil {
						s.outOfOrderList[connId] = list.New()
					}
					// out-of-order element
					s.outOfOrderList[connId].PushBack(packet.message)
				}
				s.manageReadList()
			}
		case <-s.readRequest:
			s.manageReadList()
		case sendPackage := <-s.sendMessages:
			connId := sendPackage.connId
			serverMessageInfo, ok := s.addrAndSeqNum[connId]

			if ok {
				seqNum := serverMessageInfo.serverSeq
				checksum := CalculateChecksum(connId, seqNum, len(sendPackage.payload), sendPackage.payload)
				message := *NewData(connId, seqNum, len(sendPackage.payload), sendPackage.payload, checksum)

				if len(s.windows[connId]) == 0 || (seqNum < s.windows[connId][0].SeqNum+s.params.WindowSize && len(s.windows[connId]) < s.params.MaxUnackedMessages) {
					marMessage, _ := json.Marshal(message)

					_, err := s.listener.WriteToUDP(marMessage, serverMessageInfo.packetAddr)

					if err != nil {
						log.Println(err)
					}
					s.isMessageSent[connId] = true
					s.messageBackoff[MessageId{connId, seqNum}] = &BackOffInfo{0, 0, 0, message}

					// update unack count
					s.windows[connId] = append(s.windows[connId], message)
					sort.Slice(s.windows[connId], func(i, j int) bool {
						return s.windows[connId][i].SeqNum < s.windows[connId][j].SeqNum
					})
				} else {
					s.windowOutOfOrder[connId] = append(s.windowOutOfOrder[connId], message)
					sort.Slice(s.windowOutOfOrder[connId], func(i, j int) bool {
						return s.windowOutOfOrder[connId][i].SeqNum < s.windowOutOfOrder[connId][j].SeqNum
					})
				}
				// update seq id
				s.addrAndSeqNum[connId].serverSeq++
			}

		case <-ticker.C:
			// add epoch to every client state count
			for k, _ := range s.idleEpochElapsed {
				s.idleEpochElapsed[k]++
				// end the connection
				if s.idleEpochElapsed[k] >= s.params.EpochLimit {
					if !s.containID(s.readList, k) && s.outOfOrderList[k].Len() == 0 {
						if len(s.droppedId) == 0 {
							s.droppedId <- k
							s.endConnection(k)
						}
					}

				}
			}

			// send heartbeat
			for k, v := range s.isMessageSent {
				if !v {
					ackMessage := *NewAck(k, 0)
					ackMessageMar, err := json.Marshal(ackMessage)
					if err != nil {
						log.Println(err)
					}

					_, err = s.listener.WriteToUDP(ackMessageMar, s.addrAndSeqNum[k].packetAddr)
					if err != nil {
						log.Println(err)
					}

				}
				s.isMessageSent[k] = false
			}

			for k, v := range s.messageBackoff {
				if v.totalBackOff == s.params.EpochLimit {
					s.endConnection(k.connId)
					continue
				}
				if v.runningBackoff >= v.currentBackoff {
					addr := s.addrAndSeqNum[k.connId].packetAddr
					marMessage, _ := json.Marshal(v.message)
					_, err := s.listener.WriteToUDP(marMessage, addr)

					if err != nil {
						log.Println(err)
					}
					if s.messageBackoff[k].currentBackoff == 0 {
						s.messageBackoff[k].currentBackoff++
					} else {
						s.messageBackoff[k].currentBackoff *= 2
					}
					if s.messageBackoff[k].currentBackoff > s.params.MaxBackOffInterval {
						s.messageBackoff[k].currentBackoff = s.params.MaxBackOffInterval
					}

					s.messageBackoff[k].runningBackoff = 0
					s.messageBackoff[k].totalBackOff++
					continue
				}
				s.messageBackoff[k].runningBackoff++
				s.messageBackoff[k].totalBackOff++
			}

		case <-s.closeMain:
			s.isClosed = true
			if s.checkClosed() {
				return
			}

		// close connection for the close client
		case connId := <-s.closeClient:
			if len(s.windows[connId])+len(s.windowOutOfOrder[connId]) == 0 {
				s.endConnection(connId)
			} else {
				s.closeId = connId
			}
		}
	}
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
	delete(s.connectionDupMap, s.addrAndSeqNum[connId].packetAddr.String())
	var tmp []MessageId

	for k, _ := range s.messageBackoff {
		if k.connId == connId {
			tmp = append(tmp, k)
		}
	}
	for _, m_id := range tmp {
		delete(s.messageBackoff, m_id)

	}

	delete(s.addrAndSeqNum, connId)
	delete(s.readCounters, connId)
	delete(s.outOfOrderList, connId)
	delete(s.windows, connId)
	delete(s.windowOutOfOrder, connId)
	delete(s.idleEpochElapsed, connId)
	delete(s.messageDupMap, connId)
	delete(s.isMessageSent, connId)

}

// findElement find next element in out order list that match seq number
func (s *server) findElement(connId int, seq int) *list.Element {
	for message := s.outOfOrderList[connId].Front(); message != nil; message = message.Next() {
		if message.Value.(Message).SeqNum == seq {
			s.outOfOrderList[connId].Remove(message)
			return message
		}
	}
	return nil
}

// read message from client
func (s *server) readRoutine() {
	readMessage := make([]byte, MAX_LENGTH)
	for {
		n, addr, err := s.listener.ReadFromUDP(readMessage)
		if err != nil {
			log.Println(err)
			return
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
		p := MessagePacket{packetAddr: addr, message: message}
		s.rawMessages <- p
	}
}

// read message from server
func (s *server) Read() (int, []byte, error) {
	s.readRequest <- 1
	select {
	case message, ok := <-s.readMessages:
		if !ok {
			return 0, nil, errors.New("server closed")
		}
		return message.ConnID, message.Payload, nil

	case id := <-s.droppedId:
		return id, nil, errors.New("connection drop")
	}
}

func (s *server) Write(connId int, payload []byte) error {
	sp := SendPackage{connId: connId, payload: payload}
	s.sendMessages <- sp
	return nil
}

func (s *server) CloseConn(connId int) error {
	s.closeClient <- connId
	return nil
}

func (s *server) Close() error {
	s.closeMain <- 1          // signal main routine to close
	<-s.closePending          // wait for all connection to close
	err := s.listener.Close() // signal read routine to close
	if err != nil {
		return err
	}
	return nil
}
