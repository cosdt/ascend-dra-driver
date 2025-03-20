#!/usr/bin/env bash

# Copyright 2024 The Kubernetes Authors.
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

# Very Simple Script for testing the demo

set -e

minikube status
kubectl get nodes
kubectl wait --for=condition=Ready nodes/minikube --timeout=120s
kubectl create -f demo/npu-test1.yaml
kubectl create -f demo/npu-test2.yaml
kubectl create -f demo/npu-test3.yaml
kubectl create -f demo/npu-test4.yaml
kubectl create -f demo/npu-test5.yaml

function npus-from-logs {
  local logs="$1"
  echo "$logs" | sed -nE "s/^declare -x NPU_DEVICE_[[:digit:]]+=\"(.+)\"$/\1/p"
}

function npu-id {
  local npu="$1"
  echo "$npu" | sed -nE "s/^npu-([[:digit:]]+)$/\1/p"
}

function npu-sharing-strategy-from-logs {
  local logs="$1"
  local id="$2"
  echo "$logs" | sed -nE "s/^declare -x NPU_DEVICE_${id}_SHARING_STRATEGY=\"(.+)\"$/\1/p"
}

function npu-timeslice-interval-from-logs {
  local logs="$1"
  local id="$2"
  echo "$logs" | sed -nE "s/^declare -x NPU_DEVICE_${id}_TIMESLICE_INTERVAL=\"(.+)\"$/\1/p"
}

function npu-partition-count-from-logs {
  local logs="$1"
  local id="$2"
  echo "$logs" | sed -nE "s/^declare -x NPU_DEVICE_${id}_PARTITION_COUNT=\"(.+)\"$/\1/p"
}

declare -a observed_npus
function npu-already-seen {
  local npu="$1"
  for seen in "${observed_npus[@]}"; do
    if [[ "$npu" == "$seen" ]]; then return 0; fi;
  done
  return 1
}

kubectl wait --for=condition=Ready -n npu-test1 pod/pod0 --timeout=120s
kubectl wait --for=condition=Ready -n npu-test1 pod/pod1 --timeout=120s
npu_test_1=$(kubectl get pods -n npu-test1 | grep -c 'Running')
if [ $npu_test_1 != 2 ]; then
    echo "npu_test_1 $npu_test_1 failed to match against 2 expected pods"
    exit 1
fi

npu_test1_pod0_ctr0_logs=$(kubectl logs -n npu-test1 pod0 -c ctr0)
npu_test1_pod0_ctr0_npus=$(npus-from-logs "$npu_test1_pod0_ctr0_logs")
npu_test1_pod0_ctr0_npus_count=$(echo "$npu_test1_pod0_ctr0_npus" | wc -w)
if [[ $npu_test1_pod0_ctr0_npus_count != 1 ]]; then
  echo "Expected Pod npu-test1/pod0, container ctr0 to have 1 NPU, but got $npu_test1_pod0_ctr0_npus_count: $npu_test1_pod0_ctr0_npus"
  exit 1
fi
npu_test1_pod0_ctr0_npu="$npu_test1_pod0_ctr0_npus"
if npu-already-seen "$npu_test1_pod0_ctr0_npu"; then
  echo "Pod npu-test1/pod0, container ctr0 should have a new NPU but claimed $npu_test1_pod0_ctr0_npu which is already claimed"
  exit 1
fi
echo "Pod npu-test1/pod0, container ctr0 claimed $npu_test1_pod0_ctr0_npu"
observed_npus+=("$npu_test1_pod0_ctr0_npu")

npu_test1_pod1_ctr0_logs=$(kubectl logs -n npu-test1 pod1 -c ctr0)
npu_test1_pod1_ctr0_npus=$(npus-from-logs "$npu_test1_pod1_ctr0_logs")
npu_test1_pod1_ctr0_npus_count=$(echo "$npu_test1_pod1_ctr0_npus" | wc -w)
if [[ $npu_test1_pod1_ctr0_npus_count != 1 ]]; then
  echo "Expected Pod npu-test1/pod1, container ctr0 to have 1 NPU, but got $npu_test1_pod1_ctr0_npus_count: $npu_test1_pod1_ctr0_npus"
  exit 1
fi
npu_test1_pod1_ctr0_npu="$npu_test1_pod1_ctr0_npus"
if npu-already-seen "$npu_test1_pod1_ctr0_npu"; then
  echo "Pod npu-test1/pod1, container ctr0 should have a new NPU but claimed $npu_test1_pod1_ctr0_npu which is already claimed"
  exit 1
