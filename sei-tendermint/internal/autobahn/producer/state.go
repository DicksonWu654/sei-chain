package producer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sei-protocol/sei-chain/sei-tendermint/autobahn/types"
	"github.com/sei-protocol/sei-chain/sei-tendermint/internal/autobahn/avail"
	"github.com/sei-protocol/sei-chain/sei-tendermint/internal/autobahn/consensus"
	"github.com/sei-protocol/sei-chain/sei-tendermint/internal/proxy"
	"github.com/sei-protocol/sei-chain/sei-tendermint/libs/utils"
	"github.com/sei-protocol/sei-chain/sei-tendermint/libs/utils/scope"
	"golang.org/x/time/rate"
)

// Config is the config of the block scope.
type Config struct {
	MaxGasWantedPerBlock    uint64
	MaxGasEstimatedPerBlock uint64
	MaxTxsPerBlock          uint64
	AllowEmptyBlocks        bool
	// Delay after which a non-full block can be produced.
	BlockInterval time.Duration
	// TESTONLY: max rate at which lane is produced. It can be used to do
	// benchmarks with stable throughput, in case execution performance degrades
	// when overloaded.
	MaxTxsPerSecond utils.Option[uint64]
}

const minTxGas = 21000

func (c *Config) maxTxsPerBlock() uint64 {
	return min(types.MaxTxsPerBlock, c.MaxTxsPerBlock)
}

// State is the block producer state.
type State struct {
	cfg *Config
	app *proxy.Proxy
	// mempoolInner.m is None when not producing. Present only for an active produce session.
	mempool   utils.Watch[*mempoolInner]
	consensus *consensus.State
}

// NewState constructs a new block producer state.
// Mempool is created when LocalLane is present; otherwise None until a produce session.
func NewState(cfg *Config, consensus *consensus.State, app *proxy.Proxy) *State {
	inner := &mempoolInner{}
	if lane, ok := consensus.Avail().LocalLane().Get(); ok {
		inner.m = utils.Some(newMempool(avail.BlocksPerLane, lane, consensus.Avail().NextBlock(lane)))
	}
	return &State{
		cfg:       cfg,
		app:       app,
		mempool:   utils.NewWatch(inner),
		consensus: consensus,
	}
}

// alignMempool returns the session mempool for lane (reusing one already aligned).
func (s *State) alignMempool(lane types.LaneID) *mempool {
	n := s.consensus.Avail().NextBlock(lane)
	for inner, ctrl := range s.mempool.Lock() {
		if m, ok := inner.m.Get(); ok && m.lane == lane {
			return m // same session (incl. first Run after NewState)
		}
		m := newMempool(avail.BlocksPerLane, lane, n)
		inner.m = utils.Some(m)
		ctrl.Updated()
		return m
	}
	panic("unreachable")
}

// clearMempool drops the mempool so InsertTx returns ErrNotProducing until alignMempool.
func (s *State) clearMempool() {
	for inner, ctrl := range s.mempool.Lock() {
		inner.m = utils.None[*mempool]()
		ctrl.Updated()
	}
}

