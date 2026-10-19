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
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"net/netip"
	"strconv"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
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
	d950RootInfoKey             = "hccl_rootinfo.json"
	d950RootInfoConfigMapPrefix = "root-info-"
	visibleDevicesKey           = "huawei.com/ascend-visible-devices"
	ascendRealPhyIDKey          = "huawei.com/AscendRealPhyID"
)

var d950 = &AscendD950{}

var d950ConfigActive = func() bool {
	return activeMockSpec() == "ascend-d950"
}

type AscendD950 struct {
	Enabled         bool
	kubeClient      kubernetes.Interface
	cleanupIdentity string
	cleanupPending  bool
	inactiveClean   bool
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
	a.Enabled = false
	a.inactiveClean = false
	a.cleanupIdentity = ""
	if framework.GetEnvs().PodNamespace == "" {
		klog.Warning("POD_NAMESPACE is unset; disabling optional D950 service")
		return nil
	}
	if framework.GetEnvs().PodName == "" || framework.GetEnvs().PodUID == "" {
		klog.Warning("POD_NAME/POD_UID are unset; D950 will use label-based cleanup without a Pod ownerReference")
	}
	a.Enabled = true
	a.kubeClient = framework.GetClientSet().KubeClient
	return nil
}

func (a *AscendD950) Run(ctx context.Context) error {
	if !a.Enabled {
		<-ctx.Done()
		return framework.ErrContextDone
	}
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
		return fmt.Errorf("failed to build visible devices for pod '%v' (device IDs %v): %w", pr.NamespacedName, ids, err)
	}
	localIDStart := d950LocalIDStartFromNodeName(framework.GetEnvs().NodeName)
	physicalDevices, err := buildPhysicalDevices(ids, localIDStart)
	if err != nil {
		return fmt.Errorf("failed to build physical devices for pod '%v' (device IDs %v): %w", pr.NamespacedName, ids, err)
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
	if !a.Enabled {
		return
	}
	if err := a.reconcileRootInfo(ctx); err != nil {
		envs := framework.GetEnvs()
		klog.ErrorS(err, "failed to sync d950 rootinfo ConfigMap", "namespace", envs.PodNamespace, "node", envs.NodeName, "nodeIP", envs.NodeIP)
	}
}

func (a *AscendD950) reconcileRootInfo(ctx context.Context) error {
	// The services start concurrently. An unread configuration is not an
	// inactive configuration and must not trigger deletion during startup.
	if _, ready := activeNodeResourceConfiguration(); !ready {
		return nil
	}
	envs := framework.GetEnvs()
	name := rootInfoConfigMapName(envs.NodeIP)
	if name == "" {
		return fmt.Errorf("invalid node IP %q", envs.NodeIP)
	}
	identity := envs.PodNamespace + "/" + name + "/" + envs.PodUID + "/" + envs.Instance
	if identity != a.cleanupIdentity {
		a.cleanupIdentity = identity
		a.cleanupPending = true
		a.inactiveClean = false
	}
	if !d950ConfigActive() {
		if a.inactiveClean {
			return nil
		}
		if err := a.deleteRootInfoConfigMaps(ctx, ""); err != nil {
			return err
		}
		a.inactiveClean = true
		a.cleanupPending = true
		return nil
	}
	a.inactiveClean = false
	payload, err := buildRootInfoPayload(d950LocalIDStartFromNodeName(envs.NodeName))
	if err != nil {
		return err
	}
	desired := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: envs.PodNamespace,
			Labels:    rootInfoLabels(),
		},
		Data: map[string]string{
			d950RootInfoKey: payload,
		},
	}
	if envs.PodName != "" && envs.PodUID != "" {
		controller := true
		desired.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: envs.PodName, UID: types.UID(envs.PodUID), Controller: &controller}}
	}
	cmClient := a.kubeClient.CoreV1().ConfigMaps(envs.PodNamespace)
	current, err := cmClient.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err = cmClient.Create(ctx, desired, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create %s: %w", name, err)
		}
	} else {
		if err != nil {
			return fmt.Errorf("get %s: %w", name, err)
		}
		if !ownsRootInfo(current) {
			return fmt.Errorf("refusing to update ConfigMap %s with a different owner", name)
		}
		updated := current.DeepCopy()
		for key, value := range desired.Labels {
			updated.Labels[key] = value
		}
		if updated.Data == nil {
			updated.Data = make(map[string]string)
		}
		updated.Data[d950RootInfoKey] = payload
		if len(desired.OwnerReferences) > 0 {
			refs := updated.OwnerReferences[:0]
			for _, ref := range updated.OwnerReferences {
				if ref.Controller == nil || !*ref.Controller {
					refs = append(refs, ref)
				}
			}
			updated.OwnerReferences = append(refs, desired.OwnerReferences...)
		}
		if !equality.Semantic.DeepEqual(current, updated) {
			if _, err := cmClient.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
				return fmt.Errorf("update %s: %w", name, err)
			}
		}
	}
	// Reconcile the desired object before removing historical node-IP names.
	if a.cleanupPending {
		if err := a.deleteRootInfoConfigMaps(ctx, name); err != nil {
			return err
		}
		a.cleanupPending = false
	}
	return nil
}

