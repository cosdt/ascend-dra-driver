# vNPU测试案例使用说明

本目录包含了几个vNPU分片分配的测试案例，用于验证Ascend NPU的vNPU虚拟化功能。

## 测试案例列表

1. **npu-vnpu-test1.yaml**: 单个Pod使用vir01模板的vNPU分片
2. **npu-vnpu-test2.yaml**: 两个Pod分别使用vir01和vir02模板的vNPU分片
3. **npu-vnpu-test3.yaml**: 使用CEL表达式选择支持特定vNPU模板的NPU
4. **npu-vnpu-test4.yaml**: 单个Pod中多个容器使用不同的vNPU分片
5. **npu-vnpu-test5.yaml**: 混合部署使用整卡和vNPU分片的不同Pod

## 预定义ResourceClaimTemplate

驱动在启动时会自动创建一系列预定义的ResourceClaimTemplate，存放在`npu-vnpu-system`命名空间中。这些模板基于节点上可用的vNPU分片能力自动生成：

1. **内存型模板**：命名为`npu-mem<X>`，其中X是内存大小（GB）
   - 例如：`npu-mem8`表示请求内存为8GB的NPU分片

2. **计算型模板**：命名为`npu-aicore<Y>`，其中Y是AICORE数量
   - 例如：`npu-aicore4`表示请求AICORE数量为4的NPU分片

使用这些预定义模板可以快速创建Pod，示例：

```yaml
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
    source:
      resourceClaimTemplateName: npu-mem8
      namespace: npu-vnpu-system
```

## 使用方法

### 部署测试案例

```bash
# 部署单个vNPU分片测试
kubectl apply -f npu-vnpu-test1.yaml

# 部署两个不同vNPU分片测试
kubectl apply -f npu-vnpu-test2.yaml

# 其他测试案例
kubectl apply -f npu-vnpu-test3.yaml
kubectl apply -f npu-vnpu-test4.yaml
kubectl apply -f npu-vnpu-test5.yaml
```

### 查看运行状态

```bash
# 查看测试1的状态
kubectl get pods -n npu-vnpu-test1
kubectl get resourceclaims -n npu-vnpu-test1

# 查看测试2的状态
kubectl get pods -n npu-vnpu-test2
kubectl get resourceclaims -n npu-vnpu-test2

# 查看预定义模板列表
kubectl get resourceclaimtemplates -n npu-vnpu-system
```

### 验证环境变量

登录到Pod中检查环境变量：

```bash
# 检查测试1的Pod环境变量
kubectl exec -it vnpu-pod-vir01 -n npu-vnpu-test1 -- env | grep ASCEND
```

应当看到类似以下输出：
```
ASCEND_VISIBLE_DEVICES=0
ASCEND_VNPU_SPECS=vir01
```

### 清理资源

```bash
# 清理测试资源
kubectl delete -f npu-vnpu-test1.yaml
kubectl delete -f npu-vnpu-test2.yaml
kubectl delete -f npu-vnpu-test3.yaml
kubectl delete -f npu-vnpu-test4.yaml
kubectl delete -f npu-vnpu-test5.yaml
```

## 主要特性

1. **vNPU模板**: 通过`GpuConfig.vnpuSpec.templateName`指定要使用的vNPU模板
2. **CEL表达式选择**: 使用CEL表达式选择满足算力要求的NPU设备
3. **混合部署**: 在不同Pod中分别部署使用整卡和vNPU分片的容器
4. **多容器Pod**: 在一个Pod的不同容器中使用不同的vNPU分片
5. **预定义模板**: 自动创建基于内存和AICORE数量的ResourceClaimTemplate

## CEL表达式用法

测试案例中使用了以下几种CEL表达式来选择设备：

1. **检查精确算力要求**:
   ```
   device.attributes["aicore"] == 4 && device.attributes["memory"] == 8
   ```

2. **检查高级算力要求**:
   ```
   device.attributes["aicore"] == 8 && device.attributes["memory"] == 12
   ```

3. **检查完整算力要求**:
   ```
   device.attributes["aicore"] == 16 && device.attributes["memory"] == 16
   ```

每个NPU设备上报的属性包括：
- `aicore`: 当前支持的AICORE数量
- `memory`: 当前支持的内存大小（GB）

## 注意事项

1. 确保集群中已安装Ascend DRA驱动，且支持vNPU功能
2. 确保NPU设备支持请求的vNPU模板（如vir01, vir02等）
3. 当一个物理NPU已被整卡分配后，无法再分配vNPU分片
4. 当一个物理NPU已分配某个vNPU分片后，可用的vNPU模板会根据剩余资源动态调整
5. **重要限制**: vNPU分片不能与其他分片或整卡同时在同一个容器中使用，每个容器只能使用一个vNPU分片或一个整卡
6. 可以在同一个Pod的不同容器中使用不同的vNPU分片（如测试案例4所示） 