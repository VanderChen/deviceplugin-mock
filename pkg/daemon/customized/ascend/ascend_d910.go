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
	"fmt"
	"sort"
	"strconv"
	"strings"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/json"

	dpmockv1alpha1 "volcano.sh/deviceplugin-mock/api/dpmock/v1alpha1"
	"volcano.sh/deviceplugin-mock/pkg/daemon/controller"
	"volcano.sh/deviceplugin-mock/pkg/daemon/framework"
	"volcano.sh/deviceplugin-mock/pkg/daemon/podmonitor"
)

type DeviceNetworkInfo struct {
	DeviceID      string `json:"device_id,omitempty"`
	SuperDeviceID string `json:"super_device_id,omitempty"`
	DeviceIP      string `json:"device_ip,omitempty"`
	TorIP         string `json:"tor_ip,omitempty"`
	TorPort       string `json:"tor_port,omitempty"`
}

type PodNetworkInfo struct {
	PodName  string              `json:"pod_name,omitempty"`
	ServerID string              `json:"server_id,omitempty"`
	Devices  []DeviceNetworkInfo `json:"devices,omitempty"`
}

type AscendD910 struct {
	Enabled bool
}

const (
	resourceName  = "huawei.com/ascend-1980"
	devPrefix     = "davinci"
	annotationKey = "cce.kubectl.kubernetes.io/ascend-1980-configuration"
	roceEnableKey = "dpmock.volcano.sh/roce-enable"
)

var d910 = &AscendD910{
	Enabled: false,
}

func init() {
	framework.RegisterService(d910)
}

func (a *AscendD910) Name() string {
	return "daemon-ascend-d910"
}

func (a *AscendD910) Initialize() error {
	a.Enabled = true
	return nil
}

func (a *AscendD910) Run(ctx context.Context) error {
	<-ctx.Done()
	return framework.ErrContextDone
}

func (a *AscendD910) ModifyPod(pod *v1.Pod, pr *podmonitor.PodResource) error {
	if !a.Enabled {
		return nil
	}
	if activeConfigReferencesNodeResource("ascend-d950") {
		return nil
	}

	deviceIDs, ok := pr.Resources[resourceName]
	if !ok {
		return nil
	}

	obj, ok := framework.GetStorage().Get(controller.NrcfgKey)
	if !ok {
		return errors.New("nrcfg not found in storage")
	}
	nrcfg, ok := obj.(*dpmockv1alpha1.NodeResourceConfiguration)
	if !ok {
		return fmt.Errorf("nrcfg type error, got type %T", obj)
	}

	infoJson, err := buildPodNetworkInfoJson(pr.Name, deviceIDs, requiredDeviceIP(nrcfg), requiredSuperDeviceID(nrcfg))
	if err != nil {
		return fmt.Errorf("failed to build network info for pod '%v': %w", pr.NamespacedName, err)
	}

	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}
	pod.Annotations[annotationKey] = infoJson

	return nil
}

func buildPodNetworkInfoJson(podName string, deviceIDs []string, requiredDeviceIP, requiredSuperDeviceID bool) (string, error) {
	return buildPodNetworkInfoJsonWithServerID(podName, framework.GetEnvs().NodeName, deviceIDs, requiredDeviceIP, requiredSuperDeviceID)
}

func buildPodNetworkInfoJsonWithServerID(podName, serverID string, deviceIDs []string, requiredDeviceIP, requiredSuperDeviceID bool) (string, error) {
	info := &PodNetworkInfo{
		PodName:  podName,
		ServerID: serverID,
	}
	for _, id := range deviceIDs {
		idx, err := strconv.Atoi(strings.TrimPrefix(id, devPrefix))
		if err != nil {
			return "", fmt.Errorf("failed to parse device id '%s': %w", id, err)
		}
		networkInfo := DeviceNetworkInfo{
			DeviceID: strconv.Itoa(idx),
			TorIP:    getDeviceNetworkIP(200, idx),
			TorPort:  "8888",
		}
		if requiredDeviceIP {
			networkInfo.DeviceIP = getDeviceNetworkIP(100, idx)
		}
		if requiredSuperDeviceID {
			networkInfo.SuperDeviceID = strconv.Itoa(10000 + idx/4)
		}
		info.Devices = append(info.Devices, networkInfo)
	}
	sort.Slice(info.Devices, func(i, j int) bool {
		iIdx, iErr := strconv.Atoi(info.Devices[i].DeviceID)
		jIdx, jErr := strconv.Atoi(info.Devices[j].DeviceID)
		if iErr == nil && jErr == nil {
			return iIdx < jIdx
		}
		return info.Devices[i].DeviceID < info.Devices[j].DeviceID
	})

	bb, err := json.Marshal(info)
	if err != nil {
		return "", fmt.Errorf("failed to marshal PodNetworkInfo: %w", err)
	}
	return string(bb), nil
}

func requiredSuperDeviceID(nrcfg *dpmockv1alpha1.NodeResourceConfiguration) bool {
	for _, resource := range nrcfg.Spec.Resources {
		if resource.ResourceRef != nil && resource.ResourceRef.Name == "ascend-d910c" {
			return true
		}
	}
	return false
}

func requiredDeviceIP(nrcfg *dpmockv1alpha1.NodeResourceConfiguration) bool {
	if nrcfg.Annotations != nil && nrcfg.Annotations[roceEnableKey] == "false" {
		return false
	}
	return true
}

func getDeviceNetworkIP(subnet, idx int) string {
	var ip [4]int
	ip[0] = subnet
	for i, tmp := 3, idx; i > 0 && tmp > 0; i, tmp = i-1, tmp/256 {
		ip[i] = tmp % 256
	}
	return fmt.Sprintf("%d.%d.%d.%d", ip[0], ip[1], ip[2], ip[3])
}
