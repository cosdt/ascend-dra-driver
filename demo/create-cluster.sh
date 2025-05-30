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
CURRENT_DIR="$(cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd)"

set -ex
set -o pipefail

chmod +x "${CURRENT_DIR}/scripts/create-minikube-cluster.sh"
chmod +x "${CURRENT_DIR}/install-dra-driver.sh"

source "${CURRENT_DIR}/scripts/common.sh"


# 创建或启动 minikube 集群（幂等）
${SCRIPTS_DIR}/create-minikube-cluster.sh

# 如果本地已经存在 DRIVER_IMAGE，则加载到 minikube 集群
EXISTING_IMAGE_ID="$(${CONTAINER_TOOL} images --filter "reference=${DRIVER_IMAGE}" -q)"
if [ "${EXISTING_IMAGE_ID}" != "" ]; then
  # minikube >= v1.25.0 开始支持 image load 命令
  minikube image load "${DRIVER_IMAGE}" --profile="${MINIKUBE_PROFILE_NAME}"
fi

set +x
printf '\033[0;32m'
echo "Cluster creation complete: ${MINIKUBE_PROFILE_NAME}"
printf '\033[0m'
