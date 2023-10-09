package lsp

import (
	"github.com/cmu440/lspnet"
	"sort"
)

func removeFromSlice(s []Message, seq int) []Message {
	for i, m := range s {
		if m.SeqNum == seq {
			return append(s[:i], s[i+1:]...)
		}
	}
	return s
}

func removeFromSliceCAck(s []Message, seq int) []Message {
	for i, m := range s {
		if m.SeqNum == seq {
			return s[i+1:]
		}
	}
	return s
}

type MessageId struct {
	connId int
	seqNum int
}

type BackOffInfo struct {
	runningBackoff int
	currentBackoff int
	totalBackOff   int
	message        Message
}

// packet message from client struct
// store message and connection address
type MessagePacket struct {
	packetAddr *lspnet.UDPAddr
	message    Message
}

// store client seq with address
type Client_seq struct {
	packetAddr *lspnet.UDPAddr
	serverSeq  int
}

// store client and connect id and c
type SendPackage struct {
	connId  int
	payload []byte
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
	if len(s.window) == 0 {
		return 0
	}
	return s.window[len(s.window)-1].SeqNum - s.window[0].SeqNum + 1
}