func (a *AscendD950) deleteRootInfoConfigMaps(ctx context.Context, keep string) error {
	selector := labels.Set{
		"app.kubernetes.io/component": "daemon",
		"dpmock.volcano.sh/node-name": framework.GetEnvs().NodeName,
		"dpmock.volcano.sh/mock-spec": "ascend-d950",
	}.AsSelector().String()
	client := a.kubeClient.CoreV1().ConfigMaps(framework.GetEnvs().PodNamespace)
	list, err := client.List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return fmt.Errorf("list historical rootinfo ConfigMaps: %w", err)
	}
	for i := range list.Items {
		cm := &list.Items[i]
		if cm.Name == keep || !ownsRootInfo(cm) || !strings.HasPrefix(cm.Name, d950RootInfoConfigMapPrefix) {
			continue
		}
		uid := cm.UID
		if err := client.Delete(ctx, cm.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete historical ConfigMap %s: %w", cm.Name, err)
		}
	}
	return nil
}

func ownsRootInfo(cm *v1.ConfigMap) bool {
	envs := framework.GetEnvs()
	if cm.Labels["app.kubernetes.io/component"] != "daemon" || cm.Labels["dpmock.volcano.sh/node-name"] != envs.NodeName || cm.Labels["dpmock.volcano.sh/mock-spec"] != "ascend-d950" {
		return false
	}
	if instance := cm.Labels["app.kubernetes.io/instance"]; instance != "" && instance != envs.Instance {
		return false
	}
	owner := metav1.GetControllerOf(cm)
	return owner == nil || (owner.APIVersion == "v1" && owner.Kind == "Pod" && (cm.Labels["app.kubernetes.io/instance"] != "" || owner.UID == types.UID(envs.PodUID)))
}

func d950LocalIDStartFromNodeName(nodeName string) int {
	// Mock-only local segments may repeat across nodes. Do not reintroduce a
	// cluster-wide Node List on the per-pod annotation path to make them unique.
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(nodeName))
	segmentCount := d950RackCardCount / d950CardCount
	return int(hasher.Sum32()%uint32(segmentCount)) * d950CardCount
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
	addr, err := netip.ParseAddr(nodeIP)
	if err != nil || addr.Zone() != "" {
		return ""
	}
	addr = addr.Unmap()
	if addr.Is4() {
		return d950RootInfoConfigMapPrefix + addr.String()
	}
	bb := addr.As16()
	return d950RootInfoConfigMapPrefix + "v6-" + hex.EncodeToString(bb[:])
}

func rootInfoLabels() map[string]string {
	result := map[string]string{
		"app.kubernetes.io/component": "daemon",
		"dpmock.volcano.sh/node-name": framework.GetEnvs().NodeName,
		"dpmock.volcano.sh/mock-spec": "ascend-d950",
		"ring-controller.cce":         "ascend-1980",
		"volcano.sh/config-type":      "root-info",
	}
	if instance := framework.GetEnvs().Instance; instance != "" {
		result["app.kubernetes.io/instance"] = instance
	}
	return result
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
