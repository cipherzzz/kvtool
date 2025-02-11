package testnet

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/kava-labs/kvtool/config/generate"
	"github.com/spf13/cobra"
)

// this env variable is used in supported kava templates to allow override of the image tag
// automated chain upgrades make use of it to switch between binary versions.
const kavaTagEnv = "KAVA_TAG"

func BootstrapCmd() *cobra.Command {
	bootstrapCmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "A convenience command that creates a kava testnet with the input configTemplate (defaults to master)",
		Long: `Generate a kava testnet and optionally run/integrate various other resources.

# General Overview
This command runs local networks by performing the following three steps:
1. Configure the desired resources with templates that are combined into a primary docker-compose configuration.
2. Standup the docker-compose containers and network.
3. Perform various modifications, restarts, configurations to the live networks based on the desired topology.

# Templates
The building blocks of the bootstrap command's services are templates which are defined in the
directory kvtool/config/templates. As of right now, the template you run Kava with is configurable
at runtime via the 'kava.configTemplate' flag. The Kava templates contain kava config directories
(including a genesis.json) that are supported with the corresponding kava docker image tag.

Some templates, like "master", support overriding the image tag via the KAVA_TAG env variable.

# IBC
The bootstrap command supports running a secondary chain and opening an IBC channel between the
primary Kava node and the secondary chain. To set this up, simply use the --ibc flag.

Once the two chains are started, the necessary txs are run to open a channel between the two chains
and a relayer is started to relay transactions between them. The primary denom of the secondary chain
is "uatom" and it runs under the docker container named "ibcchain".

# Automated Chain Upgrades
The bootstrap command supports running a chain that is then upgraded via an upgrade handler. The following
flags are all required to run an automated software upgrade:

--upgrade-name           - the name of the registered upgrade handler to be run.
--upgrade-height         - the height at which the upgrade should occur.
--upgrade-base-image-tag - the docker image tag of Kava that with which the chain is started.

Note that the upgrade height must be high enough to facilitate the submission of an upgrade proposal,
and the voting on it. If used with --ibc, note that the upgrade is initiated _after_ the IBC channel
is opened, which can take 70+ blocks.

When these flags are defined, the chain is initially started with the --upgrade-base-image-tag tag.
As soon as the chain is configured & producing blocks, a committee proposal is submitted to update
the chain. The committee uses First-Pass-the-Post voting so passes as soon as it gets consensus.
The committee member account votes on the proposal and then we wait for the upgrade height to be
reached. At that point, the chain halts and is restarted with the updated image tag.`,
		Example: `Run kava node with particular template:
$ kvtool testnet bootstrap --kava.configTemplate v0.12

Run kava & another chain with open IBC channel & relayer:
$ kvtool testnet bootstrap --ibc

Run a kava network with an additional pruning node:
$ kvtool testnet bootstrap --pruning

Run kava & an ethereum node:
$ kvtool testnet bootstrap --geth

The master template supports dynamic override of the Kava node's container image:
$ KAVA_TAG=v0.21.0 kvtool testnet bootstrap

Test a chain upgrade from v0.19.2 -> v0.21.0:
$ KAVA_TAG=v0.21.0 kvtool testnet bootstrap --upgrade-name v0.21.0 --upgrade-height 15 --upgrade-base-image-tag v0.19.2
`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := validateBootstrapFlags(); err != nil {
				return err
			}

			// shutdown existing networks if a docker-compose.yaml already exists.
			if _, err := os.Stat(generatedPath("docker-compose.yaml")); err == nil {
				if err2 := dockerComposeCmd("down").Run(); err2 != nil {
					return err2
				}
			}

			// remove entire generated dir in order to start from scratch
			if err := os.RemoveAll(generatedConfigDir); err != nil {
				return fmt.Errorf("could not clear old generated config: %v", err)
			}

			// generate kava node configuration
			if err := generate.GenerateKavaConfig(kavaConfigTemplate, generatedConfigDir); err != nil {
				return err
			}
			// handle pruning node configuration
			if includePruningFlag {
				if err := generate.GenerateKavaPruningConfig(kavaConfigTemplate, generatedConfigDir); err != nil {
					return err
				}
			}
			// handle ibc configuration
			if ibcFlag {
				if err := generate.GenerateIbcConfigs(generatedConfigDir); err != nil {
					return err
				}
			}
			// handle geth configuration
			if gethFlag {
				if err := generate.GenerateGethConfig(generatedConfigDir); err != nil {
					return err
				}
			}

			// pull the kava image tag if not overridden to be "local"
			kavaTagOverride := os.Getenv(kavaTagEnv)
			if kavaTagOverride != "local" {
				if err := dockerComposeCmd("pull").Run(); err != nil {
					fmt.Println(err.Error())
				}
			}

			upCmd := dockerComposeCmd("up", "-d", "--remove-orphans")
			// when doing automated chain upgrade, ensure the node starts with the desired image tag
			// if this is empty, the docker-compose should default to intended image tag
			if chainUpgradeBaseImageTag != "" {
				upCmd.Env = os.Environ()
				upCmd.Env = append(upCmd.Env, fmt.Sprintf("%s=%s", kavaTagEnv, chainUpgradeBaseImageTag))
				fmt.Printf("starting chain with image tag %s\n", chainUpgradeBaseImageTag)
			}
			if err := upCmd.Run(); err != nil {
				fmt.Println(err.Error())
			}

			if ibcFlag {
				if err := setupIbcChannelAndRelayer(); err != nil {
					return err
				}
			}

			// validation of all necessary data for an automated chain upgrade is performed in validateBootstrapFlags()
			if chainUpgradeName != "" {
				if err := runChainUpgrade(); err != nil {
					return err
				}
			}

			return nil
		},
	}

	bootstrapCmd.Flags().StringVar(&kavaConfigTemplate, "kava.configTemplate", "master", "the directory name of the template used to generating the kava config")
	bootstrapCmd.Flags().BoolVar(&includePruningFlag, "pruning", false, "flag for running pruning node alongside kava validator")
	bootstrapCmd.Flags().BoolVar(&ibcFlag, "ibc", false, "flag for if ibc is enabled")
	bootstrapCmd.Flags().BoolVar(&gethFlag, "geth", false, "flag for if geth is enabled")

	// optional data for running an automated chain upgrade
	bootstrapCmd.Flags().StringVar(&chainUpgradeName, "upgrade-name", "", "name of automated chain upgrade to run, if desired. the upgrade must be defined in the kava image container.")
	bootstrapCmd.Flags().Int64Var(&chainUpgradeHeight, "upgrade-height", 0, "height of automated chain upgrade to run.")
	bootstrapCmd.Flags().StringVar(&chainUpgradeBaseImageTag, "upgrade-base-image-tag", "", "the kava docker image tag that will be upgraded.\nthe chain is initialized from this tag and then upgraded to the new image.\nthe binary must be compatible with the kava.configTemplate genesis.json.")

	return bootstrapCmd
}

