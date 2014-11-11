package mixcoin

import (
	"errors"
	"log"
	"math/big"
	"math/rand"
	"os"
	"os/signal"
	"syscall"

	"github.com/conformal/btcjson"
	"github.com/conformal/btcutil"
	"github.com/conformal/btcwire"
)

const (
	MAX_CONF = 9999
)

var (
	blockchainHeight int
	stopping         bool
	pool             PoolManager
	rpc              RpcClient
	mix              *Mix
	cfg              *Config
	db               DB
)

func init() {
	stopping = false
}

func StartMixcoinServer() {
	log.Println("starting mixcoin server")

	cfg = GetConfig()
	db = NewMixcoinDB(cfg.DbFile)
	pool = NewPoolManager()
	rpc = NewRpcClient()
	blockchainHeight = getBlockchainHeight()

	mix = NewMix(nil)
	BootstrapPool()
	LoadReserves()
	HandleShutdown()
}

func HandleShutdown() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		for sig := range c {
			if sig == syscall.SIGINT {
				shutdown()
			}
		}
	}()
}

func shutdown() {
	// do we need to rpc.Disconnect()?
	stopping = true
	mix.Shutdown()
	log.Printf("shutdown mix")
	pool.Shutdown()
	log.Printf("shutdown pool")
	db.Close()
	log.Printf("shutdown db")
	os.Exit(0)
}

func handleChunkRequest(chunkMsg *ChunkMessage) error {
	if stopping {
		return errors.New("refused request; mixcoin shutting down")
	}
	log.Printf("handling chunk request: %+v", chunkMsg)

	err := validateChunkMsg(chunkMsg)
	if err != nil {
		log.Printf("Invalid chunk request: %v", err)
		return err
	}

	addr, err := getNewAddress()
	if err != nil {
		log.Panicf("Unable to create new address: %v", err)
		return err
	}

	encodedAddr := addr.EncodeAddress()
	log.Printf("generated address: %s", encodedAddr)

	chunkMsg.MixAddr = encodedAddr

	signChunkMessage(chunkMsg)
	registerNewChunk(encodedAddr, chunkMsg)
	return nil
}

func registerNewChunk(encodedAddr string, chunkMsg *ChunkMessage) {
	log.Printf("registering new chunk at address %s", encodedAddr)
	pool.Put(Receivable, chunkMsg)
}

func onBlockConnected(blockHash *btcwire.ShaHash, height int32) {
	if stopping {
		return
	}
	log.Printf("new block connected with hash %v, height %d", blockHash, height)

	blockchainHeight = int(height)
	go findTransactions(blockHash, int(height))
}

func prune() {
	pool.Filter(func(item PoolItem) bool {
		msg := item.(*ChunkMessage)
		return msg.SendBy <= blockchainHeight
	})
}

func findTransactions(blockHash *btcwire.ShaHash, height int) {
	prune()

	minConf := cfg.MinConfirmations

	log.Printf("getting receivable chunks")
	addrs := pool.ReceivingKeys()

	var receivableAddrs []btcutil.Address
	for _, addr := range addrs {
		decoded, err := decodeAddress(addr)
		if err != nil {
			log.Printf("error decoding address: %v", err)
		}
		receivableAddrs = append(receivableAddrs, decoded)
	}
	log.Printf("receivable addresses: %v", receivableAddrs)
	receivedByAddress, err := rpc.ListUnspentMinMaxAddresses(minConf, MAX_CONF, receivableAddrs)
	if err != nil {
		log.Panicf("error listing unspent by address: %v", err)
	}
	log.Printf("received transactions: %+v", receivedByAddress)

	// make addr -> utxo map of received txs
	received := make(map[string]*Utxo)
	for _, result := range receivedByAddress {
		if !isValidReceivedResult(result) {
			continue
		}

		amount, err := btcutil.NewAmount(result.Amount)
		if err != nil {
			log.Panicf("invalid tx amount: %v", err)
		}

		received[result.Address] = &Utxo{
			Addr:   result.Address,
			Amount: amount,
			TxId:   result.TxId,
			Index:  int(result.Vout),
		}
	}

	var receivedAddrs []string
	for addr, _ := range received {
		receivedAddrs = append(receivedAddrs, addr)
	}

	log.Printf("received addresses: %v", receivedAddrs)

	// get the chunk messages, move to pool
	chunkMsgs := pool.Scan(receivedAddrs)
	log.Printf("received for chunkmessages %+v", chunkMsgs)
	for _, item := range chunkMsgs {
		msg := item.(*ChunkMessage)
		utxo := received[msg.MixAddr]
		if isFee(msg.Nonce, blockHash, msg.Fee) {
			log.Printf("retaining as fee utxo %v", utxo)
			pool.Put(Reserve, utxo)
		} else {
			log.Printf("mixing utxo for message: %v", msg)
			pool.Put(Mixing, utxo)
			mix.Put(msg)
		}
	}
	log.Printf("done handling block")
}

func isFee(nonce int64, hash *btcwire.ShaHash, feeBips int) bool {
	bigIntHash := big.NewInt(0)
	bigIntHash.SetBytes(hash.Bytes())
	hashInt := bigIntHash.Int64()

	gen := nonce | hashInt
	fee := float64(feeBips) * 1.0e-4

	source := rand.NewSource(gen)
	rng := rand.New(source)
	return rng.Float64() <= fee
}

func isValidReceivedResult(result btcjson.ListUnspentResult) bool {
	// ListUnspentResult.Amount is a float64 in BTC
	// btcutil.Amount is an int64
	amountReceived, err := btcutil.NewAmount(result.Amount)
	if err != nil {
		log.Printf("error parsing amount received: %v", err)
	}
	amountReceivedInt := int64(amountReceived)

	hasConfirmations := result.Confirmations >= int64(cfg.MinConfirmations)
	hasAmount := amountReceivedInt >= cfg.ChunkSize

	return hasConfirmations && hasAmount
}
