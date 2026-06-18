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
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	"volcano.sh/deviceplugin-mock/pkg/daemon/framework"
	"volcano.sh/deviceplugin-mock/pkg/daemon/podmonitor"
)

const (
	d950CardCount               = 8
	d950RackCardCount           = 64
	d950RootInfoKey             = "rootinfo.json"
	d950RootInfoConfigMapPrefix = "rootinfo-"
	visibleDevicesKey           = "huawei.com/ascend-visible-devices"
	ascendRealPhyIDKey          = "huawei.com/AscendRealPhyID"
)

var d950 = &AscendD950{}

var d950ConfigActive = func() bool {
	return activeConfigReferencesNodeResource("ascend-d950")
}

var d950LocalIDStart = func(ctx context.Context, kubeClient kubernetes.Interface) (int, error) {
	return currentNodeLocalIDStart(ctx, kubeClient)
}

type AscendD950 struct {
	Enabled    bool
	kubeClient kubernetes.Interface
}

type rootInfo struct {
	Version   string     `json:"version"`
	Status    string     `json:"status"`
	RankCount int        `json:"rank_count"`
	RankList  []rootRank `json:"rank_list"`
}

type rootRank struct {
	DeviceID  int         `json:"device_id"`
	LocalID   int         `json:"local_id"`
	LevelList []rootLevel `json:"level_list"`
}

type rootLevel struct {
	NetLayer      int        `json:"net_layer"`
	NetInstanceID string     `json:"net_instance_id"`
	NetType       string     `json:"net_type"`
	NetAttr       string     `json:"net_attr"`
	RankAddrList  []rankAddr `json:"rank_addr_list"`
}

type rankAddr struct {
	AddrType string   `json:"addr_type"`
	Addr     string   `json:"addr"`
	Ports    []string `json:"ports"`
	PlaneID  string   `json:"plane_id"`
}

func init() {
	framework.RegisterService(d950)
}

func (a *AscendD950) Name() string {
	return "daemon-ascend-d950"
}

func (a *AscendD950) Initialize() error {
	if framework.GetEnvs().PodNamespace == "" {
		return errors.New("env 'POD_NAMESPACE' not set")
	}
	a.Enabled = true
	a.kubeClient = framework.GetClientSet().KubeClient
	return nil
}

func (a *AscendD950) Run(ctx context.Context) error {
	wait.UntilWithContext(ctx, a.syncRootInfoConfigMap, 30*time.Second)
	return framework.ErrContextDone
}

func (a *AscendD950) ModifyPod(pod *v1.Pod, pr *podmonitor.PodResource) error {
	if !a.Enabled {
		return nil
	}
	if !d950ConfigActive() {
		return nil
	}

	deviceIDs, ok := pr.Resources[resourceName]
	if !ok {
		return nil
	}
	ids := make([]string, len(deviceIDs))
	copy(ids, deviceIDs)
	sortDeviceIDs(ids)

	infoJSON, err := buildPodNetworkInfoJsonWithServerID(pr.Name, framework.GetEnvs().NodeIP, ids, true, true)
	if err != nil {
		return fmt.Errorf("failed to build d950 network info for pod '%v': %w", pr.NamespacedName, err)
	}

	idsJoin := strings.Join(ids, ",")
	visibleDevices, err := buildVisibleDevices(ids)
	if err != nil {
		return err
	}
	localIDStart, err := d950LocalIDStart(context.Background(), a.kubeClient)
	if err != nil {
		return fmt.Errorf("failed to resolve d950 local id start for pod '%v': %w", pr.NamespacedName, err)
	}
	physicalDevices, err := buildPhysicalDevices(ids, localIDStart)
	if err != nil {
		return err
	}

	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}
	pod.Annotations[annotationKey] = infoJSON
	pod.Annotations[ascendRealKey] = idsJoin
	pod.Annotations[kltDevKey] = idsJoin
	pod.Annotations[visibleDevicesKey] = visibleDevices
	pod.Annotations[ascendRealPhyIDKey] = physicalDevices

	return nil
}

