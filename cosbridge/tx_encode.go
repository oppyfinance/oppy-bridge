package cosbridge

import (
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/simapp"
	"github.com/cosmos/cosmos-sdk/simapp/params"
	"github.com/cosmos/cosmos-sdk/std"
	"github.com/cosmos/cosmos-sdk/x/auth/tx"
	ibctransfer "github.com/cosmos/ibc-go/v3/modules/apps/transfer/types"
	ibctypes "github.com/cosmos/ibc-go/v3/modules/core/02-client/types"
	"gitlab.com/oppy-finance/oppychain/app"
)

func genEncodingConfig() params.EncodingConfig {
	cdc := codec.NewLegacyAmino()
	interfaceRegistry := types.NewInterfaceRegistry()
	marshaler := codec.NewProtoCodec(interfaceRegistry)

	return params.EncodingConfig{
		InterfaceRegistry: interfaceRegistry,
		Marshaler:         marshaler,
		TxConfig:          tx.NewTxConfig(marshaler, tx.DefaultSignModes),
		Amino:             cdc,
	}
}

func MakeEncodingConfig() params.EncodingConfig {
	encodingConfig := genEncodingConfig()
	std.RegisterLegacyAminoCodec(encodingConfig.Amino)
	std.RegisterInterfaces(encodingConfig.InterfaceRegistry)
	ibctypes.RegisterInterfaces(encodingConfig.InterfaceRegistry)
	ibctransfer.RegisterInterfaces(encodingConfig.InterfaceRegistry)

	app.ModuleBasics.RegisterLegacyAminoCodec(encodingConfig.Amino)
	simapp.ModuleBasics.RegisterInterfaces(encodingConfig.InterfaceRegistry)
	return encodingConfig
}
