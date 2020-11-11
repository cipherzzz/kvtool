# kvtool

Assorted dev tools for working with the kava blockchain.

## Installation

```bash
make install
```

## Running: kvtool testnet

The `kvtool testnet` command starts a local Kava blockchain, local Binance Chain blockchain, and deploys a deputy that relays swaps between the two.

```bash
# Generate a new kvtool configuration based off template files
kvtool testnet gen-config kava binance deputy --kava.configTemplate master

# Pull latest docker images. Docker must be running.
cd ./full_configs/generated && docker-compose pull

# Start testnet
kvtool testnet up
```

## Interaction: kvtool testnet

REST APIs for both blockchains are exposed on localhost:
- Kava: http://localhost:1317
- Binance Chain: http://localhost:8080

You can also interact with the blockchain using the kvcli command line. In a new terminal window, set up an alias to kvcli on the dockerized kava node and use it to send a query.

```bash
# Add an alias to the dockerized kvcli
alias dkvcli='docker exec -it generated_kavanode_1 kvcli'

# Confirm that the alias has been added
alias
```

You can test the set up and alias by executing a sample query:

```bash
dkvcli q cdp params
```

## Set up: kvtool testnet chain state

To send transactions you'll need to recover a user account in the dockerized environment. Valid mnemonics for the blockchains be found in the `config/common/addresses.yaml` file.

```bash
# Recover user account
dkvcli keys add user --recover
# Enter mnemonic
arrive guide way exit polar print kitchen hair series custom siege afraid shrug crew fashion mind script divorce pattern trust project regular robust safe
```

Test transaction sending by transferring some coins to yourself.
```bash
# Query the recovered account's address
dkvcli keys show user -a
# Send yourself some coins by creating a send transaction with your address as both sender and receiver
dkvcli tx send [user-address] [user-address] 1000000ukava --from user
# Enter 'y' to confirm the transaction
confirm transaction before signing and broadcasting [y/N]:

# Check transaction result by tx hash
dkvcli q tx [tx-hash]
```

## Shut down: kvtool testnet

When you're done make sure to shut down the kvtool testnet. Always shut down the kvtool testnets before pulling the latest image from docker, otherwise you may experience errors.

```bash
kvtool testnet down
```
