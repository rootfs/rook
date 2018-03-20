# Run OSD in its own Pod

## TL;DR

A one-OSD-per-Pod placement should be implemented to improve reliability and resource efficiency for Ceph OSD daemon.

## Background

Currently in Rook 0.7, Rook Operator starts a ReplicaSet to run [`rook osd`](https://github.com/rook/rook/blob/master/cmd/rook/osd.go) command (hereafter referred to as `OSD Provisioner`)  on each storage node. The ReplicaSet has just one replica. `OSD Provisioner` scans and prepares devices, creates OSD IDs and data directories or devices, generates Ceph configuration. At last, `OSD Provisioner` starts all Ceph OSD, i.e. `ceph-osd`, daemons in foreground and tracks `ceph-osd` processes.

As observed, all Ceph OSDs are running in the same Pod.

## Limitations

The limitations of current design are:

- Reliability issue. One Pod for all OSDs doesn't have the highest reliability nor efficiency. If the Pod is deleted, accidentally or during maintenence, all OSDs are down till the ReplicaSet restart. 
- Efficiency issue. Resource limits cannot be set effectively on the OSDs since the number of osds per in the pod could vary from node to node. The operator cannot make decisions about the topology because it doesn't know in advance what devices are available on the nodes.
- Tight Ceph coupling. The monolithic device discovery and provisioning code cannot be reused for other backends.
- Process management issue. Rook's process management is very simple. Using Kubernetes pod management is much more reliable.


A more comprehensive discussions can be found at [this issue](https://github.com/rook/rook/issues/1341).

## Proposal   

We propose the following change to address the limitations.

### Terms

- Device Discovery. A Pod that is given a device or directory path filter and use the filter to discover devices on the host. The Pod updates the device orchestration Configmap with the device paths and mark the orchestration status as "DeviceDiscovered". The Pod is created by Rook Operator and runs in a Kubernetes deployment on the storage node. This process is storage backend agnostic. 

- Device Provisioner. A Pod that is given device or directory paths upon start and make backend specific storage types. For instance, the provisioner prepares OSDs for Ceph backend. It is a Kubernetes batch job and exits after the devices are prepared.

### Create new OSDs
| Sequence |Rook Operator | Device Discovery  | Device Provisioner   | Ceph OSD Deployment | 
|---|---|---|---|---|
| 1  | Read devices from cluster CRD and create an OSD Discovery deployment on each storage node  |   |   |
| 2  | Watch raw device Configmap  | Discover devices that fit those defined in Cluster CRD and update Configmap with the device paths| | |
| 3  | Detect raw device Configmap change, parse Configmap, extract device paths, and create an Device Provisioner deployment for each device  || | |
| 4  | Watch device provisioning Configmap | | Prepare OSDs, Persist OSD ID, datapath, and node info in a per node Configmap | |
| 5  | Detect device provisioning Configmap change, parse Configmap, extract OSD info, construct OSD Pod command and arg | | | |
| 6  | Create one deployment per OSD | | |
| 7  | | | | Start `ceph-osd` Daemon one Pod per device |

This change addresses the above limitations in the follow ways:
- High reliability. Each `ceph-osd` daemon runs its own Pod, their restart and upgrade are by Kubernetes controllers. Upgrading Device Provisioner Pod no longer restarts `ceph-osd` daemons.
- More efficient resource requests. Once Device Discovery detects all devices, Rook Operator is informed of the topology and assigns appropriate resources to each Ceph OSD deployment.
- Reusable. Device discovery can be used for other storage backends.

### Discussions

It is expected Device Discovery will be merged into Rook Operator once local PVs are supported in Rook Cluster CRD. Rook Operator can infer the device topoloyg from local PV Configmaps. However, as long as raw devices or directories are still in use, a dedicated Device Discovery Pod is still needed.

Alternatively, since `rook agent` is currently running as a DaemonSet on all nodes, it is conceivable to make `rook agent` to poll devices and update device orchestration Configmap. This approach, however, needs to give `rook agent` the privilege to modify Configmaps.

Device filtering can be done at Device Discovery or Operator. Since Device Discovery Pod runs on the storage nodes, it can see all devices and access to device information at great detail. Morever, the device filter can assist Device Discovery Pod to walk into directories or device patterns with specific configurations. 
Thus it is sensible for Device Discovery Pod to filter the devices and pass the net results to Operator. The device filter is stored in a Configmap, it is read by Device Discovery each time devices are to be discovered. The device filter Configmap is updated by Operator when the filter is changed in Cluster CRD.

## Impact

- Security. Device Provisioner Pod needs privilege to access Configmaps but Ceph OSD Pod don't need to access Kubernetes resources and thus don't need any RBAC rules.

- Rook Operator. Rook Operator watches two Configmaps: the raw device Configmaps that created by Device Discovery Pod and storage specific device provisioning Configmaps that are created by Device Provisioner Pod. For raw device Configmap, Operator creates storage specific device provisioner deployment to prepare these devices. For device provisioning Configmaps, Operator creates storage specific daemon deployment (e.g. Ceph OSD Daemon deployments) with the device information in Configmaps and resource information in Cluster CRD.

- Device Discovery. It is a new long running Pod that runs on each storage node. It reads device filters from configmaps, discovers storage devices on the nodes, and populates the raw devices Configmaps.

- Device Provisioner. Device Provisioner becomes a batch job, it no longer exec Ceph OSD daemon. 

- Ceph OSD Daemon. `ceph-osd` is no longer exec'ed by Device Provisioner, it becomes the Pod entrypoint. 

- Ceph OSD Pod naming. Rook Operator creates Ceph OSD Pod metadata using cluster name, node name, and OSD ID.

