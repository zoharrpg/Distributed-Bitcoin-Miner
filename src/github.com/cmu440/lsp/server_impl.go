// Contains the implementation of a LSP server.

package lsp

import (
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"time"

	"container/list"

	"github.com/cmu440/lspnet"
)

// The server type represents a server in a network that handles incoming messages from clients and
// manages various channels and data structures for message handling.
type server struct {
	listener         *lspnet.UDPConn            // server listener
	rawMessages      chan MessagePacket         // receive message from client channel
	closeMain        chan int                   // close server signal
	closeClient      chan int                   // close client signal
	readMessages     chan Message               // Read channel
	addrAndSeqNum    map[int]*Client_seq        // ID to client seq map, to get client seq by connId
	readCounters     map[int]int                // read counter for each connId
	outOfOrderList   map[int]*list.List         // outorder list for each connId
	windows          map[int][]Message          // slide window for each client
	windowOutOfOrder map[int][]Message          // slide window outorder list
	isMessageSent    map[int]bool               // in each epoch, True if there is message sent
	sendMessages     chan SendPackage           // send message from server queue channel for send message from server
	readList         *list.List                 // store ordered message
	readRequest      chan int                   // send read signal
	messageDupMap    map[int]map[int]bool       // id to receive request seq
	connectionDupMap map[string]bool            // // The above code is declaring a variable called `connectionDupMap` which is a map with string keys and boolean values.
	idleEpochElapsed map[int]int                //connid to epoch
	messageBackoff   map[MessageId]*BackOffInfo // The above code is declaring a variable named "messageBackoff" which is a map with keys of type "MessageId" and values of type "*BackOffInfo".
	closePending     chan int                   // wait for message sent and acknowledgement.
	closeId          int                        // a variable called "closeId" of type int.
	isClosed         bool                       // a boolean variable named "isClosed" and initializing it to false.
	droppedId        chan int                   // drop connection id channel
	params           *Params                    // params for server
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

// The above code is a method called `manageReadList` in a server struct. It checks if the length of
// the `readMessages` slice is 0 and if the `readList` linked list has any elements. If both conditions
// are true, it removes the first element from the `readList` linked list, assigns it to the variable
// `message`, and sends it to the `readMessages` channel.
func (s *server) manageReadList() {
	if len(s.readMessages) == 0 && s.readList.Len() > 0 {
		head := s.readList.Front()
		s.readList.Remove(head)
		message := head.Value.(Message)
		s.readMessages <- message
	}
}

// The above code is defining a method called `checkClosed` for a struct called `server`. This method
// checks if all the windows and windowOutOfOrder slices in the `server` struct are empty. If they are
// empty, it sends a value of 1 to the `closePending` channel and returns true. Otherwise, it returns
// false.
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

// The above code is the main routine of a server in a network communication system. It handles various
// types of messages received from clients and performs corresponding actions.
func (s *server) mainRoutine() {
	// The above code is creating a new ticker in Go. A ticker is a channel-based timer that will send a
	// value periodically based on the specified duration. In this case, the ticker will send a value
	// every `s.params.EpochMillis` milliseconds.
	ticker := time.NewTicker(time.Duration(s.params.EpochMillis) * time.Millisecond)
	// The above code is declaring a variable called `clientIdCounter` and initializing it with a value of 1
	clientIdCounter := 1
	// The above code is using the `defer` keyword to schedule the `Stop()` method of the `ticker` object
	// to be called when the surrounding function returns. This is typically used to ensure that resources
	// are properly cleaned up or released at the end of a function, regardless of how the function exits
	// (e.g. through a return statement or an error).
	defer ticker.Stop()
	for {
		select {
		// The above code is handling different types of messages received by a server in a Go program.
		case packet := <-s.rawMessages:
			message := packet.message
			sn := message.SeqNum
			messageAddr := packet.packetAddr
			connId := message.ConnID
			switch packet.message.Type {
			// The above code is handling the MsgAck case in a switch statement. It performs the following
			// actions:
			case MsgAck:
				// The above code is checking if a key `connId` exists in the `idleEpochElapsed` map. If the key
				// exists, it sets the value associated with that key to 0. If the key does not exist, it continues
				// to the next iteration of the loop.
				if _, exist := s.idleEpochElapsed[connId]; exist {
					s.idleEpochElapsed[connId] = 0
				} else {
					continue
				}

				// The above code is removing an element from a slice and deleting a key-value pair from a map.
				s.windows[connId] = removeFromSlice(s.windows[connId], sn)
				messageId := MessageId{connId: connId, seqNum: sn}
				if _, exist := s.messageBackoff[messageId]; exist {
					delete(s.messageBackoff, messageId)
				} else {
					continue
				}

				// The above code is part of a function in the Go programming language. It is iterating over a
				// slice of messages in `s.windowOutOfOrder[connId]`. For each message, it checks if the
				// `s.windows[connId]` slice is empty or if the message's sequence number is within the window size
				// and the number of unacknowledged messages is less than the maximum allowed. If the conditions
				// are met, it marshals the message into JSON, writes it to a UDP connection, and updates various
				// data structures and variables.
				var tmp []int
				for _, m := range s.windowOutOfOrder[connId] {
					if len(s.windows[connId]) == 0 || (m.SeqNum < s.windows[connId][0].SeqNum+s.params.WindowSize && len(s.windows[connId]) < s.params.MaxUnackedMessages) {
						marMessage, _ := json.Marshal(m)
						_, err := s.listener.WriteToUDP(marMessage, messageAddr)
						if err != nil {
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

			// The above code is handling the MsgCAck case in a switch statement. It performs the following
			// actions:
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

				// The above code is removing an element from a slice called `s.windows[connId]`. The element being
				// removed is determined by the value of `sn`.
				s.windows[connId] = removeFromSliceCAck(s.windows[connId], sn)

				// The above code is iterating over messages in the `windowOutOfOrder` slice for a specific
				// `connId`. It checks if the `windows` slice for that `connId` is empty or if the current
				// message's sequence number is within the window range and the number of unacknowledged messages
				// is less than the maximum allowed. If the conditions are met, it marshals the message into JSON,
				// writes it to the UDP listener, updates the `windows` slice and `messageBackoff` map, and sorts
				// the `windows` slice based on sequence number.
				for _, m := range s.windowOutOfOrder[connId] {
					if len(s.windows[connId]) == 0 || (m.SeqNum < s.windows[connId][0].SeqNum+s.params.WindowSize && len(s.windows[connId]) < s.params.MaxUnackedMessages) {
						marMessage, _ := json.Marshal(m)
						_, err := s.listener.WriteToUDP(marMessage, messageAddr)
						if err != nil {
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
				// The above code is sending an acknowledgment message to a client. It first creates an
				// acknowledgment message using the client ID counter and sequence number. Then, it marshals the
				// acknowledgment message into JSON format. Next, it writes the marshaled acknowledgment message to
				// the UDP listener. If the write operation is successful, it initializes various data structures
				// and maps related to the client's connection. Finally, it increments the client ID counter.
				ackMessage := *NewAck(clientIdCounter, sn)
				ackMessageMar, err := json.Marshal(ackMessage)
				if err != nil {
				}

				_, err = s.listener.WriteToUDP(ackMessageMar, messageAddr)
				if err != nil {
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
				// The above code is handling the reception of messages in a network communication system. Here is
				// a breakdown of what it does:

				// The above code is checking if a key `connId` exists in the `s.idleEpochElapsed` map. If the key
				// exists, it sets the value associated with that key to 0.
				if _, exist := s.idleEpochElapsed[connId]; exist {
					s.idleEpochElapsed[connId] = 0
				}

				// The above code is sending an acknowledgment message to a specific UDP address.
				ackMessage := *NewAck(connId, sn)
				ackMessageMar, err := json.Marshal(ackMessage)
				if err != nil {
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
		// The above code is using a select statement to wait for a read request on the channel
		// `s.readRequest`. Once a read request is received, it calls the `manageReadList()` function.
		case <-s.readRequest:
			s.manageReadList()
		case sendPackage := <-s.sendMessages:
			connId := sendPackage.connId
			serverMessageInfo, ok := s.addrAndSeqNum[connId]

			if ok {
				seqNum := serverMessageInfo.serverSeq
				checksum := CalculateChecksum(connId, seqNum, len(sendPackage.payload), sendPackage.payload)
				message := *NewData(connId, seqNum, len(sendPackage.payload), sendPackage.payload, checksum)

				// check window

				if len(s.windows[connId]) == 0 || (seqNum < s.windows[connId][0].SeqNum+s.params.WindowSize && len(s.windows[connId]) < s.params.MaxUnackedMessages) {
					marMessage, _ := json.Marshal(message)

					_, err := s.listener.WriteToUDP(marMessage, serverMessageInfo.packetAddr)

					if err != nil {
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
			for k := range s.idleEpochElapsed {
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
					}

					_, err = s.listener.WriteToUDP(ackMessageMar, s.addrAndSeqNum[k].packetAddr)
					if err != nil {
					}

				}
				s.isMessageSent[k] = false
			}

			// backoff timer

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

// The above code is defining a method called `containID` for a struct `server`. This method takes a
// pointer to a linked list `list2` and an integer `id` as parameters.
func (s *server) containID(list2 *list.List, id int) bool {
	for element := list2.Front(); element != nil; element = element.Next() {
		if element.Value.(Message).ConnID == id {
			return true
		}
	}
	return false
}

// The above code is defining a method called "endConnection" for a struct called "server". This method
// takes an integer parameter called "connId".
func (s *server) endConnection(connId int) {
	delete(s.connectionDupMap, s.addrAndSeqNum[connId].packetAddr.String())
	var tmp []MessageId

	for k := range s.messageBackoff {
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

// The above code is defining a method called `findElement` for a struct called `server`. This method
// takes two parameters: `connId` of type `int` and `seq` of type `int`. The method returns a pointer
// to a `list.Element`.
func (s *server) findElement(connId int, seq int) *list.Element {
	for message := s.outOfOrderList[connId].Front(); message != nil; message = message.Next() {
		if message.Value.(Message).SeqNum == seq {
			s.outOfOrderList[connId].Remove(message)
			return message
		}
	}
	return nil
}

// The above code is implementing a read routine for a server in Go. It listens for incoming UDP
// messages using a UDP listener. When a message is received, it is unmarshalled from JSON into a
// Message struct. If the message type is MsgData, it checks the payload size and verifies the
// checksum. If the payload size is larger than the actual payload, it discards the message. If the
// payload size is smaller, it truncates the payload to the correct size. Finally, it sends the
// received message along with the packet address to a channel for further processing.
func (s *server) readRoutine() {
	readMessage := make([]byte, MAX_LENGTH)
	for {
		n, addr, err := s.listener.ReadFromUDP(readMessage)
		if err != nil {
			return
		}

		var message Message
		err = json.Unmarshal(readMessage[:n], &message)
		if err != nil {
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

// The above code is defining a method called `Read()` for a server object. This method is used to read
// messages from the server.
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

// The above code is defining a method called "Write" for a server struct in the Go programming
// language. This method takes two parameters: "connId" of type int and "payload" of type []byte.
func (s *server) Write(connId int, payload []byte) error {
	sp := SendPackage{connId: connId, payload: payload}
	s.sendMessages <- sp
	return nil
}

// The above code is defining a method called CloseConn on a struct called server. This method takes an
// integer parameter called connId and returns an error. Inside the method, it sends the connId to a
// channel called closeClient.
func (s *server) CloseConn(connId int) error {
	s.closeClient <- connId
	return nil
}

// The above code is defining a method called `Close` for a server object. This method is responsible
// for closing the server and all its connections.
func (s *server) Close() error {
	s.closeMain <- 1          // signal main routine to close
	<-s.closePending          // wait for all connection to close
	err := s.listener.Close() // signal read routine to close
	if err != nil {
		return err
	}
	return nil
}