fi
echo "Pod npu-test1/pod1, container ctr0 claimed $npu_test1_pod1_ctr0_npu"
observed_npus+=("$npu_test1_pod1_ctr0_npu")


kubectl wait --for=condition=Ready -n npu-test2 pod/pod0 --timeout=120s
npu_test_2=$(kubectl get pods -n npu-test2 | grep -c 'Running')
if [ $npu_test_2 != 1 ]; then
    echo "npu_test_2 $npu_test_2 failed to match against 1 expected pod"
    exit 1
fi

npu_test2_pod0_ctr0_logs=$(kubectl logs -n npu-test2 pod0 -c ctr0)
npu_test2_pod0_ctr0_npus=$(npus-from-logs "$npu_test2_pod0_ctr0_logs")
npu_test2_pod0_ctr0_npus_count=$(echo "$npu_test2_pod0_ctr0_npus" | wc -w)
if [[ $npu_test2_pod0_ctr0_npus_count != 2 ]]; then
  echo "Expected Pod npu-test2/pod0, container ctr0 to have 2 NPUs, but got $npu_test2_pod0_ctr0_npus_count: $npu_test2_pod0_ctr0_npus"
  exit 1
fi
echo "$npu_test2_pod0_ctr0_npus" | while read npu_test2_pod0_ctr0_npu; do
  if npu-already-seen "$npu_test2_pod0_ctr0_npu"; then
    echo "Pod npu-test2/pod0, container ctr0 should have a new NPU but claimed $npu_test2_pod0_ctr0_npu which is already claimed"
    exit 1
  fi
  echo "Pod npu-test2/pod0, container ctr0 claimed $npu_test2_pod0_ctr0_npu"
  observed_npus+=("$npu_test2_pod0_ctr0_npu")
done


kubectl wait --for=condition=Ready -n npu-test3 pod/pod0 --timeout=120s
npu_test_3=$(kubectl get pods -n npu-test3 | grep -c 'Running')
if [ $npu_test_3 != 1 ]; then
    echo "npu_test_3 $npu_test_3 failed to match against 1 expected pod"
    exit 1
fi

npu_test3_pod0_ctr0_logs=$(kubectl logs -n npu-test3 pod0 -c ctr0)
npu_test3_pod0_ctr0_npus=$(npus-from-logs "$npu_test3_pod0_ctr0_logs")
npu_test3_pod0_ctr0_npus_count=$(echo "$npu_test3_pod0_ctr0_npus" | wc -w)
if [[ $npu_test3_pod0_ctr0_npus_count != 1 ]]; then
  echo "Expected Pod npu-test3/pod0, container ctr0 to have 1 NPU, but got $npu_test3_pod0_ctr0_npus_count: $npu_test3_pod0_ctr0_npus"
  exit 1
fi
npu_test3_pod0_ctr0_npu="$npu_test3_pod0_ctr0_npus"
if npu-already-seen "$npu_test3_pod0_ctr0_npu"; then
  echo "Pod npu-test3/pod0, container ctr0 should have a new NPU but claimed $npu_test3_pod0_ctr0_npu which is already claimed"
  exit 1
fi
echo "Pod npu-test3/pod0, container ctr0 claimed $npu_test3_pod0_ctr0_npu"
observed_npus+=("$npu_test3_pod0_ctr0_npu")
npu_test3_pod0_ctr0_sharing_strategy=$(npu-sharing-strategy-from-logs "$npu_test3_pod0_ctr0_logs" $(npu-id "$npu_test3_pod0_ctr0_npu"))
if [[ "$npu_test3_pod0_ctr0_sharing_strategy" != "TimeSlicing" ]]; then
  echo "Expected Pod npu-test3/pod0, container ctr0 to have sharing strategy TimeSlicing, got $npu_test3_pod0_ctr0_sharing_strategy"
  exit 1
fi
npu_test3_pod0_ctr0_timeslice_interval=$(npu-timeslice-interval-from-logs "$npu_test3_pod0_ctr0_logs" $(npu-id "$npu_test3_pod0_ctr0_npu"))
if [[ "$npu_test3_pod0_ctr0_timeslice_interval" != "Default" ]]; then
  echo "Expected Pod npu-test3/pod0, container ctr0 to have timeslice interval Default, got $npu_test3_pod0_ctr0_timeslice_interval"
  exit 1
