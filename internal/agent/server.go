package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	"github.com/sumartiot/dozzle-log-archive/internal/agent/pb"
	"github.com/sumartiot/dozzle-log-archive/internal/registry"
	"github.com/sumartiot/dozzle-log-archive/internal/storage"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	_ "google.golang.org/grpc/encoding/gzip"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	DozzleProtocolVersion = "v10.6.14"
	ArchiveHostID         = "log-archive"
	ArchiveHostName       = "Log Archive"
	stdoutMask            = int32(2)
)

type logStore interface {
	LogsBetween(context.Context, string, time.Time, time.Time) ([]storage.LogRow, error)
	LatestSince(context.Context, string, time.Time, time.Time) ([]storage.LogRow, error)
}

type serviceRegistry interface {
	Snapshot() []registry.Service
	Lookup(string) (registry.Service, bool)
	SubscribeStarted(context.Context) <-chan registry.Service
}

type logBroker interface {
	Subscribe(context.Context, string) <-chan storage.LogRow
}

type Server struct {
	pb.UnimplementedAgentServiceServer
	store    logStore
	registry serviceRegistry
	broker   logBroker
}

func NewService(store logStore, registry serviceRegistry, broker logBroker) *Server {
	return &Server{store: store, registry: registry, broker: broker}
}

func NewGRPCServer(service pb.AgentServiceServer, certPath, keyPath string) (*grpc.Server, error) {
	certificate, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load Dozzle TLS certificate: %w", err)
	}
	if len(certificate.Certificate) == 0 {
		return nil, errors.New("Dozzle TLS certificate contains no certificates")
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parse Dozzle TLS certificate: %w", err)
	}
	clientCAs := x509.NewCertPool()
	clientCAs.AddCert(leaf)
	tlsConfig := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{certificate},
		ClientCAs:    clientCAs,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}
	server := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(tlsConfig)),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             15 * time.Second,
			PermitWithoutStream: true,
		}),
	)
	pb.RegisterAgentServiceServer(server, service)
	return server, nil
}

func (s *Server) ListContainers(_ context.Context, request *pb.ListContainersRequest) (*pb.ListContainersResponse, error) {
	services := s.registry.Snapshot()
	containers := make([]*pb.Container, 0, len(services))
	for _, service := range services {
		container := containerFor(service)
		if matchesFilters(container, request.GetFilter()) {
			containers = append(containers, container)
		}
	}
	return &pb.ListContainersResponse{Containers: containers}, nil
}

func (s *Server) FindContainer(_ context.Context, request *pb.FindContainerRequest) (*pb.FindContainerResponse, error) {
	service, ok := s.registry.Lookup(request.GetContainerId())
	if !ok {
		return nil, status.Error(codes.NotFound, "archived service not found")
	}
	container := containerFor(service)
	if !matchesFilters(container, request.GetFilter()) {
		return nil, status.Error(codes.NotFound, "archived service excluded by filter")
	}
	return &pb.FindContainerResponse{Container: container}, nil
}

func (s *Server) StreamLogs(request *pb.StreamLogsRequest, stream pb.AgentService_StreamLogsServer) error {
	service, ok := s.registry.Lookup(request.GetContainerId())
	if !ok {
		return status.Error(codes.NotFound, "archived service not found")
	}

	liveRows := s.broker.Subscribe(stream.Context(), service.Name)
	cutoff := time.Now().UTC()
	since := time.Time{}
	if request.GetSince() != nil {
		since = request.GetSince().AsTime()
	}
	if includesStdout(request.GetStreamTypes()) {
		rows, err := s.store.LatestSince(stream.Context(), service.Name, since, cutoff)
		if err != nil {
			return unavailable(err)
		}
		for _, row := range rows {
			if err := sendLog(stream.Send, service.ID, row); err != nil {
				return err
			}
		}
	}

	for {
		select {
		case <-stream.Context().Done():
			return nil
		case row, open := <-liveRows:
			if !open {
				return status.Error(codes.ResourceExhausted, "live log subscriber was too slow; reconnect to replay persisted logs")
			}
			if includesStdout(request.GetStreamTypes()) {
				if err := sendLog(stream.Send, service.ID, row); err != nil {
					return err
				}
			}
		}
	}
}

