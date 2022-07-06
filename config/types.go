package config

import "time"

const (
	InBoundDenomFee  = "abnb"
	OutBoundDenomFee = "pjolt"

	InBoundFeeMin    = "0.00000000000000001"
	OUTBoundFeeOut   = "0.00000000000000001"
	TxTimeout        = 300
	GASFEERATIO      = "1.5"
	DUSTBNB          = "0.0001"
	MINCHECKBLOCKGAP = 6
)

var ChainID = "oppyChain-1"

const (
	InBound = iota
	OutBound
	QueryTimeOut = time.Second * 6
)

// direction is the direction of the oppy_bridge
type Direction int
