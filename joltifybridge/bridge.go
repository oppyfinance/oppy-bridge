package joltifybridge

import (
	"context"
	"encoding/base64"
	"errors"
	"io/ioutil"
	"log"
	"strconv"
	"sync"

	"github.com/cosmos/cosmos-sdk/types/bech32/legacybech32"
	cosTx "github.com/cosmos/cosmos-sdk/types/tx"
	ctypes "github.com/tendermint/tendermint/rpc/core/types"
	"gitlab.com/joltify/joltifychain-bridge/config"

	"go.uber.org/atomic"

	bcommon "gitlab.com/joltify/joltifychain-bridge/common"

	tendertypes "github.com/tendermint/tendermint/types"

	coscrypto "github.com/cosmos/cosmos-sdk/crypto/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/joltgeorge/tss/common"
	"github.com/joltgeorge/tss/keysign"
	"github.com/tendermint/tendermint/crypto"
	tmclienthttp "github.com/tendermint/tendermint/rpc/client/http"
	"gitlab.com/joltify/joltifychain-bridge/misc"
	"gitlab.com/joltify/joltifychain-bridge/tssclient"
	"gitlab.com/joltify/joltifychain/x/vault/types"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	zlog "github.com/rs/zerolog/log"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	"google.golang.org/grpc"

	xauthsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"
)

// NewJoltifyBridge new the instance for the joltify pub_chain
func NewJoltifyBridge(grpcAddr, keyringPath, passcode string, tssServer *tssclient.BridgeTssServer) (*JoltifyChainBridge, error) {
	var joltifyBridge JoltifyChainBridge
	var err error
	joltifyBridge.logger = zlog.With().Str("module", "joltifyChain").Logger()

	joltifyBridge.grpcClient, err = grpc.Dial(grpcAddr, grpc.WithInsecure())
	if err != nil {
		return nil, err
	}

	client, err := tmclienthttp.New("tcp://localhost:26657", "/websocket")
	if err != nil {
		return nil, err
	}
	err = client.Start()
	if err != nil {
		return nil, err
	}

	joltifyBridge.wsClient = client

	joltifyBridge.keyring = keyring.NewInMemory()

	dat, err := ioutil.ReadFile(keyringPath)
	if err != nil {
		log.Fatalln("error in read keyring file")
		return nil, err
	}
	err = joltifyBridge.keyring.ImportPrivKey("operator", string(dat), passcode)
	if err != nil {
		return nil, err
	}

	joltifyBridge.tssServer = tssServer

	joltifyBridge.msgSendCache = []tssPoolMsg{}
	joltifyBridge.lastTwoPools = make([]*bcommon.PoolInfo, 2)
	joltifyBridge.poolUpdateLocker = &sync.RWMutex{}

	// we put the dummy query here to avoid the panic
	query := "tm.event = 'Tx' AND transfer.sender = 'jolt1x'"
	out, err := client.Subscribe(context.Background(), "query", query)
	if err != nil {
		zlog.Logger.Error().Err(err).Msg("fail to subscribe the new transfer pool address")
	}
	joltifyBridge.TransferChan = make([]*<-chan ctypes.ResultEvent, 2)
	joltifyBridge.TransferChan = []*<-chan ctypes.ResultEvent{&out, &out}
	encode := MakeEncodingConfig()
	joltifyBridge.encoding = &encode
	joltifyBridge.pendingOutbounds = new(sync.Map)
	joltifyBridge.OutboundReqChan = make(chan *OutBoundReq, reqCacheSize)
	joltifyBridge.RetryOutboundReq = make(chan *OutBoundReq, reqCacheSize)
	joltifyBridge.poolAccLocker = &sync.Mutex{}
	return &joltifyBridge, nil
}

func (jc *JoltifyChainBridge) GetTssNodeID() string {
	return jc.tssServer.GetTssNodeID()
}

