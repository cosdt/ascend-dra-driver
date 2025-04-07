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
	"os"

	cdiapi "tags.cncf.io/container-device-interface/pkg/cdi"
	cdiparser "tags.cncf.io/container-device-interface/pkg/parser"
	cdispec "tags.cncf.io/container-device-interface/specs-go"
)

const (
	cdiVendor           = "k8s." + DriverName
	cdiClass            = "npu"
	cdiKind             = cdiVendor + "/" + cdiClass
	cdiCommonDeviceName = "common"
)

// CDIHandler manages CDI specifications and handles creation/deletion for different CDI use cases.
type CDIHandler struct {
	cache *cdiapi.Cache
}

// NewCDIHandler creates a new CDIHandler with the specified configuration.
func NewCDIHandler(config *Config) (*CDIHandler, error) {
	cache, err := cdiapi.NewCache(
		cdiapi.WithSpecDirs(config.flags.cdiRoot),
	)
	if err != nil {
		return nil, fmt.Errorf("Failed to create CDI cache: %w", err)
	}
	return &CDIHandler{cache: cache}, nil
}

// writeSpec is a helper function to write a CDI spec with the minimum required version.
func (h *CDIHandler) writeSpec(spec *cdispec.Spec, specName string) error {
	minVersion, err := cdiapi.MinimumRequiredVersion(spec)
	if err != nil {
		return fmt.Errorf("Failed to get minimum required CDI spec version: %v", err)
	}
	spec.Version = minVersion
	return h.cache.WriteSpec(spec, specName)
}

// CreateCommonSpecFile generates a common CDI spec that injects node-related environment variables.
func (h *CDIHandler) CreateCommonSpecFile() error {
	spec := &cdispec.Spec{
		Kind: cdiKind,
		Devices: []cdispec.Device{
			{
				Name: cdiCommonDeviceName,
				ContainerEdits: cdispec.ContainerEdits{
					Env: []string{
						fmt.Sprintf("KUBERNETES_NODE_NAME=%s", os.Getenv("NODE_NAME")),
						fmt.Sprintf("DRA_RESOURCE_DRIVER_NAME=%s", DriverName),
					},
				},
			},
		},
	}

	specName, err := cdiapi.GenerateNameForTransientSpec(spec, cdiCommonDeviceName)
	if err != nil {
		return fmt.Errorf("Failed to generate spec name: %w", err)
	}
	return h.writeSpec(spec, specName)
}

// CreateClaimSpecFile generates a transient CDI spec file for a given claim,
// merging multiple container edits into a single device entry.
func (h *CDIHandler) CreateClaimSpecFile(claimUID string, devices PreparedDevices) error {
	var merged cdispec.ContainerEdits
	for _, d := range devices {
		merged.Env = append(merged.Env, d.ContainerEdits.Env...)
		merged.DeviceNodes = append(merged.DeviceNodes, d.ContainerEdits.DeviceNodes...)
		merged.Hooks = append(merged.Hooks, d.ContainerEdits.Hooks...)
		merged.Mounts = append(merged.Mounts, d.ContainerEdits.Mounts...)
	}

	spec := &cdispec.Spec{
		Kind: cdiKind,
		Devices: []cdispec.Device{
			{
				Name:           claimUID,
				ContainerEdits: merged,
			},
		},
	}

	specName := cdiapi.GenerateTransientSpecName(cdiVendor, cdiClass, claimUID)
	return h.writeSpec(spec, specName)
}

// DeleteClaimSpecFile removes the transient CDI spec file corresponding to the given claim.
func (h *CDIHandler) DeleteClaimSpecFile(claimUID string) error {
	specName := cdiapi.GenerateTransientSpecName(cdiVendor, cdiClass, claimUID)
	return h.cache.RemoveSpec(specName)
}

// GetClaimDevices returns a list of CDI device names for the specified claim.
func (h *CDIHandler) GetClaimDevices(claimUID string, _ []string) []string {
	return []string{
		cdiparser.QualifiedName(cdiVendor, cdiClass, cdiCommonDeviceName),
		cdiparser.QualifiedName(cdiVendor, cdiClass, claimUID),
	}
}