fi

npu_test3_pod0_ctr1_logs=$(kubectl logs -n npu-test3 pod0 -c ctr1)
npu_test3_pod0_ctr1_npus=$(npus-from-logs "$npu_test3_pod0_ctr1_logs")
npu_test3_pod0_ctr1_npus_count=$(echo "$npu_test3_pod0_ctr1_npus" | wc -w)
if [[ $npu_test3_pod0_ctr1_npus_count != 1 ]]; then
  echo "Expected Pod npu-test3/pod0, container ctr1 to have 1 NPU, but got $npu_test3_pod0_ctr1_npus_count: $npu_test3_pod0_ctr1_npus"
  exit 1
fi
npu_test3_pod0_ctr1_npu="$npu_test3_pod0_ctr1_npus"
echo "Pod npu-test3/pod0, container ctr1 claimed $npu_test3_pod0_ctr1_npu"
if [[ "$npu_test3_pod0_ctr1_npu" != "$npu_test3_pod0_ctr0_npu" ]]; then
  echo "Pod npu-test3/pod0, container ctr1 should claim the same NPU as Pod npu-test3/pod0, container ctr0, but did not"
  exit 1
fi
npu_test3_pod0_ctr1_sharing_strategy=$(npu-sharing-strategy-from-logs "$npu_test3_pod0_ctr1_logs" $(npu-id "$npu_test3_pod0_ctr1_npu"))
if [[ "$npu_test3_pod0_ctr1_sharing_strategy" != "TimeSlicing" ]]; then
  echo "Expected Pod npu-test3/pod0, container ctr1 to have sharing strategy TimeSlicing, got $npu_test3_pod0_ctr1_sharing_strategy"
  exit 1
fi
npu_test3_pod0_ctr1_timeslice_interval=$(npu-timeslice-interval-from-logs "$npu_test3_pod0_ctr1_logs" $(npu-id "$npu_test3_pod0_ctr1_npu"))
if [[ "$npu_test3_pod0_ctr1_timeslice_interval" != "Default" ]]; then
  echo "Expected Pod npu-test3/pod0, container ctr1 to have timeslice interval Default, got $npu_test3_pod0_ctr1_timeslice_interval"
  exit 1
fi


kubectl wait --for=condition=Ready -n npu-test4 pod/pod0 --timeout=120s
kubectl wait --for=condition=Ready -n npu-test4 pod/pod1 --timeout=120s
npu_test_4=$(kubectl get pods -n npu-test4 | grep -c 'Running')
if [ $npu_test_4 != 2 ]; then
    echo "npu_test_4 $npu_test_4 failed to match against 2 expected pods"
    exit 1
fi

npu_test4_pod0_ctr0_logs=$(kubectl logs -n npu-test4 pod0 -c ctr0)
npu_test4_pod0_ctr0_npus=$(npus-from-logs "$npu_test4_pod0_ctr0_logs")
npu_test4_pod0_ctr0_npus_count=$(echo "$npu_test4_pod0_ctr0_npus" | wc -w)
if [[ $npu_test4_pod0_ctr0_npus_count != 1 ]]; then
  echo "Expected Pod npu-test4/pod0, container ctr0 to have 1 NPU, but got $npu_test4_pod0_ctr0_npus_count: $npu_test4_pod0_ctr0_npus"
  exit 1
fi
npu_test4_pod0_ctr0_npu="$npu_test4_pod0_ctr0_npus"
if npu-already-seen "$npu_test4_pod0_ctr0_npu"; then
  echo "Pod npu-test4/pod0, container ctr0 should have a new NPU but claimed $npu_test4_pod0_ctr0_npu which is already claimed"
  exit 1
fi
echo "Pod npu-test4/pod0, container ctr0 claimed $npu_test4_pod0_ctr0_npu"
observed_npus+=("$npu_test4_pod0_ctr0_npu")
npu_test4_pod0_ctr0_sharing_strategy=$(npu-sharing-strategy-from-logs "$npu_test4_pod0_ctr0_logs" $(npu-id "$npu_test4_pod0_ctr0_npu"))
if [[ "$npu_test4_pod0_ctr0_sharing_strategy" != "TimeSlicing" ]]; then
  echo "Expected Pod npu-test4/pod0, container ctr0 to have sharing strategy TimeSlicing, got $npu_test4_pod0_ctr0_sharing_strategy"
  exit 1
