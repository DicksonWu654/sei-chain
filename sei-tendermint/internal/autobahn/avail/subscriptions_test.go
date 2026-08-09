package avail

import (
	"testing"
	"time"

	"github.com/sei-protocol/sei-chain/sei-db/ledger_db/block/memblock"
	"github.com/sei-protocol/sei-chain/sei-tendermint/autobahn/types"
	"github.com/sei-protocol/sei-chain/sei-tendermint/internal/autobahn/data"
	"github.com/sei-protocol/sei-chain/sei-tendermint/internal/autobahn/epoch"
	"github.com/sei-protocol/sei-chain/sei-tendermint/libs/utils"
	"github.com/sei-protocol/sei-chain/sei-tendermint/libs/utils/require"
)

func TestSubscribeLaneProposals_ErrLanePrunedAfterMapDrop(t *testing.T) {
	rng := utils.TestRng()
	registry, keys := epoch.GenRegistry(rng, 2)
	a, b := keys[0], keys[1]
	db := memblock.NewBlockDB()
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	ds := utils.OrPanic1(data.NewState(&data.Config{Registry: registry}, db))
	state := utils.OrPanic1(NewState(a, ds, utils.None[string]()))

	lane0 := state.LocalLane().OrPanic("genesis")
	want, err := state.ProduceLocalBlock(lane0, 0, types.GenPayload(rng))
	require.NoError(t, err)
	sub, err := state.SubscribeLaneProposals(0)
	require.NoError(t, err)

	ep, err := registry.ActivateEpoch(
		map[types.PublicKey]uint64{b.Public(): 1},
		types.OpenRoadRange(), time.Time{}, registry.FirstBlock(),
	)
	require.NoError(t, err)
	state.ApplyEpoch(ep)
	_, err = state.SubscribeLaneProposals(0)
	require.ErrorIs(t, err, ErrBadLane)

	got, err := sub.Recv(t.Context())
	require.NoError(t, err)
	require.Equal(t, want.Msg().Block().Header().Hash(), got.Msg().Block().Header().Hash())

	for inner, ctrl := range state.inner.Lock() {
		inner.dropLanes([]types.LaneID{lane0})
		ctrl.Updated()
	}
	_, err = sub.Recv(t.Context())
	require.ErrorIs(t, err, ErrLanePruned)
}