func (jc *JoltifyChainBridge) TerminateBridge() error {
	err := jc.wsClient.Stop()
	if err != nil {
		jc.logger.Error().Err(err).Msg("fail to terminate the ws")
		return err
	}
	err = jc.grpcClient.Close()
	if err != nil {
		jc.logger.Error().Err(err).Msg("fail to terminate the grpc")
		return err
	}
	jc.tssServer.Stop()
	return nil
}

func (jc *JoltifyChainBridge) genSendTx(sdkMsg []sdk.Msg, accSeq, accNum, gasWanted uint64, tssSignMsg *tssclient.TssSignigMsg) (client.TxBuilder, error) {
	// Choose your codec: Amino or Protobuf. Here, we use Protobuf, given by the
	// following function.
	encCfg := *jc.encoding
	// Create a new TxBuilder.
	txBuilder := encCfg.TxConfig.NewTxBuilder()

	err := txBuilder.SetMsgs(sdkMsg...)
	if err != nil {
		return nil, err
	}

	// we use the default here
	txBuilder.SetGasLimit(gasWanted)
	// txBuilder.SetFeeAmount(...)
	// txBuilder.SetMemo(...)
	// txBuilder.SetTimeoutHeight(...)

	key, err := jc.keyring.Key("operator")
	if err != nil {
		jc.logger.Error().Err(err).Msg("fail to get the operator key")
		return nil, err
	}

	var sigV2 signing.SignatureV2
	if tssSignMsg == nil {
		sigV2 = signing.SignatureV2{
			PubKey: key.GetPubKey(),
			Data: &signing.SingleSignatureData{
				SignMode:  encCfg.TxConfig.SignModeHandler().DefaultMode(),
				Signature: nil,
			},
			Sequence: accSeq,
		}
	} else {
		pk := tssSignMsg.Pk
		cPk, err := legacybech32.UnmarshalPubKey(legacybech32.AccPK, pk)
		if err != nil {
			jc.logger.Error().Err(err).Msgf("fail to get the public key from bech32 format")
			return nil, err
		}
		sigV2 = signing.SignatureV2{
			PubKey: cPk,
			Data: &signing.SingleSignatureData{
				SignMode:  encCfg.TxConfig.SignModeHandler().DefaultMode(),
				Signature: nil,
			},
			Sequence: accSeq,
		}

	}

	err = txBuilder.SetSignatures(sigV2)
	if err != nil {
		return nil, err
	}

	signerData := xauthsigning.SignerData{
		ChainID:       chainID,
		AccountNumber: accNum,
		Sequence:      accSeq,
	}
	signatureV2, err := jc.signTx(encCfg.TxConfig, txBuilder, signerData, tssSignMsg)
	if err != nil {
		jc.logger.Error().Err(err).Msg("fail to generate the signature")
		return nil, err
	}
	err = txBuilder.SetSignatures(signatureV2)
	if err != nil {
		jc.logger.Error().Err(err).Msgf("fail to set the signature")
		return nil, err
	}

	return txBuilder, nil
}

