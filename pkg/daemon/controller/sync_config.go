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

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"

	dpmockv1alpha1 "volcano.sh/deviceplugin-mock/api/dpmock/v1alpha1"
	"volcano.sh/deviceplugin-mock/pkg/daemon/framework"
	"volcano.sh/deviceplugin-mock/pkg/daemon/resmanager"
)

const NrcfgKey = "controller/nrcfg"

// ManagedResourcesKey is durable ownership, not an inventory of all cluster CRs.
const ManagedResourcesKey = "dpmock.volcano.sh/managed-resources"

type managedResource struct {
	UndoPatch string `json:"undoPatch,omitempty"`
	Profile   string `json:"profile,omitempty"`
	Retired   bool   `json:"retired,omitempty"`
}

func nodeResourcePresent(node *corev1.Node, name string) bool {
	_, capacity := node.Status.Capacity[corev1.ResourceName(name)]
	_, allocatable := node.Status.Allocatable[corev1.ResourceName(name)]
	return capacity || allocatable
}

func hasRetiredResource(node *corev1.Node) bool {
	var owned map[string]managedResource
	if json.Unmarshal([]byte(node.Annotations[ManagedResourcesKey]), &owned) != nil {
		return false
	}
	for name, record := range owned {
		if record.Retired && nodeResourcePresent(node, name) {
			return true
		}
	}
	return false
}

var nrcfgEmpty = &dpmockv1alpha1.NodeResourceConfiguration{
	ObjectMeta: metav1.ObjectMeta{
		Name: "nrcfg-empty",
	},
}

func (c *Controller) syncConfigOnce(ctx context.Context) error {
	node, err := c.nodeLister.Get(framework.GetEnvs().NodeName)
	if err != nil {
		return fmt.Errorf("failed to get node %s: %w", framework.GetEnvs().NodeName, err)
	}

	nrcfgList, err := c.nrcfgLister.List(labels.Everything())
	if err != nil {
		return fmt.Errorf("failed to list nrcfg: %w", err)
	}
	sort.Slice(nrcfgList, func(i, j int) bool {
		if nrcfgList[i].CreationTimestamp.Equal(&nrcfgList[j].CreationTimestamp) {
			return nrcfgList[i].UID < nrcfgList[j].UID
		}
		return nrcfgList[i].CreationTimestamp.Before(&nrcfgList[j].CreationTimestamp)
	})

	for _, nrcfg := range nrcfgList {
		var selector labels.Selector
		selector, err = metav1.LabelSelectorAsSelector(nrcfg.Spec.NodeSelector)
		if err != nil {
			klog.Warningf("failed to get selector from nrcfg '%s': %v", nrcfg.Name, err)
			continue
		}
		if selector.Matches(labels.Set(node.Labels)) {
			return c.applyConfig(ctx, nrcfg) // apply first config only
		}
	}

	return c.applyConfig(ctx, nrcfgEmpty)
}

