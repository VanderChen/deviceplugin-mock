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
	"net"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/json"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"

	"volcano.sh/deviceplugin-mock/pkg/daemon/framework"
)

const connectTimeout = 5 * time.Second

func (s *server) registerToKubelet(ctx context.Context) error {
	conn, err := grpc.NewClient("passthrough:///"+pluginapi.KubeletSocket, grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			if deadline, ok := ctx.Deadline(); ok {
				return net.DialTimeout("unix", addr, time.Until(deadline))
			}
			return net.DialTimeout("unix", addr, connectTimeout)
		}))
	if err != nil {
		return fmt.Errorf("failed to connect to kubelet by socket %s: %w", pluginapi.KubeletSocket, err)
	}
	defer conn.Close()

	client := pluginapi.NewRegistrationClient(conn)
	req := &pluginapi.RegisterRequest{
		Version:      pluginapi.Version,
		Endpoint:     strings.TrimPrefix(strings.TrimPrefix(s.endpoint, pluginapi.DevicePluginPath), "/"),
		ResourceName: s.resourceName,
	}

	_, err = client.Register(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to register to kubelet: %w", err)
	}
	return nil
}

func (s *server) deregister(ctx context.Context) error {
	patch := map[string]interface{}{
		"status": map[string]interface{}{
			"allocatable": map[string]interface{}{
				s.resourceName: nil,
			},
			"capacity": map[string]interface{}{
				s.resourceName: nil,
			},
		},
	}

	bb, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("failed to marshal patch: %w", err)
	}

	_, err = framework.GetClientSet().KubeClient.CoreV1().Nodes().
		Patch(ctx, framework.GetEnvs().NodeName, types.MergePatchType, bb, metav1.PatchOptions{}, "status")
	if err != nil {
		return fmt.Errorf("failed to patch node status: %w", err)
	}

	return nil
}
