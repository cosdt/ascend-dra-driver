package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"regexp"
	"slices"
	"strings"
	"sync"

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

type (
	AllocatableDevices         map[string]resourceapi.Device
	PreparedDevices            []*PreparedDevice
	PreparedClaims             map[string]PreparedDevices
	PerDeviceCDIContainerEdits map[string]*cdiapi.ContainerEdits
)

type OpaqueDeviceConfig struct {
	Requests []string
	Config   runtime.Object
}

type VnpuTemplateAttribute struct {
	AICORE int
	Memory int
}

type VnpuTemplate struct {
	Name       string
	Attributes VnpuTemplateAttribute
}

type VnpuSlice struct {
	SliceID      string
	TemplateName string
	Allocated    bool
}

type PhysicalNpuState struct {
	DeviceName       string
	LogicID          int32
	ModelName        string
	AvailableSlices  []*VnpuSlice
	AllocatedSlices  []*VnpuSlice
	SupportTemplates map[string]*VnpuTemplate
}

type VnpuManager struct {
	sync.Mutex
	PhysicalNpus map[string]*PhysicalNpuState
	Templates    map[string]*VnpuTemplate
}

type PreparedDevice struct {
	drapbv1.Device
	ContainerEdits *cdiapi.ContainerEdits
}

type DeviceState struct {
	sync.Mutex
	cdi               *CDIHandler
	allocatable       AllocatableDevices
	checkpointManager checkpointmanager.CheckpointManager
	vnpuManager       *VnpuManager
}

func (pds PreparedDevices) GetDevices() []*drapbv1.Device {
	var ds []*drapbv1.Device
	for _, pd := range pds {
		ds = append(ds, &pd.Device)
	}
	return ds
}

// NewDeviceState 初始化并返回 DeviceState
func NewDeviceState(config *Config) (*DeviceState, error) {
	allocatable, vnpuManager, err := enumerateAllPossibleDevices()
	if err != nil {
		return nil, fmt.Errorf("error enumerating all possible devices: %v", err)
	}
	cdi, err := NewCDIHandler(config)
	if err != nil {
		return nil, fmt.Errorf("unable to create CDI handler: %v", err)
	}
	if err = cdi.CreateCommonSpecFile(); err != nil {
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
			if vnpuManager != nil {
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
	if vnpuManager != nil {
		go func() {
			if err := CreatePredefinedResourceClaimTemplates(vnpuManager); err != nil {
				log.Printf("创建预定义ResourceClaimTemplate失败: %v", err)
			}
		}()
	}
	return state, nil
}

// Prepare 完成设备的准备操作
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

	pd, err := s.prepareDevices(claim)
	if err != nil {
		return nil, fmt.Errorf("prepare failed: %v", err)
	}
	if err = s.cdi.CreateClaimSpecFile(claimUID, pd); err != nil {
		return nil, fmt.Errorf("unable to create CDI spec file for claim: %v", err)
	}
	preparedClaims[claimUID] = pd

	if err := s.checkpointManager.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint); err != nil {
		return nil, fmt.Errorf("unable to sync to checkpoint: %v", err)
	}
	return pd.GetDevices(), nil
}

// Unprepare 回收设备的准备操作
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
	if err := s.cdi.DeleteClaimSpecFile(claimUID); err != nil {
		return fmt.Errorf("unable to delete CDI spec file for claim: %v", err)
	}
	delete(preparedClaims, claimUID)
	if err := s.checkpointManager.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint); err != nil {
		return fmt.Errorf("unable to sync to checkpoint: %v", err)
	}
	return nil
}

