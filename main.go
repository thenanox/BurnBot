package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"github.com/joho/godotenv"
	"github.com/onrik/ethrpc"
)

const (
	transferABI      = `[{"anonymous":false,"inputs":[{"indexed":true,"name":"from","type":"address"},{"indexed":true,"name":"to","type":"address"},{"indexed":false,"name":"value","type":"uint256"}],"name":"Transfer","type":"event"}]`
	getBalanceABI    = `[{"constant":true,"inputs":[{"name":"_owner","type":"address"}],"name":"balanceOf","outputs":[{"name":"balance","type":"uint256"}],"payable":false,"stateMutability":"view","type":"function"}]`
	initialSupplyStr = "100000000000000000000000000000" // 100 billion CHAOS in Wei

	aixbtWallet = "0xea36d66f0ac9928b358400309a8dfbc43a973a35"
	burnAddress = "0x000000000000000000000000000000000000dead"

	tokenAddress = "0x20d704099B62aDa091028bcFc44445041eD16f09"
	fromAddress  = "0xdecaf122e4d89afbcecc341ecfe9987a67cdf93e"
	toAddress    = "0xC3E823489A201a8654646496b3c318A8c6C9B881"
)

var (
	contractAddress = common.HexToAddress(tokenAddress)
	walletAddress   = common.HexToAddress(burnAddress)
)

// loadEnv loads environment variables from a .env file.
func loadEnv() {
	log.Println("Loading environment variables...")
	err := godotenv.Load()
	if err != nil {
		log.Println("Error loading .env file: ", err)
	} else {
		log.Println("Environment variables loaded successfully from .env")
	}
}

// getRandomMessage returns a random burn-related message from a predefined list.
func getRandomMessage() string {
	messages := []string{
		"AIXBT just sparked some CHAOS, and by sparked, I mean burned it.",
		"Burn, baby, burn! CHAOS reigns.",
		"Another burn for the history books! 🔥",
		"The flames of CHAOS grow stronger!",
		"CHAOS just got a little scarcer. 🔥",
		"The inferno of CHAOS continues to rage!",
		"A blaze of CHAOS! 🔥🔥🔥",
		"The fire of CHAOS grows brighter with every burn.",
		"AIXBT is turning CHAOS into ashes!",
		"Yet another chapter in the burning saga of CHAOS!",
	}
	rand.Seed(time.Now().UnixNano())
	return messages[rand.Intn(len(messages))]
}

func healthServer() {
	var terminationSignalChannel = make(chan os.Signal, 1)
	signal.Notify(terminationSignalChannel, os.Interrupt, syscall.SIGTERM)

	waitGroup := &sync.WaitGroup{}

	startHealthServer("0.0.0.0:8080")

	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt, os.Kill)
	<-sc

	for {
		select {
		case <-terminationSignalChannel:
			fmt.Printf("shutting down server...")
			waitGroup.Wait()
			close(terminationSignalChannel)
			os.Exit(0)
		}
	}
}

func startHealthServer(addr string) *http.Server {
	// HTTP Gateway server
	router := mux.NewRouter()
	router.HandleFunc("/api/health", HandleHealth)

	srv := &http.Server{
		Addr:              addr,
		WriteTimeout:      time.Second * 15,
		ReadTimeout:       time.Second * 15,
		IdleTimeout:       time.Second * 60,
		ReadHeaderTimeout: time.Second * 15,
		Handler:           handlers.LoggingHandler(os.Stdout, router),
	}
	go func() {
		fmt.Printf("starting HTTP main server on %s", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			panic(fmt.Sprintf("error starting HTTP main server: %v", err))
		}
	}()

	return srv
}

func HandleHealth(w http.ResponseWriter, r *http.Request) {
	return
}

