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
	"bufio"
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// 获取NPU支持的模板信息
func GetNpuTemplateInfo() (map[string]*VnpuTemplate, error) {
	log.Println("开始获取NPU模板信息...")

	// 从挂载文件读取模板信息
	templateFilePath := "/etc/npu/template-info.txt"
	content, err := os.ReadFile(templateFilePath)
	if err != nil {
		// 如果找不到文件，尝试创建模拟数据（便于测试）
		log.Printf("读取模板信息文件失败: %v, 将使用模拟数据", err)

		// 创建示例模板数据
		templates := createDefaultTemplates()
		return templates, nil
	}

	templates := make(map[string]*VnpuTemplate)
	err = parseTemplateInfo(string(content), templates)
	if err != nil {
		return nil, err
	}

	log.Printf("成功从文件解析NPU模板信息，共发现%d个模板", len(templates))
	return templates, nil
}

// 创建默认模板数据（当无法从文件读取时使用）
func createDefaultTemplates() map[string]*VnpuTemplate {
	templates := make(map[string]*VnpuTemplate)

	// 添加一些默认模板
	templates["vir01"] = &VnpuTemplate{
		Name: "vir01",
		Attributes: VnpuTemplateAttribute{
			AICORE: 4,
			Memory: 8,
		},
	}

	templates["vir02"] = &VnpuTemplate{
		Name: "vir02",
		Attributes: VnpuTemplateAttribute{
			AICORE: 8,
			Memory: 12,
		},
	}

	templates["vir04"] = &VnpuTemplate{
		Name: "vir04",
		Attributes: VnpuTemplateAttribute{
			AICORE: 16,
			Memory: 16,
		},
	}

	log.Printf("使用默认模板数据，共%d个模板", len(templates))
	return templates
}

// 解析npu-smi template-info输出
func parseTemplateInfo(output string, templates map[string]*VnpuTemplate) error {
	scanner := bufio.NewScanner(strings.NewReader(output))

	// 查找表头行
	headerLine := ""
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "Name") && strings.Contains(line, "AICORE") && strings.Contains(line, "Memory") {
			headerLine = line
			break
		}
	}

	if headerLine == "" {
		return fmt.Errorf("未找到模板信息表头")
	}

	// 解析表头，确定各列位置
	headerFields := regexp.MustCompile(`\s+`).Split(strings.TrimSpace(headerLine), -1)
	columnPositions := make(map[string]int)

	for i, field := range headerFields {
		columnPositions[field] = i
	}

	// 跳过分隔行
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "==") {
			break
		}
	}

	// 处理模板数据
	currentTemplate := ""
	var currentAttrs *VnpuTemplateAttribute

	for scanner.Scan() {
		line := scanner.Text()
		line = strings.Trim(line, "|")
		if strings.TrimSpace(line) == "" || strings.Contains(line, "--") {
			continue
		}

		fields := regexp.MustCompile(`\s+`).Split(strings.TrimSpace(line), -1)

		if len(fields) > 0 && fields[0] != "" {
			// 这是模板名称行
			currentTemplate = fields[0]
			currentAttrs = &VnpuTemplateAttribute{}

			// 解析属性
			for attr, pos := range columnPositions {
				if pos < len(fields) && attr != "Name" {
					val, err := strconv.Atoi(fields[pos])
					if err != nil {
						log.Printf("警告：解析属性 %s 的值 %s 失败: %v", attr, fields[pos], err)
						continue
					}

					switch attr {
					case "AICORE":
						currentAttrs.AICORE = val
					case "Memory":
						currentAttrs.Memory = val
						// 移除其他属性，只保留AICORE和Memory
					}
				}
			}

			templates[currentTemplate] = &VnpuTemplate{
				Name:       currentTemplate,
				Attributes: *currentAttrs,
			}
		} else if currentTemplate != "" && len(fields) > 1 {
			// 这是附加属性行 - 由于我们只关心AICORE和Memory，不需要处理这些附加属性
			// 保留这个逻辑结构，但不再尝试解析其他字段
		}
	}

	return nil
}

// 初始化VnpuManager
func NewVnpuManager() (*VnpuManager, error) {
	templates, err := GetNpuTemplateInfo()
	if err != nil {
		return nil, fmt.Errorf("获取NPU模板信息失败: %v", err)
	}

	manager := &VnpuManager{
		PhysicalNpus: make(map[string]*PhysicalNpuState),
		Templates:    templates,
	}

	return manager, nil
}

