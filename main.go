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
	"strconv"
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
)

const (
	transferEventABI      = `[{"anonymous":false,"inputs":[{"indexed":true,"name":"from","type":"address"},{"indexed":true,"name":"to","type":"address"},{"indexed":false,"name":"value","type":"uint256"}],"name":"Transfer","type":"event"}]`
	getBalanceFunctionABI = `[{"constant":true,"inputs":[{"name":"_owner","type":"address"}],"name":"balanceOf","outputs":[{"name":"balance","type":"uint256"}],"payable":false,"stateMutability":"view","type":"function"}]`
	initialSupplyStr      = "100000000000000000000000000000" // 100 billion CHAOS in Wei
)

var (
	contractAddress = common.HexToAddress("0x20d704099B62aDa091028bcFc44445041eD16f09")
	fromAddress     = common.HexToAddress("0xea36d66f0AC9928b358400309a8dFbC43A973a35")
	toAddress       = common.HexToAddress("0x000000000000000000000000000000000000dEaD")
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
		fmt.Printf("starting HTTP main server on %s\n", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			panic(fmt.Sprintf("error starting HTTP main server: %v", err))
		}
	}()

	return srv
}

func HandleHealth(w http.ResponseWriter, r *http.Request) {
	return
}

func sendTelegramMessage(bot *tgbotapi.BotAPI, channelID string, message string, mediaURL string, isGIF bool) {
	log.Println("Sending message to Telegram channel...")
	if isGIF {
		// Send a GIF (Animation)
		atoi, err := strconv.Atoi(channelID)
		if err != nil {
			log.Printf("Error parsing channelid: %v", err)
		}
		animation := tgbotapi.NewAnimation(int64(atoi), tgbotapi.FileURL(mediaURL))

		animation.Caption = message
		if _, err := bot.Send(animation); err != nil {
			log.Printf("Error sending message with GIF: %v", err)
		} else {
			log.Println("Message with GIF sent successfully.")
		}
	} else {
		// Send an image
		photo := tgbotapi.NewPhotoToChannel(channelID, tgbotapi.FileURL(mediaURL))
		photo.Caption = message
		if _, err := bot.Send(photo); err != nil {
			log.Printf("Error sending message with photo: %v", err)
		} else {
			log.Println("Message with photo sent successfully.")
		}
	}
}

// monitorBurns subscribes to new blocks and processes transactions for burn events.
func monitorBurns(ctx context.Context, waitGroup *sync.WaitGroup, wssClient *ethclient.Client, bot *tgbotapi.BotAPI, channelID string, imageURL string) {
	log.Println("Started monitoring for burns...")

	// Transfer event signature (ERC-20 Transfer event: Transfer(address,address,uint256))
	transferEventSignature := []byte("Transfer(address,address,uint256)")
	transferEventTopic := crypto.Keccak256Hash(transferEventSignature)

	query := ethereum.FilterQuery{
		Addresses: []common.Address{contractAddress},
		Topics: [][]common.Hash{
			{transferEventTopic},
			{common.BytesToHash(fromAddress.Bytes())},
			{common.BytesToHash(toAddress.Bytes())},
		},
	}

	// Exponential backoff parameters
	maxRetryDelay := time.Minute * 5
	initialRetryDelay := time.Second * 1
	retryDelay := initialRetryDelay

	for {
		select {
		case <-ctx.Done():
			log.Println("Subscription finished by cancellation")
			waitGroup.Done()
			return
		default:
			log.Println("Subscribing to burn events...")
			logsChannel := make(chan types.Log)
			sub, err := wssClient.SubscribeFilterLogs(ctx, query, logsChannel)
			if err != nil {
				log.Printf("Failed to subscribe: %v. Retrying in %v...", err, retryDelay)
				time.Sleep(retryDelay)

				// Exponential backoff with jitter
				retryDelay = time.Duration(float64(retryDelay) * 1.5)
				jitter := time.Duration(rand.Float64() * float64(retryDelay))
				retryDelay += jitter

				if retryDelay > maxRetryDelay {
					retryDelay = maxRetryDelay
				}
				continue
			}

			log.Println("Subscription obtained successfully")
			retryDelay = initialRetryDelay // Reset retry delay on successful connection

			for {
				select {
				case err := <-sub.Err():
					log.Printf("Subscription error: %v", err)
					sub.Unsubscribe()
					break
				case <-ctx.Done():
					log.Println("Subscription finished by cancellation")
					sub.Unsubscribe()
					waitGroup.Done()
					return
				case vLog := <-logsChannel:
					log.Printf("Log received: %+v", vLog)
					processLog(ctx, vLog, wssClient, bot, channelID, imageURL)
				}
			}
		}
	}
}

