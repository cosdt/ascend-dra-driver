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
	"slices"
	"sync"

	resourceapi "k8s.io/api/resource/v1beta1"
	"k8s.io/apimachinery/pkg/runtime"
	drapbv1 "k8s.io/kubelet/pkg/apis/dra/v1beta1"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"

	configapi "Ascend-dra-driver/api/example.com/resource/gpu/v1alpha1"

	cdiapi "tags.cncf.io/container-device-interface/pkg/cdi"
	cdispec "tags.cncf.io/container-device-interface/specs-go"
)

type AllocatableDevices map[string]resourceapi.Device
type PreparedDevices []*PreparedDevice
type PreparedClaims map[string]PreparedDevices
type PerDeviceCDIContainerEdits map[string]*cdiapi.ContainerEdits

type OpaqueDeviceConfig struct {
	Requests []string
	Config   runtime.Object
}

// VnpuTemplateAttribute 存储vNPU模板的各项资源属性
type VnpuTemplateAttribute struct {
	AICORE  int
	Memory  int // 单位GB
	AICPU   int
	VPC     int
	PNGD    int
	VENC    int
	VDEC    int
	JPEGD   int
	JPEGE   int
}

// VnpuTemplate 表示一个vNPU模板
type VnpuTemplate struct {
	Name       string
	Attributes VnpuTemplateAttribute
}

// VnpuSlice 表示一个vNPU分片
type VnpuSlice struct {
	SliceID      string // 例如: npu-0-1, npu-0-2 表示npu-0的第1,2个slice
	TemplateName string // 对应的模板名称，例如: vir01, vir02
	Allocated    bool   // 是否已分配
}

// PhysicalNpuState 维护一个物理NPU卡的状态
type PhysicalNpuState struct {
	DeviceName       string              // 设备名称，例如: npu-0
	LogicID          int                 // 逻辑ID
	AvailableSlices  []*VnpuSlice        // 可用的vNPU分片
	AllocatedSlices  []*VnpuSlice        // 已分配的vNPU分片
	SupportTemplates map[string]*VnpuTemplate // 支持的所有模板
}

// VnpuManager 管理所有物理NPU和vNPU
type VnpuManager struct {
	sync.Mutex
	PhysicalNpus map[string]*PhysicalNpuState // 键为设备名称
	Templates    map[string]*VnpuTemplate     // 所有可用的模板
}

type PreparedDevice struct {
	drapbv1.Device
	ContainerEdits *cdiapi.ContainerEdits
}

func (pds PreparedDevices) GetDevices() []*drapbv1.Device {
	var devices []*drapbv1.Device
	for _, pd := range pds {
		devices = append(devices, &pd.Device)
	}
	return devices
}

type DeviceState struct {
	sync.Mutex
	cdi               *CDIHandler
	allocatable       AllocatableDevices
	checkpointManager checkpointmanager.CheckpointManager
	vnpuManager       *VnpuManager
}

func NewDeviceState(config *Config) (*DeviceState, error) {
	allocatable, err := enumerateAllPossibleDevices()
	if err != nil {
		return nil, fmt.Errorf("error enumerating all possible devices: %v", err)
	}

	cdi, err := NewCDIHandler(config)
	if err != nil {
		return nil, fmt.Errorf("unable to create CDI handler: %v", err)
	}

	err = cdi.CreateCommonSpecFile()
	if err != nil {
		return nil, fmt.Errorf("unable to create CDI spec file for common edits: %v", err)
	}

	checkpointManager, err := checkpointmanager.NewCheckpointManager(DriverPluginPath)
	if err != nil {
		return nil, fmt.Errorf("unable to create checkpoint manager: %v", err)
	}

	// 初始化vNPU管理器
	vnpuManager, err := NewVnpuManager()
	if err != nil {
		log.Printf("警告: 初始化vNPU管理器失败: %v, 将只支持整卡分配", err)
		// 即使失败，也继续，只是不支持vNPU分配
	}

	state := &DeviceState{
		cdi:               cdi,
		allocatable:       allocatable,
		checkpointManager: checkpointManager,
		vnpuManager:       vnpuManager,
	}

	checkpoints, err := state.checkpointManager.ListCheckpoints()
	if err != nil {
		return nil, fmt.Errorf("unable to list checkpoints: %v", err)
	}

	for _, c := range checkpoints {
		if c == DriverPluginCheckpointFile {
			return state, nil
		}
	}

	checkpoint := newCheckpoint()
	if err := state.checkpointManager.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint); err != nil {
		return nil, fmt.Errorf("unable to sync to checkpoint: %v", err)
	}

	return state, nil
}