// 初始化物理NPU
func (m *VnpuManager) InitPhysicalNpu(deviceName string, logicID int32) {
	m.Lock()
	defer m.Unlock()

	log.Printf("初始化物理NPU: %s, LogicID: %d", deviceName, logicID)

	// 创建物理NPU状态对象
	physicalNpu := &PhysicalNpuState{
		DeviceName:       deviceName,
		LogicID:          logicID,
		AvailableSlices:  []*VnpuSlice{},
		AllocatedSlices:  []*VnpuSlice{},
		SupportTemplates: make(map[string]*VnpuTemplate),
	}

	// 添加所有支持的模板
	for name, template := range m.Templates {
		physicalNpu.SupportTemplates[name] = template
	}

	// 添加"整卡"作为默认可用分片
	slice := &VnpuSlice{
		SliceID:      deviceName, // 整卡时SliceID等于设备名
		TemplateName: "",         // 整卡没有特定模板
		Allocated:    false,
	}
	physicalNpu.AvailableSlices = append(physicalNpu.AvailableSlices, slice)

	m.PhysicalNpus[deviceName] = physicalNpu
	log.Printf("物理NPU %s 初始化完成", deviceName)
}

// 释放vNPU切片
func (m *VnpuManager) ReleaseSlice(sliceID string) error {
	m.Lock()
	defer m.Unlock()

	log.Printf("尝试释放vNPU切片: %s", sliceID)

	// 查找切片所属的物理NPU
	var physicalNpu *PhysicalNpuState
	var sliceIndex int
	found := false

	for _, npu := range m.PhysicalNpus {
		for i, slice := range npu.AllocatedSlices {
			if slice.SliceID == sliceID {
				physicalNpu = npu
				sliceIndex = i
				found = true
				break
			}
		}
		if found {
			break
		}
	}

	if !found {
		return fmt.Errorf("找不到vNPU切片: %s", sliceID)
	}

	// 移除已分配的切片
	slice := physicalNpu.AllocatedSlices[sliceIndex]
	physicalNpu.AllocatedSlices = append(physicalNpu.AllocatedSlices[:sliceIndex], physicalNpu.AllocatedSlices[sliceIndex+1:]...)

	// 如果是整卡，将其添加回可用切片
	if slice.SliceID == physicalNpu.DeviceName {
		slice.Allocated = false
		physicalNpu.AvailableSlices = append(physicalNpu.AvailableSlices, slice)
		log.Printf("成功释放物理NPU %s 的整卡", physicalNpu.DeviceName)
		return nil
	}

	// 如果没有分配的切片，恢复整卡可用
	if len(physicalNpu.AllocatedSlices) == 0 {
		found := false
		for _, slice := range physicalNpu.AvailableSlices {
			if slice.SliceID == physicalNpu.DeviceName {
				found = true
				break
			}
		}

		if !found {
			wholeCardSlice := &VnpuSlice{
				SliceID:      physicalNpu.DeviceName,
				TemplateName: "",
				Allocated:    false,
			}
			physicalNpu.AvailableSlices = append(physicalNpu.AvailableSlices, wholeCardSlice)
		}
	}

	// 更新当前NPU支持的模板
	m.updateSupportTemplates(physicalNpu)

	log.Printf("成功释放vNPU切片: %s", sliceID)
	return nil
}

// 更新物理NPU支持的模板列表
func (m *VnpuManager) updateSupportTemplates(physicalNpu *PhysicalNpuState) {
	// 如果有分片被分配，重新计算支持的模板
	if len(physicalNpu.AllocatedSlices) > 0 {
		// 清空当前支持的模板
		physicalNpu.SupportTemplates = make(map[string]*VnpuTemplate)

		// 添加仍然可用的模板
		// 这里需要根据已分配的资源计算剩余资源，然后确定哪些模板仍然可用
		// 简化实现，这里假设任何分片分配后，只支持指定的一些模板
		for name, template := range m.Templates {
			// 这里根据实际情况检查模板是否仍可用
			// 简化示例：只保留小型模板
			if strings.HasPrefix(name, "vir01") {
				physicalNpu.SupportTemplates[name] = template
			}
		}
	} else {
		// 如果没有分片被分配，支持所有模板
		physicalNpu.SupportTemplates = make(map[string]*VnpuTemplate)
		for name, template := range m.Templates {
			physicalNpu.SupportTemplates[name] = template
		}
	}
}

// 获取设备对应的ASCEND_VNPU_SPECS环境变量值
func (m *VnpuManager) GetVnpuSpecsEnv(sliceID string) (string, error) {
	m.Lock()
	defer m.Unlock()

	// 查找切片
	var slice *VnpuSlice
	found := false

	for _, npu := range m.PhysicalNpus {
		for _, s := range npu.AllocatedSlices {
			if s.SliceID == sliceID {
				slice = s
				found = true
				break
			}
		}
		if found {
			break
		}
	}

	if !found {
		return "", fmt.Errorf("找不到vNPU切片: %s", sliceID)
	}

	// 整卡时，不需要设置ASCEND_VNPU_SPECS
	if slice.TemplateName == "" {
		return "", nil
	}

	return slice.TemplateName, nil
}
