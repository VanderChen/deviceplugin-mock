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
	"encoding/json"
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"volcano.sh/deviceplugin-mock/pkg/daemon/framework"
	"volcano.sh/deviceplugin-mock/pkg/daemon/podmonitor"
)

func TestAscendD950ModifyPod(t *testing.T) {
	framework.GetEnvs().NodeName = "kind-worker2"
	framework.GetEnvs().NodeIP = "192.168.7.100"
	d950.Enabled = true
	d950.kubeClient = fake.NewSimpleClientset(
		node("kind-control-plane", true),
		node("kind-worker", false),
		node("kind-worker2", false),
	)
	oldD950ConfigActive := d950ConfigActive
	d950ConfigActive = func() bool { return true }
	defer func() {
		d950.Enabled = false
		d950.kubeClient = nil
		d950ConfigActive = oldD950ConfigActive
	}()

	pod := &v1.Pod{}
	pr := &podmonitor.PodResource{
		NamespacedName: types.NamespacedName{Name: "deployment1-6c49f95d74-kswdl", Namespace: "default"},
		Resources: map[string][]string{
			resourceName: {"davinci1", "davinci0"},
		},
	}

	if err := d950.ModifyPod(pod, pr); err != nil {
		t.Fatalf("ModifyPod() error = %v", err)
	}

	if got, want := pod.Annotations[ascendRealKey], "davinci0,davinci1"; got != want {
		t.Fatalf("annotation %s = %q, want %q", ascendRealKey, got, want)
	}
	if got, want := pod.Annotations[kltDevKey], "davinci0,davinci1"; got != want {
		t.Fatalf("annotation %s = %q, want %q", kltDevKey, got, want)
	}
	if got, want := pod.Annotations[visibleDevicesKey], "Ascend910-0,Ascend910-1"; got != want {
		t.Fatalf("annotation %s = %q, want %q", visibleDevicesKey, got, want)
	}
	localIDStart := d950LocalIDStartFromNodeName(framework.GetEnvs().NodeName)
	wantPhysicalDevices, err := buildPhysicalDevices([]string{"davinci0", "davinci1"}, localIDStart)
	if err != nil {
		t.Fatalf("buildPhysicalDevices() error = %v", err)
	}
	if got, want := pod.Annotations[ascendRealPhyIDKey], wantPhysicalDevices; got != want {
		t.Fatalf("annotation %s = %q, want %q", ascendRealPhyIDKey, got, want)
	}

	wantNetworkInfo := `{"pod_name":"deployment1-6c49f95d74-kswdl","server_id":"192.168.7.100","devices":[{"device_id":"0","super_device_id":"10000","device_ip":"100.0.0.0","tor_ip":"200.0.0.0","tor_port":"8888"},{"device_id":"1","super_device_id":"10000","device_ip":"100.0.0.1","tor_ip":"200.0.0.1","tor_port":"8888"}]}`
	if got := pod.Annotations[annotationKey]; got != wantNetworkInfo {
		t.Fatalf("annotation %s = %q, want %q", annotationKey, got, wantNetworkInfo)
	}
}

func TestAscendD950DoesNotListNodesForPodAnnotations(t *testing.T) {
	framework.GetEnvs().NodeName = "kind-worker2"
	framework.GetEnvs().NodeIP = "192.168.7.100"
	client := fake.NewSimpleClientset(
		node("kind-control-plane", true),
		node("kind-worker", false),
		node("kind-worker2", false),
	)
	nodeListCount := 0
	client.Fake.PrependReactor("list", "nodes", func(_ k8stesting.Action) (bool, runtime.Object, error) {
		nodeListCount++
		return false, nil, nil
	})
	svc := &AscendD950{Enabled: true, kubeClient: client}
	oldD950ConfigActive := d950ConfigActive
	d950ConfigActive = func() bool { return true }
	defer func() {
		d950ConfigActive = oldD950ConfigActive
	}()

	for _, name := range []string{"pod-1", "pod-2"} {
		pod := &v1.Pod{}
		pr := &podmonitor.PodResource{
			NamespacedName: types.NamespacedName{Name: name, Namespace: "default"},
			Resources: map[string][]string{
				resourceName: {"davinci0", "davinci1"},
			},
		}
		if err := svc.ModifyPod(pod, pr); err != nil {
			t.Fatalf("ModifyPod(%s) error = %v", name, err)
		}
		localIDStart := d950LocalIDStartFromNodeName(framework.GetEnvs().NodeName)
		wantPhysicalDevices, err := buildPhysicalDevices([]string{"davinci0", "davinci1"}, localIDStart)
		if err != nil {
			t.Fatalf("buildPhysicalDevices() error = %v", err)
		}
		if got, want := pod.Annotations[ascendRealPhyIDKey], wantPhysicalDevices; got != want {
			t.Fatalf("annotation %s = %q, want %q", ascendRealPhyIDKey, got, want)
		}
	}

	if want := 0; nodeListCount != want {
		t.Fatalf("node list count = %d, want %d", nodeListCount, want)
	}
}

