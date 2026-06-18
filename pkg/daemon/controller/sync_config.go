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
	"fmt"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	dpmockv1alpha1 "volcano.sh/deviceplugin-mock/api/dpmock/v1alpha1"
	"volcano.sh/deviceplugin-mock/pkg/daemon/framework"
	"volcano.sh/deviceplugin-mock/pkg/daemon/resmanager"
)

const NrcfgKey = "controller/nrcfg"

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

	knownResourceNames := make(map[string]struct{})
	for _, nrcfg := range nrcfgList {
		for _, desc := range nrcfg.Status.ResourceDescriptions {
			knownResourceNames[desc.ResourceName] = struct{}{}
		}
	}

	for _, nrcfg := range nrcfgList {
		var selector labels.Selector
		selector, err = metav1.LabelSelectorAsSelector(nrcfg.Spec.NodeSelector)
		if err != nil {
			klog.Warningf("failed to get selector from nrcfg '%s': %v", nrcfg.Name, err)
			continue
		}
		if selector.Matches(labels.Set(node.Labels)) {
			c.applyConfig(ctx, nrcfg, knownResourceNames)
			return nil // apply first config only
		}
	}

	c.applyConfig(ctx, nrcfgEmpty, knownResourceNames)
	return nil
}

func (c *Controller) applyConfig(ctx context.Context, nrcfg *dpmockv1alpha1.NodeResourceConfiguration, knownResourceNames map[string]struct{}) {
	klog.V(3).InfoS("applying NodeResourceConfiguration for current node", "nrcfg", nrcfg.Name)
	defer klog.V(3).InfoS("apply NodeResourceConfiguration for current node finished", "nrcfg", nrcfg.Name)

	resDescMap := make(map[string]*dpmockv1alpha1.ResourceDescription)
	for _, desc := range nrcfg.Status.ResourceDescriptions {
		resDescMap[desc.ResourceName] = &desc
	}

	// clean up resources nonexistent in config
	for name, manager := range c.managers {
		if _, ok := resDescMap[name]; !ok {
			klog.V(3).InfoS("clean up resource", "name", name)
			c.staleResources[name] = struct{}{}
			manager.Cancel()
			delete(c.managers, name)
		}
	}

	for name := range knownResourceNames {
		if _, ok := resDescMap[name]; ok {
			continue
		}
		c.staleResources[name] = struct{}{}
	}

	for name := range c.staleResources {
		if _, ok := resDescMap[name]; ok {
			delete(c.staleResources, name)
			continue
		}
		absent, err := resmanager.CleanupNodeStatusResource(ctx, name)
		if err != nil {
			klog.ErrorS(err, "failed to clean stale resource from node status", "name", name)
			continue
		}
		if absent {
			delete(c.staleResources, name)
		}
	}

	for name, desc := range resDescMap {
		if _, ok := c.managers[name]; !ok {
			// create resources nonexistent in current node
			manager := resmanager.New(name)
			child, cancel := context.WithCancel(ctx)
			go wait.UntilWithContext(child, manager.Run, 5*time.Second)
			c.managers[name] = resourceManagerWithCancel{
				ResourceManager: manager,
				Cancel:          cancel,
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
}
