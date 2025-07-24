# 1.GPU驱动安装

> 如果要安装指定版本的GPU驱动，可以参考该流程



**禁用原有官方驱动**

```bash
lsmod | grep nvidia
# 如果显示若干行（代表已加载的驱动模块），则需要先禁用

# 禁用原有驱动
sudo apt-get purge 'nvidia*'
sudo apt-get autoremove
```



**禁用 nouveau 驱动**（Linux内核自带的驱动程序）

```bash
lsmod | grep nouveau
# 如果有输出，代表 nouveau 驱动已加载，需要先禁用

# 禁用 nouveau 驱动
sudo tee /etc/modprobe.d/disable-nouveau.conf <<EOF
blacklist nouveau
options nouveau modeset=0
EOF

# 更新 initramfs
sudo update-initramfs -u
# 重启系统
sudo reboot
```



**安装驱动**

```bash
# 安装下载好的驱动
chmod +x NVIDIA-Linux-x86_64-550.163.01.run
./NVIDIA-Linux-x86_64-550.163.01.run --silent --no-cc-version-check --disable-nouveau --dkms
```



**验证安装成功**


<img width="819" height="901" alt="image" src="https://github.com/user-attachments/assets/9b445c40-673b-471d-9d94-a37c9f6d2d22" />


---



# 2.NVIDIA Container Toolkit安装



安装命令

```bash
curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey | sudo gpg --dearmor -o /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg \
  && curl -s -L https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list | \
    sed 's#deb https://#deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://#g' | \
    sudo tee /etc/apt/sources.list.d/nvidia-container-toolkit.list
    
sed -i -e '/experimental/ s/^#//g' /etc/apt/sources.list.d/nvidia-container-toolkit.list
apt-get update
export NVIDIA_CONTAINER_TOOLKIT_VERSION=1.17.8-1
  sudo apt-get install -y \
      nvidia-container-toolkit=${NVIDIA_CONTAINER_TOOLKIT_VERSION} \
      nvidia-container-toolkit-base=${NVIDIA_CONTAINER_TOOLKIT_VERSION} \
      libnvidia-container-tools=${NVIDIA_CONTAINER_TOOLKIT_VERSION} \
      libnvidia-container1=${NVIDIA_CONTAINER_TOOLKIT_VERSION}
```



验证安装成功


<img width="1415" height="247" alt="image" src="https://github.com/user-attachments/assets/a1737b9c-bf55-464b-a2fe-45907982b533" />



---



# 3.集群部署

> 因为这次环境是单台机器（ubuntu系统），故准备部署轻量级集群，正巧 microk8s 支持 gpu 设备插件一键部署，就选择 microk8s





```bash
# 安装命令
snap install microk8s --classic --channel=1.29/stable	

# 可以设置一下别名，方便点
snap alias microk8s.kubectl kubectl
```



阻塞参考

> 1 - 如果执行 `microk8s inspect` 命令存在报错：
>
> `cp: cannot stat '/var/snap/microk8s/8006/var/kubernetes/backend/localnode.yaml': No such file or directory`
>
> 手动创建一下该文件即可
>
> ```bash
> mkdir -p /var/snap/microk8s/8006/var/kubernetes/backend
> touch /var/snap/microk8s/8006/var/kubernetes/backend/localnode.yaml
> ```
>
> ---
>
> 2 - 镜像拉取不下来
>
> microk8s ≥ 1.23的版本，每个仓库都需要配置单独的 `host.toml`。分布在 `/var/snap/microk8s/current/args/certs.d/` 下
>
> - 例如 docker 仓库
>
> ```toml
> # vim /var/snap/microk8s/current/args/certs.d/docker.io/hosts.toml
> server = "https://registry-1.docker.io"
> 
> [host."https://docker.xuanyuan.me"]
>   capabilities = ["pull", "resolve"]
> # 随后重启集群
> # snap restart microk8s
> ```



成功示例


<img width="1139" height="69" alt="image" src="https://github.com/user-attachments/assets/0e621adb-4da7-41de-8e8f-30178e84633e" />




# 4.安装GPU设备插件



> [版本选择 ](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/platform-support.html)，要选择和驱动版本适配的 GPU Operator 版本，不然会报错

```bash
# 安装GPU设备插件命令
microk8s enable nvidia --gpu-operator-version=25.3.0 
```



> 此外，如果机器上的GPU支持 **NVSwitch** 互联架构，需要额外安装并启用 `nvidia-fabricmanager`，否则GPU间高速通信无法正常启用。也会影响设备插件的安装
>
> ```bash
> lspci | grep -i nvidia 
> # 出现内容：Bridge: NVIDIA Corporation Device 1af1 
> # 代表支持 NVSwitch
> 
> 
> # 查看一下，原来有没有安装
> sudo systemctl status nvidia-fabricmanager
> 
> # 安装（版本要和驱动适配）
> sudo apt install nvidia-fabricmanager-550
> sudo systemctl enable --now nvidia-fabricmanager
> sudo systemctl status nvidia-fabricmanager
> 
> # 验证安装结果
> nv-fabricmanager -v
> # Fabric Manager version is : 550.163.01
> ```



安装成功示例

<img width="1117" height="357" alt="image" src="https://github.com/user-attachments/assets/b1d1ae1a-34ea-487b-a4ca-f21b6fec3ea5" />




**kubectl describe node**，含有gpu资源信息

<img width="335" height="357" alt="image" src="https://github.com/user-attachments/assets/6836420d-452a-41a6-a32a-dce65be9b6ab" />