func (s *DeviceState) Prepare(claim *resourceapi.ResourceClaim) ([]*drapbv1.Device, error) {
	s.Lock()
	defer s.Unlock()

	claimUID := string(claim.UID)

	checkpoint := newCheckpoint()
	if err := s.checkpointManager.GetCheckpoint(DriverPluginCheckpointFile, checkpoint); err != nil {
		return nil, fmt.Errorf("unable to sync from checkpoint: %v", err)
	}
	preparedClaims := checkpoint.V1.PreparedClaims

	if preparedClaims[claimUID] != nil {
		return preparedClaims[claimUID].GetDevices(), nil
	}

	preparedDevices, err := s.prepareDevices(claim)
	if err != nil {
		return nil, fmt.Errorf("prepare failed: %v", err)
	}

	if err = s.cdi.CreateClaimSpecFile(claimUID, preparedDevices); err != nil {
		return nil, fmt.Errorf("unable to create CDI spec file for claim: %v", err)
	}

	preparedClaims[claimUID] = preparedDevices
	if err := s.checkpointManager.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint); err != nil {
		return nil, fmt.Errorf("unable to sync to checkpoint: %v", err)
	}

	return preparedClaims[claimUID].GetDevices(), nil
}

func (s *DeviceState) Unprepare(claimUID string) error {
	s.Lock()
	defer s.Unlock()

	checkpoint := newCheckpoint()
	if err := s.checkpointManager.GetCheckpoint(DriverPluginCheckpointFile, checkpoint); err != nil {
		return fmt.Errorf("unable to sync from checkpoint: %v", err)
	}
	preparedClaims := checkpoint.V1.PreparedClaims

	if preparedClaims[claimUID] == nil {
		return nil
	}

	if err := s.unprepareDevices(claimUID, preparedClaims[claimUID]); err != nil {
		return fmt.Errorf("unprepare failed: %v", err)
	}

	err := s.cdi.DeleteClaimSpecFile(claimUID)
	if err != nil {
		return fmt.Errorf("unable to delete CDI spec file for claim: %v", err)
	}

	delete(preparedClaims, claimUID)
	if err := s.checkpointManager.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint); err != nil {
		return fmt.Errorf("unable to sync to checkpoint: %v", err)
	}

	return nil
}

