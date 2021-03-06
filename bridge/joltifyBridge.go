package bridge

import (
	"context"
	"fmt"
	"html"
	"io/ioutil"
	"log"
	"os"
	"os/signal"
	"path"
	"sync"
	"time"

	"gitlab.com/joltify/joltifychain-bridge/monitor"
	"gitlab.com/joltify/joltifychain-bridge/tssclient"

	"gitlab.com/joltify/joltifychain-bridge/config"
	"gitlab.com/joltify/joltifychain-bridge/joltifybridge"
	"gitlab.com/joltify/joltifychain-bridge/pubchain"

	zlog "github.com/rs/zerolog/log"

	tmtypes "github.com/tendermint/tendermint/types"
)

// NewBridgeService starts the new bridge service
func NewBridgeService(config config.Config) {
	wg := sync.WaitGroup{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)

	passcodeLength := 32
	passcode := make([]byte, passcodeLength)
	n, err := os.Stdin.Read(passcode)
	if err != nil {
		return
	}
	if n > passcodeLength {
		log.Fatalln("the passcode is too long")
		return
	}

	metrics := monitor.NewMetric()
	if config.EnableMonitor {
		metrics.Enable()
	}

	// fixme, in docker it needs to be changed to basehome
	tssServer, _, err := tssclient.StartTssServer(config.HomeDir, config.TssConfig)
	if err != nil {
		log.Fatalln("fail to start the tss")
		return
	}

	joltifyBridge, err := joltifybridge.NewJoltifyBridge(config.JoltifyChain.GrpcAddress, config.JoltifyChain.WsAddress, tssServer)
	if err != nil {
		log.Fatalln("fail to create the invoice joltify_bridge", err)
		return
	}

	keyringPath := path.Join(config.HomeDir, config.KeyringAddress)

	dat, err := ioutil.ReadFile(keyringPath)
	if err != nil {
		log.Fatalln("error in read keyring file")
		return
	}

	// fixme need to update the passcode
	err = joltifyBridge.Keyring.ImportPrivKey("operator", string(dat), "12345678")
	if err != nil {
		return
	}

	defer func() {
		err := joltifyBridge.TerminateBridge()
		if err != nil {
			return
		}
	}()

	err = joltifyBridge.InitValidators(config.JoltifyChain.HTTPAddress)
	if err != nil {
		fmt.Printf("error in init the validators %v", err)
		cancel()
		return
	}
	tssHTTPServer := NewJoltifyHttpServer(ctx, config.TssConfig.HTTPAddr, joltifyBridge.GetTssNodeID())

	wg.Add(1)
	ret := tssHTTPServer.Start(&wg)
	if ret != nil {
		cancel()
		return
	}

	// now we monitor the bsc transfer event
	ci, err := pubchain.NewChainInstance(config.PubChainConfig.WsAddress, config.PubChainConfig.TokenAddress, tssServer)
	if err != nil {
		fmt.Printf("fail to connect the public pub_chain with address %v\n", config.PubChainConfig.WsAddress)
		cancel()
		return
	}

	wg.Add(1)
	addEventLoop(ctx, &wg, joltifyBridge, ci, metrics)

	<-c
	ctx.Done()
	cancel()
	wg.Wait()
	fmt.Printf("we quit gracefully\n")
}

