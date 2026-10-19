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

package controller

import (
	"context"
	"maps"
	"sync/atomic"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	corev1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	dpmockv1alpha1 "volcano.sh/deviceplugin-mock/client-go/listers/dpmock/v1alpha1"
	"volcano.sh/deviceplugin-mock/pkg/daemon/framework"
	"volcano.sh/deviceplugin-mock/pkg/daemon/resmanager"
)

type Controller struct {
	nodeLister  corev1.NodeLister
	nrcfgLister dpmockv1alpha1.NodeResourceConfigurationLister

	synchronized   atomic.Bool
	managers       map[string]resourceManagerWithCancel
	staleResources map[string]struct{}
	kubeClient     kubernetes.Interface
	newManager     func(string) resmanager.ResourceManager
}

type resourceManagerWithCancel struct {
	resmanager.ResourceManager
	Cancel   context.CancelFunc
	Done     <-chan struct{}
	Profile  string
	Stopping bool
}

func init() {
	ctr := &Controller{}
	framework.RegisterService(ctr, framework.WithOfficial(), framework.WithKubeClientInitFunc(ctr.KubeClientInit))
}

func (c *Controller) Name() string {
	return "daemon-controller"
}

func (c *Controller) KubeClientInit(clientSet framework.ClientSet) error {
	notifySyncCfg := cache.ResourceEventHandlerFuncs{
		AddFunc: func(_ interface{}) {
			c.synchronized.Store(false)
		},
		UpdateFunc: func(_, _ interface{}) {
			c.synchronized.Store(false)
		},
		DeleteFunc: func(_ interface{}) {
			c.synchronized.Store(false)
		},
	}

	_, err := clientSet.DpmockInformerFactory.Dpmock().V1alpha1().NodeResourceConfigurations().Informer().AddEventHandler(notifySyncCfg)
	if err != nil {
		return err
	}

	_, err = clientSet.KubeInformerFactory.Core().V1().Nodes().Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    notifySyncCfg.AddFunc,
		DeleteFunc: notifySyncCfg.DeleteFunc,
		UpdateFunc: func(oldObj, newObj interface{}) {
			// Most status changes do not affect selection. A retired mock resource
			// reappearing from kubelet's checkpoint does require another cleanup.
			oldNode, newNode := oldObj.(*v1.Node), newObj.(*v1.Node)
			if !maps.Equal(oldNode.Labels, newNode.Labels) || oldNode.Annotations[ManagedResourcesKey] != newNode.Annotations[ManagedResourcesKey] || hasRetiredResource(newNode) {
				c.synchronized.Store(false)
			}
		},
	})
	return err
}

func (c *Controller) Initialize() error {
	c.synchronized.Store(false)
	c.managers = make(map[string]resourceManagerWithCancel)
	c.staleResources = make(map[string]struct{})

	clientSet := framework.GetClientSet()
	c.kubeClient = clientSet.KubeClient
	c.newManager = resmanager.New
	c.nodeLister = clientSet.KubeInformerFactory.Core().V1().Nodes().Lister()
	c.nrcfgLister = clientSet.DpmockInformerFactory.Dpmock().V1alpha1().NodeResourceConfigurations().Lister()

	return nil
}

func (c *Controller) Run(ctx context.Context) error {
	wait.UntilWithContext(ctx, func(ctx context.Context) {
		if c.synchronized.Load() {
			return
		}

		c.synchronized.Store(true)
		if err := c.syncConfigOnce(ctx); err != nil {
			klog.ErrorS(err, "failed to sync config once")
			c.synchronized.Store(false)
		}
	}, time.Second)

	return framework.ErrContextDone
}
