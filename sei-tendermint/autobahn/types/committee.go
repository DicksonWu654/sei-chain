package types

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"iter"
	"maps"
	"slices"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
	"github.com/sei-protocol/sei-chain/sei-tendermint/libs/utils"
)

// Immutable slice.
type ImSlice[T any] struct{ s []T }

func (s ImSlice[T]) Len() int         { return len(s.s) }
func (s ImSlice[T]) At(i int) T       { return s.s[i] }
func (s ImSlice[T]) All() iter.Seq[T] { return slices.Values(s.s) }

// Committee represents the consensus committee.
// Lanes carry membership (validator + joined); weights are voting stake.
//
// Membership order is replica order (PublicKey.Compare). Leader/EvmShard and
// tipcut header concatenation walk that order. Lanes() follows the same order
// (one LaneID per replica); LaneID.Compare is for sorting lane lists elsewhere.
type Committee struct {
	lanes       ImSlice[LaneID] // in Replicas() order; one per member
	byValidator map[PublicKey]LaneID
	weights     map[PublicKey]uint64
	totalWeight uint64
}

const MaxValidators = 100

func (c *Committee) HasReplica(k PublicKey) bool {
	_, ok := c.weights[k]
	return ok
}

func (c *Committee) HasLane(l LaneID) bool {
	got, ok := c.byValidator[l.Validator]
	return ok && got.Joined == l.Joined
}

func (c *Committee) Lane(v PublicKey) utils.Option[LaneID] {
	lane, ok := c.byValidator[v]
	if !ok {
		return utils.None[LaneID]()
	}
	return utils.Some(lane)
}

// Replicas yields validators in PublicKey order (membership order).
func (c *Committee) Replicas() iter.Seq[PublicKey] {
	return func(yield func(PublicKey) bool) {
		for lane := range c.lanes.All() {
			if !yield(lane.Validator) {
				return
			}
		}
	}
}

// Lanes returns each replica's LaneID in Replicas() order.
func (c *Committee) Lanes() ImSlice[LaneID] { return c.lanes }

// Deterministic random oracle selecting a replica with probability proportional to the weight.
// Walks membership (Replicas) order so seed → PublicKey is network-wide deterministic.
func (c *Committee) randomReplica(seed []byte) PublicKey {
	h := sha256.Sum256(seed[:])
	var x, total uint256.Int
	x.SetBytes32(h[:])
	total.SetUint64(c.totalWeight)
	y := x.Mod(&x, &total).Uint64()
	// TODO(gprusak): this can be optimized to O(1) lookup
	for k := range c.Replicas() {
		w := c.weights[k]
		if y < w {
			return k
		}
		y -= w
	}
	panic("unreachable")
}

// Weight of validator k.
func (c *Committee) Weight(k PublicKey) uint64 { return c.weights[k] }

// Replica which is responsible for sequencing transactions from this addr.
func (c *Committee) EvmShard(addr common.Address) PublicKey {
	// TODO(gprusak): given that we currently do not have censorship-resistance,
	// from correctness perspective if doesn't matter if shards are proportional to weights.
	// For private testnet we need the load on each validator to be the same.
	// For mainnet we need to resolve this issue somehow differently.
	return c.randomReplica(addr[:])
}

// Leader for the consensus round with the given index.
func (c *Committee) Leader(view View) PublicKey {
	// TODO(gprusak): this needs domain separation.
	d := binary.BigEndian.AppendUint64(nil, uint64(view.Index))
	d = binary.BigEndian.AppendUint64(d, uint64(view.Number))
	return c.randomReplica(d)
}

// Faulty is the maximal total weight of faulty replicas that consensus can tolerate.
func (c *Committee) Faulty() uint64 {
	// 3f < N
	return (c.totalWeight - 1) / 3
}

// CommitQuorum is the weight of the quorum required for CommitQC.
func (c *Committee) CommitQuorum() uint64 {
	return c.totalWeight - c.Faulty()
}

