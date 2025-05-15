#!/usr/bin/env bash

# Copyright 2023 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# This scripts invokes `kind build image` so that the resulting
# image has a containerd with CDI support.
#
# Usage: kind-build-image.sh <tag of generated image>

# A reference to the current directory where this script is located
SCRIPTS_DIR="$(cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd)"

# The name of the example driver
: ${DRIVER_NAME:=ascend-dra-driver}

# The registry, image and tag for the example driver
: ${DRIVER_IMAGE_REGISTRY:="registry.example.com"}
: ${DRIVER_IMAGE_NAME:="${DRIVER_NAME}"}
: ${DRIVER_IMAGE_TAG:="v0.1.0"}
: ${DRIVER_IMAGE_PLATFORM:="ubuntu22.04"}

# 集群名称（minikube 的 profile 名）
: ${MINIKUBE_PROFILE_NAME:="${DRIVER_NAME}-cluster"}

# The derived name of the driver image to build
: ${DRIVER_IMAGE:="${DRIVER_IMAGE_REGISTRY}/${DRIVER_IMAGE_NAME}:${DRIVER_IMAGE_TAG}"}


# Container tool, e.g. docker/podman
if [[ -z "${CONTAINER_TOOL}" ]]; then
    if [[ -n "$(which docker)" ]]; then
        echo "Docker found in PATH."
        CONTAINER_TOOL=docker
    elif [[ -n "$(which podman)" ]]; then
        echo "Podman found in PATH."
        CONTAINER_TOOL=podman
    else
        echo "No container tool detected. Please install Docker or Podman."
        return 1
    fi
fi