func (a *AscendD950) syncRootInfoConfigMap(ctx context.Context) {
	if !d950ConfigActive() {
		a.deleteRootInfoConfigMap(ctx)
		return
	}

	localIDStart, err := d950LocalIDStart(ctx, a.kubeClient)
	if err != nil {
		logRootInfoSyncError(err)
		return
	}

	payload, err := buildRootInfoPayload(localIDStart)
	if err != nil {
		logRootInfoSyncError(err)
		return
	}

	name := rootInfoConfigMapName(framework.GetEnvs().NodeIP)
	namespace := framework.GetEnvs().PodNamespace
	desired := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    rootInfoLabels(),
		},
		Data: map[string]string{
			d950RootInfoKey: payload,
		},
	}

	cmClient := a.kubeClient.CoreV1().ConfigMaps(namespace)
	current, err := cmClient.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err = cmClient.Create(ctx, desired, metav1.CreateOptions{}); err != nil {
			logRootInfoSyncError(err)
		}
		return
	}
	if err != nil {
		logRootInfoSyncError(err)
		return
	}

	if equality.Semantic.DeepEqual(current.Data, desired.Data) && equality.Semantic.DeepEqual(current.Labels, desired.Labels) {
		return
	}

	updated := current.DeepCopy()
	updated.Labels = desired.Labels
	updated.Data = desired.Data
	if _, err = cmClient.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
		logRootInfoSyncError(err)
	}
}

func (a *AscendD950) deleteRootInfoConfigMap(ctx context.Context) {
	name := rootInfoConfigMapName(framework.GetEnvs().NodeIP)
	namespace := framework.GetEnvs().PodNamespace
	err := a.kubeClient.CoreV1().ConfigMaps(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return
	}
	if err != nil {
		logRootInfoSyncError(err)
	}
}

func logRootInfoSyncError(err error) {
	// Keep rootinfo failures visible without stopping the daemon service loop.
	if err != nil {
		klog.ErrorS(err, "failed to sync d950 rootinfo ConfigMap")
	}
}

func buildVisibleDevices(deviceIDs []string) (string, error) {
	devices := make([]string, 0, len(deviceIDs))
	for _, id := range deviceIDs {
		idx, err := deviceIndexFromID(id)
		if err != nil {
			return "", err
		}
		devices = append(devices, "Ascend910-"+strconv.Itoa(idx))
	}
	return strings.Join(devices, ","), nil
}

func buildPhysicalDevices(deviceIDs []string, localIDStart int) (string, error) {
	devices := make([]string, 0, len(deviceIDs))
	for _, id := range deviceIDs {
		idx, err := deviceIndexFromID(id)
		if err != nil {
			return "", err
		}
		devices = append(devices, "Ascend910-"+strconv.Itoa(localIDStart+idx))
	}
	return strings.Join(devices, ","), nil
}

func deviceIndexFromID(id string) (int, error) {
	idx, err := strconv.Atoi(strings.TrimPrefix(id, devPrefix))
	if err != nil {
		return 0, fmt.Errorf("failed to parse device id '%s': %w", id, err)
	}
	return idx, nil
}

func rootInfoConfigMapName(nodeIP string) string {
	return d950RootInfoConfigMapPrefix + nodeIP
}

func rootInfoLabels() map[string]string {
	return map[string]string{
		"app.kubernetes.io/component": "daemon",
		"dpmock.volcano.sh/node-name": framework.GetEnvs().NodeName,
		"dpmock.volcano.sh/mock-spec": "ascend-d950",
		"ring-controller.cce":         "ascend-1980",
		"volcano.sh/config-type":      "root-info",
	}
}

func buildRootInfoPayload(localIDStart int) (string, error) {
	info := buildRootInfo(localIDStart)
	if err := validateUniqueRootInfoAddrs(info); err != nil {
		return "", err
	}
	bb, err := json.Marshal(info)
	if err != nil {
		return "", fmt.Errorf("failed to marshal rootinfo: %w", err)
	}
	return string(bb), nil
}

func buildRootInfo(localIDStart int) *rootInfo {
	info := &rootInfo{
		Version:   "2.0",
		Status:    "completed",
		RankCount: d950CardCount,
	}
	for idx := 0; idx < d950CardCount; idx++ {
		localID := localIDStart + idx
		info.RankList = append(info.RankList, rootRank{
			DeviceID: idx,
			LocalID:  localID,
			LevelList: []rootLevel{
				buildTopoFileDescLevel(localID),
				buildClosLevel(localID),
			},
		})
	}
	return info
}

