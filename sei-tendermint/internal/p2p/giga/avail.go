package giga

import (
	"context"
	"errors"
	"fmt"

	"github.com/sei-protocol/sei-chain/sei-tendermint/autobahn/types"
	"github.com/sei-protocol/sei-chain/sei-tendermint/internal/autobahn/avail"
	apb "github.com/sei-protocol/sei-chain/sei-tendermint/internal/autobahn/pb"
	"github.com/sei-protocol/sei-chain/sei-tendermint/internal/p2p/giga/pb"
	"github.com/sei-protocol/sei-chain/sei-tendermint/internal/p2p/mux"
	"github.com/sei-protocol/sei-chain/sei-tendermint/internal/p2p/rpc"
	"github.com/sei-protocol/sei-chain/sei-tendermint/libs/utils"
)

func (x *Service) serverStreamLaneProposals(ctx context.Context, server rpc.Server[API]) error {
	return StreamLaneProposals.Serve(ctx, server, func(ctx context.Context, stream rpc.Stream[*pb.LaneProposal, *pb.StreamLaneProposalsReq]) error {
		reqRaw, err := stream.Recv(ctx)
		if err != nil {
			return err
		}
		req, err := StreamLaneProposalsReqConv.Decode(reqRaw)
		if err != nil {
			return fmt.Errorf("StreamLaneProposalsReqConv.Decode(): %w", err)
		}
		sub, err := x.validatorState().Avail().SubscribeLaneProposals(req.LaneID, req.FirstBlockNumber)
		if err != nil {
			return err
		}
		for {
			p, err := sub.Recv(ctx)
			if err != nil {
				// Lane closed / tipcut pruned: end the stream cleanly so the client
				// can wait for a new LaneID of this producer.
				if errors.Is(err, avail.ErrLanePruned) {
					return nil
				}
				return err
			}
			if err := stream.Send(ctx, LaneProposalConv.Encode(p)); err != nil {
				return fmt.Errorf("stream.Send(): %w", err)
			}
		}
	})
}

func (x *Service) serverStreamLaneVotes(ctx context.Context, server rpc.Server[API]) error {
	return StreamLaneVotes.Serve(ctx, server, func(ctx context.Context, stream rpc.Stream[*pb.LaneVote, *pb.StreamLaneVotesReq]) error {
		reqRaw, err := stream.Recv(ctx)
		if err != nil {
			return err
		}
		_ = reqRaw
		sub := x.validatorState().Avail().SubscribeLaneVotes()
		for {
			batch, err := sub.RecvBatch(ctx)
			if err != nil {
				return err
			}
			for _, vote := range batch {
				if err := stream.Send(ctx, LaneVoteConv.Encode(vote)); err != nil {
					return fmt.Errorf("stream.Send(): %w", err)
				}
			}
		}
	})
}

func (x *Service) serverStreamAppVotes(ctx context.Context, server rpc.Server[API]) error {
	return StreamAppVotes.Serve(ctx, server, func(ctx context.Context, stream rpc.Stream[*pb.AppVote, *pb.StreamAppVotesReq]) error {
		reqRaw, err := stream.Recv(ctx)
		if err != nil {
			return err
		}
		_ = reqRaw
		sub := x.validatorState().Avail().SubscribeAppVotes()
		for {
			vote, err := sub.Recv(ctx)
			if err != nil {
				return err
			}
			if err := stream.Send(ctx, AppVoteConv.Encode(vote)); err != nil {
				return fmt.Errorf("stream.Send(): %w", err)
			}
		}
	})
}

