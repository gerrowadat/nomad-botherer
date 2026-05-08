// Package grpcserver provides a gRPC endpoint for querying and controlling
// nomad-botherer. All RPCs require a pre-shared API key supplied in the
// "authorization" metadata header as "Bearer <key>".
package grpcserver

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/gerrowadat/nomad-botherer/internal/grpcapi"
	"github.com/gerrowadat/nomad-botherer/internal/nomad"
)

// DiffSource is satisfied by *nomad.Differ.
type DiffSource interface {
	Diffs() ([]nomad.JobDiff, time.Time, string)
}

// GitStatusSource is satisfied by *gitwatch.Watcher.
type GitStatusSource interface {
	Trigger()
	Status() (lastCommit string, lastUpdate time.Time)
}

// Server implements grpcapi.NomadBothererServer.
type Server struct {
	grpcapi.UnimplementedNomadBothererServer

	apiKey string
	diffs  DiffSource
	git    GitStatusSource

	// Prometheus metrics
	rpcTotal   *prometheus.CounterVec
	rpcErrors  *prometheus.CounterVec
}

// New creates a Server using the default Prometheus registry.
func New(apiKey string, diffs DiffSource, git GitStatusSource) *Server {
	return NewWithRegistry(apiKey, diffs, git, prometheus.DefaultRegisterer)
}

// NewWithRegistry creates a Server with a custom Prometheus Registerer.
func NewWithRegistry(apiKey string, diffs DiffSource, git GitStatusSource, reg prometheus.Registerer) *Server {
	return &Server{
		apiKey: apiKey,
		diffs:  diffs,
		git:    git,
		rpcTotal: promauto.With(reg).NewCounterVec(prometheus.CounterOpts{
			Name: "nomad_botherer_grpc_requests_total",
			Help: "Total number of gRPC requests, by method and status code.",
		}, []string{"method", "code"}),
		rpcErrors: promauto.With(reg).NewCounterVec(prometheus.CounterOpts{
			Name: "nomad_botherer_grpc_auth_errors_total",
			Help: "Total number of gRPC requests rejected due to authentication failure.",
		}, []string{"method"}),
	}
}

// GRPCServer builds and returns a configured *grpc.Server bound to s.
// The caller is responsible for registering it on a listener and shutting it
// down gracefully.
func (s *Server) GRPCServer() *grpc.Server {
	srv := grpc.NewServer(
		grpc.UnaryInterceptor(s.authInterceptor),
	)
	grpcapi.RegisterNomadBothererServer(srv, s)
	return srv
}

// Run starts listening on addr and blocks until ctx is cancelled.
func (s *Server) Run(ctx context.Context, addr string) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("grpc listen %s: %w", addr, err)
	}

	srv := s.GRPCServer()

	go func() {
		<-ctx.Done()
		srv.GracefulStop()
	}()

	slog.Info("gRPC server listening", "addr", addr)
	if err := srv.Serve(lis); err != nil {
		return fmt.Errorf("grpc serve: %w", err)
	}
	return nil
}

// authInterceptor enforces the pre-shared API key.
// Clients must supply metadata: authorization: Bearer <key>
func (s *Server) authInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		s.rpcErrors.WithLabelValues(info.FullMethod).Inc()
		return nil, status.Error(codes.Unauthenticated, "missing metadata")
	}

	values := md.Get("authorization")
	if len(values) == 0 || values[0] != "Bearer "+s.apiKey {
		s.rpcErrors.WithLabelValues(info.FullMethod).Inc()
		return nil, status.Error(codes.Unauthenticated, "invalid or missing API key")
	}

	resp, err := handler(ctx, req)
	code := codes.OK
	if err != nil {
		if st, ok := status.FromError(err); ok {
			code = st.Code()
		} else {
			code = codes.Unknown
		}
	}
	s.rpcTotal.WithLabelValues(info.FullMethod, code.String()).Inc()
	return resp, err
}

// GetDiffs returns the latest set of job diffs.
func (s *Server) GetDiffs(_ context.Context, _ *grpcapi.GetDiffsRequest) (*grpcapi.GetDiffsResponse, error) {
	diffs, lastCheck, lastCommit := s.diffs.Diffs()

	pbDiffs := make([]*grpcapi.JobDiff, 0, len(diffs))
	for _, d := range diffs {
		pbDiffs = append(pbDiffs, &grpcapi.JobDiff{
			JobId:    d.JobID,
			HclFile:  d.HCLFile,
			DiffType: string(d.DiffType),
			Detail:   d.Detail,
		})
	}

	resp := &grpcapi.GetDiffsResponse{
		Diffs:      pbDiffs,
		LastCommit: lastCommit,
	}
	if !lastCheck.IsZero() {
		resp.LastCheckTime = lastCheck.UTC().Format(time.RFC3339)
	}
	return resp, nil
}

// GetStatus returns git watcher status.
func (s *Server) GetStatus(_ context.Context, _ *grpcapi.GetStatusRequest) (*grpcapi.GetStatusResponse, error) {
	lastCommit, lastUpdate := s.git.Status()
	resp := &grpcapi.GetStatusResponse{
		LastCommit: lastCommit,
	}
	if !lastUpdate.IsZero() {
		resp.LastUpdateTime = lastUpdate.UTC().Format(time.RFC3339)
	}
	return resp, nil
}

// TriggerRefresh triggers an immediate git pull.
func (s *Server) TriggerRefresh(_ context.Context, _ *grpcapi.TriggerRefreshRequest) (*grpcapi.TriggerRefreshResponse, error) {
	s.git.Trigger()
	return &grpcapi.TriggerRefreshResponse{Message: "refresh triggered"}, nil
}
