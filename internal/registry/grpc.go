package registry

import (
	"context"
	"errors"
	"time"

	"github.com/fin/multi-agent-rag/internal/models"
	"github.com/fin/multi-agent-rag/internal/registry/registrypb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type grpcServer struct {
	registrypb.UnimplementedRegistryServiceServer
	svc *Service
}

// NewGRPCServer adapts the domain Service to the generated gRPC server.
func NewGRPCServer(svc *Service) registrypb.RegistryServiceServer {
	return &grpcServer{svc: svc}
}

func rfc3339(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func toProto(r models.Repo) *registrypb.Repo {
	return &registrypb.Repo{
		Id:              r.ID,
		Name:            r.Name,
		Provider:        string(r.Provider),
		RemoteUrl:       r.RemoteURL,
		DefaultBranch:   r.DefaultBranch,
		EmbeddingModel:  r.EmbeddingModel,
		EmbeddingDim:    int32(r.EmbeddingDim),
		ActiveVersion:   int32(r.ActiveVersion),
		Labels:          r.Labels,
		CommitsBehind:   int32(r.CommitsBehind),
		LastIngestedAt:  rfc3339(r.LastIngestedAt),
		LastHeartbeatAt: rfc3339(r.LastHeartbeatAt),
		Stale:           r.IsStale(),
	}
}

func fromProto(p *registrypb.Repo) models.Repo {
	return models.Repo{
		ID:             p.Id,
		Name:           p.Name,
		Provider:       models.GitProvider(p.Provider),
		RemoteURL:      p.RemoteUrl,
		DefaultBranch:  p.DefaultBranch,
		EmbeddingModel: p.EmbeddingModel,
		EmbeddingDim:   int(p.EmbeddingDim),
		ActiveVersion:  int(p.ActiveVersion),
		Labels:         p.Labels,
		CommitsBehind:  int(p.CommitsBehind),
	}
}

// mapErr converts domain errors to gRPC status codes.
func mapErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, ErrAlreadyExists):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, models.ErrInvalidRepo):
		return status.Error(codes.InvalidArgument, err.Error())
	default:
		// Anything else (DB/transport/internal failures) is a server-side fault.
		return status.Error(codes.Internal, err.Error())
	}
}

func (g *grpcServer) Register(ctx context.Context, req *registrypb.RegisterRequest) (*registrypb.RegisterResponse, error) {
	if req.Repo == nil {
		return nil, status.Error(codes.InvalidArgument, "repo is required")
	}
	out, err := g.svc.Register(ctx, fromProto(req.Repo))
	if err != nil {
		return nil, mapErr(err)
	}
	return &registrypb.RegisterResponse{Repo: toProto(out)}, nil
}

func (g *grpcServer) GetRepo(ctx context.Context, req *registrypb.GetRepoRequest) (*registrypb.GetRepoResponse, error) {
	out, err := g.svc.Get(ctx, req.Id)
	if err != nil {
		return nil, mapErr(err)
	}
	return &registrypb.GetRepoResponse{Repo: toProto(out)}, nil
}

func (g *grpcServer) ListRepos(ctx context.Context, req *registrypb.ListReposRequest) (*registrypb.ListReposResponse, error) {
	out, err := g.svc.List(ctx, ListFilter{
		Provider:  models.GitProvider(req.Provider),
		Labels:    req.Labels,
		StaleOnly: req.StaleOnly,
	})
	if err != nil {
		return nil, mapErr(err)
	}
	resp := &registrypb.ListReposResponse{Repos: make([]*registrypb.Repo, 0, len(out))}
	for _, r := range out {
		resp.Repos = append(resp.Repos, toProto(r))
	}
	return resp, nil
}

func (g *grpcServer) Heartbeat(ctx context.Context, req *registrypb.HeartbeatRequest) (*registrypb.HeartbeatResponse, error) {
	if err := g.svc.Heartbeat(ctx, req.Id); err != nil {
		return nil, mapErr(err)
	}
	return &registrypb.HeartbeatResponse{}, nil
}

func (g *grpcServer) MarkIngested(ctx context.Context, req *registrypb.MarkIngestedRequest) (*registrypb.MarkIngestedResponse, error) {
	if err := g.svc.MarkIngested(ctx, req.Id, int(req.CommitsBehind), int(req.ActiveVersion)); err != nil {
		return nil, mapErr(err)
	}
	return &registrypb.MarkIngestedResponse{}, nil
}

func (g *grpcServer) Deregister(ctx context.Context, req *registrypb.DeregisterRequest) (*registrypb.DeregisterResponse, error) {
	if err := g.svc.Deregister(ctx, req.Id); err != nil {
		return nil, mapErr(err)
	}
	return &registrypb.DeregisterResponse{}, nil
}