// sendTelegramMessage sends a message and an image to a Telegram channel.
func sendTelegramMessage(bot *tgbotapi.BotAPI, channelID string, message string, imageURL string) {
	log.Println("Sending message to Telegram channel...")
	if len(imageURL) > 0 {
		photo := tgbotapi.NewPhotoToChannel(channelID, tgbotapi.FileURL(imageURL))
		photo.Caption = message
		if _, err := bot.Send(photo); err != nil {
			log.Printf("Error sending message with photo: %v", err)
		} else {
			log.Println("Message with photo sent successfully.")
		}
	} else {
		photo := tgbotapi.NewMessageToChannel(channelID, message)
		if _, err := bot.Send(photo); err != nil {
			log.Printf("Error sending message: %v", err)
		} else {
			log.Println("Message sent successfully.")
		}
	}
}

// monitorBurns subscribes to new blocks and processes transactions for burn events.
func monitorBurns(wssClient *ethclient.Client, httpClient *ethrpc.EthRPC, bot *tgbotapi.BotAPI, channelID string, imageURL string) {
	log.Println("Started monitoring for burns...")

	headers := make(chan *types.Header)
	sub, err := wssClient.SubscribeNewHead(context.Background(), headers)
	if err != nil {
		log.Fatalf("Failed to subscribe to new blocks: %v", err)
	}

	for {
		select {
		case err := <-sub.Err():
			log.Printf("Subscription error: %v", err)
			return
		case header := <-headers:
			blockNumber := header.Number.Uint64()
			log.Printf("New block detected: %d", blockNumber)

			// Fetch the full block
			block, err := httpClient.EthGetBlockByNumber(int(blockNumber), true)
			if err != nil {
				log.Printf("Error fetching block: %v", err)
				continue
			}

			// Process transactions in the block
			for _, tx := range block.Transactions {
				// Check if the transaction is from the monitored wallet to the burn address
				if strings.EqualFold(tx.From, fromAddress) && strings.EqualFold(tx.To, toAddress) {

					// Transfer event signature (ERC-20 Transfer event: Transfer(address,address,uint256))
					transferEventSignature := []byte("Transfer(address,address,uint256)")
					transferEventTopic := crypto.Keccak256Hash(transferEventSignature)

					// Create a filter query for logs in the specific block
					query := ethereum.FilterQuery{
						FromBlock: header.Number,
						ToBlock:   header.Number,
						Addresses: []common.Address{contractAddress},
						Topics:    [][]common.Hash{{transferEventTopic}}, // Topic[0] is the event signature
					}

					// Fetch logs from the block
					logs, err := wssClient.FilterLogs(context.Background(), query)
					if err != nil {
						log.Fatalf("Failed to fetch logs: %v", err)
					}

					// ABI of the contract (only include Transfer event for simplicity)
					contractABI, err := abi.JSON(strings.NewReader(transferABI))
					if err != nil {
						log.Fatalf("Failed to parse contract ABI: %v", err)
					}

					// Process each log
					for _, vLog := range logs {
						// Unpack log data using ABI
						event := struct {
							From  common.Address
							To    common.Address
							Value *big.Int
						}{}
						err := contractABI.UnpackIntoInterface(&event, "Transfer", vLog.Data)
						if err != nil {
							log.Fatalf("Failed to unpack log data: %v", err)
						}

						// Extract indexed fields from topics
						event.From = common.HexToAddress(vLog.Topics[1].Hex())
						event.To = common.HexToAddress(vLog.Topics[2].Hex())

						if strings.EqualFold(event.From.String(), aixbtWallet) && strings.EqualFold(event.To.String(), burnAddress) {

							// Token has 18 decimals, so divide by 10^18
							decimals := big.NewInt(1e18)
							humanReadableValue := new(big.Float).Quo(new(big.Float).SetInt(event.Value), new(big.Float).SetInt(decimals))

							// Truncate the value to an integer
							intValue := new(big.Int)
							humanReadableValue.Int(intValue) // Converts the float to its integer part

							// Parse the ERC-20 ABI
							parsedABI, err := abi.JSON(strings.NewReader(getBalanceABI))
							if err != nil {
								log.Fatalf("Failed to parse ABI: %v", err)
							}

							// Prepare the call data for the balanceOf function
							data, err := parsedABI.Pack("balanceOf", walletAddress)
							if err != nil {
								log.Fatalf("Failed to pack data for balanceOf function: %v", err)
							}

							// Call the contract to get the balance
							result, err := wssClient.CallContract(context.Background(), ethereum.CallMsg{
								To:   &contractAddress,
								Data: data,
							}, nil)
							if err != nil {
								log.Fatalf("Failed to call contract: %v", err)
							}

							// Unpack the result
							var balance *big.Int
							err = parsedABI.UnpackIntoInterface(&balance, "balanceOf", result)
							if err != nil {
								log.Fatalf("Failed to unpack result: %v", err)
							}

							// Calculate the percentage burned
							initialSupply, _ := new(big.Float).SetString(initialSupplyStr)
							burnedFloat := new(big.Float).SetInt(balance)
							percentBurned := new(big.Float).Quo(burnedFloat, initialSupply)
							percentBurned.Mul(percentBurned, big.NewFloat(100))

							// Get a random message
							randomMessage := getRandomMessage()

							// Prepare the burn message
							message := fmt.Sprintf(
								`🔥🔥🔥CHAOS BURN🔥🔥🔥

AIXBT burned: %s $CHAOS

Total Burned: %.2f%% of supply

%s

View Transaction: https://basescan.org/tx/%s`,
								humanReadableValue.String(),
								percentBurned,
								randomMessage,
								tx.Hash,
							)

							// SendburnAmount the message with the image
							sendTelegramMessage(bot, channelID, message, imageURL)
						}
					}
				}
			}
		}
	}
}

