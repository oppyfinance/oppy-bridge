package cosbridge

import (
	"errors"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	bcommon "gitlab.com/oppy-finance/oppy-bridge/common"

	grpc1 "github.com/gogo/protobuf/grpc"
	zlog "github.com/rs/zerolog/log"
	"gitlab.com/oppy-finance/oppy-bridge/pubchain"
	vaulttypes "gitlab.com/oppy-finance/oppychain/x/vault/types"
)

// FeedTx feed the tx with the given
func (oc *OppyChainInstance) FeedTx(conn grpc1.ClientConn, lastPoolInfo *vaulttypes.PoolInfo, pi *pubchain.Instance) error {
	// we always increase the account seq regardless the tx successful or not

	found, err := oc.CheckWhetherSigner(lastPoolInfo)
	if err != nil {
		oc.logger.Error().Err(err).Msg("fail to check whether we are the node submit the mint request")
		return err
	}
	if !found {
		zlog.Info().Msgf("we are not the signer")
		return nil
	}

	currentPool := lastPoolInfo.CreatePool.PoolAddr
	acc, err := QueryAccount(conn, currentPool.String(), "")
	if err != nil {
		oc.logger.Error().Err(err).Msgf("fail to query the account")
		return errors.New("invalid account query")
	}

	inboundItems := pi.PopItem(pubchain.GroupSign)
	if inboundItems == nil {
		oc.logger.Info().Msgf("empty queue")
		return nil
	}

	accNum := acc.GetAccountNumber()
	accSeq := acc.GetSequence()
	address := acc.GetAddress()
	poolPubkey := lastPoolInfo.CreatePool.PoolPubKey
	for _, el := range inboundItems {
		el.SetAccountInfo(accNum, accSeq, address, poolPubkey)
		accSeq++
	}
	pi.InboundReqChan <- inboundItems
	return nil
}

// FeedIBCTx feed the tx with the given
func (oc *OppyChainInstance) FeedIBCTx(latestHeight int64, acc authtypes.AccountI) ([]*bcommon.OutBoundReq, error) {
	// we always increase the account seq regardless the tx successful or not

	ibcOutBoundItems := oc.PopItem(pubchain.GroupSign, JOLT, latestHeight)
	if ibcOutBoundItems == nil {
		zlog.Logger.Info().Msgf("empty queue for ibc requests")
		return nil, nil
	}

	accNum := acc.GetAccountNumber()
	accSeq := acc.GetSequence()
	for i := range ibcOutBoundItems {
		ibcOutBoundItems[i].CosAccSeq = accSeq
		ibcOutBoundItems[i].CosAccNum = accNum
		accSeq++
	}
	return ibcOutBoundItems, nil
}
