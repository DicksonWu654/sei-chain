package types

import (
	"cmp"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"fmt"

	"github.com/sei-protocol/sei-chain/sei-tendermint/internal/autobahn/pb"
	"github.com/sei-protocol/sei-chain/sei-tendermint/internal/protoutils"
	"github.com/sei-protocol/sei-chain/sei-tendermint/libs/utils"
)

// LaneID identifies a validator's continuous committee membership streak.
// Joined is the epoch in which that streak began.
//
// Identity rules: stay keeps the same LaneID; leave ends that identity; rejoin
// allocates a new LaneID (typically with tip 0). Avail map retention and tipEpoch
// dispose live in package avail (see its package doc).
//
// LaneID is a plain value type (passed by value); fields are public by design.
type LaneID struct {
	Validator PublicKey
	Joined    EpochIndex
}

func NewLaneID(validator PublicKey, joined EpochIndex) LaneID {
	return LaneID{Validator: validator, Joined: joined}
}

// Compare orders by validator, then joined.
func (l LaneID) Compare(other LaneID) int {
	return cmp.Or(
		l.Validator.Compare(other.Validator),
		cmp.Compare(l.Joined, other.Joined),
	)
}

// Bytes returns a stable encoding: pubkey bytes || big-endian joined.
func (l LaneID) Bytes() []byte {
	vb := l.Validator.Bytes()
	b := make([]byte, 0, len(vb)+8)
	b = append(b, vb...)
	return binary.BigEndian.AppendUint64(b, uint64(l.Joined))
}

// LaneIDFromBytes parses Bytes() encoding (exactly ed25519 pubkey || u64be joined).
func LaneIDFromBytes(b []byte) (LaneID, error) {
	want := ed25519.PublicKeySize + 8
	if len(b) != want {
		return LaneID{}, fmt.Errorf("LaneID: got %d bytes, want %d", len(b), want)
	}
	joined := EpochIndex(binary.BigEndian.Uint64(b[ed25519.PublicKeySize:]))
	validator, err := PublicKeyFromBytes(b[:ed25519.PublicKeySize])
	if err != nil {
		return LaneID{}, fmt.Errorf("LaneID validator: %w", err)
	}
	return NewLaneID(validator, joined), nil
}

func (l LaneID) String() string {
	return fmt.Sprintf("%s@e%d", l.Validator.String(), l.Joined)
}

func (l LaneID) HexString() string { return hex.EncodeToString(l.Bytes()) }

var LaneIDConv = protoutils.Conv[LaneID, *pb.LaneID]{
	Encode: func(l LaneID) *pb.LaneID {
		return &pb.LaneID{
			Validator: PublicKeyConv.Encode(l.Validator),
			Joined:    utils.Alloc(uint64(l.Joined)),
		}
	},
	Decode: func(p *pb.LaneID) (LaneID, error) {
		validator, err := PublicKeyConv.DecodeReq(p.Validator)
		if err != nil {
			return LaneID{}, fmt.Errorf("validator: %w", err)
		}
		if p.Joined == nil {
			return LaneID{}, fmt.Errorf("joined: missing")
		}
		return NewLaneID(validator, EpochIndex(*p.Joined)), nil
	},
}