// prepareDevices 根据Claim分配或获取对应Device
func (s *DeviceState) prepareDevices(claim *resourceapi.ResourceClaim) (PreparedDevices, error) {
	if claim.Status.Allocation == nil {
		return nil, fmt.Errorf("claim not yet allocated")
	}
	configs, err := GetOpaqueDeviceConfigs(configapi.Decoder, DriverName, claim.Status.Allocation.Devices.Config)
	if err != nil {
		return nil, fmt.Errorf("error getting opaque device configs: %v", err)
	}

	// 默认配置插入到最前
	configs = slices.Insert(configs, 0, &OpaqueDeviceConfig{
		Requests: []string{},
		Config:   configapi.DefaultGpuConfig(),
	})

	configResultsMap := make(map[runtime.Object][]*resourceapi.DeviceRequestAllocationResult)

	for _, result := range claim.Status.Allocation.Devices.Results {
		origDevice := result.Device

		// 如果有vnpuManager，先尝试分配vNPU分片
		if s.vnpuManager != nil {
			if err := s.allocateVnpuSlice(&result, configs, origDevice); err != nil {
				log.Printf("警告: 分配vNPU分片失败: %v，尝试使用整卡分配", err)
			}
		}

		if _, ok := s.allocatable[origDevice]; !ok {
			return nil, fmt.Errorf("requested NPU is not allocatable: %v", origDevice)
		}
		// 找到匹配的配置
		for _, c := range slices.Backward(configs) {
			if len(c.Requests) == 0 || slices.Contains(c.Requests, result.Request) {
				configResultsMap[c.Config] = append(configResultsMap[c.Config], &result)
				break
			}
		}
	}

	perDeviceCDIContainerEdits := make(PerDeviceCDIContainerEdits)
	for cfgObj, results := range configResultsMap {
		gpuConfig, ok := cfgObj.(*configapi.GpuConfig)
		if !ok {
			return nil, fmt.Errorf("runtime object is not a recognized configuration")
		}
		if err = gpuConfig.Normalize(); err != nil {
			return nil, fmt.Errorf("error normalizing NPU config: %w", err)
		}
		if err = gpuConfig.Validate(); err != nil {
			return nil, fmt.Errorf("error validating NPU config: %w", err)
		}
		edits, err := s.applyConfig(gpuConfig, results)
		if err != nil {
			return nil, fmt.Errorf("error applying NPU config: %w", err)
		}
		for devID, edit := range edits {
			perDeviceCDIContainerEdits[devID] = edit
		}
	}

	var preparedDevices PreparedDevices
	for _, results := range configResultsMap {
		for _, result := range results {
			dev := &PreparedDevice{
				Device: drapbv1.Device{
					RequestNames: []string{result.Request},
					PoolName:     result.Pool,
					DeviceName:   result.Device,
					CDIDeviceIDs: s.cdi.GetClaimDevices(string(claim.UID), []string{result.Device}),
				},
				ContainerEdits: perDeviceCDIContainerEdits[result.Device],
			}
			preparedDevices = append(preparedDevices, dev)
		}
	}
	return preparedDevices, nil
}

// allocateVnpuSlice 根据用户需求尝试分配vNPU分片
func (s *DeviceState) allocateVnpuSlice(
	result *resourceapi.DeviceRequestAllocationResult,
	configs []*OpaqueDeviceConfig,
	origDevice string,
) error {
	var requestedAicore, requestedMemory int
	var templateName string
	for _, oc := range configs {
		if gpuConfig, ok := oc.Config.(*configapi.GpuConfig); ok {
			if gpuConfig.VnpuSpec != nil && gpuConfig.VnpuSpec.TemplateName != "" {
				templateName = gpuConfig.VnpuSpec.TemplateName
				if tpl, found := s.vnpuManager.Templates[templateName]; found {
					requestedAicore = tpl.Attributes.AICORE
					requestedMemory = tpl.Attributes.Memory
					log.Printf("从模板 %s 获取资源需求: AICORE=%d, Memory=%dGB",
						templateName, requestedAicore, requestedMemory)
					break
				}
			}
		}
	}
	slice, err := s.vnpuManager.AllocateSlice(origDevice, requestedAicore, requestedMemory)
	if err != nil {
		return err
	}
	result.Device = slice.SliceID
	log.Printf("成功为设备 %s 分配vNPU分片: %s (模板: %s, AICORE: %d, Memory: %dGB)",
		origDevice, slice.SliceID, templateName, requestedAicore, requestedMemory)
	return nil
}