func main() {
	// Load environment variables
	loadEnv()
	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	channel := os.Getenv("TELEGRAM_CHANNEL_ID")
	wssURL := os.Getenv("ETHEREUM_WSS_URL")
	httpURL := os.Getenv("ETHEREUM_HTTP_URL")
	imageURL := os.Getenv("IMAGE_URL")

	if len(botToken) == 0 || len(wssURL) == 0 || len(httpURL) == 0 || len(imageURL) == 0 {
		log.Fatalf("environment variables not set")
	}

	var terminationSignalChannel = make(chan os.Signal, 1)
	signal.Notify(terminationSignalChannel, os.Interrupt, syscall.SIGTERM)

	// cancellable context
	waitGroup := &sync.WaitGroup{}

	// Connect to Ethereum client
	log.Println("Connecting to WSS Ethereum client...")
	wssClient, err := ethclient.Dial(wssURL)
	if err != nil {
		log.Fatalf("Failed to connect to Ethereum client: %v", err)
	}
	log.Println("Connected to WSS Ethereum client successfully.")

	// Connect to Ethereum client
	log.Println("Connecting to HTTP Ethereum client...")
	httpClient := ethrpc.New(httpURL)
	log.Println("Connected to HTTP Ethereum client successfully.")

	// Create Telegram bot
	log.Println("Initializing Telegram bot...")
	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		log.Fatalf("Failed to create Telegram bot: %v", err)
	}
	log.Println("Telegram bot initialized successfully.")

	// Review in which channels is the bot
	//updates := bot.GetUpdatesChan(tgbotapi.UpdateConfig{})
	//var channel string
	//for update := range updates {
	//	if update.ChannelPost != nil {
	//		channel = strconv.FormatInt(update.ChannelPost.Chat.ID, 10)
	//		log.Printf("Channel ID detected: %s", channel)
	//		break // Exit loop once the channel ID is found
	//	}
	//}

	// Test connectivity with Telegram
	testMessage := "🚀 Bot is online and ready! 🔥"
	sendTelegramMessage(bot, channel, testMessage, "")

	waitGroup.Add(1)
	// Start health server to keep alive
	go healthServer()
	// Start polling for burn events
	go monitorBurns(wssClient, httpClient, bot, channel, imageURL)

	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt, os.Kill)
	<-sc

	for {
		select {
		case <-terminationSignalChannel:
			fmt.Printf("shutting down server...")
			waitGroup.Wait()
			close(terminationSignalChannel)
			os.Exit(0)
		}
	}
}
