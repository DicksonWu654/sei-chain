package types

import (
	"testing"
	"time"

	"github.com/sei-protocol/sei-chain/sei-tendermint/libs/utils"
	"github.com/sei-protocol/sei-chain/sei-tendermint/libs/utils/require"
)

func TestEpochIsClosed(t *testing.T) {
	rng := utils.TestRng()
	a := GenSecretKey(rng).Public()
	b := GenSecretKey(rng).Public()
	c := GenSecretKey(rng).Public()

	ep1 := NewEpoch(1, OpenRoadRange(), time.Time{},
		utils.OrPanic1(NewCommittee(map[PublicKey]uint64{a: 1, c: 1})), 0)

	stay := NewLaneID(a, 0)
	leave := NewLaneID(b, 0)
	joiner := NewLaneID(c, 1)
	// joined == tip: not closed (live dispose uses Joined < tip).
	sameEpochAbsent := NewLaneID(GenSecretKey(rng).Public(), 1)

	require.False(t, ep1.IsClosed(stay))
	require.True(t, ep1.IsClosed(leave))
	require.False(t, ep1.IsClosed(joiner))
	require.False(t, ep1.IsClosed(sameEpochAbsent))
}
