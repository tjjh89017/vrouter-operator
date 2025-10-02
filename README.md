# vrouter-operator
Deploy your vRouter(vyos) in your Kubernetes with KubeVirt

# Full Guide with Video here

https://hackmd.io/@TJibejKyT3OH1YR5f1duzg/ryMdLbvigx

# Demo Video

Youtube Link: [https://www.youtube.com/watch?v=pvdPgob3jAE](https://www.youtube.com/watch?v=pvdPgob3jAE)

[![vRouter-Operator-Demo](http://img.youtube.com/vi/pvdPgob3jAE/0.jpg)](https://www.youtube.com/watch?v=pvdPgob3jAE "vRouter-Operator Demo")

# Prepare VyOS VM image

Prepare your VyOS image with the following build flavor. You still can build with `ttyS0` if you need.

> [!NOTE]
> Please install `qemu-guest-agent` without `cloud-init`, mostly, we don't need that.

```yaml
# VyOS image for generic KVM

image_format = "qcow2"
image_opts = "-c"

disk_size = 4

packages = ["qemu-guest-agent"]

# GRUB console settings
[boot_settings]
    console_type = "tty"
    console_num = '0'
```

# Prepare a ServiceAccount in your namespace for KubeVirt VM

The following example creates a ServiceAccount in default namespace.

> [!NOTE]
> Please note, the service account name should be `vrouter-operator-controller-manager`


```
---
apiVersion: v1
kind: ServiceAccount
metadata:
  labels:
  name: vrouter-operator-controller-manager
  namespace: default
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: default-namespace-vrouterconfig-viewer-role-binding
subjects:
- kind: ServiceAccount
  name: vrouter-operator-controller-manager
  namespace: default
roleRef:
  kind: ClusterRole
  name: vrouter-operator-vrouterconfig-viewer-role
  apiGroup: rbac.authorization.k8s.io
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: default-namespace-view
subjects:
- kind: ServiceAccount
  name: vrouter-operator-controller-manager
  namespace: default
roleRef:
  kind: ClusterRole
  name: view
  apiGroup: rbac.authorization.k8s.io
```

# Prepare VRouterConfig

Some example as below

```yaml
apiVersion: vrouter.kojuro.date/v1
kind: VRouterConfig
metadata:
  name: vrouterconfig-sample
spec:
  config: |
    system {
        host-name vyos-k8s-demo
        login {
            user vyos {
                authentication {
                    encrypted-password $6$QxPS.uk6mfo$9QBSo8u1FkH16gMyAVhus6fU3LOzvLR9Z9.82m3tiHFAxTtIkhaZSWssSgzt4v4dGAL8rhVQxTg0oAG9/q11h/
                    plaintext-password ""
                }
            }
        }
        syslog {
            global {
                facility all {
                    level info
                }
                facility protocols {
                    level debug
                }
            }
        }
        ntp {
            allow-client {
                address 127.0.0.0/8
                address 169.254.0.0/16
                address 10.0.0.0/8
                address 172.16.0.0/12
                address 192.168.0.0/16
                address ::1/128
                address fe80::/10
                address fc00::/7
            }
            server "time1.vyos.net"
            server "time2.vyos.net"
            server "time3.vyos.net"
        }
        console {
            device ttyS0 {
                speed 115200
            }
        }
        config-management {
            commit-revisions 100
        }
    }
    
    interfaces {
        loopback lo {
        }
    }
  command: |
    set system host-name 'VyOS-1'
    set interface eth eth0 address dhcp 
```

# Prepare KubeVirt VM for VyOS

Prepare a KubeVirt VM CRD as you need for the VyOS VM. And put annotations like:

```yaml
apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  annotations:
    vrouter.kojuro.date/config: vrouterconfig-sample
  name: vyos
  namespace: default
```
