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
	"strings"

	"Ascend-dra-driver/pkg/server"

	"huawei.com/npu-exporter/v5/devmanager"
	resourceapi "k8s.io/api/resource/v1beta1"
	"k8s.io/utils/ptr"

	"github.com/google/uuid"
)

func enumerateAllPossibleDevices() (AllocatableDevices, *VnpuManager, error) {
	devM, err := devmanager.AutoInit("")
	if err != nil {
		return nil, nil, err
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
			DriverDomain + "index": {IntValue: ptr.To(int64(dev.LogicID))},
			DriverDomain + "uuid":  {StringValue: ptr.To(uuidStr)},
			DriverDomain + "model": {StringValue: ptr.To(dev.DevType)},
		}

		// 添加vNPU模板信息作为设备属性
		if vnpuManager != nil {
			// 初始化物理NPU
			vnpuManager.InitPhysicalNpu(deviceName, dev.LogicID, dev.DevType)

			// 添加支持的vNPU模板属性
			physicalNpu := vnpuManager.PhysicalNpus[deviceName]
			if physicalNpu != nil {
				// 找出当前支持的最大算力属性
				maxAicore := 0
				maxMemory := 0

				// 检查NPU是否已被拆分
				isAlreadySplit := len(physicalNpu.AllocatedSlices) > 0

				if !isAlreadySplit {
					// 如果NPU未被拆分，应该使用整卡的物理资源值
					// 使用底层函数直接获取整卡资源值
					// 获取整卡AI Core数量
					aiCoreCount, err := hdm.GetChipAiCoreCount()
					if err == nil {
						maxAicore = int(aiCoreCount)
					} else {
						log.Printf("获取整卡AI Core数量失败: %v，使用默认值", err)
						// 回退到之前的方法，使用预定义值
						if strings.Contains(dev.DevType, "Ascend910") {
							maxAicore = 32
						} else if strings.Contains(dev.DevType, "Ascend310P") {
							maxAicore = 16
						}
					}

					// 获取整卡内存大小
					memSize, err := hdm.GetChipMem()
					if err == nil {
						maxMemory = int(memSize)
					} else {
						log.Printf("获取整卡内存大小失败: %v，使用默认值", err)
						// 回退到之前的方法，使用预定义值
						if strings.Contains(dev.DevType, "Ascend910") {
							maxMemory = 32
						} else if strings.Contains(dev.DevType, "Ascend310P") {
							maxMemory = 16
						}
					}
				} else {
					// 如果NPU已被拆分，计算后续最大可用模板的算力值
					// 只考虑剩余可用的模板（即经过updateSupportTemplates更新后的模板）
					availableTemplates := physicalNpu.SupportTemplates
					if len(availableTemplates) > 0 {
						// 从剩余可用的模板中找出最大算力
						for _, template := range availableTemplates {
							if template.Attributes.AICORE > maxAicore {
								maxAicore = template.Attributes.AICORE
							}
							if template.Attributes.Memory > maxMemory {
								maxMemory = template.Attributes.Memory
							}
						}
					} else {
						// 如果没有可用模板，则表示无法再分配，将值设为0
						maxAicore = 0
						maxMemory = 0
					}
				}

				// 将最大算力属性作为设备属性
				devAttributes[DriverDomain+"aicore"] = resourceapi.DeviceAttribute{
					IntValue: ptr.To(int64(maxAicore)),
				}
				devAttributes[DriverDomain+"memory"] = resourceapi.DeviceAttribute{
					IntValue: ptr.To(int64(maxMemory)),
				}
			}
		}

		device := resourceapi.Device{
			Name: deviceName,
			Basic: &resourceapi.BasicDevice{
				Attributes: devAttributes,
			},
		}
		alldevices[device.Name] = device

		log.Printf("发现NPU设备: %s, 型号: %s", deviceName, dev.DevType)
	}
	return alldevices, vnpuManager, nil
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
