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
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	resourceapi "k8s.io/api/resource/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	drapbv1 "k8s.io/kubelet/pkg/apis/dra/v1beta1"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"

	configapi "Ascend-dra-driver/api/example.com/resource/gpu/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
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

// VnpuTemplateAttribute 存储vNPU模板的资源属性
type VnpuTemplateAttribute struct {
	AICORE int
	Memory int // 单位GB
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
	DeviceName       string                   // 设备名称，例如: npu-0
	LogicID          int32                    // 逻辑ID
	ModelName        string                   // 设备型号，例如: Ascend910、Ascend310P等
	AvailableSlices  []*VnpuSlice             // 可用的vNPU分片
	AllocatedSlices  []*VnpuSlice             // 已分配的vNPU分片
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
	allocatable, vnpuManager, err := enumerateAllPossibleDevices()
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
			// 创建预定义的ResourceClaimTemplate
			if vnpuManager != nil {
				// 异步创建，避免阻塞驱动启动
				if err := CreatePredefinedResourceClaimTemplates(vnpuManager); err != nil {
					log.Printf("创建预定义ResourceClaimTemplate失败: %v", err)
				}
			}
			return state, nil
		}
	}

	checkpoint := newCheckpoint()
	if err := state.checkpointManager.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint); err != nil {
		return nil, fmt.Errorf("unable to sync to checkpoint: %v", err)
	}

	// 创建预定义的ResourceClaimTemplate
	if vnpuManager != nil {
		go func() {
			// 异步创建，避免阻塞驱动启动
			if err := CreatePredefinedResourceClaimTemplates(vnpuManager); err != nil {
				log.Printf("创建预定义ResourceClaimTemplate失败: %v", err)
			}
		}()
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

	// Add the default NPU Config to the front of the config list with the
	// lowest precedence.
	configs = slices.Insert(configs, 0, &OpaqueDeviceConfig{
		Requests: []string{},
		Config:   configapi.DefaultGpuConfig(),
	})

	configResultsMap := make(map[runtime.Object][]*resourceapi.DeviceRequestAllocationResult)
	for _, result := range claim.Status.Allocation.Devices.Results {
		origDevice := result.Device

		// 尝试分配vNPU分片或整卡
		if s.vnpuManager != nil {
			// 从配置中获取算力需求
			var requestedAicore, requestedMemory int
			var templateName string

			for _, config := range configs {
				if gpuConfig, ok := config.Config.(*configapi.GpuConfig); ok {
					if gpuConfig.VnpuSpec != nil && gpuConfig.VnpuSpec.TemplateName != "" {
						// 从VnpuSpec中获取模板名称
						templateName = gpuConfig.VnpuSpec.TemplateName
						// 根据模板名称查找对应的资源配置
						if template, ok := s.vnpuManager.Templates[templateName]; ok {
							requestedAicore = template.Attributes.AICORE
							requestedMemory = template.Attributes.Memory
							log.Printf("从模板 %s 获取资源需求: AICORE=%d, Memory=%dGB",
								templateName, requestedAicore, requestedMemory)
						}
						break
					}
				}
			}

			// 根据算力需求选择合适的切分方案
			slice, err := s.vnpuManager.AllocateSlice(origDevice, requestedAicore, requestedMemory)
			if err != nil {
				log.Printf("警告: 分配vNPU分片失败: %v，将使用整卡分配", err)
			} else {
				// 替换设备ID为分片ID
				result.Device = slice.SliceID
				log.Printf("成功为设备 %s 分配vNPU分片: %s (模板: %s, AICORE: %d, Memory: %dGB)",
					origDevice, slice.SliceID, templateName, requestedAicore, requestedMemory)
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
			var prefix string
			var num1, num2 int

			sliceMatched, err := fmt.Sscanf(deviceID, "%s-%d-%d", &prefix, &num1, &num2)
			if err == nil && sliceMatched == 3 && prefix == "npu" {
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

// AllocateSlice 根据算力需求分配vNPU分片
func (m *VnpuManager) AllocateSlice(deviceName string, requestedAicore, requestedMemory int) (*VnpuSlice, error) {
	m.Lock()
	defer m.Unlock()

	log.Printf("尝试分配vNPU切片，设备: %s, 需求: AICORE=%d, Memory=%dGB", deviceName, requestedAicore, requestedMemory)

	physicalNpu, ok := m.PhysicalNpus[deviceName]
	if !ok {
		return nil, fmt.Errorf("找不到物理NPU设备: %s", deviceName)
	}

	// 如果需求为0，尝试整卡分配
	if requestedAicore == 0 && requestedMemory == 0 {
		for i, slice := range physicalNpu.AvailableSlices {
			if slice.SliceID == deviceName && !slice.Allocated {
				slice.Allocated = true
				physicalNpu.AllocatedSlices = append(physicalNpu.AllocatedSlices, slice)
				physicalNpu.AvailableSlices = append(physicalNpu.AvailableSlices[:i], physicalNpu.AvailableSlices[i+1:]...)
				log.Printf("成功分配物理NPU %s 的整卡", deviceName)
				return slice, nil
			}
		}
		return nil, fmt.Errorf("物理NPU %s 的整卡已被分配", deviceName)
	}

	// 根据算力需求选择合适的切分方案
	var bestSlice *VnpuSlice
	var bestDiff int = math.MaxInt32

	for _, template := range physicalNpu.SupportTemplates {
		// 检查模板是否满足需求
		if template.Attributes.AICORE >= requestedAicore &&
			template.Attributes.Memory >= requestedMemory {
			// 计算与需求的差异
			diff := (template.Attributes.AICORE - requestedAicore) +
				(template.Attributes.Memory - requestedMemory)

			// 选择差异最小的方案
			if diff < bestDiff {
				bestDiff = diff
				// 创建新的分片
				bestSlice = &VnpuSlice{
					SliceID:      fmt.Sprintf("%s-%d", deviceName, len(physicalNpu.AllocatedSlices)+1),
					TemplateName: template.Name,
					Allocated:    true,
				}
			}
		}
	}

	if bestSlice == nil {
		return nil, fmt.Errorf("找不到满足需求的切分方案: AICORE>=%d, Memory>=%dGB",
			requestedAicore, requestedMemory)
	}

	// 分配选中的分片
	physicalNpu.AllocatedSlices = append(physicalNpu.AllocatedSlices, bestSlice)
	log.Printf("成功分配vNPU分片: %s (AICORE: %d, Memory: %dGB)",
		bestSlice.SliceID, requestedAicore, requestedMemory)

	return bestSlice, nil
}

// 在DeviceState的初始化或者vnpuManager初始化后调用
func CreatePredefinedResourceClaimTemplates(vnpuManager *VnpuManager) error {
	log.Printf("开始创建预定义的ResourceClaimTemplate...")

	// 创建K8s客户端
	config, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("获取集群内配置失败: %v", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("创建Kubernetes客户端失败: %v", err)
	}

	// 创建专用的命名空间
	nsName := "npu-vnpu-system"
	_, err = clientset.CoreV1().Namespaces().Create(
		context.TODO(),
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: nsName,
			},
		},
		metav1.CreateOptions{},
	)
	if err != nil && !errors.IsAlreadyExists(err) {
		log.Printf("创建命名空间失败: %v", err)
	}

	// 使用map记录所有唯一的型号
	uniqueModels := make(map[string]bool)

	// 使用map记录所有唯一的模板，以属性(AICORE、Memory)作为键
	uniqueTemplates := make(map[string]*VnpuTemplate)

	// 使用map记录已更新的模板，避免重复更新
	updatedTemplates := make(map[string]bool)

	// 首先收集所有唯一的设备型号和模板
	for _, physicalNpu := range vnpuManager.PhysicalNpus {
		// 记录唯一的型号
		modelName := physicalNpu.ModelName
		if modelName == "" {
			modelName = "unknown"
		}
		uniqueModels[modelName] = true

		// 记录唯一的模板
		for _, template := range physicalNpu.SupportTemplates {
			key := fmt.Sprintf("aicore-%d-mem-%d", template.Attributes.AICORE, template.Attributes.Memory)
			if _, exists := uniqueTemplates[key]; !exists {
				uniqueTemplates[key] = template
			}
		}
	}

	// 为每个唯一型号创建整卡模板
	for modelName := range uniqueModels {
		// 去掉型号中可能存在的空格和特殊字符，确保名称合法
		safeModel := strings.ReplaceAll(modelName, " ", "-")
		safeModel = strings.ReplaceAll(safeModel, "/", "-")
		safeModel = strings.ToLower(safeModel)

		// 为该型号创建整卡模板
		fullCardName := fmt.Sprintf("npu-%s", safeModel)

		// 如果已经更新过这个模板，跳过
		if updatedTemplates[fullCardName] {
			continue
		}

		// 直接使用型号匹配
		celExpression := fmt.Sprintf("device.attributes[\""+DriverDomainName+"\"].model == \"%s\"", modelName)

		err = createResourceClaimTemplate(clientset, nsName, fullCardName,
			celExpression, "") // 整卡没有特定模板名称
		if err != nil {
			log.Printf("创建/更新整卡ResourceClaimTemplate %s 失败: %v", fullCardName, err)
		} else {
			log.Printf("成功创建/更新整卡ResourceClaimTemplate: %s", fullCardName)
			updatedTemplates[fullCardName] = true
		}
	}

	// 为每个唯一模板和唯一型号的组合创建ResourceClaimTemplate
	for _, template := range uniqueTemplates {
		for modelName := range uniqueModels {
			// 去掉型号中可能存在的空格和特殊字符，确保名称合法
			safeModel := strings.ReplaceAll(modelName, " ", "-")
			safeModel = strings.ReplaceAll(safeModel, "/", "-")
			safeModel = strings.ToLower(safeModel)

			// 基于内存创建ResourceClaimTemplate
			memName := fmt.Sprintf("npu-%s-mem%d", safeModel, template.Attributes.Memory)

			// 如果已经更新过这个模板，跳过
			if updatedTemplates[memName] {
				continue
			}

			memoryExpression := fmt.Sprintf("device.attributes[\""+DriverDomainName+"\"]."+
				"memory >= %d && device.attributes[\""+DriverDomainName+"\"].model == \"%s\"",
				template.Attributes.Memory, modelName)

			err = createResourceClaimTemplate(clientset, nsName, memName,
				memoryExpression, template.Name)
			if err != nil {
				log.Printf("创建/更新ResourceClaimTemplate %s 失败: %v", memName, err)
			} else {
				log.Printf("成功创建/更新ResourceClaimTemplate: %s", memName)
				updatedTemplates[memName] = true
			}

			// 基于AICORE创建ResourceClaimTemplate
			aicoreName := fmt.Sprintf("npu-%s-aicore%d", safeModel, template.Attributes.AICORE)

			// 如果已经更新过这个模板，跳过
			if updatedTemplates[aicoreName] {
				continue
			}

			aicoreExpression := fmt.Sprintf("device.attributes[\""+DriverDomainName+"\"].aicore >= %d && device.attributes[\""+DriverDomainName+"\"].model == \"%s\"",
				template.Attributes.AICORE, modelName)

			err = createResourceClaimTemplate(clientset, nsName, aicoreName,
				aicoreExpression, template.Name)
			if err != nil {
				log.Printf("创建/更新ResourceClaimTemplate %s 失败: %v", aicoreName, err)
			} else {
				log.Printf("成功创建/更新ResourceClaimTemplate: %s", aicoreName)
				updatedTemplates[aicoreName] = true
			}
		}
	}

	log.Printf("预定义ResourceClaimTemplate创建完成")
	return nil
}

// 创建单个ResourceClaimTemplate
func createResourceClaimTemplate(clientset *kubernetes.Clientset, namespace, name, celExpression, templateName string) error {
	paramObj := map[string]interface{}{
		"apiVersion": "gpu.resource.example.com/v1alpha1",
		"kind":       "GpuConfig",
		"vnpuSpec": map[string]interface{}{
			"templateName": templateName,
		},
	}

	// 序列化为 JSON
	raw, err := json.Marshal(paramObj)
	if err != nil {
		panic(err)
	}

	// 构建ResourceClaimTemplate对象
	rct := &resourceapi.ResourceClaimTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: resourceapi.ResourceClaimTemplateSpec{
			Spec: resourceapi.ResourceClaimSpec{
				Devices: resourceapi.DeviceClaim{
					Requests: []resourceapi.DeviceRequest{
						{
							Name:            "npu",
							DeviceClassName: "npu.example.com",
							Selectors: []resourceapi.DeviceSelector{
								{
									CEL: &resourceapi.CELDeviceSelector{
										Expression: celExpression,
									},
								},
							},
						},
					},
					Config: []resourceapi.DeviceClaimConfiguration{
						{
							DeviceConfiguration: resourceapi.DeviceConfiguration{
								Opaque: &resourceapi.OpaqueDeviceConfiguration{
									Driver: DriverName,
									Parameters: runtime.RawExtension{
										Raw: raw,
									},
								},
							},
						},
					},
				},
			},
		},
	}

	// 尝试获取已存在的模板
	_, err = clientset.ResourceV1beta1().ResourceClaimTemplates(namespace).Get(
		context.TODO(),
		name,
		metav1.GetOptions{},
	)

	if err == nil {
		// 如果模板存在，先删除它
		err = clientset.ResourceV1beta1().ResourceClaimTemplates(namespace).Delete(
			context.TODO(),
			name,
			metav1.DeleteOptions{},
		)
		if err != nil {
			return fmt.Errorf("删除已存在的ResourceClaimTemplate失败: %v", err)
		}
		// 等待一小段时间确保删除完成
		time.Sleep(time.Second)
	}

	// 创建新的模板
	_, err = clientset.ResourceV1beta1().ResourceClaimTemplates(namespace).Create(
		context.TODO(),
		rct,
		metav1.CreateOptions{},
	)

	return err
}
