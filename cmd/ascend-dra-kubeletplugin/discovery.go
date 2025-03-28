/*
 * Copyright 2023 The Kubernetes Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package main

import (
	"fmt"
	"log"
	"math/rand"
	"os"

	"Ascend-dra-driver/pkg/server"

	"huawei.com/npu-exporter/v5/devmanager"
	resourceapi "k8s.io/api/resource/v1beta1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"

	"github.com/google/uuid"
)

func enumerateAllPossibleDevices() (AllocatableDevices, error) {
	devM, err := devmanager.AutoInit("")
	if err != nil {
		return nil, err
	}
	hdm := server.NewHwDevManager(devM)
	allInfo := hdm.AllInfo

	// 初始化vNPU管理器
	vnpuManager, err := NewVnpuManager()
	if err != nil {
		log.Printf("初始化vNPU管理器失败: %v，将只支持整卡分配", err)
		// 即使初始化失败，仍然继续，只不过不支持vNPU分配
	}

	alldevices := make(AllocatableDevices)
	// 遍历所有设备，根据实际硬件信息构造 resourceapi.Device 对象
	for _, dev := range allInfo.AllDevs {
		// 使用 dev.LogicID 作为设备索引，设备名称格式为 "npu-<LogicID>"
		deviceName := fmt.Sprintf("npu-%d", dev.LogicID)
		// 生成设备唯一标识（例如使用 NODE_NAME 和设备ID 拼接）
		uuidStr := fmt.Sprintf("%s-%d", os.Getenv("NODE_NAME"), dev.LogicID)

		// 构建基本设备属性
		devAttributes := map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
			"index": {IntValue: ptr.To(int64(dev.LogicID))},
			"uuid":  {StringValue: ptr.To(uuidStr)},
			"model": {StringValue: ptr.To(dev.DevType)},
		}

		// 添加vNPU模板信息作为设备属性
		if vnpuManager != nil {
			// 初始化物理NPU
			vnpuManager.InitPhysicalNpu(deviceName, dev.LogicID)

			// 添加支持的vNPU模板属性
			physicalNpu := vnpuManager.PhysicalNpus[deviceName]
			if physicalNpu != nil {
				for name, template := range physicalNpu.SupportTemplates {
					attrName := fmt.Sprintf("template.%s.aicore", name)
					devAttributes[resourceapi.QualifiedName(attrName)] = resourceapi.DeviceAttribute{
						IntValue: ptr.To(int64(template.Attributes.AICORE)),
					}

					attrName = fmt.Sprintf("template.%s.memory", name)
					devAttributes[resourceapi.QualifiedName(attrName)] = resourceapi.DeviceAttribute{
						IntValue: ptr.To(int64(template.Attributes.Memory)),
					}

					attrName = fmt.Sprintf("template.%s.aicpu", name)
					devAttributes[resourceapi.QualifiedName(attrName)] = resourceapi.DeviceAttribute{
						IntValue: ptr.To(int64(template.Attributes.AICPU)),
					}

					// 添加更多vNPU模板属性...
				}
			}
		}

		device := resourceapi.Device{
			Name: deviceName,
			Basic: &resourceapi.BasicDevice{
				Attributes: devAttributes,
				Capacity: map[resourceapi.QualifiedName]resourceapi.DeviceCapacity{
					"memory": {Value: resource.MustParse("32Gi")},
				},
			},
		}
		alldevices[device.Name] = device

		log.Printf("发现NPU设备: %s, 型号: %s", deviceName, dev.DevType)
	}
	return alldevices, nil
}

func contains(slice []string, item string) bool {
	for _, v := range slice {
		if v == item {
			return true
		}
	}
	return false
}

func generateUUIDs(seed string, count int) []string {
	rand := rand.New(rand.NewSource(hash(seed)))

	uuids := make([]string, count)
	for i := 0; i < count; i++ {
		charset := make([]byte, 16)
		rand.Read(charset)
		uuid, _ := uuid.FromBytes(charset)
		uuids[i] = "npu-" + uuid.String()
	}

	return uuids
}

func hash(s string) int64 {
	h := int64(0)
	for _, c := range s {
		h = 31*h + int64(c)
	}
	return h
}
