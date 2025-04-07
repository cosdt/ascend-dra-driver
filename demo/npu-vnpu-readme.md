# vNPU测试案例使用说明

本目录包含了几个vNPU分片分配的测试案例，用于验证Ascend NPU的vNPU虚拟化功能。

## 测试案例列表

1. **npu-vnpu-test1.yaml**: 使用整卡模板分配整个NPU设备
2. **npu-vnpu-test2.yaml**: 使用内存模板分配vNPU设备
3. **npu-vnpu-test3.yaml**: 使用AICORE模板分配vNPU设备

## 预定义DeviceClass

驱动在启动时会自动创建一系列预定义的DeviceClass。这些DeviceClass基于节点上可用的vNPU分片能力自动生成：

1. **整卡型DeviceClass**：命名为`npu-<model>.example.com`，其中model是设备型号
   - 例如：`npu-ascend310p.example.com`表示Ascend 310P型号的整卡

2. **内存型DeviceClass**：命名为`npu-<model>-mem<X>.example.com`，其中X是内存大小（GB）
   - 例如：`npu-ascend310p-mem8.example.com`表示请求Ascend 310P型号，内存为8GB的NPU分片

3. **计算型DeviceClass**：命名为`npu-<model>-aicore<Y>.example.com`，其中Y是AICORE数量
   - 例如：`npu-ascend310p-aicore4.example.com`表示请求Ascend 310P型号，AICORE数量为4的NPU分片

## 架构变更说明

相比于早期版本，当前测试案例做了以下架构调整：

1. **从ResourceClaimTemplate到DeviceClass**：
   - 之前：使用预定义的ResourceClaimTemplate来定义分配逻辑
   - 现在：使用预定义的DeviceClass来定义分配逻辑，避免了ResourceClaimTemplate作为namespaced资源的限制

2. **CEL表达式的位置**：
   - 之前：CEL表达式位于ResourceClaimTemplate中
   - 现在：CEL表达式位于DeviceClass中，ResourceClaimTemplate仅引用相应DeviceClass

3. **创建ResourceClaimTemplate**：
   - 之前：直接引用预定义的ResourceClaimTemplate
   - 现在：每个测试案例创建自己的ResourceClaimTemplate，并引用相应的DeviceClass

## 示例用法

创建使用特定DeviceClass的ResourceClaimTemplate和Pod：

```yaml
---
apiVersion: resource.k8s.io/v1beta1
kind: ResourceClaimTemplate
metadata:
  namespace: my-namespace
  name: my-npu-template
spec:
  spec:
    devices:
      requests:
      - name: npu
        deviceClassName: npu-ascend310p-mem8.example.com
      config:
      - opaque:
          driver: npu.example.com
          parameters:
            apiVersion: gpu.resource.example.com/v1alpha1
            kind: GpuConfig
            vnpuSpec:
              templateName: vir04

---
apiVersion: v1
kind: Pod
metadata:
  name: npu-pod
spec:
  containers:
  - name: npu-container
    image: your-image
    resources:
      claims:
      - name: npu
  resourceClaims:
  - name: npu
    resourceClaimTemplateName: my-npu-template
```

## 使用方法

### 部署测试案例

```bash
# 部署整卡分配测试
kubectl apply -f npu-vnpu-test1.yaml

# 部署内存型vNPU分片测试
kubectl apply -f npu-vnpu-test2.yaml

# 部署AICORE型vNPU分片测试
kubectl apply -f npu-vnpu-test3.yaml
```

### 查看运行状态

```bash
# 查看Pod状态
kubectl get pods -n npu-vnpu-system

# 查看ResourceClaim状态
kubectl get resourceclaims -n npu-vnpu-system

# 查看ResourceClaimTemplate
kubectl get resourceclaimtemplates -n npu-vnpu-system

# 查看DeviceClass（集群范围资源）
kubectl get deviceclasses
```

### 验证环境变量

登录到Pod中检查环境变量：

```bash
# 检查整卡Pod环境变量
kubectl exec -it npu-pod-fullcard -n npu-vnpu-system -- env | grep ASCEND
```

应当看到类似以下输出：
```
ASCEND_VISIBLE_DEVICES=0
```

```bash
# 检查vNPU分片Pod环境变量
kubectl exec -it npu-pod-mem -n npu-vnpu-system -- env | grep ASCEND
```

应当看到类似以下输出：
```
ASCEND_VISIBLE_DEVICES=0
ASCEND_VNPU_SPECS=vir04
```

### 清理资源

```bash
# 清理测试资源
kubectl delete -f npu-vnpu-test1.yaml
kubectl delete -f npu-vnpu-test2.yaml
kubectl delete -f npu-vnpu-test3.yaml
```

## 主要特性

1. **DeviceClass选择**: 通过DeviceClass选择满足特定条件的设备
2. **vNPU模板**: 通过`GpuConfig.vnpuSpec.templateName`指定要使用的vNPU模板
3. **CEL表达式**: DeviceClass使用CEL表达式定义设备选择条件
4. **集群范围资源**: DeviceClass作为集群范围资源，可以跨命名空间重用

## CEL表达式用法

DeviceClass中使用了以下几种CEL表达式来选择设备：

1. **检查设备型号**:
   ```
   device.attributes["npu.example.com"].model == "Ascend310P"
   ```

2. **检查内存要求**:
   ```
   device.attributes["npu.example.com"].memory >= 8 && device.attributes["npu.example.com"].model == "Ascend310P"
   ```

3. **检查AICORE要求**:
   ```
   device.attributes["npu.example.com"].aicore >= 4 && device.attributes["npu.example.com"].model == "Ascend310P"
   ```

每个NPU设备上报的属性包括：
- `model`: 设备型号
- `aicore`: 当前支持的AICORE数量
- `memory`: 当前支持的内存大小（GB）

## 注意事项

1. 确保集群中已安装Ascend DRA驱动，且支持vNPU功能
2. 确保NPU设备支持请求的vNPU模板（如vir01, vir04等）
3. 当一个物理NPU已被整卡分配后，无法再分配vNPU分片
4. 当一个物理NPU已分配某个vNPU分片后，可用的vNPU模板会根据剩余资源动态调整
5. **重要限制**: vNPU分片不能与其他分片或整卡同时在同一个容器中使用，每个容器只能使用一个vNPU分片或一个整卡
6. 可以在同一个Pod的不同容器中使用不同的vNPU分片（如测试案例4所示） 