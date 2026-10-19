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
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes/fake"
	corelisters "k8s.io/client-go/listers/core/v1"
	ktesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"

	dpmock "volcano.sh/deviceplugin-mock/api/dpmock/v1alpha1"
	listers "volcano.sh/deviceplugin-mock/client-go/listers/dpmock/v1alpha1"
	"volcano.sh/deviceplugin-mock/pkg/daemon/framework"
	"volcano.sh/deviceplugin-mock/pkg/daemon/resmanager"
)

const testResource = "example.com/mock"

type testManager struct{}

func (*testManager) Run(ctx context.Context)            { <-ctx.Done() }
func (*testManager) Update(*dpmock.ResourceDescription) {}

func controllerFixture(t *testing.T, owned map[string]managedResource) (*Controller, *fake.Clientset) {
	t.Helper()
	oldEnv := *framework.GetEnvs()
	framework.GetEnvs().NodeName = "worker"
	stored, hadStored := framework.GetStorage().Get(NrcfgKey)
	t.Cleanup(func() {
		*framework.GetEnvs() = oldEnv
		if hadStored {
			framework.GetStorage().Set(NrcfgKey, stored)
		} else {
			framework.GetStorage().Delete(NrcfgKey)
		}
	})
	data, _ := json.Marshal(owned)
	node := &v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker", Annotations: map[string]string{ManagedResourcesKey: string(data), "external": "keep"}}, Status: v1.NodeStatus{Capacity: v1.ResourceList{v1.ResourceName(testResource): resource.MustParse("8")}, Allocatable: v1.ResourceList{v1.ResourceName(testResource): resource.MustParse("8")}}}
	client := fake.NewSimpleClientset(node)
	ni := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	ni.Add(node)
	ci := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	c := &Controller{kubeClient: client, nodeLister: corelisters.NewNodeLister(ni), nrcfgLister: listers.NewNodeResourceConfigurationLister(ci), managers: map[string]resourceManagerWithCancel{}, staleResources: map[string]struct{}{}, newManager: func(string) resmanager.ResourceManager { return &testManager{} }}
	return c, client
}

