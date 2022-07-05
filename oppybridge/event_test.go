package oppybridge

import (
	"context"
	"encoding/base64"
	"strconv"
	"testing"

	"github.com/cosmos/cosmos-sdk/client/grpc/tmservice"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/bech32/legacybech32" // nolint
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/suite"
	"gitlab.com/oppy-finance/oppy-bridge/misc"
	"gitlab.com/oppy-finance/oppychain/testutil/network"
	vaulttypes "gitlab.com/oppy-finance/oppychain/x/vault/types"
)

type EventTestSuite struct {
	suite.Suite
	cfg         network.Config
	network     *network.Network
	validatorky keyring.Keyring
	queryClient tmservice.ServiceClient
}

func (v *EventTestSuite) SetupSuite() {
	misc.SetupBech32Prefix()
	cfg := network.DefaultConfig()
	cfg.BondDenom = "stake"
	v.cfg = cfg
	v.validatorky = keyring.NewInMemory()
	// now we put the mock pool list in the test
	state := vaulttypes.GenesisState{}
	stateStaking := stakingtypes.GenesisState{}

	v.Require().NoError(cfg.Codec.UnmarshalJSON(cfg.GenesisState[vaulttypes.ModuleName], &state))
	v.Require().NoError(cfg.Codec.UnmarshalJSON(cfg.GenesisState[stakingtypes.ModuleName], &stateStaking))

	validators, err := genNValidator(3, v.validatorky)
	v.Require().NoError(err)
	for i := 1; i < 5; i++ {
		randPoolSk := ed25519.GenPrivKey()
		poolPubKey, err := legacybech32.MarshalPubKey(legacybech32.AccPK, randPoolSk.PubKey()) // nolint
		v.Require().NoError(err)

		var nodes []sdk.AccAddress
		for _, el := range validators {
			operator, err := sdk.ValAddressFromBech32(el.OperatorAddress)
			if err != nil {
				panic(err)
			}
			nodes = append(nodes, operator.Bytes())
		}
		pro := vaulttypes.PoolProposal{
			PoolPubKey: poolPubKey,
			Nodes:      nodes,
		}
		state.CreatePoolList = append(state.CreatePoolList, &vaulttypes.CreatePool{BlockHeight: strconv.Itoa(i), Validators: validators, Proposal: []*vaulttypes.PoolProposal{&pro}})
	}
	// add the validators
	var allV []*vaulttypes.Validator
	for i := 0; i < 4; i++ {
		sk := ed25519.GenPrivKey()
		v := vaulttypes.Validator{Pubkey: sk.PubKey().Bytes(), Power: 10}
		allV = append(allV, &v)
	}

	state.ValidatorinfoList = append(state.ValidatorinfoList, &vaulttypes.Validators{AllValidators: allV, Height: 100})
	state.ValidatorinfoList = append(state.ValidatorinfoList, &vaulttypes.Validators{AllValidators: allV, Height: 200})
	state.ValidatorinfoList = append(state.ValidatorinfoList, &vaulttypes.Validators{AllValidators: allV, Height: 300})
	state.ValidatorinfoList = append(state.ValidatorinfoList, &vaulttypes.Validators{AllValidators: allV, Height: 400})

	testToken := vaulttypes.IssueToken{
		Index: "testindex",
	}
	state.IssueTokenList = append(state.IssueTokenList, &testToken)

	buf, err := cfg.Codec.MarshalJSON(&state)
	v.Require().NoError(err)
	cfg.GenesisState[vaulttypes.ModuleName] = buf

	var stateVault stakingtypes.GenesisState
	v.Require().NoError(cfg.Codec.UnmarshalJSON(cfg.GenesisState[stakingtypes.ModuleName], &stateVault))
	stateVault.Params.MaxValidators = 3
	state.Params.BlockChurnInterval = 1
	buf, err = cfg.Codec.MarshalJSON(&stateVault)
	v.Require().NoError(err)
	cfg.GenesisState[stakingtypes.ModuleName] = buf

	v.network = network.New(v.T(), cfg)

	v.Require().NotNil(v.network)

	_, err = v.network.WaitForHeight(1)
	v.Require().Nil(err)

	v.queryClient = tmservice.NewServiceClient(v.network.Validators[0].ClientCtx)
}

func (e EventTestSuite) TestSubscribe() {
	accs, err := generateRandomPrivKey(2)
	e.Assert().NoError(err)
	tss := TssMock{
		accs[0].sk,
		nil,
		true,
		true,
	}
	tl, err := createMockTokenlist("testAddr", "testDenom")
	e.Require().NoError(err)
	oc, err := NewOppyBridge(e.network.Validators[0].APIAddress, e.network.Validators[0].RPCAddress, &tss, tl)
	e.Require().NoError(err)
	defer func() {
		err := oc.TerminateBridge()
		if err != nil {
			oc.logger.Error().Err(err).Msgf("fail to terminate the bridge")
		}
	}()
	query := "tm.event = 'NewBlock'"
	eventChain, err := oc.AddSubscribe(context.Background(), query)
	e.Require().NoError(err)
	data := <-eventChain
	e.T().Logf("new block event test %v", data.Events)
}

func (e EventTestSuite) TestHandleUpdateEvent() {
	accs, err := generateRandomPrivKey(2)
	e.Assert().NoError(err)
	tss := TssMock{
		accs[0].sk,
		nil,
		false,
		true,
	}
	tl, err := createMockTokenlist("testAddr", "testDenom")
	e.Require().NoError(err)
	oc, err := NewOppyBridge(e.network.Validators[0].APIAddress, e.network.Validators[0].RPCAddress, &tss, tl)
	e.Require().NoError(err)
	defer func() {
		err := oc.TerminateBridge()
		if err != nil {
			oc.logger.Error().Err(err).Msgf("fail to terminate the bridge")
		}
	}()
	oc.grpcClient = e.network.Validators[0].ClientCtx
	err = oc.InitValidators(e.network.Validators[0].APIAddress)
	e.Require().NoError(err)
	data := base64.StdEncoding.EncodeToString(e.network.Validators[0].PubKey.Bytes())
	oc.myValidatorInfo.Result.ValidatorInfo.PubKey.Value = data

	err = oc.HandleUpdateValidators(100)
	e.Require().NoError(err)
	e.Require().Equal(len(oc.keyGenCache), 0)
	tss.keygenSuccess = true

	// we set the keyring as the validator key, it should run the keygen successfully
	oc.Keyring = e.validatorky
	err = oc.HandleUpdateValidators(100)
	e.Require().NoError(err)
	e.Require().Equal(len(oc.keyGenCache), 1)
}

func TestEvent(t *testing.T) {
	suite.Run(t, new(EventTestSuite))
}
