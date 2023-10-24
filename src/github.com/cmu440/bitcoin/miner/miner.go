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

	for {
		message, err := ReadMessage(miner)
		if err != nil {
			LOGF.Printf("[Miner %v] Error: %v\n", miner.ConnID(), err)
			break
		} else {
			if message.Type == bitcoin.Request {
				go process(miner, message.Data, message.Lower, message.Upper)
			}
		}
	}

	// TODO: implement this!
}
func process(client lsp.Client, data string, lower uint64, upper uint64) {
	nonce, minHash := lower, bitcoin.Hash(data, lower)

	for i := lower + 1; i <= upper; i++ {
		hash := bitcoin.Hash(data, i)
		if minHash > hash {
			minHash = hash
			nonce = i
		}
	}
	result := bitcoin.NewResult(minHash, nonce)
	result.Lower = lower
	result.Upper = upper
	err := SendMessage(client, *result)

	if err != nil {
		fmt.Printf("[Miner %v] Error: %v\n", client.ConnID(), err)
	} else {
		fmt.Printf("[Miner %v] Send Result Back to Server: %v, %v\n", client.ConnID(), minHash, nonce)
	}
}
func ReadMessage(client lsp.Client) (bitcoin.Message, error) {
	payload, err := client.Read()
	if err != nil {
		return bitcoin.Message{}, err
	}
	var message bitcoin.Message
	err = json.Unmarshal(payload, &message)
	if err != nil {
		fmt.Println("Unmarshal error")
		return bitcoin.Message{}, err
	}
	return message, nil

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
