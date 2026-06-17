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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"volcano.sh/deviceplugin-mock/pkg/daemon/framework"
	"volcano.sh/deviceplugin-mock/pkg/daemon/podmonitor"
)

func TestAscendD950ModifyPod(t *testing.T) {
	framework.GetEnvs().NodeIP = "192.168.7.100"
	d950.Enabled = true
	oldD950ConfigActive := d950ConfigActive
	d950ConfigActive = func() bool { return true }
	defer func() {
		d950.Enabled = false
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

	wantNetworkInfo := `{"pod_name":"deployment1-6c49f95d74-kswdl","server_id":"192.168.7.100","devices":[{"device_id":"0","super_device_id":"10000","device_ip":"100.0.0.0","tor_ip":"200.0.0.0","tor_port":"8888"},{"device_id":"1","super_device_id":"10000","device_ip":"100.0.0.1","tor_ip":"200.0.0.1","tor_port":"8888"}]}`
	if got := pod.Annotations[annotationKey]; got != wantNetworkInfo {
		t.Fatalf("annotation %s = %q, want %q", annotationKey, got, wantNetworkInfo)
	}
}

func TestBuildRootInfoPayload(t *testing.T) {
	payload, err := buildRootInfoPayload()
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
	if err = validateUniqueRootInfoAddrs(info); err != nil {
		t.Fatalf("validateUniqueRootInfoAddrs() error = %v", err)
	}
}

func TestAscendD950SyncRootInfoConfigMap(t *testing.T) {
	framework.GetEnvs().NodeName = "kind-worker"
	framework.GetEnvs().NodeIP = "172.18.0.2"
	framework.GetEnvs().PodNamespace = "volcano-system"

	client := fake.NewSimpleClientset()
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
	if cm.Data[d950RootInfoKey] == "" {
		t.Fatalf("ConfigMap data %q is empty", d950RootInfoKey)
	}
}
