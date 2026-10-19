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

package ascend

import (
	"context"
	"errors"
	"reflect"
	"testing"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	dpmock "volcano.sh/deviceplugin-mock/api/dpmock/v1alpha1"
	"volcano.sh/deviceplugin-mock/pkg/daemon/controller"
	"volcano.sh/deviceplugin-mock/pkg/daemon/framework"
)

func preserveAscendTestState(t *testing.T) {
	t.Helper()
	envs, svc, active := *framework.GetEnvs(), *d950, d950ConfigActive
	stored, hadStored := framework.GetStorage().Get(controller.NrcfgKey)
	framework.GetStorage().Set(controller.NrcfgKey, &dpmock.NodeResourceConfiguration{})
	t.Cleanup(func() {
		*framework.GetEnvs(), *d950, d950ConfigActive = envs, svc, active
		if hadStored {
			framework.GetStorage().Set(controller.NrcfgKey, stored)
		} else {
			framework.GetStorage().Delete(controller.NrcfgKey)
		}
	})
}

func rootInfoFixture(t *testing.T) (*AscendD950, *fake.Clientset, *bool) {
	t.Helper()
	preserveAscendTestState(t)
	*framework.GetEnvs() = framework.Envs{NodeName: "worker", NodeIP: "172.18.0.2", PodNamespace: "mock", PodName: "daemon-pod", PodUID: "pod-uid", Instance: "release"}
	active := true
	d950ConfigActive = func() bool { return active }
	client := fake.NewSimpleClientset()
	return &AscendD950{Enabled: true, kubeClient: client}, client, &active
}

