# Telegram bot to track burns in chaos token

This is a bot that send messages to a telegram channel where the bot is added as admin. It tracks the burns from an ethereum network.

Right now it is implemented to track one specific wallet burns in an specific contract, but can be easily extended.

To run it locally, it is needed to create an .env file like this:

'''
TELEGRAM_BOT_TOKEN=<token from bot father in telegram>
ETHEREUM_RPC_URL=<infura url to project>
INFURA_API_KEY=<infura key>
BURN_IMAGE_URL=<url to an image to include in the message>
'''

### Roadmap

- Extend to any smart contract and wallet by configuration
- Work with any ethereum network (not just base)
- Generalize to use any blockchain provider (not just infura)
- Collection of messages externalized or configured by telegram commands