func (x *Service) serverStreamAppQCs(ctx context.Context, server rpc.Server[API]) error {
	return StreamAppQCs.Serve(ctx, server, func(ctx context.Context, stream rpc.Stream[*pb.StreamAppQCsResp, *pb.StreamAppQCsReq]) error {
		reqRaw, err := stream.Recv(ctx)
		if err != nil {
			return err
		}
		_ = reqRaw
		next := types.RoadIndex(0)
		for {
			appQC, commitQC, err := x.validatorState().Avail().WaitForAppQC(ctx, next)
			if err != nil {
				return fmt.Errorf("x.validatorState().Avail().WaitForAppQC(): %w", err)
			}
			next = appQC.Next()
			if err := stream.Send(ctx, StreamAppQCsRespConv.Encode(&StreamAppQCsResp{
				AppQC:    appQC,
				CommitQC: commitQC,
			})); err != nil {
				return fmt.Errorf("stream.Send(): %w", err)
			}
		}
	})
}

func (x *Service) serverStreamCommitQCs(ctx context.Context, server rpc.Server[API]) error {
	return StreamCommitQCs.Serve(ctx, server, func(ctx context.Context, stream rpc.Stream[*apb.CommitQC, *pb.StreamCommitQCsReq]) error {
		next := types.RoadIndex(0)
		for {
			qc, err := x.validatorState().Avail().CommitQC(ctx, next)
			if err != nil {
				if errors.Is(err, types.ErrPruned) {
					next = x.validatorState().Avail().FirstCommitQC()
					continue
				}
				return fmt.Errorf("x.validatorState().Avail().FirstCommitQC(): %w", err)
			}
			next = qc.Index() + 1
			if err := stream.Send(ctx, types.CommitQCConv.Encode(qc)); err != nil {
				return fmt.Errorf("stream.Send(): %w", err)
			}
		}
	})
}

func (x *Service) clientStreamLaneProposals(ctx context.Context, c rpc.Client[API], peer types.PublicKey) error {
	a := x.validatorState().Avail()
	var exclude utils.Option[types.LaneID]
	first := types.BlockNumber(0)
	for ctx.Err() == nil {
		lane, err := a.WaitLane(ctx, peer, exclude)
		if err != nil {
			return err
		}
		if err := x.streamLaneProposalsOnce(ctx, c, lane, first); err != nil {
			return err
		}
		// Stream ended. Only exclude when the applied committee has dropped or
		// replaced this LaneID (leave / rejoin). If it is still present, reconnect
		// to the same identity — a Stay / transport blip must not hang on
		// WaitLane(exclude). Rejoin is at least one epoch after leave, so tip prune
		// of the leave map lands before a new LaneID; we do not need to cancel the
		// old stream early on rejoin.
		cur, ok := a.Lane(peer).Get()
		if !ok || cur != lane {
			exclude = utils.Some(lane)
			first = 0
		} else {
			exclude = utils.None[types.LaneID]()
			first = a.NextBlock(lane)
		}
	}
	return ctx.Err()
}

func (x *Service) streamLaneProposalsOnce(ctx context.Context, c rpc.Client[API], lane types.LaneID, first types.BlockNumber) error {
	stream, err := StreamLaneProposals.Call(ctx, c)
	if err != nil {
		return err
	}
	defer stream.Close()
	// TODO(gprusak): dissemination of LaneProposals is the main source of bandwidth consumption.
	// * to keep low latency, we need to push the lane proposals (streaming is required)
	// * to avoid wasting bandwidth, set FirstBlockNumber from local tip once peers are authenticated
	req := &StreamLaneProposalsReq{LaneID: lane, FirstBlockNumber: first}
	if err := stream.Send(ctx, StreamLaneProposalsReqConv.Encode(req)); err != nil {
		return fmt.Errorf("client.StreamLaneProposals(): %w", err)
	}
	for {
		rawProposal, err := stream.Recv(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Server closed after lane prune (handler returns nil → mux CLOSE).
			if errors.Is(err, mux.ErrRemoteClosed) {
				return nil
			}
			return fmt.Errorf("stream.Recv(): %w", err)
		}
		proposal, err := LaneProposalConv.Decode(rawProposal)
		if err != nil {
			return fmt.Errorf("LaneProposalConv.Decode(): %w", err)
		}
		if proposal.Msg().Block().Header().Lane() != lane {
			return fmt.Errorf("producer lane = %v, want %v", proposal.Msg().Block().Header().Lane(), lane)
		}
		if err := x.validatorState().Avail().PushBlock(ctx, proposal); err != nil {
			return fmt.Errorf("s.PushLaneProposal(): %w", err)
		}
	}
}

