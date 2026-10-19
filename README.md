# Volcano Device Plugin Mock

This project, `deviceplugin-mock`, is a component of the [Volcano](https://volcano.sh/) ecosystem. It is designed to mock device plugins in a Kubernetes cluster, allowing for the testing and development of schedulers and other components that rely on device resources without needing actual hardware.

## Overview

The project consists of two main components:

- **Controller**: Manages the lifecycle and configuration of the mock devices.
- **Daemon**: Runs on nodes and interacts with the Kubelet to register mock devices.

## Getting Started

### Prerequisites

- Go 1.24.0+
- Docker (or another container runtime)
- Kubernetes cluster

### Building

You can build the binaries and images using the provided `Makefile`.

#### Build Binaries

To build the controller and daemon binaries locally:

```sh
make build
```

The binaries will be placed in `_output/bin/`.

#### Build Images

To build the Docker images:

```sh
make images
```

You can customize the image registry and name using environment variables:

```sh
make images IMAGE_REGISTRY=myregistry.local IMAGE_NAME=my/mocker TAG=v1.0.0
```

### Deployment

#### Using Helm

A Helm chart is provided in `installer/charts/volcano-deviceplugin-mock`.

1. **Package the Helm chart:**

    ```sh
    make helm-package
    ```

    The chart will be saved to `_output/charts`.

2. **Install the chart:**

    ```sh
    helm install deviceplugin-mock ./installer/charts/volcano-deviceplugin-mock -n volcano-system --create-namespace
    ```

#### Manual Deployment

The `Makefile` also provides targets for managing manifests:

- **Generate Manifests:**
  
  ```sh
  make manifests
  ```

- **Generate Helm Template:**

  ```sh
  make helm-template
  ```
  This will output the rendered manifests to `_output/manifest`.

## Ascend profiles and cleanup

D950 publishes `root-info-<node-ip>` in the daemon namespace, with the JSON
payload under `hccl_rootinfo.json`. IPv6 addresses use `root-info-v6-` followed
by the 16-byte address in lowercase hexadecimal. When a node IP changes,
owned historical `root-info-*` ConfigMaps are cleaned after the current object
is written successfully. The unpublished `rootinfo-*` naming is not supported.
The chart injects the daemon Pod identity for owner references and scopes
ConfigMap permissions to its namespace.

D950 uses the node IP for the network annotation's `server_id`; existing D910
profiles retain the node name. Its HCCS/SIO annotation intentionally preserves
D910C mock behavior. Local physical ID segments are stable per node but may
repeat between nodes; rack-wide uniqueness is not simulated. The per-Pod path
does not list cluster Nodes.

The daemon records resource ownership in the Node annotation
`dpmock.volcano.sh/managed-resources` before advertising a resource. This lets
cleanup resume after a restart without deleting resources that merely appear
in another node's configuration. Each entry stores the profile and undo patch
as well as the resource name. Failed cleanup is retried automatically. After
removal, the entry becomes `{"retired":true}`: kubelet may later restore stale
capacity from its checkpoint, and the Node watch will remove that resource
again. An absent retired resource causes no periodic cleanup requests.

Before handing the same resource name to a real device plugin, explicitly
remove its retired entry from this annotation to transfer ownership.

An older daemon has no durable ownership record. For a resource that is
**confirmed to have been created by this mock on this node**, an administrator
can seed a cleanup record, for example:

```sh
kubectl annotate node <node-name> 'dpmock.volcano.sh/managed-resources={"huawei.com/ascend-1980":{"undoPatch":"metadata:\n  annotations:\n    volcano.sh/device-topologys: null\n    huawei.com/ascend-1980-status: null\n    huawei.com/ascend-status: null\n"}}'
```

If the annotation already exists, merge the new entry into its existing JSON
map instead of replacing it. A Node annotation update triggers reconciliation.
Unowned resources from old installations are deliberately not inferred from
cluster-wide configurations.

## Development

- **Run linting and checks:**

  ```sh
  make vet
  make fmt
  ```

- **Generate code (DeepCopy, Client, etc.):**

  ```sh
  make generate-all
  ```

## License

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