// unprepareDevices 回收指定 ClaimUID 下的设备
func (s *DeviceState) unprepareDevices(claimUID string, devices PreparedDevices) error {
	log.Printf("开始释放设备，claimUID: %s", claimUID)
	if s.vnpuManager == nil {
		return nil
	}
	for _, dev := range devices {
		if err := s.vnpuManager.ReleaseSlice(dev.Device.DeviceName); err != nil {
			log.Printf("警告: 释放vNPU分片 %s 失败: %v", dev.Device.DeviceName, err)
		} else {
			log.Printf("成功释放vNPU分片: %s", dev.Device.DeviceName)
		}
	}
	return nil
}

// applyConfig 为每个设备设置相应的环境变量
func (s *DeviceState) applyConfig(
	config *configapi.GpuConfig,
	results []*resourceapi.DeviceRequestAllocationResult,
) (PerDeviceCDIContainerEdits, error) {
	perDeviceEdits := make(PerDeviceCDIContainerEdits)
	for _, result := range results {
		envs := buildBaseEnv(result.Device)
		if s.vnpuManager != nil {
			envs = s.addVnpuEnvIfSlice(envs, result.Device)
		}
		envs = addSharingStrategyEnv(envs, config, result.Device)
		edits := &cdispec.ContainerEdits{Env: envs}
		perDeviceEdits[result.Device] = &cdiapi.ContainerEdits{ContainerEdits: edits}
	}
	return perDeviceEdits, nil
}

// buildBaseEnv 构建基础环境变量，如 ASCEND_VISIBLE_DEVICES
func buildBaseEnv(deviceName string) []string {
	return []string{
		fmt.Sprintf("ASCEND_VISIBLE_DEVICES=%s", deviceName[4:5]),
	}
}

// addVnpuEnvIfSlice 如果是分片格式 npu-x-y，增加 ASCEND_VNPU_SPECS
func (s *DeviceState) addVnpuEnvIfSlice(envs []string, deviceID string) []string {
	r := regexp.MustCompile(`^npu-(\d+)-(\d+)$`)
	if !r.MatchString(deviceID) {
		return envs
	}
	vnpuSpec, err := s.vnpuManager.GetVnpuSpecsEnv(deviceID)
	if err != nil {
		log.Printf("警告: 获取vNPU规格失败: %v", err)
		return envs
	}
	if vnpuSpec != "" {
		envs = append(envs, fmt.Sprintf("ASCEND_VNPU_SPECS=%s", vnpuSpec))
		log.Printf("为设备 %s 设置vNPU规格: %s", deviceID, vnpuSpec)
	}
	return envs
}

// addSharingStrategyEnv 为设备添加共享策略的环境变量
func addSharingStrategyEnv(envs []string, config *configapi.GpuConfig, deviceName string) []string {
	if config.Sharing == nil {
		return envs
	}
	envs = append(envs, fmt.Sprintf("NPU_DEVICE_%s_SHARING_STRATEGY=%s", deviceName[4:], config.Sharing.Strategy))
	switch {
	case config.Sharing.IsTimeSlicing():
		tsconfig, _ := config.Sharing.GetTimeSlicingConfig()
		if tsconfig != nil {
			envs = append(envs, fmt.Sprintf("NPU_DEVICE_%s_TIMESLICE_INTERVAL=%v", deviceName[4:], tsconfig.Interval))
		}
	case config.Sharing.IsSpacePartitioning():
		spconfig, _ := config.Sharing.GetSpacePartitioningConfig()
		if spconfig != nil {
			envs = append(envs, fmt.Sprintf("NPU_DEVICE_%s_PARTITION_COUNT=%v", deviceName[4:], spconfig.PartitionCount))
		}
	}
	return envs
}