func addEventLoop(ctx context.Context, wg *sync.WaitGroup, joltChain *joltifybridge.JoltifyChainInstance, pi *pubchain.PubChainInstance, metric *monitor.Metric) {
	defer wg.Done()
	query := "tm.event = 'ValidatorSetUpdates'"
	ctxLocal, cancelLocal := context.WithTimeout(ctx, time.Second*5)
	defer cancelLocal()

	validatorUpdateChan, err := joltChain.AddSubscribe(ctxLocal, query)
	if err != nil {
		fmt.Printf("fail to start the subscription")
		return
	}

	query = "tm.event = 'NewBlock'"
	newBlockChan, err := joltChain.AddSubscribe(ctxLocal, query)
	if err != nil {
		fmt.Printf("fail to start the subscription")
		return
	}

	query = "tm.event = 'Tx'"

	newJoltifyTxChan, err := joltChain.AddSubscribe(ctxLocal, query)
	if err != nil {
		fmt.Printf("fail to start the subscription")
		return
	}

	wg.Add(1)

	// pubNewBlockChan is the channel for the new blocks for the public chain
	pubNewBlockChan, err := pi.StartSubscription(ctx, wg)
	if err != nil {
		fmt.Printf("fail to subscribe the token transfer with err %v\n", err)
		return
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
				// process the update of the validators
			case vals := <-validatorUpdateChan:
				height, err := joltChain.GetLastBlockHeight()
				if err != nil {
					continue
				}
				validatorUpdates := vals.Data.(tmtypes.EventDataValidatorSetUpdates).ValidatorUpdates
				err = joltChain.HandleUpdateValidators(validatorUpdates, height)
				if err != nil {
					fmt.Printf("error in handle update validator")
					continue
				}

				// process the new joltify block, validator may need to submit the pool address
			case block := <-newBlockChan:
				currentBlockHeight := block.Data.(tmtypes.EventDataNewBlock).Block.Height
				joltChain.CheckAndUpdatePool(currentBlockHeight)
				joltChain.CurrentHeight = currentBlockHeight
				// now we check whether we need to update the pool
				// we query the pool from the chain directly.
				poolInfo, err := joltChain.QueryLastPoolAddress()
				if err != nil {
					zlog.Logger.Error().Err(err).Msgf("error in get pool with error %v", err)
					continue
				}
				if len(poolInfo) != 2 {
					zlog.Logger.Warn().Msgf("the pool only have %v address, bridge will not work", len(poolInfo))
					continue
				}

				// now we need to put the failed inbound request to the process channel, for each new joltify block
				// we process one failure
				itemInbound := pi.PopItem()
				metric.UpdateInboundTxNum(float64(pi.Size()))
				if itemInbound != nil {
					itemInbound.SetItemHeight(currentBlockHeight)
					pi.InboundReqChan <- itemInbound
				}

				currentPool := pi.GetPool()
				// this means the pools has not been filled with two address
				if currentPool[0] == nil {
					for _, el := range poolInfo {
						err := pi.UpdatePool(el)
						if err != nil {
							zlog.Log().Err(err).Msgf("fail to update the pool")
						}
						joltChain.UpdatePool(el)
					}
					continue
				}

				if NeedUpdate(poolInfo, currentPool) {
					err := pi.UpdatePool(poolInfo[0])
					if err != nil {
						zlog.Log().Err(err).Msgf("fail to update the pool")
					}
					previousPool := joltChain.UpdatePool(poolInfo[0])
					if previousPool.Pk != poolInfo[0].CreatePool.PoolPubKey {
						// we force the first try of the tx to be run without blocking by the block wait
						joltChain.AddMoveFundItem(previousPool, currentBlockHeight-config.MINCHECKBLOCKGAP+5)
						pi.AddMoveFundItem(previousPool, pi.CurrentHeight-config.MINCHECKBLOCKGAP+5)
					}
				}

				// we move fund if some pool retired
				previousPool, _ := joltChain.PopMoveFundItemAfterBlock(currentBlockHeight)
				if previousPool == nil {
					continue
				}
				// we get the latest pool address and move funds to the latest pool
				isSigner, err := joltChain.CheckWhetherSigner(previousPool.PoolInfo)
				if err != nil {
					zlog.Logger.Warn().Msg("fail in check whether we are signer in moving fund")
					continue
				}
				if !isSigner {
					continue
				}
				emptyAcc, err := joltChain.MoveFunds(previousPool, poolInfo[0].CreatePool.PoolAddr, currentBlockHeight)
				if emptyAcc {
					tick := html.UnescapeString("&#" + "127974" + ";")
					zlog.Logger.Info().Msgf("%v successfully moved funds from %v to %v", tick, previousPool.JoltifyAddress.String(), poolInfo[0].CreatePool.PoolAddr.String())
					continue
				}
				if err != nil {
					zlog.Log().Err(err).Msgf("fail to move the fund from %v to %v", previousPool.JoltifyAddress.String(), poolInfo[1].CreatePool.PoolAddr.String())
				}
				joltChain.AddMoveFundItem(previousPool, currentBlockHeight)

			case r := <-newJoltifyTxChan:
				result := r.Data.(tmtypes.EventDataTx).Result
				if result.Code != 0 {
					// this means this tx is not a successful tx
					zlog.Warn().Msgf("not a valid top up message with error code %v (%v)", result.Code, result.Log)
					continue
				}
				blockHeight := r.Data.(tmtypes.EventDataTx).Height
				tx := r.Data.(tmtypes.EventDataTx).Tx
				joltChain.CheckOutBoundTx(blockHeight, tx)

				// process the public chain new block event
			case head := <-pubNewBlockChan:
				err := pi.ProcessNewBlock(head.Number)
				pi.CurrentHeight = head.Number.Int64()
				if err != nil {
					zlog.Logger.Error().Err(err).Msg("fail to process the inbound block")
				}
				// we delete the expired tx
				pi.DeleteExpired(head.Number.Uint64())

				// now we need to put the failed outbound request to the process channel
				// todo need to check after a given block gap
				itemOutBound := joltChain.PopItem()
				metric.UpdateOutboundTxNum(float64(joltChain.Size()))
				if itemOutBound != nil {
					itemOutBound.SetItemHeight(head.Number.Int64())
					joltChain.OutboundReqChan <- itemOutBound
				}

				// we move fund in the public chain
				previousPool, _ := pi.PopMoveFundItemAfterBlock(int64(head.Number.Uint64()))
				if previousPool == nil {
					continue
				}

				// we get the latest pool address and move funds to the latest pool
				currentPool := pi.GetPool()
				isSigner, err := joltChain.CheckWhetherSigner(previousPool.PoolInfo)
				if err != nil {
					zlog.Logger.Warn().Msg("fail in check whether we are signer in moving fund")
					continue
				}
				if !isSigner {
					continue
				}
				emptyAccount, err := pi.MoveFunds(previousPool, currentPool[1].EthAddress, head.Number.Int64())
				if err != nil {
					zlog.Log().Err(err).Msgf("fail to move the fund from %v to %v", previousPool.EthAddress.String(), currentPool[1].EthAddress.String())
					pi.AddMoveFundItem(previousPool, pi.CurrentHeight)
					continue
				}
				if emptyAccount {
					tick := html.UnescapeString("&#" + "9989" + ";")
					zlog.Logger.Info().Msgf("%v account %v is clear no need to move", tick, previousPool.EthAddress.String())
					continue
				}

				// we add this account to "retry" to ensure it is the empty account in the next balance check
				pi.AddMoveFundItem(previousPool, pi.CurrentHeight)

			// process the in-bound top up event which will mint coin for users
			case item := <-pi.InboundReqChan:
				// first we check whether this tx has already been submitted by others
				pools := joltChain.GetPool()
				found, err := joltChain.CheckWhetherSigner(pools[1].PoolInfo)
				if err != nil {
					zlog.Logger.Error().Err(err).Msg("fail to check whether we are the node submit the mint request")
					continue
				}
				if found {
					txHash, index, err := joltChain.ProcessInBound(item)
					if err != nil {
						pi.AddItem(item)
						zlog.Logger.Error().Err(err).Msg("fail to mint the coin for the user")
						continue
					}

					go func() {
						err := joltChain.CheckTxStatus(index)
						if err != nil {
							zlog.Logger.Error().Err(err).Msgf("the tx has not been sussfully submitted retry")
							pi.AddItem(item)
						}
						tick := html.UnescapeString("&#" + "128229" + ";")
						zlog.Logger.Info().Msgf("%v txid(%v) have successfully top up", tick, txHash)
					}()

				}

			case item := <-joltChain.OutboundReqChan:
				pools := joltChain.GetPool()
				found, err := joltChain.CheckWhetherSigner(pools[1].PoolInfo)
				if err != nil {
					zlog.Logger.Error().Err(err).Msg("fail to check whether we are the node submit the mint request")
					continue
				}
				if found {
					toAddr, fromAddr, amount, blockHeight := item.GetOutBoundInfo()
					txHash, err := pi.ProcessOutBound(toAddr, fromAddr, amount, blockHeight)
					if err != nil {
						zlog.Logger.Error().Err(err).Msg("fail to broadcast the tx")
						joltChain.AddItem(item)
					} else {
						// though we submit the tx successful, we may still fail as tx may run out of gas,so we need to check
						go func() {
							err := pi.CheckTxStatus(txHash)
							if err == nil {
								tick := html.UnescapeString("&#" + "128229" + ";")
								zlog.Logger.Info().Msgf("%v we have send outbound tx(%v) from %v to %v (%v)", tick, txHash, fromAddr, toAddr, amount.String())
								return
							}
							if err.Error() != "tx failed" {
								zlog.Logger.Info().Msgf("fail to check the tx status")
								return
							}

							zlog.Logger.Warn().Msgf("the tx is fail in submission, we need to resend")
							joltChain.AddItem(item)
						}()
					}
				}

			}
		}
	}()
}
