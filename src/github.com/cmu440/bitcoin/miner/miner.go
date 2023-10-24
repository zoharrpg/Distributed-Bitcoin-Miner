package main

import (
	"encoding/json"
	"fmt"
	"github.com/cmu440/bitcoin"
	"log"
	"math"
	"math/rand"
	"os"
	"time"

	"github.com/cmu440/lsp"
)

// Attempt to connect miner as a client to the server.
func joinWithServer(hostport string) (lsp.Client, error) {
	// You will need this for randomized isn
	seed := rand.NewSource(time.Now().UnixNano())
	isn := rand.New(seed).Intn(int(math.Pow(2, 8)))

	client, err := lsp.NewClient(hostport, isn, lsp.NewParams())
	if err != nil {
		return nil, err
	}
	joinMessage := *bitcoin.NewJoin()

	err = SendMessage(client, joinMessage)
	if err != nil {
		fmt.Println("Send message error")
		return nil, err
	}
	// TODO: implement this!

	return client, nil
}

var LOGF *log.Logger

func main() {
	// You may need a logger for debug purpose
	const (
		name = "minerLog.txt"
		flag = os.O_RDWR | os.O_CREATE
		perm = os.FileMode(0666)
	)

	file, err := os.OpenFile(name, flag, perm)
	if err != nil {
		return
	}
	defer file.Close()

	const numArgs = 2
	if len(os.Args) != numArgs {
		fmt.Printf("Usage: ./%s <hostport>", os.Args[0])
		return
	}

	hostport := os.Args[1]
	miner, err := joinWithServer(hostport)
	if err != nil {
		fmt.Println("Failed to join with server:", err)
		return
	}

	defer miner.Close()

	// TODO: implement this!
}

func SendMessage(client lsp.Client, message bitcoin.Message) error {
	var packet []byte
	packet, err := json.Marshal(message)
	if err != nil {
		return err
	}
	err = client.Write(packet)
	if err != nil {
		return err
	}
	return nil
}