// GetOpaqueDeviceConfigs 筛选并解析Opaque配置
func GetOpaqueDeviceConfigs(
	decoder runtime.Decoder,
	driverName string,
	possibleConfigs []resourceapi.DeviceAllocationConfiguration,
) ([]*OpaqueDeviceConfig, error) {
	var classConfigs, claimConfigs []resourceapi.DeviceAllocationConfiguration
	for _, cfg := range possibleConfigs {
		switch cfg.Source {
		case resourceapi.AllocationConfigSourceClass:
			classConfigs = append(classConfigs, cfg)
		case resourceapi.AllocationConfigSourceClaim:
			claimConfigs = append(claimConfigs, cfg)
		default:
			return nil, fmt.Errorf("invalid config source: %v", cfg.Source)
		}
	}
	candidateConfigs := append(classConfigs, claimConfigs...)
	var result []*OpaqueDeviceConfig
	for _, cfg := range candidateConfigs {
		if cfg.DeviceConfiguration.Opaque == nil {
			return nil, fmt.Errorf("only opaque parameters are supported by this driver")
		}
		if cfg.DeviceConfiguration.Opaque.Driver != driverName {
			continue
		}
		decodedConfig, err := runtime.Decode(decoder, cfg.DeviceConfiguration.Opaque.Parameters.Raw)
		if err != nil {
			return nil, fmt.Errorf("error decoding config parameters: %w", err)
		}
		result = append(result, &OpaqueDeviceConfig{
			Requests: cfg.Requests,
			Config:   decodedConfig,
		})
	}
	return result, nil
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
	if requestedAicore == 0 && requestedMemory == 0 {
		return m.allocateFullCard(physicalNpu, deviceName)
	}
	return m.allocateSliceByTemplate(physicalNpu, deviceName, requestedAicore, requestedMemory)
}

// 分配整卡
func (m *VnpuManager) allocateFullCard(npu *PhysicalNpuState, deviceName string) (*VnpuSlice, error) {
	for i, slice := range npu.AvailableSlices {
		if slice.SliceID == deviceName && !slice.Allocated {
			slice.Allocated = true
			npu.AllocatedSlices = append(npu.AllocatedSlices, slice)
			npu.AvailableSlices = append(npu.AvailableSlices[:i], npu.AvailableSlices[i+1:]...)
			log.Printf("成功分配物理NPU %s 的整卡", deviceName)
			return slice, nil
		}
	}
	return nil, fmt.Errorf("物理NPU %s 的整卡已被分配", deviceName)
}

// 根据模板属性分配vNPU分片
func (m *VnpuManager) allocateSliceByTemplate(
	npu *PhysicalNpuState,
	deviceName string,
	requestedAicore, requestedMemory int,
) (*VnpuSlice, error) {
	var bestSlice *VnpuSlice
	bestDiff := math.MaxInt32
	for _, template := range npu.SupportTemplates {
		if template.Attributes.AICORE >= requestedAicore &&
			template.Attributes.Memory >= requestedMemory {
			diff := (template.Attributes.AICORE - requestedAicore) + (template.Attributes.Memory - requestedMemory)
			if diff < bestDiff {
				bestDiff = diff
				bestSlice = &VnpuSlice{
					SliceID:      fmt.Sprintf("%s-%d", deviceName, len(npu.AllocatedSlices)+1),
					TemplateName: template.Name,
					Allocated:    true,
				}
			}
		}
	}
	if bestSlice == nil {
		return nil, fmt.Errorf("找不到满足需求的切分方案: AICORE>=%d, Memory>=%dGB", requestedAicore, requestedMemory)
	}
	npu.AllocatedSlices = append(npu.AllocatedSlices, bestSlice)
	log.Printf("成功分配vNPU分片: %s (AICORE: %d, Memory: %dGB)",
		bestSlice.SliceID, requestedAicore, requestedMemory)
	return bestSlice, nil
}

