package main

import (
	"encoding/json"
	"fmt"
	"github.com/cmu440/bitcoin"
	"log"
	"os"
	"sort"
	"strconv"

	"github.com/cmu440/lsp"
)

const (
	chunkSize      = 10000
	initialListLen = 0
	channelSize    = 1
)

type result struct {
	nonce uint64
	hash  uint64
}

type resultInfo struct {
	minerId int
	result  result
}

type ByHash []result

func (a ByHash) Len() int           { return len(a) }
func (a ByHash) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a ByHash) Less(i, j int) bool { return a[i].hash < a[j].hash }

type job struct {
	lower uint64
	upper uint64
}

type jobInfo struct {
	requestId int
	job       job
}

type reassignJob struct {
	requestId int
	minerId   int
}

type requestMap struct {
	clientId int
	message  string
	results  []result
	totalNum int // total number of jobs
}

type clientRequest struct {
	requestId     int
	remainingJobs []job
}

type request struct {
	requestId int
	clientId  int
	message   bitcoin.Message
}

type ByRemainingJobs []clientRequest

func (slice ByRemainingJobs) Len() int      { return len(slice) }
func (slice ByRemainingJobs) Swap(i, j int) { slice[i], slice[j] = slice[j], slice[i] }

// Less
// LOAD BALANCING POLICY:
// Shortest Remaining Time First
func (slice ByRemainingJobs) Less(i, j int) bool {
	// If the server gets multiple requests of similar length, the requests should finish approximately in the order they arrived.
	// For example, if 100 identical requests come in and the 100th request finishes first, that would not be very fair.
	iLen := len(slice[i].remainingJobs)
	jLen := len(slice[j].remainingJobs)
	if iLen == jLen {
		return slice[i].requestId < slice[j].requestId
	}

	// If the server gets an extremely large request followed by a small request, we expect
	// that the small request will finish before the large request
	// Shortest Remaining Time First
	return iLen < jLen
}

func (slice ByRemainingJobs) findByRequestId(requestId int) int {
	for i, req := range slice {
		if req.requestId == requestId {
			return i
		}
	}
	return -1
}

// Server
// An LSP server that manages the entire Bitcoin cracking enterprise.
type server struct {
	lspServer       lsp.Server
	requestChan     chan request
	requestList     []clientRequest    // request list
	requestInfo     map[int]requestMap // request id -> request map
	freeMiners      []int              // FIFO
	busyMiners      map[int]jobInfo    // miner id -> job
	freeMinerChan   chan int
	reassignJobChan chan reassignJob
	resultChan      chan resultInfo
	closeClientChan chan int
	closeMinerChan  chan int
}

func startServer(port int) (*server, error) {
	params := lsp.NewParams()
	lspServer, err := lsp.NewServer(port, params)
	srv := &server{
		lspServer:       lspServer,
		requestChan:     make(chan request, channelSize),
		requestList:     make([]clientRequest, initialListLen),
		requestInfo:     make(map[int]requestMap),
		freeMiners:      make([]int, initialListLen),
		busyMiners:      make(map[int]jobInfo),
		freeMinerChan:   make(chan int, channelSize),
		reassignJobChan: make(chan reassignJob, channelSize),
		resultChan:      make(chan resultInfo, channelSize),
		closeClientChan: make(chan int, channelSize),
		closeMinerChan:  make(chan int, channelSize),
	}
	return srv, err
}

var LOGF *log.Logger

func main() {
	// You may need a logger for debug purpose
	const (
		name = "serverLog.txt"
		flag = os.O_RDWR | os.O_CREATE
		perm = os.FileMode(0666)
	)

	file, err := os.OpenFile(name, flag, perm)
	if err != nil {
		return
	}
	defer file.Close()

	LOGF = log.New(file, "", log.Lshortfile|log.Lmicroseconds)
	// Usage: LOGF.Println() or LOGF.Printf()

	const numArgs = 2
	if len(os.Args) != numArgs {
		fmt.Printf("Usage: ./%s <port>", os.Args[0])
		return
	}

	port, err := strconv.Atoi(os.Args[1])
	if err != nil {
		fmt.Println("Port must be a number:", err)
		return
	}

	srv, err := startServer(port)
	if err != nil {
		fmt.Println(err.Error())
		return
	}
	fmt.Println("Server listening on port", port)

	defer srv.lspServer.Close()

	requestId := 0

	go srv.scheduler()

	for {
		connId, payload, err := srv.lspServer.Read()
		if err != nil {
			index := -1
			for i, miner := range srv.freeMiners {
				if miner == connId {
					index = i
					break
				}
			}
			if index != -1 {
				srv.closeMinerChan <- index
			} else if _, ok := srv.busyMiners[connId]; ok {
				// When the server loses contact with a miner, it should reassign any job that the worker was handling
				// to a different worker.
				srv.reassignJobChan <- reassignJob{srv.busyMiners[connId].requestId, connId}
			} else {
				// When the server loses contact with a request client, it should cease working on any requests being
				// done on behalf of the client (you need not forcibly terminate a job on a miner—just wait
				// for it to complete and ignore its results).
				srv.closeClientChan <- connId
			}
			continue
		}
		var msg bitcoin.Message
		err = json.Unmarshal(payload, &msg)
		if err != nil {
			continue
		}
		switch msg.Type {
		case bitcoin.Join:
			// At any time, the server can have any number of workers available
			// A miner joins the server.
			srv.freeMinerChan <- connId
		case bitcoin.Request:
			// and can receive any number of requests from any number of clients.
			srv.requestChan <- request{requestId, connId, msg}
			requestId++
		case bitcoin.Result:
			srv.resultChan <- resultInfo{connId, result{msg.Nonce, msg.Hash}}
		}
	}
}