func (s *DeviceState) prepareDevices(claim *resourceapi.ResourceClaim) (PreparedDevices, error) {
	if claim.Status.Allocation == nil {
		return nil, fmt.Errorf("claim not yet allocated")
	}

	// Retrieve the full set of device configs for the driver.
	configs, err := GetOpaqueDeviceConfigs(
		configapi.Decoder,
		DriverName,
		claim.Status.Allocation.Devices.Config,
	)
	if err != nil {
		return nil, fmt.Errorf("error getting opaque device configs: %v", err)
	}

	// 检查是否需要vNPU分片
	var requestedTemplate string
	for _, config := range configs {
		switch castConfig := config.Config.(type) {
		case *configapi.GpuConfig:
			if castConfig.VnpuSpec != nil && castConfig.VnpuSpec.TemplateName != "" {
				requestedTemplate = castConfig.VnpuSpec.TemplateName
				log.Printf("发现vNPU规格需求: %s", requestedTemplate)
			}
		}
	}

	// Add the default NPU Config to the front of the config list with the
	// lowest precedence.
	configs = slices.Insert(configs, 0, &OpaqueDeviceConfig{
		Requests: []string{},
		Config:   configapi.DefaultGpuConfig(), // 这里可保持结构不变，内部逻辑可更新为 NPU 相关
	})

	configResultsMap := make(map[runtime.Object][]*resourceapi.DeviceRequestAllocationResult)
	for _, result := range claim.Status.Allocation.Devices.Results {
		origDevice := result.Device
		
		// 检查是否请求了vNPU分片
		if requestedTemplate != "" && s.vnpuManager != nil {
			// 分配vNPU分片
			log.Printf("为设备 %s 尝试分配vNPU分片，模板: %s", origDevice, requestedTemplate)
			slice, err := s.vnpuManager.AllocateSlice(origDevice, requestedTemplate)
			if err != nil {
				log.Printf("警告: 分配vNPU分片失败: %v，将使用整卡分配", err)
			} else {
				// 替换设备ID为分片ID
				result.Device = slice.SliceID
				log.Printf("成功为设备 %s 分配vNPU分片: %s, 模板: %s", 
					origDevice, slice.SliceID, requestedTemplate)
			}
		} else {
			// 整卡分配
			if s.vnpuManager != nil {
				_, err := s.vnpuManager.AllocateSlice(origDevice, "")
				if err != nil {
					log.Printf("警告: 分配整卡失败: %v", err)
				} else {
					log.Printf("成功为设备 %s 分配整卡", origDevice)
				}
			}
		}
		
		if _, exists := s.allocatable[origDevice]; !exists {
			return nil, fmt.Errorf("requested NPU is not allocatable: %v", origDevice)
		}
		for _, c := range slices.Backward(configs) {
			if len(c.Requests) == 0 || slices.Contains(c.Requests, result.Request) {
				configResultsMap[c.Config] = append(configResultsMap[c.Config], &result)
				break
			}
		}
	}

	perDeviceCDIContainerEdits := make(PerDeviceCDIContainerEdits)
	for c, results := range configResultsMap {
		var config *configapi.GpuConfig
		switch castConfig := c.(type) {
		case *configapi.GpuConfig:
			config = castConfig
		default:
			return nil, fmt.Errorf("runtime object is not a recognized configuration")
		}

		if err := config.Normalize(); err != nil {
			return nil, fmt.Errorf("error normalizing NPU config: %w", err)
		}

		if err := config.Validate(); err != nil {
			return nil, fmt.Errorf("error validating NPU config: %w", err)
		}

		containerEdits, err := s.applyConfig(config, results)
		if err != nil {
			return nil, fmt.Errorf("error applying NPU config: %w", err)
		}

		for k, v := range containerEdits {
			perDeviceCDIContainerEdits[k] = v
		}
	}

	var preparedDevices PreparedDevices
	for _, results := range configResultsMap {
		for _, result := range results {
			device := &PreparedDevice{
				Device: drapbv1.Device{
					RequestNames: []string{result.Request},
					PoolName:     result.Pool,
					DeviceName:   result.Device,
					CDIDeviceIDs: s.cdi.GetClaimDevices(string(claim.UID), []string{result.Device}),
				},
				ContainerEdits: perDeviceCDIContainerEdits[result.Device],
			}
			preparedDevices = append(preparedDevices, device)
		}
	}

	return preparedDevices, nil
}

func (s *DeviceState) unprepareDevices(claimUID string, devices PreparedDevices) error {
	log.Printf("开始释放设备，claimUID: %s", claimUID)
	
	// 如果没有vNPU管理器，直接返回成功
	if s.vnpuManager == nil {
		return nil
	}
	
	// 遍历所有设备，释放对应的vNPU分片
	for _, device := range devices {
		deviceID := device.Device.DeviceName
		
		err := s.vnpuManager.ReleaseSlice(deviceID)
		if err != nil {
			log.Printf("警告: 释放vNPU分片 %s 失败: %v", deviceID, err)
			// 继续处理下一个设备，不中断流程
		} else {
			log.Printf("成功释放vNPU分片: %s", deviceID)
		}
	}
	
	return nil
}