// AppQuorum is the weight of the quorum required for AppQC.
func (c *Committee) AppQuorum() uint64 {
	// This needs to be in range (c.Faulty(), c.CommitQuorum()]
	return c.CommitQuorum()
}

// PrepareQuorum is the weight of the quorum required for PrepareQC.
func (c *Committee) PrepareQuorum() uint64 {
	return c.CommitQuorum()
}

// TimeoutQuorum is the size of the quorum required for TimeoutQC.
func (c *Committee) TimeoutQuorum() uint64 {
	return c.CommitQuorum()
}

// LaneQuorum is the weight of the quorum required for LaneQC.
func (c *Committee) LaneQuorum() uint64 {
	return c.Faulty() + 1
}

// NewCommittee is genesis: joined = 0 for every member.
func NewCommittee(weights map[PublicKey]uint64) (*Committee, error) {
	weights, totalWeight, err := normalizeWeights(weights)
	if err != nil {
		return nil, err
	}
	lanes := make([]LaneID, 0, len(weights))
	for v := range weights {
		lanes = append(lanes, NewLaneID(v, 0))
	}
	return newCommittee(lanes, weights, totalWeight)
}

// DeriveNext builds the committee for epoch e>0 from this committee:
// copy joined on stay, stamp e on join. EpochIndex stays on Epoch, not Committee.
func (c *Committee) DeriveNext(weights map[PublicKey]uint64, e EpochIndex) (*Committee, error) {
	if e == 0 {
		return nil, errors.New("DeriveNext: epoch must be > 0")
	}
	weights, totalWeight, err := normalizeWeights(weights)
	if err != nil {
		return nil, err
	}
	lanes := make([]LaneID, 0, len(weights))
	for v := range weights {
		lanes = append(lanes, c.Lane(v).Or(NewLaneID(v, e)))
	}
	return newCommittee(lanes, weights, totalWeight)
}

// normalizeWeights clones weights, drops zero entries, and returns the filtered
// map plus total stake. Errors on overflow or empty total.
func normalizeWeights(weights map[PublicKey]uint64) (map[PublicKey]uint64, uint64, error) {
	weights = maps.Clone(weights)
	totalWeight := uint64(0)
	for k, w := range weights {
		if w == 0 {
			delete(weights, k)
			continue
		}
		if utils.Max[uint64]()-totalWeight < w {
			return nil, 0, fmt.Errorf("total weight overflow")
		}
		totalWeight += w
	}
	if totalWeight == 0 {
		return nil, 0, errors.New("total weight is 0")
	}
	if len(weights) > MaxValidators {
		return nil, 0, fmt.Errorf("too many validators: got %d, want <= %d", len(weights), MaxValidators)
	}
	return weights, totalWeight, nil
}

// newCommittee rejects duplicate validators, orders replicas by PublicKey,
// and stores lanes in that same order (one LaneID per replica).
func newCommittee(lanes []LaneID, weights map[PublicKey]uint64, totalWeight uint64) (*Committee, error) {
	byValidator := make(map[PublicKey]LaneID, len(lanes))
	for _, lane := range lanes {
		if _, ok := byValidator[lane.Validator]; ok {
			return nil, fmt.Errorf(
				"duplicate validator in committee lanes: %q with joined %d and %d",
				lane.Validator, byValidator[lane.Validator].Joined, lane.Joined,
			)
		}
		byValidator[lane.Validator] = lane
	}
	replicas := slices.Collect(maps.Keys(byValidator))
	slices.SortFunc(replicas, PublicKey.Compare)
	ordered := make([]LaneID, len(replicas))
	for i, v := range replicas {
		ordered[i] = byValidator[v]
	}
	return &Committee{
		lanes:       ImSlice[LaneID]{ordered},
		byValidator: byValidator,
		weights:     weights,
		totalWeight: totalWeight,
	}, nil
}