// CreatePredefinedResourceClaimTemplates 幂等创建/更新 ResourceClaimTemplate
func CreatePredefinedResourceClaimTemplates(vnpuManager *VnpuManager) error {
	log.Printf("开始创建预定义的ResourceClaimTemplate...")
	config, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("获取集群内配置失败: %v", err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("创建Kubernetes客户端失败: %v", err)
	}
	nsName := "npu-vnpu-system"
	if err = ensureNamespaceExists(clientset, nsName); err != nil {
		log.Printf("创建命名空间失败: %v", err)
	}

	uniqueModels := make(map[string]bool)
	uniqueTemplates := make(map[string]*VnpuTemplate)

	for _, pNpu := range vnpuManager.PhysicalNpus {
		modelName := pNpu.ModelName
		if modelName == "" {
			modelName = "unknown"
		}
		uniqueModels[modelName] = true
		for _, tpl := range pNpu.SupportTemplates {
			key := fmt.Sprintf("aicore-%d-mem-%d", tpl.Attributes.AICORE, tpl.Attributes.Memory)
			if _, ok := uniqueTemplates[key]; !ok {
				uniqueTemplates[key] = tpl
			}
		}
	}

	// 为每个唯一型号创建整卡模板
	for modelName := range uniqueModels {
		if err := createFullCardRCT(clientset, nsName, modelName); err != nil {
			log.Printf("创建/更新整卡ResourceClaimTemplate失败: %v", err)
		}
	}

	// 为每个唯一模板 + 每个唯一型号 创建对应RCT
	for _, tpl := range uniqueTemplates {
		for modelName := range uniqueModels {
			if err := createMemoryRCT(clientset, nsName, modelName, tpl); err != nil {
				log.Printf("创建/更新Memory ResourceClaimTemplate失败: %v", err)
			}
			if err := createAicoreRCT(clientset, nsName, modelName, tpl); err != nil {
				log.Printf("创建/更新AICORE ResourceClaimTemplate失败: %v", err)
			}
		}
	}

	log.Printf("预定义ResourceClaimTemplate创建完成")
	return nil
}

// ensureNamespaceExists 确保命名空间存在，不存在则创建
func ensureNamespaceExists(clientset *kubernetes.Clientset, nsName string) error {
	_, err := clientset.CoreV1().Namespaces().Get(context.TODO(), nsName, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !errors.IsNotFound(err) {
		return err
	}
	_, err = clientset.CoreV1().Namespaces().Create(
		context.TODO(),
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}},
		metav1.CreateOptions{},
	)
	return err
}

// createFullCardRCT 创建或更新“整卡”模板
func createFullCardRCT(clientset *kubernetes.Clientset, nsName, modelName string) error {
	safeModel := toSafeModelName(modelName)
	rctName := fmt.Sprintf("npu-%s", safeModel)
	expr := fmt.Sprintf(`device.attributes["%s"].model == "%s"`, DriverDomainName, modelName)
	return upsertResourceClaimTemplate(clientset, nsName, rctName, expr, "")
}

// createMemoryRCT 按 Memory 创建或更新模板
func createMemoryRCT(clientset *kubernetes.Clientset, nsName, modelName string, tpl *VnpuTemplate) error {
	safeModel := toSafeModelName(modelName)
	rctName := fmt.Sprintf("npu-%s-mem%d", safeModel, tpl.Attributes.Memory)
	expr := fmt.Sprintf(`device.attributes["%s"].memory >= %d && device.attributes["%s"].model == "%s"`,
		DriverDomainName, tpl.Attributes.Memory, DriverDomainName, modelName)
	return upsertResourceClaimTemplate(clientset, nsName, rctName, expr, tpl.Name)
}