fi
npu_test4_pod0_ctr0_timeslice_interval=$(npu-timeslice-interval-from-logs "$npu_test4_pod0_ctr0_logs" $(npu-id "$npu_test4_pod0_ctr0_npu"))
if [[ "$npu_test4_pod0_ctr0_timeslice_interval" != "Default" ]]; then
  echo "Expected Pod npu-test4/pod0, container ctr0 to have timeslice interval Default, got $npu_test4_pod0_ctr0_timeslice_interval"
  exit 1
fi

npu_test4_pod1_ctr0_logs=$(kubectl logs -n npu-test4 pod1 -c ctr0)
npu_test4_pod1_ctr0_npus=$(npus-from-logs "$npu_test4_pod1_ctr0_logs")
npu_test4_pod1_ctr0_npus_count=$(echo "$npu_test4_pod1_ctr0_npus" | wc -w)
if [[ $npu_test4_pod1_ctr0_npus_count != 1 ]]; then
  echo "Expected Pod npu-test4/pod1, container ctr0 to have 1 NPU, but got $npu_test4_pod1_ctr0_npus_count: $npu_test4_pod1_ctr0_npus"
  exit 1
fi
npu_test4_pod1_ctr0_npu="$npu_test4_pod1_ctr0_npus"
echo "Pod npu-test4/pod1, container ctr0 claimed $npu_test4_pod1_ctr0_npu"
if [[ "$npu_test4_pod1_ctr0_npu" != "$npu_test4_pod1_ctr0_npu" ]]; then
  echo "Pod npu-test4/pod1, container ctr0 should claim the same NPU as Pod npu-test4/pod1, container ctr0, but did not"
  exit 1
fi
npu_test4_pod1_ctr0_sharing_strategy=$(npu-sharing-strategy-from-logs "$npu_test4_pod1_ctr0_logs" $(npu-id "$npu_test4_pod1_ctr0_npu"))
if [[ "$npu_test4_pod1_ctr0_sharing_strategy" != "TimeSlicing" ]]; then
  echo "Expected Pod npu-test4/pod1, container ctr0 to have sharing strategy TimeSlicing, got $npu_test4_pod1_ctr0_sharing_strategy"
  exit 1
fi
npu_test4_pod1_ctr0_timeslice_interval=$(npu-timeslice-interval-from-logs "$npu_test4_pod1_ctr0_logs" $(npu-id "$npu_test4_pod1_ctr0_npu"))
if [[ "$npu_test4_pod1_ctr0_timeslice_interval" != "Default" ]]; then
  echo "Expected Pod npu-test4/pod1, container ctr0 to have timeslice interval Default, got $npu_test4_pod1_ctr0_timeslice_interval"
  exit 1
fi


kubectl wait --for=condition=Ready -n npu-test5 pod/pod0 --timeout=120s
npu_test_5=$(kubectl get pods -n npu-test5 | grep -c 'Running')
if [ $npu_test_5 != 1 ]; then
    echo "npu_test_5 $npu_test_5 failed to match against 1 expected pod"
    exit 1
fi

npu_test5_pod0_ts_ctr0_logs=$(kubectl logs -n npu-test5 pod0 -c ts-ctr0)
npu_test5_pod0_ts_ctr0_npus=$(npus-from-logs "$npu_test5_pod0_ts_ctr0_logs")
npu_test5_pod0_ts_ctr0_npus_count=$(echo "$npu_test5_pod0_ts_ctr0_npus" | wc -w)
if [[ $npu_test5_pod0_ts_ctr0_npus_count != 1 ]]; then
  echo "Expected Pod npu-test5/pod0, container ts-ctr0 to have 1 NPU, but got $npu_test5_pod0_ts_ctr0_npus_count: $npu_test5_pod0_ts_ctr0_npus"
  exit 1
fi
npu_test5_pod0_ts_ctr0_npu="$npu_test5_pod0_ts_ctr0_npus"
if npu-already-seen "$npu_test5_pod0_ts_ctr0_npu"; then
  echo "Pod npu-test5/pod0, container ts-ctr0 should have a new NPU but claimed $npu_test5_pod0_ts_ctr0_npu which is already claimed"
  exit 1