func (srv *server) write2client(connId int, message *bitcoin.Message) {
	payload, err := json.Marshal(*message)
	if err != nil {
		return
	}
	err = srv.lspServer.Write(connId, payload)
	if err != nil {
		return
	}
}

func (srv *server) removeRequest(requestId int) {
	delete(srv.requestInfo, requestId)
	index := ByRemainingJobs(srv.requestList).findByRequestId(requestId)
	if index == -1 {
		return
	}
	srv.requestList = append(srv.requestList[:index], srv.requestList[index+1:]...)
}

// scheduler
// aims to minimize mean response time for all client requests.
// requests that arrive earlier have some sort of priority.
func (srv *server) scheduler() {
	for {
		select {
		case minerId := <-srv.freeMinerChan:
			// A miner joins the server.
			srv.freeMiners = append(srv.freeMiners, minerId)

		case index := <-srv.closeMinerChan:
			// remove the miner from freeMiners
			srv.freeMiners = append(srv.freeMiners[:index], srv.freeMiners[index+1:]...)

		case reassignJob := <-srv.reassignJobChan:
			// find the request from requestList
			index := ByRemainingJobs(srv.requestList).findByRequestId(reassignJob.requestId)
			if index == -1 {
				// the corresponding client has been disconnected
				continue
			}

			// place the job back to list
			job := srv.busyMiners[reassignJob.minerId].job
			srv.requestList[index].remainingJobs = append(srv.requestList[index].remainingJobs, job)
			sort.Sort(ByRemainingJobs(srv.requestList))

			// remove the job from the previous miner
			delete(srv.busyMiners, reassignJob.minerId)

		case clientId := <-srv.closeClientChan:
			// remove the requests from requestList
			/*
				requestIds := make([]int, initialListLen)
				for reqId, reqMap := range srv.requestInfo {
					if reqMap.clientId == clientId {
						requestIds = append(requestIds, reqId)
					}
				}
				for _, requestId := range requestIds {
					index := ByRemainingJobs(srv.requestList).findByRequestId(requestId)
					srv.requestList = append(srv.requestList[:index], srv.requestList[index+1:]...)
					delete(srv.requestInfo, requestId)
				}
			*/
			requestId := -1
			for reqId, reqMap := range srv.requestInfo {
				if reqMap.clientId == clientId {
					requestId = reqId
					break
				}
			}
			if requestId == -1 {
				continue
			}
			srv.removeRequest(requestId)

		case req := <-srv.requestChan:
			// A client sends a request to the server.
			msg := req.message
			// For each client request, it splits the request into more manageable-sized jobs
			var jobs []job
			current := msg.Lower
			for current+chunkSize < msg.Upper {
				jobs = append(jobs, job{current, current + chunkSize})
				current += chunkSize
			}
			jobs = append(jobs, job{current, msg.Upper})

			srv.requestList = append(srv.requestList, clientRequest{req.requestId, jobs})
			srv.requestInfo[req.requestId] = requestMap{req.clientId, msg.Data, make([]result, initialListLen), len(jobs)}

		case res := <-srv.resultChan:
			// append the res
			requestId := srv.busyMiners[res.minerId].requestId
			if _, ok := srv.requestInfo[requestId]; !ok {
				continue
			}
			// Get a copy of the requestMap
			reqMap := srv.requestInfo[requestId]
			reqMap.results = append(reqMap.results, res.result)
			srv.requestInfo[requestId] = reqMap

			delete(srv.busyMiners, res.minerId)
			// Work should be divided roughly evenly across workers. Try to minimize the idle time of each worker.
			srv.freeMiners = append(srv.freeMiners, res.minerId)

			// The server waits for each worker to respond before generating and sending the final result back to the client.
			// Once the miner exhausts all possible nonces, it determines the least hash value and
			// its corresponding nonce and sends back the final result: [Result minHash nonce]
			if len(srv.requestInfo[requestId].results) == srv.requestInfo[requestId].totalNum {
				// sort the results
				requestMap := srv.requestInfo[requestId]
				sort.Sort(ByHash(requestMap.results))

				result := requestMap.results[0]
				// send the result back to the client
				srv.write2client(requestMap.clientId, bitcoin.NewResult(result.hash, result.nonce))

				// remove the request from requestList
				srv.removeRequest(requestId)
			}
		default:
			if len(srv.requestList) == 0 {
				continue
			}
			if len(srv.requestList[0].remainingJobs) == 0 {
				continue
			}
			// If there are no available miners left, the server should wait for a new miner to join
			// before reassigning the old miner’s job.
			if len(srv.freeMiners) == 0 {
				continue
			}
			// LOAD BALANCING POLICY:
			// FIFO for miner
			miner := srv.freeMiners[0]
			srv.freeMiners = srv.freeMiners[1:]

			// assign the job to the miner
			req := srv.requestList[0]
			job := req.remainingJobs[0]

			// farms them out to its available miners (it is up to you to choose a suitable maximum job size).
			// The server should be "fair" in the way it distributes tasks.
			srv.busyMiners[miner] = jobInfo{req.requestId, job}

			// send the job to the miner
			srv.write2client(miner, bitcoin.NewRequest(srv.requestInfo[req.requestId].message, job.lower, job.upper))

			srv.requestList[0].remainingJobs = srv.requestList[0].remainingJobs[1:]
			sort.Sort(ByRemainingJobs(srv.requestList))
		}
	}
}
