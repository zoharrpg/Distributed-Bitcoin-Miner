package main

import (
	"fmt"
	"log"
	"os"
	"strconv"

	"github.com/cmu440/lsp"
)

type server struct {
	lspServer lsp.Server
}

func startServer(port int) (*server, error) {
	// TODO: implement this!
	params := lsp.NewParams()
	lspServer, err := lsp.NewServer(port, params)
	return &server{lspServer: lspServer}, err
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

	// TODO: implement this!

	// Server
	// An LSP server that manages the entire Bitcoin cracking enterprise.
	// At any time, the server can have any number of workers available, and can receive any number of requests from any number of clients.
	// For each client request, it splits the request into multiple smaller jobs and distributes these jobs to its available miners.
	// The server waits for each worker to respond before generating and sending the final result back to the client.
	// The server should be "fair" in the way it distributes tasks.

	// The server breaks this request into more manageable-sized jobs and
	// farms them out to its available miners (it is up to you to choose a suitable maximum job size).
	// Once the miner exhausts all possible nonces, it determines the least hash value and its corresponding nonce and
	// sends back the final result:
	// [Result minHash nonce]

	// The server collects all final results from the workers, determines the least hash value
	// and its corresponding nonce, and sends back the final result to the request client

	// When the server loses contact with a miner, it should reassign any job that the worker was handling to a different worker.
	// If there are no available miners left, the server should wait for a new miner to join before reassigning the old miner’s job.

	// When the server loses contact with a request client, it should cease working on any requests being done on behalf of the client
	// (you need not forcibly terminate a job on a miner—just wait for it to complete and ignore its results).
}

// scheduler
// aims to minimize mean response time for all client requests.
// requests that arrive earlier have some sort of priority.
// If the server gets an extremely large request followed by a small request, we expect
//that the small request will finish before the large request.1
//• If the server gets multiple requests of similar length, the requests should finish approximately in the order they arrived. For example, if 100 identical requests come in and the 100th request finishes first, that would not be very fair.
//• Work should be divided roughly evenly across workers. Try to minimize the idle time of each worker.
// Shortest Remaining Time First