func validateBootstrapFlags() error {
	hasUpgradeName := chainUpgradeName != ""
	hasUpgradeBaseImageTag := chainUpgradeBaseImageTag != ""
	// the upgrade flags are all or nothing. both the upgrade name and the image tag are required for
	// an automated chain upgrade
	if (hasUpgradeName && !hasUpgradeBaseImageTag) || (hasUpgradeBaseImageTag && !hasUpgradeName) {
		return fmt.Errorf("automated chain upgrades require both --upgrade-name and --upgrade-base-image-tag to be defined")
	}
	// if running an automate chain upgrade, there must be a sufficiently high upgrade height.
	if hasUpgradeName && chainUpgradeHeight < 10 {
		// TODO: is 10 a sufficient height for an upgrade to occur with proposal & voting? probs not..
		return fmt.Errorf("upgrade height must be > 10, found %d", chainUpgradeHeight)
	}
	if kavaConfigTemplate == "pruning-node" {
		return fmt.Errorf("the pruning node must be run alongside a different template, see --pruning")
	}
	return nil
}

func setupIbcChannelAndRelayer() error {
	// wait for chains to be up and running before setting up ibc
	// wait for block 2, as waiting only for block 1 sometimes leads to client expiration problems
	if err := waitForBlock(2, 10*time.Second, "kavanode"); err != nil {
		return err
	}
	if err := waitForBlock(2, 5*time.Second, "ibcnode"); err != nil {
		return err
	}

	fmt.Println("Attempting to establish IBC channel connection between chains...")
	// open the channel between kava and ibcnode
	openConnectionCmd := exec.Command("docker", "run", "-v", fmt.Sprintf("%s:%s", generatedPath("relayer"), "/home/relayer/.relayer"), "--name", "ibc-relayer", "--rm", "--net", "generated_default", relayerImageTag, "rly", "transact", "link", "transfer", "-r", "10", "-t", "30s")
	openConnectionCmd.Stdout = os.Stdout
	openConnectionCmd.Stderr = os.Stderr
	if err := openConnectionCmd.Run(); err != nil {
		fmt.Println(err.Error())
		return fmt.Errorf("[relayer] failed to open ibc connection")
	}
	fmt.Printf("IBC connection complete, starting relayer process...\n")
	// setup and run the relayer
	if err := generate.AddRelayerToNetwork(generatedConfigDir); err != nil {
		return err
	}
	if err := dockerComposeCmd("up", "-d", "relayer").Run(); err != nil {
		return err
	}
	// prune temp containers used to initialize ibc channel
	pruneCmd := exec.Command("docker", "container", "prune", "-f")
	pruneCmd.Stdout = os.Stdout
	pruneCmd.Stderr = os.Stderr
	if err := pruneCmd.Run(); err != nil {
		return err
	}
	fmt.Println("IBC relayer ready!")
	return nil
}

