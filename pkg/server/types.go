/* Copyright(C) 2022. Huawei Technologies Co.,Ltd. All rights reserved.
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

// Package server 包含向kubelet注册、k8s设备插件接口和grpc服务的实现。
package server

import (
	"sync"

	"google.golang.org/grpc"
	"k8s.io/api/core/v1"
	"Ascend-dra-driver/pkg/common"
	"Ascend-dra-driver/pkg/device"
)

// InterfaceServer 为提供服务而持续运行的对象接口
type InterfaceServer interface {
	Start(*common.FileWatch) error
	Stop()
	GetRestartFlag() bool
	SetRestartFlag(bool)
}

// PluginServer 实现DevicePluginServer接口；管理grpc服务器的注册和生命周期
type PluginServer struct {
	manager              device.DevManager
	grpcServer           *grpc.Server
	isRunning            *common.AtomicBool
	cachedDevices        []common.NpuDevice
	deviceType           string
	ascendRuntimeOptions string
	defaultDevs          []string
	allocMapLock         sync.RWMutex
	cachedLock           sync.RWMutex
	reciChan             chan interface{}
	stop                 chan interface{}
	klt2RealDevMap       map[string]string
	restart              bool
}

// PodDevice 定义Pod中的设备信息
type PodDevice struct {
	ResourceName string
	DeviceIds    []string
}

// PodDeviceInfo 定义Pod的设备信息，包括kubelet分配和实际分配的设备
type PodDeviceInfo struct {
	Pod        v1.Pod
	KltDevice  []string
	RealDevice []string
}
