# Run OSD in its own Pod

## TL;DR

A one-OSD-per-Pod placement should be implemented to improve reliability and resource efficiency for Ceph OSD daemon.

## Background

Currently in Rook 0.7, Rook Operator starts a ReplicaSet for Rook OSD agent on each storage node. The ReplicaSet has just one replica. Rook OSD agents scan and prepare devices, create OSD IDs and data directories, generate Ceph configuration. At last, OSD agents start all Ceph OSD daemons in foreground and track the OSD processes.

As observed, all Ceph OSDs are running in the same Pod.

## Limitations

One Pod for all OSDs doesn't have the highest reliability nor efficiency. If the Pod is deleted, accidentally or during maintenence, all OSDs are down till the ReplicaSet restart. If nodes have asymmetric configurations, some have more devices than others, the Rook Operator doesn't request appropriate resources for the OSD Pods, since the resources are requested before the OSDs are discovered.

A more comprehensive discussions can be found at [this issue](https://github.com/rook/rook/issues/1341).

## Proposal   

Based on offline discussion with [Travis](https://github.com/travisn), we propose the following OSD startup change.

### Create new OSDs
| Sequence |Rook Operator  | Rook OSD Agent  | Ceph OSD  | Note  |
|---|---|---|---|---|
| 1  | Create a Pod for OSD Agents for each storage node  |   |   | Consider a CronJob for peridically invocation to detect device change  |
| 2  |   |  Discover and prepare OSDs  |   | |
| 3  |   |  Persist OSD ID, datapath, and node info in a per node Configmap | | |
| 4  | Watch Configmap, parse Configmap, extract OSD info, construct OSD Pod command and arg | | |
| 5  | Create one deployment per OSD | | |
| 6  | | | Start OSD Daemon | Running `ceph-osd` in a dedicated Pod eliminates the rules and caps assigned to Rook OSD Agent |

### OSD Addition/Deletion

Rook OSD agent updates Configmap to reflect current OSD devices on the node.
Rook Operator creates/deletes OSD deployments based on Configmap change. 

### OSD Pod Naming

Rook Operator creates OSD Pod names using cluster name, node name, and OSD ID.

### Impact

#### Rook Operator

No privilege change, all the RBAC rules assigned to Rook Operator still works, unless we decide to choose to run OSD Agent in a CronJob.

Rook Operator will watch Configmaps created by OSD Agents and reconcile Ceph OSD Daemon deployments with the latest information in Configmaps.

#### Rook OSD Agent

There is no privilege change, Rook osd agent still uses the same RBAC rules.

However, the agent no longer exec Ceph OSD daemon. It either continues watching and polling the devices or exits immediately after creating the OSD Configmap.

#### Ceph OSD Daemon

`ceph-osd` is no longer exec'ed by Rook OSD agent, it becomes the Pod entrypoint and does not need any RBAC rules assigned to Rook OSD Agent. 