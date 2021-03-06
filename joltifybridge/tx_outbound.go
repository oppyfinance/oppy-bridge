package joltifybridge

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"

	bcommon "gitlab.com/joltify/joltifychain-bridge/common"
	"gitlab.com/joltify/joltifychain-bridge/tssclient"
	vaulttypes "gitlab.com/joltify/joltifychain/x/vault/types"

	"github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	ethcommon "github.com/ethereum/go-ethereum/common"
	"gitlab.com/joltify/joltifychain-bridge/config"
	"gitlab.com/joltify/joltifychain-bridge/misc"
)

func (jc *JoltifyChainInstance) processMsg(blockHeight int64, address []types.AccAddress, curEthAddr ethcommon.Address, msg *banktypes.MsgSend, txHash []byte) error {
	txID := strings.ToLower(hex.EncodeToString(txHash))

	toAddress, err := types.AccAddressFromBech32(msg.ToAddress)
	if err != nil {
		jc.logger.Error().Err(err).Msg("fail to parse the to outReceiverAddress")
		return err
	}

	// here we need to calculate the node's eth address from public key rather than the joltify chain address
	acc, err := queryAccount(msg.FromAddress, jc.grpcClient)
	if err != nil {
		jc.logger.Error().Err(err).Msg("Fail to query the account")
		return err
	}

	fromEthAddr, err := misc.AccountPubKeyToEthAddress(acc.GetPubKey())
	if err != nil {
		jc.logger.Error().Err(err).Msg("Fail to get the eth address")
		return err
	}
	// now we wrap the fromEthAddress with joltify hex address
	wrapFromEthAddr, err := types.AccAddressFromHex(fromEthAddr.Hex()[2:])
	if err != nil {
		jc.logger.Error().Err(err).Msg("Fail to wrap the eth address")
		return err
	}

	// we check whether it is the message to the pool
	if !(toAddress.Equals(address[0]) || toAddress.Equals(address[1])) {
		jc.logger.Warn().Msg("not a top up message to the pool")
		return errors.New("not a top up message to the pool")
	}

	// it means the sender pay the fee in one tx
	if len(msg.Amount) == 2 {
		// now we search for the index of the outboundemo and the outbounddemofee
		found := false
		indexDemo := 0
		indexDemoFee := 0
		if msg.Amount[0].GetDenom() == config.OutBoundDenom && msg.Amount[1].GetDenom() == config.OutBoundDenomFee {
			indexDemo = 0
			indexDemoFee = 1
			found = true
		}

		if msg.Amount[1].GetDenom() == config.OutBoundDenom && msg.Amount[0].GetDenom() == config.OutBoundDenomFee {
			indexDemo = 1
			indexDemoFee = 0
			found = true
		}
		if !found {
			return errors.New("invalid fee pair")
		}

		item := jc.processDemonAndFee(txID, blockHeight, wrapFromEthAddr, msg.Amount[indexDemo].Amount, msg.Amount[indexDemoFee].Amount)
		// since the cosmos address is different from the eth address, we need to derive the eth address from the public key
		if item != nil {
			itemReq := newOutboundReq(txID, item.outReceiverAddress, curEthAddr, item.token, blockHeight)
			jc.AddItem(&itemReq)
			return nil
		}
		return errors.New("not enough fee")
	}

	return errors.New("we only allow fee and top up in one tx now")
}

func (jc *JoltifyChainInstance) processDemonAndFee(txID string, blockHeight int64, fromAddress types.AccAddress, DemonAmount, feeAmount types.Int) *outboundTx {
	token := types.Coin{
		Denom:  config.OutBoundDenom,
		Amount: DemonAmount,
	}
	fee := types.Coin{
		Denom:  config.OutBoundDenomFee,
		Amount: feeAmount,
	}

	tx := outboundTx{
		ethcommon.BytesToAddress(fromAddress.Bytes()),
		uint64(blockHeight),
		token,
		fee,
	}
	jc.logger.Info().Msgf("we add the outbound tokens tx(%v):%v", txID, tx.token.String())
	err := tx.Verify()
	if err != nil {
		return nil
	}
	return &tx
}

