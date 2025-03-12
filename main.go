package main

import (
	"context"
	"encoding/json"
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
	latestBurnEvent struct {
		Value      string    `json:"value"`
		Percentage float64   `json:"percentage_burned"`
		Timestamp  time.Time `json:"timestamp"`
	}
	latestBurnEventMutex sync.RWMutex
	activeChannels       = make(map[int64]bool)
	activeChannelsMutex  sync.RWMutex
)

func main() {
	// Load environment variables
	loadEnv()
	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	channelIDs := os.Getenv("TELEGRAM_CHANNEL_ID")
	imageURL := os.Getenv("IMAGE_URL")
	wssURL := os.Getenv("ETHEREUM_WSS_URL")

	if len(botToken) == 0 || len(wssURL) == 0 || len(imageURL) == 0 {
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
	bot.Debug = true

	// Initialize bot commands and pre-configured channels
	if err := initializeBot(bot, channelIDs); err != nil {
		log.Fatalf("Failed to initialize bot: %v", err)
	}
	log.Println("Telegram bot initialized successfully")

	// Start health server to keep alive
	server := startHTTPServer("0.0.0.0:8080")

	// Set up Telegram updates
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := bot.GetUpdatesChan(u)

	// Start handling Telegram updates
	go func() {
		for update := range updates {
			handleTelegramCommand(bot, update)
		}
	}()

	// Prepare routine
	ctx, cancel := context.WithCancel(context.Background())
	waitGroup.Add(1)
	// Start polling for burn events
	go monitorBurns(ctx, waitGroup, wssClient, bot, imageURL)

	for {
		select {
		case <-terminationSignalChannel:
			log.Println("Signal received, shutting down...")
			cancel()
		case <-ctx.Done():
			fmt.Printf("shutting down server...")
			server.SetKeepAlivesEnabled(false)
			_ = server.Shutdown(ctx)
			waitGroup.Wait()
			fmt.Printf("shutting down successfully")
			os.Exit(0)
		}
	}
}

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

// logActiveChannels prints all active channel IDs in the format CHANNEL_ID=id1;id2;...
func logActiveChannels() {
	activeChannelsMutex.RLock()
	defer activeChannelsMutex.RUnlock()

	var channelIDs []string
	for channelID := range activeChannels {
		channelIDs = append(channelIDs, fmt.Sprintf("%d", channelID))
	}
	log.Printf("Current active channels - CHANNEL_ID=%s", strings.Join(channelIDs, ";"))
}

// initializeBot sets up the bot with commands and pre-configured channels
func initializeBot(bot *tgbotapi.BotAPI, channelIDs string) error {
	// Set up bot commands
	commands := []tgbotapi.BotCommand{
		{
			Command:     "start",
			Description: "Start receiving burn notifications in this channel",
		},
		{
			Command:     "stop",
			Description: "Stop receiving burn notifications in this channel",
		},
	}

	cmdConfig := tgbotapi.NewSetMyCommandsWithScope(tgbotapi.NewBotCommandScopeDefault(), commands...)
	_, err := bot.Request(cmdConfig)
	if err != nil {
		return fmt.Errorf("failed to set bot commands: %v", err)
	}

	// Load pre-configured channels from environment
	if channelIDs != "" {
		for _, channelID := range strings.Split(channelIDs, ";") {
			id, err := strconv.ParseInt(channelID, 10, 64)
			if err != nil {
				log.Printf("Invalid channel ID in CHANNEL_ID env var: %s", channelID)
				continue
			}
			activeChannelsMutex.Lock()
			activeChannels[id] = true
			activeChannelsMutex.Unlock()
		}
		logActiveChannels()
	}

	return nil
}

// handleTelegramCommand processes bot commands
func handleTelegramCommand(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	var message *tgbotapi.Message
	if update.Message != nil {
		message = update.Message
	} else if update.ChannelPost != nil {
		message = update.ChannelPost
	} else {
		return
	}

	if !message.IsCommand() {
		return
	}

	switch message.Command() {
	case "start":
		activeChannelsMutex.Lock()
		activeChannels[message.Chat.ID] = true
		activeChannelsMutex.Unlock()

		msg := tgbotapi.NewMessage(message.Chat.ID, "🔥 CHAOS Burn Bot activated! You will receive notifications about CHAOS token burns in this channel.")
		if _, err := bot.Send(msg); err != nil {
			log.Printf("Error sending start message: %v", err)
		}
		logActiveChannels()

	case "stop":
		activeChannelsMutex.Lock()
		delete(activeChannels, message.Chat.ID)
		activeChannelsMutex.Unlock()

		msg := tgbotapi.NewMessage(message.Chat.ID, "CHAOS Burn Bot deactivated. You will no longer receive burn notifications.")
		if _, err := bot.Send(msg); err != nil {
			log.Printf("Error sending stop message: %v", err)
		}
		logActiveChannels()
	}
}

// monitorBurns subscribes to new blocks and processes transactions for burn events.
func monitorBurns(ctx context.Context, waitGroup *sync.WaitGroup, wssClient *ethclient.Client, bot *tgbotapi.BotAPI, imageURL string) {
	log.Println("Started monitoring for burns...")
	defer waitGroup.Done()

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
			log.Println("Monitor burns cancelled, shutting down...")
			return
		default:
			log.Println("Subscribing to burn events...")
			logsChannel := make(chan types.Log)
			sub, err := wssClient.SubscribeFilterLogs(ctx, query, logsChannel)
			if err != nil {
				log.Printf("Failed to subscribe: %v. Retrying in %v...", err, retryDelay)
				time.Sleep(retryDelay)
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

		subscriptionLoop:
			for {
				select {
				case <-ctx.Done():
					log.Println("Monitor burns cancelled, shutting down...")
					sub.Unsubscribe()
					return
				case err := <-sub.Err():
					if err != nil {
						log.Printf("Subscription error: %v", err)
					}
					log.Println("Subscription ended, reconnecting...")
					sub.Unsubscribe()
					break subscriptionLoop // exit inner loop on error
				case vLog := <-logsChannel:
					log.Printf("Log received: %+v", vLog)
					processLog(ctx, vLog, wssClient, bot, imageURL)
				}
			}
		}
	}
}