func processLog(ctx context.Context, vLog types.Log, wssClient *ethclient.Client, bot *tgbotapi.BotAPI, channelID string, imageURL string) {
	// ABI for the smart contract (include only relevant events)
	contractABI, err := abi.JSON(strings.NewReader(transferEventABI))
	if err != nil {
		log.Fatalf("Failed to parse ABI: %v", err)
	}

	// Decode the log
	event := struct {
		From  common.Address
		To    common.Address
		Value *big.Int
	}{}

	err = contractABI.UnpackIntoInterface(&event, "Transfer", vLog.Data)
	if err != nil {
		log.Fatalf("Failed to unpack log: %v", err)
	}

	// Extract indexed topics (from and to)
	event.From = common.HexToAddress(vLog.Topics[1].Hex())
	event.To = common.HexToAddress(vLog.Topics[2].Hex())

	log.Printf("Transfer Event: From %s To %s Value %s", event.From.Hex(), event.To.Hex(), event.Value.String())

	notifyTelegram(ctx, event, wssClient, bot, channelID, imageURL)
}

func notifyTelegram(ctx context.Context, event struct {
	From  common.Address
	To    common.Address
	Value *big.Int
}, wssClient *ethclient.Client, bot *tgbotapi.BotAPI, channelID string, imageURL string) {
	// Token has 18 decimals, so divide by 10^18
	decimals := big.NewInt(1e18)
	humanReadableValue := new(big.Float).Quo(new(big.Float).SetInt(event.Value), new(big.Float).SetInt(decimals))

	// Truncate the value to an integer
	intValue := new(big.Int)
	humanReadableValue.Int(intValue) // Converts the float to its integer part

	// Parse the ERC-20 ABI
	parsedABI, err := abi.JSON(strings.NewReader(getBalanceFunctionABI))
	if err != nil {
		log.Fatalf("Failed to parse ABI: %v", err)
	}

	// Prepare the call data for the balanceOf function
	data, err := parsedABI.Pack("balanceOf", toAddress)
	if err != nil {
		log.Fatalf("Failed to pack data for balanceOf function: %v", err)
	}

	// Call the contract to get the balance
	result, err := wssClient.CallContract(ctx, ethereum.CallMsg{
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

Burns: https://basescan.org/token/0x20d704099b62ada091028bcfc44445041ed16f09?a=0xea36d66f0ac9928b358400309a8dfbc43a973a35`,
		humanReadableValue.String(),
		percentBurned,
		randomMessage,
	)

	// SendburnAmount the message with the image
	sendTelegramMessage(bot, channelID, message, imageURL, true)
}

func main() {
	// Load environment variables
	loadEnv()
	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	channel := os.Getenv("TELEGRAM_CHANNEL_ID")
	wssURL := os.Getenv("ETHEREUM_WSS_URL")
	//httpURL := os.Getenv("ETHEREUM_HTTP_URL")
	imageURL := os.Getenv("IMAGE_URL")

	if len(botToken) == 0 || len(channel) == 0 || len(wssURL) == 0 || len(imageURL) == 0 {
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
	log.Println("Connected to WSS Ethereum client successfully")

	// Create Telegram bot
	log.Println("Initializing Telegram bot...")
	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		log.Fatalf("Failed to create Telegram bot: %v", err)
	}
	log.Println("Telegram bot initialized successfully")

	// Start health server to keep alive
	server := startHealthServer("0.0.0.0:8080")

	// Prepare routine
	ctx, cancel := context.WithCancel(context.Background())
	waitGroup.Add(1)
	// Start polling for burn events
	go monitorBurns(ctx, waitGroup, wssClient, bot, channel, imageURL)

	for {
		select {
		case <-terminationSignalChannel:
			log.Println("Signal received, shutting down...")
			cancel()
		case <-ctx.Done():
			fmt.Printf("shutting down server...")
			server.SetKeepAlivesEnabled(false)
			err = server.Shutdown(ctx)
			waitGroup.Wait()
			fmt.Printf("shutting down successfully")
			os.Exit(0)
		}
	}
}