func (s *Server) LogsBetweenDates(request *pb.LogsBetweenDatesRequest, stream pb.AgentService_LogsBetweenDatesServer) error {
	service, ok := s.registry.Lookup(request.GetContainerId())
	if !ok {
		return status.Error(codes.NotFound, "archived service not found")
	}
	if request.GetSince() == nil || request.GetUntil() == nil {
		return status.Error(codes.InvalidArgument, "since and until are required")
	}
	if !includesStdout(request.GetStreamTypes()) {
		return nil
	}
	rows, err := s.store.LogsBetween(stream.Context(), service.Name, request.GetSince().AsTime(), request.GetUntil().AsTime())
	if err != nil {
		return unavailable(err)
	}
	for _, row := range rows {
		if err := sendLog(stream.Send, service.ID, row); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) StreamRawBytes(request *pb.StreamRawBytesRequest, stream pb.AgentService_StreamRawBytesServer) error {
	service, ok := s.registry.Lookup(request.GetContainerId())
	if !ok {
		return status.Error(codes.NotFound, "archived service not found")
	}
	if request.GetSince() == nil || request.GetUntil() == nil {
		return status.Error(codes.InvalidArgument, "since and until are required")
	}
	if !includesStdout(request.GetStreamTypes()) {
		return nil
	}
	rows, err := s.store.LogsBetween(stream.Context(), service.Name, request.GetSince().AsTime(), request.GetUntil().AsTime())
	if err != nil {
		return unavailable(err)
	}
	for _, row := range rows {
		if err := stream.Send(&pb.StreamRawBytesResponse{Data: []byte(row.Message + "\n")}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) StreamEvents(_ *pb.StreamEventsRequest, stream pb.AgentService_StreamEventsServer) error {
	<-stream.Context().Done()
	return nil
}

func (s *Server) StreamStats(_ *pb.StreamStatsRequest, stream pb.AgentService_StreamStatsServer) error {
	<-stream.Context().Done()
	return nil
}

func (s *Server) StreamContainerStarted(_ *pb.StreamContainerStartedRequest, stream pb.AgentService_StreamContainerStartedServer) error {
	started := s.registry.SubscribeStarted(stream.Context())
	for {
		select {
		case <-stream.Context().Done():
			return nil
		case service, open := <-started:
			if !open {
				return status.Error(codes.ResourceExhausted, "container-started subscriber was too slow")
			}
			if err := stream.Send(&pb.StreamContainerStartedResponse{Container: containerFor(service)}); err != nil {
				return err
			}
		}
	}
}

func (s *Server) HostInfo(context.Context, *pb.HostInfoRequest) (*pb.HostInfoResponse, error) {
	return &pb.HostInfoResponse{Host: &pb.Host{
		Id:           ArchiveHostID,
		Name:         ArchiveHostName,
		AgentVersion: DozzleProtocolVersion,
	}}, nil
}

func (s *Server) ContainerAction(context.Context, *pb.ContainerActionRequest) (*pb.ContainerActionResponse, error) {
	return nil, readOnlyError("ContainerAction")
}

func (s *Server) UpdateContainer(*pb.UpdateContainerRequest, pb.AgentService_UpdateContainerServer) error {
	return readOnlyError("UpdateContainer")
}

func (s *Server) ContainerExec(pb.AgentService_ContainerExecServer) error {
	return readOnlyError("ContainerExec")
}

func (s *Server) ContainerAttach(pb.AgentService_ContainerAttachServer) error {
	return readOnlyError("ContainerAttach")
}

func (s *Server) UpdateNotificationConfig(context.Context, *pb.UpdateNotificationConfigRequest) (*pb.UpdateNotificationConfigResponse, error) {
	return &pb.UpdateNotificationConfigResponse{}, nil
}

func (s *Server) UpdateCloudConfig(context.Context, *pb.UpdateCloudConfigRequest) (*pb.UpdateCloudConfigResponse, error) {
	return &pb.UpdateCloudConfigResponse{}, nil
}

func (s *Server) GetNotificationStats(context.Context, *pb.GetNotificationStatsRequest) (*pb.GetNotificationStatsResponse, error) {
	return &pb.GetNotificationStatsResponse{Stats: []*pb.NotificationSubscriptionStats{}}, nil
}

func containerFor(service registry.Service) *pb.Container {
	earliest := service.Earliest.UTC()
	return &pb.Container{
		Id:          service.ID,
		Name:        service.Name,
		Image:       "postgres-log-archive",
		State:       "running",
		Created:     timestamppb.New(earliest),
		Started:     timestamppb.New(earliest),
		Finished:    timestamppb.New(time.Time{}),
		Host:        ArchiveHostID,
		Labels:      map[string]string{"coolify.resourceName": service.Name},
		FullyLoaded: true,
	}
}

func sendLog(send func(*pb.StreamLogsResponse) error, containerID string, row storage.LogRow) error {
	event, err := logEvent(containerID, row)
	if err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	return send(&pb.StreamLogsResponse{Event: event})
}

func logEvent(containerID string, row storage.LogRow) (*pb.LogEvent, error) {
	message, err := anypb.New(&pb.SingleMessage{Message: row.Message})
	if err != nil {
		return nil, fmt.Errorf("encode log message: %w", err)
	}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(row.Message))
	return &pb.LogEvent{
		Id:          hash.Sum32(),
		ContainerId: containerID,
		Message:     message,
		Timestamp:   &timestamppb.Timestamp{Seconds: row.Timestamp.UnixMilli()},
		Level:       "unknown",
		Stream:      "stdout",
		Type:        "single",
		RawMessage:  row.Message,
	}, nil
}

func includesStdout(streamTypes int32) bool {
	return streamTypes == 0 || streamTypes&stdoutMask != 0
}

func readOnlyError(operation string) error {
	return status.Errorf(codes.FailedPrecondition, "%s is unavailable: log archive containers are read-only", operation)
}

func unavailable(err error) error {
	return status.Error(codes.Unavailable, err.Error())
}

func matchesFilters(container *pb.Container, filters map[string]*pb.RepeatedString) bool {
	for key, repeated := range filters {
		values := repeated.GetValues()
		if len(values) == 0 {
			continue
		}
		switch key {
		case "label":
			for _, value := range values {
				name, expected, hasValue := strings.Cut(value, "=")
				actual, exists := container.GetLabels()[name]
				if !exists || hasValue && actual != expected {
					return false
				}
			}
		case "id":
			if !anyMatch(values, func(value string) bool { return strings.HasPrefix(container.GetId(), value) }) {
				return false
			}
		case "name":
			if !anyMatch(values, func(value string) bool { return strings.Contains(container.GetName(), value) }) {
				return false
			}
		case "status", "state":
			if !anyMatch(values, func(value string) bool { return container.GetState() == value }) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func anyMatch(values []string, predicate func(string) bool) bool {
	for _, value := range values {
		if predicate(value) {
			return true
		}
	}
	return false
}
