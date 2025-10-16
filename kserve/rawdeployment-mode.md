# 0.前言

需要预备的工具

1. Docker
2. Kind
3. Helm



# 1.集群部署

创建kind-config自定义配置，因为需要挂载一些NPU需要的文件到集群内

```yaml
apiVersion: kind.x-k8s.io/v1alpha4
kind: Cluster
nodes:
  - role: control-plane
    extraMounts:
      - hostPath: /usr/local/Ascend
        containerPath: /usr/local/Ascend
      - hostPath: /usr/local/dcmi
        containerPath: /usr/local/dcmi
      - hostPath: /usr/local/bin/npu-smi
        containerPath: /usr/local/bin/npu-smi
      - hostPath: /etc/ascend_install.info
        containerPath: /etc/ascend_install.info
      - hostPath: /root/.cache
        containerPath: /root/.cache
      - hostPath: ~/kserve/model/Qwen2.5-0.5B-Instruct #这里我是先把模型下载下来然后挂载到集群内
        containerPath: /model/Qwen2.5-0.5B-Instruct
containerdConfigPatches:
  - |
    [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runc.options]
      SystemdCgroup = false		# 设置 containerd 使用 cgroupfs 而非 systemd，提高兼容性
  - |
    [plugins."io.containerd.grpc.v1.cri".registry.mirrors."docker.io"]
      endpoint = ["https://docker.xuanyuan.me"]	# 设置 Docker Hub 镜像加速
```



```bash
kind create cluster --image kindest/node:v1.25.11 --config kind-config.yaml	--name dev
# 镜像版本可以自己指定或者默认
```



成功示例：

<img width="463" height="293" alt="image" src="https://github.com/user-attachments/assets/bcb49282-50c7-4870-bde6-ec2103a2a72f" />




<img width="893" height="249" alt="image" src="https://github.com/user-attachments/assets/0dd2adb1-7d9d-4a88-b671-8759806b80af" />






# 2.DevicePlugin部署

