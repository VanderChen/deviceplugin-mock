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

package resmanager

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
	"sigs.k8s.io/yaml"

	dpmockv1alpha1 "volcano.sh/deviceplugin-mock/api/dpmock/v1alpha1"
	"volcano.sh/deviceplugin-mock/pkg/daemon/framework"
	"volcano.sh/deviceplugin-mock/pkg/daemon/resmanager/deviceplugin"
	"volcano.sh/deviceplugin-mock/pkg/util"
)

const (
	pluginDir          = "volcano"
	endpointNameLength = 8
)

type ResourceManager interface {
	Run(ctx context.Context)
	Update(desc *dpmockv1alpha1.ResourceDescription)
}

func New(resourceName string) ResourceManager {
	manager := resourceManager{
		resourceName: resourceName,
		dpServer:     deviceplugin.NewDpServer(resourceName, path.Join(pluginapi.DevicePluginPath, pluginDir, rand.String(endpointNameLength)+".sock")),
	}
	return &manager
}

type resourceManager struct {
	resourceName string
	dpServer     deviceplugin.Server

	desc         atomic.Pointer[dpmockv1alpha1.ResourceDescription]
	synchronized atomic.Bool
}

func (m *resourceManager) Run(ctx context.Context) {
	klog.V(3).InfoS("resource manager start", "resourceName", m.resourceName)
	defer klog.V(3).InfoS("resource manager end", "resourceName", m.resourceName)

	go wait.UntilWithContext(ctx, func(ctx context.Context) {
		if err := m.dpServer.Serve(ctx); err != nil {
			klog.ErrorS(err, "dp server terminated with error", "resourceName", m.resourceName)
		}
	}, 5*time.Second)

	go wait.UntilWithContext(ctx, func(ctx context.Context) {
		if m.synchronized.Load() {
			return
		}

		m.synchronized.Store(true)
		if err := m.syncConfigOnce(ctx); err != nil {
			klog.ErrorS(err, "sync config once failed", "resourceName", m.resourceName)
			m.synchronized.Store(false)
		}
	}, time.Second)

	<-ctx.Done()

	m.onExit()
}

func (m *resourceManager) Update(desc *dpmockv1alpha1.ResourceDescription) {
	if equality.Semantic.DeepEqual(m.desc.Load(), desc) {
		return
	}

	m.desc.Store(desc.DeepCopy())
	m.synchronized.Store(false)
}

func (m *resourceManager) syncConfigOnce(ctx context.Context) error {
	desc := m.desc.Load()
	if desc == nil {
		return nil
	}

	deviceIDs := util.BuildDeviceID(&desc.DeviceIDFormat)
	m.dpServer.UpdateDeviceIDs(deviceIDs)
	klog.V(3).InfoS("resource update deviceIDs", "resourceName", m.resourceName, "format", desc.DeviceIDFormat)

	if desc.NodePatch != "" {
		if err := patchToNode(ctx, []byte(desc.NodePatch)); err != nil {
			return err
		}
		klog.V(3).InfoS("resource patch to node success", "resourceName", m.resourceName, "patch", desc.NodePatch)
	}

	return nil
}

func (m *resourceManager) onExit() {
	desc := m.desc.Load()
	if desc != nil && desc.NodeUndoPatch != "" {
		if err := patchToNode(framework.ContextOnExit(), []byte(desc.NodeUndoPatch)); err != nil {
			klog.ErrorS(err, "resource undo patch to node failed", "resourceName", m.resourceName, "undoPatch", desc.NodeUndoPatch)
		} else {
			klog.V(3).InfoS("resource undo patch to node success", "resourceName", m.resourceName, "undoPatch", desc.NodeUndoPatch)
		}
	}
	if err := ensureNodeStatusResourceRemoved(framework.ContextOnExit(), m.resourceName); err != nil {
		klog.ErrorS(err, "resource cleanup from node status failed", "resourceName", m.resourceName)
	}
}

func patchToNode(ctx context.Context, patch []byte) error {
	jsonPatch, err := yaml.YAMLToJSON(patch)
	if err != nil {
		return err
	}

	_, err = framework.GetClientSet().KubeClient.CoreV1().Nodes().
		Patch(ctx, framework.GetEnvs().NodeName, types.StrategicMergePatchType, jsonPatch, metav1.PatchOptions{})
	if err != nil {
		klog.V(5).InfoS("patch to node fail", "error", err, "patch", string(jsonPatch))
		return fmt.Errorf("patch to node fail: %w", err)
	}
	klog.V(3).InfoS("patch to node success", "patch", string(jsonPatch))

	return nil
}

func ensureNodeStatusResourceRemoved(ctx context.Context, resourceName string) error {
	return wait.PollUntilContextTimeout(ctx, time.Second, 15*time.Second, true, func(ctx context.Context) (bool, error) {
		if _, err := CleanupNodeStatusResource(ctx, resourceName); err != nil {
			klog.ErrorS(err, "failed to patch node status for resource cleanup", "resourceName", resourceName)
			return false, nil
		}

		node, err := framework.GetClientSet().KubeClient.CoreV1().Nodes().Get(ctx, framework.GetEnvs().NodeName, metav1.GetOptions{})
		if err != nil {
			klog.ErrorS(err, "failed to get node while checking resource cleanup", "resourceName", resourceName)
			return false, nil
		}
		_, capacityExists := node.Status.Capacity[corev1.ResourceName(resourceName)]
		_, allocatableExists := node.Status.Allocatable[corev1.ResourceName(resourceName)]
		return !capacityExists && !allocatableExists, nil
	})
}

func CleanupNodeStatusResource(ctx context.Context, resourceName string) (bool, error) {
	patch, err := buildNodeStatusResourceRemovedPatch(resourceName)
	if err != nil {
		return false, err
	}
	if _, err = framework.GetClientSet().KubeClient.CoreV1().Nodes().
		Patch(ctx, framework.GetEnvs().NodeName, types.MergePatchType, patch, metav1.PatchOptions{}, "status"); err != nil {
		return false, err
	}

	node, err := framework.GetClientSet().KubeClient.CoreV1().Nodes().Get(ctx, framework.GetEnvs().NodeName, metav1.GetOptions{})
	if err != nil {
		return false, err
	}
	_, capacityExists := node.Status.Capacity[corev1.ResourceName(resourceName)]
	_, allocatableExists := node.Status.Allocatable[corev1.ResourceName(resourceName)]
	return !capacityExists && !allocatableExists, nil
}

func buildNodeStatusResourceRemovedPatch(resourceName string) ([]byte, error) {
	patch := map[string]interface{}{
		"status": map[string]interface{}{
			"allocatable": map[string]interface{}{
				resourceName: nil,
			},
			"capacity": map[string]interface{}{
				resourceName: nil,
			},
		},
	}
	bb, err := json.Marshal(patch)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal node status cleanup patch: %w", err)
	}
	return bb, nil
}
