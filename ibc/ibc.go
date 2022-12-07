package ibc

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	ibctypes "github.com/cosmos/ibc-go/v3/modules/core/02-client/types"
	grpc1 "github.com/gogo/protobuf/grpc"
	zlog "github.com/rs/zerolog/log"
	"gitlab.com/oppy-finance/oppy-bridge/common"
	"gitlab.com/oppy-finance/oppy-bridge/cosbridge"
	"gitlab.com/oppy-finance/oppy-bridge/pubchain"
	"gitlab.com/oppy-finance/oppy-bridge/tssclient"

	ibctransfer "github.com/cosmos/ibc-go/v3/modules/apps/transfer/types"
	"html"
)

const (
	ibcChannel = "channel-0"
	ibcChainID = "joltifylocalnet_8888-1"
	revision   = 1
)

// DoProcessIbc  mint the token in oppy chain
func DoProcessIbc(conn grpc1.ClientConn, items []*common.OutBoundReq, oc *cosbridge.OppyChainInstance, joltGrpcAddr string) (map[string]string, error) {
	signMsgs := make([]*tssclient.TssSignigMsg, len(items))
	issueReqs := make([]sdk.Msg, len(items))

	blockHeight, err := cosbridge.QueryBlockHeightNoSafe(joltGrpcAddr)
	if err != nil {
		zlog.Error().Err(err).Msgf("fail to get the block height in process the inbound tx")
		return nil, err
	}

	roundBlockHeight := blockHeight / pubchain.ROUNDBLOCK
	targetChainTimeoutHeight := blockHeight + 150

	height := ibctypes.NewHeight(revision, uint64(targetChainTimeoutHeight))

	lastPool := oc.GetPool()[1]
	for i, item := range items {
		index := item.Hash().Hex()
		receiver, fromAddress, _, _, _ := item.GetOutBoundInfo()
		zlog.Info().Msgf("we are about to prepare the tx with other nodes with index %v", index)

		r, err := common.CommonAddressToAccAddress(receiver)
		if err != nil {
			panic("common address to eth address " + err.Error())
		}
		s, err := common.CommonAddressToAccAddress(fromAddress)
		if err != nil {
			panic("common address to eth address " + err.Error())
		}

		msg := ibctransfer.NewMsgTransfer("transfer", ibcChannel, item.Coin, s.String(), item.IbcReceiver, height, 0)

		tick := html.UnescapeString("&#" + "128296" + ";")
		zlog.Info().Msgf("%v we do the top up for %v at height %v", tick, r.String(), roundBlockHeight)
		signMsg := tssclient.TssSignigMsg{
			Pk:          lastPool.Pk,
			Signers:     nil,
			BlockHeight: roundBlockHeight,
			Version:     tssclient.TssVersion,
		}
		signMsgs[i] = &signMsg
		issueReqs[i] = msg
	}
	// as in a group, the accseq MUST has been sorted.

	hashIndexMap := make(map[string]string)
	accSeq := items[0].CosAccSeq
	accNum := items[0].CosAccNum
	// we can use the last poolAddress, as chain will move the cons in pool2 -> pool1
	// for batchsigning, the signMsgs for all the members in the grop is the same
	txHashes, err := oc.BatchComposeAndSend(conn, issueReqs, accSeq, accNum, signMsgs[0], lastPool.CosAddress)
	if err != nil {
		zlog.Error().Msgf("we fail to process one or more txs")
	}
	for _, el := range items {
		index := el.TxID
		txHash := txHashes[el.CosAccSeq]
		hashIndexMap[index] = txHash
	}

	return hashIndexMap, nil
}