func buildTopoFileDescLevel(cardIdx int) rootLevel {
	level := rootLevel{
		NetLayer:      0,
		NetInstanceID: "superpod1_1",
		NetType:       "TOPO_FILE_DESC",
		NetAttr:       "",
	}
	for port := 0; port <= 8; port++ {
		level.RankAddrList = append(level.RankAddrList, d950RankAddr(cardIdx, port+1, "0/"+strconv.Itoa(port), "plane0", d950IntraNodeAddrPrefix))
	}
	for port := 0; port <= 8; port++ {
		level.RankAddrList = append(level.RankAddrList, d950RankAddr(cardIdx, port+0x0a, "1/"+strconv.Itoa(port), "plane1", d950IntraNodeAddrPrefix))
	}
	level.RankAddrList = append(level.RankAddrList,
		d950RankAddr(cardIdx, 0xb5, "0/1,0/2", "plane0", d950IntraNodeAddrPrefix),
		d950RankAddr(cardIdx, 0xb7, "1/1,1/2", "plane1", d950IntraNodeAddrPrefix),
	)
	return level
}

func buildClosLevel(cardIdx int) rootLevel {
	return rootLevel{
		NetLayer:      1,
		NetInstanceID: "superpod1",
		NetType:       "CLOS",
		NetAttr:       "",
		RankAddrList: []rankAddr{
			d950RankAddr(cardIdx, 0xb6, "0/1,0/2", "plane0", d950InterNodeAddrPrefix),
			d950RankAddr(cardIdx, 0xb8, "1/1,1/2", "plane1", d950InterNodeAddrPrefix),
		},
	}
}

const (
	d950IntraNodeAddrPrefix = "000000000000002000100000df"
	d950InterNodeAddrPrefix = "000000000000000000100000df"
)

func d950RankAddr(cardIdx, offset int, ports, planeID, prefix string) rankAddr {
	return rankAddr{
		AddrType: "EID",
		Addr:     fmt.Sprintf("%s%06x", prefix, 0x1000+cardIdx*0x100+offset),
		Ports:    strings.Split(ports, ","),
		PlaneID:  planeID,
	}
}

func validateUniqueRootInfoAddrs(info *rootInfo) error {
	seen := make(map[string]struct{})
	for _, rank := range info.RankList {
		for _, level := range rank.LevelList {
			for _, addr := range level.RankAddrList {
				if _, ok := seen[addr.Addr]; ok {
					return fmt.Errorf("duplicate rootinfo addr %s", addr.Addr)
				}
				seen[addr.Addr] = struct{}{}
			}
		}
	}
	return nil
}

func currentNodeLocalIDStart(ctx context.Context, kubeClient kubernetes.Interface) (int, error) {
	if kubeClient == nil {
		return 0, errors.New("kube client is nil")
	}

	nodes, err := kubeClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, fmt.Errorf("failed to list nodes: %w", err)
	}

	nodeName := framework.GetEnvs().NodeName
	preferred := filterWorkerNodeNames(nodes.Items)
	idx := indexOfNode(preferred, nodeName)
	if idx < 0 {
		all := nodeNames(nodes.Items)
		idx = indexOfNode(all, nodeName)
	}
	if idx < 0 {
		return 0, fmt.Errorf("node %q not found", nodeName)
	}

	return (idx * d950CardCount) % d950RackCardCount, nil
}

func filterWorkerNodeNames(nodes []v1.Node) []string {
	names := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if _, ok := node.Labels["node-role.kubernetes.io/control-plane"]; ok {
			continue
		}
		if _, ok := node.Labels["node-role.kubernetes.io/master"]; ok {
			continue
		}
		names = append(names, node.Name)
	}
	sort.Strings(names)
	return names
}

func nodeNames(nodes []v1.Node) []string {
	names := make([]string, 0, len(nodes))
	for _, node := range nodes {
		names = append(names, node.Name)
	}
	sort.Strings(names)
	return names
}

func indexOfNode(names []string, nodeName string) int {
	for idx, name := range names {
		if name == nodeName {
			return idx
		}
	}
	return -1
}