func TestControllerRetriesWithoutConfigEvent(t *testing.T) {
	c, client := controllerFixture(t, map[string]managedResource{testResource: {}})
	failures := 0
	client.PrependReactor("patch", "nodes", func(a ktesting.Action) (bool, runtime.Object, error) {
		if a.GetSubresource() == "status" && failures == 0 {
			failures++
			return true, nil, errors.New("transient cleanup error")
		}
		return false, nil, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); c.Run(ctx) }()
	err := wait.PollUntilContextTimeout(context.Background(), 20*time.Millisecond, 4*time.Second, true, func(context.Context) (bool, error) {
		node, err := client.CoreV1().Nodes().Get(context.Background(), "worker", metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		var records map[string]managedResource
		if err := json.Unmarshal([]byte(node.Annotations[ManagedResourcesKey]), &records); err != nil {
			return false, err
		}
		return records[testResource].Retired && !nodeResourcePresent(node, testResource), nil
	})
	cancel()
	<-done
	if err != nil {
		t.Fatal(err)
	}
	if failures != 1 {
		t.Fatal("failure path not exercised")
	}
	node, _ := client.CoreV1().Nodes().Get(context.Background(), "worker", metav1.GetOptions{})
	if _, ok := node.Status.Capacity[v1.ResourceName(testResource)]; ok {
		t.Fatal("capacity leaked")
	}
	if node.Annotations["external"] != "keep" {
		t.Fatal("external annotation lost")
	}
}

func TestControllerRetriesWhenPatchDoesNotRemoveResource(t *testing.T) {
	c, client := controllerFixture(t, map[string]managedResource{testResource: {}})
	first := true
	client.PrependReactor("patch", "nodes", func(a ktesting.Action) (bool, runtime.Object, error) {
		if a.GetSubresource() == "status" && first {
			first = false
			obj, _ := client.Tracker().Get(v1.SchemeGroupVersion.WithResource("nodes"), "", "worker")
			return true, obj, nil
		}
		return false, nil, nil
	})
	if err := c.applyConfig(context.Background(), nrcfgEmpty); err == nil {
		t.Fatal("pending cleanup was marked synchronized")
	}
	if err := c.applyConfig(context.Background(), nrcfgEmpty); err != nil {
		t.Fatalf("retry failed: %v", err)
	}
}

func TestControllerDoesNotCleanUnownedResource(t *testing.T) {
	c, client := controllerFixture(t, nil)
	if err := c.applyConfig(context.Background(), nrcfgEmpty); err != nil {
		t.Fatal(err)
	}
	for _, a := range client.Actions() {
		if a.GetVerb() == "patch" && a.GetSubresource() == "status" {
			t.Fatal("unowned resource was patched")
		}
	}
	node, _ := client.CoreV1().Nodes().Get(context.Background(), "worker", metav1.GetOptions{})
	quantity := node.Status.Capacity[v1.ResourceName(testResource)]
	if quantity.String() != "8" {
		t.Fatal("unowned capacity changed")
	}
}

func TestRetiredResourceReappearingAfterCleanup(t *testing.T) {
	c, client := controllerFixture(t, map[string]managedResource{testResource: {Retired: true}})
	ctx := context.Background()
	if err := c.applyConfig(ctx, nrcfgEmpty); err != nil {
		t.Fatal(err)
	}
	node, _ := client.CoreV1().Nodes().Get(ctx, "worker", metav1.GetOptions{})
	if hasRetiredResource(node) {
		t.Fatal("absent retired resource triggers cleanup")
	}
	// Kubelet restores its checkpoint after the first successful cleanup.
	node.Status.Capacity = v1.ResourceList{v1.ResourceName(testResource): resource.MustParse("8")}
	node.Status.Allocatable = v1.ResourceList{v1.ResourceName(testResource): resource.MustParse("0")}
	client.CoreV1().Nodes().UpdateStatus(ctx, node, metav1.UpdateOptions{})
	if !hasRetiredResource(node) {
		t.Fatal("kubelet rewrite would not trigger reconciliation")
	}
	// A fresh controller instance still recognizes the persisted tombstone.
	restarted := &Controller{kubeClient: client, managers: map[string]resourceManagerWithCancel{}, staleResources: map[string]struct{}{}}
	if err := restarted.applyConfig(ctx, nrcfgEmpty); err != nil {
		t.Fatal(err)
	}
	node, _ = client.CoreV1().Nodes().Get(ctx, "worker", metav1.GetOptions{})
	if nodeResourcePresent(node, testResource) {
		t.Fatal("rewritten resource survived restart cleanup")
	}
	client.ClearActions()
	if err := restarted.applyConfig(ctx, nrcfgEmpty); err != nil {
		t.Fatal(err)
	}
	for _, a := range client.Actions() {
		if a.GetVerb() == "patch" {
			t.Fatal("absent retired resource produces patches")
		}
	}
}

func TestOwnershipPrecedesManagerAndSwitchWaitsForExit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, client := controllerFixture(t, nil)
	oldDone := make(chan struct{})
	c.managers[testResource] = resourceManagerWithCancel{ResourceManager: &testManager{}, Cancel: func() {}, Done: oldDone, Profile: "old"}
	owned, _ := json.Marshal(map[string]managedResource{testResource: {Profile: "old", UndoPatch: "metadata:\n  annotations:\n    old-status: null\n"}})
	node, _ := client.CoreV1().Nodes().Get(ctx, "worker", metav1.GetOptions{})
	node.Annotations[ManagedResourcesKey] = string(owned)
	node.Annotations["old-status"] = "stale"
	client.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
	cfg := &dpmock.NodeResourceConfiguration{Spec: dpmock.NodeResourceConfigurationSpec{Resources: []dpmock.ResourceRequirement{{ResourceInfo: dpmock.ResourceInfo{ResourceName: testResource}}}}, Status: dpmock.NodeResourceConfigurationStatus{ResourceDescriptions: []dpmock.ResourceDescription{{ResourceBasicDescription: dpmock.ResourceBasicDescription{ResourceName: testResource}}}}}
	starts := 0
	c.newManager = func(string) resmanager.ResourceManager {
		current, _ := client.CoreV1().Nodes().Get(ctx, "worker", metav1.GetOptions{})
		if current.Annotations[ManagedResourcesKey] == "" || current.Annotations[ManagedResourcesKey] == "{}" {
			t.Fatal("manager started without ownership")
		}
		if _, ok := current.Annotations["old-status"]; ok {
			t.Fatal("new manager started before old undo")
		}
		starts++
		return &testManager{}
	}
	if err := c.applyConfig(ctx, cfg); err == nil || starts != 0 {
		t.Fatal("replacement did not wait for old exit")
	}
	close(oldDone)
	if err := c.applyConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if starts != 1 {
		t.Fatal("replacement did not start")
	}
	cancel()
	for _, manager := range c.managers {
		<-manager.Done
	}
}
