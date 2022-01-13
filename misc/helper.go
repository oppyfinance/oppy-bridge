package misc

import (
	"encoding/base64"
	"math/big"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/bech32/legacybech32"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/joltgeorge/tss/keysign"
	"github.com/tendermint/btcd/btcec"
)

// SetupBech32Prefix sets up the prefix of the joltify chain
func SetupBech32Prefix() {
	config := types.GetConfig()
	config.SetBech32PrefixForAccount("jolt", "joltpub")
	config.SetBech32PrefixForValidator("joltval", "joltvpub")
	config.SetBech32PrefixForConsensusNode("joltvalcons", "joltcpub")
}

// PoolPubKeyToJoltAddress return the jolt encoded pubkey
func PoolPubKeyToJoltAddress(pk string) (types.AccAddress, error) {
	pubkey, err := legacybech32.UnmarshalPubKey(legacybech32.AccPK, pk)
	if err != nil {
		return types.AccAddress{}, err
	}
	addr, err := types.AccAddressFromHex(pubkey.Address().String())
	return addr, err
}

// PoolPubKeyToEthAddress export the joltify pubkey to the ETH format address
func PoolPubKeyToEthAddress(pk string) (common.Address, error) {
	pubkey, err := legacybech32.UnmarshalPubKey(legacybech32.AccPK, pk)
	if err != nil {
		return common.Address{}, err
	}
	//pk2, err := btcec.ParsePubKey(pubkey.Bytes(), btcec.S256())
	//if err != nil {
	//	return common.Address{}, err
	//}

	pk2, err := crypto.DecompressPubkey(pubkey.Bytes())
	if err != nil {
		return common.Address{}, err
	}
	addr := crypto.PubkeyToAddress(*pk2)
	return addr, nil
}

// AccountPubKeyToEthAddress export the joltify pubkey to the ETH format address
func AccountPubKeyToEthAddress(pk cryptotypes.PubKey) (common.Address, error) {
	pubkey, err := crypto.DecompressPubkey(pk.Bytes())
	// expectedAddr := crypto.PubkeyToAddress(*pubkey)
	// pk2, err := btcec.ParsePubKey(pk.Bytes(), btcec.S256())
	if err != nil {
		return common.Address{}, err
	}
	addr := crypto.PubkeyToAddress(*pubkey)
	return addr, nil
}

// SerializeSig for both joltify chain and public chain
func SerializeSig(sig *keysign.Signature, needRecovery bool) ([]byte, error) {
	rBytes, err := base64.StdEncoding.DecodeString(sig.R)
	if err != nil {
		return nil, err
	}
	sBytes, err := base64.StdEncoding.DecodeString(sig.S)
	if err != nil {
		return nil, err
	}

	vBytes, err := base64.StdEncoding.DecodeString(sig.RecoveryID)
	if err != nil {
		return nil, err
	}

	R := new(big.Int).SetBytes(rBytes)
	S := new(big.Int).SetBytes(sBytes)
	V := new(big.Int).SetBytes(vBytes)
	N := btcec.S256().N
	halfOrder := new(big.Int).Rsh(N, 1)
	// see: https://github.com/ethereum/go-ethereum/blob/f9401ae011ddf7f8d2d95020b7446c17f8d98dc1/crypto/signature_nocgo.go#L90-L93
	if S.Cmp(halfOrder) == 1 {
		S.Sub(N, S)
	}

	// Serialize signature to R || S.
	// R, S are padded to 32 bytes respectively.
	rBytes = R.Bytes()
	sBytes = S.Bytes()
	vBytes = V.Bytes()

	if needRecovery {
		sigBytes := make([]byte, 65)
		// 0 pad the byte arrays from the left if they aren't big enough.
		copy(sigBytes[32-len(rBytes):32], rBytes)
		copy(sigBytes[64-len(sBytes):64], sBytes)
		copy(sigBytes[65-len(vBytes):65], vBytes)
		return sigBytes, nil
	}
	sigBytes := make([]byte, 64)
	// 0 pad the byte arrays from the left if they aren't big enough.
	copy(sigBytes[32-len(rBytes):32], rBytes)
	copy(sigBytes[64-len(sBytes):64], sBytes)
	return sigBytes, nil
}

// MakeSignature serialize the r,s,v to the signature bytes
func MakeSignature(r, s, v *big.Int) []byte {
	rBytes := r.Bytes()
	sBytes := s.Bytes()

	sigBytes := make([]byte, 65)
	// 0 pad the byte arrays from the left if they aren't big enough.
	copy(sigBytes[32-len(rBytes):32], rBytes)
	copy(sigBytes[64-len(sBytes):64], sBytes)
	sigBytes[64] = byte(v.Uint64())
	return sigBytes
}

// EthSignPubKeytoJoltAddr derives the joltify address from the pubkey derived from the signature
func EthSignPubKeyToJoltAddr(pk []byte) (types.AccAddress, error) {
	pk2, err := btcec.ParsePubKey(pk, btcec.S256())
	if err != nil {
		return types.AccAddress{}, err
	}

	pk3 := secp256k1.PubKey{Key: pk2.SerializeCompressed()}
	joltAddr, err := types.AccAddressFromHex(pk3.Address().String())
	return joltAddr, err
}

func isProtectedV(v *big.Int) bool {
	if v.BitLen() <= 8 {
		v := v.Uint64()
		return v != 27 && v != 28 && v != 1 && v != 0
	}
	// anything not 27 or 28 is considered protected
	return true
}

func RecoverRecID(chainID uint64, v *big.Int) *big.Int {
	if v.Uint64() == 0 || v.Uint64() == 1 {
		return v
	}

	if isProtectedV(v) {
		plainV := new(big.Int).SetUint64(v.Uint64() - 35 - 2*chainID)
		return plainV
	} else {
		// Only EIP-155 signatures can be optionally protected. Since
		// we determined this v value is not protected, it must be a
		// raw 27 or 28.
		plainV := new(big.Int).SetUint64(v.Uint64() - 27)
		return plainV
	}
}
