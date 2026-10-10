// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package grpc

import (
	"fmt"

	"google.golang.org/grpc/encoding"
	"google.golang.org/grpc/mem"
)

// rawFrame is one encoded conduitv1.Frame. The session layer encodes and
// decodes frames itself (transports never interpret them), so the gRPC
// message is the already encoded protobuf: on the wire it is exactly the
// message a generated Conduit stub would send.
type rawFrame struct {
	b []byte
}

// frameCodec passes encoded frames through unchanged. Its name is "proto",
// so the content type is application/grpc+proto, as for a generated stub.
//
// It is never registered globally (encoding.RegisterCodecV2 would replace
// the proto codec of every gRPC user in the process). The conduit server
// selects it with grpc.ForceServerCodecV2 and the dialer with
// grpc.ForceCodecV2, so only conduit streams use it.
type frameCodec struct{}

var _ encoding.CodecV2 = frameCodec{}

// Marshal copies the frame: gRPC may write the returned buffer after
// SendMsg returns, and the caller of WriteFrame may reuse its slice.
func (frameCodec) Marshal(v any) (mem.BufferSlice, error) {
	f, ok := v.(*rawFrame)
	if !ok {
		return nil, fmt.Errorf("conduit/grpc: cannot marshal %T", v)
	}
	b := make([]byte, len(f.b))
	copy(b, f.b)
	return mem.BufferSlice{mem.SliceBuffer(b)}, nil
}

// Unmarshal copies the received message out of gRPC's pooled buffers.
func (frameCodec) Unmarshal(data mem.BufferSlice, v any) error {
	f, ok := v.(*rawFrame)
	if !ok {
		return fmt.Errorf("conduit/grpc: cannot unmarshal into %T", v)
	}
	f.b = data.Materialize()
	return nil
}

// Name implements encoding.CodecV2.
func (frameCodec) Name() string { return codecName }

// codecName is the content subtype of conduit streams.
const codecName = "proto"