// Run runs the background tasks of the producer state:
// * prunes executed lane blocks from mempool
// * pushes new lane blocks from mempool to avail state
// Note that mempool capacity bounds the number of unexecuted blocks of the local lane.
// This is needed so that we can track the evm nonces of sequenced txs - mempool admits txs
// sequentially in the nonce order.
//
// Sessions: WaitForLocalLane → produce until WaitMustStop; then clearMempool. Stay keeps the session.
func (s *State) Run(ctx context.Context) error {
	availState := s.consensus.Avail()
	for ctx.Err() == nil {
		lane, err := availState.WaitForLocalLane(ctx)
		if err != nil {
			return err
		}

		err = utils.IgnoreCancel(scope.Run(ctx, func(ctx context.Context, sc scope.Scope) error {
			sc.Spawn(func() error {
				return s.produceSession(ctx, availState, lane)
			})
			sc.Spawn(func() error {
				// Cancels seal / executed waits that do not observe committee.
				if err := availState.WaitMustStop(ctx, lane); err != nil {
					return err
				}
				return context.Canceled
			})
			return nil
		}))
		s.clearMempool()
		if err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (s *State) produceSession(ctx context.Context, availState *avail.State, lane types.LaneID) error {
	m := s.alignMempool(lane)
	firstBlock := m.first
	return scope.Run(ctx, func(ctx context.Context, scope scope.Scope) error {
		scope.Spawn(func() error {
			// Task pruning executed lane blocks from the mempool
			dataState := s.consensus.Data()
			var err error
			for toExecute := firstBlock; ; {
				if toExecute, err = dataState.WaitUntilExecuted(ctx, lane, toExecute); err != nil {
					return err
				}
				s.pruneMempool(m, toExecute)
			}
		})
		scope.Spawn(func() error {
			// Task pushing blocks from mempool to avail state.
			limit := rate.Inf
			burst := 1
			if l, ok := s.cfg.MaxTxsPerSecond.Get(); ok {
				limit = rate.Limit(l)
				burst = int(l + s.cfg.MaxTxsPerBlock) // nolint:gosec
			}
			limiter := rate.NewLimiter(limit, burst)
			lastBlockTime := time.Now()
			for toProduce := firstBlock; ; toProduce += 1 {
				if err := availState.WaitForCapacity(ctx, lane, toProduce); err != nil {
					return s.sessionOpErr(lane, "availState.WaitForCapacity()", err)
				}
				var payload *types.Payload
				// Wait until either
				// * there is a full proposal in mempool
				// * BlockInterval since the last block passed AND (AllowEmptyBlocks OR mempool is non-empty)
				for _, ctrl := range s.mempool.Lock() {
					// Wait for full payload with timeout.
					if err := utils.WithDeadline(ctx, utils.Some(lastBlockTime.Add(s.cfg.BlockInterval)), func(ctx context.Context) error {
						return ctrl.WaitUntil(ctx, func() bool { return toProduce < m.next })
					}); err != nil {
						if ctx.Err() != nil {
							return ctx.Err()
						}
						// Wait for non-empty payload.
						if err := ctrl.WaitUntil(ctx, func() bool {
							return toProduce < m.next || (toProduce == m.next && m.CanSealBlock(s.cfg.AllowEmptyBlocks))
						}); err != nil {
							return err
						}
						// Seal the payload if needed.
						if toProduce == m.next {
							m.SealBlock()
							ctrl.Updated()
						}
					}
					b, ok := m.blocks[toProduce]
					if !ok {
						// Block number tracking should always be in sync between avail state and mempool:
						// * mempool keeps blocks until they are executed.
						// * blocks can be executed only after they are included in the lane.
						// * lane is populated from the mempool.
						return fmt.Errorf("mempool mismatched block production")
					}
					var err error
					payload, err = types.PayloadBuilder{
						CreatedAt:         time.Now(),
						TotalGasWanted:    b.gasWanted,
						TotalGasEstimated: b.gasEstimated,
						Txs:               b.txs,
					}.Build()
					if err != nil {
						// This should never happen: we construct the payload from correctly sized data.
						panic(fmt.Errorf("PayloadBuilder{}.Build(): %w", err))
					}
				}
				if _, err := availState.ProduceLocalBlock(lane, toProduce, payload); err != nil {
					return s.sessionOpErr(lane, "availState.ProduceLocalBlock()", err)
				}
				lastBlockTime = time.Now()
				if err := limiter.WaitN(ctx, len(payload.Txs())); err != nil {
					return fmt.Errorf("limiter(): %w", err)
				}
			}
		})
		return nil
	})
}

// sessionOpErr maps leave ErrBadLane → Canceled so Run can WaitForLocalLane again.
func (s *State) sessionOpErr(lane types.LaneID, op string, err error) error {
	if errors.Is(err, avail.ErrBadLane) {
		if got, ok := s.consensus.Avail().LocalLane().Get(); !ok || got != lane {
			return context.Canceled
		}
	}
	return fmt.Errorf("%s: %w", op, err)
}