fi
echo "Pod npu-test5/pod0, container ts-ctr0 claimed $npu_test5_pod0_ts_ctr0_npu"
observed_npus+=("$npu_test5_pod0_ts_ctr0_npu")
npu_test5_pod0_ts_ctr0_sharing_strategy=$(npu-sharing-strategy-from-logs "$npu_test5_pod0_ts_ctr0_logs" $(npu-id "$npu_test5_pod0_ts_ctr0_npu"))
if [[ "$npu_test5_pod0_ts_ctr0_sharing_strategy" != "TimeSlicing" ]]; then
  echo "Expected Pod npu-test5/pod0, container ts-ctr0 to have sharing strategy TimeSlicing, got $npu_test5_pod0_ts_ctr0_sharing_strategy"
  exit 1
fi
npu_test5_pod0_ts_ctr0_timeslice_interval=$(npu-timeslice-interval-from-logs "$npu_test5_pod0_ts_ctr0_logs" $(npu-id "$npu_test5_pod0_ts_ctr0_npu"))
if [[ "$npu_test5_pod0_ts_ctr0_timeslice_interval" != "Long" ]]; then
  echo "Expected Pod npu-test5/pod0, container ts-ctr0 to have timeslice interval Long, got $npu_test5_pod0_ts_ctr0_timeslice_interval"
  exit 1
fi

npu_test5_pod0_ts_ctr1_logs=$(kubectl logs -n npu-test5 pod0 -c ts-ctr1)
npu_test5_pod0_ts_ctr1_npus=$(npus-from-logs "$npu_test5_pod0_ts_ctr1_logs")
npu_test5_pod0_ts_ctr1_npus_count=$(echo "$npu_test5_pod0_ts_ctr1_npus" | wc -w)
if [[ $npu_test5_pod0_ts_ctr1_npus_count != 1 ]]; then
  echo "Expected Pod npu-test5/pod0, container ts-ctr1 to have 1 NPU, but got $npu_test5_pod0_ts_ctr1_npus_count: $npu_test5_pod0_ts_ctr1_npus"
  exit 1
fi
npu_test5_pod0_ts_ctr1_npu="$npu_test5_pod0_ts_ctr1_npus"
echo "Pod npu-test5/pod0, container ts-ctr1 claimed $npu_test5_pod0_ts_ctr1_npu"
if [[ "$npu_test5_pod0_ts_ctr1_npu" != "$npu_test5_pod0_ts_ctr0_npu" ]]; then
  echo "Pod npu-test5/pod0, container ts-ctr1 should claim the same NPU as Pod npu-test5/pod0, container ts-ctr0, but did not"
  exit 1
fi
npu_test5_pod0_ts_ctr1_sharing_strategy=$(npu-sharing-strategy-from-logs "$npu_test5_pod0_ts_ctr1_logs" $(npu-id "$npu_test5_pod0_ts_ctr1_npu"))
if [[ "$npu_test5_pod0_ts_ctr1_sharing_strategy" != "TimeSlicing" ]]; then
  echo "Expected Pod npu-test5/pod0, container ts-ctr1 to have sharing strategy TimeSlicing, got $npu_test5_pod0_ts_ctr1_sharing_strategy"
  exit 1
fi
npu_test5_pod0_ts_ctr1_timeslice_interval=$(npu-timeslice-interval-from-logs "$npu_test5_pod0_ts_ctr1_logs" $(npu-id "$npu_test5_pod0_ts_ctr1_npu"))
if [[ "$npu_test5_pod0_ts_ctr1_timeslice_interval" != "Long" ]]; then
  echo "Expected Pod npu-test5/pod0, container ts-ctr1 to have timeslice interval Long, got $npu_test5_pod0_ts_ctr1_timeslice_interval"
  exit 1
fi

npu_test5_pod0_sp_ctr0_logs=$(kubectl logs -n npu-test5 pod0 -c sp-ctr0)
npu_test5_pod0_sp_ctr0_npus=$(npus-from-logs "$npu_test5_pod0_sp_ctr0_logs")
npu_test5_pod0_sp_ctr0_npus_count=$(echo "$npu_test5_pod0_sp_ctr0_npus" | wc -w)
if [[ $npu_test5_pod0_sp_ctr0_npus_count != 1 ]]; then
  echo "Expected Pod npu-test5/pod0, container sp-ctr0 to have 1 NPU, but got $npu_test5_pod0_sp_ctr0_npus_count: $npu_test5_pod0_sp_ctr0_npus"
  exit 1