func (jc *JoltifyChainBridge) signTx(txConfig client.TxConfig, txBuilder client.TxBuilder, signerData xauthsigning.SignerData, signMsg *tssclient.TssSignigMsg) (signing.SignatureV2, error) {
	var sigV2 signing.SignatureV2

	signMode := txConfig.SignModeHandler().DefaultMode()
	// Generate the bytes to be signed.
	signBytes, err := txConfig.SignModeHandler().GetSignBytes(signMode, signerData, txBuilder.GetTx())
	if err != nil {
		return sigV2, err
	}

	var signature []byte
	var pk coscrypto.PubKey
	if signMsg == nil {
		// Sign those bytes by the node itself
		signature, pk, err = jc.keyring.Sign("operator", signBytes)
		if err != nil {
			return sigV2, err
		}
	} else {
		hashedMsg := crypto.Sha256(signBytes)
		encodedMsg := base64.StdEncoding.EncodeToString(hashedMsg)
		signMsg.Msgs = []string{encodedMsg}
		resp, err := jc.doTssSign(signMsg)
		if err != nil {
			return signing.SignatureV2{}, err
		}
		if resp.Status != common.Success {
			jc.logger.Error().Err(err).Msg("fail to generate the signature")
			// todo we need to handle the blame
			return signing.SignatureV2{}, err
		}
		if len(resp.Signatures) != 1 {
			jc.logger.Error().Msgf("we should only have 1 signature")
			return signing.SignatureV2{}, errors.New("more than 1 signature received")
		}
		signature, err = misc.SerializeSig(&resp.Signatures[0], false)
		if err != nil {
			jc.logger.Error().Msgf("fail to encode the signature")
			return signing.SignatureV2{}, err
		}

		pubkey, err := legacybech32.UnmarshalPubKey(legacybech32.AccPK, signMsg.Pk)
		if err != nil {
			jc.logger.Error().Err(err).Msgf("fail to get the pubkey")
			return signing.SignatureV2{}, err
		}
		pk = pubkey
	}

	// Construct the SignatureV2 struct
	sigData := signing.SingleSignatureData{
		SignMode:  signMode,
		Signature: signature,
	}

	sigV2 = signing.SignatureV2{
		PubKey:   pk,
		Data:     &sigData,
		Sequence: signerData.Sequence,
	}
	return sigV2, nil
}

func (jc *JoltifyChainBridge) doTssSign(msg *tssclient.TssSignigMsg) (keysign.Response, error) {
	resp, err := jc.tssServer.KeySign(msg.Pk, msg.Msgs, msg.BlockHeight, msg.Signers, tssclient.TssVersion)
	if err != nil {
		jc.logger.Error().Err(err).Msg("fail to generate the tss signature")
		return keysign.Response{}, err
	}
	return resp, nil
}

// SimBroadcastTx broadcast the tx to the joltifyChain to get gas estimation
func (jc *JoltifyChainBridge) SimBroadcastTx(ctx context.Context, txbytes []byte) (uint64, error) {
	// Broadcast the tx via gRPC. We create a new client for the Protobuf Tx
	// service.
	txClient := cosTx.NewServiceClient(jc.grpcClient)
	// We then call the BroadcastTx method on this client.
	grpcRes, err := txClient.Simulate(ctx, &cosTx.SimulateRequest{TxBytes: txbytes})
	if err != nil {
		return 0, err
	}
	gasUsed := grpcRes.GetGasInfo().GasUsed
	return gasUsed, nil
}

// GasEstimation this function get the estimation of the fee
func (jc *JoltifyChainBridge) GasEstimation(sdkMsg []sdk.Msg, accSeq uint64, tssSignMsg *tssclient.TssSignigMsg) (uint64, error) {
	encoding := MakeEncodingConfig()
	encCfg := encoding
	// Create a new TxBuilder.
	txBuilder := encCfg.TxConfig.NewTxBuilder()

	err := txBuilder.SetMsgs(sdkMsg...)
	if err != nil {
		jc.logger.Error().Err(err).Msg("fail to query the gas price")
		return 0, err
	}
	txBuilder.SetGasLimit(200000)

	key, err := jc.keyring.Key("operator")
	if err != nil {
		jc.logger.Error().Err(err).Msg("fail to get the operator key")
		return 0, err
	}
	var pubKey coscrypto.PubKey
	if tssSignMsg == nil {
		pubKey = key.GetPubKey()
	} else {
		pk := tssSignMsg.Pk
		cPk, err := legacybech32.UnmarshalPubKey(legacybech32.AccPK, pk)
		if err != nil {
			jc.logger.Error().Err(err).Msgf("fail to get the public key from bech32 format")
			return 0, err
		}
		pubKey = cPk

	}

	sigV2 := signing.SignatureV2{
		PubKey: pubKey,
		Data: &signing.SingleSignatureData{
			SignMode:  encCfg.TxConfig.SignModeHandler().DefaultMode(),
			Signature: nil,
		},
		Sequence: accSeq,
	}

	err = txBuilder.SetSignatures(sigV2)
	if err != nil {
		return 0, err
	}

	txBytes, err := jc.encoding.TxConfig.TxEncoder()(txBuilder.GetTx())
	if err != nil {
		jc.logger.Error().Err(err).Msg("fail to encode the tx")
		return 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), grpcTimeout)
	defer cancel()
	gasUsed, err := jc.SimBroadcastTx(ctx, txBytes)
	if err != nil {
		jc.logger.Error().Err(err).Msg("fail to estimate gas consumption")
		return 0, err
	}

	gasUsedDec := sdk.NewDecFromIntWithPrec(sdk.NewIntFromUint64(gasUsed), 0)
	gasWanted := gasUsedDec.Mul(sdk.MustNewDecFromStr(config.GASFEERATIO)).RoundInt64()
	return uint64(gasWanted), nil
}