func TestBuildRootInfoPayload(t *testing.T) {
	payload, err := buildRootInfoPayload(16)
	if err != nil {
		t.Fatalf("buildRootInfoPayload() error = %v", err)
	}

	info := &rootInfo{}
	if err = json.Unmarshal([]byte(payload), info); err != nil {
		t.Fatalf("failed to unmarshal rootinfo payload: %v", err)
	}
	if got, want := info.Version, "2.0"; got != want {
		t.Fatalf("version = %q, want %q", got, want)
	}
	if got, want := info.Status, "completed"; got != want {
		t.Fatalf("status = %q, want %q", got, want)
	}
	if got, want := info.RankCount, d950CardCount; got != want {
		t.Fatalf("rank_count = %d, want %d", got, want)
	}
	if got, want := len(info.RankList), d950CardCount; got != want {
		t.Fatalf("rank_list length = %d, want %d", got, want)
	}
	for idx, rank := range info.RankList {
		if got, want := rank.DeviceID, idx; got != want {
			t.Fatalf("rank[%d].device_id = %d, want %d", idx, got, want)
		}
		if got, want := rank.LocalID, 16+idx; got != want {
			t.Fatalf("rank[%d].local_id = %d, want %d", idx, got, want)
		}
	}
	if err = validateUniqueRootInfoAddrs(info); err != nil {
		t.Fatalf("validateUniqueRootInfoAddrs() error = %v", err)
	}
}

func TestAscendD950SyncRootInfoConfigMap(t *testing.T) {
	framework.GetEnvs().NodeName = "kind-worker"
	framework.GetEnvs().NodeIP = "172.18.0.2"
	framework.GetEnvs().PodNamespace = "volcano-system"

	client := fake.NewSimpleClientset(
		node("kind-control-plane", true),
		node("kind-worker", false),
		node("kind-worker2", false),
	)
	svc := &AscendD950{Enabled: true, kubeClient: client}
	oldD950ConfigActive := d950ConfigActive
	d950ConfigActive = func() bool { return true }
	defer func() {
		d950ConfigActive = oldD950ConfigActive
	}()
	svc.syncRootInfoConfigMap(context.Background())

	cm, err := client.CoreV1().ConfigMaps("volcano-system").Get(context.Background(), "rootinfo-172.18.0.2", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get rootinfo ConfigMap: %v", err)
	}
	if got, want := cm.Labels["dpmock.volcano.sh/node-name"], "kind-worker"; got != want {
		t.Fatalf("node label = %q, want %q", got, want)
	}
	if got, want := cm.Labels["dpmock.volcano.sh/mock-spec"], "ascend-d950"; got != want {
		t.Fatalf("mock-spec label = %q, want %q", got, want)
	}
	if got, want := cm.Labels["ring-controller.cce"], "ascend-1980"; got != want {
		t.Fatalf("ring-controller label = %q, want %q", got, want)
	}
	if got, want := cm.Labels["volcano.sh/config-type"], "root-info"; got != want {
		t.Fatalf("config-type label = %q, want %q", got, want)
	}
	if cm.Data[d950RootInfoKey] == "" {
		t.Fatalf("ConfigMap data %q is empty", d950RootInfoKey)
	}

	info := &rootInfo{}
	if err = json.Unmarshal([]byte(cm.Data[d950RootInfoKey]), info); err != nil {
		t.Fatalf("failed to unmarshal rootinfo payload: %v", err)
	}
	if got, want := info.RankList[0].DeviceID, 0; got != want {
		t.Fatalf("rank[0].device_id = %d, want %d", got, want)
	}
	localIDStart := d950LocalIDStartFromNodeName(framework.GetEnvs().NodeName)
	if got, want := info.RankList[0].LocalID, localIDStart; got != want {
		t.Fatalf("rank[0].local_id = %d, want %d", got, want)
	}
	if got, want := info.RankList[7].LocalID, localIDStart+7; got != want {
		t.Fatalf("rank[7].local_id = %d, want %d", got, want)
	}
}

func TestAscendD950DeletesRootInfoConfigMapWhenInactive(t *testing.T) {
	framework.GetEnvs().NodeName = "kind-worker"
	framework.GetEnvs().NodeIP = "172.18.0.2"
	framework.GetEnvs().PodNamespace = "volcano-system"

	client := fake.NewSimpleClientset(&v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "rootinfo-172.18.0.2",
			Namespace: "volcano-system",
		},
		Data: map[string]string{
			d950RootInfoKey: "{}",
		},
	})
	svc := &AscendD950{Enabled: true, kubeClient: client}
	oldD950ConfigActive := d950ConfigActive
	d950ConfigActive = func() bool { return false }
	defer func() {
		d950ConfigActive = oldD950ConfigActive
	}()

	svc.syncRootInfoConfigMap(context.Background())

	_, err := client.CoreV1().ConfigMaps("volcano-system").Get(context.Background(), "rootinfo-172.18.0.2", metav1.GetOptions{})
	if err == nil {
		t.Fatalf("rootinfo ConfigMap still exists after inactive D950 sync")
	}
}

func TestD950LocalIDStartFromNodeName(t *testing.T) {
	for _, name := range []string{"kind-worker", "kind-worker2", "node-08", ""} {
		got := d950LocalIDStartFromNodeName(name)
		if got < 0 || got >= d950RackCardCount {
			t.Fatalf("d950LocalIDStartFromNodeName(%q) = %d, want in [0,%d)", name, got, d950RackCardCount)
		}
		if got%d950CardCount != 0 {
			t.Fatalf("d950LocalIDStartFromNodeName(%q) = %d, want multiple of %d", name, got, d950CardCount)
		}
		if again := d950LocalIDStartFromNodeName(name); again != got {
			t.Fatalf("d950LocalIDStartFromNodeName(%q) is not stable: %d then %d", name, got, again)
		}
	}
}

func node(name string, controlPlane bool) *v1.Node {
	labels := map[string]string{}
	if controlPlane {
		labels["node-role.kubernetes.io/control-plane"] = ""
	}
	return &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
	}
}
