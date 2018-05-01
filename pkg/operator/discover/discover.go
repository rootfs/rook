/*
Copyright 2018 The Rook Authors. All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package discover to discover devices on storage nodes.
package discover

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"

	"github.com/coreos/pkg/capnslog"
	rookalpha "github.com/rook/rook/pkg/apis/rook.io/v1alpha1"
	"github.com/rook/rook/pkg/clusterd"
	discoverDaemon "github.com/rook/rook/pkg/daemon/discover"
	"github.com/rook/rook/pkg/operator/k8sutil"
	"github.com/rook/rook/pkg/util/sys"

	"k8s.io/api/core/v1"
	extensions "k8s.io/api/extensions/v1beta1"
	"k8s.io/api/rbac/v1beta1"
	kserrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	discoverDaemonsetName             = "rook-discover"
	discoverDaemonsetTolerationEnv    = "DISCOVER_TOLERATION"
	discoverDaemonsetTolerationKeyEnv = "DISCOVER_TOLERATION_KEY"
	deviceInUseCMName                 = "local-device-in-use-"
	deviceInUseAppName                = "rook-claimed-devices"
)

var logger = capnslog.NewPackageLogger("github.com/rook/rook", "op-discover")

var accessRules = []v1beta1.PolicyRule{
	{
		APIGroups: []string{""},
		Resources: []string{"configmaps"},
		Verbs:     []string{"get", "list", "update", "create", "delete"},
	},
}

// Discover reference to be deployed
type Discover struct {
	clientset kubernetes.Interface
}

// New creates an instance of Discover
func New(clientset kubernetes.Interface) *Discover {
	return &Discover{
		clientset: clientset,
	}
}

// Start the discover
func (d *Discover) Start(namespace, discoverImage string) error {

	err := k8sutil.MakeRole(d.clientset, namespace, discoverDaemonsetName, accessRules, nil)
	if err != nil {
		return fmt.Errorf("failed to init RBAC for rook-discover. %+v", err)
	}

	err = d.createDiscoverDaemonSet(namespace, discoverImage)
	if err != nil {
		return fmt.Errorf("Error starting discover daemonset: %v", err)
	}
	return nil
}

func (d *Discover) createDiscoverDaemonSet(namespace, discoverImage string) error {
	privileged := false
	ds := &extensions.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: discoverDaemonsetName,
		},
		Spec: extensions.DaemonSetSpec{
			UpdateStrategy: extensions.DaemonSetUpdateStrategy{
				Type: extensions.RollingUpdateDaemonSetStrategyType,
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": discoverDaemonsetName,
					},
				},
				Spec: v1.PodSpec{
					ServiceAccountName: discoverDaemonsetName,
					Containers: []v1.Container{
						{
							Name:  discoverDaemonsetName,
							Image: discoverImage,
							Args:  []string{"discover"},
							SecurityContext: &v1.SecurityContext{
								Privileged: &privileged,
							},
							VolumeMounts: []v1.VolumeMount{
								{
									Name:      "dev",
									MountPath: "/dev",
									ReadOnly:  true,
								},
								{
									Name:      "sys",
									MountPath: "/sys",
									ReadOnly:  true,
								},
								{
									Name:      "udev",
									MountPath: "/run/udev",
									ReadOnly:  true,
								},
							},
							Env: []v1.EnvVar{
								k8sutil.NamespaceEnvVar(),
								k8sutil.NodeEnvVar(),
							},
						},
					},
					Volumes: []v1.Volume{
						{
							Name: "dev",
							VolumeSource: v1.VolumeSource{
								HostPath: &v1.HostPathVolumeSource{
									Path: "/dev",
								},
							},
						},
						{
							Name: "sys",
							VolumeSource: v1.VolumeSource{
								HostPath: &v1.HostPathVolumeSource{
									Path: "/sys",
								},
							},
						},
						{
							Name: "udev",
							VolumeSource: v1.VolumeSource{
								HostPath: &v1.HostPathVolumeSource{
									Path: "/run/udev",
								},
							},
						},
					},
					HostNetwork: false,
				},
			},
		},
	}

	// Add toleration if any
	tolerationValue := os.Getenv(discoverDaemonsetTolerationEnv)
	if tolerationValue != "" {
		ds.Spec.Template.Spec.Tolerations = []v1.Toleration{
			{
				Effect:   v1.TaintEffect(tolerationValue),
				Operator: v1.TolerationOpExists,
				Key:      os.Getenv(discoverDaemonsetTolerationKeyEnv),
			},
		}
	}

	_, err := d.clientset.Extensions().DaemonSets(namespace).Create(ds)
	if err != nil {
		if !kserrors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create rook-discover daemon set. %+v", err)
		}
		logger.Infof("rook-discover daemonset already exists")
	} else {
		logger.Infof("rook-discover daemonset started")
	}
	return nil

}

func ListDevices(context *clusterd.Context, namespace, nodeName string) (map[string][]sys.LocalDisk, error) {
	var devices map[string][]sys.LocalDisk
	listOpts := metav1.ListOptions{LabelSelector: fmt.Sprintf("%s=%s", k8sutil.AppAttr, discoverDaemon.AppName)}
	cms, err := context.Clientset.CoreV1().ConfigMaps(namespace).List(listOpts)
	if err != nil {
		return devices, fmt.Errorf("failed to list device configmaps: %+v", err)
	}
	devices = make(map[string][]sys.LocalDisk, len(cms.Items))
	for _, cm := range cms.Items {
		node := cm.ObjectMeta.Labels[discoverDaemon.NodeAttr]
		if len(nodeName) > 0 && node != nodeName {
			continue
		}
		deviceJson := cm.Data[discoverDaemon.LocalDiskCMData]
		logger.Debugf("node %s, device %s", node, deviceJson)

		if len(node) == 0 || len(deviceJson) == 0 {
			continue
		}
		var d []sys.LocalDisk
		err = json.Unmarshal([]byte(deviceJson), &d)
		if err != nil {
			logger.Warningf("failed to unmarshal %s", deviceJson)
			continue
		}
		devices[node] = d
	}
	logger.Debugf("devices %+v", devices)
	return devices, nil
}

func ListDevicesInUse(context *clusterd.Context, namespace, nodeName string) ([]sys.LocalDisk, *v1.ConfigMap, error) {
	var devices []sys.LocalDisk

	if len(nodeName) == 0 {
		return devices, nil, fmt.Errorf("empty node name")
	}

	listOpts := metav1.ListOptions{LabelSelector: fmt.Sprintf("%s=%s", k8sutil.AppAttr, deviceInUseAppName)}
	cms, err := context.Clientset.CoreV1().ConfigMaps(namespace).List(listOpts)
	if err != nil {
		return devices, nil, fmt.Errorf("failed to list device in use configmaps: %+v", err)
	}

	for _, cm := range cms.Items {
		node := cm.ObjectMeta.Labels[discoverDaemon.NodeAttr]
		if node != nodeName {
			continue
		}
		deviceJson := cm.Data[discoverDaemon.LocalDiskCMData]
		logger.Debugf("node %s, device in use %s", node, deviceJson)

		if len(node) == 0 || len(deviceJson) == 0 {
			continue
		}

		err = json.Unmarshal([]byte(deviceJson), &devices)
		if err != nil {
			logger.Warningf("failed to unmarshal %s", deviceJson)
			continue
		}
		logger.Debugf("devices in use %+v", devices)
		return devices, &cm, nil
	}
	// when reaching here, the device-in-use cm doesn't exist, create one
	cm := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deviceInUseCMName + nodeName,
			Namespace: namespace,
			Labels: map[string]string{
				k8sutil.AppAttr:         deviceInUseAppName,
				discoverDaemon.NodeAttr: nodeName,
			},
		},
		Data: make(map[string]string, 1),
	}
	cm, err = context.Clientset.CoreV1().ConfigMaps(namespace).Create(cm)
	return devices, cm, err
}

func FreeDevices(context *clusterd.Context, namespace, nodeName string, devicesToFree []rookalpha.Device) error {
	if len(nodeName) == 0 || len(devicesToFree) == 0 {
		return nil
	}

	listOpts := metav1.ListOptions{LabelSelector: fmt.Sprintf("%s=%s", k8sutil.AppAttr, deviceInUseAppName)}
	cms, err := context.Clientset.CoreV1().ConfigMaps(namespace).List(listOpts)
	if err != nil {
		return fmt.Errorf("failed to list device in use configmaps: %+v", err)
	}

	for _, cm := range cms.Items {
		node := cm.ObjectMeta.Labels[discoverDaemon.NodeAttr]
		if node != nodeName {
			continue
		}
		deviceJson := cm.Data[discoverDaemon.LocalDiskCMData]
		logger.Debugf("node %s, device in use %s", node, deviceJson)

		if len(node) == 0 || len(deviceJson) == 0 {
			continue
		}
		devicesInUse := []sys.LocalDisk{}
		err = json.Unmarshal([]byte(deviceJson), &devicesInUse)
		if err != nil {
			logger.Warningf("failed to unmarshal %s", deviceJson)
			continue
		}
		newDevicesInUse := []sys.LocalDisk{}
		for i := range devicesInUse {
			stillInUse := true
			for j := range devicesToFree {
				if devicesInUse[i].Name == devicesToFree[j].Name {
					stillInUse = false
					break
				}
			}
			if stillInUse {
				newDevicesInUse = append(newDevicesInUse, devicesInUse[i])
			}
		}
		logger.Infof("new devices in use %+v", newDevicesInUse)
		// update configmap
		newDeviceJson, err := json.Marshal(newDevicesInUse)
		if err != nil {
			logger.Infof("failed to marshal: %v", err)
			return err
		}
		data := make(map[string]string, 1)
		data[discoverDaemon.LocalDiskCMData] = string(newDeviceJson)
		cm.Data = data
		_, err = context.Clientset.CoreV1().ConfigMaps(namespace).Update(&cm)
		if err != nil {
			logger.Warningf("failed to update device in use on node %s: %v", nodeName, err)
		}
		return err
	}
	return nil
}

func GetAvailableDevices(context *clusterd.Context, nodeName, clusterName string, devices []rookalpha.Device, filter string, useAllDevices bool) ([]rookalpha.Device, error) {
	results := []rookalpha.Device{}
	if len(devices) == 0 && len(filter) == 0 && !useAllDevices {
		return results, nil
	}
	namespace := os.Getenv(k8sutil.PodNamespaceEnvVar)
	// find all devices
	allDevices, err := ListDevices(context, namespace, nodeName)
	if err != nil {
		return results, err
	}
	// find those on the node
	nodeAllDevices, ok := allDevices[nodeName]
	if !ok {
		return results, fmt.Errorf("node %s has no devices", nodeName)
	}
	// find those in use on the node
	devicesInUse, cm, err := ListDevicesInUse(context, namespace, nodeName)
	if err != nil {
		return results, err
	}
	// filter those in use
	nodeDevices := []sys.LocalDisk{}
	for i := range nodeAllDevices {
		isInUse := false
		for j := range devicesInUse {
			if nodeAllDevices[i].Name == devicesInUse[j].Name {
				isInUse = true
				break
			}
		}
		if !isInUse {
			nodeDevices = append(nodeDevices, nodeAllDevices[i])
		}
	}

	// now those left are free to use
	if len(devices) > 0 {
		for i := range devices {
			for j := range nodeDevices {
				if devices[i].Name == nodeDevices[j].Name {
					results = append(results, devices[i])
					devicesInUse = append(devicesInUse, nodeDevices[j])
				}
			}
		}
	} else if len(filter) >= 0 {
		for i := range nodeDevices {
			//TODO support filter based on other keys
			matched, err := regexp.Match(filter, []byte(nodeDevices[i].Name))
			if err == nil && matched {
				d := rookalpha.Device{
					Name: nodeDevices[i].Name,
				}
				devicesInUse = append(devicesInUse, nodeDevices[i])
				results = append(results, d)
			}
		}
	} else if useAllDevices {
		for i := range nodeDevices {
			d := rookalpha.Device{
				Name: nodeDevices[i].Name,
			}
			results = append(results, d)
			devicesInUse = append(devicesInUse, nodeDevices[i])
		}
	}
	// mark these devices in use
	if len(results) > 0 {
		deviceJson, err := json.Marshal(devicesInUse)
		if err != nil {
			logger.Infof("failed to marshal: %v", err)
			return results, err
		}
		data := make(map[string]string, 1)
		data[discoverDaemon.LocalDiskCMData] = string(deviceJson)
		cm.Data = data
		_, err = context.Clientset.CoreV1().ConfigMaps(namespace).Update(cm)
		if err != nil {
			logger.Warningf("failed to update device in use on node %s: %v", nodeName, err)
		}
		return results, err
	}
	return results, nil
}
