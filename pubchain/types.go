package pubchain

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/rs/zerolog"
	"gitlab.com/joltify/joltifychain-bridge/generated"
	"gitlab.com/joltify/joltifychain-bridge/tssclient"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/rs/zerolog/log"

	bcommon "gitlab.com/joltify/joltifychain-bridge/common"
)

const (
	reqCacheSize      = 512
	retryCacheSize    = 128
	chainQueryTimeout = time.Second * 5
	GasLimit          = 2100000
	GasPrice          = "0.00000001"
)

// InboundReq is the account that top up account info to joltify pub_chain
type InboundReq struct {
	address     sdk.AccAddress
	txID        []byte // this indicates the identical inbound req
	toPoolAddr  common.Address
	coin        sdk.Coin
	blockHeight int64
}

func (i *InboundReq) Hash() common.Hash {
	hash := crypto.Keccak256Hash(i.address.Bytes(), i.txID)
	return hash
}

func NewAccountInboundReq(address sdk.AccAddress, toPoolAddr common.Address, coin sdk.Coin, txid []byte, blockHeight int64) InboundReq {
	return InboundReq{
		address,
		txid,
		toPoolAddr,
		coin,
		blockHeight,
	}
}

// GetInboundReqInfo returns the info of the inbound transaction
func (acq *InboundReq) GetInboundReqInfo() (sdk.AccAddress, common.Address, sdk.Coin, int64) {
	return acq.address, acq.toPoolAddr, acq.coin, acq.blockHeight
}

// SetItemHeight sets the block height of the tx
func (acq *InboundReq) SetItemHeight(blockHeight int64) {
	acq.blockHeight = blockHeight
}

func (pi *PubChainInstance) AddItem(req *InboundReq) {
	pi.RetryInboundReq.Store(req.Hash().Big(), req)
}

func (pi *PubChainInstance) PopItem() *InboundReq {
	max := big.NewInt(0)
	pi.RetryInboundReq.Range(func(key, value interface{}) bool {
		h := key.(*big.Int)
		if max.Cmp(h) == -1 {
			max = h
		}
		return true
	})
	if max.Cmp(big.NewInt(0)) == 1 {
		item, _ := pi.RetryInboundReq.LoadAndDelete(max)
		return item.(*InboundReq)
	}
	return nil
}

func (pi *PubChainInstance) Size() int {
	i := 0
	pi.RetryInboundReq.Range(func(key, value interface{}) bool {
		i += 1
		return true
	})
	return i
}

func (pi *PubChainInstance) ShowItems() {
	pi.RetryInboundReq.Range(func(key, value interface{}) bool {
		el := value.(*InboundReq)
		pi.logger.Warn().Msgf("tx in the prepare pool %v:%v\n", key, el.txID)
		return true
	})
	return
}

type inboundTx struct {
	address        sdk.AccAddress
	pubBlockHeight uint64 // this variable is used to delete the expired tx
	token          sdk.Coin
	fee            sdk.Coin
}

type inboundTxBnb struct {
	blockHeight uint64
	txID        string
	fee         sdk.Coin
}

// PubChainInstance hold the joltify_bridge entity
type PubChainInstance struct {
	EthClient          *ethclient.Client
	tokenAddr          string
	tokenInstance      *generated.Token
	tokenAbi           *abi.ABI
	logger             zerolog.Logger
	pendingInbounds    *sync.Map
	pendingInboundsBnB *sync.Map
	lastTwoPools       []*bcommon.PoolInfo
	poolLocker         *sync.RWMutex
	tssServer          tssclient.TssSign
	InboundReqChan     chan *InboundReq
	RetryInboundReq    *sync.Map // if a tx fail to process, we need to put in this channel and wait for retry
	moveFundReq        *sync.Map
	CurrentHeight      int64
}

// NewChainInstance initialize the joltify_bridge entity
func NewChainInstance(ws, tokenAddr string, tssServer tssclient.TssSign) (*PubChainInstance, error) {
	logger := log.With().Str("module", "pubchain").Logger()

	wsClient, err := ethclient.Dial(ws)
	if err != nil {
		logger.Error().Err(err).Msg("fail to dial the websocket")
		return nil, errors.New("fail to dial the network")
	}

	tokenIns, err := generated.NewToken(common.HexToAddress(tokenAddr), wsClient)
	if err != nil {
		return nil, errors.New("fail to get the new token")
	}

	tAbi, err := abi.JSON(strings.NewReader(generated.TokenMetaData.ABI))
	if err != nil {
		return nil, fmt.Errorf("fail to get the tokenABI with err %v", err)
	}

	return &PubChainInstance{
		logger:             logger,
		EthClient:          wsClient,
		tokenAddr:          tokenAddr,
		tokenInstance:      tokenIns,
		tokenAbi:           &tAbi,
		pendingInbounds:    new(sync.Map),
		pendingInboundsBnB: new(sync.Map),
		poolLocker:         &sync.RWMutex{},
		tssServer:          tssServer,
		lastTwoPools:       make([]*bcommon.PoolInfo, 2),
		InboundReqChan:     make(chan *InboundReq, reqCacheSize),
		RetryInboundReq:    &sync.Map{},
		moveFundReq:        &sync.Map{},
	}, nil
}
