package common

import (
	"github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
	"gitlab.com/oppy-finance/oppy-bridge/misc"
	"testing"
)

func TestAddressConvert(t *testing.T) {
	misc.SetupBech32Prefix()
	addr, err := types.AccAddressFromBech32("oppy1txtsnx4gr4effr8542778fsxc20j5vzq7wu7r7")
	require.NoError(t, err)
	out := AccAddressToCommonAddress(addr)
	result, err := CommonAddressToAccAddress(out)
	require.NoError(t, err)
	require.True(t, addr.Equals(result))
}
