package registry

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/fin/multi-agent-rag/internal/models"
	"github.com/fin/multi-agent-rag/internal/registry/registrypb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func dialInProc(t *testing.T, svc *Service) registrypb.RegistryServiceClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	registrypb.RegisterRegistryServiceServer(srv, NewGRPCServer(svc))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return registrypb.NewRegistryServiceClient(conn)
}

func TestGRPCRegisterAndGet(t *testing.T) {
	ctx := context.Background()
	client := dialInProc(t, NewService(NewMemStore()))

	_, err := client.Register(ctx, &registrypb.RegisterRequest{Repo: &registrypb.Repo{
		Id: "g1", Name: "acme/g1", Provider: "github",
		RemoteUrl: "https://github.com/acme/g1", DefaultBranch: "main",
		EmbeddingModel: "nomic-embed-text",
	}})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	resp, err := client.GetRepo(ctx, &registrypb.GetRepoRequest{Id: "g1"})
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	if resp.Repo.EmbeddingDim != int32(models.DefaultEmbeddingDim) {
		t.Fatalf("EmbeddingDim = %d, want %d", resp.Repo.EmbeddingDim, models.DefaultEmbeddingDim)
	}
	if !resp.Repo.Stale {
		t.Fatal("never-ingested repo should report stale=true")
	}
}

func TestGRPCGetMissingIsNotFound(t *testing.T) {
	client := dialInProc(t, NewService(NewMemStore()))
	_, err := client.GetRepo(context.Background(), &registrypb.GetRepoRequest{Id: "nope"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("code = %v, want NotFound", status.Code(err))
	}
}

func TestGRPCRegisterInvalidIsInvalidArgument(t *testing.T) {
	client := dialInProc(t, NewService(NewMemStore()))
	_, err := client.Register(context.Background(), &registrypb.RegisterRequest{Repo: &registrypb.Repo{
		Id: "bad", Name: "x", Provider: "svn",
		RemoteUrl: "u", DefaultBranch: "main", EmbeddingModel: "m",
	}})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestMapErr(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want codes.Code
	}{
		{"nil", nil, codes.OK},
		{"not found", ErrNotFound, codes.NotFound},
		{"already exists", ErrAlreadyExists, codes.AlreadyExists},
		{"wrapped already exists", fmt.Errorf("register x: %w", ErrAlreadyExists), codes.AlreadyExists},
		{"validation error", fmt.Errorf("validate repo: %w: bad", models.ErrInvalidRepo), codes.InvalidArgument},
		{"generic error", errors.New("db exploded"), codes.Internal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := status.Code(mapErr(tt.err)); got != tt.want {
				t.Fatalf("mapErr(%v) code = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