[官方仓库](https://gitee.com/ascend/mind-cluster/blob/master/component/ascend-device-plugin/README.md)，参照官方的教程，生成 output 目录，包含如下文件

<img width="1221" height="117" alt="image" src="https://github.com/user-attachments/assets/edd29c3b-2f9a-457a-bcac-6c02063fd1f9" />


因为我测试的物理机的NPU是310P型号的，所以使用的是 `device-plugin-310-v6.0.0.yaml`



```bash
# 给节点加一下标签，方便匹配
kubectl label node dev-control-plane accelerator=huawei-Ascend310P

# 构建镜像
cd mind-cluster/component/ascend-device-plugin/output 
docker build -t ascend-k8sdeviceplugin:v6.0.0 .

# 把镜像传入集群内
kind load docker-image   ascend-k8sdeviceplugin:v6.0.0 --name dev

# 创建插件进程
kubectl apply -f device-plugin-310P-v6.0.0.yaml 
```



成功示例：

<img width="895" height="69" alt="image" src="https://github.com/user-attachments/assets/94f34fc4-09fb-40e3-b4c4-8b5a990f77c4" />




`kubectl describe node`，可以看到NPU信息已经出现在资源列表了

<img width="375" height="445" alt="image" src="https://github.com/user-attachments/assets/3ab8b11b-3d3c-4bde-ad82-1defa5fc8980" />








> 如果devicePlugin Pod一直处于ContainerCreating状态，查看events有
>
> - MountVolume.SetUp failed for volume "log-path" : hostPath type check failed: /var/log/mindx-dl/devicePlugin is not a directory
>
> 进入集群手动创建这个目录就好了
>
> ```bash
> docker exec -it dev-control-plane bash
> mkdir -p /var/log/mindx-dl/devicePlugin
> exit
> ```







# 3.KServe安装

参考 [官方QuickInstall](https://kserve.github.io/website/latest/get_started/#install-the-kserve-quickstart-environment) ，我这里执行的是 `quick_install.sh -r`（安装Rawdeployment模式）



> 若安装过程有阻塞，可以参考下面的修改点
>
> ```bash
> #!/bin/bash
> 
> set -eo pipefail
> ############################################################
> # Help                                                     #
> ############################################################
> Help() {
> # Display Help
> echo "KServe quick install script."
> echo
> echo "Syntax: [-s|-r]"
> echo "options:"
> echo "s Serverless Mode."
> echo "r RawDeployment Mode."
> echo "u Uninstall."
> echo "d Install only dependencies."
> echo "k Install KEDA."
> echo
> }
> 
> # 一：Helm版本 <3.8 需要声明一下该环境变量
> export HELM_EXPERIMENTAL_OCI=1
> 
> ...
> 
> 
> # 二：若报错
> # Error: template:gateway/templates/deployment.yaml:71:24: executing "gateway/templates/deployment .yaml" at <eq .values.platform "openshif t">: error calling eq:incompatible types for comparison
> # 这里加上 --set platform=""
> helm upgrade --install istio-ingressgateway istio/gateway -n istio-system --version ${ISTIO_VERSION} \
> --set platform="" --set-string podAnnotations."cluster-autoscaler\.kubernetes\.io/safe-to-evict"=true
> 
> ...
> 
> # Install Cert Manager
> # helm repo add jetstack https://charts.jetstack.io --force-update
> # 三：如果卡在helm repo add这类步骤（网络问题），可以去官网下载tgz包再传到服务器上，然后修改一下对应命令
> # 例如下面 jetstack/cert-manage 修改为本地的./cert-manager-v1.16.1.tgz
> helm install \
> cert-manager ./cert-manager-v1.16.1.tgz \		
> --namespace cert-manager \
> --create-namespace \
> --version ${CERT_MANAGER_VERSION} \
> --set crds.enabled=true
> echo "😀 Successfully installed Cert Manager"
> 
> ...
> ```



安装成功示例：

<img width="911" height="155" alt="image" src="https://github.com/user-attachments/assets/73b35bcb-4031-4b33-b884-ecd54626eca5" />




# 4.测试

参考:

- [vllm-ascend-quick-start](https://vllm-ascend.readthedocs.io/en/latest/tutorials/single_node_300i.html)
- [kserve-use-vllm-backend](https://kserve.github.io/website/latest/modelserving/v1beta1/llm/huggingface/text_generation/#serve-the-hugging-face-llm-model-using-vllm-backend)

自定义一个Predictor（或者自定义ServingRuntime，然后Predictor引用该ServingRuntime）

```yaml
apiVersion: serving.kserve.io/v1beta1
kind: InferenceService
metadata:
  name: vllm-ascend-qwen2-5
  namespace: kserve-test
  annotations:
    "sidecar.istio.io/inject": "true"
spec:
  predictor:
    containers:
      - name: kserve-container
        image: quay.io/ascend/vllm-ascend:main-310p
        command: ["/bin/bash", "-c"]
        args:
        - |
          source /usr/local/Ascend/nnal/atb/set_env.sh && \			# 启动容器时设置环境变量
          source /usr/local/Ascend/ascend-toolkit/set_env.sh && \
          vllm serve /Qwen/Qwen2.5-0.5B-Instruct \					# 启动模型服务
            --tensor-parallel-size 1 \
            --enforce-eager \
            --dtype float16 \
            --compilation-config '{"custom_ops":["none", "+rms_norm", "+rotary_embedding"]}'
        ports:
          - containerPort: 8000
        env:
          - name: LD_LIBRARY_PATH
            value: "/usr/local/Ascend/ascend-toolkit/latest/tools/aml/lib64:/usr/local/Ascend/ascend-toolkit/latest/tools/aml/lib64/plugin:/usr/local/Ascend/ascend-toolkit/latest/lib64:/usr/local/Ascend/ascend-toolkit/latest/lib64/plugin/opskernel:/usr/local/Ascend/ascend-toolkit/latest/lib64/plugin/nnengine:/usr/local/Ascend/ascend-toolkit/latest/opp/built-in/op_impl/ai_core/tbe/op_tiling/lib/linux/aarch64:/usr/local/Ascend/driver/lib64:/usr/local/Ascend/driver/lib64/common:/usr/local/Ascend/driver/lib64/driver"
          - name: VLLM_USE_MODELSCOPE
            value: "true"
          - name: PYTORCH_NPU_ALLOC_CONF
            value: "max_split_size_mb:256"
        volumeMounts:
          - name: model-volume
            mountPath: /Qwen/Qwen2.5-0.5B-Instruct
          - name: ascend-lib64
            mountPath: /usr/local/Ascend/driver/lib64
          - name: ascend-version
            mountPath: /usr/local/Ascend/driver/version.info
          - name: dcmi 
            mountPath: /usr/local/dcmi
          - name: npu-smi 
            mountPath: /usr/local/bin/npu-smi
          - name: ascend-install-info 
            mountPath: /etc/ascend_install.info
          - name: cache 
            mountPath: /root/.cache
        resources:
          limits:
            cpu: "4"
            memory: 24Gi
            huawei.com/Ascend310P: "1"
          requests:
            cpu: "4"
            memory: 24Gi
            huawei.com/Ascend310P: "1"
    volumes:
      - name: model-volume
        hostPath:
          path: /model/Qwen2.5-0.5B-Instruct
      - name: ascend-lib64
        hostPath:
          path: /usr/local/Ascend/driver/lib64
      - name: ascend-version
        hostPath:
          path: /usr/local/Ascend/driver/version.info
      - name: dcmi
        hostPath:
          path: /usr/local/dcmi
      - name: npu-smi
        hostPath:
          path: /usr/local/bin/npu-smi
      - name: ascend-install-info
        hostPath:
          path: /etc/ascend_install.info
      - name: cache
        hostPath:
          path: /root/.cache
```





```bash
kubectl create namespace kserve-test
kubectl apply -f inferenceservice.yaml	# 上述yaml
```

<img width="949" height="201" alt="image" src="https://github.com/user-attachments/assets/ba4d05ad-4616-4cff-af7e-582c50da2d76" />


等一会，模型启动要一段时间（同时可以查看Pod log，正常准备完成时结尾会输出`INFO:Application startup complete.`）



**集群内部访问：**

Pod Ready后查看service

<img width="805" height="75" alt="image" src="https://github.com/user-attachments/assets/901c2d0b-bd6c-46ff-b702-9add0ace046e" />


> InferenceService创建后，KServe会自动为该模型生成一个`Kubernetes service`。用于模型推理请求的内部访问



```bash
# 进入集群
docker exec -it dev-control-plane bash
# 请求一下模型服务
curl http://10.96.90.50:80/v1/completions \
  -H "Content-Type: application/json" \
  -d '{
    "prompt": "The future of AI is",
    "max_tokens": 64,
    "top_p": 0.95,
    "top_k": 50,
    "temperature": 0.6
  }'
```

示例响应

<img width="1377" height="559" alt="image" src="https://github.com/user-attachments/assets/713f6b03-9a56-4a18-80da-b50b347fffff" />




**集群外部访问：**

之前 KServe 的 QuickInstall 安装脚本内有安装 **Istio**（服务网格），用于外部访问和路由

> Istio功能点挺多的，这里我们仅关注外部访问和路由方面



**一：安装和使用 cloud-provider-kind**（用于在 Kind 集群中模拟云环境的 LoadBalancer 功能）

​	查看 `istio-system namespace` 下的 service

<img width="1063" height="93" alt="image" src="https://github.com/user-attachments/assets/0e3e178d-5061-4ec6-86a9-624de8b657ad" />


> kind 默认不支持 LoadBalancer 类型的 Service，因为它没有集成像云平台（AWS, GCP, Azure）那样的 云负载均衡器，所以 LoadBalancer 类型服务无法分配 EXTERNAL-IP，状态就一直是 pending



```bash
# 安装 cloud-provider-kind
go install sigs.k8s.io/cloud-provider-kind@latest
# 运行并监听端口（需要一个独立的终端窗口保持前台运行）
cloud-provider-kind --enable-lb-port-mapping
```

再查看一下 `EXTERNAL-IP` 有值了

<img width="1067" height="89" alt="image" src="https://github.com/user-attachments/assets/8172f862-06d3-4780-acdb-cbf30eb386cd" />




**二：创建Gateway+VirtualService**

​	在`istio-system namespace` 下创建一个 Gateway（用于配置 Istio IngressGateway 如何接收外部请求）

```yaml
apiVersion: networking.istio.io/v1beta1
kind: Gateway
metadata:
  name: kserve-model-gateway
  namespace: istio-system
spec:
  selector:
    istio: ingressgateway
  servers:
  - port:
      number: 80
      protocol: HTTP
    hosts:
    - "*"  
```

和一个VirtualService（用于定义外部请求从 Gateway 到集群内服务的路由规则）

```yaml
apiVersion: networking.istio.io/v1beta1
kind: VirtualService
metadata:
  name: model-vs
  namespace: kserve-test
spec:
  hosts:
  - "*"  # 或指定域名
  gateways:
  - istio-system/kserve-model-gateway 
  http:
  - match:
      - uri:
          prefix: "/" 
    route:
      - destination:
          host: vllm-ascend-qwen2-5-predictor.kserve-test.svc.cluster.local
          port:
            number: 80 
```



```bash
kubectl apply -f gateway.yaml
kubectl apply -f virtual-service.yaml

# 还需要给容器注入sidecar，需要重启一下InferenceService（或者重启Pod）
kubectl label namespace kserve-test istio-injection=enabled --overwrite	
kubectl delete -f inferenceservice.yaml
kubectl apply -f inferenceservice.yaml
```



三：通过 **NodeIP+Port** 访问

​	查看NodeIP和service端口映射

<img width="1675" height="163" alt="image" src="https://github.com/user-attachments/assets/8586b03e-c9e2-41e1-a8cc-03e86d74c856" />


```bash
# 尝试在宿主机上发送请求
curl http://172.18.0.2:31678/v1/completions \
-H "Host: vllm-ascend-qwen2-5-kserve-test.example.com" \
-H "Content-Type: application/json" \   
-d '{
    "prompt": "The future of human is",
    "max_tokens": 64,
    "top_p": 0.95,
    "top_k": 50,
    "temperature": 0.6
  }'
```

示例响应：

<img width="1255" height="559" alt="image" src="https://github.com/user-attachments/assets/9af5cbd3-8af8-437a-9d2c-a167ab99db48" />