// BroadcastTx broadcast the tx to the joltifyChain
func (jc *JoltifyChainBridge) BroadcastTx(ctx context.Context, txBytes []byte) (bool, string, error) {
	// Broadcast the tx via gRPC. We create a new client for the Protobuf Tx
	// service.
	txClient := cosTx.NewServiceClient(jc.grpcClient)
	// We then call the BroadcastTx method on this client.
	grpcRes, err := txClient.BroadcastTx(
		ctx,
		&cosTx.BroadcastTxRequest{
			Mode:    cosTx.BroadcastMode_BROADCAST_MODE_BLOCK,
			TxBytes: txBytes, // Proto-binary of the signed transaction, see previous step.
		},
	)
	if err != nil {
		return false, "", err
	}

	if grpcRes.GetTxResponse().Code != 0 {
		jc.logger.Error().Err(err).Msgf("fail to broadcast with response %v", grpcRes.TxResponse)
		return false, "", nil
	}
	txHash := grpcRes.GetTxResponse().TxHash
	return true, txHash, nil
}

func (jc *JoltifyChainBridge) CreatePoolAccInfo(accAddr string) error {
	acc, err := queryAccount(accAddr, jc.grpcClient)
	if err != nil {
		jc.logger.Error().Err(err).Msg("Fail to query the pool account")
		return err
	}
	accInfo := poolAccInfo{
		acc.GetAccountNumber(),
		atomic.NewUint64(acc.GetSequence()),
	}
	jc.poolAccLocker.Lock()
	jc.poolAccInfo = &accInfo
	jc.poolAccLocker.Unlock()
	return nil
}

func (jc *JoltifyChainBridge) AcquirePoolAccountInfo() (uint64, uint64) {
	accSeq := jc.poolAccInfo.accSeq.Inc()
	accNum := jc.poolAccInfo.accountNum
	return accNum, accSeq
}

func (jc *JoltifyChainBridge) prepareTssPool(creator sdk.AccAddress, pubKey, height string) error {
	msg := types.NewMsgCreateCreatePool(creator, pubKey, height)

	acc, err := queryAccount(creator.String(), jc.grpcClient)
	if err != nil {
		jc.logger.Error().Err(err).Msg("Fail to query the account")
		return err
	}

	dHeight, err := strconv.ParseInt(height, 10, 64)
	if err != nil {
		jc.logger.Error().Err(err).Msgf("fail to parse the height")
		return err
	}

	item := tssPoolMsg{
		msg,
		acc,
		pubKey,
		dHeight,
	}
	jc.poolUpdateLocker.Lock()
	// we store the latest two tss pool outReceiverAddress
	jc.msgSendCache = append(jc.msgSendCache, item)
	jc.poolUpdateLocker.Unlock()
	return nil
}

// GetLastBlockHeight gets the current block height
func (jc *JoltifyChainBridge) GetLastBlockHeight() (int64, error) {
	b, err := GetLastBlockHeight(jc.grpcClient)
	return b, err
}