func (s *DeviceState) applyConfig(config *configapi.GpuConfig, results []*resourceapi.DeviceRequestAllocationResult) (PerDeviceCDIContainerEdits, error) {
	perDeviceEdits := make(PerDeviceCDIContainerEdits)

	for _, result := range results {
		envs := []string{
			fmt.Sprintf("NPU_DEVICE_%s=%s", result.Device[4:], result.Device),
			fmt.Sprintf("ASCEND_VISIBLE_DEVICES=%s", result.Device[4:]), // 设置ASCEND_VISIBLE_DEVICES为NPU的LogicID
		}
		
		// 检查是否需要设置vNPU规格
		if s.vnpuManager != nil {
			// 需要区分是分片ID还是物理设备ID
			// 如果是整卡分配，那么是物理设备ID
			// 如果是vNPU分配，那么需要查找到对应的模板名称
			
			deviceID := result.Device
			
			// 检查是否为分片的命名模式 npu-x-y
			isSlice := false
			var sliceID string
			
			sliceMatched, err := fmt.Sscanf(deviceID, "%s-%d-%d", &sliceID, &sliceID, &sliceID)
			if err == nil && sliceMatched == 3 {
				isSlice = true
			}
			
			if isSlice {
				// 如果是vNPU分片，获取对应的模板名称
				vnpuSpec, err := s.vnpuManager.GetVnpuSpecsEnv(deviceID)
				if err != nil {
					log.Printf("警告: 获取vNPU规格失败: %v", err)
				} else if vnpuSpec != "" {
					envs = append(envs, fmt.Sprintf("ASCEND_VNPU_SPECS=%s", vnpuSpec))
					log.Printf("为设备 %s 设置vNPU规格: %s", deviceID, vnpuSpec)
				}
			}
		}

		if config.Sharing != nil {
			envs = append(envs, fmt.Sprintf("NPU_DEVICE_%s_SHARING_STRATEGY=%s", result.Device[4:], config.Sharing.Strategy))
		}

		switch {
		case config.Sharing.IsTimeSlicing():
			tsconfig, err := config.Sharing.GetTimeSlicingConfig()
			if err != nil {
				return nil, fmt.Errorf("unable to get time slicing config for device %v: %w", result.Device, err)
			}
			envs = append(envs, fmt.Sprintf("NPU_DEVICE_%s_TIMESLICE_INTERVAL=%v", result.Device[4:], tsconfig.Interval))
		case config.Sharing.IsSpacePartitioning():
			spconfig, err := config.Sharing.GetSpacePartitioningConfig()
			if err != nil {
				return nil, fmt.Errorf("unable to get space partitioning config for device %v: %w", result.Device, err)
			}
			envs = append(envs, fmt.Sprintf("NPU_DEVICE_%s_PARTITION_COUNT=%v", result.Device[4:], spconfig.PartitionCount))
		}

		edits := &cdispec.ContainerEdits{
			Env: envs,
		}

		perDeviceEdits[result.Device] = &cdiapi.ContainerEdits{ContainerEdits: edits}
	}

	return perDeviceEdits, nil
}

func GetOpaqueDeviceConfigs(
	decoder runtime.Decoder,
	driverName string,
	possibleConfigs []resourceapi.DeviceAllocationConfiguration,
) ([]*OpaqueDeviceConfig, error) {
	var classConfigs []resourceapi.DeviceAllocationConfiguration
	var claimConfigs []resourceapi.DeviceAllocationConfiguration
	var candidateConfigs []resourceapi.DeviceAllocationConfiguration
	for _, config := range possibleConfigs {
		switch config.Source {
		case resourceapi.AllocationConfigSourceClass:
			classConfigs = append(classConfigs, config)
		case resourceapi.AllocationConfigSourceClaim:
			claimConfigs = append(claimConfigs, config)
		default:
			return nil, fmt.Errorf("invalid config source: %v", config.Source)
		}
	}
	candidateConfigs = append(candidateConfigs, classConfigs...)
	candidateConfigs = append(candidateConfigs, claimConfigs...)

	var resultConfigs []*OpaqueDeviceConfig
	for _, config := range candidateConfigs {
		if config.DeviceConfiguration.Opaque == nil {
			return nil, fmt.Errorf("only opaque parameters are supported by this driver")
		}

		if config.DeviceConfiguration.Opaque.Driver != driverName {
			continue
		}

		decodedConfig, err := runtime.Decode(decoder, config.DeviceConfiguration.Opaque.Parameters.Raw)
		if err != nil {
			return nil, fmt.Errorf("error decoding config parameters: %w", err)
		}

		resultConfig := &OpaqueDeviceConfig{
			Requests: config.Requests,
			Config:   decodedConfig,
		}

		resultConfigs = append(resultConfigs, resultConfig)
	}

	return resultConfigs, nil
}
