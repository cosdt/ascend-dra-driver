package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
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
	"k8s.io/utils/ptr"

	configapi "Ascend-dra-driver/api/example.com/resource/gpu/v1alpha1"

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
	Type         string
}

type PhysicalNpuState struct {
	DeviceName       string
	PhysicalDeviceID string
	LogicID          int32
	ModelName        string
	AvailableSlices  []*VnpuSlice
	AllocatedSlices  []*VnpuSlice
	SupportTemplates map[string]*VnpuTemplate
	NextSliceIndex   int
}

type DeviceUpdateCallback func(deviceName string, physicalNpu *PhysicalNpuState)

type VnpuManager struct {
	sync.Mutex
	PhysicalNpus         map[string]*PhysicalNpuState
	Templates            map[string]*VnpuTemplate
	deviceUpdateCallback DeviceUpdateCallback
}

func (m *VnpuManager) SetDeviceUpdateCallback(callback DeviceUpdateCallback) {
	m.Lock()
	defer m.Unlock()
	m.deviceUpdateCallback = callback
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

// NewDeviceState initializes and returns a DeviceState
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

	if vnpuManager != nil {
		vnpuManager.SetDeviceUpdateCallback(func(deviceName string, physicalNpu *PhysicalNpuState) {
			if added := state.UpdateAllocatableDevice(deviceName, physicalNpu); added {
				log.Printf("Added new device %s to allocatable devices", deviceName)
			}
		})
	}

	checkpoints, err := state.checkpointManager.ListCheckpoints()
	if err != nil {
		return nil, fmt.Errorf("unable to list checkpoints: %v", err)
	}
	for _, c := range checkpoints {
		if c == DriverPluginCheckpointFile {
			if vnpuManager != nil {
				if err := CreatePredefinedDeviceClasses(vnpuManager); err != nil {
					log.Printf("Failed to create predefined DeviceClasses: %v", err)
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
			if err := CreatePredefinedDeviceClasses(vnpuManager); err != nil {
				log.Printf("Failed to create predefined DeviceClasses: %v", err)
			}
		}()
	}
	return state, nil
}

// Prepare completes device preparation
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

// Unprepare reclaims device resources
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

// prepareDevices allocates or retrieves the corresponding Device based on the Claim
func (s *DeviceState) prepareDevices(claim *resourceapi.ResourceClaim) (PreparedDevices, error) {
	if claim.Status.Allocation == nil {
		return nil, fmt.Errorf("claim not yet allocated")
	}
	configs, err := GetOpaqueDeviceConfigs(configapi.Decoder, DriverName, claim.Status.Allocation.Devices.Config)
	if err != nil {
		return nil, fmt.Errorf("error getting opaque device configs: %v", err)
	}

	// Insert default config at the beginning
	configs = slices.Insert(configs, 0, &OpaqueDeviceConfig{
		Requests: []string{},
		Config:   configapi.DefaultGpuConfig(),
	})

	configResultsMap := make(map[runtime.Object][]*resourceapi.DeviceRequestAllocationResult)

	for _, result := range claim.Status.Allocation.Devices.Results {
		origDevice := result.Device

		// If vnpuManager is available, try to allocate vNPU slices first
		if s.vnpuManager != nil {
			if err := s.allocateVnpuSlice(&result, configs, origDevice); err != nil {
				log.Printf("Warning: failed to allocate vNPU slice: %v, attempting to use full card allocation", err)
			}
		}

		if _, ok := s.allocatable[origDevice]; !ok {
			return nil, fmt.Errorf("requested NPU is not allocatable: %v", origDevice)
		}
		// Find matching config
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

// allocateVnpuSlice tries to allocate a vNPU slice based on user requirements
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
					log.Printf("Obtained resource requirements from template %s: AICORE=%d, Memory=%dGB",
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
	log.Printf("Successfully allocated vNPU slice for device %s: %s (template: %s, AICORE: %d, Memory: %dGB)",
		origDevice, slice.SliceID, templateName, requestedAicore, requestedMemory)
	return nil
}

// unprepareDevices reclaims devices under the specified ClaimUID
func (s *DeviceState) unprepareDevices(claimUID string, devices PreparedDevices) error {
	log.Printf("Starting to release devices, claimUID: %s", claimUID)
	if s.vnpuManager == nil {
		return nil
	}
	for _, dev := range devices {
		if err := s.vnpuManager.ReleaseSlice(dev.Device.DeviceName); err != nil {
			log.Printf("Warning: failed to release vNPU slice %s: %v", dev.Device.DeviceName, err)
		} else {
			log.Printf("Successfully released vNPU slice: %s", dev.Device.DeviceName)
		}
	}
	return nil
}

// applyConfig sets corresponding environment variables for each device
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

// buildBaseEnv constructs basic environment variables such as ASCEND_VISIBLE_DEVICES
func buildBaseEnv(deviceName string) []string {
	return []string{
		fmt.Sprintf("ASCEND_VISIBLE_DEVICES=%s", deviceName[4:5]),
	}
}

// addVnpuEnvIfSlice adds ASCEND_VNPU_SPECS if it is a slice format npu-x-y
func (s *DeviceState) addVnpuEnvIfSlice(envs []string, deviceID string) []string {
	r := regexp.MustCompile(`^npu-(\d+)-(\d+)$`)
	if !r.MatchString(deviceID) {
		return envs
	}
	vnpuSpec, err := s.vnpuManager.GetVnpuSpecsEnv(deviceID)
	if err != nil {
		log.Printf("Warning: failed to get vNPU specs: %v", err)
		return envs
	}
	if vnpuSpec != "" {
		envs = append(envs, fmt.Sprintf("ASCEND_VNPU_SPECS=%s", vnpuSpec))
		log.Printf("Set vNPU specs for device %s: %s", deviceID, vnpuSpec)
	}
	return envs
}

// addSharingStrategyEnv adds environment variables for the sharing strategy
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

// GetOpaqueDeviceConfigs filters and decodes opaque configurations
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

// AllocateSlice allocates a vNPU slice based on the requested computational resources
func (m *VnpuManager) AllocateSlice(deviceName string, requestedAicore, requestedMemory int) (*VnpuSlice, error) {
	m.Lock()
	defer m.Unlock()
	log.Printf("Attempting to allocate vNPU slice, device: %s, requirements: AICORE=%d, Memory=%dGB", deviceName, requestedAicore, requestedMemory)
	physicalNpu, ok := m.PhysicalNpus[deviceName]
	if !ok {
		return nil, fmt.Errorf("physical NPU not found: %s", deviceName)
	}
	if requestedAicore == 0 && requestedMemory == 0 {
		return m.allocateFullCard(physicalNpu, deviceName)
	}
	return m.allocateSliceByTemplate(physicalNpu, deviceName, requestedAicore, requestedMemory)
}

// allocateFullCard allocates the entire card
func (m *VnpuManager) allocateFullCard(npu *PhysicalNpuState, deviceName string) (*VnpuSlice, error) {
	for i, slice := range npu.AvailableSlices {
		if slice.SliceID == deviceName && !slice.Allocated {
			slice.Allocated = true
			npu.AllocatedSlices = append(npu.AllocatedSlices, slice)
			npu.AvailableSlices = append(npu.AvailableSlices[:i], npu.AvailableSlices[i+1:]...)
			log.Printf("Successfully allocated the full physical NPU slice %s", deviceName)
			return slice, nil
		}
	}
	return nil, fmt.Errorf("the slice %s has already been allocated", deviceName)
}

// allocateSliceByTemplate allocates a vNPU slice based on template attributes
func (m *VnpuManager) allocateSliceByTemplate(
	npu *PhysicalNpuState,
	deviceName string,
	requestedAicore, requestedMemory int,
) (*VnpuSlice, error) {
	var bestTemplate *VnpuTemplate
	bestDiff := math.MaxInt32
	for _, template := range npu.SupportTemplates {
		if template.Attributes.AICORE >= requestedAicore &&
			template.Attributes.Memory >= requestedMemory {
			diff := (template.Attributes.AICORE - requestedAicore) + (template.Attributes.Memory - requestedMemory)
			if diff < bestDiff {
				bestDiff = diff
				bestTemplate = template
			}
		}
	}
	if bestTemplate == nil {
		return nil, fmt.Errorf("no partition scheme found that meets the requirements: AICORE>=%d, Memory>=%dGB", requestedAicore, requestedMemory)
	}

	var currentSlice *VnpuSlice
	var sliceIndex int
	for i, slice := range npu.AvailableSlices {
		if slice.SliceID == deviceName && !slice.Allocated {
			currentSlice = slice
			sliceIndex = i
			break
		}
	}

	if currentSlice == nil {
		return nil, fmt.Errorf("cannot find available slice %s", deviceName)
	}

	npu.AvailableSlices = append(npu.AvailableSlices[:sliceIndex], npu.AvailableSlices[sliceIndex+1:]...)

	currentSlice.TemplateName = bestTemplate.Name
	currentSlice.Allocated = true

	npu.AllocatedSlices = append(npu.AllocatedSlices, currentSlice)

	newSliceID := fmt.Sprintf("npu-%d-%d", npu.LogicID, npu.NextSliceIndex)
	newSlice := &VnpuSlice{
		SliceID:      newSliceID,
		TemplateName: "",
		Allocated:    false,
		Type:         "vNPU",
	}
	npu.AvailableSlices = append(npu.AvailableSlices, newSlice)

	if m.deviceUpdateCallback != nil {
		m.deviceUpdateCallback(newSliceID, npu)
	}

	npu.NextSliceIndex++

	log.Printf("Successfully allocated vNPU slice: %s with template %s (AICORE: %d, Memory: %dGB)",
		currentSlice.SliceID, bestTemplate.Name, bestTemplate.Attributes.AICORE, bestTemplate.Attributes.Memory)
	log.Printf("Created new available slice: %s representing remaining resources", newSliceID)

	return currentSlice, nil
}

// CreatePredefinedDeviceClasses idempotently creates/updates DeviceClasses
func CreatePredefinedDeviceClasses(vnpuManager *VnpuManager) error {
	log.Printf("Starting to create predefined DeviceClasses...")
	config, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("failed to get in-cluster config: %v", err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create Kubernetes client: %v", err)
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

	// Create a full-card DeviceClass for each unique model
	for modelName := range uniqueModels {
		if err := createFullCardDeviceClass(clientset, modelName); err != nil {
			log.Printf("Failed to create/update full-card DeviceClass: %v", err)
		}
	}

	// Create the corresponding DeviceClass for each unique template and each unique model
	for _, tpl := range uniqueTemplates {
		for modelName := range uniqueModels {
			if err := createMemoryDeviceClass(clientset, modelName, tpl); err != nil {
				log.Printf("Failed to create/update Memory DeviceClass: %v", err)
			}
			if err := createAicoreDeviceClass(clientset, modelName, tpl); err != nil {
				log.Printf("Failed to create/update AICORE DeviceClass: %v", err)
			}
		}
	}

	log.Printf("Predefined DeviceClass creation completed")
	return nil
}

// createFullCardDeviceClass creates or updates a "full-card" DeviceClass
func createFullCardDeviceClass(clientset *kubernetes.Clientset, modelName string) error {
	safeModel := toSafeModelName(modelName)
	dcName := fmt.Sprintf("npu-%s.example.com", safeModel)
	expr := fmt.Sprintf(`device.attributes["%s"].model == "%s" && device.attributes["%s"].type == "NPU"`,
		DriverDomainName, modelName, DriverDomainName)
	return upsertDeviceClass(clientset, dcName, expr, "")
}

// createMemoryDeviceClass creates or updates a DeviceClass based on memory
func createMemoryDeviceClass(clientset *kubernetes.Clientset, modelName string, tpl *VnpuTemplate) error {
	safeModel := toSafeModelName(modelName)
	dcName := fmt.Sprintf("npu-%s-mem%d.example.com", safeModel, tpl.Attributes.Memory)
	expr := fmt.Sprintf(`device.attributes["%s"].memory >= %d && device.attributes["%s"].model == "%s"`,
		DriverDomainName, tpl.Attributes.Memory, DriverDomainName, modelName)
	return upsertDeviceClass(clientset, dcName, expr, tpl.Name)
}

// createAicoreDeviceClass creates or updates a DeviceClass based on AICORE
func createAicoreDeviceClass(clientset *kubernetes.Clientset, modelName string, tpl *VnpuTemplate) error {
	safeModel := toSafeModelName(modelName)
	dcName := fmt.Sprintf("npu-%s-aicore%d.example.com", safeModel, tpl.Attributes.AICORE)
	expr := fmt.Sprintf(`device.attributes["%s"].aicore >= %d && device.attributes["%s"].model == "%s"`,
		DriverDomainName, tpl.Attributes.AICORE, DriverDomainName, modelName)
	return upsertDeviceClass(clientset, dcName, expr, tpl.Name)
}

// upsertDeviceClass idempotently creates/updates a DeviceClass
func upsertDeviceClass(clientset *kubernetes.Clientset, name, expr, tpl string) error {
	want, err := buildDeviceClass(name, expr, tpl)
	if err != nil {
		return err
	}

	got, getErr := clientset.ResourceV1beta1().DeviceClasses().Get(
		context.TODO(), name, metav1.GetOptions{},
	)
	if errors.IsNotFound(getErr) {
		_, createErr := clientset.ResourceV1beta1().DeviceClasses().Create(
			context.TODO(), want, metav1.CreateOptions{},
		)
		if createErr != nil {
			return fmt.Errorf("failed to create DeviceClass: %v", createErr)
		}
		log.Printf("Successfully created DeviceClass: %s", name)
		return nil
	}
	if getErr != nil {
		return fmt.Errorf("failed to get DeviceClass: %v", getErr)
	}

	if !deviceClassEquals(got, want) {
		want.ObjectMeta.ResourceVersion = got.ObjectMeta.ResourceVersion
		_, updateErr := clientset.ResourceV1beta1().DeviceClasses().Update(
			context.TODO(), want, metav1.UpdateOptions{},
		)
		if updateErr != nil {
			return fmt.Errorf("failed to update DeviceClass: %v", updateErr)
		}
		log.Printf("Successfully updated DeviceClass: %s", name)
	}

	return nil
}

// buildDeviceClass generates the target DeviceClass
func buildDeviceClass(name, celExpression, tplName string) (*resourceapi.DeviceClass, error) {
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
	return &resourceapi.DeviceClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: resourceapi.DeviceClassSpec{
			Selectors: []resourceapi.DeviceSelector{{
				CEL: &resourceapi.CELDeviceSelector{Expression: celExpression},
			}},
			Config: []resourceapi.DeviceClassConfiguration{
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
	}, nil
}

// deviceClassEquals performs a simple comparison of the Spec fields
func deviceClassEquals(a, b *resourceapi.DeviceClass) bool {
	aSpec, _ := json.Marshal(a.Spec)
	bSpec, _ := json.Marshal(b.Spec)
	return string(aSpec) == string(bSpec)
}

// toSafeModelName removes extra characters from model and converts to lowercase
func toSafeModelName(model string) string {
	model = strings.ReplaceAll(model, " ", "-")
	model = strings.ReplaceAll(model, "/", "-")
	return strings.ToLower(model)
}

func (s *DeviceState) UpdateAllocatableDevice(deviceName string, physicalNpu *PhysicalNpuState) bool {
	_, exists := s.allocatable[deviceName]
	if exists {
		return false
	}

	var sliceType string = "NPU"
	for _, slice := range physicalNpu.AvailableSlices {
		if slice.SliceID == deviceName {
			sliceType = slice.Type
			break
		}
	}

	uuidStr := fmt.Sprintf("%s-%d", os.Getenv("NODE_NAME"), physicalNpu.LogicID)

	devAttributes := map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
		DriverDomain + "index": {IntValue: ptr.To(int64(physicalNpu.LogicID))},
		DriverDomain + "uuid":  {StringValue: ptr.To(uuidStr)},
		DriverDomain + "model": {StringValue: ptr.To(physicalNpu.ModelName)},
		DriverDomain + "type":  {StringValue: ptr.To(sliceType)},
	}

	if s.vnpuManager != nil {
		maxAicore, maxMemory := 0, 0
		for _, tpl := range physicalNpu.SupportTemplates {
			if tpl.Attributes.AICORE > maxAicore {
				maxAicore = tpl.Attributes.AICORE
			}
			if tpl.Attributes.Memory > maxMemory {
				maxMemory = tpl.Attributes.Memory
			}
		}

		devAttributes[DriverDomain+"aicore"] = resourceapi.DeviceAttribute{IntValue: ptr.To(int64(maxAicore))}
		devAttributes[DriverDomain+"memory"] = resourceapi.DeviceAttribute{IntValue: ptr.To(int64(maxMemory))}
	}

	device := resourceapi.Device{
		Name: deviceName,
		Basic: &resourceapi.BasicDevice{
			Attributes: devAttributes,
		},
	}

	s.allocatable[deviceName] = device
	log.Printf("Added new allocatable NPU device: %s, Type: %s, Model: %s", deviceName, sliceType, physicalNpu.ModelName)
	return true
}
