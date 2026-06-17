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
	dpmockv1alpha1 "volcano.sh/deviceplugin-mock/api/dpmock/v1alpha1"
	"volcano.sh/deviceplugin-mock/pkg/daemon/controller"
	"volcano.sh/deviceplugin-mock/pkg/daemon/framework"
)

func activeConfigReferencesNodeResource(name string) bool {
	nrcfg, ok := activeNodeResourceConfiguration()
	if !ok {
		return false
	}
	for _, resource := range nrcfg.Spec.Resources {
		if resource.ResourceRef != nil && resource.ResourceRef.Name == name {
			return true
		}
	}
	return false
}

func activeNodeResourceConfiguration() (*dpmockv1alpha1.NodeResourceConfiguration, bool) {
	obj, ok := framework.GetStorage().Get(controller.NrcfgKey)
	if !ok {
		return nil, false
	}
	nrcfg, ok := obj.(*dpmockv1alpha1.NodeResourceConfiguration)
	return nrcfg, ok
}
