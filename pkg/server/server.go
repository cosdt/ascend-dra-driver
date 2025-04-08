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
	"Ascend-dra-driver/pkg/common"
)

// Start 启动gRPC服务器，向Kubelet注册设备插件
func (ps *PluginServer) Start(socketWatcher *common.FileWatch) error {
	// 清理
	ps.Stop()

	var err error

	ps.Stop()

	return err
}

// Stop 停止gRPC服务器
func (ps *PluginServer) Stop() {
	ps.isRunning.Store(false)

	if ps.grpcServer == nil {
		return
	}
	ps.stopListAndWatch()
	ps.grpcServer.Stop()

	return
}

// GetRestartFlag 获取重启标志
func (ps *PluginServer) GetRestartFlag() bool {
	return ps.restart
}

// SetRestartFlag 设置重启标志
func (ps *PluginServer) SetRestartFlag(flag bool) {
	ps.restart = flag
}