// createAicoreRCT 按 AICORE 创建或更新模板
func createAicoreRCT(clientset *kubernetes.Clientset, nsName, modelName string, tpl *VnpuTemplate) error {
	safeModel := toSafeModelName(modelName)
	rctName := fmt.Sprintf("npu-%s-aicore%d", safeModel, tpl.Attributes.AICORE)
	expr := fmt.Sprintf(`device.attributes["%s"].aicore >= %d && device.attributes["%s"].model == "%s"`,
		DriverDomainName, tpl.Attributes.AICORE, DriverDomainName, modelName)
	return upsertResourceClaimTemplate(clientset, nsName, rctName, expr, tpl.Name)
}

// toSafeModelName 去掉型号中多余字符，转换为小写
func toSafeModelName(model string) string {
	model = strings.ReplaceAll(model, " ", "-")
	model = strings.ReplaceAll(model, "/", "-")
	return strings.ToLower(model)
}

// upsertResourceClaimTemplate 幂等创建/更新 RCT
func upsertResourceClaimTemplate(clientset *kubernetes.Clientset, ns, name, expr, tplName string) error {
	want, err := buildResourceClaimTemplate(ns, name, expr, tplName)
	if err != nil {
		return err
	}
	got, getErr := clientset.ResourceV1beta1().ResourceClaimTemplates(ns).Get(
		context.TODO(), name, metav1.GetOptions{},
	)
	if errors.IsNotFound(getErr) {
		_, createErr := clientset.ResourceV1beta1().ResourceClaimTemplates(ns).Create(
			context.TODO(), want, metav1.CreateOptions{},
		)
		if createErr != nil {
			return fmt.Errorf("创建ResourceClaimTemplate失败: %v", createErr)
		}
		log.Printf("成功创建ResourceClaimTemplate: %s", name)
		return nil
	}
	if getErr != nil {
		return fmt.Errorf("获取ResourceClaimTemplate失败: %v", getErr)
	}
	if !rctEquals(got, want) {
		want.ObjectMeta.ResourceVersion = got.ObjectMeta.ResourceVersion
		_, updateErr := clientset.ResourceV1beta1().ResourceClaimTemplates(ns).Update(
			context.TODO(), want, metav1.UpdateOptions{},
		)
		if updateErr != nil {
			return fmt.Errorf("更新ResourceClaimTemplate失败: %v", updateErr)
		}
		log.Printf("成功更新ResourceClaimTemplate: %s", name)
	}
	return nil
}

// buildResourceClaimTemplate 生成目标ResourceClaimTemplate
func buildResourceClaimTemplate(ns, name, celExpression, tplName string) (*resourceapi.ResourceClaimTemplate, error) {
	paramObj := map[string]interface{}{
		"apiVersion": "gpu.resource.example.com/v1alpha1",
		"kind":       "GpuConfig",
		"vnpuSpec": map[string]interface{}{
			"templateName": tplName,
		},
	}
	raw, err := json.Marshal(paramObj)
	if err != nil {
		return nil, err
	}
	return &resourceapi.ResourceClaimTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Spec: resourceapi.ResourceClaimTemplateSpec{
			Spec: resourceapi.ResourceClaimSpec{
				Devices: resourceapi.DeviceClaim{
					Requests: []resourceapi.DeviceRequest{{
						Name:            "npu",
						DeviceClassName: "npu.example.com",
						Selectors: []resourceapi.DeviceSelector{{
							CEL: &resourceapi.CELDeviceSelector{Expression: celExpression},
						}},
					}},
					Config: []resourceapi.DeviceClaimConfiguration{{
						DeviceConfiguration: resourceapi.DeviceConfiguration{
							Opaque: &resourceapi.OpaqueDeviceConfiguration{
								Driver: DriverName,
								Parameters: runtime.RawExtension{
									Raw: raw,
								},
							},
						},
					}},
				},
			},
		},
	}, nil
}

// rctEquals 简单对比 Spec 是否一致（可根据需求调整）
func rctEquals(a, b *resourceapi.ResourceClaimTemplate) bool {
	aSpec, _ := json.Marshal(a.Spec)
	bSpec, _ := json.Marshal(b.Spec)
	return string(aSpec) == string(bSpec)
}