func processLog(ctx context.Context, vLog types.Log, wssClient *ethclient.Client, bot *tgbotapi.BotAPI, imageURL string) {
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

	notifyTelegram(ctx, event, wssClient, bot, imageURL)
}

// notifyTelegram sends burn notifications to all active channels
func notifyTelegram(ctx context.Context, event struct {
	From  common.Address
	To    common.Address
	Value *big.Int
}, wssClient *ethclient.Client, bot *tgbotapi.BotAPI, imageURL string) {
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
	percentBurnedFloat, _ := percentBurned.Float64()

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
		percentBurnedFloat,
		randomMessage,
	)

	// Send the message with the image to all active channels
	sendTelegramMessage(bot, message, imageURL, false)

	// Update the latest burn event
	latestBurnEventMutex.Lock()
	latestBurnEvent = struct {
		Value      string    `json:"value"`
		Percentage float64   `json:"percentage_burned"`
		Timestamp  time.Time `json:"timestamp"`
	}{
		Value:      humanReadableValue.String(),
		Percentage: percentBurnedFloat,
		Timestamp:  time.Now(),
	}
	latestBurnEventMutex.Unlock()
}

// sendTelegramMessage sends a message to all active channels
func sendTelegramMessage(bot *tgbotapi.BotAPI, message string, mediaURL string, isGIF bool) {
	log.Println("Sending message to Telegram channels...")

	activeChannelsMutex.RLock()
	defer activeChannelsMutex.RUnlock()

	for channelID := range activeChannels {
		if isGIF {
			// Send a GIF (Animation)
			animation := tgbotapi.NewAnimation(channelID, tgbotapi.FileURL(mediaURL))
			animation.Caption = message
			if _, err := bot.Send(animation); err != nil {
				log.Printf("Error sending message with GIF to channel %d: %v", channelID, err)
			} else {
				log.Printf("Message with GIF sent successfully to channel %d", channelID)
			}
		} else {
			// Send an image
			photo := tgbotapi.NewPhoto(channelID, tgbotapi.FileURL(mediaURL))
			photo.Caption = message
			if _, err := bot.Send(photo); err != nil {
				log.Printf("Error sending message with photo to channel %d: %v", channelID, err)
			} else {
				log.Printf("Message with photo sent successfully to channel %d", channelID)
			}
		}
	}
}

func startHTTPServer(addr string) *http.Server {
	// HTTP Gateway server
	router := mux.NewRouter()
	router.HandleFunc("/api/health", HandleHealth).Methods("GET")

	// Add CORS support for the latest-burn endpoint
	router.HandleFunc("/api/latest-burn", HandleLatestBurnEvent).Methods("GET", "OPTIONS")
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Allow all origins
			w.Header().Set("Access-Control-Allow-Origin", "*")

			// Allow GET and OPTIONS methods
			w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")

			// Allow Content-Type header
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

			// Handle preflight requests
			if r.Method == "OPTIONS" {
				w.WriteHeader(http.StatusOK)
				return
			}

			next.ServeHTTP(w, r)
		})
	})

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

func HandleLatestBurnEvent(w http.ResponseWriter, r *http.Request) {
	latestBurnEventMutex.RLock()
	defer latestBurnEventMutex.RUnlock()

	w.Header().Set("Content-Type", "application/json")

	// If no burn event has occurred yet
	if latestBurnEvent.Timestamp.IsZero() {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "No burn events have occurred yet"})
		return
	}

	json.NewEncoder(w).Encode(latestBurnEvent)
}
