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
	"sort"
	"strconv"
	"strings"

	v1 "k8s.io/api/core/v1"

	"volcano.sh/deviceplugin-mock/pkg/daemon/framework"
	"volcano.sh/deviceplugin-mock/pkg/daemon/podmonitor"
)

type AscendCommon struct {
	Enabled       bool
	resourceNames []string
}

const (
	ascendRealKey = "huawei.com/AscendReal"
	kltDevKey     = "huawei.com/kltDev"
)

var common = &AscendCommon{
	Enabled:       false,
	resourceNames: []string{"huawei.com/ascend-1980", "huawei.com/ascend-310"},
}

func init() {
	framework.RegisterService(common)
}

func (a *AscendCommon) Name() string {
	return "daemon-ascend-common"
}

func (a *AscendCommon) Initialize() error {
	a.Enabled = true
	return nil
}

func (a *AscendCommon) Run(ctx context.Context) error {
	<-ctx.Done()
	return framework.ErrContextDone
}

func (a *AscendCommon) ModifyPod(pod *v1.Pod, pr *podmonitor.PodResource) error {
	if !a.Enabled {
		return nil
	}

	deviceIDs, ok := a.getDeviceIDs(pr.Resources)
	if !ok {
		return nil
	}
	idsJoin := strings.Join(deviceIDs, ",")

	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}
	pod.Annotations[ascendRealKey] = idsJoin
	pod.Annotations[kltDevKey] = idsJoin

	return nil
}

func (a *AscendCommon) getDeviceIDs(resources map[string][]string) ([]string, bool) {
	for _, r := range a.resourceNames {
		deviceIDs, ok := resources[r]
		if ok {
			ids := make([]string, len(deviceIDs))
			copy(ids, deviceIDs)
			sortDeviceIDs(ids)
			return ids, true
		}
	}
	return nil, false
}

func sortDeviceIDs(ids []string) {
	sort.Slice(ids, func(i, j int) bool {
		iIdx, iErr := strconv.Atoi(strings.TrimPrefix(ids[i], devPrefix))
		jIdx, jErr := strconv.Atoi(strings.TrimPrefix(ids[j], devPrefix))
		if iErr == nil && jErr == nil {
			return iIdx < jIdx
		}
		return ids[i] < ids[j]
	})
}
