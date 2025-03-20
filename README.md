# 昇腾DRA驱动

本仓库包含用于Kubernetes [动态资源分配(DRA)](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/)功能的示例资源驱动。

本项目旨在展示如何构建DRA资源驱动并将其封装在[helm chart](https://helm.sh/)中的最佳实践。它可以作为实现您自己资源集驱动的起点。

## 里程碑
- [ ] 实现vNPU占整卡的分配
- [ ] 实现vNPU分配后重新计算剩余设备，并更新device列表
- [ ] 实现多节点多卡调度分配
- [ ] 实现基本故障处理
- [ ] 实现基本运行时动态分配（可行性分析）
- [ ] 实现全面故障处理

## 快速开始和演示

在深入了解该示例驱动程序构建细节之前，通过快速演示了解其运行情况是很有用的。

驱动本身提供对一组模拟NPU设备的访问，本演示将介绍构建和安装驱动程序，然后运行消耗这些NPU的工作负载的过程。

以下步骤已在Linux和Mac上测试并验证。

### 前置条件

* [GNU Make 3.81+](https://www.gnu.org/software/make/)
* [GNU Tar 1.34+](https://www.gnu.org/software/tar/)
* [docker v20.10+ (包括buildx)](https://docs.docker.com/engine/install/) 或 [Podman v4.9+](https://podman.io/docs/installation)
* [kind v0.17.0+](https://kind.sigs.k8s.io/docs/user/quick-start/)
* [helm v3.7.0+](https://helm.sh/docs/intro/install/)
* [kubectl v1.18+](https://kubernetes.io/docs/reference/kubectl/)

### 演示
首先克隆此仓库并进入目录。此演示中使用的所有脚本和示例Pod规范都包含在这里，花点时间浏览各个文件，看看有哪些内容：
```
git clone https://github.com/kubernetes-sigs/ascend-dra-driver.git
cd ascend-dra-driver
```

**注意**：脚本将自动使用PATH中找到的`docker`或`podman`作为容器工具命令。要覆盖此行为，可以通过调用`export CONTAINER_TOOL=docker`设置`CONTAINER_TOOL`环境变量，或者在脚本前加上`CONTAINER_TOOL=docker`（例如`CONTAINER_TOOL=docker ./path/to/script.sh`）。请记住，构建Kind镜像目前需要Docker。

从这里我们将构建示例资源驱动程序的镜像：
```bash
./demo/build-driver.sh
```

并创建一个`kind`集群来运行它：
```bash
./demo/create-cluster.sh
```

集群创建成功后，仔细检查一切是否按预期启动：
```console
$ kubectl get pod -A
NAMESPACE            NAME                                                               READY   STATUS    RESTARTS   AGE
kube-system          coredns-5d78c9869d-6jrx9                                           1/1     Running   0          1m
kube-system          coredns-5d78c9869d-dpr8p                                           1/1     Running   0          1m
kube-system          etcd-ascend-dra-driver-cluster-control-plane                      1/1     Running   0          1m
kube-system          kindnet-g88bv                                                      1/1     Running   0          1m
kube-system          kindnet-msp95                                                      1/1     Running   0          1m
kube-system          kube-apiserver-ascend-dra-driver-cluster-control-plane            1/1     Running   0          1m
kube-system          kube-controller-manager-ascend-dra-driver-cluster-control-plane   1/1     Running   0          1m
kube-system          kube-proxy-kgz4z                                                   1/1     Running   0          1m
kube-system          kube-proxy-x6fnd                                                   1/1     Running   0          1m
kube-system          kube-scheduler-ascend-dra-driver-cluster-control-plane            1/1     Running   0          1m
local-path-storage   local-path-provisioner-7dbf974f64-9jmc7                            1/1     Running   0          1m
```

然后通过`helm`安装示例资源驱动程序。
```bash
helm upgrade -i \
  --create-namespace \
  --namespace ascend-dra-driver \
  ascend-dra-driver \
  deployments/helm/ascend-dra-driver
```

检查驱动程序组件是否已成功启动：
```console
$ kubectl get pod -n ascend-dra-driver
NAME                                             READY   STATUS    RESTARTS   AGE
ascend-dra-driver-kubeletplugin-qwmbl           1/1     Running   0          1m
```

并显示工作节点上可用NPU设备的初始状态：
```
$ kubectl get resourceslice -o yaml
```

接下来，部署五个示例应用程序，演示如何以各种方式使用`ResourceClaim`、`ResourceClaimTemplate`和自定义`NpuConfig`对象来选择和配置资源：
```bash
kubectl apply --filename=demo/npu-test{1,2,3,4,5}.yaml
```

并验证它们是否成功启动：
```console
$ kubectl get pod -A
NAMESPACE   NAME   READY   STATUS              RESTARTS   AGE
...
npu-test1   pod0   0/1     Pending             0          2s
npu-test1   pod1   0/1     Pending             0          2s
npu-test2   pod0   0/2     Pending             0          2s
npu-test3   pod0   0/1     ContainerCreating   0          2s
npu-test3   pod1   0/1     ContainerCreating   0          2s
npu-test4   pod0   0/1     Pending             0          2s
npu-test5   pod0   0/4     Pending             0          2s
...
```

使用您喜欢的编辑器查看每个`npu-test{1,2,3,4,5}.yaml`文件，了解它们的功能。

然后转储每个应用程序的日志，验证是否根据这些语义为它们分配了NPU：
```bash
for example in $(seq 1 5); do \
  echo "npu-test${example}:"
  for pod in $(kubectl get pod -n npu-test${example} --output=jsonpath='{.items[*].metadata.name}'); do \
    for ctr in $(kubectl get pod -n npu-test${example} ${pod} -o jsonpath='{.spec.containers[*].name}'); do \
      echo "${pod} ${ctr}:"
      if [ "${example}" -lt 3 ]; then
        kubectl logs -n npu-test${example} ${pod} -c ${ctr}| grep -E "NPU_DEVICE_[0-9]+=" | grep -v "RESOURCE_CLAIM"
      else
        kubectl logs -n npu-test${example} ${pod} -c ${ctr}| grep -E "NPU_DEVICE_[0-9]+" | grep -v "RESOURCE_CLAIM"
      fi
    done
  done
  echo ""
done
```

在这个示例资源驱动程序中，没有"实际"的NPU提供给任何容器。相反，在每个容器中设置了一组环境变量，以指示真实资源驱动程序*会*注入哪些NPU以及它们*会*如何配置。

您可以使用这些环境变量中设置的NPU ID以及NPU共享设置来验证它们是否以与图中所示语义一致的方式分发。

验证一切正常运行后，删除所有示例应用程序：
```bash
kubectl delete --wait=false --filename=demo/npu-test{1,2,3,4,5}.yaml
```

并等待它们终止：
```console
$ kubectl get pod -A
NAMESPACE   NAME   READY   STATUS        RESTARTS   AGE
...
npu-test1   pod0   1/1     Terminating   0          31m
npu-test1   pod1   1/1     Terminating   0          31m
npu-test2   pod0   2/2     Terminating   0          31m
npu-test3   pod0   1/1     Terminating   0          31m
npu-test3   pod1   1/1     Terminating   0          31m
npu-test4   pod0   1/1     Terminating   0          31m
npu-test5   pod0   4/4     Terminating   0          31m
...
```

最后，您可以运行以下命令清理环境并删除先前启动的`kind`集群：
```bash
./demo/delete-cluster.sh
```

## 昇腾DRA开发环境构建（KIND）

### 前置条件
- 获取多个二进制 参考： [.gitkeep](dev/tools/.gitkeep)
  - 注意环境是arm还是amd
- 安装kind环境（k8s为 v1.32.0 版本）

### 环境配置
1. 创建单机集群
```bash
./demo/create-cluster.sh
```

2. 编译和安装dra初始驱动镜像
```bash
./demo/build-driver.sh

# 安装
./demo/install-dra-driver.sh
```

3. 编译并启动开发版dra驱动
```bash
# 编译dra驱动
cd ./dev/dra
./build_dra.sh

# 同步开发编译版dra驱动及调试工具到dra驱动容器
./all_cp.sh

# 进入dra驱动容器
./pod_into_dra.sh

# 进入/root目录
cd

# 启动调试
./start_debug.sh

# 在本地开发环境使用远程调试配置连接
# zjknps.jieshi.space:9341
```

4. （可选）替换k8s组件，以调度器为案例。 参考： [K8s远程调试，你的姿势对了吗？](https://cloud.tencent.com/developer/article/1624638)
```bash
# 复制调试工具及可调试版本二进制
cd ./dev/node
./all_cp.sh

# 进入主node节点
./pod_into_node.sh

# 进入/root路径
cd 

# 禁用默认调度器实例
./disable_schedule.sh

# 杀掉调度器实例
./kill_process.sh

# 启动调试版本调度器
./start_debug.sh

# 使用远程调试配置连接
zjknps.jieshi.space:9523
```

## DRA资源驱动剖析

待定

## 代码组织

待定

## 最佳实践

待定

## 参考资料

有关Kubernetes DRA功能和开发自定义资源驱动程序的更多信息，请参阅以下资源：

* [Kubernetes中的动态资源分配](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/)
* 待定

## 社区、讨论、贡献和支持

了解如何在[社区页面](http://kubernetes.io/community/)上与Kubernetes社区互动。

您可以通过以下方式联系本项目的维护者：

- [Slack](https://slack.k8s.io/)
- [邮件列表](https://groups.google.com/a/kubernetes.io/g/dev)

### 行为准则

参与Kubernetes社区受[Kubernetes行为准则](code-of-conduct.md)的约束。

[owners]: https://git.k8s.io/community/contributors/guide/owners.md
[Creative Commons 4.0]: https://git.k8s.io/website/LICENSE