func (c *Controller) applyConfig(ctx context.Context, nrcfg *dpmockv1alpha1.NodeResourceConfiguration) error {
	klog.V(3).InfoS("applying NodeResourceConfiguration for current node", "nrcfg", nrcfg.Name)
	defer klog.V(3).InfoS("apply NodeResourceConfiguration for current node finished", "nrcfg", nrcfg.Name)

	nodeName := framework.GetEnvs().NodeName
	node, err := c.kubeClient.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	owned := make(map[string]managedResource)
	if data := node.Annotations[ManagedResourcesKey]; data != "" {
		if err := json.Unmarshal([]byte(data), &owned); err != nil {
			return fmt.Errorf("invalid node managed-resources record: %w", err)
		}
		if owned == nil {
			owned = make(map[string]managedResource)
		}
	}
	resDescMap := make(map[string]*dpmockv1alpha1.ResourceDescription)
	profiles := make(map[string]string)
	for i, desc := range nrcfg.Status.ResourceDescriptions {
		resDescMap[desc.ResourceName] = &desc
		if i < len(nrcfg.Spec.Resources) {
			identity, err := json.Marshal(nrcfg.Spec.Resources[i].ResourceInfo)
			if err != nil {
				return fmt.Errorf("marshal resource identity for nrcfg %s resource %s: %w", nrcfg.Name, desc.ResourceName, err)
			}
			profiles[desc.ResourceName] = string(identity)
		}
	}
	var pending []error
	// A replacement must wait for both old workers and onExit. Otherwise old
	// deregistration/undo can remove the newly advertised resource or topology.
	for name, manager := range c.managers {
		_, wanted := resDescMap[name]
		if !wanted || manager.Profile != profiles[name] || manager.Stopping {
			c.staleResources[name] = struct{}{}
			if !manager.Stopping {
				manager.Cancel()
				manager.Stopping = true
				c.managers[name] = manager
			}
			select {
			case <-manager.Done:
				delete(c.managers, name)
			default:
				pending = append(pending, fmt.Errorf("resource %s is stopping", name))
			}
		}
	}
	for name, record := range owned {
		_, wanted := resDescMap[name]
		if (!wanted && (!record.Retired || nodeResourcePresent(node, name))) || (record.Profile != "" && record.Profile != profiles[name]) {
			c.staleResources[name] = struct{}{}
		}
	}
	for name := range c.staleResources {
		if _, running := c.managers[name]; running {
			continue
		}
		if patch := owned[name].UndoPatch; patch != "" {
			body, err := yaml.YAMLToJSON([]byte(patch))
			if err == nil {
				_, err = c.kubeClient.CoreV1().Nodes().Patch(ctx, nodeName, types.StrategicMergePatchType, body, metav1.PatchOptions{})
			}
			if err != nil {
				pending = append(pending, fmt.Errorf("undo resource %s: %w", name, err))
				continue
			}
		}
		absent, err := resmanager.CleanupNodeStatusResourceWithClient(ctx, c.kubeClient, nodeName, name)
		if err != nil {
			pending = append(pending, fmt.Errorf("clean stale resource %s: %w", name, err))
			continue
		}
		if absent {
			delete(c.staleResources, name)
			// Kubelet can restore 8/0 from its device-plugin checkpoint after a
			// successful removal. Retain ownership to recognize that later write.
			owned[name] = managedResource{Retired: true}
		} else {
			pending = append(pending, fmt.Errorf("stale resource %s is still present", name))
		}
	}
	for name, desc := range resDescMap {
		if _, stale := c.staleResources[name]; stale {
			continue
		}
		owned[name] = managedResource{UndoPatch: desc.NodeUndoPatch, Profile: profiles[name]}
	}
	// Persist ownership before starting/advertising any new resource. Include a
	// resourceVersion precondition so a concurrent actor cannot lose its record.
	data, err := json.Marshal(owned)
	if err != nil {
		return err
	}
	if string(data) != node.Annotations[ManagedResourcesKey] {
		// Cleanup may have changed the node resourceVersion since the first Get.
		fresh, err := c.kubeClient.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if fresh.Annotations[ManagedResourcesKey] != node.Annotations[ManagedResourcesKey] {
			return errors.New("managed-resources changed concurrently; retry")
		}
		patch, err := json.Marshal(map[string]any{
			"metadata": map[string]any{
				"resourceVersion": fresh.ResourceVersion,
				"annotations":     map[string]string{ManagedResourcesKey: string(data)},
			},
		})
		if err != nil {
			return fmt.Errorf("marshal managed-resources patch for node %s: %w", nodeName, err)
		}
		if _, err := c.kubeClient.CoreV1().Nodes().Patch(ctx, nodeName, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
			return err
		}
	}
	for name, desc := range resDescMap {
		if _, stale := c.staleResources[name]; stale {
			continue
		}
		if _, ok := c.managers[name]; !ok {
			// create resources nonexistent in current node
			manager := c.newManager(name)
			manager.Update(desc)
			child, cancel := context.WithCancel(ctx)
			done := make(chan struct{})
			go func() {
				defer close(done)
				wait.UntilWithContext(child, manager.Run, 5*time.Second)
			}()
			c.managers[name] = resourceManagerWithCancel{
				ResourceManager: manager,
				Cancel:          cancel,
				Done:            done,
				Profile:         profiles[name],
			}
		}

		// update resources status (capacity, deviceID, etc.)
		c.managers[name].Update(desc)
	}

	var names []string
	for name := range c.managers {
		names = append(names, name)
	}
	klog.V(3).InfoS("alive resource managers", "names", names)

	framework.GetStorage().Set(NrcfgKey, nrcfg)
	return errors.Join(pending...)
}