func (x *Service) clientStreamLaneVotes(ctx context.Context, c rpc.Client[API]) error {
	stream, err := StreamLaneVotes.Call(ctx, c)
	if err != nil {
		return fmt.Errorf("client.StreamLaneVotes(): %w", err)
	}
	defer stream.Close()
	if err := stream.Send(ctx, &pb.StreamLaneVotesReq{}); err != nil {
		return err
	}
	for {
		rawVote, err := stream.Recv(ctx)
		if err != nil {
			return fmt.Errorf("stream.Recv(): %w", err)
		}
		vote, err := LaneVoteConv.Decode(rawVote)
		if err != nil {
			return fmt.Errorf("LaneVoteConv.Decode(): %w", err)
		}
		if err := x.validatorState().Avail().PushVote(ctx, vote); err != nil {
			return fmt.Errorf("s.PushLaneVote(): %w", err)
		}
	}
}

func (x *Service) clientStreamCommitQCs(ctx context.Context, c rpc.Client[API]) error {
	stream, err := StreamCommitQCs.Call(ctx, c)
	if err != nil {
		return fmt.Errorf("client.StreamCommitQCs(): %w", err)
	}
	defer stream.Close()
	if err := stream.Send(ctx, &pb.StreamCommitQCsReq{}); err != nil {
		return err
	}
	for {
		resp, err := stream.Recv(ctx)
		if err != nil {
			return fmt.Errorf("stream.Recv(): %w", err)
		}
		qc, err := types.CommitQCConv.Decode(resp)
		if err != nil {
			return fmt.Errorf("types.CommitQCConv.Decode(): %w", err)
		}
		if err := x.validatorState().Avail().PushCommitQC(ctx, qc); err != nil {
			return fmt.Errorf("s.PushFirstCommitQC(): %w", err)
		}
	}
}

func (x *Service) clientStreamAppVotes(ctx context.Context, c rpc.Client[API]) error {
	stream, err := StreamAppVotes.Call(ctx, c)
	if err != nil {
		return fmt.Errorf("client.StreamAppVotes(): %w", err)
	}
	defer stream.Close()
	if err := stream.Send(ctx, &pb.StreamAppVotesReq{}); err != nil {
		return err
	}
	for {
		rawVote, err := stream.Recv(ctx)
		if err != nil {
			return fmt.Errorf("stream.Recv(): %w", err)
		}
		vote, err := AppVoteConv.Decode(rawVote)
		if err != nil {
			return fmt.Errorf("AppVoteConv.Decode(): %w", err)
		}
		if err := x.validatorState().Avail().PushAppVote(ctx, vote); err != nil {
			return fmt.Errorf("s.PushLaneVote(): %w", err)
		}
	}
}

func (x *Service) clientStreamAppQCs(ctx context.Context, c rpc.Client[API]) error {
	stream, err := StreamAppQCs.Call(ctx, c)
	if err != nil {
		return fmt.Errorf("client.StreamAppQCs(): %w", err)
	}
	defer stream.Close()
	if err := stream.Send(ctx, &pb.StreamAppQCsReq{}); err != nil {
		return err
	}
	for {
		resp, err := stream.Recv(ctx)
		if err != nil {
			return fmt.Errorf("stream.Recv(): %w", err)
		}
		msg, err := StreamAppQCsRespConv.Decode(resp)
		if err != nil {
			return fmt.Errorf("StreamAppQCsRespConv.Decode(): %w", err)
		}
		if err := x.validatorState().Avail().PushAppQC(msg.AppQC, msg.CommitQC); err != nil {
			return fmt.Errorf("s.PushFirstCommitQC(): %w", err)
		}
	}
}