// CheckAndUpdatePool send the tx to the joltify pub_chain, if the pool outReceiverAddress is updated, it returns true
func (jc *JoltifyChainBridge) CheckAndUpdatePool(blockHeight int64) (bool, string) {
	jc.poolUpdateLocker.Lock()
	if len(jc.msgSendCache) < 1 {
		jc.poolUpdateLocker.Unlock()
		return false, ""
	}
	el := jc.msgSendCache[0]
	jc.poolUpdateLocker.Unlock()
	if el.blockHeight == blockHeight {
		jc.logger.Info().Msgf("we are submit the block at height>>>>>>>>%v\n", el.blockHeight)
		ctx, cancel := context.WithTimeout(context.Background(), grpcTimeout)
		defer cancel()

		gasWanted, err := jc.GasEstimation([]sdk.Msg{el.msg}, el.acc.GetSequence(), nil)
		if err != nil {
			jc.logger.Error().Err(err).Msg("Fail to get the gas estimation")
			return false, ""
		}
		txBuilder, err := jc.genSendTx([]sdk.Msg{el.msg}, el.acc.GetSequence(), el.acc.GetAccountNumber(), gasWanted, nil)
		if err != nil {
			jc.logger.Error().Err(err).Msg("fail to generate the tx")
			return false, ""
		}
		txBytes, err := jc.encoding.TxConfig.TxEncoder()(txBuilder.GetTx())
		if err != nil {
			jc.logger.Error().Err(err).Msg("fail to encode the tx")
			return false, ""
		}
		ok, resp, err := jc.BroadcastTx(ctx, txBytes)
		if err != nil || !ok {
			jc.logger.Error().Err(err).Msgf("fail to broadcast the tx->%v", resp)
			return false, ""
		}
		jc.msgSendCache = jc.msgSendCache[1:]
		jc.logger.Info().Msgf("successfully broadcast the pool info")
		cancel()
		return true, el.poolPubKey
	}
	return false, ""
}

// CheckOutBoundTx checks
func (jc *JoltifyChainBridge) CheckOutBoundTx(blockHeight int64, rawTx tendertypes.Tx) {
	pools := jc.GetPool()
	if pools[0] == nil || pools[1] == nil {
		return
	}
	poolAddress := []sdk.AccAddress{pools[0].JoltifyAddress, pools[1].JoltifyAddress}
	config := jc.encoding

	tx, err := config.TxConfig.TxDecoder()(rawTx)
	if err != nil {
		jc.logger.Info().Msgf("fail to decode the data and skip this tx")
		return
	}
	txWithMemo := tx.(sdk.TxWithMemo)
	memo := txWithMemo.GetMemo()
	for _, msg := range txWithMemo.GetMsgs() {
		switch eachMsg := msg.(type) {
		case *banktypes.MsgSend:
			err := jc.processMsg(blockHeight, poolAddress, pools[1].EthAddress, eachMsg, rawTx.Hash(), memo)
			if err != nil {
				jc.logger.Error().Err(err).Msgf("fail to process the send message")
			}
		default:
			continue
		}
	}
}

// DoTssSign test for keysign
func (jc *JoltifyChainBridge) DoTssSign() (keysign.Response, error) {
	poolInfo, err := jc.QueryLastPoolAddress()
	if err != nil {
		jc.logger.Error().Err(err).Msgf("error in get pool with error %v", err)
		return keysign.Response{}, nil

	}
	if len(poolInfo) != 2 {
		jc.logger.Info().Msgf("fail to query the pool with length %v", len(poolInfo))
		return keysign.Response{}, nil
	}
	msgtosign := base64.StdEncoding.EncodeToString([]byte("hello"))
	msg := tssclient.TssSignigMsg{
		// fixme of the pool pubkey
		Pk:          poolInfo[0].GetCreatePool().PoolPubKey,
		Msgs:        []string{msgtosign},
		BlockHeight: int64(100),
		Version:     tssclient.TssVersion,
	}
	resp, err := jc.doTssSign(&msg)
	return resp, err
}
