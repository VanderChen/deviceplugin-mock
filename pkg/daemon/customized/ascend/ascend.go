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
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	corev1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/klog/v2"

	"volcano.sh/deviceplugin-mock/pkg/daemon/framework"
	"volcano.sh/deviceplugin-mock/pkg/daemon/podmonitor"
)

type AscendService struct {
	kubeClient       kubernetes.Interface
	podLister        corev1.PodLister
	podModifyMethods map[string]func(pod *v1.Pod, pr *podmonitor.PodResource) error
}

func init() {
	ascend := &AscendService{
		podModifyMethods: map[string]func(pod *v1.Pod, pr *podmonitor.PodResource) error{
			common.Name(): common.ModifyPod,
			d910.Name():   d910.ModifyPod,
			d950.Name():   d950.ModifyPod,
		},
	}
	framework.RegisterService(ascend)
}

func (a *AscendService) Name() string {
	return "daemon-ascend"
}

func (a *AscendService) Initialize() error {
	a.kubeClient = framework.GetClientSet().KubeClient
	a.podLister = framework.GetClientSet().KubeInformerFactory.Core().V1().Pods().Lister()

	return nil
}

func (a *AscendService) Run(ctx context.Context) error {
	wait.UntilWithContext(ctx, a.podDeviceInfoHandler, 2*time.Second)

	return framework.ErrContextDone
}

func (a *AscendService) podDeviceInfoHandler(ctx context.Context) {
	obj, ok := framework.GetStorage().Get(podmonitor.PodResourcesKey)
	if !ok {
		klog.V(4).Info("pod resources not found")
		return
	}
	prs, ok := obj.(map[string]*podmonitor.PodResource)
	if !ok {
		klog.Errorf("pod resources type error, got type %T", obj)
		return
	}

	for _, pr := range prs {
		select {
		case <-ctx.Done():
			return
		default:
		}

		pod, err := a.podLister.Pods(pr.Namespace).Get(pr.Name)
		if err != nil {
			klog.ErrorS(err, "pod not found for pod resource", "pod", pr.NamespacedName)
			continue
		}

		if isPodTerminated(pod) {
			klog.V(4).InfoS("ignore terminated pod", "pod", klog.KObj(pod))
			continue
		}

		podModified := a.modifyPod(pod, pr)
		if equality.Semantic.DeepEqual(podModified, pod) {
			klog.V(5).InfoS("pod does not need to be updated", "pod", klog.KObj(pod))
			continue
		}

		_, err = a.kubeClient.CoreV1().Pods(podModified.Namespace).Update(ctx, podModified, metav1.UpdateOptions{})
		if err != nil {
			klog.ErrorS(err, "failed to update pod", "pod", klog.KObj(podModified))
			continue
		}

		klog.V(3).InfoS("pod updated", "pod", klog.KObj(podModified))
	}
}

func (a *AscendService) modifyPod(pod *v1.Pod, pr *podmonitor.PodResource) *v1.Pod {
	newPod := pod.DeepCopy()
	for name, f := range a.podModifyMethods {
		err := f(newPod, pr)
		if err != nil {
			klog.ErrorS(err, "failed to modify pod", "method", name, "pod", klog.KObj(pod))
			continue
		}
	}
	return newPod
}

func isPodTerminated(pod *v1.Pod) bool {
	return pod.DeletionTimestamp != nil || pod.Status.Phase == v1.PodSucceeded || pod.Status.Phase == v1.PodFailed
}
