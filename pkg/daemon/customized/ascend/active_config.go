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
	"strings"
	dpmockv1alpha1 "volcano.sh/deviceplugin-mock/api/dpmock/v1alpha1"
	"volcano.sh/deviceplugin-mock/pkg/daemon/controller"
	"volcano.sh/deviceplugin-mock/pkg/daemon/framework"
)

// activeMockSpec centralizes precedence. Mixed Ascend profiles are unsupported;
// retain D950 precedence and the legacy D910 fallback for direct resources.
func activeMockSpec() string {
	nrcfg, ok := activeNodeResourceConfiguration()
	if !ok {
		return ""
	}
	profile := ""
	for _, resource := range nrcfg.Spec.Resources {
		if ref := resource.ResourceRef; ref != nil && strings.HasPrefix(ref.Name, "ascend-") {
			if ref.Name == "ascend-d950" {
				return ref.Name
			}
			profile = ref.Name
		} else if resource.ResourceName == resourceName && profile == "" {
			profile = "ascend-d910"
		}
	}
	return profile
}

func activeNodeResourceConfiguration() (*dpmockv1alpha1.NodeResourceConfiguration, bool) {
	obj, ok := framework.GetStorage().Get(controller.NrcfgKey)
	if !ok {
		return nil, false
	}
	nrcfg, ok := obj.(*dpmockv1alpha1.NodeResourceConfiguration)
	return nrcfg, ok && nrcfg != nil
}