func TestRootInfoContractAndLifecycle(t *testing.T) {
	svc, client, active := rootInfoFixture(t)
	ctx := context.Background()
	historical := &v1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "root-info-172.18.0.1", Namespace: "mock", UID: "historical", Labels: rootInfoLabels()}, Data: map[string]string{"hccl_rootinfo.json": "old-IP"}}
	if _, err := client.CoreV1().ConfigMaps("mock").Create(ctx, historical, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	// Another release must never be adopted or deleted.
	foreign := historical.DeepCopy()
	foreign.Name, foreign.UID = "root-info-other", "other"
	foreign.Labels["app.kubernetes.io/instance"] = "other-release"
	if _, err := client.CoreV1().ConfigMaps("mock").Create(ctx, foreign, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := svc.reconcileRootInfo(ctx); err != nil {
		t.Fatal(err)
	}
	cm, err := client.CoreV1().ConfigMaps("mock").Get(ctx, "root-info-172.18.0.2", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if cm.Data["hccl_rootinfo.json"] == "" || len(cm.OwnerReferences) != 1 || cm.OwnerReferences[0].UID != "pod-uid" {
		t.Fatalf("wrong contract/owner: %+v", cm)
	}
	if _, err := client.CoreV1().ConfigMaps("mock").Get(ctx, historical.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("historical node-IP CM remains: %v", err)
	}
	cm.Labels["example.com/external"] = "keep"
	cm.Data["external"] = "keep"
	cm.Annotations = map[string]string{"example.com/external": "keep"}
	if _, err := client.CoreV1().ConfigMaps("mock").Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := svc.reconcileRootInfo(ctx); err != nil {
		t.Fatal(err)
	}
	cm, _ = client.CoreV1().ConfigMaps("mock").Get(ctx, cm.Name, metav1.GetOptions{})
	if cm.Labels["example.com/external"] != "keep" || cm.Data["external"] != "keep" || cm.Annotations["example.com/external"] != "keep" {
		t.Fatalf("external fields lost: %+v", cm)
	}
	client.ClearActions()
	if err := svc.reconcileRootInfo(ctx); err != nil {
		t.Fatal(err)
	}
	for _, a := range client.Actions() {
		if a.GetVerb() != "get" {
			t.Fatalf("steady state has unnecessary action: %v", a)
		}
	}
	*active = false
	if err := svc.reconcileRootInfo(ctx); err != nil {
		t.Fatal(err)
	}
	client.ClearActions()
	for i := 0; i < 3; i++ {
		if err := svc.reconcileRootInfo(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(client.Actions()) != 0 {
		t.Fatal("inactive state repeats API requests")
	}
	if _, err := client.CoreV1().ConfigMaps("mock").Get(ctx, foreign.Name, metav1.GetOptions{}); err != nil {
		t.Fatalf("foreign CM removed: %v", err)
	}
	*active = true
	if err := svc.reconcileRootInfo(ctx); err != nil {
		t.Fatal(err)
	}
	*active = false
	if err := svc.reconcileRootInfo(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().ConfigMaps("mock").Get(ctx, cm.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("reactivation failed to reset deletion state")
	}
}

func TestRootInfoRetriesCreationAndHistoricalCleanup(t *testing.T) {
	svc, client, active := rootInfoFixture(t)
	ctx := context.Background()
	historical := &v1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "root-info-172.18.0.1", Namespace: "mock", UID: "historical", Labels: rootInfoLabels()}}
	client.CoreV1().ConfigMaps("mock").Create(ctx, historical, metav1.CreateOptions{})
	createFails := true
	client.PrependReactor("create", "configmaps", func(ktesting.Action) (bool, runtime.Object, error) {
		if createFails {
			return true, nil, errors.New("create unavailable")
		}
		return false, nil, nil
	})
	if err := svc.reconcileRootInfo(ctx); err == nil {
		t.Fatal("create failure hidden")
	}
	if _, err := client.CoreV1().ConfigMaps("mock").Get(ctx, historical.Name, metav1.GetOptions{}); err != nil {
		t.Fatal("failed creation deleted historical node-IP CM")
	}
	createFails = false
	if err := svc.reconcileRootInfo(ctx); err != nil {
		t.Fatal(err)
	}
	*active = false
	deleteFails := true
	client.PrependReactor("delete", "configmaps", func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.(ktesting.DeleteAction).GetDeleteOptions().Preconditions == nil {
			t.Fatal("missing UID precondition")
		}
		if deleteFails {
			return true, nil, errors.New("delete unavailable")
		}
		return false, nil, nil
	})
	if err := svc.reconcileRootInfo(ctx); err == nil {
		t.Fatal("delete failure hidden")
	}
	deleteFails = false
	if err := svc.reconcileRootInfo(ctx); err != nil {
		t.Fatal(err)
	}
	if !svc.inactiveClean {
		t.Fatal("deletion was not retried")
	}
}

func TestRootInfoAddressAndOptionalService(t *testing.T) {
	for ip, want := range map[string]string{"172.18.0.2": "root-info-172.18.0.2", "2001:db8::1": "root-info-v6-20010db8000000000000000000000001", "invalid": "", "fe80::1%eth0": ""} {
		got := rootInfoConfigMapName(ip)
		if got != want {
			t.Fatalf("%s => %q, want %q", ip, got, want)
		}
		if got != "" && len(validation.IsDNS1123Subdomain(got)) != 0 {
			t.Fatalf("invalid name %s", got)
		}
	}
	svc, client, _ := rootInfoFixture(t)
	framework.GetEnvs().PodNamespace = ""
	if err := svc.Initialize(); err != nil || svc.Enabled {
		t.Fatalf("missing namespace did not disable optional service: %v", err)
	}
	svc.syncRootInfoConfigMap(context.Background())
	if len(client.Actions()) != 0 {
		t.Fatal("disabled service called API")
	}
}

func TestRootInfoWaitsForConfigurationAndMigratesIdentity(t *testing.T) {
	svc, client, _ := rootInfoFixture(t)
	ctx := context.Background()
	framework.GetStorage().Delete(controller.NrcfgKey)
	if err := svc.reconcileRootInfo(ctx); err != nil {
		t.Fatal(err)
	}
	if len(client.Actions()) != 0 {
		t.Fatal("unknown configuration triggered API requests")
	}
	framework.GetStorage().Set(controller.NrcfgKey, &dpmock.NodeResourceConfiguration{})
	if err := svc.reconcileRootInfo(ctx); err != nil {
		t.Fatal(err)
	}
	framework.GetEnvs().PodUID = "replacement-pod"
	if err := svc.reconcileRootInfo(ctx); err != nil {
		t.Fatal(err)
	}
	cm, err := client.CoreV1().ConfigMaps("mock").Get(ctx, "root-info-172.18.0.2", metav1.GetOptions{})
	if err != nil || cm.OwnerReferences[0].UID != "replacement-pod" {
		t.Fatalf("owner not refreshed: %v %+v", err, cm)
	}
	framework.GetEnvs().NodeIP = "2001:db8::1"
	if err := svc.reconcileRootInfo(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().ConfigMaps("mock").Get(ctx, cm.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("old IP ConfigMap remains")
	}
	if _, err := client.CoreV1().ConfigMaps("mock").Get(ctx, "root-info-v6-20010db8000000000000000000000001", metav1.GetOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestRootInfoDoesNotAdoptAnotherRelease(t *testing.T) {
	svc, client, _ := rootInfoFixture(t)
	labels := rootInfoLabels()
	labels["app.kubernetes.io/instance"] = "foreign"
	cm := &v1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "root-info-172.18.0.2", Namespace: "mock", Labels: labels}, Data: map[string]string{"external": "keep"}}
	if _, err := client.CoreV1().ConfigMaps("mock").Create(context.Background(), cm, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	client.ClearActions()
	if err := svc.reconcileRootInfo(context.Background()); err == nil {
		t.Fatal("foreign CM was adopted")
	}
	for _, action := range client.Actions() {
		if action.GetVerb() != "get" {
			t.Fatalf("foreign CM mutated: %v", action)
		}
	}
}

func TestDeviceIDSortAndProfileSelection(t *testing.T) {
	preserveAscendTestState(t)
	want := []string{"davinci02", "davinci2", "davinci10", "davinci1x", "other"}
	for _, ids := range [][]string{{"other", "davinci10", "davinci2", "davinci1x", "davinci02"}, {"davinci1x", "davinci02", "davinci2", "other", "davinci10"}} {
		sortDeviceIDs(ids)
		if !reflect.DeepEqual(ids, want) {
			t.Fatalf("sort=%v", ids)
		}
	}
	for _, profile := range []string{"ascend-d910a", "ascend-d910b", "ascend-d910c", "ascend-d950"} {
		cfg := &dpmock.NodeResourceConfiguration{Spec: dpmock.NodeResourceConfigurationSpec{Resources: []dpmock.ResourceRequirement{{ResourceInfo: dpmock.ResourceInfo{ResourceRef: &dpmock.ResourceReference{Name: profile}}}}}}
		framework.GetStorage().Set(controller.NrcfgKey, cfg)
		if activeMockSpec() != profile {
			t.Fatalf("profile=%s want %s", activeMockSpec(), profile)
		}
	}
	framework.GetStorage().Set(controller.NrcfgKey, &dpmock.NodeResourceConfiguration{})
	if activeMockSpec() != "" {
		t.Fatal("empty configuration activated a profile")
	}
}
