package avail

import (
	"context"
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
	sub, err := state.SubscribeLaneProposals(lane0, 0)
	require.NoError(t, err)

	ep, err := registry.ActivateEpoch(
		map[types.PublicKey]uint64{b.Public(): 1},
		types.OpenRoadRange(), time.Time{}, registry.FirstBlock(),
	)
	require.NoError(t, err)
	state.ApplyEpoch(ep)

	// Wrong producer key is rejected even if that peer has a lane in the committee.
	otherLane := types.NewLaneID(b.Public(), ep.EpochIndex())
	_, err = state.SubscribeLaneProposals(otherLane, 0)
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

func TestSubscribeLaneProposals_WrongValidator(t *testing.T) {
	rng := utils.TestRng()
	registry, keys := epoch.GenRegistry(rng, 2)
	a, b := keys[0], keys[1]
	db := memblock.NewBlockDB()
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	ds := utils.OrPanic1(data.NewState(&data.Config{Registry: registry}, db))
	state := utils.OrPanic1(NewState(a, ds, utils.None[string]()))

	_, err := state.SubscribeLaneProposals(types.NewLaneID(b.Public(), 0), 0)
	require.ErrorIs(t, err, ErrBadLane)
}

// Leave → tip dispose → rejoin allocates a new LaneID; WaitLane skips the closed
// identity and Subscribe serves the new lane (StreamLaneProposals client path).
func TestWaitLane_LeaveRejoinNewLaneID(t *testing.T) {
	ctx := t.Context()
	rng := utils.TestRng()
	registry, keys := epoch.GenRegistry(rng, 2)
	a, b := keys[0], keys[1]
	db := memblock.NewBlockDB()
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	ds := utils.OrPanic1(data.NewState(&data.Config{Registry: registry}, db))
	state := utils.OrPanic1(NewState(a, ds, utils.None[string]()))

	lane0 := state.LocalLane().OrPanic("genesis")
	got, err := state.WaitLane(ctx, a.Public(), utils.None[types.LaneID]())
	require.NoError(t, err)
	require.Equal(t, lane0, got)

	sub, err := state.SubscribeLaneProposals(lane0, 0)
	require.NoError(t, err)
	_, err = state.ProduceLocalBlock(lane0, 0, types.GenPayload(rng))
	require.NoError(t, err)
	_, err = sub.Recv(ctx)
	require.NoError(t, err)

	// Leave: a out of committee. Leave map still serves until dispose.
	epLeave, err := registry.ActivateEpoch(
		map[types.PublicKey]uint64{b.Public(): 1},
		types.OpenRoadRange(), time.Time{}, registry.FirstBlock(),
	)
	require.NoError(t, err)
	state.ApplyEpoch(epLeave)
	require.False(t, state.LocalLane().IsPresent())

	// Tip dispose of leave map → stream ends (ErrLanePruned).
	for inner, ctrl := range state.inner.Lock() {
		inner.dropLanes([]types.LaneID{lane0})
		ctrl.Updated()
	}
	_, err = sub.Recv(ctx)
	require.ErrorIs(t, err, ErrLanePruned)

	// Client would WaitLane(..., exclude=lane0); must not accept the closed identity.
	waitCtx, cancel := context.WithCancel(ctx)
	done := make(chan types.LaneID, 1)
	go func() {
		lane, err := state.WaitLane(waitCtx, a.Public(), utils.Some(lane0))
		if err == nil {
			done <- lane
		}
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("WaitLane returned before rejoin")
	case <-time.After(20 * time.Millisecond):
	}

	// Rejoin under a new LaneID (Joined = leave epoch index).
	epJoin, err := registry.ActivateEpoch(
		map[types.PublicKey]uint64{a.Public(): 1, b.Public(): 1},
		types.OpenRoadRange(), time.Time{}, registry.FirstBlock(),
	)
	require.NoError(t, err)
	state.ApplyEpoch(epJoin)
	lane1 := state.LocalLane().OrPanic("rejoin")
	require.NotEqual(t, lane0, lane1)
	require.Equal(t, a.Public(), lane1.Validator)

	select {
	case got := <-done:
		require.Equal(t, lane1, got)
	case <-time.After(time.Second):
		cancel()
		t.Fatal("WaitLane did not observe rejoin LaneID")
	}
	cancel()

	// New subscribe on the rejoin lane; closed lane0 still key-ok but map gone → prune on Recv.
	sub0, err := state.SubscribeLaneProposals(lane0, 0)
	require.NoError(t, err)
	_, err = sub0.Recv(ctx)
	require.ErrorIs(t, err, ErrLanePruned)

	sub1, err := state.SubscribeLaneProposals(lane1, 0)
	require.NoError(t, err)
	want, err := state.ProduceLocalBlock(lane1, 0, types.GenPayload(rng))
	require.NoError(t, err)
	gotBlk, err := sub1.Recv(ctx)
	require.NoError(t, err)
	require.Equal(t, want.Msg().Block().Header().Hash(), gotBlk.Msg().Block().Header().Hash())
	require.Equal(t, lane1, gotBlk.Msg().Block().Header().Lane())
}
