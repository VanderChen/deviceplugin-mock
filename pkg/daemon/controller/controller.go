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
	"sync/atomic"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
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
}

type resourceManagerWithCancel struct {
	resmanager.ResourceManager
	Cancel context.CancelFunc
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

	return nil
}

func (c *Controller) Initialize() error {
	c.synchronized.Store(false)
	c.managers = make(map[string]resourceManagerWithCancel)
	c.staleResources = make(map[string]struct{})

	clientSet := framework.GetClientSet()
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
