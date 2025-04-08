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

// fetchAiCore attempts to retrieve the total number of AI Cores on the card.
// If it fails, it returns a fallback value according to the device type.
func fetchAiCore(hdm *server.HwDevManager, devType string) (int, error) {
	aiCoreCount, err := hdm.GetChipAiCoreCount()
	if err == nil {
		return int(aiCoreCount), nil
	}
	if strings.Contains(devType, "Ascend910") {
		return 32, err
	} else if strings.Contains(devType, "Ascend310P") {
		return 16, err
	}
	return 0, err
}

// fetchMemory attempts to retrieve total memory from the card.
// If it fails, it returns a fallback value according to the device type.
func fetchMemory(hdm *server.HwDevManager, devType string) (int, error) {
	memSize, err := hdm.GetChipMem()
	if err == nil {
		return int(memSize), nil
	}
	if strings.Contains(devType, "Ascend910") {
		return 32, err
	} else if strings.Contains(devType, "Ascend310P") {
		return 16, err
	}
	return 0, err
}

// getDeviceResources returns the maximum AI Core and memory for a device
// depending on whether it has been split into vNPUs or not.
func getDeviceResources(hdm *server.HwDevManager, devType string, vnpuManager *VnpuManager, deviceName string) (int, int) {
	if vnpuManager == nil {
		return 0, 0
	}
	physicalNpu := vnpuManager.PhysicalNpus[deviceName]
	if physicalNpu == nil {
		return 0, 0
	}

	// If the device has not been split yet, return the full card resources
	if len(physicalNpu.AllocatedSlices) == 0 {
		aiCores, errCore := fetchAiCore(hdm, devType)
		if errCore != nil {
			log.Printf("Failed to fetch AI Core count: %v", errCore)
		}
		mem, errMem := fetchMemory(hdm, devType)
		if errMem != nil {
			log.Printf("Failed to fetch memory size: %v", errMem)
		}
		return aiCores, mem
	}

	// If the device has already been split, find the largest remaining
	// AI Core and memory values from the available templates
	maxAicore, maxMemory := 0, 0
	for _, tpl := range physicalNpu.SupportTemplates {
		if tpl.Attributes.AICORE > maxAicore {
			maxAicore = tpl.Attributes.AICORE
		}
		if tpl.Attributes.Memory > maxMemory {
			maxMemory = tpl.Attributes.Memory
		}
	}
	return maxAicore, maxMemory
}

// enumerateAllPossibleDevices initializes the devmanager, creates a vNPU manager if possible,
// and enumerates all possible devices to produce an AllocatableDevices map.
func enumerateAllPossibleDevices() (AllocatableDevices, *VnpuManager, error) {
	devM, err := devmanager.AutoInit("")
	if err != nil {
		return nil, nil, err
	}
	hdm := server.NewHwDevManager(devM)
	allInfo := hdm.AllInfo

	vnpuManager, err := NewVnpuManager()
	if err != nil {
		log.Printf("Failed to initialize vNPU manager: %v. Only full-card allocation is supported.", err)
	}

	alldevices := make(AllocatableDevices)
	for _, dev := range allInfo.AllDevs {
		deviceName := fmt.Sprintf("npu-%d-0", dev.LogicID)
		uuidStr := fmt.Sprintf("%s-%d", os.Getenv("NODE_NAME"), dev.LogicID)

		devAttributes := map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
			DriverDomain + "index": {IntValue: ptr.To(int64(dev.LogicID))},
			DriverDomain + "uuid":  {StringValue: ptr.To(uuidStr)},
			DriverDomain + "model": {StringValue: ptr.To(dev.DevType)},
			DriverDomain + "type":  {StringValue: ptr.To("NPU")},
		}

		if vnpuManager != nil {
			vnpuManager.InitPhysicalNpu(deviceName, dev.LogicID, dev.DevType)
			maxAicore, maxMemory := getDeviceResources(hdm, dev.DevType, vnpuManager, deviceName)
			devAttributes[DriverDomain+"aicore"] = resourceapi.DeviceAttribute{IntValue: ptr.To(int64(maxAicore))}
			devAttributes[DriverDomain+"memory"] = resourceapi.DeviceAttribute{IntValue: ptr.To(int64(maxMemory))}
		}

		device := resourceapi.Device{
			Name: deviceName,
			Basic: &resourceapi.BasicDevice{
				Attributes: devAttributes,
			},
		}
		alldevices[device.Name] = device
		log.Printf("Discovered NPU device: %s, Type: NPU, Model: %s", deviceName, dev.DevType)
	}
	return alldevices, vnpuManager, nil
}

// contains checks if a string slice contains the target item.
func contains(slice []string, item string) bool {
	for _, v := range slice {
		if v == item {
			return true
		}
	}
	return false
}

// generateUUIDs generates a list of UUID strings based on a seed string.
func generateUUIDs(seed string, count int) []string {
	rng := rand.New(rand.NewSource(hash(seed)))
	uuids := make([]string, count)
	for i := 0; i < count; i++ {
		charset := make([]byte, 16)
		rng.Read(charset)
		u, _ := uuid.FromBytes(charset)
		uuids[i] = "npu-" + u.String()
	}
	return uuids
}

// hash implements a simple hash function for a string, returning an int64.
func hash(s string) int64 {
	var h int64
	for _, c := range s {
		h = 31*h + int64(c)
	}
	return h
}