fi
npu_test5_pod0_sp_ctr0_npu="$npu_test5_pod0_sp_ctr0_npus"
if npu-already-seen "$npu_test5_pod0_sp_ctr0_npu"; then
  echo "Pod npu-test5/pod0, container sp-ctr0 should have a new NPU but claimed $npu_test5_pod0_sp_ctr0_npu which is already claimed"
  exit 1
fi
echo "Pod npu-test5/pod0, container sp-ctr0 claimed $npu_test5_pod0_sp_ctr0_npu"
observed_npus+=("$npu_test5_pod0_sp_ctr0_npu")
npu_test5_pod0_sp_ctr0_sharing_strategy=$(npu-sharing-strategy-from-logs "$npu_test5_pod0_sp_ctr0_logs" $(npu-id "$npu_test5_pod0_sp_ctr0_npu"))
if [[ "$npu_test5_pod0_sp_ctr0_sharing_strategy" != "SpacePartitioning" ]]; then
  echo "Expected Pod npu-test5/pod0, container sp-ctr0 to have sharing strategy SpacePartitioning, got $npu_test5_pod0_sp_ctr0_sharing_strategy"
  exit 1
fi
npu_test5_pod0_sp_ctr0_partition_count=$(npu-partition-count-from-logs "$npu_test5_pod0_sp_ctr0_logs" $(npu-id "$npu_test5_pod0_sp_ctr0_npu"))
if [[ "$npu_test5_pod0_sp_ctr0_partition_count" != "10" ]]; then
  echo "Expected Pod npu-test5/pod0, container sp-ctr0 to have partition count 10, got $npu_test5_pod0_sp_ctr0_partition_count"
  exit 1
fi

npu_test5_pod0_sp_ctr1_logs=$(kubectl logs -n npu-test5 pod0 -c sp-ctr1)
npu_test5_pod0_sp_ctr1_npus=$(npus-from-logs "$npu_test5_pod0_sp_ctr1_logs")
npu_test5_pod0_sp_ctr1_npus_count=$(echo "$npu_test5_pod0_sp_ctr1_npus" | wc -w)
if [[ $npu_test5_pod0_sp_ctr1_npus_count != 1 ]]; then
  echo "Expected Pod npu-test5/pod0, container sp-ctr1 to have 1 NPU, but got $npu_test5_pod0_sp_ctr1_npus_count: $npu_test5_pod0_sp_ctr1_npus"
  exit 1
fi
npu_test5_pod0_sp_ctr1_npu="$npu_test5_pod0_sp_ctr1_npus"
echo "Pod npu-test5/pod0, container sp-ctr1 claimed $npu_test5_pod0_sp_ctr1_npu"
if [[ "$npu_test5_pod0_sp_ctr1_npu" != "$npu_test5_pod0_sp_ctr0_npu" ]]; then
  echo "Pod npu-test5/pod0, container sp-ctr1 should claim the same NPU as Pod npu-test5/pod0, container sp-ctr0, but did not"
  exit 1
fi
npu_test5_pod0_sp_ctr1_sharing_strategy=$(npu-sharing-strategy-from-logs "$npu_test5_pod0_sp_ctr1_logs" $(npu-id "$npu_test5_pod0_sp_ctr1_npu"))
if [[ "$npu_test5_pod0_sp_ctr1_sharing_strategy" != "SpacePartitioning" ]]; then
  echo "Expected Pod npu-test5/pod0, container sp-ctr1 to have sharing strategy SpacePartitioning, got $npu_test5_pod0_sp_ctr1_sharing_strategy"
  exit 1
fi
npu_test5_pod0_sp_ctr1_partition_count=$(npu-partition-count-from-logs "$npu_test5_pod0_sp_ctr1_logs" $(npu-id "$npu_test5_pod0_sp_ctr1_npu"))
if [[ "$npu_test5_pod0_sp_ctr1_partition_count" != "10" ]]; then
  echo "Expected Pod npu-test5/pod0, container sp-ctr1 to have partition count 10, got $npu_test5_pod0_sp_ctr1_partition_count"
  exit 1
fi

# test that deletion is fast (less than the default grace period of 30s)
# see https://github.com/kubernetes/kubernetes/issues/127188 for details
kubectl delete -f demo/npu-test1.yaml --timeout=25s
kubectl delete -f demo/npu-test2.yaml --timeout=25s
kubectl delete -f demo/npu-test3.yaml --timeout=25s
kubectl delete -f demo/npu-test4.yaml --timeout=25s
kubectl delete -f demo/npu-test5.yaml --timeout=25s

echo "test ran successfully"
