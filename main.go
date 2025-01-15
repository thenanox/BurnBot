package main

import (
	"context"
	"fmt"
	"log"
	"math/big"
	"math/rand"
	"os"
	"strconv"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
)

const (
	smartContract    = "0xc3e823489a201a8654646496b3c318a8c6c9b881"
	initialSupplyStr = "100000000000000000000000000000" // 100 billion CHAOS in Wei
)

// loadEnv loads environment variables from a .env file.
func loadEnv() {
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
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
		"A new chapter of CHAOS unfolds. 🔥🔥",
		"Light up the blockchain with CHAOS burns!",
		"Burn it down, build it stronger—CHAOS reigns.",
		"One burn closer to ultimate CHAOS!",
		"CHAOS burns like a phoenix rising. 🔥",
	}
	rand.Seed(time.Now().UnixNano())
	return messages[rand.Intn(len(messages))]
}

// sendTelegramMessage sends a message with an image to a Telegram channel.
func sendTelegramMessage(bot *tgbotapi.BotAPI, channelID string, message, imageURL string) {
	photo := tgbotapi.NewPhotoToChannel(channelID, tgbotapi.FileURL(imageURL))
	photo.Caption = message
	if _, err := bot.Send(photo); err != nil {
		log.Printf("Error sending photo with message: %v", err)
	} else {
		log.Println("Photo with message sent successfully")
	}
}

// testTelegramConnectivity sends a test message to confirm the bot can post in the Telegram channel.
func testTelegramConnectivity(bot *tgbotapi.BotAPI, channelID, testMessage string) error {
	msg := tgbotapi.NewMessageToChannel(channelID, testMessage)
	_, err := bot.Send(msg)
	if err != nil {
		log.Printf("Error testing Telegram connectivity: %v", err)
		return err
	}
	log.Println("Test message sent successfully to the Telegram channel")
	return nil
}

// listenForBurnEvents listens for burn events and sends Telegram updates.
func listenForBurnEvents(client *ethclient.Client, bot *tgbotapi.BotAPI, channelID string, imageURL string) {
	address := common.HexToAddress(smartContract)

	// Set up a filter query for burn events
	query := ethereum.FilterQuery{
		Addresses: []common.Address{address},
	}

	// Subscribe to logs
	logs := make(chan types.Log)
	sub, err := client.SubscribeFilterLogs(context.Background(), query, logs)
	if err != nil {
		log.Fatalf("Failed to subscribe to logs: %v", err)
	}

	log.Println("Listening for burn events...")

	// Monitor logs for events
	initialSupply, _ := new(big.Float).SetString(initialSupplyStr)

	for vLog := range logs {
		log.Printf("New log received: %v", vLog)

		// Extract the burn data and transaction hash
		txHash := vLog.TxHash.Hex()
		burnAmount := big.NewInt(0).SetBytes(vLog.Data)

		// Calculate total burned percentage
		burnedFloat := new(big.Float).SetInt(burnAmount)
		percentBurned := new(big.Float).Quo(burnedFloat, initialSupply)
		percentBurned.Mul(percentBurned, big.NewFloat(100))

		// Log the parsed data
		log.Printf("Burn Amount: %s, Percent Burned: %.2f%%, TxHash: %s", burnAmount.Text(10), percentBurned, txHash)

		// Get a random message
		randomMessage := getRandomMessage()

		// Prepare the burn message
		message := fmt.Sprintf(
			`🔥🔥🔥CHAOS BURN🔥🔥🔥

AIXBT burned: %s $CHAOS

Total Burned: %.2f%% of supply

%s

View Transaction: https://basescan.org/tx/%s`,
			burnAmount.Text(10),
			percentBurned,
			randomMessage,
			txHash,
		)

		// Send the message with the image to the Telegram channel
		sendTelegramMessage(bot, channelID, message, imageURL)
	}

	// Handle subscription errors
	defer sub.Unsubscribe()
	go func() {
		for err := range sub.Err() {
			log.Printf("Subscription error: %v", err)
		}
	}()
}

func main() {
	// Load environment variables
	loadEnv()
	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	rpcURL := os.Getenv("ETHEREUM_RPC_URL")
	imageURL := os.Getenv("BURN_IMAGE_URL")

	// Validate environment variables
	if botToken == "" || rpcURL == "" || imageURL == "" {
		log.Fatal("Missing required environment variables. Please check your .env file.")
	}

	// Connect to Ethereum client
	client, err := ethclient.Dial(rpcURL)
	if err != nil {
		log.Fatalf("Failed to connect to Ethereum client: %v", err)
	}

	// Create Telegram bot
	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		log.Fatalf("Failed to create Telegram bot: %v", err)
	}

	updates := bot.GetUpdatesChan(tgbotapi.UpdateConfig{})
	var channel string
	for update := range updates {
		if update.ChannelPost != nil {
			channel = strconv.FormatInt(update.ChannelPost.Chat.ID, 10)
			log.Printf("Channel ID detected: %s", channel)
			break // Exit loop once the channel ID is found
		}
	}

	// Send a test message to verify connectivity
	testMessage := "🚀 The CHAOS bot is now online and ready to track burns! 🔥"
	err = testTelegramConnectivity(bot, channel, testMessage)
	if err != nil {
		log.Fatal("Test message failed. Exiting.")
	}

	// Start listening for burn events
	go listenForBurnEvents(client, bot, channel, imageURL)

	// Keep the program running
	select {}
}
