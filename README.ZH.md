# 昇腾DRA驱动

## 里程碑
- [ ] 实现vNPU占整卡的分配
- [ ] 实现vNPU分配后重新计算剩余设备，并更新device列表
- [ ] 实现多节点多卡调度分配
- [ ] 实现基本故障处理
- [ ] 实现基本运行时动态分配（可行性分析）
- [ ] 实现全面故障处理

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
