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
	"context"
	"errors"
	"testing"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestCleanupChecksBeforePatch(t *testing.T) {
	for _, present := range []bool{false, true} {
		node := &v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker"}}
		if present {
			node.Status.Allocatable = v1.ResourceList{"example.com/mock": resource.MustParse("2")}
		}
		client := fake.NewSimpleClientset(node)
		absent, err := CleanupNodeStatusResourceWithClient(context.Background(), client, "worker", "example.com/mock")
		if err != nil || !absent {
			t.Fatalf("absent=%v error=%v", absent, err)
		}
		patches := 0
		for _, a := range client.Actions() {
			if a.GetVerb() == "patch" {
				patches++
			}
		}
		if (!present && patches != 0) || (present && patches != 1) {
			t.Fatalf("present=%v patches=%d", present, patches)
		}
	}
}

func TestCleanupDoesNotSwallowReadOrWriteErrors(t *testing.T) {
	for _, verb := range []string{"get", "patch"} {
		client := fake.NewSimpleClientset(&v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker"}, Status: v1.NodeStatus{Capacity: v1.ResourceList{"example.com/mock": resource.MustParse("2")}}})
		client.PrependReactor(verb, "nodes", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New("API failure") })
		if absent, err := CleanupNodeStatusResourceWithClient(context.Background(), client, "worker", "example.com/mock"); err == nil || absent {
			t.Fatalf("%s error hidden", verb)
		}
	}
}