func runChainUpgrade() error {
	fmt.Printf(
		"configured for automated chain upgrade\n\tupgrade name: %s\n\tupgrade height: %d\n\tstarting tag: %s\n",
		chainUpgradeName, chainUpgradeHeight, chainUpgradeBaseImageTag,
	)

	// write upgrade proposal to json file
	upgradeJson, err := writeUpgradeProposal()
	if err != nil {
		return err
	}

	// wait for chain to start producing blocks
	if err := waitForBlock(1, 5*time.Second, "kavanode"); err != nil {
		return err
	}

	// submit upgrade proposal via God Committee (committee 3)
	fmt.Println("submitting upgrade proposal")
	cmd := fmt.Sprintf("tx committee submit-proposal 3 %s --gas auto --gas-adjustment 1.2 --gas-prices 0.05ukava --from committee -y",
		upgradeJson,
	)
	if err := runKavaCli(strings.Split(cmd, " ")...); err != nil {
		return err
	}

	// vote on the committee proposal
	cmd = "tx committee vote 1 yes --from committee --gas auto --gas-adjustment 1.2 --gas-prices 0.01ukava -y"
	if err := runKavaCli(strings.Split(cmd, " ")...); err != nil {
		return err
	}

	// wait for chain halt at upgrade height
	if err := waitForBlock(chainUpgradeHeight, time.Duration(chainUpgradeHeight)*4*time.Second, "kavanode"); err != nil {
		return err
	}
	fmt.Printf("chain has reached upgrade height (%d) and halted!\n", chainUpgradeHeight)

	fmt.Println("restarting chain with upgraded image")
	// this runs with the desired image because KAVA_TAG will be correctly set, or if that is unset,
	// the docker-compose files supporting upgrades default to the desired template version.
	if err := dockerComposeCmd("up", "--force-recreate", "-d", "kavanode").Run(); err != nil {
		return err
	}

	return nil
}

// writeUpgradeProposal writes a proposal json to a file in the kavanode container and returns the path
func writeUpgradeProposal() (string, error) {
	content := fmt.Sprintf(`{
		"@type": "/cosmos.upgrade.v1beta1.SoftwareUpgradeProposal",
		"title": "Automated Chain Upgrade",
		"description": "An auto-magical chain upgrade performed by kvtool.",
		"plan": { "name": "%s", "height": "%d" }
	}`, chainUpgradeName, chainUpgradeHeight)
	// write the file to a location inside the container
	return "/root/.kava/config/upgrade-proposal.json", os.WriteFile(
		generatedPath("kava", "initstate", ".kava", "config", "upgrade-proposal.json"),
		[]byte(content),
		0644,
	)
}

func waitForBlock(n int64, timeout time.Duration, chainDockerServiceName string) error {
	b := backoff.NewExponentialBackOff()
	b.MaxInterval = 2 * time.Second
	b.MaxElapsedTime = timeout
	return backoff.Retry(blockGTE(chainDockerServiceName, n), b)
}

// blockGTE is a backoff operation that uses kava's CLI to query the chain for the current block number
// the operation fails in the following cases:
// 1. the chain cannot be reached, 2. result cannot be parsed, 3. current height is less than desired height `n`
func blockGTE(chainDockerServiceName string, n int64) backoff.Operation {
	return func() error {
		cmd := "kava status | jq -r .sync_info.latest_block_height"
		// can't use dockerComposeCmd because Output() sets Stdout
		out, err := exec.Command("docker-compose", "-f", generatedPath("docker-compose.yaml"), "exec", "-T", chainDockerServiceName, "bash", "-c", cmd).Output()
		if err != nil {
			return err
		}
		height, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
		if err != nil {
			return err
		}
		if height < n {
			fmt.Printf("waiting for %s to reach height %d, currently @ %d\n", chainDockerServiceName, n, height)
			return fmt.Errorf("waiting for height %d, found %d", n, height)
		}
		return nil
	}
}

// runKavaCli execs into the kava container and runs `kava args...`
func runKavaCli(args ...string) error {
	pieces := make([]string, 4, len(args)+4)
	pieces[0] = "exec"
	pieces[1] = "-T"
	pieces[2] = "kavanode"
	pieces[3] = "kava"
	pieces = append(pieces, args...)
	return dockerComposeCmd(pieces...).Run()
}

func dockerComposeCmd(args ...string) *exec.Cmd {
	// exec.Command requires all items to be in single []string variadic
	// combine the args with the file flag & value
	pieces := []string{"-f", generatedPath("docker-compose.yaml")}
	pieces = append(pieces, args...)
	cmd := exec.Command("docker-compose", pieces...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}
