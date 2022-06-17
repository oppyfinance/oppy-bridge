package oppybridge

import (
	"errors"
	"gitlab.com/oppy-finance/oppy-bridge/common"

	"gitlab.com/oppy-finance/oppy-bridge/tssclient"
	vaulttypes "gitlab.com/oppy-finance/oppychain/x/vault/types"
)

func prepareIssueTokenRequest(item *common.InBoundReq, creatorAddr, index string) (*vaulttypes.MsgCreateIssueToken, error) {
	userAddr, _, coin, _, _ := item.GetInboundReqInfo()

	a, err := vaulttypes.NewMsgCreateIssueToken(creatorAddr, index, coin.String(), userAddr.String())
	if err != nil {
		return nil, err
	}
	return a, nil
}

// ProcessInBound mint the token in oppy chain
func (jc *OppyChainInstance) ProcessInBound(item *common.InBoundReq) (string, string, error) {

	accSeq, accNum, poolAddress, poolPk := item.GetAccountInfo()
	// we need to check against the previous account sequence
	index := item.Hash().Hex()
	if jc.CheckWhetherAlreadyExist(index) {
		jc.logger.Warn().Msg("already submitted by others")
		return "", "", nil
	}

	jc.logger.Info().Msgf("we are about to prepare the tx with other nodes with index %v", index)
	issueReq, err := prepareIssueTokenRequest(item, poolAddress.String(), index)
	if err != nil {
		jc.logger.Error().Err(err).Msg("fail to prepare the issuing of the token")
		return "", "", err
	}

	_, _, _, _, roundBlockHeight := item.GetInboundReqInfo()
	jc.logger.Info().Msgf("we do the top up for %v at height %v", issueReq.Receiver.String(), roundBlockHeight)
	signMsg := tssclient.TssSignigMsg{
		Pk:          poolPk,
		Signers:     nil,
		BlockHeight: roundBlockHeight,
		Version:     tssclient.TssVersion,
	}

	key, err := jc.Keyring.Key("operator")
	if err != nil {
		jc.logger.Error().Err(err).Msg("fail to get the operator key")
		return "", "", err
	}

	ok, txHash, err := jc.composeAndSend(key, issueReq, accSeq, accNum, &signMsg, poolAddress)
	if err != nil || !ok {
		jc.logger.Error().Err(err).Msgf("fail to broadcast the tx->%v", txHash)
		return "", index, errors.New("fail to process the inbound tx")
	}
	return txHash, index, nil
}