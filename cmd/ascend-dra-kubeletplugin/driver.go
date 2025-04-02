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
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	coreclientset "k8s.io/client-go/kubernetes"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/klog/v2"

	drapbv1 "k8s.io/kubelet/pkg/apis/dra/v1beta1"
)

var _ drapbv1.DRAPluginServer = &driver{}

type driver struct {
	client coreclientset.Interface
	plugin kubeletplugin.DRAPlugin
	state  *DeviceState
}

func NewDriver(ctx context.Context, config *Config) (*driver, error) {
	driver := &driver{
		client: config.coreclient,
	}

	state, err := NewDeviceState(config)
	if err != nil {
		return nil, err
	}
	driver.state = state

	plugin, err := kubeletplugin.Start(
		ctx,
		[]any{driver},
		kubeletplugin.KubeClient(config.coreclient),
		kubeletplugin.NodeName(config.flags.nodeName),
		kubeletplugin.DriverName(DriverName),
		kubeletplugin.RegistrarSocketPath(PluginRegistrationPath),
		kubeletplugin.PluginSocketPath(DriverPluginSocketPath),
		kubeletplugin.KubeletPluginSocketPath(DriverPluginSocketPath))
	if err != nil {
		return nil, err
	}
	driver.plugin = plugin

	var resources kubeletplugin.Resources
	for _, device := range state.allocatable {
		resources.Devices = append(resources.Devices, device)
	}

	if err := plugin.PublishResources(ctx, resources); err != nil {
		return nil, err
	}

	return driver, nil
}

func (d *driver) Shutdown(ctx context.Context) error {
	d.plugin.Stop()
	return nil
}

func (d *driver) NodePrepareResources(ctx context.Context, req *drapbv1.NodePrepareResourcesRequest) (*drapbv1.NodePrepareResourcesResponse, error) {
	klog.Infof("NodePrepareResources called: number of claims: %d", len(req.Claims))
	preparedResources := &drapbv1.NodePrepareResourcesResponse{Claims: map[string]*drapbv1.NodePrepareResourceResponse{}}

	for _, claim := range req.Claims {
		preparedResources.Claims[claim.UID] = d.nodePrepareResource(ctx, claim)
	}

	// 所有资源处理完毕后，上报最新的资源状态
	var resources kubeletplugin.Resources
	// 上报所有可用的slice
	for _, deviceName := range d.state.allocatable {
		resources.Devices = append(resources.Devices, deviceName)
		klog.V(4).Infof("Publishing available device: %s", deviceName)
	}

	if err := d.plugin.PublishResources(ctx, resources); err != nil {
		klog.Errorf("Failed to publish resources after preparing claims: %v", err)
	} else {
		klog.Infof("Successfully published updated resources after preparing %d claims", len(req.Claims))
	}

	return preparedResources, nil
}

// getAvailableDeviceNames 返回当前所有可用设备的名称
func (d *driver) getAvailableDeviceNames() []string {
	var deviceNames []string
	
	if d.state.vnpuManager != nil {
		// 从VnpuManager获取所有可用slice
		for _, physicalNpu := range d.state.vnpuManager.PhysicalNpus {
			for _, slice := range physicalNpu.AvailableSlices {
				if !slice.Allocated {
					deviceNames = append(deviceNames, slice.SliceID)
				}
			}
		}
	} else {
		// 如果没有VnpuManager，按旧逻辑返回所有设备
		for deviceName := range d.state.allocatable {
			deviceNames = append(deviceNames, deviceName)
		}
	}
	
	return deviceNames
}

func (d *driver) nodePrepareResource(ctx context.Context, claim *drapbv1.Claim) *drapbv1.NodePrepareResourceResponse {
	resourceClaim, err := d.client.ResourceV1beta1().ResourceClaims(claim.Namespace).Get(
		ctx,
		claim.Name,
		metav1.GetOptions{})
	if err != nil {
		return &drapbv1.NodePrepareResourceResponse{
			Error: fmt.Sprintf("failed to fetch ResourceClaim %s in namespace %s", claim.Name, claim.Namespace),
		}
	}

	prepared, err := d.state.Prepare(resourceClaim)
	if err != nil {
		return &drapbv1.NodePrepareResourceResponse{
			Error: fmt.Sprintf("error preparing devices for claim %v: %v", claim.UID, err),
		}
	}

	klog.Infof("Returning newly prepared devices for claim '%v': %v", claim.UID, prepared)
	return &drapbv1.NodePrepareResourceResponse{Devices: prepared}
}

func (d *driver) NodeUnprepareResources(ctx context.Context, req *drapbv1.NodeUnprepareResourcesRequest) (*drapbv1.NodeUnprepareResourcesResponse, error) {
	klog.Infof("NodeUnprepareResources called: number of claims: %d", len(req.Claims))
	unpreparedResources := &drapbv1.NodeUnprepareResourcesResponse{Claims: map[string]*drapbv1.NodeUnprepareResourceResponse{}}

	for _, claim := range req.Claims {
		unpreparedResources.Claims[claim.UID] = d.nodeUnprepareResource(ctx, claim)
	}

	var resources kubeletplugin.Resources
	for _, device := range d.state.allocatable {
		resources.Devices = append(resources.Devices, device)
	}

	if err := d.plugin.PublishResources(ctx, resources); err != nil {
		klog.Errorf("Failed to publish resources after unpreparing claims: %v", err)
	} else {
		klog.Infof("Successfully published updated resources after unpreparing %d claims", len(req.Claims))
	}

	return unpreparedResources, nil
}

func (d *driver) nodeUnprepareResource(ctx context.Context, claim *drapbv1.Claim) *drapbv1.NodeUnprepareResourceResponse {
	if err := d.state.Unprepare(claim.UID); err != nil {
		return &drapbv1.NodeUnprepareResourceResponse{
			Error: fmt.Sprintf("error unpreparing devices for claim %v: %v", claim.UID, err),
		}
	}

	return &drapbv1.NodeUnprepareResourceResponse{}
}