// GetPool get the latest two pool address
func (jc *JoltifyChainInstance) GetPool() []*bcommon.PoolInfo {
	jc.poolUpdateLocker.RLock()
	defer jc.poolUpdateLocker.RUnlock()
	var ret []*bcommon.PoolInfo
	ret = append(ret, jc.lastTwoPools...)
	return ret
}

// UpdatePool update the tss pool address
func (jc *JoltifyChainInstance) UpdatePool(pool *vaulttypes.PoolInfo) *bcommon.PoolInfo {
	poolPubKey := pool.CreatePool.PoolPubKey
	ethAddr, err := misc.PoolPubKeyToEthAddress(poolPubKey)
	if err != nil {
		fmt.Printf("fail to convert the jolt address to eth address %v", poolPubKey)
		return nil
	}

	addr, err := misc.PoolPubKeyToJoltAddress(poolPubKey)
	if err != nil {
		fmt.Printf("fail to convert the jolt address to jolt address %v", poolPubKey)
		return nil
	}

	p := bcommon.PoolInfo{
		Pk:             poolPubKey,
		JoltifyAddress: addr,
		EthAddress:     ethAddr,
		PoolInfo:       pool,
	}

	jc.poolUpdateLocker.Lock()
	previousPool := jc.lastTwoPools[0]

	if jc.lastTwoPools[1] != nil {
		jc.lastTwoPools[0] = jc.lastTwoPools[1]
	}
	jc.lastTwoPools[1] = &p
	jc.poolUpdateLocker.Unlock()
	return previousPool
}

func (jc *JoltifyChainInstance) MoveFunds(fromPool *bcommon.PoolInfo, to types.AccAddress, height int64) (bool, error) {
	from := fromPool.JoltifyAddress
	acc, err := queryAccount(from.String(), jc.grpcClient)
	if err != nil {
		jc.logger.Error().Err(err).Msg("Fail to query the pool account")
		return false, err
	}
	coins, err := queryBalance(from.String(), jc.grpcClient)
	if err != nil {
		jc.logger.Error().Err(err).Msg("Fail to query the balance")
		return false, err
	}
	if len(coins) == 0 {
		jc.logger.Warn().Msg("we do not have any balance skip send")
		return true, nil
	}

	msg := banktypes.NewMsgSend(from, to, coins)

	signMsg := tssclient.TssSignigMsg{
		Pk:          fromPool.Pk,
		Signers:     nil,
		BlockHeight: height,
		Version:     tssclient.TssVersion,
	}

	ok, resp, err := jc.composeAndSend(msg, acc.GetSequence(), acc.GetAccountNumber(), &signMsg)
	if err != nil || !ok {
		jc.logger.Error().Err(err).Msgf("fail to broadcast the tx->%v", resp)
		return false, errors.New("fail to process the inbound tx")
	}
	return false, nil
}

// GetOutBoundInfo return the outbound tx info
func (o *OutBoundReq) GetOutBoundInfo() (ethcommon.Address, ethcommon.Address, *big.Int, int64) {
	return o.outReceiverAddress, o.fromPoolAddr, o.coin.Amount.BigInt(), o.blockHeight
}

// Verify checks whether the outbound tx has paid enough fee
func (a *outboundTx) Verify() error {
	if a.fee.Denom != config.OutBoundDenomFee {
		return errors.New("invalid outbound fee denom")
	}
	amount, err := types.NewDecFromStr(config.OUTBoundFeeOut)
	if err != nil {
		return errors.New("invalid minimal inbound fee")
	}
	if a.fee.Amount.LT(types.NewIntFromBigInt(amount.BigInt())) {
		return fmt.Errorf("the fee is not enough with %s<%s", a.fee.Amount, amount.BigInt().String())
	}
	return nil
}

// SetItemHeight sets the block height of the tx
func (o *OutBoundReq) SetItemHeight(blockHeight int64) {
	o.blockHeight = blockHeight
}
