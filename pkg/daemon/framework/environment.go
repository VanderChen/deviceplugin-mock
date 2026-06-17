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

package framework

import (
	"errors"
	"os"
)

type Envs struct {
	NodeName     string
	NodeIP       string
	PodNamespace string
}

var envs Envs

func GetEnvs() *Envs {
	return &envs
}

func initializeEnvironment() error {
	nodeName := os.Getenv("KUBE_NODE_NAME")
	if nodeName == "" {
		return errors.New("env 'KUBE_NODE_NAME' not set")
	}
	nodeIP := os.Getenv("KUBE_NODE_IP")
	if nodeIP == "" {
		return errors.New("env 'KUBE_NODE_IP' not set")
	}

	envs.NodeName = nodeName
	envs.NodeIP = nodeIP
	envs.PodNamespace = os.Getenv("POD_NAMESPACE")

	return nil
}
