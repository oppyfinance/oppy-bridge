package common

import (
	"errors"
	"github.com/cosmos/cosmos-sdk/types"
	"github.com/ethereum/go-ethereum/common"
	"strings"
)

func AccAddressFromHex(bech32PrefixAccAddr, address string) (types.AccAddress, error) {

	if len(strings.TrimSpace(address)) == 0 {
		return types.AccAddress{}, errors.New("empty address string is not allowed")
	}

	bz, err := types.GetFromBech32(address, bech32PrefixAccAddr)
	if err != nil {
		return nil, err
	}

	err = types.VerifyAddressFormat(bz)
	if err != nil {
		return nil, err
	}

	return bz, nil
}

func AccAddressToCommonAddress(in types.AccAddress) common.Address {
	b := in.Bytes()
	addr := common.BytesToAddress(b)
	return addr
}

func CommonAddressToAccAddress(in common.Address) (types.AccAddress, error) {
	return in.Bytes(), nil
}
