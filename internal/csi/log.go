package csi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// RequestIDKey is the gRPC metadata key of a call's request id. It is the
// endpoint's own key (internal/server's requestIDKey): the endpoint keeps a
// sane id it receives under it and logs it as request_id on its "rpc" line,
// so zfs-csi's line and the endpoint's for one call can be joined.
const RequestIDKey = "x-request-id"

// PVC parameters external-provisioner adds to CreateVolume with
// --extra-create-metadata (deploy/k8s/controller.yaml).
const (
	paramPVCName      = k8sParamPrefix + "pvc/name"
	paramPVCNamespace = k8sParamPrefix + "pvc/namespace"
)

// UnaryInterceptor gives every CSI call a request id, forwards it to the
// endpoint in the outgoing metadata, and logs one WARN line, "rpc failed",
// per call that fails: method, code, error, duration, request_id, and the
// volume id, snapshot id and PVC the request names, when it names them.
//
// The request id is the caller's x-request-id when it is one the endpoint
// would keep (the sidecars send none today), else a fresh one. Successful
// calls are not logged: the endpoint's journal has a line for every call.
func UnaryInterceptor(log *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		id := incomingRequestID(ctx)
		ctx = metadata.AppendToOutgoingContext(ctx, RequestIDKey, id)
		start := time.Now()
		resp, err := handler(ctx, req)
		if err != nil {
			st := status.Convert(err)
			attrs := []any{"method", info.FullMethod, "code", st.Code().String(), "error", st.Message()}
			attrs = append(attrs, requestAttrs(req)...)
			attrs = append(attrs, "duration", time.Since(start), "request_id", id)
			log.WarnContext(ctx, "rpc failed", attrs...)
		}
		return resp, err
	}
}

// requestAttrs are the ids a CSI request names, for the log line. A
// CreateVolume's name is the volume id this driver returns (the PV name).
func requestAttrs(req any) []any {
	var out []any
	add := func(k, v string) {
		if v != "" {
			out = append(out, k, v)
		}
	}
	switch r := req.(type) {
	case *csi.CreateVolumeRequest:
		add("volume_id", r.GetName())
	case *csi.CreateSnapshotRequest:
		add("snapshot_name", r.GetName())
	}
	if r, ok := req.(interface{ GetVolumeId() string }); ok {
		add("volume_id", r.GetVolumeId())
	}
	if r, ok := req.(interface{ GetSourceVolumeId() string }); ok {
		add("volume_id", r.GetSourceVolumeId())
	}
	if r, ok := req.(interface{ GetSnapshotId() string }); ok {
		add("snapshot_id", r.GetSnapshotId())
	}
	if r, ok := req.(interface{ GetParameters() map[string]string }); ok {
		add("pvc_name", r.GetParameters()[paramPVCName])
		add("pvc_namespace", r.GetParameters()[paramPVCNamespace])
	}
	return out
}

// incomingRequestID is the caller's x-request-id if the endpoint would
// keep it, or a fresh one.
func incomingRequestID(ctx context.Context) string {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if v := md.Get(RequestIDKey); len(v) == 1 && validRequestID(v[0]) {
			return v[0]
		}
	}
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// validRequestID is the endpoint's rule (internal/server validRequestID):
// 1-64 characters of [A-Za-z0-9._-]. An id it refuses would be replaced
// there, and the two lines would no longer match.
func validRequestID(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}
