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

package resmanager

import (
	"encoding/json"
	"testing"
)

func TestBuildNodeStatusResourceRemovedPatch(t *testing.T) {
	patch, err := buildNodeStatusResourceRemovedPatch("huawei.com/ascend-1980")
	if err != nil {
		t.Fatalf("buildNodeStatusResourceRemovedPatch() error = %v", err)
	}

	got := map[string]map[string]map[string]*string{}
	if err = json.Unmarshal(patch, &got); err != nil {
		t.Fatalf("failed to unmarshal patch: %v", err)
	}
	if got["status"]["capacity"]["huawei.com/ascend-1980"] != nil {
		t.Fatalf("capacity cleanup value is not null")
	}
	if got["status"]["allocatable"]["huawei.com/ascend-1980"] != nil {
		t.Fatalf("allocatable cleanup value is not null")
	}
}
