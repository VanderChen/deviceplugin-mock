/*
Copyright 2025 The Volcano Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package deviceplugin

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

func (s *server) registerDevicePluginServer(g *grpc.Server) {
	pluginapi.RegisterDevicePluginServer(g, s)
}

func (s *server) GetDevicePluginOptions(context.Context, *pluginapi.Empty) (*pluginapi.DevicePluginOptions, error) {
	return &pluginapi.DevicePluginOptions{PreStartRequired: false}, nil
}

func (s *server) ListAndWatch(_ *pluginapi.Empty, server grpc.ServerStreamingServer[pluginapi.ListAndWatchResponse]) error {
	klog.V(3).InfoS("kubelet list and watch start", "resource", s.resourceName)
	defer func() {
		klog.V(3).InfoS("kubelet list and watch end", "resource", s.resourceName)
		select {
		case s.kubeletConnEvt <- struct{}{}:
		default:
		}
	}()

	s.deviceIDs.ListAndWatch(server.Context(), func(list []string) {
		if err := updateDevices(list, server); err != nil {
			klog.ErrorS(err, "failed to update devices to kubelet", s.resourceName)
		}
	})

	return nil
}

func updateDevices(ids []string, srv grpc.ServerStreamingServer[pluginapi.ListAndWatchResponse]) error {
	devices := make([]*pluginapi.Device, 0, len(ids))
	for _, id := range ids {
		devices = append(devices, &pluginapi.Device{
			ID:     id,
			Health: pluginapi.Healthy,
		})
	}
	err := srv.Send(&pluginapi.ListAndWatchResponse{Devices: devices})
	if err != nil {
		return fmt.Errorf("failed to send device list: %w", err)
	}
	return nil
}

func (s *server) GetPreferredAllocation(context.Context, *pluginapi.PreferredAllocationRequest) (*pluginapi.PreferredAllocationResponse, error) {
	return &pluginapi.PreferredAllocationResponse{}, nil
}

func (s *server) Allocate(ctx context.Context, request *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	id := s.allocateID.Add(1)
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		klog.V(3).InfoS("allocate request", "resource", s.resourceName, "id", id, "metadata", md)
	}

	ret := &pluginapi.AllocateResponse{}
	for _, req := range request.ContainerRequests {
		devIds := strings.Join(req.DevicesIds, ",")
		klog.V(3).InfoS("allocate request", "resource", s.resourceName, "id", id, "devIds", devIds)
		resp := pluginapi.ContainerAllocateResponse{
			Envs: map[string]string{
				"DEV_IDS": devIds,
			},
		}
		ret.ContainerResponses = append(ret.ContainerResponses, &resp)
	}
	return ret, nil
}

func (s *server) PreStartContainer(context.Context, *pluginapi.PreStartContainerRequest) (*pluginapi.PreStartContainerResponse, error) {
	return &pluginapi.PreStartContainerResponse{}, nil
}
